// Command migrate applies embedded SQL migrations, and with -seed loads local demo data (never in production).
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"time"

	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/platform/config"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/platform/db"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/platform/logx"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/migrations"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/seeds"
)

func main() {
	seed := flag.Bool("seed", false, "load local demo seed data (APP_ENV=local only)")
	flag.Parse()
	cfg, err := config.Load()
	if err != nil {
		slog.Error("config", "err", err.Error())
		os.Exit(1)
	}
	logx.Setup(cfg.LogLevel)
	ctx := context.Background()
	pool, err := db.ConnectRetry(ctx, cfg.DatabaseURL, 90*time.Second)
	if err != nil {
		slog.Error("db", "err", err.Error())
		os.Exit(1)
	}
	defer pool.Close()
	applied, err := db.Migrate(ctx, pool, migrations.FS)
	if err != nil {
		slog.Error("migrate", "err", err.Error())
		os.Exit(1)
	}
	slog.Info("migrations applied", "versions", applied)
	if *seed {
		if !cfg.IsLocal() {
			slog.Error("refusing to seed outside APP_ENV=local|test")
			os.Exit(1)
		}
		if _, err := pool.Exec(ctx, seeds.Dev); err != nil {
			slog.Error("seed", "err", err.Error())
			os.Exit(1)
		}
		slog.Info("dev seed loaded")
	}
}
