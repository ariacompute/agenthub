package adapter

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"agenthub/internal/a2a"
	"agenthub/internal/registry"
)

// A real A2A round trip: the card is fetched, the card's own url is where the
// message is posted, and the answer is read out of the task. A stub server keeps
// it offline while still exercising the whole path.

func scriptedAgentServer(t *testing.T, sendResult string) (*httptest.Server, *string) {
	t.Helper()
	var posted string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/.well-known/agent-card.json":
			_, _ = w.Write([]byte(`{"name":"planner","url":"` + "http://" + r.Host + `","version":"1.0"}`))
		case r.Method == http.MethodPost:
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			posted = body["method"].(string)
			_, _ = w.Write([]byte(sendResult))
		default:
			w.WriteHeader(http.StatusTeapot)
		}
	}))
	t.Cleanup(server.Close)
	return server, &posted
}

func agentFor(t *testing.T, cardURL string, egress ...string) *agentConnector {
	t.Helper()
	return &agentConnector{
		cardURL: cardURL,
		client:  &http.Client{Timeout: 5 * time.Second},
		timeout: 5 * time.Second,
		egress:  egress,
	}
}

func TestAgentInvokeCompletesARemoteTurn(t *testing.T) {
	server, posted := scriptedAgentServer(t, `{"jsonrpc":"2.0","id":"agenthub-1","result":{"id":"task-1","status":{"state":"completed","message":{"parts":[{"kind":"text","text":" done "}]}},"artifacts":[{"parts":[{"kind":"text","text":"the answer"}]}]}}`)
	c := agentFor(t, server.URL+"/.well-known/agent-card.json")

	res, err := c.Invoke(context.Background(), InvokeRequest{Entry: registry.Entry{Kind: registry.KindAgent, Name: "planner"}, Prompt: "plan the week"})
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if *posted != a2a.MethodMessageSend {
		t.Fatalf("method = %q, want message/send", *posted)
	}
	if !strings.Contains(res.Text, "the answer") || !strings.Contains(res.Text, "done") {
		t.Fatalf("text = %q", res.Text)
	}
	data := dataPart(t, res)
	// The card's name is what an operator needs to know which agent answered.
	if data["provider"] != "a2a:planner" || data["state"] != "completed" {
		t.Fatalf("data = %+v", data)
	}
	if len(res.Artifacts) != 1 || res.Artifacts[0].Name != "agent.a2a" {
		t.Fatalf("artifacts = %+v", res.Artifacts)
	}
}

func TestAgentInvokeParksWhenTheRemoteAsksForAHuman(t *testing.T) {
	server, _ := scriptedAgentServer(t, `{"jsonrpc":"2.0","id":"agenthub-1","result":{"id":"task-1","status":{"state":"input-required","message":{"parts":[{"kind":"text","text":"need approval"}]}}}}`)
	c := agentFor(t, server.URL+"/.well-known/agent-card.json")

	res, err := c.Invoke(context.Background(), InvokeRequest{Entry: registry.Entry{Kind: registry.KindAgent, Name: "planner"}, ResourceID: "res-1", Prompt: "transfer money"})
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	// A parked turn is an answer, but the task is not finished work.
	if !res.RequiresInput || res.Prompt != "approval_required:res-1" {
		t.Fatalf("result = %+v", res)
	}
	if !strings.Contains(res.Text, "need approval") {
		t.Fatalf("text = %q", res.Text)
	}
}

func TestAgentInvokeExplainsAnEmptyAnswer(t *testing.T) {
	server, _ := scriptedAgentServer(t, `{"jsonrpc":"2.0","id":"agenthub-1","result":{"status":{"state":"completed"}}}`)
	c := agentFor(t, server.URL+"/.well-known/agent-card.json")

	res, err := c.Invoke(context.Background(), InvokeRequest{Entry: registry.Entry{Kind: registry.KindAgent, Name: "planner"}, Prompt: "hello"})
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	// Silence must be visible in the text, not an empty artifact.
	if !strings.Contains(res.Text, "no text part returned") {
		t.Fatalf("text = %q", res.Text)
	}
}

