package registry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"agenthub/internal/httputil"
)

func code(t *testing.T, err error) httputil.Code {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	return httputil.AsError(err).Code
}

func TestStatusMachine(t *testing.T) {
	legal := []struct{ from, to Status }{
		{StatusDraft, StatusPending},
		{StatusPending, StatusApproved},
		{StatusPending, StatusRejected},
		{StatusApproved, StatusDeprecated},
		{StatusRejected, StatusPending},
		{StatusDeprecated, StatusPending},
	}
	for _, c := range legal {
		if !CanTransition(c.from, c.to) {
			t.Fatalf("%s -> %s must be allowed", c.from, c.to)
		}
	}
	illegal := []struct{ from, to Status }{
		{StatusDraft, StatusApproved},
		{StatusRejected, StatusApproved},
		{StatusDeprecated, StatusApproved},
		{StatusApproved, StatusDraft},
	}
	for _, c := range illegal {
		if CanTransition(c.from, c.to) {
			t.Fatalf("%s -> %s must be rejected", c.from, c.to)
		}
	}
	// An unknown state has no edges at all.
	if CanTransition(Status("bogus"), StatusApproved) {
		t.Fatal("unknown status must not transition")
	}
	// Repeating the current state is an idempotent save, not an event.
	if !CanTransition(StatusApproved, StatusApproved) {
		t.Fatal("same status must be allowed")
	}
}

