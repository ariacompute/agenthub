package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"agenthub/internal/authz"
	"agenthub/internal/config"
	"agenthub/internal/sandbox"
)

// The sandbox control plane is the one surface where an operator can start a
// process on a host, so the handlers are tested end to end against a real
// Service: a fake manager would let a wrong wiring pass.

func sandboxService(t *testing.T) *sandbox.Service {
	t.Helper()
	return sandboxServiceWith(t, sandbox.ArgsPassthrough)
}

// sandboxServiceWith lets one case run under the shipping default ("none") while
// the rest of the surface keeps the passthrough a console try-it needs.
func sandboxServiceWith(t *testing.T, argsPolicy string) *sandbox.Service {
	t.Helper()
	store := sandbox.NewMemoryStore()
	registry := sandbox.NewRegistry(sandbox.RegistryOptions{Default: sandbox.ProviderMock, Clock: time.Now})
	if err := registry.Register(sandbox.NewMockProvider()); err != nil {
		t.Fatalf("register mock: %v", err)
	}
	// One bus shared by both: the manager publishes, the facade replays.
	bus := sandbox.NewBus(store, nil)
	manager := sandbox.NewManager(sandbox.ManagerOptions{
		Registry: registry,
		Store:    store,
		Bus:      bus,
		Clock:    time.Now,
		// ArgsPolicy passthrough: the console's try-it endpoint sends argv, and
		// Exec now enforces the profile on every argument. The default "none"
		// is covered by the sandbox package's own policy cases.
		Defaults:    sandbox.Defaults{Provider: sandbox.ProviderMock, Network: sandbox.NetworkNone, TimeoutMS: 1000, MaxOutputBytes: 4096, ArgsPolicy: argsPolicy},
		IdleTTL:     10 * time.Minute,
		MaxTTL:      time.Hour,
		MaxPerOwner: 4, MaxTotal: 16,
	})
	service := sandbox.NewService(sandbox.ServiceOptions{
		Manager:  manager,
		Registry: registry,
		Bus:      bus,
		Queue:    sandbox.NewJobQueue(),
		Interval: time.Hour,
	})
	t.Cleanup(service.Stop)
	return service
}

func sandboxDeps(t *testing.T, cfg *config.Config) (Deps, *sandbox.Service) {
	t.Helper()
	if cfg == nil {
		cfg = openConfig("")
	}
	d := testDeps(t, cfg)
	d.Sandboxes = sandboxService(t)
	return d, d.Sandboxes
}

// sandboxRequest builds a request that already carries an identity and a path
// value, which is exactly what identify plus the router hand a handler. The
// middleware always installs the dev principal in this harness, so the ownership
// checks can only be reached by calling the handler directly.
func sandboxRequest(method, target, body string, p *authz.Principal, pathID string) *http.Request {
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, target, nil)
	} else {
		r = httptest.NewRequest(method, target, strings.NewReader(body))
	}
	r = withPrincipal(r, p)
	if pathID != "" {
		r.SetPathValue("id", pathID)
		r.SetPathValue("job", pathID)
	}
	return r
}

func sandboxRecorder() *httptest.ResponseRecorder { return httptest.NewRecorder() }

func errorCode(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Code string `json:"code"`
	}
	decodeJSON(t, w, &body)
	return body.Code
}

