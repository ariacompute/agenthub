package tests

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"agenthub/internal/adapter"
	"agenthub/internal/config"
	"agenthub/internal/handlers"
	"agenthub/internal/hub"
	"agenthub/internal/registry"
	"agenthub/internal/sandbox"
)

// sandboxHarness boots the execution control plane the way cmd/agenthub-server
// does — memory store, mock provider — and drives it over real HTTP. The point is
// the whole chain: a lease appears, a command runs inside it, the audit row says
// so, and revoking it closes the environment without failing anything.
type sandboxHarness struct {
	t      *testing.T
	server *httptest.Server
	svc    *sandbox.Service
}

func newSandboxHarness(t *testing.T) *sandboxHarness {
	t.Helper()
	cfg := sandboxTestConfig(t)
	registryStore := registry.New()

	h := hub.New(cfg, registryStore, adapter.Options{
		CLITimeout:     cfg.CLITimeout,
		CLIAllowlist:   cfg.CLIAllowlist,
		MaxOutputBytes: cfg.MaxOutputBytes,
	}, &hub.MockNotifier{}, hub.Persistence{})
	t.Cleanup(h.Close)

	svc := sandboxTestService(t, cfg)
	server := httptest.NewServer(handlers.Register(handlers.Deps{
		Cfg:       cfg,
		Hub:       h,
		Reg:       registryStore,
		Log:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		Sandboxes: svc,
	}))
	t.Cleanup(server.Close)

	return &sandboxHarness{t: t, server: server, svc: svc}
}

func sandboxTestConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg := &config.Config{
		HTTPPort:        "3500",
		Env:             "dev",
		StaticDir:       t.TempDir(),
		SessionTTL:      time.Hour,
		LegacyMethods:   true,
		MaxParallel:     4,
		CLITimeout:      time.Second,
		CLIAllowlist:    []string{"echo"},
		MaxOutputBytes:  4096,
		EventBuffer:     64,
		DatabaseURL:     testDatabaseURL,
		PushRecordLimit: 100,

		// The control plane under test. mock is configured explicitly, never
		// selected as a fallback: the point of the default being docker is that a
		// host without it refuses rather than pretends.
		SandboxEnabled:         true,
		SandboxProvider:        sandbox.ProviderMock,
		SandboxDefaultImage:    "alpine:3",
		SandboxIdleTTL:         10 * time.Minute,
		SandboxMaxTTL:          time.Hour,
		SandboxMaxPerOwner:     4,
		SandboxMaxTotal:        8,
		SandboxNodePollTimeout: 2 * time.Second,
		SandboxSweepInterval:   time.Second,
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("config: %v", err)
	}
	return cfg
}

func sandboxTestService(t *testing.T, cfg *config.Config) *sandbox.Service {
	t.Helper()
	registryStore := sandbox.NewRegistry(sandbox.RegistryOptions{Default: cfg.SandboxProvider, Clock: time.Now})
	if err := registryStore.Register(sandbox.NewMockProvider()); err != nil {
		t.Fatalf("register mock: %v", err)
	}
	store := sandbox.NewMemoryStore()
	bus := sandbox.NewBus(store, time.Now)
	manager := sandbox.NewManager(sandbox.ManagerOptions{
		Registry: registryStore,
		Store:    store,
		// Passthrough because the console's try-it endpoint sends argv and the
		// case below runs one. Exec enforces the profile on every argument, so a
		// "none" policy would refuse the request before the provider runs.
		Defaults:    sandbox.Defaults{Provider: cfg.SandboxProvider, Network: sandbox.NetworkNone, TimeoutMS: 1000, MaxOutputBytes: 4096, ArgsPolicy: sandbox.ArgsPassthrough, IdleTTL: cfg.SandboxIdleTTL},
		IdleTTL:     cfg.SandboxIdleTTL,
		MaxTTL:      cfg.SandboxMaxTTL,
		MaxPerOwner: cfg.SandboxMaxPerOwner,
		MaxTotal:    cfg.SandboxMaxTotal,
		Bus:         bus,
		Clock:       time.Now,
	})
	svc := sandbox.NewService(sandbox.ServiceOptions{
		Manager:  manager,
		Registry: registryStore,
		Bus:      bus,
		Queue:    sandbox.NewJobQueue(),
		Interval: cfg.SandboxSweepInterval,
		Clock:    time.Now,
	})
	t.Cleanup(svc.Stop)
	return svc
}

