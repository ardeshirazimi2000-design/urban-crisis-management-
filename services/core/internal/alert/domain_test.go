package alert

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestAggregateStatus(t *testing.T) {
	cases := []struct {
		in   map[string]string
		want string
	}{
		{map[string]string{"push": "accepted", "sms": "delivered"}, Sent},
		{map[string]string{"push": "accepted", "sms": "failed"}, PartiallySent},
		{map[string]string{"push": "rejected", "sms": "failed"}, Failed},
		{map[string]string{"push": "accepted", "sms": "unknown"}, Sending},
		{map[string]string{"push": "pending"}, Sending},
	}
	for _, c := range cases {
		if got := AggregateStatus(c.in); got != c.want {
			t.Errorf("%v: want %s got %s", c.in, c.want, got)
		}
	}
}

func TestRenderMarksTestMessages(t *testing.T) {
	tpl, ok := FindTemplate("earthquake_aftershock_advisory", 1)
	if !ok {
		t.Fatal("template missing")
	}
	exp := time.Date(2026, 10, 8, 20, 0, 0, 0, time.UTC)
	test := Render(tpl, "test", "مرکز", "منطقه ۶", exp, map[string]string{"instructions": "در فضای باز بمانید."})
	if !strings.HasPrefix(test, "«آزمایشی»") {
		t.Fatalf("test message must be visibly marked: %s", test)
	}
	op := Render(tpl, "operational", "مرکز", "منطقه ۶", exp, map[string]string{"instructions": "x"})
	if strings.Contains(op, "آزمایشی") || strings.Contains(op, "{{") {
		t.Fatalf("bad operational rendering: %s", op)
	}
	if !strings.Contains(op, "23:30") { // 20:00 UTC = 23:30 Tehran
		t.Fatalf("expiry must be shown in Tehran time: %s", op)
	}
}

func TestCreateValidation(t *testing.T) {
	now := time.Now()
	r := CreateRequest{Mode: "operational", Severity: "warning", IssuerOrgID: uuid.New(), Region: map[string]any{"type": "Polygon"},
		RegionLabel: "x", TemplateCode: "evacuation_order", TemplateVersion: 1, Params: map[string]string{"instructions": "y"},
		Channels: []string{"sms"}, ExpiresAt: now.Add(2 * time.Hour)}
	if err := r.Validate(now, 72*time.Hour); err != nil {
		t.Fatal(err)
	}
	r.ExpiresAt = now.Add(100 * time.Hour)
	if r.Validate(now, 72*time.Hour) == nil {
		t.Fatal("expiry beyond max validity must fail")
	}
	r.ExpiresAt = now.Add(2 * time.Hour)
	r.Params = map[string]string{}
	if r.Validate(now, 72*time.Hour) == nil {
		t.Fatal("missing template param must fail")
	}
	r.Params = map[string]string{"instructions": "y", "evil": "z"}
	if r.Validate(now, 72*time.Hour) == nil {
		t.Fatal("unknown template param must fail")
	}
}

func TestChannelLength(t *testing.T) {
	long := strings.Repeat("ب", 400)
	if CheckChannelLengths(long, []string{"sms"}) == nil {
		t.Fatal("sms limit must be enforced")
	}
	if CheckChannelLengths(long, []string{"push"}) != nil {
		t.Fatal("push allows 400")
	}
}
