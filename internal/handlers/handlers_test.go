package handlers

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agenthub/internal/a2a"
	"agenthub/internal/adapter"
	"agenthub/internal/config"
	"agenthub/internal/hub"
	"agenthub/internal/registry"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func testDeps(t *testing.T, cfg *config.Config, entries ...*registry.Entry) Deps {
	t.Helper()
	reg := registry.New()
	for _, e := range entries {
		stored, err := reg.Upsert(e)
		if err != nil {
			t.Fatalf("seed %q: %v", e.Name, err)
		}
		*e = *stored
	}
	h := hub.New(cfg, reg, adapter.Options{
		CLITimeout:     time.Second,
		CLIAllowlist:   []string{"echo"},
		MaxOutputBytes: 1024,
	}, &hub.MockNotifier{})
	return Deps{
		Cfg: cfg,
		Hub: h,
		Reg: reg,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func openConfig(staticDir string) *config.Config {
	return &config.Config{
		HTTPPort:       "3500",
		Env:            "dev",
		StaticDir:      staticDir,
		AuthOpenMode:   true,
		LegacyMethods:  true,
		MaxParallel:    4,
		CLITimeout:     time.Second,
		CLIAllowlist:   []string{"echo"},
		MaxOutputBytes: 1024,
		EventBuffer:    64,
		PushEnabled:    true,
	}
}

func do(t *testing.T, h http.Handler, method, target string, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, target, nil)
	} else {
		r = httptest.NewRequest(method, target, strings.NewReader(body))
	}
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func decodeJSON(t *testing.T, w *httptest.ResponseRecorder, dst any) {
	t.Helper()
	if err := json.Unmarshal(w.Body.Bytes(), dst); err != nil {
		t.Fatalf("decode %q: %v", w.Body.String(), err)
	}
}

func rpcCall(t *testing.T, h http.Handler, method string, params string) a2a.Response {
	t.Helper()
	body := `{"jsonrpc":"2.0","id":1,"method":"` + method + `","params":` + params + `}`
	w := do(t, h, http.MethodPost, "/a2a", body, nil)
	var resp a2a.Response
	decodeJSON(t, w, &resp)
	return resp
}

func rpcCode(t *testing.T, resp a2a.Response) int {
	t.Helper()
	if resp.Error == nil {
		t.Fatalf("expected an error object, got result %+v", resp.Result)
	}
	return resp.Error.Code
}

// runOnce sends one task through the A2A endpoint and returns its id.
func runOnce(t *testing.T, h http.Handler, resourceID string) string {
	t.Helper()
	params := `{"message":{"role":"user","parts":[{"type":"text","text":"hello"}]},"metadata":{"resourceId":"` + resourceID + `"}}`
	resp := rpcCall(t, h, a2a.MethodMessageSend, params)
	if resp.Error != nil {
		t.Fatalf("message/send failed: %+v", resp.Error)
	}
	task := resultTask(t, resp)
	return task.ID
}

func resultTask(t *testing.T, resp a2a.Response) a2a.Task {
	t.Helper()
	raw, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	var task a2a.Task
	if err := json.Unmarshal(raw, &task); err != nil {
		t.Fatalf("result is not a task: %v (%s)", err, raw)
	}
	return task
}

// ---------------------------------------------------------------------------
// discovery + health
// ---------------------------------------------------------------------------

func TestHealthz(t *testing.T) {
	d := testDeps(t, openConfig(""), &registry.Entry{Kind: registry.KindModel, Name: "m1", Health: registry.HealthHealthy})
	w := do(t, Register(d), http.MethodGet, "/healthz", "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var body healthResponse
	decodeJSON(t, w, &body)
	if body.Status != "ok" || body.Version == "" || body.ProtocolVersion == "" {
		t.Fatalf("healthz = %+v", body)
	}
	if len(body.Methods) != len(a2a.Methods) {
		t.Fatalf("methods = %v, want %v", body.Methods, a2a.Methods)
	}
	if body.Auth != "open" || !body.LegacyMethods || !body.PushEnabled {
		t.Fatalf("healthz flags = %+v", body)
	}
	if body.Resources["model"] != 1 || body.Health["healthy"] != 1 {
		t.Fatalf("counts = %+v / %+v", body.Resources, body.Health)
	}
}

func TestAgentCardEndpoints(t *testing.T) {
	entry := &registry.Entry{Kind: registry.KindMCP, Name: "mcp-1"}
	d := testDeps(t, openConfig(""), entry)
	h := Register(d)

	for _, path := range []string{"/.well-known/agent.json", "/a2a/agent-card"} {
		w := do(t, h, http.MethodGet, path, "", nil)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: status = %d", path, w.Code)
		}
		var card a2a.AgentCard
		decodeJSON(t, w, &card)
		if card.Name != "agenthub" || card.ProtocolVersion == "" {
			t.Fatalf("%s: card = %+v", path, card)
		}
		if len(card.Skills) != 1 || card.Skills[0].ID != entry.ID {
			t.Fatalf("%s: skills = %+v", path, card.Skills)
		}
		if !strings.HasPrefix(card.URL, "http://") {
			t.Fatalf("%s: url = %q", path, card.URL)
		}
	}

	// A forwarded proto is honoured when the hub sits behind a proxy.
	w := do(t, h, http.MethodGet, "/.well-known/agent.json", "", map[string]string{"X-Forwarded-Proto": "https"})
	var card a2a.AgentCard
	decodeJSON(t, w, &card)
	if !strings.HasPrefix(card.URL, "https://") {
		t.Fatalf("url = %q, want https", card.URL)
	}
}

// ---------------------------------------------------------------------------
// control plane auth
// ---------------------------------------------------------------------------

func TestRequireAdmin(t *testing.T) {
	entries := []*registry.Entry{{Kind: registry.KindModel, Name: "m1"}}

	// Open mode (no token configured): reachable.
	d := testDeps(t, openConfig(""), entries...)
	if w := do(t, Register(d), http.MethodGet, "/api/resources", "", nil); w.Code != http.StatusOK {
		t.Fatalf("open mode: status = %d, want 200", w.Code)
	}

	// Token configured: requests must carry it.
	cfg := openConfig("")
	cfg.AdminToken = "secret"
	d = testDeps(t, cfg, entries...)
	h := Register(d)
	if w := do(t, h, http.MethodGet, "/api/resources", "", nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("no token: status = %d, want 401", w.Code)
	}
	var body struct {
		Code string `json:"code"`
	}
	decodeJSON(t, do(t, h, http.MethodGet, "/api/resources", "", nil), &body)
	if body.Code != "unauthorized" {
		t.Fatalf("code = %q, want unauthorized", body.Code)
	}
	for _, header := range []string{"Bearer secret", "ApiKey secret", "secret"} {
		if w := do(t, h, http.MethodGet, "/api/resources", "", map[string]string{"Authorization": header}); w.Code != http.StatusOK {
			t.Fatalf("header %q: status = %d, want 200", header, w.Code)
		}
	}
	if w := do(t, h, http.MethodGet, "/api/resources", "", map[string]string{"Authorization": "Bearer wrong"}); w.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token: status = %d, want 401", w.Code)
	}

	// /healthz and the A2A plane are never behind the admin token.
	if w := do(t, h, http.MethodGet, "/healthz", "", nil); w.Code != http.StatusOK {
		t.Fatalf("healthz: status = %d", w.Code)
	}
	if w := do(t, h, http.MethodGet, "/.well-known/agent.json", "", nil); w.Code != http.StatusOK {
		t.Fatalf("agent card: status = %d", w.Code)
	}
}

