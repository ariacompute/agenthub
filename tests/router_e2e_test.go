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
	"agenthub/internal/router"
)

// routerHarness boots the scheduler the way cmd/agenthub-server does and drives it
// over real HTTP: a trigger goes in, one beat later a hub task exists and the
// ticket says so. The whole chain — filter, decide, launch, report — is the point
// of the test.
type routerHarness struct {
	t      *testing.T
	server *httptest.Server
	hub    *hub.Hub
	svc    *router.Service
}

func newRouterHarness(t *testing.T) *routerHarness {
	t.Helper()

	cfg := &config.Config{
		HTTPPort:        "3500",
		Env:             "dev",
		StaticDir:       t.TempDir(),
		SessionTTL:      time.Hour,
		LegacyMethods:   true,
		MaxParallel:     4,
		CLITimeout:      time.Second,
		CLIAllowlist:    []string{"echo", "ls"},
		MaxOutputBytes:  4096,
		EventBuffer:     128,
		DatabaseURL:     testDatabaseURL,
		PushRecordLimit: 100,
		// The scheduler under test: a fast beat so the suite does not wait two
		// seconds per step, and a short backoff so a retry is observable.
		RouterEnabled:       true,
		RouterInterval:      10 * time.Millisecond,
		RouterBatchMax:      10,
		RouterMaxTickets:    2,
		RouterMaxAttempts:   2,
		RouterAssessMode:    config.RouterAssessRule,
		RouterTriggerTTL:    time.Hour,
		RouterPruneInterval: time.Hour,
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("config: %v", err)
	}

	reg := registry.New()
	agent, err := reg.Upsert(&registry.Entry{
		Kind: registry.KindAgent, Name: "worker", Endpoint: "http://agent",
		Version: "1.0.0", Capabilities: []string{"plan"}, Health: registry.HealthHealthy,
	})
	if err != nil {
		t.Fatalf("seed agent: %v", err)
	}

	h := hub.New(cfg, reg, adapter.Options{
		CLITimeout:     cfg.CLITimeout,
		CLIAllowlist:   cfg.CLIAllowlist,
		MaxOutputBytes: cfg.MaxOutputBytes,
	}, &hub.MockNotifier{}, hub.Persistence{})
	t.Cleanup(h.Close)

	svc := router.NewService(router.ServiceOptions{
		Store:       router.NewMemoryStore(),
		Runner:      h,
		Clock:       time.Now,
		Interval:    cfg.RouterInterval,
		MaxParallel: cfg.RouterMaxTickets,
		MaxAttempts: cfg.RouterMaxAttempts,
		BatchMax:    cfg.RouterBatchMax,
		Resource:    agent.ID,
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := svc.Run(ctx); err != nil {
			t.Errorf("router stopped: %v", err)
		}
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("router did not stop")
		}
		svc.Stop()
	})

	server := httptest.NewServer(handlers.Register(handlers.Deps{
		Cfg:    cfg,
		Hub:    h,
		Reg:    reg,
		Log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		Router: svc,
	}))
	t.Cleanup(server.Close)

	return &routerHarness{t: t, server: server, hub: h, svc: svc}
}

func (h *routerHarness) do(method, path, body string) (int, []byte) {
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
	return res.StatusCode, raw
}

// waitFor polls over HTTP until cond holds, so nothing depends on a fixed sleep.
func (h *routerHarness) waitFor(what string, cond func() bool) {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	h.t.Fatalf("timed out waiting for %s", what)
}

func TestE2ERouterTurnsATriggerIntoATask(t *testing.T) {
	h := newRouterHarness(t)

	code, body := h.do(http.MethodPost, "/api/router/triggers",
		`{"source":"manual","body":"summarise the open issues"}`)
	if code != http.StatusAccepted {
		t.Fatalf("inject: %d %s", code, body)
	}
	var trig router.Trigger
	if err := json.Unmarshal(body, &trig); err != nil {
		t.Fatalf("decode trigger: %v", err)
	}

	// The beat picks it up on its own: no manual kick.
	h.waitFor("the ticket to run", func() bool {
		_, raw := h.do(http.MethodGet, "/api/router/tickets", "")
		var list struct {
			Tickets []*router.Ticket `json:"tickets"`
		}
		_ = json.Unmarshal(raw, &list)
		return len(list.Tickets) == 1 && list.Tickets[0].TaskID != ""
	})

	_, raw := h.do(http.MethodGet, "/api/router/tickets", "")
	var list struct {
		Tickets []*router.Ticket `json:"tickets"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatalf("decode tickets: %v", err)
	}
	ticket := list.Tickets[0]
	if ticket.Resource == "" {
		t.Fatal("the ticket ran without a worker")
	}

	// The run is an ordinary hub task, so the trace is where every other run is.
	if code, _ := h.do(http.MethodGet, "/api/tasks/"+ticket.TaskID, ""); code != http.StatusOK {
		t.Fatalf("the ticket's task is not readable: %d", code)
	}

	// The trigger records what happened to it, which is the audit trail. The
	// verdict is written just after the ticket it points at exists, so the console
	// follows the same poll a browser would.
	var stored router.Trigger
	h.waitFor("the trigger verdict", func() bool {
		_, body := h.do(http.MethodGet, "/api/router/triggers/"+trig.ID, "")
		var current router.Trigger
		if err := json.Unmarshal(body, &current); err != nil {
			return false
		}
		stored = current
		return current.Disposition == string(router.DispositionRouted)
	})
	if stored.TicketID != ticket.ID {
		_, all := h.do(http.MethodGet, "/api/router/tickets", "")
		t.Fatalf("trigger ticket = %q, want %q\nall tickets: %s", stored.TicketID, ticket.ID, all)
	}
	if stored.DecisionID == "" {
		t.Fatal("the routed trigger carries no decision id")
	}

	// The event stream replays the whole hop before it starts streaming, so a
	// client that reconnects with since=0 sees what it missed.
	streamed := h.readStream("/api/router/events?since=0", 2*time.Second)
	for _, kind := range []string{router.EventTriggerReceived, router.EventDecision, router.EventTicketCreated} {
		if !strings.Contains(streamed, kind) {
			t.Fatalf("the replay is missing %q:\n%s", kind, streamed)
		}
	}
}

// readStream reads an SSE endpoint for a bounded time. The endpoint never closes
// on its own, so the deadline is what ends the read rather than the server.
func (h *routerHarness) readStream(path string, window time.Duration) string {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), window)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.server.URL+path, nil)
	if err != nil {
		h.t.Fatalf("build stream request: %v", err)
	}
	res, err := h.server.Client().Do(req)
	if err != nil {
		h.t.Fatalf("stream: %v", err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	return string(raw)
}

func TestE2ERouterStatsReportTheWaterLevel(t *testing.T) {
	h := newRouterHarness(t)

	code, raw := h.do(http.MethodGet, "/api/router/stats", "")
	if code != http.StatusOK {
		t.Fatalf("stats: %d %s", code, raw)
	}
	var stats router.Stats
	if err := json.Unmarshal(raw, &stats); err != nil {
		t.Fatalf("decode stats: %v", err)
	}
	if !stats.Live {
		t.Fatal("the beat is running but the stats say otherwise")
	}
}

func TestE2ERouterRefusesAnUnknownSource(t *testing.T) {
	h := newRouterHarness(t)
	if code, _ := h.do(http.MethodPost, "/api/router/triggers", `{"source":"smoke-signal","body":"hi"}`); code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", code)
	}
}
