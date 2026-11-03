package adapter

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"agenthub/internal/httputil"
	"agenthub/internal/registry"
	"agenthub/internal/sandbox"
)

// The adapters run commands and start MCP servers only through the sandbox
// control plane. These cases wire a real Manager over a stub provider, so the
// lease, the receipt and the policy are the real ones and only the process is
// replaced.

type stubProvider struct {
	mu        sync.Mutex
	execReq   sandbox.ExecRequest
	result    sandbox.ExecResult
	execErr   error
	session   sandbox.Session
	attachErr error
	available bool
	reason    string
}

func newStubProvider() *stubProvider {
	return &stubProvider{
		result:    sandbox.ExecResult{ExitCode: 0, Stdout: "done", Provider: "stub", Receipt: "exit"},
		available: true,
	}
}

func (p *stubProvider) Name() string { return "stub" }

// Describe declares the network vocabulary this stub accepts.
//
// It is not a boundary — it executes nothing — so it declares what the profile
// defaults ask for. Without a declaration Manager.Acquire refuses every lease on
// capability grounds and these cases would prove nothing about the adapter.
func (p *stubProvider) Describe(context.Context) (sandbox.Descriptor, error) {
	return sandbox.Descriptor{
		Name: "stub", Layer: sandbox.LayerProcess, Isolation: "process",
		Network: []string{sandbox.NetworkNone, sandbox.NetworkBridge, sandbox.NetworkPlatform},
	}, nil
}

func (p *stubProvider) Probe(context.Context) (sandbox.Availability, error) {
	return sandbox.Availability{Available: p.available, Reason: p.reason}, nil
}

func (p *stubProvider) Create(_ context.Context, spec sandbox.Spec) (sandbox.Handle, error) {
	return sandbox.Handle{ID: spec.LeaseID, Provider: "stub", Ref: "stub-ref", Spec: spec}, nil
}

func (p *stubProvider) Exec(_ context.Context, _ sandbox.Handle, req sandbox.ExecRequest) (sandbox.ExecResult, error) {
	p.mu.Lock()
	p.execReq = req
	p.mu.Unlock()
	return p.result, p.execErr
}

func (p *stubProvider) Attach(context.Context, sandbox.Handle, sandbox.ExecRequest) (sandbox.Session, error) {
	if p.attachErr != nil {
		return nil, p.attachErr
	}
	return p.session, nil
}

func (p *stubProvider) Destroy(context.Context, sandbox.Handle) error { return nil }

func (p *stubProvider) Status(context.Context, sandbox.Handle) (string, error) {
	return sandbox.StateReady, nil
}

func (p *stubProvider) lastExec() sandbox.ExecRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.execReq
}

// scriptedSession answers each round trip with the next canned frame, which is
// exactly what a stdio MCP handshake looks like from the connector's side.
type scriptedSession struct {
	mu     sync.Mutex
	frames [][]byte
	err    error
	closed bool
}

func (s *scriptedSession) RoundTrip(context.Context, []byte) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	if len(s.frames) == 0 {
		return []byte(`{"result":{}}`), nil
	}
	frame := s.frames[0]
	s.frames = s.frames[1:]
	return frame, nil
}

func (s *scriptedSession) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

func (s *scriptedSession) wasClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func sandboxFor(t *testing.T, provider *stubProvider) *sandbox.Service {
	t.Helper()
	store := sandbox.NewMemoryStore()
	registrys := sandbox.NewRegistry(sandbox.RegistryOptions{Default: "stub", Clock: time.Now})
	if err := registrys.Register(provider); err != nil {
		t.Fatalf("register stub: %v", err)
	}
	manager := sandbox.NewManager(sandbox.ManagerOptions{
		Registry:    registrys,
		Store:       store,
		Clock:       time.Now,
		Defaults:    sandbox.Defaults{Provider: "stub", Network: sandbox.NetworkNone, TimeoutMS: 1234, MaxOutputBytes: 8192},
		IdleTTL:     10 * time.Minute,
		MaxTTL:      time.Hour,
		MaxPerOwner: 4,
		MaxTotal:    16,
	})
	service := sandbox.NewService(sandbox.ServiceOptions{
		Manager:  manager,
		Registry: registrys,
		Bus:      sandbox.NewBus(store, nil),
		Queue:    sandbox.NewJobQueue(),
		Interval: time.Hour,
	})
	t.Cleanup(service.Stop)
	return service
}

