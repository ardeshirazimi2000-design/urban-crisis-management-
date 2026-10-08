package itest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/audit"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/notification"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/outbox"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/report"
)

// recordingPublisher records published messages and fails for selected aggregate keys.
type recordingPublisher struct {
	mu     sync.Mutex
	failOn map[string]bool
	sent   []outbox.Message
}

func (p *recordingPublisher) Publish(_ context.Context, msgs []outbox.Message) []error {
	p.mu.Lock()
	defer p.mu.Unlock()
	errs := make([]error, len(msgs))
	for i, m := range msgs {
		if p.failOn[string(m.Key)] {
			errs[i] = errors.New("broker unavailable")
			continue
		}
		p.sent = append(p.sent, m)
	}
	return errs
}
func (p *recordingPublisher) Close() error { return nil }

func (p *recordingPublisher) sentFor(agg uuid.UUID) []outbox.Message {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []outbox.Message
	for _, m := range p.sent {
		if string(m.Key) == agg.String() {
			out = append(out, m)
		}
	}
	return out
}

func drain(t *testing.T, r *outbox.Relay) {
	t.Helper()
	for i := 0; i < 50; i++ {
		n, err := r.RunOnce(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			return
		}
	}
}

// AT-01: a valid report gets a stable server-side id, is durable before 202, and its event is eventually published.
func TestAT01_ReportAcceptedAndEventPublished(t *testing.T) {
	citizen := login(t)
	var created struct {
		ReportID      uuid.UUID `json:"report_id"`
		Status        string    `json:"status"`
		CorrelationID string    `json:"correlation_id"`
	}
	r := citizen.do("POST", "/reports", reportBody(35.7, 51.4), idem(), &created, http.StatusAccepted)
	if created.Status != "received" || created.ReportID == uuid.Nil || created.CorrelationID != r.Header.Get("X-Correlation-ID") {
		t.Fatalf("unexpected response %+v", created)
	}
	var n int
	pool.QueryRow(context.Background(), `SELECT count(*) FROM outbox_events WHERE aggregate_id=$1 AND event_type='report.created' AND published_at IS NULL`,
		created.ReportID).Scan(&n)
	if n != 1 {
		t.Fatalf("expected 1 pending outbox event, got %d", n)
	}
	pub := &recordingPublisher{}
	drain(t, &outbox.Relay{Pool: pool, Pub: pub, TopicPrefix: "crisis."})
	msgs := pub.sentFor(created.ReportID)
	if len(msgs) != 1 || msgs[0].Topic != "crisis.report.created.v1" {
		t.Fatalf("event not published: %+v", msgs)
	}
	var env outbox.Envelope
	if err := json.Unmarshal(msgs[0].Value, &env); err != nil || env.PublishedAt == nil || env.SchemaVersion != 1 ||
		env.CorrelationID != created.CorrelationID {
		t.Fatalf("bad envelope %+v (%v)", env, err)
	}
	var payload map[string]any
	json.Unmarshal(env.Payload, &payload)
	if _, leaked := payload["reporter_ref"]; leaked {
		t.Fatal("reporter identity must not be in the event")
	}
}

// AT-02: retrying with the same Idempotency-Key yields one domain effect and the same response.
func TestAT02_IdempotentReportCreation(t *testing.T) {
	citizen := login(t)
	h := idem()
	var a, b struct {
		ReportID uuid.UUID `json:"report_id"`
	}
	citizen.do("POST", "/reports", reportBody(35.72, 51.42), h, &a, 202)
	r := citizen.do("POST", "/reports", reportBody(35.72, 51.42), h, &b, 202)
	if a.ReportID != b.ReportID || r.Header.Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay mismatch %v %v", a.ReportID, b.ReportID)
	}
	// Same key, different body -> rejected.
	r = citizen.raw("POST", "/reports", reportBody(35.73, 51.43), h)
	if r.Status != 422 || r.errCode() != "IDEMPOTENCY_KEY_REUSED" {
		t.Fatalf("want IDEMPOTENCY_KEY_REUSED, got %d %s", r.Status, r.Body)
	}
	// Concurrent duplicates -> exactly one report.
	h2 := idem()
	var wg sync.WaitGroup
	ids := make(chan string, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := citizen.raw("POST", "/reports", reportBody(35.74, 51.44), h2)
			var out struct {
				ReportID string `json:"report_id"`
			}
			json.Unmarshal(r.Body, &out)
			ids <- fmt.Sprintf("%d:%s", r.Status, out.ReportID)
		}()
	}
	wg.Wait()
	close(ids)
	distinct := map[string]bool{}
	for id := range ids {
		distinct[id] = true
	}
	var n int
	pool.QueryRow(context.Background(), `SELECT count(*) FROM reports WHERE reporter_ref=$1 AND ST_DWithin(location, ST_SetSRID(ST_MakePoint(51.44,35.74),4326)::geography, 1)`,
		citizen.id).Scan(&n)
	if n != 1 {
		t.Fatalf("expected exactly one report, got %d (responses %v)", n, distinct)
	}
}

