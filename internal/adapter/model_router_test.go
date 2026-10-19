package adapter

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agenthub/internal/httputil"
	"agenthub/internal/registry"
)

func modelEntry(cfg *registry.ModelConfig) registry.Entry {
	return registry.Entry{
		ID: "model-1", Kind: registry.KindModel, Name: "router", Version: "1.0.0",
		Endpoint:   "http://model.test",
		KindConfig: &registry.KindConfig{Model: cfg},
	}
}

func chatServer(t *testing.T, handler func(model string, w http.ResponseWriter)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model    string `json:"model"`
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = decodeJSONBody(r, &body)
		w.Header().Set("Content-Type", "application/json")
		handler(body.Model, w)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func decodeJSONBody(r *http.Request, dst any) error {
	defer func() { _ = r.Body.Close() }()
	return json.NewDecoder(r.Body).Decode(dst)
}

func TestCompletionsURLAcceptsBothShapes(t *testing.T) {
	if got := completionsURL("https://router.test/v1"); got != "https://router.test/v1/chat/completions" {
		t.Fatalf("base = %q", got)
	}
	if got := completionsURL("https://router.test/v1/"); got != "https://router.test/v1/chat/completions" {
		t.Fatalf("trailing slash = %q", got)
	}
	if got := completionsURL("https://router.test/v1/chat/completions"); got != "https://router.test/v1/chat/completions" {
		t.Fatalf("explicit = %q", got)
	}
	if got := completionsURL(""); got != "" {
		t.Fatalf("empty = %q", got)
	}
}

func TestMockModeStaysOffline(t *testing.T) {
	e := modelEntry(&registry.ModelConfig{ProviderMode: registry.ProviderModeMock})
	conn, err := For(&e, options())
	if err != nil {
		t.Fatal(err)
	}
	out, provider, err := conn.(*modelConnector).router.GenerateWith(context.Background(), "hello")
	if err != nil || provider != "mock" || !strings.Contains(out, "provider=mock") {
		t.Fatalf("out = %q, provider = %q, err = %v", out, provider, err)
	}
}

func TestRouterModeCallsOpenAICompatibleEndpoint(t *testing.T) {
	srv := chatServer(t, func(model string, w http.ResponseWriter) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"from router: ` + model + `"}}]}`))
	})
	e := modelEntry(&registry.ModelConfig{
		ProviderMode: registry.ProviderModeOpenAICompatible,
		Endpoint:     srv.URL,
		ModelID:      "gpt-4o-mini",
		TimeoutMS:    5000,
	})
	conn, err := For(&e, options())
	if err != nil {
		t.Fatal(err)
	}
	res, err := conn.Invoke(context.Background(), InvokeRequest{ResourceID: e.ID, Entry: e, Prompt: "hello"})
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if res.Text != "from router: gpt-4o-mini" {
		t.Fatalf("text = %q", res.Text)
	}
	// The artifact has to name the model that answered, not just the kind.
	data := res.Artifacts[0].Parts[0].Data
	if got := data["provider"]; got != "openai:gpt-4o-mini" {
		t.Fatalf("provider = %v", got)
	}
}

func TestRouterFallsBackDownTheChain(t *testing.T) {
	var seen []string
	srv := chatServer(t, func(model string, w http.ResponseWriter) {
		seen = append(seen, model)
		if model == "primary" {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"served by ` + model + `"}}]}`))
	})
	e := modelEntry(&registry.ModelConfig{
		ProviderMode: registry.ProviderModeOpenAICompatible,
		Endpoint:     srv.URL,
		ModelID:      "primary",
		Targets:      []registry.ModelTarget{{Name: "secondary", Model: "secondary"}},
	})
	conn, _ := For(&e, options())
	res, err := conn.Invoke(context.Background(), InvokeRequest{ResourceID: e.ID, Entry: e, Prompt: "hello"})
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if res.Text != "served by secondary" {
		t.Fatalf("text = %q", res.Text)
	}
	if len(seen) != 2 || seen[0] != "primary" || seen[1] != "secondary" {
		t.Fatalf("chain = %v", seen)
	}
}

