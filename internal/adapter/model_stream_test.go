package adapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"agenthub/internal/a2a"
	"agenthub/internal/httputil"
	"agenthub/internal/registry"
)

// roundTripFunc lets a test describe the transport answer without opening a
// socket; it is the seam a "connection refused" path uses.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type fakeProvider struct {
	name  string
	out   string
	err   error
	calls *int
}

func (f fakeProvider) Name() string { return f.name }

func (f fakeProvider) Generate(context.Context, string) (string, error) {
	if f.calls != nil {
		(*f.calls)++
	}
	return f.out, f.err
}

type fakeStreamProvider struct {
	fakeProvider
	chunk     string
	streamErr error
}

func (f fakeStreamProvider) GenerateStream(_ context.Context, _ string, sink StreamSink) (string, error) {
	if f.calls != nil {
		(*f.calls)++
	}
	if f.chunk != "" && sink != nil {
		if err := sink(streamChunk("model.stream", 0, a2a.NewTextPart(f.chunk))); err != nil {
			return "", err
		}
	}
	if f.streamErr != nil {
		return "", f.streamErr
	}
	return f.out, nil
}

var errBoom = errors.New("provider exploded")

func TestOpenAIProviderStreamsDeltas(t *testing.T) {
	t.Setenv("TEST_MODEL_KEY", "secret")
	var sawStream bool
	var sawAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization")
		var payload map[string]any
		if err := decodeJSON(r, &payload); err != nil {
			t.Errorf("decode payload: %v", err)
		}
		sawStream, _ = payload["stream"].(bool)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Hel\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"lo\"}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	p := newOpenAIProvider("openai:test", srv.URL, "model-1", "TEST_MODEL_KEY", time.Second, 16)
	var chunks []a2a.Artifact
	out, err := p.GenerateStream(context.Background(), "hi", func(a a2a.Artifact) error {
		chunks = append(chunks, a)
		return nil
	})
	if err != nil {
		t.Fatalf("GenerateStream: %v", err)
	}
	if out != "Hello" {
		t.Fatalf("out = %q, want Hello", out)
	}
	if !sawStream {
		t.Fatal("the request must ask for stream=true")
	}
	if sawAuth != "Bearer secret" {
		t.Fatalf("Authorization = %q, want the env credential", sawAuth)
	}
	if len(chunks) != 2 {
		t.Fatalf("chunks = %d, want 2", len(chunks))
	}
	if chunks[0].Parts[0].Text != "Hel" || chunks[1].Parts[0].Text != "lo" {
		t.Fatalf("chunk text = %q / %q", chunks[0].Parts[0].Text, chunks[1].Parts[0].Text)
	}
	if chunks[0].LastChunk == nil || *chunks[0].LastChunk {
		t.Fatal("a streamed chunk must not claim to be the last one")
	}
	if chunks[0].Append == nil || !*chunks[0].Append {
		t.Fatal("a streamed chunk must be appendable")
	}
}

func TestOpenAIProviderStreamMapsFailures(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		body    string
		code    httputil.Code
		wantErr bool
	}{
		{name: "unauthorized", status: http.StatusUnauthorized, code: CodeProviderUnauthorized, wantErr: true},
		{name: "upstream failure", status: http.StatusBadGateway, code: CodeResourceUnreachable, wantErr: true},
		{name: "malformed frame", status: http.StatusOK, body: "data: not-json\n\n", code: CodeProviderMalformed, wantErr: true},
		{name: "error frame", status: http.StatusOK, body: "data: {\"error\":{\"message\":\"boom\"}}\n\n", code: CodeResourceUnreachable, wantErr: true},
		{name: "empty stream", status: http.StatusOK, body: "data: [DONE]\n\n", code: CodeProviderMalformed, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer srv.Close()
			p := newOpenAIProvider("p", srv.URL, "m", "", time.Second, 8)
			_, err := p.GenerateStream(context.Background(), "hi", nil)
			if err == nil {
				t.Fatal("expected an error")
			}
			if got := httputil.AsError(err).Code; got != tc.code {
				t.Fatalf("code = %q, want %q (err: %v)", got, tc.code, err)
			}
		})
	}
}

