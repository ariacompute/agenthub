package adapter

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"agenthub/internal/a2a"
	"agenthub/internal/httputil"
	"agenthub/internal/registry"
	"agenthub/internal/sandbox"
)

// This file closes the last gaps in the package's statement coverage: the
// branches a normal call never reaches, and only those. Each case names the
// caller-visible consequence, because "the line ran" is not a test.

func TestMockAgentSaysWhatItWasHanded(t *testing.T) {
	entry := registry.Entry{ID: "agent-1", Kind: registry.KindAgent, Name: "researcher"}
	// A mock cannot use a package, but the trace still has to show that the binds
	// resolved and how many of them did not.
	res := mockAgentInvoke(InvokeRequest{
		ResourceID: entry.ID,
		Entry:      entry,
		Prompt:     "plan the week",
		Distribution: &registry.Distribution{
			AgentID:    entry.ID,
			MCP:        []registry.MCPBinding{{ID: "mcp-1"}},
			Skills:     []registry.SkillBinding{{ID: "skill-1"}},
			Missing:    []string{"skill-gone"},
			Unresolved: []string{"legacy label"},
		},
	})
	var data map[string]any
	for _, part := range res.Parts {
		if part.Data != nil {
			data = part.Data
		}
	}
	if data == nil {
		t.Fatal("no data part")
	}
	summary, ok := data["distribution"].(map[string]any)
	if !ok {
		t.Fatalf("distribution summary = %#v", data["distribution"])
	}
	if summary["mcp"] != 1 || summary["skills"] != 1 || summary["missing"] != 1 || summary["unresolved"] != 1 {
		t.Fatalf("summary = %#v", summary)
	}

	// Without a package the key is absent rather than zeroed: a mock run of an
	// unbound agent must not look like it was handed empty abilities.
	bare := mockAgentInvoke(InvokeRequest{ResourceID: entry.ID, Entry: entry, Prompt: "hi"})
	for _, part := range bare.Parts {
		if part.Data != nil {
			if _, ok := part.Data["distribution"]; ok {
				t.Fatal("an unbound agent reported a package")
			}
		}
	}
}

// A card whose endpoint cannot even become a request is unreachable, not
// malformed: the operator has to fix an address, not a payload.
func TestAgentCallsRejectAnUnusableEndpoint(t *testing.T) {
	conn := &agentConnector{client: http.DefaultClient, timeout: time.Second}
	entry := registry.Entry{ID: "agent-1", Kind: registry.KindAgent, Name: "x"}
	req := InvokeRequest{ResourceID: entry.ID, Entry: entry, Prompt: "hi"}

	for _, tc := range []struct {
		name string
		call func() (string, string, error)
	}{
		{"send", func() (string, string, error) {
			return conn.sendMessage(context.Background(), "http://bad host/x", req)
		}},
		{"stream", func() (string, string, error) {
			return conn.streamMessage(context.Background(), "http://bad host/x", req)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := tc.call(); httputil.AsError(err).Code != CodeResourceUnreachable {
				t.Fatalf("code = %q", httputil.AsError(err).Code)
			}
		})
	}
}

// The two ways a stream dies mid-flight are distinct on the wire: an oversized
// payload is the peer sending too much, a read error is the peer going away.
func TestAgentStreamDistinguishesOversizedFromDropped(t *testing.T) {
	entry := registry.Entry{ID: "agent-1", Kind: registry.KindAgent, Name: "x"}
	req := InvokeRequest{ResourceID: entry.ID, Entry: entry, Prompt: "hi"}

	t.Run("oversized", func(t *testing.T) {
		// Each frame is valid and each line stays under the per-line cap, so the
		// stream is read frame by frame until the total budget runs out rather
		// than failing on the first unparsable frame.
		frame := `data: {"result":{"kind":"status-update","status":{"state":"working","message":{"parts":[{"type":"text","text":"` +
			strings.Repeat("x", 30<<10) + `"}]}}}}` + "\n\n"
		huge := strings.Repeat(frame, 40)
		conn := &agentConnector{client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(huge))}, nil
		})}}
		if _, _, err := conn.streamMessage(context.Background(), "http://agent.test/rpc", req); httputil.AsError(err).Code != CodeProviderMalformed {
			t.Fatalf("code = %q", httputil.AsError(err).Code)
		}
	})

	t.Run("dropped", func(t *testing.T) {
		conn := &agentConnector{client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: failingBody{}}, nil
		})}}
		if _, _, err := conn.streamMessage(context.Background(), "http://agent.test/rpc", req); httputil.AsError(err).Code != CodeResourceUnreachable {
			t.Fatalf("code = %q", httputil.AsError(err).Code)
		}
	})
}

