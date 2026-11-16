package adapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"agenthub/internal/a2a"
	"agenthub/internal/httputil"
	"agenthub/internal/registry"
)

type failingBody struct{}

func (failingBody) Read([]byte) (int, error) { return 0, errors.New("body broke") }
func (failingBody) Close() error             { return nil }

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func agentTransport(card string, rpc func(*http.Request) (*http.Response, error)) *http.Client {
	return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "/card") {
			return jsonResponse(http.StatusOK, card), nil
		}
		return rpc(r)
	})}
}

func TestAgentCardFetchFailures(t *testing.T) {
	t.Run("transport failure", func(t *testing.T) {
		conn := &agentConnector{
			cardURL: "http://agent.test/card",
			client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return nil, errBoom
			})},
			timeout: time.Second,
		}
		e := registry.Entry{ID: "agent-1", Kind: registry.KindAgent, Name: "x"}
		if _, err := conn.Invoke(context.Background(), InvokeRequest{ResourceID: e.ID, Entry: e, Prompt: "go"}); httputil.AsError(err).Code != CodeResourceUnreachable {
			t.Fatalf("code = %q", httputil.AsError(err).Code)
		}
	})

	t.Run("non-2xx card", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadGateway)
		}))
		defer srv.Close()
		conn := &agentConnector{cardURL: srv.URL + "/card", client: srv.Client(), timeout: time.Second}
		e := registry.Entry{ID: "agent-1", Kind: registry.KindAgent, Name: "x"}
		if _, err := conn.Invoke(context.Background(), InvokeRequest{ResourceID: e.ID, Entry: e, Prompt: "go"}); httputil.AsError(err).Code != CodeResourceUnreachable {
			t.Fatalf("code = %q", httputil.AsError(err).Code)
		}
	})

	t.Run("malformed card", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("not-json"))
		}))
		defer srv.Close()
		conn := &agentConnector{cardURL: srv.URL, client: srv.Client(), timeout: time.Second}
		e := registry.Entry{ID: "agent-1", Kind: registry.KindAgent, Name: "x"}
		if _, err := conn.Invoke(context.Background(), InvokeRequest{ResourceID: e.ID, Entry: e, Prompt: "go"}); httputil.AsError(err).Code != CodeProviderMalformed {
			t.Fatalf("code = %q", httputil.AsError(err).Code)
		}
	})

	t.Run("card body read failure", func(t *testing.T) {
		conn := &agentConnector{
			cardURL: "http://agent.test/card",
			client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Body: failingBody{}}, nil
			})},
			timeout: time.Second,
		}
		e := registry.Entry{ID: "agent-1", Kind: registry.KindAgent, Name: "x"}
		if _, err := conn.Invoke(context.Background(), InvokeRequest{ResourceID: e.ID, Entry: e, Prompt: "go"}); httputil.AsError(err).Code != CodeResourceUnreachable {
			t.Fatalf("code = %q", httputil.AsError(err).Code)
		}
	})
}

func TestAgentSendFailures(t *testing.T) {
	card := `{"name":"x","url":"http://agent.test"}`
	cases := []struct {
		name   string
		rpc    func(*http.Request) (*http.Response, error)
		code   httputil.Code
		errNil bool
	}{
		{
			name: "transport failure",
			rpc:  func(*http.Request) (*http.Response, error) { return nil, errBoom },
			code: CodeResourceUnreachable,
		},
		{
			name: "body read failure",
			rpc: func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Body: failingBody{}}, nil
			},
			code: CodeResourceUnreachable,
		},
		{
			name: "non-2xx answer",
			rpc: func(*http.Request) (*http.Response, error) {
				return jsonResponse(http.StatusBadGateway, ""), nil
			},
			code: CodeResourceUnreachable,
		},
		{
			name: "malformed envelope",
			rpc: func(*http.Request) (*http.Response, error) {
				return jsonResponse(http.StatusOK, "not-json"), nil
			},
			code: CodeProviderMalformed,
		},
		{
			name: "rpc error",
			rpc: func(*http.Request) (*http.Response, error) {
				return jsonResponse(http.StatusOK, `{"jsonrpc":"2.0","error":{"code":-32007,"message":"resource_unreachable"}}`), nil
			},
			code: CodeResourceUnreachable,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			conn := &agentConnector{
				cardURL: "http://agent.test/card",
				client:  agentTransport(card, tc.rpc),
				timeout: time.Second,
			}
			e := registry.Entry{ID: "agent-1", Kind: registry.KindAgent, Name: "x"}
			_, err := conn.Invoke(context.Background(), InvokeRequest{ResourceID: e.ID, Entry: e, Prompt: "go"})
			if got := httputil.AsError(err).Code; got != tc.code {
				t.Fatalf("code = %q, want %q (err: %v)", got, tc.code, err)
			}
		})
	}
}

