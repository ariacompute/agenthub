package adapter

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"agenthub/internal/registry"
)

func agentEntry(cfg *registry.AgentConfig) registry.Entry {
	return registry.Entry{
		ID: "agent-1", Kind: registry.KindAgent, Name: "adk", Version: "1.0.0",
		Endpoint:   "http://agent.test",
		KindConfig: &registry.KindConfig{Agent: cfg},
	}
}

// a2aServer serves an Agent Card and answers message/send like a real A2A agent:
// a task carrying one text artifact.
func a2aServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/.well-known/agent-card.json":
			_, _ = w.Write([]byte(`{"name":"adk-agent","version":"1.0.0","protocolVersion":"0.3.0","url":"` + "http://" + r.Host + `/a2a"}`))
		case "/a2a":
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":"agenthub-1","result":{"id":"t1","status":{"state":"completed"},"artifacts":[{"artifactId":"a1","name":"answer","parts":[{"kind":"text","text":"the real answer"}]}]}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// An entry that publishes an Agent Card must be called for real: the result
// carries the agent's own answer, not a mock digest.
func TestAgentCardRunsARealCall(t *testing.T) {
	srv := a2aServer(t)
	e := agentEntry(&registry.AgentConfig{CardURL: srv.URL + "/.well-known/agent-card.json"})
	conn, err := For(&e, options())
	if err != nil {
		t.Fatal(err)
	}
	res, err := conn.Invoke(context.Background(), InvokeRequest{ResourceID: e.ID, Entry: e, Prompt: "hello"})
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if res.Text != "the real answer" {
		t.Fatalf("text = %q, want the agent's answer", res.Text)
	}
	if got := res.Artifacts[0].Metadata["provider"]; got != "a2a:adk-agent" {
		t.Fatalf("provider = %v, want the agent's card name", got)
	}
}

// Without an Agent Card there is no endpoint to speak to, so the connector stays
// offline — and says so on the result.
func TestAgentWithoutCardStaysMock(t *testing.T) {
	e := agentEntry(nil)
	conn, err := For(&e, options())
	if err != nil {
		t.Fatal(err)
	}
	res, err := conn.Invoke(context.Background(), InvokeRequest{ResourceID: e.ID, Entry: e, Prompt: "hello"})
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if got := res.Artifacts[0].Metadata["provider"]; got != MockProviderName {
		t.Fatalf("provider = %v, want %q", got, MockProviderName)
	}
}

// A card that cannot be fetched is a failed call, not a completed one: this is
// the case that used to read "Completed" against an agent nobody deployed.
func TestAgentCardUnreachableFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)

	e := agentEntry(&registry.AgentConfig{CardURL: srv.URL + "/.well-known/agent-card.json"})
	conn, err := For(&e, options())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Invoke(context.Background(), InvokeRequest{ResourceID: e.ID, Entry: e, Prompt: "hello"}); err == nil {
		t.Fatal("an unreachable agent must not produce a result")
	}
}

// The endpoint comes from the card's own url when it declares one, and from the
// card's origin when it does not.
func TestEndpointForPrefersTheCardURL(t *testing.T) {
	if got := endpointFor(&agentCard{URL: "https://agent.test/a2a/"}, "https://agent.test/.well-known/agent-card.json"); got != "https://agent.test/a2a" {
		t.Fatalf("declared url = %q", got)
	}
	if got := endpointFor(&agentCard{}, "https://agent.test/.well-known/agent-card.json"); got != "https://agent.test" {
		t.Fatalf("derived origin = %q", got)
	}
	// A card from another party with a non-http url must not be posted to.
	if got := endpointFor(&agentCard{URL: "stdio://pi"}, "https://agent.test/.well-known/agent-card.json"); got != "https://agent.test" {
		t.Fatalf("non-http url = %q, want the card origin", got)
	}
}

