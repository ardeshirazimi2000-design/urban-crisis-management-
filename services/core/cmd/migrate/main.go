// Command migrate applies embedded SQL migrations, and with -seed loads local demo data (never in production).
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/app"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/audit"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/guidance"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/identity"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/platform/config"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/platform/db"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/platform/logx"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/migrations"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/seeds"
)

func main() {
	seed := flag.Bool("seed", false, "load local demo seed data (APP_ENV=local only)")
	sealExisting := flag.Bool("seal-existing", false, "seal incidents and alerts created before the integrity migration (run once after upgrading)")
	bootstrap := flag.Bool("bootstrap-admin", false, "create the first security administrator from BOOTSTRAP_ADMIN_* after a Shahkar match")
	flag.Parse()
	cfg, err := config.Load()
	if err != nil {
		slog.Error("config", "err", err.Error())
		os.Exit(1)
	}
	db.IntegrityKey = cfg.IntegrityKey
	// Every process that writes audit records must sign them with the same key as the API.
	audit.Configure(cfg.AuditKey, cfg.AuditFallback)
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
	if *sealExisting {
		n, err := app.New(cfg, pool, nil, nil).Compliance.SealExisting(ctx)
		if err != nil {
			slog.Error("seal existing", "err", err.Error())
			os.Exit(1)
		}
		slog.Info("existing records sealed", "count", n)
	}
	if *bootstrap {
		a := app.New(cfg, pool, nil, nil)
		temp, err := a.Identity.Bootstrap(ctx, identity.BootstrapAdmin{
			NationalCode: os.Getenv("BOOTSTRAP_ADMIN_NATIONAL_CODE"), Mobile: os.Getenv("BOOTSTRAP_ADMIN_MOBILE"),
			Username: os.Getenv("BOOTSTRAP_ADMIN_USERNAME"), FirstName: os.Getenv("BOOTSTRAP_ADMIN_FIRST_NAME"),
			LastName: os.Getenv("BOOTSTRAP_ADMIN_LAST_NAME")})
		if err != nil {
			slog.Error("bootstrap admin", "err", err.Error())
			os.Exit(1)
		}
		// Printed once on the terminal only (never logged); it must be changed at first sign-in.
		fmt.Printf("\nSecurity administrator %q created.\nTemporary password (shown once): %s\n\n", os.Getenv("BOOTSTRAP_ADMIN_USERNAME"), temp)
	}
	if *seed {
		if !cfg.IsLocal() {
			slog.Error("refusing to seed outside APP_ENV=local|test")
			os.Exit(1)
		}
		if _, err := pool.Exec(ctx, seeds.Dev); err != nil {
			slog.Error("seed", "err", err.Error())
			os.Exit(1)
		}
		n, err := guidance.Seed(ctx, pool, seeds.GuidanceFA)
		if err != nil {
			slog.Error("guidance seed", "err", err.Error())
			os.Exit(1)
		}
		slog.Info("dev seed loaded", "guidance_cards_added", n)
	}
}