// A frame the client cannot read is malformed; the frame itself is not retried.
func TestAgentStreamRejectsUnreadableFrames(t *testing.T) {
	entry := registry.Entry{ID: "agent-1", Kind: registry.KindAgent, Name: "x"}
	req := InvokeRequest{ResourceID: entry.ID, Entry: entry, Prompt: "hi"}

	for _, tc := range []struct {
		name string
		body string
		code httputil.Code
	}{
		{"not json", "data: {nope}\n\n", CodeProviderMalformed},
		{"unreadable result", "data: {\"result\":{\"kind\":\"message\",\"parts\":\"oops\"}}\n\n", CodeProviderMalformed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conn := &agentConnector{client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(tc.body))}, nil
			})}}
			if _, _, err := conn.streamMessage(context.Background(), "http://agent.test/rpc", req); httputil.AsError(err).Code != tc.code {
				t.Fatalf("code = %q", httputil.AsError(err).Code)
			}
		})
	}
}

func TestMCPPostReportsAnUnusableURL(t *testing.T) {
	if _, err := mcpPost(context.Background(), http.DefaultClient, "http://bad host/x", "{}"); err == nil {
		t.Fatal("an unusable url was accepted")
	}
}

// newJSONServer answers each call with the status and body the case asks for,
// counting from one, so a test can say "the second call fails".
func newJSONServer(t *testing.T, reply func(call int) (int, string)) *httptest.Server {
	t.Helper()
	call := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call++
		status, body := reply(call)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	return srv
}

func TestMCPHTTPCallFailureIsUnreachable(t *testing.T) {
	// tools/list answers, tools/call does not: the tool existed, the call did
	// not, and that is a transport failure rather than a policy refusal.
	srv := newJSONServer(t, func(call int) (int, string) {
		if call == 1 {
			return http.StatusOK, `{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"lookup"}]}}`
		}
		return http.StatusInternalServerError, `{"jsonrpc":"2.0","id":3,"error":{"message":"boom"}}`
	})
	defer srv.Close()

	conn := &mcpConnector{}
	entry := registry.Entry{ID: "mcp-1", Kind: registry.KindMCP, Name: "hr", Endpoint: srv.URL}
	cfg := &registry.MCPConfig{Transport: string(registry.TransportStreamableHTTP), ToolAllowlist: []string{"lookup"}}
	if _, err := conn.invokeHTTP(context.Background(), InvokeRequest{ResourceID: entry.ID, Entry: entry, Prompt: "hi"}, cfg, string(registry.TransportStreamableHTTP), srv.URL); httputil.AsError(err).Code != CodeResourceUnreachable {
		t.Fatalf("code = %q", httputil.AsError(err).Code)
	}
}

// failingProvider never answers, so the router has to report the provider's own
// error instead of inventing a completion.
type failingProvider struct{ err error }

func (p failingProvider) Name() string { return "failing" }
func (p failingProvider) Generate(context.Context, string) (string, error) {
	return "", p.err
}
func (p failingProvider) Health(context.Context) (registry.Health, error) {
	return registry.HealthUnhealthy, p.err
}

func TestModelRouterKeepsTheLastProviderError(t *testing.T) {
	boom := errors.New("upstream 502")
	router := &ModelRouter{providers: []ModelProvider{failingProvider{err: boom}}}
	if _, _, err := router.GenerateWith(context.Background(), "hi"); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if _, _, err := router.GenerateStreamWith(context.Background(), "hi", nil); !errors.Is(err, boom) {
		t.Fatalf("stream err = %v", err)
	}
}

func TestModelConnectorBuildsARouterWhenItHasNone(t *testing.T) {
	// A connector restored from config has no router yet; the first call builds
	// one against the instance wide mode instead of panicking on a nil deref.
	conn := &modelConnector{defaultMode: string(registry.ProviderModeMock)}
	entry := registry.Entry{ID: "model-1", Kind: registry.KindModel, Name: "brain"}
	res, err := conn.Invoke(context.Background(), InvokeRequest{ResourceID: entry.ID, Entry: entry, Prompt: "hi"})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if strings.TrimSpace(res.Text) == "" {
		t.Fatal("no text")
	}
}