// Health is a measurement: a card that answers is healthy, one that does not
// means the agent is not running, however healthy the record claims to be.
func TestAgentHealthProbesTheCard(t *testing.T) {
	srv := a2aServer(t)
	withCard := agentEntry(&registry.AgentConfig{CardURL: srv.URL + "/.well-known/agent-card.json"})
	conn, err := For(&withCard, options())
	if err != nil {
		t.Fatal(err)
	}
	if h, err := conn.Health(context.Background(), &withCard); err != nil || h != registry.HealthHealthy {
		t.Fatalf("reachable card = %v, %v; want healthy", h, err)
	}

	down := agentEntry(&registry.AgentConfig{CardURL: "http://127.0.0.1:1/.well-known/agent-card.json"})
	conn2, err := For(&down, Options{AgentTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if h, err := conn2.Health(context.Background(), &down); err != nil || h != registry.HealthUnhealthy {
		t.Fatalf("unreachable card = %v, %v; want unhealthy", h, err)
	}

	// No card, nothing to probe: the stable mock value stands.
	noCard := agentEntry(nil)
	conn3, err := For(&noCard, options())
	if err != nil {
		t.Fatal(err)
	}
	if h, err := conn3.Health(context.Background(), &noCard); err != nil || h != registry.HealthHealthy {
		t.Fatalf("no card = %v, %v", h, err)
	}
}

// The answer may arrive as a task, a bare message, and with the discriminator
// spelled "kind" (the specification) or "type" (agenthub's own wire format).
func TestTextFromSendResultAcceptsBothShapes(t *testing.T) {
	cases := []struct {
		name  string
		raw   string
		text  string
		state string
	}{
		{"artifact kind", `{"status":{"state":"completed"},"artifacts":[{"parts":[{"kind":"text","text":"a"}]}]}`, "a", "completed"},
		{"artifact type", `{"status":{"state":"completed"},"artifacts":[{"parts":[{"type":"text","text":"b"}]}]}`, "b", "completed"},
		{"status message", `{"status":{"state":"completed","message":{"parts":[{"kind":"text","text":"c"}]}}}`, "c", "completed"},
		{"bare message", `{"role":"agent","parts":[{"kind":"text","text":"d"}]}`, "d", "completed"},
		{"input required", `{"status":{"state":"input-required","message":{"parts":[{"kind":"text","text":"approve?"}]}}}`, "approve?", "input-required"},
		{"no text", `{"status":{"state":"completed"}}`, "", "completed"},
	}
	for _, tc := range cases {
		text, state, err := textFromSendResult(json.RawMessage(tc.raw))
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if text != tc.text || state != tc.state {
			t.Fatalf("%s: got (%q, %q), want (%q, %q)", tc.name, text, state, tc.text, tc.state)
		}
	}
}

// Every offline connector stamps its provenance, so a fabricated result can be
// told apart from a real one on the trace page.
func TestOfflineConnectorsStampMockProvenance(t *testing.T) {
	entries := map[registry.Kind]registry.Entry{
		registry.KindAgent:  {ID: "a", Kind: registry.KindAgent, Name: "a"},
		registry.KindMCP:    {ID: "m", Kind: registry.KindMCP, Name: "m"},
		registry.KindSkill:  {ID: "s", Kind: registry.KindSkill, Name: "s"},
		registry.KindCustom: {ID: "c", Kind: registry.KindCustom, Name: "c"},
	}
	for kind, e := range entries {
		conn, err := For(&e, options())
		if err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		res, err := conn.Invoke(context.Background(), InvokeRequest{ResourceID: e.ID, Entry: e, Prompt: "hello"})
		if err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		if len(res.Artifacts) == 0 {
			t.Fatalf("%s: no artifact", kind)
		}
		if got := res.Artifacts[0].Metadata["provider"]; got != MockProviderName {
			t.Fatalf("%s: provider = %v, want %q", kind, got, MockProviderName)
		}
	}
}