// AC L1: an instance without the control plane answers 503 on every sandbox
// route, never 404 — "not wired in" and "wrong URL" are different problems.
func TestSandboxRoutesReportFeatureUnavailable(t *testing.T) {
	h := Register(testDeps(t, openConfig(""))) // Sandboxes left nil
	cases := []struct {
		method, target, body string
	}{
		{http.MethodGet, "/api/sandboxes", ""},
		{http.MethodGet, "/api/sandboxes/stats", ""},
		{http.MethodGet, "/api/sandboxes/events", ""},
		{http.MethodGet, "/api/sandboxes/lease-1", ""},
		{http.MethodPost, "/api/sandboxes/lease-1/exec", `{"command":"echo"}`},
		{http.MethodPost, "/api/sandboxes/lease-1/renew", ""},
		{http.MethodDelete, "/api/sandboxes/lease-1", ""},
		{http.MethodGet, "/api/sandbox-providers", ""},
		{http.MethodPost, "/api/sandbox-providers/refresh", ""},
		{http.MethodGet, "/api/sandbox-nodes", ""},
		{http.MethodPost, "/api/sandbox-nodes", `{"id":"node-1"}`},
		{http.MethodDelete, "/api/sandbox-nodes/node-1", ""},
		{http.MethodPost, "/api/sandbox-nodes/node-1/heartbeat", ""},
		{http.MethodGet, "/api/sandbox-nodes/node-1/jobs", ""},
		{http.MethodPost, "/api/sandbox-nodes/node-1/jobs/job-1", `{}`},
	}
	for _, tc := range cases {
		w := do(t, h, tc.method, tc.target, tc.body, nil)
		if w.Code != http.StatusServiceUnavailable {
			t.Errorf("%s %s: status = %d, want 503", tc.method, tc.target, w.Code)
			continue
		}
		if code := errorCode(t, w); code != "feature_unavailable" {
			t.Errorf("%s %s: code = %q", tc.method, tc.target, code)
		}
	}
}

func TestSandboxProviderCatalogue(t *testing.T) {
	d, _ := sandboxDeps(t, nil)
	h := Register(d)

	w := do(t, h, http.MethodGet, "/api/sandbox-providers", "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", w.Code, w.Body.String())
	}
	var body struct {
		Providers []sandbox.ProviderInfo `json:"providers"`
		Groups    []sandboxLayerGroup    `json:"groups"`
		Layers    []sandbox.Layer        `json:"layers"`
		Default   string                 `json:"default"`
	}
	decodeJSON(t, w, &body)
	if len(body.Providers) != 1 || body.Providers[0].Name != sandbox.ProviderMock {
		t.Fatalf("providers = %+v", body.Providers)
	}
	if body.Default != sandbox.ProviderMock {
		t.Fatalf("default = %q", body.Default)
	}
	if len(body.Layers) != len(sandbox.Layers) {
		t.Fatalf("layers = %v", body.Layers)
	}
	// Empty layers are skipped so the console never renders a heading with
	// nothing under it.
	if len(body.Groups) != 1 || body.Groups[0].Layer != sandbox.LayerNone || len(body.Groups[0].Providers) != 1 {
		t.Fatalf("groups = %+v", body.Groups)
	}

	// Refresh is a POST because it spawns the runtime binaries.
	w = do(t, h, http.MethodPost, "/api/sandbox-providers/refresh", "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("refresh: status = %d (%s)", w.Code, w.Body.String())
	}
	var refreshed struct {
		Providers []sandbox.ProviderInfo `json:"providers"`
	}
	decodeJSON(t, w, &refreshed)
	if len(refreshed.Providers) != 1 {
		t.Fatalf("refreshed = %+v", refreshed.Providers)
	}
}

