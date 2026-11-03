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
		"GET /api/audit":              {http.MethodGet, "/api/audit", ""},
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
		// With no channel store wired the retry answers 503 through the same gate.
		"POST /api/channels/{id}/deliveries/{did}/retry": {http.MethodPost, "/api/channels/ch1/deliveries/dlv1/retry", ""},
		"GET /api/connectors":                            {http.MethodGet, "/api/connectors", ""},
		// Allowed connector: the probe runs the full send path.
		"POST /api/connectors/{id}/actions": {http.MethodPost, "/api/connectors/" + mcp.ID + "/actions", `{"action":"invoke","args":{}}`},
		// The approval queue answers 404 for an unknown task, which is not 404 for
		// a missing route: the probe is checking that the route exists.
		"GET /api/connectors/approvals":               {http.MethodGet, "/api/connectors/approvals", ""},
		"POST /api/connectors/approvals/{id}/approve": {http.MethodPost, "/api/connectors/approvals/task-1/approve", ""},
		"POST /api/connectors/approvals/{id}/reject":  {http.MethodPost, "/api/connectors/approvals/task-1/reject", ""},
		"POST /api/threads/{id}/audio":                {http.MethodPost, "/api/threads/" + finished + "/audio", ""},
		// Computers / schedules / knowledge answer 503 without a store wired in, not 404.
		"GET /api/computers":         {http.MethodGet, "/api/computers", ""},
		"POST /api/computers":        {http.MethodPost, "/api/computers", `{"kind":"browser"}`},
		"GET /api/computers/{id}":    {http.MethodGet, "/api/computers/comp1", ""},
		"DELETE /api/computers/{id}": {http.MethodDelete, "/api/computers/comp1", ""},
		"GET /api/schedules":         {http.MethodGet, "/api/schedules", ""},
		"POST /api/schedules":        {http.MethodPost, "/api/schedules", `{"goal":"demo"}`},
		"GET /api/schedules/{id}":    {http.MethodGet, "/api/schedules/sched1", ""},
		"PUT /api/schedules/{id}":    {http.MethodPut, "/api/schedules/sched1", `{"goal":"demo"}`},
		"DELETE /api/schedules/{id}": {http.MethodDelete, "/api/schedules/sched1", ""},
		// The router endpoints answer 503 without a service wired in, not 404.
		"POST /api/schedules/{id}/run":                 {http.MethodPost, "/api/schedules/sched1/run", ""},
		"GET /api/router/stats":                        {http.MethodGet, "/api/router/stats", ""},
		"GET /api/router/triggers":                     {http.MethodGet, "/api/router/triggers", ""},
		"POST /api/router/triggers":                    {http.MethodPost, "/api/router/triggers", `{"source":"manual","body":"demo"}`},
		"GET /api/router/triggers/{id}":                {http.MethodGet, "/api/router/triggers/trg1", ""},
		"GET /api/router/tickets":                      {http.MethodGet, "/api/router/tickets", ""},
		"GET /api/router/tickets/{id}":                 {http.MethodGet, "/api/router/tickets/tkt1", ""},
		"POST /api/router/tickets/{id}/cancel":         {http.MethodPost, "/api/router/tickets/tkt1/cancel", ""},
		"POST /api/router/tickets/{id}/handback":       {http.MethodPost, "/api/router/tickets/tkt1/handback", `{"triggerId":"trg1"}`},
		"DELETE /api/router/threads/{id}/subscription": {http.MethodDelete, "/api/router/threads/th1/subscription", ""},
		"GET /api/router/policies":                     {http.MethodGet, "/api/router/policies", ""},
		"PUT /api/router/policies/{source}":            {http.MethodPut, "/api/router/policies/thread", `{"mode":"candidate"}`},
		"POST /api/router/passes":                      {http.MethodPost, "/api/router/passes", ""},
		"GET /api/router/events":                       {http.MethodGet, "/api/router/events?since=0", ""},
		// The sandbox endpoints answer 503 without a control plane wired in, not
		// 404. The job poll carries wait=0 so the probe cannot park: an idle node
		// is supposed to get an immediate 204.
		"GET /api/sandboxes":                      {http.MethodGet, "/api/sandboxes", ""},
		"GET /api/sandboxes/stats":                {http.MethodGet, "/api/sandboxes/stats", ""},
		"GET /api/sandboxes/events":               {http.MethodGet, "/api/sandboxes/events?since=0", ""},
		"GET /api/sandboxes/{id}":                 {http.MethodGet, "/api/sandboxes/sbx1", ""},
		"POST /api/sandboxes/{id}/exec":           {http.MethodPost, "/api/sandboxes/sbx1/exec", `{"command":"echo","args":["hi"]}`},
		"POST /api/sandboxes/{id}/renew":          {http.MethodPost, "/api/sandboxes/sbx1/renew", ""},
		"DELETE /api/sandboxes/{id}":              {http.MethodDelete, "/api/sandboxes/sbx1", ""},
		"GET /api/sandbox-providers":              {http.MethodGet, "/api/sandbox-providers", ""},
		"POST /api/sandbox-providers/refresh":     {http.MethodPost, "/api/sandbox-providers/refresh", ""},
		"GET /api/sandbox-nodes":                  {http.MethodGet, "/api/sandbox-nodes", ""},
		"POST /api/sandbox-nodes":                 {http.MethodPost, "/api/sandbox-nodes", `{"id":"node-1","name":"laptop"}`},
		"DELETE /api/sandbox-nodes/{id}":          {http.MethodDelete, "/api/sandbox-nodes/node-1", ""},
		"POST /api/sandbox-nodes/{id}/heartbeat":  {http.MethodPost, "/api/sandbox-nodes/node-1/heartbeat", ""},
		"GET /api/sandbox-nodes/{id}/jobs":        {http.MethodGet, "/api/sandbox-nodes/node-1/jobs?wait=0", ""},
		"POST /api/sandbox-nodes/{id}/jobs/{job}": {http.MethodPost, "/api/sandbox-nodes/node-1/jobs/job-1", `{"exitCode":0}`},
		"GET /api/knowledge":                      {http.MethodGet, "/api/knowledge", ""},
		// Shared files answer files_disabled without a backend, which is the state
		// an instance that never configured one is in. The probe only asserts the
		// route exists, and 503 is not 404.
		"GET /api/files":              {http.MethodGet, "/api/files", ""},
		"POST /api/files":             {http.MethodPost, "/api/files", ""},
		"GET /api/files/{id}":         {http.MethodGet, "/api/files/file-1", ""},
		"GET /api/files/{id}/content": {http.MethodGet, "/api/files/file-1/content", ""},
		"DELETE /api/files/{id}":      {http.MethodDelete, "/api/files/file-1", ""},
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