// AT-03: Kafka down during intake: the report and outbox entry survive; publication resumes after recovery,
// preserving per-aggregate order; a permanently failing event is dead-lettered and can be replayed.
func TestAT03_BrokerOutageAndRecovery(t *testing.T) {
	citizen := login(t)
	operator := login(t, grant{Role: "OPERATOR"})
	var created struct {
		ReportID uuid.UUID `json:"report_id"`
	}
	citizen.do("POST", "/reports", reportBody(35.75, 51.45), idem(), &created, 202)
	operator.do("POST", "/reports/"+created.ReportID.String()+"/review", map[string]any{"decision": "rejected", "version": 1, "reason": "test"}, nil, nil, 200)

	down := &recordingPublisher{failOn: map[string]bool{created.ReportID.String(): true}}
	relay := &outbox.Relay{Pool: pool, Pub: down, TopicPrefix: "crisis.", MaxAttempts: 3}
	drain(t, relay)
	var attempts int
	var published *time.Time
	pool.QueryRow(context.Background(), `SELECT attempts, published_at FROM outbox_events WHERE aggregate_id=$1 AND event_type='report.created'`,
		created.ReportID).Scan(&attempts, &published)
	if attempts != 1 || published != nil {
		t.Fatalf("expected one failed attempt, got attempts=%d published=%v", attempts, published)
	}
	// The later event of the same aggregate must not overtake the failed one.
	var reviewedPublished *time.Time
	pool.QueryRow(context.Background(), `SELECT published_at FROM outbox_events WHERE aggregate_id=$1 AND event_type='report.reviewed'`,
		created.ReportID).Scan(&reviewedPublished)
	if reviewedPublished != nil {
		t.Fatal("per-aggregate order violated")
	}
	// Broker recovers.
	pool.Exec(context.Background(), `UPDATE outbox_events SET next_attempt_at=now() WHERE aggregate_id=$1`, created.ReportID)
	up := &recordingPublisher{}
	drain(t, &outbox.Relay{Pool: pool, Pub: up, TopicPrefix: "crisis."})
	msgs := up.sentFor(created.ReportID)
	if len(msgs) != 2 || msgs[0].Topic != "crisis.report.created.v1" || msgs[1].Topic != "crisis.report.reviewed.v1" {
		t.Fatalf("expected created then reviewed, got %v", msgs)
	}

	// Dead-letter after max attempts, then replay via admin API.
	var c2 struct {
		ReportID uuid.UUID `json:"report_id"`
	}
	citizen.do("POST", "/reports", reportBody(35.76, 51.46), idem(), &c2, 202)
	bad := &recordingPublisher{failOn: map[string]bool{c2.ReportID.String(): true}}
	for i := 0; i < 3; i++ {
		pool.Exec(context.Background(), `UPDATE outbox_events SET next_attempt_at=now() WHERE aggregate_id=$1`, c2.ReportID)
		drain(t, &outbox.Relay{Pool: pool, Pub: bad, TopicPrefix: "crisis.", MaxAttempts: 3})
	}
	var dead *time.Time
	pool.QueryRow(context.Background(), `SELECT dead_lettered_at FROM outbox_events WHERE aggregate_id=$1`, c2.ReportID).Scan(&dead)
	if dead == nil {
		t.Fatal("event should be dead-lettered after max attempts")
	}
	admin := login(t, grant{Role: "SECURITY_ADMIN"})
	operator.do("POST", "/admin/outbox/replay", map[string]any{"aggregate_id": c2.ReportID, "reason": "x"}, nil, nil, 403)
	var rep struct {
		Requeued int `json:"requeued"`
	}
	admin.do("POST", "/admin/outbox/replay", map[string]any{"aggregate_id": c2.ReportID, "reason": "broker fixed"}, nil, &rep, 200)
	if rep.Requeued != 1 {
		t.Fatalf("requeued %d", rep.Requeued)
	}
	ok := &recordingPublisher{}
	drain(t, &outbox.Relay{Pool: pool, Pub: ok, TopicPrefix: "crisis."})
	if len(ok.sentFor(c2.ReportID)) != 1 {
		t.Fatal("replayed event not published")
	}
}