func adapterCode(t *testing.T, err error) httputil.Code {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error")
	}
	var coded *httputil.Error
	if !errors.As(err, &coded) {
		t.Fatalf("expected a coded error, got %T: %v", err, err)
	}
	return coded.Code
}

func dataPart(t *testing.T, res InvokeResult) map[string]any {
	t.Helper()
	for _, part := range res.Parts {
		if part.Type != "data" || part.Data == nil {
			continue
		}
		raw, err := json.Marshal(part.Data)
		if err != nil {
			t.Fatalf("marshal data part: %v", err)
		}
		var out map[string]any
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("unmarshal data part: %v", err)
		}
		return out
	}
	t.Fatalf("no data part in %+v", res.Parts)
	return nil
}

// --- CLI ---

func cliEntryFor(t *testing.T, command, provider string) registry.Entry {
	t.Helper()
	return registry.Entry{
		ID:   "cli-1",
		Kind: registry.KindCLI,
		Name: "git",
		KindConfig: &registry.KindConfig{
			CLI:     &registry.CLIConfig{Command: command},
			Sandbox: &registry.SandboxConfig{Provider: provider, ArgsPolicy: "passthrough"},
		},
	}
}

func TestCLIRunsInsideALeasedEnvironment(t *testing.T) {
	provider := newStubProvider()
	provider.result = sandbox.ExecResult{ExitCode: 2, Stdout: "  pushed  ", Stderr: "ignored", Provider: "stub", Receipt: "exit", Truncated: true}
	service := sandboxFor(t, provider)

	connector, err := For(&registry.Entry{Kind: registry.KindCLI}, Options{CLIAllowlist: []string{"git"}, CLITimeout: 3 * time.Second, Sandbox: service})
	if err != nil {
		t.Fatalf("connector: %v", err)
	}
	e := cliEntryFor(t, "git", "")
	res, err := connector.Invoke(context.Background(), InvokeRequest{
		Entry:      e,
		Prompt:     "push the branch",
		Args:       []string{"--porcelain"},
		OwnerID:    "u1",
		ResourceID: e.ID,
		Scope:      "ticket-1",
	})
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}

	// The command comes from the record and the prompt is only stdin.
	if got := provider.lastExec(); got.Command != "git" || got.Stdin != "push the branch" {
		t.Fatalf("exec request = %+v", got)
	}
	if !strings.Contains(res.Text, "cli=git exit=2 truncated=true") {
		t.Fatalf("text = %q", res.Text)
	}
	if res.Parts[0].Type != "text" || !strings.Contains(res.Parts[0].Text, "pushed") {
		t.Fatalf("stdout must lead the parts, got %+v", res.Parts[0])
	}
	data := dataPart(t, res)
	if data["provider"] != "stub" || data["exit_code"].(float64) != 2 {
		t.Fatalf("data = %+v", data)
	}
	// The lease id is what lets an operator find the audit row.
	if data["lease_id"] == nil || data["lease_id"] == "" {
		t.Fatalf("data = %+v, want the lease id", data)
	}
	if len(res.Artifacts) != 1 || res.Artifacts[0].Name != "cli.receipt" {
		t.Fatalf("artifacts = %+v", res.Artifacts)
	}
}

func TestCLIFallsBackToStderrWhenStdoutIsEmpty(t *testing.T) {
	provider := newStubProvider()
	provider.result = sandbox.ExecResult{ExitCode: 1, Stderr: "  fatal: not a git repository  ", Provider: "stub"}
	service := sandboxFor(t, provider)

	connector, _ := For(&registry.Entry{Kind: registry.KindCLI}, Options{CLIAllowlist: []string{"git"}, Sandbox: service})
	res, err := connector.Invoke(context.Background(), InvokeRequest{Entry: cliEntryFor(t, "git", ""), OwnerID: "u1"})
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if res.Parts[0].Type != "text" || res.Parts[0].Text != "fatal: not a git repository" {
		t.Fatalf("parts = %+v", res.Parts)
	}
}

