package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agenthub/internal/adapter"
	"agenthub/internal/registry"
	"agenthub/internal/sandbox"
)

// TestVersionIsStampable pins the contract the Makefile and Dockerfile rely on:
// `go build -ldflags "-X agenthub/internal/config.Version=..."` can only
// overwrite a package-level string var. Turning Version into a const would
// silently drop the stamp and every build would report the sentinel instead.
func TestVersionIsStampable(t *testing.T) {
	if strings.TrimSpace(Version) == "" {
		t.Fatal("config.Version must never be empty: /healthz and the Agent Card report it")
	}
	if strings.TrimSpace(ProtocolVersion) == "" {
		t.Fatal("config.ProtocolVersion must never be empty")
	}
	// -X refuses constants, so this fails to compile the moment Version
	// becomes a const — the assertion below documents the intent at runtime.
	if strings.ContainsAny(Version, " \n\t") {
		t.Fatalf("config.Version must be a single token, got %q", Version)
	}
	if strings.ContainsAny(Commit, " \n\t") {
		t.Fatalf("config.Commit must be a single token, got %q", Commit)
	}
}

func TestBuildString(t *testing.T) {
	defer func(v, c string) { Version, Commit = v, c }(Version, Commit)

	cases := []struct {
		version, commit, want string
	}{
		{"v0.2.0", "1a2b3c4", "v0.2.0@1a2b3c4"},
		{"v0.2.0", "", "v0.2.0"},        // no commit stamped: never render "@unknown"
		{"v0.2.0", "unknown", "v0.2.0"}, // sentinel commit is treated as absent
		{"dev", "unknown", "dev"},       // unstamped build stays obvious
	}
	for _, c := range cases {
		Version, Commit = c.version, c.commit
		if got := Build(); got != c.want {
			t.Fatalf("Build(%q, %q) = %q, want %q", c.version, c.commit, got, c.want)
		}
	}
}

// TestBuildCommitIsSevenChars guards the console contract: the foot renders
// "version@commit" and a longer hash would overflow the sidebar.
func TestBuildCommitIsSevenChars(t *testing.T) {
	defer func(c string) { Commit = c }(Commit)
	Commit = "1a2b3c4d5e6f"
	if got := Build(); got != Version+"@1a2b3c4d5e6f" {
		t.Fatalf("Build must not truncate, got %q", got)
	}
	// Truncation is the console's job (Sidebar.vue slices to 7).
}

func TestBuildNote(t *testing.T) {
	defer func(d, dirty string) { Distance, Dirty = d, dirty }(Distance, Dirty)

	cases := []struct {
		distance, dirty, want string
	}{
		{"0", "false", ""},          // exactly on a clean tag: nothing to caveat
		{"2", "false", "+2"},        // unreleased commits since the tag
		{"0", "true", "dirty"},      // local modification of a tagged build
		{"2", "true", "+2 dirty"},   // both
		{"", "false", ""},           // unstamped distance is treated as "at tag"
		{"0", "", ""},               // unstamped dirty is treated as clean
		{"12", "true", "+12 dirty"}, // multi-digit distance keeps its place
	}
	for _, c := range cases {
		Distance, Dirty = c.distance, c.dirty
		if got := BuildNote(); got != c.want {
			t.Fatalf("BuildNote(%q, %q) = %q, want %q", c.distance, c.dirty, got, c.want)
		}
	}
}

func TestLoadDefaults(t *testing.T) {
	for _, key := range []string{
		"AGENTHUB_HTTP_PORT", "AGENTHUB_ENV", "AGENTHUB_BOOTSTRAP_ADMIN_EMAIL",
		"AGENTHUB_SESSION_TTL", "AGENTHUB_MAX_PARALLEL", "AGENTHUB_CLI_TIMEOUT",
	} {
		t.Setenv(key, "")
		os.Unsetenv(key)
	}
	t.Setenv("DATABASE_URL", "postgres://agenthub:agenthub@localhost:5434/agenthub")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.HTTPPort != "3500" {
		t.Fatalf("port = %q", cfg.HTTPPort)
	}
	if cfg.MaxParallel != 8 {
		t.Fatalf("max parallel = %d", cfg.MaxParallel)
	}
	if cfg.CLITimeout != 15*time.Second {
		t.Fatalf("cli timeout = %v", cfg.CLITimeout)
	}
	if cfg.PushRecordLimit != 100 {
		t.Fatalf("push record limit = %d, want 100", cfg.PushRecordLimit)
	}
	// Identities live in the users table now: there is no shared token left to
	// fall back to, so every request is authenticated as a session or as
	// anonymous.
	if got := cfg.AuthMode(); got != "session" {
		t.Fatalf("auth mode = %q, want session", got)
	}
	if cfg.SessionTTL != 12*time.Hour {
		t.Fatalf("session ttl = %v, want 12h", cfg.SessionTTL)
	}
	if cfg.CookieSecure {
		t.Fatal("cookies must not be Secure by default outside prod: a plain http dev box could not log in")
	}
	if cfg.SeedDemo {
		t.Fatal("SeedDemo must default to off: an instance starts with only the resources you register")
	}
}