// AT-04: with AI unavailable (no report.scored event) intake and manual review continue.
// When the score arrives it is stored once (inbox) and only moves received -> triage.
func TestAT04_AIOutageDoesNotBlockReview(t *testing.T) {
	citizen := login(t)
	operator := login(t, grant{Role: "OPERATOR"})
	var created struct {
		ReportID uuid.UUID `json:"report_id"`
	}
	citizen.do("POST", "/reports", reportBody(35.701, 51.401), idem(), &created, 202)
	var rp struct {
		Status           string `json:"status"`
		EnrichmentStatus string `json:"enrichment_status"`
		Version          int    `json:"version"`
	}
	operator.do("GET", "/reports/"+created.ReportID.String(), nil, nil, &rp, 200)
	if rp.EnrichmentStatus != "pending" || rp.Status != "received" {
		t.Fatalf("unexpected %+v", rp)
	}
	operator.do("POST", "/reports/"+created.ReportID.String()+"/claim", nil, nil, nil, 200)

	// Late AI score arrives twice (at-least-once delivery).
	h := report.HandleScored(pool)
	payload, _ := json.Marshal(map[string]any{"report_id": created.ReportID, "model_version": "kw-1.0.0", "predicted_type": "fire",
		"type_confidence": 0.4, "urgency_signal": 0.9, "signals": map[string]any{"keywords": []string{"آتش"}}, "status": "ok"})
	env := outbox.Envelope{EventID: uuid.New(), EventType: "report.scored", SchemaVersion: 1, Payload: payload}
	for i := 0; i < 2; i++ {
		if err := h(context.Background(), env); err != nil {
			t.Fatal(err)
		}
	}
	var scores int
	pool.QueryRow(context.Background(), `SELECT count(*) FROM report_ai_scores WHERE report_id=$1`, created.ReportID).Scan(&scores)
	if scores != 1 {
		t.Fatalf("expected one score, got %d", scores)
	}
	operator.do("GET", "/reports/"+created.ReportID.String(), nil, nil, &rp, 200)
	if rp.Status != "under_review" {
		t.Fatalf("AI must not change a report already under human review, got %s", rp.Status)
	}
	operator.do("POST", "/reports/"+created.ReportID.String()+"/review",
		map[string]any{"decision": "accepted", "version": rp.Version, "reason": "تأیید میدانی"}, nil, nil, 200)

	// Unknown report -> permanent error (goes to DLQ, not retried forever).
	bad, _ := json.Marshal(map[string]any{"report_id": uuid.New(), "model_version": "kw-1.0.0", "status": "ok"})
	if err := h(context.Background(), outbox.Envelope{EventID: uuid.New(), EventType: "report.scored", SchemaVersion: 1, Payload: bad}); err == nil {
		t.Fatal("unknown report must fail permanently")
	}
}

// AT-05: users outside the owning organization cannot read or change data; the attempt is audited.
func TestAT05_CrossOrganizationAccessDenied(t *testing.T) {
	commandOp := login(t, grant{Role: "OPERATOR", OrgCode: "command"})
	fireOp := login(t, grant{Role: "OPERATOR", OrgCode: "fire"})
	in := createIncident(t, commandOp, "command", "open")

	r := fireOp.raw("GET", "/incidents/"+in.ID.String(), nil, nil)
	if r.Status != 404 {
		t.Fatalf("cross-org read must look like not-found, got %d", r.Status)
	}
	r = fireOp.raw("POST", "/incidents/"+in.ID.String()+"/transitions", map[string]any{"to": "contained", "reason": "x", "version": in.Version}, nil)
	if r.Status != 403 {
		t.Fatalf("cross-org change must be forbidden, got %d", r.Status)
	}
	var list struct {
		Items []incidentResp `json:"items"`
	}
	fireOp.do("GET", "/incidents", nil, nil, &list, 200)
	for _, i := range list.Items {
		if i.ID == in.ID {
			t.Fatal("cross-org incident leaked in list")
		}
	}
	if countAudit(t, "incident:read", "denied", in.ID.String()) != 1 {
		t.Fatal("denied read must be audited")
	}
	// Citizens cannot read other people's reports.
	c1, c2 := login(t), login(t)
	var created struct {
		ReportID uuid.UUID `json:"report_id"`
	}
	c1.do("POST", "/reports", reportBody(35.702, 51.402), idem(), &created, 202)
	c1.do("GET", "/reports/"+created.ReportID.String(), nil, nil, nil, 200)
	c2.do("GET", "/reports/"+created.ReportID.String(), nil, nil, nil, 404)
	c2.do("GET", "/reports", nil, nil, nil, 403)
}

