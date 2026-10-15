// Package registry is the catalogue of everything agenthub can interconnect:
// model, agent, mcp, skill and cli resources.
//
// The entry model follows the MCP Registry convention (name / version /
// repository / tags / transport / auth schemes / env) so a resource can be
// discovered and described uniformly, while transport stays a descriptive
// field: agenthub is transport agnostic and never branches on it.
package registry

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"agenthub/internal/httputil"
)

// Kind enumerates the five interconnectable asset classes.
type Kind string

const (
	KindModel Kind = "model"
	KindAgent Kind = "agent"
	KindMCP   Kind = "mcp"
	KindSkill Kind = "skill"
	KindCLI   Kind = "cli"
)

// Kinds lists every supported kind in presentation order.
var Kinds = []Kind{KindModel, KindAgent, KindMCP, KindSkill, KindCLI}

// ParseKind normalises a kind string.
func ParseKind(s string) (Kind, error) {
	k := Kind(strings.ToLower(strings.TrimSpace(s)))
	for _, c := range Kinds {
		if c == k {
			return k, nil
		}
	}
	return "", &httputil.Error{Code: CodeInvalidKind}
}

// Valid reports whether the kind is one of the five supported classes.
func (k Kind) Valid() bool {
	for _, c := range Kinds {
		if c == k {
			return true
		}
	}
	return false
}

// Health is the observed reachability of a resource.
type Health string

const (
	HealthHealthy   Health = "healthy"
	HealthDegraded  Health = "degraded"
	HealthUnhealthy Health = "unhealthy"
	HealthUnknown   Health = "unknown"
)

// Healths lists every health value.
var Healths = []Health{HealthHealthy, HealthDegraded, HealthUnhealthy, HealthUnknown}

// ParseHealth normalises a health string.
func ParseHealth(s string) (Health, error) {
	h := Health(strings.ToLower(strings.TrimSpace(s)))
	for _, c := range Healths {
		if c == h {
			return h, nil
		}
	}
	return "", &httputil.Error{Code: CodeInvalidHealth}
}

// Transport records how a resource is reached. agenthub stores it for
// discovery only: MCP is transport agnostic and the adapters do not branch on it.
type Transport string

const (
	TransportStdio          Transport = "stdio"
	TransportStreamableHTTP Transport = "streamable-http"
	TransportSSELegacy      Transport = "sse-legacy"
)

// Transports lists every transport value.
var Transports = []Transport{TransportStdio, TransportStreamableHTTP, TransportSSELegacy}

// ParseTransport normalises a transport string; the empty string means "unset".
func ParseTransport(s string) (Transport, error) {
	t := Transport(strings.ToLower(strings.TrimSpace(s)))
	if t == "" {
		return "", nil
	}
	for _, c := range Transports {
		if c == t {
			return c, nil
		}
	}
	return "", &httputil.Error{Code: CodeInvalidTransport}
}

// Machine readable error codes produced by this package.
const (
	CodeInvalidKind      = httputil.Code("invalid_kind")
	CodeInvalidHealth    = httputil.Code("invalid_health")
	CodeInvalidTransport = httputil.Code("invalid_transport")
	CodeInvalidEntry     = httputil.Code("invalid_entry")
	CodeNotFound         = httputil.Code("resource_not_found")
	CodeDuplicate        = httputil.Code("resource_already_exists")
)

// Entry is one registered resource.
type Entry struct {
	ID           string            `json:"id"`
	Kind         Kind              `json:"kind"`
	Name         string            `json:"name"`
	Description  string            `json:"description,omitempty"`
	Endpoint     string            `json:"endpoint,omitempty"`
	Version      string            `json:"version,omitempty"`
	Repository   string            `json:"repository,omitempty"`
	Tags         []string          `json:"tags,omitempty"`
	Capabilities []string          `json:"capabilities,omitempty"`
	Transport    Transport         `json:"transport,omitempty"`
	AuthSchemes  []string          `json:"auth_schemes,omitempty"`
	Env          map[string]string `json:"env,omitempty"`
	Health       Health            `json:"health"`
	Metadata     map[string]string `json:"metadata,omitempty"`
	CreatedAt    time.Time         `json:"createdAt"`
	UpdatedAt    time.Time         `json:"updatedAt"`
}

// Registry owns the resource catalogue.
type Registry struct {
	mu      sync.RWMutex
	entries map[string]*Entry
}

// New builds an empty registry.
func New() *Registry {
	return &Registry{entries: map[string]*Entry{}}
}

