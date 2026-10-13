package identity

import (
	"context"
	"errors"
	"fmt"
	"net/http"
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

// Module registers the sign-in endpoints and the identity administration endpoints.
type Module struct {
	Svc          *Service
	Guard        *auth.Guard
	LoginLimiter *httpx.RateLimiter // per client IP
}

func (m *Module) Routes(r *httpx.Router) {
	r.Public("GET /auth/banner", m.banner)
	r.Public("POST /auth/console/login", m.consoleLogin)
	r.Public("POST /auth/console/verify", m.consoleVerify)
	r.Public("POST /auth/app/start", m.appStart)
	r.Public("POST /auth/app/verify", m.appVerify)
	r.Public("POST /auth/refresh", m.refresh)
	r.Auth("POST /auth/logout", m.logout)
	r.Auth("POST /auth/console/change-password", m.changePassword)
	r.Auth("GET /me/sessions", m.mySessions)
	m.adminRoutes(r)
}

func (m *Module) limit(r *http.Request, kind string) error {
	if m.LoginLimiter != nil && !m.LoginLimiter.Allow(kind+":"+httpx.ClientIP(r)) {
		return httpx.ErrRateLimited
	}
	return nil
}

func (m *Module) banner(w http.ResponseWriter, r *http.Request) error {
	st, err := m.Svc.Settings.Get(r.Context())
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"banner": st.Banner,
		"help": "ورود به کنسول: نام کاربری و رمز عبور، سپس کد ارسال‌شده به موبایل ثبت‌شده. در صورت فراموشی رمز یا قفل شدن حساب با مدیر امنیت تماس بگیرید."})
	return nil
}

// ---------------------------------------------------------------------------
// Console: password, then SMS code
// ---------------------------------------------------------------------------

type consoleUser struct {
	ID           uuid.UUID
	PasswordHash *string
	Mobile       *string
	NationalCode *string
	Kind         string
	Status       string
	Console      bool
	Locked       bool
	ShahkarAt    *time.Time
	CIDRs        []string
}

func (m *Module) failLogin(ctx context.Context, userID *uuid.UUID, username, reason string, clientErr *httpx.Error) error {
	_ = audit.WriteStandalone(ctx, m.Svc.Pool, audit.Entry{ActorID: userID, Action: "console.login", TargetType: "user",
		Outcome: "denied", Channel: "console", Details: map[string]any{"username": username, "reason": reason}})
	return clientErr
}