// AT-06: concurrent allocation of the same resource yields at most one active assignment.
func TestAT06_ConcurrentAllocation(t *testing.T) {
	cmd := login(t, grant{Role: "COMMANDER", OrgCode: "command"}, grant{Role: "RESOURCE_MANAGER"})
	inc1 := createIncident(t, cmd, "command", "open")
	inc2 := createIncident(t, cmd, "command", "open")
	resourceID := uuid.New()
	if _, err := pool.Exec(context.Background(), `INSERT INTO resources (id, organization_id, type, name, location, last_seen_at)
		VALUES ($1, $2, 'ambulance', 'آزمون همزمانی', ST_SetSRID(ST_MakePoint(51.4,35.7),4326)::geography, now())`,
		resourceID, orgID(t, "ems")); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	statuses := make(chan int, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		incID := inc1.ID
		if i%2 == 1 {
			incID = inc2.ID
		}
		go func(incID uuid.UUID) {
			defer wg.Done()
			r := cmd.raw("POST", "/incidents/"+incID.String()+"/assignments", map[string]any{"resource_id": resourceID, "reason": "race"}, nil)
			statuses <- r.Status
		}(incID)
	}
	wg.Wait()
	close(statuses)
	ok, conflict := 0, 0
	for s := range statuses {
		switch s {
		case 201:
			ok++
		case 409:
			conflict++
		default:
			t.Fatalf("unexpected status %d", s)
		}
	}
	var active int
	pool.QueryRow(context.Background(), `SELECT count(*) FROM assignments WHERE resource_id=$1 AND status IN ('proposed','assigned','acknowledged','en_route','on_scene')`,
		resourceID).Scan(&active)
	if ok != 1 || conflict != 19 || active != 1 {
		t.Fatalf("ok=%d conflict=%d active=%d", ok, conflict, active)
	}
	// Completing the mission releases the resource for re-allocation.
	var a struct {
		ID      uuid.UUID `json:"id"`
		Version int       `json:"version"`
	}
	pool.QueryRow(context.Background(), `SELECT id, version FROM assignments WHERE resource_id=$1`, resourceID).Scan(&a.ID, &a.Version)
	cmd.do("POST", "/assignments/"+a.ID.String()+"/status", map[string]any{"status": "cancelled", "version": a.Version}, nil, nil, 422) // reason required
	cmd.do("POST", "/assignments/"+a.ID.String()+"/status", map[string]any{"status": "cancelled", "reason": "تغییر اولویت", "version": a.Version}, nil, nil, 200)
	cmd.do("POST", "/incidents/"+inc2.ID.String()+"/assignments", map[string]any{"resource_id": resourceID, "reason": "again"}, nil, nil, 201)
}

type alertResp struct {
	ID      uuid.UUID `json:"id"`
	Status  string    `json:"status"`
	Version int       `json:"version"`
}

func draftAlert(t *testing.T, c *client, channels []string) alertResp {
	var a alertResp
	c.do("POST", "/alerts", map[string]any{"mode": "test", "severity": "warning", "issuer_org_id": orgID(t, "command"),
		"region":       map[string]any{"type": "Polygon", "coordinates": [][][]float64{{{51.38, 35.69}, {51.42, 35.69}, {51.42, 35.72}, {51.38, 35.72}, {51.38, 35.69}}}},
		"region_label": "منطقه آزمون", "template_code": "system_test", "template_version": 1, "channels": channels,
		"expires_at": time.Now().Add(3 * time.Hour).UTC().Format(time.RFC3339)}, idem(), &a, 201)
	return a
}