func TestOpenAICallsRejectAnUnusableEndpoint(t *testing.T) {
	p := newOpenAIProvider("openai:x", "http://bad host/v1", "m", "", time.Second, 8)
	if _, err := p.Generate(context.Background(), "hi"); httputil.AsError(err).Code != CodeResourceUnreachable {
		t.Fatalf("generate code = %q", httputil.AsError(err).Code)
	}
	if _, err := p.GenerateStream(context.Background(), "hi", nil); httputil.AsError(err).Code != CodeResourceUnreachable {
		t.Fatalf("stream code = %q", httputil.AsError(err).Code)
	}
}

func TestOpenAIReportsABrokenBody(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: failingBody{}}, nil
	})}
	p := newOpenAIProvider("openai:x", "http://provider.test/v1", "m", "", time.Second, 8)
	p.client = client

	if _, err := p.Generate(context.Background(), "hi"); httputil.AsError(err).Code != CodeResourceUnreachable {
		t.Fatalf("generate code = %q", httputil.AsError(err).Code)
	}
	if _, err := p.GenerateStream(context.Background(), "hi", nil); httputil.AsError(err).Code != CodeResourceUnreachable {
		t.Fatalf("stream code = %q", httputil.AsError(err).Code)
	}
}

func TestOpenAIStreamSkipsFramesWithoutText(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body: io.NopCloser(strings.NewReader(
				"data: {\"choices\":[{\"delta\":{}}]}\n\n" +
					"data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n" +
					"data: [DONE]\n\n")),
		}, nil
	})}
	p := newOpenAIProvider("openai:x", "http://provider.test/v1", "m", "", time.Second, 8)
	p.client = client
	out, err := p.GenerateStream(context.Background(), "hi", nil)
	if err != nil {
		t.Fatalf("GenerateStream: %v", err)
	}
	// The empty frame contributes nothing, and must not have been mistaken for
	// the answer either.
	if out != "ok" {
		t.Fatalf("out = %q", out)
	}
}

func TestModelRouterSkipsATargetWithoutAModel(t *testing.T) {
	entry := registry.Entry{
		ID: "model-1", Kind: registry.KindModel, Name: "brain",
		KindConfig: &registry.KindConfig{Model: &registry.ModelConfig{
			ProviderMode: registry.ProviderModeOpenAICompatible,
			Endpoint:     "http://provider.test/v1",
			ModelID:      "primary",
			Targets: []registry.ModelTarget{
				{Name: "unnamed"},
				{Name: "same", Model: "primary"},
				{Name: "fallback", Model: "second"},
			},
		}},
	}
	router := newModelRouterFor(&entry, "")
	// primary + the real fallback; a target with no model (or the same model
	// twice) would only add a duplicate call.
	if len(router.providers) != 2 {
		t.Fatalf("providers = %d", len(router.providers))
	}
}

func TestProbeHelpersRefuseWhatTheyCannotCall(t *testing.T) {
	if _, err := probeHTTP(context.Background(), http.DefaultClient, http.MethodGet, "http://bad host/x", nil, nil); httputil.AsError(err).Code != CodeResourceUnreachable {
		t.Fatalf("code = %q", httputil.AsError(err).Code)
	}
	if got := endpointOf(nil); got != "" {
		t.Fatalf("endpointOf(nil) = %q", got)
	}
}

func TestLeaseProfileCarriesDeclaredEnvKeys(t *testing.T) {
	cli := registry.Entry{ID: "cli-1", Kind: registry.KindCLI, Name: "shell",
		KindConfig: &registry.KindConfig{CLI: &registry.CLIConfig{Env: map[string]string{"B": "2", "A": "1"}}}}
	if got := leaseProfile(&cli).EnvAllow; len(got) != 2 || got[0] != "A" || got[1] != "B" {
		t.Fatalf("cli env = %v", got)
	}

	mcp := registry.Entry{ID: "mcp-1", Kind: registry.KindMCP, Name: "hr",
		KindConfig: &registry.KindConfig{MCP: &registry.MCPConfig{Env: map[string]string{"TOKEN_NAME": "x"}}}}
	if got := leaseProfile(&mcp).EnvAllow; len(got) != 1 || got[0] != "TOKEN_NAME" {
		t.Fatalf("mcp env = %v", got)
	}
}

