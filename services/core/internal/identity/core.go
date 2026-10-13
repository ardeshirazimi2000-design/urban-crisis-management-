// Package identity is the built-in identity provider (AUTH_MODE=local) required by the AFTA protection profile:
//
//   - Console: username + password, then an SMS one-time code to the Shahkar-verified mobile.
//   - Apps: first sign-in (and sign-in from a new device) verifies national code ↔ mobile ownership with Shahkar,
//     then an SMS code; later sign-ins from the same device need only the SMS code.
//   - Sessions: short-lived access tokens, rotating refresh tokens with reuse detection, idle timeout,
//     per-user limits and optional IP / user-agent binding.
//   - Authentication failures lock the account, progressively longer on repeated locks.
package identity

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/apiir"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/audit"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/auth"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/platform/config"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/platform/db"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/platform/httpx"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/security"
)

const (
	issuer          = "urban-crisis"
	audienceConsole = "crisis-console"
	audienceApp     = "crisis-app"
)

// Service holds the identity building blocks shared by the HTTP handlers.
type Service struct {
	Pool     *pgxpool.Pool
	Cfg      config.Config
	APIIR    apiir.Client
	Settings *security.Store
	Resolver *auth.Resolver
	Loc      *time.Location
	Now      func() time.Time
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) mac(parts ...string) string {
	m := hmac.New(sha256.New, []byte(s.Cfg.OTPPepper))
	m.Write([]byte(strings.Join(parts, "|")))
	return hex.EncodeToString(m.Sum(nil))
}

func randomToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func audienceOf(channel string) string {
	if channel == "app" {
		return audienceApp
	}
	return audienceConsole
}

// ---------------------------------------------------------------------------
// Errors (Persian messages; codes are stable for clients)
// ---------------------------------------------------------------------------

var (
	errInvalidLogin  = httpx.NewError(http.StatusUnauthorized, "LOGIN_FAILED", "نام کاربری یا رمز عبور نادرست است")
	errOTPInvalid    = httpx.NewError(http.StatusUnauthorized, "OTP_INVALID", "کد تأیید نادرست یا منقضی است")
	errShahkarDown   = httpx.NewError(http.StatusBadGateway, "SHAHKAR_UNAVAILABLE", "سامانه شاهکار در حال حاضر پاسخگو نیست؛ چند دقیقه دیگر تلاش کنید")
	errShahkarNoMat  = httpx.NewError(http.StatusUnprocessableEntity, "SHAHKAR_MISMATCH", "شماره موبایل به نام صاحب این کد ملی ثبت نشده است")
	errSMSFailed     = httpx.NewError(http.StatusBadGateway, "SMS_FAILED", "ارسال پیامک کد تأیید ناموفق بود؛ لطفاً دوباره تلاش کنید")
	errSessionEnded  = httpx.NewError(http.StatusUnauthorized, "SESSION_ENDED", "نشست شما پایان یافته است؛ دوباره وارد شوید")
	errSessionIdle   = httpx.NewError(http.StatusUnauthorized, "SESSION_IDLE_TIMEOUT", "نشست به دلیل عدم فعالیت خاتمه یافت؛ دوباره وارد شوید")
	errSessionMoved  = httpx.NewError(http.StatusUnauthorized, "SESSION_BINDING", "نشست از نشانی یا مرورگر دیگری استفاده شد و خاتمه یافت؛ دوباره وارد شوید")
	errSessionRepl   = httpx.NewError(http.StatusUnauthorized, "SESSION_REPLACED", "با ورود دوباره همین حساب، این نشست خاتمه یافت")
	errSessionLimit  = httpx.NewError(http.StatusConflict, "SESSION_LIMIT", "تعداد نشست‌های همزمان شما به سقف رسیده است؛ ابتدا از نشست دیگر خارج شوید")
	errNotAllowedNet = httpx.NewError(http.StatusForbidden, "SESSION_NOT_ALLOWED", "ورود به کنسول از این شبکه مجاز نیست")
	errNotAllowedTim = httpx.NewError(http.StatusForbidden, "SESSION_NOT_ALLOWED", "ورود به کنسول در این روز یا ساعت مجاز نیست")
)

