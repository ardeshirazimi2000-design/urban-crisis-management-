// Package push is the platform's self-hosted notification service: every app user gets an unguessable topic that
// the app's background service long-polls (no Google/third-party push), and an SMS outbox delivers mission
// notices through api.ir. Public alerts reach citizens through the push and SMS adapters in alerts.go.
package push

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/apiir"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/auth"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/platform/httpx"
)

// PollWait is how long a poll waits for a message; below the server's 30 s write timeout and typical proxy limits.
const PollWait = 20 * time.Second

// Execer is satisfied by a transaction and by the pool.
type Execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

type Service struct {
	Pool       *pgxpool.Pool
	SMS        apiir.Client
	MissionSMS bool // mission notices also go out by SMS

	brokerOnce sync.Once
	mu         sync.Mutex
	waiters    map[string][]chan struct{}
}

var topicRe = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

func (s *Service) Routes(r *httpx.Router) {
	r.Auth("POST /me/push", s.register)
	r.Auth("DELETE /me/push", s.unregister)
	r.Public("GET /push/poll", s.poll)
}

func newTopic() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// register returns the caller's topic, creating it on first use.
func (s *Service) register(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	p := auth.FromContext(ctx)
	var topic string
	err := s.Pool.QueryRow(ctx, `INSERT INTO push_topics (user_id, topic) VALUES ($1, $2)
		ON CONFLICT (user_id) DO UPDATE SET user_id = EXCLUDED.user_id RETURNING topic`, p.UserID, newTopic()).Scan(&topic)
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"topic": topic, "poll_path": "/push/poll", "wait_seconds": int(PollWait.Seconds())})
	return nil
}

// unregister drops the caller's topic (sign-out); a later register issues a new one.
func (s *Service) unregister(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	if _, err := s.Pool.Exec(ctx, `DELETE FROM push_topics WHERE user_id=$1`, auth.FromContext(ctx).UserID); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

type Message struct {
	ID        int64     `json:"id"`
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	Kind      string    `json:"kind"`
	Ref       *string   `json:"ref"`
	CreatedAt time.Time `json:"created_at"`
}

// poll is the long-poll used by the apps' background service. The topic travels in the X-Push-Topic header (not the
// URL) so it stays out of access logs. Without ?since the call only returns the current cursor.
func (s *Service) poll(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	topic := r.Header.Get("X-Push-Topic")
	if !topicRe.MatchString(topic) {
		return httpx.ErrNotFound
	}
	tag, err := s.Pool.Exec(ctx, `UPDATE push_topics SET last_poll_at=now() WHERE topic=$1`, topic)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return httpx.ErrNotFound // unknown or revoked: the app registers again
	}
	sinceRaw := r.URL.Query().Get("since")
	if sinceRaw == "" {
		var last int64
		if err := s.Pool.QueryRow(ctx, `SELECT COALESCE(max(id), 0) FROM push_messages WHERE topic=$1`, topic).Scan(&last); err != nil {
			return err
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"messages": []Message{}, "last_id": last})
		return nil
	}
	since, err := strconv.ParseInt(sinceRaw, 10, 64)
	if err != nil || since < 0 {
		return httpx.Validation(httpx.FieldDetail{Field: "since", Reason: "invalid"})
	}
	msgs, err := s.fetch(ctx, topic, since)
	if err != nil {
		return err
	}
	if len(msgs) == 0 {
		s.wait(ctx, topic, PollWait)
		if ctx.Err() != nil {
			return nil
		}
		if msgs, err = s.fetch(ctx, topic, since); err != nil {
			return err
		}
	}
	last := since
	for _, m := range msgs {
		last = max(last, m.ID)
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"messages": msgs, "last_id": last})
	return nil
}

func (s *Service) fetch(ctx context.Context, topic string, since int64) ([]Message, error) {
	rows, err := s.Pool.Query(ctx, `SELECT id, title, body, kind, ref, created_at FROM push_messages
		WHERE topic=$1 AND id>$2 ORDER BY id LIMIT 50`, topic, since)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Message, error) {
		var m Message
		err := row.Scan(&m.ID, &m.Title, &m.Body, &m.Kind, &m.Ref, &m.CreatedAt)
		return m, err
	})
}

// wait blocks until a message for topic is published (any API instance, via LISTEN/NOTIFY), d passes or ctx ends.
func (s *Service) wait(ctx context.Context, topic string, d time.Duration) {
	s.brokerOnce.Do(func() { go s.listen(context.Background()) })
	ch := make(chan struct{}, 1)
	s.mu.Lock()
	if s.waiters == nil {
		s.waiters = map[string][]chan struct{}{}
	}
	s.waiters[topic] = append(s.waiters[topic], ch)
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		list := s.waiters[topic]
		for i, c := range list {
			if c == ch {
				list = append(list[:i], list[i+1:]...)
				break
			}
		}
		if len(list) == 0 {
			delete(s.waiters, topic)
		} else {
			s.waiters[topic] = list
		}
		s.mu.Unlock()
	}()
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ch:
	case <-t.C:
	case <-ctx.Done():
	}
}

func (s *Service) wake(topic string) {
	s.mu.Lock()
	for _, c := range s.waiters[topic] {
		select {
		case c <- struct{}{}:
		default:
		}
	}
	s.mu.Unlock()
}

// listen holds one connection on LISTEN push and wakes local waiters; it reconnects after errors.
func (s *Service) listen(ctx context.Context) {
	for ctx.Err() == nil {
		if err := s.listenOnce(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("push listener", "err", err.Error())
			time.Sleep(2 * time.Second)
		}
	}
}