func TestSandboxLeaseLifecycle(t *testing.T) {
	d, service := sandboxDeps(t, nil)
	h := Register(d)
	ctx := context.Background()

	lease, err := service.Manager().Acquire(ctx, sandbox.LeaseRequest{OwnerID: "dev", ResourceID: "res-1"})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}

	w := do(t, h, http.MethodGet, "/api/sandboxes", "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("list: status = %d", w.Code)
	}
	var listing struct {
		Leases  []sandbox.Lease `json:"leases"`
		Default sandbox.Profile `json:"default"`
		Live    bool            `json:"live"`
	}
	decodeJSON(t, w, &listing)
	if len(listing.Leases) != 1 || listing.Leases[0].ID != lease.ID {
		t.Fatalf("leases = %+v", listing.Leases)
	}
	if listing.Default.Provider != sandbox.ProviderMock || listing.Live {
		t.Fatalf("listing = %+v", listing)
	}

	w = do(t, h, http.MethodGet, "/api/sandboxes/"+lease.ID, "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("get: status = %d", w.Code)
	}
	var detail struct {
		Lease sandbox.Lease        `json:"lease"`
		Execs []sandbox.ExecRecord `json:"execs"`
	}
	decodeJSON(t, w, &detail)
	if detail.Lease.ID != lease.ID || len(detail.Execs) != 0 {
		t.Fatalf("detail = %+v", detail)
	}

	// A whitespace command is not a command.
	w = do(t, h, http.MethodPost, "/api/sandboxes/"+lease.ID+"/exec", `{"command":"   "}`, nil)
	if w.Code != http.StatusBadRequest || errorCode(t, w) != "sandbox_policy_denied" {
		t.Fatalf("blank command: status = %d (%s)", w.Code, w.Body.String())
	}
	w = do(t, h, http.MethodPost, "/api/sandboxes/"+lease.ID+"/exec", `{"command":`, nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("malformed body: status = %d", w.Code)
	}

	w = do(t, h, http.MethodPost, "/api/sandboxes/"+lease.ID+"/exec", `{"command":"echo","args":["hi"],"timeoutMs":500}`, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("exec: status = %d (%s)", w.Code, w.Body.String())
	}
	var res sandbox.ExecResult
	decodeJSON(t, w, &res)
	if res.Provider != sandbox.ProviderMock {
		t.Fatalf("receipt = %+v", res)
	}

	// The receipt is persisted, so the audit drawer has something to show.
	w = do(t, h, http.MethodGet, "/api/sandboxes/"+lease.ID, "", nil)
	decodeJSON(t, w, &detail)
	if len(detail.Execs) != 1 {
		t.Fatalf("execs = %+v, want the recorded call", detail.Execs)
	}

	w = do(t, h, http.MethodPost, "/api/sandboxes/"+lease.ID+"/renew", "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("renew: status = %d (%s)", w.Code, w.Body.String())
	}
	var renewed sandbox.Lease
	decodeJSON(t, w, &renewed)
	// A renewal never shortens the deadline. "Strictly later" would be flaky:
	// Windows timers tick in ~15ms steps, so two calls can share a timestamp.
	if renewed.ExpiresAt.Before(lease.ExpiresAt) {
		t.Fatalf("renewed = %+v, want an expiry no earlier than %v", renewed.ExpiresAt, lease.ExpiresAt)
	}
	if renewed.State != sandbox.StateReady || renewed.Execs != 1 {
		t.Fatalf("renewed = %+v, want the same environment with its audit intact", renewed)
	}

	w = do(t, h, http.MethodGet, "/api/sandboxes/nope", "", nil)
	if w.Code != http.StatusNotFound || errorCode(t, w) != "sandbox_not_found" {
		t.Fatalf("unknown lease: status = %d (%s)", w.Code, w.Body.String())
	}

	if w = do(t, h, http.MethodDelete, "/api/sandboxes/"+lease.ID, "", nil); w.Code != http.StatusNoContent {
		t.Fatalf("delete: status = %d (%s)", w.Code, w.Body.String())
	}
	// Destroying is idempotent: the desired state (no environment) already holds,
	// so a retried DELETE is not a client error either.
	if w = do(t, h, http.MethodDelete, "/api/sandboxes/"+lease.ID, "", nil); w.Code != http.StatusNoContent {
		t.Fatalf("delete again: status = %d (%s)", w.Code, w.Body.String())
	}
	// The row survives as a terminated lease so the audit drawer still has it.
	w = do(t, h, http.MethodGet, "/api/sandboxes/"+lease.ID, "", nil)
	decodeJSON(t, w, &detail)
	if w.Code != http.StatusOK || detail.Lease.State != sandbox.StateTerminated {
		t.Fatalf("after release: status = %d lease = %+v", w.Code, detail.Lease)
	}
	// Exec on a released lease is a conflict, not a 404: the URL is right, the
	// environment is gone.
	w = do(t, h, http.MethodPost, "/api/sandboxes/"+lease.ID+"/exec", `{"command":"echo"}`, nil)
	if w.Code != http.StatusConflict || errorCode(t, w) != "sandbox_lease_lost" {
		t.Fatalf("exec after release: status = %d (%s)", w.Code, w.Body.String())
	}
}