func (m *Module) consoleLogin(w http.ResponseWriter, r *http.Request) error {
	ctx := httpx.WithChannel(r.Context(), "console")
	if err := m.limit(r, "login"); err != nil {
		return err
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := httpx.DecodeJSON(w, r, &req, 4<<10); err != nil {
		return err
	}
	username := strings.ToLower(strings.TrimSpace(req.Username))
	if username == "" || req.Password == "" || len(req.Password) > 128 || len(username) > 64 {
		return errInvalidLogin
	}
	var u consoleUser
	err := m.Svc.Pool.QueryRow(ctx, `SELECT id, password_hash, mobile, national_code, kind, status, console_access,
		COALESCE(locked_until > now(), false), shahkar_verified_at, allowed_cidrs FROM users WHERE username=$1`, username).
		Scan(&u.ID, &u.PasswordHash, &u.Mobile, &u.NationalCode, &u.Kind, &u.Status, &u.Console, &u.Locked, &u.ShahkarAt, &u.CIDRs)
	found := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	hash := ""
	if u.PasswordHash != nil {
		hash = *u.PasswordHash
	}
	pwOK := security.VerifyPassword(req.Password, hash) // also run for unknown users (timing)
	if !found {
		return m.failLogin(ctx, nil, username, "unknown_user", errInvalidLogin)
	}
	if u.Locked {
		return m.failLogin(ctx, &u.ID, username, "locked", httpx.NewError(http.StatusLocked, "ACCOUNT_LOCKED",
			fmt.Sprintf("حساب به‌طور موقت قفل شده است؛ %d دقیقه دیگر تلاش کنید", m.Svc.lockedMinutes(ctx, u.ID))))
	}
	if !pwOK {
		m.Svc.recordFailure(ctx, u.ID, "console", "password")
		return m.failLogin(ctx, &u.ID, username, "bad_password", errInvalidLogin)
	}
	if u.Status != "active" || !u.Console || u.Kind != "staff" || u.Mobile == nil || u.NationalCode == nil {
		return m.failLogin(ctx, &u.ID, username, "not_allowed", errInvalidLogin)
	}
	grants, err := auth.LoadGrants(ctx, m.Svc.Pool, u.ID)
	if err != nil {
		return err
	}
	if !slices.ContainsFunc(grants, func(g auth.Grant) bool { return g.Role != auth.RoleCitizen }) {
		return m.failLogin(ctx, &u.ID, username, "no_console_role", errInvalidLogin)
	}
	if err := m.Svc.consoleAllowed(ctx, u.ID, u.CIDRs); err != nil {
		return err
	}
	st, err := m.Svc.Settings.Get(ctx)
	if err != nil {
		return err
	}
	// Periodic re-verification of SIM ownership before trusting the mobile as the second factor.
	if shahkarStale(u.ShahkarAt, st.ShahkarReverifyDay, m.Svc.now()) {
		if err := m.Svc.RequireShahkar(ctx, *u.NationalCode, *u.Mobile, "console_login", &u.ID, nil); err != nil {
			if errors.Is(err, errShahkarNoMat) {
				_ = db.WithTx(ctx, m.Svc.Pool, func(tx pgx.Tx) error {
					if _, err := tx.Exec(ctx, `UPDATE users SET status='suspended', updated_at=now() WHERE id=$1`, u.ID); err != nil {
						return err
					}
					return audit.Write(ctx, tx, audit.Entry{ActorID: &u.ID, Action: "shahkar.mismatch_suspend", TargetType: "user",
						TargetID: u.ID.String(), Outcome: "denied"})
				})
				return httpx.NewError(http.StatusForbidden, "SHAHKAR_MISMATCH", "مالکیت شماره موبایل تأیید نشد؛ حساب تعلیق شد. با مدیر امنیت تماس بگیرید")
			}
			return err
		}
		if _, err := m.Svc.Pool.Exec(ctx, `UPDATE users SET shahkar_verified_at=now() WHERE id=$1`, u.ID); err != nil {
			return err
		}
	}
	ch, err := m.Svc.issueOTP(ctx, challengeReq{UserID: &u.ID, Channel: "console", Purpose: "console_login", Mobile: *u.Mobile})
	if err != nil {
		return err
	}
	_ = audit.WriteStandalone(ctx, m.Svc.Pool, audit.Entry{ActorID: &u.ID, Action: "console.login.password_ok", TargetType: "user",
		TargetID: u.ID.String(), Outcome: "success"})
	httpx.JSON(w, http.StatusOK, map[string]any{"step": "otp", "challenge": ch})
	return nil
}

func (m *Module) consoleVerify(w http.ResponseWriter, r *http.Request) error {
	ctx := httpx.WithChannel(r.Context(), "console")
	if err := m.limit(r, "otp"); err != nil {
		return err
	}
	var req struct {
		ChallengeID uuid.UUID `json:"challenge_id"`
		Code        string    `json:"code"`
	}
	if err := httpx.DecodeJSON(w, r, &req, 4<<10); err != nil {
		return err
	}
	c, failedUser, err := m.Svc.verifyOTP(ctx, req.ChallengeID, req.Code, "console")
	if err != nil {
		if failedUser != nil {
			m.Svc.recordFailure(ctx, *failedUser, "console", "otp")
		}
		_ = audit.WriteStandalone(ctx, m.Svc.Pool, audit.Entry{ActorID: failedUser, Action: "console.login.otp", TargetType: "user", Outcome: "denied"})
		return err
	}
	if c.Purpose != "console_login" || c.UserID == nil {
		return errOTPInvalid
	}
	var out map[string]any
	err = db.WithTx(ctx, m.Svc.Pool, func(tx pgx.Tx) error {
		var u sessionUser
		var status string
		var locked, mustChange bool
		var pwChanged *time.Time
		if err := tx.QueryRow(ctx, `SELECT id, subject_id, display_name, status, COALESCE(locked_until > now(), false), must_change_password,
			password_changed_at FROM users WHERE id=$1 FOR UPDATE`, *c.UserID).
			Scan(&u.ID, &u.Subject, &u.DisplayName, &status, &locked, &mustChange, &pwChanged); err != nil {
			return err
		}
		if status != "active" || locked {
			return httpx.NewError(http.StatusForbidden, "ACCOUNT_INACTIVE", "حساب کاربری غیرفعال یا قفل است")
		}
		history, err := m.Svc.recordLogin(ctx, tx, u.ID)
		if err != nil {
			return err
		}
		tok, sid, err := m.Svc.createSession(ctx, tx, u, "console", "")
		if err != nil {
			return err
		}
		st, err := m.Svc.Settings.Get(ctx)
		if err != nil {
			return err
		}
		if err := audit.Write(ctx, tx, audit.Entry{ActorID: &u.ID, Action: "console.login", TargetType: "session", TargetID: sid.String(),
			Outcome: "success"}); err != nil {
			return err
		}
		out = map[string]any{"tokens": tok, "password_change_required": mustChange || security.PasswordExpired(pwChanged, st.PasswordMaxAgeDays),
			"access_history": history}
		return nil
	})
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, out)
	return nil
}

