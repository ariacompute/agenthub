// docs/docs.md and docs/docs_cn.md are generated from
// web/src/lib/docs-content.js by `make docs-md`; the route registry in
// internal/handlers/handlers.go is maintained separately. Nothing used to
// compare them, which is how `DELETE /api/computers/{id}` shipped live while
// missing from the table.
//
// These tests derive the truth from the Go source and compare it in both
// directions against BOTH generated files:
//
//   - a registered route with no row in either table;
//   - a table row for a path the router does not serve.
//
// Because the tables are generated, a route added to the JS but not regenerated
// fails here too; the failure message carries the command. The screen's own
// copy keys and access classes are guarded by web/tests/docs-content.spec.js.

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

// generatedDocs is every markdown the generator writes. The Chinese table used
// to be invisible to this guard, so a stale zh file could ship unnoticed.
var generatedDocs = []string{"../docs/docs.md", "../docs/docs_cn.md"}

func documentedRoutes(t *testing.T, file string) map[routeEndpoint]bool {
	t.Helper()
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read %s: %v", file, err)
	}
	out := map[routeEndpoint]bool{}
	for _, m := range documentedEndpoint.FindAllStringSubmatch(string(raw), -1) {
		out[routeEndpoint{method: m[1], path: m[2]}] = true
	}
	if len(out) < 100 {
		t.Fatalf("parsed only %d documented routes from %s; the table format changed and this guard is blind", len(out), file)
	}
	return out
}

// TestEveryRouteIsDocumented is the set-difference guard: registered but
// undocumented is the failure that shipped a live DELETE with no table row.
func TestEveryRouteIsDocumented(t *testing.T) {
	registered := registeredRoutes(t)
	for _, file := range generatedDocs {
		documented := documentedRoutes(t, file)
		var missing []string
		for _, route := range registered {
			if !documented[route] {
				missing = append(missing, route.String())
			}
		}
		if len(missing) > 0 {
			sort.Strings(missing)
			t.Fatalf(
				"these routes are registered in internal/handlers/handlers.go but absent from %s:\n  %s\n"+
					"add them to web/src/lib/docs-content.js (group + descKey) and run `make docs-md`",
				file, strings.Join(missing, "\n  "),
			)
		}
	}
}

// TestNoDocumentedRouteIsDead is the other direction: a row for a path nobody
// serves teaches an integrator to call a 404.
func TestNoDocumentedRouteIsDead(t *testing.T) {
	registered := map[routeEndpoint]bool{}
	for _, route := range registeredRoutes(t) {
		registered[route] = true
	}
	for _, file := range generatedDocs {
		var stale []string
		for route := range documentedRoutes(t, file) {
			if !registered[route] {
				stale = append(stale, route.String())
			}
		}
		if len(stale) > 0 {
			sort.Strings(stale)
			t.Fatalf(
				"%s documents routes that no handler serves:\n  %s\n"+
					"either register them or drop them from web/src/lib/docs-content.js and run `make docs-md`",
				file, strings.Join(stale, "\n  "),
			)
		}
	}
}

// httpCodeDeclaration matches `CodeFileNotFound Code = "file_not_found"`.
var httpCodeDeclaration = regexp.MustCompile(`Code\w+\s+Code\s*=\s*"([a-z0-9_]+)"`)

// TestEveryErrorCodeIsDocumented closes the other half of the same door. The
// route table was compared against the router; the error table never was, so
// six codes added with shared files and the approval queue (and the five API-key
// codes before them) reached a deployment without reaching /docs. The console is
// where an operator looks a code up, and "not in the table" reads as "not real".
//
// The truth is derived from internal/httputil/json.go, the same file whose
// status mapping another test already pins. A code may appear either in
// errorGroups (the by-status table) or in adapterCodes (the adapter-owned ones,
// which say something about the resource rather than the caller).
func TestEveryErrorCodeIsDocumented(t *testing.T) {
	source, err := os.ReadFile("../internal/httputil/json.go")
	if err != nil {
		t.Fatalf("read httputil codes: %v", err)
	}
	docs, err := os.ReadFile("../web/src/lib/docs-content.js")
	if err != nil {
		t.Fatalf("read docs-content.js: %v", err)
	}

	documented := map[string]bool{}
	for _, m := range regexp.MustCompile(`codes: '([^']+)'`).FindAllStringSubmatch(string(docs), -1) {
		for _, code := range strings.Split(m[1], ",") {
			documented[strings.TrimSpace(code)] = true
		}
	}
	for _, m := range regexp.MustCompile(`code: '([a-z0-9_]+)'`).FindAllStringSubmatch(string(docs), -1) {
		documented[m[1]] = true
	}

	var missing []string
	for _, m := range httpCodeDeclaration.FindAllStringSubmatch(string(source), -1) {
		if !documented[m[1]] {
			missing = append(missing, m[1])
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf(
			"error codes the server can return but /docs never lists:\n  %s\n"+
				"add them to errorGroups (or adapterCodes) in web/src/lib/docs-content.js and run `make docs-md`",
			strings.Join(missing, "\n  "),
		)
	}
}