func TestArgsPolicyWithoutAnyConfigIsNone(t *testing.T) {
	entry := registry.Entry{ID: "cli-1", Kind: registry.KindCLI, Name: "shell", KindConfig: &registry.KindConfig{}}
	if got := argsPolicy(&entry); got != "none" {
		t.Fatalf("policy = %q", got)
	}
}

func TestSkillInvokeStopsOnACancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := newSkillConnector().Invoke(ctx, InvokeRequest{Entry: registry.Entry{Kind: registry.KindSkill, Name: "s"}}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

func TestSkillManualHTTPFailures(t *testing.T) {
	entry := registry.Entry{ID: "skill-1", Kind: registry.KindSkill, Name: "onboarding"}

	t.Run("unusable endpoint", func(t *testing.T) {
		conn := &skillConnector{client: http.DefaultClient}
		e := entry
		e.Endpoint = "http://bad host/SKILL.md"
		if _, err := conn.loadManual(context.Background(), &e); httputil.AsError(err).Code != CodeResourceUnreachable {
			t.Fatalf("code = %q", httputil.AsError(err).Code)
		}
	})

	t.Run("non-2xx", func(t *testing.T) {
		conn := &skillConnector{client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader(""))}, nil
		})}}
		e := entry
		e.Endpoint = "http://skill.test/SKILL.md"
		if _, err := conn.loadManual(context.Background(), &e); httputil.AsError(err).Code != CodeResourceUnreachable {
			t.Fatalf("code = %q", httputil.AsError(err).Code)
		}
	})

	t.Run("broken body", func(t *testing.T) {
		conn := &skillConnector{client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Body: failingBody{}}, nil
		})}}
		e := entry
		e.Endpoint = "http://skill.test/SKILL.md"
		if _, err := conn.loadManual(context.Background(), &e); httputil.AsError(err).Code != CodeResourceUnreachable {
			t.Fatalf("code = %q", httputil.AsError(err).Code)
		}
	})
}

func TestSpeechUsesTheConfiguredProvider(t *testing.T) {
	s := Speech{provider: fakeSpeech{}}
	audio, err := s.Synthesize(context.Background(), "hello")
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if string(audio) != "fake" {
		t.Fatalf("audio = %q", audio)
	}
}

type fakeSpeech struct{}

func (fakeSpeech) Transcribe(context.Context, []byte) (string, error) { return "fake", nil }
func (fakeSpeech) Synthesize(context.Context, string) ([]byte, error) { return []byte("fake"), nil }

func TestSSEDecoderRefusesAnOversizedStream(t *testing.T) {
	dec := newSSEDecoderLimit(strings.NewReader("data: 0123456789\n\n"), 4)
	if _, _, _, err := dec.Next(); !errors.Is(err, errStreamTooLarge) {
		t.Fatalf("err = %v", err)
	}
}

func TestResultFrameWithoutText(t *testing.T) {
	// A finalized task with no parts still names its state; the caller then falls
	// back to the "no text part returned" sentence rather than an empty answer.
	text, state, kind, final, err := agentFrame([]byte(`{"kind":"status-update","final":true,"status":{"state":"completed"}}`))
	if err != nil || text != "" || state != "completed" || kind != a2a.KindStatusUpdate || !final {
		t.Fatalf("text=%q state=%q kind=%q final=%v err=%v", text, state, kind, final, err)
	}
}

func TestNewMessageIDFallsBackToTheClock(t *testing.T) {
	original := randRead
	randRead = func([]byte) (int, error) { return 0, errors.New("no entropy") }
	t.Cleanup(func() { randRead = original })

	// A clock-derived id is worse than a random one but still correlates a
	// message with its task, which is all the field is for.
	id := newMessageID()
	if !strings.HasPrefix(id, "agenthub-") {
		t.Fatalf("id = %q", id)
	}
}

