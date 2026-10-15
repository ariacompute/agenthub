// Package tests holds the end-to-end suite: it boots agenthub exactly like
// cmd/agenthub-server does and drives it over real HTTP, so the A2A contract,
// the control plane and the console endpoints are exercised together.
package tests

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"agenthub/internal/a2a"
	"agenthub/internal/adapter"
	"agenthub/internal/config"
	"agenthub/internal/handlers"
	"agenthub/internal/hub"
	"agenthub/internal/registry"
)

// ---------------------------------------------------------------------------
// harness
// ---------------------------------------------------------------------------

type harness struct {
	t      *testing.T
	server *httptest.Server
	reg    *registry.Registry
	h      *hub.Hub
	pusher *hub.MockNotifier
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	cfg := &config.Config{
		HTTPPort:       "3500",
		Env:            "dev",
		StaticDir:      t.TempDir(),
		AuthOpenMode:   true,
		LegacyMethods:  true,
		MaxParallel:    4,
		CLITimeout:     time.Second,
		CLIAllowlist:   []string{"echo", "ls"},
		MaxOutputBytes: 4096,
		EventBuffer:    128,
		PushEnabled:    true,
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("config: %v", err)
	}

	reg := registry.New()
	seed := []*registry.Entry{
		{Kind: registry.KindModel, Name: "demo-model", Endpoint: "http://model", Version: "1.0.0", Capabilities: []string{"chat"}, Health: registry.HealthHealthy},
		{Kind: registry.KindAgent, Name: "demo-agent", Endpoint: "http://agent", Version: "0.9.0", Capabilities: []string{"plan"}, Health: registry.HealthHealthy},
		{Kind: registry.KindMCP, Name: "demo-mcp", Endpoint: "stdio://demo", Transport: registry.TransportStdio, Capabilities: []string{"tools/list"}, Health: registry.HealthHealthy},
		{Kind: registry.KindSkill, Name: "demo-skill", Version: "2.1.0", Capabilities: []string{"search"}, Health: registry.HealthDegraded},
		{Kind: registry.KindCLI, Name: "demo-cli", Capabilities: []string{"echo"}, Metadata: map[string]string{"command": "echo"}, Health: registry.HealthHealthy},
	}
	for _, e := range seed {
		stored, err := reg.Upsert(e)
		if err != nil {
			t.Fatalf("seed %q: %v", e.Name, err)
		}
		*e = *stored
	}

	pusher := &hub.MockNotifier{}
	h := hub.New(cfg, reg, adapter.Options{
		CLITimeout:     cfg.CLITimeout,
		CLIAllowlist:   cfg.CLIAllowlist,
		MaxOutputBytes: cfg.MaxOutputBytes,
	}, pusher)

	server := httptest.NewServer(handlers.Register(handlers.Deps{
		Cfg: cfg,
		Hub: h,
		Reg: reg,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}))
	t.Cleanup(server.Close)

	return &harness{t: t, server: server, reg: reg, h: h, pusher: pusher}
}

