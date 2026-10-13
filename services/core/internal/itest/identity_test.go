package itest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/apiir"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/app"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/identity"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/media"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/platform/db"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/security"
)

// A second server in AUTH_MODE=local (built-in identity: Shahkar + SMS OTP) against the same database.
var (
	localOnce sync.Once
	localURL  string
	localApp  *app.App
	mock      *apiir.Mock
)

const staffPassword = "Strong-Pass-2025!x"

func local(t *testing.T) {
	t.Helper()
	localOnce.Do(func() {
		c := cfg
		c.AuthMode = "local"
		a := app.New(c, pool, identity.Verifier{Secret: []byte(c.JWTSecret)}, &media.FSStore{Dir: c.ObjectStoreDir, Secret: []byte(c.MediaURLSecret)})
		localApp = a
		mock = a.APIIR.(*apiir.Mock)
		localURL = httptest.NewServer(a.Handler).URL
	})
}

var ncSeq = 100000

// nationalCode returns a fresh valid Iranian national code.
func nationalCode() string {
	ncSeq++
	base := fmt.Sprintf("%09d", 1_000_000_00+ncSeq)
	sum := 0
	for i := 0; i < 9; i++ {
		sum += int(base[i]-'0') * (10 - i)
	}
	r := sum % 11
	if r >= 2 {
		r = 11 - r
	}
	return fmt.Sprintf("%s%d", base, r)
}

func mobile() string { return fmt.Sprintf("0912%07d", seq.Add(1)%10_000_000) }

type lc struct {
	t       *testing.T
	token   string
	refresh string
	ua      string
}

func (c *lc) call(method, path string, body any) result {
	c.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, localURL+"/api/v1"+path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	ua := c.ua
	if ua == "" {
		ua = "itest-browser"
	}
	req.Header.Set("User-Agent", ua)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return result{Status: resp.StatusCode, Body: b, Header: resp.Header}
}

func (c *lc) must(method, path string, body any, want int, out any) result {
	c.t.Helper()
	r := c.call(method, path, body)
	if r.Status != want {
		c.t.Fatalf("%s %s: want %d got %d: %s", method, path, want, r.Status, r.Body)
	}
	if out != nil {
		r.json(c.t, out)
	}
	return r
}

type staffUser struct {
	ID       uuid.UUID
	Username string
	Mobile   string
	NC       string
}

// staff inserts a console user with a known password and one role.
func staff(t *testing.T, role string) staffUser {
	t.Helper()
	local(t)
	ctx := context.Background()
	u := staffUser{ID: uuid.New(), Username: fmt.Sprintf("u%d", seq.Add(1)), Mobile: mobile(), NC: nationalCode()}
	err := db.WithTx(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO users (id, subject_id, display_name, kind, national_code, mobile, first_name, last_name, username,
			console_access, app_access, shahkar_verified_at) VALUES ($1,$2,'کاربر آزمون','staff',$3,$4,'کاربر','آزمون',$5,true,true,now())`,
			u.ID, "local:"+u.ID.String(), u.NC, u.Mobile, u.Username); err != nil {
			return err
		}
		if err := security.SetPassword(ctx, tx, u.ID, staffPassword, false, 0); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO user_roles (user_id, role_code, reason) VALUES ($1,$2,'test')`, u.ID, role)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// consoleLogin signs in with password + OTP.
func consoleLogin(t *testing.T, username, password, mob string) (*lc, map[string]any) {
	t.Helper()
	c := &lc{t: t}
	var step struct {
		Challenge struct {
			ChallengeID uuid.UUID `json:"challenge_id"`
		} `json:"challenge"`
	}
	c.must("POST", "/auth/console/login", map[string]any{"username": username, "password": password}, 200, &step)
	var out struct {
		Tokens struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
		} `json:"tokens"`
		PasswordChangeRequired bool           `json:"password_change_required"`
		AccessHistory          map[string]any `json:"access_history"`
	}
	c.must("POST", "/auth/console/verify", map[string]any{"challenge_id": step.Challenge.ChallengeID, "code": mock.LastCode(mob)}, 200, &out)
	c.token, c.refresh = out.Tokens.AccessToken, out.Tokens.RefreshToken
	return c, map[string]any{"password_change_required": out.PasswordChangeRequired, "access_history": out.AccessHistory}
}

