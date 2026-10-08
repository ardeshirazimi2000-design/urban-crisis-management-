package outbox

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"math/rand/v2"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/segmentio/kafka-go"
)

type Message struct {
	ID    uuid.UUID
	Topic string
	Key   []byte
	Value []byte
}

// Publisher sends messages; the returned slice has one error (or nil) per message.
type Publisher interface {
	Publish(ctx context.Context, msgs []Message) []error
	Close() error
}

type Relay struct {
	Pool        *pgxpool.Pool
	Pub         Publisher
	TopicPrefix string
	BatchSize   int
	MaxAttempts int
	Interval    time.Duration
	Now         func() time.Time
}

func (r *Relay) defaults() {
	if r.BatchSize == 0 {
		r.BatchSize = 100
	}
	if r.MaxAttempts == 0 {
		r.MaxAttempts = 12
	}
	if r.Interval == 0 {
		r.Interval = 500 * time.Millisecond
	}
	if r.Now == nil {
		r.Now = time.Now
	}
}

// Run polls until ctx is cancelled.
func (r *Relay) Run(ctx context.Context) error {
	r.defaults()
	for {
		n, err := r.RunOnce(ctx)
		if err != nil && ctx.Err() == nil {
			slog.Error("outbox relay iteration failed", "err", err.Error())
		}
		if n == r.BatchSize {
			continue // backlog: keep draining
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(r.Interval):
		}
	}
}

// Backoff returns exponential backoff with full jitter, capped at 5 minutes.
func Backoff(attempt int) time.Duration {
	base := float64(time.Second) * math.Pow(2, float64(attempt-1))
	capped := math.Min(base, float64(5*time.Minute))
	return time.Duration(capped/2 + rand.Float64()*capped/2)
}

// RunOnce publishes one batch. Per-aggregate order is preserved: an event is eligible only if no earlier
// event of the same aggregate is still pending. Multiple relays can run concurrently (SKIP LOCKED).
func (r *Relay) RunOnce(ctx context.Context) (int, error) {
	r.defaults()
	tx, err := r.Pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	rows, err := tx.Query(ctx, `SELECT o.id, o.aggregate_id, o.event_type, o.schema_version, o.payload, o.attempts
		FROM outbox_events o
		WHERE o.published_at IS NULL AND o.dead_lettered_at IS NULL AND o.next_attempt_at <= now()
		  AND NOT EXISTS (SELECT 1 FROM outbox_events p WHERE p.aggregate_id = o.aggregate_id
		                  AND p.published_at IS NULL AND p.dead_lettered_at IS NULL AND p.seq < o.seq)
		ORDER BY o.seq LIMIT $1 FOR UPDATE SKIP LOCKED`, r.BatchSize)
	if err != nil {
		return 0, err
	}
	type pending struct {
		id       uuid.UUID
		attempts int
	}
	var msgs []Message
	var meta []pending
	for rows.Next() {
		var (
			id, aggID uuid.UUID
			typ       string
			ver, att  int
			payload   []byte
		)
		if err := rows.Scan(&id, &aggID, &typ, &ver, &payload, &att); err != nil {
			rows.Close()
			return 0, err
		}
		value, err := stampPublished(payload, r.Now().UTC())
		if err != nil {
			value = payload
		}
		msgs = append(msgs, Message{ID: id, Topic: Topic(r.TopicPrefix, typ, ver), Key: []byte(aggID.String()), Value: value})
		meta = append(meta, pending{id: id, attempts: att})
	}
	rows.Close()
	if len(msgs) == 0 {
		return 0, nil
	}
	errs := r.Pub.Publish(ctx, msgs)
	for i, m := range meta {
		var perr error
		if i < len(errs) {
			perr = errs[i]
		}
		if perr == nil {
			if _, err := tx.Exec(ctx, `UPDATE outbox_events SET published_at = now(), attempts = attempts + 1, last_error = NULL WHERE id = $1`, m.id); err != nil {
				return 0, err
			}
			continue
		}
		attempt := m.attempts + 1
		if attempt >= r.MaxAttempts {
			slog.Error("outbox event dead-lettered", "event_id", m.id.String(), "attempts", attempt, "err", perr.Error())
			_, err = tx.Exec(ctx, `UPDATE outbox_events SET attempts = $2, last_error = $3, dead_lettered_at = now() WHERE id = $1`,
				m.id, attempt, truncate(perr.Error(), 500))
		} else {
			_, err = tx.Exec(ctx, `UPDATE outbox_events SET attempts = $2, last_error = $3, next_attempt_at = now() + $4::interval WHERE id = $1`,
				m.id, attempt, truncate(perr.Error(), 500), fmt.Sprintf("%d milliseconds", Backoff(attempt).Milliseconds()))
		}
		if err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return len(msgs), nil
}

func stampPublished(payload []byte, t time.Time) ([]byte, error) {
	var env map[string]json.RawMessage
	if err := json.Unmarshal(payload, &env); err != nil {
		return nil, err
	}
	ts, _ := json.Marshal(t)
	env["published_at"] = ts
	return json.Marshal(env)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// ---------------------------------------------------------------------------
// Publishers
// ---------------------------------------------------------------------------

type KafkaPublisher struct{ w *kafka.Writer }

func NewKafkaPublisher(brokers []string) *KafkaPublisher {
	return &KafkaPublisher{w: &kafka.Writer{
		Addr:                   kafka.TCP(brokers...),
		Balancer:               &kafka.Hash{}, // key = aggregate id -> stable partition -> per-aggregate order
		RequiredAcks:           kafka.RequireAll,
		AllowAutoTopicCreation: true,
		BatchTimeout:           10 * time.Millisecond,
		WriteTimeout:           10 * time.Second,
	}}
}

func (p *KafkaPublisher) Publish(ctx context.Context, msgs []Message) []error {
	km := make([]kafka.Message, len(msgs))
	for i, m := range msgs {
		km[i] = kafka.Message{Topic: m.Topic, Key: m.Key, Value: m.Value,
			Headers: []kafka.Header{{Key: "event_id", Value: []byte(m.ID.String())}}}
	}
	errs := make([]error, len(msgs))
	err := p.w.WriteMessages(ctx, km...)
	if err == nil {
		return errs
	}
	if we, ok := err.(kafka.WriteErrors); ok {
		for i := range errs {
			if i < len(we) {
				errs[i] = we[i]
			}
		}
		return errs
	}
	for i := range errs {
		errs[i] = err
	}
	return errs
}

func (p *KafkaPublisher) Close() error { return p.w.Close() }

// LogPublisher is for local development without Kafka: it logs event metadata (never payloads).
type LogPublisher struct{}

func (LogPublisher) Publish(_ context.Context, msgs []Message) []error {
	for _, m := range msgs {
		slog.Info("event published (log publisher)", "topic", m.Topic, "event_id", m.ID.String())
	}
	return make([]error, len(msgs))
}

func (LogPublisher) Close() error { return nil }