func TestCLIUsesTheConfiguredTimeoutWhenThePolicyHasNone(t *testing.T) {
	provider := newStubProvider()
	service := sandboxFor(t, provider)

	connector, _ := For(&registry.Entry{Kind: registry.KindCLI}, Options{CLIAllowlist: []string{"git"}, CLITimeout: 2500 * time.Millisecond, Sandbox: service})
	if _, err := connector.Invoke(context.Background(), InvokeRequest{Entry: cliEntryFor(t, "git", "")}); err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if got := provider.lastExec().TimeoutMS; got != 2500 {
		t.Fatalf("timeout = %d, want the configured CLI timeout", got)
	}
}

func TestCLIPropagatesASandboxFailure(t *testing.T) {
	provider := newStubProvider()
	provider.execErr = &httputil.Error{Code: sandbox.CodePolicyDenied}
	service := sandboxFor(t, provider)

	connector, _ := For(&registry.Entry{Kind: registry.KindCLI}, Options{CLIAllowlist: []string{"git"}, Sandbox: service})
	_, err := connector.Invoke(context.Background(), InvokeRequest{Entry: cliEntryFor(t, "git", "")})
	if code := adapterCode(t, err); code != sandbox.CodePolicyDenied {
		t.Fatalf("code = %s, want the provider's own refusal", code)
	}
}

func TestCLIHealthMeasuresTheProviderWhenSandboxed(t *testing.T) {
	provider := newStubProvider()
	service := sandboxFor(t, provider)
	connector, _ := For(&registry.Entry{Kind: registry.KindCLI}, Options{CLIAllowlist: []string{"git"}, Sandbox: service})
	e := cliEntryFor(t, "git", "")

	// The binary lives inside the environment, so health is the provider's health.
	health, err := connector.Health(context.Background(), &e)
	if err != nil || health != registry.HealthHealthy {
		t.Fatalf("health = %v (%v)", health, err)
	}

	// The catalogue caches probes, so an availability change only shows up after a
	// refresh: that is the console's refresh button.
	provider.available = false
	service.RefreshCatalog(context.Background())
	health, _ = connector.Health(context.Background(), &e)
	if health != registry.HealthUnhealthy {
		t.Fatalf("health = %v, want unhealthy when the provider cannot run here", health)
	}

	// A record that names no provider inherits the instance default, which is the
	// same provider here: still unhealthy while it cannot run.
	implicit := cliEntryFor(t, "git", "")
	if health, _ := connector.Health(context.Background(), &implicit); health != registry.HealthUnhealthy {
		t.Fatalf("health = %v, want the default provider's verdict", health)
	}
}

// --- MCP over stdio ---

func stdioMCPEntry(allowlist []string) registry.Entry {
	return registry.Entry{
		ID:   "mcp-1",
		Kind: registry.KindMCP,
		Name: "docs",
		KindConfig: &registry.KindConfig{
			MCP: &registry.MCPConfig{
				Transport:     string(registry.TransportStdio),
				Command:       "mcp-server",
				ToolAllowlist: allowlist,
			},
			Sandbox: &registry.SandboxConfig{ArgsPolicy: "passthrough"},
		},
	}
}

func toolsFrame(names ...string) []byte {
	tools := make([]map[string]any, 0, len(names))
	for _, name := range names {
		tools = append(tools, map[string]any{"name": name, "description": name})
	}
	raw, _ := json.Marshal(map[string]any{"result": map[string]any{"tools": tools}})
	return raw
}

func TestMCPStdioCallsTheFirstAdvertisedAllowedTool(t *testing.T) {
	session := &scriptedSession{frames: [][]byte{
		[]byte(`{"result":{"protocolVersion":"2024-11-05"}}`),
		toolsFrame("search", "write"),
		[]byte(`{"result":{"content":[{"type":"text","text":"ok"}]}}`),
	}}
	provider := newStubProvider()
	provider.session = session
	service := sandboxFor(t, provider)

	connector, _ := For(&registry.Entry{Kind: registry.KindMCP}, Options{Sandbox: service})
	e := stdioMCPEntry([]string{"write"})
	if _, err := connector.Invoke(context.Background(), InvokeRequest{Entry: e, Prompt: "the docs", OwnerID: "u1"}); err != nil {
		t.Fatalf("invoke: %v", err)
	}

	res, err := connector.Invoke(context.Background(), InvokeRequest{Entry: e, Prompt: "the docs", OwnerID: "u1"})
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if !strings.Contains(res.Text, "transport=stdio") || !strings.Contains(res.Text, "tool=write") {
		t.Fatalf("text = %q", res.Text)
	}
	data := dataPart(t, res)
	if data["transport"] != "stdio" || data["lease_id"] == nil {
		t.Fatalf("data = %+v", data)
	}
	if len(res.Artifacts) != 2 || res.Artifacts[0].Name != "mcp.tools" || res.Artifacts[1].Name != "mcp.call" {
		t.Fatalf("artifacts = %+v", res.Artifacts)
	}
	if !session.wasClosed() {
		t.Fatal("the session must be closed so the environment can be reused cleanly")
	}
}