func TestConsoleSignInPasswordAndOTP(t *testing.T) {
	op := staff(t, "OPERATOR")
	anon := &lc{t: t}
	if r := anon.call("POST", "/auth/console/login", map[string]any{"username": op.Username, "password": "wrong"}); r.Status != 401 || r.errCode() != "LOGIN_FAILED" {
		t.Fatalf("wrong password: %d %s", r.Status, r.Body)
	}
	if r := anon.call("POST", "/auth/console/login", map[string]any{"username": "nobody-here", "password": "x"}); r.errCode() != "LOGIN_FAILED" {
		t.Fatal("unknown users must get the same message")
	}
	var step struct {
		Challenge struct {
			ChallengeID uuid.UUID `json:"challenge_id"`
		} `json:"challenge"`
	}
	anon.must("POST", "/auth/console/login", map[string]any{"username": op.Username, "password": staffPassword}, 200, &step)
	anon.must("POST", "/auth/console/verify", map[string]any{"challenge_id": step.Challenge.ChallengeID, "code": "000000"}, 401, nil)
	var out struct {
		Tokens struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
		} `json:"tokens"`
		AccessHistory map[string]any `json:"access_history"`
	}
	anon.must("POST", "/auth/console/verify", map[string]any{"challenge_id": step.Challenge.ChallengeID, "code": mock.LastCode(op.Mobile)}, 200, &out)
	if n, _ := out.AccessHistory["failed_since_last_login"].(float64); n < 2 {
		t.Fatalf("access history must report the failed attempts: %v", out.AccessHistory)
	}
	c := &lc{t: t, token: out.Tokens.AccessToken, refresh: out.Tokens.RefreshToken}
	var me struct {
		Roles []string `json:"roles"`
	}
	c.must("GET", "/me", nil, 200, &me)
	if len(me.Roles) != 1 || me.Roles[0] != "OPERATOR" {
		t.Fatalf("roles %v", me.Roles)
	}
	c.must("GET", "/reports", nil, 200, nil)

	// Refresh rotation; presenting the old refresh token again ends the session (stolen-token detection).
	var rot struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	anon.must("POST", "/auth/refresh", map[string]any{"refresh_token": c.refresh}, 200, &rot)
	anon.must("POST", "/auth/refresh", map[string]any{"refresh_token": c.refresh}, 401, nil)
	c.token = rot.AccessToken
	if r := c.call("GET", "/me", nil); r.Status != 401 {
		t.Fatalf("session must end after refresh-token reuse: %d", r.Status)
	}
	if countAudit(t, "session.refresh_reuse", "denied", "") == 0 {
		var n int
		pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_log WHERE action='session.refresh_reuse'`).Scan(&n)
		if n == 0 {
			t.Fatal("refresh reuse must be audited")
		}
	}
}

func TestConsoleLockoutAndSessionLimit(t *testing.T) {
	op := staff(t, "OPERATOR")
	anon := &lc{t: t}
	for i := 0; i < 5; i++ {
		anon.must("POST", "/auth/console/login", map[string]any{"username": op.Username, "password": "nope"}, 401, nil)
	}
	r := anon.call("POST", "/auth/console/login", map[string]any{"username": op.Username, "password": staffPassword})
	if r.Status != 423 {
		t.Fatalf("account must be locked: %d %s", r.Status, r.Body)
	}
	var locks int
	pool.QueryRow(context.Background(), `SELECT lock_count FROM users WHERE id=$1`, op.ID).Scan(&locks)
	if locks != 1 {
		t.Fatalf("lock_count %d", locks)
	}

	other := staff(t, "OPERATOR")
	first, _ := consoleLogin(t, other.Username, staffPassword, other.Mobile)
	pool.Exec(context.Background(), `UPDATE auth_challenges SET created_at=created_at - interval '2 minutes' WHERE mobile=$1`, other.Mobile)
	second, _ := consoleLogin(t, other.Username, staffPassword, other.Mobile)
	second.must("GET", "/me", nil, 200, nil)
	if r := first.call("GET", "/me", nil); r.Status != 401 || r.errCode() != "SESSION_REPLACED" {
		t.Fatalf("older console session must be replaced: %d %s", r.Status, r.Body)
	}
	// Session bound to the browser: another user agent cannot use the token.
	second.ua = "another-browser"
	if r := second.call("GET", "/me", nil); r.errCode() != "SESSION_BINDING" {
		t.Fatalf("user-agent binding: %s", r.Body)
	}
}

func TestUserAdministrationWithShahkar(t *testing.T) {
	admin := staff(t, "SECURITY_ADMIN")
	c, _ := consoleLogin(t, admin.Username, staffPassword, admin.Mobile)
	nc, mob := nationalCode(), mobile()
	body := map[string]any{"national_code": nc, "mobile": mob, "first_name": "پرویز", "last_name": "نادری", "username": "pnaderi" + nc[:4],
		"console_access": true, "role": map[string]any{"role": "OPERATOR"}}
	mock.SetMismatch(nc, mob, true)
	if r := c.call("POST", "/admin/users", body); r.Status != 422 || r.errCode() != "SHAHKAR_MISMATCH" {
		t.Fatalf("mismatch must be refused: %d %s", r.Status, r.Body)
	}
	mock.SetMismatch(nc, mob, false)
	var created struct {
		User              map[string]any `json:"user"`
		TemporaryPassword string         `json:"temporary_password"`
	}
	c.must("POST", "/admin/users", body, 201, &created)
	if !strings.HasPrefix(created.TemporaryPassword, "Tmp-") {
		t.Fatalf("temporary password %q", created.TemporaryPassword)
	}
	uid := created.User["id"].(string)

	// First sign-in with the temporary password: only changing it is allowed.
	u, info := consoleLogin(t, body["username"].(string), created.TemporaryPassword, mob)
	if info["password_change_required"] != true {
		t.Fatal("temporary password must require a change")
	}
	if r := u.call("GET", "/reports", nil); r.errCode() != "PASSWORD_CHANGE_REQUIRED" {
		t.Fatalf("expected PASSWORD_CHANGE_REQUIRED, got %s", r.Body)
	}
	u.must("POST", "/auth/console/change-password", map[string]any{"current_password": created.TemporaryPassword, "new_password": "short"}, 422, nil)
	u.must("POST", "/auth/console/change-password", map[string]any{"current_password": created.TemporaryPassword, "new_password": "New-Strong-Pass-99!"}, 204, nil)
	u.must("GET", "/reports", nil, 200, nil)

	// One role per user; changing it ends the user's sessions. Nobody changes their own role.
	c.must("PUT", "/admin/users/"+uid+"/role", map[string]any{"role": map[string]any{"role": "GIS_ANALYST"}, "reason": "جابجایی"}, 200, nil)
	if r := u.call("GET", "/me", nil); r.Status != 401 {
		t.Fatal("role change must end sessions")
	}
	c.must("PUT", "/admin/users/"+admin.ID.String()+"/role", map[string]any{"role": map[string]any{"role": "OPERATOR"}, "reason": "x"}, 422, nil)
	var roles int
	pool.QueryRow(context.Background(), `SELECT count(*) FROM user_roles WHERE user_id=$1`, uid).Scan(&roles)
	if roles != 1 {
		t.Fatalf("user must hold exactly one role, has %d", roles)
	}

	// Password reset issues a working temporary password.
	var reset struct {
		TemporaryPassword string `json:"temporary_password"`
	}
	c.must("POST", "/admin/users/"+uid+"/reset-password", nil, 200, &reset)
	pool.Exec(context.Background(), `UPDATE auth_challenges SET created_at=created_at - interval '2 minutes' WHERE mobile=$1`, mob)
	if _, info := consoleLogin(t, body["username"].(string), reset.TemporaryPassword, mob); info["password_change_required"] != true {
		t.Fatal("reset password must require a change")
	}
}

func TestSeparationOfDutiesAndSettings(t *testing.T) {
	admin := staff(t, "SECURITY_ADMIN")
	auditor := staff(t, "SECURITY_AUDITOR")
	a, _ := consoleLogin(t, admin.Username, staffPassword, admin.Mobile)
	au, _ := consoleLogin(t, auditor.Username, staffPassword, auditor.Mobile)

	a.must("GET", "/admin/audit", nil, 403, nil)
	au.must("GET", "/admin/audit", nil, 200, nil)
	var v struct {
		Valid bool `json:"valid"`
	}
	au.must("GET", "/admin/audit/verify", nil, 200, &v)
	if !v.Valid {
		t.Fatal("audit chain must verify")
	}
	a.must("PUT", "/admin/settings/password.minLength", map[string]any{"value": 14}, 200, nil)
	a.must("PUT", "/admin/settings/audit.maxRows", map[string]any{"value": 6000000}, 403, nil)
	au.must("PUT", "/admin/settings/password.minLength", map[string]any{"value": 8}, 403, nil)
	au.must("PUT", "/admin/settings/audit.disabledEvents", map[string]any{"value": []string{"console.login"}}, 422, nil)
	a.must("PUT", "/admin/settings/password.minLength", map[string]any{"value": 4}, 422, nil)
	a.must("PUT", "/admin/settings/password.minLength", map[string]any{"value": 12}, 200, nil)

	// Console access restricted by network: the test client (127.0.0.1) is outside 10.0.0.0/8.
	a.must("PUT", "/admin/settings/access.consoleCidrs", map[string]any{"value": []string{"10.0.0.0/8"}}, 200, nil)
	op := staff(t, "OPERATOR")
	if r := (&lc{t: t}).call("POST", "/auth/console/login", map[string]any{"username": op.Username, "password": staffPassword}); r.errCode() != "SESSION_NOT_ALLOWED" {
		t.Fatalf("network restriction: %s", r.Body)
	}
	a.must("PUT", "/admin/settings/access.consoleCidrs", map[string]any{"value": []string{}}, 200, nil)

	var keyed int
	pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_log WHERE action='settings.update' AND hash_alg='hmac-sha256' AND ip<>''`).Scan(&keyed)
	if keyed == 0 {
		t.Fatal("settings changes must be audited with a keyed hash and the caller's address")
	}
}