// A lease belongs to the identity that created it; a stranger gets 404 so the
// existence of somebody else's environment is not disclosed. The middleware
// always resolves the dev principal in this harness, so the identity is injected
// on the request and the handlers are called directly: that is exactly what
// identify hands them in production.
func TestSandboxLeaseOwnership(t *testing.T) {
	d, service := sandboxDeps(t, nil)
	ctx := context.Background()

	lease, err := service.Manager().Acquire(ctx, sandbox.LeaseRequest{OwnerID: "dev", ResourceID: "res-1"})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	stranger := &authz.Principal{UserID: "someone-else"}

	w := sandboxRecorder()
	r := sandboxRequest(http.MethodGet, "/api/sandboxes/"+lease.ID, "", stranger, lease.ID)
	d.handleGetSandbox(w, r)
	if w.Code != http.StatusNotFound || errorCode(t, w) != "sandbox_not_found" {
		t.Fatalf("get: status = %d (%s)", w.Code, w.Body.String())
	}
	if _, ok := d.sandboxLease(sandboxRecorder(), r); ok {
		t.Fatal("sandboxLease must refuse another owner's lease")
	}

	w = sandboxRecorder()
	d.handleExecSandbox(w, sandboxRequest(http.MethodPost, "/api/sandboxes/"+lease.ID+"/exec", `{"command":"echo"}`, stranger, lease.ID))
	if w.Code != http.StatusNotFound {
		t.Fatalf("exec: status = %d (%s)", w.Code, w.Body.String())
	}
	w = sandboxRecorder()
	d.handleRenewSandbox(w, sandboxRequest(http.MethodPost, "/api/sandboxes/"+lease.ID+"/renew", "", stranger, lease.ID))
	if w.Code != http.StatusNotFound {
		t.Fatalf("renew: status = %d (%s)", w.Code, w.Body.String())
	}
	w = sandboxRecorder()
	d.handleTerminateSandbox(w, sandboxRequest(http.MethodDelete, "/api/sandboxes/"+lease.ID, "", stranger, lease.ID))
	if w.Code != http.StatusNotFound {
		t.Fatalf("terminate: status = %d (%s)", w.Code, w.Body.String())
	}

	// The owner still sees the lease untouched by those refusals.
	w = sandboxRecorder()
	d.handleGetSandbox(w, sandboxRequest(http.MethodGet, "/api/sandboxes/"+lease.ID, "", &authz.Principal{UserID: "dev"}, lease.ID))
	if w.Code != http.StatusOK {
		t.Fatalf("owner: status = %d (%s)", w.Code, w.Body.String())
	}
}

func TestSandboxStatsAndEventReplay(t *testing.T) {
	d, service := sandboxDeps(t, nil)
	h := Register(d)
	ctx := context.Background()

	if _, _, err := service.Manager().ExecOn(ctx, sandbox.LeaseRequest{OwnerID: "dev", ResourceID: "res-1"},
		sandbox.ExecRequest{Command: "echo"}); err != nil {
		t.Fatalf("exec on: %v", err)
	}

	w := do(t, h, http.MethodGet, "/api/sandboxes/stats", "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("stats: status = %d", w.Code)
	}
	var stats sandbox.Stats
	decodeJSON(t, w, &stats)
	// ExecOn keeps an owned lease, so the water level shows one live environment
	// and at least the call that just ran.
	if stats.ActiveLeases != 1 || stats.RecentExecs == 0 {
		t.Fatalf("stats = %+v", stats)
	}

	// The stream replays what happened while nobody was listening, then parks.
	reqCtx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	r := httptest.NewRequest(http.MethodGet, "/api/sandboxes/events", nil).WithContext(reqCtx)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)

	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type = %q", ct)
	}
	out := rec.Body.String()
	if !strings.Contains(out, "event: sandbox_event") || !strings.Contains(out, `"kind"`) {
		t.Fatalf("replay = %s", out)
	}
}

