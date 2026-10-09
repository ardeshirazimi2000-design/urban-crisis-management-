// Package config loads runtime configuration from environment variables.
// Secrets are read from the environment (injected by a secret manager in production) and never logged.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	AppEnv   string // local | staging | production
	HTTPAddr string
	LogLevel string

	DatabaseURL string
	AutoMigrate bool

	// Auth: "dev" (HS256 shared secret, local only) or "oidc" (JWKS from issuer).
	AuthMode     string
	DevJWTSecret string
	// Optional access codes for the dev sign-in (a test server reachable from the Internet). When the staff code
	// is set, /dev/token requires a code: the staff code may request any role, the citizen code only CITIZEN.
	DevAccessCodeStaff   string
	DevAccessCodeCitizen string
	OIDCIssuerURL        string
	OIDCAudience         string
	OIDCJWKSURL          string
	PrincipalTTL         time.Duration
	BootstrapAdmins      []string // OIDC subjects that receive SECURITY_ADMIN on first sight

	KafkaBrokers []string
	TopicPrefix  string
	Publisher    string // kafka | log

	ObjectStore         string // fs | s3
	ObjectStoreDir      string
	S3Endpoint          string
	S3AccessKey         string
	S3SecretKey         string
	S3Bucket            string
	S3UseSSL            bool
	MediaURLSecret      string
	MediaMaxBytes       int64
	MediaAllowUnscanned bool
	PublicBaseURL       string

	// Service area bounding box (WGS84). Reports outside are rejected (AT-10).
	AreaMinLat, AreaMaxLat, AreaMinLng, AreaMaxLng float64

	ReportRatePerMin  float64
	ReportBurst       int
	DefaultRatePerMin float64
	DefaultBurst      int

	AlertRequireDistinctApprover bool
	AlertMaxValidity             time.Duration

	ResourceStaleAfter time.Duration
	GISStaleAfter      time.Duration

	CORSOrigins []string
}

func Load() (Config, error) {
	c := Config{
		AppEnv:      env("APP_ENV", "local"),
		HTTPAddr:    env("HTTP_ADDR", ":8080"),
		LogLevel:    env("LOG_LEVEL", "info"),
		DatabaseURL: env("DATABASE_URL", "postgres://crisis:local-only@localhost:5432/crisis?sslmode=disable"),
		AutoMigrate: envBool("AUTO_MIGRATE", false),

		AuthMode:             env("AUTH_MODE", "dev"),
		DevJWTSecret:         env("DEV_JWT_SECRET", ""),
		DevAccessCodeStaff:   env("DEV_ACCESS_CODE_STAFF", ""),
		DevAccessCodeCitizen: env("DEV_ACCESS_CODE_CITIZEN", ""),
		OIDCIssuerURL:        env("OIDC_ISSUER_URL", ""),
		OIDCAudience:         env("OIDC_AUDIENCE", "crisis-api"),
		OIDCJWKSURL:          env("OIDC_JWKS_URL", ""),
		PrincipalTTL:         envDuration("PRINCIPAL_CACHE_TTL", 30*time.Second),
		BootstrapAdmins:      envList("BOOTSTRAP_SECURITY_ADMIN_SUBJECTS"),

		KafkaBrokers: envList("KAFKA_BROKERS"),
		TopicPrefix:  env("KAFKA_TOPIC_PREFIX", "crisis."),
		Publisher:    env("EVENT_PUBLISHER", "log"),

		ObjectStore:         env("OBJECT_STORE", "fs"),
		ObjectStoreDir:      env("OBJECT_STORE_DIR", "./data/objects"),
		S3Endpoint:          env("OBJECT_STORAGE_ENDPOINT", ""),
		S3AccessKey:         env("OBJECT_STORAGE_ACCESS_KEY", ""),
		S3SecretKey:         env("OBJECT_STORAGE_SECRET_KEY", ""),
		S3Bucket:            env("OBJECT_STORAGE_BUCKET", "crisis-media"),
		S3UseSSL:            envBool("OBJECT_STORAGE_USE_SSL", false),
		MediaURLSecret:      env("MEDIA_URL_SECRET", ""),
		MediaMaxBytes:       int64(envInt("MEDIA_MAX_BYTES", 10<<20)),
		MediaAllowUnscanned: envBool("MEDIA_ALLOW_UNSCANNED", false),
		PublicBaseURL:       env("PUBLIC_BASE_URL", "http://localhost:8080"),

		// Default: Tehran province, generous margin.
		AreaMinLat: envFloat("SERVICE_AREA_MIN_LAT", 34.8),
		AreaMaxLat: envFloat("SERVICE_AREA_MAX_LAT", 36.3),
		AreaMinLng: envFloat("SERVICE_AREA_MIN_LNG", 50.3),
		AreaMaxLng: envFloat("SERVICE_AREA_MAX_LNG", 53.2),

		ReportRatePerMin:  envFloat("RATE_REPORT_PER_MIN", 6),
		ReportBurst:       envInt("RATE_REPORT_BURST", 5),
		DefaultRatePerMin: envFloat("RATE_DEFAULT_PER_MIN", 300),
		DefaultBurst:      envInt("RATE_DEFAULT_BURST", 60),

		AlertRequireDistinctApprover: envBool("ALERT_REQUIRE_DISTINCT_APPROVER", true),
		AlertMaxValidity:             envDuration("ALERT_MAX_VALIDITY", 72*time.Hour),

		ResourceStaleAfter: envDuration("RESOURCE_STALE_AFTER", 10*time.Minute),
		GISStaleAfter:      envDuration("GIS_STALE_AFTER", 24*time.Hour),

		CORSOrigins: envList("CORS_ORIGINS"),
	}
	// "/" means relative media URLs, so the console works whichever address (LAN or public) it was opened on.
	c.PublicBaseURL = strings.TrimRight(c.PublicBaseURL, "/")
	return c, c.validate()
}