func TestSetStatusRejectsIllegalTransition(t *testing.T) {
	reg := New()
	e, err := reg.Upsert(&Entry{Kind: KindMCP, Name: "hr", Version: "1.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	if e.Status != StatusApproved {
		t.Fatalf("default status = %q, want approved", e.Status)
	}
	if _, err := reg.SetStatus(e.ID, StatusDraft); code(t, err) != CodeInvalidStatusTransition {
		t.Fatalf("approved -> draft = %v", err)
	}
	moved, err := reg.SetStatus(e.ID, StatusDeprecated)
	if err != nil || moved.Status != StatusDeprecated {
		t.Fatalf("deprecate = %+v, %v", moved, err)
	}
	if _, err := reg.SetStatus("missing", StatusApproved); code(t, err) != CodeNotFound {
		t.Fatalf("missing entry = %v", err)
	}
}

func TestDiscoverableHidesUnapproved(t *testing.T) {
	reg := New()
	if _, err := reg.Upsert(&Entry{Kind: KindMCP, Name: "approved", Version: "1.0.0"}); err != nil {
		t.Fatal(err)
	}
	pending, err := reg.Upsert(&Entry{Kind: KindMCP, Name: "pending", Version: "1.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reg.SetStatus(pending.ID, StatusDeprecated); err != nil {
		t.Fatal(err)
	}
	if _, err := reg.SetStatus(pending.ID, StatusPending); err != nil {
		t.Fatal(err)
	}
	if got := len(MCPServerDescriptions(reg)); got != 1 {
		t.Fatalf("exported servers = %d, want 1", got)
	}
	card := AgentCard(reg, "http://localhost", "0.1.0", "1.0.0")
	if len(card.Skills) != 1 || card.Skills[0].Name != "approved" {
		t.Fatalf("card skills = %+v", card.Skills)
	}
	if card.Metadata["resourceTotal"] != 1 {
		t.Fatalf("card total = %v", card.Metadata["resourceTotal"])
	}
	// The control plane still sees everything: a curator cannot act on what a
	// listing hides.
	if got := reg.ListFiltered(Filter{}); len(got) != 2 {
		t.Fatalf("control plane listing = %d, want 2", len(got))
	}
	if got := reg.ListFiltered(Filter{Status: string(StatusPending)}); len(got) != 1 {
		t.Fatalf("pending filter = %d, want 1", len(got))
	}
}

func TestNamespaceAutoApproveDecidesDefaultStatus(t *testing.T) {
	reg := New()
	if _, err := reg.UpsertNamespace(&Namespace{ID: "curated", Name: "Curated", AutoApprove: false}); err != nil {
		t.Fatal(err)
	}
	e, err := reg.Upsert(&Entry{Kind: KindSkill, Name: "sop", Version: "1.0.0", Namespace: "curated"})
	if err != nil {
		t.Fatal(err)
	}
	if e.Status != StatusDraft {
		t.Fatalf("status = %q, want draft", e.Status)
	}
	if got := reg.ListFiltered(Filter{Namespace: "curated"}); len(got) != 1 {
		t.Fatalf("namespace filter = %d, want 1", len(got))
	}
	if got := reg.ListFiltered(Filter{Namespace: DefaultNamespace}); len(got) != 0 {
		t.Fatalf("default namespace = %d, want 0", len(got))
	}
}

func TestNamespaceValidationAndDefaultIsProtected(t *testing.T) {
	reg := New()
	if _, err := reg.UpsertNamespace(&Namespace{ID: "Not A Slug"}); code(t, err) != CodeInvalidNamespace {
		t.Fatalf("bad slug = %v", err)
	}
	if err := reg.DeleteNamespace(DefaultNamespace); code(t, err) != CodeInvalidNamespace {
		t.Fatalf("deleting default = %v", err)
	}
	if _, ok := reg.GetNamespace("never-created"); ok {
		t.Fatal("unknown namespace must be absent")
	}
	// An unknown namespace stays permissive so a record registered into one
	// nobody created yet behaves like it did before namespaces existed.
	if !reg.AutoApprove("never-created") {
		t.Fatal("unknown namespace must default to auto approve")
	}
	if len(reg.ListNamespaces()) != 1 || reg.ListNamespaces()[0].ID != DefaultNamespace {
		t.Fatalf("namespaces = %+v", reg.ListNamespaces())
	}
}

func TestDedupKeyIsNamespaceNameVersion(t *testing.T) {
	reg := New()
	if _, err := reg.Upsert(&Entry{Kind: KindMCP, Name: "hr", Version: "1.0.0"}); err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Upsert(&Entry{Kind: KindMCP, Name: "hr", Version: "1.0.0"}); code(t, err) != CodeDuplicate {
		t.Fatalf("same name+version = %v", err)
	}
	// Same name, different version is a different record.
	if _, err := reg.Upsert(&Entry{Kind: KindMCP, Name: "hr", Version: "2.0.0"}); err != nil {
		t.Fatalf("new version = %v", err)
	}
	// Same name+version in another namespace does not collide.
	if _, err := reg.Upsert(&Entry{Kind: KindMCP, Name: "hr", Version: "1.0.0", Namespace: "other"}); err != nil {
		t.Fatalf("other namespace = %v", err)
	}
	if got, ok := reg.FindByKey(DefaultNamespace, "hr", "1.0.0"); !ok || got.Version != "1.0.0" {
		t.Fatalf("find by key = %+v, %v", got, ok)
	}
	if _, ok := reg.FindByKey(DefaultNamespace, "hr", "9.9.9"); ok {
		t.Fatal("missing key must be absent")
	}
}

func TestValidateKindConfigModelRouter(t *testing.T) {
	reg := New()
	if _, err := reg.Upsert(&Entry{
		Kind: KindModel, Name: "router", Version: "1.0.0",
		KindConfig: &KindConfig{Model: &ModelConfig{ProviderMode: ProviderModeOpenAICompatible}},
	}); code(t, err) != CodeInvalidKindConfig {
		t.Fatalf("router without endpoint = %v", err)
	}
	if _, err := reg.Upsert(&Entry{
		Kind: KindModel, Name: "router", Version: "1.0.0",
		KindConfig: &KindConfig{Model: &ModelConfig{
			ProviderMode: ProviderModeOpenAICompatible,
			Endpoint:     "https://router.test/v1",
			ModelID:      "gpt-4o-mini",
			APIKeyEnv:    "OPENAI_API_KEY",
			Targets:      []ModelTarget{{Name: "primary", Weight: 3}, {Name: "fallback", Model: "gpt-4o"}},
			TimeoutMS:    5000,
		}},
	}); err != nil {
		t.Fatalf("valid router = %v", err)
	}
	// A bad credential reference is refused before it can ever be stored.
	if _, err := reg.Upsert(&Entry{
		Kind: KindModel, Name: "router2", Version: "1.0.0",
		KindConfig: &KindConfig{Model: &ModelConfig{
			ProviderMode: ProviderModeOpenAICompatible,
			Endpoint:     "https://router.test/v1",
			ModelID:      "m",
			APIKeyEnv:    "not a var name",
		}},
	}); code(t, err) != CodeInvalidKindConfig {
		t.Fatalf("bad api key env = %v", err)
	}
	// An unknown provider mode is refused.
	if _, err := reg.Upsert(&Entry{
		Kind: KindModel, Name: "router3", Version: "1.0.0",
		KindConfig: &KindConfig{Model: &ModelConfig{ProviderMode: "carrier-pigeon"}},
	}); code(t, err) != CodeInvalidKindConfig {
		t.Fatalf("bad provider mode = %v", err)
	}
	// The offline mock needs nothing.
	if _, err := reg.Upsert(&Entry{
		Kind: KindModel, Name: "offline", Version: "1.0.0",
		KindConfig: &KindConfig{Model: &ModelConfig{ProviderMode: ProviderModeMock}},
	}); err != nil {
		t.Fatalf("mock mode = %v", err)
	}
}

func TestValidateKindConfigPerKind(t *testing.T) {
	reg := New()
	// MCP over stdio needs a command.
	if _, err := reg.Upsert(&Entry{
		Kind: KindMCP, Name: "stdio", Version: "1.0.0",
		KindConfig: &KindConfig{MCP: &MCPConfig{Transport: string(TransportStdio)}},
	}); code(t, err) != CodeInvalidKindConfig {
		t.Fatalf("stdio without command = %v", err)
	}
	if _, err := reg.Upsert(&Entry{
		Kind: KindMCP, Name: "stdio", Version: "1.0.0",
		KindConfig: &KindConfig{MCP: &MCPConfig{Transport: string(TransportStdio), Command: "npx"}},
	}); err != nil {
		t.Fatalf("stdio with command = %v", err)
	}
	// MCP over HTTP needs a URL.
	if _, err := reg.Upsert(&Entry{
		Kind: KindMCP, Name: "http", Version: "1.0.0",
		KindConfig: &KindConfig{MCP: &MCPConfig{Transport: string(TransportStreamableHTTP)}},
	}); code(t, err) != CodeInvalidKindConfig {
		t.Fatalf("http without url = %v", err)
	}
	// Agent capabilities are a closed vocabulary.
	if _, err := reg.Upsert(&Entry{
		Kind: KindAgent, Name: "aria", Version: "1.0.0",
		KindConfig: &KindConfig{Agent: &AgentConfig{Capabilities: []string{"telepathy"}}},
	}); code(t, err) != CodeInvalidKindConfig {
		t.Fatalf("unknown capability = %v", err)
	}
	if _, err := reg.Upsert(&Entry{
		Kind: KindAgent, Name: "aria", Version: "1.0.0",
		KindConfig: &KindConfig{Agent: &AgentConfig{
			CardURL:      "https://agent.test/.well-known/agent.json",
			Capabilities: []string{AgentCapStreaming, AgentCapPushNotifications},
		}},
	}); err != nil {
		t.Fatalf("valid agent = %v", err)
	}
	// CLI must name a command.
	if _, err := reg.Upsert(&Entry{
		Kind: KindCLI, Name: "git", Version: "1.0.0",
		KindConfig: &KindConfig{CLI: &CLIConfig{Allowlist: []string{"git"}}},
	}); code(t, err) != CodeInvalidKindConfig {
		t.Fatalf("cli without command = %v", err)
	}
	// Custom must carry valid JSON.
	if _, err := reg.Upsert(&Entry{
		Kind: KindCustom, Name: "thing", Version: "1.0.0",
		KindConfig: &KindConfig{Custom: &CustomConfig{Schema: "{not json"}},
	}); code(t, err) != CodeInvalidKindConfig {
		t.Fatalf("broken schema = %v", err)
	}
	if _, err := reg.Upsert(&Entry{
		Kind: KindCustom, Name: "thing", Version: "1.0.0",
		KindConfig: &KindConfig{Custom: &CustomConfig{Schema: `{"type":"object"}`}},
	}); err != nil {
		t.Fatalf("valid custom = %v", err)
	}
	// A kind unknown to the registry never validates.
	if _, err := reg.Upsert(&Entry{Kind: "bogus", Name: "x"}); code(t, err) != CodeInvalidKind {
		t.Fatalf("bad kind = %v", err)
	}
}

func TestSyncFromFixtureImportsThenUpdates(t *testing.T) {
	reg := New()
	svc := NewSourceService(reg, nil)
	src, err := svc.UpsertSource(&RegistrySource{
		Namespace: DefaultNamespace, Kind: SourceKindMCP, Name: "MCP Registry",
		URL: "https://registry.modelcontextprotocol.test/v0/servers", Fixture: true, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := svc.Sync(context.Background(), src.ID)
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != SyncStatusOK || first.Imported != 3 || first.Updated != 0 {
		t.Fatalf("first sync = %+v", first)
	}
	second, err := svc.Sync(context.Background(), src.ID)
	if err != nil {
		t.Fatal(err)
	}
	// The dedup key turns a second run into updates, never duplicates.
	if second.Imported != 0 || second.Updated != 3 {
		t.Fatalf("second sync = %+v", second)
	}
	if reg.Size() != 3 {
		t.Fatalf("size = %d, want 3", reg.Size())
	}
	got, _ := svc.GetSource(src.ID)
	if got.LastSyncStatus != SyncStatusOK || got.LastSyncCount != 3 || got.LastSyncAt == nil {
		t.Fatalf("source after sync = %+v", got)
	}
	// Imported records are traceable to their upstream.
	for _, e := range reg.Snapshot() {
		if e.SourceID != src.ID {
			t.Fatalf("entry %s has no source", e.Name)
		}
	}
}

func TestSyncKeepsCurationState(t *testing.T) {
	reg := New()
	if _, err := reg.UpsertNamespace(&Namespace{ID: "curated", AutoApprove: false}); err != nil {
		t.Fatal(err)
	}
	svc := NewSourceService(reg, nil)
	src, err := svc.UpsertSource(&RegistrySource{
		Namespace: "curated", Kind: SourceKindSkill, Name: "Skill Registry",
		URL: "https://skills.test/index.json", Fixture: true, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Sync(context.Background(), src.ID); err != nil {
		t.Fatal(err)
	}
	entry, ok := reg.FindByKey("curated", "incident-triage", "1.2.0")
	if !ok {
		t.Fatal("skill was not imported")
	}
	if entry.Status != StatusDraft {
		t.Fatalf("status = %q, want draft", entry.Status)
	}
	// Curation is a two step walk: a draft is submitted, then approved.
	if _, err := reg.SetStatus(entry.ID, StatusApproved); code(t, err) != CodeInvalidStatusTransition {
		t.Fatalf("draft -> approved must be refused")
	}
	if _, err := reg.SetStatus(entry.ID, StatusPending); err != nil {
		t.Fatal(err)
	}
	approved, err := reg.SetStatus(entry.ID, StatusApproved)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Sync(context.Background(), src.ID); err != nil {
		t.Fatal(err)
	}
	if again, _ := reg.FindByKey("curated", "incident-triage", "1.2.0"); again.Status != StatusApproved {
		t.Fatalf("status after re-sync = %q, want approved", again.Status)
	}
	if approved.Status != StatusApproved {
		t.Fatalf("approved status = %q", approved.Status)
	}
}

// The offline pin is the deployment level kill switch: with it on, a source
// that points at a real URL still runs against the deterministic fixture, so
// an operator can never turn outbound traffic on by accident.
func TestSyncFixturePinOverridesTheRealClient(t *testing.T) {
	reached := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"servers":[{"name":"live-only","version":"9.9.9"}]}`))
	}))
	defer srv.Close()

	reg := New()
	svc := NewSourceService(reg, nil, WithFixturePin(true))
	src, err := svc.UpsertSource(&RegistrySource{
		Namespace: DefaultNamespace, Kind: SourceKindMCP, Name: "Pinned",
		URL: srv.URL, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := svc.Sync(context.Background(), src.ID)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != SyncStatusOK || res.Imported != 3 {
		t.Fatalf("pinned sync = %+v, want the fixture's three records", res)
	}
	if reached {
		t.Fatal("the upstream server was contacted while the fixture pin is on")
	}
	// The same source without the pin does reach the network.
	live := NewSourceService(New(), nil)
	liveSrc, err := live.UpsertSource(&RegistrySource{
		Namespace: DefaultNamespace, Kind: SourceKindMCP, Name: "Live",
		URL: srv.URL, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := live.Sync(context.Background(), liveSrc.ID); err != nil {
		t.Fatal(err)
	}
	if !reached {
		t.Fatal("the upstream server was never contacted without the pin")
	}
}

func TestSyncDisabledAndMissingSources(t *testing.T) {
	reg := New()
	svc := NewSourceService(reg, nil)
	src, _ := svc.UpsertSource(&RegistrySource{
		Namespace: DefaultNamespace, Kind: SourceKindMCP, Name: "Off",
		URL: "https://registry.test/x", Fixture: true, Enabled: false,
	})
	if _, err := svc.Sync(context.Background(), src.ID); code(t, err) != CodeInvalidSource {
		t.Fatalf("disabled source = %v", err)
	}
	if _, err := svc.Sync(context.Background(), "missing"); code(t, err) != CodeNotFound {
		t.Fatalf("missing source = %v", err)
	}
	if _, err := svc.UpsertSource(&RegistrySource{Kind: "bogus", Name: "x", URL: "https://x.test"}); code(t, err) != CodeInvalidSource {
		t.Fatalf("bad kind = %v", err)
	}
	if _, err := svc.UpsertSource(&RegistrySource{Kind: SourceKindMCP, Name: "x", URL: "not-a-url"}); code(t, err) != CodeInvalidSource {
		t.Fatalf("bad url = %v", err)
	}
	if _, err := svc.UpsertSource(&RegistrySource{
		Kind: SourceKindMCP, Name: "x", URL: "https://x.test", AuthScheme: AuthSchemeBearer,
	}); code(t, err) != CodeInvalidSource {
		t.Fatalf("bearer without env = %v", err)
	}
}

func TestSyncOverHTTP(t *testing.T) {
	var seenAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"servers":[
			{"name":"remote-mcp","description":"from upstream","version":"3.0.0","url":"https://remote.test/mcp","tags":["remote"]},
			{"name":"","version":"1.0.0"}
		]}`))
	}))
	defer srv.Close()

	t.Setenv("AGENTHUB_TEST_UPSTREAM_TOKEN", "secret-token")
	reg := New()
	svc := NewSourceService(reg, nil)
	src, err := svc.UpsertSource(&RegistrySource{
		Namespace: DefaultNamespace, Kind: SourceKindMCP, Name: "Live",
		URL: srv.URL, AuthScheme: AuthSchemeBearer, AuthTokenEnv: "AGENTHUB_TEST_UPSTREAM_TOKEN", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := svc.Sync(context.Background(), src.ID)
	if err != nil {
		t.Fatal(err)
	}
	// The nameless record never becomes a resource: the client drops it before
	// it can reach the catalogue.
	if res.Imported != 1 || res.Total != 1 {
		t.Fatalf("sync = %+v", res)
	}
	if e := entryFromRecord(SourceKindMCP, map[string]any{"version": "1.0.0"}); e != nil {
		t.Fatalf("nameless record = %+v", e)
	}
	if seenAuth != "Bearer secret-token" {
		t.Fatalf("auth header = %q", seenAuth)
	}
	e, ok := reg.FindByKey(DefaultNamespace, "remote-mcp", "3.0.0")
	if !ok {
		t.Fatal("record was not imported")
	}
	if e.Description != "from upstream" || e.Endpoint != "https://remote.test/mcp" {
		t.Fatalf("entry = %+v", e)
	}
}

func TestSyncHTTPReportsFailures(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	reg := New()
	svc := NewSourceService(reg, nil)
	src, _ := svc.UpsertSource(&RegistrySource{
		Namespace: DefaultNamespace, Kind: SourceKindMCP, Name: "Broken", URL: srv.URL, Enabled: true,
	})
	res, err := svc.Sync(context.Background(), src.ID)
	if err == nil {
		t.Fatal("a failing upstream must not look like success")
	}
	if res.Status != SyncStatusFailed || res.Error == "" {
		t.Fatalf("result = %+v", res)
	}
	got, _ := svc.GetSource(src.ID)
	if got.LastSyncStatus != SyncStatusFailed {
		t.Fatalf("source = %+v", got)
	}

	// A missing credential fails the run instead of importing nothing silently.
	os.Unsetenv("AGENTHUB_TEST_MISSING_TOKEN")
	noCred, _ := svc.UpsertSource(&RegistrySource{
		Namespace: DefaultNamespace, Kind: SourceKindMCP, Name: "NoCred", URL: srv.URL,
		AuthScheme: AuthSchemeBearer, AuthTokenEnv: "AGENTHUB_TEST_MISSING_TOKEN", Enabled: true,
	})
	if _, err := svc.Sync(context.Background(), noCred.ID); code(t, err) != CodeUpstreamAuthMissing {
		t.Fatalf("missing credential = %v", err)
	}
}

func TestDecodeRecordsShapes(t *testing.T) {
	if got, err := decodeRecords([]byte(`[{"name":"a"},{"name":"b"}]`)); err != nil || len(got) != 2 {
		t.Fatalf("bare array = %d, %v", len(got), err)
	}
	if got, err := decodeRecords([]byte(`{"items":[{"name":"a"}]}`)); err != nil || len(got) != 1 {
		t.Fatalf("items = %d, %v", len(got), err)
	}
	if got, err := decodeRecords([]byte(`{"name":"single"}`)); err != nil || len(got) != 1 {
		t.Fatalf("single object = %d, %v", len(got), err)
	}
	if _, err := decodeRecords([]byte(`not json`)); code(t, err) != CodeUpstreamMalformed {
		t.Fatalf("malformed = %v", err)
	}
	if _, err := decodeRecords([]byte(`42`)); code(t, err) != CodeUpstreamMalformed {
		t.Fatalf("scalar = %v", err)
	}
}

func TestFieldOfNamesTheOffendingInput(t *testing.T) {
	err := ValidateKind(&Entry{Kind: KindCLI, Name: "x", KindConfig: &KindConfig{CLI: &CLIConfig{}}})
	if code(t, err) != CodeInvalidKindConfig {
		t.Fatalf("code = %v", err)
	}
	if got := FieldOf(err); got != "command" {
		t.Fatalf("field = %q, want command", got)
	}
	if got := FieldOf(&httputil.Error{Code: CodeNotFound}); got != "" {
		t.Fatalf("plain error field = %q", got)
	}
}
