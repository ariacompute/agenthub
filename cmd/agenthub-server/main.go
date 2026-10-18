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
	"agenthub/internal/auth"
	"agenthub/internal/authz"
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
	// Demo resources are opt-in (AGENTHUB_SEED_DEMO=true): a default instance
	// starts empty, which is what the README scripts switch on explicitly.
	if cfg.SeedDemo {
		if err := store.Seed(db); err != nil {
			slog.Error("seed", "error", err)
			os.Exit(1)
		}
	} else {
		slog.Info("store: demo resources not seeded", "hint", "set AGENTHUB_SEED_DEMO=true to register the five demo resources")
	}

	// Identities: the authorization layer first (it resolves a user into
	// permissions), then the identity layer on top of it. Both are backed by the
	// same database as everything else.
	az := authz.New(store.NewAuthzStore(db))
	authSvc := auth.New(
		store.NewUserStore(db),
		store.NewSessionStore(db),
		store.NewOAuthStore(db),
		az,
		auth.Options{
			CookieName: "agenthub_session",
			TTL:        cfg.SessionTTL,
			Secure:     cfg.CookieSecure,
			OAuth:      cfg.OAuth,
		},
	)
	// The administrator described by .env is upserted on every boot, so a fresh
	// instance is never unreachable and a restart never resets a password the
	// operator changed in the console.
	if err := authSvc.BootstrapAdmin(context.Background(), cfg.BootstrapAdminEmail, cfg.BootstrapAdminPassword, cfg.BootstrapAdminName); err != nil {
		slog.Error("bootstrap admin", "error", err)
		os.Exit(1)
	}
	if cfg.BootstrapAdminEmail != "" {
		slog.Info("auth: bootstrap administrator ready", "email", cfg.BootstrapAdminEmail)
	} else {
		slog.Info("auth: no bootstrap administrator configured", "hint", "set AGENTHUB_BOOTSTRAP_ADMIN_EMAIL/PASSWORD, or the first registered account becomes the owner")
	}
	// Expired sessions are dead weight in a table that is read on every
	// request; sweep them once at boot and then hourly.
	sweepCtx, stopSweep := context.WithCancel(context.Background())
	defer stopSweep()
	go sweepSessions(sweepCtx, store.NewSessionStore(db))

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

	deps := handlers.Deps{Cfg: cfg, Hub: h, Reg: reg, DB: db, Log: slog.Default(), Auth: authSvc, Authz: az}
	srv := &http.Server{
		Addr:              ":" + cfg.HTTPPort,
		Handler:           handlers.Register(deps),
		ReadHeaderTimeout: 30 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		attrs := []any{"port", cfg.HTTPPort, "env", cfg.Env, "build", config.Build()}
		if note := config.BuildNote(); note != "" {
			attrs = append(attrs, "note", note)
		}
		slog.Info("agenthub listening", attrs...)
		// What the single listener answers, so `docker compose logs -f agenthub`
		// (or an attached `docker compose up`) spells out the endpoints behind
		// the port. Host-side port mappings are not visible in here — compose
		// owns those, see `make ports`.
		endpoints := make([]string, 0, len(handlers.Listeners()))
		for _, l := range handlers.Listeners() {
			endpoints = append(endpoints, l.Endpoint+" - "+l.Use)
		}
		slog.Info("agenthub endpoints", "listen", ":"+cfg.HTTPPort, "endpoints", endpoints)
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

// sweepSessions deletes sessions that expired a while ago. Revoked sessions are
// kept for a day: they are the record of who was logged out and when.
func sweepSessions(ctx context.Context, sessions auth.SessionStore) {
	sweep := func() {
		if err := sessions.DeleteExpired(ctx, time.Now().UTC().Add(-24*time.Hour)); err != nil {
			slog.Warn("auth: sweep expired sessions", "error", err)
		}
	}
	sweep()
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sweep()
		}
	}
}

func setupLogger(cfg *config.Config) {
	level := slog.LevelInfo
	if cfg.Env == "dev" {
		level = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})))
}