// The demo resources are the only "mock data" agenthub can insert, so they must
// be opt-in and impossible to enable in production.
func TestSeedDemoIsOptIn(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://agenthub:agenthub@localhost:5434/agenthub")

	for _, tc := range []struct {
		raw  string
		want bool
	}{
		{"true", true},
		{"TRUE", true},
		{"1", true},
		{"false", false},
		{"0", false},
		{"not-a-bool", false}, // unparsable falls back to the default
	} {
		t.Setenv("AGENTHUB_SEED_DEMO", tc.raw)
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if cfg.SeedDemo != tc.want {
			t.Fatalf("AGENTHUB_SEED_DEMO=%q: SeedDemo = %v, want %v", tc.raw, cfg.SeedDemo, tc.want)
		}
	}

	t.Setenv("AGENTHUB_SEED_DEMO", "")
	os.Unsetenv("AGENTHUB_SEED_DEMO")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.SeedDemo {
		t.Fatal("unset AGENTHUB_SEED_DEMO must leave demo resources off")
	}

	t.Setenv("AGENTHUB_SEED_DEMO", "true")
	t.Setenv("AGENTHUB_ENV", "prod")
	t.Setenv("AGENTHUB_BOOTSTRAP_ADMIN_EMAIL", "admin@example.com")
	if _, err := Load(); err == nil {
		t.Fatal("production must refuse demo resources")
	}
}

// TestBootstrapAdminIsValidated pins how an operator seeds the first account:
// .env carries the address, and a typo must fail at boot rather than leaving an
// instance nobody can log in to.
func TestBootstrapAdminIsValidated(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://agenthub:agenthub@localhost:5434/agenthub")

	t.Setenv("AGENTHUB_BOOTSTRAP_ADMIN_EMAIL", "admin@example.com")
	t.Setenv("AGENTHUB_BOOTSTRAP_ADMIN_PASSWORD", "correct horse battery")
	t.Setenv("AGENTHUB_BOOTSTRAP_ADMIN_NAME", "Admin")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.BootstrapAdminEmail != "admin@example.com" || cfg.BootstrapAdminName != "Admin" {
		t.Fatalf("bootstrap admin = %q / %q", cfg.BootstrapAdminEmail, cfg.BootstrapAdminName)
	}
	if cfg.BootstrapAdminPassword == "" {
		t.Fatal("bootstrap password must survive Load: main hashes it into the users table")
	}

	// A malformed address is rejected, so the operator sees the typo at boot.
	t.Setenv("AGENTHUB_BOOTSTRAP_ADMIN_EMAIL", "not-an-email")
	if _, err := Load(); err == nil {
		t.Fatal("malformed AGENTHUB_BOOTSTRAP_ADMIN_EMAIL must be rejected")
	}

	// Production cannot start without an administrator.
	t.Setenv("AGENTHUB_BOOTSTRAP_ADMIN_EMAIL", "")
	os.Unsetenv("AGENTHUB_BOOTSTRAP_ADMIN_EMAIL")
	t.Setenv("AGENTHUB_ENV", "prod")
	if _, err := Load(); err == nil {
		t.Fatal("AGENTHUB_ENV=prod must require AGENTHUB_BOOTSTRAP_ADMIN_EMAIL")
	}
}

// TestLoadRejectsMissingDatabaseURL pins the "database is mandatory" rule: a
// deployment without DATABASE_URL must fail fast instead of running on state
// that disappears with the process.
func TestLoadRejectsMissingDatabaseURL(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	os.Unsetenv("DATABASE_URL")
	if _, err := Load(); err == nil {
		t.Fatal("Load without DATABASE_URL must fail")
	}
}