func (h *harness) do(method, path, body string) (*http.Response, []byte) {
	h.t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, h.server.URL+path, reader)
	if err != nil {
		h.t.Fatalf("build request: %v", err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := h.server.Client().Do(req)
	if err != nil {
		h.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		h.t.Fatalf("read body: %v", err)
	}
	return res, raw
}

func (h *harness) getJSON(path string, dst any) *http.Response {
	h.t.Helper()
	res, raw := h.do(http.MethodGet, path, "")
	if err := json.Unmarshal(raw, dst); err != nil {
		h.t.Fatalf("decode %s: %v (%s)", path, err, raw)
	}
	return res
}

// rpc performs one JSON-RPC call over HTTP.
func (h *harness) rpc(method string, params string) a2a.Response {
	h.t.Helper()
	body := `{"jsonrpc":"2.0","id":"e2e","method":"` + method + `","params":` + params + `}`
	res, raw := h.do(http.MethodPost, "/a2a", body)
	if res.StatusCode >= http.StatusInternalServerError {
		h.t.Fatalf("%s: status = %d (%s)", method, res.StatusCode, raw)
	}
	if ct := res.Header.Get("Content-Type"); ct != "application/json" {
		h.t.Fatalf("%s: content type = %q", method, ct)
	}
	var resp a2a.Response
	if err := json.Unmarshal(raw, &resp); err != nil {
		h.t.Fatalf("decode rpc response: %v (%s)", err, raw)
	}
	if resp.JSONRPC != a2a.JSONRPCVersion || resp.ID != "e2e" {
		h.t.Fatalf("envelope = %+v", resp)
	}
	return resp
}

func (h *harness) task(resp a2a.Response) a2a.Task {
	h.t.Helper()
	if resp.Error != nil {
		h.t.Fatalf("unexpected rpc error: %+v", resp.Error)
	}
	raw, err := json.Marshal(resp.Result)
	if err != nil {
		h.t.Fatalf("marshal result: %v", err)
	}
	var task a2a.Task
	if err := json.Unmarshal(raw, &task); err != nil {
		h.t.Fatalf("result is not a task: %v (%s)", err, raw)
	}
	return task
}

func (h *harness) send(resourceID, text string) a2a.Task {
	h.t.Helper()
	params := `{"message":{"role":"user","parts":[{"type":"text","text":"` + text + `"}]},"metadata":{"resourceId":"` + resourceID + `"}}`
	return h.task(h.rpc(a2a.MethodMessageSend, params))
}

// stream performs message/stream and returns every SSE frame body.
func (h *harness) stream(method string, params string) (string, []string) {
	h.t.Helper()
	body := `{"jsonrpc":"2.0","id":"stream","method":"` + method + `","params":` + params + `}`
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.server.URL+"/a2a", strings.NewReader(body))
	if err != nil {
		h.t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := h.server.Client().Do(req)
	if err != nil {
		h.t.Fatalf("stream: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(res.Body)
		h.t.Fatalf("stream status = %d (%s)", res.StatusCode, raw)
	}
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		h.t.Fatalf("read stream: %v", err)
	}
	out := string(raw)
	var data []string
	scanner := bufio.NewScanner(strings.NewReader(out))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data: ") {
			data = append(data, strings.TrimPrefix(line, "data: "))
		}
	}
	return out, data
}

func resourceID(t *testing.T, reg *registry.Registry, kind registry.Kind) string {
	t.Helper()
	for _, e := range reg.List(string(kind), "") {
		return e.ID
	}
	t.Fatalf("no %s resource registered", kind)
	return ""
}

// ---------------------------------------------------------------------------
// discovery + operations
// ---------------------------------------------------------------------------

func TestE2EAgentCardAndHealthz(t *testing.T) {
	h := newHarness(t)

	var card a2a.AgentCard
	res := h.getJSON("/.well-known/agent.json", &card)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}
	if card.Name != "agenthub" || card.ProtocolVersion != config.ProtocolVersion {
		t.Fatalf("card = %+v", card)
	}
	if !strings.HasPrefix(card.URL, h.server.URL) {
		t.Fatalf("card url = %q, want %q prefix", card.URL, h.server.URL)
	}
	if len(card.Skills) != 5 {
		t.Fatalf("skills = %d, want one per resource", len(card.Skills))
	}
	if !card.Capabilities.Streaming || !card.Capabilities.PushNotifications {
		t.Fatalf("capabilities = %+v", card.Capabilities)
	}

	var health struct {
		Status    string         `json:"status"`
		Version   string         `json:"version"`
		Resources map[string]int `json:"resources"`
		Health    map[string]int `json:"health"`
		Tasks     int            `json:"tasks"`
		Methods   []string       `json:"methods"`
		Auth      string         `json:"auth"`
	}
	h.getJSON("/healthz", &health)
	if health.Status != "ok" || health.Version != config.Version {
		t.Fatalf("healthz = %+v", health)
	}
	for _, kind := range []string{"model", "agent", "mcp", "skill", "cli"} {
		if health.Resources[kind] != 1 {
			t.Fatalf("resources = %+v, want one %s", health.Resources, kind)
		}
	}
	if health.Health["healthy"] != 4 || health.Health["degraded"] != 1 {
		t.Fatalf("health counts = %+v", health.Health)
	}
	if health.Auth != "open" {
		t.Fatalf("auth = %q, want open", health.Auth)
	}
}