func TestRouterDoesNotFabricateWhenEveryProviderFails(t *testing.T) {
	srv := chatServer(t, func(model string, w http.ResponseWriter) {
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	e := modelEntry(&registry.ModelConfig{
		ProviderMode: registry.ProviderModeOpenAICompatible,
		Endpoint:     srv.URL,
		ModelID:      "primary",
	})
	conn, _ := For(&e, options())
	if _, err := conn.Invoke(context.Background(), InvokeRequest{ResourceID: e.ID, Entry: e, Prompt: "hello"}); err == nil {
		t.Fatal("a failing router must not answer with a mock completion")
	}
	// The offline fallback is opt in and, when enabled, says so in the artifact.
	e.KindConfig.Model.FallbackMock = true
	conn, _ = For(&e, options())
	res, err := conn.Invoke(context.Background(), InvokeRequest{ResourceID: e.ID, Entry: e, Prompt: "hello"})
	if err != nil {
		t.Fatalf("fallback mock: %v", err)
	}
	if got := res.Artifacts[0].Parts[0].Data["provider"]; got != "mock" {
		t.Fatalf("provider = %v", got)
	}
}

func TestRouterReportsUnauthorized(t *testing.T) {
	srv := chatServer(t, func(model string, w http.ResponseWriter) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	e := modelEntry(&registry.ModelConfig{
		ProviderMode: registry.ProviderModeOpenAICompatible,
		Endpoint:     srv.URL,
		ModelID:      "primary",
	})
	conn, _ := For(&e, options())
	_, err := conn.Invoke(context.Background(), InvokeRequest{ResourceID: e.ID, Entry: e, Prompt: "hello"})
	if httputil.AsError(err).Code != CodeProviderUnauthorized {
		t.Fatalf("code = %v", httputil.AsError(err).Code)
	}
}

func TestRouterRejectsMalformedCompletion(t *testing.T) {
	srv := chatServer(t, func(model string, w http.ResponseWriter) {
		_, _ = w.Write([]byte(`not json`))
	})
	e := modelEntry(&registry.ModelConfig{
		ProviderMode: registry.ProviderModeOpenAICompatible,
		Endpoint:     srv.URL,
		ModelID:      "primary",
	})
	conn, _ := For(&e, options())
	_, err := conn.Invoke(context.Background(), InvokeRequest{ResourceID: e.ID, Entry: e, Prompt: "hello"})
	if httputil.AsError(err).Code != CodeProviderMalformed {
		t.Fatalf("code = %v", httputil.AsError(err).Code)
	}
}

// AGENTHUB_MODEL_ROUTER_MODE is the instance wide default: an entry that omits
// provider_mode inherits it, an entry that names one keeps its own, and an
// unset option stays offline instead of becoming a gateway call nobody asked for.
func TestInstanceDefaultSelectsTheProviderChain(t *testing.T) {
	srv := chatServer(t, func(model string, w http.ResponseWriter) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"served by ` + model + `"}}]}`))
	})
	// No ProviderMode: the instance decides.
	inheriting := modelEntry(&registry.ModelConfig{Endpoint: srv.URL, ModelID: "inherited", TimeoutMS: 5000})

	providerOf := func(t *testing.T, opts Options, e registry.Entry) any {
		t.Helper()
		conn, err := For(&e, opts)
		if err != nil {
			t.Fatal(err)
		}
		res, err := conn.Invoke(context.Background(), InvokeRequest{ResourceID: e.ID, Entry: e, Prompt: "hello"})
		if err != nil {
			t.Fatalf("invoke: %v", err)
		}
		return res.Artifacts[0].Parts[0].Data["provider"]
	}

	if got := providerOf(t, Options{ModelRouterMode: registry.ProviderModeOpenAICompatible}, inheriting); got != "openai:inherited" {
		t.Fatalf("inherited provider = %v, want the real one", got)
	}
	if got := providerOf(t, Options{ModelRouterMode: registry.ProviderModeMock}, inheriting); got != "mock" {
		t.Fatalf("mock instance provider = %v, want mock", got)
	}
	if got := providerOf(t, Options{}, inheriting); got != "mock" {
		t.Fatalf("unset mode provider = %v, want mock", got)
	}
	// The entry's own opt-out beats a global opt-in.
	optingOut := modelEntry(&registry.ModelConfig{ProviderMode: registry.ProviderModeMock, Endpoint: srv.URL, ModelID: "ignored"})
	if got := providerOf(t, Options{ModelRouterMode: registry.ProviderModeOpenAICompatible}, optingOut); got != "mock" {
		t.Fatalf("entry opt-out provider = %v, want the entry's mock", got)
	}
}

func TestRouterSendsCredentialFromEnvironmentOnly(t *testing.T) {
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer srv.Close()
	t.Setenv("AGENTHUB_TEST_MODEL_KEY", "sk-test")

	e := modelEntry(&registry.ModelConfig{
		ProviderMode: registry.ProviderModeOpenAICompatible,
		Endpoint:     srv.URL,
		ModelID:      "primary",
		APIKeyEnv:    "AGENTHUB_TEST_MODEL_KEY",
	})
	conn, _ := For(&e, options())
	if _, err := conn.Invoke(context.Background(), InvokeRequest{ResourceID: e.ID, Entry: e, Prompt: "hello"}); err != nil {
		t.Fatal(err)
	}
	if auth != "Bearer sk-test" {
		t.Fatalf("auth = %q", auth)
	}
}