// AT-07: dispatch without approval is blocked and recorded as a security event; four-eyes is enforced.
func TestAT07_AlertWorkflow(t *testing.T) {
	op := login(t, grant{Role: "OPERATOR", OrgCode: "command"})
	cmd := login(t, grant{Role: "COMMANDER", OrgCode: "command"})
	a := draftAlert(t, op, []string{"internal"})

	r := cmd.raw("POST", "/alerts/"+a.ID.String()+"/dispatch", nil, idem())
	if r.Status != 409 || r.errCode() != "NOT_APPROVED" {
		t.Fatalf("want NOT_APPROVED, got %d %s", r.Status, r.Body)
	}
	if countAudit(t, "alert:dispatch", "denied", a.ID.String()) != 1 {
		t.Fatal("blocked dispatch must be audited")
	}
	op.do("POST", "/alerts/"+a.ID.String()+"/approve", map[string]any{"version": 1, "reason": "x"}, nil, nil, 403) // operators cannot approve
	op.do("POST", "/alerts/"+a.ID.String()+"/submit", map[string]any{"version": 1}, nil, &a, 200)

	// A commander who drafted the alert cannot approve it (four-eyes).
	cmdAuthor := login(t, grant{Role: "COMMANDER", OrgCode: "command"})
	own := draftAlert(t, cmdAuthor, []string{"internal"})
	cmdAuthor.do("POST", "/alerts/"+own.ID.String()+"/submit", map[string]any{"version": 1}, nil, &own, 200)
	r = cmdAuthor.raw("POST", "/alerts/"+own.ID.String()+"/approve", map[string]any{"version": own.Version, "reason": "self"}, nil)
	if r.Status != 403 || r.errCode() != "FOUR_EYES_REQUIRED" {
		t.Fatalf("want FOUR_EYES_REQUIRED, got %d %s", r.Status, r.Body)
	}

	cmd.do("POST", "/alerts/"+a.ID.String()+"/approve", map[string]any{"version": a.Version, "reason": "تأیید"}, nil, &a, 200)
	key := idem()
	cmd.do("POST", "/alerts/"+a.ID.String()+"/dispatch", nil, key, &a, 202)
	cmd.do("POST", "/alerts/"+a.ID.String()+"/dispatch", nil, key, nil, 200) // idempotent repeat
	r = cmd.raw("POST", "/alerts/"+a.ID.String()+"/dispatch", nil, idem())
	if r.Status != 409 || r.errCode() != "ALREADY_DISPATCHED" {
		t.Fatalf("second dispatch with new key must be refused, got %d", r.Status)
	}
	var attempts int
	pool.QueryRow(context.Background(), `SELECT count(*) FROM notification_attempts WHERE alert_id=$1`, a.ID).Scan(&attempts)
	if attempts != 1 {
		t.Fatalf("expected 1 attempt, got %d", attempts)
	}
	w := &notification.Worker{Pool: pool, Adapters: map[string]notification.Adapter{"internal": notification.InternalAdapter{}}}
	for i := 0; i < 3; i++ {
		w.RunOnce(context.Background())
	}
	cmd.do("GET", "/alerts/"+a.ID.String(), nil, nil, &a, 200)
	if a.Status != "sent" {
		t.Fatalf("expected sent, got %s", a.Status)
	}
	r = cmd.raw("POST", "/alerts/"+a.ID.String()+"/cancel", map[string]any{"version": a.Version, "reason": "x"}, nil)
	if r.Status != 409 || r.errCode() != "CANNOT_CANCEL" {
		t.Fatalf("sent alert needs a correction, not cancel: %d", r.Status)
	}
}

// scriptedAdapter returns predetermined outcomes for Send and Query.
type scriptedAdapter struct {
	mu    sync.Mutex
	send  []notification.Outcome
	query []notification.Outcome
	sends int
}

func (s *scriptedAdapter) Channel() string { return "sms" }
func (s *scriptedAdapter) next(q *[]notification.Outcome) notification.Outcome {
	o := (*q)[0]
	if len(*q) > 1 {
		*q = (*q)[1:]
	}
	return o
}
func (s *scriptedAdapter) Send(context.Context, notification.Message) notification.Result {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sends++
	return notification.Result{Outcome: s.next(&s.send), ProviderMessageID: "pm-1", Code: "X"}
}
func (s *scriptedAdapter) Query(context.Context, string) notification.Result {
	s.mu.Lock()
	defer s.mu.Unlock()
	return notification.Result{Outcome: s.next(&s.query), Code: "Q"}
}

func dispatchedAlert(t *testing.T) uuid.UUID {
	op := login(t, grant{Role: "OPERATOR", OrgCode: "command"})
	cmd := login(t, grant{Role: "COMMANDER", OrgCode: "command"})
	a := draftAlert(t, op, []string{"sms"})
	op.do("POST", "/alerts/"+a.ID.String()+"/submit", map[string]any{"version": 1}, nil, &a, 200)
	cmd.do("POST", "/alerts/"+a.ID.String()+"/approve", map[string]any{"version": a.Version, "reason": "ok"}, nil, &a, 200)
	cmd.do("POST", "/alerts/"+a.ID.String()+"/dispatch", nil, idem(), nil, 202)
	return a.ID
}

func attemptStatuses(t *testing.T, alertID uuid.UUID) []string {
	rows, _ := pool.Query(context.Background(), `SELECT status FROM notification_attempts WHERE alert_id=$1 ORDER BY attempt_no`, alertID)
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		rows.Scan(&s)
		out = append(out, s)
	}
	return out
}