// The listener uses its own connection, outside the pool, so closing the pool at shutdown never waits for it.
func (s *Service) listenOnce(ctx context.Context) error {
	conn, err := pgx.ConnectConfig(ctx, s.Pool.Config().ConnConfig.Copy())
	if err != nil {
		return err
	}
	defer conn.Close(context.Background()) //nolint:errcheck
	if _, err := conn.Exec(ctx, "LISTEN push"); err != nil {
		return err
	}
	for {
		n, err := conn.WaitForNotification(ctx)
		if err != nil {
			return err
		}
		s.wake(n.Payload)
	}
}

// Publish queues a push message for a user inside the caller's transaction; users without a topic are skipped.
func Publish(ctx context.Context, q Execer, userID uuid.UUID, title, body, kind, ref string) error {
	_, err := q.Exec(ctx, `WITH t AS (SELECT topic FROM push_topics WHERE user_id=$1),
		ins AS (INSERT INTO push_messages (topic, title, body, kind, ref) SELECT topic, $2, $3, $4, $5 FROM t RETURNING topic)
		SELECT pg_notify('push', topic) FROM ins`, userID, title, body, kind, ref)
	return err
}

// EnqueueSMS queues an SMS to a user's registered mobile inside the caller's transaction.
func EnqueueSMS(ctx context.Context, q Execer, userID uuid.UUID, message, kind, ref string) error {
	_, err := q.Exec(ctx, `INSERT INTO sms_outbox (user_id, mobile, message, kind, ref)
		SELECT id, mobile, $2, $3, $4 FROM users WHERE id=$1 AND mobile IS NOT NULL AND mobile <> '' AND status='active'`,
		userID, message, kind, ref)
	return err
}

// NotifyUser sends a push message and, when enabled, the same text by SMS. A nil Service does nothing.
func (s *Service) NotifyUser(ctx context.Context, q Execer, userID uuid.UUID, title, body, kind, ref string) error {
	if s == nil {
		return nil
	}
	if err := Publish(ctx, q, userID, title, body, kind, ref); err != nil {
		return err
	}
	if s.MissionSMS {
		return EnqueueSMS(ctx, q, userID, "همراه پاد — "+title+": "+body, kind, ref)
	}
	return nil
}

// Run sends queued SMS and purges old push data until ctx ends.
func (s *Service) Run(ctx context.Context) {
	tick := time.NewTicker(3 * time.Second)
	defer tick.Stop()
	purge := time.NewTicker(15 * time.Minute)
	defer purge.Stop()
	s.purge(ctx)
	for {
		if _, err := s.SendDueSMS(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("sms outbox", "err", err.Error())
		}
		select {
		case <-ctx.Done():
			return
		case <-purge.C:
			s.purge(ctx)
		case <-tick.C:
		}
	}
}

func (s *Service) purge(ctx context.Context) {
	for _, q := range []string{
		`DELETE FROM push_messages WHERE created_at < now() - interval '48 hours'`,
		`DELETE FROM user_locations WHERE updated_at < now() - interval '72 hours'`,
		`DELETE FROM sms_outbox WHERE created_at < now() - interval '30 days'`,
		// A worker that died mid-send leaves rows in 'sending'; retry them (provider may then see a duplicate).
		`UPDATE sms_outbox SET status='pending', next_attempt_at=now() WHERE status='sending' AND next_attempt_at < now() - interval '5 minutes'`,
	} {
		if _, err := s.Pool.Exec(ctx, q); err != nil && ctx.Err() == nil {
			slog.WarnContext(ctx, "push maintenance", "err", err.Error())
		}
	}
}

// SendDueSMS sends up to 20 due SMS and returns how many were attempted.
func (s *Service) SendDueSMS(ctx context.Context) (int, error) {
	if s.SMS == nil {
		return 0, errors.New("no SMS client")
	}
	type row struct {
		id       int64
		mobile   string
		message  string
		attempts int
	}
	rows, err := s.Pool.Query(ctx, `UPDATE sms_outbox SET status='sending', attempts=attempts+1, next_attempt_at=now()
		WHERE id IN (SELECT id FROM sms_outbox WHERE status='pending' AND next_attempt_at <= now()
		             ORDER BY next_attempt_at LIMIT 20 FOR UPDATE SKIP LOCKED)
		RETURNING id, mobile, message, attempts`)
	if err != nil {
		return 0, err
	}
	batch, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (row, error) {
		var x row
		err := r.Scan(&x.id, &x.mobile, &x.message, &x.attempts)
		return x, err
	})
	if err != nil {
		return 0, err
	}
	for _, x := range batch {
		sctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		err := s.SMS.SendSMS(sctx, []string{x.mobile}, x.message)
		cancel()
		if err == nil {
			_, err = s.Pool.Exec(ctx, `UPDATE sms_outbox SET status='sent', sent_at=now(), last_error=NULL WHERE id=$1`, x.id)
			if err != nil {
				return len(batch), err
			}
			continue
		}
		status, backoff := "pending", time.Duration(1<<min(x.attempts, 6))*30*time.Second
		if x.attempts >= 5 {
			status = "failed"
		}
		slog.WarnContext(ctx, "sms send failed", "id", x.id, "attempt", x.attempts, "err", err.Error())
		if _, err := s.Pool.Exec(ctx, `UPDATE sms_outbox SET status=$2, last_error=$3, next_attempt_at=now() + $4::interval WHERE id=$1`,
			x.id, status, truncate(err.Error(), 300), fmt.Sprintf("%d seconds", int(backoff.Seconds()))); err != nil {
			return len(batch), err
		}
	}
	return len(batch), nil
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
