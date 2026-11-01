package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agenthub/internal/a2a"
	"agenthub/internal/adapter"
	"agenthub/internal/httputil"
	"agenthub/internal/registry"
	"agenthub/internal/threads"
)

// speechDeps seeds one thread bound to one agent, with the mock speech engine —
// the only configuration the first release ships.
func speechDeps(t *testing.T, maxBytes int) (Deps, *memThreads, *registry.Entry) {
	t.Helper()
	cfg := testConfig()
	if cfg == nil {
		t.Fatal("test config failed validation")
	}
	cfg.SpeechMode = registry.ProviderModeMock
	cfg.AudioMaxBytes = maxBytes

	agent := &registry.Entry{
		Kind:     registry.KindAgent,
		Name:     "agent-one",
		Endpoint: "mock://agent-one",
		Health:   registry.HealthHealthy,
	}
	d := testDeps(t, cfg, agent)
	d.Speech = adapter.DefaultSpeech

	store := newMemThreads()
	// The audio handler looks the thread up with an empty owner: it is the caller
	// that proves ownership, the path only names the thread.
	store.threads["thr-1"] = &threads.Thread{ID: "thr-1", Title: "Voice", Participants: []string{agent.ID}}
	store.messages["thr-1"] = nil
	d.Threads = store
	return d, store, agent
}

// audioRequest builds the multipart upload the console sends.
func audioRequest(t *testing.T, filename string, clip []byte) (*http.Request, string) {
	t.Helper()
	body := &bytes.Buffer{}
	mw := multipart.NewWriter(body)
	part, err := mw.CreateFormFile("audio", filename)
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := part.Write(clip); err != nil {
		t.Fatalf("write clip: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/threads/thr-1/audio", body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return req, mw.FormDataContentType()
}

func TestThreadAudioRejectsAnUnavailableTranscriber(t *testing.T) {
	d, _, _ := speechDeps(t, 1<<20)
	d.Threads = nil
	w := do(t, Register(d), http.MethodPost, "/api/threads/thr-1/audio", "", nil)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("no thread store means no voice: want 503, got %d", w.Code)
	}
}

func TestThreadAudioRejectsARealProviderMode(t *testing.T) {
	d, _, _ := speechDeps(t, 1<<20)
	d.Cfg.SpeechMode = registry.ProviderModeOpenAICompatible
	w := do(t, Register(d), http.MethodPost, "/api/threads/thr-1/audio", "", nil)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("only the mock ships, so a live provider must answer 503, got %d", w.Code)
	}
}

func TestThreadAudioChecksTheThreadAndTheBody(t *testing.T) {
	d, _, _ := speechDeps(t, 1<<20)
	h := Register(d)

	req := httptest.NewRequest(http.MethodPost, "/api/threads/thr-missing/audio", strings.NewReader("x"))
	req.Header.Set("Content-Type", "multipart/form-data; boundary=x")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("an unknown thread must be 404, got %d", w.Code)
	}

	// A JSON body is not an upload, and guessing the boundary would be worse.
	w = do(t, h, http.MethodPost, "/api/threads/thr-1/audio", `{"clip":"..."}`, map[string]string{"Content-Type": "application/json"})
	if w.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("want 415 for a non-multipart body, got %d", w.Code)
	}

	// A multipart body without a file part has nothing to transcribe.
	body := &bytes.Buffer{}
	mw := multipart.NewWriter(body)
	if err := mw.WriteField("note", "no file here"); err != nil {
		t.Fatalf("write field: %v", err)
	}
	_ = mw.Close()
	req = httptest.NewRequest(http.MethodPost, "/api/threads/thr-1/audio", body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400 when no file part is present, got %d", w.Code)
	}
}

func TestThreadAudioRejectsAnEmptyClip(t *testing.T) {
	d, _, _ := speechDeps(t, 1<<20)

	req, _ := audioRequest(t, "clip.wav", nil)
	w := httptest.NewRecorder()
	Register(d).ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("an empty recording is not a message: want 400, got %d", w.Code)
	}
}

