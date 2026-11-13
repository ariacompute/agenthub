package adapter

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agenthub/internal/registry"
	"agenthub/internal/sandbox"
)

// The MCP connector's permission boundary and its small transport helpers. These
// are the parts an operator cannot see from the outside until they are wrong: an
// empty allowlist that behaves like "every tool", an environment that leaks the
// server's secrets into a sandbox, or a failed JSON-RPC call reported as success.

func TestAnEmptyToolAllowlistPermitsNothing(t *testing.T) {
	tools := []map[string]any{{"name": "read"}, {"name": "write"}}

	if name, ok := pickAllowedTool(tools, nil); ok || name != "" {
		t.Fatalf("no allowlist must mean no tools, got %q", name)
	}
	if name, ok := pickAllowedTool(tools, []string{"  "}); ok || name != "" {
		t.Fatalf("a blank allowlist must mean no tools, got %q", name)
	}
}

func TestTheAllowlistPicksAnAdvertisedToolFirst(t *testing.T) {
	tools := []map[string]any{{"name": "read"}, {"name": "write"}}

	// Among the allowed names the server's own order decides, so a tool the
	// operator listed second is still reachable when the server advertises it first.
	name, ok := pickAllowedTool(tools, []string{"write", "read"})
	if !ok || name != "read" {
		t.Fatalf("an advertised tool the allowlist permits must be chosen, got %q (%v)", name, ok)
	}
	// Names are trimmed on both sides: an operator pasting a list with spaces
	// must not silently lose every entry.
	name, ok = pickAllowedTool(tools, []string{"  read  "})
	if !ok || name != "read" {
		t.Fatalf("an entry with padding must still match, got %q (%v)", name, ok)
	}
}

func TestAnUnreachableServerStillUsesTheFirstAllowedTool(t *testing.T) {
	// tools/list failed or returned nothing: the allowlist is the operator's
	// statement of intent, so the first entry is what we ask for.
	name, ok := pickAllowedTool(nil, []string{"call_action"})
	if !ok || name != "call_action" {
		t.Fatalf("the declared tool must be used when the server advertises none, got %q (%v)", name, ok)
	}

	// A malformed entry in the frame must be skipped, not turned into a call.
	junk := []map[string]any{{"name": 42}, {}}
	name, ok = pickAllowedTool(junk, []string{"fallback"})
	if !ok || name != "fallback" {
		t.Fatalf("nameless tools must be skipped, got %q (%v)", name, ok)
	}
}

func TestParseToolListKeepsTheNameDescriptionAndSchema(t *testing.T) {
	frame := []byte(`{"jsonrpc":"2.0","id":2,"result":{"tools":[
		{"name":"read","description":"read a doc","inputSchema":{"type":"object","properties":{"id":{"type":"string"}}}}
	]}}`)

	tools := parseToolList(frame)
	if len(tools) != 1 {
		t.Fatalf("expected one tool, got %d", len(tools))
	}
	if tools[0]["name"] != "read" || tools[0]["description"] != "read a doc" {
		t.Fatalf("the tool must keep its name and description, got %+v", tools[0])
	}
	schema, ok := tools[0]["inputSchema"].(map[string]any)
	if !ok || schema["type"] != "object" {
		t.Fatalf("the input schema must survive, got %+v", tools[0]["inputSchema"])
	}

	// A frame that is not JSON is an empty catalogue, not a panic: the caller then
	// falls back to the allowlist.
	if got := parseToolList([]byte("not json")); got != nil {
		t.Fatalf("garbage must read as no tools, got %+v", got)
	}
	if got := parseToolList([]byte(`{"result":{}}`)); len(got) != 0 {
		t.Fatalf("a server with no tools must read as none, got %+v", got)
	}
}