func TestMCPStdioRefusesWhenNothingIsAllowed(t *testing.T) {
	session := &scriptedSession{frames: [][]byte{
		[]byte(`{"result":{}}`),
		toolsFrame("search"),
		[]byte(`{"result":{}}`),
	}}
	provider := newStubProvider()
	provider.session = session
	service := sandboxFor(t, provider)
	connector, _ := For(&registry.Entry{Kind: registry.KindMCP}, Options{Sandbox: service})

	// An empty allowlist permits nothing: for MCP the safe default is "no tools".
	_, err := connector.Invoke(context.Background(), InvokeRequest{Entry: stdioMCPEntry(nil), Prompt: "x"})
	if code := adapterCode(t, err); code != sandbox.CodePolicyDenied {
		t.Fatalf("code = %s", code)
	}
}

func TestMCPStdioReportsAToolError(t *testing.T) {
	session := &scriptedSession{frames: [][]byte{
		[]byte(`{"result":{}}`),
		toolsFrame("search"),
		[]byte(`{"error":{"message":"boom"}}`),
	}}
	provider := newStubProvider()
	provider.session = session
	service := sandboxFor(t, provider)
	connector, _ := For(&registry.Entry{Kind: registry.KindMCP}, Options{Sandbox: service})

	_, err := connector.Invoke(context.Background(), InvokeRequest{Entry: stdioMCPEntry([]string{"search"}), Prompt: "x"})
	if code := adapterCode(t, err); code != CodeResourceUnreachable {
		t.Fatalf("code = %s, want the tool error to surface", code)
	}
}

func TestMCPStdioPropagatesATransportFailure(t *testing.T) {
	provider := newStubProvider()
	provider.session = &scriptedSession{err: &httputil.Error{Code: sandbox.CodeLeaseLost}}
	service := sandboxFor(t, provider)
	connector, _ := For(&registry.Entry{Kind: registry.KindMCP}, Options{Sandbox: service})

	_, err := connector.Invoke(context.Background(), InvokeRequest{Entry: stdioMCPEntry([]string{"search"}), Prompt: "x"})
	if code := adapterCode(t, err); code != sandbox.CodeLeaseLost {
		t.Fatalf("code = %s", code)
	}
}

func TestMCPStdioPropagatesAnAttachFailure(t *testing.T) {
	provider := newStubProvider()
	provider.attachErr = &httputil.Error{Code: CodeResourceUnreachable}
	service := sandboxFor(t, provider)
	connector, _ := For(&registry.Entry{Kind: registry.KindMCP}, Options{Sandbox: service})

	_, err := connector.Invoke(context.Background(), InvokeRequest{Entry: stdioMCPEntry([]string{"search"}), Prompt: "x"})
	if code := adapterCode(t, err); code != CodeResourceUnreachable {
		t.Fatalf("code = %s", code)
	}
}

func TestMCPHealthMeasuresTheProviderWhenSandboxed(t *testing.T) {
	provider := newStubProvider()
	service := sandboxFor(t, provider)
	connector, _ := For(&registry.Entry{Kind: registry.KindMCP}, Options{Sandbox: service})
	e := stdioMCPEntry([]string{"search"})

	if health, err := connector.Health(context.Background(), &e); err != nil || health != registry.HealthHealthy {
		t.Fatalf("health = %v (%v)", health, err)
	}
	provider.available = false
	provider.reason = "not installed"
	service.RefreshCatalog(context.Background())
	if health, _ := connector.Health(context.Background(), &e); health != registry.HealthUnhealthy {
		t.Fatalf("health = %v, want unhealthy", health)
	}
	if health, _ := connector.Health(context.Background(), nil); health != registry.HealthUnknown {
		t.Fatalf("health = %v, want unknown for no entry", health)
	}
}