func TestOpenAIProviderStreamPropagatesSinkFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"x\"}}]}\n\n")
	}))
	defer srv.Close()
	p := newOpenAIProvider("p", srv.URL, "m", "", time.Second, 8)
	_, err := p.GenerateStream(context.Background(), "hi", func(a2a.Artifact) error { return errBoom })
	if !errors.Is(err, errBoom) {
		t.Fatalf("err = %v, want the sink error", err)
	}
}

func TestOpenAIProviderStreamRejectsAnEmptyEndpoint(t *testing.T) {
	p := newOpenAIProvider("p", "", "m", "", time.Second, 8)
	if _, err := p.GenerateStream(context.Background(), "hi", nil); httputil.AsError(err).Code != CodeResourceUnreachable {
		t.Fatalf("code = %q, want resource_unreachable", httputil.AsError(err).Code)
	}
}

func TestOpenAIProviderStreamRefusesAnOversizedStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		payload := strings.Repeat("y", 2048)
		for i := 0; i < 600; i++ {
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"%s\"}}]}\n\n", payload)
		}
	}))
	defer srv.Close()
	p := newOpenAIProvider("p", srv.URL, "m", "", time.Second, 8)
	_, err := p.GenerateStream(context.Background(), "hi", nil)
	if httputil.AsError(err).Code != CodeProviderMalformed {
		t.Fatalf("code = %q, want model_provider_malformed", httputil.AsError(err).Code)
	}
}

func TestModelRouterStreamsAndFallsBack(t *testing.T) {
	t.Run("streaming provider answers", func(t *testing.T) {
		var chunks int
		router := &ModelRouter{providers: []ModelProvider{fakeStreamProvider{
			fakeProvider: fakeProvider{name: "s", out: "answer"},
			chunk:        "ans",
		}}}
		out, name, err := router.GenerateStreamWith(context.Background(), "q", func(a2a.Artifact) error {
			chunks++
			return nil
		})
		if err != nil || out != "answer" || name != "s" || chunks != 1 {
			t.Fatalf("out=%q name=%q chunks=%d err=%v", out, name, chunks, err)
		}
	})

	t.Run("a provider that cannot stream still answers", func(t *testing.T) {
		var chunks int
		router := &ModelRouter{providers: []ModelProvider{fakeProvider{name: "plain", out: "plain answer"}}}
		out, name, err := router.GenerateStreamWith(context.Background(), "q", func(a2a.Artifact) error {
			chunks++
			return nil
		})
		if err != nil || out != "plain answer" || name != "plain" || chunks != 0 {
			t.Fatalf("out=%q name=%q chunks=%d err=%v", out, name, chunks, err)
		}
	})

	t.Run("falls back before emitting", func(t *testing.T) {
		calls := 0
		router := &ModelRouter{providers: []ModelProvider{
			fakeProvider{name: "bad", err: errBoom, calls: &calls},
			fakeStreamProvider{fakeProvider: fakeProvider{name: "good", out: "recovered"}, chunk: "re"},
		}}
		out, name, err := router.GenerateStreamWith(context.Background(), "q", nil)
		if err != nil || out != "recovered" || name != "good" {
			t.Fatalf("out=%q name=%q err=%v", out, name, err)
		}
	})

	t.Run("never splices two answers", func(t *testing.T) {
		second := 0
		router := &ModelRouter{providers: []ModelProvider{
			fakeStreamProvider{fakeProvider: fakeProvider{name: "partial"}, chunk: "half", streamErr: errBoom},
			fakeStreamProvider{fakeProvider: fakeProvider{name: "second", out: "never", calls: &second}, chunk: "other"},
		}}
		_, _, err := router.GenerateStreamWith(context.Background(), "q", nil)
		if !errors.Is(err, errBoom) {
			t.Fatalf("err = %v, want the first provider's error", err)
		}
		if second != 0 {
			t.Fatal("a provider that already emitted must not be replaced")
		}
	})

	t.Run("empty router", func(t *testing.T) {
		var router *ModelRouter
		if _, _, err := router.GenerateStreamWith(context.Background(), "q", nil); err == nil {
			t.Fatal("a nil router must fail")
		}
		if _, _, err := (&ModelRouter{}).GenerateStreamWith(context.Background(), "q", nil); err == nil {
			t.Fatal("an empty router must fail")
		}
	})
}