func (h *sandboxHarness) do(method, path, body string) (int, []byte) {
	h.t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, h.server.URL+path, reader)
	if err != nil {
		h.t.Fatalf("request: %v", err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := h.server.Client().Do(req)
	if err != nil {
		h.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close()
	payload, err := io.ReadAll(res.Body)
	if err != nil {
		h.t.Fatalf("read body: %v", err)
	}
	return res.StatusCode, payload
}

func (h *sandboxHarness) json(method, path, body string) (int, map[string]any) {
	h.t.Helper()
	status, payload := h.do(method, path, body)
	out := map[string]any{}
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &out); err != nil {
			h.t.Fatalf("%s %s: body is not JSON: %s", method, path, payload)
		}
	}
	return status, out
}

// lease creates an environment owned by the dev principal, which is who the HTTP
// caller resolves to when no auth service is wired in. It goes through the service
// rather than through /api/computers because the computer row needs a database,
// and this suite is about the control plane, not persistence.
func (h *sandboxHarness) lease() string {
	h.t.Helper()
	lease, err := h.svc.Acquire(context.Background(), sandbox.LeaseRequest{
		OwnerID: "dev", ResourceID: "agent-1", Scope: "ticket-1",
	})
	if err != nil {
		h.t.Fatalf("acquire: %v", err)
	}
	if lease.ID == "" {
		h.t.Fatal("a lease without an id cannot be addressed")
	}
	return lease.ID
}

func TestSandboxConsoleEndpoints(t *testing.T) {
	h := newSandboxHarness(t)

	status, list := h.json(http.MethodGet, "/api/sandboxes", "")
	if status != http.StatusOK {
		t.Fatalf("list: %d", status)
	}
	if _, ok := list["default"]; !ok {
		t.Fatal("the list must carry the resolved instance defaults")
	}

	leaseID := h.lease()

	// The catalogue is grouped by layer, and the default is marked.
	status, providers := h.json(http.MethodGet, "/api/sandbox-providers", "")
	if status != http.StatusOK {
		t.Fatalf("providers: %d", status)
	}
	if providers["default"] != sandbox.ProviderMock {
		t.Fatalf("expected the mock as default, got %v", providers["default"])
	}
	groups, _ := providers["groups"].([]any)
	if len(groups) == 0 {
		t.Fatal("the catalogue must be grouped by isolation layer")
	}

	// The lease detail carries its audit trail.
	status, detail := h.json(http.MethodGet, "/api/sandboxes/"+leaseID, "")
	if status != http.StatusOK {
		t.Fatalf("detail: %d %v", status, detail)
	}
	if _, ok := detail["execs"]; !ok {
		t.Fatal("the detail must include the audit trail")
	}

	// A console probe runs through the control plane, not through the scheduler.
	status, receipt := h.json(http.MethodPost, "/api/sandboxes/"+leaseID+"/exec", `{"command":"echo","args":["hi"]}`)
	if status != http.StatusOK {
		t.Fatalf("exec: %d %v", status, receipt)
	}
	if receipt["provider"] != sandbox.ProviderMock {
		t.Fatalf("the receipt must name the provider: %v", receipt)
	}

	status, refreshed := h.json(http.MethodPost, "/api/sandboxes/"+leaseID+"/renew", "")
	if status != http.StatusOK {
		t.Fatalf("renew: %d %v", status, refreshed)
	}

	status, stats := h.json(http.MethodGet, "/api/sandboxes/stats", "")
	if status != http.StatusOK {
		t.Fatalf("stats: %d", status)
	}
	if stats["activeLeases"].(float64) < 1 {
		t.Fatalf("expected at least one active lease, got %v", stats["activeLeases"])
	}
	if stats["recentExecs"].(float64) < 1 {
		t.Fatalf("expected the probe to count as a recent run, got %v", stats["recentExecs"])
	}

	if status, _ := h.do(http.MethodDelete, "/api/sandboxes/"+leaseID, ""); status != http.StatusNoContent {
		t.Fatalf("terminate: %d", status)
	}
	// Still addressable on purpose: the console keeps the drawer open on it, and
	// the audit trail it just wrote is the answer to "what did that run do".
	status, gone := h.json(http.MethodGet, "/api/sandboxes/"+leaseID, "")
	if status != http.StatusOK {
		t.Fatalf("expected the terminated lease to stay addressable, got %d", status)
	}
	closed, _ := gone["lease"].(map[string]any)
	if closed["state"] != sandbox.StateTerminated {
		t.Fatalf("expected %s, got %v", sandbox.StateTerminated, closed["state"])
	}
}

