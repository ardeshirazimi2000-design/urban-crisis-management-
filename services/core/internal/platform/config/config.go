// Package config loads runtime configuration from environment variables.
// Secrets are read from the environment (injected by a secret manager in production) and never logged.
package config

import (
	"fmt"
	"os"
	"sort"
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

	// Auth: "local" (built-in identity: Shahkar + SMS OTP, AFTA profile), "dev" (HS256 shared secret, local only)
	// or "oidc" (JWKS from issuer).
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

	// Built-in identity (AUTH_MODE=local) and AFTA security functions.
	JWTSecret           string // signs access tokens of local sessions
	OTPPepper           string // keys OTP code hashes
	AuditKey            string // HMAC key of the audit chain (FCS_COP.1)
	IntegrityKey        string // HMAC key of record integrity seals (FDP_SDI.2)
	AuditFallback       string // local append-only file used when the audit store is unavailable (FRU_FLT.1)
	TimeZone            string // console access windows
	AccessTokenTTL      time.Duration
	ConsoleSessionTTL   time.Duration
	AppSessionTTL       time.Duration
	OTPLength           int
	OTPResendAfter      time.Duration
	OTPMaxPerHour       int
	PasswordMinLen      int
	PasswordMaxAge      int // days, 0 = unlimited
	LockoutThreshold    int
	LockoutMinutes      int
	ShahkarReverifyDays int
	AppCitizenSignup    bool

	APIIRMode        string // live | mock
	APIIRBaseURL     string
	APIIRToken       string
	APIIRShahkar     string // Shahkar | ShahkarPro | ShahkarLite
	APIIROTPTemplate int
	APIIRTimeout     time.Duration

	// Trusted path (FTP_TRP.1): refuse plain HTTP unless the request came over TLS (directly or via the proxy).
	RequireHTTPS bool
	ProxyHops    int // reverse proxies in front of the API that append X-Forwarded-For
	TLSCertFile  string
	TLSKeyFile   string
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

		JWTSecret:           env("JWT_SECRET", "dev-only-jwt-secret-change-me-0123456789"),
		OTPPepper:           env("OTP_PEPPER", "dev-only-otp-pepper-change-me-0123456789"),
		AuditKey:            env("AUDIT_KEY", "dev-only-audit-key-change-me-0123456789"),
		IntegrityKey:        env("INTEGRITY_KEY", "dev-only-integrity-key-change-me-012345"),
		AuditFallback:       env("AUDIT_FALLBACK_FILE", ""),
		TimeZone:            env("TIME_ZONE", "Asia/Tehran"),
		AccessTokenTTL:      envDuration("ACCESS_TOKEN_TTL", 15*time.Minute),
		ConsoleSessionTTL:   envDuration("CONSOLE_SESSION_TTL", 8*time.Hour),
		AppSessionTTL:       envDuration("APP_SESSION_TTL", 30*24*time.Hour),
		OTPLength:           envInt("OTP_LENGTH", 6),
		OTPResendAfter:      envDuration("OTP_RESEND_AFTER", 60*time.Second),
		OTPMaxPerHour:       envInt("OTP_MAX_PER_HOUR", 5),
		PasswordMinLen:      envInt("PASSWORD_MIN_LENGTH", 12),
		PasswordMaxAge:      envInt("PASSWORD_MAX_AGE_DAYS", 90),
		LockoutThreshold:    envInt("LOGIN_MAX_FAILURES", 5),
		LockoutMinutes:      envInt("LOCKOUT_MINUTES", 15),
		ShahkarReverifyDays: envInt("SHAHKAR_REVERIFY_DAYS", 180),
		AppCitizenSignup:    envBool("APP_CITIZEN_SIGNUP", true),

		APIIRMode:        env("APIIR_MODE", "mock"),
		APIIRBaseURL:     strings.TrimRight(env("APIIR_BASE_URL", "https://s.api.ir"), "/"),
		APIIRToken:       cleanToken(env("APIIR_TOKEN", "")),
		APIIRShahkar:     env("APIIR_SHAHKAR_ENDPOINT", "ShahkarPro"),
		APIIROTPTemplate: envInt("APIIR_OTP_TEMPLATE", 1),
		APIIRTimeout:     envDuration("APIIR_TIMEOUT", 10*time.Second),

		RequireHTTPS: envBool("REQUIRE_HTTPS", false),
		ProxyHops:    envInt("TRUSTED_PROXY_HOPS", 0),
		TLSCertFile:  env("TLS_CERT_FILE", ""),
		TLSKeyFile:   env("TLS_KEY_FILE", ""),
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
	case "local":
		if !c.IsLocal() {
			var weak []string
			for name, v := range map[string]string{"JWT_SECRET": c.JWTSecret, "OTP_PEPPER": c.OTPPepper, "AUDIT_KEY": c.AuditKey, "INTEGRITY_KEY": c.IntegrityKey} {
				if strings.HasPrefix(v, "dev-only") {
					weak = append(weak, name)
				}
			}
			if len(weak) > 0 {
				sort.Strings(weak)
				return fmt.Errorf("set real secrets for %s (openssl rand -base64 48)", strings.Join(weak, ", "))
			}
			if c.APIIRMode != "live" || c.APIIRToken == "" {
				return fmt.Errorf("AUTH_MODE=local outside local/test needs APIIR_MODE=live and APIIR_TOKEN")
			}
			if !c.RequireHTTPS {
				return fmt.Errorf("REQUIRE_HTTPS=true is required outside local/test (trusted path)")
			}
		}
	case "oidc":
		if c.OIDCIssuerURL == "" {
			return fmt.Errorf("OIDC_ISSUER_URL is required when AUTH_MODE=oidc")
		}
	default:
		return fmt.Errorf("unknown AUTH_MODE %q", c.AuthMode)
	}
	for name, v := range map[string]string{"JWT_SECRET": c.JWTSecret, "OTP_PEPPER": c.OTPPepper, "AUDIT_KEY": c.AuditKey, "INTEGRITY_KEY": c.IntegrityKey} {
		if len(v) < 32 {
			return fmt.Errorf("%s must be at least 32 characters", name)
		}
	}
	if !c.IsLocal() && (strings.HasPrefix(c.AuditKey, "dev-only") || strings.HasPrefix(c.IntegrityKey, "dev-only")) {
		return fmt.Errorf("set real AUDIT_KEY and INTEGRITY_KEY outside local/test")
	}
	switch c.APIIRMode {
	case "live", "mock":
	default:
		return fmt.Errorf("APIIR_MODE must be live or mock")
	}
	switch c.APIIRShahkar {
	case "Shahkar", "ShahkarPro", "ShahkarLite":
	default:
		return fmt.Errorf("APIIR_SHAHKAR_ENDPOINT must be Shahkar, ShahkarPro or ShahkarLite")
	}
	if c.OTPLength < 4 || c.OTPLength > 8 {
		return fmt.Errorf("OTP_LENGTH must be 4..8")
	}
	if _, err := time.LoadLocation(c.TimeZone); err != nil {
		return fmt.Errorf("TIME_ZONE: %w", err)
	}
	if (c.TLSCertFile == "") != (c.TLSKeyFile == "") {
		return fmt.Errorf("TLS_CERT_FILE and TLS_KEY_FILE must be set together")
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

// cleanToken accepts an api.ir key pasted with quotes or its "Bearer " prefix.
func cleanToken(t string) string {
	t = strings.Trim(strings.TrimSpace(t), `"'`)
	if len(t) > 7 && strings.EqualFold(t[:7], "bearer ") {
		t = t[7:]
	}
	return strings.TrimSpace(t)
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
