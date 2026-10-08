package auth

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/audit"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/platform/httpx"
)

type ctxKey int

const principalKey ctxKey = 1

func WithPrincipal(ctx context.Context, p *Principal) context.Context {
	return context.WithValue(ctx, principalKey, p)
}

func FromContext(ctx context.Context) *Principal {
	p, _ := ctx.Value(principalKey).(*Principal)
	return p
}

// Resolver maps verified token claims to a Principal with grants loaded from the database.
// Results are cached briefly; role changes therefore propagate within PrincipalTTL.
type Resolver struct {
	Pool            *pgxpool.Pool
	TTL             time.Duration
	BootstrapAdmins []string

	mu    sync.Mutex
	cache map[string]cached
}

type cached struct {
	p   *Principal
	exp time.Time
}

func (r *Resolver) Invalidate(subject string) {
	r.mu.Lock()
	delete(r.cache, subject)
	r.mu.Unlock()
}

func (r *Resolver) InvalidateAll() {
	r.mu.Lock()
	r.cache = nil
	r.mu.Unlock()
}

func (r *Resolver) Resolve(ctx context.Context, c Claims) (*Principal, error) {
	r.mu.Lock()
	if e, ok := r.cache[c.Subject]; ok && time.Now().Before(e.exp) {
		r.mu.Unlock()
		return e.p, nil
	}
	r.mu.Unlock()

	p := &Principal{Subject: c.Subject, DisplayName: c.Name}
	var status string
	err := r.Pool.QueryRow(ctx, `INSERT INTO users (subject_id, display_name) VALUES ($1, $2)
		ON CONFLICT (subject_id) DO UPDATE SET display_name = CASE WHEN EXCLUDED.display_name <> '' THEN EXCLUDED.display_name ELSE users.display_name END
		RETURNING id, display_name, status`, c.Subject, c.Name).Scan(&p.UserID, &p.DisplayName, &status)
	if err != nil {
		return nil, err
	}
	if status != "active" {
		return nil, ErrSuspended
	}
	if slices.Contains(r.BootstrapAdmins, c.Subject) {
		if err := r.bootstrapAdmin(ctx, p.UserID); err != nil {
			return nil, err
		}
	}
	grants, err := LoadGrants(ctx, r.Pool, p.UserID)
	if err != nil {
		return nil, err
	}
	if len(grants) == 0 {
		// Every authenticated identity without explicit grants is a citizen.
		grants = []Grant{{Role: RoleCitizen}}
	}
	p.Grants = grants

	r.mu.Lock()
	if r.cache == nil {
		r.cache = map[string]cached{}
	}
	r.cache[c.Subject] = cached{p: p, exp: time.Now().Add(r.TTL)}
	r.mu.Unlock()
	return p, nil
}

var ErrSuspended = errors.New("user suspended")

func (r *Resolver) bootstrapAdmin(ctx context.Context, userID uuid.UUID) error {
	tx, err := r.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	tag, err := tx.Exec(ctx, `INSERT INTO user_roles (user_id, role_code, scope_org_id, reason)
		VALUES ($1, 'SECURITY_ADMIN', NULL, 'bootstrap') ON CONFLICT DO NOTHING`, userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 1 {
		if err := audit.Write(ctx, tx, audit.Entry{ActorID: &userID, Action: "role.bootstrap_grant", TargetType: "user",
			TargetID: userID.String(), Outcome: "success", Reason: "BOOTSTRAP_SECURITY_ADMIN_SUBJECTS",
			Details: map[string]any{"role": "SECURITY_ADMIN"}, CorrelationID: httpx.CorrelationID(ctx)}); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func LoadGrants(ctx context.Context, q interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, userID uuid.UUID) ([]Grant, error) {
	rows, err := q.Query(ctx, `SELECT role_code, scope_org_id FROM user_roles
		WHERE user_id = $1 AND (expires_at IS NULL OR expires_at > now()) ORDER BY role_code`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Grant
	for rows.Next() {
		var g Grant
		if err := rows.Scan(&g.Role, &g.ScopeOrg); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// Authenticate verifies the bearer token and attaches the Principal. Requests without a token are rejected
// unless the route is public (health, metrics, media download with signed URL).
func Authenticate(v Verifier, res *Resolver) httpx.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := r.Header.Get("Authorization")
			if !strings.HasPrefix(h, "Bearer ") {
				httpx.WriteError(w, r, httpx.ErrUnauthenticated)
				return
			}
			claims, err := v.Verify(r.Context(), strings.TrimPrefix(h, "Bearer "))
			if err != nil {
				httpx.WriteError(w, r, httpx.ErrUnauthenticated)
				return
			}
			p, err := res.Resolve(r.Context(), claims)
			if errors.Is(err, ErrSuspended) {
				httpx.WriteError(w, r, httpx.ErrForbidden)
				return
			}
			if err != nil {
				httpx.WriteError(w, r, err)
				return
			}
			next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), p)))
		})
	}
}

// Guard centralises authorization decisions so every denial is audited (AT-05, AT-07).
type Guard struct {
	Pool *pgxpool.Pool
}

// Deny records a denied/security event and returns the error to send to the client.
func (g *Guard) Deny(ctx context.Context, action, targetType, targetID, reason string, clientErr *httpx.Error) error {
	p := FromContext(ctx)
	e := audit.Entry{Action: action, TargetType: targetType, TargetID: targetID, Outcome: "denied", Reason: reason,
		CorrelationID: httpx.CorrelationID(ctx)}
	if p != nil {
		e.ActorID = &p.UserID
		e.ActorRoles = p.RoleNames()
	}
	// Use a detached context: the denial must be recorded even if the client disconnects.
	actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	if err := audit.WriteStandalone(actx, g.Pool, e); err != nil {
		slog.ErrorContext(ctx, "audit denied event failed", "err", err.Error(), "action", action)
	}
	if clientErr == nil {
		clientErr = httpx.ErrForbidden
	}
	return clientErr
}

// Require returns a FORBIDDEN error (audited) unless the caller holds perm in any scope.
func (g *Guard) Require(ctx context.Context, perm Permission, targetType, targetID string) error {
	p := FromContext(ctx)
	if p == nil {
		return httpx.ErrUnauthenticated
	}
	if !p.Has(perm) {
		return g.Deny(ctx, string(perm), targetType, targetID, "missing_permission", nil)
	}
	return nil
}

// RequireOrg returns FORBIDDEN (audited) unless perm is held for org. Use NOT_FOUND-style hiding where appropriate.
func (g *Guard) RequireOrg(ctx context.Context, perm Permission, org uuid.UUID, targetType, targetID string) error {
	p := FromContext(ctx)
	if p == nil {
		return httpx.ErrUnauthenticated
	}
	if !p.CanInOrg(perm, org) {
		return g.Deny(ctx, string(perm), targetType, targetID, "out_of_org_scope", nil)
	}
	return nil
}