func TestAppShahkarFirstThenOTP(t *testing.T) {
	local(t)
	nc, mob, device := nationalCode(), mobile(), "device-"+uuid.NewString()
	anon := &lc{t: t, ua: "crisis-app/1.0"}
	start := func(token string) (uuid.UUID, bool) {
		var s struct {
			Challenge struct {
				ChallengeID uuid.UUID `json:"challenge_id"`
			} `json:"challenge"`
			Shahkar bool `json:"shahkar_verified"`
		}
		pool.Exec(context.Background(), `UPDATE auth_challenges SET created_at=created_at - interval '2 minutes' WHERE mobile=$1`, mob)
		anon.must("POST", "/auth/app/start", map[string]any{"national_code": nc, "mobile": mob, "device_id": device, "device_token": token}, 200, &s)
		return s.Challenge.ChallengeID, s.Shahkar
	}
	before := mock.ShahkarCall
	id, viaShahkar := start("")
	if !viaShahkar || mock.ShahkarCall != before+1 {
		t.Fatal("first sign-in must go through Shahkar")
	}
	anon.must("POST", "/auth/app/verify", map[string]any{"challenge_id": id, "code": mock.LastCode(mob), "device_id": device}, 422, nil)
	id, _ = start("")
	var out struct {
		Tokens struct {
			AccessToken string `json:"access_token"`
		} `json:"tokens"`
		DeviceToken string `json:"device_token"`
	}
	anon.must("POST", "/auth/app/verify", map[string]any{"challenge_id": id, "code": mock.LastCode(mob), "device_id": device,
		"first_name": "مریم", "last_name": "احمدی"}, 200, &out)
	citizen := &lc{t: t, token: out.Tokens.AccessToken, ua: "crisis-app/1.0"}
	var me struct {
		Roles []string `json:"roles"`
	}
	citizen.must("GET", "/me", nil, 200, &me)
	if len(me.Roles) != 1 || me.Roles[0] != "CITIZEN" {
		t.Fatalf("roles %v", me.Roles)
	}
	citizen.must("GET", "/admin/users", nil, 403, nil)

	// Later sign-in from the same device: SMS code only.
	before = mock.ShahkarCall
	id, viaShahkar = start(out.DeviceToken)
	if viaShahkar || mock.ShahkarCall != before {
		t.Fatal("a known device must not repeat Shahkar")
	}
	anon.must("POST", "/auth/app/verify", map[string]any{"challenge_id": id, "code": mock.LastCode(mob), "device_id": device}, 200, nil)

	// A device without its token (e.g. another phone) goes through Shahkar again.
	before = mock.ShahkarCall
	if _, viaShahkar = start("forged-token"); !viaShahkar || mock.ShahkarCall != before+1 {
		t.Fatal("unknown device must go through Shahkar")
	}

	// Console-only staff cannot use the app; app-channel tokens never carry console permissions.
	op := staff(t, "OPERATOR")
	pool.Exec(context.Background(), `UPDATE users SET app_access=false WHERE id=$1`, op.ID)
	if r := anon.call("POST", "/auth/app/start", map[string]any{"national_code": op.NC, "mobile": op.Mobile, "device_id": device}); r.Status != 403 {
		t.Fatalf("console-only user on app: %d %s", r.Status, r.Body)
	}
}

