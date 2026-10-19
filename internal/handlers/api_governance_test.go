package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agenthub/internal/registry"
)

// The governance endpoints decide what everybody may find, so their tests are
// about the guardrails: an unapproved record is not discoverable, an illegal
// transition is refused rather than ignored, and a source that cannot be reached
// reports that instead of importing nothing quietly.

func governanceDeps(t *testing.T) Deps {
	t.Helper()
	d := testDeps(t, governanceConfig())
	d.Sources = registry.NewSourceService(d.Reg, nil)
	return d
}

func governanceServer(t *testing.T) *httptest.Server {
	t.Helper()
	d := governanceDeps(t)
	srv := httptest.NewServer(Register(d))
	t.Cleanup(srv.Close)
	return srv
}

func doJSON(t *testing.T, method, url, body string) (*http.Response, map[string]any) {
	t.Helper()
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer func() { _ = res.Body.Close() }()
	out := map[string]any{}
	_ = json.NewDecoder(res.Body).Decode(&out)
	return res, out
}

func TestNamespacesAreListedAndCreated(t *testing.T) {
	srv := governanceServer(t)
	res, body := doJSON(t, http.MethodGet, srv.URL+"/api/registries", "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}
	list, _ := body["registries"].([]any)
	if len(list) != 1 {
		t.Fatalf("registries = %v, want the default namespace", list)
	}

	res, body = doJSON(t, http.MethodPost, srv.URL+"/api/registries",
		`{"id":"curated","name":"Curated","auto_approve":false}`)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d, body = %v", res.StatusCode, body)
	}
	if body["id"] != "curated" || body["auto_approve"] != false {
		t.Fatalf("created = %v", body)
	}

	// A namespace id that is not a slug is refused before it can reach a URL.
	res, _ = doJSON(t, http.MethodPost, srv.URL+"/api/registries", `{"id":"Not A Slug"}`)
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad slug status = %d", res.StatusCode)
	}
	// The default namespace is the fallback for every record without one.
	res, _ = doJSON(t, http.MethodDelete, srv.URL+"/api/registries/default", "")
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("delete default status = %d", res.StatusCode)
	}
}

func TestApprovalMakesARecordDiscoverable(t *testing.T) {
	srv := governanceServer(t)
	// A curated namespace starts records as drafts, so nothing is discoverable
	// until somebody approves it.
	_, _ = doJSON(t, http.MethodPost, srv.URL+"/api/registries",
		`{"id":"curated","name":"Curated","auto_approve":false}`)
	res, body := doJSON(t, http.MethodPost, srv.URL+"/api/resources",
		`{"kind":"mcp","name":"hr","version":"1.0.0","namespace":"curated","endpoint":"https://hr.test"}`)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("register = %d, %v", res.StatusCode, body)
	}
	id, _ := body["id"].(string)
	if body["status"] != "draft" {
		t.Fatalf("status = %v, want draft", body["status"])
	}

	// The MCP export is the discoverable catalogue: a draft is not in it.
	res, body = doJSON(t, http.MethodGet, srv.URL+"/api/export/mcp", "")
	servers, _ := body["servers"].([]any)
	for _, raw := range servers {
		m, _ := raw.(map[string]any)
		if m["name"] == "hr" {
			t.Fatalf("a draft leaked into the export: %v", m)
		}
	}

	// draft -> approved is not an edge; draft -> pending -> approved is.
	res, _ = doJSON(t, http.MethodPost, srv.URL+"/api/resources/"+id+"/status", `{"status":"approved"}`)
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("draft -> approved status = %d", res.StatusCode)
	}
	res, body = doJSON(t, http.MethodPost, srv.URL+"/api/resources/"+id+"/status",
		`{"status":"pending","reason":"ready for review"}`)
	if res.StatusCode != http.StatusOK || body["status"] != "pending" {
		t.Fatalf("submit = %d, %v", res.StatusCode, body)
	}
	res, body = doJSON(t, http.MethodPost, srv.URL+"/api/resources/"+id+"/status", `{"status":"approved"}`)
	if res.StatusCode != http.StatusOK || body["status"] != "approved" {
		t.Fatalf("approve = %d, %v", res.StatusCode, body)
	}

	res, body = doJSON(t, http.MethodGet, srv.URL+"/api/export/mcp", "")
	servers, _ = body["servers"].([]any)
	found := false
	for _, raw := range servers {
		m, _ := raw.(map[string]any)
		if m["name"] == "hr" {
			found = true
		}
	}
	if !found {
		t.Fatalf("approved record missing from the export: %v", servers)
	}

	// An unknown state is a bad request, not a silent no-op.
	res, _ = doJSON(t, http.MethodPost, srv.URL+"/api/resources/"+id+"/status", `{"status":"whatever"}`)
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown status = %d", res.StatusCode)
	}
	res, _ = doJSON(t, http.MethodPost, srv.URL+"/api/resources/does-not-exist/status", `{"status":"approved"}`)
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("missing record status = %d", res.StatusCode)
	}
}

