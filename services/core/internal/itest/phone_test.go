package itest

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func phoneBody() map[string]any {
	return map[string]any{"type": "trapped_people", "description": "تماس: دو نفر زیر آوار در ساختمان سه طبقه",
		"location":    map[string]any{"lat": 35.7155, "lng": 51.4122, "accuracy_m": 200},
		"caller_name": "علی", "caller_phone": "۰۹۱۲ ۱۲۳ ۴۵۶۷", "callback_requested": true, "address_text": "خیابان نمونه، کوچه ۳"}
}

// Phone intake: operators record calls; caller details are personal data with restricted visibility.
func TestPhoneIntake(t *testing.T) {
	op := login(t, grant{Role: "OPERATOR", OrgCode: "command"})
	analyst := login(t, grant{Role: "GIS_ANALYST"})
	citizen := login(t)

	citizen.do("POST", "/reports/phone", phoneBody(), idem(), nil, 403)

	h := idem()
	var created struct {
		ReportID uuid.UUID `json:"report_id"`
		Status   string    `json:"status"`
	}
	op.do("POST", "/reports/phone", phoneBody(), h, &created, 202)
	r := op.do("POST", "/reports/phone", phoneBody(), h, nil, 202)
	if created.Status != "received" || r.Header.Get("Idempotent-Replayed") != "true" {
		t.Fatalf("unexpected %+v / replay header %q", created, r.Header.Get("Idempotent-Replayed"))
	}

	type detail struct {
		Source   string `json:"source"`
		Status   string `json:"status"`
		Location struct {
			Source string `json:"source"`
		} `json:"location"`
		Contact *struct {
			CallerName        string `json:"caller_name"`
			CallerPhone       string `json:"caller_phone"`
			CallbackRequested bool   `json:"callback_requested"`
			AddressText       string `json:"address_text"`
		} `json:"contact"`
	}
	var d detail
	op.do("GET", "/reports/"+created.ReportID.String(), nil, nil, &d, 200)
	if d.Source != "phone" || d.Status != "received" || d.Location.Source != "manual" || d.Contact == nil ||
		d.Contact.CallerPhone != "09121234567" || !d.Contact.CallbackRequested || d.Contact.AddressText == "" {
		t.Fatalf("operator view wrong: %+v contact=%+v", d, d.Contact)
	}
	var a detail
	analyst.do("GET", "/reports/"+created.ReportID.String(), nil, nil, &a, 200)
	if a.Contact != nil {
		t.Fatal("caller details must be hidden from roles without report:read_precise")
	}

	// Personal data must not leak into the event bus.
	var payload string
	pool.QueryRow(context.Background(), `SELECT payload::text FROM outbox_events WHERE aggregate_id=$1 AND event_type='report.created'`,
		created.ReportID).Scan(&payload)
	if !strings.Contains(payload, `"source": "phone"`) || strings.Contains(payload, "0912") || strings.Contains(payload, "علی") {
		t.Fatalf("event payload must carry source but no caller data: %s", payload)
	}
	if countAudit(t, "report.phone_intake", "success", created.ReportID.String()) != 1 {
		t.Fatal("phone intake must be audited")
	}

	// Enters the normal review flow: another operator can claim and accept it.
	op2 := login(t, grant{Role: "OPERATOR"})
	op2.do("POST", "/reports/"+created.ReportID.String()+"/claim", nil, nil, nil, 200)

	// Validation errors are structured.
	bad := phoneBody()
	bad["caller_phone"] = "12"
	bad["media_ids"] = []string{uuid.NewString()}
	res := op.raw("POST", "/reports/phone", bad, idem())
	if res.Status != 422 || !strings.Contains(string(res.Body), "caller_phone") || !strings.Contains(string(res.Body), "media_ids") {
		t.Fatalf("want 422 with caller_phone and media_ids details, got %d %s", res.Status, res.Body)
	}
}

// The operator queue's default "open" filter keeps reports visible after AI triage moves them on.
func TestReportQueueOpenFilter(t *testing.T) {
	op := login(t, grant{Role: "OPERATOR", OrgCode: "command"})
	var created struct {
		ReportID uuid.UUID `json:"report_id"`
	}
	op.do("POST", "/reports/phone", phoneBody(), idem(), &created, 202)
	if _, err := pool.Exec(context.Background(), `UPDATE reports SET status='triage' WHERE id=$1`, created.ReportID); err != nil {
		t.Fatal(err)
	}
	has := func(status string) bool {
		var page struct {
			Items []struct {
				ID uuid.UUID `json:"id"`
			} `json:"items"`
		}
		op.do("GET", "/reports?limit=200&status="+status, nil, nil, &page, 200)
		for _, it := range page.Items {
			if it.ID == created.ReportID {
				return true
			}
		}
		return false
	}
	if has("received") || !has("open") || !has("triage") {
		t.Fatalf("open filter: received=%v open=%v triage=%v", has("received"), has("open"), has("triage"))
	}
	op.do("GET", "/reports?status=bogus", nil, nil, nil, 422)
}