// --- agent safety seams ---

func TestAgentCredentialIsAttachedOnlyWhenConfigured(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "http://agent.test/a2a", nil)
	(&agentConnector{}).authorize(req)
	if got := req.Header.Get("Authorization"); got != "" {
		t.Fatalf("no configured variable means no header, got %q", got)
	}

	t.Setenv("AGENTHUB_TEST_TOKEN", "   ")
	(&agentConnector{credentialEnv: "AGENTHUB_TEST_TOKEN"}).authorize(req)
	if got := req.Header.Get("Authorization"); got != "" {
		t.Fatalf("a blank value must not become a header, got %q", got)
	}

	t.Setenv("AGENTHUB_TEST_TOKEN", "s3cret")
	(&agentConnector{credentialEnv: "AGENTHUB_TEST_TOKEN"}).authorize(req)
	if got := req.Header.Get("Authorization"); got != "Bearer s3cret" {
		t.Fatalf("authorization = %q", got)
	}
}

func TestAgentTargetMustMatchTheEgressAllowlist(t *testing.T) {
	open := &agentConnector{}
	if err := open.allowedTarget("http://127.0.0.1:8080/a2a"); err != nil {
		t.Fatalf("an undeclared allowlist keeps the previous behaviour: %v", err)
	}

	guarded := &agentConnector{egress: []string{" Example.COM ", "*.partner.test", "", "internal.test"}}
	for _, tc := range []struct {
		url string
		ok  bool
	}{
		{"https://example.com/a2a", true},
		{"https://api.example.com/a2a", true}, // a subdomain of an allowed host
		{"https://partner.test/a2a", true},
		{"https://eu.partner.test/a2a", true},
		{"https://internal.test.evil/a2a", false}, // suffix match must be on labels
		{"https://evil.internal.test/a2a", true},
		{"https://127.0.0.1/a2a", false},
		{"not a url", false},
		{"http:///nohost", false},
	} {
		err := guarded.allowedTarget(tc.url)
		if tc.ok && err != nil {
			t.Errorf("%s: refused (%v)", tc.url, err)
		}
		if !tc.ok {
			if err == nil {
				t.Errorf("%s: must be refused", tc.url)
				continue
			}
			if code := adapterCode(t, err); code != sandbox.CodePolicyDenied {
				t.Errorf("%s: code = %s", tc.url, code)
			}
		}
	}
}

func TestAgentHelpersKeepTheirContracts(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want bool
	}{
		{"http://x/a2a", true},
		{"https://x/a2a", true},
		{"ftp://x", false},
		{"", false},
	} {
		if got := isHTTP(tc.raw); got != tc.want {
			t.Errorf("isHTTP(%q) = %v", tc.raw, got)
		}
	}
	for _, tc := range []struct {
		prompt string
		want   bool
	}{
		{"please approve the transfer", true},
		{"needs your approval", true},
		{"confirm the payment", true},
		{"confirmed already", true},
		{"approved", false}, // an answer must not re-trigger the question
		{"summarise this", false},
	} {
		if got := requiresApproval(tc.prompt); got != tc.want {
			t.Errorf("requiresApproval(%q) = %v, want %v", tc.prompt, got, tc.want)
		}
	}
}

// --- model provider failure modes ---

func TestOpenAIProviderErrorMap(t *testing.T) {
	var nilProvider *openAIProvider
	if _, err := nilProvider.Generate(context.Background(), "hi"); adapterCode(t, err) != CodeResourceUnreachable {
		t.Fatalf("a nil provider must not panic")
	}
	if got := nilProvider.effectiveMaxTokens(); got != registry.DefaultModelMaxTokens {
		t.Fatalf("max tokens = %d, want the shared default", got)
	}
	if _, err := newOpenAIProvider("m", "", "", "", 10*time.Second, 0).Generate(context.Background(), "hi"); adapterCode(t, err) != CodeResourceUnreachable {
		t.Fatalf("an empty endpoint must be refused before the request is built")
	}

	provider := newOpenAIProvider("m", "http://127.0.0.1:1/v1", "", "", 500*time.Millisecond, 0)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := provider.Generate(canceled, "hi"); !errors.Is(err, context.Canceled) {
		t.Fatalf("a cancelled call must return the context error, got %v", err)
	}
	if _, err := provider.Generate(context.Background(), "hi"); adapterCode(t, err) != CodeResourceUnreachable {
		t.Fatalf("nothing is listening on the endpoint")
	}
	if got := provider.effectiveMaxTokens(); got != registry.DefaultModelMaxTokens {
		t.Fatalf("max tokens = %d", got)
	}
}

