// Package security holds the run-time security settings (AFTA PP: FMT_SMF.1, FMT_MOF.1, FMT_MTD.1),
// password policy, network/time access rules and security notifications.
//
// Audit-related settings are managed by the security auditor (audit:configure), all others by the security
// administrator (settings:manage), so nobody can both weaken auditing and act unaudited.
package security

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"regexp"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/platform/config"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/platform/db"
)

type Hours struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// Settings is the effective configuration (stored overrides on top of environment defaults).
type Settings struct {
	PasswordMinLength  int      `json:"password.minLength"`
	PasswordMaxAgeDays int      `json:"password.maxAgeDays"`
	PasswordHistory    int      `json:"password.history"`
	LockoutThreshold   int      `json:"lockout.threshold"`
	LockoutMinutes     int      `json:"lockout.minutes"`
	LockoutProgressive bool     `json:"lockout.progressive"`
	OTPTTLSec          int      `json:"otp.ttlSec"`
	OTPMaxAttempts     int      `json:"otp.maxAttempts"`
	MaxConsoleSessions int      `json:"sessions.maxConsole"`
	MaxAppSessions     int      `json:"sessions.maxApp"`
	SessionLimitAction string   `json:"sessions.limitAction"`
	ConsoleIdleMinutes int      `json:"sessions.consoleIdleMinutes"`
	AppIdleDays        int      `json:"sessions.appIdleDays"`
	BindIPConsole      bool     `json:"sessions.bindIpConsole"`
	BindIPApp          bool     `json:"sessions.bindIpApp"`
	BindUserAgent      bool     `json:"sessions.bindUserAgent"`
	ConsoleCIDRs       []string `json:"access.consoleCidrs"`
	ConsoleHours       Hours    `json:"access.consoleHours"`
	ConsoleDays        []int    `json:"access.consoleDays"`
	ShahkarReverifyDay int      `json:"shahkar.reverifyDays"`
	ShahkarOnNewDevice bool     `json:"shahkar.onNewDevice"`
	CitizenSignup      bool     `json:"app.citizenSignup"`
	ExportEnabled      bool     `json:"export.enabled"`
	ExportMaxRows      int      `json:"export.maxRows"`
	IntegrityCheckMin  int      `json:"integrity.checkMinutes"`
	AuditDisabled      []string `json:"audit.disabledEvents"`
	AuditWarnRows      int      `json:"audit.warnRows"`
	AuditMaxRows       int      `json:"audit.maxRows"`
	AuditFullAction    string   `json:"audit.fullAction"`
	Banner             string   `json:"access.banner"`
}

// Defaults derives the default settings from the environment.
func Defaults(c config.Config) Settings {
	return Settings{
		PasswordMinLength: c.PasswordMinLen, PasswordMaxAgeDays: c.PasswordMaxAge, PasswordHistory: 5,
		LockoutThreshold: c.LockoutThreshold, LockoutMinutes: c.LockoutMinutes, LockoutProgressive: true,
		OTPTTLSec: 120, OTPMaxAttempts: 5,
		MaxConsoleSessions: 1, MaxAppSessions: 2, SessionLimitAction: "replace",
		ConsoleIdleMinutes: 30, AppIdleDays: 14,
		BindIPConsole: true, BindIPApp: false, BindUserAgent: true,
		ConsoleCIDRs: []string{}, ConsoleHours: Hours{From: "00:00", To: "23:59"}, ConsoleDays: []int{0, 1, 2, 3, 4, 5, 6},
		ShahkarReverifyDay: c.ShahkarReverifyDays, ShahkarOnNewDevice: true, CitizenSignup: c.AppCitizenSignup,
		ExportEnabled: true, ExportMaxRows: 5000, IntegrityCheckMin: 60,
		AuditDisabled: []string{}, AuditWarnRows: 4_000_000, AuditMaxRows: 5_000_000, AuditFullAction: "block",
		Banner: "این سامانه متعلق به مدیریت بحران شهری است. هرگونه دسترسی غیرمجاز ممنوع است و تمامی فعالیت‌ها ثبت و پایش می‌شود.",
	}
}

