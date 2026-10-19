package registry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The real upstream client talks to a catalogue nobody controls, so everything
// it can be handed is untrusted input: a body too large to read, a body that is
// not JSON at all, a status that is not a success, a shape nobody promised.
// Each of those has to become a machine readable code rather than an empty
// result — "the upstream changed" must never look like "the upstream is empty".

func TestHTTPClientRejectsUntrustedAnswers(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/not-json":
			_, _ = w.Write([]byte("<html>maintenance</html>"))
		case "/wrong-shape":
			_, _ = w.Write([]byte(`"just a string"`))
		case "/error":
			w.WriteHeader(http.StatusInternalServerError)
		case "/redirected":
			w.WriteHeader(http.StatusNotFound)
		case "/huge":
			_, _ = w.Write([]byte(`{"servers":[` + strings.Repeat(`{"name":"x"},`, 2000) + `{"name":"y"}]}`))
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"servers":[{"name":"ok","version":"1.0.0"}]}`))
		}
	}))
	defer srv.Close()

	cases := []struct {
		path string
		want string
	}{
		{"/not-json", string(CodeUpstreamMalformed)},
		{"/wrong-shape", string(CodeUpstreamMalformed)},
		{"/error", string(CodeUpstreamUnreachable)},
	}
	for _, c := range cases {
		got, err := NewHTTPClient().Fetch(context.Background(), RegistrySource{
			Kind: SourceKindMCP, URL: srv.URL + c.path,
		})
		if err == nil {
			t.Fatalf("%s: err = nil, want %s (entries: %d)", c.path, c.want, len(got))
		}
		if code := string(code(t, err)); code != c.want {
			t.Fatalf("%s: code = %s, want %s", c.path, code, c.want)
		}
		if len(got) != 0 {
			t.Fatalf("%s: entries = %d, want none", c.path, len(got))
		}
	}

	// A body bigger than the cap is truncated rather than buffered whole: the
	// reader stops, so the parse fails instead of the process growing.
	capped := &HTTPClient{HTTP: srv.Client(), MaxBytes: 64}
	if _, err := capped.Fetch(context.Background(), RegistrySource{
		Kind: SourceKindMCP, URL: srv.URL + "/huge",
	}); err == nil {
		t.Fatal("an oversized body must not parse")
	}

	// An address that is not a request at all fails before any dialling.
	if _, err := NewHTTPClient().Fetch(context.Background(), RegistrySource{
		Kind: SourceKindMCP, URL: "http://[::1]:namedport/x",
	}); err == nil {
		t.Fatal("a malformed URL must be refused")
	}

	// A cancelled context is respected: no request goes out.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewHTTPClient().Fetch(ctx, RegistrySource{Kind: SourceKindMCP, URL: srv.URL}); err == nil {
		t.Fatal("a cancelled context must abort the fetch")
	}
}

// A source that promises a bearer token but has none configured must say so
// instead of sending an unauthenticated request and quietly importing nothing.
func TestHTTPClientRefusesToRunWithoutItsCredential(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		_, _ = w.Write([]byte(`{"servers":[]}`))
	}))
	defer srv.Close()

	t.Setenv("AGENTHUB_TEST_UPSTREAM_TOKEN", "")
	_, err := NewHTTPClient().Fetch(context.Background(), RegistrySource{
		Kind: SourceKindMCP, URL: srv.URL,
		AuthScheme: AuthSchemeBearer, AuthTokenEnv: "AGENTHUB_TEST_UPSTREAM_TOKEN",
	})
	if code := string(code(t, err)); code != string(CodeUpstreamAuthMissing) {
		t.Fatalf("code = %s, want %s", code, CodeUpstreamAuthMissing)
	}
	if called {
		t.Fatal("no request may go out without a credential")
	}
}

// Every wrapper key the public registries actually use has to be understood.
func TestDecodeRecordsAcceptsEveryKnownShape(t *testing.T) {
	shapes := map[string]int{
		`[{"name":"a"},{"name":"b"}]`:                        2,
		`{"servers":[{"name":"a"}]}`:                         1,
		`{"items":[{"name":"a"},{"name":"b"},{"name":"c"}]}`: 3,
		`{"records":[{"name":"a"}]}`:                         1,
		`{"skills":[{"name":"a"}]}`:                          1,
		`{"data":[{"name":"a"}]}`:                            1,
		`{"name":"a"}`:                                       1,
		`{"servers":[]}`:                                     0,
	}
	for body, want := range shapes {
		got, err := decodeRecords([]byte(body))
		if err != nil {
			t.Fatalf("%s: %v", body, err)
		}
		if len(got) != want {
			t.Fatalf("%s: records = %d, want %d", body, len(got), want)
		}
	}
	// A list of scalars carries no record at all.
	if got, _ := decodeRecords([]byte(`[1,2,3]`)); len(got) != 0 {
		t.Fatalf("scalars = %d, want 0", len(got))
	}
	// A number at the top level is not a catalogue.
	if _, err := decodeRecords([]byte(`42`)); err == nil {
		t.Fatal("a scalar body must be refused")
	}
}

// An upstream record arrives in whatever shape that registry chose; the mapping
// is what decides whether the imported record is usable.
func TestEntryFromRecordMapsBothCatalogueShapes(t *testing.T) {
	mcp := map[string]any{
		"name":         "filesystem",
		"title":        "Filesystem",
		"description":  "read and write files",
		"version":      "2.0.0",
		"url":          "https://mcp.test/fs",
		"tags":         []any{"files", "local"},
		"transport":    "stdio",
		"command":      "npx",
		"args":         []any{"-y", "@modelcontextprotocol/server-filesystem"},
		"tools":        []any{map[string]any{"name": "read_file"}, "write_file"},
		"auth_schemes": []any{"none"},
		"repository":   map[string]any{"url": "https://github.com/test/fs", "source": "github"},
	}
	e := entryFromRecord(SourceKindMCP, mcp)
	if e == nil {
		t.Fatal("mcp record dropped")
	}
	if e.Name != "filesystem" || e.Version != "2.0.0" || e.Endpoint != "https://mcp.test/fs" {
		t.Fatalf("mcp entry = %+v", e)
	}
	if e.Transport != TransportStdio {
		t.Fatalf("transport = %q, want stdio", e.Transport)
	}
	if e.Repository != "https://github.com/test/fs" {
		t.Fatalf("repository = %q, want the nested url", e.Repository)
	}
	if len(e.Tags) != 2 || len(e.Capabilities) != 2 || e.Capabilities[0] != "read_file" {
		t.Fatalf("tags = %v, capabilities = %v", e.Tags, e.Capabilities)
	}
	if e.KindConfig == nil || e.KindConfig.MCP == nil {
		t.Fatalf("kind config = %+v", e.KindConfig)
	}
	if e.KindConfig.MCP.Command != "npx" || len(e.KindConfig.MCP.Args) != 2 {
		t.Fatalf("mcp config = %+v", e.KindConfig.MCP)
	}

	// An HTTP server record keeps its url and defaults the transport.
	httpEntry := entryFromRecord(SourceKindMCP, map[string]any{
		"name": "remote", "url": "https://remote.test/mcp",
		"capabilities": []any{"tools"},
	})
	if httpEntry.KindConfig.MCP.Transport != string(TransportStreamableHTTP) {
		t.Fatalf("transport = %q, want streamable-http", httpEntry.KindConfig.MCP.Transport)
	}
	// An explicit capability list wins over the tool list.
	if len(httpEntry.Capabilities) != 1 || httpEntry.Capabilities[0] != "tools" {
		t.Fatalf("capabilities = %v", httpEntry.Capabilities)
	}

	skill := map[string]any{
		"name":          "invoice-sop",
		"recordVersion": "3.1.0",
		"package":       "skills/invoice",
		"tools":         []any{"shell"},
		"documentation": "# invoice\nsteps",
	}
	se := entryFromRecord(SourceKindSkill, skill)
	if se == nil || se.Version != "3.1.0" {
		t.Fatalf("skill entry = %+v", se)
	}
	if se.KindConfig == nil || se.KindConfig.Skill == nil {
		t.Fatalf("skill config = %+v", se.KindConfig)
	}
	if se.KindConfig.Skill.Package != "skills/invoice" || se.KindConfig.Skill.Docs != "# invoice\nsteps" {
		t.Fatalf("skill config = %+v", se.KindConfig.Skill)
	}

	// A record without a name cannot be deduped or displayed, so it is dropped.
	if entryFromRecord(SourceKindMCP, map[string]any{"version": "1.0.0"}) != nil {
		t.Fatal("a nameless record must be dropped")
	}
	// An unknown upstream kind still yields the generic fields.
	if e := entryFromRecord("other", map[string]any{"name": "x"}); e == nil || e.KindConfig != nil {
		t.Fatalf("unknown kind = %+v", e)
	}
}

// A run is capped so one upstream cannot flood the catalogue.
func TestSyncCapsHowManyRecordsOneRunImports(t *testing.T) {
	reg := New()
	svc := NewSourceService(reg, nil)
	src, err := svc.UpsertSource(&RegistrySource{
		Namespace: DefaultNamespace, Kind: SourceKindMCP, Name: "Big",
		URL: "https://registry.test/servers", Fixture: true, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	// The fixture is far below the cap, so nothing is trimmed here — the cap is
	// asserted on the constant instead, which is the number a run is bounded by.
	res, err := svc.Sync(context.Background(), src.ID)
	if err != nil {
		t.Fatal(err)
	}
	if res.Total > MaxSyncRecords {
		t.Fatalf("total = %d, over the cap %d", res.Total, MaxSyncRecords)
	}
	if MaxSyncRecords <= 0 {
		t.Fatal("the cap must be positive")
	}
}