func TestValidateRejectsBadInput(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*Config)
	}{
		{"empty port", func(c *Config) { c.HTTPPort = "" }},
		{"bad port", func(c *Config) { c.HTTPPort = "not-a-port" }},
		{"out of range port", func(c *Config) { c.HTTPPort = "70000" }},
		{"bad env", func(c *Config) { c.Env = "whatever" }},
		{"zero event buffer", func(c *Config) { c.EventBuffer = 0 }},
		{"zero parallel", func(c *Config) { c.MaxParallel = 0 }},
		{"negative timeout", func(c *Config) { c.CLITimeout = -time.Second }},
		{"zero output cap", func(c *Config) { c.MaxOutputBytes = 0 }},
		{"prod without bootstrap admin", func(c *Config) { c.Env = "prod"; c.BootstrapAdminEmail = "" }},
		{"bad bootstrap admin email", func(c *Config) { c.BootstrapAdminEmail = "not-an-email" }},
		{"zero session ttl", func(c *Config) { c.SessionTTL = 0 }},
		{"missing database url", func(c *Config) { c.DatabaseURL = "" }},
		{"blank database url", func(c *Config) { c.DatabaseURL = "   " }},
		{"zero push record limit", func(c *Config) { c.PushRecordLimit = 0 }},
		{"sync on without an interval", func(c *Config) {
			c.RegistrySyncEnabled = true
			c.RegistrySyncInterval = 0
		}},
		{"sync on with a negative interval", func(c *Config) {
			c.RegistrySyncEnabled = true
			c.RegistrySyncInterval = -time.Minute
		}},
		{"unknown model router mode", func(c *Config) { c.ModelRouterMode = "anthropic" }},
		{"unknown speech mode", func(c *Config) { c.SpeechMode = "elevenlabs" }},
		{"audio cap below one mebibyte", func(c *Config) { c.AudioMaxBytes = 1024 }},
		{"short push secret", func(c *Config) { c.PushSecret = "too-short" }},
		{"prod with a weak encryption key", func(c *Config) {
			c.Env = "prod"
			c.BootstrapAdminEmail = "admin@example.com"
			c.EncryptionKey = "short"
		}},
		{"prod with demo resources", func(c *Config) {
			c.Env = "prod"
			c.BootstrapAdminEmail = "admin@example.com"
			c.EncryptionKey = strings.Repeat("k", 32)
			c.SeedDemo = true
		}},
		{"backup on with an invalid schedule", func(c *Config) {
			c.BackupDir = "/var/lib/agenthub/backups"
			c.BackupMode = "whenever"
		}},
		// The scheduler's tuning is only enforced once it is switched on, but a
		// value that breaks it must not be accepted silently.
		{"router on without an interval", func(c *Config) {
			c.RouterEnabled = true
			c.RouterInterval = 0
		}},
		{"router with an absurd batch", func(c *Config) {
			enableRouter(c)
			c.RouterBatchMax = 1000
		}},
		{"router with no ticket slots", func(c *Config) {
			enableRouter(c)
			c.RouterMaxTickets = 0
		}},
		{"router with absurd retries", func(c *Config) {
			enableRouter(c)
			c.RouterMaxAttempts = 11
		}},
		{"router with no retention window", func(c *Config) {
			enableRouter(c)
			c.RouterTriggerTTL = 0
		}},
		{"router with no prune cadence", func(c *Config) {
			enableRouter(c)
			c.RouterPruneInterval = 0
		}},
		{"router with an unknown assess mode", func(c *Config) {
			enableRouter(c)
			c.RouterAssessMode = "oracle"
		}},
	}
	for _, c := range cases {
		cfg := validBaseConfig()
		c.mut(cfg)
		if err := cfg.Validate(); err == nil {
			t.Fatalf("%s: expected validation error", c.name)
		}
	}
}

