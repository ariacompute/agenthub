// Package registry is the catalogue of everything agenthub can interconnect:
// model, agent, mcp, skill and cli resources.
//
// The entry model follows the MCP Registry convention (name / version /
// repository / tags / transport / auth schemes / env) so a resource can be
// discovered and described uniformly, while transport stays a descriptive
// field: agenthub is transport agnostic and never branches on it.
package registry

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
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

// ResourceStore persists the catalogue. It is the port the registry delegates
// to: the default implementation is the in-memory map of Registry itself, the
// PostgreSQL one lives in internal/store.
//
// Implementations return ErrNotFound for an unknown id and ErrDuplicate when an
// insert collides with an existing id. Every other failure is reported as-is so
// the caller can map it onto internal_error.
type ResourceStore interface {
	// Upsert validates nothing: the registry has already normalised the entry.
	// It stores it and returns the stored copy.
	Upsert(ctx context.Context, e *Entry) (*Entry, error)
	Get(ctx context.Context, id string) (*Entry, error)
	// List returns entries of one kind, sorted by id. An empty kind means all.
	List(ctx context.Context, kind string) ([]*Entry, error)
	Delete(ctx context.Context, id string) error
	SetHealth(ctx context.Context, id string, h Health) error
	// Counts returns per-kind and per-health totals. Both maps are pre-filled
	// with every known kind / health so absent buckets report zero.
	Counts(ctx context.Context) (byKind map[string]int, byHealth map[string]int, err error)
	Size(ctx context.Context) (int, error)
}

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
//
// It is the in-memory implementation of the catalogue and, at the same time,
// the facade the rest of agenthub talks to: when a ResourceStore is attached
// every read and write is delegated to it so the catalogue survives a restart.
// The public surface is identical in both modes, which keeps the A2A contract,
// the control plane and the tests unchanged.
type Registry struct {
	mu      sync.RWMutex
	entries map[string]*Entry
	store   ResourceStore
}

// New builds an empty, in-memory registry.
func New() *Registry {
	return &Registry{entries: map[string]*Entry{}}
}

// NewWithStore builds a registry backed by a ResourceStore.
func NewWithStore(store ResourceStore) *Registry {
	return &Registry{entries: map[string]*Entry{}, store: store}
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

	if r.store != nil {
		if e.ID == "" {
			e.ID = r.freshID(string(e.Kind))
		}
		stored, err := r.store.Upsert(context.Background(), &e)
		if err != nil {
			return nil, storeErr("upsert", err)
		}
		return stored, nil
	}

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

// freshID generates an id the store does not know yet.
func (r *Registry) freshID(kind string) string {
	for i := 0; i < 8; i++ {
		id := NewID(kind)
		if _, err := r.store.Get(context.Background(), id); err != nil {
			return id
		}
	}
	return NewID(kind)
}

// Register creates a new resource; reusing an existing id is rejected.
func (r *Registry) Register(in *Entry) (*Entry, error) {
	if in != nil && in.ID != "" {
		if r.store != nil {
			if _, err := r.store.Get(context.Background(), in.ID); err == nil {
				return nil, &httputil.Error{Code: CodeDuplicate}
			} else if !errors.Is(err, ErrNotFound) {
				return nil, storeErr("register", err)
			}
		} else {
			r.mu.RLock()
			_, exists := r.entries[in.ID]
			r.mu.RUnlock()
			if exists {
				return nil, &httputil.Error{Code: CodeDuplicate}
			}
		}
	}
	return r.Upsert(in)
}

// Get returns one entry by id.
func (r *Registry) Get(id string) (*Entry, bool) {
	if r.store != nil {
		e, err := r.store.Get(context.Background(), id)
		if err != nil {
			if !errors.Is(err, ErrNotFound) {
				slog.Error("registry: load entry", "id", id, "error", err)
			}
			return nil, false
		}
		return e, true
	}
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
	if r.store != nil {
		return storeErr("delete", r.store.Delete(context.Background(), id))
	}
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

	var out []*Entry
	if r.store != nil {
		got, err := r.store.List(context.Background(), string(filter))
		if err != nil {
			slog.Error("registry: list entries", "kind", filter, "error", err)
			return nil
		}
		out = got
	} else {
		r.mu.RLock()
		out = make([]*Entry, 0, len(r.entries))
		for _, e := range r.entries {
			cp := *e
			out = append(out, &cp)
		}
		r.mu.RUnlock()
	}
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
	if r.store != nil {
		return storeErr("set_health", r.store.SetHealth(context.Background(), id, h))
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
	if r.store != nil {
		byKind, _, err := r.store.Counts(context.Background())
		if err != nil {
			slog.Error("registry: count resources", "error", err)
			return zeroCounts(Kinds)
		}
		return byKind
	}
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
	if r.store != nil {
		_, byHealth, err := r.store.Counts(context.Background())
		if err != nil {
			slog.Error("registry: count health", "error", err)
			return zeroCounts(Healths)
		}
		return byHealth
	}
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
	if r.store != nil {
		n, err := r.store.Size(context.Background())
		if err != nil {
			slog.Error("registry: size", "error", err)
			return 0
		}
		return n
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.entries)
}

// Snapshot returns a copy of every entry, sorted by id.
func (r *Registry) Snapshot() []*Entry {
	return r.List("", "")
}

// storeErr normalises a persistence failure: the driver text never reaches the
// caller, only a machine readable code.
func storeErr(op string, err error) error {
	if err == nil {
		return nil
	}
	var he *httputil.Error
	if errors.As(err, &he) {
		return err
	}
	if errors.Is(err, ErrNotFound) {
		return &httputil.Error{Code: CodeNotFound}
	}
	if errors.Is(err, ErrDuplicate) {
		return &httputil.Error{Code: CodeDuplicate}
	}
	slog.Error("registry: persistence failure", "op", op, "error", err)
	return &httputil.Error{Code: httputil.CodeInternal}
}

func zeroCounts[T ~string](keys []T) map[string]int {
	out := make(map[string]int, len(keys))
	for _, k := range keys {
		out[string(k)] = 0
	}
	return out
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

// Sentinel errors a ResourceStore implementation returns.
var (
	// ErrNotFound is kept for callers that prefer sentinel comparison; it is
	// also the error a ResourceStore must return for an unknown id.
	ErrNotFound = errors.New(string(CodeNotFound))
	// ErrDuplicate is returned by a ResourceStore when an insert collides with
	// an existing id.
	ErrDuplicate = errors.New(string(CodeDuplicate))
)