func TestSandboxExecRefusesAnEmptyCommand(t *testing.T) {
	h := newSandboxHarness(t)
	leaseID := h.lease()

	status, body := h.json(http.MethodPost, "/api/sandboxes/"+leaseID+"/exec", `{"command":"  "}`)
	if status != http.StatusBadRequest {
		t.Fatalf("expected a refusal, got %d %v", status, body)
	}
	if body["code"] != string(sandbox.CodePolicyDenied) {
		t.Fatalf("expected %s, got %v", sandbox.CodePolicyDenied, body["code"])
	}
}

func TestSandboxUnknownLeaseIsNotFound(t *testing.T) {
	h := newSandboxHarness(t)
	status, body := h.json(http.MethodGet, "/api/sandboxes/does-not-exist", "")
	if status != http.StatusNotFound {
		t.Fatalf("expected 404, got %d %v", status, body)
	}
	if body["code"] != string(sandbox.CodeNotFound) {
		t.Fatalf("expected %s, got %v", sandbox.CodeNotFound, body["code"])
	}
}

func TestAnotherOwnersLeaseIsInvisible(t *testing.T) {
	h := newSandboxHarness(t)
	// The HTTP caller is the dev principal; this lease belongs to somebody else.
	stranger, err := h.svc.Acquire(t.Context(), sandbox.LeaseRequest{
		OwnerID: "stranger", ResourceID: "agent-9", Scope: "theirs",
	})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	status, body := h.json(http.MethodGet, "/api/sandboxes/"+stranger.ID, "")
	// 404, not 403: whether somebody else's environment exists is not information
	// this caller needs.
	if status != http.StatusNotFound {
		t.Fatalf("expected 404, got %d %v", status, body)
	}
}

func TestSandboxEndpointsAreUnavailableWhenNotWired(t *testing.T) {
	cfg := sandboxTestConfig(t)
	server := httptest.NewServer(handlers.Register(handlers.Deps{
		Cfg: cfg,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}))
	t.Cleanup(server.Close)

	for _, path := range []string{"/api/sandboxes", "/api/sandboxes/stats", "/api/sandbox-providers", "/api/sandbox-nodes"} {
		req, _ := http.NewRequest(http.MethodGet, server.URL+path, nil)
		res, err := server.Client().Do(req)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		res.Body.Close()
		// 503 says "this instance does not have the feature"; 404 would suggest the
		// URL is wrong and send an operator looking for a newer server.
		if res.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("%s: expected 503, got %d", path, res.StatusCode)
		}
	}
}

