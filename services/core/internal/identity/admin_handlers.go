package identity

import (
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/audit"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/auth"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/platform/db"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/platform/httpx"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/security"
)

var usernameRe = regexp.MustCompile(`^[a-z][a-z0-9._-]{3,31}$`)

func (m *Module) adminRoutes(r *httpx.Router) {
	r.Auth("POST /admin/users", m.createUser)
	r.Auth("GET /admin/users/{id}", m.getUser)
	r.Auth("PATCH /admin/users/{id}", m.updateUser)
	r.Auth("PUT /admin/users/{id}/role", m.setRole)
	r.Auth("POST /admin/users/{id}/reset-password", m.resetPassword)
	r.Auth("POST /admin/users/{id}/unlock", m.unlock)
	r.Auth("POST /admin/users/{id}/shahkar", m.reverify)
	r.Auth("GET /admin/users/{id}/sessions", m.userSessions)
	r.Auth("DELETE /admin/users/{id}/sessions", m.revokeAll)
	r.Auth("DELETE /admin/users/{id}/sessions/{sid}", m.revokeOne)
	r.Auth("POST /admin/shahkar/inquiry", m.inquiry)
	r.Auth("GET /admin/shahkar/inquiries", m.inquiries)
	r.Auth("GET /admin/settings", m.getSettings)
	r.Auth("PUT /admin/settings/{key}", m.putSetting)
	r.Auth("GET /admin/notifications", m.notifications)
}

func (m *Module) actor(r *http.Request) *auth.Principal { return auth.FromContext(r.Context()) }

func (m *Module) write(r *http.Request, tx pgx.Tx, action, targetType, targetID, reason string, details map[string]any) error {
	p := m.actor(r)
	return audit.Write(r.Context(), tx, audit.Entry{ActorID: &p.UserID, ActorRoles: p.RoleNames(), Action: action, TargetType: targetType,
		TargetID: targetID, Outcome: "success", Reason: reason, Details: details})
}

type roleReq struct {
	Role       string     `json:"role"`
	ScopeOrgID *uuid.UUID `json:"scope_org_id"`
	ExpiresAt  *time.Time `json:"expires_at"`
}

// validRole checks a staff role assignment. Citizens are not assigned through the console.
func validRole(v *httpx.Validator, rr *roleReq) {
	v.Check(auth.IsKnownRole(rr.Role) && rr.Role != string(auth.RoleCitizen), "role.role", "unknown")
	v.Check(rr.ExpiresAt == nil || rr.ExpiresAt.After(time.Now()), "role.expires_at", "in_past")
}

// assignRole replaces the user's role: exactly one (permanent or time-boxed) role per user.
func assignRole(r *http.Request, tx pgx.Tx, userID uuid.UUID, rr roleReq, reason string) error {
	ctx := r.Context()
	p := auth.FromContext(ctx)
	if rr.Role != string(auth.RoleSecurityAdmin) {
		if err := assertAnotherAdmin(r, tx, userID); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM user_roles WHERE user_id=$1`, userID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO user_roles (user_id, role_code, scope_org_id, granted_by, expires_at, reason) VALUES ($1,$2,$3,$4,$5,$6)`,
		userID, rr.Role, rr.ScopeOrgID, p.UserID, rr.ExpiresAt, reason); err != nil {
		return err
	}
	return RevokeUserSessions(ctx, tx, userID, "role_changed", "")
}

