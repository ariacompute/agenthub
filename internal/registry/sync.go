package registry

import (
	"context"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"agenthub/internal/httputil"
)

// MaxSyncRecords caps how many records one run may import. It is a safety rail:
// an upstream registry that answers with a hundred thousand entries must not
// turn into a hundred thousand writes, and a runaway pagination bug must not
// exhaust the connection pool.
const MaxSyncRecords = 500

// SourceService owns the upstream registry sources and runs their
// synchronisation.
//
// It keeps the same two behaviours as the rest of the platform: without a
// SourceStore everything lives in memory and the pipeline is fully offline; with
// one attached the identical public methods read and write PostgreSQL.
type SourceService struct {
	reg   *Registry
	store SourceStore

	// forceFixture is the deployment level pin: when it is on no source
	// reaches the network, whatever its own fixture flag says.
	forceFixture bool

	mu       sync.Mutex
	sources  map[string]*RegistrySource
	inFlight map[string]bool
}

// SourceServiceOption configures a service at construction time. Everything is
// set before the service is published, so no option needs locking.
type SourceServiceOption func(*SourceService)

// WithFixturePin forces every source onto the offline deterministic client
// (AGENTHUB_REGISTRY_SYNC_FIXTURE). It is the air-gapped switch: with it on,
// no agenthub process opens a connection to an upstream registry.
func WithFixturePin(on bool) SourceServiceOption {
	return func(s *SourceService) { s.forceFixture = on }
}

// NewSourceService builds the service on top of a registry.
func NewSourceService(reg *Registry, store SourceStore, opts ...SourceServiceOption) *SourceService {
	s := &SourceService{
		reg:      reg,
		store:    store,
		sources:  map[string]*RegistrySource{},
		inFlight: map[string]bool{},
	}
	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}
	return s
}

// ClientFor resolves the fetcher a source needs: the offline fixture when the
// source is marked as one (or the whole instance is pinned to offline), the
// real HTTP client otherwise.
func (s *SourceService) ClientFor(src RegistrySource) Client {
	if src.Fixture || s.forceFixture {
		return FixtureClient{}
	}
	return NewHTTPClient()
}

