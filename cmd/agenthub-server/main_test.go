package main

// The wiring in main.go is where a configuration value becomes behaviour, and it
// is the one place no other test reaches: everything below main() talks to real
// services. These cases pin the decisions that are silent when they are wrong —
// which providers a build exposes, what a lease inherits by default, whether the
// semantic layer is on, and how a thread's tail reaches a worker brief.
//
// Nothing here opens a database or a socket: the helpers that need one take it as
// an argument, and the cases below pass nil where the point is the refusal.

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"agenthub/internal/audit"
	"agenthub/internal/auth"
	"agenthub/internal/config"
	"agenthub/internal/registry"
	"agenthub/internal/sandbox"
	"agenthub/internal/threads"
)

func TestRuntimeOrDefault(t *testing.T) {
	if got := runtimeOrDefault("runsc", "kata"); got != "runsc" {
		t.Fatalf("configured runtime = %q, want runsc", got)
	}
	if got := runtimeOrDefault("", "kata"); got != "kata" {
		t.Fatalf("empty runtime = %q, want the fallback", got)
	}
}

func TestSandboxDefaultsAreClosed(t *testing.T) {
	cfg := &config.Config{
		SandboxProvider:        sandbox.ProviderDocker,
		SandboxRuntime:         "runsc",
		SandboxDefaultImage:    "img",
		SandboxEgressAllowlist: []string{"example.test"},
		MaxOutputBytes:         4096,
		SandboxIdleTTL:         5 * time.Minute,
		// CLITimeout left at zero: a build that names nothing must still cap a
		// command rather than run it forever.
	}
	d := sandboxDefaults(cfg)

	if d.TimeoutMS != 15000 {
		t.Fatalf("timeout = %dms, want the 15s default", d.TimeoutMS)
	}
	if d.Network != sandbox.NetworkNone {
		t.Fatalf("network = %q, want none: a resource that names nothing gets no egress", d.Network)
	}
	if d.ArgsPolicy != sandbox.ArgsNone {
		t.Fatalf("args policy = %q, want none", d.ArgsPolicy)
	}
	if d.CPU != 1 || d.MemoryMB != 1024 || d.PidsLimit != 128 {
		t.Fatalf("caps = %g cpu / %dMB / %d pids, want the fixed floor", d.CPU, d.MemoryMB, d.PidsLimit)
	}
	if d.MaxOutputBytes != 4096 || d.IdleTTL != 5*time.Minute {
		t.Fatalf("output/idle not carried from the config: %d / %v", d.MaxOutputBytes, d.IdleTTL)
	}
	if len(d.EnvAllow) != 5 || d.EnvAllow[0] != "PATH" {
		t.Fatalf("EnvAllow = %v, want PATH plus four, never the process environment", d.EnvAllow)
	}
	if len(d.EgressAllow) != 1 || d.EgressAllow[0] != "example.test" {
		t.Fatalf("egress = %v, want the configured allowlist", d.EgressAllow)
	}

	// A configured timeout is used as given instead of the default.
	cfg.CLITimeout = 2 * time.Second
	if got := sandboxDefaults(cfg).TimeoutMS; got != 2000 {
		t.Fatalf("configured timeout = %dms, want 2000", got)
	}
}

func TestBuiltinSandboxProvidersCoverEveryLayer(t *testing.T) {
	providers := builtinSandboxProviders(&config.Config{}, sandbox.NewJobQueue())
	// Eight built-ins plus one entry per hosted platform preset: the count is the
	// contract the catalogue screen renders.
	if want := 8 + len(sandbox.PlatformPresets()); len(providers) != want {
		t.Fatalf("providers = %d, want %d", len(providers), want)
	}
	seen := map[string]bool{}
	for _, p := range providers {
		seen[p.Name()] = true
	}
	for _, name := range []string{sandbox.ProviderDocker, "podman", "gvisor", "kata", "bwrap", "local", "mock", "remote"} {
		if !seen[name] {
			t.Fatalf("no provider named %q; got %v", name, seen)
		}
	}
}