// Titles are the Persian labels shown in the console.
var Titles = map[string]string{
	"password.minLength":          "حداقل طول رمز عبور",
	"password.maxAgeDays":         "حداکثر عمر رمز (روز، ۰ = نامحدود)",
	"password.history":            "تعداد رمزهای قبلی غیرقابل تکرار",
	"lockout.threshold":           "آستانه تلاش ناموفق احراز هویت",
	"lockout.minutes":             "مدت قفل حساب (دقیقه)",
	"lockout.progressive":         "دو برابر شدن مدت قفل در قفل‌های متوالی",
	"otp.ttlSec":                  "اعتبار کد یکبارمصرف (ثانیه)",
	"otp.maxAttempts":             "حداکثر تلاش برای هر کد یکبارمصرف",
	"sessions.maxConsole":         "حداکثر نشست همزمان کنسول برای هر کاربر",
	"sessions.maxApp":             "حداکثر نشست همزمان اپ برای هر کاربر",
	"sessions.limitAction":        "اقدام در صورت عبور از سقف نشست (replace: ابطال قدیمی‌ترین / deny: رد نشست جدید)",
	"sessions.consoleIdleMinutes": "خاتمه نشست کنسول پس از عدم فعالیت (دقیقه)",
	"sessions.appIdleDays":        "خاتمه نشست اپ پس از عدم فعالیت (روز)",
	"sessions.bindIpConsole":      "مقید بودن نشست کنسول به آدرس IP",
	"sessions.bindIpApp":          "مقید بودن نشست اپ به آدرس IP",
	"sessions.bindUserAgent":      "مقید بودن نشست به مرورگر/برنامه کاربر",
	"access.consoleCidrs":         "شبکه‌های مجاز برای ورود به کنسول (خالی = همه)",
	"access.consoleHours":         "بازه ساعت مجاز ورود به کنسول",
	"access.consoleDays":          "روزهای مجاز ورود به کنسول (۰ = یکشنبه … ۶ = شنبه)",
	"access.banner":               "پیام هشدار پیش از ورود",
	"shahkar.reverifyDays":        "دوره استعلام مجدد شاهکار (روز)",
	"shahkar.onNewDevice":         "استعلام شاهکار در ورود از دستگاه جدید",
	"app.citizenSignup":           "ثبت‌نام شهروند از اپلیکیشن",
	"export.enabled":              "امکان خروجی گرفتن از داده‌ها",
	"export.maxRows":              "حداکثر رکورد در هر خروجی",
	"integrity.checkMinutes":      "فاصله بررسی خودکار صحت داده‌ها (دقیقه)",
	"audit.disabledEvents":        "رویدادهای غیرضروری غیرفعال در ممیزی",
	"audit.warnRows":              "آستانه هشدار حجم ممیزی (تعداد رکورد)",
	"audit.maxRows":               "ظرفیت ممیزی (تعداد رکورد)",
	"audit.fullAction":            "اقدام هنگام پر شدن ممیزی (block: توقف فعالیت کاربران غیرمدیر / alert: فقط هشدار)",
}