// ---------------------------------------------------------------------------
// JSON-RPC
// ---------------------------------------------------------------------------

func TestJSONRPCErrorPaths(t *testing.T) {
	entry := &registry.Entry{Kind: registry.KindSkill, Name: "s1"}
	h := Register(testDeps(t, openConfig(""), entry))

	w := do(t, h, http.MethodPost, "/a2a", "not json", nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("malformed body: status = %d, want 400", w.Code)
	}
	var resp a2a.Response
	decodeJSON(t, w, &resp)
	if rpcCode(t, resp) != a2a.CodeParseError {
		t.Fatalf("code = %d, want parse_error", rpcCode(t, resp))
	}

	resp = rpcCall(t, h, a2a.MethodTasksGet, `{"id":"task_missing"}`)
	if rpcCode(t, resp) != a2a.CodeTaskNotFound {
		t.Fatalf("code = %d, want task_not_found", rpcCode(t, resp))
	}

	// Wrong version, unknown method and unknown params field.
	for _, tc := range []struct {
		name string
		body string
		code int
	}{
		{"bad version", `{"jsonrpc":"1.0","id":1,"method":"tasks/get","params":{"id":"x"}}`, a2a.CodeInvalidRequest},
		{"unknown method", `{"jsonrpc":"2.0","id":1,"method":"nope","params":{}}`, a2a.CodeMethodNotFound},
		{"missing params", `{"jsonrpc":"2.0","id":1,"method":"tasks/get"}`, a2a.CodeInvalidParams},
		{"unknown field", `{"jsonrpc":"2.0","id":1,"method":"tasks/get","params":{"id":"x","nope":1}}`, a2a.CodeInvalidParams},
	} {
		w := do(t, h, http.MethodPost, "/a2a", tc.body, nil)
		var r a2a.Response
		decodeJSON(t, w, &r)
		if rpcCode(t, r) != tc.code {
			t.Errorf("%s: code = %d, want %d", tc.name, rpcCode(t, r), tc.code)
		}
	}
}

