package adapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"agenthub/internal/a2a"
	"agenthub/internal/httputil"
	"agenthub/internal/registry"
)

// agentWithCard serves a card and the JSON-RPC endpoint the card points at.
func agentWithCard(t *testing.T, streaming bool, rpc http.HandlerFunc) (*httptest.Server, *agentConnector) {
	t.Helper()
	mux := http.NewServeMux()
	var srv *httptest.Server
	mux.HandleFunc("/card", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"name":         "streamer",
			"url":          srv.URL,
			"capabilities": map[string]any{"streaming": streaming},
		})
	})
	mux.HandleFunc("/", rpc)
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &agentConnector{cardURL: srv.URL + "/card", client: srv.Client(), timeout: 5 * time.Second}
}

func TestAgentStreamsWhenTheCardAdvertisesIt(t *testing.T) {
	var method string
	_, conn := agentWithCard(t, true, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		method, _ = body["method"].(string)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"jsonrpc":"2.0","id":1,"result":{"kind":"status-update","status":{"state":"working"},"final":false}}`+"\n\n")
		fmt.Fprint(w, `data: {"jsonrpc":"2.0","id":1,"result":{"kind":"artifact-update","artifact":{"parts":[{"kind":"text","text":"alpha"}]}}}`+"\n\n")
		fmt.Fprint(w, `data: {"jsonrpc":"2.0","id":1,"result":{"kind":"artifact-update","artifact":{"parts":[{"kind":"text","text":"beta"}]}}}`+"\n\n")
		fmt.Fprint(w, `data: {"jsonrpc":"2.0","id":1,"result":{"kind":"status-update","status":{"state":"completed","message":{"parts":[{"kind":"text","text":"alphabeta"}]}},"final":true}}`+"\n\n")
	})
	e := registry.Entry{ID: "agent-1", Kind: registry.KindAgent, Name: "streamer"}
	var chunks []string
	res, err := conn.Invoke(context.Background(), InvokeRequest{
		ResourceID: e.ID,
		Entry:      e,
		Prompt:     "go",
		Stream: func(a a2a.Artifact) error {
			chunks = append(chunks, a.Parts[0].Text)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if method != a2a.MethodMessageStream {
		t.Fatalf("method = %q, want message/stream", method)
	}
	if res.Text != "alphabeta" {
		t.Fatalf("text = %q", res.Text)
	}
	if len(chunks) != 2 || chunks[0] != "alpha" || chunks[1] != "beta" {
		t.Fatalf("chunks = %v", chunks)
	}
	if len(res.Artifacts) == 0 || res.Artifacts[0].Name != "agent.a2a" {
		t.Fatalf("artifacts = %+v", res.Artifacts)
	}
}

func TestAgentStreamFallsBackToTheStatusMessage(t *testing.T) {
	_, conn := agentWithCard(t, true, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"jsonrpc":"2.0","id":1,"result":{"kind":"status-update","status":{"state":"completed","message":{"parts":[{"kind":"text","text":"only a summary"}]}},"final":true}}`+"\n\n")
	})
	e := registry.Entry{ID: "agent-1", Kind: registry.KindAgent, Name: "streamer"}
	res, err := conn.Invoke(context.Background(), InvokeRequest{
		ResourceID: e.ID, Entry: e, Prompt: "go",
		Stream: func(a2a.Artifact) error { return nil },
	})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if res.Text != "only a summary" {
		t.Fatalf("text = %q", res.Text)
	}
}

