package security

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"math/big"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"golang.org/x/crypto/argon2"

	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/platform/db"
)

// Argon2id parameters (RFC 9106 second recommended option).
const (
	argonTime    = 3
	argonMemory  = 64 * 1024
	argonThreads = 2
	argonKeyLen  = 32
)

// HashPassword returns an encoded argon2id hash.
func HashPassword(pw string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(pw), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

var dummyHash, _ = HashPassword("dummy-password-for-timing")

// VerifyPassword compares in constant time. An empty hash is compared against a dummy so timing does not
// reveal whether a username exists.
func VerifyPassword(pw, encoded string) bool {
	ok := true
	if encoded == "" {
		encoded, ok = dummyHash, false
	}
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	var m, t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil {
		return false
	}
	salt, err1 := base64.RawStdEncoding.DecodeString(parts[4])
	want, err2 := base64.RawStdEncoding.DecodeString(parts[5])
	if err1 != nil || err2 != nil {
		return false
	}
	got := argon2.IDKey([]byte(pw), salt, t, m, p, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1 && ok
}

// PasswordProblems lists policy violations (empty = acceptable).
func PasswordProblems(pw string, minLen int, personal ...string) []string {
	var out []string
	n := len([]rune(pw))
	if n < minLen {
		out = append(out, fmt.Sprintf("حداقل %d کاراکتر", minLen))
	}
	if n > 128 {
		out = append(out, "حداکثر ۱۲۸ کاراکتر")
	}
	var lower, upper, digit, symbol bool
	for _, r := range pw {
		switch {
		case r >= 'a' && r <= 'z':
			lower = true
		case r >= 'A' && r <= 'Z':
			upper = true
		case unicode.IsDigit(r):
			digit = true
		default:
			symbol = true
		}
	}
	if !lower || !upper {
		out = append(out, "حروف کوچک و بزرگ لاتین")
	}
	if !digit {
		out = append(out, "حداقل یک رقم")
	}
	if !symbol {
		out = append(out, "حداقل یک نماد")
	}
	low := strings.ToLower(pw)
	for _, v := range personal {
		if len(v) >= 4 && strings.Contains(low, strings.ToLower(v)) {
			out = append(out, "نباید شامل نام کاربری، کد ملی یا موبایل باشد")
			break
		}
	}
	return out
}

// Characters that cannot be confused when read aloud or typed by hand (no 0/O, 1/l/I).
const tempAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZabcdefghjkmnpqrstuvwxyz23456789"

// TemporaryPassword is issued by the user administrator; it must be changed at first console sign-in.
func TemporaryPassword() string {
	var b strings.Builder
	b.WriteString("Tmp-")
	for range 12 {
		b.WriteByte(tempAlphabet[RandomInt(len(tempAlphabet))])
	}
	fmt.Fprintf(&b, "#%d", 10+RandomInt(90))
	return b.String()
}

// RandomInt returns a uniform random integer in [0, n).
func RandomInt(n int) int {
	v, err := rand.Int(rand.Reader, big.NewInt(int64(n)))
	if err != nil {
		panic(err)
	}
	return int(v.Int64())
}

// RandomDigits returns n random decimal digits.
func RandomDigits(n int) string {
	var b strings.Builder
	for range n {
		b.WriteByte(byte('0' + RandomInt(10)))
	}
	return b.String()
}

// ErrReused is returned when a new password repeats a recent one.
var ErrReused = fmt.Errorf("password reused")

// SetPassword checks reuse against the last `history` passwords, stores the new hash and records it.
func SetPassword(ctx context.Context, tx db.DBTX, userID uuid.UUID, pw string, mustChange bool, history int) error {
	if history > 0 {
		rows, err := tx.Query(ctx, `SELECT password_hash FROM password_history WHERE user_id=$1 ORDER BY created_at DESC LIMIT $2`, userID, history)
		if err != nil {
			return err
		}
		var prev []string
		for rows.Next() {
			var h string
			if err := rows.Scan(&h); err != nil {
				rows.Close()
				return err
			}
			prev = append(prev, h)
		}
		rows.Close()
		for _, h := range prev {
			if VerifyPassword(pw, h) {
				return ErrReused
			}
		}
	}
	hash, err := HashPassword(pw)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE users SET password_hash=$2, password_changed_at=now(), must_change_password=$3, updated_at=now() WHERE id=$1`,
		userID, hash, mustChange); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO password_history (user_id, password_hash) VALUES ($1,$2)`, userID, hash)
	return err
}

// PasswordExpired reports whether the password is older than the policy allows.
func PasswordExpired(changedAt *time.Time, maxAgeDays int) bool {
	return maxAgeDays > 0 && changedAt != nil && time.Since(*changedAt) > time.Duration(maxAgeDays)*24*time.Hour
}

// LocalClock returns the day of week (0 = Sunday) and HH:MM in loc.
func LocalClock(now time.Time, loc *time.Location) (int, string) {
	t := now.In(loc)
	return int(t.Weekday()), t.Format("15:04")
}

// InWindow reports whether hhmm lies in [from, to]; windows may wrap past midnight.
func InWindow(hhmm string, h Hours) bool {
	if h.From <= h.To {
		return hhmm >= h.From && hhmm <= h.To
	}
	return hhmm >= h.From || hhmm <= h.To
}

// MaskMobile hides the middle digits: 0912***4567.
func MaskMobile(m string) string {
	if len(m) < 8 {
		return "***"
	}
	return m[:4] + "***" + m[len(m)-4:]
}

// ValidNationalCode checks the Iranian national code check digit.
func ValidNationalCode(nc string) bool {
	if len(nc) != 10 {
		return false
	}
	allSame := true
	sum := 0
	for i, r := range nc {
		if r < '0' || r > '9' {
			return false
		}
		if nc[i] != nc[0] {
			allSame = false
		}
		if i < 9 {
			sum += int(r-'0') * (10 - i)
		}
	}
	if allSame {
		return false
	}
	rem := sum % 11
	check := int(nc[9] - '0')
	return (rem < 2 && check == rem) || (rem >= 2 && check == 11-rem)
}

// NormalizeDigits maps Persian/Arabic digits to ASCII and removes spaces and invisible marks.
func NormalizeDigits(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= '۰' && r <= '۹':
			b.WriteRune('0' + (r - '۰'))
		case r >= '٠' && r <= '٩':
			b.WriteRune('0' + (r - '٠'))
		case unicode.IsSpace(r) || r == '‌' || r == '‎' || r == '‏' || r == '-':
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// NormalizeMobile returns 09xxxxxxxxx or "" when invalid.
func NormalizeMobile(s string) string {
	m := NormalizeDigits(s)
	switch {
	case strings.HasPrefix(m, "+98"):
		m = "0" + m[3:]
	case strings.HasPrefix(m, "0098"):
		m = "0" + m[4:]
	case strings.HasPrefix(m, "98") && len(m) == 12:
		m = "0" + m[2:]
	case strings.HasPrefix(m, "9") && len(m) == 10:
		m = "0" + m
	}
	if len(m) != 11 || !strings.HasPrefix(m, "09") {
		return ""
	}
	for _, r := range m {
		if r < '0' || r > '9' {
			return ""
		}
	}
	return m
}
