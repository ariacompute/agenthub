// The endpoint table in docs/docs.md and the route registry in
// internal/handlers/handlers.go are written by hand, in different languages, and
// nothing compared them.
//
// The existing guard is web/tests/docs-content.spec.js, and it can only detect a
// deletion from the table: it asserts the table against itself (a count and the
// presence of every descKey), so a route that was registered in Go and never
// added here is invisible to it. That is exactly how `DELETE /api/computers/{id}`
// stayed undocumented while the handler was live.
//
// This test derives the truth from the Go source instead. It reads the
// registrations, normalises them the way the table writes them, and reports the
// difference in both directions of the hand-off:
//
//   - a registered /api route with no row in docs/docs.md;
//   - a docs row that warns about a path the router does not register.
//
// docs/docs.md is generated from web/src/lib/docs-content.js by
// `make docs-md`, so a route added to the JS and not regenerated fails here too,
// with the command in the message.

package tests

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// handlerRegistration matches `mux.HandleFunc("GET /api/x", …)` and the
// method-less form the protocol endpoints use.
var handlerRegistration = regexp.MustCompile(`HandleFunc\(\s*"(?:([A-Z]+) )?(/[^" ]+)"`)

// documentedEndpoint matches a row of the docs table:
// `| `GET` | `/api/computers/{id}` | …`.
var documentedEndpoint = regexp.MustCompile("\\|\\s*`([A-Z]+)`\\s*\\|\\s*`(/[^`]+)`")

type routeEndpoint struct {
	method string
	path   string
}

func (r routeEndpoint) String() string { return r.method + " " + r.path }

// registeredRoutes reads every registration the table is expected to carry.
// The protocol surface (/a2a, /.well-known, /healthz) is in the table too, so
// there is no prefix filter — the static fallback is not registered with a
// method and would be caught by the assertion below if one ever appears.
func registeredRoutes(t *testing.T) []routeEndpoint {
	t.Helper()
	raw, err := os.ReadFile("../internal/handlers/handlers.go")
	if err != nil {
		t.Fatalf("read the route registry: %v", err)
	}
	seen := map[routeEndpoint]bool{}
	var out []routeEndpoint
	for _, m := range handlerRegistration.FindAllStringSubmatch(string(raw), -1) {
		method, path := m[1], m[2]
		if method == "" {
			t.Fatalf("route %q has no method: the docs table has a method column", path)
		}
		r := routeEndpoint{method: method, path: path}
		if seen[r] {
			continue
		}
		seen[r] = true
		out = append(out, r)
	}
	if len(out) < 100 {
		t.Fatalf("parsed only %d routes; the registry format changed and this guard is blind", len(out))
	}
	return out
}

func documentedRoutes(t *testing.T) map[routeEndpoint]bool {
	t.Helper()
	raw, err := os.ReadFile("../docs/docs.md")
	if err != nil {
		t.Fatalf("read docs/docs.md: %v", err)
	}
	out := map[routeEndpoint]bool{}
	for _, m := range documentedEndpoint.FindAllStringSubmatch(string(raw), -1) {
		out[routeEndpoint{method: m[1], path: m[2]}] = true
	}
	if len(out) < 100 {
		t.Fatalf("parsed only %d documented routes; the table format changed and this guard is blind", len(out))
	}
	return out
}

// TestEveryRouteIsDocumented is the set-difference guard: registered but
// undocumented is the failure that shipped a live DELETE with no table row.
func TestEveryRouteIsDocumented(t *testing.T) {
	documented := documentedRoutes(t)
	var missing []string
	for _, route := range registeredRoutes(t) {
		if !documented[route] {
			missing = append(missing, route.String())
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Fatalf(
			"these routes are registered in internal/handlers/handlers.go but absent from docs/docs.md:\n  %s\n"+
				"add them to web/src/lib/docs-content.js (group + descKey) and run `make docs-md`",
			strings.Join(missing, "\n  "),
		)
	}
}

// TestNoDocumentedRouteIsDead is the other direction: a row for a path nobody
// serves teaches an integrator to call a 404.
func TestNoDocumentedRouteIsDead(t *testing.T) {
	registered := map[routeEndpoint]bool{}
	for _, route := range registeredRoutes(t) {
		registered[route] = true
	}
	var stale []string
	for route := range documentedRoutes(t) {
		if !registered[route] {
			stale = append(stale, route.String())
		}
	}
	if len(stale) > 0 {
		sort.Strings(stale)
		t.Fatalf(
			"docs/docs.md documents routes that no handler serves:\n  %s\n"+
				"either register them or drop them from web/src/lib/docs-content.js and run `make docs-md`",
			strings.Join(stale, "\n  "),
		)
	}
}
