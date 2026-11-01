// Command agenthub-server is the single agenthub binary: it wires PostgreSQL,
// the resource registry, the mock adapters and the A2A hub behind one HTTP
// listener that also hosts the Vue3 console.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
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
	"agenthub/internal/router"
	"agenthub/internal/sandbox"
	"agenthub/internal/secure"
	"agenthub/internal/store"
	"agenthub/internal/threads"

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
	// Initialise the static-at-rest encryption module. When no dedicated key is
	// configured the module logs a warning and falls back to a dev-only key.
	secure.Configure(cfg.EncryptionKey, cfg.EncryptionKey)

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
	// Migrations run on the application connection at startup.
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
		store.NewAPIKeyStore(db),
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

	// Execution environments. The stack is built before the adapters because the
	// adapters are what run commands: assembling them first would mean one code
	// path where a CLI resource still executes on the host.
	sandboxSvc, err := newSandboxStack(cfg, db)
	if err != nil {
		slog.Error("sandbox", "error", err)
		os.Exit(1)
	}
	if sandboxSvc != nil {
		// A resource may name an execution provider; naming one is a claim this
		// host can run it. Resolve through the catalogue (and its 30s probe cache)
		// so an unknown or unusable backend is refused while it is being written,
		// not on the first execution.
		reg.WithSandboxChecker(func(name string) error {
			_, _, err := sandboxSvc.Registry().RequireAvailable(context.Background(), name)
			return err
		})
		sandboxCtx, stopSandbox := context.WithCancel(context.Background())
		// Registered before the router's defer, so shutdown runs it *after* the
		// router stopped: no new task can arrive while the sweep is still up,
		// and the sweep only reclaims, it never destroys a live lease.
		defer sandboxSvc.Stop()
		defer stopSandbox()
		go func() {
			if err := sandboxSvc.Run(sandboxCtx); err != nil {
				slog.Error("sandbox: sweep stopped", "error", err)
			}
		}()
	}

	opts := adapter.Options{
		CLITimeout:      cfg.CLITimeout,
		CLIAllowlist:    cfg.CLIAllowlist,
		MaxOutputBytes:  cfg.MaxOutputBytes,
		ModelRouterMode: cfg.ModelRouterMode,
		AgentTimeout:    cfg.AgentTimeout,
		Sandbox:         sandboxSvc,
	}
	pushRepo := store.NewPushRepository(db)
	pusher := hub.PushNotifier(&hub.MockNotifier{Repo: pushRepo})
	if cfg.PushSecret != "" {
		// A configured push secret enables the real, signed, SSRF-guarded
		// callback delivery instead of the recording-only mock.
		pusher = hub.NewHTTPNotifier(pushRepo, cfg.PushSecret)
	}
	h := hub.New(cfg, reg, opts, pusher, hub.Persistence{
		Tasks:  store.NewTaskRepository(db),
		Events: store.NewEventRepository(db),
		Pushes: pushRepo,
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

	// Collaboration threads (agentbuddy) share the same database as the rest of
	// the hub; nil when the instance runs without one, like every other store.
	threadsStore := store.NewThreadsStore(db)
	channelsStore := store.NewChannelsStore(db)
	computersStore := store.NewComputersStore(db)
	schedulesStore := store.NewSchedulesStore(db)
	knowledgeStore := store.NewKnowledgeStore(db)

	// The scheduler. Off unless an operator switched it on: it opens tasks on its
	// own, which is not a behaviour an instance should acquire by upgrading.
	// Its tickets run as ordinary hub tasks, so a scheduled run inherits the state
	// machine, spans, artifacts, events and push notifications of any other run.
	var routerSvc *router.Service
	routerCtx, stopRouter := context.WithCancel(context.Background())
	defer stopRouter()
	if cfg.RouterEnabled {
		routerSvc = router.NewService(router.ServiceOptions{
			Store:     store.NewRouterStore(db),
			Runner:    h,
			Schedules: schedulesStore,
			Generator: routerGenerator(cfg, reg),
			History:   threadHistory(threadsStore),
			ThreadLink: func(ctx context.Context, threadID, taskID, resourceID string) error {
				_, err := threadsStore.UpsertAgentMessage(ctx, threadID, taskID, resourceID, "")
				return err
			},
			Clock:       time.Now,
			Interval:    cfg.RouterInterval,
			MaxParallel: cfg.RouterMaxTickets,
			MaxAttempts: cfg.RouterMaxAttempts,
			BatchMax:    cfg.RouterBatchMax,
			Resource:    cfg.RouterResource,
			TriggerTTL:  cfg.RouterTriggerTTL,
			PruneEvery:  cfg.RouterPruneInterval,
		})
		defer routerSvc.Stop()
		go func() {
			if err := routerSvc.Run(routerCtx); err != nil {
				slog.Error("router: beat stopped", "error", err)
			}
		}()
		slog.Info("router: scheduler enabled",
			"interval", cfg.RouterInterval, "batch", cfg.RouterBatchMax,
			"maxTickets", cfg.RouterMaxTickets, "assess", cfg.RouterAssessMode,
			"resource", cfg.RouterResource)
	} else {
		slog.Info("router: scheduler disabled",
			"hint", "set AGENTHUB_ROUTER_ENABLED=true, schedules then fire on their cron expression")
	}

	deps := handlers.Deps{Cfg: cfg, Hub: h, Reg: reg, DB: db, Log: slog.Default(), Auth: authSvc, Authz: az, Backups: backupSvc, Sources: sources, Threads: threadsStore, Channels: channelsStore, Computers: computersStore, Schedules: schedulesStore, Knowledge: knowledgeStore, Speech: adapter.DefaultSpeech, Router: routerSvc, Sandboxes: sandboxSvc}
	srv := &http.Server{
		Addr:              ":" + cfg.HTTPPort,
		Handler:           handlers.Wrap(handlers.Register(deps), cfg),
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

// newSandboxStack assembles the execution-environment control plane: the provider
// catalogue, the lease manager and the facade the handlers and adapters talk to.
//
// It returns nil when the feature is switched off, which is what makes the sandbox
// endpoints answer 503 feature_unavailable and the adapters keep their pre-sandbox
// behaviour. Everything else — including a provider that cannot run on this host —
// is a started control plane that fails closed at execution time with
// sandbox_unsupported: refusing loudly beats degrading quietly.
func newSandboxStack(cfg *config.Config, db *gorm.DB) (*sandbox.Service, error) {
	if !cfg.SandboxEnabled {
		slog.Info("sandbox: disabled",
			"hint", "set AGENTHUB_SANDBOX_ENABLED=true to lease execution environments; commands then run where the resource says")
		return nil, nil
	}

	// The queue exists before the catalogue because the remote provider is built
	// on it: a node's long poll *is* the queue, so there is nothing to persist.
	queue := sandbox.NewJobQueue()
	catalogue := sandbox.NewRegistry(sandbox.RegistryOptions{
		Default: cfg.SandboxProvider,
		Clock:   time.Now,
	})
	for _, provider := range builtinSandboxProviders(cfg, queue) {
		if err := catalogue.Register(provider); err != nil {
			return nil, fmt.Errorf("register provider %q: %w", provider.Name(), err)
		}
	}
	for i, spec := range cfg.SandboxProviders {
		provider, err := sandbox.NewDeclarativeProvider(spec)
		if err != nil {
			return nil, fmt.Errorf("AGENTHUB_SANDBOX_PROVIDERS[%d] (%s): %w", i, spec.Name, err)
		}
		if err := catalogue.Register(provider); err != nil {
			// A duplicate name is refused rather than replaced: two definitions of
			// "docker" would make the catalogue lie about what will run.
			return nil, fmt.Errorf("AGENTHUB_SANDBOX_PROVIDERS[%d] (%s): %w", i, spec.Name, err)
		}
	}

	sbStore := store.NewSandboxStore(db)
	bus := sandbox.NewBus(sbStore, time.Now)
	manager := sandbox.NewManager(sandbox.ManagerOptions{
		Registry: catalogue,
		Store:    sbStore,
		Defaults: sandboxDefaults(cfg),
		IdleTTL:  cfg.SandboxIdleTTL,
		MaxTTL:   cfg.SandboxMaxTTL,
		// The ceilings are what keeps one tenant from filling the host, and the
		// router's concurrency is checked against them in config.Validate.
		MaxPerOwner: cfg.SandboxMaxPerOwner,
		MaxTotal:    cfg.SandboxMaxTotal,
		Bus:         bus,
		Clock:       time.Now,
		Log:         slog.Default(),
	})
	service := sandbox.NewService(sandbox.ServiceOptions{
		Manager:  manager,
		Registry: catalogue,
		Bus:      bus,
		Queue:    queue,
		Interval: cfg.SandboxSweepInterval,
		Clock:    time.Now,
		Log:      slog.Default(),
	})

	// Probe the default provider once at boot so the reason a host cannot execute
	// appears in the startup log rather than waiting for the first task to fail.
	info, ierr := catalogue.Info(context.Background(), cfg.SandboxProvider)
	switch {
	case ierr != nil:
		slog.Warn("sandbox: default provider is not registered",
			"provider", cfg.SandboxProvider, "error", ierr.Error())
	case !info.Available:
		slog.Warn("sandbox: default provider cannot run on this host, execution will be refused",
			"provider", cfg.SandboxProvider, "reason", info.Reason,
			"hint", "install the runtime, point AGENTHUB_SANDBOX_PROVIDER at another provider, or set it to mock for an offline instance")
	default:
		slog.Info("sandbox: control plane ready",
			"provider", cfg.SandboxProvider, "layer", info.Layer, "isolation", info.Isolation,
			"providers", len(catalogue.Names()),
			"maxTotal", cfg.SandboxMaxTotal, "idleTTL", cfg.SandboxIdleTTL)
	}
	return service, nil
}

// builtinSandboxProviders is the shipped catalogue. It is registered in full even
// when most entries cannot run here: the console's job is to show what could be
// enabled, with the probe's reason next to each unavailable entry.
//
// gVisor and Kata are the same container backend with a different --runtime, not
// two implementations: giving them their own code would only let the three drift
// apart.
func builtinSandboxProviders(cfg *config.Config, queue *sandbox.JobQueue) []sandbox.Provider {
	host := cfg.SandboxDockerHost
	runtime := strings.TrimSpace(cfg.SandboxRuntime)
	providers := []sandbox.Provider{
		sandbox.NewDockerProvider(sandbox.ProviderDocker, "docker", runtime, host),
		sandbox.NewDockerProvider("podman", "podman", "", host),
		sandbox.NewDockerProvider("gvisor", "docker", runtimeOrDefault(runtime, "runsc"), host),
		sandbox.NewDockerProvider("kata", "docker", runtimeOrDefault(runtime, "kata"), host),
		sandbox.NewBwrapProvider(),
		sandbox.NewLocalProvider(),
		sandbox.NewMockProvider(),
		sandbox.NewRemoteProvider(queue),
	}
	// The hosted platforms are catalogue entries, not SDK integrations: each one
	// talks to an operator-run shim so no vendor code lands in this binary.
	for _, preset := range sandbox.PlatformPresets() {
		providers = append(providers, sandbox.NewHTTPProvider(preset, os.Getenv))
	}
	return providers
}

// sandboxDefaults is the instance-wide policy a resource inherits. Its whole
// purpose is that a resource which names nothing still gets a closed sandbox:
// no network, a read-only root, a capped output and no caller-supplied arguments.
func sandboxDefaults(cfg *config.Config) sandbox.Defaults {
	timeout := cfg.CLITimeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	return sandbox.Defaults{
		Provider:       cfg.SandboxProvider,
		Runtime:        cfg.SandboxRuntime,
		Image:          cfg.SandboxDefaultImage,
		Network:        sandbox.NetworkNone,
		EgressAllow:    cfg.SandboxEgressAllowlist,
		CPU:            1,
		MemoryMB:       1024,
		PidsLimit:      128,
		TimeoutMS:      int(timeout / time.Millisecond),
		MaxOutputBytes: cfg.MaxOutputBytes,
		// PATH only, plus the four variables every CLI expects to find. The
		// agenthub process's own environment (DATABASE_URL, API keys) is never
		// inherited into a sandbox.
		EnvAllow:   []string{"PATH", "HOME", "LANG", "TZ", "TERM"},
		ArgsPolicy: sandbox.ArgsNone,
		ReuseScope: "",
		IdleTTL:    cfg.SandboxIdleTTL,
	}
}

func runtimeOrDefault(runtime, fallback string) string {
	if runtime != "" {
		return runtime
	}
	return fallback
}

// newBackupService assembles the checkpoint service.
//
// It never fails the boot: an unusable backup directory is logged and the
// feature comes up disabled, because losing the console over a missing volume
// would be a worse outcome than not taking backups for one boot.
func newBackupService(cfg *config.Config, db *gorm.DB, h *hub.Hub, pusher hub.PushNotifier) *backup.Service {
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
	opts := backup.Options{
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
	}
	// At-rest encryption for backups: a stolen backup volume is useless without
	// the key. Left unset means plaintext archives (the previous behaviour).
	if cfg.EncryptionKey != "" {
		opts.Key = []byte(cfg.EncryptionKey)
	}
	return backup.New(opts)
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

// routerGenerator builds the semantic layer's model backend. It returns nil — and
// so leaves the scheduler on its program rules — in three cases, each logged:
// the mode is "rule", no real provider is configured, or no kind=model resource
// exists. The mock provider deliberately does not qualify: it answers with a
// summary rather than JSON, which the assessor would reject on every beat, and a
// layer that always fails is worse than one that is honestly absent.
func routerGenerator(cfg *config.Config, reg *registry.Registry) router.Generator {
	if cfg.RouterAssessMode != config.RouterAssessModel {
		return nil
	}
	if cfg.ModelRouterMode != registry.ProviderModeOpenAICompatible {
		slog.Warn("router: semantic assessment needs a real model provider",
			"hint", "set AGENTHUB_MODEL_ROUTER_MODE="+registry.ProviderModeOpenAICompatible+" and configure the model resource; running on the program rules")
		return nil
	}
	entry := pickModelEntry(reg, cfg.RouterAssessResource)
	if entry == nil {
		slog.Warn("router: no usable kind=model resource", "resource", cfg.RouterAssessResource,
			"hint", "register one, or set AGENTHUB_ROUTER_ASSESS_RESOURCE; running on the program rules")
		return nil
	}
	slog.Info("router: semantic assessment via model resource", "resource", entry.ID, "name", entry.Name)
	return adapter.NewModelRouterFor(entry, cfg.ModelRouterMode)
}

// pickModelEntry returns the requested model resource, or the first healthy one.
func pickModelEntry(reg *registry.Registry, id string) *registry.Entry {
	if reg == nil {
		return nil
	}
	if id != "" {
		entry, ok := reg.Get(id)
		if !ok || entry.Kind != registry.KindModel {
			return nil
		}
		return entry
	}
	for _, entry := range reg.List(string(registry.KindModel), "") {
		if entry.Health != registry.HealthUnhealthy {
			return entry
		}
	}
	return nil
}

// threadHistory feeds the tail of a conversation into the worker brief, so a
// follow-up does not force the worker to re-read the whole thread.
func threadHistory(ts threads.Store) router.HistoryProvider {
	if ts == nil {
		return nil
	}
	return func(ctx context.Context, threadID string, limit int) ([]string, error) {
		msgs, err := ts.ListMessages(ctx, threadID, 0)
		if err != nil {
			return nil, err
		}
		start := len(msgs) - limit
		if start < 0 {
			start = 0
		}
		out := make([]string, 0, len(msgs)-start)
		for _, m := range msgs[start:] {
			out = append(out, m.AuthorType+": "+m.Body)
		}
		return out, nil
	}
}