// The registry knobs only constrain a deployment that opted into them: an
// instance that never synchronises must stay valid with the zeros it was built
// with, and an unset router mode must not reject a caller that built the struct
// directly instead of going through Load.
func TestRegistryDefaultsStayValid(t *testing.T) {
	cfg := &Config{
		HTTPPort:        "3500",
		Env:             "dev",
		MaxParallel:     8,
		CLITimeout:      time.Second,
		MaxOutputBytes:  1024,
		EventBuffer:     64,
		DatabaseURL:     "postgres://agenthub:agenthub@localhost:5434/agenthub",
		PushRecordLimit: 100,
		SessionTTL:      time.Hour,
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("a config built by hand must stay valid: %v", err)
	}
	if cfg.ModelRouterMode != registry.ProviderModeMock {
		t.Fatalf("router mode = %q, want it normalised to mock", cfg.ModelRouterMode)
	}
	// Sync off: the interval is irrelevant.
	cfg.RegistrySyncEnabled = false
	cfg.RegistrySyncInterval = 0
	if err := cfg.Validate(); err != nil {
		t.Fatalf("sync off must not require an interval: %v", err)
	}
	// Sync on with a real interval is the only combination that asks for one.
	cfg.RegistrySyncEnabled = true
	cfg.RegistrySyncInterval = 30 * time.Minute
	if err := cfg.Validate(); err != nil {
		t.Fatalf("sync on with an interval: %v", err)
	}
}

// The defaults are the offline posture: nothing reaches an upstream registry
// unless an operator says so.
func TestLoadDefaultsStayOffline(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://agenthub:agenthub@localhost:5434/agenthub")
	os.Unsetenv("AGENTHUB_REGISTRY_SYNC_ENABLED")
	os.Unsetenv("AGENTHUB_REGISTRY_SYNC_FIXTURE")
	os.Unsetenv("AGENTHUB_MODEL_ROUTER_MODE")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.RegistrySyncEnabled {
		t.Fatal("synchronisation must be off by default")
	}
	if !cfg.RegistrySyncFixture {
		t.Fatal("the offline fixture client must be pinned by default")
	}
	if cfg.ModelRouterMode != registry.ProviderModeMock {
		t.Fatalf("router mode = %q, want mock", cfg.ModelRouterMode)
	}
	if cfg.RegistrySyncInterval <= 0 {
		t.Fatalf("interval = %v, want a usable default", cfg.RegistrySyncInterval)
	}
}

// An unparsable value must fall back to the default rather than fail the boot:
// a typo in one variable should not take a whole instance down.
func TestEnvHelpersFallBackOnGarbage(t *testing.T) {
	t.Setenv("AGENTHUB_MAX_PARALLEL", "many")
	if got := envInt("AGENTHUB_MAX_PARALLEL", 4); got != 4 {
		t.Fatalf("envInt = %d, want the default", got)
	}
	t.Setenv("AGENTHUB_SESSION_TTL", "soon")
	if got := envDur("AGENTHUB_SESSION_TTL", time.Hour); got != time.Hour {
		t.Fatalf("envDur = %v, want the default", got)
	}
	t.Setenv("AGENTHUB_SEED_DEMO", "perhaps")
	if got := envBool("AGENTHUB_SEED_DEMO", false); got {
		t.Fatalf("envBool = %v, want the default", got)
	}
	// The parsed forms are honoured.
	t.Setenv("AGENTHUB_SESSION_TTL", "2h")
	if got := envDur("AGENTHUB_SESSION_TTL", time.Hour); got != 2*time.Hour {
		t.Fatalf("envDur = %v, want 2h", got)
	}
}

// The backup schedule is seeded from the environment once, before the console
// ever saves one; it has to carry the mode through.
func TestBackupScheduleCarriesTheMode(t *testing.T) {
	cfg := &Config{
		BackupMode:      "daily",
		BackupInterval:  12 * time.Hour,
		BackupDailyTime: "04:30",
		BackupTimezone:  "UTC",
		BackupCron:      "0 4 * * *",
		BackupKeepLast:  5,
		BackupMaxAge:    14,
	}
	s := cfg.BackupSchedule()
	if s.Mode != "daily" || s.DailyTime != "04:30" || s.CronExpr != "0 4 * * *" || s.KeepLast != 5 {
		t.Fatalf("schedule = %+v", s)
	}
	if s.MaxAgeDays != 14 || s.Interval != 12*time.Hour || s.Timezone != "UTC" {
		t.Fatalf("schedule = %+v", s)
	}
}

func TestEnvHelpersParseLists(t *testing.T) {
	t.Setenv("AGENTHUB_CLI_ALLOWLIST", " git , ls ,")
	got := envList("AGENTHUB_CLI_ALLOWLIST", []string{"default"})
	if len(got) != 2 || got[0] != "git" || got[1] != "ls" {
		t.Fatalf("envList = %v", got)
	}
	t.Setenv("AGENTHUB_CLI_ALLOWLIST", "   ")
	if got := envList("AGENTHUB_CLI_ALLOWLIST", []string{"default"}); len(got) != 1 || got[0] != "default" {
		t.Fatalf("blank list should fall back, got %v", got)
	}
}