func TestAgentStreamSkipsEmptyFramesAndReportsTransportFailure(t *testing.T) {
	t.Run("empty data frame", func(t *testing.T) {
		srv, conn := agentWithCard(t, true, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: \n\n"))
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
		})
		_ = srv
		e := registry.Entry{ID: "agent-1", Kind: registry.KindAgent, Name: "x"}
		res, err := conn.Invoke(context.Background(), InvokeRequest{
			ResourceID: e.ID, Entry: e, Prompt: "go",
			Stream: func(a2a.Artifact) error { return nil },
		})
		if err != nil {
			t.Fatalf("Invoke: %v", err)
		}
		if !strings.Contains(res.Text, "no text part returned") {
			t.Fatalf("text = %q", res.Text)
		}
	})

	t.Run("stream transport failure", func(t *testing.T) {
		conn := &agentConnector{
			cardURL: "http://agent.test/card",
			client: agentTransport(`{"name":"x","url":"http://agent.test","capabilities":{"streaming":true}}`,
				func(*http.Request) (*http.Response, error) { return nil, errBoom }),
			timeout: time.Second,
		}
		e := registry.Entry{ID: "agent-1", Kind: registry.KindAgent, Name: "x"}
		_, err := conn.Invoke(context.Background(), InvokeRequest{
			ResourceID: e.ID, Entry: e, Prompt: "go",
			Stream: func(a2a.Artifact) error { return nil },
		})
		if httputil.AsError(err).Code != CodeResourceUnreachable {
			t.Fatalf("code = %q", httputil.AsError(err).Code)
		}
	})

	t.Run("stream plain body read failure", func(t *testing.T) {
		conn := &agentConnector{
			cardURL: "http://agent.test/card",
			client: agentTransport(`{"name":"x","url":"http://agent.test","capabilities":{"streaming":true}}`,
				func(*http.Request) (*http.Response, error) {
					return &http.Response{
						StatusCode: http.StatusOK,
						Header:     http.Header{"Content-Type": []string{"application/json"}},
						Body:       failingBody{},
					}, nil
				}),
			timeout: time.Second,
		}
		e := registry.Entry{ID: "agent-1", Kind: registry.KindAgent, Name: "x"}
		_, err := conn.Invoke(context.Background(), InvokeRequest{
			ResourceID: e.ID, Entry: e, Prompt: "go",
			Stream: func(a2a.Artifact) error { return nil },
		})
		if httputil.AsError(err).Code != CodeResourceUnreachable {
			t.Fatalf("code = %q", httputil.AsError(err).Code)
		}
	})
}

func TestAgentFrameRejectsAnUnreadableResult(t *testing.T) {
	if _, _, _, _, err := agentFrame(json.RawMessage("123")); httputil.AsError(err).Code != CodeProviderMalformed {
		t.Fatalf("code = %q", httputil.AsError(err).Code)
	}
	if _, _, err := textFromSendResult(json.RawMessage("123")); httputil.AsError(err).Code != CodeProviderMalformed {
		t.Fatalf("code = %q", httputil.AsError(err).Code)
	}
}

func TestSkillManualEdgeCases(t *testing.T) {
	conn := &skillConnector{}
	e := registry.Entry{ID: "skill-1", Kind: registry.KindSkill, Name: "s"}

	// A scheme the adapter cannot read is not a manual.
	e.Endpoint = "command://git"
	res, err := conn.Invoke(context.Background(), InvokeRequest{ResourceID: e.ID, Entry: e, Prompt: "go"})
	if err != nil || skillData(t, res)["provider"] != MockProviderName {
		t.Fatalf("provider = %v, err = %v", skillData(t, res)["provider"], err)
	}

	// A declared local directory that does not exist keeps the offline plan.
	e.Endpoint = "file://" + t.TempDir() + "/missing"
	res, err = conn.Invoke(context.Background(), InvokeRequest{ResourceID: e.ID, Entry: e, Prompt: "go"})
	if err != nil || skillData(t, res)["provider"] != MockProviderName {
		t.Fatalf("missing directory must fall back: %v / %v", skillData(t, res)["provider"], err)
	}

	// A nil client gets the strict default and an unreachable bridge falls back.
	e.Endpoint = "http://127.0.0.1:1"
	res, err = conn.Invoke(context.Background(), InvokeRequest{ResourceID: e.ID, Entry: e, Prompt: "go"})
	if err != nil || skillData(t, res)["provider"] != MockProviderName {
		t.Fatalf("unreachable manual must fall back: %v / %v", skillData(t, res)["provider"], err)
	}

	if _, err := readSkillFile(t.TempDir() + "/missing.md"); httputil.AsError(err).Code != CodeResourceUnreachable {
		t.Fatalf("missing manual code = %q", httputil.AsError(err).Code)
	}
}