func TestJSONRPCSendGetCancel(t *testing.T) {
	entry := &registry.Entry{Kind: registry.KindSkill, Name: "s1"}
	d := testDeps(t, openConfig(""), entry)
	h := Register(d)

	id := runOnce(t, h, entry.ID)

	got := rpcCall(t, h, a2a.MethodTasksGet, `{"id":"`+id+`"}`)
	if got.Error != nil {
		t.Fatalf("tasks/get: %+v", got.Error)
	}
	task := resultTask(t, got)
	if task.Status.State != a2a.TaskStateCompleted {
		t.Fatalf("state = %s, want completed", task.Status.State)
	}

	// historyLength trims the transcript.
	one := 1
	raw, _ := json.Marshal(map[string]any{"id": id, "historyLength": one})
	got = rpcCall(t, h, a2a.MethodTasksGet, string(raw))
	trimmed := resultTask(t, got)
	if len(trimmed.History) != 1 {
		t.Fatalf("history = %d, want 1", len(trimmed.History))
	}

	// A completed task can no longer be cancelled.
	got = rpcCall(t, h, a2a.MethodTasksCancel, `{"id":"`+id+`"}`)
	if rpcCode(t, got) != a2a.CodeTaskNotCancelable {
		t.Fatalf("code = %d, want task_not_cancelable", rpcCode(t, got))
	}

	// Cancelling a live task works, and a second cancel is rejected.
	live := d.Hub.Store().Create(entry.ID, a2a.NewMessage(a2a.RoleUser, a2a.NewTextPart("x")))
	if live.Status.State != a2a.TaskStateSubmitted {
		t.Fatalf("state = %s, want submitted", live.Status.State)
	}
	canceled := rpcCall(t, h, a2a.MethodTasksCancel, `{"id":"`+live.ID+`"}`)
	if canceled.Error != nil {
		t.Fatalf("cancel: %+v", canceled.Error)
	}
	if resultTask(t, canceled).Status.State != a2a.TaskStateCanceled {
		t.Fatalf("state = %s, want canceled", resultTask(t, canceled).Status.State)
	}
	if again := rpcCall(t, h, a2a.MethodTasksCancel, `{"id":"`+live.ID+`"}`); rpcCode(t, again) != a2a.CodeTaskNotCancelable {
		t.Fatalf("second cancel: code = %d", rpcCode(t, again))
	}
}

