// Package alert owns the public-warning workflow: draft, preview, submit, approve (four-eyes),
// dispatch (idempotent), cancel and expiry. AI never issues alerts (ADR-004/ADR-005).
package alert

import (
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/platform/httpx"
)

const (
	Draft           = "draft"
	PendingApproval = "pending_approval"
	Approved        = "approved"
	Sending         = "sending"
	PartiallySent   = "partially_sent"
	Sent            = "sent"
	Failed          = "failed"
	Cancelled       = "cancelled"
	Expired         = "expired"
)

var Statuses = []string{Draft, PendingApproval, Approved, Sending, PartiallySent, Sent, Failed, Cancelled, Expired}
var Severities = []string{"advisory", "watch", "warning", "emergency"}
var Channels = []string{"internal", "push", "sms", "cell_broadcast"}

// ChannelMaxRunes is the rendered-text limit per channel; drafts exceeding it are rejected at validation.
var ChannelMaxRunes = map[string]int{"internal": 4000, "push": 1000, "sms": 335, "cell_broadcast": 1395}

// CanCancel lists statuses from which an alert can be cancelled. A fully sent alert needs a correction alert instead.
func CanCancel(status string) bool {
	return slices.Contains([]string{Draft, PendingApproval, Approved, Sending}, status)
}

// Template is a versioned, approved message template. Changing wording means a new version (D-10).
type Template struct {
	Code    string
	Version int
	Title   string
	Body    string // placeholders: {{region}} {{issuer}} {{expires}} {{instructions}} {{mode_prefix}}
	Params  []string
}

var Templates = []Template{
	{Code: "earthquake_aftershock_advisory", Version: 1, Title: "توصیه ایمنی پس‌لرزه",
		Body:   "{{mode_prefix}}[{{issuer}}] احتمال وقوع پس‌لرزه در {{region}}. از ساختمان‌های آسیب‌دیده فاصله بگیرید. {{instructions}} اعتبار تا {{expires}}.",
		Params: []string{"instructions"}},
	{Code: "evacuation_order", Version: 1, Title: "دستور تخلیه",
		Body:   "{{mode_prefix}}[{{issuer}}] دستور تخلیه برای {{region}}. {{instructions}} به نزدیک‌ترین محل اسکان اضطراری مراجعه کنید. اعتبار تا {{expires}}.",
		Params: []string{"instructions"}},
	{Code: "shelter_info", Version: 1, Title: "اطلاع‌رسانی اسکان",
		Body:   "{{mode_prefix}}[{{issuer}}] محل اسکان اضطراری برای ساکنان {{region}}: {{shelter}}. {{instructions}} اعتبار تا {{expires}}.",
		Params: []string{"shelter", "instructions"}},
	{Code: "road_closure", Version: 1, Title: "انسداد مسیر",
		Body:   "{{mode_prefix}}[{{issuer}}] مسیر {{road}} در محدوده {{region}} مسدود است. {{instructions}} اعتبار تا {{expires}}.",
		Params: []string{"road", "instructions"}},
	{Code: "correction", Version: 1, Title: "اصلاحیه",
		Body:   "{{mode_prefix}}[{{issuer}}] اصلاحیه پیام قبلی برای {{region}}: {{instructions}} اعتبار تا {{expires}}.",
		Params: []string{"instructions"}},
	{Code: "system_test", Version: 1, Title: "پیام آزمایشی",
		Body:   "{{mode_prefix}}[{{issuer}}] این یک پیام آزمایشی سامانه هشدار برای {{region}} است و نیاز به اقدام ندارد.",
		Params: nil},
}

func FindTemplate(code string, version int) (Template, bool) {
	for _, t := range Templates {
		if t.Code == code && t.Version == version {
			return t, true
		}
	}
	return Template{}, false
}

var tehran = func() *time.Location {
	loc, err := time.LoadLocation("Asia/Tehran")
	if err != nil {
		return time.FixedZone("IRST", 3*3600+1800)
	}
	return loc
}()

