package handlers

import (
	"context"
	"net/http"
	"strconv"
	"sync"
	"testing"

	"agenthub/internal/computers"
	"agenthub/internal/knowledge"
	"agenthub/internal/schedules"
)

// In-memory implementations of the agent-capability Store ports. The store/
// package only ships the GORM (PostgreSQL) implementation, which is DB-gated;
// these let the HTTP layer be exercised without a database.

type memComputers struct {
	mu    sync.Mutex
	items []*computers.Computer
}

func (m *memComputers) List(_ context.Context, ownerID string) ([]*computers.Computer, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*computers.Computer, 0, len(m.items))
	for _, c := range m.items {
		if ownerID != "" && c.OwnerID != ownerID {
			continue
		}
		out = append(out, c)
	}
	return out, nil
}

func (m *memComputers) Get(_ context.Context, id string) (*computers.Computer, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range m.items {
		if c.ID == id {
			return c, nil
		}
	}
	return nil, nil
}

func (m *memComputers) Create(_ context.Context, c *computers.Computer) (*computers.Computer, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c.ID == "" {
		c.ID = "comp-" + strconv.Itoa(len(m.items)+1)
	}
	m.items = append(m.items, c)
	return c, nil
}

type memSchedules struct {
	mu    sync.Mutex
	items []*schedules.Schedule
}

func (m *memSchedules) List(_ context.Context, ownerID string) ([]*schedules.Schedule, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*schedules.Schedule, 0, len(m.items))
	for _, s := range m.items {
		if ownerID != "" && s.OwnerID != ownerID {
			continue
		}
		out = append(out, s)
	}
	return out, nil
}

func (m *memSchedules) Get(_ context.Context, id string) (*schedules.Schedule, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range m.items {
		if s.ID == id {
			return s, nil
		}
	}
	return nil, nil
}

func (m *memSchedules) Create(_ context.Context, s *schedules.Schedule) (*schedules.Schedule, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s.ID == "" {
		s.ID = "sched-" + strconv.Itoa(len(m.items)+1)
	}
	m.items = append(m.items, s)
	return s, nil
}

func (m *memSchedules) Update(_ context.Context, s *schedules.Schedule) (*schedules.Schedule, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, x := range m.items {
		if x.ID == s.ID {
			m.items[i] = s
			return s, nil
		}
	}
	return s, nil
}

func (m *memSchedules) Delete(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, x := range m.items {
		if x.ID == id {
			m.items = append(m.items[:i], m.items[i+1:]...)
			return nil
		}
	}
	return nil
}

type memKnowledge struct {
	mu    sync.Mutex
	items []*knowledge.Knowledge
}

func (m *memKnowledge) List(_ context.Context, agentID string) ([]*knowledge.Knowledge, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*knowledge.Knowledge, 0, len(m.items))
	for _, k := range m.items {
		if agentID != "" && k.AgentID != agentID {
			continue
		}
		out = append(out, k)
	}
	return out, nil
}

func (m *memKnowledge) Add(_ context.Context, k *knowledge.Knowledge) (*knowledge.Knowledge, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if k.ID == "" {
		k.ID = "kn-" + strconv.Itoa(len(m.items)+1)
	}
	m.items = append(m.items, k)
	return k, nil
}

func agentcapHandler(t *testing.T, c computers.Store, s schedules.Store, k knowledge.Store) http.Handler {
	t.Helper()
	d := testDeps(t, openConfig(""))
	d.Computers = c
	d.Schedules = s
	d.Knowledge = k
	return Register(d)
}

type capErrBody struct {
	Code string `json:"code"`
}

func TestComputersEndpoints(t *testing.T) {
	h := agentcapHandler(t, &memComputers{}, &memSchedules{}, &memKnowledge{})

	wList := do(t, h, http.MethodGet, "/api/computers", "", nil)
	if wList.Code != http.StatusOK {
		t.Fatalf("list empty: status = %d", wList.Code)
	}

	// An unknown kind falls back to browser; a valid kind is preserved.
	wBad := do(t, h, http.MethodPost, "/api/computers", `{"kind":"nonsense","agentId":"a1"}`, nil)
	if wBad.Code != http.StatusCreated {
		t.Fatalf("create bad kind: status = %d (%s)", wBad.Code, wBad.Body.String())
	}
	var created computers.Computer
	decodeJSON(t, wBad, &created)
	if created.Kind != computers.KindBrowser {
		t.Fatalf("kind = %q, want browser fallback", created.Kind)
	}

	wScreen := do(t, h, http.MethodPost, "/api/computers", `{"kind":"screen","agentId":"a1"}`, nil)
	if wScreen.Code != http.StatusCreated {
		t.Fatalf("create screen: status = %d (%s)", wScreen.Code, wScreen.Body.String())
	}
	var screen computers.Computer
	decodeJSON(t, wScreen, &screen)
	if screen.Kind != computers.KindScreen || screen.ID == "" {
		t.Fatalf("created = %+v", screen)
	}

	wGet := do(t, h, http.MethodGet, "/api/computers/"+screen.ID, "", nil)
	if wGet.Code != http.StatusOK {
		t.Fatalf("get: status = %d", wGet.Code)
	}

	// A missing id is a 404 with the computer_not_found machine code.
	wMiss := do(t, h, http.MethodGet, "/api/computers/missing", "", nil)
	if wMiss.Code != http.StatusNotFound {
		t.Fatalf("get missing: status = %d, want 404", wMiss.Code)
	}
	var eb capErrBody
	decodeJSON(t, wMiss, &eb)
	if eb.Code != "computer_not_found" {
		t.Fatalf("code = %q, want computer_not_found", eb.Code)
	}

	wList2 := do(t, h, http.MethodGet, "/api/computers", "", nil)
	if wList2.Code != http.StatusOK {
		t.Fatalf("list after create: status = %d", wList2.Code)
	}
	var listing []*computers.Computer
	decodeJSON(t, wList2, &listing)
	if len(listing) != 2 {
		t.Fatalf("listing = %d, want 2", len(listing))
	}
}

