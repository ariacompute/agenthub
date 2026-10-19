package registry

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"testing"

	"agenthub/internal/httputil"
)

// The registry has two behaviours — in memory and delegated to a store — and
// the whole point of the ports is that they must agree. These tests drive the
// store side through fakes, so the rules are pinned without a database: the
// dedup key, the curation state machine, the id generation and the way a
// persistence failure is normalised.

// ---------------------------------------------------------------------------
// fakes
// ---------------------------------------------------------------------------

// memResourceStore is a ResourceStore that behaves like the GORM one: unknown
// ids are ErrNotFound, and a failure can be injected per operation.
type memResourceStore struct {
	mu     sync.Mutex
	byID   map[string]*Entry
	failOn map[string]error
	counts map[string]int
}

func newMemResourceStore() *memResourceStore {
	return &memResourceStore{byID: map[string]*Entry{}, failOn: map[string]error{}}
}

func (s *memResourceStore) fail(op string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failOn == nil {
		s.failOn = map[string]error{}
	}
	s.failOn[op] = err
}

func (s *memResourceStore) err(op string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.failOn[op]
}

func (s *memResourceStore) Upsert(_ context.Context, e *Entry) (*Entry, error) {
	if err := s.err("upsert"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *e
	s.byID[e.ID] = &cp
	out := cp
	return &out, nil
}

func (s *memResourceStore) Get(_ context.Context, id string) (*Entry, error) {
	if err := s.err("get"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.byID[id]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *e
	return &cp, nil
}

func (s *memResourceStore) List(_ context.Context, f Filter) ([]*Entry, error) {
	if err := s.err("list"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Entry, 0, len(s.byID))
	for _, e := range s.byID {
		cp := *e
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (s *memResourceStore) Delete(_ context.Context, id string) error {
	if err := s.err("delete"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.byID[id]; !ok {
		return ErrNotFound
	}
	delete(s.byID, id)
	return nil
}

func (s *memResourceStore) SetHealth(_ context.Context, id string, h Health) error {
	if err := s.err("set_health"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.byID[id]
	if !ok {
		return ErrNotFound
	}
	e.Health = h
	return nil
}

func (s *memResourceStore) Counts(_ context.Context) (map[string]int, map[string]int, error) {
	if err := s.err("counts"); err != nil {
		return nil, nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	byKind, byHealth := zeroCounts(Kinds), zeroCounts(Healths)
	for _, e := range s.byID {
		byKind[string(e.Kind)]++
		byHealth[string(e.Health)]++
	}
	return byKind, byHealth, nil
}

func (s *memResourceStore) Size(_ context.Context) (int, error) {
	if err := s.err("size"); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.byID), nil
}

type memNamespaceStore struct {
	mu   sync.Mutex
	byID map[string]*Namespace
	fail bool
}

func newMemNamespaceStore() *memNamespaceStore {
	return &memNamespaceStore{byID: map[string]*Namespace{}}
}

func (s *memNamespaceStore) UpsertNamespace(_ context.Context, n *Namespace) (*Namespace, error) {
	if s.fail {
		return nil, errors.New("boom")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *n
	s.byID[n.ID] = &cp
	out := cp
	return &out, nil
}

func (s *memNamespaceStore) GetNamespace(_ context.Context, id string) (*Namespace, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, ok := s.byID[id]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *n
	return &cp, nil
}

func (s *memNamespaceStore) ListNamespaces(_ context.Context) ([]*Namespace, error) {
	if s.fail {
		return nil, errors.New("boom")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Namespace, 0, len(s.byID))
	for _, n := range s.byID {
		cp := *n
		out = append(out, &cp)
	}
	return out, nil
}

func (s *memNamespaceStore) DeleteNamespace(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.byID[id]; !ok {
		return ErrNotFound
	}
	delete(s.byID, id)
	return nil
}

type memSourceStore struct {
	mu       sync.Mutex
	byID     map[string]*RegistrySource
	lastSync map[string]SyncResult
	fail     bool
}

func newMemSourceStore() *memSourceStore {
	return &memSourceStore{byID: map[string]*RegistrySource{}, lastSync: map[string]SyncResult{}}
}

func (s *memSourceStore) UpsertSource(_ context.Context, src *RegistrySource) (*RegistrySource, error) {
	if s.fail {
		return nil, errors.New("boom")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *src
	s.byID[src.ID] = &cp
	out := cp
	return &out, nil
}

func (s *memSourceStore) GetSource(_ context.Context, id string) (*RegistrySource, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	src, ok := s.byID[id]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *src
	return &cp, nil
}

func (s *memSourceStore) ListSources(_ context.Context, _ string) ([]*RegistrySource, error) {
	if s.fail {
		return nil, errors.New("boom")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*RegistrySource, 0, len(s.byID))
	for _, src := range s.byID {
		cp := *src
		out = append(out, &cp)
	}
	return out, nil
}

func (s *memSourceStore) DeleteSource(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.byID[id]; !ok {
		return ErrNotFound
	}
	delete(s.byID, id)
	return nil
}

func (s *memSourceStore) MarkSync(_ context.Context, id string, r SyncResult) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastSync[id] = r
	src, ok := s.byID[id]
	if ok {
		src.LastSyncStatus = r.Status
		src.LastSyncCount = r.Imported + r.Updated
		src.LastSyncError = r.Error
		at := r.At
		src.LastSyncAt = &at
	}
	return nil
}

// ---------------------------------------------------------------------------
// resources
// ---------------------------------------------------------------------------

// A store backed registry must apply exactly the rules the in-memory one does.
func TestStoreModeKeepsTheDedupKeyAndTheStateMachine(t *testing.T) {
	store := newMemResourceStore()
	reg := NewWithStore(store)

	first, err := reg.Upsert(&Entry{Kind: KindModel, Name: "router", Version: "1.0.0", Namespace: "team-a"})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if first.ID == "" {
		t.Fatal("store mode must generate an id")
	}
	// The same name and version in the same namespace is the same record.
	if _, err := reg.Upsert(&Entry{Kind: KindModel, Name: "router", Version: "1.0.0", Namespace: "team-a"}); code(t, err) != CodeDuplicate {
		t.Fatalf("duplicate = %v, want resource_already_exists", err)
	}
	// A new version, or another namespace, is a different record.
	if _, err := reg.Upsert(&Entry{Kind: KindModel, Name: "router", Version: "1.1.0", Namespace: "team-a"}); err != nil {
		t.Fatalf("new version: %v", err)
	}
	if _, err := reg.Upsert(&Entry{Kind: KindModel, Name: "router", Version: "1.0.0", Namespace: "team-b"}); err != nil {
		t.Fatalf("other namespace: %v", err)
	}
	if reg.Size() != 3 {
		t.Fatalf("size = %d, want 3", reg.Size())
	}

	// Reusing an explicit id is a duplicate, not an overwrite.
	if _, err := reg.Register(&Entry{ID: first.ID, Kind: KindModel, Name: "renamed", Version: "1.0.0"}); code(t, err) != CodeDuplicate {
		t.Fatalf("register with a known id = %v, want resource_already_exists", err)
	}
	// An unknown id registers normally.
	if _, err := reg.Register(&Entry{ID: "model-fresh", Kind: KindModel, Name: "fresh"}); err != nil {
		t.Fatalf("register: %v", err)
	}
	if _, ok := reg.Get("model-missing"); ok {
		t.Fatal("an unknown id must not resolve")
	}

	// FindByKey is what synchronisation uses instead of colliding.
	if e, ok := reg.FindByKey("team-a", "router", "1.0.0"); !ok || e.ID != first.ID {
		t.Fatalf("find by key = %v, %v", e, ok)
	}
	if _, ok := reg.FindByKey("team-a", "router", "9.9.9"); ok {
		t.Fatal("an absent key must not resolve")
	}

	// The curation machine is enforced on the stored copy too. The record is
	// approved by default, so approved -> pending is the illegal edge here.
	if _, err := reg.SetStatus(first.ID, StatusPending); code(t, err) != CodeInvalidStatusTransition {
		t.Fatalf("approved -> pending = %v, want a refusal", err)
	}
	if _, err := reg.SetStatus(first.ID, Status("whatever")); code(t, err) != CodeInvalidStatusTransition {
		t.Fatalf("unknown status = %v, want a refusal", err)
	}
	// approved -> deprecated -> pending -> approved is the way back.
	if _, err := reg.SetStatus(first.ID, StatusDeprecated); err != nil {
		t.Fatalf("deprecate: %v", err)
	}
	if _, err := reg.SetStatus(first.ID, StatusPending); err != nil {
		t.Fatalf("resubmit: %v", err)
	}
	if _, err := reg.SetStatus(first.ID, StatusApproved); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if _, err := reg.SetStatus("model-missing", StatusApproved); code(t, err) != CodeNotFound {
		t.Fatalf("missing record = %v, want resource_not_found", err)
	}

	// Counts and listings read through the store.
	if got := reg.CountsByKind()[string(KindModel)]; got != 4 {
		t.Fatalf("model count = %d, want 4", got)
	}
	if got := reg.HealthCounts()[string(HealthUnknown)]; got != 4 {
		t.Fatalf("unknown health = %d, want 4", got)
	}
	if got := len(reg.ListFiltered(Filter{Namespace: "team-a"})); got != 2 {
		t.Fatalf("team-a = %d, want 2", got)
	}
	if got := len(reg.ListFiltered(Filter{Status: string(StatusApproved)})); got != 4 {
		t.Fatalf("approved = %d, want 4", got)
	}
	if got := len(reg.ListFiltered(Filter{Query: "fresh"})); got != 1 {
		t.Fatalf("query = %d, want 1", got)
	}
	if got := len(reg.List("nope", "")); got != 0 {
		t.Fatalf("unknown kind = %d, want 0", got)
	}
	if err := reg.SetHealth(first.ID, HealthHealthy); err != nil {
		t.Fatalf("set health: %v", err)
	}
	if err := reg.Delete(first.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := reg.Delete(first.ID); code(t, err) != CodeNotFound {
		t.Fatalf("second delete = %v, want resource_not_found", err)
	}
}

// A driver message must never reach the API: the sentinel errors map to the
// same codes the in-memory path produces and everything else collapses to
// internal_error.
func TestStoreModeNormalisesPersistenceFailures(t *testing.T) {
	boom := errors.New("connection reset")
	cases := []struct {
		op   string
		err  error
		want httputil.Code
	}{
		{"upsert", boom, httputil.CodeInternal},
		{"upsert", ErrDuplicate, CodeDuplicate},
		{"get", ErrNotFound, CodeNotFound},
		{"delete", ErrNotFound, CodeNotFound},
		{"set_health", ErrNotFound, CodeNotFound},
	}
	for _, c := range cases {
		store := newMemResourceStore()
		store.fail(c.op, c.err)
		reg := NewWithStore(store)
		var err error
		switch c.op {
		case "upsert":
			_, err = reg.Upsert(&Entry{Kind: KindModel, Name: "x"})
		case "get":
			// Get swallows the failure and reports "absent" — a lookup that
			// cannot be served is not an error the caller can act on. The
			// mapping is asserted through SetStatus, which does surface it.
			if _, ok := reg.Get("model-x"); ok {
				t.Fatalf("%s: a failing lookup must report absent", c.op)
			}
			_, err = reg.SetStatus("model-x", StatusApproved)
		case "delete":
			err = reg.Delete("model-x")
		case "set_health":
			err = reg.SetHealth("model-x", HealthHealthy)
		}
		if code(t, err) != c.want {
			t.Fatalf("%s: code = %v, want %v", c.op, httputil.AsError(err).Code, c.want)
		}
	}
}

// A count that cannot be read must not turn into a map with holes: the console
// renders every kind and health bucket, so each one has to report zero.
func TestStoreModeCountsFallBackToZero(t *testing.T) {
	store := newMemResourceStore()
	store.fail("counts", errors.New("boom"))
	store.fail("size", errors.New("boom"))
	reg := NewWithStore(store)
	for _, k := range Kinds {
		if got := reg.CountsByKind()[string(k)]; got != 0 {
			t.Fatalf("kind %s = %d, want 0", k, got)
		}
	}
	for _, h := range Healths {
		if got := reg.HealthCounts()[string(h)]; got != 0 {
			t.Fatalf("health %s = %d, want 0", h, got)
		}
	}
	if got := reg.Size(); got != 0 {
		t.Fatalf("size = %d, want 0", got)
	}
	// A listing that fails is empty rather than partial.
	store.fail("list", errors.New("boom"))
	if got := reg.Snapshot(); len(got) != 0 {
		t.Fatalf("snapshot = %d, want 0", len(got))
	}
}

// ---------------------------------------------------------------------------
// namespaces
// ---------------------------------------------------------------------------

func TestNamespacesRoundTripThroughTheStore(t *testing.T) {
	ns := newMemNamespaceStore()
	reg := NewWithStores(newMemResourceStore(), ns)

	if _, err := reg.UpsertNamespace(&Namespace{ID: "curated", Name: "Curated"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	ns.mu.Lock()
	_, persisted := ns.byID["curated"]
	ns.mu.Unlock()
	if !persisted {
		t.Fatal("the namespace did not reach the store")
	}

	// A fresh registry over the same store sees it after loading.
	reloaded := NewWithStores(newMemResourceStore(), ns)
	if err := reloaded.LoadNamespaces(context.Background()); err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := reloaded.DefaultStatus("curated"); got != StatusDraft {
		t.Fatalf("default status = %q, want draft for a curated namespace", got)
	}
	if got := reloaded.DefaultStatus("default"); got != StatusApproved {
		t.Fatalf("default status = %q, want approved", got)
	}
	if _, ok := reloaded.GetNamespace("curated"); !ok {
		t.Fatal("loaded namespace is missing")
	}
	if _, ok := reloaded.GetNamespace("not a slug"); ok {
		t.Fatal("an invalid id must not resolve")
	}

	// Deleting goes through the store, and the default namespace refuses.
	if err := reloaded.DeleteNamespace("curated"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := reloaded.DeleteNamespace("curated"); code(t, err) != CodeNotFound {
		t.Fatalf("second delete = %v, want resource_not_found", err)
	}
	if err := reloaded.DeleteNamespace(DefaultNamespace); code(t, err) != CodeInvalidNamespace {
		t.Fatalf("delete default = %v, want invalid_namespace", err)
	}
	if err := reloaded.DeleteNamespace("Not A Slug"); code(t, err) != CodeInvalidNamespace {
		t.Fatalf("delete invalid id = %v, want invalid_namespace", err)
	}

	// A store that fails reports internal_error, never the driver text.
	ns.fail = true
	if _, err := reloaded.UpsertNamespace(&Namespace{ID: "broken"}); code(t, err) != httputil.CodeInternal {
		t.Fatalf("broken upsert = %v, want internal_error", err)
	}
	if err := reloaded.LoadNamespaces(context.Background()); code(t, err) != httputil.CodeInternal {
		t.Fatalf("broken load = %v, want internal_error", err)
	}

	// Without a namespace store the in-memory view is still seeded.
	plain := New().WithNamespaceStore(nil)
	if got := plain.ListNamespaces(); len(got) != 1 || got[0].ID != DefaultNamespace {
		t.Fatalf("namespaces = %v, want just the default one", got)
	}
	if got := plain.AutoApprove("never-seen"); !got {
		t.Fatal("an unknown namespace must stay permissive")
	}
}

// ---------------------------------------------------------------------------
// sources
// ---------------------------------------------------------------------------

func TestSourceServicePersistsThroughTheStore(t *testing.T) {
	store := newMemSourceStore()
	reg := New()
	svc := NewSourceService(reg, store)

	src, err := svc.UpsertSource(&RegistrySource{
		Namespace: DefaultNamespace, Kind: SourceKindSkill, Name: "Skills",
		URL: "https://skills.test/index.json", Fixture: true, Enabled: true,
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	store.mu.Lock()
	_, persisted := store.byID[src.ID]
	store.mu.Unlock()
	if !persisted {
		t.Fatal("the source did not reach the store")
	}

	// Syncing writes the outcome back through the store, not just in memory.
	src2, err := svc.UpsertSource(&RegistrySource{
		ID: "src-fixture", Namespace: DefaultNamespace, Kind: SourceKindMCP, Name: "MCP",
		URL: "https://registry.test/servers", Fixture: true, Enabled: true,
	})
	if err != nil {
		t.Fatalf("upsert fixture: %v", err)
	}
	res, err := svc.Sync(context.Background(), src2.ID)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if res.Imported != 3 {
		t.Fatalf("imported = %d, want 3", res.Imported)
	}
	store.mu.Lock()
	recorded, ok := store.lastSync[src2.ID]
	store.mu.Unlock()
	if !ok || recorded.Status != SyncStatusOK {
		t.Fatalf("mark sync = %+v, %v", recorded, ok)
	}
	got, ok := svc.GetSource(src2.ID)
	if !ok || got.LastSyncStatus != SyncStatusOK {
		t.Fatalf("source after sync = %+v, %v", got, ok)
	}

	// SyncAll walks the enabled sources of a namespace and skips the rest.
	_, _ = svc.UpsertSource(&RegistrySource{
		ID: "src-off", Namespace: DefaultNamespace, Kind: SourceKindMCP, Name: "Off",
		URL: "https://registry.test/servers", Fixture: true, Enabled: false,
	})
	// Every source in this test is a fixture: the run must stay offline.
	_, _ = svc.UpsertSource(&RegistrySource{
		ID: "src-elsewhere", Namespace: "other", Kind: SourceKindMCP, Name: "Elsewhere",
		URL: "https://registry.test/servers", Fixture: true, Enabled: true,
	})
	if got := svc.ListSources(DefaultNamespace); len(got) != 3 {
		t.Fatalf("sources in default = %d, want 3", len(got))
	}
	if got := svc.ListSources("other"); len(got) != 1 {
		t.Fatalf("sources in other = %d, want 1", len(got))
	}
	all := svc.SyncAll(context.Background(), DefaultNamespace)
	if len(all) != 2 {
		t.Fatalf("sync all = %v, want the two enabled sources", all)
	}
	if _, ok := all["src-off"]; ok {
		t.Fatal("a disabled source must not run")
	}

	if err := svc.DeleteSource(src2.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := svc.DeleteSource(src2.ID); code(t, err) != CodeNotFound {
		t.Fatalf("second delete = %v, want resource_not_found", err)
	}
	if _, ok := svc.GetSource("src-missing"); ok {
		t.Fatal("an unknown source must not resolve")
	}

	// A store that fails reports internal_error rather than the driver text.
	store.fail = true
	if _, err := svc.UpsertSource(&RegistrySource{
		Kind: SourceKindMCP, Name: "Broken", URL: "https://registry.test/servers",
	}); code(t, err) != httputil.CodeInternal {
		t.Fatalf("broken upsert = %v, want internal_error", err)
	}
	if got := svc.ListSources(""); len(got) != 0 {
		t.Fatalf("broken list = %d, want 0", len(got))
	}
}

// The source validator is the gate in front of both the store and the run, so
// every field that can be wrong has to be named.
func TestValidateSourceNamesTheOffendingField(t *testing.T) {
	cases := []struct {
		name string
		src  *RegistrySource
	}{
		{"nil", nil},
		{"no name", &RegistrySource{Kind: SourceKindMCP, URL: "https://x.test"}},
		{"unknown kind", &RegistrySource{Kind: "model", Name: "x", URL: "https://x.test"}},
		{"url that is not a URL", &RegistrySource{Kind: SourceKindMCP, Name: "x", URL: "x.test"}},
		{"unknown auth scheme", &RegistrySource{
			Kind: SourceKindMCP, Name: "x", URL: "https://x.test", AuthScheme: "basic",
		}},
		{"bearer without a variable", &RegistrySource{
			Kind: SourceKindMCP, Name: "x", URL: "https://x.test", AuthScheme: AuthSchemeBearer,
		}},
		{"variable that is not a name", &RegistrySource{
			Kind: SourceKindMCP, Name: "x", URL: "https://x.test",
			AuthScheme: AuthSchemeBearer, AuthTokenEnv: "sk-token",
		}},
		{"namespace that is not a slug", &RegistrySource{
			Kind: SourceKindMCP, Name: "x", URL: "https://x.test", Namespace: "Not A Slug",
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			want := CodeInvalidSource
			if c.name == "namespace that is not a slug" {
				// The namespace has its own code: the console renders it next
				// to the namespace field, not next to the source as a whole.
				want = CodeInvalidNamespace
			}
			if err := ValidateSource(c.src); code(t, err) != want {
				t.Fatalf("ValidateSource = %v, want %v", err, want)
			}
		})
	}

	// A minimal source is enough: no auth, no namespace, no schedule.
	if err := ValidateSource(&RegistrySource{Kind: SourceKindSkill, Name: "Skills", URL: "https://skills.test"}); err != nil {
		t.Fatalf("minimal source = %v", err)
	}
	var nilSource *RegistrySource
	if got := nilSource.EffectiveNamespace(); got != DefaultNamespace {
		t.Fatalf("nil namespace = %q, want default", got)
	}
	if got := (&RegistrySource{Namespace: " Team-A "}).EffectiveNamespace(); got != "team-a" {
		t.Fatalf("namespace = %q, want team-a", got)
	}
	// The label is what a log line may carry: the id, never the endpoint.
	if got := UpstreamLabel(RegistrySource{ID: "src-1", Kind: SourceKindMCP, URL: "https://secret.test"}); got != "mcp source src-1" {
		t.Fatalf("label = %q", got)
	}
	if strings.Contains(UpstreamLabel(RegistrySource{ID: "src-1", URL: "https://secret.test"}), "secret") {
		t.Fatal("the label leaked the upstream endpoint")
	}
}