func TestJSONRPCResumeAndPush(t *testing.T) {
	agent := &registry.Entry{Kind: registry.KindAgent, Name: "a1"}
	h := Register(testDeps(t, openConfig(""), agent))

	resp := rpcCall(t, h, a2a.MethodMessageSend, `{"message":{"role":"user","parts":[{"type":"text","text":"please approve the transfer"}]},"metadata":{"resourceId":"`+agent.ID+`"}}`)
	if resp.Error != nil {
		t.Fatalf("send: %+v", resp.Error)
	}
	parked := resultTask(t, resp)
	if parked.Status.State != a2a.TaskStateInputRequired {
		t.Fatalf("state = %s, want input-required", parked.Status.State)
	}

	// Resuming a task that is not parked is rejected.
	wrong := rpcCall(t, h, a2a.MethodTasksResume, `{"id":"task_missing","message":{"role":"user","parts":[{"type":"text","text":"ok"}]}}`)
	if rpcCode(t, wrong) != a2a.CodeTaskNotFound {
		t.Fatalf("code = %d, want task_not_found", rpcCode(t, wrong))
	}

	resumed := rpcCall(t, h, a2a.MethodTasksResume, `{"id":"`+parked.ID+`","message":{"role":"user","parts":[{"type":"text","text":"approved"}]}}`)
	if resumed.Error != nil {
		t.Fatalf("resume: %+v", resumed.Error)
	}
	if resultTask(t, resumed).Status.State != a2a.TaskStateCompleted {
		t.Fatalf("state = %s, want completed", resultTask(t, resumed).Status.State)
	}

	// Push notification config round trip.
	set := rpcCall(t, h, a2a.MethodPushSet, `{"taskId":"`+parked.ID+`","pushNotificationConfig":{"url":"http://callback"}}`)
	if set.Error != nil {
		t.Fatalf("push set: %+v", set.Error)
	}
	get := rpcCall(t, h, a2a.MethodPushGet, `{"id":"`+parked.ID+`"}`)
	if get.Error != nil {
		t.Fatalf("push get: %+v", get.Error)
	}
	if bad := rpcCall(t, h, a2a.MethodPushSet, `{"taskId":"`+parked.ID+`","pushNotificationConfig":{}}`); rpcCode(t, bad) != a2a.CodeInvalidParams {
		t.Fatalf("empty url: code = %d, want invalid_params", rpcCode(t, bad))
	}
}

