// Package apiir is the client of the api.ir gateway: Shahkar (national code ↔ mobile owner match), SMS OTP and
// service SMS. Contract (servers: https://s.api.ir, Bearer auth):
//
//	POST /api/sw1/Shahkar | ShahkarPro  {nationalCode, mobile, isCompany} → ResultDataOfboolean
//	POST /api/sw1/ShahkarLite           {nationalCode, mobile}            → ResultDataOfboolean
//	POST /api/sw1/SmsOTP                {code, mobile, template}          → ResultDataOfboolean
//	POST /api/sw1/SendSms               {message, mobiles[]}              → ResultDataOfboolean
//	ResultDataOfboolean = {data: bool, success: bool, code: int, message: string|null}
package apiir

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

type ShahkarResult struct {
	OK       bool // the provider answered; false = network/provider failure
	Matched  bool // mobile is registered to the national code (valid only when OK)
	Endpoint string
	Code     string
	Message  string
	Duration time.Duration
}

type Client interface {
	Shahkar(ctx context.Context, nationalCode, mobile string) ShahkarResult
	SendOTP(ctx context.Context, mobile, code string) error
	SendSMS(ctx context.Context, mobiles []string, message string) error
}

type result struct {
	Data    *bool           `json:"data"`
	Success bool            `json:"success"`
	Code    json.RawMessage `json:"code"`
	Message *string         `json:"message"`
}

// Live calls api.ir.
type Live struct {
	BaseURL  string
	Token    string
	Endpoint string // Shahkar | ShahkarPro | ShahkarLite
	Template int
	HTTP     *http.Client
}

func NewLive(baseURL, token, endpoint string, template int, timeout time.Duration) *Live {
	return &Live{BaseURL: strings.TrimRight(baseURL, "/"), Token: token, Endpoint: endpoint, Template: template,
		HTTP: &http.Client{Timeout: timeout}}
}

func (l *Live) post(ctx context.Context, path string, body any) (result, error) {
	var r result
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, l.BaseURL+path, bytes.NewReader(b))
	if err != nil {
		return r, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+l.Token)
	resp, err := l.HTTP.Do(req)
	if err != nil {
		return r, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return r, fmt.Errorf("api.ir HTTP %d", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return r, fmt.Errorf("api.ir decode: %w", err)
	}
	return r, nil
}

func (r result) code() string { return strings.Trim(string(r.Code), `"`) }

func (r result) message() string {
	if r.Message == nil {
		return ""
	}
	return *r.Message
}

func (l *Live) Shahkar(ctx context.Context, nationalCode, mobile string) ShahkarResult {
	body := map[string]any{"nationalCode": nationalCode, "mobile": mobile}
	if l.Endpoint != "ShahkarLite" {
		body["isCompany"] = false
	}
	start := time.Now()
	r, err := l.post(ctx, "/api/sw1/"+l.Endpoint, body)
	out := ShahkarResult{Endpoint: l.Endpoint, Duration: time.Since(start)}
	if err != nil {
		slog.WarnContext(ctx, "shahkar call failed", "err", err.Error(), "endpoint", l.Endpoint)
		out.Message = err.Error()
		return out
	}
	out.OK, out.Code, out.Message = r.Success, r.code(), r.message()
	out.Matched = r.Success && r.Data != nil && *r.Data
	return out
}

func (l *Live) SendOTP(ctx context.Context, mobile, code string) error {
	r, err := l.post(ctx, "/api/sw1/SmsOTP", map[string]any{"code": code, "mobile": mobile, "template": l.Template})
	if err != nil {
		return err
	}
	if !r.Success || (r.Data != nil && !*r.Data) {
		return fmt.Errorf("api.ir SmsOTP rejected: %s %s", r.code(), r.message())
	}
	return nil
}

func (l *Live) SendSMS(ctx context.Context, mobiles []string, message string) error {
	r, err := l.post(ctx, "/api/sw1/SendSms", map[string]any{"message": message, "mobiles": mobiles})
	if err != nil {
		return err
	}
	if !r.Success {
		return fmt.Errorf("api.ir SendSms rejected: %s %s", r.code(), r.message())
	}
	return nil
}

// Mock is the offline simulator for development and tests (refused outside local/test by configuration).
// Shahkar matches unless the pair was registered as a mismatch or the mobile ends with 9999; codes are kept
// in memory (and logged when Log is set) instead of being delivered.
type Mock struct {
	Log bool

	mu          sync.Mutex
	sent        map[string]string
	mismatches  map[string]bool
	down        bool
	ShahkarCall int
	SMS         []string
}

func NewMock(log bool) *Mock {
	return &Mock{Log: log, sent: map[string]string{}, mismatches: map[string]bool{}}
}

func (m *Mock) Shahkar(_ context.Context, nationalCode, mobile string) ShahkarResult {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ShahkarCall++
	if m.down {
		return ShahkarResult{Endpoint: "mock", Message: "simulated outage"}
	}
	matched := !m.mismatches[nationalCode+":"+mobile] && !strings.HasSuffix(mobile, "9999")
	return ShahkarResult{OK: true, Matched: matched, Endpoint: "mock", Code: "0"}
}

func (m *Mock) SendOTP(ctx context.Context, mobile, code string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.down {
		return fmt.Errorf("simulated outage")
	}
	m.sent[mobile] = code
	if m.Log {
		slog.InfoContext(ctx, "[mock SMS] OTP", "mobile", mobile, "code", code)
	}
	return nil
}

func (m *Mock) SendSMS(_ context.Context, mobiles []string, message string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.SMS = append(m.SMS, strings.Join(mobiles, ",")+": "+message)
	return nil
}

// LastCode returns the last code "sent" to mobile.
func (m *Mock) LastCode(mobile string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sent[mobile]
}

// SetMismatch makes Shahkar report that mobile is not registered to nationalCode.
func (m *Mock) SetMismatch(nationalCode, mobile string, on bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if on {
		m.mismatches[nationalCode+":"+mobile] = true
	} else {
		delete(m.mismatches, nationalCode+":"+mobile)
	}
}

// SetDown simulates a provider outage.
func (m *Mock) SetDown(on bool) {
	m.mu.Lock()
	m.down = on
	m.mu.Unlock()
}
