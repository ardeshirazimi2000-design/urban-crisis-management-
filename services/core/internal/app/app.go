// Package app wires the modular monolith (ADR-001): one deployable with clear module boundaries,
// separately runnable workers (outbox relay, notifier, event consumer).
package app

import (
	"context"
	"fmt"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/admin"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/alert"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/auth"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/gis"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/incident"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/media"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/medical"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/notification"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/outbox"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/platform/config"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/platform/httpx"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/relief"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/report"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/resource"
)

type App struct {
	Cfg      config.Config
	Pool     *pgxpool.Pool
	Verifier auth.Verifier
	Resolver *auth.Resolver
	Store    media.Store
	Handler  http.Handler
	Routes   []string
	draining atomic.Bool
}

// New builds the HTTP handler. verifier and store are injected so tests can substitute them.
func New(cfg config.Config, pool *pgxpool.Pool, verifier auth.Verifier, store media.Store) *App {
	a := &App{Cfg: cfg, Pool: pool, Verifier: verifier, Store: store}
	a.Resolver = &auth.Resolver{Pool: pool, TTL: cfg.PrincipalTTL, BootstrapAdmins: cfg.BootstrapAdmins}
	guard := &auth.Guard{Pool: pool}
	area := gis.AreaFromConfig(cfg)

	mux := http.NewServeMux()
	defaultLimiter := httpx.NewRateLimiter(cfg.DefaultRatePerMin, cfg.DefaultBurst)
	// Limits are keyed by principal, not IP: during a disaster many citizens share carrier-grade NAT addresses.
	authMW := func(next http.Handler) http.Handler {
		limited := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !defaultLimiter.Allow("user:" + auth.FromContext(r.Context()).UserID.String()) {
				httpx.WriteError(w, r, httpx.ErrRateLimited)
				return
			}
			next.ServeHTTP(w, r)
		})
		return auth.Authenticate(verifier, a.Resolver)(limited)
	}
	router := &httpx.Router{Mux: mux, Prefix: "/api/v1", AuthMW: authMW}

	reportLimiter := httpx.NewRateLimiter(cfg.ReportRatePerMin, cfg.ReportBurst)
	(&report.Module{Pool: pool, Guard: guard, Area: area, Limiter: reportLimiter}).Routes(router)
	(&media.Module{Pool: pool, Guard: guard, Store: store, Scanner: media.NoopScanner{}, MaxBytes: cfg.MediaMaxBytes,
		AllowUnscanned: cfg.MediaAllowUnscanned, Limiter: reportLimiter}).Routes(router)
	(&incident.Module{Pool: pool, Guard: guard, Area: area}).Routes(router)
	(&resource.Module{Pool: pool, Guard: guard, Area: area, StaleAfter: cfg.ResourceStaleAfter}).Routes(router)
	(&relief.Module{Pool: pool, Guard: guard, Area: area}).Routes(router)
	(&medical.Module{Pool: pool, Guard: guard, Area: area}).Routes(router)
	(&alert.Module{Pool: pool, Guard: guard, RequireDistinctApprover: cfg.AlertRequireDistinctApprover,
		MaxValidity: cfg.AlertMaxValidity}).Routes(router)
	(&gis.Module{Pool: pool, Guard: guard, Area: area, StaleAfter: cfg.GISStaleAfter, ResourceStaleAfter: cfg.ResourceStaleAfter}).Routes(router)
	(&admin.Module{Pool: pool, Guard: guard, Resolver: a.Resolver, ReplayLimiter: httpx.NewRateLimiter(5, 3)}).Routes(router)
	if cfg.IsLocal() && cfg.AuthMode == "dev" {
		dev := &admin.DevTokens{Pool: pool, Secret: []byte(cfg.DevJWTSecret), Audience: cfg.OIDCAudience, Resolver: a.Resolver,
			StaffCode: cfg.DevAccessCodeStaff, CitizenCode: cfg.DevAccessCodeCitizen}
		if dev.StaffCode != "" {
			dev.Limiter = httpx.NewRateLimiter(10, 10) // per IP: bounds access-code guessing
		}
		dev.Routes(router)
	}

	// Liveness: process is up. Readiness: can serve traffic (DB reachable, not draining).
	// Kafka/AI/notification providers are deliberately NOT readiness dependencies (no restart storms).
	mux.HandleFunc("GET /health/live", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /health/ready", func(w http.ResponseWriter, r *http.Request) {
		if a.draining.Load() {
			httpx.JSON(w, http.StatusServiceUnavailable, map[string]string{"status": "draining"})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()
		if err := pool.Ping(ctx); err != nil {
			httpx.JSON(w, http.StatusServiceUnavailable, map[string]string{"status": "database_unavailable"})
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]string{"status": "ready"})
	})
	mux.HandleFunc("GET /metrics", httpx.Metrics.Handler())
	a.Routes = append(router.Routes, "GET /health/live", "GET /health/ready")
	a.registerGauges()

	a.Handler = httpx.Chain(mux, httpx.Recover, httpx.Correlation, httpx.AccessLog, httpx.SecurityHeaders, httpx.CORS(cfg.CORSOrigins))
	return a
}

// Drain flips readiness to failing so load balancers stop routing before shutdown.
func (a *App) Drain() { a.draining.Store(true) }

func (a *App) registerGauges() {
	pool := a.Pool
	httpx.Metrics.RegisterGauge("crisis_outbox_events", "Outbox events by state", func(ctx context.Context) (map[string]float64, error) {
		var pending, dead float64
		err := pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE published_at IS NULL AND dead_lettered_at IS NULL),
			count(*) FILTER (WHERE dead_lettered_at IS NOT NULL) FROM outbox_events`).Scan(&pending, &dead)
		return map[string]float64{`state="pending"`: pending, `state="dead"`: dead}, err
	})
	httpx.Metrics.RegisterGauge("crisis_outbox_oldest_pending_seconds", "Age of the oldest unpublished outbox event", func(ctx context.Context) (map[string]float64, error) {
		var age *float64
		err := pool.QueryRow(ctx, `SELECT EXTRACT(EPOCH FROM now() - min(created_at)) FROM outbox_events
			WHERE published_at IS NULL AND dead_lettered_at IS NULL`).Scan(&age)
		v := 0.0
		if age != nil {
			v = *age
		}
		return map[string]float64{"": v}, err
	})
	httpx.Metrics.RegisterGauge("crisis_reports_open", "Reports awaiting a review decision", func(ctx context.Context) (map[string]float64, error) {
		rows, err := pool.Query(ctx, `SELECT status, count(*) FROM reports WHERE status IN ('received','triage','under_review') GROUP BY status`)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := map[string]float64{}
		for rows.Next() {
			var s string
			var n float64
			if err := rows.Scan(&s, &n); err != nil {
				return nil, err
			}
			out[fmt.Sprintf("status=%q", s)] = n
		}
		return out, rows.Err()
	})
	httpx.Metrics.RegisterGauge("crisis_notification_attempts", "Notification attempts by channel and status", func(ctx context.Context) (map[string]float64, error) {
		rows, err := pool.Query(ctx, `SELECT channel, status, count(*) FROM notification_attempts GROUP BY channel, status`)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := map[string]float64{}
		for rows.Next() {
			var c, s string
			var n float64
			if err := rows.Scan(&c, &s, &n); err != nil {
				return nil, err
			}
			out[fmt.Sprintf("channel=%q,status=%q", c, s)] = n
		}
		return out, rows.Err()
	})
}

// NewPublisher returns the configured event publisher.
func NewPublisher(cfg config.Config) outbox.Publisher {
	if cfg.Publisher == "kafka" {
		return outbox.NewKafkaPublisher(cfg.KafkaBrokers)
	}
	return outbox.LogPublisher{}
}

// NewAdapters returns the notification channel adapters. All public channels are sandboxed in the MVP.
func NewAdapters() map[string]notification.Adapter {
	return map[string]notification.Adapter{
		"internal":       notification.InternalAdapter{},
		"push":           notification.NewSandbox("push", 0.05, 0.05),
		"sms":            notification.NewSandbox("sms", 0.10, 0.05),
		"cell_broadcast": notification.CellBroadcastAdapter{},
	}
}

// NewStore builds the configured object store.
func NewStore(ctx context.Context, cfg config.Config) (media.Store, error) {
	if cfg.ObjectStore == "s3" {
		return media.NewS3Store(ctx, cfg.S3Endpoint, cfg.S3AccessKey, cfg.S3SecretKey, cfg.S3Bucket, cfg.S3UseSSL)
	}
	return &media.FSStore{Dir: cfg.ObjectStoreDir, Secret: []byte(cfg.MediaURLSecret), BaseURL: cfg.PublicBaseURL + "/api/v1/media/blob"}, nil
}

// NewVerifier builds the token verifier for the configured auth mode.
func NewVerifier(ctx context.Context, cfg config.Config) (auth.Verifier, error) {
	if cfg.AuthMode == "dev" {
		return auth.DevVerifier{Secret: []byte(cfg.DevJWTSecret), Audience: cfg.OIDCAudience}, nil
	}
	return auth.NewOIDCVerifier(ctx, cfg.OIDCIssuerURL, cfg.OIDCAudience, cfg.OIDCJWKSURL)
}
