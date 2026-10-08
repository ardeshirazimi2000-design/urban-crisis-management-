// Command api serves the core REST API. For a single-process local setup it can also run the
// outbox relay and notifier in-process (RUN_RELAY / RUN_NOTIFIER); in production run them separately.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/app"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/notification"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/outbox"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/platform/config"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/platform/db"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/platform/logx"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/migrations"
)

func main() {
	if err := run(); err != nil {
		slog.Error("fatal", "err", err.Error())
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	logx.Setup(cfg.LogLevel)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	if cfg.AutoMigrate {
		applied, err := db.Migrate(ctx, pool, migrations.FS)
		if err != nil {
			return err
		}
		slog.Info("migrations applied", "versions", applied)
	}
	verifier, err := app.NewVerifier(ctx, cfg)
	if err != nil {
		return err
	}
	store, err := app.NewStore(ctx, cfg)
	if err != nil {
		return err
	}
	a := app.New(cfg, pool, verifier, store)

	var wg sync.WaitGroup
	workerCtx, cancelWorkers := context.WithCancel(context.Background())
	defer cancelWorkers()
	if b, _ := strconv.ParseBool(os.Getenv("RUN_RELAY")); b {
		pub := app.NewPublisher(cfg)
		defer pub.Close()
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = (&outbox.Relay{Pool: pool, Pub: pub, TopicPrefix: cfg.TopicPrefix}).Run(workerCtx)
		}()
	}
	if b, _ := strconv.ParseBool(os.Getenv("RUN_NOTIFIER")); b {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = (&notification.Worker{Pool: pool, Adapters: app.NewAdapters()}).Run(workerCtx)
		}()
	}

	srv := &http.Server{Addr: cfg.HTTPAddr, Handler: a.Handler, ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 120 * time.Second, MaxHeaderBytes: 32 << 10}
	errCh := make(chan error, 1)
	go func() {
		slog.Info("api listening", "addr", cfg.HTTPAddr, "env", cfg.AppEnv, "auth_mode", cfg.AuthMode)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()
	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}
	// Graceful shutdown: fail readiness, let the load balancer notice, then drain in-flight requests.
	slog.Info("shutting down")
	a.Drain()
	time.Sleep(time.Duration(envInt("SHUTDOWN_DRAIN_SECONDS", 3)) * time.Second)
	shCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	err = srv.Shutdown(shCtx)
	cancelWorkers()
	wg.Wait()
	return err
}

func envInt(k string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(k)); err == nil {
		return v
	}
	return def
}