func (m *Module) changePassword(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	p := auth.FromContext(ctx)
	if p.SessionID == nil || p.Channel != "console" {
		return httpx.ErrForbidden
	}
	var req struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if err := httpx.DecodeJSON(w, r, &req, 4<<10); err != nil {
		return err
	}
	st, err := m.Svc.Settings.Get(ctx)
	if err != nil {
		return err
	}
	var hash, username, nc, mobile *string
	if err := m.Svc.Pool.QueryRow(ctx, `SELECT password_hash, username, national_code, mobile FROM users WHERE id=$1`, p.UserID).
		Scan(&hash, &username, &nc, &mobile); err != nil {
		return err
	}
	if hash == nil || !security.VerifyPassword(req.CurrentPassword, *hash) {
		m.Svc.recordFailure(ctx, p.UserID, "console", "change_password")
		_ = audit.WriteStandalone(ctx, m.Svc.Pool, audit.Entry{ActorID: &p.UserID, Action: "password.change", TargetType: "user",
			TargetID: p.UserID.String(), Outcome: "denied", Reason: "wrong_current_password"})
		return httpx.NewError(http.StatusBadRequest, "WRONG_PASSWORD", "رمز فعلی نادرست است")
	}
	if probs := security.PasswordProblems(req.NewPassword, st.PasswordMinLength, deref(username), deref(nc), deref(mobile)); len(probs) > 0 {
		return httpx.NewError(http.StatusUnprocessableEntity, "WEAK_PASSWORD", "رمز عبور جدید: "+strings.Join(probs, "، "))
	}
	if req.NewPassword == req.CurrentPassword {
		return httpx.NewError(http.StatusUnprocessableEntity, "WEAK_PASSWORD", "رمز جدید باید با رمز فعلی متفاوت باشد")
	}
	err = db.WithTx(ctx, m.Svc.Pool, func(tx pgx.Tx) error {
		if err := security.SetPassword(ctx, tx, p.UserID, req.NewPassword, false, st.PasswordHistory); err != nil {
			if errors.Is(err, security.ErrReused) {
				return httpx.NewError(http.StatusUnprocessableEntity, "PASSWORD_REUSED",
					fmt.Sprintf("رمز عبور نباید با %d رمز قبلی یکسان باشد", st.PasswordHistory))
			}
			return err
		}
		// Other sessions of the user end; this one continues.
		if _, err := tx.Exec(ctx, `UPDATE sessions SET revoked_at=now(), revoked_reason='password_changed'
			WHERE user_id=$1 AND revoked_at IS NULL AND id<>$2`, p.UserID, *p.SessionID); err != nil {
			return err
		}
		return audit.Write(ctx, tx, audit.Entry{ActorID: &p.UserID, ActorRoles: p.RoleNames(), Action: "password.change", TargetType: "user",
			TargetID: p.UserID.String(), Outcome: "success"})
	})
	if err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// ---------------------------------------------------------------------------
// Apps: Shahkar on first sign-in / new device, SMS code every time
// ---------------------------------------------------------------------------

type appUser struct {
	ID        uuid.UUID
	Mobile    *string
	Kind      string
	Status    string
	App       bool
	Locked    bool
	ShahkarAt *time.Time
	Enrolled  *time.Time
}

func (m *Module) appStart(w http.ResponseWriter, r *http.Request) error {
	ctx := httpx.WithChannel(r.Context(), "app")
	if err := m.limit(r, "app"); err != nil {
		return err
	}
	var req struct {
		NationalCode string `json:"national_code"`
		Mobile       string `json:"mobile"`
		DeviceID     string `json:"device_id"`
		DeviceToken  string `json:"device_token"`
	}
	if err := httpx.DecodeJSON(w, r, &req, 4<<10); err != nil {
		return err
	}
	nc := security.NormalizeDigits(req.NationalCode)
	mobile := security.NormalizeMobile(req.Mobile)
	var v httpx.Validator
	v.Check(security.ValidNationalCode(nc), "national_code", "invalid")
	v.Check(mobile != "", "mobile", "invalid")
	v.Check(len(req.DeviceID) >= 8 && len(req.DeviceID) <= 128, "device_id", "invalid")
	if err := v.Err(); err != nil {
		return err
	}
	st, err := m.Svc.Settings.Get(ctx)
	if err != nil {
		return err
	}
	var u appUser
	err = m.Svc.Pool.QueryRow(ctx, `SELECT id, mobile, kind, status, app_access, COALESCE(locked_until > now(), false),
		shahkar_verified_at, app_enrolled_at FROM users WHERE national_code=$1`, nc).
		Scan(&u.ID, &u.Mobile, &u.Kind, &u.Status, &u.App, &u.Locked, &u.ShahkarAt, &u.Enrolled)
	exists := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	needShahkar := true
	purpose := "app_signup"
	if exists {
		if u.Locked {
			return httpx.NewError(http.StatusLocked, "ACCOUNT_LOCKED", fmt.Sprintf("حساب به‌طور موقت قفل شده است؛ %d دقیقه دیگر تلاش کنید",
				m.Svc.lockedMinutes(ctx, u.ID)))
		}
		if u.Status != "active" || (u.Kind == "staff" && !u.App) {
			_ = audit.WriteStandalone(ctx, m.Svc.Pool, audit.Entry{ActorID: &u.ID, Action: "app.login", TargetType: "user",
				TargetID: u.ID.String(), Outcome: "denied", Reason: "not_allowed"})
			return httpx.NewError(http.StatusForbidden, "APP_NOT_ALLOWED", "دسترسی اپلیکیشن برای این حساب فعال نیست")
		}
		if u.Mobile != nil && *u.Mobile != mobile && u.Kind == "staff" {
			return httpx.NewError(http.StatusUnprocessableEntity, "MOBILE_MISMATCH", "شماره موبایل با اطلاعات ثبت‌شده شما مطابقت ندارد")
		}
		purpose = "app_login"
		sameMobile := u.Mobile != nil && *u.Mobile == mobile
		knownDevice := sameMobile && u.Enrolled != nil && m.deviceTrusted(ctx, u.ID, req.DeviceID, req.DeviceToken, st.ShahkarOnNewDevice)
		needShahkar = !knownDevice || shahkarStale(u.ShahkarAt, st.ShahkarReverifyDay, m.Svc.now())
	} else if !st.CitizenSignup {
		return httpx.NewError(http.StatusForbidden, "SIGNUP_DISABLED", "ثبت‌نام از اپلیکیشن در حال حاضر امکان‌پذیر نیست")
	}
	var uid *uuid.UUID
	if exists {
		uid = &u.ID
	}
	if needShahkar {
		if err := m.Svc.RequireShahkar(ctx, nc, mobile, purpose, uid, nil); err != nil {
			if errors.Is(err, errShahkarNoMat) && exists {
				_ = audit.WriteStandalone(ctx, m.Svc.Pool, audit.Entry{ActorID: uid, Action: "shahkar.mismatch", TargetType: "user",
					TargetID: u.ID.String(), Outcome: "denied", Channel: "app"})
			}
			return err
		}
	}
	ch, err := m.Svc.issueOTP(ctx, challengeReq{UserID: uid, Channel: "app", Purpose: purpose, Mobile: mobile, NationalCode: nc,
		DeviceID: req.DeviceID, ShahkarOK: needShahkar})
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"step": "otp", "challenge": ch, "shahkar_verified": needShahkar,
		"needs_profile": !exists})
	return nil
}

