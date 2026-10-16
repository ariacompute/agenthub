// Package config loads agenthub configuration from the environment.
//
// Every knob has a documented default and the whole set is validated before the
// server starts: a broken configuration fails fast instead of silently
// degrading at runtime.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Version is the agenthub release reported by /healthz and the Agent Card. It
// carries the git tag verbatim, prefix included ("v0.2.0").
//
// It is a var, not a const, so the build can stamp the real release from git:
//
//	go build -ldflags "-X agenthub/internal/config.Version=$(git describe --tags --abbrev=0 --always)"
//
// The fallback is a dev sentinel on purpose: an unstamped build must be obvious
// in /healthz rather than silently claiming a release number it never was.
var Version = "dev"

// Commit is the seven character git revision the binary was built from; the
// console renders it next to Version as "v0.2.0@1a2b3c4". Stamped the same way
// as Version, and likewise a var so the linker can overwrite it.
var Commit = "unknown"

// Distance is how many commits the build sits after the tag in Version: "0"
// means the build is exactly at the tag. Combined with Version and Commit it
// tells you whether a binary is a release or an unreleased point in history.
var Distance = "0"

// Dirty is "true" when the working tree had uncommitted changes at build time,
// so a binary can never be mistaken for a reproducible release.
var Dirty = "false"

// ProtocolVersion is the A2A revision implemented by internal/a2a. It tracks the
// protocol, not the build, so it is never stamped and stays a const.
const ProtocolVersion = "0.2.0"

// Build returns the "version@commit" string the console and logs show, e.g.
// "v0.2.0@1a2b3c4". It degrades to the version alone when the build carried no
// commit, so an unstamped binary never renders a misleading "…@unknown".
func Build() string {
	if Commit == "" || Commit == "unknown" {
		return Version
	}
	return Version + "@" + Commit
}

// BuildNote summarises how far a build diverges from its tag: "" when the build
// sits exactly on a clean tag, otherwise "+<n>" for the commits since the tag
// and/or "dirty", e.g. "+2 dirty". The console appends it to Build() so an
// unreleased or locally modified build is visible at a glance.
func BuildNote() string {
	parts := make([]string, 0, 2)
	if Distance != "" && Distance != "0" {
		parts = append(parts, "+"+Distance)
	}
	if Dirty == "true" {
		parts = append(parts, "dirty")
	}
	return strings.Join(parts, " ")
}

// Config is the fully validated runtime configuration.
type Config struct {
	HTTPPort        string
	Env             string
	StaticDir       string
	AdminToken      string
	AuthOpenMode    bool
	DatabaseURL     string
	LegacyMethods   bool
	MaxParallel     int
	CLITimeout      time.Duration
	CLIAllowlist    []string
	MaxOutputBytes  int
	EventBuffer     int
	PushEnabled     bool
	PushRecordLimit int
}

// Load reads the environment (after LoadDotEnv) and validates the result.
func Load() (*Config, error) {
	c := &Config{
		HTTPPort:   envStr("AGENTHUB_HTTP_PORT", "3500"),
		Env:        envStr("AGENTHUB_ENV", "dev"),
		StaticDir:  envStr("AGENTHUB_STATIC_DIR", "./backend/static"),
		AdminToken: envStr("AGENTHUB_ADMIN_TOKEN", ""),
		// Local bootstrap convenience: open mode defaults on, but Validate
		// refuses to run a production deployment without a token.
		AuthOpenMode:   envBool("AGENTHUB_AUTH_OPEN_MODE", true),
		DatabaseURL:    envStr("DATABASE_URL", ""),
		LegacyMethods:  envBool("AGENTHUB_A2A_LEGACY_METHODS", true),
		MaxParallel:    envInt("AGENTHUB_MAX_PARALLEL", 8),
		CLITimeout:     envDur("AGENTHUB_CLI_TIMEOUT", 15*time.Second),
		CLIAllowlist:   envList("AGENTHUB_CLI_ALLOWLIST", []string{"git", "ls", "cat", "echo", "date", "aria-memo"}),
		MaxOutputBytes: envInt("AGENTHUB_MAX_OUTPUT_BYTES", 65536),
		EventBuffer:    envInt("AGENTHUB_EVENT_BUFFER", 512),
		PushEnabled:    envBool("AGENTHUB_PUSH_ENABLED", true),
		// Retention cap for the push callback audit trail, per task.
		PushRecordLimit: envInt("AGENTHUB_PUSH_RECORD_LIMIT", 100),
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return c, nil
}

// Validate rejects impossible combinations instead of silently falling back.
func (c *Config) Validate() error {
	if c.HTTPPort == "" {
		return fmt.Errorf("AGENTHUB_HTTP_PORT must not be empty")
	}
	if p, err := strconv.Atoi(c.HTTPPort); err != nil || p <= 0 || p > 65535 {
		return fmt.Errorf("AGENTHUB_HTTP_PORT must be a port number, got %q", c.HTTPPort)
	}
	switch c.Env {
	case "dev", "staging", "prod":
	default:
		return fmt.Errorf("AGENTHUB_ENV must be dev, staging or prod, got %q", c.Env)
	}
	if c.MaxParallel < 1 || c.MaxParallel > 64 {
		return fmt.Errorf("AGENTHUB_MAX_PARALLEL must be 1..64, got %d", c.MaxParallel)
	}
	if c.CLITimeout <= 0 {
		return fmt.Errorf("AGENTHUB_CLI_TIMEOUT must be positive")
	}
	if c.MaxOutputBytes < 1 {
		return fmt.Errorf("AGENTHUB_MAX_OUTPUT_BYTES must be positive")
	}
	if c.EventBuffer < 1 {
		return fmt.Errorf("AGENTHUB_EVENT_BUFFER must be positive")
	}
	// Persistence is mandatory: agenthub is a database backed service and a
	// missing DATABASE_URL must fail fast instead of silently running on a
	// state that disappears with the process.
	if strings.TrimSpace(c.DatabaseURL) == "" {
		return fmt.Errorf("DATABASE_URL must be set")
	}
	if c.PushRecordLimit < 1 {
		return fmt.Errorf("AGENTHUB_PUSH_RECORD_LIMIT must be positive, got %d", c.PushRecordLimit)
	}
	if c.Env == "prod" && c.AdminToken == "" && c.AuthOpenMode {
		return fmt.Errorf("AGENTHUB_ENV=prod requires AGENTHUB_ADMIN_TOKEN (or AGENTHUB_AUTH_OPEN_MODE=false with another auth layer)")
	}
	return nil
}

// ControlPlaneOpen reports whether /api/* is reachable without a token.
func (c *Config) ControlPlaneOpen() bool {
	return c.AdminToken == "" && c.AuthOpenMode
}

func envStr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func envBool(key string, def bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}

func envDur(key string, def time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return def
	}
	return d
}

func envList(key string, def []string) []string {
	v := os.Getenv(key)
	if strings.TrimSpace(v) == "" {
		return def
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return def
	}
	return out
}
