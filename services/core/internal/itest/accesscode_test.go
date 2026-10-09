package itest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/admin"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/auth"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/platform/httpx"
)

// Test server reachable from the Internet: dev sign-in requires an access code, and the citizen code
// (shipped inside the test APK) can never yield staff roles or take over a staff account.
func TestDevTokenAccessCodes(t *testing.T) {
	mux := http.NewServeMux()
	router := &httpx.Router{Mux: mux}
	(&admin.DevTokens{Pool: pool, Secret: []byte(devSecret), Audience: cfg.OIDCAudience, Resolver: &auth.Resolver{Pool: pool, TTL: cfg.PrincipalTTL},
		StaffCode: "staff-code-123", CitizenCode: "citizen-code-456", Limiter: httpx.NewRateLimiter(60, 100)}).Routes(router)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	post := func(body map[string]any) int {
		b, _ := json.Marshal(body)
		resp, err := http.Post(srv.URL+"/dev/token", "application/json", bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	n := seq.Add(1)
	staff := fmt.Sprintf("ac-op-%d", n)
	citizen := fmt.Sprintf("citizen-ac-%d", n)
	admin := []map[string]string{{"role": "SECURITY_ADMIN"}}
	cases := []struct {
		name string
		body map[string]any
		want int
	}{
		{"no code", map[string]any{"subject": staff, "grants": admin}, 401},
		{"wrong code", map[string]any{"subject": staff, "grants": admin, "access_code": "guess-guess"}, 401},
		{"citizen code asks for staff role", map[string]any{"subject": citizen, "grants": admin, "access_code": "citizen-code-456"}, 403},
		{"citizen code on non-citizen subject", map[string]any{"subject": staff, "access_code": "citizen-code-456"}, 403},
		{"staff code", map[string]any{"subject": staff, "grants": admin, "access_code": " staff-code-123 "}, 200},
		{"citizen code", map[string]any{"subject": citizen, "access_code": "citizen-code-456"}, 200},
		{"citizen code cannot demote a staff account", map[string]any{"subject": "citizen-x" + staff, "grants": admin, "access_code": "staff-code-123"}, 200},
		{"...even with a citizen- subject", map[string]any{"subject": "citizen-x" + staff, "access_code": "citizen-code-456"}, 403},
	}
	for _, c := range cases {
		if got := post(c.body); got != c.want {
			t.Errorf("%s: status %d, want %d", c.name, got, c.want)
		}
	}
}
