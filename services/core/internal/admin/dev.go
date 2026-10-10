package admin

import (
	"crypto/subtle"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/audit"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/auth"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/platform/db"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/platform/httpx"
)

// DevTokens is registered ONLY when APP_ENV is local/test and AUTH_MODE=dev (enforced in cmd/api and config).
// It replaces an OIDC provider for local demos: it creates the user, sets its grants and mints a short-lived token.
type DevTokens struct {
	Pool     *pgxpool.Pool
	Secret   []byte
	Audience string
	Resolver *auth.Resolver

	// Access codes for a test server reachable from outside the office. Empty StaffCode = no code required.
	StaffCode   string
	CitizenCode string
	Limiter     *httpx.RateLimiter // per client IP; bounds access-code guessing
}

var errAccessCode = httpx.NewError(http.StatusUnauthorized, "ACCESS_CODE_INVALID", "کد دسترسی آزمایشی نادرست است")

// normalizeCode makes typing on phones forgiving: Persian/Arabic digits, upper case (auto-capitalisation),
// spaces and invisible characters (ZWNJ, RTL marks) are mapped/removed. Codes are lower-case hex.
func normalizeCode(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= '۰' && r <= '۹':
			b.WriteRune('0' + (r - '۰'))
		case r >= '٠' && r <= '٩':
			b.WriteRune('0' + (r - '٠'))
		case (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || r == '-' || r == '_':
			b.WriteRune(r)
		}
	}
	return b.String()
}

func codeEq(a, b string) bool {
	return b != "" && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

var subjectRe = regexp.MustCompile(`^[a-z0-9._-]{3,64}$`)

type devGrant struct {
	Role    string `json:"role"`
	OrgCode string `json:"org_code"` // empty = all organizations
}

func (d *DevTokens) Routes(r *httpx.Router) {
	r.Public("POST /dev/token", d.token)
}

func (d *DevTokens) token(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	var req struct {
		Subject    string     `json:"subject"`
		Name       string     `json:"name"`
		Grants     []devGrant `json:"grants"`
		AccessCode string     `json:"access_code"`
	}
	if d.Limiter != nil && !d.Limiter.Allow("dev-token:"+httpx.ClientIP(r)) {
		return httpx.ErrRateLimited
	}
	if err := httpx.DecodeJSON(w, r, &req, 8<<10); err != nil {
		return err
	}
	citizenOnly := false
	if d.StaffCode != "" {
		code := normalizeCode(req.AccessCode)
		switch {
		case codeEq(code, normalizeCode(d.StaffCode)):
		case codeEq(code, normalizeCode(d.CitizenCode)):
			citizenOnly = true
		default:
			slog.WarnContext(ctx, "dev_token_access_code_rejected", "ip", httpx.ClientIP(r))
			return errAccessCode
		}
	}
	if citizenOnly {
		// The citizen code (built into the test APK) may only create citizen accounts, never touch staff ones.
		ok := strings.HasPrefix(req.Subject, "citizen-")
		for _, g := range req.Grants {
			ok = ok && g.Role == string(auth.RoleCitizen)
		}
		if !ok {
			return httpx.ErrForbidden
		}
	}
	var v httpx.Validator
	v.Check(subjectRe.MatchString(req.Subject), "subject", "invalid")
	for _, g := range req.Grants {
		v.Check(auth.IsKnownRole(g.Role), "grants.role", "unknown:"+g.Role)
	}
	if err := v.Err(); err != nil {
		return err
	}
	subject := "dev:" + req.Subject
	err := db.WithTx(ctx, d.Pool, func(tx pgx.Tx) error {
		var uid uuid.UUID
		if err := tx.QueryRow(ctx, `INSERT INTO users (subject_id, display_name) VALUES ($1,$2)
			ON CONFLICT (subject_id) DO UPDATE SET display_name=EXCLUDED.display_name RETURNING id`, subject, req.Name).Scan(&uid); err != nil {
			return err
		}
		if citizenOnly {
			var staffRoles int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM user_roles WHERE user_id=$1 AND role_code<>$2`, uid, string(auth.RoleCitizen)).Scan(&staffRoles); err != nil {
				return err
			}
			if staffRoles > 0 {
				return httpx.ErrForbidden
			}
		}
		if _, err := tx.Exec(ctx, `DELETE FROM user_roles WHERE user_id=$1`, uid); err != nil {
			return err
		}
		for _, g := range req.Grants {
			var org *uuid.UUID
			if g.OrgCode != "" {
				var id uuid.UUID
				if err := tx.QueryRow(ctx, `SELECT id FROM organizations WHERE code=$1`, g.OrgCode).Scan(&id); err != nil {
					return httpx.Validation(httpx.FieldDetail{Field: "grants.org_code", Reason: "unknown:" + g.OrgCode})
				}
				org = &id
			}
			if _, err := tx.Exec(ctx, `INSERT INTO user_roles (user_id, role_code, scope_org_id, reason) VALUES ($1,$2,$3,'dev token')
				ON CONFLICT DO NOTHING`, uid, g.Role, org); err != nil {
				return err
			}
		}
		return audit.Write(ctx, tx, audit.Entry{ActorID: &uid, Action: "dev.token_issued", TargetType: "user", TargetID: uid.String(),
			Outcome: "success", Reason: "local development only", Details: map[string]any{"grants": len(req.Grants), "citizen_code": citizenOnly},
			CorrelationID: httpx.CorrelationID(ctx)})
	})
	if err != nil {
		return err
	}
	d.Resolver.Invalidate(subject)
	tok, err := auth.IssueDevToken(d.Secret, d.Audience, subject, req.Name, 8*time.Hour)
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"access_token": tok, "token_type": "Bearer", "expires_in": int((8 * time.Hour).Seconds())})
	return nil
}
