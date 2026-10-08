package logx

import (
	"strings"
	"testing"
)

func TestRedact(t *testing.T) {
	s := Redact("auth=Bearer eyJhbGciOi.abc.def phone 09121234567")
	if strings.Contains(s, "eyJ") || strings.Contains(s, "09121234567") {
		t.Fatalf("not redacted: %s", s)
	}
}