// AT-08: provider timeout -> unknown + reconciliation, never a blind resend.
func TestAT08_ProviderTimeoutReconciliation(t *testing.T) {
	// Case 1: timeout, reconciliation finds the message -> accepted, no resend.
	id := dispatchedAlert(t)
	ad := &scriptedAdapter{send: []notification.Outcome{notification.Unknown}, query: []notification.Outcome{notification.Accepted}}
	w := &notification.Worker{Pool: pool, Adapters: map[string]notification.Adapter{"sms": ad}, ReconcileAfter: time.Millisecond}
	w.RunOnce(context.Background())
	if got := attemptStatuses(t, id); len(got) != 1 || got[0] != "unknown" {
		t.Fatalf("after timeout: %v", got)
	}
	pool.Exec(context.Background(), `UPDATE notification_attempts SET next_attempt_at=now() WHERE alert_id=$1`, id)
	w.RunOnce(context.Background())
	if got := attemptStatuses(t, id); len(got) != 1 || got[0] != "accepted" || ad.sends != 1 {
		t.Fatalf("after reconcile: %v sends=%d", got, ad.sends)
	}

	// Case 2: timeout, provider has no record -> safe retry as a new attempt.
	id2 := dispatchedAlert(t)
	ad2 := &scriptedAdapter{send: []notification.Outcome{notification.Unknown, notification.Accepted}, query: []notification.Outcome{notification.NotFound}}
	w2 := &notification.Worker{Pool: pool, Adapters: map[string]notification.Adapter{"sms": ad2}, ReconcileAfter: time.Millisecond}
	for i := 0; i < 3; i++ {
		pool.Exec(context.Background(), `UPDATE notification_attempts SET next_attempt_at=now() WHERE alert_id=$1`, id2)
		w2.RunOnce(context.Background())
	}
	got := attemptStatuses(t, id2)
	if len(got) != 2 || got[0] != "failed" || got[1] != "accepted" {
		t.Fatalf("expected [failed accepted], got %v", got)
	}
	var st string
	pool.QueryRow(context.Background(), `SELECT status FROM alerts WHERE id=$1`, id2).Scan(&st)
	if st != "sent" {
		t.Fatalf("alert status %s", st)
	}

	// Case 3: permanent rejection -> no retry, alert failed.
	id3 := dispatchedAlert(t)
	ad3 := &scriptedAdapter{send: []notification.Outcome{notification.Rejected}}
	(&notification.Worker{Pool: pool, Adapters: map[string]notification.Adapter{"sms": ad3}}).RunOnce(context.Background())
	pool.QueryRow(context.Background(), `SELECT status FROM alerts WHERE id=$1`, id3).Scan(&st)
	if got := attemptStatuses(t, id3); len(got) != 1 || st != "failed" {
		t.Fatalf("rejected must not retry: %v %s", got, st)
	}
}

// AT-10: invalid or out-of-area locations are rejected with a structured error.
func TestAT10_InvalidLocation(t *testing.T) {
	c := login(t)
	for _, body := range []map[string]any{
		reportBody(48.85, 2.35),
		reportBody(95, 51.4),
		{"type": "fire", "location": map[string]any{"lat": 35.7, "lng": 51.4, "accuracy_m": -1}},
		{"type": "fire", "location": map[string]any{"lat": "x", "lng": 51.4, "accuracy_m": 1}},
	} {
		r := c.raw("POST", "/reports", body, idem())
		if r.Status != 422 || r.errCode() != "VALIDATION_ERROR" {
			t.Fatalf("want 422 VALIDATION_ERROR, got %d %s", r.Status, r.Body)
		}
		var e struct {
			Error struct {
				CorrelationID string `json:"correlation_id"`
				Details       []struct {
					Field string `json:"field"`
				} `json:"details"`
			} `json:"error"`
		}
		r.json(t, &e)
		if e.Error.CorrelationID == "" || len(e.Error.Details) == 0 {
			t.Fatalf("structured error expected: %s", r.Body)
		}
	}
	r := c.raw("POST", "/reports", reportBody(35.7, 51.4), nil)
	if r.Status != 400 || r.errCode() != "IDEMPOTENCY_KEY_REQUIRED" {
		t.Fatalf("missing idempotency key: %d", r.Status)
	}
	r = c.raw("POST", "/reports", map[string]any{"type": "fire", "unknown": 1}, idem())
	if r.Status != 422 {
		t.Fatalf("unknown fields must be rejected: %d", r.Status)
	}
}

// Location privacy: roles without report:read_precise see coarsened coordinates.
func TestLocationPrivacy(t *testing.T) {
	citizen := login(t)
	analyst := login(t, grant{Role: "GIS_ANALYST"})
	operator := login(t, grant{Role: "OPERATOR"})
	var created struct {
		ReportID uuid.UUID `json:"report_id"`
	}
	citizen.do("POST", "/reports", reportBody(35.712345, 51.398765), idem(), &created, 202)
	type loc struct {
		Location struct {
			Lat       float64 `json:"lat"`
			Precision string  `json:"precision"`
		} `json:"location"`
	}
	var a, o loc
	analyst.do("GET", "/reports/"+created.ReportID.String(), nil, nil, &a, 200)
	operator.do("GET", "/reports/"+created.ReportID.String(), nil, nil, &o, 200)
	if a.Location.Precision != "approximate" || a.Location.Lat != 35.71 {
		t.Fatalf("analyst must get approximate location: %+v", a)
	}
	if o.Location.Precision != "exact" || o.Location.Lat != 35.712345 {
		t.Fatalf("operator must get exact location: %+v", o)
	}
}