func tooMany(msg string) *httpx.Error {
	return httpx.NewError(http.StatusTooManyRequests, "RATE_LIMITED", msg)
}

// ---------------------------------------------------------------------------
// Shahkar
// ---------------------------------------------------------------------------

// Shahkar asks api.ir whether mobile belongs to the owner of nationalCode and records the inquiry.
func (s *Service) Shahkar(ctx context.Context, nationalCode, mobile, purpose string, userID, requestedBy *uuid.UUID) (apiir.ShahkarResult, error) {
	r := s.APIIR.Shahkar(ctx, nationalCode, mobile)
	var matched *bool
	if r.OK {
		matched = &r.Matched
	}
	if _, err := s.Pool.Exec(ctx, `INSERT INTO shahkar_inquiries (national_code, mobile, matched, provider_code, message, context, user_id, requested_by, duration_ms)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`, nationalCode, mobile, matched, r.Endpoint+":"+r.Code, r.Message, purpose, userID, requestedBy,
		int(r.Duration.Milliseconds())); err != nil {
		return r, err
	}
	outcome := "success"
	if !r.OK {
		outcome = "failed"
	} else if !r.Matched {
		outcome = "denied"
	}
	target := ""
	if userID != nil {
		target = userID.String()
	}
	if err := audit.WriteStandalone(ctx, s.Pool, audit.Entry{ActorID: requestedBy, Action: "shahkar.inquiry", TargetType: "user",
		TargetID: target, Outcome: outcome, Details: map[string]any{"context": purpose, "mobile": security.MaskMobile(mobile),
			"national_code": maskNC(nationalCode), "matched": matched}}); err != nil {
		return r, err
	}
	return r, nil
}

// RequireShahkar returns an error unless Shahkar confirms the pair.
func (s *Service) RequireShahkar(ctx context.Context, nationalCode, mobile, purpose string, userID, requestedBy *uuid.UUID) error {
	r, err := s.Shahkar(ctx, nationalCode, mobile, purpose, userID, requestedBy)
	if err != nil {
		return err
	}
	if !r.OK {
		return errShahkarDown
	}
	if !r.Matched {
		return errShahkarNoMat
	}
	return nil
}

func maskNC(nc string) string {
	if len(nc) != 10 {
		return "***"
	}
	return nc[:3] + "****" + nc[7:]
}

func shahkarStale(at *time.Time, days int, now time.Time) bool {
	return at == nil || now.Sub(*at) > time.Duration(days)*24*time.Hour
}

// ---------------------------------------------------------------------------
// One-time codes
// ---------------------------------------------------------------------------

type challengeReq struct {
	UserID       *uuid.UUID
	Channel      string
	Purpose      string
	Mobile       string
	NationalCode string
	DeviceID     string
	ShahkarOK    bool
}

type challengeResp struct {
	ChallengeID  uuid.UUID `json:"challenge_id"`
	MobileMasked string    `json:"mobile_masked"`
	ExpiresIn    int       `json:"expires_in"`
	ResendAfter  int       `json:"resend_after"`
}

