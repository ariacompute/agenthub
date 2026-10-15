// Command agenthub-server is the single agenthub binary: it wires PostgreSQL,
// the resource registry, the mock adapters and the A2A hub behind one HTTP
// listener that also hosts the Vue3 console.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"agenthub/internal/adapter"
	"agenthub/internal/config"
	"agenthub/internal/handlers"
	"agenthub/internal/hub"
	"agenthub/internal/registry"
	"agenthub/internal/store"
)

func main() {
	config.LoadDotEnv()

	cfg, err := config.Load()
	if err != nil {
		slog.Error("config", "error", err)
		os.Exit(1)
	}
	setupLogger(cfg)

	db, err := store.Open(cfg.DatabaseURL, cfg.Env == "dev")
	if err != nil {
		slog.Error("database", "error", err)
		os.Exit(1)
	}
	defer func() {
		if err := store.Close(db); err != nil {
			slog.Error("database close", "error", err)
		}
	}()
	if err := store.Migrate(db); err != nil {
		slog.Error("migrate", "error", err)
		os.Exit(1)
	}
	if err := store.Seed(db); err != nil {
		slog.Error("seed", "error", err)
		os.Exit(1)
	}

	reg := registry.NewWithStore(store.NewResourceStore(db))

	opts := adapter.Options{
		CLITimeout:     cfg.CLITimeout,
		CLIAllowlist:   cfg.CLIAllowlist,
		MaxOutputBytes: cfg.MaxOutputBytes,
	}
	pusher := &hub.MockNotifier{Repo: store.NewPushRepository(db)}
	h := hub.New(cfg, reg, opts, pusher, hub.Persistence{
		Tasks:  store.NewTaskRepository(db),
		Events: store.NewEventRepository(db),
		Pushes: pusher.Repo,
	})
	defer h.Close()

	deps := handlers.Deps{Cfg: cfg, Hub: h, Reg: reg, DB: db, Log: slog.Default()}
	srv := &http.Server{
		Addr:              ":" + cfg.HTTPPort,
		Handler:           handlers.Register(deps),
		ReadHeaderTimeout: 30 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		slog.Info("agenthub listening", "port", cfg.HTTPPort, "env", cfg.Env, "version", config.Version)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case err := <-errCh:
		if err != nil {
			slog.Error("server", "error", err)
		}
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("shutdown", "error", err)
	}
	slog.Info("agenthub stopped")
}

func setupLogger(cfg *config.Config) {
	level := slog.LevelInfo
	if cfg.Env == "dev" {
		level = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})))
}
