// Package itest holds integration/acceptance tests (AT-01..AT-10 of the detailed design) against a real
// PostgreSQL+PostGIS. They run when TEST_DATABASE_URL is set; the database is reset on start.
package itest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/app"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/auth"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/media"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/platform/config"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/platform/db"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/migrations"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/seeds"
)

const devSecret = "integration-test-secret-0123456789abcdef"

var (
	pool   *pgxpool.Pool
	server *httptest.Server
	cfg    config.Config
	seq    atomic.Int64
)

func TestMain(m *testing.M) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		fmt.Println("TEST_DATABASE_URL not set; skipping integration tests")
		os.Exit(0)
	}
	ctx := context.Background()
	var err error
	pool, err = db.Connect(ctx, url)
	if err != nil {
		fmt.Println("connect:", err)
		os.Exit(1)
	}
	if _, err := pool.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public;`); err != nil {
		fmt.Println("reset:", err)
		os.Exit(1)
	}
	if _, err := db.Migrate(ctx, pool, migrations.FS); err != nil {
		fmt.Println("migrate:", err)
		os.Exit(1)
	}
	if _, err := pool.Exec(ctx, seeds.Dev); err != nil {
		fmt.Println("seed:", err)
		os.Exit(1)
	}
	for k, v := range map[string]string{"APP_ENV": "test", "AUTH_MODE": "dev", "DEV_JWT_SECRET": devSecret,
		"MEDIA_URL_SECRET": "integration-media-secret-0123456789abcdef", "MEDIA_ALLOW_UNSCANNED": "true",
		"RATE_REPORT_PER_MIN": "100000", "RATE_REPORT_BURST": "100000", "RATE_DEFAULT_PER_MIN": "1000000", "RATE_DEFAULT_BURST": "100000",
		"PRINCIPAL_CACHE_TTL": "1ms"} {
		os.Setenv(k, v)
	}
	cfg, err = config.Load()
	if err != nil {
		fmt.Println("config:", err)
		os.Exit(1)
	}
	dir, _ := os.MkdirTemp("", "crisis-media")
	cfg.ObjectStoreDir = dir
	verifier := auth.DevVerifier{Secret: []byte(devSecret), Audience: cfg.OIDCAudience}
	fs := &media.FSStore{Dir: dir, Secret: []byte(cfg.MediaURLSecret)}
	a := app.New(cfg, pool, verifier, fs)
	server = httptest.NewServer(a.Handler)
	fs.BaseURL = server.URL + "/api/v1/media/blob"
	code := m.Run()
	server.Close()
	pool.Close()
	os.RemoveAll(dir)
	os.Exit(code)
}

// ---------------------------------------------------------------------------
// HTTP helpers
// ---------------------------------------------------------------------------

type client struct {
	t     *testing.T
	token string
	id    uuid.UUID
}

type grant struct {
	Role    string `json:"role"`
	OrgCode string `json:"org_code,omitempty"`
}

// login creates a unique dev user with the given grants.
func login(t *testing.T, grants ...grant) *client {
	t.Helper()
	sub := fmt.Sprintf("it-%d-%d", time.Now().UnixNano()%1e9, seq.Add(1))
	body, _ := json.Marshal(map[string]any{"subject": sub, "name": sub, "grants": grants})
	resp, err := http.Post(server.URL+"/api/v1/dev/token", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tok); err != nil || tok.AccessToken == "" {
		t.Fatalf("login failed: %v", err)
	}
	c := &client{t: t, token: tok.AccessToken}
	var me struct {
		UserID uuid.UUID `json:"user_id"`
	}
	c.do("GET", "/me", nil, nil, &me, 200)
	c.id = me.UserID
	return c
}

type result struct {
	Status int
	Body   []byte
	Header http.Header
}

func (r result) json(t *testing.T, v any) {
	t.Helper()
	if err := json.Unmarshal(r.Body, v); err != nil {
		t.Fatalf("decode %s: %v", r.Body, err)
	}
}

func (r result) errCode() string {
	var e struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(r.Body, &e)
	return e.Error.Code
}

func (c *client) raw(method, path string, body any, headers map[string]string) result {
	c.t.Helper()
	var rd io.Reader
	if b, ok := body.([]byte); ok {
		rd = bytes.NewReader(b)
	} else if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, server.URL+"/api/v1"+path, rd)
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return result{Status: resp.StatusCode, Body: b, Header: resp.Header}
}

// do performs a request and asserts the status; out may be nil.
func (c *client) do(method, path string, body any, headers map[string]string, out any, want int) result {
	c.t.Helper()
	r := c.raw(method, path, body, headers)
	if r.Status != want {
		c.t.Fatalf("%s %s: want %d got %d: %s", method, path, want, r.Status, r.Body)
	}
	if out != nil {
		r.json(c.t, out)
	}
	return r
}

func idem() map[string]string { return map[string]string{"Idempotency-Key": "k-" + uuid.NewString()} }

func orgID(t *testing.T, code string) uuid.UUID {
	var id uuid.UUID
	if err := pool.QueryRow(context.Background(), `SELECT id FROM organizations WHERE code=$1`, code).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func reportBody(lat, lng float64) map[string]any {
	return map[string]any{"type": "structural_damage", "description": "ترک در دیوار",
		"location": map[string]any{"lat": lat, "lng": lng, "accuracy_m": 15}}
}

func createAcceptedReport(t *testing.T, citizen, operator *client) uuid.UUID {
	var created struct {
		ReportID uuid.UUID `json:"report_id"`
	}
	citizen.do("POST", "/reports", reportBody(35.71, 51.41), idem(), &created, 202)
	operator.do("POST", "/reports/"+created.ReportID.String()+"/review",
		map[string]any{"decision": "accepted", "version": 1, "reason": "ok"}, nil, nil, 200)
	return created.ReportID
}

type incidentResp struct {
	ID      uuid.UUID `json:"id"`
	Status  string    `json:"status"`
	Version int       `json:"version"`
}

func createIncident(t *testing.T, c *client, org string, status string) incidentResp {
	var in incidentResp
	c.do("POST", "/incidents", map[string]any{"type": "earthquake", "title": "حادثه آزمون", "severity": "high",
		"owner_org_id": orgID(t, org), "status": status, "location": map[string]any{"lat": 35.7, "lng": 51.4}}, idem(), &in, 201)
	return in
}

func countAudit(t *testing.T, action, outcome, target string) int {
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_log WHERE action=$1 AND outcome=$2 AND target_id=$3`,
		action, outcome, target).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }
