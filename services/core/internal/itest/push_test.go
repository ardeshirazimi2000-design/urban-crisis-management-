package itest

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/apiir"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/notification"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/push"
)

type pollResp struct {
	Messages []struct {
		ID    int64  `json:"id"`
		Title string `json:"title"`
		Body  string `json:"body"`
		Kind  string `json:"kind"`
	} `json:"messages"`
	LastID int64 `json:"last_id"`
}

// registerPush returns the user's topic and the current cursor.
func registerPush(t *testing.T, c *client) (string, int64) {
	t.Helper()
	var reg struct {
		Topic string `json:"topic"`
	}
	c.do("POST", "/me/push", nil, nil, &reg, 200)
	var again struct {
		Topic string `json:"topic"`
	}
	c.do("POST", "/me/push", nil, nil, &again, 200)
	if reg.Topic == "" || again.Topic != reg.Topic {
		t.Fatalf("topic must be stable per user: %q %q", reg.Topic, again.Topic)
	}
	var cur pollResp
	anon := &client{t: t}
	anon.do("GET", "/push/poll", nil, map[string]string{"X-Push-Topic": reg.Topic}, &cur, 200)
	return reg.Topic, cur.LastID
}

func pollOnce(t *testing.T, topic string, since int64) pollResp {
	t.Helper()
	var out pollResp
	(&client{t: t}).do("GET", "/push/poll?since="+strconv.FormatInt(since, 10), nil, map[string]string{"X-Push-Topic": topic}, &out, 200)
	return out
}

func setMobile(t *testing.T, id uuid.UUID, mobile string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `UPDATE users SET mobile=$2 WHERE id=$1`, id, mobile); err != nil {
		t.Fatal(err)
	}
}

func TestPushMissionNotice(t *testing.T) {
	ctx := context.Background()
	commander := login(t, grant{Role: "COMMANDER", OrgCode: "ems"}, grant{Role: "RESOURCE_MANAGER"})
	responder := login(t, grant{Role: "RESPONDER", OrgCode: "ems"})
	setMobile(t, responder.id, "09120000101")
	topic, cursor := registerPush(t, responder)

	// An unknown topic is refused; the topic is a capability, not a guessable id.
	if r := (&client{t: t}).raw("GET", "/push/poll?since=0", nil, map[string]string{"X-Push-Topic": strings.Repeat("A", 43)}); r.Status != 404 {
		t.Fatalf("unknown topic: %d", r.Status)
	}

	in := createIncident(t, commander, "ems", "open")
	var res struct {
		ID uuid.UUID `json:"id"`
	}
	commander.do("POST", "/resources", map[string]any{"organization_id": orgID(t, "ems"), "type": "ambulance", "name": "آمبولانس پوش",
		"location": map[string]any{"lat": 35.7, "lng": 51.4}}, nil, &res, 201)
	var asg struct {
		ID      uuid.UUID `json:"id"`
		Version int       `json:"version"`
	}
	commander.do("POST", "/incidents/"+in.ID.String()+"/assignments",
		map[string]any{"resource_id": res.ID, "reason": "اعزام", "assignee_id": responder.id}, nil, &asg, 201)

	got := pollOnce(t, topic, cursor)
	if len(got.Messages) != 1 || got.Messages[0].Kind != "assignment" || got.Messages[0].Title != "مأموریت جدید" ||
		!strings.Contains(got.Messages[0].Body, in.Code) {
		t.Fatalf("mission push: %+v", got)
	}

	// The SMS goes out through the outbox.
	mock := apiir.NewMock(false)
	svc := &push.Service{Pool: pool, SMS: mock}
	if _, err := svc.SendDueSMS(ctx); err != nil {
		t.Fatal(err)
	}
	if !smsTo(mock, "09120000101", "مأموریت جدید") {
		t.Fatalf("mission SMS not sent: %v", mock.SMS)
	}

	// Cancelling by the commander notifies the responder.
	commander.do("POST", "/assignments/"+asg.ID.String()+"/status", map[string]any{"status": "cancelled", "reason": "تغییر اولویت",
		"version": asg.Version}, nil, nil, 200)
	got = pollOnce(t, topic, got.LastID)
	if len(got.Messages) != 1 || got.Messages[0].Title != "لغو مأموریت" {
		t.Fatalf("cancel push: %+v", got)
	}

	// Signing out revokes the topic.
	responder.do("DELETE", "/me/push", nil, nil, nil, 204)
	if r := (&client{t: t}).raw("GET", "/push/poll?since=0", nil, map[string]string{"X-Push-Topic": topic}); r.Status != 404 {
		t.Fatalf("revoked topic still polls: %d", r.Status)
	}
}

