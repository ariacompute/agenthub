package adapter

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"agenthub/internal/httputil"
	"agenthub/internal/registry"
)

func TestTranscribeIsDeterministic(t *testing.T) {
	var s Speech
	out, err := s.Transcribe(context.Background(), []byte("voice-bytes-1"))
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
	if !strings.Contains(out, "Transcribed voice note") {
		t.Fatalf("unexpected transcript: %q", out)
	}
	// The same bytes must always yield the same transcript so tests are stable.
	again, _ := s.Transcribe(context.Background(), []byte("voice-bytes-1"))
	if again != out {
		t.Fatalf("Transcribe is not deterministic: %q != %q", out, again)
	}
	// Different bytes produce different text.
	other, _ := s.Transcribe(context.Background(), []byte("different-clip"))
	if other == out {
		t.Fatalf("different input produced identical transcript")
	}
	// An empty clip is an error rather than a blank transcript.
	if _, err := s.Transcribe(context.Background(), nil); err == nil {
		t.Fatal("empty audio must error")
	}
}

func TestSynthesizeProducesValidWAV(t *testing.T) {
	var s Speech
	wav, err := s.Synthesize(context.Background(), "hello")
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	// 44-byte PCM WAV header plus one second at 8kHz/16-bit mono.
	const expected = 44 + 8000*2
	if len(wav) != expected {
		t.Fatalf("wav length = %d, want %d", len(wav), expected)
	}
	if string(wav[0:4]) != "RIFF" || string(wav[8:12]) != "WAVE" {
		t.Fatalf("not a RIFF/WAVE container: % x", wav[0:12])
	}
	if string(wav[12:16]) != "fmt " {
		t.Fatalf("missing fmt chunk")
	}
	if sr := binary.LittleEndian.Uint32(wav[24:28]); sr != 8000 {
		t.Fatalf("sample rate = %d, want 8000", sr)
	}
	if ch := binary.LittleEndian.Uint16(wav[22:24]); ch != 1 {
		t.Fatalf("channels = %d, want mono", ch)
	}
	if bits := binary.LittleEndian.Uint16(wav[34:36]); bits != 16 {
		t.Fatalf("bits = %d, want 16", bits)
	}
	if _, err := s.Synthesize(context.Background(), ""); err == nil {
		t.Fatal("empty text must error")
	}
}

func speechProviderFor(t *testing.T, handler http.HandlerFunc) (*openAISpeech, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return &openAISpeech{
		transcriptionsURL: srv.URL + "/audio/transcriptions",
		speechURL:         srv.URL + "/audio/speech",
		sttModel:          DefaultSpeechSTTModel,
		ttsModel:          DefaultSpeechTTSModel,
		client:            srv.Client(),
		maxBytes:          DefaultSpeechMaxBytes,
	}, srv
}

func TestNewSpeechFallsBackToTheMock(t *testing.T) {
	for _, opts := range []SpeechOptions{
		{},
		{Mode: registry.ProviderModeMock, Endpoint: "http://example.test"},
		{Mode: registry.ProviderModeOpenAICompatible},
	} {
		s := NewSpeech(opts)
		out, err := s.Transcribe(context.Background(), []byte("clip"))
		if err != nil || !strings.Contains(out, "Transcribed voice note") {
			t.Fatalf("opts = %+v: out = %q, err = %v", opts, out, err)
		}
	}
}

func TestNewSpeechUsesARealProvider(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"text": "real transcript"})
	}))
	defer srv.Close()

	s := NewSpeech(SpeechOptions{
		Mode:     registry.ProviderModeOpenAICompatible,
		Endpoint: srv.URL,
		Egress:   []string{"127.0.0.1"},
	})
	out, err := s.Transcribe(context.Background(), []byte("clip"))
	if err != nil || out != "real transcript" {
		t.Fatalf("out = %q, err = %v", out, err)
	}
}

func TestOpenAISpeechTranscribes(t *testing.T) {
	t.Setenv("TEST_SPEECH_KEY", "secret")
	provider, _ := speechProviderFor(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/audio/transcriptions" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer secret" {
			t.Errorf("Authorization = %q", got)
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("parse multipart: %v", err)
			return
		}
		if got := r.FormValue("model"); got != DefaultSpeechSTTModel {
			t.Errorf("model = %q", got)
		}
		file, header, err := r.FormFile("file")
		if err != nil {
			t.Errorf("form file: %v", err)
			return
		}
		defer file.Close()
		if header.Filename != "clip.wav" {
			t.Errorf("filename = %q", header.Filename)
		}
		raw, _ := io.ReadAll(file)
		if string(raw) != "voice" {
			t.Errorf("clip = %q", raw)
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"text": "hello"})
	})
	provider.apiKeyEnv = "TEST_SPEECH_KEY"

	out, err := provider.Transcribe(context.Background(), []byte("voice"))
	if err != nil || out != "hello" {
		t.Fatalf("out = %q, err = %v", out, err)
	}
}