func TestSkillManualParsingEdges(t *testing.T) {
	manual := parseSkillManual("# T\n\n1. [ ]\n2. [x] done\n")
	// An empty checkbox line is not a step; the checked one is.
	if len(manual.Steps) != 1 || manual.Steps[0] != "done" {
		t.Fatalf("steps = %v", manual.Steps)
	}

	var body strings.Builder
	body.WriteString("# Many\n\n")
	for i := 0; i < maxSkillSteps+3; i++ {
		fmt.Fprintf(&body, "%d. step %d\n", i+1, i+1)
	}
	if got := len(parseSkillManual(body.String()).Steps); got != maxSkillSteps {
		t.Fatalf("parsed steps = %d, want the cap %d", got, maxSkillSteps)
	}

	declared := make([]string, maxSkillSteps+3)
	for i := range declared {
		declared[i] = fmt.Sprintf("declared %d", i)
	}
	e := &registry.Entry{Kind: registry.KindSkill, KindConfig: &registry.KindConfig{Skill: &registry.SkillConfig{Steps: declared}}}
	if got := len(skillDeclaredSteps(e)); got != maxSkillSteps {
		t.Fatalf("declared steps = %d, want the cap %d", got, maxSkillSteps)
	}
	if skillDeclaredSteps(nil) != nil || skillDeclaredSteps(&registry.Entry{}) != nil {
		t.Fatal("an entry without skill config declares no steps")
	}
}

func TestSkillHealthEdges(t *testing.T) {
	conn := &skillConnector{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := conn.Health(ctx, skillEntry("https://example.test")); err == nil {
		t.Fatal("a cancelled context must be reported")
	}
	health, err := conn.Health(context.Background(), skillEntry("file://%zz"))
	if err != nil || health != registry.HealthUnknown {
		t.Fatalf("unparsable file URL = %v, %v; want unknown", health, err)
	}
	// A path url.Parse can read goes through u.Path, including a drive letter.
	if got, err := localSkillDir("file:///C:/skills/pdf"); err != nil || got == "" {
		t.Fatalf("posix-style file URL = %q, %v", got, err)
	}
}

func TestCustomConnectorEdges(t *testing.T) {
	if _, ok, err := dispatchOf("   "); ok || err != nil {
		t.Fatalf("blank schema = %v, %v", ok, err)
	}

	// A connector built without a client gets the strict default; a loopback
	// endpoint is therefore blocked, not silently reached.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	e := customEntry(srv.URL, `{"x-agenthub-dispatch":{"path":"run"}}`)
	if _, err := (&customConnector{}).Invoke(context.Background(), InvokeRequest{ResourceID: e.ID, Entry: e, Prompt: "go"}); httputil.AsError(err).Code != CodeResourceUnreachable {
		t.Fatalf("code = %q", httputil.AsError(err).Code)
	}

	// A body that fails mid-read is a resource failure, not a malformed answer.
	broken := &customConnector{client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: failingBody{}}, nil
	})}}
	e = customEntry("http://example.test", `{"x-agenthub-dispatch":{"path":"run"}}`)
	if _, err := broken.Invoke(context.Background(), InvokeRequest{ResourceID: e.ID, Entry: e, Prompt: "go"}); httputil.AsError(err).Code != CodeResourceUnreachable {
		t.Fatalf("code = %q", httputil.AsError(err).Code)
	}

	if health, err := (&customConnector{}).Health(context.Background(), &registry.Entry{}); err != nil || health != registry.HealthUnknown {
		t.Fatalf("health = %v, %v; want unknown", health, err)
	}
}