// Upsert validates and stores an entry, generating an id when absent.
func (r *Registry) Upsert(in *Entry) (*Entry, error) {
	if in == nil {
		return nil, &httputil.Error{Code: CodeInvalidEntry}
	}
	kind, err := ParseKind(string(in.Kind))
	if err != nil {
		return nil, err
	}
	if _, err := ParseHealth(string(in.Health)); err != nil && in.Health != "" {
		return nil, err
	}
	if _, err := ParseTransport(string(in.Transport)); err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.Name) == "" {
		return nil, &httputil.Error{Code: CodeInvalidEntry}
	}
	e := *in
	e.Kind = kind
	e.Name = strings.TrimSpace(e.Name)
	if e.Health == "" {
		e.Health = HealthUnknown
	}
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now().UTC()
	}
	e.UpdatedAt = time.Now().UTC()

	r.mu.Lock()
	defer r.mu.Unlock()
	if e.ID == "" {
		e.ID = NewID(string(e.Kind))
		for {
			if _, exists := r.entries[e.ID]; !exists {
				break
			}
			e.ID = NewID(string(e.Kind))
		}
	}
	r.entries[e.ID] = &e
	stored := e
	return &stored, nil
}

// Register creates a new resource; reusing an existing id is rejected.
func (r *Registry) Register(in *Entry) (*Entry, error) {
	if in != nil && in.ID != "" {
		r.mu.RLock()
		_, exists := r.entries[in.ID]
		r.mu.RUnlock()
		if exists {
			return nil, &httputil.Error{Code: CodeDuplicate}
		}
	}
	return r.Upsert(in)
}

// Get returns one entry by id.
func (r *Registry) Get(id string) (*Entry, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.entries[id]
	if !ok {
		return nil, false
	}
	cp := *e
	return &cp, true
}

// Delete removes an entry.
func (r *Registry) Delete(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.entries[id]; !ok {
		return &httputil.Error{Code: CodeNotFound}
	}
	delete(r.entries, id)
	return nil
}

// List returns entries filtered by kind ("" means all) and free text query,
// sorted by id so results are stable across calls.
func (r *Registry) List(kind string, query string) []*Entry {
	var filter Kind
	if strings.TrimSpace(kind) != "" {
		parsed, err := ParseKind(kind)
		if err != nil {
			return nil
		}
		filter = parsed
	}
	q := strings.ToLower(strings.TrimSpace(query))
	r.mu.RLock()
	out := make([]*Entry, 0, len(r.entries))
	for _, e := range r.entries {
		cp := *e
		out = append(out, &cp)
	}
	r.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	if filter == "" && q == "" {
		return out
	}
	filtered := make([]*Entry, 0, len(out))
	for _, e := range out {
		if filter != "" && e.Kind != filter {
			continue
		}
		if q != "" && !matches(e, q) {
			continue
		}
		filtered = append(filtered, e)
	}
	return filtered
}

// SetHealth updates the observed health of a resource.
func (r *Registry) SetHealth(id string, h Health) error {
	if _, err := ParseHealth(string(h)); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.entries[id]
	if !ok {
		return &httputil.Error{Code: CodeNotFound}
	}
	e.Health = h
	e.UpdatedAt = time.Now().UTC()
	return nil
}

// CountsByKind returns the number of registered resources per kind.
func (r *Registry) CountsByKind() map[string]int {
	out := map[string]int{}
	for _, k := range Kinds {
		out[string(k)] = 0
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, e := range r.entries {
		out[string(e.Kind)]++
	}
	return out
}

// HealthCounts returns the number of resources per health value.
func (r *Registry) HealthCounts() map[string]int {
	out := map[string]int{}
	for _, h := range Healths {
		out[string(h)] = 0
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, e := range r.entries {
		out[string(e.Health)]++
	}
	return out
}

// Size returns the number of registered resources.
func (r *Registry) Size() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.entries)
}

// Snapshot returns a copy of every entry, sorted by id.
func (r *Registry) Snapshot() []*Entry {
	return r.List("", "")
}

// ReplaceAll reloads the catalogue from a snapshot.
func (r *Registry) ReplaceAll(entries []*Entry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries = map[string]*Entry{}
	for _, e := range entries {
		if e == nil {
			continue
		}
		cp := *e
		r.entries[cp.ID] = &cp
	}
}

func matches(e *Entry, q string) bool {
	if strings.Contains(strings.ToLower(e.ID), q) ||
		strings.Contains(strings.ToLower(e.Name), q) ||
		strings.Contains(strings.ToLower(e.Description), q) ||
		strings.Contains(strings.ToLower(e.Endpoint), q) {
		return true
	}
	for _, t := range e.Tags {
		if strings.Contains(strings.ToLower(t), q) {
			return true
		}
	}
	return false
}

// NewID builds a short random identifier with a readable prefix.
func NewID(prefix string) string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand never fails on supported platforms; fall back to a
		// counter-free deterministic suffix rather than panicking.
		return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
	}
	if prefix == "" {
		return hex.EncodeToString(buf)
	}
	return prefix + "-" + hex.EncodeToString(buf)
}

// ErrNotFound is kept for callers that prefer sentinel comparison.
var ErrNotFound = errors.New(string(CodeNotFound))
