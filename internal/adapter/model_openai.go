package adapter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"agenthub/internal/a2a"
	"agenthub/internal/httputil"
	"agenthub/internal/registry"
)

// Provider failure codes. They are generic on purpose: a provider body is
// untrusted input and never reaches the caller or the log.
const (
	CodeProviderUnauthorized = httputil.Code("model_provider_unauthorized")
	CodeProviderMalformed    = httputil.Code("model_provider_malformed")
)

// Defaults for a real model call.
const (
	DefaultModelTimeout = 30 * time.Second
	// DefaultModelMaxBytes caps a completion. A runaway generation must not
	// exhaust memory just because the gateway kept streaming.
	DefaultModelMaxBytes = 1 << 20
)

// openAIProvider talks to any OpenAI compatible chat completions endpoint,
// which is exactly what a model router — LiteLLM, OpenRouter, a self hosted
// gateway — exposes. One provider therefore covers "a model" and "a router with
// a fallback chain": the chain is several providers with different models in
// front of the same endpoint.
//
// Generate asks for a single completion; GenerateStream sets stream=true and
// translates the SSE frames into incremental artifacts. Both paths share the
// same request shape, limits and error codes.
type openAIProvider struct {
	name      string
	endpoint  string
	model     string
	apiKeyEnv string
	client    *http.Client
	maxTokens int
}

// newOpenAIProvider builds a provider. apiKeyEnv names the environment variable
// holding the credential: the secret itself is never stored or logged. maxTokens
// is the completion budget; <= 0 keeps the built-in default.
func newOpenAIProvider(name, endpoint, model, apiKeyEnv string, timeout time.Duration, maxTokens int) *openAIProvider {
	if timeout <= 0 {
		timeout = DefaultModelTimeout
	}
	return &openAIProvider{
		name:      name,
		endpoint:  completionsURL(endpoint),
		model:     model,
		apiKeyEnv: strings.TrimSpace(apiKeyEnv),
		client:    newProviderClient(timeout),
		maxTokens: maxTokens,
	}
}

