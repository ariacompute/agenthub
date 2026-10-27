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
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"agenthub/internal/httputil"
)

// Kind enumerates the five interconnectable asset classes.
type Kind string

const (
	KindModel  Kind = "model"
	KindAgent  Kind = "agent"
	KindMCP    Kind = "mcp"
	KindSkill  Kind = "skill"
	KindCLI    Kind = "cli"
	KindCustom Kind = "custom"
)

// Kinds lists every supported kind in presentation order. Custom is last: it is
// the escape hatch for anything that is not one of the four protocol shaped
// kinds, mirroring the AWS CUSTOM record type.
var Kinds = []Kind{KindModel, KindAgent, KindMCP, KindSkill, KindCLI, KindCustom}

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
	// List returns entries matching the kind, namespace and status of the
	// filter, sorted by id. An empty field means no constraint; Query is
	// applied by the caller because it is a text match, not a column.
	List(ctx context.Context, f Filter) ([]*Entry, error)
	Delete(ctx context.Context, id string) error
	SetHealth(ctx context.Context, id string, h Health) error
	// Counts returns per-kind and per-health totals. Both maps are pre-filled
	// with every known kind / health so absent buckets report zero.
	Counts(ctx context.Context) (byKind map[string]int, byHealth map[string]int, err error)
	Size(ctx context.Context) (int, error)
}