func smsTo(m *apiir.Mock, mobile, text string) bool {
	for _, s := range m.SMS {
		if strings.Contains(s, mobile) && strings.Contains(s, text) {
			return true
		}
	}
	return false
}

func TestPushAlertToPeopleInArea(t *testing.T) {
	ctx := context.Background()
	inside := login(t, grant{Role: "CITIZEN"})
	outside := login(t, grant{Role: "CITIZEN"})
	notListed := login(t, grant{Role: "CITIZEN"})
	setMobile(t, inside.id, "09120000201")
	setMobile(t, outside.id, "09120000202")
	setMobile(t, notListed.id, "09120000203")
	inTopic, inCur := registerPush(t, inside)
	outTopic, outCur := registerPush(t, outside)
	nlTopic, nlCur := registerPush(t, notListed)

	// Fetching public alerts records where the app user is (draftAlert's area covers 51.38–51.42, 35.69–35.72).
	inside.do("GET", "/alerts/public?lat=35.70&lng=51.40", nil, nil, nil, 200)
	notListed.do("GET", "/alerts/public?lat=35.705&lng=51.405", nil, nil, nil, 200)
	outside.do("GET", "/alerts/public?lat=35.80&lng=51.60", nil, nil, nil, 200)

	op := login(t, grant{Role: "OPERATOR", OrgCode: "command"})
	cmd := login(t, grant{Role: "COMMANDER", OrgCode: "command"})
	a := draftAlert(t, op, []string{"push", "sms"}) // test mode: only listed test mobiles receive it
	op.do("POST", "/alerts/"+a.ID.String()+"/submit", map[string]any{"version": 1}, nil, &a, 200)
	cmd.do("POST", "/alerts/"+a.ID.String()+"/approve", map[string]any{"version": a.Version, "reason": "تأیید"}, nil, &a, 200)
	cmd.do("POST", "/alerts/"+a.ID.String()+"/dispatch", nil, idem(), &a, 202)

	mock := apiir.NewMock(false)
	test := []string{"09120000201", "09120000202"}
	w := &notification.Worker{Pool: pool, Adapters: map[string]notification.Adapter{
		"push": &push.AlertAdapter{Pool: pool, Name: "push", TestMobiles: test},
		"sms":  &push.AlertAdapter{Pool: pool, Name: "sms", SMS: mock, TestMobiles: test},
	}}
	for i := 0; i < 3; i++ {
		if _, err := w.RunOnce(ctx); err != nil {
			t.Fatal(err)
		}
	}

	if got := pollOnce(t, inTopic, inCur); len(got.Messages) != 1 || got.Messages[0].Kind != "alert" {
		t.Fatalf("listed user inside the area must get the alert: %+v", got)
	}
	if !smsTo(mock, "09120000201", "همراه پاد") {
		t.Fatalf("SMS to the user inside the area missing: %v", mock.SMS)
	}
	// Outside the area, or not a listed test mobile: nothing (polls return empty after the wait, so check the queue).
	var n int
	pool.QueryRow(ctx, `SELECT count(*) FROM push_messages WHERE topic = ANY($1) AND id > $2`, []string{outTopic, nlTopic}, min(outCur, nlCur)).Scan(&n)
	if n != 0 || smsTo(mock, "09120000202", "") || smsTo(mock, "09120000203", "") {
		t.Fatalf("alert leaked outside the area or to an unlisted test recipient: push=%d sms=%v", n, mock.SMS)
	}

	// A retry never sends twice to someone already reached.
	ad := &push.AlertAdapter{Pool: pool, Name: "sms", SMS: mock, TestMobiles: test}
	before := len(mock.SMS)
	if r := ad.Send(ctx, notification.Message{AlertID: a.ID.String(), Mode: "test", Text: "x"}); r.Outcome != notification.Accepted || r.Code != "SMS_0" {
		t.Fatalf("retry: %+v", r)
	}
	if len(mock.SMS) != before {
		t.Fatal("retry re-sent SMS")
	}
}

// A waiting poll returns as soon as a message is published (LISTEN/NOTIFY), not after the full wait.
func TestPushPollWakesOnPublish(t *testing.T) {
	u := login(t, grant{Role: "CITIZEN"})
	topic, cur := registerPush(t, u)
	done := make(chan pollResp, 1)
	start := time.Now()
	go func() { done <- pollOnce(t, topic, cur) }()
	time.Sleep(500 * time.Millisecond)
	if err := push.Publish(context.Background(), pool, u.id, "عنوان", "متن", "alert", "x"); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-done:
		if len(got.Messages) != 1 || time.Since(start) > 5*time.Second {
			t.Fatalf("poll: %+v after %s", got, time.Since(start))
		}
	case <-time.After(push.PollWait + 5*time.Second):
		t.Fatal("poll did not return")
	}
}