// newProviderClient builds the outbound client for an endpoint an operator
// declared on a resource. The address blocklist of netutil.SafeClient does not
// apply here: the destination is written by an operator, not by a caller (that
// is the surface SafeClientWithAllowlist guards). Redirects still are not
// followed — the first hop is trusted, the second is not.
func newProviderClient(timeout time.Duration) *http.Client {
	if timeout <= 0 {
		timeout = DefaultModelTimeout
	}
	return &http.Client{
		Timeout: timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// completionsURL accepts either a base URL or an endpoint that already points
// at /chat/completions, because both appear in the wild.
func completionsURL(endpoint string) string {
	e := strings.TrimSpace(endpoint)
	if e == "" {
		return ""
	}
	if strings.HasSuffix(e, "/chat/completions") {
		return e
	}
	return strings.TrimSuffix(e, "/") + "/chat/completions"
}

func (p *openAIProvider) Name() string { return p.name }

// effectiveMaxTokens falls back to the shared default so a provider built
// without a budget keeps asking for the same one it always did.
func (p *openAIProvider) effectiveMaxTokens() int {
	if p != nil && p.maxTokens > 0 {
		return p.maxTokens
	}
	return registry.DefaultModelMaxTokens
}

func (p *openAIProvider) Generate(ctx context.Context, prompt string) (string, error) {
	if p == nil || p.endpoint == "" {
		return "", &httputil.Error{Code: CodeResourceUnreachable}
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	payload := map[string]any{
		"model":      p.model,
		"stream":     false,
		"messages":   []map[string]string{{"role": "user", "content": prompt}},
		"max_tokens": p.effectiveMaxTokens(),
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", &httputil.Error{Code: CodeProviderMalformed}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, bytes.NewReader(body))
	if err != nil {
		return "", &httputil.Error{Code: CodeResourceUnreachable}
	}
	req.Header.Set("Content-Type", "application/json")
	if p.apiKeyEnv != "" {
		if key := os.Getenv(p.apiKeyEnv); key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
	}
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
	raw, err := io.ReadAll(io.LimitReader(resp.Body, DefaultModelMaxBytes))
	if err != nil {
		return "", &httputil.Error{Code: CodeResourceUnreachable}
	}
	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
				// Reasoning models answer in reasoning_content and leave content
				// empty, so the visible text has to be read from there.
				ReasoningContent string `json:"reasoning_content"`
				Reasoning        string `json:"reasoning"`
			} `json:"message"`
			Text string `json:"text"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", &httputil.Error{Code: CodeProviderMalformed}
	}
	for _, c := range parsed.Choices {
		if strings.TrimSpace(c.Message.Content) != "" {
			return c.Message.Content, nil
		}
		if strings.TrimSpace(c.Message.ReasoningContent) != "" {
			return c.Message.ReasoningContent, nil
		}
		if strings.TrimSpace(c.Message.Reasoning) != "" {
			return c.Message.Reasoning, nil
		}
		if strings.TrimSpace(c.Text) != "" {
			return c.Text, nil
		}
	}
	return "", &httputil.Error{Code: CodeProviderMalformed}
}

// completionFrame is one chunk of a streamed completion. Gateways disagree on
// where the visible text lives — delta.content is the specification, reasoning
// models use reasoning_content, and a few proxies keep message.text — so every
// documented location is read in order.
type completionFrame struct {
	Choices []struct {
		Delta struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
			Reasoning        string `json:"reasoning"`
		} `json:"delta"`
		Message struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
			Reasoning        string `json:"reasoning"`
		} `json:"message"`
		Text string `json:"text"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (f completionFrame) text() string {
	for _, c := range f.Choices {
		for _, candidate := range []string{
			c.Delta.Content, c.Delta.ReasoningContent, c.Delta.Reasoning,
			c.Message.Content, c.Message.ReasoningContent, c.Message.Reasoning, c.Text,
		} {
			if strings.TrimSpace(candidate) != "" {
				return candidate
			}
		}
	}
	return ""
}

// GenerateStream asks for stream=true and hands every non-empty delta to sink.
// The aggregate it returns is byte-identical to what a non-streamed call would
// have produced for the same upstream, which is what lets the hub persist one
// final artifact while the subscriber watches the chunks arrive.
func (p *openAIProvider) GenerateStream(ctx context.Context, prompt string, sink StreamSink) (string, error) {
	if p == nil || p.endpoint == "" {
		return "", &httputil.Error{Code: CodeResourceUnreachable}
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	payload := map[string]any{
		"model":      p.model,
		"stream":     true,
		"messages":   []map[string]string{{"role": "user", "content": prompt}},
		"max_tokens": p.effectiveMaxTokens(),
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", &httputil.Error{Code: CodeProviderMalformed}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, bytes.NewReader(body))
	if err != nil {
		return "", &httputil.Error{Code: CodeResourceUnreachable}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	if p.apiKeyEnv != "" {
		if key := os.Getenv(p.apiKeyEnv); key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
	}
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

	decoder := newSSEDecoder(resp.Body)
	var out strings.Builder
	index := 0
	for {
		_, data, ok, err := decoder.Next()
		if err != nil {
			if errors.Is(err, errStreamTooLarge) {
				return "", &httputil.Error{Code: CodeProviderMalformed}
			}
			return "", &httputil.Error{Code: CodeResourceUnreachable}
		}
		if !ok {
			break
		}
		if strings.TrimSpace(data) == "" {
			continue
		}
		var frame completionFrame
		if err := json.Unmarshal([]byte(data), &frame); err != nil {
			return "", &httputil.Error{Code: CodeProviderMalformed}
		}
		if frame.Error != nil {
			// The upstream answered inside the stream and named its own
			// failure; its message is untrusted input and never reaches the
			// caller, only the machine code does.
			return "", &httputil.Error{Code: CodeResourceUnreachable}
		}
		piece := frame.text()
		if piece == "" {
			continue
		}
		out.WriteString(piece)
		if sink != nil {
			chunk := streamChunk("model.stream", index, a2a.NewTextPart(piece),
				"kind", string(registry.KindModel), "provider", p.name)
			if err := sink(chunk); err != nil {
				return out.String(), err
			}
			index++
		}
	}
	if out.Len() == 0 {
		// A 200 with no completion is a malformed answer, exactly as it is on
		// the non-streamed path.
		return "", &httputil.Error{Code: CodeProviderMalformed}
	}
	return out.String(), nil
}

// defaultMode normalises the instance wide provider mode (AGENTHUB_MODEL_ROUTER_MODE)
// coming from the options: empty or unknown degrades to the offline default, so a
// typo in the environment keeps every model entry offline instead of turning it
// into a gateway call nobody configured.
func defaultMode(opts Options) string {
	if opts.ModelRouterMode != "" {
		return opts.ModelRouterMode
	}
	return registry.DefaultModelProviderMode
}

// newModelRouterFor builds the provider chain a model entry asks for. instanceDefault
// is the provider mode entries inherit when they do not name one
// (AGENTHUB_MODEL_ROUTER_MODE); the entry's own value still wins.
//
// Mock mode stays entirely offline. Router mode puts the configured endpoint
// first and appends one provider per fallback target, so a router that fronts
// several models degrades down the list instead of failing outright. The mock is
// only appended when the operator asked for it explicitly: silently answering
// with a mock after a real provider failed would fabricate a completion.
func newModelRouterFor(e *registry.Entry, instanceDefault string) *ModelRouter {
	if e == nil || e.KindConfig == nil || e.KindConfig.Model == nil {
		return NewModelRouter()
	}
	cfg := e.KindConfig.Model
	if cfg.EffectiveProviderModeWith(instanceDefault) != registry.ProviderModeOpenAICompatible {
		return NewModelRouter()
	}
	endpoint := cfg.EffectiveEndpoint(e)
	if endpoint == "" {
		return NewModelRouter()
	}
	maxTokens := cfg.EffectiveMaxTokens()
	providers := []ModelProvider{
		newOpenAIProvider("openai:"+cfg.EffectiveModelID(e), endpoint, cfg.EffectiveModelID(e), cfg.APIKeyEnv, time.Duration(cfg.TimeoutMS)*time.Millisecond, maxTokens),
	}
	for _, t := range cfg.Targets {
		model := strings.TrimSpace(t.Model)
		if model == "" || model == cfg.EffectiveModelID(e) {
			continue
		}
		providers = append(providers, newOpenAIProvider("openai:"+t.Name, endpoint, model, cfg.APIKeyEnv, time.Duration(cfg.TimeoutMS)*time.Millisecond, maxTokens))
	}
	if cfg.FallbackMock {
		providers = append(providers, mockModelProvider{})
	}
	return &ModelRouter{providers: providers}
}

// modelLabel renders the provider chain for logs and artifacts. It names
// providers and models only — never credentials, prompts or completions.
func modelLabel(r *ModelRouter) string {
	if r == nil || len(r.providers) == 0 {
		return "none"
	}
	names := make([]string, 0, len(r.providers))
	for _, p := range r.providers {
		names = append(names, p.Name())
	}
	return fmt.Sprintf("%d:%s", len(r.providers), strings.Join(names, ">"))
}