// Entry is one registered resource.
//
// Every field added after the first release carries omitempty and has a
// backwards compatible default: an entry written by an older client still
// validates and still behaves the way it did.
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
	// Namespace is the catalogue the record belongs to. Empty means the
	// default namespace, which is where every pre-namespace record lives.
	Namespace string `json:"namespace,omitempty"`
	// Status is the curation state. Empty is read as approved so an upgrade
	// never hides resources that were registered before curation existed.
	Status Status `json:"status,omitempty"`
	// DisplayName is the optional human facing label AWS calls displayName.
	DisplayName string `json:"display_name,omitempty"`
	// SourceID is set when the record arrived from an upstream registry.
	SourceID string `json:"source_id,omitempty"`
	// KindConfig is the resource type specific descriptor: the model router,
	// the Agent Card, the MCP server definition, the skill manual or the CLI
	// command.
	KindConfig *KindConfig `json:"kind_config,omitempty"`
	// Persona is the optional human-facing presentation of the resource. It
	// carries nothing that changes behaviour, only how a console renders it.
	Persona   *Persona  `json:"persona,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Persona is the human-facing presentation of a resource (display label, tagline
// and avatar). It is descriptive only: nothing here changes routing or auth.
type Persona struct {
	DisplayName string `json:"displayName,omitempty"`
	Title       string `json:"title,omitempty"`
	Tagline     string `json:"tagline,omitempty"`
	AvatarURL   string `json:"avatarUrl,omitempty"`
}

// EffectiveNamespace returns the namespace of an entry, falling back to the
// default one.
func (e *Entry) EffectiveNamespace() string {
	if e == nil || strings.TrimSpace(e.Namespace) == "" {
		return DefaultNamespace
	}
	return strings.ToLower(strings.TrimSpace(e.Namespace))
}

// EffectiveStatus returns the curation state of an entry. An empty status is
// approved: records registered before curation existed must stay discoverable.
func (e *Entry) EffectiveStatus() Status {
	if e == nil || !e.Status.Valid() {
		return StatusApproved
	}
	return e.Status
}

// EffectiveDisplayName falls back to the name when no display label was given.
func (e *Entry) EffectiveDisplayName() string {
	if e == nil {
		return ""
	}
	if strings.TrimSpace(e.DisplayName) != "" {
		return strings.TrimSpace(e.DisplayName)
	}
	return e.Name
}

// normalizePersona trims fields, caps their length and drops an avatar URL that
// is not http(s). A malformed or dangerous URL is dropped rather than rejected so
// an older client can still save the rest of the record; an all-empty persona is
// reduced to nil so the column stays null and the wire shape stays compact.
func normalizePersona(p *Persona) *Persona {
	if p == nil {
		return nil
	}
	const maxLen = 256
	trim := func(s string) string {
		s = strings.TrimSpace(s)
		if len(s) > maxLen {
			s = s[:maxLen]
		}
		return s
	}
	p.DisplayName = trim(p.DisplayName)
	p.Title = trim(p.Title)
	p.Tagline = trim(p.Tagline)
	p.AvatarURL = strings.TrimSpace(p.AvatarURL)
	if p.AvatarURL != "" {
		if u, err := url.Parse(p.AvatarURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") {
			p.AvatarURL = ""
		}
	}
	if p.DisplayName == "" && p.Title == "" && p.Tagline == "" && p.AvatarURL == "" {
		return nil
	}
	return p
}

// Registry owns the resource catalogue.
//
// It is the in-memory implementation of the catalogue and, at the same time,
// the facade the rest of agenthub talks to: when a ResourceStore is attached
// every read and write is delegated to it so the catalogue survives a restart.
// The public surface is identical in both modes, which keeps the A2A contract,
// the control plane and the tests unchanged.
type Registry struct {
	mu         sync.RWMutex
	entries    map[string]*Entry
	namespaces map[string]*Namespace
	store      ResourceStore
	nsStore    NamespaceStore
	// modelProviderMode is the instance wide default for model entries that do
	// not name a provider mode themselves (AGENTHUB_MODEL_ROUTER_MODE). It is
	// set at wiring time and never mutated afterwards, so reads need no lock.
	modelProviderMode string
}

// New builds an empty, in-memory registry.
func New() *Registry {
	r := &Registry{entries: map[string]*Entry{}, namespaces: map[string]*Namespace{}}
	r.EnsureNamespace(DefaultNamespace)
	return r
}

// NewWithStore builds a registry backed by a ResourceStore.
func NewWithStore(store ResourceStore) *Registry {
	r := &Registry{entries: map[string]*Entry{}, namespaces: map[string]*Namespace{}, store: store}
	r.EnsureNamespace(DefaultNamespace)
	return r
}

// NewWithStores builds a registry backed by a ResourceStore and a
// NamespaceStore. Both are optional: a nil store keeps the in-memory behaviour
// so the whole platform still runs without a database.
func NewWithStores(store ResourceStore, ns NamespaceStore) *Registry {
	r := &Registry{
		entries:    map[string]*Entry{},
		namespaces: map[string]*Namespace{},
		store:      store,
		nsStore:    ns,
	}
	r.EnsureNamespace(DefaultNamespace)
	return r
}

// WithNamespaceStore attaches the namespace persistence port to an existing
// registry. It exists for wiring order: the namespace store is often known
// later than the resource store.
func (r *Registry) WithNamespaceStore(ns NamespaceStore) *Registry {
	r.mu.Lock()
	r.nsStore = ns
	r.mu.Unlock()
	return r
}

// WithModelRouterMode attaches the instance wide provider mode model entries
// inherit when they do not name one (AGENTHUB_MODEL_ROUTER_MODE). Same shape as
// WithNamespaceStore: wiring order decides when the value is known.
func (r *Registry) WithModelRouterMode(mode string) *Registry {
	r.mu.Lock()
	r.modelProviderMode = mode
	r.mu.Unlock()
	return r
}

// effectiveModelProviderMode is the value Upsert validates against. An unset or
// unknown mode degrades to DefaultModelProviderMode, so a misconfigured
// environment can never turn every model entry into a gateway call.
func (r *Registry) effectiveModelProviderMode() string {
	if r == nil {
		return DefaultModelProviderMode
	}
	if mode := strings.ToLower(strings.TrimSpace(r.modelProviderMode)); knownProviderMode(mode) {
		return mode
	}
	return DefaultModelProviderMode
}

// Upsert validates and stores an entry, generating an id when absent.
//
// Validation is generic (kind, health, transport, name) plus per-kind (the
// resource type specific descriptor). A record also has to respect the curation
// state machine: saving a record may not silently walk it from rejected back to
// approved, that is a curator's decision expressed through the approval
// endpoint.
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
	if err := ValidateKindWithMode(in, r.effectiveModelProviderMode()); err != nil {
		return nil, err
	}
	ns, err := ParseNamespaceID(in.Namespace)
	if err != nil {
		return nil, err
	}
	e := *in
	e.Kind = kind
	e.Name = strings.TrimSpace(e.Name)
	e.Namespace = ns
	e.Persona = normalizePersona(e.Persona)
	if e.Health == "" {
		e.Health = HealthUnknown
	}
	e.Status, err = r.resolveStatus(in.ID, in.Status, ns)
	if err != nil {
		return nil, err
	}
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now().UTC()
	}
	e.UpdatedAt = time.Now().UTC()

	if r.store != nil {
		if e.ID == "" {
			e.ID = r.freshID(string(e.Kind))
		}
		if err := r.checkUnique(context.Background(), &e); err != nil {
			return nil, err
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
	if conflict := r.conflictLocked(&e); conflict != nil {
		return nil, &httputil.Error{Code: CodeDuplicate}
	}
	r.entries[e.ID] = &e
	stored := e
	return &stored, nil
}

// resolveStatus decides the curation state of a save.
//
// An unspecified status means "whatever this namespace does by default", which
// is what every write from an older client looks like. An explicit change has to
// be a legal edge of the state machine; a save that repeats the current state is
// always allowed.
func (r *Registry) resolveStatus(id string, want Status, namespace string) (Status, error) {
	if !want.Valid() {
		if strings.TrimSpace(string(want)) == "" {
			return r.DefaultStatus(namespace), nil
		}
		return "", &httputil.Error{Code: CodeInvalidStatusTransition}
	}
	current := r.currentStatus(id, namespace)
	if current == "" || current == want {
		return want, nil
	}
	if !CanTransition(current, want) {
		return "", &httputil.Error{Code: CodeInvalidStatusTransition}
	}
	return want, nil
}

// currentStatus reads the stored status of an entry, or "" when it is new.
func (r *Registry) currentStatus(id, namespace string) Status {
	if strings.TrimSpace(id) == "" {
		return ""
	}
	if r.store != nil {
		e, err := r.store.Get(context.Background(), id)
		if err != nil || e == nil {
			return ""
		}
		return e.EffectiveStatus()
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.entries[id]
	if !ok || e == nil {
		return ""
	}
	return e.EffectiveStatus()
}

// checkUnique enforces the (namespace, name, version) dedup key in store mode.
// The database carries the unique index as the real guarantee; this is the
// friendly error in front of it so the caller gets resource_already_exists
// rather than a driver message.
func (r *Registry) checkUnique(ctx context.Context, e *Entry) error {
	all, err := r.store.List(ctx, Filter{})
	if err != nil {
		return storeErr("unique_check", err)
	}
	for _, other := range all {
		if other == nil || other.ID == e.ID {
			continue
		}
		if sameKey(other, e) {
			return &httputil.Error{Code: CodeDuplicate}
		}
	}
	return nil
}

// conflictLocked is the in-memory twin of checkUnique.
func (r *Registry) conflictLocked(e *Entry) *Entry {
	for _, other := range r.entries {
		if other == nil || other.ID == e.ID {
			continue
		}
		if sameKey(other, e) {
			return other
		}
	}
	return nil
}

// sameKey reports whether two entries collide on the (namespace, name, version)
// dedup key. Version is compared verbatim, so "1.0" and "1.0.0" are different
// records and the same name may be reused across versions — exactly the AWS
// rule that name plus recordVersion is the dedup key.
func sameKey(a, b *Entry) bool {
	return a.EffectiveNamespace() == b.EffectiveNamespace() &&
		strings.EqualFold(strings.TrimSpace(a.Name), strings.TrimSpace(b.Name)) &&
		strings.TrimSpace(a.Version) == strings.TrimSpace(b.Version)
}

// FindByKey resolves the (namespace, name, version) dedup key to an entry.
// Synchronisation uses it to refresh the record an upstream already contributed
// instead of colliding with it.
func (r *Registry) FindByKey(namespace, name, version string) (*Entry, bool) {
	key := &Entry{Namespace: namespace, Name: name, Version: version}
	if r.store != nil {
		all, err := r.store.List(context.Background(), Filter{})
		if err != nil {
			slog.Error("registry: find by key", "error", err)
			return nil, false
		}
		for _, e := range all {
			if e != nil && sameKey(e, key) {
				return e, true
			}
		}
		return nil, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, e := range r.entries {
		if e != nil && sameKey(e, key) {
			cp := *e
			return &cp, true
		}
	}
	return nil, false
}

// SetStatus applies one curated transition and is the only way a record moves
// between curation states through the control plane.
func (r *Registry) SetStatus(id string, to Status) (*Entry, error) {
	if !to.Valid() {
		return nil, &httputil.Error{Code: CodeInvalidStatusTransition}
	}
	if r.store != nil {
		e, err := r.store.Get(context.Background(), id)
		if err != nil {
			return nil, storeErr("get", err)
		}
		next, err := Transition(e.EffectiveStatus(), to)
		if err != nil {
			return nil, err
		}
		e.Status = next
		e.UpdatedAt = time.Now().UTC()
		stored, err := r.store.Upsert(context.Background(), e)
		if err != nil {
			return nil, storeErr("set_status", err)
		}
		return stored, nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.entries[id]
	if !ok {
		return nil, &httputil.Error{Code: CodeNotFound}
	}
	next, err := Transition(e.EffectiveStatus(), to)
	if err != nil {
		return nil, err
	}
	e.Status = next
	e.UpdatedAt = time.Now().UTC()
	cp := *e
	return &cp, nil
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

// Filter narrows a listing. An empty field means "no constraint", except Status
// where empty means every state — the control plane needs to see drafts and
// pending records to curate them, while discovery APIs apply Discoverable on
// top.
type Filter struct {
	Kind      string
	Query     string
	Namespace string
	Status    string
}

// List returns entries filtered by kind ("" means all) and free text query,
// sorted by id so results are stable across calls.
func (r *Registry) List(kind string, query string) []*Entry {
	return r.ListFiltered(Filter{Kind: kind, Query: query})
}

// ListFiltered returns entries matching every non-empty field of f.
func (r *Registry) ListFiltered(f Filter) []*Entry {
	var filter Kind
	if strings.TrimSpace(f.Kind) != "" {
		parsed, err := ParseKind(f.Kind)
		if err != nil {
			return nil
		}
		filter = parsed
	}
	var status Status
	if strings.TrimSpace(f.Status) != "" {
		parsed, err := ParseStatus(f.Status)
		if err != nil {
			return nil
		}
		status = parsed
	}
	ns := strings.ToLower(strings.TrimSpace(f.Namespace))
	q := strings.ToLower(strings.TrimSpace(f.Query))

	var out []*Entry
	if r.store != nil {
		got, err := r.store.List(context.Background(), f)
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
	if filter == "" && q == "" && ns == "" && status == "" {
		return out
	}
	filtered := make([]*Entry, 0, len(out))
	for _, e := range out {
		if filter != "" && e.Kind != filter {
			continue
		}
		if ns != "" && e.EffectiveNamespace() != ns {
			continue
		}
		if status != "" && e.EffectiveStatus() != status {
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