func TestAgentInvokeSkipsThinkingParts(t *testing.T) {
	server, _ := scriptedAgentServer(t, `{"jsonrpc":"2.0","id":"agenthub-1","result":{"status":{"state":"completed"},"artifacts":[{"parts":[
		{"kind":"text","text":"internal reasoning","metadata":{"adk_thought":true}},
		{"kind":"text","text":"the answer"}]}]}}`)
	c := agentFor(t, server.URL+"/.well-known/agent-card.json")

	res, err := c.Invoke(context.Background(), InvokeRequest{Entry: registry.Entry{Kind: registry.KindAgent, Name: "planner"}, Prompt: "hello"})
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	// ADK publishes reasoning as a part; echoing it back would present thinking as
	// the reply.
	if strings.Contains(res.Text, "internal reasoning") || res.Text != "the answer" {
		t.Fatalf("text = %q", res.Text)
	}
}

func TestAgentInvokeRefusesAnEndpointOutsideTheAllowlist(t *testing.T) {
	server, _ := scriptedAgentServer(t, `{"jsonrpc":"2.0","id":"agenthub-1","result":{}}`)
	c := agentFor(t, server.URL+"/.well-known/agent-card.json", "partner.test")

	_, err := c.Invoke(context.Background(), InvokeRequest{Entry: registry.Entry{Kind: registry.KindAgent, Name: "planner"}, Prompt: "hello"})
	if code := adapterCode(t, err); code != "sandbox_policy_denied" {
		t.Fatalf("code = %s, want the card's target to be refused", code)
	}
}

func TestAgentInvokeReportsCardAndRPCFailures(t *testing.T) {
	ctx := context.Background()
	entry := registry.Entry{Kind: registry.KindAgent, Name: "planner"}

	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer broken.Close()
	if _, err := agentFor(t, broken.URL).Invoke(ctx, InvokeRequest{Entry: entry}); adapterCode(t, err) != CodeResourceUnreachable {
		t.Fatalf("a card that 500s must be unreachable")
	}

	garbage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not a card"))
	}))
	defer garbage.Close()
	if _, err := agentFor(t, garbage.URL).Invoke(ctx, InvokeRequest{Entry: entry}); adapterCode(t, err) != CodeProviderMalformed {
		t.Fatalf("a 200 that is not a card must be malformed")
	}

	// A well-formed card whose message/send carries a JSON-RPC error.
	server, _ := scriptedAgentServer(t, `{"jsonrpc":"2.0","id":"agenthub-1","error":{"code":-32000,"message":"no"}}`)
	if _, err := agentFor(t, server.URL+"/.well-known/agent-card.json").Invoke(ctx, InvokeRequest{Entry: entry}); adapterCode(t, err) != CodeResourceUnreachable {
		t.Fatalf("a JSON-RPC error must not read as an empty answer")
	}

	// A card fetch the client cannot even build.
	if _, err := agentFor(t, "://nope").Invoke(ctx, InvokeRequest{Entry: entry}); err == nil {
		t.Fatalf("an unusable card URL must fail")
	}
}

func TestAgentInvokeWithNoCardStaysMock(t *testing.T) {
	c := &agentConnector{client: &http.Client{Timeout: time.Second}}
	res, err := c.Invoke(context.Background(), InvokeRequest{Entry: registry.Entry{Kind: registry.KindAgent, Name: "planner"}, Prompt: "summarise"})
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if dataPart(t, res)["provider"] != MockProviderName {
		t.Fatalf("a fabricated answer must be labelled mock: %+v", res.Parts)
	}

	// And the approval gate still parks it.
	res, err = c.Invoke(context.Background(), InvokeRequest{Entry: registry.Entry{Kind: registry.KindAgent, Name: "planner"}, Prompt: "please approve this"})
	if err != nil || !res.RequiresInput {
		t.Fatalf("result = %+v (%v)", res, err)
	}

	// A cancelled context short-circuits before any work.
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Invoke(canceled, InvokeRequest{Entry: registry.Entry{Kind: registry.KindAgent}}); err == nil {
		t.Fatal("a cancelled call must not fabricate a result")
	}
}