func TestApprovalQueue(t *testing.T) {
	srv := governanceServer(t)
	_, _ = doJSON(t, http.MethodPost, srv.URL+"/api/registries",
		`{"id":"curated","name":"Curated","auto_approve":false}`)
	_, body := doJSON(t, http.MethodPost, srv.URL+"/api/resources",
		`{"kind":"skill","name":"sop","version":"1.0.0","namespace":"curated"}`)
	id, _ := body["id"].(string)
	_, _ = doJSON(t, http.MethodPost, srv.URL+"/api/resources/"+id+"/status", `{"status":"pending"}`)

	res, body := doJSON(t, http.MethodGet, srv.URL+"/api/registries/approvals", "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}
	records, _ := body["records"].([]any)
	if len(records) != 1 {
		t.Fatalf("queue = %v, want the one pending record", records)
	}
}

func TestRegistrySourcesAndSync(t *testing.T) {
	srv := governanceServer(t)
	res, body := doJSON(t, http.MethodPost, srv.URL+"/api/registry-sources",
		`{"kind":"mcp","name":"MCP Registry","url":"https://registry.test/servers","fixture":true,"enabled":true}`)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create = %d, %v", res.StatusCode, body)
	}
	id, _ := body["id"].(string)
	// A source that never ran carries no sync outcome at all: an omitted field
	// is the honest answer, an empty "ok" would be a claim nobody made.
	if _, ok := body["last_sync_status"]; ok {
		t.Fatalf("a fresh source must not claim a sync: %v", body)
	}

	res, body = doJSON(t, http.MethodPost, srv.URL+"/api/registry-sources/"+id+"/sync", "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("sync = %d, %v", res.StatusCode, body)
	}
	if body["status"] != registry.SyncStatusOK {
		t.Fatalf("result = %v", body)
	}
	if got := body["imported"]; got != float64(3) {
		t.Fatalf("imported = %v, want 3", got)
	}

	// The imported records are in the catalogue and traceable to the source.
	res, body = doJSON(t, http.MethodGet, srv.URL+"/api/resources?kind=mcp", "")
	list, _ := body["resources"].([]any)
	if len(list) != 3 {
		t.Fatalf("resources = %d, want 3", len(list))
	}

	// Re-running the same source updates instead of duplicating.
	_, body = doJSON(t, http.MethodPost, srv.URL+"/api/registry-sources/"+id+"/sync", "")
	if body["imported"] != float64(0) || body["updated"] != float64(3) {
		t.Fatalf("second sync = %v", body)
	}

	// A source that cannot be reached is an error, not an empty success. The
	// address is a closed loopback port so the test never touches the network.
	_, body = doJSON(t, http.MethodPost, srv.URL+"/api/registry-sources",
		`{"kind":"skill","name":"Broken","url":"http://127.0.0.1:1/index.json","enabled":true}`)
	brokenID, _ := body["id"].(string)
	res, _ = doJSON(t, http.MethodPost, srv.URL+"/api/registry-sources/"+brokenID+"/sync", "")
	if res.StatusCode != http.StatusBadGateway {
		t.Fatalf("broken sync status = %d", res.StatusCode)
	}

	// A malformed source never becomes one.
	res, _ = doJSON(t, http.MethodPost, srv.URL+"/api/registry-sources",
		`{"kind":"bogus","name":"x","url":"https://x.test"}`)
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad kind status = %d", res.StatusCode)
	}
	res, _ = doJSON(t, http.MethodPost, srv.URL+"/api/registry-sources",
		`{"kind":"mcp","name":"x","url":"not-a-url"}`)
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad url status = %d", res.StatusCode)
	}

	res, body = doJSON(t, http.MethodGet, srv.URL+"/api/registry-sources", "")
	sources, _ := body["sources"].([]any)
	if len(sources) != 2 {
		t.Fatalf("sources = %d, want 2", len(sources))
	}
	res, _ = doJSON(t, http.MethodDelete, srv.URL+"/api/registry-sources/"+brokenID, "")
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("delete status = %d", res.StatusCode)
	}
}

func TestResourceListingFiltersByNamespaceAndStatus(t *testing.T) {
	srv := governanceServer(t)
	_, _ = doJSON(t, http.MethodPost, srv.URL+"/api/registries",
		`{"id":"curated","name":"Curated","auto_approve":false}`)
	_, _ = doJSON(t, http.MethodPost, srv.URL+"/api/resources",
		`{"kind":"mcp","name":"curated-mcp","version":"1.0.0","namespace":"curated"}`)
	_, _ = doJSON(t, http.MethodPost, srv.URL+"/api/resources",
		`{"kind":"mcp","name":"open-mcp","version":"1.0.0"}`)

	_, body := doJSON(t, http.MethodGet, srv.URL+"/api/resources?namespace=curated", "")
	list, _ := body["resources"].([]any)
	if len(list) != 1 {
		t.Fatalf("curated = %d, want 1", len(list))
	}
	_, body = doJSON(t, http.MethodGet, srv.URL+"/api/resources?status=draft", "")
	list, _ = body["resources"].([]any)
	if len(list) != 1 {
		t.Fatalf("drafts = %d, want 1", len(list))
	}
	// An invalid filter value is a bad request rather than an empty listing.
	res, _ := doJSON(t, http.MethodGet, srv.URL+"/api/resources?status=bogus", "")
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad status filter = %d", res.StatusCode)
	}
}
