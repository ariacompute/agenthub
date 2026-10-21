// Package config loads agenthub configuration from the environment.
//
// Every knob has a documented default and the whole set is validated before the
// server starts: a broken configuration fails fast instead of silently
// degrading at runtime.
package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"agenthub/internal/adapter"
	"agenthub/internal/auth"
	"agenthub/internal/backup"
	"agenthub/internal/registry"
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
const ProtocolVersion = "1.0.0"

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
	HTTPPort  string
	Env       string
	StaticDir string
	// Identity: the bootstrap administrator is the only account that exists
	// before someone registers, and .env is how an operator seeds it.
	BootstrapAdminEmail    string
	BootstrapAdminPassword string
	BootstrapAdminName     string
	// Session lifetime and whether the session cookie may only travel over
	// https.
	SessionTTL   time.Duration
	CookieSecure bool
	// PublicBaseURL is the externally visible origin, used to build the OAuth
	// redirect URI. Empty falls back to the request host at redirect time.
	PublicBaseURL string
	DatabaseURL   string
	// MigratorDatabaseURL, when set, is a higher-privilege DSN used only to run
	// schema migrations. The runtime DatabaseURL should be the least-privilege
	// app role; the migrator role owns DDL. See scripts/provision-db-roles.sql.
	MigratorDatabaseURL string
	// SeedDemo inserts the five demo resources at startup. It is off by
	// default: an instance should start with whatever the operator registers,
	// not with sample rows nobody asked for. Turn it on for a local walkthrough
	// or for scripts/test-api.sh, which targets those resources.
	SeedDemo        bool
	LegacyMethods   bool
	MaxParallel     int
	CLITimeout      time.Duration
	CLIAllowlist    []string
	AgentTimeout    time.Duration
	MaxOutputBytes  int
	EventBuffer     int
	PushEnabled     bool
	PushRecordLimit int
	// EncryptionKey, when set, isolates static-at-rest encryption (any future
	// PII or provider secret columns) from the rest of the system. It is read
	// from the environment only and never persisted. Production requires it to be
	// at least 32 bytes; when empty a dev-only key is derived with a warning.
	EncryptionKey string
	// PushSecret, when set, enables signed A2A push callbacks: the outgoing
	// payload is HMAC-SHA256 signed and carries a timestamp + nonce so the
	// receiver can verify authenticity, freshness and replay. It is never
	// persisted and should be a strong, randomly generated value shared only
	// with the callback peer.
	PushSecret string
	// Database checkpoints. BackupDir empty means the feature is off: no
	// directory, no files, and the console says so. The rest are only the
	// initial policy — the console edits the schedule afterwards and it is
	// persisted, so a restart does not reset an operator's choice.
	BackupDir       string
	BackupMode      string
	BackupInterval  time.Duration
	BackupDailyTime string
	BackupTimezone  string
	BackupCron      string
	BackupKeepLast  int
	BackupMaxAge    int
	// Upstream registry synchronisation.
	//
	// RegistrySyncEnabled starts the background puller; the console can still
	// trigger a source by hand when it is off, which is the default posture
	// most deployments want.
	RegistrySyncEnabled bool
	// RegistrySyncInterval is how often the background puller runs. It is a
	// plain interval rather than a per-source cron: a schedule on a source is
	// honoured by the puller, and an instance with one policy is easier to
	// reason about than one timer per source.
	RegistrySyncInterval time.Duration
	// RegistrySyncFixture forces every source onto the offline client, whatever
	// the source itself says. It is the air-gapped switch: no agenthub process
	// opens a connection to an upstream registry while it is on.
	RegistrySyncFixture bool
	// ModelRouterMode is the provider mode a model resource defaults to when it
	// declares none: "mock" keeps an instance without credentials usable.
	ModelRouterMode string
	// OAuth providers. A provider without credentials is simply absent from the
	// login page, so an instance without any of them stays self contained.
	OAuth auth.OAuthConfig
}