func TestModelConnectorInvokeStreams(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"abc\"}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	e := &registry.Entry{
		ID:   "model-1",
		Kind: registry.KindModel,
		Name: "demo-model",
		KindConfig: &registry.KindConfig{Model: &registry.ModelConfig{
			ProviderMode: registry.ProviderModeOpenAICompatible,
			Endpoint:     srv.URL,
			ModelID:      "m",
		}},
	}
	conn := &modelConnector{router: NewModelRouterFor(e, registry.ProviderModeMock), defaultMode: registry.ProviderModeMock}
	var chunks []a2a.Artifact
	res, err := conn.Invoke(context.Background(), InvokeRequest{
		ResourceID: e.ID,
		Entry:      *e,
		Prompt:     "hi",
		Stream: func(a a2a.Artifact) error {
			chunks = append(chunks, a)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if res.Text != "abc" {
		t.Fatalf("text = %q", res.Text)
	}
	if len(chunks) != 1 {
		t.Fatalf("chunks = %d, want 1", len(chunks))
	}
	if len(res.Artifacts) == 0 {
		t.Fatal("the aggregate artifact must still be returned")
	}
}

func TestModelConnectorWithoutStreamDoesNotEmitChunks(t *testing.T) {
	e := &registry.Entry{ID: "model-1", Kind: registry.KindModel, Name: "demo"}
	conn := &modelConnector{router: NewModelRouter(), defaultMode: registry.ProviderModeMock}
	res, err := conn.Invoke(context.Background(), InvokeRequest{ResourceID: e.ID, Entry: *e, Prompt: "hi"})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if !strings.Contains(res.Text, "provider=mock") {
		t.Fatalf("text = %q", res.Text)
	}
}

func decodeJSON(r *http.Request, dst any) error {
	return json.NewDecoder(r.Body).Decode(dst)
}

func TestOpenAIProviderStreamNeedsAContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p := newOpenAIProvider("p", "http://127.0.0.1:1", "m", "", time.Second, 8)
	if _, err := p.GenerateStream(ctx, "hi", nil); err == nil {
		t.Fatal("a cancelled context must stop before dialling")
	}
}

func TestOpenAIProviderStreamMapsTransportFailure(t *testing.T) {
	p := newOpenAIProvider("p", "http://example.invalid", "m", "", time.Second, 8)
	p.client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errBoom
	})}
	if _, err := p.GenerateStream(context.Background(), "hi", nil); httputil.AsError(err).Code != CodeResourceUnreachable {
		t.Fatalf("code = %q, want resource_unreachable", httputil.AsError(err).Code)
	}
}

func TestOpenAIProviderNameAndDefaults(t *testing.T) {
	p := newOpenAIProvider("", "http://example.test", "", "", 0, 0)
	if p.Name() != "" {
		t.Fatalf("name = %q", p.Name())
	}
	if p.effectiveMaxTokens() != registry.DefaultModelMaxTokens {
		t.Fatalf("max tokens = %d", p.effectiveMaxTokens())
	}
	if p.client.Timeout != DefaultModelTimeout {
		t.Fatalf("timeout = %v", p.client.Timeout)
	}
}