// Render produces the exact text that will be sent. Test-mode messages are always prefixed so they
// cannot be mistaken for operational warnings.
func Render(t Template, mode, issuer, region string, expires time.Time, params map[string]string) string {
	prefix := ""
	if mode == "test" {
		prefix = "«آزمایشی» "
	}
	repl := []string{"{{mode_prefix}}", prefix, "{{issuer}}", issuer, "{{region}}", region,
		"{{expires}}", FormatTehran(expires) + " به وقت تهران"}
	for _, p := range t.Params {
		repl = append(repl, "{{"+p+"}}", strings.TrimSpace(params[p]))
	}
	out := strings.NewReplacer(repl...).Replace(t.Body)
	return strings.Join(strings.Fields(out), " ")
}

type CreateRequest struct {
	IncidentID      *uuid.UUID        `json:"incident_id"`
	Mode            string            `json:"mode"`
	Severity        string            `json:"severity"`
	IssuerOrgID     uuid.UUID         `json:"issuer_org_id"`
	Region          map[string]any    `json:"region"` // GeoJSON Polygon / MultiPolygon
	RegionLabel     string            `json:"region_label"`
	TemplateCode    string            `json:"template_code"`
	TemplateVersion int               `json:"template_version"`
	Params          map[string]string `json:"params"`
	Channels        []string          `json:"channels"`
	ExpiresAt       time.Time         `json:"expires_at"`
}

func (req *CreateRequest) Validate(now time.Time, maxValidity time.Duration) error {
	var v httpx.Validator
	req.RegionLabel = strings.TrimSpace(req.RegionLabel)
	v.Check(req.Mode == "test" || req.Mode == "operational", "mode", "must_be_test_or_operational")
	v.Check(slices.Contains(Severities, req.Severity), "severity", "not_allowed")
	v.Check(req.IssuerOrgID != uuid.Nil, "issuer_org_id", "required")
	v.Check(req.Region != nil, "region", "required")
	v.Check(req.RegionLabel != "" && utf8.RuneCountInString(req.RegionLabel) <= 120, "region_label", "required_max_120")
	t, ok := FindTemplate(req.TemplateCode, req.TemplateVersion)
	v.Check(ok, "template_code", "unknown_template_or_version")
	v.Check(len(req.Channels) > 0, "channels", "required")
	seen := map[string]bool{}
	for _, c := range req.Channels {
		v.Check(slices.Contains(Channels, c), "channels", "not_allowed:"+c)
		v.Check(!seen[c], "channels", "duplicate:"+c)
		seen[c] = true
	}
	v.Check(req.ExpiresAt.After(now.Add(5*time.Minute)), "expires_at", "must_be_at_least_5_minutes_ahead")
	v.Check(!req.ExpiresAt.After(now.Add(maxValidity)), "expires_at", fmt.Sprintf("must_be_within_%s", maxValidity))
	if ok {
		for _, p := range t.Params {
			val := strings.TrimSpace(req.Params[p])
			v.Check(val != "" && utf8.RuneCountInString(val) <= 300, "params."+p, "required_max_300")
		}
		for k := range req.Params {
			v.Check(slices.Contains(t.Params, k), "params."+k, "unknown_param")
		}
	}
	return v.Err()
}

// CheckChannelLengths ensures the rendered text fits every requested channel.
func CheckChannelLengths(text string, channels []string) error {
	var v httpx.Validator
	n := utf8.RuneCountInString(text)
	for _, c := range channels {
		v.Check(n <= ChannelMaxRunes[c], "channels", fmt.Sprintf("text_too_long_for_%s(%d>%d)", c, n, ChannelMaxRunes[c]))
	}
	return v.Err()
}

// AggregateStatus derives the alert's delivery status from the latest attempt of each channel.
// Provider acceptance counts as sent-to-provider; it is NOT proof of receipt by users.
func AggregateStatus(latestPerChannel map[string]string) string {
	terminalOK := map[string]bool{"accepted": true, "delivered": true}
	terminalFail := map[string]bool{"rejected": true, "failed": true, "superseded": true}
	ok, fail := 0, 0
	for _, st := range latestPerChannel {
		switch {
		case terminalOK[st]:
			ok++
		case terminalFail[st]:
			fail++
		default:
			return Sending // pending, in_flight or unknown: still being worked/reconciled
		}
	}
	switch {
	case ok > 0 && fail == 0:
		return Sent
	case ok == 0:
		return Failed
	default:
		return PartiallySent
	}
}
