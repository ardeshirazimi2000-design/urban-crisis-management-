package identity

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/audit"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/auth"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/platform/db"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/security"
)

type BootstrapAdmin struct {
	NationalCode string
	Mobile       string
	Username     string
	FirstName    string
	LastName     string
}

// Bootstrap creates the first security administrator after a Shahkar match and returns a one-time
// temporary password. There are no default accounts or fixed passwords.
func (s *Service) Bootstrap(ctx context.Context, b BootstrapAdmin) (string, error) {
	nc := security.NormalizeDigits(b.NationalCode)
	mobile := security.NormalizeMobile(b.Mobile)
	username := strings.ToLower(strings.TrimSpace(b.Username))
	switch {
	case !security.ValidNationalCode(nc):
		return "", fmt.Errorf("BOOTSTRAP_ADMIN_NATIONAL_CODE is not a valid national code")
	case mobile == "":
		return "", fmt.Errorf("BOOTSTRAP_ADMIN_MOBILE is not a valid mobile number")
	case !usernameRe.MatchString(username):
		return "", fmt.Errorf("BOOTSTRAP_ADMIN_USERNAME must start with a letter (4-32 of a-z 0-9 . _ -)")
	case strings.TrimSpace(b.FirstName) == "" || strings.TrimSpace(b.LastName) == "":
		return "", fmt.Errorf("BOOTSTRAP_ADMIN_FIRST_NAME and BOOTSTRAP_ADMIN_LAST_NAME are required")
	}
	var admins int
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM user_roles ur JOIN users u ON u.id=ur.user_id
		WHERE ur.role_code='SECURITY_ADMIN' AND u.kind='staff'`).Scan(&admins); err != nil {
		return "", err
	}
	if admins > 0 {
		return "", fmt.Errorf("a security administrator already exists; create further users from the console")
	}
	r, err := s.Shahkar(ctx, nc, mobile, "bootstrap_admin", nil, nil)
	if err != nil {
		return "", err
	}
	if !r.OK {
		return "", fmt.Errorf("Shahkar did not answer (%s); check APIIR_TOKEN and APIIR_SHAHKAR_ENDPOINT", r.Message)
	}
	if !r.Matched {
		return "", fmt.Errorf("Shahkar: mobile %s is not registered to national code %s", security.MaskMobile(mobile), maskNC(nc))
	}
	temp := security.TemporaryPassword()
	id := uuid.New()
	err = db.WithTx(ctx, s.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO users (id, subject_id, display_name, kind, national_code, mobile, first_name, last_name,
			username, console_access, shahkar_verified_at) VALUES ($1,$2,$3,'staff',$4,$5,$6,$7,$8,true,now())`,
			id, "local:"+id.String(), b.FirstName+" "+b.LastName, nc, mobile, b.FirstName, b.LastName, username); err != nil {
			return err
		}
		if err := security.SetPassword(ctx, tx, id, temp, true, 0); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO user_roles (user_id, role_code, reason) VALUES ($1,'SECURITY_ADMIN','bootstrap')`, id); err != nil {
			return err
		}
		return audit.Write(ctx, tx, audit.Entry{ActorID: &id, ActorRoles: []string{string(auth.RoleSecurityAdmin)}, Action: "user.bootstrap_admin",
			TargetType: "user", TargetID: id.String(), Outcome: "success", Channel: "cli"})
	})
	if err != nil {
		return "", err
	}
	return temp, nil
}

// Maintenance removes expired authentication data (FDP_RIP.2) and refreshes the audit capacity state.
func (s *Service) Maintenance(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		s.purge(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *Service) purge(ctx context.Context) {
	for _, q := range []string{
		`DELETE FROM auth_challenges WHERE created_at < now() - interval '1 day'`,
		`DELETE FROM sessions WHERE (revoked_at IS NOT NULL AND revoked_at < now() - interval '90 days') OR expires_at < now() - interval '90 days'`,
		`UPDATE sessions SET refresh_hash='-', prev_refresh_hash=NULL WHERE (revoked_at IS NOT NULL OR expires_at < now()) AND refresh_hash<>'-'`,
	} {
		if _, err := s.Pool.Exec(ctx, q); err != nil && ctx.Err() == nil {
			slog.WarnContext(ctx, "identity maintenance", "err", err.Error())
		}
	}
}