// assertAnotherAdmin keeps at least one active security administrator besides userID.
func assertAnotherAdmin(r *http.Request, tx pgx.Tx, userID uuid.UUID) error {
	var isAdmin bool
	if err := tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM user_roles WHERE user_id=$1 AND role_code='SECURITY_ADMIN')`, userID).Scan(&isAdmin); err != nil {
		return err
	}
	if !isAdmin {
		return nil
	}
	var others int
	if err := tx.QueryRow(r.Context(), `SELECT count(*) FROM user_roles ur JOIN users u ON u.id=ur.user_id
		WHERE ur.role_code='SECURITY_ADMIN' AND ur.user_id<>$1 AND u.status='active' AND (ur.expires_at IS NULL OR ur.expires_at > now())`,
		userID).Scan(&others); err != nil {
		return err
	}
	if others == 0 {
		return httpx.Conflict("LAST_SECURITY_ADMIN", "حداقل یک مدیر امنیت فعال دیگر باید وجود داشته باشد")
	}
	return nil
}

func (m *Module) createUser(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	if err := m.Guard.Require(ctx, auth.UserManage, "user", ""); err != nil {
		return err
	}
	p := m.actor(r)
	var req struct {
		NationalCode  string     `json:"national_code"`
		Mobile        string     `json:"mobile"`
		FirstName     string     `json:"first_name"`
		LastName      string     `json:"last_name"`
		PositionTitle string     `json:"position_title"`
		Username      string     `json:"username"`
		ConsoleAccess bool       `json:"console_access"`
		AppAccess     bool       `json:"app_access"`
		AllowedCIDRs  []string   `json:"allowed_cidrs"`
		Role          roleReq    `json:"role"`
		Reason        string     `json:"reason"`
		OrgID         *uuid.UUID `json:"organization_id"`
	}
	if err := httpx.DecodeJSON(w, r, &req, 8<<10); err != nil {
		return err
	}
	nc := security.NormalizeDigits(req.NationalCode)
	mobile := security.NormalizeMobile(req.Mobile)
	req.Username = strings.ToLower(strings.TrimSpace(req.Username))
	req.FirstName, req.LastName = strings.TrimSpace(req.FirstName), strings.TrimSpace(req.LastName)
	var v httpx.Validator
	v.Check(security.ValidNationalCode(nc), "national_code", "invalid")
	v.Check(mobile != "", "mobile", "invalid")
	v.Check(req.FirstName != "" && len(req.FirstName) <= 100, "first_name", "required")
	v.Check(req.LastName != "" && len(req.LastName) <= 100, "last_name", "required")
	v.Check(req.ConsoleAccess || req.AppAccess, "console_access", "console_or_app_required")
	v.Check(!req.ConsoleAccess || usernameRe.MatchString(req.Username), "username", "required_for_console")
	v.Check(req.Username == "" || usernameRe.MatchString(req.Username), "username", "invalid")
	for _, c := range req.AllowedCIDRs {
		v.Check(security.ValidCIDR(c), "allowed_cidrs", "invalid")
	}
	validRole(&v, &req.Role)
	if req.AppAccess && !req.ConsoleAccess {
		v.Check(slices.Contains(auth.AppRoles, auth.Role(req.Role.Role)), "role.role", "app_only_user_needs_app_role")
	}
	if err := v.Err(); err != nil {
		return err
	}
	var exists bool
	if err := m.Svc.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE national_code=$1)
		OR ($2 <> '' AND EXISTS(SELECT 1 FROM users WHERE username=$2))`, nc, req.Username).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return httpx.Conflict("DUPLICATE_USER", "کاربری با این کد ملی یا نام کاربری وجود دارد")
	}
	// Identity proofing: the mobile used as second factor must belong to the person (FIA_UAU, Shahkar).
	if err := m.Svc.RequireShahkar(ctx, nc, mobile, "console_create_user", nil, &p.UserID); err != nil {
		return err
	}
	id := uuid.New()
	temp := ""
	err := db.WithTx(ctx, m.Svc.Pool, func(tx pgx.Tx) error {
		var username *string
		if req.Username != "" {
			username = &req.Username
		}
		cidrs := req.AllowedCIDRs
		if cidrs == nil {
			cidrs = []string{}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO users (id, subject_id, display_name, kind, national_code, mobile, first_name, last_name,
			position_title, username, console_access, app_access, allowed_cidrs, shahkar_verified_at, created_by)
			VALUES ($1,$2,$3,'staff',$4,$5,$6,$7,$8,$9,$10,$11,$12,now(),$13)`, id, "local:"+id.String(), req.FirstName+" "+req.LastName,
			nc, mobile, req.FirstName, req.LastName, strings.TrimSpace(req.PositionTitle), username, req.ConsoleAccess, req.AppAccess, cidrs, p.UserID); err != nil {
			if db.IsUniqueViolation(err, "") {
				return httpx.Conflict("DUPLICATE_USER", "کاربری با این کد ملی یا نام کاربری وجود دارد")
			}
			return err
		}
		if req.ConsoleAccess {
			temp = security.TemporaryPassword()
			if err := security.SetPassword(ctx, tx, id, temp, true, 0); err != nil {
				return err
			}
		}
		if err := assignRole(r, tx, id, req.Role, "تعریف کاربر"); err != nil {
			return err
		}
		return m.write(r, tx, "user.create", "user", id.String(), req.Reason, map[string]any{"role": req.Role.Role,
			"scope_org_id": req.Role.ScopeOrgID, "console": req.ConsoleAccess, "app": req.AppAccess, "shahkar": true})
	})
	if err != nil {
		return err
	}
	u, err := m.loadUser(r, id)
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusCreated, map[string]any{"user": u, "temporary_password": temp})
	return nil
}

func (m *Module) loadUser(r *http.Request, id uuid.UUID) (map[string]any, error) {
	ctx := r.Context()
	var (
		subject, display, kind, status, first, last, position string
		nc, mobile, username, lastIP                          *string
		console, app, mustChange, locked                      bool
		shahkarAt, enrolled, lastLogin, lockedUntil, pwAt     *time.Time
		failedSince                                           int
		cidrs                                                 []string
		created                                               time.Time
	)
	err := m.Svc.Pool.QueryRow(ctx, `SELECT subject_id, display_name, kind, status, first_name, last_name, position_title, national_code,
		mobile, username, last_login_ip, console_access, app_access, must_change_password, COALESCE(locked_until > now(), false),
		shahkar_verified_at, app_enrolled_at, last_login_at, locked_until, password_changed_at, failed_since_success, allowed_cidrs, created_at
		FROM users WHERE id=$1`, id).Scan(&subject, &display, &kind, &status, &first, &last, &position, &nc, &mobile, &username, &lastIP,
		&console, &app, &mustChange, &locked, &shahkarAt, &enrolled, &lastLogin, &lockedUntil, &pwAt, &failedSince, &cidrs, &created)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, httpx.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	gs := []map[string]any{}
	rows, err := m.Svc.Pool.Query(ctx, `SELECT id, role_code, scope_org_id, expires_at, reason FROM user_roles WHERE user_id=$1`, id)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var gid uuid.UUID
		var role, reason string
		var org *uuid.UUID
		var exp *time.Time
		if err := rows.Scan(&gid, &role, &org, &exp, &reason); err != nil {
			rows.Close()
			return nil, err
		}
		gs = append(gs, map[string]any{"grant_id": gid, "role": role, "scope_org_id": org, "expires_at": exp, "reason": reason})
	}
	rows.Close()
	methods := []string{}
	if console {
		methods = append(methods, "console: password + sms-otp")
	}
	if app || kind == "citizen" {
		methods = append(methods, "app: shahkar + sms-otp (first sign-in / new device), device + sms-otp (later)")
	}
	return map[string]any{"id": id, "subject": subject, "display_name": display, "kind": kind, "status": status, "first_name": first,
		"last_name": last, "position_title": position, "national_code": nc, "mobile": mobile, "username": username,
		"console_access": console, "app_access": app, "must_change_password": mustChange, "locked": locked, "locked_until": lockedUntil,
		"shahkar_verified_at": shahkarAt, "app_enrolled_at": enrolled, "last_login_at": lastLogin, "last_login_ip": lastIP,
		"password_changed_at": pwAt, "failed_since_success": failedSince, "allowed_cidrs": cidrs, "created_at": created,
		"grants": gs, "auth_methods": methods}, nil
}

func (m *Module) targetID(r *http.Request, perm auth.Permission) (uuid.UUID, error) {
	if err := m.Guard.Require(r.Context(), perm, "user", r.PathValue("id")); err != nil {
		return uuid.Nil, err
	}
	return httpx.PathUUID(r, "id")
}

func (m *Module) getUser(w http.ResponseWriter, r *http.Request) error {
	id, err := m.targetID(r, auth.UserManage)
	if err != nil {
		return err
	}
	u, err := m.loadUser(r, id)
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, u)
	return nil
}

func (m *Module) updateUser(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	id, err := m.targetID(r, auth.UserManage)
	if err != nil {
		return err
	}
	p := m.actor(r)
	var req struct {
		Mobile        *string   `json:"mobile"`
		PositionTitle *string   `json:"position_title"`
		Username      *string   `json:"username"`
		ConsoleAccess *bool     `json:"console_access"`
		AppAccess     *bool     `json:"app_access"`
		AllowedCIDRs  *[]string `json:"allowed_cidrs"`
		Reason        string    `json:"reason"`
	}
	if err := httpx.DecodeJSON(w, r, &req, 8<<10); err != nil {
		return err
	}
	var nc, curMobile, curUsername *string
	var kind string
	var console bool
	if err := m.Svc.Pool.QueryRow(ctx, `SELECT national_code, mobile, username, kind, console_access FROM users WHERE id=$1`, id).
		Scan(&nc, &curMobile, &curUsername, &kind, &console); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.ErrNotFound
		}
		return err
	}
	var v httpx.Validator
	newMobile := ""
	if req.Mobile != nil {
		newMobile = security.NormalizeMobile(*req.Mobile)
		v.Check(newMobile != "", "mobile", "invalid")
	}
	if req.Username != nil {
		*req.Username = strings.ToLower(strings.TrimSpace(*req.Username))
		v.Check(usernameRe.MatchString(*req.Username), "username", "invalid")
	}
	if req.AllowedCIDRs != nil {
		for _, c := range *req.AllowedCIDRs {
			v.Check(security.ValidCIDR(c), "allowed_cidrs", "invalid")
		}
	}
	wantConsole := console
	if req.ConsoleAccess != nil {
		wantConsole = *req.ConsoleAccess
		v.Check(kind == "staff" || !wantConsole, "console_access", "citizens_have_no_console")
	}
	username := curUsername
	if req.Username != nil {
		username = req.Username
	}
	v.Check(!wantConsole || username != nil, "username", "required_for_console")
	v.Check(id != p.UserID || req.ConsoleAccess == nil, "console_access", "cannot_modify_self")
	if err := v.Err(); err != nil {
		return err
	}
	mobileChanged := newMobile != "" && (curMobile == nil || *curMobile != newMobile)
	if mobileChanged {
		if nc == nil {
			return httpx.Validation(httpx.FieldDetail{Field: "national_code", Reason: "missing"})
		}
		if err := m.Svc.RequireShahkar(ctx, *nc, newMobile, "console_update_mobile", &id, &p.UserID); err != nil {
			return err
		}
	}
	temp := ""
	err = db.WithTx(ctx, m.Svc.Pool, func(tx pgx.Tx) error {
		if req.Username != nil && (curUsername == nil || *curUsername != *req.Username) {
			var taken bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE username=$1 AND id<>$2)`, *req.Username, id).Scan(&taken); err != nil {
				return err
			}
			if taken {
				return httpx.Conflict("DUPLICATE_USERNAME", "نام کاربری تکراری است")
			}
		}
		var hasPassword bool
		if err := tx.QueryRow(ctx, `UPDATE users SET mobile=COALESCE(NULLIF($2,''), mobile), position_title=COALESCE($3, position_title),
			username=COALESCE($4, username), console_access=COALESCE($5, console_access), app_access=COALESCE($6, app_access),
			allowed_cidrs=COALESCE($7, allowed_cidrs), shahkar_verified_at=CASE WHEN $8 THEN now() ELSE shahkar_verified_at END,
			app_enrolled_at=CASE WHEN $8 THEN NULL ELSE app_enrolled_at END, updated_at=now() WHERE id=$1 RETURNING password_hash IS NOT NULL`,
			id, newMobile, req.PositionTitle, req.Username, req.ConsoleAccess, req.AppAccess, req.AllowedCIDRs, mobileChanged).Scan(&hasPassword); err != nil {
			return err
		}
		if mobileChanged {
			// The old number may no longer belong to the person: devices and sessions end.
			if _, err := tx.Exec(ctx, `DELETE FROM user_devices WHERE user_id=$1`, id); err != nil {
				return err
			}
			if err := RevokeUserSessions(ctx, tx, id, "mobile_changed", ""); err != nil {
				return err
			}
		}
		if req.ConsoleAccess != nil && !*req.ConsoleAccess {
			if err := RevokeUserSessions(ctx, tx, id, "console_access_removed", "console"); err != nil {
				return err
			}
		}
		if wantConsole && !console && !hasPassword {
			temp = security.TemporaryPassword()
			if err := security.SetPassword(ctx, tx, id, temp, true, 0); err != nil {
				return err
			}
		}
		return m.write(r, tx, "user.update", "user", id.String(), req.Reason, map[string]any{"mobile_changed": mobileChanged,
			"console": req.ConsoleAccess, "app": req.AppAccess, "username": req.Username != nil, "allowed_cidrs": req.AllowedCIDRs})
	})
	if err != nil {
		return err
	}
	m.Svc.Resolver.InvalidateAll()
	u, err := m.loadUser(r, id)
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"user": u, "temporary_password": temp})
	return nil
}