func TestPickModelEntry(t *testing.T) {
	if entry := pickModelEntry(nil, ""); entry != nil {
		t.Fatalf("a nil registry must not resolve an entry")
	}

	reg := registry.New()
	model, _ := reg.Register(&registry.Entry{ID: "model-a", Kind: registry.KindModel, Name: "a"})
	skill, _ := reg.Register(&registry.Entry{ID: "skill-a", Kind: registry.KindSkill, Name: "s"})

	if entry := pickModelEntry(reg, "model-a"); entry == nil || entry.ID != "model-a" {
		t.Fatalf("explicit id = %v, want model-a", entry)
	}
	// Naming a resource of the wrong kind is a configuration mistake, not a
	// reason to fall back to some other model.
	if entry := pickModelEntry(reg, skill.ID); entry != nil {
		t.Fatalf("a kind=skill id resolved as a model: %v", entry)
	}
	if entry := pickModelEntry(reg, "does-not-exist"); entry != nil {
		t.Fatalf("an unknown id resolved an entry")
	}
	// No id: the first usable model, whatever its health as long as it is not
	// known bad.
	if entry := pickModelEntry(reg, ""); entry == nil || entry.ID != model.ID {
		t.Fatalf("default = %v, want the first healthy model", entry)
	}
	if err := reg.SetHealth(model.ID, registry.HealthUnhealthy); err != nil {
		t.Fatalf("set health: %v", err)
	}
	if entry := pickModelEntry(reg, ""); entry != nil {
		t.Fatalf("an unhealthy model must not be chosen by default: %v", entry)
	}
}

func TestRouterGeneratorStaysOnTheRuleLayer(t *testing.T) {
	reg := registry.New()
	_, _ = reg.Register(&registry.Entry{ID: "model-a", Kind: registry.KindModel, Name: "a"})

	// The default mode is the program layer.
	if gen := routerGenerator(&config.Config{RouterAssessMode: config.RouterAssessRule}, reg); gen != nil {
		t.Fatalf("rule mode built a semantic generator")
	}
	// Asking for the model layer without a real provider is still a no: the mock
	// answers with prose, which the assessor would reject on every beat.
	cfg := &config.Config{
		RouterAssessMode: config.RouterAssessModel,
		ModelRouterMode:  registry.ProviderModeMock,
	}
	if gen := routerGenerator(cfg, reg); gen != nil {
		t.Fatalf("mock provider built a semantic generator")
	}
	// A real provider with no model resource is the same answer.
	cfg.ModelRouterMode = registry.ProviderModeOpenAICompatible
	cfg.RouterAssessResource = "does-not-exist"
	if gen := routerGenerator(cfg, reg); gen != nil {
		t.Fatalf("an unnamed model resource built a semantic generator")
	}
	// With both present the layer switches on.
	cfg.RouterAssessResource = "model-a"
	if gen := routerGenerator(cfg, reg); gen == nil {
		t.Fatalf("openai provider plus a model resource must build the generator")
	}
}

func TestThreadHistoryTakesTheTail(t *testing.T) {
	if threadHistory(nil) != nil {
		t.Fatalf("a nil store must not produce a provider")
	}

	store := &fakeThreads{messages: []*threads.Message{
		{AuthorType: "user", Body: "first"},
		{AuthorType: "agent", Body: "second"},
		{AuthorType: "user", Body: "third"},
	}}
	provider := threadHistory(store)
	if provider == nil {
		t.Fatal("a wired store must produce a provider")
	}

	got, err := provider(context.Background(), "t-1", 2)
	if err != nil {
		t.Fatalf("provider: %v", err)
	}
	if len(got) != 2 || got[0] != "agent: second" || got[1] != "user: third" {
		t.Fatalf("tail = %v, want the last two turns", got)
	}
	// A limit wider than the thread yields everything rather than a negative
	// slice.
	if got, _ := provider(context.Background(), "t-1", 10); len(got) != 3 {
		t.Fatalf("wide limit = %d turns, want all 3", len(got))
	}
	if got, _ := provider(context.Background(), "t-1", 0); len(got) != 0 {
		t.Fatalf("zero limit = %d turns, want none", len(got))
	}

	store.err = errors.New("store is not answering")
	if _, err := provider(context.Background(), "t-1", 1); err == nil {
		t.Fatal("a failing read must be reported, not swallowed")
	}
}

