// Command consumer applies events from other services to the core (currently report.scored.v1 from AI assist).
// Delivery is at-least-once; the inbox makes the effect exactly-once. Poison messages go to <topic>.dlq.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/segmentio/kafka-go"

	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/inbox"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/outbox"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/platform/config"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/platform/db"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/platform/logx"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/report"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("config", "err", err.Error())
		os.Exit(1)
	}
	logx.Setup(cfg.LogLevel)
	if len(cfg.KafkaBrokers) == 0 {
		slog.Error("KAFKA_BROKERS is required")
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	pool, err := db.ConnectRetry(ctx, cfg.DatabaseURL, 90*time.Second)
	if err != nil {
		slog.Error("db", "err", err.Error())
		os.Exit(1)
	}
	defer pool.Close()
	dlq := &kafka.Writer{Addr: kafka.TCP(cfg.KafkaBrokers...), RequiredAcks: kafka.RequireAll,
		AllowAutoTopicCreation: true, WriteTimeout: 10 * time.Second}
	defer dlq.Close()
	c := &inbox.Consumer{Brokers: cfg.KafkaBrokers, GroupID: report.ScoredConsumer,
		Topic: outbox.Topic(cfg.TopicPrefix, "report.scored", 1), Handler: report.HandleScored(pool), DLQ: dlq}
	_ = c.Run(ctx)
}