// deviceTrusted reports whether this device was registered by an earlier sign-in and presented its secret.
// When Shahkar on new devices is switched off, any device is trusted once the user is enrolled.
func (m *Module) deviceTrusted(ctx context.Context, userID uuid.UUID, deviceID, token string, shahkarOnNew bool) bool {
	if !shahkarOnNew {
		return true
	}
	if token == "" {
		return false
	}
	var hash *string
	if err := m.Svc.Pool.QueryRow(ctx, `SELECT secret_hash FROM user_devices WHERE user_id=$1 AND device_id=$2`, userID, deviceID).Scan(&hash); err != nil {
		return false
	}
	return hash != nil && *hash == m.Svc.mac("device", deviceID, token)
}

func (m *Module) appVerify(w http.ResponseWriter, r *http.Request) error {
	ctx := httpx.WithChannel(r.Context(), "app")
	if err := m.limit(r, "otp"); err != nil {
		return err
	}
	var req struct {
		ChallengeID uuid.UUID `json:"challenge_id"`
		Code        string    `json:"code"`
		DeviceID    string    `json:"device_id"`
		DeviceName  string    `json:"device_name"`
		FirstName   string    `json:"first_name"`
		LastName    string    `json:"last_name"`
	}
	if err := httpx.DecodeJSON(w, r, &req, 4<<10); err != nil {
		return err
	}
	c, failedUser, err := m.Svc.verifyOTP(ctx, req.ChallengeID, req.Code, "app")
	if err != nil {
		if failedUser != nil {
			m.Svc.recordFailure(ctx, *failedUser, "app", "otp")
		}
		_ = audit.WriteStandalone(ctx, m.Svc.Pool, audit.Entry{ActorID: failedUser, Action: "app.login.otp", TargetType: "user", Outcome: "denied"})
		return err
	}
	if c.DeviceID == nil || *c.DeviceID != req.DeviceID || c.NationalCode == nil {
		return errOTPInvalid
	}
	req.FirstName, req.LastName = strings.TrimSpace(req.FirstName), strings.TrimSpace(req.LastName)
	if c.UserID == nil && (req.FirstName == "" || req.LastName == "" || len(req.FirstName) > 100 || len(req.LastName) > 100) {
		return httpx.Validation(httpx.FieldDetail{Field: "first_name", Reason: "required"}, httpx.FieldDetail{Field: "last_name", Reason: "required"})
	}
	var out map[string]any
	err = db.WithTx(ctx, m.Svc.Pool, func(tx pgx.Tx) error {
		var u sessionUser
		action := "app.login"
		if c.UserID == nil {
			if c.Purpose != "app_signup" || !c.ShahkarOK {
				return errOTPInvalid
			}
			u.ID = uuid.New()
			u.Subject = "local:" + u.ID.String()
			u.DisplayName = req.FirstName + " " + req.LastName
			err := tx.QueryRow(ctx, `INSERT INTO users (id, subject_id, display_name, kind, national_code, mobile, first_name, last_name,
				app_access, shahkar_verified_at, app_enrolled_at) VALUES ($1,$2,$3,'citizen',$4,$5,$6,$7,true,now(),now()) RETURNING id`,
				u.ID, u.Subject, u.DisplayName, *c.NationalCode, c.Mobile, req.FirstName, req.LastName).Scan(&u.ID)
			if db.IsUniqueViolation(err, "") {
				return httpx.Conflict("ALREADY_REGISTERED", "این کد ملی قبلاً ثبت شده است؛ دوباره وارد شوید")
			}
			if err != nil {
				return err
			}
			action = "app.signup"
		} else {
			var status string
			var locked bool
			if err := tx.QueryRow(ctx, `SELECT id, subject_id, display_name, status, COALESCE(locked_until > now(), false) FROM users
				WHERE id=$1 FOR UPDATE`, *c.UserID).Scan(&u.ID, &u.Subject, &u.DisplayName, &status, &locked); err != nil {
				return err
			}
			if status != "active" || locked {
				return httpx.NewError(http.StatusForbidden, "ACCOUNT_INACTIVE", "حساب کاربری غیرفعال یا قفل است")
			}
			// A citizen whose new mobile Shahkar confirmed moves to it; devices and sessions of the old number end.
			var oldMobile *string
			if err := tx.QueryRow(ctx, `SELECT mobile FROM users WHERE id=$1`, u.ID).Scan(&oldMobile); err != nil {
				return err
			}
			if oldMobile != nil && *oldMobile != c.Mobile {
				if !c.ShahkarOK {
					return errOTPInvalid
				}
				if _, err := tx.Exec(ctx, `DELETE FROM user_devices WHERE user_id=$1`, u.ID); err != nil {
					return err
				}
				if err := RevokeUserSessions(ctx, tx, u.ID, "mobile_changed", "app"); err != nil {
					return err
				}
			}
			if _, err := tx.Exec(ctx, `UPDATE users SET mobile=$2, shahkar_verified_at=CASE WHEN $3 THEN now() ELSE shahkar_verified_at END,
				app_enrolled_at=COALESCE(app_enrolled_at, now()), updated_at=now() WHERE id=$1`, u.ID, c.Mobile, c.ShahkarOK); err != nil {
				return err
			}
		}
		history, err := m.Svc.recordLogin(ctx, tx, u.ID)
		if err != nil {
			return err
		}
		deviceToken := randomToken(32)
		name := req.DeviceName
		if len(name) > 100 {
			name = name[:100]
		}
		if _, err := tx.Exec(ctx, `INSERT INTO user_devices (user_id, device_id, secret_hash, name) VALUES ($1,$2,$3,$4)
			ON CONFLICT (user_id, device_id) DO UPDATE SET secret_hash=EXCLUDED.secret_hash, name=EXCLUDED.name, last_seen_at=now()`,
			u.ID, req.DeviceID, m.Svc.mac("device", req.DeviceID, deviceToken), name); err != nil {
			return err
		}
		tok, sid, err := m.Svc.createSession(ctx, tx, u, "app", req.DeviceID)
		if err != nil {
			return err
		}
		if err := audit.Write(ctx, tx, audit.Entry{ActorID: &u.ID, Action: action, TargetType: "session", TargetID: sid.String(),
			Outcome: "success", Details: map[string]any{"shahkar": c.ShahkarOK}}); err != nil {
			return err
		}
		out = map[string]any{"tokens": tok, "device_token": deviceToken, "access_history": history}
		return nil
	})
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, out)
	return nil
}