func TestJSONRPCStreamHasNoSentinel(t *testing.T) {
	entry := &registry.Entry{Kind: registry.KindMCP, Name: "mcp-1"}
	h := Register(testDeps(t, openConfig(""), entry))

	body := `{"jsonrpc":"2.0","id":7,"method":"message/stream","params":{"message":{"role":"user","parts":[{"type":"text","text":"hello"}]},"metadata":{"resourceId":"` + entry.ID + `"}}}`
	w := do(t, h, http.MethodPost, "/a2a", body, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type = %q, want text/event-stream", ct)
	}
	out := w.Body.String()
	if strings.Contains(out, "[DONE]") {
		t.Fatal("A2A streaming has no sentinel frame")
	}
	for _, want := range []string{"event: task_status_update", "event: task_artifact_update", `"state":"completed"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("stream missing %q: %s", want, out)
		}
	}
	// Every frame must be a JSON-RPC response carrying the request id.
	if !strings.Contains(out, `"id":7`) {
		t.Fatalf("frames must echo the request id: %s", out)
	}
}

func TestJSONRPCStreamErrorFrame(t *testing.T) {
	h := Register(testDeps(t, openConfig(""))) // empty registry
	body := `{"jsonrpc":"2.0","id":3,"method":"message/stream","params":{"message":{"role":"user","parts":[{"type":"text","text":"hello"}]}}}`
	w := do(t, h, http.MethodPost, "/a2a", body, nil)
	out := w.Body.String()
	if !strings.Contains(out, "event: error") {
		t.Fatalf("expected an error frame: %s", out)
	}
	if !strings.Contains(out, "resource_not_found") {
		t.Fatalf("expected the machine readable code: %s", out)
	}
}

func TestLegacyMethodAliases(t *testing.T) {
	entry := &registry.Entry{Kind: registry.KindModel, Name: "m1"}
	cfg := openConfig("")
	h := Register(testDeps(t, cfg, entry))

	params := `{"message":{"role":"user","parts":[{"type":"text","text":"hello"}]},"metadata":{"resourceId":"` + entry.ID + `"}}`
	if resp := rpcCall(t, h, a2a.MethodTasksSend, params); resp.Error != nil {
		t.Fatalf("tasks/send alias: %+v", resp.Error)
	}

	// Strict deployments can turn the aliases off.
	cfg.LegacyMethods = false
	strict := Register(testDeps(t, cfg, entry))
	if resp := rpcCall(t, strict, a2a.MethodTasksSend, params); rpcCode(t, resp) != a2a.CodeMethodNotFound {
		t.Fatalf("code = %d, want method_not_found", rpcCode(t, resp))
	}
	if resp := rpcCall(t, strict, a2a.MethodMessageSend, params); resp.Error != nil {
		t.Fatalf("canonical method must still work: %+v", resp.Error)
	}
}

func TestA2APostOnly(t *testing.T) {
	// /a2a is registered for POST only; a GET must never reach the JSON-RPC
	// dispatcher (it falls through to the console handler instead).
	h := Register(testDeps(t, openConfig("")))
	w := do(t, h, http.MethodPost, "/a2a", `{"jsonrpc":"2.0","id":1,"method":"tasks/get","params":{"id":"x"}}`, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("post: status = %d", w.Code)
	}
	w = do(t, h, http.MethodGet, "/a2a", "", nil)
	if strings.Contains(w.Body.String(), "jsonrpc") {
		t.Fatalf("GET /a2a must not dispatch JSON-RPC: %s", w.Body.String())
	}
}

// ---------------------------------------------------------------------------
// control plane: resources
// ---------------------------------------------------------------------------

func TestResourceCRUD(t *testing.T) {
	h := Register(testDeps(t, openConfig("")))

	w := do(t, h, http.MethodPost, "/api/resources", `{"kind":"model","name":"m1","endpoint":"http://x","transport":"streamable-http"}`, nil)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: status = %d (%s)", w.Code, w.Body.String())
	}
	var created registry.Entry
	decodeJSON(t, w, &created)
	if created.ID == "" || created.Kind != registry.KindModel || created.Health != registry.HealthUnknown {
		t.Fatalf("created = %+v", created)
	}

	for _, tc := range []struct {
		name string
		body string
		code string
	}{
		{"bad kind", `{"kind":"nope","name":"x"}`, "invalid_kind"},
		{"bad transport", `{"kind":"mcp","name":"x","transport":"carrier-pigeon"}`, "invalid_transport"},
		{"no name", `{"kind":"skill","name":"   "}`, "invalid_entry"},
		{"malformed", `{"kind":`, "bad_request"},
		{"unknown field", `{"kind":"cli","name":"x","nope":1}`, "bad_request"},
	} {
		w := do(t, h, http.MethodPost, "/api/resources", tc.body, nil)
		var body struct {
			Code string `json:"code"`
		}
		decodeJSON(t, w, &body)
		if body.Code != tc.code {
			t.Errorf("%s: code = %q, want %q", tc.name, body.Code, tc.code)
		}
	}

	// Duplicate ids are rejected on create.
	w = do(t, h, http.MethodPost, "/api/resources", `{"id":"`+created.ID+`","kind":"agent","name":"dup"}`, nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("duplicate: status = %d (%s)", w.Code, w.Body.String())
	}

	if w = do(t, h, http.MethodGet, "/api/resources/"+created.ID, "", nil); w.Code != http.StatusOK {
		t.Fatalf("get: status = %d", w.Code)
	}
	if w = do(t, h, http.MethodGet, "/api/resources?kind=nope", "", nil); w.Code != http.StatusBadRequest {
		t.Fatalf("bad kind filter: status = %d", w.Code)
	}
	if w = do(t, h, http.MethodGet, "/api/resources?kind=model&q=m1", "", nil); w.Code != http.StatusOK {
		t.Fatalf("filtered list: status = %d", w.Code)
	}
	var listing struct {
		Resources []registry.Entry `json:"resources"`
		Counts    map[string]int   `json:"counts"`
	}
	decodeJSON(t, w, &listing)
	if len(listing.Resources) != 1 || listing.Counts["model"] != 1 {
		t.Fatalf("listing = %+v", listing)
	}

	w = do(t, h, http.MethodPut, "/api/resources/"+created.ID, `{"kind":"model","name":"m1-renamed","version":"2.0.0","health":"healthy"}`, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("update: status = %d (%s)", w.Code, w.Body.String())
	}
	var updated registry.Entry
	decodeJSON(t, w, &updated)
	if updated.Name != "m1-renamed" || updated.ID != created.ID {
		t.Fatalf("updated = %+v", updated)
	}
	if w = do(t, h, http.MethodPut, "/api/resources/nope", `{"kind":"model","name":"x"}`, nil); w.Code != http.StatusNotFound {
		t.Fatalf("update missing: status = %d, want 404", w.Code)
	}

	if w = do(t, h, http.MethodPost, "/api/resources/"+created.ID+"/health", `{"health":"degraded"}`, nil); w.Code != http.StatusOK {
		t.Fatalf("health: status = %d (%s)", w.Code, w.Body.String())
	}
	if w = do(t, h, http.MethodPost, "/api/resources/"+created.ID+"/health", `{"health":"purple"}`, nil); w.Code != http.StatusBadRequest {
		t.Fatalf("bad health: status = %d (%s)", w.Code, w.Body.String())
	}

	if w = do(t, h, http.MethodDelete, "/api/resources/"+created.ID, "", nil); w.Code != http.StatusNoContent {
		t.Fatalf("delete: status = %d, want 204", w.Code)
	}
	if w = do(t, h, http.MethodDelete, "/api/resources/"+created.ID, "", nil); w.Code != http.StatusNotFound {
		t.Fatalf("delete again: status = %d, want 404 (%s)", w.Code, w.Body.String())
	}
}

func TestExportEndpoints(t *testing.T) {
	entry := &registry.Entry{
		Kind:      registry.KindMCP,
		Name:      "mcp-1",
		Version:   "1.2.3",
		Transport: registry.TransportStdio,
		Endpoint:  "stdio://mcp-1",
	}
	h := Register(testDeps(t, openConfig(""), entry))

	w := do(t, h, http.MethodGet, "/api/export/mcp", "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("mcp export: status = %d", w.Code)
	}
	var export struct {
		Servers []registry.MCPServerDescription `json:"servers"`
	}
	decodeJSON(t, w, &export)
	if len(export.Servers) != 1 || export.Servers[0].ResourceKind != registry.KindMCP || export.Servers[0].Version != "1.2.3" {
		t.Fatalf("servers = %+v", export.Servers)
	}

	w = do(t, h, http.MethodGet, "/api/export/agent-card", "", nil)
	var card a2a.AgentCard
	decodeJSON(t, w, &card)
	if len(card.Skills) != 1 || card.Skills[0].ResourceKind != "mcp" {
		t.Fatalf("card = %+v", card)
	}
}

// ---------------------------------------------------------------------------
// control plane: tasks
// ---------------------------------------------------------------------------

func TestTaskAPI(t *testing.T) {
	entry := &registry.Entry{Kind: registry.KindSkill, Name: "s1"}
	d := testDeps(t, openConfig(""), entry)
	h := Register(d)
	id := runOnce(t, h, entry.ID)

	w := do(t, h, http.MethodGet, "/api/tasks", "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("list: status = %d", w.Code)
	}
	var list struct {
		Tasks []a2a.Task `json:"tasks"`
	}
	decodeJSON(t, w, &list)
	if len(list.Tasks) != 1 {
		t.Fatalf("tasks = %d, want 1", len(list.Tasks))
	}

	if w = do(t, h, http.MethodGet, "/api/tasks/"+id, "", nil); w.Code != http.StatusOK {
		t.Fatalf("get: status = %d", w.Code)
	}
	if w = do(t, h, http.MethodGet, "/api/tasks/nope", "", nil); w.Code != http.StatusNotFound {
		t.Fatalf("get missing: status = %d, want 404", w.Code)
	}

	w = do(t, h, http.MethodGet, "/api/tasks/"+id+"/spans", "", nil)
	var spans struct {
		Spans []hub.Span `json:"spans"`
	}
	decodeJSON(t, w, &spans)
	if len(spans.Spans) == 0 {
		t.Fatalf("spans = %+v, want the run timeline", spans.Spans)
	}
	if w = do(t, h, http.MethodGet, "/api/tasks/nope/spans", "", nil); w.Code != http.StatusNotFound {
		t.Fatalf("spans missing: status = %d, want 404", w.Code)
	}

	// A finished task replays instantly and closes the stream.
	w = do(t, h, http.MethodGet, "/api/tasks/"+id+"/events", "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("events: status = %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("events content type = %q", ct)
	}
	out := w.Body.String()
	if !strings.Contains(out, a2a.EventTaskStatus) || !strings.Contains(out, a2a.EventTaskSpan) {
		t.Fatalf("replay = %s", out)
	}
	if w = do(t, h, http.MethodGet, "/api/tasks/nope/events", "", nil); w.Code != http.StatusNotFound {
		t.Fatalf("events missing: status = %d, want 404", w.Code)
	}

	// The console cancel / resume endpoints mirror the A2A methods.
	live := d.Hub.Store().Create(entry.ID, a2a.NewMessage(a2a.RoleUser, a2a.NewTextPart("hello")))
	w = do(t, h, http.MethodPost, "/api/tasks/"+live.ID+"/cancel", "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("cancel: status = %d (%s)", w.Code, w.Body.String())
	}
	if w = do(t, h, http.MethodPost, "/api/tasks/nope/cancel", "", nil); w.Code != http.StatusNotFound {
		t.Fatalf("cancel missing: status = %d, want 404", w.Code)
	}
	// A canceled task is terminal: resuming it is a client error.
	w = do(t, h, http.MethodPost, "/api/tasks/"+live.ID+"/resume", `{"message":{"role":"user","parts":[{"type":"text","text":"ok"}]}}`, nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("resume a canceled task: status = %d (%s)", w.Code, w.Body.String())
	}
}

// ---------------------------------------------------------------------------
// static console
// ---------------------------------------------------------------------------

func TestStaticFallback(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html>console</html>"), 0o644); err != nil {
		t.Fatalf("write index: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "app.js"), []byte("console.log(1)"), 0o644); err != nil {
		t.Fatalf("write asset: %v", err)
	}
	h := Register(testDeps(t, openConfig(dir)))

	if w := do(t, h, http.MethodGet, "/", "", nil); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "console") {
		t.Fatalf("index: status = %d body = %q", w.Code, w.Body.String())
	}
	if w := do(t, h, http.MethodGet, "/app.js", "", nil); w.Code != http.StatusOK {
		t.Fatalf("asset: status = %d", w.Code)
	}
	// Unknown paths fall back to index.html so the Vue router owns them.
	if w := do(t, h, http.MethodGet, "/registry", "", nil); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "console") {
		t.Fatalf("spa fallback: status = %d body = %q", w.Code, w.Body.String())
	}

	// Without a build the console 404s with a machine readable code instead of
	// silently serving an empty page.
	empty := Register(testDeps(t, openConfig(filepath.Join(t.TempDir(), "missing"))))
	w := do(t, empty, http.MethodGet, "/", "", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("missing bundle: status = %d, want 404", w.Code)
	}
	var body struct {
		Code string `json:"code"`
	}
	decodeJSON(t, w, &body)
	if body.Code != "not_found" {
		t.Fatalf("code = %q, want not_found", body.Code)
	}
}
