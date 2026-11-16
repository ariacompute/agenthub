package adapter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"strings"
	"time"

	"agenthub/internal/httputil"
	"agenthub/internal/netutil"
)

// Defaults for a real voice call.
const (
	DefaultSpeechTimeout  = 30 * time.Second
	DefaultSpeechMaxBytes = 16 << 20
	DefaultSpeechSTTModel = "whisper-1"
	DefaultSpeechTTSModel = "tts-1"
)

// openAISpeech talks to the audio half of an OpenAI compatible gateway:
// POST /audio/transcriptions for STT and POST /audio/speech for TTS. It is the
// same provider shape the model router uses, so one base URL covers both when
// the gateway serves them.
type openAISpeech struct {
	transcriptionsURL string
	speechURL         string
	apiKeyEnv         string
	sttModel          string
	ttsModel          string
	client            *http.Client
	maxBytes          int64
}

func newOpenAISpeech(opts SpeechOptions) *openAISpeech {
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultSpeechTimeout
	}
	maxBytes := opts.MaxBytes
	if maxBytes <= 0 {
		maxBytes = DefaultSpeechMaxBytes
	}
	stt := strings.TrimSpace(opts.STTModel)
	if stt == "" {
		stt = DefaultSpeechSTTModel
	}
	tts := strings.TrimSpace(opts.TTSModel)
	if tts == "" {
		tts = DefaultSpeechTTSModel
	}
	base := strings.TrimRight(strings.TrimSpace(opts.Endpoint), "/")
	return &openAISpeech{
		transcriptionsURL: audioURL(base, "transcriptions"),
		speechURL:         audioURL(base, "speech"),
		apiKeyEnv:         strings.TrimSpace(opts.APIKeyEnv),
		sttModel:          stt,
		ttsModel:          tts,
		client:            netutil.SafeClientWithAllowlist(timeout, maxBytes, opts.Egress),
		maxBytes:          maxBytes,
	}
}

// audioURL accepts a base URL, one that already ends at /audio, or the full
// operation path, because all three appear in the wild.
func audioURL(base, operation string) string {
	switch {
	case strings.HasSuffix(base, "/audio/"+operation):
		return base
	case strings.HasSuffix(base, "/audio"):
		return base + "/" + operation
	default:
		return base + "/audio/" + operation
	}
}

func (p *openAISpeech) Transcribe(ctx context.Context, audio []byte) (string, error) {
	if len(audio) == 0 {
		return "", fmt.Errorf("empty audio payload")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("model", p.sttModel); err != nil {
		return "", &httputil.Error{Code: CodeProviderMalformed}
	}
	part, err := writer.CreateFormFile("file", "clip.wav")
	if err != nil {
		return "", &httputil.Error{Code: CodeProviderMalformed}
	}
	if _, err := part.Write(audio); err != nil {
		return "", &httputil.Error{Code: CodeProviderMalformed}
	}
	if err := writer.Close(); err != nil {
		return "", &httputil.Error{Code: CodeProviderMalformed}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.transcriptionsURL, &body)
	if err != nil {
		return "", &httputil.Error{Code: CodeResourceUnreachable}
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Accept", "application/json")
	p.authorize(req)
	resp, err := p.client.Do(req)
	if err != nil {
		return "", &httputil.Error{Code: CodeResourceUnreachable}
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return "", &httputil.Error{Code: CodeProviderUnauthorized}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", &httputil.Error{Code: CodeResourceUnreachable}
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, p.maxBytes))
	if err != nil {
		return "", &httputil.Error{Code: CodeResourceUnreachable}
	}
	var parsed struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", &httputil.Error{Code: CodeProviderMalformed}
	}
	if strings.TrimSpace(parsed.Text) == "" {
		return "", &httputil.Error{Code: CodeProviderMalformed}
	}
	return parsed.Text, nil
}

func (p *openAISpeech) Synthesize(ctx context.Context, text string) ([]byte, error) {
	if text == "" {
		return nil, fmt.Errorf("empty text")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	payload, err := json.Marshal(map[string]any{
		"model":           p.ttsModel,
		"input":           text,
		"response_format": "wav",
	})
	if err != nil {
		return nil, &httputil.Error{Code: CodeProviderMalformed}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.speechURL, bytes.NewReader(payload))
	if err != nil {
		return nil, &httputil.Error{Code: CodeResourceUnreachable}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "audio/wav")
	p.authorize(req)
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, &httputil.Error{Code: CodeResourceUnreachable}
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, &httputil.Error{Code: CodeProviderUnauthorized}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &httputil.Error{Code: CodeResourceUnreachable}
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, p.maxBytes))
	if err != nil {
		return nil, &httputil.Error{Code: CodeResourceUnreachable}
	}
	if len(raw) == 0 {
		return nil, &httputil.Error{Code: CodeProviderMalformed}
	}
	return raw, nil
}

func (p *openAISpeech) authorize(req *http.Request) {
	if p.apiKeyEnv == "" {
		return
	}
	if key := strings.TrimSpace(os.Getenv(p.apiKeyEnv)); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
}