func TestMCPHTTPFallsBackOnlyWithoutDeclaredTools(t *testing.T) {
	// The demo records name an endpoint nobody serves and declare no allowlist:
	// no tool was ever permitted, so the deterministic answer stands.
	offline := newJSONServer(t, func(int) (int, string) { return http.StatusBadGateway, `{}` })
	defer offline.Close()

	conn := &mcpConnector{}
	entry := registry.Entry{ID: "mcp-1", Kind: registry.KindMCP, Name: "demo-mcp", Endpoint: offline.URL}
	cfg := &registry.MCPConfig{Transport: string(registry.TransportStreamableHTTP)}
	res, err := conn.invokeHTTP(context.Background(), InvokeRequest{ResourceID: entry.ID, Entry: entry, Prompt: "hi"}, cfg, string(registry.TransportStreamableHTTP), offline.URL)
	if err != nil {
		t.Fatalf("invokeHTTP: %v", err)
	}
	if strings.TrimSpace(res.Text) == "" {
		t.Fatal("the deterministic answer is empty")
	}
}

func TestMCPHTTPRefusesWhenNoAdvertisedToolIsAllowed(t *testing.T) {
	// A record that declares tools keeps the strict behaviour: the server
	// advertised nothing allowed, so nothing is called.
	srv := newJSONServer(t, func(int) (int, string) {
		return http.StatusOK, `{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"other"}]}}`
	})
	defer srv.Close()

	conn := &mcpConnector{}
	entry := registry.Entry{ID: "mcp-1", Kind: registry.KindMCP, Name: "hr", Endpoint: srv.URL}
	// A blank entry is the one way the allowlist can exist and still permit
	// nothing, which is exactly the case the refusal exists for.
	cfg := &registry.MCPConfig{Transport: string(registry.TransportStreamableHTTP), ToolAllowlist: []string{"  "}}
	if _, err := conn.invokeHTTP(context.Background(), InvokeRequest{ResourceID: entry.ID, Entry: entry, Prompt: "hi"}, cfg, string(registry.TransportStreamableHTTP), srv.URL); httputil.AsError(err).Code != sandbox.CodePolicyDenied {
		t.Fatalf("code = %q", httputil.AsError(err).Code)
	}
}

func TestSSEDecoderDefaultBudgetTripsOnManySmallLines(t *testing.T) {
	body := strings.Repeat("data: "+strings.Repeat("x", 30<<10)+"\n\n", 40)
	dec := newSSEDecoder(strings.NewReader(body))
	for i := 0; i < 100; i++ {
		_, _, ok, err := dec.Next()
		if err != nil {
			if !errors.Is(err, errStreamTooLarge) {
				t.Fatalf("err = %v", err)
			}
			return
		}
		if !ok {
			break
		}
	}
	t.Fatal("the default budget never tripped")
}

func TestMCPHTTPWithoutAnEndpointStaysOffline(t *testing.T) {
	// A record with neither a config URL nor an endpoint URL cannot be dialled;
	// the deterministic answer stands rather than dialling an empty address.
	conn := &mcpConnector{}
	entry := registry.Entry{ID: "mcp-1", Kind: registry.KindMCP, Name: "hr"}
	cfg := &registry.MCPConfig{Transport: string(registry.TransportStreamableHTTP)}
	res, err := conn.invokeHTTP(context.Background(), InvokeRequest{ResourceID: entry.ID, Entry: entry, Prompt: "hi"}, cfg, string(registry.TransportStreamableHTTP), "")
	if err != nil {
		t.Fatalf("invokeHTTP: %v", err)
	}
	if strings.TrimSpace(res.Text) == "" {
		t.Fatal("no deterministic answer")
	}
}

