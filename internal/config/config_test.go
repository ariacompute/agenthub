package config

import (
	"os"
	"strings"
	"testing"
	"time"
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
		"AGENTHUB_HTTP_PORT", "AGENTHUB_ENV", "AGENTHUB_ADMIN_TOKEN",
		"AGENTHUB_AUTH_OPEN_MODE", "AGENTHUB_MAX_PARALLEL", "AGENTHUB_CLI_TIMEOUT",
	} {
		t.Setenv(key, "")
		os.Unsetenv(key)
	}
	t.Setenv("DATABASE_URL", "postgres://agenthub:agenthub@localhost:5434/agenthub?sslmode=disable")
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
	if !cfg.ControlPlaneOpen() {
		t.Fatal("open mode should be enabled by default with no token")
	}
	if cfg.SeedDemo {
		t.Fatal("SeedDemo must default to off: an instance starts with only the resources you register")
	}
}

// The demo resources are the only "mock data" agenthub can insert, so they must
// be opt-in and impossible to enable in production.
func TestSeedDemoIsOptIn(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://agenthub:agenthub@localhost:5434/agenthub?sslmode=disable")

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
	t.Setenv("AGENTHUB_ADMIN_TOKEN", "secret")
	t.Setenv("AGENTHUB_AUTH_OPEN_MODE", "false")
	if _, err := Load(); err == nil {
		t.Fatal("production must refuse demo resources")
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
		{"bad port", func(c *Config) { c.HTTPPort = "not-a-port" }},
		{"bad env", func(c *Config) { c.Env = "whatever" }},
		{"zero parallel", func(c *Config) { c.MaxParallel = 0 }},
		{"negative timeout", func(c *Config) { c.CLITimeout = -time.Second }},
		{"zero output cap", func(c *Config) { c.MaxOutputBytes = 0 }},
		{"prod without token", func(c *Config) { c.Env = "prod"; c.AdminToken = ""; c.AuthOpenMode = true }},
		{"missing database url", func(c *Config) { c.DatabaseURL = "" }},
		{"blank database url", func(c *Config) { c.DatabaseURL = "   " }},
		{"zero push record limit", func(c *Config) { c.PushRecordLimit = 0 }},
	}
	for _, c := range cases {
		cfg := &Config{
			HTTPPort:        "3500",
			Env:             "dev",
			MaxParallel:     8,
			CLITimeout:      time.Second,
			MaxOutputBytes:  1024,
			EventBuffer:     64,
			DatabaseURL:     "postgres://agenthub:agenthub@localhost:5434/agenthub?sslmode=disable",
			PushRecordLimit: 100,
		}
		c.mut(cfg)
		if err := cfg.Validate(); err == nil {
			t.Fatalf("%s: expected validation error", c.name)
		}
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
