// Command relay publishes committed outbox events to Kafka (at-least-once, per-aggregate ordered).
// Several replicas may run concurrently (SKIP LOCKED).
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/app"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/outbox"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/platform/config"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/platform/db"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/platform/logx"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("config", "err", err.Error())
		os.Exit(1)
	}
	logx.Setup(cfg.LogLevel)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		slog.Error("db", "err", err.Error())
		os.Exit(1)
	}
	defer pool.Close()
	pub := app.NewPublisher(cfg)
	defer pub.Close()
	slog.Info("outbox relay started", "publisher", cfg.Publisher)
	_ = (&outbox.Relay{Pool: pool, Pub: pub, TopicPrefix: cfg.TopicPrefix}).Run(ctx)
}