func TestSandboxNodeEnrollment(t *testing.T) {
	d, service := sandboxDeps(t, nil)
	h := Register(d)
	ctx := context.Background()

	// A blank name falls back to the id, and capabilities are trimmed and
	// de-duplicated so the console list stays stable.
	w := do(t, h, http.MethodPost, "/api/sandbox-nodes",
		`{"id":"node.a-1","name":"  ","capabilities":["docker"," docker ","", " bwrap "]}`, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("register: status = %d (%s)", w.Code, w.Body.String())
	}
	var node sandbox.Node
	decodeJSON(t, w, &node)
	if node.ID != "node.a-1" || node.Name != "node.a-1" || node.Status != sandbox.NodeOnline {
		t.Fatalf("node = %+v", node)
	}
	if len(node.Capabilities) != 2 || node.Capabilities[0] != "docker" || node.Capabilities[1] != "bwrap" {
		t.Fatalf("capabilities = %v", node.Capabilities)
	}
	if node.OwnerID != "dev" {
		t.Fatalf("owner = %q, want the enrolling identity", node.OwnerID)
	}

	// Re-enrolling the same machine id is an update, not a second node.
	w = do(t, h, http.MethodPost, "/api/sandbox-nodes", `{"id":"node.a-1","name":"workstation"}`, nil)
	decodeJSON(t, w, &node)
	if node.Name != "workstation" {
		t.Fatalf("node = %+v, want the refreshed name", node)
	}
	if got, err := service.Node(ctx, "node.a-1"); err != nil || got.Name != "workstation" {
		t.Fatalf("stored node = %+v (%v)", got, err)
	}

	// A machine that names no provider still accepts node-routed work.
	w = do(t, h, http.MethodPost, "/api/sandbox-nodes", `{"id":"node-2"}`, nil)
	decodeJSON(t, w, &node)
	if len(node.Capabilities) != 1 || node.Capabilities[0] != "remote" {
		t.Fatalf("capabilities = %v, want the remote default", node.Capabilities)
	}

	// A bad identity is a malformed request, not a policy refusal: the desktop
	// must not go looking for a sandbox_policy_denied rule that does not exist.
	for _, tc := range []struct {
		name string
		body string
	}{
		{"empty id", `{"id":"   "}`},
		{"path characters", `{"id":"node/../etc"}`},
		{"too long", `{"id":"` + strings.Repeat("a", 65) + `"}`},
		{"unexpected field", `{"id":"node-3","nope":1}`},
		{"not json", `{"id":`},
	} {
		w := do(t, h, http.MethodPost, "/api/sandbox-nodes", tc.body, nil)
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400 (%s)", tc.name, w.Code, w.Body.String())
			continue
		}
		if code := errorCode(t, w); code != "bad_request" {
			t.Errorf("%s: code = %q, want bad_request", tc.name, code)
		}
	}

	w = do(t, h, http.MethodGet, "/api/sandbox-nodes", "", nil)
	var fleet struct {
		Nodes    []sandbox.Node `json:"nodes"`
		PollHint string         `json:"pollHint"`
	}
	decodeJSON(t, w, &fleet)
	if len(fleet.Nodes) != 2 {
		t.Fatalf("nodes = %+v", fleet.Nodes)
	}
	// The default long poll stays under a typical 30s proxy idle timeout.
	if fleet.PollHint != "25s" {
		t.Fatalf("poll hint = %q, want 25s", fleet.PollHint)
	}
}