func TestIntegritySealsAndAuditArchive(t *testing.T) {
	local(t)
	ctx := context.Background()
	cmd := login(t, grant{Role: "COMMANDER", OrgCode: "command"})
	in := createIncident(t, cmd, "command", "open")
	auditor := staff(t, "SECURITY_AUDITOR")
	au, _ := consoleLogin(t, auditor.Username, staffPassword, auditor.Mobile)

	var res struct {
		Checked    int `json:"checked"`
		Violations []struct {
			ID uuid.UUID `json:"record_id"`
		} `json:"violations"`
	}
	au.must("POST", "/admin/integrity/check", nil, 200, &res)
	for _, v := range res.Violations {
		if v.ID == in.ID {
			t.Fatal("an incident changed through the application must verify")
		}
	}

	// A direct database edit without the key is detected.
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	conn.Exec(ctx, `SELECT set_config('crisis.integrity_key', '', false)`)
	if _, err := conn.Exec(ctx, `UPDATE incidents SET severity='low' WHERE id=$1`, in.ID); err != nil {
		t.Fatal(err)
	}
	conn.Exec(ctx, `SELECT set_config('crisis.integrity_key', $1, false)`, db.IntegrityKey)
	conn.Release()
	au.must("POST", "/admin/integrity/check", nil, 200, &res)
	found := false
	for _, v := range res.Violations {
		found = found || v.ID == in.ID
	}
	if !found {
		t.Fatal("tampered incident must be reported")
	}
	var n int
	pool.QueryRow(ctx, `SELECT count(*) FROM security_notifications WHERE kind='integrity'`).Scan(&n)
	if n == 0 {
		t.Fatal("integrity violation must raise a security notification")
	}
	// Repair the record through the application path (re-seals) so later tests start clean.
	pool.Exec(ctx, `UPDATE incidents SET severity='high' WHERE id=$1`, in.ID)

	// Export is audited and signed; archiving keeps the remaining chain verifiable.
	r := au.call("GET", "/admin/audit/export?format=csv", nil)
	if r.Status != 200 || !strings.HasPrefix(r.Header.Get("X-Export-Digest"), "hmac-sha256=") {
		t.Fatalf("export: %d %v", r.Status, r.Header)
	}
	admin := staff(t, "SECURITY_ADMIN")
	a, _ := consoleLogin(t, admin.Username, staffPassword, admin.Mobile)
	a.must("POST", "/admin/audit/archive", map[string]any{"before": "2999-01-01", "reason": "x"}, 403, nil)
	var before int
	pool.QueryRow(ctx, `SELECT count(*) FROM audit_log`).Scan(&before)
	au.must("POST", "/admin/audit/archive", map[string]any{"before": time.Now().Add(-time.Second).UTC().Format(time.RFC3339), "reason": "بایگانی دوره‌ای"}, 200, nil)
	var after int
	pool.QueryRow(ctx, `SELECT count(*) FROM audit_log`).Scan(&after)
	if after >= before {
		t.Fatalf("archive must remove old records: %d -> %d", before, after)
	}
	var v struct {
		Valid   bool `json:"valid"`
		Checked int  `json:"checked"`
	}
	au.must("GET", "/admin/audit/verify", nil, 200, &v)
	if !v.Valid {
		t.Fatal("chain must verify from the archive anchor")
	}
	if _, err := pool.Exec(ctx, `DELETE FROM audit_log WHERE seq = (SELECT max(seq) FROM audit_log)`); err == nil {
		t.Fatal("audit rows must not be deletable outside an archive")
	}

	// Controlled export of operational data: permission, audit and digest.
	cmdLocal := staff(t, "COMMANDER")
	c, _ := consoleLogin(t, cmdLocal.Username, staffPassword, cmdLocal.Mobile)
	if r := c.call("GET", "/admin/export/incidents?format=json", nil); r.Status != 200 || r.Header.Get("X-Export-Digest") == "" {
		t.Fatalf("export incidents: %d %s", r.Status, r.Body)
	}
	au.must("GET", "/admin/export/incidents", nil, 403, nil)
}