func TestOpenAISpeechSynthesizes(t *testing.T) {
	provider, _ := speechProviderFor(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/audio/speech" {
			t.Errorf("path = %s", r.URL.Path)
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode: %v", err)
		}
		if payload["model"] != DefaultSpeechTTSModel || payload["input"] != "hello" {
			t.Errorf("payload = %+v", payload)
		}
		if payload["response_format"] != "wav" {
			t.Errorf("response_format = %v", payload["response_format"])
		}
		w.Header().Set("Content-Type", "audio/wav")
		_, _ = w.Write([]byte("RIFFdata"))
	})
	out, err := provider.Synthesize(context.Background(), "hello")
	if err != nil || string(out) != "RIFFdata" {
		t.Fatalf("out = %q, err = %v", out, err)
	}
}

func TestOpenAISpeechTranscribeFailures(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		code   httputil.Code
	}{
		{name: "unauthorized", status: http.StatusUnauthorized, code: CodeProviderUnauthorized},
		{name: "upstream failure", status: http.StatusInternalServerError, code: CodeResourceUnreachable},
		{name: "malformed body", status: http.StatusOK, body: "not-json", code: CodeProviderMalformed},
		{name: "empty transcript", status: http.StatusOK, body: `{"text":"   "}`, code: CodeProviderMalformed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider, _ := speechProviderFor(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			})
			if _, err := provider.Transcribe(context.Background(), []byte("clip")); httputil.AsError(err).Code != tc.code {
				t.Fatalf("code = %q, want %q", httputil.AsError(err).Code, tc.code)
			}
		})
	}
}

func TestOpenAISpeechSynthesizeFailures(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		code   httputil.Code
	}{
		{name: "unauthorized", status: http.StatusForbidden, code: CodeProviderUnauthorized},
		{name: "upstream failure", status: http.StatusBadGateway, code: CodeResourceUnreachable},
		{name: "empty audio", status: http.StatusOK, code: CodeProviderMalformed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider, _ := speechProviderFor(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			})
			if _, err := provider.Synthesize(context.Background(), "hi"); httputil.AsError(err).Code != tc.code {
				t.Fatalf("code = %q, want %q", httputil.AsError(err).Code, tc.code)
			}
		})
	}
}

func TestOpenAISpeechRejectsEmptyInput(t *testing.T) {
	provider, _ := speechProviderFor(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("empty input must be refused before a request")
	})
	if _, err := provider.Transcribe(context.Background(), nil); err == nil {
		t.Fatal("empty audio must error")
	}
	if _, err := provider.Synthesize(context.Background(), ""); err == nil {
		t.Fatal("empty text must error")
	}
}

func TestOpenAISpeechMapsTransportAndContextFailures(t *testing.T) {
	provider, _ := speechProviderFor(t, func(w http.ResponseWriter, r *http.Request) {})
	provider.client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("connection refused")
	})}
	if _, err := provider.Transcribe(context.Background(), []byte("clip")); httputil.AsError(err).Code != CodeResourceUnreachable {
		t.Fatalf("transcribe code = %q", httputil.AsError(err).Code)
	}
	if _, err := provider.Synthesize(context.Background(), "hi"); httputil.AsError(err).Code != CodeResourceUnreachable {
		t.Fatalf("synthesize code = %q", httputil.AsError(err).Code)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := provider.Transcribe(ctx, []byte("clip")); err == nil {
		t.Fatal("a cancelled context must stop transcribing")
	}
	if _, err := provider.Synthesize(ctx, "hi"); err == nil {
		t.Fatal("a cancelled context must stop synthesizing")
	}
}

func TestAudioURLShapes(t *testing.T) {
	cases := map[string]string{
		"https://api.example.test":                      "https://api.example.test/audio/transcriptions",
		"https://api.example.test/v1":                   "https://api.example.test/v1/audio/transcriptions",
		"https://api.example.test/audio":                "https://api.example.test/audio/transcriptions",
		"https://api.example.test/audio/transcriptions": "https://api.example.test/audio/transcriptions",
	}
	for base, want := range cases {
		if got := audioURL(base, "transcriptions"); got != want {
			t.Fatalf("audioURL(%q) = %q, want %q", base, got, want)
		}
	}
}

func TestSpeechOptionsDefaults(t *testing.T) {
	provider := newOpenAISpeech(SpeechOptions{Endpoint: "http://example.test"})
	if provider.sttModel != DefaultSpeechSTTModel || provider.ttsModel != DefaultSpeechTTSModel {
		t.Fatalf("models = %q / %q", provider.sttModel, provider.ttsModel)
	}
	if provider.maxBytes != DefaultSpeechMaxBytes || provider.client.Timeout != DefaultSpeechTimeout {
		t.Fatalf("limits = %d / %v", provider.maxBytes, provider.client.Timeout)
	}
	if provider.apiKeyEnv != "" {
		t.Fatalf("apiKeyEnv = %q", provider.apiKeyEnv)
	}
	provider = newOpenAISpeech(SpeechOptions{Endpoint: "http://example.test", Timeout: time.Second, MaxBytes: 1024, STTModel: "stt", TTSModel: "tts", APIKeyEnv: "KEY"})
	if provider.sttModel != "stt" || provider.ttsModel != "tts" || provider.maxBytes != 1024 || provider.client.Timeout != time.Second {
		t.Fatalf("explicit options were not honoured: %+v", provider)
	}
}
