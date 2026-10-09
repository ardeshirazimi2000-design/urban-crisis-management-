// Command notifier delivers dispatched alerts via channel adapters, reconciles unknown states and expires alerts.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"
	_ "time/tzdata" // embedded zone data: Asia/Tehran without OS tzdata

	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/app"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/notification"
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
	pool, err := db.ConnectRetry(ctx, cfg.DatabaseURL, 90*time.Second)
	if err != nil {
		slog.Error("db", "err", err.Error())
		os.Exit(1)
	}
	defer pool.Close()
	slog.Info("notifier started")
	_ = (&notification.Worker{Pool: pool, Adapters: app.NewAdapters()}).Run(ctx)
}