// ---------------------------------------------------------------------------
// happy paths over the five adapters
// ---------------------------------------------------------------------------

func TestE2ESendAcrossEveryKind(t *testing.T) {
	h := newHarness(t)
	for _, kind := range registry.Kinds {
		id := resourceID(t, h.reg, kind)
		task := h.send(id, "hello from e2e")
		if task.Status.State != a2a.TaskStateCompleted {
			t.Fatalf("%s: state = %s, want completed", kind, task.Status.State)
		}
		if task.ResourceID != id {
			t.Fatalf("%s: resource = %s, want %s", kind, task.ResourceID, id)
		}
		if len(task.Artifacts) == 0 {
			t.Fatalf("%s: no artifacts", kind)
		}
		spans := h.h.Store().Spans(task.ID)
		if len(spans) == 0 {
			t.Fatalf("%s: no spans", kind)
		}
		for _, s := range spans {
			if s.Status != hub.SpanStatusOK {
				t.Fatalf("%s: span = %+v", kind, s)
			}
		}
	}
}

func TestE2ETaskLifecycleGetCancel(t *testing.T) {
	h := newHarness(t)
	model := resourceID(t, h.reg, registry.KindModel)

	task := h.send(model, "hello")
	if task.ID == "" {
		t.Fatal("task id missing")
	}

	got := h.task(h.rpc(a2a.MethodTasksGet, `{"id":"`+task.ID+`"}`))
	if got.ID != task.ID || got.Status.State != a2a.TaskStateCompleted {
		t.Fatalf("tasks/get = %+v", got)
	}

	// A completed task is terminal and refuses to be cancelled.
	errResp := h.rpc(a2a.MethodTasksCancel, `{"id":"`+task.ID+`"}`)
	if errResp.Error == nil || errResp.Error.Code != a2a.CodeTaskNotCancelable {
		t.Fatalf("cancel completed = %+v", errResp)
	}

	// A live task can be cancelled and reports the terminal state.
	live := h.h.Store().Create(model, a2a.NewMessage(a2a.RoleUser, a2a.NewTextPart("hello")))
	canceled := h.task(h.rpc(a2a.MethodTasksCancel, `{"id":"`+live.ID+`"}`))
	if canceled.Status.State != a2a.TaskStateCanceled {
		t.Fatalf("state = %s, want canceled", canceled.Status.State)
	}

	// Unknown tasks are reported with the A2A code, not a generic failure.
	missing := h.rpc(a2a.MethodTasksGet, `{"id":"task_does_not_exist"}`)
	if missing.Error == nil || missing.Error.Code != a2a.CodeTaskNotFound {
		t.Fatalf("tasks/get missing = %+v", missing)
	}
}

func TestE2EInputRequiredAndResume(t *testing.T) {
	h := newHarness(t)
	agent := resourceID(t, h.reg, registry.KindAgent)

	parked := h.send(agent, "please approve the transfer")
	if parked.Status.State != a2a.TaskStateInputRequired {
		t.Fatalf("state = %s, want input-required", parked.Status.State)
	}
	pending := h.h.Store().Pending(parked.ID)
	if pending == nil || !strings.HasPrefix(pending.Prompt, "approval_required:") {
		t.Fatalf("pending = %+v", pending)
	}
	if len(h.h.Store().Spans(parked.ID)) == 0 {
		t.Fatal("the parked run must still be traceable")
	}

	// The answer is fed back into the flow; it must not re-ask the question.
	resumed := h.task(h.rpc(a2a.MethodTasksResume, `{"id":"`+parked.ID+`","message":{"role":"user","parts":[{"type":"text","text":"approved"}]}}`))
	if resumed.Status.State != a2a.TaskStateCompleted {
		t.Fatalf("state = %s, want completed", resumed.Status.State)
	}
	if h.h.Store().Pending(parked.ID) != nil {
		t.Fatal("resuming must clear the pending question")
	}

	// Resuming something that is not parked is a protocol error.
	again := h.rpc(a2a.MethodTasksResume, `{"id":"`+parked.ID+`","message":{"role":"user","parts":[{"type":"text","text":"again"}]}}`)
	if again.Error == nil || again.Error.Code != a2a.CodeTaskNotResumable {
		t.Fatalf("second resume = %+v", again)
	}
}

