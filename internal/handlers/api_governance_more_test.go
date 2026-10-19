package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"agenthub/internal/authz"
	"agenthub/internal/config"
	"agenthub/internal/registry"
)

// The second half of the governance surface: the edit and delete paths, the
// filters a listing may be asked for, and the gates. Each new permission is a
// gate of its own, and none of them is implied by another.

func TestNamespaceCanBeRenamedAndRemoved(t *testing.T) {
	srv := governanceServer(t)
	_, body := doJSON(t, http.MethodPost, srv.URL+"/api/registries",
		`{"id":"curated","name":"Curated","description":"needs a look","auto_approve":false}`)
	id, _ := body["id"].(string)

	// A rename keeps the id: every record and URL that points at it stays valid.
	res, body := doJSON(t, http.MethodPut, srv.URL+"/api/registries/"+id,
		`{"name":"Reviewed","description":"checked weekly","auto_approve":true}`)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("update = %d, %v", res.StatusCode, body)
	}
	if body["id"] != id || body["name"] != "Reviewed" || body["auto_approve"] != true {
		t.Fatalf("updated = %v", body)
	}

	// A body that is not JSON is a bad request, never a panic.
	res, _ = doJSON(t, http.MethodPut, srv.URL+"/api/registries/"+id, `{"name":`)
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("malformed update = %d, want 400", res.StatusCode)
	}

	// Deleting an unknown namespace is a 404, not a silent success.
	res, _ = doJSON(t, http.MethodDelete, srv.URL+"/api/registries/never-existed", "")
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("delete unknown = %d, want 404", res.StatusCode)
	}
	res, _ = doJSON(t, http.MethodDelete, srv.URL+"/api/registries/"+id, "")
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("delete = %d, want 204", res.StatusCode)
	}
}

func TestSourceCanBeEditedAndRemoved(t *testing.T) {
	srv := governanceServer(t)
	_, body := doJSON(t, http.MethodPost, srv.URL+"/api/registry-sources",
		`{"kind":"mcp","name":"MCP","url":"https://registry.test/servers","fixture":true,"enabled":true}`)
	id, _ := body["id"].(string)

	// Editing by id may not move the record to another id.
	res, body := doJSON(t, http.MethodPut, srv.URL+"/api/registry-sources/"+id,
		`{"kind":"mcp","name":"Renamed","url":"https://registry.test/v2","enabled":false,"schedule":"0 3 * * *"}`)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("update = %d, %v", res.StatusCode, body)
	}
	if body["id"] != id || body["name"] != "Renamed" || body["enabled"] != false {
		t.Fatalf("updated = %v", body)
	}

	// An edit that violates the source rules is refused with the field's code.
	res, body = doJSON(t, http.MethodPut, srv.URL+"/api/registry-sources/"+id,
		`{"kind":"mcp","name":"Renamed","url":"not-a-url"}`)
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad update = %d, %v", res.StatusCode, body)
	}

	res, _ = doJSON(t, http.MethodDelete, srv.URL+"/api/registry-sources/never-existed", "")
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("delete unknown = %d, want 404", res.StatusCode)
	}
	res, _ = doJSON(t, http.MethodDelete, srv.URL+"/api/registry-sources/"+id, "")
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("delete = %d, want 204", res.StatusCode)
	}

	// Nothing is left, and the listing says so plainly.
	res, body = doJSON(t, http.MethodGet, srv.URL+"/api/registry-sources", "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("list = %d", res.StatusCode)
	}
	sources, _ := body["sources"].([]any)
	if len(sources) != 0 {
		t.Fatalf("sources = %d, want 0", len(sources))
	}
}

// An instance without a synchronisation service is a supported deployment, so
// every source route answers "not configured" rather than crashing.
func TestSourceRoutesWithoutAServiceReportNotConfigured(t *testing.T) {
	d := testDeps(t, governanceConfig())
	srv := httptest.NewServer(Register(d))
	t.Cleanup(srv.Close)

	probes := []struct{ method, path, body string }{
		{http.MethodGet, "/api/registry-sources", ""},
		{http.MethodPost, "/api/registry-sources", `{"kind":"mcp","name":"x","url":"https://x.test"}`},
		{http.MethodPut, "/api/registry-sources/src-x", `{"kind":"mcp","name":"x","url":"https://x.test"}`},
		{http.MethodDelete, "/api/registry-sources/src-x", ""},
		{http.MethodPost, "/api/registry-sources/src-x/sync", ""},
	}
	for _, p := range probes {
		res, _ := doJSON(t, p.method, srv.URL+p.path, p.body)
		if res.StatusCode != http.StatusNotFound {
			t.Fatalf("%s %s = %d, want 404", p.method, p.path, res.StatusCode)
		}
	}
}