// Audit log is tamper-evident and append-only.
func TestAuditChain(t *testing.T) {
	res, err := audit.Verify(context.Background(), pool)
	if err != nil || !res.Valid || res.Checked == 0 {
		t.Fatalf("chain invalid: %+v %v", res, err)
	}
	if _, err := pool.Exec(context.Background(), `UPDATE audit_log SET reason='tampered' WHERE seq=1`); err == nil {
		t.Fatal("audit_log must be append-only")
	}
	if _, err := pool.Exec(context.Background(), `DELETE FROM audit_log WHERE seq=1`); err == nil {
		t.Fatal("audit_log must be append-only")
	}
}

// Incident lifecycle through the API, including approval-gated transitions and evidence linking.
func TestIncidentLifecycle(t *testing.T) {
	citizen := login(t)
	op := login(t, grant{Role: "OPERATOR", OrgCode: "command"})
	cmd := login(t, grant{Role: "COMMANDER", OrgCode: "command"})
	rid := createAcceptedReport(t, citizen, op)
	in := createIncident(t, op, "command", "draft")
	op.do("POST", "/incidents/"+in.ID.String()+"/reports", map[string]any{"report_id": rid, "reason": "هم‌مکان"}, nil, nil, 200)
	r := op.raw("POST", "/incidents/"+in.ID.String()+"/reports", map[string]any{"report_id": rid, "reason": "dup"}, nil)
	if r.Status != 409 {
		t.Fatalf("duplicate link must conflict: %d", r.Status)
	}
	steps := []struct {
		c    *client
		to   string
		want int
	}{
		{op, "open", 200}, {op, "active", 403}, {cmd, "active", 200}, {op, "contained", 200}, {op, "resolved", 200},
		{op, "closed", 403}, {cmd, "closed", 200}, {cmd, "active", 409},
	}
	for _, s := range steps {
		cur := incidentResp{}
		cmd.do("GET", "/incidents/"+in.ID.String(), nil, nil, &struct {
			Incident *incidentResp `json:"incident"`
		}{&cur}, 200)
		s.c.do("POST", "/incidents/"+in.ID.String()+"/transitions", map[string]any{"to": s.to, "reason": "گام آزمون", "version": cur.Version}, nil, nil, s.want)
	}
	// Stale version -> conflict.
	r = cmd.raw("POST", "/incidents/"+in.ID.String()+"/transitions", map[string]any{"to": "open", "reason": "reopen", "version": 1}, nil)
	if r.Status != 409 || r.errCode() != "VERSION_CONFLICT" {
		t.Fatalf("want VERSION_CONFLICT got %d %s", r.Status, r.Body)
	}
	var detail struct {
		Timeline []struct {
			EventType string `json:"event_type"`
		} `json:"timeline"`
	}
	cmd.do("GET", "/incidents/"+in.ID.String(), nil, nil, &detail, 200)
	if len(detail.Timeline) < 7 {
		t.Fatalf("timeline incomplete: %d", len(detail.Timeline))
	}
}