func TestE2EStreaming(t *testing.T) {
	h := newHarness(t)
	mcp := resourceID(t, h.reg, registry.KindMCP)

	params := `{"message":{"role":"user","parts":[{"type":"text","text":"hello"}]},"metadata":{"resourceId":"` + mcp + `"}}`
	out, frames := h.stream(a2a.MethodMessageStream, params)

	if strings.Contains(out, "[DONE]") {
		t.Fatal("A2A streaming must not emit a sentinel frame")
	}
	if !strings.Contains(out, "event: task_status_update") {
		t.Fatalf("no status frame: %s", out)
	}
	if !strings.Contains(out, "event: task_artifact_update") {
		t.Fatalf("no artifact frame: %s", out)
	}
	if len(frames) < 3 {
		t.Fatalf("frames = %d, want at least a status, an artifact and the terminal frame", len(frames))
	}

	// The last frame ends the run: no further frame follows the terminal state.
	var lastState string
	for _, f := range frames {
		var resp a2a.Response
		if err := json.Unmarshal([]byte(f), &resp); err != nil {
			t.Fatalf("frame is not a JSON-RPC response: %v (%s)", err, f)
		}
		raw, _ := json.Marshal(resp.Result)
		var ev a2a.TaskStatusUpdateEvent
		if err := json.Unmarshal(raw, &ev); err == nil && ev.Kind == a2a.KindStatusUpdate {
			lastState = string(ev.Status.State)
			if ev.Final && ev.Status.State == a2a.TaskStateCompleted {
				lastState = "completed-final"
			}
		}
	}
	if lastState != "completed-final" {
		t.Fatalf("last status frame = %q, want a final completed frame", lastState)
	}
}

func TestE2EStreamingErrorFrame(t *testing.T) {
	h := newHarness(t)
	// A selector that resolves to nothing must surface as an error frame.
	params := `{"message":{"role":"user","parts":[{"type":"text","text":"hello"}]},"metadata":{"resourceId":"nope"}}`
	out, _ := h.stream(a2a.MethodMessageStream, params)
	if !strings.Contains(out, "event: error") {
		t.Fatalf("expected an error frame: %s", out)
	}
	if !strings.Contains(out, a2a.MsgResourceNotFound) {
		t.Fatalf("expected the machine readable code %q: %s", a2a.MsgResourceNotFound, out)
	}
}

func TestE2EFlowBranchAndParallel(t *testing.T) {
	h := newHarness(t)
	skill := resourceID(t, h.reg, registry.KindSkill)
	model := resourceID(t, h.reg, registry.KindModel)
	cli := resourceID(t, h.reg, registry.KindCLI)

	flow := `[` +
		`{"op":"branch","label":"route","steps":[` +
		`{"when":"contains:never","steps":[{"op":"then","resource":"` + cli + `"}]},` +
		`{"when":"contains:hello","steps":[{"op":"then","resource":"` + skill + `"}]}]},` +
		`{"op":"parallel","label":"fan-out","steps":[` +
		`{"op":"then","resource":"` + model + `"},` +
		`{"op":"then","resource":"` + cli + `"}]}]`
	params := `{"message":{"role":"user","parts":[{"type":"text","text":"hello"}]},"metadata":{"resourceId":"` + skill + `","flow":` + flow + `}}`

	task := h.task(h.rpc(a2a.MethodMessageSend, params))
	if task.Status.State != a2a.TaskStateCompleted {
		t.Fatalf("state = %s, want completed", task.Status.State)
	}
	if len(task.Artifacts) < 3 {
		t.Fatalf("artifacts = %d, want the branch plus both parallel members", len(task.Artifacts))
	}
	spans := h.h.Store().Spans(task.ID)
	if len(spans) < 4 {
		t.Fatalf("spans = %d, want branch + member spans", len(spans))
	}
	var labels []string
	for _, s := range spans {
		labels = append(labels, s.Label)
	}
	joined := strings.Join(labels, "|")
	for _, want := range []string{"route", "fan-out"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q span in %v", want, labels)
		}
	}
}