func (m *Module) setRole(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	id, err := m.targetID(r, auth.RoleManage)
	if err != nil {
		return err
	}
	var req struct {
		Role   roleReq `json:"role"`
		Reason string  `json:"reason"`
	}
	if err := httpx.DecodeJSON(w, r, &req, 4<<10); err != nil {
		return err
	}
	var v httpx.Validator
	validRole(&v, &req.Role)
	v.Check(strings.TrimSpace(req.Reason) != "", "reason", "required")
	v.Check(id != m.actor(r).UserID, "id", "cannot_modify_own_roles")
	if err := v.Err(); err != nil {
		return err
	}
	err = db.WithTx(ctx, m.Svc.Pool, func(tx pgx.Tx) error {
		var kind string
		if err := tx.QueryRow(ctx, `SELECT kind FROM users WHERE id=$1 FOR UPDATE`, id).Scan(&kind); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return httpx.ErrNotFound
			}
			return err
		}
		if kind != "staff" {
			return httpx.Validation(httpx.FieldDetail{Field: "id", Reason: "citizens_have_no_staff_role"})
		}
		if err := assignRole(r, tx, id, req.Role, req.Reason); err != nil {
			return err
		}
		return m.write(r, tx, "user.role", "user", id.String(), req.Reason, map[string]any{"role": req.Role.Role,
			"scope_org_id": req.Role.ScopeOrgID, "expires_at": req.Role.ExpiresAt})
	})
	if err != nil {
		return err
	}
	m.Svc.Resolver.InvalidateAll()
	u, err := m.loadUser(r, id)
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, u)
	return nil
}