func (c Config) IsLocal() bool { return c.AppEnv == "local" || c.AppEnv == "test" }

func (c Config) validate() error {
	switch c.AuthMode {
	case "dev":
		if !c.IsLocal() {
			return fmt.Errorf("AUTH_MODE=dev is only allowed when APP_ENV is local or test")
		}
		if len(c.DevJWTSecret) < 32 {
			return fmt.Errorf("DEV_JWT_SECRET must be at least 32 characters")
		}
		if c.DevAccessCodeCitizen != "" && c.DevAccessCodeStaff == "" {
			return fmt.Errorf("DEV_ACCESS_CODE_CITIZEN requires DEV_ACCESS_CODE_STAFF")
		}
		for _, code := range []string{c.DevAccessCodeStaff, c.DevAccessCodeCitizen} {
			if code != "" && len(code) < 8 {
				return fmt.Errorf("dev access codes must be at least 8 characters")
			}
		}
		if c.DevAccessCodeStaff != "" && c.DevAccessCodeStaff == c.DevAccessCodeCitizen {
			return fmt.Errorf("DEV_ACCESS_CODE_STAFF and DEV_ACCESS_CODE_CITIZEN must differ")
		}
	case "oidc":
		if c.OIDCIssuerURL == "" {
			return fmt.Errorf("OIDC_ISSUER_URL is required when AUTH_MODE=oidc")
		}
	default:
		return fmt.Errorf("unknown AUTH_MODE %q", c.AuthMode)
	}
	if len(c.MediaURLSecret) < 32 {
		return fmt.Errorf("MEDIA_URL_SECRET must be at least 32 characters")
	}
	if c.MediaAllowUnscanned && !c.IsLocal() {
		return fmt.Errorf("MEDIA_ALLOW_UNSCANNED is only allowed in local/test")
	}
	if c.Publisher == "kafka" && len(c.KafkaBrokers) == 0 {
		return fmt.Errorf("KAFKA_BROKERS is required when EVENT_PUBLISHER=kafka")
	}
	return nil
}

func env(k, def string) string {
	if v, ok := os.LookupEnv(k); ok && v != "" {
		return v
	}
	return def
}

func envBool(k string, def bool) bool {
	v, err := strconv.ParseBool(env(k, strconv.FormatBool(def)))
	if err != nil {
		return def
	}
	return v
}

func envInt(k string, def int) int {
	v, err := strconv.Atoi(env(k, strconv.Itoa(def)))
	if err != nil {
		return def
	}
	return v
}

func envFloat(k string, def float64) float64 {
	v, err := strconv.ParseFloat(env(k, ""), 64)
	if err != nil {
		return def
	}
	return v
}

func envDuration(k string, def time.Duration) time.Duration {
	v, err := time.ParseDuration(env(k, ""))
	if err != nil {
		return def
	}
	return v
}

func envList(k string) []string {
	var out []string
	for _, p := range strings.Split(env(k, ""), ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