func TestSandboxNodeOwnershipCannotBeHijacked(t *testing.T) {
	d, _ := sandboxDeps(t, nil)
	stranger := &authz.Principal{UserID: "someone-else"}

	if w := do(t, Register(d), http.MethodPost, "/api/sandbox-nodes", `{"id":"node-1"}`, nil); w.Code != http.StatusOK {
		t.Fatalf("register: status = %d (%s)", w.Code, w.Body.String())
	}
	// Re-registering somebody else's machine id must not transfer it.
	w := sandboxRecorder()
	d.handleRegisterSandboxNode(w, sandboxRequest(http.MethodPost, "/api/sandbox-nodes", `{"id":"node-1"}`, stranger, ""))
	if w.Code != http.StatusNotFound || errorCode(t, w) != "sandbox_not_found" {
		t.Fatalf("hijack: status = %d (%s)", w.Code, w.Body.String())
	}

	// Every node-scoped route resolves through sandboxNode, so they all refuse.
	handlers := map[string]func(http.ResponseWriter, *http.Request){
		"heartbeat": d.handleNodeHeartbeat,
		"claim":     d.handleClaimNodeJob,
		"report":    d.handleReportNodeJob,
		"remove":    d.handleRemoveSandboxNode,
	}
	for name, handler := range handlers {
		w := sandboxRecorder()
		handler(w, sandboxRequest(http.MethodPost, "/api/sandbox-nodes/node-1", `{"exitCode":0}`, stranger, "node-1"))
		if w.Code != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404", name, w.Code)
		}
		if _, ok := d.sandboxNode(sandboxRecorder(), sandboxRequest(http.MethodGet, "/api/sandbox-nodes/node-1", "", stranger, "node-1")); ok {
			t.Errorf("%s: sandboxNode must refuse another owner's node", name)
		}
	}
}

func TestSandboxNodeHeartbeatAndClaim(t *testing.T) {
	// A short poll budget keeps the "nobody has work" answer cheap here; the
	// production default is 25s precisely because an idle node should park.
	cfg := openConfig("")
	cfg.SandboxNodePollTimeout = 50 * time.Millisecond
	d, service := sandboxDeps(t, cfg)
	h := Register(d)

	if w := do(t, h, http.MethodPost, "/api/sandbox-nodes", `{"id":"node-1","capabilities":["docker"]}`, nil); w.Code != http.StatusOK {
		t.Fatalf("register: status = %d", w.Code)
	}

	w := do(t, h, http.MethodPost, "/api/sandbox-nodes/node-1/heartbeat", "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("heartbeat: status = %d (%s)", w.Code, w.Body.String())
	}
	var beat struct {
		Node     sandbox.Node `json:"node"`
		PollHint string       `json:"pollHint"`
	}
	decodeJSON(t, w, &beat)
	if beat.Node.ID != "node-1" || beat.PollHint == "" {
		t.Fatalf("heartbeat = %+v", beat)
	}

	// An idle poll answers 204 so the node can simply poll again.
	if w := do(t, h, http.MethodGet, "/api/sandbox-nodes/node-1/jobs?wait=0", "", nil); w.Code != http.StatusNoContent {
		t.Fatalf("idle claim: status = %d (%s)", w.Code, w.Body.String())
	}
	// A malformed wait is ignored rather than failing the poll.
	if w := do(t, h, http.MethodGet, "/api/sandbox-nodes/node-1/jobs?wait=soon", "", nil); w.Code != http.StatusNoContent {
		t.Fatalf("bad wait: status = %d", w.Code)
	}

	service.Queue().Enqueue(&sandbox.Job{ID: "job-1", LeaseID: "lease-1", Provider: "docker", Op: sandbox.JobExec})
	w = do(t, h, http.MethodGet, "/api/sandbox-nodes/node-1/jobs?wait=1", "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("claim: status = %d (%s)", w.Code, w.Body.String())
	}
	var claimed sandbox.Job
	decodeJSON(t, w, &claimed)
	if claimed.ID != "job-1" || claimed.State != sandbox.JobClaimed || claimed.NodeID != "node-1" {
		t.Fatalf("claimed = %+v", claimed)
	}

	// Meanwhile a job routed to a provider this node does not advertise is not
	// handed out: capabilities are a filter, not a suggestion.
	service.Queue().Enqueue(&sandbox.Job{ID: "job-2", Provider: "bwrap", Op: sandbox.JobExec})
	if w := do(t, h, http.MethodGet, "/api/sandbox-nodes/node-1/jobs?wait=0", "", nil); w.Code != http.StatusNoContent {
		t.Fatalf("ineligible claim: status = %d (%s)", w.Code, w.Body.String())
	}
}