func (m *Module) resetPassword(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	id, err := m.targetID(r, auth.UserManage)
	if err != nil {
		return err
	}
	if id == m.actor(r).UserID {
		return httpx.NewError(http.StatusForbidden, "USE_CHANGE_PASSWORD", "برای حساب خودتان از «تغییر رمز» استفاده کنید")
	}
	temp := security.TemporaryPassword()
	err = db.WithTx(ctx, m.Svc.Pool, func(tx pgx.Tx) error {
		var console bool
		if err := tx.QueryRow(ctx, `SELECT console_access FROM users WHERE id=$1 FOR UPDATE`, id).Scan(&console); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return httpx.ErrNotFound
			}
			return err
		}
		if !console {
			return httpx.Validation(httpx.FieldDetail{Field: "id", Reason: "no_console_access"})
		}
		if err := security.SetPassword(ctx, tx, id, temp, true, 0); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE users SET failed_logins=0, locked_until=NULL WHERE id=$1`, id); err != nil {
			return err
		}
		if err := RevokeUserSessions(ctx, tx, id, "password_reset", "console"); err != nil {
			return err
		}
		return m.write(r, tx, "user.password_reset", "user", id.String(), "", nil)
	})
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"temporary_password": temp})
	return nil
}

func (m *Module) unlock(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	id, err := m.targetID(r, auth.UserManage)
	if err != nil {
		return err
	}
	err = db.WithTx(ctx, m.Svc.Pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE users SET locked_until=NULL, failed_logins=0 WHERE id=$1`, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return httpx.ErrNotFound
		}
		return m.write(r, tx, "user.unlock", "user", id.String(), "", nil)
	})
	if err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (m *Module) reverify(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	id, err := m.targetID(r, auth.ShahkarInquire)
	if err != nil {
		return err
	}
	p := m.actor(r)
	var nc, mobile *string
	if err := m.Svc.Pool.QueryRow(ctx, `SELECT national_code, mobile FROM users WHERE id=$1`, id).Scan(&nc, &mobile); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.ErrNotFound
		}
		return err
	}
	if nc == nil || mobile == nil {
		return httpx.Validation(httpx.FieldDetail{Field: "id", Reason: "no_identity"})
	}
	res, err := m.Svc.Shahkar(ctx, *nc, *mobile, "console_reverify", &id, &p.UserID)
	if err != nil {
		return err
	}
	if !res.OK {
		return errShahkarDown
	}
	err = db.WithTx(ctx, m.Svc.Pool, func(tx pgx.Tx) error {
		if res.Matched {
			_, err := tx.Exec(ctx, `UPDATE users SET shahkar_verified_at=now() WHERE id=$1`, id)
			return err
		}
		if id == p.UserID {
			return nil
		}
		if _, err := tx.Exec(ctx, `UPDATE users SET status='suspended', updated_at=now() WHERE id=$1`, id); err != nil {
			return err
		}
		if err := RevokeUserSessions(ctx, tx, id, "shahkar_mismatch", ""); err != nil {
			return err
		}
		return m.write(r, tx, "shahkar.mismatch_suspend", "user", id.String(), "", nil)
	})
	if err != nil {
		return err
	}
	m.Svc.Resolver.InvalidateAll()
	httpx.JSON(w, http.StatusOK, map[string]any{"matched": res.Matched})
	return nil
}

