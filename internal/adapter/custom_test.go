package adapter

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"agenthub/internal/httputil"
	"agenthub/internal/registry"
)

func customEntry(endpoint, schema string) registry.Entry {
	return registry.Entry{
		ID:       "custom-1",
		Kind:     registry.KindCustom,
		Name:     "custom-one",
		Endpoint: endpoint,
		KindConfig: &registry.KindConfig{
			Custom: &registry.CustomConfig{Schema: schema},
		},
	}
}

func TestCustomDispatchIssuesTheDeclaredCall(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/run" {
			t.Errorf("path = %s, want /run", r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		if body["input"] != "go" {
			t.Errorf("input = %v", body["input"])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	e := customEntry(srv.URL, `{"type":"object","x-agenthub-dispatch":{"method":"POST","path":"run"}}`)
	conn := &customConnector{client: srv.Client()}
	res, err := conn.Invoke(context.Background(), InvokeRequest{ResourceID: e.ID, Entry: e, Prompt: "go"})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if !strings.Contains(res.Text, "status=200") || !strings.Contains(res.Text, "bytes=") {
		t.Fatalf("text = %q", res.Text)
	}
	if len(res.Artifacts) != 1 || res.Artifacts[0].Name != "custom.result" {
		t.Fatalf("artifacts = %+v", res.Artifacts)
	}
	data := res.Artifacts[0].Parts[0].Data
	if data["provider"] != "custom-http" || data["method"] != http.MethodPost {
		t.Fatalf("artifact data = %+v", data)
	}
	if data["response"] != `{"ok":true}` {
		t.Fatalf("response = %v", data["response"])
	}
}

func TestCustomDispatchDefaultsToPostAndAcceptsAQuery(t *testing.T) {
	var method, rawQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, rawQuery = r.Method, r.URL.RawQuery
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	e := customEntry(srv.URL, `{"x-agenthub-dispatch":{"path":"status?verbose=1"}}`)
	conn := &customConnector{client: srv.Client()}
	if _, err := conn.Invoke(context.Background(), InvokeRequest{ResourceID: e.ID, Entry: e, Prompt: "go"}); err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if method != http.MethodPost {
		t.Fatalf("method = %q, want the POST default", method)
	}
	if rawQuery != "verbose=1" {
		t.Fatalf("query = %q", rawQuery)
	}
}

func TestCustomDispatchAcceptsAnEmptyPath(t *testing.T) {
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_, _ = w.Write([]byte("root"))
	}))
	defer srv.Close()

	e := customEntry(srv.URL, `{"x-agenthub-dispatch":{"method":"GET"}}`)
	conn := &customConnector{client: srv.Client()}
	if _, err := conn.Invoke(context.Background(), InvokeRequest{ResourceID: e.ID, Entry: e, Prompt: "go"}); err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if path != "/" {
		t.Fatalf("path = %q, want the endpoint root", path)
	}
}