func TestModelConnectorEdges(t *testing.T) {
	if (&mockUnavailable{}).Error() != "no_model_provider" {
		t.Fatal("the sentinel must keep its machine readable text")
	}

	// A chain whose only provider fails has nothing to fall back to.
	router := &ModelRouter{providers: []ModelProvider{fakeProvider{name: "bad", err: errBoom}}}
	if _, _, err := router.GenerateStreamWith(context.Background(), "q", nil); !errors.Is(err, errBoom) {
		t.Fatalf("err = %v", err)
	}

	// An empty router is rebuilt rather than left to answer nothing.
	conn := &modelConnector{router: &ModelRouter{}, defaultMode: registry.ProviderModeMock}
	e := registry.Entry{ID: "model-1", Kind: registry.KindModel, Name: "demo"}
	res, err := conn.Invoke(context.Background(), InvokeRequest{ResourceID: e.ID, Entry: e, Prompt: "hi"})
	if err != nil || !strings.Contains(res.Text, "provider=mock") {
		t.Fatalf("text = %q, err = %v", res.Text, err)
	}

	if health, err := conn.Health(context.Background(), nil); err != nil || health != registry.HealthUnknown {
		t.Fatalf("nil entry health = %v, %v", health, err)
	}
	if health, err := conn.Health(context.Background(), &registry.Entry{}); err != nil || health != registry.HealthUnknown {
		t.Fatalf("offline entry health = %v, %v", health, err)
	}
}

func TestCompletionFrameTextLocations(t *testing.T) {
	cases := []string{
		`{"choices":[{"message":{"content":"message content"}}]}`,
		`{"choices":[{"message":{"reasoning_content":"message reasoning"}}]}`,
		`{"choices":[{"message":{"reasoning":"message reasoning plain"}}]}`,
		`{"choices":[{"text":"legacy text"}]}`,
	}
	for _, raw := range cases {
		var frame completionFrame
		if err := json.Unmarshal([]byte(raw), &frame); err != nil {
			t.Fatal(err)
		}
		if frame.text() == "" {
			t.Fatalf("no text extracted from %s", raw)
		}
	}
	var empty completionFrame
	if empty.text() != "" {
		t.Fatal("an empty frame has no text")
	}
}

func TestNewProviderClientDefaults(t *testing.T) {
	c := newProviderClient(0)
	if c.Timeout != DefaultModelTimeout {
		t.Fatalf("timeout = %v", c.Timeout)
	}
	if err := c.CheckRedirect(nil, nil); !errors.Is(err, http.ErrUseLastResponse) {
		t.Fatalf("redirect error = %v", err)
	}
}

func TestSpeechBodyAndURLFailures(t *testing.T) {
	broken := &openAISpeech{
		transcriptionsURL: "http://speech.test/audio/transcriptions",
		speechURL:         "http://speech.test/audio/speech",
		sttModel:          DefaultSpeechSTTModel,
		ttsModel:          DefaultSpeechTTSModel,
		client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Body: failingBody{}}, nil
		})},
		maxBytes: DefaultSpeechMaxBytes,
	}
	if _, err := broken.Transcribe(context.Background(), []byte("clip")); httputil.AsError(err).Code != CodeResourceUnreachable {
		t.Fatalf("transcribe code = %q", httputil.AsError(err).Code)
	}
	if _, err := broken.Synthesize(context.Background(), "hi"); httputil.AsError(err).Code != CodeResourceUnreachable {
		t.Fatalf("synthesize code = %q", httputil.AsError(err).Code)
	}

	// A base URL with a control character cannot be turned into a request.
	invalid := newOpenAISpeech(SpeechOptions{Endpoint: "http://exa\nmple.test"})
	if _, err := invalid.Transcribe(context.Background(), []byte("clip")); httputil.AsError(err).Code != CodeResourceUnreachable {
		t.Fatalf("transcribe code = %q", httputil.AsError(err).Code)
	}
	if _, err := invalid.Synthesize(context.Background(), "hi"); httputil.AsError(err).Code != CodeResourceUnreachable {
		t.Fatalf("synthesize code = %q", httputil.AsError(err).Code)
	}
}

func TestSSEDecoderEventWithoutData(t *testing.T) {
	frames, err := readAllFrames(t, "event: ping\n\n", 0)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(frames) != 1 || frames[0].event != "ping" || frames[0].data != "" {
		t.Fatalf("frames = %+v", frames)
	}
}
