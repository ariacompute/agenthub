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
	d := testDeps(t, openConfig(dir), entry)
	h := Register(d)
	// A finished task: its SSE stream replays the terminal event and closes at
	// once, where a live one would block the probe until something happens.
	finished := runOnce(t, h, entry.ID)

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
		"GET /api/*":                  {http.MethodGet, "/api/resources", ""},
		"GET /healthz":                {http.MethodGet, "/healthz", ""},
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