// ---------------------------------------------------------------------------
// Both channels
// ---------------------------------------------------------------------------

func (m *Module) refresh(w http.ResponseWriter, r *http.Request) error {
	if err := m.limit(r, "refresh"); err != nil {
		return err
	}
	var req struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := httpx.DecodeJSON(w, r, &req, 4<<10); err != nil {
		return err
	}
	tok, err := m.Svc.rotate(r.Context(), req.RefreshToken)
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, tok)
	return nil
}

func (m *Module) logout(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	p := auth.FromContext(ctx)
	if p.SessionID == nil {
		w.WriteHeader(http.StatusNoContent)
		return nil
	}
	err := db.WithTx(ctx, m.Svc.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE sessions SET revoked_at=now(), revoked_reason='logout' WHERE id=$1 AND revoked_at IS NULL`, *p.SessionID); err != nil {
			return err
		}
		return audit.Write(ctx, tx, audit.Entry{ActorID: &p.UserID, ActorRoles: p.RoleNames(), Action: p.Channel + ".logout",
			TargetType: "session", TargetID: p.SessionID.String(), Outcome: "success"})
	})
	if err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (m *Module) mySessions(w http.ResponseWriter, r *http.Request) error {
	p := auth.FromContext(r.Context())
	items, err := listSessions(r.Context(), m.Svc, p.UserID)
	if err != nil {
		return err
	}
	var history map[string]any
	var lastAt, lastFailed *time.Time
	var lastIP *string
	var failed int
	if err := m.Svc.Pool.QueryRow(r.Context(), `SELECT last_login_at, last_login_ip, failed_since_success, last_failed_login_at FROM users WHERE id=$1`,
		p.UserID).Scan(&lastAt, &lastIP, &failed, &lastFailed); err == nil {
		history = map[string]any{"last_login_at": lastAt, "last_login_ip": lastIP, "failed_since_last_login": failed, "last_failed_login_at": lastFailed}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items, "current": p.SessionID, "access_history": history})
	return nil
}

func listSessions(ctx context.Context, s *Service, userID uuid.UUID) ([]map[string]any, error) {
	rows, err := s.Pool.Query(ctx, `SELECT s.id, s.channel, s.device_id, d.name, s.ip, s.user_agent, s.created_at, s.last_seen_at,
		s.expires_at, s.revoked_at, s.revoked_reason FROM sessions s LEFT JOIN user_devices d ON d.user_id=s.user_id AND d.device_id=s.device_id
		WHERE s.user_id=$1 ORDER BY s.created_at DESC LIMIT 50`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id uuid.UUID
		var channel string
		var device, name, ip, ua, reason *string
		var created, seen, expires time.Time
		var revoked *time.Time
		if err := rows.Scan(&id, &channel, &device, &name, &ip, &ua, &created, &seen, &expires, &revoked, &reason); err != nil {
			return nil, err
		}
		items = append(items, map[string]any{"id": id, "channel": channel, "device_id": device, "device_name": name, "ip": ip,
			"user_agent": ua, "created_at": created, "last_seen_at": seen, "expires_at": expires, "revoked_at": revoked, "revoked_reason": reason})
	}
	return items, rows.Err()
}