func TestSetupLoggerFollowsTheEnvironment(t *testing.T) {
	setupLogger(&config.Config{Env: "dev"})
	if !slog.Default().Enabled(context.Background(), slog.LevelDebug) {
		t.Fatal("dev must log at debug")
	}
	setupLogger(&config.Config{Env: "prod"})
	if slog.Default().Enabled(context.Background(), slog.LevelDebug) {
		t.Fatal("prod must not log at debug")
	}
}

func TestNewFilesServiceWithoutABackendOrADatabase(t *testing.T) {
	// A nil database means no service: the routes then answer files_disabled
	// rather than dereferencing it.
	if svc := newFilesService(&config.Config{}, nil); svc != nil {
		t.Fatalf("a nil database built a service")
	}
}

func TestNewBackupServiceWithoutADirectory(t *testing.T) {
	// BackupDir empty is the default: the feature is off and boot carries on.
	if svc := newBackupService(&config.Config{}, nil, nil, nil); svc == nil {
		t.Fatal("the disabled service must still be constructible")
	}
}

func TestSweepSessionsStopsWithTheContext(t *testing.T) {
	// A cancelled context runs one sweep and returns: the ticker loop is the only
	// part left, and it must not spin.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	store := &fakeSessions{}
	sweepSessions(ctx, store)
	if store.deleted != 1 {
		t.Fatalf("sweeps = %d, want exactly one before the context ended", store.deleted)
	}

	// A failing sweep is logged and the loop still ends on the context.
	store.err = errors.New("store is not answering")
	sweepSessions(ctx, store)
	if store.deleted != 2 {
		t.Fatalf("sweeps = %d, want the failure counted as an attempt", store.deleted)
	}
}

func TestPruneAuditRespectsRetentionAndContext(t *testing.T) {
	rec := audit.New(nil, audit.Options{})

	// No retention window means the ledger is never trimmed.
	pruneAudit(context.Background(), rec, 0)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	pruneAudit(ctx, rec, 24*time.Hour)
}

func TestSyncSourcesStopsWithTheContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// No sources configured: the pass runs, finds nothing and the loop returns on
	// the cancelled context.
	syncSources(ctx, registry.NewSourceService(registry.New(), nil), time.Hour)
}

// fakeThreads answers the one method the history provider calls and fails loudly
// on the rest: a provider that reached for anything else would be a surprise.
type fakeThreads struct {
	messages []*threads.Message
	err      error
}

func (f *fakeThreads) ListMessages(context.Context, string, int64) ([]*threads.Message, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.messages, nil
}

func (f *fakeThreads) CreateThread(context.Context, *threads.Thread) (*threads.Thread, error) {
	return nil, errors.New("not used")
}

func (f *fakeThreads) GetThread(context.Context, string, string) (*threads.Thread, error) {
	return nil, errors.New("not used")
}

func (f *fakeThreads) GetThreadUnscoped(context.Context, string) (*threads.Thread, error) {
	return nil, errors.New("not used")
}

func (f *fakeThreads) ListThreads(context.Context, string) ([]*threads.Thread, error) {
	return nil, errors.New("not used")
}

func (f *fakeThreads) DeleteThread(context.Context, string, string) error {
	return errors.New("not used")
}

func (f *fakeThreads) SetParticipants(context.Context, string, []string) error {
	return errors.New("not used")
}

func (f *fakeThreads) AddMessage(context.Context, *threads.Message) (*threads.Message, error) {
	return nil, errors.New("not used")
}

func (f *fakeThreads) UpsertAgentMessage(context.Context, string, string, string, string) (*threads.Message, error) {
	return nil, errors.New("not used")
}

// fakeSessions counts sweeps and can fail them.
type fakeSessions struct {
	auth.SessionStore
	deleted int
	err     error
}

func (f *fakeSessions) DeleteExpired(context.Context, time.Time) error {
	f.deleted++
	return f.err
}
