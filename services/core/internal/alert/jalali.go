package alert

import (
	"fmt"
	"time"
)

// toJalali converts a Gregorian date to the Solar Hijri (Jalali) calendar used in official Iranian notices.
// Algorithm: the widely used arithmetic conversion (valid for the modern era).
func toJalali(gy, gm, gd int) (jy, jm, jd int) {
	gdm := [12]int{0, 31, 59, 90, 120, 151, 181, 212, 243, 273, 304, 334}
	gy2 := gy
	if gm > 2 {
		gy2 = gy + 1
	}
	days := 355666 + 365*gy + (gy2+3)/4 - (gy2+99)/100 + (gy2+399)/400 + gd + gdm[gm-1]
	jy = -1595 + 33*(days/12053)
	days %= 12053
	jy += 4 * (days / 1461)
	days %= 1461
	if days > 365 {
		jy += (days - 1) / 365
		days = (days - 1) % 365
	}
	if days < 186 {
		jm = 1 + days/31
		jd = 1 + days%31
	} else {
		jm = 7 + (days-186)/30
		jd = 1 + (days-186)%30
	}
	return
}

var persianDigits = []rune("۰۱۲۳۴۵۶۷۸۹")

func faDigits(s string) string {
	out := []rune(s)
	for i, r := range out {
		if r >= '0' && r <= '9' {
			out[i] = persianDigits[r-'0']
		}
	}
	return string(out)
}

// FormatTehran renders a timestamp as "۱۴۰۵/۰۷/۱۷ ساعت ۰۶:۲۸" in Tehran local time.
func FormatTehran(t time.Time) string {
	lt := t.In(tehran)
	jy, jm, jd := toJalali(lt.Year(), int(lt.Month()), lt.Day())
	return faDigits(fmt.Sprintf("%04d/%02d/%02d ساعت %02d:%02d", jy, jm, jd, lt.Hour(), lt.Minute()))
}