func TestEgressAndProviderAreReadFromTheEntry(t *testing.T) {
	entry := &registry.Entry{
		KindConfig: &registry.KindConfig{Agent: &registry.AgentConfig{EgressAllowlist: []string{"api.example.com"}}},
	}
	if got := egressAllow(entry); len(got) != 1 || got[0] != "api.example.com" {
		t.Fatalf("the agent egress allowlist must reach the transport, got %v", got)
	}
	// A plain MCP entry has no agent block: no egress grant, so private targets
	// stay blocked.
	if got := egressAllow(&registry.Entry{KindConfig: &registry.KindConfig{}}); got != nil {
		t.Fatalf("an entry without an agent block grants no egress, got %v", got)
	}
	if got := egressAllow(nil); got != nil {
		t.Fatalf("a nil entry grants no egress, got %v", got)
	}

	if got := resProvider(nil); got != MockProviderName {
		t.Fatalf("no lease means the mock backend, got %q", got)
	}
	if got := resProvider(&sandbox.Lease{Provider: sandbox.ProviderDocker}); got != sandbox.ProviderDocker {
		t.Fatalf("a lease must report its own backend, got %q", got)
	}
}

func TestTheEnvironmentOfAJobIsBothDeclaredAndAllowed(t *testing.T) {
	if got := envForJob(nil); got != nil {
		t.Fatalf("a nil entry has no environment, got %v", got)
	}
	if got := envForJob(&registry.Entry{KindConfig: &registry.KindConfig{}}); got != nil {
		t.Fatalf("an entry without MCP config has no environment, got %v", got)
	}

	declared := &registry.Entry{KindConfig: &registry.KindConfig{
		MCP: &registry.MCPConfig{Env: map[string]string{"TOKEN": "abc"}},
	}}
	// Declared but not allowed: the sandbox profile is the gate, so this stays out.
	if got := envForJob(declared); got != nil {
		t.Fatalf("an environment the profile does not allow must not be forwarded, got %v", got)
	}

	allowed := &registry.Entry{KindConfig: &registry.KindConfig{
		MCP:     &registry.MCPConfig{Env: map[string]string{"TOKEN": "abc", "SECRET": "x"}},
		Sandbox: &registry.SandboxConfig{EnvAllow: []string{"TOKEN"}},
	}}
	got := envForJob(allowed)
	if len(got) != 1 || got["TOKEN"] != "abc" {
		t.Fatalf("only the allow-listed variable may be forwarded, got %v", got)
	}

	// The audit trail prints names in a stable order, never map iteration order.
	keys := sortedKeys(map[string]string{"b": "1", "a": "2", "c": "3"})
	if strings.Join(keys, ",") != "a,b,c" {
		t.Fatalf("keys must be sorted, got %v", keys)
	}
	if len(sortedKeys(nil)) != 0 {
		t.Fatal("an empty map must produce an empty list")
	}
}

func TestMCPPostReportsAFailedCallInsteadOfReturningItsBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The accept header is what makes a Streamable HTTP server answer with
		// JSON rather than an event stream.
		if r.Header.Get("Accept") == "" || r.Header.Get("Content-Type") != "application/json" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":2,"result":{"tools":[]}}`))
	}))
	defer server.Close()

	body, err := mcpPost(context.Background(), server.Client(), server.URL, `{"jsonrpc":"2.0","id":2}`)
	if err != nil {
		t.Fatalf("a 200 must be returned: %v", err)
	}
	if !strings.Contains(string(body), `"tools"`) {
		t.Fatalf("the body must come back whole, got %q", body)
	}

	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer failing.Close()

	// A tool call that fails is reported, never retried: repeating it would repeat
	// its side effect.
	if _, err := mcpPost(context.Background(), failing.Client(), failing.URL, "{}"); err == nil {
		t.Fatal("a 5xx must be an error")
	} else if !strings.Contains(err.Error(), "500") {
		t.Fatalf("the error must name the status, got %v", err)
	}

	badURL := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	badURL.Close()
	if _, err := mcpPost(context.Background(), badURL.Client(), badURL.URL, "{}"); err == nil {
		t.Fatal("an unreachable server must be an error, not an empty body")
	}
}