func TestLoadDotEnvDoesNotOverride(t *testing.T) {
	t.Setenv("AGENTHUB_TEST_KEY", "already-set")
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/.env", []byte("AGENTHUB_TEST_KEY=from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !applyDotEnv(dir + "/.env") {
		t.Fatal("applyDotEnv should report success")
	}
	if got := os.Getenv("AGENTHUB_TEST_KEY"); got != "already-set" {
		t.Fatalf("existing env was overwritten: %q", got)
	}
}

// unsetEnv blanks a variable and clears it afterwards so the case cannot leak
// into a sibling test through the shared process environment.
func unsetEnv(t *testing.T, key string) {
	t.Helper()
	if err := os.Unsetenv(key); err != nil {
		t.Fatalf("unsetenv %s: %v", key, err)
	}
	t.Cleanup(func() { _ = os.Unsetenv(key) })
}

func TestEnvHelpersReadRealValues(t *testing.T) {
	unsetEnv(t, "AGENTHUB_MAX_PARALLEL")
	if got := envInt("AGENTHUB_MAX_PARALLEL", 4); got != 4 {
		t.Fatalf("envInt with an unset variable = %d, want the default 4", got)
	}
	t.Setenv("AGENTHUB_MAX_PARALLEL", "12")
	if got := envInt("AGENTHUB_MAX_PARALLEL", 4); got != 12 {
		t.Fatalf("envInt = %d, want 12", got)
	}
	// A list made only of separators trims down to nothing and must fall back
	// instead of yielding an empty slice that would silently deny everything.
	t.Setenv("AGENTHUB_CLI_ALLOWLIST", ", ,")
	if got := envList("AGENTHUB_CLI_ALLOWLIST", []string{"default"}); len(got) != 1 || got[0] != "default" {
		t.Fatalf("separator-only list should fall back, got %v", got)
	}
	unsetEnv(t, "AGENTHUB_CLI_ALLOWLIST")
	if got := envList("AGENTHUB_CLI_ALLOWLIST", []string{"default"}); len(got) != 1 || got[0] != "default" {
		t.Fatalf("unset list should fall back, got %v", got)
	}
}

func sandboxSpecsJSON(t *testing.T, specs []sandbox.DeclarativeSpec) string {
	t.Helper()
	b, err := json.Marshal(specs)
	if err != nil {
		t.Fatalf("marshal specs: %v", err)
	}
	return string(b)
}

// A declarative template is the only way an operator adds a sandbox tool, so the
// loader pins the minimum shape before the provider itself validates the argv.
func TestParseSandboxProvidersAcceptsTemplate(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "sbx")
	raw := sandboxSpecsJSON(t, []sandbox.DeclarativeSpec{{
		Name:     "sbx",
		Layer:    "process",
		Binary:   bin,
		ExecArgs: [][]string{{"run"}},
	}})
	specs, err := ParseSandboxProviders(raw)
	if err != nil {
		t.Fatalf("ParseSandboxProviders: %v", err)
	}
	if len(specs) != 1 || specs[0].Name != "sbx" || specs[0].Binary != bin {
		t.Fatalf("specs = %+v", specs)
	}
}

func TestParseSandboxProvidersEmptyIsOff(t *testing.T) {
	for _, raw := range []string{"", "   ", "\n\t "} {
		specs, err := ParseSandboxProviders(raw)
		if err != nil {
			t.Fatalf("%q: unexpected error: %v", raw, err)
		}
		if specs != nil {
			t.Fatalf("%q: specs = %v, want nil", raw, specs)
		}
	}
}

