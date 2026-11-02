package handlers

// The refusals around one resource: an id that names nothing, a health value the
// registry has no state for, a visibility that would grant more than the caller
// asked, an action a connector does not declare.
//
// These are the answers an operator sees when a catalogue entry is deleted in
// another tab, when a probe is fired at a record that was never published, or
// when a console build is older than the server. Each is a 4xx with a code; a
// 200 carrying a stale record, or a 500, would both be wrong.

import (
	"net/http"
	"testing"

	"agenthub/internal/registry"
)

// mcpEntry is a healthy, published MCP record: enough for a route to reach its
// handler instead of being refused by the visibility filter.
func mcpEntry() *registry.Entry {
	return &registry.Entry{
		ID:         "res-mcp",
		Kind:       registry.KindMCP,
		Name:       "files",
		Endpoint:   "stdio://files",
		Health:     registry.HealthHealthy,
		Visibility: registry.VisibilityPublic,
	}
}

// TestAnUnknownResourceIdIsANotFound covers every route that takes a resource
// id. They must agree, because the console decides between "deleted" and
// "forbidden" from the status alone.
func TestAnUnknownResourceIdIsANotFound(t *testing.T) {
	cases := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{"read", http.MethodGet, "/api/resources/nope", ""},
		{"update", http.MethodPut, "/api/resources/nope", `{"kind":"mcp","name":"x","endpoint":"stdio://x"}`},
		{"delete", http.MethodDelete, "/api/resources/nope", ""},
		{"health", http.MethodPost, "/api/resources/nope/health", `{"health":"healthy"}`},
		{"probe", http.MethodPost, "/api/resources/nope/probe", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := Register(testDeps(t, openConfig("")))
			w := do(t, h, tc.method, tc.path, tc.body, nil)
			if w.Code != http.StatusNotFound {
				t.Fatalf("%s %s: status = %d, want 404 (body %s)", tc.method, tc.path, w.Code, w.Body.String())
			}
			if code := errCode(t, w); code != "not_found" {
				t.Fatalf("code = %q, want not_found", code)
			}
		})
	}
}

// TestResourceRefusesValuesTheRegistryCannotHonour keeps the write side honest:
// a draft cannot be published by a caller who did not say so, and a health state
// the registry does not have is a client bug rather than a server error.
func TestResourceRefusesValuesTheRegistryCannotHonour(t *testing.T) {
	h := Register(testDeps(t, openConfig(""), mcpEntry()))

	// A health value outside the enum: the console has a colour for each state,
	// so an unknown one must not be stored and then rendered blank.
	w := do(t, h, http.MethodPost, "/api/resources/res-mcp/health", `{"health":"glowing"}`, nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("health: status = %d, want 400 (body %s)", w.Code, w.Body.String())
	}

	// A visibility the registry cannot parse would leave the record in whatever
	// state the caller asked for, which is how a private entry becomes public.
	w = do(t, h, http.MethodPut, "/api/resources/res-mcp",
		`{"kind":"mcp","name":"files","endpoint":"stdio://files","visibility":"everyone"}`, nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("visibility: status = %d, want 400 (body %s)", w.Code, w.Body.String())
	}
	var body struct {
		Code string `json:"code"`
	}
	decodeJSON(t, w, &body)
	if body.Code != string(registry.CodeInvalidVisibility) {
		t.Fatalf("code = %q, want %q", body.Code, registry.CodeInvalidVisibility)
	}
}

// TestResourceUpdateCannotChangeItsIdentity pins that the path wins over the
// body: an update that names another record must not move the id, which would
// be an upsert wearing an update's clothes.
func TestResourceUpdateCannotChangeItsIdentity(t *testing.T) {
	h := Register(testDeps(t, openConfig(""), mcpEntry()))

	w := do(t, h, http.MethodPut, "/api/resources/res-mcp",
		`{"id":"someone-else","kind":"mcp","name":"files","endpoint":"stdio://files","health":"healthy"}`, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("update: status = %d, body = %s", w.Code, w.Body.String())
	}
	var stored registry.Entry
	decodeJSON(t, w, &stored)
	if stored.ID != "res-mcp" {
		t.Fatalf("id = %q, want res-mcp", stored.ID)
	}
}

// TestConnectorActionsRefuseUnknownIds covers the two lookups the connector
// screen makes: the declared action list and the single action invocation.
func TestConnectorActionsRefuseUnknownIds(t *testing.T) {
	h := Register(testDeps(t, openConfig(""), mcpEntry()))

	w := do(t, h, http.MethodGet, "/api/connectors/nope/actions", "", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("list actions: status = %d, want 404 (body %s)", w.Code, w.Body.String())
	}

	// A known connector with an action it does not declare is refused by the
	// connector, not by the route: the answer comes from the adapter.
	w = do(t, h, http.MethodPost, "/api/connectors/res-mcp/actions",
		`{"action":"teleport","input":{}}`, nil)
	if w.Code < http.StatusBadRequest {
		t.Fatalf("unknown action: status = %d, want an error (body %s)", w.Code, w.Body.String())
	}
}