func TestComputersFeatureDisabled(t *testing.T) {
	// No Computers store wired in: the endpoint reports the feature as unavailable
	// with a 503 and the single feature_unavailable code.
	h := agentcapHandler(t, nil, nil, nil)
	w := do(t, h, http.MethodGet, "/api/computers", "", nil)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", w.Code)
	}
	var eb capErrBody
	decodeJSON(t, w, &eb)
	if eb.Code != "feature_unavailable" {
		t.Fatalf("code = %q, want feature_unavailable", eb.Code)
	}
}

func TestSchedulesEndpoints(t *testing.T) {
	h := agentcapHandler(t, &memComputers{}, &memSchedules{}, &memKnowledge{})

	// An empty goal is a validation error, not a 500.
	w := do(t, h, http.MethodPost, "/api/schedules", `{"goal":""}`, nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("empty goal: status = %d (%s)", w.Code, w.Body.String())
	}
	var eb capErrBody
	decodeJSON(t, w, &eb)
	if eb.Code != "schedule_invalid" {
		t.Fatalf("code = %q, want schedule_invalid", eb.Code)
	}

	w = do(t, h, http.MethodPost, "/api/schedules",
		`{"goal":"daily digest","trigger":"cron","cronExpr":"0 9 * * *","enabled":true}`, nil)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: status = %d (%s)", w.Code, w.Body.String())
	}
	var sched schedules.Schedule
	decodeJSON(t, w, &sched)
	if sched.ID == "" || sched.Goal != "daily digest" || !sched.Enabled {
		t.Fatalf("created = %+v", sched)
	}
	if sched.Trigger != "cron" {
		t.Fatalf("trigger = %q, want cron", sched.Trigger)
	}

	if w := do(t, h, http.MethodGet, "/api/schedules/"+sched.ID, "", nil); w.Code != http.StatusOK {
		t.Fatalf("get: status = %d", w.Code)
	}
	if w := do(t, h, http.MethodGet, "/api/schedules/missing", "", nil); w.Code != http.StatusNotFound {
		t.Fatalf("get missing: status = %d, want 404", w.Code)
	}

	// Toggle enabled off.
	w = do(t, h, http.MethodPut, "/api/schedules/"+sched.ID,
		`{"goal":"daily digest","enabled":false}`, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("update: status = %d (%s)", w.Code, w.Body.String())
	}
	var updated schedules.Schedule
	decodeJSON(t, w, &updated)
	if updated.Enabled {
		t.Fatalf("enabled = %v, want false", updated.Enabled)
	}

	// Delete is idempotent: a missing id still returns 204.
	if w := do(t, h, http.MethodDelete, "/api/schedules/"+sched.ID, "", nil); w.Code != http.StatusNoContent {
		t.Fatalf("delete: status = %d, want 204", w.Code)
	}
	if w := do(t, h, http.MethodDelete, "/api/schedules/does-not-exist", "", nil); w.Code != http.StatusNoContent {
		t.Fatalf("delete missing: status = %d, want 204", w.Code)
	}
}

func TestSchedulesFeatureDisabled(t *testing.T) {
	// No Schedules store wired in: the endpoint reports the feature as unavailable
	// with a 503 and the single feature_unavailable code.
	h := agentcapHandler(t, nil, nil, nil)
	w := do(t, h, http.MethodGet, "/api/schedules", "", nil)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", w.Code)
	}
	var eb capErrBody
	decodeJSON(t, w, &eb)
	if eb.Code != "feature_unavailable" {
		t.Fatalf("code = %q, want feature_unavailable", eb.Code)
	}
}

func TestKnowledgeEndpoints(t *testing.T) {
	store := &memKnowledge{}
	store.Add(context.Background(), &knowledge.Knowledge{AgentID: "a1", ChunkText: "fact one", SourceRef: "s1"})
	store.Add(context.Background(), &knowledge.Knowledge{AgentID: "a2", ChunkText: "fact two", SourceRef: "s2"})
	h := agentcapHandler(t, &memComputers{}, &memSchedules{}, store)

	w := do(t, h, http.MethodGet, "/api/knowledge", "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("list all: status = %d", w.Code)
	}
	var all []knowledge.Knowledge
	decodeJSON(t, w, &all)
	if len(all) != 2 {
		t.Fatalf("all = %d, want 2", len(all))
	}

	w = do(t, h, http.MethodGet, "/api/knowledge?agentId=a1", "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("list filtered: status = %d", w.Code)
	}
	var filtered []knowledge.Knowledge
	decodeJSON(t, w, &filtered)
	if len(filtered) != 1 || filtered[0].AgentID != "a1" {
		t.Fatalf("filtered = %+v", filtered)
	}
}

func TestKnowledgeFeatureDisabled(t *testing.T) {
	h := agentcapHandler(t, nil, nil, nil)
	w := do(t, h, http.MethodGet, "/api/knowledge", "", nil)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", w.Code)
	}
	var eb capErrBody
	decodeJSON(t, w, &eb)
	if eb.Code != "feature_unavailable" {
		t.Fatalf("code = %q, want feature_unavailable", eb.Code)
	}
}
