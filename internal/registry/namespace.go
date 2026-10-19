package registry

import (
	"context"
	"regexp"
	"sort"
	"strings"
	"time"

	"agenthub/internal/httputil"
)

// DefaultNamespace is the namespace every pre-existing entry belongs to.
//
// agenthub grew namespaces after the catalogue existed, so a record without one
// is read as belonging here rather than as invalid: an upgrade must never make
// resources disappear.
const DefaultNamespace = "default"

// CodeInvalidNamespace is returned for a namespace id that is not a slug.
const CodeInvalidNamespace = httputil.Code("invalid_namespace")

// namespacePattern is deliberately strict: a namespace id ends up in URLs and
// in a unique index, so it stays a lowercase slug.
var namespacePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// Namespace is one catalogue. AWS lets an organisation split its registry by
// resource type, stage of development or team; agenthub does the same with a
// lightweight namespace that carries its own approval policy.
type Namespace struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// AutoApprove decides the state a newly registered record starts in:
	// approved when the namespace is permissive, draft when a curator has to
	// look at it first.
	AutoApprove bool      `json:"auto_approve"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// NamespaceStore persists namespaces. It is the port the registry delegates to;
// the in-memory map is the default implementation and the PostgreSQL one lives
// in internal/store.
type NamespaceStore interface {
	UpsertNamespace(ctx context.Context, n *Namespace) (*Namespace, error)
	GetNamespace(ctx context.Context, id string) (*Namespace, error)
	ListNamespaces(ctx context.Context) ([]*Namespace, error)
	DeleteNamespace(ctx context.Context, id string) error
}

// ParseNamespaceID normalises and validates a namespace id.
func ParseNamespaceID(s string) (string, error) {
	v := strings.ToLower(strings.TrimSpace(s))
	if v == "" {
		return DefaultNamespace, nil
	}
	if !namespacePattern.MatchString(v) {
		return "", &httputil.Error{Code: CodeInvalidNamespace}
	}
	return v, nil
}

// UpsertNamespace creates or updates a namespace. The default namespace always
// exists: it is what every pre-namespace record belongs to.
func (r *Registry) UpsertNamespace(in *Namespace) (*Namespace, error) {
	if in == nil {
		return nil, &httputil.Error{Code: CodeInvalidNamespace}
	}
	id, err := ParseNamespaceID(in.ID)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.Name) == "" {
		in.Name = id
	}
	n := *in
	n.ID = id
	n.Name = strings.TrimSpace(n.Name)
	if n.CreatedAt.IsZero() {
		n.CreatedAt = time.Now().UTC()
	}
	n.UpdatedAt = time.Now().UTC()

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.namespaces == nil {
		r.namespaces = map[string]*Namespace{}
	}
	if prev, ok := r.namespaces[id]; ok && !prev.CreatedAt.IsZero() {
		n.CreatedAt = prev.CreatedAt
	}
	if r.nsStore != nil {
		stored, err := r.nsStore.UpsertNamespace(context.Background(), &n)
		if err != nil {
			return nil, storeErr("upsert_namespace", err)
		}
		r.namespaces[id] = stored
		cp := *stored
		return &cp, nil
	}
	r.namespaces[id] = &n
	cp := n
	return &cp, nil
}

// GetNamespace returns one namespace by id.
func (r *Registry) GetNamespace(id string) (*Namespace, bool) {
	id, err := ParseNamespaceID(id)
	if err != nil {
		return nil, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	n, ok := r.namespaces[id]
	if !ok {
		return nil, false
	}
	cp := *n
	return &cp, true
}

// ListNamespaces returns every namespace sorted by id, with the default one
// guaranteed to be present.
func (r *Registry) ListNamespaces() []*Namespace {
	r.mu.RLock()
	out := make([]*Namespace, 0, len(r.namespaces))
	for _, n := range r.namespaces {
		cp := *n
		out = append(out, &cp)
	}
	hasDefault := false
	for _, n := range out {
		if n.ID == DefaultNamespace {
			hasDefault = true
			break
		}
	}
	r.mu.RUnlock()
	if !hasDefault {
		n := defaultNamespace()
		out = append(out, &n)
	}
	sortNamespaces(out)
	return out
}

// DeleteNamespace removes a namespace. The default namespace is the fallback
// for every record without one, so it can never be deleted.
func (r *Registry) DeleteNamespace(id string) error {
	id, err := ParseNamespaceID(id)
	if err != nil {
		return err
	}
	if id == DefaultNamespace {
		return &httputil.Error{Code: CodeInvalidNamespace}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.namespaces[id]; !ok {
		return &httputil.Error{Code: CodeNotFound}
	}
	if r.nsStore != nil {
		if err := r.nsStore.DeleteNamespace(context.Background(), id); err != nil {
			return storeErr("delete_namespace", err)
		}
	}
	delete(r.namespaces, id)
	return nil
}

// AutoApprove reports whether a namespace publishes records without review.
// An unknown namespace defaults to permissive so a record registered into a
// namespace nobody created yet still behaves the way it did before namespaces
// existed.
func (r *Registry) AutoApprove(namespace string) bool {
	id, err := ParseNamespaceID(namespace)
	if err != nil {
		return true
	}
	r.mu.RLock()
	n, ok := r.namespaces[id]
	r.mu.RUnlock()
	if !ok {
		return true
	}
	return n.AutoApprove
}

// DefaultStatus is the state a freshly registered record starts in.
func (r *Registry) DefaultStatus(namespace string) Status {
	if r.AutoApprove(namespace) {
		return StatusApproved
	}
	return StatusDraft
}

// EnsureNamespace seeds the default namespace when it is missing. It is
// idempotent and safe to call on every boot.
func (r *Registry) EnsureNamespace(id string) {
	id, err := ParseNamespaceID(id)
	if err != nil {
		return
	}
	r.mu.RLock()
	_, exists := r.namespaces[id]
	r.mu.RUnlock()
	if exists {
		return
	}
	n := defaultNamespace()
	n.ID = id
	n.Name = id
	if id == DefaultNamespace {
		n.Name = "Default"
	}
	_, _ = r.UpsertNamespace(&n)
}

// LoadNamespaces primes the in-memory view from the store so AutoApprove and
// DefaultStatus answer without a query per registration.
func (r *Registry) LoadNamespaces(ctx context.Context) error {
	if r.nsStore == nil {
		r.EnsureNamespace(DefaultNamespace)
		return nil
	}
	got, err := r.nsStore.ListNamespaces(ctx)
	if err != nil {
		return storeErr("list_namespaces", err)
	}
	r.mu.Lock()
	if r.namespaces == nil {
		r.namespaces = map[string]*Namespace{}
	}
	for _, n := range got {
		cp := *n
		r.namespaces[cp.ID] = &cp
	}
	r.mu.Unlock()
	r.EnsureNamespace(DefaultNamespace)
	return nil
}

func defaultNamespace() Namespace {
	now := time.Now().UTC()
	return Namespace{
		ID:          DefaultNamespace,
		Name:        "Default",
		Description: "The namespace every record without an explicit one belongs to.",
		AutoApprove: true,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
}

func sortNamespaces(ns []*Namespace) {
	sort.Slice(ns, func(i, j int) bool { return ns[i].ID < ns[j].ID })
}