// issueOTP creates a challenge and delivers the code by SMS, with a per-mobile cooldown and hourly cap.
func (s *Service) issueOTP(ctx context.Context, c challengeReq) (challengeResp, error) {
	st, err := s.Settings.Get(ctx)
	if err != nil {
		return challengeResp{}, err
	}
	var last *time.Time
	var hour int
	if err := s.Pool.QueryRow(ctx, `SELECT max(created_at), count(*) FROM auth_challenges
		WHERE mobile=$1 AND created_at > now() - interval '1 hour'`, c.Mobile).Scan(&last, &hour); err != nil {
		return challengeResp{}, err
	}
	if last != nil {
		if wait := s.Cfg.OTPResendAfter - s.now().Sub(*last); wait > 0 {
			return challengeResp{}, tooMany(fmt.Sprintf("ارسال مجدد کد تا %d ثانیه دیگر امکان‌پذیر است", int(wait.Seconds())+1))
		}
	}
	if hour >= s.Cfg.OTPMaxPerHour {
		return challengeResp{}, tooMany("سقف ارسال کد در یک ساعت تکمیل شده است")
	}
	id := uuid.New()
	code := security.RandomDigits(s.Cfg.OTPLength)
	err = db.WithTx(ctx, s.Pool, func(tx pgx.Tx) error {
		// Supersede earlier pending challenges for this mobile.
		if _, err := tx.Exec(ctx, `UPDATE auth_challenges SET consumed_at=now() WHERE mobile=$1 AND consumed_at IS NULL`, c.Mobile); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO auth_challenges (id, user_id, channel, purpose, mobile, national_code, device_id, shahkar_ok, code_hash, ip, expires_at)
			VALUES ($1,$2,$3,$4,$5,NULLIF($6,''),NULLIF($7,''),$8,$9,$10, now() + make_interval(secs => $11))`,
			id, c.UserID, c.Channel, c.Purpose, c.Mobile, c.NationalCode, c.DeviceID, c.ShahkarOK, s.mac(id.String(), code),
			httpx.Info(ctx).IP, st.OTPTTLSec)
		return err
	})
	if err != nil {
		return challengeResp{}, err
	}
	if err := s.APIIR.SendOTP(ctx, c.Mobile, code); err != nil {
		slog.WarnContext(ctx, "otp delivery failed", "err", err.Error())
		_, _ = s.Pool.Exec(ctx, `UPDATE auth_challenges SET consumed_at=now() WHERE id=$1`, id)
		return challengeResp{}, errSMSFailed
	}
	return challengeResp{ChallengeID: id, MobileMasked: security.MaskMobile(c.Mobile), ExpiresIn: st.OTPTTLSec,
		ResendAfter: int(s.Cfg.OTPResendAfter.Seconds())}, nil
}

type challenge struct {
	ID           uuid.UUID
	UserID       *uuid.UUID
	Channel      string
	Purpose      string
	Mobile       string
	NationalCode *string
	DeviceID     *string
	ShahkarOK    bool
}

// verifyOTP consumes a challenge when the code matches. Wrong codes count against the attempt limit; the
// returned failure carries the user, if any, so the caller can count it as an authentication failure.
func (s *Service) verifyOTP(ctx context.Context, id uuid.UUID, code, channel string) (challenge, *uuid.UUID, error) {
	st, err := s.Settings.Get(ctx)
	if err != nil {
		return challenge{}, nil, err
	}
	var c challenge
	var hash string
	var attempts int
	var expired, consumed bool
	err = s.Pool.QueryRow(ctx, `SELECT id, user_id, channel, purpose, mobile, national_code, device_id, shahkar_ok, code_hash, attempts,
		expires_at < now(), consumed_at IS NOT NULL FROM auth_challenges WHERE id=$1`, id).
		Scan(&c.ID, &c.UserID, &c.Channel, &c.Purpose, &c.Mobile, &c.NationalCode, &c.DeviceID, &c.ShahkarOK, &hash, &attempts, &expired, &consumed)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, nil, errOTPInvalid
	}
	if err != nil {
		return c, nil, err
	}
	if c.Channel != channel || expired || consumed || attempts >= st.OTPMaxAttempts {
		return c, c.UserID, errOTPInvalid
	}
	code = security.NormalizeDigits(code)
	if subtle.ConstantTimeCompare([]byte(s.mac(id.String(), code)), []byte(hash)) != 1 {
		_, err := s.Pool.Exec(ctx, `UPDATE auth_challenges SET attempts=attempts+1,
			consumed_at=CASE WHEN attempts+1 >= $2 THEN now() ELSE consumed_at END WHERE id=$1`, id, st.OTPMaxAttempts)
		if err != nil {
			return c, c.UserID, err
		}
		return c, c.UserID, errOTPInvalid
	}
	tag, err := s.Pool.Exec(ctx, `UPDATE auth_challenges SET consumed_at=now() WHERE id=$1 AND consumed_at IS NULL`, id)
	if err != nil {
		return c, c.UserID, err
	}
	if tag.RowsAffected() != 1 {
		return c, c.UserID, errOTPInvalid
	}
	return c, nil, nil
}

// ---------------------------------------------------------------------------
// Authentication failures (FIA_AFL.1) and session establishment (FTA_TSE.1)
// ---------------------------------------------------------------------------

// recordFailure counts a failed password or code. On reaching the threshold the account is locked; with
// progressive lockout each further lock doubles the duration (at most 24 h).
func (s *Service) recordFailure(ctx context.Context, userID uuid.UUID, channel, reason string) {
	st, err := s.Settings.Get(ctx)
	if err != nil {
		return
	}
	err = db.WithTx(ctx, s.Pool, func(tx pgx.Tx) error {
		var failed, locks int
		if err := tx.QueryRow(ctx, `UPDATE users SET failed_logins=failed_logins+1, failed_since_success=failed_since_success+1,
			last_failed_login_at=now() WHERE id=$1 RETURNING failed_logins, lock_count`, userID).Scan(&failed, &locks); err != nil {
			return err
		}
		if failed < st.LockoutThreshold {
			return nil
		}
		minutes := st.LockoutMinutes
		if st.LockoutProgressive {
			minutes = min(st.LockoutMinutes<<min(locks, 10), 24*60)
		}
		if _, err := tx.Exec(ctx, `UPDATE users SET locked_until=now() + make_interval(mins => $2), lock_count=lock_count+1, failed_logins=0
			WHERE id=$1`, userID, minutes); err != nil {
			return err
		}
		return audit.Write(ctx, tx, audit.Entry{ActorID: &userID, Action: "account.locked", TargetType: "user", TargetID: userID.String(),
			Outcome: "denied", Channel: channel, Details: map[string]any{"reason": reason, "minutes": minutes, "lock_count": locks + 1}})
	})
	if err != nil {
		slog.ErrorContext(ctx, "record auth failure", "err", err.Error())
	}
}

func (s *Service) lockedMinutes(ctx context.Context, userID uuid.UUID) int {
	var left int
	_ = s.Pool.QueryRow(ctx, `SELECT COALESCE(CEIL(EXTRACT(EPOCH FROM locked_until - now()) / 60), 0)::int FROM users WHERE id=$1`, userID).Scan(&left)
	return left
}

// consoleAllowed enforces network, day and time-of-day restrictions on console sign-in (global and per user).
func (s *Service) consoleAllowed(ctx context.Context, userID uuid.UUID, userCIDRs []string) error {
	st, err := s.Settings.Get(ctx)
	if err != nil {
		return err
	}
	ip := httpx.Info(ctx).IP
	day, hhmm := security.LocalClock(s.now(), s.Loc)
	var reason string
	var clientErr *httpx.Error
	switch {
	case !security.IPAllowed(ip, st.ConsoleCIDRs):
		reason, clientErr = "network", errNotAllowedNet
	case !security.IPAllowed(ip, userCIDRs):
		reason, clientErr = "user_network", errNotAllowedNet
	case !containsInt(st.ConsoleDays, day):
		reason, clientErr = "day", errNotAllowedTim
	case !security.InWindow(hhmm, st.ConsoleHours):
		reason, clientErr = "time", errNotAllowedTim
	default:
		return nil
	}
	_ = audit.WriteStandalone(ctx, s.Pool, audit.Entry{ActorID: &userID, Action: "session.denied", TargetType: "user", TargetID: userID.String(),
		Outcome: "denied", Channel: "console", Details: map[string]any{"reason": reason, "day": day, "time": hhmm}})
	return clientErr
}

func containsInt(list []int, v int) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Sessions
// ---------------------------------------------------------------------------

type tokens struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
}

type accessClaims struct {
	jwt.RegisteredClaims
	SID  string `json:"sid"`
	Name string `json:"name,omitempty"`
}

func (s *Service) signAccess(subject, name, channel string, sid uuid.UUID) (string, error) {
	now := s.now()
	c := accessClaims{RegisteredClaims: jwt.RegisteredClaims{Issuer: issuer, Subject: subject, Audience: jwt.ClaimStrings{audienceOf(channel)},
		IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(now.Add(s.Cfg.AccessTokenTTL)), ID: uuid.NewString()},
		SID: sid.String(), Name: name}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, c).SignedString([]byte(s.Cfg.JWTSecret))
}

// Verifier validates access tokens of built-in sessions for both channels.
type Verifier struct{ Secret []byte }

func (v Verifier) Verify(_ context.Context, raw string) (auth.Claims, error) {
	for _, ch := range []string{"console", "app"} {
		var c accessClaims
		_, err := jwt.ParseWithClaims(raw, &c, func(t *jwt.Token) (any, error) { return v.Secret, nil },
			jwt.WithValidMethods([]string{"HS256"}), jwt.WithIssuer(issuer), jwt.WithAudience(audienceOf(ch)),
			jwt.WithExpirationRequired(), jwt.WithLeeway(30*time.Second))
		if err != nil {
			continue
		}
		sid, err := uuid.Parse(c.SID)
		if err != nil || c.Subject == "" {
			return auth.Claims{}, auth.ErrInvalidToken
		}
		return auth.Claims{Subject: c.Subject, Name: c.Name, SessionID: &sid, Channel: ch}, nil
	}
	return auth.Claims{}, auth.ErrInvalidToken
}

type sessionUser struct {
	ID          uuid.UUID
	Subject     string
	DisplayName string
}

// createSession enforces the per-user session limit and issues the token pair.
func (s *Service) createSession(ctx context.Context, tx pgx.Tx, u sessionUser, channel, deviceID string) (tokens, uuid.UUID, error) {
	st, err := s.Settings.Get(ctx)
	if err != nil {
		return tokens{}, uuid.Nil, err
	}
	limit := st.MaxConsoleSessions
	ttl := s.Cfg.ConsoleSessionTTL
	if channel == "app" {
		limit, ttl = st.MaxAppSessions, s.Cfg.AppSessionTTL
	}
	rows, err := tx.Query(ctx, `SELECT id FROM sessions WHERE user_id=$1 AND channel=$2 AND revoked_at IS NULL AND expires_at > now()
		ORDER BY last_seen_at ASC FOR UPDATE`, u.ID, channel)
	if err != nil {
		return tokens{}, uuid.Nil, err
	}
	var active []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return tokens{}, uuid.Nil, err
		}
		active = append(active, id)
	}
	rows.Close()
	if len(active) >= limit {
		if st.SessionLimitAction == "deny" {
			return tokens{}, uuid.Nil, errSessionLimit
		}
		for _, id := range active[:len(active)-limit+1] {
			if _, err := tx.Exec(ctx, `UPDATE sessions SET revoked_at=now(), revoked_reason='replaced' WHERE id=$1`, id); err != nil {
				return tokens{}, uuid.Nil, err
			}
			if err := audit.Write(ctx, tx, audit.Entry{ActorID: &u.ID, Action: "session.replaced", TargetType: "session",
				TargetID: id.String(), Outcome: "success", Channel: channel}); err != nil {
				return tokens{}, uuid.Nil, err
			}
		}
	}
	ri := httpx.Info(ctx)
	sid := uuid.New()
	secret := randomToken(32)
	boundIP := ""
	if (channel == "console" && st.BindIPConsole) || (channel == "app" && st.BindIPApp) {
		boundIP = ri.IP
	}
	if _, err := tx.Exec(ctx, `INSERT INTO sessions (id, user_id, channel, refresh_hash, device_id, ip, bound_ip, user_agent, expires_at)
		VALUES ($1,$2,$3,$4,NULLIF($5,''),$6,NULLIF($7,''),$8,$9)`, sid, u.ID, channel, s.mac("refresh", secret), deviceID, ri.IP, boundIP,
		ri.UserAgent, s.now().Add(ttl)); err != nil {
		return tokens{}, uuid.Nil, err
	}
	access, err := s.signAccess(u.Subject, u.DisplayName, channel, sid)
	if err != nil {
		return tokens{}, uuid.Nil, err
	}
	return tokens{AccessToken: access, RefreshToken: sid.String() + "." + secret, TokenType: "Bearer",
		ExpiresIn: int(s.Cfg.AccessTokenTTL.Seconds())}, sid, nil
}

type sessionRow struct {
	UserID     uuid.UUID
	Channel    string
	RevokedAt  *time.Time
	Reason     *string
	ExpiresAt  time.Time
	LastSeen   time.Time
	BoundIP    *string
	UserAgent  *string
	MustChange bool
	PwChanged  *time.Time
}

func (s *Service) loadSession(ctx context.Context, q db.DBTX, sid uuid.UUID, lock bool) (sessionRow, error) {
	var r sessionRow
	forUpdate := ""
	if lock {
		forUpdate = " FOR UPDATE OF s"
	}
	err := q.QueryRow(ctx, `SELECT s.user_id, s.channel, s.revoked_at, s.revoked_reason, s.expires_at, s.last_seen_at, s.bound_ip, s.user_agent,
		u.must_change_password, u.password_changed_at FROM sessions s JOIN users u ON u.id=s.user_id WHERE s.id=$1`+forUpdate, sid).
		Scan(&r.UserID, &r.Channel, &r.RevokedAt, &r.Reason, &r.ExpiresAt, &r.LastSeen, &r.BoundIP, &r.UserAgent, &r.MustChange, &r.PwChanged)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, errSessionEnded
	}
	return r, err
}

// validate applies revocation, expiry, idle timeout and binding; it revokes the session when one fails.
func (s *Service) validate(ctx context.Context, q db.DBTX, sid uuid.UUID, r sessionRow) error {
	if r.RevokedAt != nil {
		if r.Reason != nil && *r.Reason == "replaced" {
			return errSessionRepl
		}
		return errSessionEnded
	}
	if s.now().After(r.ExpiresAt) {
		return errSessionEnded
	}
	st, err := s.Settings.Get(ctx)
	if err != nil {
		return err
	}
	idle := time.Duration(st.ConsoleIdleMinutes) * time.Minute
	if r.Channel == "app" {
		idle = time.Duration(st.AppIdleDays) * 24 * time.Hour
	}
	ri := httpx.Info(ctx)
	revoke := func(reason, action string, clientErr *httpx.Error) error {
		if _, err := q.Exec(ctx, `UPDATE sessions SET revoked_at=now(), revoked_reason=$2 WHERE id=$1 AND revoked_at IS NULL`, sid, reason); err != nil {
			return err
		}
		uid := r.UserID
		_ = audit.WriteStandalone(ctx, s.Pool, audit.Entry{ActorID: &uid, Action: action, TargetType: "session", TargetID: sid.String(),
			Outcome: "denied", Channel: r.Channel, Details: map[string]any{"reason": reason}})
		return clientErr
	}
	if s.now().Sub(r.LastSeen) > idle {
		return revoke("idle_timeout", "session.idle_timeout", errSessionIdle)
	}
	if r.BoundIP != nil && *r.BoundIP != ri.IP {
		return revoke("ip_changed", "session.hijack_suspected", errSessionMoved)
	}
	if st.BindUserAgent && r.UserAgent != nil && *r.UserAgent != ri.UserAgent {
		return revoke("user_agent_changed", "session.hijack_suspected", errSessionMoved)
	}
	return nil
}

// Check implements auth.SessionChecker.
func (s *Service) Check(ctx context.Context, sid, userID uuid.UUID, channel string) (bool, error) {
	r, err := s.loadSession(ctx, s.Pool, sid, false)
	if err != nil {
		return false, err
	}
	if r.UserID != userID || r.Channel != channel {
		return false, errSessionEnded
	}
	if err := s.validate(ctx, s.Pool, sid, r); err != nil {
		return false, err
	}
	// Activity keeps the session alive; writes are throttled.
	if s.now().Sub(r.LastSeen) > 30*time.Second {
		if _, err := s.Pool.Exec(ctx, `UPDATE sessions SET last_seen_at=now() WHERE id=$1`, sid); err != nil {
			return false, err
		}
	}
	st, err := s.Settings.Get(ctx)
	if err != nil {
		return false, err
	}
	return channel == "console" && (r.MustChange || security.PasswordExpired(r.PwChanged, st.PasswordMaxAgeDays)), nil
}

// rotate exchanges a refresh token for a new pair. Presenting an already-rotated token revokes the session.
func (s *Service) rotate(ctx context.Context, refresh string) (tokens, error) {
	sidStr, secret, ok := strings.Cut(refresh, ".")
	sid, err := uuid.Parse(sidStr)
	if !ok || err != nil || secret == "" {
		return tokens{}, errSessionEnded
	}
	var out tokens
	var failure error
	err = db.WithTx(ctx, s.Pool, func(tx pgx.Tx) error {
		r, err := s.loadSession(ctx, tx, sid, true)
		if err != nil {
			return err
		}
		var cur, prev string
		var prevHash *string
		if err := tx.QueryRow(ctx, `SELECT refresh_hash, prev_refresh_hash FROM sessions WHERE id=$1`, sid).Scan(&cur, &prevHash); err != nil {
			return err
		}
		if prevHash != nil {
			prev = *prevHash
		}
		h := s.mac("refresh", secret)
		if r.RevokedAt == nil && prev != "" && subtle.ConstantTimeCompare([]byte(h), []byte(prev)) == 1 {
			if _, err := tx.Exec(ctx, `UPDATE sessions SET revoked_at=now(), revoked_reason='refresh_reuse' WHERE id=$1`, sid); err != nil {
				return err
			}
			uid := r.UserID
			failure = errSessionEnded
			return audit.Write(ctx, tx, audit.Entry{ActorID: &uid, Action: "session.refresh_reuse", TargetType: "session",
				TargetID: sid.String(), Outcome: "denied", Channel: r.Channel})
		}
		if subtle.ConstantTimeCompare([]byte(h), []byte(cur)) != 1 {
			failure = errSessionEnded
			return nil
		}
		if err := s.validate(ctx, tx, sid, r); err != nil {
			failure = err
			return nil
		}
		var u sessionUser
		var status string
		if err := tx.QueryRow(ctx, `SELECT id, subject_id, display_name, status FROM users WHERE id=$1`, r.UserID).
			Scan(&u.ID, &u.Subject, &u.DisplayName, &status); err != nil {
			return err
		}
		if status != "active" {
			failure = errSessionEnded
			return nil
		}
		next := randomToken(32)
		if _, err := tx.Exec(ctx, `UPDATE sessions SET prev_refresh_hash=refresh_hash, refresh_hash=$2, last_seen_at=now() WHERE id=$1`,
			sid, s.mac("refresh", next)); err != nil {
			return err
		}
		access, err := s.signAccess(u.Subject, u.DisplayName, r.Channel, sid)
		if err != nil {
			return err
		}
		out = tokens{AccessToken: access, RefreshToken: sid.String() + "." + next, TokenType: "Bearer", ExpiresIn: int(s.Cfg.AccessTokenTTL.Seconds())}
		return nil
	})
	if err != nil {
		return tokens{}, err
	}
	if failure != nil {
		return tokens{}, failure
	}
	return out, nil
}

// RevokeUserSessions ends every active session of a user (optionally only one channel).
func RevokeUserSessions(ctx context.Context, tx db.DBTX, userID uuid.UUID, reason, channel string) error {
	_, err := tx.Exec(ctx, `UPDATE sessions SET revoked_at=now(), revoked_reason=$2
		WHERE user_id=$1 AND revoked_at IS NULL AND ($3 = '' OR channel=$3)`, userID, reason, channel)
	return err
}

// recordLogin resets failure counters and returns the access history shown after sign-in (FTA_TAH.1).
func (s *Service) recordLogin(ctx context.Context, tx pgx.Tx, userID uuid.UUID) (map[string]any, error) {
	var lastAt *time.Time
	var lastIP *string
	var failed int
	var lastFailed *time.Time
	if err := tx.QueryRow(ctx, `SELECT last_login_at, last_login_ip, failed_since_success, last_failed_login_at FROM users WHERE id=$1`, userID).
		Scan(&lastAt, &lastIP, &failed, &lastFailed); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE users SET last_login_at=now(), last_login_ip=$2, failed_logins=0, failed_since_success=0,
		lock_count=0 WHERE id=$1`, userID, httpx.Info(ctx).IP); err != nil {
		return nil, err
	}
	return map[string]any{"last_login_at": lastAt, "last_login_ip": lastIP, "failed_since_last_login": failed,
		"last_failed_login_at": lastFailed}, nil
}