func TestEndpointForPrefersTheCardThenTheOrigin(t *testing.T) {
	cases := []struct {
		name    string
		card    *agentCard
		cardURL string
		want    string
	}{
		{"card url wins", &agentCard{URL: "https://agent.example/a2a/"}, "https://card.example/.well-known/agent-card.json", "https://agent.example/a2a"},
		{"well-known prefix", nil, "https://card.example/.well-known/agent-card.json", "https://card.example"},
		{"plain url", nil, "https://card.example/agent.json", "https://card.example/agent.json"},
		{"card url not http", &agentCard{URL: "internal.service/a2a"}, "https://card.example/.well-known/agent-card.json", "https://card.example"},
	}
	for _, tc := range cases {
		if got := endpointFor(tc.card, tc.cardURL); got != tc.want {
			t.Errorf("%s: endpointFor = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestSendPartClassification(t *testing.T) {
	cases := []struct {
		name string
		part sendPart
		want bool
	}{
		{"typed text", sendPart{Type: a2a.PartTypeText, Text: "hi"}, true},
		{"kinded text", sendPart{Kind: a2a.PartTypeText, Text: "hi"}, true},
		{"data is not an answer", sendPart{Type: "data"}, false},
		{"thought is not an answer", sendPart{Kind: a2a.PartTypeText, Text: "hi", Metadata: map[string]any{"adk_thought": true}}, false},
		{"other metadata keeps it", sendPart{Kind: a2a.PartTypeText, Text: "hi", Metadata: map[string]any{"adk_thought": false}}, true},
	}
	for _, tc := range cases {
		if got := tc.part.isAnswer(); got != tc.want {
			t.Errorf("%s: isAnswer = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestNewMessageIDIsUnpredictable(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 8; i++ {
		id := newMessageID()
		if len(id) != 32 {
			t.Fatalf("message id = %q, want 16 random bytes in hex", id)
		}
		if seen[id] {
			t.Fatalf("duplicate message id %q", id)
		}
		seen[id] = true
	}
}

func TestCLIHealthWithoutASandbox(t *testing.T) {
	// "go" is the one binary guaranteed to exist on the machine running this test.
	connector, err := For(&registry.Entry{Kind: registry.KindCLI}, Options{CLIAllowlist: []string{"go"}, CLITimeout: time.Second})
	if err != nil {
		t.Fatalf("connector: %v", err)
	}
	allowed := cliEntryFor(t, "go", "")
	if health, _ := connector.Health(context.Background(), &allowed); health != registry.HealthHealthy {
		t.Fatalf("health = %v, want healthy for an allowlisted command on PATH", health)
	}

	// Found on PATH but not allowlisted: the record is usable-looking and unusable.
	blocked := cliEntryFor(t, "go", "")
	blocked.KindConfig.CLI.Command = "go"
	notAllowed, _ := For(&registry.Entry{Kind: registry.KindCLI}, Options{CLIAllowlist: []string{"git"}, CLITimeout: time.Second})
	if health, _ := notAllowed.Health(context.Background(), &blocked); health != registry.HealthDegraded {
		t.Fatalf("health = %v, want degraded for a command outside the allowlist", health)
	}

	missing := cliEntryFor(t, "agenthub-no-such-binary-xyz", "")
	if health, _ := connector.Health(context.Background(), &missing); health != registry.HealthUnhealthy {
		t.Fatalf("health = %v, want unhealthy for a missing binary", health)
	}

	// A record with no command at all has nothing to measure.
	empty := &registry.Entry{Kind: registry.KindCLI, Name: "nothing"}
	if health, _ := connector.Health(context.Background(), empty); health != registry.HealthUnknown {
		t.Fatalf("health = %v, want unknown", health)
	}
}

func TestMCPInvokeRoutesThroughTheConfiguredTransport(t *testing.T) {
	provider := newStubProvider()
	service := sandboxFor(t, provider)

	// A stdio record without a command has nothing to attach to, so the connector
	// keeps the deterministic answer instead of failing the task.
	transportless := stdioMCPEntry([]string{"search"})
	transportless.KindConfig.MCP.Command = ""
	connector, _ := For(&registry.Entry{Kind: registry.KindMCP}, Options{Sandbox: service})
	res, err := connector.Invoke(context.Background(), InvokeRequest{Entry: transportless, Prompt: "x"})
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if dataPart(t, res)["provider"] != MockProviderName {
		t.Fatalf("a record with no server must be labelled mock: %+v", res.Parts)
	}

	// The same holds for a cancelled context, before any transport is chosen.
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := connector.Invoke(canceled, InvokeRequest{Entry: transportless}); err == nil {
		t.Fatal("a cancelled call must not return a result")
	}

	// Streamable HTTP goes through the SSRF-safe client, which refuses a loopback
	// target unless the record declares the egress.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["method"] == "tools/list" {
			_, _ = w.Write(toolsFrame("search"))
			return
		}
		_, _ = w.Write([]byte(`{"result":{"content":[{"type":"text","text":"ok"}]}}`))
	}))
	defer server.Close()

	httpEntry := registry.Entry{
		ID:   "mcp-http",
		Kind: registry.KindMCP,
		Name: "docs",
		KindConfig: &registry.KindConfig{
			MCP: &registry.MCPConfig{
				Transport:     string(registry.TransportStreamableHTTP),
				URL:           server.URL,
				ToolAllowlist: []string{"search"},
			},
			Agent: &registry.AgentConfig{EgressAllowlist: []string{"127.0.0.1"}},
		},
	}
	res, err = connector.Invoke(context.Background(), InvokeRequest{Entry: httpEntry, Prompt: "x"})
	if err != nil {
		t.Fatalf("http invoke: %v", err)
	}
	if !strings.Contains(res.Text, "transport=streamable-http") || !strings.Contains(res.Text, "tool=search") {
		t.Fatalf("text = %q", res.Text)
	}
	if len(res.Artifacts) != 1 || res.Artifacts[0].Name != "mcp.call" {
		t.Fatalf("artifacts = %+v", res.Artifacts)
	}

	// Without the egress declaration the loopback target is refused.
	httpEntry.KindConfig.Agent = nil
	if _, err := connector.Invoke(context.Background(), InvokeRequest{Entry: httpEntry, Prompt: "x"}); err == nil {
		t.Fatal("a loopback MCP server must need an explicit egress allowlist")
	}
}

func TestMockHelpersKeepTheirContracts(t *testing.T) {
	if got := pick("anything", 0); got != 0 {
		t.Fatalf("pick with no options = %d", got)
	}
	if got := itoa(0); got != "0" {
		t.Fatalf("itoa(0) = %q", got)
	}
	if got := itoa(-7); got != "-7" {
		t.Fatalf("itoa(-7) = %q", got)
	}
	if meta() != nil {
		t.Fatal("no pairs must mean no metadata")
	}
	if got := meta("a", "1", "orphan"); len(got) != 1 || got["a"] != "1" {
		t.Fatalf("meta = %v", got)
	}
	if s, truncated := truncate("abcdef", 3); s != "abc" || !truncated {
		t.Fatalf("truncate = %q %v", s, truncated)
	}
	if s, truncated := truncate("abc", 0); s != "abc" || truncated {
		t.Fatalf("a zero cap means no cap: %q %v", s, truncated)
	}

	ctx, cancel := boundedContext(context.Background(), 0)
	defer cancel()
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) > 20*time.Second {
		t.Fatalf("a zero timeout must fall back to the shared default, got %v", deadline)
	}
}
