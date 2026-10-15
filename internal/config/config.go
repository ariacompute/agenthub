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

const (
	// Version is the agenthub release reported by /healthz and the Agent Card.
	Version = "0.1.0"
	// ProtocolVersion is the A2A revision implemented by internal/a2a.
	ProtocolVersion = "0.2.0"
)

// Config is the fully validated runtime configuration.
type Config struct {
	HTTPPort       string
	Env            string
	StaticDir      string
	AdminToken     string
	AuthOpenMode   bool
	DataFile       string
	LegacyMethods  bool
	MaxParallel    int
	CLITimeout     time.Duration
	CLIAllowlist   []string
	MaxOutputBytes int
	EventBuffer    int
	PushEnabled    bool
}

// Load reads the environment (after LoadDotEnv) and validates the result.
func Load() (*Config, error) {
	c := &Config{
		HTTPPort:       envStr("AGENTHUB_HTTP_PORT", "3500"),
		Env:            envStr("AGENTHUB_ENV", "dev"),
		StaticDir:      envStr("AGENTHUB_STATIC_DIR", "./backend/static"),
		AdminToken:     envStr("AGENTHUB_ADMIN_TOKEN", ""),
		// Local bootstrap convenience: open mode defaults on, but Validate
		// refuses to run a production deployment without a token.
		AuthOpenMode:   envBool("AGENTHUB_AUTH_OPEN_MODE", true),
		DataFile:       envStr("AGENTHUB_DATA_FILE", ""),
		LegacyMethods:  envBool("AGENTHUB_A2A_LEGACY_METHODS", true),
		MaxParallel:    envInt("AGENTHUB_MAX_PARALLEL", 8),
		CLITimeout:     envDur("AGENTHUB_CLI_TIMEOUT", 15*time.Second),
		CLIAllowlist:   envList("AGENTHUB_CLI_ALLOWLIST", []string{"git", "ls", "cat", "echo", "date", "aria-memo"}),
		MaxOutputBytes: envInt("AGENTHUB_MAX_OUTPUT_BYTES", 65536),
		EventBuffer:    envInt("AGENTHUB_EVENT_BUFFER", 512),
		PushEnabled:    envBool("AGENTHUB_PUSH_ENABLED", true),
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