func TestCustomDispatchRejectsUnsafeDescriptors(t *testing.T) {
	cases := []struct {
		name   string
		schema string
	}{
		{name: "traversal", schema: `{"x-agenthub-dispatch":{"path":"../admin"}}`},
		{name: "absolute", schema: `{"x-agenthub-dispatch":{"path":"/admin"}}`},
		{name: "double slash", schema: `{"x-agenthub-dispatch":{"path":"a//b"}}`},
		{name: "backslash", schema: `{"x-agenthub-dispatch":{"path":"a\\b"}}`},
		{name: "control character", schema: `{"x-agenthub-dispatch":{"path":"a\u0001b"}}`},
		{name: "unsupported method", schema: `{"x-agenthub-dispatch":{"method":"DELETE","path":"run"}}`},
		{name: "descriptor is not an object", schema: `{"x-agenthub-dispatch":"nope"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var hits int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				atomic.AddInt32(&hits, 1)
			}))
			defer srv.Close()
			e := customEntry(srv.URL, tc.schema)
			conn := &customConnector{client: srv.Client()}
			_, err := conn.Invoke(context.Background(), InvokeRequest{ResourceID: e.ID, Entry: e, Prompt: "go"})
			if httputil.AsError(err).Code != CodeProviderMalformed {
				t.Fatalf("code = %q, want model_provider_malformed", httputil.AsError(err).Code)
			}
			if atomic.LoadInt32(&hits) != 0 {
				t.Fatal("an unsafe descriptor must be refused before any request")
			}
		})
	}
}

func TestCustomWithoutADescriptorEchoesTheSchema(t *testing.T) {
	for _, tc := range []struct {
		name     string
		endpoint string
		schema   string
	}{
		{name: "no descriptor", endpoint: "http://example.test", schema: `{"type":"object"}`},
		{name: "no endpoint", endpoint: "", schema: `{"x-agenthub-dispatch":{"path":"run"}}`},
		{name: "unreachable scheme", endpoint: "mock://custom", schema: `{"x-agenthub-dispatch":{"path":"run"}}`},
		{name: "schema is not json", endpoint: "http://example.test", schema: `not-json`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := customEntry(tc.endpoint, tc.schema)
			conn := &customConnector{client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				t.Fatal("the echo path must not open a connection")
				return nil, errBoom
			})}}
			res, err := conn.Invoke(context.Background(), InvokeRequest{ResourceID: e.ID, Entry: e, Prompt: "go"})
			if err != nil {
				t.Fatalf("Invoke: %v", err)
			}
			if len(res.Artifacts) != 1 || res.Artifacts[0].Name != "custom.schema" {
				t.Fatalf("artifacts = %+v", res.Artifacts)
			}
			data := res.Artifacts[0].Parts[0].Data
			if data["provider"] != MockProviderName {
				t.Fatalf("provider = %v", data["provider"])
			}
		})
	}
}

func TestCustomDispatchMapsFailures(t *testing.T) {
	t.Run("unauthorized", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		}))
		defer srv.Close()
		e := customEntry(srv.URL, `{"x-agenthub-dispatch":{"path":"run"}}`)
		conn := &customConnector{client: srv.Client()}
		if _, err := conn.Invoke(context.Background(), InvokeRequest{ResourceID: e.ID, Entry: e, Prompt: "go"}); httputil.AsError(err).Code != CodeProviderUnauthorized {
			t.Fatalf("code = %q", httputil.AsError(err).Code)
		}
	})

	t.Run("upstream failure", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadGateway)
		}))
		defer srv.Close()
		e := customEntry(srv.URL, `{"x-agenthub-dispatch":{"path":"run"}}`)
		conn := &customConnector{client: srv.Client()}
		if _, err := conn.Invoke(context.Background(), InvokeRequest{ResourceID: e.ID, Entry: e, Prompt: "go"}); httputil.AsError(err).Code != CodeResourceUnreachable {
			t.Fatalf("code = %q", httputil.AsError(err).Code)
		}
	})

	t.Run("transport failure", func(t *testing.T) {
		e := customEntry("http://example.test", `{"x-agenthub-dispatch":{"path":"run"}}`)
		conn := &customConnector{client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("connection refused")
		})}}
		if _, err := conn.Invoke(context.Background(), InvokeRequest{ResourceID: e.ID, Entry: e, Prompt: "go"}); httputil.AsError(err).Code != CodeResourceUnreachable {
			t.Fatalf("code = %q", httputil.AsError(err).Code)
		}
	})

	t.Run("unusable url", func(t *testing.T) {
		// Parses as http, but has no host: the descriptor cannot be resolved.
		e := customEntry("http:///path", `{"x-agenthub-dispatch":{"path":"run"}}`)
		conn := &customConnector{client: &http.Client{}}
		if _, err := conn.Invoke(context.Background(), InvokeRequest{ResourceID: e.ID, Entry: e, Prompt: "go"}); httputil.AsError(err).Code != CodeProviderMalformed {
			t.Fatalf("code = %q", httputil.AsError(err).Code)
		}
	})

	t.Run("cancelled context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		e := customEntry("http://example.test", `{"x-agenthub-dispatch":{"path":"run"}}`)
		conn := &customConnector{client: &http.Client{}}
		if _, err := conn.Invoke(ctx, InvokeRequest{ResourceID: e.ID, Entry: e, Prompt: "go"}); err == nil {
			t.Fatal("a cancelled context must be reported")
		}
	})
}

func TestCustomResponseIsBoundedAndDigested(t *testing.T) {
	body := strings.Repeat("z", customMaxResponseExcerpt+100)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	e := customEntry(srv.URL, `{"x-agenthub-dispatch":{"path":"big"}}`)
	conn := &customConnector{client: srv.Client()}
	res, err := conn.Invoke(context.Background(), InvokeRequest{ResourceID: e.ID, Entry: e, Prompt: "go"})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	data := res.Artifacts[0].Parts[0].Data
	if data["truncated"] != true {
		t.Fatalf("truncated = %v, want true", data["truncated"])
	}
	excerpt, _ := data["response"].(string)
	if len(excerpt) != customMaxResponseExcerpt {
		t.Fatalf("excerpt length = %d, want %d", len(excerpt), customMaxResponseExcerpt)
	}
	if data["response_digest"] == "" {
		t.Fatal("the whole body must still be digested")
	}
}