func TestOpenAIProviderMapsStatusesAndShapes(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		code   httputil.Code
		want   string
		apiKey string
	}{
		{name: "unauthorized", status: http.StatusUnauthorized, body: `{}`, code: CodeProviderUnauthorized},
		{name: "forbidden", status: http.StatusForbidden, body: `{}`, code: CodeProviderUnauthorized},
		{name: "server error", status: http.StatusInternalServerError, body: `{}`, code: CodeResourceUnreachable},
		{name: "not json", status: http.StatusOK, body: `not json`, code: CodeProviderMalformed},
		{name: "no choices", status: http.StatusOK, body: `{"choices":[]}`, code: CodeProviderMalformed},
		{name: "content wins", status: http.StatusOK, body: `{"choices":[{"message":{"content":"answer","reasoning_content":"thought"}}]}`, want: "answer"},
		{name: "reasoning content", status: http.StatusOK, body: `{"choices":[{"message":{"reasoning_content":"thought"}}]}`, want: "thought"},
		{name: "reasoning", status: http.StatusOK, body: `{"choices":[{"message":{"reasoning":"thought"}}]}`, want: "thought"},
		{name: "legacy text", status: http.StatusOK, body: `{"choices":[{"text":"legacy"}]}`, want: "legacy"},
		{name: "empty choice", status: http.StatusOK, body: `{"choices":[{"message":{"content":"   "}},{"message":{"content":"second"}}]}`, want: "second"},
		{name: "credential header", status: http.StatusOK, body: `{"choices":[{"message":{"content":"answer"}}]}`, want: "answer", apiKey: "sk-test"},
	}

	for _, tc := range cases {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if tc.apiKey != "" && r.Header.Get("Authorization") != "Bearer "+tc.apiKey {
				t.Errorf("%s: authorization = %q", tc.name, r.Header.Get("Authorization"))
			}
			if ct := r.Header.Get("Content-Type"); ct != "application/json" {
				t.Errorf("%s: content type = %q", tc.name, ct)
			}
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte(tc.body))
		}))

		provider := newOpenAIProvider("gpt-test", server.URL+"/v1", "", "AGENTHUB_TEST_API_KEY", 5*time.Second, 64)
		if tc.apiKey != "" {
			t.Setenv("AGENTHUB_TEST_API_KEY", tc.apiKey)
		} else {
			t.Setenv("AGENTHUB_TEST_API_KEY", "")
		}
		reply, err := provider.Generate(context.Background(), "hi")
		server.Close()

		if tc.want != "" {
			if err != nil || reply != tc.want {
				t.Errorf("%s: reply = %q err = %v, want %q", tc.name, reply, err, tc.want)
			}
			continue
		}
		if err == nil {
			t.Errorf("%s: expected an error", tc.name)
			continue
		}
		if code := adapterCode(t, err); code != tc.code {
			t.Errorf("%s: code = %s, want %s", tc.name, code, tc.code)
		}
	}
}

func TestOpenAIProviderSendsTheConfiguredBudget(t *testing.T) {
	var payload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&payload)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer server.Close()

	provider := newOpenAIProvider("openai", server.URL, "gpt-test", "", 5*time.Second, 128)
	if _, err := provider.Generate(context.Background(), "hi"); err != nil {
		t.Fatalf("generate: %v", err)
	}
	if got, _ := payload["max_tokens"].(float64); int(got) != 128 {
		t.Fatalf("max_tokens = %v, want the configured budget", payload["max_tokens"])
	}
	if payload["model"] != "gpt-test" || payload["stream"] != false {
		t.Fatalf("payload = %+v", payload)
	}
	if _, ok := os.LookupEnv("AGENTHUB_TEST_API_KEY"); !ok {
		// Only here to document that no credential is required for a local model.
		t.Log("no api key configured, which is the offline case")
	}
}