func (m *Module) userSessions(w http.ResponseWriter, r *http.Request) error {
	id, err := m.targetID(r, auth.SessionManage)
	if err != nil {
		return err
	}
	items, err := listSessions(r.Context(), m.Svc, id)
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
	return nil
}

func (m *Module) revokeAll(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	id, err := m.targetID(r, auth.SessionManage)
	if err != nil {
		return err
	}
	err = db.WithTx(ctx, m.Svc.Pool, func(tx pgx.Tx) error {
		if err := RevokeUserSessions(ctx, tx, id, "revoked_by_admin", ""); err != nil {
			return err
		}
		return m.write(r, tx, "session.revoke_all", "user", id.String(), "", nil)
	})
	if err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (m *Module) revokeOne(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	id, err := m.targetID(r, auth.SessionManage)
	if err != nil {
		return err
	}
	sid, err := httpx.PathUUID(r, "sid")
	if err != nil {
		return err
	}
	err = db.WithTx(ctx, m.Svc.Pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE sessions SET revoked_at=now(), revoked_reason='revoked_by_admin' WHERE id=$1 AND user_id=$2 AND revoked_at IS NULL`, sid, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return httpx.ErrNotFound
		}
		return m.write(r, tx, "session.revoke", "session", sid.String(), "", map[string]any{"user_id": id})
	})
	if err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (m *Module) inquiry(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	if err := m.Guard.Require(ctx, auth.ShahkarInquire, "shahkar", ""); err != nil {
		return err
	}
	var req struct {
		NationalCode string `json:"national_code"`
		Mobile       string `json:"mobile"`
	}
	if err := httpx.DecodeJSON(w, r, &req, 4<<10); err != nil {
		return err
	}
	nc, mobile := security.NormalizeDigits(req.NationalCode), security.NormalizeMobile(req.Mobile)
	var v httpx.Validator
	v.Check(security.ValidNationalCode(nc), "national_code", "invalid")
	v.Check(mobile != "", "mobile", "invalid")
	if err := v.Err(); err != nil {
		return err
	}
	p := m.actor(r)
	res, err := m.Svc.Shahkar(ctx, nc, mobile, "console_manual", nil, &p.UserID)
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"ok": res.OK, "matched": res.Matched, "endpoint": res.Endpoint, "message": res.Message})
	return nil
}

func (m *Module) inquiries(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	if err := m.Guard.Require(ctx, auth.ShahkarInquire, "shahkar", ""); err != nil {
		return err
	}
	nc := security.NormalizeDigits(r.URL.Query().Get("national_code"))
	rows, err := m.Svc.Pool.Query(ctx, `SELECT i.id, i.national_code, i.mobile, i.matched, i.provider_code, i.message, i.context,
		i.user_id, u.display_name, i.duration_ms, i.created_at FROM shahkar_inquiries i LEFT JOIN users u ON u.id=i.requested_by
		WHERE $1 = '' OR i.national_code=$1 ORDER BY i.id DESC LIMIT 200`, nc)
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id int64
		var ncv, mob, purpose string
		var matched *bool
		var code, msg, by *string
		var uid *uuid.UUID
		var dur *int
		var at time.Time
		if err := rows.Scan(&id, &ncv, &mob, &matched, &code, &msg, &purpose, &uid, &by, &dur, &at); err != nil {
			return err
		}
		items = append(items, map[string]any{"id": id, "national_code": ncv, "mobile": mob, "matched": matched, "provider_code": code,
			"message": msg, "context": purpose, "user_id": uid, "requested_by": by, "duration_ms": dur, "created_at": at})
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
	return rows.Err()
}

func (m *Module) getSettings(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	p := m.actor(r)
	if !p.Has(auth.SettingsManage) && !p.Has(auth.AuditConfigure) {
		return m.Guard.Deny(ctx, string(auth.SettingsManage), "settings", "", "missing_permission", nil)
	}
	st, err := m.Svc.Settings.Get(ctx)
	if err != nil {
		return err
	}
	values := st.AsMap()
	items := []map[string]any{}
	for _, k := range security.Keys() {
		perm := auth.SettingsManage
		if security.IsAuditKey(k) {
			perm = auth.AuditConfigure
		}
		items = append(items, map[string]any{"key": k, "title": security.Titles[k], "value": values[k], "editable": p.Has(perm),
			"managed_by": string(perm)})
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items, "selectable_events": security.SelectableEvents})
	return nil
}

func (m *Module) putSetting(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	key := r.PathValue("key")
	perm := auth.SettingsManage
	if security.IsAuditKey(key) {
		perm = auth.AuditConfigure
	}
	if err := m.Guard.Require(ctx, perm, "settings", key); err != nil {
		return err
	}
	var req struct {
		Value  json.RawMessage `json:"value"`
		Reason string          `json:"reason"`
	}
	if err := httpx.DecodeJSON(w, r, &req, 16<<10); err != nil {
		return err
	}
	var before, after any
	err := db.WithTx(ctx, m.Svc.Pool, func(tx pgx.Tx) error {
		var msg string
		before, after, msg = m.Svc.Settings.Set(ctx, tx, key, req.Value, m.actor(r).UserID)
		if msg != "" {
			return httpx.NewError(http.StatusUnprocessableEntity, "INVALID_SETTING", security.Titles[key]+": "+msg)
		}
		action := "settings.update"
		if security.IsAuditKey(key) {
			action = "audit.config"
		}
		return m.write(r, tx, action, "settings", key, req.Reason, map[string]any{"before": before, "after": after})
	})
	// The cache may have been refilled from the pool before the commit (e.g. by the audit write).
	m.Svc.Settings.Invalidate()
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"key": key, "value": after})
	return nil
}

func (m *Module) notifications(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	p := m.actor(r)
	if !p.Has(auth.SettingsManage) && !p.Has(auth.AuditRead) {
		return m.Guard.Deny(ctx, string(auth.AuditRead), "notifications", "", "missing_permission", nil)
	}
	rows, err := m.Svc.Pool.Query(ctx, `SELECT id, kind, severity, message, details::text, created_at FROM security_notifications ORDER BY id DESC LIMIT 100`)
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id int64
		var kind, sev, msg, details string
		var at time.Time
		if err := rows.Scan(&id, &kind, &sev, &msg, &details, &at); err != nil {
			return err
		}
		items = append(items, map[string]any{"id": id, "kind": kind, "severity": sev, "message": msg, "details": json.RawMessage(details), "created_at": at})
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
	return rows.Err()
}
