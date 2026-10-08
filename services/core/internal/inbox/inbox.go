// Package inbox makes event consumption idempotent (at-least-once delivery, exactly-once effect)
// and provides a Kafka consumer loop with bounded retry and a dead-letter topic.
package inbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/segmentio/kafka-go"

	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/outbox"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/platform/httpx"
)

// Process runs fn exactly once per (consumer, eventID): the dedupe record and the handler's writes commit together.
// It returns duplicate=true without calling fn if the event was already processed.
func Process(ctx context.Context, pool *pgxpool.Pool, consumer string, eventID uuid.UUID, fn func(tx pgx.Tx) error) (duplicate bool, err error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	tag, err := tx.Exec(ctx, `INSERT INTO inbox_events (consumer, event_id) VALUES ($1,$2) ON CONFLICT DO NOTHING`, consumer, eventID)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() == 0 {
		return true, nil
	}
	if err := fn(tx); err != nil {
		return false, err
	}
	return false, tx.Commit(ctx)
}

// PermanentError marks a failure that retrying cannot fix (bad schema, unknown aggregate); it goes to the DLQ.
type PermanentError struct{ Err error }

func (e PermanentError) Error() string { return "permanent: " + e.Err.Error() }
func (e PermanentError) Unwrap() error { return e.Err }

type Handler func(ctx context.Context, env outbox.Envelope) error

type Consumer struct {
	Brokers    []string
	GroupID    string
	Topic      string
	Handler    Handler
	MaxRetries int
	DLQ        *kafka.Writer
}

// Run consumes until ctx is cancelled. Offsets are committed only after the handler succeeded
// or the message was parked in the DLQ.
func (c *Consumer) Run(ctx context.Context) error {
	if c.MaxRetries == 0 {
		c.MaxRetries = 5
	}
	// A group reader started before its topic exists never gets partitions assigned; create it up front
	// and keep watching for partition changes.
	if err := EnsureTopic(ctx, c.Brokers[0], c.Topic, 3); err != nil {
		slog.Warn("ensure topic failed", "topic", c.Topic, "err", err.Error())
	}
	r := kafka.NewReader(kafka.ReaderConfig{Brokers: c.Brokers, GroupID: c.GroupID, Topic: c.Topic,
		MinBytes: 1, MaxBytes: 10e6, MaxWait: 500 * time.Millisecond, StartOffset: kafka.FirstOffset,
		WatchPartitionChanges: true, PartitionWatchInterval: 5 * time.Second})
	defer r.Close()
	slog.Info("consumer started", "topic", c.Topic, "group", c.GroupID)
	for {
		msg, err := r.FetchMessage(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			slog.Error("fetch failed", "err", err.Error())
			time.Sleep(time.Second)
			continue
		}
		c.handle(ctx, msg)
		if ctx.Err() != nil {
			return nil
		}
		if err := r.CommitMessages(ctx, msg); err != nil && ctx.Err() == nil {
			slog.Error("commit failed", "err", err.Error())
		}
	}
}

func (c *Consumer) handle(ctx context.Context, msg kafka.Message) {
	var env outbox.Envelope
	err := json.Unmarshal(msg.Value, &env)
	if err != nil || env.EventID == uuid.Nil {
		c.deadLetter(ctx, msg, fmt.Errorf("invalid envelope: %v", err))
		return
	}
	hctx := httpx.WithCorrelationID(ctx, env.CorrelationID)
	for attempt := 1; ; attempt++ {
		err = c.Handler(hctx, env)
		if err == nil {
			return
		}
		var perm PermanentError
		if errors.As(err, &perm) || attempt > c.MaxRetries {
			c.deadLetter(ctx, msg, err)
			return
		}
		slog.Warn("handler failed; retrying", "event_id", env.EventID.String(), "attempt", attempt, "err", err.Error())
		select {
		case <-ctx.Done():
			return
		case <-time.After(outbox.Backoff(attempt)):
		}
	}
}

func (c *Consumer) deadLetter(ctx context.Context, msg kafka.Message, cause error) {
	slog.Error("message dead-lettered", "topic", msg.Topic, "partition", msg.Partition, "offset", msg.Offset, "err", cause.Error())
	if c.DLQ == nil {
		return
	}
	err := c.DLQ.WriteMessages(ctx, kafka.Message{Topic: msg.Topic + ".dlq", Key: msg.Key, Value: msg.Value,
		Headers: append(msg.Headers, kafka.Header{Key: "dlq_error", Value: []byte(cause.Error())},
			kafka.Header{Key: "dlq_consumer", Value: []byte(c.GroupID)})})
	if err != nil {
		slog.Error("dlq write failed", "err", err.Error())
	}
}

// EnsureTopic creates the topic if missing (idempotent). Production clusters should pre-provision topics
// with explicit partitions, replication and ACLs; this keeps local/dev setups self-healing.
func EnsureTopic(ctx context.Context, broker, topic string, partitions int) error {
	conn, err := kafka.DialContext(ctx, "tcp", broker)
	if err != nil {
		return err
	}
	defer conn.Close()
	ctrl, err := conn.Controller()
	if err != nil {
		return err
	}
	cc, err := kafka.DialContext(ctx, "tcp", net.JoinHostPort(ctrl.Host, strconv.Itoa(ctrl.Port)))
	if err != nil {
		return err
	}
	defer cc.Close()
	err = cc.CreateTopics(kafka.TopicConfig{Topic: topic, NumPartitions: partitions, ReplicationFactor: 1})
	if err != nil && !errors.Is(err, kafka.TopicAlreadyExists) {
		return err
	}
	return nil
}