func TestExecutionNodeEnrollsClaimsAndReports(t *testing.T) {
	h := newSandboxHarness(t)

	status, node := h.json(http.MethodPost, "/api/sandbox-nodes", `{"id":"node-1","name":"laptop","capabilities":["remote"]}`)
	if status != http.StatusOK {
		t.Fatalf("enroll: %d %v", status, node)
	}
	if node["id"] != "node-1" {
		t.Fatalf("expected the enrolled id back, got %v", node["id"])
	}

	status, listed := h.json(http.MethodGet, "/api/sandbox-nodes", "")
	if status != http.StatusOK {
		t.Fatalf("list nodes: %d", status)
	}
	if len(listed["nodes"].([]any)) != 1 {
		t.Fatalf("expected one node, got %v", listed["nodes"])
	}

	// An idle poll is an empty 204, not an error.
	status, _ = h.do(http.MethodGet, "/api/sandbox-nodes/node-1/jobs?wait=0", "")
	if status != http.StatusNoContent {
		t.Fatalf("idle poll: expected 204, got %d", status)
	}

	// Queued work is handed to the node that can run it.
	jobDone := h.svc.Queue().Enqueue(&sandbox.Job{ID: "job-1", Provider: sandbox.ProviderMock, Op: sandbox.JobExec})
	status, claimed := h.json(http.MethodGet, "/api/sandbox-nodes/node-1/jobs?wait=1", "")
	if status != http.StatusOK {
		t.Fatalf("claim: %d %v", status, claimed)
	}
	if claimed["id"] != "job-1" {
		t.Fatalf("expected job-1, got %v", claimed["id"])
	}

	status, reported := h.json(http.MethodPost, "/api/sandbox-nodes/node-1/jobs/job-1", `{"exitCode":0,"stdout":"ok","durationMs":3}`)
	if status != http.StatusOK {
		t.Fatalf("report: %d %v", status, reported)
	}
	select {
	case <-jobDone:
	case <-time.After(2 * time.Second):
		t.Fatal("reporting a result must wake whoever is waiting on the job")
	}
}

func TestOnlyTheEnrolledNodeCanCompleteItsJob(t *testing.T) {
	h := newSandboxHarness(t)
	if status, _ := h.do(http.MethodPost, "/api/sandbox-nodes", `{"id":"node-1","name":"a"}`); status != http.StatusOK {
		t.Fatalf("enroll node-1: %d", status)
	}
	if status, _ := h.do(http.MethodPost, "/api/sandbox-nodes", `{"id":"node-2","name":"b"}`); status != http.StatusOK {
		t.Fatalf("enroll node-2: %d", status)
	}
	h.svc.Queue().Enqueue(&sandbox.Job{ID: "job-9", Provider: sandbox.ProviderMock, Op: sandbox.JobExec})
	if status, claimed := h.json(http.MethodGet, "/api/sandbox-nodes/node-1/jobs?wait=1", ""); status != http.StatusOK {
		t.Fatalf("claim: %d %v", status, claimed)
	}
	// node-2 did not run it, so it cannot answer for it.
	status, body := h.json(http.MethodPost, "/api/sandbox-nodes/node-2/jobs/job-9", `{"exitCode":0}`)
	if status != http.StatusNotFound {
		t.Fatalf("expected 404, got %d %v", status, body)
	}
}

func TestEnrollingAnotherOwnersNodeIdIsRefused(t *testing.T) {
	h := newSandboxHarness(t)
	if err := h.svc.RegisterNode(t.Context(), &sandbox.Node{
		ID: "taken", OwnerID: "stranger", Name: "theirs", Status: sandbox.NodeOnline, LastSeenAt: time.Now(),
	}); err != nil {
		t.Fatalf("seed node: %v", err)
	}
	status, body := h.json(http.MethodPost, "/api/sandbox-nodes", `{"id":"taken","name":"mine"}`)
	if status != http.StatusNotFound {
		t.Fatalf("expected the hijack to be refused, got %d %v", status, body)
	}
}

func TestEnrollingWithAnUnsafeNodeIdIsRefused(t *testing.T) {
	h := newSandboxHarness(t)
	status, body := h.json(http.MethodPost, "/api/sandbox-nodes", `{"id":"../etc/passwd","name":"x"}`)
	if status != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d %v", status, body)
	}
}

func TestSandboxEventsStreamReplaysFromSince(t *testing.T) {
	h := newSandboxHarness(t)
	leaseID := h.lease()

	// Frames are addressed to their owner; "dev" is who the HTTP caller resolves to.
	events, _, err := h.svc.EventsSince(t.Context(), "dev", 0, 50)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("creating a lease must publish a frame")
	}

	req, _ := http.NewRequest(http.MethodGet, h.server.URL+"/api/sandboxes/events?since=0", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	res, err := h.server.Client().Do(req.WithContext(ctx))
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("stream: %d", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("expected an event stream, got %q", ct)
	}
	buf := make([]byte, 4096)
	read, _ := res.Body.Read(buf)
	frame := string(buf[:read])
	if !strings.Contains(frame, "sandbox_event") {
		t.Fatalf("expected a replayed frame, got %q", frame)
	}
	_ = leaseID
}
