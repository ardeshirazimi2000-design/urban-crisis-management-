// Package logx configures structured JSON logging with redaction of sensitive attributes.
package logx

import (
	"log/slog"
	"os"
	"regexp"
	"strings"
)

// Attribute keys that must never be logged verbatim.
var sensitiveKeys = map[string]bool{
	"authorization": true, "token": true, "access_token": true, "refresh_token": true, "password": true,
	"secret": true, "phone": true, "phone_number": true, "national_id": true, "lat": true, "lng": true,
	"location": true, "description": true, "email": true,
}

var (
	bearerRe = regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9._~+/=-]+`)
	phoneRe  = regexp.MustCompile(`(\+98|0)?9\d{9}`)
)

// Redact masks secrets and personal data that may appear inside free-form strings.
func Redact(s string) string {
	s = bearerRe.ReplaceAllString(s, "Bearer [REDACTED]")
	return phoneRe.ReplaceAllString(s, "[PHONE]")
}

func Setup(level string) *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	h := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: lvl,
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			if sensitiveKeys[strings.ToLower(a.Key)] {
				return slog.String(a.Key, "[REDACTED]")
			}
			if a.Value.Kind() == slog.KindString {
				return slog.String(a.Key, Redact(a.Value.String()))
			}
			return a
		},
	})
	l := slog.New(h)
	slog.SetDefault(l)
	return l
}