func TestSandboxNodeJobReport(t *testing.T) {
	d, service := sandboxDeps(t, nil)
	h := Register(d)

	if w := do(t, h, http.MethodPost, "/api/sandbox-nodes", `{"id":"node-1"}`, nil); w.Code != http.StatusOK {
		t.Fatalf("register: status = %d", w.Code)
	}
	if w := do(t, h, http.MethodPost, "/api/sandbox-nodes", `{"id":"node-2"}`, nil); w.Code != http.StatusOK {
		t.Fatalf("register node-2: status = %d", w.Code)
	}
	done := service.Queue().Enqueue(&sandbox.Job{ID: "job-1", LeaseID: "lease-1", Provider: "docker", Op: sandbox.JobExec})
	service.Queue().Enqueue(&sandbox.Job{ID: "job-2", Provider: "docker", Op: sandbox.JobExec})

	// Unknown job.
	if w := do(t, h, http.MethodPost, "/api/sandbox-nodes/node-1/jobs/nope", `{"exitCode":0}`, nil); w.Code != http.StatusNotFound {
		t.Fatalf("unknown job: status = %d (%s)", w.Code, w.Body.String())
	}
	// Claim it first, so the report has an owning node to check against.
	if _, ok := service.Queue().Claim("node-1", []string{"remote"}, 0); !ok {
		t.Fatal("node-1 must be able to claim its job")
	}
	// One node must not complete another node's job.
	if w := do(t, h, http.MethodPost, "/api/sandbox-nodes/node-2/jobs/job-1", `{"exitCode":0}`, nil); w.Code != http.StatusNotFound {
		t.Fatalf("cross-node report: status = %d (%s)", w.Code, w.Body.String())
	}
	// A malformed report is a client error and leaves the job claimed.
	if w := do(t, h, http.MethodPost, "/api/sandbox-nodes/node-1/jobs/job-1", `{"exitCode":`, nil); w.Code != http.StatusBadRequest {
		t.Fatalf("malformed report: status = %d", w.Code)
	}

	w := do(t, h, http.MethodPost, "/api/sandbox-nodes/node-1/jobs/job-1",
		`{"exitCode":0,"stdout":"hi","stderr":"","durationMs":12,"receipt":"exit"}`, nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"ok":true`) {
		t.Fatalf("report: status = %d (%s)", w.Code, w.Body.String())
	}
	select {
	case <-done:
	default:
		t.Fatal("the caller waiting on the job must be woken")
	}
	stored, _ := service.Queue().Get("job-1")
	if stored.State != sandbox.JobDone || stored.Result.Stdout != "hi" || stored.FinishedAt == nil {
		t.Fatalf("stored job = %+v", stored)
	}
	if stored.Result.Provider != "docker" || stored.Result.Ref != "lease-1" {
		t.Fatalf("the receipt must name the provider and the lease: %+v", stored.Result)
	}

	// A reported error marks the job failed instead of done.
	service.Queue().Claim("node-1", []string{"docker"}, 0)
	w = do(t, h, http.MethodPost, "/api/sandbox-nodes/node-1/jobs/job-2", `{"exitCode":1,"error":" boom "}`, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("failed report: status = %d (%s)", w.Code, w.Body.String())
	}
	failed, _ := service.Queue().Get("job-2")
	if failed.State != sandbox.JobFailed || failed.Error != "boom" {
		t.Fatalf("failed job = %+v", failed)
	}
}

func TestSandboxNodeRevocation(t *testing.T) {
	d, service := sandboxDeps(t, nil)
	h := Register(d)

	if w := do(t, h, http.MethodPost, "/api/sandbox-nodes", `{"id":"node-1"}`, nil); w.Code != http.StatusOK {
		t.Fatalf("register: status = %d", w.Code)
	}
	if w := do(t, h, http.MethodDelete, "/api/sandbox-nodes/node-1", "", nil); w.Code != http.StatusNoContent {
		t.Fatalf("delete: status = %d (%s)", w.Code, w.Body.String())
	}
	if _, err := service.Node(context.Background(), "node-1"); err == nil {
		t.Fatal("a revoked node must be gone")
	}
	// The lease that was waiting on it is not destroyed here: it fails its next
	// call and rebuilds. Revoking twice is simply 404.
	if w := do(t, h, http.MethodDelete, "/api/sandbox-nodes/node-1", "", nil); w.Code != http.StatusNotFound {
		t.Fatalf("delete again: status = %d", w.Code)
	}
}

func TestSandboxPollHintIsClamped(t *testing.T) {
	cases := []struct {
		name string
		cfg  *config.Config
		want time.Duration
	}{
		{"unset", nil, 25 * time.Second},
		{"configured", &config.Config{SandboxNodePollTimeout: 5 * time.Second}, 5 * time.Second},
		{"above the proxy idle timeout", &config.Config{SandboxNodePollTimeout: 2 * time.Minute}, 60 * time.Second},
	}
	for _, tc := range cases {
		d := Deps{Cfg: tc.cfg}
		if got := d.nodePollTimeout(); got != tc.want {
			t.Errorf("%s: poll timeout = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestSandboxNodeHelpers(t *testing.T) {
	for _, tc := range []struct {
		id   string
		want bool
	}{
		{"node-1", true},
		{"node.a_1", true},
		{"A9", true},
		{"", false},
		{strings.Repeat("a", 64), true},
		{strings.Repeat("a", 65), false},
		{"node 1", false},
		{"node/1", false},
		{"nöde", false},
	} {
		if got := validNodeID(tc.id); got != tc.want {
			t.Errorf("validNodeID(%q) = %v, want %v", tc.id, got, tc.want)
		}
	}

	for _, tc := range []struct {
		name string
		in   []string
		want []string
	}{
		{"empty means remote only", nil, []string{"remote"}},
		{"blank collapses to remote", []string{"", "  "}, []string{"remote"}},
		{"trimmed and de-duplicated", []string{" docker ", "docker", "bwrap"}, []string{"docker", "bwrap"}},
	} {
		got := normalizeCapabilities(tc.in)
		if len(got) != len(tc.want) {
			t.Fatalf("%s: %v, want %v", tc.name, got, tc.want)
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Fatalf("%s: %v, want %v", tc.name, got, tc.want)
			}
		}
	}

	if groups := groupByLayer(nil); len(groups) != 0 {
		t.Fatalf("an empty catalogue renders no headings, got %+v", groups)
	}
}

// The try-it endpoint used to hand caller supplied argv straight to the
// provider: the lease said "no arguments" and the endpoint sent them anyway.
// Exec is the funnel that now enforces it, so the refusal surfaces here as a
// policy code instead of a process starting.
func TestTheExecEndpointCannotSmuggleArgumentsPastThePolicy(t *testing.T) {
	d := testDeps(t, openConfig(""))
	d.Sandboxes = sandboxServiceWith(t, sandbox.ArgsNone)
	lease, err := d.Sandboxes.Manager().Acquire(context.Background(), sandbox.LeaseRequest{
		OwnerID: "dev", Provider: sandbox.ProviderMock,
	})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	caller := principalAs("dev", authz.PermResourcesRead, authz.PermResourcesWrite)

	// No arguments is what a "none" policy leaves room for.
	w := sandboxRecorder()
	d.handleExecSandbox(w, sandboxRequest(http.MethodPost, "/api/sandboxes/x/exec", `{"command":"ls"}`, caller, lease.ID))
	if w.Code != http.StatusOK {
		t.Fatalf("bare command: status = %d (%s)", w.Code, w.Body.String())
	}

	// An argument that rewrites the command is refused here, not deep inside a
	// provider: the endpoint is the door every caller shares.
	w = sandboxRecorder()
	d.handleExecSandbox(w, sandboxRequest(http.MethodPost, "/api/sandboxes/x/exec",
		`{"command":"ls","args":["-c","cat /etc/shadow"]}`, caller, lease.ID))
	if w.Code != http.StatusForbidden {
		t.Fatalf("smuggled argument: status = %d (%s), want 403", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), string(sandbox.CodePolicyDenied)) {
		t.Fatalf("body = %s, want %q", w.Body.String(), sandbox.CodePolicyDenied)
	}
}
