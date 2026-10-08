// Package outbox implements the Transactional Outbox (ADR-003): domain changes and their events are
// committed in one transaction; a relay publishes them to Kafka afterwards with retry, backoff and a DB-side DLQ.
package outbox

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/platform/httpx"
)

const Producer = "core-service"

type Aggregate struct {
	Type    string    `json:"type"`
	ID      uuid.UUID `json:"id"`
	Version int       `json:"version,omitempty"`
}

// Envelope is the versioned event contract (contracts/events/envelope.v1.json).
type Envelope struct {
	EventID       uuid.UUID       `json:"event_id"`
	EventType     string          `json:"event_type"`
	SchemaVersion int             `json:"schema_version"`
	Aggregate     Aggregate       `json:"aggregate"`
	OccurredAt    time.Time       `json:"occurred_at"`
	PublishedAt   *time.Time      `json:"published_at,omitempty"`
	Producer      string          `json:"producer"`
	CorrelationID string          `json:"correlation_id"`
	CausationID   string          `json:"causation_id,omitempty"`
	Payload       json.RawMessage `json:"payload"`
}

// Topic maps an event to its Kafka topic, e.g. crisis.report.created.v1.
func Topic(prefix, eventType string, schemaVersion int) string {
	return fmt.Sprintf("%s%s.v%d", prefix, eventType, schemaVersion)
}

type Event struct {
	Type          string
	SchemaVersion int
	Aggregate     Aggregate
	OccurredAt    time.Time
	CausationID   string
	Payload       any // must not contain media or unnecessary personal data
}

// Enqueue stores the event in the outbox within tx. It returns the event id.
func Enqueue(ctx context.Context, tx pgx.Tx, e Event) (uuid.UUID, error) {
	if e.SchemaVersion == 0 {
		e.SchemaVersion = 1
	}
	if e.OccurredAt.IsZero() {
		e.OccurredAt = time.Now().UTC()
	}
	payload, err := json.Marshal(e.Payload)
	if err != nil {
		return uuid.Nil, fmt.Errorf("marshal event payload: %w", err)
	}
	env := Envelope{
		EventID: uuid.New(), EventType: e.Type, SchemaVersion: e.SchemaVersion, Aggregate: e.Aggregate,
		OccurredAt: e.OccurredAt.UTC(), Producer: Producer, CorrelationID: httpx.CorrelationID(ctx),
		CausationID: e.CausationID, Payload: payload,
	}
	body, err := json.Marshal(env)
	if err != nil {
		return uuid.Nil, err
	}
	var ver *int
	if e.Aggregate.Version != 0 {
		ver = &e.Aggregate.Version
	}
	_, err = tx.Exec(ctx, `INSERT INTO outbox_events (id, aggregate_type, aggregate_id, aggregate_version, event_type,
		schema_version, payload) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		env.EventID, e.Aggregate.Type, e.Aggregate.ID, ver, e.Type, e.SchemaVersion, body)
	if err != nil {
		return uuid.Nil, fmt.Errorf("outbox insert: %w", err)
	}
	return env.EventID, nil
}
