package handlers

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agenthub/internal/registry"
)

// The endpoint list feeds the startup log and `make ports`, so a listener that
// no longer routes — or a route nobody documents — is a documentation bug these
// cases must catch.
func TestListenersCoverRoutes(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html>console</html>"), 0o644); err != nil {
		t.Fatalf("write index: %v", err)
	}

	entry := &registry.Entry{Kind: registry.KindSkill, Name: "s1"}
	// With identities wired in: the auth and admin routes only exist as routes
	// when there is a user store behind them.
	d, _ := authTestDeps(t, dir, entry)
	h := Register(d)
	// A finished task: its SSE stream replays the terminal event and closes at
	// once, where a live one would block the probe until something happens.
	finished := runOnce(t, h, entry.ID)

	// A connector (kind=mcp) with the "allowed" policy so the connector action
	// probe exercises the full send path instead of a 404.
	mcp := &registry.Entry{
		Kind:       registry.KindMCP,
		Name:       "c1",
		Endpoint:   "http://example.test/core",
		KindConfig: &registry.KindConfig{ConnectorPolicy: registry.ConnectorPolicyAllowed},
	}
	if _, err := d.Reg.Upsert(mcp); err != nil {
		t.Fatalf("seed connector: %v", err)
	}

	// One concrete request per listed endpoint: enough to prove the route is
	// still wired up (404 means it is gone).
	probes := map[string]struct {
		method string
		target string
		body   string
	}{
		"GET /":                       {http.MethodGet, "/", ""},
		"GET /.well-known/agent.json": {http.MethodGet, "/.well-known/agent.json", ""},
		"POST /a2a":                   {http.MethodPost, "/a2a", `{"jsonrpc":"2.0","id":1,"method":"tasks/get","params":{"id":"x"}}`},
		"GET /a2a/stream":             {http.MethodGet, "/a2a/stream?task=" + finished, ""},
		"GET /api/auth/config":        {http.MethodGet, "/api/auth/config", ""},
		"POST /api/auth/login":        {http.MethodPost, "/api/auth/login", `{"email":"a@b.c","password":"x"}`},
		"GET /api/auth/me":            {http.MethodGet, "/api/auth/me", ""},
		"GET /api/*":                  {http.MethodGet, "/api/resources", ""},
		"GET /api/users":              {http.MethodGet, "/api/users", ""},
		"GET /api/roles":              {http.MethodGet, "/api/roles", ""},
		"GET /api/grants":             {http.MethodGet, "/api/grants", ""},
		// Backups answer "disabled" rather than 404 when no service is wired
		// in, which is the state an instance without a backup volume is in.
		"GET /api/backups": {http.MethodGet, "/api/backups", ""},
		// API keys answer 401 without a session: there is no key service to wire
		// into the test harness, so the auth gate itself is what is exercised.
		"GET /api/api-keys": {http.MethodGet, "/api/api-keys", ""},
		"GET /api/channels": {http.MethodGet, "/api/channels", ""},
		// Public webhook with no channel store wired: it answers 503, not 404.
		"POST /api/channels/{id}/webhook": {http.MethodPost, "/api/channels/ch1/webhook", `{"text":"hi","event_id":"e1"}`},
		"POST /api/channels/{id}/deliver": {http.MethodPost, "/api/channels/ch1/deliver", `{"message":"hi"}`},
		"GET /api/connectors":             {http.MethodGet, "/api/connectors", ""},
		// Allowed connector: the probe runs the full send path.
		"POST /api/connectors/{id}/actions": {http.MethodPost, "/api/connectors/" + mcp.ID + "/actions", `{"action":"invoke","args":{}}`},
		"POST /api/threads/{id}/audio":      {http.MethodPost, "/api/threads/" + finished + "/audio", ""},
		// Computers / schedules / knowledge answer 503 without a store wired in, not 404.
		"GET /api/computers":         {http.MethodGet, "/api/computers", ""},
		"POST /api/computers":        {http.MethodPost, "/api/computers", `{"kind":"browser"}`},
		"GET /api/computers/{id}":    {http.MethodGet, "/api/computers/comp1", ""},
		"GET /api/schedules":         {http.MethodGet, "/api/schedules", ""},
		"POST /api/schedules":        {http.MethodPost, "/api/schedules", `{"goal":"demo"}`},
		"GET /api/schedules/{id}":    {http.MethodGet, "/api/schedules/sched1", ""},
		"PUT /api/schedules/{id}":    {http.MethodPut, "/api/schedules/sched1", `{"goal":"demo"}`},
		"DELETE /api/schedules/{id}": {http.MethodDelete, "/api/schedules/sched1", ""},
		"GET /api/knowledge":         {http.MethodGet, "/api/knowledge", ""},
		"GET /healthz":               {http.MethodGet, "/healthz", ""},
	}

	listeners := Listeners()
	if len(listeners) == 0 {
		t.Fatal("Listeners() is empty")
	}

	seen := make(map[string]bool, len(listeners))
	for _, l := range listeners {
		if seen[l.Endpoint] {
			t.Errorf("duplicate endpoint %q", l.Endpoint)
		}
		seen[l.Endpoint] = true

		method, path, ok := strings.Cut(l.Endpoint, " ")
		if !ok || method == "" || !strings.HasPrefix(path, "/") {
			t.Errorf("%q: endpoint must be \"<method> <path>\"", l.Endpoint)
		}
		if strings.TrimSpace(l.Use) == "" {
			t.Errorf("%q: use is empty, nothing to show for the port", l.Endpoint)
		}

		probe, ok := probes[l.Endpoint]
		if !ok {
			t.Errorf("%q: no probe registered, the test cannot see a routing regression here", l.Endpoint)
			continue
		}
		if w := do(t, h, probe.method, probe.target, probe.body, nil); w.Code == http.StatusNotFound {
			t.Errorf("%s %s: status = %d, route is no longer served", probe.method, probe.target, w.Code)
		}
	}

	for endpoint := range probes {
		if !seen[endpoint] {
			t.Errorf("%q is routed but not documented by Listeners()", endpoint)
		}
	}
}

// Every listener must describe itself in one line: the log renders them as
// "<endpoint> - <use>", so neither part may carry a newline.
func TestListenerUsesAreOneLine(t *testing.T) {
	for _, l := range Listeners() {
		if strings.ContainsAny(l.Endpoint, "\n\r") || strings.ContainsAny(l.Use, "\n\r") {
			t.Errorf("%q: lists rendered by the startup log must stay single line", l.Endpoint)
		}
	}
}