func TestAgentStreamParksOnInputRequired(t *testing.T) {
	_, conn := agentWithCard(t, true, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"jsonrpc":"2.0","id":1,"result":{"kind":"status-update","status":{"state":"input-required","message":{"parts":[{"kind":"text","text":"approve?"}]}},"final":true}}`+"\n\n")
	})
	e := registry.Entry{ID: "agent-1", Kind: registry.KindAgent, Name: "streamer"}
	res, err := conn.Invoke(context.Background(), InvokeRequest{
		ResourceID: e.ID, Entry: e, Prompt: "go",
		Stream: func(a2a.Artifact) error { return nil },
	})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if !res.RequiresInput || res.Prompt != "approval_required:"+e.ID {
		t.Fatalf("result = %+v", res)
	}
}

func TestAgentWithoutStreamingCapabilityUsesSend(t *testing.T) {
	var method string
	_, conn := agentWithCard(t, false, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		method, _ = body["method"].(string)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0",
			"id":      "agenthub-1",
			"result": map[string]any{
				"status": map[string]any{
					"state":   "completed",
					"message": map[string]any{"parts": []any{map[string]any{"kind": "text", "text": "done"}}},
				},
			},
		})
	})
	e := registry.Entry{ID: "agent-1", Kind: registry.KindAgent, Name: "plain"}
	res, err := conn.Invoke(context.Background(), InvokeRequest{
		ResourceID: e.ID, Entry: e, Prompt: "go",
		Stream: func(a2a.Artifact) error { return nil },
	})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if method != a2a.MethodMessageSend {
		t.Fatalf("method = %q, want message/send", method)
	}
	if res.Text != "done" {
		t.Fatalf("text = %q", res.Text)
	}
}

func TestAgentStreamAcceptsAPlainJSONBody(t *testing.T) {
	_, conn := agentWithCard(t, true, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0",
			"id":      "agenthub-stream-1",
			"result": map[string]any{
				"status": map[string]any{
					"state":   "completed",
					"message": map[string]any{"parts": []any{map[string]any{"kind": "text", "text": "single shot"}}},
				},
			},
		})
	})
	e := registry.Entry{ID: "agent-1", Kind: registry.KindAgent, Name: "fallback"}
	res, err := conn.Invoke(context.Background(), InvokeRequest{
		ResourceID: e.ID, Entry: e, Prompt: "go",
		Stream: func(a2a.Artifact) error { return nil },
	})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if res.Text != "single shot" {
		t.Fatalf("text = %q", res.Text)
	}
}

func TestAgentStreamMapsFailures(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		ctype   string
		body    string
		code    httputil.Code
		sinkErr error
	}{
		{name: "http failure", status: http.StatusBadGateway, ctype: "text/event-stream", code: CodeResourceUnreachable},
		{name: "malformed frame", status: http.StatusOK, ctype: "text/event-stream", body: "data: not-json\n\n", code: CodeProviderMalformed},
		{name: "rpc error frame", status: http.StatusOK, ctype: "text/event-stream", body: `data: {"jsonrpc":"2.0","error":{"code":-32007,"message":"resource_unreachable"}}` + "\n\n", code: CodeResourceUnreachable},
		{name: "malformed plain body", status: http.StatusOK, ctype: "application/json", body: "not-json", code: CodeProviderMalformed},
		{name: "plain rpc error", status: http.StatusOK, ctype: "application/json", body: `{"jsonrpc":"2.0","error":{"code":-32007,"message":"resource_unreachable"}}`, code: CodeResourceUnreachable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, conn := agentWithCard(t, true, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", tc.ctype)
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			})
			e := registry.Entry{ID: "agent-1", Kind: registry.KindAgent, Name: "broken"}
			_, err := conn.Invoke(context.Background(), InvokeRequest{
				ResourceID: e.ID, Entry: e, Prompt: "go",
				Stream: func(a2a.Artifact) error { return nil },
			})
			if got := httputil.AsError(err).Code; got != tc.code {
				t.Fatalf("code = %q, want %q (err: %v)", got, tc.code, err)
			}
		})
	}
}

func TestAgentStreamPropagatesSinkFailure(t *testing.T) {
	_, conn := agentWithCard(t, true, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"jsonrpc":"2.0","id":1,"result":{"kind":"artifact-update","artifact":{"parts":[{"kind":"text","text":"x"}]}}}`+"\n\n")
	})
	e := registry.Entry{ID: "agent-1", Kind: registry.KindAgent, Name: "sink"}
	_, err := conn.Invoke(context.Background(), InvokeRequest{
		ResourceID: e.ID, Entry: e, Prompt: "go",
		Stream: func(a2a.Artifact) error { return errBoom },
	})
	if !errors.Is(err, errBoom) {
		t.Fatalf("err = %v, want the sink error", err)
	}
}

func TestAgentFrameClassification(t *testing.T) {
	// A bare task result (no kind) is read through textFromSendResult.
	text, state, kind, final, err := agentFrame(json.RawMessage(`{"status":{"state":"completed","message":{"parts":[{"kind":"text","text":"answer"}]}}}`))
	if err != nil || text != "answer" || state != "completed" || kind != "" || !final {
		t.Fatalf("frame = %q %q %q %v %v", text, state, kind, final, err)
	}
	if _, _, _, _, err := agentFrame(json.RawMessage(`not-json`)); httputil.AsError(err).Code != CodeProviderMalformed {
		t.Fatalf("malformed frame code = %q", httputil.AsError(err).Code)
	}
}

func TestIsTerminalAgentState(t *testing.T) {
	for _, state := range []string{"completed", "failed", "canceled", "cancelled", "input-required"} {
		if !isTerminalAgentState(state) {
			t.Fatalf("%q must be terminal", state)
		}
	}
	if isTerminalAgentState("working") {
		t.Fatal("working is not terminal")
	}
}