// The listing filters are how the console separates one catalogue from another,
// so a filter that cannot be honoured is an error rather than an empty list.
func TestListingFiltersAreValidated(t *testing.T) {
	srv := governanceServer(t)
	_, _ = doJSON(t, http.MethodPost, srv.URL+"/api/resources", `{"kind":"skill","name":"sop","version":"1.0.0"}`)

	for _, q := range []string{"kind=nope", "status=whatever", "namespace=Not A Slug"} {
		res, body := doJSON(t, http.MethodGet, srv.URL+"/api/resources?"+q, "")
		if res.StatusCode != http.StatusBadRequest {
			t.Fatalf("?%s = %d, %v", q, res.StatusCode, body)
		}
	}
	// A valid namespace with nothing in it is an empty list, not an error.
	res, body := doJSON(t, http.MethodGet, srv.URL+"/api/resources?namespace=empty&status=approved", "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("valid filter = %d, %v", res.StatusCode, body)
	}
	list, _ := body["resources"].([]any)
	if len(list) != 0 {
		t.Fatalf("resources = %d, want 0", len(list))
	}
}

// Each governance permission is its own gate: holding one of them must not
// imply the others, or a reviewer could rewrite the catalogue they review.
func TestGovernancePermissionsAreSeparate(t *testing.T) {
	d, az := authTestDeps(t, "")
	d.Sources = registry.NewSourceService(d.Reg, nil)
	h := Register(d)
	owner := bootstrapOwner(t, h)

	w := do(t, h, http.MethodPost, "/api/users",
		`{"email":"curator@example.com","password":"correct horse","name":"Curator"}`, authHeader(owner))
	var created struct {
		User struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	decodeJSON(t, w, &created)
	curator := sessionOf(t, h, "curator@example.com", "correct horse")

	// Without any permission every governance route is closed.
	probes := []struct{ method, path, body string }{
		{http.MethodGet, "/api/registries", ""},
		{http.MethodPost, "/api/registries", `{"id":"x"}`},
		{http.MethodGet, "/api/registries/approvals", ""},
		{http.MethodPost, "/api/resources/res-x/status", `{"status":"pending"}`},
		{http.MethodGet, "/api/registry-sources", ""},
		{http.MethodPost, "/api/registry-sources", `{"kind":"mcp","name":"x","url":"https://x.test"}`},
	}
	for _, p := range probes {
		w := do(t, h, p.method, p.path, p.body, authHeader(curator))
		if w.Code != http.StatusForbidden {
			t.Fatalf("%s %s: status = %d, want 403", p.method, p.path, w.Code)
		}
	}

	// Curation alone opens the review queue and nothing else.
	grant(t, az, created.User.ID, "role-curator", authz.PermRegistryCurate)
	if w := do(t, h, http.MethodGet, "/api/registries/approvals", "", authHeader(curator)); w.Code != http.StatusOK {
		t.Fatalf("curate: approvals = %d, want 200", w.Code)
	}
	if w := do(t, h, http.MethodPost, "/api/registries", `{"id":"x"}`, authHeader(curator)); w.Code != http.StatusForbidden {
		t.Fatalf("curate must not imply registries:manage: %d", w.Code)
	}
	if w := do(t, h, http.MethodGet, "/api/registry-sources", "", authHeader(curator)); w.Code != http.StatusForbidden {
		t.Fatalf("curate must not imply sources:manage: %d", w.Code)
	}

	// sources:manage opens the source routes and only those.
	grant(t, az, created.User.ID, "role-curator", authz.PermSourcesManage)
	if w := do(t, h, http.MethodGet, "/api/registry-sources", "", authHeader(curator)); w.Code != http.StatusOK {
		t.Fatalf("sources:manage: list = %d, want 200", w.Code)
	}
	if w := do(t, h, http.MethodGet, "/api/registries", "", authHeader(curator)); w.Code != http.StatusForbidden {
		t.Fatalf("sources:manage must not imply registries:manage: %d", w.Code)
	}

	// registries:manage is the last one, and it is still not curation.
	grant(t, az, created.User.ID, "role-curator", authz.PermRegistriesManage)
	if w := do(t, h, http.MethodGet, "/api/registries", "", authHeader(curator)); w.Code != http.StatusOK {
		t.Fatalf("registries:manage: list = %d, want 200", w.Code)
	}
	if w := do(t, h, http.MethodGet, "/api/registries/approvals", "", authHeader(curator)); w.Code != http.StatusForbidden {
		t.Fatalf("registries:manage must not imply registry:curate: %d", w.Code)
	}
}

// The curation endpoint is the only way a record changes state, so it has to
// refuse both a body it cannot read and an edge that does not exist.
func TestCurationEndpointRejectsBadInput(t *testing.T) {
	srv := governanceServer(t)
	_, body := doJSON(t, http.MethodPost, srv.URL+"/api/resources", `{"kind":"mcp","name":"hr","endpoint":"https://hr.test"}`)
	id, _ := body["id"].(string)

	res, _ := doJSON(t, http.MethodPost, srv.URL+"/api/resources/"+id+"/status", `{`)
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("malformed body = %d, want 400", res.StatusCode)
	}
	res, _ = doJSON(t, http.MethodPost, srv.URL+"/api/resources/"+id+"/status", `{"status":"draft"}`)
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("approved -> draft = %d, want 400: that edge does not exist", res.StatusCode)
	}
	// Deprecating is how an approved record is withdrawn.
	res, body = doJSON(t, http.MethodPost, srv.URL+"/api/resources/"+id+"/status", `{"status":"deprecated"}`)
	if res.StatusCode != http.StatusOK || body["status"] != "deprecated" {
		t.Fatalf("deprecate = %d, %v", res.StatusCode, body)
	}
	// A deprecated record leaves the discoverable catalogue.
	res, body = doJSON(t, http.MethodGet, srv.URL+"/api/export/mcp", "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("export = %d", res.StatusCode)
	}
	servers, _ := body["servers"].([]any)
	for _, raw := range servers {
		if m, _ := raw.(map[string]any); m["name"] == "hr" {
			t.Fatal("a deprecated record is still discoverable")
		}
	}
	// And it comes back through pending.
	res, body = doJSON(t, http.MethodPost, srv.URL+"/api/resources/"+id+"/status", `{"status":"pending"}`)
	if res.StatusCode != http.StatusOK || body["status"] != "pending" {
		t.Fatalf("resubmit = %d, %v", res.StatusCode, body)
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// governanceConfig is the configuration the governance handlers run under. It
// lives here so a test can build Deps without the source service attached and
// assert the "not configured" path.
func governanceConfig() *config.Config {
	return &config.Config{
		MaxParallel:    4,
		MaxOutputBytes: 1024,
		EventBuffer:    16,
		PushEnabled:    true,
		LegacyMethods:  true,
	}
}

// bootstrapOwner registers the first account, which becomes the owner, and
// returns its session: an instance with no account has no other way in.
func bootstrapOwner(t *testing.T, h http.Handler) string {
	t.Helper()
	w := do(t, h, http.MethodPost, "/api/auth/register",
		`{"email":"owner@example.com","password":"correct horse","name":"Owner"}`, nil)
	if w.Code != http.StatusOK && w.Code != http.StatusCreated {
		t.Fatalf("register the owner: %d, %s", w.Code, w.Body.String())
	}
	return sessionOf(t, h, "owner@example.com", "correct horse")
}

// grant replaces the permissions one role carries, so a test can prove that
// holding exactly one permission opens exactly one group of routes.
func grant(t *testing.T, az *memAuthz, userID, role string, perms ...string) {
	t.Helper()
	az.mu.Lock()
	defer az.mu.Unlock()
	az.roles[userID] = []string{role}
	az.rolePerm[role] = perms
}