func TestE2ELegacyMethodAlias(t *testing.T) {
	h := newHarness(t)
	model := resourceID(t, h.reg, registry.KindModel)

	params := `{"message":{"role":"user","parts":[{"type":"text","text":"hello"}]},"metadata":{"resourceId":"` + model + `"}}`
	legacy := h.rpc(a2a.MethodTasksSend, params)
	if legacy.Error != nil {
		t.Fatalf("tasks/send: %+v", legacy.Error)
	}
	if h.task(legacy).Status.State != a2a.TaskStateCompleted {
		t.Fatalf("state = %s, want completed", h.task(legacy).Status.State)
	}

	// tasks/sendSubscribe is the streaming alias.
	_, frames := h.stream(a2a.MethodTasksSendSubscribe, params)
	if len(frames) == 0 {
		t.Fatal("no frames from the legacy streaming alias")
	}

	// A method that does not exist is reported, not silently accepted.
	unknown := h.rpc("tasks/nope", `{}`)
	if unknown.Error == nil || unknown.Error.Code != a2a.CodeMethodNotFound {
		t.Fatalf("unknown method = %+v", unknown)
	}
}

func TestE2EPushNotifications(t *testing.T) {
	h := newHarness(t)
	agent := resourceID(t, h.reg, registry.KindAgent)

	parked := h.send(agent, "please approve this")
	if parked.Status.State != a2a.TaskStateInputRequired {
		t.Fatalf("state = %s, want input-required", parked.Status.State)
	}

	cfgResp := h.rpc(a2a.MethodPushSet, `{"taskId":"`+parked.ID+`","pushNotificationConfig":{"url":"http://callback.example","token":"t"}}`)
	if cfgResp.Error != nil {
		t.Fatalf("push set: %+v", cfgResp.Error)
	}
	raw, _ := json.Marshal(cfgResp.Result)
	var cfg a2a.PushNotificationConfig
	if err := json.Unmarshal(raw, &cfg); err != nil || cfg.URL != "http://callback.example" {
		t.Fatalf("push set result = %s (%v)", raw, err)
	}
	if got := h.rpc(a2a.MethodPushGet, `{"id":"`+parked.ID+`"}`); got.Error != nil {
		t.Fatalf("push get: %+v", got.Error)
	}

	// Completing the task fires the registered callback.
	resumed := h.task(h.rpc(a2a.MethodTasksResume, `{"id":"`+parked.ID+`","message":{"role":"user","parts":[{"type":"text","text":"approved"}]}}`))
	if resumed.Status.State != a2a.TaskStateCompleted {
		t.Fatalf("state = %s, want completed", resumed.Status.State)
	}
	// The callback was registered after the task parked, so exactly one
	// notification is delivered: the terminal one.
	records := h.pusher.Records()
	if len(records) != 1 {
		t.Fatalf("push records = %+v, want exactly one", records)
	}
	if records[0].TaskID != parked.ID || records[0].URL != "http://callback.example" || records[0].State != a2a.TaskStateCompleted {
		t.Fatalf("record = %+v", records[0])
	}
}

// ---------------------------------------------------------------------------
// control plane drives the data plane
// ---------------------------------------------------------------------------