func TestLookPathResolvesOrRefuses(t *testing.T) {
	// A binary that cannot exist: the connector must report unhealthy rather than
	// pretending it can run.
	if path, err := lookPath("agenthub-no-such-binary-xyz"); err == nil {
		t.Fatalf("a missing binary must be an error, got %q", path)
	}
	// Anything the host does provide must come back as an absolute path.
	if path, err := lookPath("go"); err == nil && !strings.ContainsAny(path, `/\`) {
		t.Fatalf("a found binary must be a path, got %q", path)
	}
}

func TestTheModelRouterLabelsItsChainWithoutCredentials(t *testing.T) {
	if got := modelLabel(nil); got != "none" {
		t.Fatalf("no router must read as none, got %q", got)
	}
	label := modelLabel(NewModelRouter())
	if !strings.HasPrefix(label, "1:") {
		t.Fatalf("the label must be provider count plus names, got %q", label)
	}
	// The label reaches a receipt, so it must not be able to carry a key.
	if strings.Contains(label, "sk-") {
		t.Fatalf("a label must never contain a credential: %q", label)
	}

	if got := (mockUnavailable{}).Error(); got != "no_model_provider" {
		t.Fatalf("the unavailable-provider error is a machine code, got %q", got)
	}
}

func TestMCPToolCallUsesTheAllowedToolOverHTTP(t *testing.T) {
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var frame struct {
			Method string `json:"method"`
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &frame)
		calls = append(calls, frame.Method)
		switch frame.Method {
		case "tools/list":
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"read"},{"name":"write"}]}}`))
		case "tools/call":
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":3,"result":{"content":[{"type":"text","text":"ok"}]}}`))
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer server.Close()

	entry := &registry.Entry{
		Name: "docs",
		KindConfig: &registry.KindConfig{
			MCP: &registry.MCPConfig{URL: server.URL, ToolAllowlist: []string{"write"}},
			// A loopback test server is a private target, so the entry has to grant
			// egress explicitly: the SSRF guard is what makes the default refusal real.
			Agent: &registry.AgentConfig{EgressAllowlist: []string{"127.0.0.1"}},
		},
	}
	conn := &mcpConnector{}
	result, err := conn.invokeHTTP(context.Background(), InvokeRequest{Entry: *entry, Prompt: "look it up"}, entry.KindConfig.MCP, string(registry.TransportStreamableHTTP), server.URL)
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}

	if strings.Join(calls, ",") != "tools/list,tools/call" {
		t.Fatalf("the call must list tools before calling one, got %v", calls)
	}
	if !strings.Contains(result.Text, "tool=write") {
		t.Fatalf("the allow-listed tool must be the one called, got %q", result.Text)
	}
}

// A seeded demo record names an endpoint nobody serves and declares no tool
// allowlist. No tool was ever permitted, so this is an offline deterministic
// record, not a policy violation.
func TestOfflineMCPRecordUsesTheDeterministicAnswer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unreachable", http.StatusBadGateway)
	}))
	url := server.URL
	server.Close()

	entry := &registry.Entry{ID: "demo-mcp", Name: "legacy-hr-mcp", Kind: registry.KindMCP}
	conn := &mcpConnector{}
	res, err := conn.invokeHTTP(context.Background(), InvokeRequest{Entry: *entry, Prompt: "hello"},
		mcpConfig(entry), string(registry.TransportStreamableHTTP), url)
	if err != nil {
		t.Fatalf("an offline demo record must still answer, got %v", err)
	}
	if !strings.Contains(res.Text, "mcp=legacy-hr-mcp") || len(res.Artifacts) != 2 {
		t.Fatalf("the deterministic answer did not come back: %+v", res)
	}
}

// A record declaring tools opted into real invocation. Its unreachable endpoint
// must be reported, never silently mocked away.
func TestDeclaredMCPToolsNeverFallBackToMock(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unreachable", http.StatusBadGateway)
	}))
	url := server.URL
	server.Close()

	entry := &registry.Entry{ID: "mcp-1", Name: "docs", Kind: registry.KindMCP}
	cfg := &registry.MCPConfig{ToolAllowlist: []string{"call_action"}}
	conn := &mcpConnector{}
	_, err := conn.invokeHTTP(context.Background(), InvokeRequest{Entry: *entry, Prompt: "hello"},
		cfg, string(registry.TransportStreamableHTTP), url)
	if err == nil {
		t.Fatal("a declared tool must not be silently mocked")
	}
	if got := err.Error(); got != string(CodeResourceUnreachable) {
		t.Fatalf("code = %q, want %q", got, CodeResourceUnreachable)
	}
}
