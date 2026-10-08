package admin

import (
	"net/http"
	"regexp"
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
		Subject string     `json:"subject"`
		Name    string     `json:"name"`
		Grants  []devGrant `json:"grants"`
	}
	if err := httpx.DecodeJSON(w, r, &req, 8<<10); err != nil {
		return err
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
			Outcome: "success", Reason: "local development only", Details: map[string]any{"grants": len(req.Grants)},
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