func TestParseSandboxProvidersRejects(t *testing.T) {
	abs := filepath.Join(t.TempDir(), "sbx")
	valid := func() sandbox.DeclarativeSpec {
		return sandbox.DeclarativeSpec{Name: "sbx", Binary: abs, ExecArgs: [][]string{{"run"}}}
	}
	cases := []struct {
		name string
		raw  string
	}{
		{"broken json", "{"},
		{"object instead of array", `{"name":"sbx"}`},
		{"missing name", sandboxSpecsJSON(t, []sandbox.DeclarativeSpec{func() sandbox.DeclarativeSpec {
			s := valid()
			s.Name = "  "
			return s
		}()})},
		{"relative binary", sandboxSpecsJSON(t, []sandbox.DeclarativeSpec{func() sandbox.DeclarativeSpec {
			s := valid()
			s.Binary = "sbx"
			return s
		}()})},
		{"no exec args", sandboxSpecsJSON(t, []sandbox.DeclarativeSpec{func() sandbox.DeclarativeSpec {
			s := valid()
			s.ExecArgs = nil
			return s
		}()})},
		{"three exec args", sandboxSpecsJSON(t, []sandbox.DeclarativeSpec{func() sandbox.DeclarativeSpec {
			s := valid()
			s.ExecArgs = [][]string{{"a"}, {"b"}, {"c"}}
			return s
		}()})},
	}
	for _, c := range cases {
		if _, err := ParseSandboxProviders(c.raw); err == nil {
			t.Fatalf("%s: expected an error", c.name)
		}
	}
}

// validBaseConfig is the smallest configuration that passes Validate. Tests that
// pin one specific rejection have to start from something valid, otherwise the
// first check fires and the case under test never runs.
func validBaseConfig() *Config {
	return &Config{
		HTTPPort:        "3500",
		Env:             "dev",
		MaxParallel:     8,
		CLITimeout:      time.Second,
		MaxOutputBytes:  1024,
		EventBuffer:     64,
		DatabaseURL:     "postgres://agenthub:agenthub@localhost:5434/agenthub",
		PushRecordLimit: 100,
		SessionTTL:      time.Hour,
	}
}

// enableRouter fills every knob the scheduler's block checks, so a case can then
// break exactly one of them and be sure that is the check that fires.
func enableRouter(c *Config) {
	c.RouterEnabled = true
	c.RouterInterval = time.Second
	c.RouterBatchMax = 20
	c.RouterMaxTickets = 8
	c.RouterMaxAttempts = 3
	c.RouterTriggerTTL = time.Hour
	c.RouterPruneInterval = time.Minute
}

// The voice provider is off by default: an instance with no audio endpoint
// keeps the deterministic mock instead of asking for credentials nobody set.
func TestSpeechDefaultsStayOffline(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://agenthub:agenthub@localhost:5434/agenthub")
	for _, key := range []string{
		"AGENTHUB_SPEECH_ENDPOINT", "AGENTHUB_SPEECH_API_KEY_ENV",
		"AGENTHUB_SPEECH_STT_MODEL", "AGENTHUB_SPEECH_TTS_MODEL",
		"AGENTHUB_SPEECH_TIMEOUT", "AGENTHUB_SPEECH_EGRESS_ALLOWLIST",
	} {
		os.Unsetenv(key)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.SpeechEndpoint != "" {
		t.Fatalf("endpoint = %q, want empty for the mock", cfg.SpeechEndpoint)
	}
	if cfg.SpeechAPIKeyEnv != "AGENTHUB_SPEECH_API_KEY" {
		t.Fatalf("api key env = %q", cfg.SpeechAPIKeyEnv)
	}
	if cfg.SpeechSTTModel != adapter.DefaultSpeechSTTModel || cfg.SpeechTTSModel != adapter.DefaultSpeechTTSModel {
		t.Fatalf("models = %q / %q", cfg.SpeechSTTModel, cfg.SpeechTTSModel)
	}
	if cfg.SpeechTimeout != adapter.DefaultSpeechTimeout {
		t.Fatalf("timeout = %v", cfg.SpeechTimeout)
	}
	if len(cfg.SpeechEgressAllowlist) != 0 {
		t.Fatalf("egress = %v, want none", cfg.SpeechEgressAllowlist)
	}
}

// A real endpoint has to be an absolute http(s) URL, and the credential slot has
// to name an environment variable rather than hold the secret itself.
func TestSpeechValidation(t *testing.T) {
	cfg := validBaseConfig()
	cfg.SpeechEndpoint = "https://api.example.test/v1"
	cfg.SpeechAPIKeyEnv = "OPENAI_KEY"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("a valid speech endpoint must pass: %v", err)
	}

	cfg = validBaseConfig()
	cfg.SpeechEndpoint = "api.example.test"
	if err := cfg.Validate(); err == nil {
		t.Fatal("a bare host must be rejected")
	}

	cfg = validBaseConfig()
	cfg.SpeechEndpoint = "ftp://api.example.test"
	if err := cfg.Validate(); err == nil {
		t.Fatal("a non-http scheme must be rejected")
	}

	cfg = validBaseConfig()
	cfg.SpeechEndpoint = "https://api.example.test"
	cfg.SpeechAPIKeyEnv = "sk-live-123"
	if err := cfg.Validate(); err == nil {
		t.Fatal("a pasted secret must be rejected as an env name")
	}
}