func TestE2EControlPlaneRoundTrip(t *testing.T) {
	h := newHarness(t)

	// Register a new resource through the console API ...
	res, raw := h.do(http.MethodPost, "/api/resources", `{"kind":"mcp","name":"e2e-mcp","version":"3.0.0","endpoint":"stdio://e2e","transport":"stdio","tags":["e2e"],"auth_schemes":["bearer"]}`)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create: status = %d (%s)", res.StatusCode, raw)
	}
	var created registry.Entry
	if err := json.Unmarshal(raw, &created); err != nil {
		t.Fatalf("decode created: %v", err)
	}
	if created.ID == "" || created.Kind != registry.KindMCP || created.Health != registry.HealthUnknown {
		t.Fatalf("created = %+v", created)
	}

	// ... it shows up in the listing ...
	var listing struct {
		Resources []registry.Entry `json:"resources"`
		Counts    map[string]int   `json:"counts"`
	}
	h.getJSON("/api/resources?kind=mcp", &listing)
	if listing.Counts["mcp"] != 2 {
		t.Fatalf("counts = %+v, want two mcp resources", listing.Counts)
	}

	// ... is exported as an MCP server description ...
	var export struct {
		Servers []registry.MCPServerDescription `json:"servers"`
	}
	h.getJSON("/api/export/mcp", &export)
	var found bool
	for _, s := range export.Servers {
		if s.Name == "e2e-mcp" && s.Version == "3.0.0" && s.Transport == registry.TransportStdio {
			found = true
		}
	}
	if !found {
		t.Fatalf("e2e-mcp missing from the export: %+v", export.Servers)
	}

	// ... appears on the Agent Card ...
	var card a2a.AgentCard
	h.getJSON("/api/export/agent-card", &card)
	var onCard bool
	for _, s := range card.Skills {
		if s.ID == created.ID {
			onCard = true
		}
	}
	if !onCard {
		t.Fatalf("e2e-mcp missing from the agent card: %+v", card.Skills)
	}

	// ... and can run a task immediately through the A2A data plane.
	task := h.send(created.ID, "hello from the console")
	if task.Status.State != a2a.TaskStateCompleted {
		t.Fatalf("state = %s, want completed", task.Status.State)
	}

	// The console task endpoints see the same run.
	var tasks struct {
		Tasks []a2a.Task `json:"tasks"`
	}
	h.getJSON("/api/tasks", &tasks)
	if len(tasks.Tasks) != 1 {
		t.Fatalf("tasks = %d, want 1", len(tasks.Tasks))
	}
	var spans struct {
		Spans []hub.Span `json:"spans"`
	}
	h.getJSON("/api/tasks/"+task.ID+"/spans", &spans)
	if len(spans.Spans) == 0 {
		t.Fatal("expected the span timeline of the run")
	}

	// Deleting the resource removes it from discovery.
	res, raw = h.do(http.MethodDelete, "/api/resources/"+created.ID, "")
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: status = %d (%s)", res.StatusCode, raw)
	}
	h.getJSON("/api/resources?kind=mcp", &listing)
	if listing.Counts["mcp"] != 1 {
		t.Fatalf("counts after delete = %+v", listing.Counts)
	}
}

func TestE2EControlPlaneRejectsBadInput(t *testing.T) {
	h := newHarness(t)

	for _, tc := range []struct {
		name string
		body string
		code string
	}{
		{"unknown kind", `{"kind":"robot","name":"x"}`, "invalid_kind"},
		{"unknown transport", `{"kind":"mcp","name":"x","transport":"telepathy"}`, "invalid_transport"},
		{"missing name", `{"kind":"cli","name":""}`, "invalid_entry"},
	} {
		res, raw := h.do(http.MethodPost, "/api/resources", tc.body)
		var body struct {
			Code string `json:"code"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatalf("%s: decode %s: %v", tc.name, raw, err)
		}
		if body.Code != tc.code {
			t.Errorf("%s: code = %q, want %q", tc.name, body.Code, tc.code)
		}
		if res.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", tc.name, res.StatusCode)
		}
	}

	// The data plane never leaks prose either: the code is the whole message.
	resp := h.rpc(a2a.MethodTasksGet, `{"id":"task_missing"}`)
	if resp.Error == nil {
		t.Fatal("expected an error")
	}
	if strings.ContainsAny(resp.Error.Message, " \t\n") {
		t.Fatalf("error message must be a machine readable token: %q", resp.Error.Message)
	}
}