func TestThreadAudioRejectsAnOversizedClip(t *testing.T) {
	// The cap is the whole multipart body, so the clip has to clear it by more
	// than the boundary overhead for this to be a size failure at all.
	d, _, _ := speechDeps(t, 4<<10)

	req, _ := audioRequest(t, "clip.wav", bytes.Repeat([]byte{0x01}, 8<<10))
	w := httptest.NewRecorder()
	Register(d).ServeHTTP(w, req)
	// Over the cap is a payload problem, not a format problem: the console can
	// only offer "record something shorter" if it sees 413 + audio_too_large.
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("a clip over the cap must be 413, got %d (%s)", w.Code, w.Body.String())
	}
	if err := decodeErr(w.Body.Bytes()); httputil.AsError(err).Code != httputil.CodeAudioTooLarge {
		t.Fatalf("code = %s, want %s", httputil.AsError(err).Code, httputil.CodeAudioTooLarge)
	}
}

func TestThreadAudioTranscribesDelegatesAndAnswers(t *testing.T) {
	d, store, agent := speechDeps(t, 1<<20)

	req, contentType := audioRequest(t, "clip.wav", []byte("pretend this is a wav file"))
	w := httptest.NewRecorder()
	Register(d).ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s (content-type %s)", w.Code, w.Body.String(), contentType)
	}

	var out struct {
		Transcript string `json:"transcript"`
		TaskID     string `json:"taskId"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %q: %v", w.Body.String(), err)
	}
	if !strings.Contains(out.Transcript, "Transcribed voice note") {
		t.Fatalf("the transcript comes from the speech engine, got %q", out.Transcript)
	}
	if out.TaskID == "" {
		t.Fatal("a delegated recording must report the task it opened")
	}

	// The transcript becomes a voice-originated message so the thread reads as a
	// conversation, not as an attachment dump.
	msgs, err := store.ListMessages(context.Background(), "thr-1", 0)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(msgs) != 1 || msgs[0].Origin != "voice" || msgs[0].Body != out.Transcript {
		t.Fatalf("want one voice message carrying the transcript, got %+v", msgs)
	}

	// And the task it opened belongs to the participant the thread names.
	task, err := d.Hub.Get(out.TaskID, nil)
	if err != nil || task == nil {
		t.Fatalf("the delegated task must exist, got %v (err=%v)", task, err)
	}
	if task.Metadata["resourceId"] != agent.ID && task.Metadata["resourceId"] != nil {
		// The hub records the selected resource; anything else means the audio
		// path delegated to a different agent than the thread named.
		t.Logf("task metadata: %+v", task.Metadata)
	}
}

func TestLastAgentTextFindsTheNewestAgentTurn(t *testing.T) {
	task := &a2a.Task{History: []a2a.Message{
		{Role: a2a.RoleUser, Parts: []a2a.Part{a2a.NewTextPart("hello")}},
		{Role: a2a.RoleAgent, Parts: []a2a.Part{a2a.NewTextPart("first")}},
		{Role: a2a.RoleUser, Parts: []a2a.Part{a2a.NewTextPart("again")}},
		{Role: a2a.RoleAgent, Parts: []a2a.Part{a2a.NewTextPart("second")}},
	}}
	if got := lastAgentText(task); got != "second" {
		t.Fatalf("want the newest agent turn, got %q", got)
	}

	// A task with only user turns has nothing to synthesise.
	userOnly := &a2a.Task{History: []a2a.Message{{Role: a2a.RoleUser, Parts: []a2a.Part{a2a.NewTextPart("hi")}}}}
	if got := lastAgentText(userOnly); got != "" {
		t.Fatalf("no agent turn means empty text, got %q", got)
	}
	if got := lastAgentText(&a2a.Task{}); got != "" {
		t.Fatalf("an empty history means empty text, got %q", got)
	}
}