// Empty voice knobs are filled with the documented defaults instead of running
// with a zero timeout or an unnamed model.
func TestSpeechValidateFillsDefaults(t *testing.T) {
	cfg := validBaseConfig()
	cfg.SpeechEndpoint = "https://api.example.test"
	cfg.SpeechSTTModel = "  "
	cfg.SpeechTTSModel = ""
	cfg.SpeechTimeout = 0
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if cfg.SpeechSTTModel != adapter.DefaultSpeechSTTModel || cfg.SpeechTTSModel != adapter.DefaultSpeechTTSModel {
		t.Fatalf("models = %q / %q", cfg.SpeechSTTModel, cfg.SpeechTTSModel)
	}
	if cfg.SpeechTimeout != adapter.DefaultSpeechTimeout {
		t.Fatalf("timeout = %v", cfg.SpeechTimeout)
	}
}

func TestIsEnvName(t *testing.T) {
	for _, ok := range []string{"KEY", "_KEY", "KEY_2", "openai_key"} {
		if !isEnvName(ok) {
			t.Fatalf("%q should be a valid env name", ok)
		}
	}
	for _, bad := range []string{"", "1KEY", "sk-123", "KEY-NAME", "KEY NAME"} {
		if isEnvName(bad) {
			t.Fatalf("%q should not be a valid env name", bad)
		}
	}
}

// A malformed template is a startup failure named after the variable, not a
// config that silently runs without the provider the operator asked for.
func TestLoadRejectsBadSandboxProviders(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://agenthub:agenthub@localhost:5434/agenthub")
	t.Setenv("AGENTHUB_SANDBOX_PROVIDERS", `{"name":"sbx"}`)
	if _, err := Load(); err == nil {
		t.Fatal("Load must reject a malformed AGENTHUB_SANDBOX_PROVIDERS")
	}
}