// BackupEnabled reports whether checkpointing is configured at all. Everything
// else about backups is gated on this one answer, so an instance without a
// directory behaves exactly as it did before the feature existed.
func (c *Config) BackupEnabled() bool { return strings.TrimSpace(c.BackupDir) != "" }

// BackupSchedule is the policy a fresh schedule row is seeded from. It is only
// consulted when the console has never saved one.
func (c *Config) BackupSchedule() backup.Schedule {
	s := backup.DefaultSchedule()
	s.Mode = c.BackupMode
	s.Enabled = c.BackupMode != backup.ModeOff
	s.Interval = c.BackupInterval
	s.DailyTime = c.BackupDailyTime
	s.Timezone = c.BackupTimezone
	s.CronExpr = c.BackupCron
	s.KeepLast = c.BackupKeepLast
	s.MaxAgeDays = c.BackupMaxAge
	return s
}

// Load reads the environment (after LoadDotEnv) and validates the result.
func Load() (*Config, error) {
	c := &Config{
		HTTPPort:               envStr("AGENTHUB_HTTP_PORT", "3500"),
		Env:                    envStr("AGENTHUB_ENV", "dev"),
		StaticDir:              envStr("AGENTHUB_STATIC_DIR", "./backend/static"),
		BootstrapAdminEmail:    envStr("AGENTHUB_BOOTSTRAP_ADMIN_EMAIL", ""),
		BootstrapAdminPassword: envStr("AGENTHUB_BOOTSTRAP_ADMIN_PASSWORD", ""),
		BootstrapAdminName:     envStr("AGENTHUB_BOOTSTRAP_ADMIN_NAME", ""),
		SessionTTL:             envDur("AGENTHUB_SESSION_TTL", 12*time.Hour),
		// Secure cookies by default in production; a plain http dev box can
		// turn them off.
		CookieSecure:        envBool("AGENTHUB_COOKIE_SECURE", envStr("AGENTHUB_ENV", "dev") == "prod"),
		PublicBaseURL:       envStr("AGENTHUB_PUBLIC_BASE_URL", ""),
		DatabaseURL:         envStr("DATABASE_URL", ""),
		MigratorDatabaseURL: envStr("AGENTHUB_MIGRATOR_DATABASE_URL", ""),
		SeedDemo:            envBool("AGENTHUB_SEED_DEMO", false),
		LegacyMethods:       envBool("AGENTHUB_A2A_LEGACY_METHODS", true),
		MaxParallel:         envInt("AGENTHUB_MAX_PARALLEL", 8),
		CLITimeout:          envDur("AGENTHUB_CLI_TIMEOUT", 15*time.Second),
		CLIAllowlist:        envList("AGENTHUB_CLI_ALLOWLIST", []string{"git", "ls", "cat", "echo", "date", "aria-memo"}),
		AgentTimeout:        envDur("AGENTHUB_AGENT_TIMEOUT", adapter.DefaultAgentTimeout),
		MaxOutputBytes:      envInt("AGENTHUB_MAX_OUTPUT_BYTES", 65536),
		EventBuffer:         envInt("AGENTHUB_EVENT_BUFFER", 512),
		PushEnabled:         envBool("AGENTHUB_PUSH_ENABLED", true),
		// Retention cap for the push callback audit trail, per task.
		PushRecordLimit: envInt("AGENTHUB_PUSH_RECORD_LIMIT", 100),
		PushSecret:      os.Getenv("AGENTHUB_PUSH_SECRET"),
		EncryptionKey:   os.Getenv("AGENTHUB_ENCRYPTION_KEY"),
		// Checkpoints are off until a directory is configured: a container with
		// no volume must not quietly fill its own writable layer.
		BackupDir:       envStr("AGENTHUB_BACKUP_DIR", ""),
		BackupMode:      envStr("AGENTHUB_BACKUP_MODE", backup.ModeOff),
		BackupInterval:  envDur("AGENTHUB_BACKUP_INTERVAL", 24*time.Hour),
		BackupDailyTime: envStr("AGENTHUB_BACKUP_DAILY_TIME", "03:00"),
		BackupTimezone:  envStr("AGENTHUB_BACKUP_TIMEZONE", "UTC"),
		BackupCron:      envStr("AGENTHUB_BACKUP_CRON", ""),
		BackupKeepLast:  envInt("AGENTHUB_BACKUP_KEEP_LAST", 7),
		BackupMaxAge:    envInt("AGENTHUB_BACKUP_MAX_AGE_DAYS", 30),
		// Synchronisation is off by default: an instance must not start
		// reaching out to an upstream registry until an operator configured
		// one and decided how often to pull it.
		RegistrySyncEnabled:  envBool("AGENTHUB_REGISTRY_SYNC_ENABLED", false),
		RegistrySyncInterval: envDur("AGENTHUB_REGISTRY_SYNC_INTERVAL", time.Hour),
		// On by default: turning synchronisation on must not silently turn
		// outbound traffic on with it. An operator who wants a real upstream
		// registry sets this to false explicitly.
		RegistrySyncFixture: envBool("AGENTHUB_REGISTRY_SYNC_FIXTURE", true),
		ModelRouterMode:     envStr("AGENTHUB_MODEL_ROUTER_MODE", registry.ProviderModeMock),
		OAuth: auth.OAuthConfig{
			Enabled: envBool("AGENTHUB_OAUTH_ENABLED", true),
			Google: auth.ProviderConfig{
				ClientID:     envStr("AGENTHUB_OAUTH_GOOGLE_CLIENT_ID", ""),
				ClientSecret: envStr("AGENTHUB_OAUTH_GOOGLE_CLIENT_SECRET", ""),
				Scopes:       envList("AGENTHUB_OAUTH_GOOGLE_SCOPES", nil),
			},
			GitHub: auth.ProviderConfig{
				ClientID:     envStr("AGENTHUB_OAUTH_GITHUB_CLIENT_ID", ""),
				ClientSecret: envStr("AGENTHUB_OAUTH_GITHUB_CLIENT_SECRET", ""),
				Scopes:       envList("AGENTHUB_OAUTH_GITHUB_SCOPES", nil),
			},
			OIDC: auth.ProviderConfig{
				Issuer:       envStr("AGENTHUB_OAUTH_OIDC_ISSUER", ""),
				ClientID:     envStr("AGENTHUB_OAUTH_OIDC_CLIENT_ID", ""),
				ClientSecret: envStr("AGENTHUB_OAUTH_OIDC_CLIENT_SECRET", ""),
				Scopes:       envList("AGENTHUB_OAUTH_OIDC_SCOPES", nil),
			},
		},
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
	// AgentTimeout is deliberately not required: zero is a legitimate "unset" for
	// a config built by hand, and the adapter substitutes its own default.
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
	// In production the database connection must not be cleartext: a missing or
	// disabled sslmode would let credentials and data traverse the wire in the
	// clear. dev/staging are exempt so a laptop can use a local no-TLS postgres.
	if c.Env == "prod" && !HasSecureSSLMode(c.DatabaseURL) {
		return fmt.Errorf("DATABASE_URL must set sslmode=require|verify-ca|verify-full in production")
	}
	if c.PushRecordLimit < 1 {
		return fmt.Errorf("AGENTHUB_PUSH_RECORD_LIMIT must be positive, got %d", c.PushRecordLimit)
	}
	if c.SessionTTL <= 0 {
		return fmt.Errorf("AGENTHUB_SESSION_TTL must be positive")
	}
	// The single shared admin token is gone: identities live in the database, so
	// a production deployment must arrive with an administrator it can log in as.
	if c.Env == "prod" && strings.TrimSpace(c.BootstrapAdminEmail) == "" {
		return fmt.Errorf("AGENTHUB_ENV=prod requires AGENTHUB_BOOTSTRAP_ADMIN_EMAIL (the shared AGENTHUB_ADMIN_TOKEN was removed)")
	}
	if c.BootstrapAdminEmail != "" && !auth.ValidEmail(auth.NormalizeEmail(c.BootstrapAdminEmail)) {
		return fmt.Errorf("AGENTHUB_BOOTSTRAP_ADMIN_EMAIL is not a valid address: %q", c.BootstrapAdminEmail)
	}
	// Demo resources in production would be indistinguishable from real ones.
	if c.Env == "prod" && c.SeedDemo {
		return fmt.Errorf("AGENTHUB_SEED_DEMO must stay off when AGENTHUB_ENV=prod")
	}
	// Static-at-rest encryption key: required and strong in production. The key
	// protects any encrypted column and a weak or missing value would undermine
	// the encryption domain entirely.
	if c.Env == "prod" && len(c.EncryptionKey) < 32 {
		return fmt.Errorf("AGENTHUB_ENCRYPTION_KEY must be at least 32 bytes in production")
	}
	// A push secret, when configured, must be strong enough to be a real HMAC key.
	if c.PushSecret != "" && len(c.PushSecret) < 16 {
		return fmt.Errorf("AGENTHUB_PUSH_SECRET must be at least 16 bytes")
	}
	// Backup settings are only checked when checkpointing is switched on: an
	// instance without a directory stays valid exactly as it was, and one with
	// a directory must never start with a policy that silently does nothing.
	if c.BackupEnabled() {
		if err := c.BackupSchedule().Validate(); err != nil {
			return fmt.Errorf("backup schedule is invalid: %w", err)
		}
	}
	// The interval only matters when the background puller is on; leaving it
	// unset on an instance that never synchronises is not a misconfiguration.
	if c.RegistrySyncEnabled && c.RegistrySyncInterval <= 0 {
		return fmt.Errorf("AGENTHUB_REGISTRY_SYNC_INTERVAL must be positive when AGENTHUB_REGISTRY_SYNC_ENABLED is true")
	}
	// An unset mode is the offline mock: a caller that built the struct
	// directly instead of going through Load must not be rejected for it.
	if strings.TrimSpace(c.ModelRouterMode) == "" {
		c.ModelRouterMode = registry.ProviderModeMock
	}
	switch c.ModelRouterMode {
	case registry.ProviderModeMock, registry.ProviderModeOpenAICompatible:
	default:
		return fmt.Errorf("AGENTHUB_MODEL_ROUTER_MODE must be %q or %q, got %q",
			registry.ProviderModeMock, registry.ProviderModeOpenAICompatible, c.ModelRouterMode)
	}
	return nil
}

// AuthMode is the identity mode reported by /healthz. agenthub always
// authenticates against the users table now: the shared admin token is gone.
func (c *Config) AuthMode() string { return "session" }

// HasSecureSSLMode reports whether dsn requests TLS to the database. It accepts
// both the URI form (postgres://...?sslmode=require) and the keyword form
// (host=... sslmode=verify-full) that libpq accepts.
func HasSecureSSLMode(dsn string) bool {
	if parsed, err := url.Parse(dsn); err == nil && parsed.Scheme != "" {
		switch strings.ToLower(parsed.Query().Get("sslmode")) {
		case "require", "verify-ca", "verify-full":
			return true
		}
		return false
	}
	// Keyword form: scan for an sslmode= token.
	for _, kv := range strings.Fields(dsn) {
		if strings.HasPrefix(kv, "sslmode=") {
			mode := strings.ToLower(strings.TrimPrefix(kv, "sslmode="))
			return mode == "require" || mode == "verify-ca" || mode == "verify-full"
		}
	}
	return false
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
