package registry

import (
	"context"
	"strings"
	"time"

	"agenthub/internal/httputil"
)

// Upstream registry kinds agenthub can synchronise from.
const (
	// SourceKindMCP is an MCP Registry: a catalogue of MCP server records.
	SourceKindMCP = "mcp"
	// SourceKindSkill is a skill registry: a catalogue of skill descriptors.
	SourceKindSkill = "skill"
)

// SourceKinds lists the upstream kinds, in presentation order.
var SourceKinds = []string{SourceKindMCP, SourceKindSkill}

// Auth schemes an upstream registry may require.
const (
	AuthSchemeNone   = "none"
	AuthSchemeBearer = "bearer"
)

// AuthSchemesForSources lists the accepted upstream auth schemes.
var AuthSchemesForSources = []string{AuthSchemeNone, AuthSchemeBearer}

// Sync statuses recorded on a source after an attempt.
const (
	SyncStatusOK     = "ok"
	SyncStatusFailed = "failed"
)

// SourceError codes.
const (
	CodeInvalidSource = httputil.Code("invalid_registry_source")
	CodeSyncInFlight  = httputil.Code("registry_sync_in_flight")
)

// RegistrySource is one upstream catalogue agenthub pulls records from.
//
// AWS calls the same idea URL-based synchronization: a publisher points the
// registry at a live endpoint and the record stays in sync instead of being
// edited by hand. The outbound credential is referenced by name only — the
// token itself never enters the database, the API or the logs.
type RegistrySource struct {
	ID           string `json:"id"`
	Namespace    string `json:"namespace"`
	Kind         string `json:"kind"`
	Name         string `json:"name"`
	URL          string `json:"url"`
	AuthScheme   string `json:"auth_scheme,omitempty"`
	AuthTokenEnv string `json:"auth_token_env,omitempty"`
	// Fixture switches the source to the offline, deterministic client. It is
	// what lets a deployment — and the whole test suite — exercise the sync
	// pipeline without reaching the network.
	Fixture    bool       `json:"fixture"`
	Enabled    bool       `json:"enabled"`
	Schedule   string     `json:"schedule,omitempty"`
	LastSyncAt *time.Time `json:"last_sync_at,omitempty"`
	// LastSyncStatus is "ok" or "failed"; empty means never run.
	LastSyncStatus string    `json:"last_sync_status,omitempty"`
	LastSyncCount  int       `json:"last_sync_count,omitempty"`
	LastSyncError  string    `json:"last_sync_error,omitempty"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

// EffectiveNamespace returns the namespace records from this source land in.
func (s *RegistrySource) EffectiveNamespace() string {
	if s == nil || strings.TrimSpace(s.Namespace) == "" {
		return DefaultNamespace
	}
	return strings.ToLower(strings.TrimSpace(s.Namespace))
}

// SyncResult is the outcome of one synchronisation run.
type SyncResult struct {
	Status   string    `json:"status"`
	Imported int       `json:"imported"`
	Updated  int       `json:"updated"`
	Skipped  int       `json:"skipped"`
	Total    int       `json:"total"`
	Error    string    `json:"error,omitempty"`
	At       time.Time `json:"at"`
}

// Client fetches records from one upstream registry.
//
// Two implementations exist: the offline fixture and the real HTTP client. Both
// return plain entries so the orchestration behind them is identical and a
// deployment can switch without touching anything else.
type Client interface {
	Fetch(ctx context.Context, src RegistrySource) ([]*Entry, error)
}

// SourceStore persists sources and their sync outcome. It is the port the
// synchroniser delegates to; the GORM implementation lives in internal/store.
type SourceStore interface {
	UpsertSource(ctx context.Context, s *RegistrySource) (*RegistrySource, error)
	GetSource(ctx context.Context, id string) (*RegistrySource, error)
	ListSources(ctx context.Context, namespace string) ([]*RegistrySource, error)
	DeleteSource(ctx context.Context, id string) error
	MarkSync(ctx context.Context, id string, r SyncResult) error
}

// ValidateSource checks a source before it is stored or run.
func ValidateSource(s *RegistrySource) error {
	if s == nil {
		return &httputil.Error{Code: CodeInvalidSource}
	}
	if strings.TrimSpace(s.Name) == "" {
		return newFieldError(CodeInvalidSource, "name")
	}
	known := false
	for _, k := range SourceKinds {
		if s.Kind == k {
			known = true
			break
		}
	}
	if !known {
		return newFieldError(CodeInvalidSource, "kind")
	}
	if !isHTTPURL(s.URL) {
		return newFieldError(CodeInvalidSource, "url")
	}
	scheme := strings.TrimSpace(s.AuthScheme)
	if scheme == "" {
		scheme = AuthSchemeNone
	}
	knownScheme := false
	for _, a := range AuthSchemesForSources {
		if scheme == a {
			knownScheme = true
			break
		}
	}
	if !knownScheme {
		return newFieldError(CodeInvalidSource, "auth_scheme")
	}
	if scheme == AuthSchemeBearer && strings.TrimSpace(s.AuthTokenEnv) == "" {
		return newFieldError(CodeInvalidSource, "auth_token_env")
	}
	if strings.TrimSpace(s.AuthTokenEnv) != "" && !validEnvName(s.AuthTokenEnv) {
		return newFieldError(CodeInvalidSource, "auth_token_env")
	}
	if _, err := ParseNamespaceID(s.Namespace); err != nil {
		return err
	}
	return nil
}