// Media: real MIME detection, owner-only access and short-lived signed URLs.
func TestMediaUpload(t *testing.T) {
	citizen := login(t)
	other := login(t)
	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00\x1f\x15\xc4\x89\x00\x00\x00\nIDATx\x9cc\x00\x01\x00\x00\x05\x00\x01\r\n-\xb4\x00\x00\x00\x00IEND\xaeB`\x82")
	r := citizen.raw("POST", "/media", png, map[string]string{"Content-Type": "image/jpeg"})
	if r.Status != 422 {
		t.Fatalf("declared/actual type mismatch must be rejected: %d", r.Status)
	}
	r = citizen.raw("POST", "/media", []byte("<script>alert(1)</script>"), map[string]string{"Content-Type": "text/html"})
	if r.Status != 415 {
		t.Fatalf("html must be refused: %d", r.Status)
	}
	var m struct {
		MediaID uuid.UUID `json:"media_id"`
		SHA256  string    `json:"sha256"`
	}
	citizen.do("POST", "/media", png, map[string]string{"Content-Type": "image/png"}, &m, 201)
	body := reportBody(35.705, 51.405)
	body["media_ids"] = []uuid.UUID{m.MediaID}
	citizen.do("POST", "/reports", body, idem(), nil, 202)
	r = other.raw("POST", "/reports", map[string]any{"type": "fire", "location": map[string]any{"lat": 35.7, "lng": 51.4, "accuracy_m": 5},
		"media_ids": []uuid.UUID{m.MediaID}}, idem())
	if r.Status != 422 {
		t.Fatalf("someone else's media must not attach: %d", r.Status)
	}
	other.do("GET", "/media/"+m.MediaID.String()+"/url", nil, nil, nil, 404)
	var u struct {
		URL string `json:"url"`
	}
	citizen.do("GET", "/media/"+m.MediaID.String()+"/url", nil, nil, &u, 200)
	resp, err := http.Get(u.URL)
	if err != nil || resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "image/png" {
		t.Fatalf("signed url fetch failed: %v %v", err, resp)
	}
	resp.Body.Close()
	resp, _ = http.Get(u.URL + "0")
	if resp.StatusCode != 403 {
		t.Fatalf("tampered signature must fail: %d", resp.StatusCode)
	}
	resp.Body.Close()
}

// GIS: feature collection carries freshness; impact areas are labelled estimates.
func TestGISFeatures(t *testing.T) {
	analyst := login(t, grant{Role: "GIS_ANALYST"})
	citizen := login(t)
	citizen.do("GET", "/gis/features", nil, nil, nil, 403)
	var fc struct {
		Type     string `json:"type"`
		Features []struct {
			Properties map[string]any `json:"properties"`
		} `json:"features"`
		Meta struct {
			Layers map[string]struct {
				Count      int `json:"count"`
				StaleCount int `json:"stale_count"`
			} `json:"layers"`
		} `json:"meta"`
	}
	analyst.do("GET", "/gis/features?layers=hospital,road_closure", nil, nil, &fc, 200)
	if fc.Type != "FeatureCollection" || fc.Meta.Layers["hospital"].Count < 1 || fc.Meta.Layers["road_closure"].StaleCount != 1 {
		t.Fatalf("unexpected layers %+v", fc.Meta.Layers)
	}
	var ia map[string]any
	analyst.do("POST", "/gis/impact-area", map[string]any{"center": map[string]any{"lat": 35.7, "lng": 51.4}, "radius_m": 2000,
		"uncertainty_m": 500, "source": "manual-estimate"}, nil, &ia, 201)
	if ia["estimate"] != true || ia["algorithm_version"] != "radial-buffer-v1" {
		t.Fatalf("impact area must be labelled as estimate: %v", ia)
	}
	analyst.do("GET", "/gis/features?bbox=0,0,100,100", nil, nil, nil, 422)
}

// Role administration: separation of duties and break-glass constraints.
func TestRoleAdministration(t *testing.T) {
	admin := login(t, grant{Role: "SECURITY_ADMIN"})
	cmd := login(t, grant{Role: "COMMANDER", OrgCode: "command"})
	target := login(t)
	cmd.do("POST", "/admin/users/"+target.id.String()+"/roles", map[string]any{"role": "OPERATOR", "reason": "x"}, nil, nil, 403)
	admin.do("POST", "/admin/users/"+admin.id.String()+"/roles", map[string]any{"role": "COMMANDER", "reason": "self"}, nil, nil, 422)
	admin.do("POST", "/admin/users/"+target.id.String()+"/roles", map[string]any{"role": "COMMANDER", "reason": "bg", "break_glass": true,
		"expires_at": time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)}, nil, nil, 422)
	var g struct {
		GrantID uuid.UUID `json:"grant_id"`
	}
	admin.do("POST", "/admin/users/"+target.id.String()+"/roles", map[string]any{"role": "COMMANDER", "reason": "شیفت اضطراری",
		"break_glass": true, "expires_at": time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339)}, nil, &g, 201)
	if countAudit(t, "role.break_glass_grant", "success", target.id.String()) != 1 {
		t.Fatal("break-glass must be audited")
	}
	time.Sleep(5 * time.Millisecond) // principal cache TTL in tests
	var me struct {
		Roles []string `json:"roles"`
	}
	target.do("GET", "/me", nil, nil, &me, 200)
	if len(me.Roles) != 1 || me.Roles[0] != "COMMANDER" {
		t.Fatalf("grant not effective: %v", me.Roles)
	}
	admin.do("DELETE", "/admin/users/"+target.id.String()+"/roles/"+g.GrantID.String()+"?reason=end", nil, nil, nil, 204)
}

func TestHealthEndpoints(t *testing.T) {
	for _, p := range []string{"/health/live", "/health/ready", "/metrics"} {
		resp, err := http.Get(server.URL + p)
		if err != nil || resp.StatusCode != 200 {
			t.Fatalf("%s: %v %v", p, err, resp)
		}
		resp.Body.Close()
	}
	resp, _ := http.Get(server.URL + "/api/v1/reports")
	if resp.StatusCode != 401 {
		t.Fatalf("unauthenticated request must be 401, got %d", resp.StatusCode)
	}
	resp.Body.Close()
	if !contains(fmt.Sprint(resp.Header), "X-Correlation-Id") {
		t.Fatal("correlation id header missing")
	}
}
