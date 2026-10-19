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
	"agenthub/internal/backup"
	"agenthub/internal/config"
	"agenthub/internal/handlers"
	"agenthub/internal/hub"
	"agenthub/internal/registry"
	"agenthub/internal/store"

	"gorm.io/gorm"
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

	// The catalogue: resources plus the namespaces they belong to. Loading the
	// namespaces once at boot is what lets "does this record need review?" be
	// answered in memory on every registration.
	reg := registry.NewWithStores(store.NewResourceStore(db), store.NewNamespaceStore(db)).
		WithModelRouterMode(cfg.ModelRouterMode)
	if err := reg.LoadNamespaces(context.Background()); err != nil {
		slog.Error("registry: load namespaces", "error", err)
		os.Exit(1)
	}
	// Upstream registries. The service keeps working with a nil store, so an
	// instance that never federates anything simply has no sources.
	sources := registry.NewSourceService(reg, store.NewSourceStore(db),
		registry.WithFixturePin(cfg.RegistrySyncFixture))
	if cfg.RegistrySyncFixture {
		slog.Info("registry: upstream synchronisation is pinned to the offline client",
			"hint", "AGENTHUB_REGISTRY_SYNC_FIXTURE is on, no source will reach the network")
	}

	opts := adapter.Options{
		CLITimeout:      cfg.CLITimeout,
		CLIAllowlist:    cfg.CLIAllowlist,
		MaxOutputBytes:  cfg.MaxOutputBytes,
		ModelRouterMode: cfg.ModelRouterMode,
		AgentTimeout:    cfg.AgentTimeout,
	}
	pusher := &hub.MockNotifier{Repo: store.NewPushRepository(db)}
	h := hub.New(cfg, reg, opts, pusher, hub.Persistence{
		Tasks:  store.NewTaskRepository(db),
		Events: store.NewEventRepository(db),
		Pushes: pusher.Repo,
	})
	defer h.Close()

	// Database checkpoints. The directory is the switch: without one the
	// service stays disabled and the console says so, which is what an
	// instance with no backup volume mounted should look like.
	backupSvc := newBackupService(cfg, db, h, pusher)
	if backupSvc.Enabled() {
		// The environment seeds the policy once; afterwards the row the
		// console saved is the operator's and survives every restart.
		if err := backupSvc.SeedSchedule(context.Background(), cfg.BackupSchedule(), "boot"); err != nil {
			slog.Warn("backup: could not seed the schedule", "error", err)
		}
		backupCtx, stopBackups := context.WithCancel(context.Background())
		defer stopBackups()
		go func() {
			if err := backupSvc.Run(backupCtx); err != nil {
				slog.Error("backup: scheduler stopped", "error", err)
			}
		}()
	}

	// Background synchronisation. It is off unless an operator switched it on,
	// and it stops with the same cancellation the rest of the process uses, so
	// a shutdown never leaves a pull half written.
	syncCtx, stopSync := context.WithCancel(context.Background())
	defer stopSync()
	if cfg.RegistrySyncEnabled {
		go syncSources(syncCtx, sources, cfg.RegistrySyncInterval)
	} else {
		slog.Info("registry: background synchronisation disabled",
			"hint", "set AGENTHUB_REGISTRY_SYNC_ENABLED=true, or trigger a source from the console")
	}

	deps := handlers.Deps{Cfg: cfg, Hub: h, Reg: reg, DB: db, Log: slog.Default(), Auth: authSvc, Authz: az, Backups: backupSvc, Sources: sources}
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

// newBackupService assembles the checkpoint service.
//
// It never fails the boot: an unusable backup directory is logged and the
// feature comes up disabled, because losing the console over a missing volume
// would be a worse outcome than not taking backups for one boot.
func newBackupService(cfg *config.Config, db *gorm.DB, h *hub.Hub, pusher *hub.MockNotifier) *backup.Service {
	log := slog.Default()
	if !cfg.BackupEnabled() {
		log.Info("backup: disabled", "hint", "set AGENTHUB_BACKUP_DIR to enable database checkpoints")
		return backup.New(backup.Options{Log: log})
	}
	// The directory is usually a mounted volume that appears with the
	// container; creating it keeps a fresh deployment from failing on a path
	// nobody has written to yet.
	if err := os.MkdirAll(cfg.BackupDir, 0o750); err != nil {
		log.Error("backup: directory is not usable", "dir", cfg.BackupDir, "error", err)
		return backup.New(backup.Options{Log: log})
	}
	// "" means auto: pg_dump when the runtime image has the client, the
	// built-in COPY engine otherwise. The engine name travels with every
	// archive, so the two never have to agree.
	engine, err := backup.SelectEngine(cfg.DatabaseURL, "")
	if err != nil {
		log.Error("backup: no dump engine available", "error", err)
		return backup.New(backup.Options{Dir: cfg.BackupDir, Log: log})
	}
	log.Info("backup: enabled", "dir", cfg.BackupDir, "engine", engine.Name())

	// After a restore the in-memory state still describes the database that
	// was replaced. The registry and the task store read PostgreSQL on every
	// call, so only two things here do hold state: the event sequence
	// counter, and the push log the console reads back.
	reload := backup.Reloaders(
		backup.ReloaderFunc(func(ctx context.Context) error { return h.ResetEventSeq() }),
		backup.ReloaderFunc(func(ctx context.Context) error { pusher.Reset(); return nil }),
	)
	return backup.New(backup.Options{
		Dir:     cfg.BackupDir,
		Engine:  engine,
		Store:   store.NewBackupStore(db),
		Quiesce: h,
		Reload:  reload,
		// Applied is how a restore compares the archive's schema against this
		// build; Migrate catches the database up when the archive is older.
		Applied: func(ctx context.Context) ([]string, error) { return store.Applied(db) },
		Migrate: func() error { return store.Migrate(db) },
		Log:     log,
	})
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

// syncSources pulls every enabled upstream registry on a fixed interval.
//
// A failed source is logged and skipped: one unreachable registry must not
// stop the others, and the outcome is recorded on the source itself so the
// console can show it.
func syncSources(ctx context.Context, sources *registry.SourceService, interval time.Duration) {
	run := func() {
		for id, res := range sources.SyncAll(ctx, "") {
			if res.Status == registry.SyncStatusFailed {
				slog.Warn("registry: scheduled sync failed", "source", id, "code", res.Error)
				continue
			}
			slog.Info("registry: scheduled sync", "source", id,
				"imported", res.Imported, "updated", res.Updated, "skipped", res.Skipped)
		}
	}
	run()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run()
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
