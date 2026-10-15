package config

import (
	"os"
	"testing"
	"time"
)

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