// The sandbox block is only enforced when the control plane is switched on, and
// every bound it checks maps to a real failure mode: a lease that outlives its
// sweep, a ceiling below the per-owner cap, a poll window that hangs a node.
func TestValidateSandboxBlock(t *testing.T) {
	sandboxConfig := func() *Config {
		c := validBaseConfig()
		c.SandboxEnabled = true
		c.SandboxProvider = sandbox.ProviderDocker
		c.SandboxIdleTTL = time.Minute
		c.SandboxMaxTTL = 10 * time.Minute
		c.SandboxMaxPerOwner = 4
		c.SandboxMaxTotal = 16
		c.SandboxNodePollTimeout = 30 * time.Second
		c.SandboxSweepInterval = time.Minute
		return c
	}

	if err := sandboxConfig().Validate(); err != nil {
		t.Fatalf("valid sandbox config rejected: %v", err)
	}

	// An unset provider defaults to docker instead of being rejected.
	c := sandboxConfig()
	c.SandboxProvider = ""
	if err := c.Validate(); err != nil {
		t.Fatalf("unset provider must default: %v", err)
	}
	if c.SandboxProvider != sandbox.ProviderDocker {
		t.Fatalf("provider = %q, want the docker default", c.SandboxProvider)
	}

	// A hand-built Config leaves the quota block zeroed; Validate fills it with
	// the shipped numbers instead of rejecting the instance.
	c = sandboxConfig()
	if c.SandboxCPU != 0 || c.SandboxMaxMemoryMB != 0 {
		t.Fatalf("the fixture must start with a zeroed quota block: %+v", c)
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("a zeroed quota block must be filled, not rejected: %v", err)
	}
	if c.SandboxCPU != 1 || c.SandboxMemoryMB != 1024 || c.SandboxPidsLimit != 128 {
		t.Fatalf("defaults = %g/%dMB/%d, want 1/1024/128", c.SandboxCPU, c.SandboxMemoryMB, c.SandboxPidsLimit)
	}
	if c.SandboxMaxCPU != 4 || c.SandboxMaxMemoryMB != 4096 || c.SandboxMaxPidsLimit != 512 {
		t.Fatalf("ceilings = %g/%dMB/%d, want 4/4096/512", c.SandboxMaxCPU, c.SandboxMaxMemoryMB, c.SandboxMaxPidsLimit)
	}

	// And an operator who sizes both halves keeps exactly what they wrote.
	c = sandboxConfig()
	c.SandboxCPU, c.SandboxMemoryMB, c.SandboxPidsLimit = 2, 2048, 256
	c.SandboxMaxCPU, c.SandboxMaxMemoryMB, c.SandboxMaxPidsLimit = 8, 8192, 1024
	if err := c.Validate(); err != nil {
		t.Fatalf("explicit quotas must be accepted: %v", err)
	}
	if c.SandboxCPU != 2 || c.SandboxMaxMemoryMB != 8192 {
		t.Fatalf("explicit quotas were rewritten: %g/%d", c.SandboxCPU, c.SandboxMaxMemoryMB)
	}

	cases := []struct {
		name string
		mut  func(*Config)
	}{
		{"zero idle ttl", func(c *Config) { c.SandboxIdleTTL = 0 }},
		{"zero max ttl", func(c *Config) { c.SandboxMaxTTL = 0 }},
		{"max ttl below idle ttl", func(c *Config) { c.SandboxMaxTTL = 30 * time.Second }},
		{"zero per owner", func(c *Config) { c.SandboxMaxPerOwner = 0 }},
		{"per owner above the cap", func(c *Config) { c.SandboxMaxPerOwner = 65 }},
		{"total below per owner", func(c *Config) { c.SandboxMaxTotal = 3 }},
		{"total above the cap", func(c *Config) { c.SandboxMaxTotal = 300 }},
		{"zero node poll timeout", func(c *Config) { c.SandboxNodePollTimeout = 0 }},
		{"node poll timeout above a minute", func(c *Config) { c.SandboxNodePollTimeout = 90 * time.Second }},
		{"zero sweep interval", func(c *Config) { c.SandboxSweepInterval = 0 }},
		{"sweep interval above ten minutes", func(c *Config) { c.SandboxSweepInterval = 11 * time.Minute }},
		// Quotas: a zero default is "no ceiling", which is not what an operator
		// means to write, and a ceiling below the default would clip every
		// resource before it runs.
		{"negative cpu default", func(c *Config) {
			c.SandboxCPU, c.SandboxMemoryMB, c.SandboxPidsLimit = -1, 1024, 128
		}},
		{"zero memory default", func(c *Config) {
			c.SandboxCPU, c.SandboxMemoryMB, c.SandboxPidsLimit = 1, 0, 128
		}},
		{"ceiling below the default", func(c *Config) {
			c.SandboxCPU, c.SandboxMemoryMB, c.SandboxPidsLimit = 2, 1024, 128
			c.SandboxMaxCPU, c.SandboxMaxMemoryMB, c.SandboxMaxPidsLimit = 1, 4096, 512
		}},
		{"ceiling out of range", func(c *Config) {
			c.SandboxCPU, c.SandboxMemoryMB, c.SandboxPidsLimit = 1, 1024, 128
			c.SandboxMaxCPU, c.SandboxMaxMemoryMB, c.SandboxMaxPidsLimit = 1024, 4096, 512
		}},
		{"relative work directory root", func(c *Config) {
			c.SandboxWorkDirRoots = []string{"srv/work"}
		}},
		{"sandbox ceiling below the router ticket bound", func(c *Config) {
			c.RouterEnabled = true
			c.RouterInterval = time.Second
			c.RouterBatchMax = 10
			c.RouterMaxTickets = 32
			c.RouterMaxAttempts = 3
			c.RouterTriggerTTL = time.Hour
			c.RouterPruneInterval = time.Minute
			c.SandboxMaxTotal = 16
		}},
	}
	for _, tc := range cases {
		cfg := sandboxConfig()
		tc.mut(cfg)
		if err := cfg.Validate(); err == nil {
			t.Fatalf("%s: expected a validation error", tc.name)
		}
	}
}

// The same sandbox block must be ignored while the feature is off: an instance
// that never leases a sandbox is not misconfigured by zeroed knobs.
func TestValidateIgnoresSandboxWhenDisabled(t *testing.T) {
	cfg := validBaseConfig()
	cfg.SandboxEnabled = false
	cfg.SandboxIdleTTL = 0
	cfg.SandboxMaxTTL = 0
	cfg.SandboxMaxPerOwner = 0
	cfg.SandboxMaxTotal = 0
	if err := cfg.Validate(); err != nil {
		t.Fatalf("sandbox off must stay valid: %v", err)
	}
}