// UpsertSource stores a source, generating an id when absent.
func (s *SourceService) UpsertSource(in *RegistrySource) (*RegistrySource, error) {
	if in == nil {
		return nil, &httputil.Error{Code: CodeInvalidSource}
	}
	if err := ValidateSource(in); err != nil {
		return nil, err
	}
	src := *in
	ns, err := ParseNamespaceID(src.Namespace)
	if err != nil {
		return nil, err
	}
	src.Namespace = ns
	src.Name = strings.TrimSpace(src.Name)
	src.URL = strings.TrimSpace(src.URL)
	if src.AuthScheme == "" {
		src.AuthScheme = AuthSchemeNone
	}
	if src.CreatedAt.IsZero() {
		src.CreatedAt = time.Now().UTC()
	}
	src.UpdatedAt = time.Now().UTC()
	if src.ID == "" {
		src.ID = NewID("src")
	}
	if s.store != nil {
		stored, err := s.store.UpsertSource(context.Background(), &src)
		if err != nil {
			return nil, storeErr("upsert_source", err)
		}
		return stored, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if prev, ok := s.sources[src.ID]; ok && !prev.CreatedAt.IsZero() {
		src.CreatedAt = prev.CreatedAt
	}
	s.sources[src.ID] = &src
	cp := src
	return &cp, nil
}

// GetSource returns one source by id.
func (s *SourceService) GetSource(id string) (*RegistrySource, bool) {
	if s.store != nil {
		src, err := s.store.GetSource(context.Background(), id)
		if err != nil {
			return nil, false
		}
		return src, true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	src, ok := s.sources[id]
	if !ok {
		return nil, false
	}
	cp := *src
	return &cp, true
}

// ListSources returns every source in a namespace, sorted by id. An empty
// namespace means all.
func (s *SourceService) ListSources(namespace string) []*RegistrySource {
	var out []*RegistrySource
	if s.store != nil {
		got, err := s.store.ListSources(context.Background(), namespace)
		if err != nil {
			slog.Error("registry: list sources", "error", err)
			return nil
		}
		out = got
	} else {
		s.mu.Lock()
		out = make([]*RegistrySource, 0, len(s.sources))
		for _, src := range s.sources {
			cp := *src
			out = append(out, &cp)
		}
		s.mu.Unlock()
	}
	ns := strings.TrimSpace(namespace)
	if ns != "" {
		kept := out[:0]
		for _, src := range out {
			if src.Namespace == ns {
				kept = append(kept, src)
			}
		}
		out = kept
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// DeleteSource removes a source. Records it already imported stay: they are
// part of the catalogue now and only a curator can withdraw them.
func (s *SourceService) DeleteSource(id string) error {
	if s.store != nil {
		return storeErr("delete_source", s.store.DeleteSource(context.Background(), id))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.sources[id]; !ok {
		return &httputil.Error{Code: CodeNotFound}
	}
	delete(s.sources, id)
	return nil
}

// Sync pulls one source and merges its records into the catalogue.
//
// The merge key is (namespace, name, version): a record that already exists is
// refreshed in place and keeps its curation state, a new one enters the
// namespace's default state. Imported records go through the same Upsert path
// as a hand written registration, so an upstream catalogue can never bypass
// validation or curation.
func (s *SourceService) Sync(ctx context.Context, id string) (SyncResult, error) {
	src, ok := s.GetSource(id)
	if !ok {
		return SyncResult{}, &httputil.Error{Code: CodeNotFound}
	}
	if !src.Enabled {
		return SyncResult{Status: SyncStatusFailed, Error: "source_disabled", At: time.Now().UTC()},
			newFieldError(CodeInvalidSource, "enabled")
	}
	if !s.begin(id) {
		return SyncResult{}, &httputil.Error{Code: CodeSyncInFlight}
	}
	defer s.end(id)

	res := SyncResult{At: time.Now().UTC()}
	records, err := s.ClientFor(*src).Fetch(ctx, *src)
	if err != nil {
		res.Status = SyncStatusFailed
		// Only a machine readable code reaches the caller: the upstream body may
		// carry anything and must never be echoed into the API or the log.
		res.Error = string(httputil.AsError(err).Code)
		if res.Error == "" {
			res.Error = "upstream_unreachable"
		}
		slog.Warn("registry: sync failed", "source", id, "code", res.Error)
		s.mark(ctx, src, res)
		return res, err
	}
	if len(records) > MaxSyncRecords {
		records = records[:MaxSyncRecords]
	}
	res.Total = len(records)

	ns := strings.TrimSpace(src.Namespace)
	if ns == "" {
		ns = DefaultNamespace
	}
	for _, rec := range records {
		if rec == nil {
			res.Skipped++
			continue
		}
		e := *rec
		e.Kind = Kind(src.Kind)
		e.Namespace = ns
		e.SourceID = src.ID
		e.ID = ""
		if strings.TrimSpace(e.Name) == "" {
			res.Skipped++
			continue
		}
		if existing, found := s.reg.FindByKey(ns, e.Name, e.Version); found {
			// Keep the curation state an operator already assigned: a
			// re-synchronised record refreshes its metadata, it does not reset
			// an approval back to draft.
			e.ID = existing.ID
			e.Status = existing.EffectiveStatus()
		}
		if _, err := s.reg.Upsert(&e); err != nil {
			res.Skipped++
			slog.Debug("registry: sync skipped record", "source", id, "name", e.Name, "code", httputil.AsError(err).Code)
			continue
		}
		if e.ID != "" {
			res.Updated++
		} else {
			res.Imported++
		}
	}
	res.Status = SyncStatusOK
	slog.Info("registry: sync complete", "source", id, "imported", res.Imported, "updated", res.Updated, "skipped", res.Skipped)
	s.mark(ctx, src, res)
	return res, nil
}

// SyncAll runs every enabled source in a namespace. Sources without a schedule
// are still run: the console triggers them manually.
func (s *SourceService) SyncAll(ctx context.Context, namespace string) map[string]SyncResult {
	out := map[string]SyncResult{}
	for _, src := range s.ListSources(namespace) {
		if !src.Enabled {
			continue
		}
		res, err := s.Sync(ctx, src.ID)
		if err != nil && res.Status == "" {
			res.Status = SyncStatusFailed
			res.Error = string(httputil.AsError(err).Code)
			res.At = time.Now().UTC()
		}
		out[src.ID] = res
	}
	return out
}

// mark records the outcome on the source so the console can show it.
func (s *SourceService) mark(ctx context.Context, src *RegistrySource, res SyncResult) {
	if src == nil {
		return
	}
	src.LastSyncStatus = res.Status
	src.LastSyncCount = res.Imported + res.Updated
	src.LastSyncError = res.Error
	at := res.At
	src.LastSyncAt = &at
	src.UpdatedAt = time.Now().UTC()
	if s.store != nil {
		if err := s.store.MarkSync(ctx, src.ID, res); err != nil {
			slog.Error("registry: record sync outcome", "source", src.ID, "error", err)
		}
		return
	}
	s.mu.Lock()
	if cur, ok := s.sources[src.ID]; ok {
		cur.LastSyncStatus = src.LastSyncStatus
		cur.LastSyncCount = src.LastSyncCount
		cur.LastSyncError = src.LastSyncError
		cur.LastSyncAt = src.LastSyncAt
		cur.UpdatedAt = src.UpdatedAt
	}
	s.mu.Unlock()
}

// begin claims a source for one run. Synchronisation is single flight: two
// concurrent runs against the same upstream would fight over the same dedup
// key and double every counter.
func (s *SourceService) begin(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.inFlight[id] {
		return false
	}
	s.inFlight[id] = true
	return true
}

func (s *SourceService) end(id string) {
	s.mu.Lock()
	delete(s.inFlight, id)
	s.mu.Unlock()
}