func TestOpenAIStreamSkipsAnEmptyContentFrame(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body: io.NopCloser(strings.NewReader(
				"data: {\"choices\":[{\"delta\":{\"content\":\"\"}}]}\n\n" +
					"data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n" +
					"data: [DONE]\n\n")),
		}, nil
	})}
	p := newOpenAIProvider("openai:x", "http://provider.test/v1", "m", "", time.Second, 8)
	p.client = client
	out, err := p.GenerateStream(context.Background(), "hi", nil)
	if err != nil || out != "ok" {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

func TestOpenAIStreamSkipsABlankDataEvent(t *testing.T) {
	// A gateway that emits an empty data field is sending a heartbeat, not a
	// completion; skipping it keeps the answer the frames that did carry text.
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body: io.NopCloser(strings.NewReader(
				"data:\n\n" +
					"data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n" +
					"data: [DONE]\n\n")),
		}, nil
	})}
	p := newOpenAIProvider("openai:x", "http://provider.test/v1", "m", "", time.Second, 8)
	p.client = client
	out, err := p.GenerateStream(context.Background(), "hi", nil)
	if err != nil || out != "ok" {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

func TestReadSkillFileFailures(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		if _, err := readSkillFile(filepath.Join(t.TempDir(), "nope.md")); httputil.AsError(err).Code != CodeResourceUnreachable {
			t.Fatalf("code = %q", httputil.AsError(err).Code)
		}
	})

	t.Run("directory", func(t *testing.T) {
		// Opening a directory succeeds and reading it does not; that is the read
		// error path a real filesystem gives us.
		dir := t.TempDir()
		if _, err := readSkillFile(dir); httputil.AsError(err).Code != CodeResourceUnreachable {
			t.Fatalf("code = %q", httputil.AsError(err).Code)
		}
	})

	t.Run("oversized", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "huge.md")
		if err := os.WriteFile(path, bytes.Repeat([]byte("x"), maxSkillManualBytes+16), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := readSkillFile(path); httputil.AsError(err).Code != CodeProviderMalformed {
			t.Fatalf("code = %q", httputil.AsError(err).Code)
		}
	})
}

func TestSkillManualTransportFailure(t *testing.T) {
	conn := &skillConnector{client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errBoom
	})}}
	entry := registry.Entry{ID: "skill-1", Kind: registry.KindSkill, Name: "s", Endpoint: "http://skill.test/SKILL.md"}
	if _, err := conn.loadManual(context.Background(), &entry); httputil.AsError(err).Code != CodeResourceUnreachable {
		t.Fatalf("code = %q", httputil.AsError(err).Code)
	}
}

func TestLocalSkillDirKeepsWindowsPathsUsable(t *testing.T) {
	original := hostOS
	t.Cleanup(func() { hostOS = original })

	hostOS = "windows"
	// file:///C:/skills/x parses with a leading slash on every platform; only
	// Windows has to drop it, which is the branch this pins.
	got, err := localSkillDir("file:///C:/skills/onboarding")
	if err != nil {
		t.Fatalf("localSkillDir: %v", err)
	}
	if got != filepath.FromSlash("C:/skills/onboarding") {
		t.Fatalf("path = %q", got)
	}

	hostOS = "linux"
	got, err = localSkillDir("file:///C:/skills/onboarding")
	if err != nil {
		t.Fatalf("localSkillDir: %v", err)
	}
	if got != "/C:/skills/onboarding" {
		t.Fatalf("path = %q", got)
	}
}

// fakeSandboxProvider is the smallest provider that can hold a session: it is
// how the stdio transport is driven without docker, a container or a network.
type fakeSandboxProvider struct {
	name   string
	failAt int
	seen   int
}

func (p *fakeSandboxProvider) Name() string { return p.name }

func (p *fakeSandboxProvider) Describe(context.Context) (sandbox.Descriptor, error) {
	// The manager refuses a profile whose network mode the descriptor never
	// lists, so the fake declares the one it can keep.
	return sandbox.Descriptor{Name: p.name, Network: []string{sandbox.NetworkNone}}, nil
}

func (p *fakeSandboxProvider) Probe(context.Context) (sandbox.Availability, error) {
	return sandbox.Availability{Available: true}, nil
}

func (p *fakeSandboxProvider) Create(_ context.Context, spec sandbox.Spec) (sandbox.Handle, error) {
	return sandbox.Handle{ID: "handle-1", Provider: p.name, Ref: "ref-1", Spec: spec}, nil
}

func (p *fakeSandboxProvider) Exec(context.Context, sandbox.Handle, sandbox.ExecRequest) (sandbox.ExecResult, error) {
	return sandbox.ExecResult{}, nil
}

func (p *fakeSandboxProvider) Attach(context.Context, sandbox.Handle, sandbox.ExecRequest) (sandbox.Session, error) {
	return &fakeSandboxSession{provider: p}, nil
}

func (p *fakeSandboxProvider) Destroy(context.Context, sandbox.Handle) error { return nil }

func (p *fakeSandboxProvider) Status(context.Context, sandbox.Handle) (string, error) {
	return "running", nil
}

type fakeSandboxSession struct{ provider *fakeSandboxProvider }

