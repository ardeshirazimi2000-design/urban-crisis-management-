package itest

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/inbox"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/outbox"
)

// TestKafkaRoundTrip publishes outbox events through a real broker and consumes them with the inbox consumer,
// delivering each message twice to prove exactly-once effect. Runs only when TEST_KAFKA_BROKERS is set.
func TestKafkaRoundTrip(t *testing.T) {
	brokers := os.Getenv("TEST_KAFKA_BROKERS")
	if brokers == "" {
		t.Skip("TEST_KAFKA_BROKERS not set")
	}
	prefix := "it" + strings.ReplaceAll(uuid.NewString()[:8], "-", "") + "."
	citizen := login(t)
	var created struct {
		ReportID uuid.UUID `json:"report_id"`
	}
	citizen.do("POST", "/reports", reportBody(35.709, 51.409), idem(), &created, 202)

	pub := outbox.NewKafkaPublisher(strings.Split(brokers, ","))
	defer pub.Close()
	topic := outbox.Topic(prefix, "report.created", 1)
	if err := inbox.EnsureTopic(context.Background(), strings.Split(brokers, ",")[0], topic, 1); err != nil {
		t.Fatal(err)
	}
	// Only relay this test's aggregate (other tests' events would auto-create unrelated topics).
	pool.Exec(context.Background(), `UPDATE outbox_events SET published_at=now() WHERE published_at IS NULL AND aggregate_id<>$1`, created.ReportID)
	drain(t, &outbox.Relay{Pool: pool, Pub: pub, TopicPrefix: prefix})
	// Re-publish the same events (simulates relay crash after send, before marking published).
	pool.Exec(context.Background(), `UPDATE outbox_events SET published_at=NULL WHERE aggregate_id=$1`, created.ReportID)
	drain(t, &outbox.Relay{Pool: pool, Pub: pub, TopicPrefix: prefix})

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	effects := 0
	seen := 0
	c := &inbox.Consumer{Brokers: strings.Split(brokers, ","), GroupID: prefix + "g", Topic: topic,
		Handler: func(hctx context.Context, env outbox.Envelope) error {
			var p map[string]any
			json.Unmarshal(env.Payload, &p)
			if p["report_id"] != created.ReportID.String() {
				return nil
			}
			seen++
			dup, err := inbox.Process(hctx, pool, prefix+"consumer", env.EventID, func(tx pgx.Tx) error { effects++; return nil })
			_ = dup
			if seen == 2 {
				cancel()
			}
			return err
		}}
	c.Run(ctx)
	if seen != 2 || effects != 1 {
		t.Fatalf("expected 2 deliveries and 1 effect, got seen=%d effects=%d", seen, effects)
	}
}