// Keys returns all setting keys in a stable order.
func Keys() []string {
	out := make([]string, 0, len(Titles))
	for k := range Titles {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// IsAuditKey reports whether a setting belongs to the auditor.
func IsAuditKey(key string) bool { return len(key) > 6 && key[:6] == "audit." }

// SelectableEvents are the only audit events the auditor may switch off (FAU_SEL.1). Authentication, access
// control, identity/role/settings management, sessions, integrity and audit events are always recorded.
var SelectableEvents = []string{"incident.view", "report.view"}

var (
	hhmmRe = regexp.MustCompile(`^([01]\d|2[0-3]):[0-5]\d$`)
	ErrBad = fmt.Errorf("invalid setting")
)

type rangeRule struct{ min, max int }

var intRules = map[string]rangeRule{
	"password.minLength": {8, 64}, "password.maxAgeDays": {0, 365}, "password.history": {0, 24},
	"lockout.threshold": {3, 20}, "lockout.minutes": {1, 1440}, "otp.ttlSec": {60, 600}, "otp.maxAttempts": {1, 10},
	"sessions.maxConsole": {1, 5}, "sessions.maxApp": {1, 10}, "sessions.consoleIdleMinutes": {1, 240},
	"sessions.appIdleDays": {1, 90}, "shahkar.reverifyDays": {1, 730}, "export.maxRows": {1, 100000},
	"integrity.checkMinutes": {5, 1440}, "audit.warnRows": {1000, 1 << 40}, "audit.maxRows": {1000, 1 << 40},
}

// Validate checks one value against the setting's rule; it returns a Persian message on failure.
func Validate(key string, raw json.RawMessage) (any, string) {
	if _, ok := Titles[key]; !ok {
		return nil, "تنظیم ناشناخته"
	}
	if r, ok := intRules[key]; ok {
		var n int
		if json.Unmarshal(raw, &n) != nil || n < r.min || n > r.max {
			return nil, fmt.Sprintf("عدد صحیح بین %d و %d", r.min, r.max)
		}
		return n, ""
	}
	switch key {
	case "lockout.progressive", "sessions.bindIpConsole", "sessions.bindIpApp", "sessions.bindUserAgent",
		"shahkar.onNewDevice", "app.citizenSignup", "export.enabled":
		var b bool
		if json.Unmarshal(raw, &b) != nil {
			return nil, "مقدار بله/خیر"
		}
		return b, ""
	case "sessions.limitAction":
		var s string
		if json.Unmarshal(raw, &s) != nil || (s != "replace" && s != "deny") {
			return nil, "replace یا deny"
		}
		return s, ""
	case "audit.fullAction":
		var s string
		if json.Unmarshal(raw, &s) != nil || (s != "block" && s != "alert") {
			return nil, "block یا alert"
		}
		return s, ""
	case "access.consoleCidrs":
		var list []string
		if json.Unmarshal(raw, &list) != nil || len(list) > 100 {
			return nil, "فهرست شبکه‌ها"
		}
		for _, c := range list {
			if !ValidCIDR(c) {
				return nil, "آدرس/شبکه IP نامعتبر: " + c
			}
		}
		return list, ""
	case "access.consoleHours":
		var h Hours
		if json.Unmarshal(raw, &h) != nil || !hhmmRe.MatchString(h.From) || !hhmmRe.MatchString(h.To) {
			return nil, "قالب ساعت HH:MM"
		}
		return h, ""
	case "access.consoleDays":
		var d []int
		if json.Unmarshal(raw, &d) != nil || len(d) == 0 || len(d) > 7 {
			return nil, "حداقل یک روز"
		}
		for _, x := range d {
			if x < 0 || x > 6 {
				return nil, "روز بین ۰ و ۶"
			}
		}
		return d, ""
	case "access.banner":
		var s string
		if json.Unmarshal(raw, &s) != nil || len([]rune(s)) > 1000 {
			return nil, "حداکثر ۱۰۰۰ نویسه"
		}
		return s, ""
	case "audit.disabledEvents":
		var list []string
		if json.Unmarshal(raw, &list) != nil {
			return nil, "فهرست رویدادها"
		}
		for _, e := range list {
			if !slices.Contains(SelectableEvents, e) {
				return nil, "این رویداد قابل غیرفعال‌سازی نیست: " + e
			}
		}
		return list, ""
	}
	return nil, "تنظیم ناشناخته"
}

// ValidCIDR accepts an IP address or a CIDR network.
func ValidCIDR(s string) bool {
	if net.ParseIP(s) != nil {
		return true
	}
	_, _, err := net.ParseCIDR(s)
	return err == nil
}

// IPAllowed reports whether ip is inside one of cidrs (empty list = allowed).
func IPAllowed(ip string, cidrs []string) bool {
	if len(cidrs) == 0 {
		return true
	}
	addr := net.ParseIP(ip)
	if addr == nil {
		return false
	}
	for _, c := range cidrs {
		if one := net.ParseIP(c); one != nil {
			if one.Equal(addr) {
				return true
			}
			continue
		}
		if _, n, err := net.ParseCIDR(c); err == nil && n.Contains(addr) {
			return true
		}
	}
	return false
}

// Store reads and writes settings with a short cache.
type Store struct {
	Pool     db.DBTX
	Defaults Settings

	mu    sync.Mutex
	at    time.Time
	value *Settings
}

func (s *Store) Invalidate() {
	s.mu.Lock()
	s.value = nil
	s.mu.Unlock()
}

// Get returns the effective settings.
func (s *Store) Get(ctx context.Context) (Settings, error) {
	s.mu.Lock()
	if s.value != nil && time.Since(s.at) < 2*time.Second {
		v := *s.value
		s.mu.Unlock()
		return v, nil
	}
	s.mu.Unlock()
	merged := map[string]json.RawMessage{}
	base, _ := json.Marshal(s.Defaults)
	_ = json.Unmarshal(base, &merged)
	rows, err := s.Pool.Query(ctx, `SELECT key, value::text FROM security_settings`)
	if err != nil {
		return s.Defaults, err
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return s.Defaults, err
		}
		if _, msg := Validate(k, json.RawMessage(v)); msg == "" {
			merged[k] = json.RawMessage(v)
		}
	}
	if err := rows.Err(); err != nil {
		return s.Defaults, err
	}
	b, _ := json.Marshal(merged)
	var out Settings
	if err := json.Unmarshal(b, &out); err != nil {
		return s.Defaults, err
	}
	s.mu.Lock()
	s.value, s.at = &out, time.Now()
	s.mu.Unlock()
	return out, nil
}

// AsMap returns the settings keyed by setting name.
func (st Settings) AsMap() map[string]any {
	b, _ := json.Marshal(st)
	out := map[string]any{}
	_ = json.Unmarshal(b, &out)
	return out
}

// Set validates and stores one setting inside tx, returning the previous and new values.
func (s *Store) Set(ctx context.Context, tx db.DBTX, key string, raw json.RawMessage, actor uuid.UUID) (before, after any, msg string) {
	v, msg := Validate(key, raw)
	if msg != "" {
		return nil, nil, msg
	}
	cur, err := s.Get(ctx)
	if err != nil {
		return nil, nil, "خطا در خواندن تنظیمات"
	}
	if key == "audit.warnRows" && v.(int) >= cur.AuditMaxRows {
		return nil, nil, "آستانه هشدار باید کمتر از ظرفیت باشد"
	}
	if key == "audit.maxRows" && v.(int) <= cur.AuditWarnRows {
		return nil, nil, "ظرفیت باید بیشتر از آستانه هشدار باشد"
	}
	stored, _ := json.Marshal(v)
	if _, err := tx.Exec(ctx, `INSERT INTO security_settings (key, value, updated_by) VALUES ($1, $2::jsonb, $3)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_by = EXCLUDED.updated_by, updated_at = now()`,
		key, string(stored), actor); err != nil {
		return nil, nil, "خطا در ذخیره تنظیم"
	}
	s.Invalidate()
	return cur.AsMap()[key], v, ""
}