func (s *fakeSandboxSession) RoundTrip(_ context.Context, payload []byte) ([]byte, error) {
	s.provider.seen++
	if s.provider.seen == s.provider.failAt {
		return nil, errors.New("the server went away")
	}
	if strings.Contains(string(payload), "tools/list") {
		return []byte(`{"result":{"tools":[{"name":"lookup"}]}}`), nil
	}
	return []byte(`{"result":{}}`), nil
}

func (s *fakeSandboxSession) Close() error { return nil }

// leaseStore keeps leases in memory. Only the lease methods are implemented:
// the embedded nil Store answers the rest, and any accidental call panics
// instead of quietly passing.
type leaseStore struct {
	sandbox.Store
	mu     sync.Mutex
	leases map[string]*sandbox.Lease
}

func newLeaseStore() *leaseStore { return &leaseStore{leases: map[string]*sandbox.Lease{}} }

func (s *leaseStore) SaveLease(_ context.Context, l *sandbox.Lease) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *l
	s.leases[l.ID] = &cp
	return nil
}

func (s *leaseStore) GetLease(_ context.Context, id string) (*sandbox.Lease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if l, ok := s.leases[id]; ok {
		cp := *l
		return &cp, nil
	}
	return nil, fmt.Errorf("lease %s not found", id)
}

func (s *leaseStore) FindLease(context.Context, string, string, string, string) (*sandbox.Lease, error) {
	return nil, nil
}

func (s *leaseStore) ListLeases(context.Context, string) ([]*sandbox.Lease, error) { return nil, nil }

func (s *leaseStore) DeleteLease(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.leases, id)
	return nil
}

func (s *leaseStore) ExpiredLeases(context.Context, time.Time, int) ([]*sandbox.Lease, error) {
	return nil, nil
}

func (s *leaseStore) CountLeases(context.Context, string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.leases), nil
}

// stdioConnector wires the mcp connector to the fake provider so the two
// transport-failure branches of the stdio path can be reached.
func stdioConnector(t *testing.T, failAt int) *mcpConnector {
	t.Helper()
	provider := &fakeSandboxProvider{name: "fake", failAt: failAt}
	registryService := sandbox.NewRegistry(sandbox.RegistryOptions{})
	if err := registryService.Register(provider); err != nil {
		t.Fatalf("register: %v", err)
	}
	manager := sandbox.NewManager(sandbox.ManagerOptions{
		Registry: registryService,
		Store:    newLeaseStore(),
	})
	return &mcpConnector{sandbox: sandbox.NewService(sandbox.ServiceOptions{
		Manager:  manager,
		Registry: registryService,
	})}
}

func stdioEntry() registry.Entry {
	return registry.Entry{
		ID: "mcp-1", Kind: registry.KindMCP, Name: "stdio-server",
		KindConfig: &registry.KindConfig{
			MCP:     &registry.MCPConfig{Transport: string(registry.TransportStdio), Command: "server", ToolAllowlist: []string{"lookup"}},
			Sandbox: &registry.SandboxConfig{Provider: "fake"},
		},
	}
}

func TestMCPStdioReportsATransportFailure(t *testing.T) {
	// initialize, tools/list and tools/call are three round trips; a failure at
	// any of them is the server's, not the policy's.
	for _, tc := range []struct {
		name   string
		failAt int
	}{
		{"tools list", 2},
		{"tools call", 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entry := stdioEntry()
			_, err := stdioConnector(t, tc.failAt).Invoke(context.Background(), InvokeRequest{ResourceID: entry.ID, Entry: entry, Prompt: "hi"})
			if err == nil {
				t.Fatal("a broken session was reported as success")
			}
			if !strings.Contains(err.Error(), "went away") {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestSSEDecoderSkipsAKeepAliveComment(t *testing.T) {
	// A gateway that sends ": keep-alive" frames is telling us it is alive, not
	// answering; the comment must not become an empty completion.
	// The bare word without a colon is what a sloppy gateway emits; it carries
	// no field and is skipped rather than parsed as an empty data line.
	dec := newSSEDecoder(strings.NewReader(": keep-alive\nstray-word\ndata: hello\n\n"))
	for {
		_, data, ok, err := dec.Next()
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		if !ok {
			t.Fatal("the data frame was never returned")
		}
		if data == "hello" {
			return
		}
	}
}
