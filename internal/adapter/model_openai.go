package adapter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

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
// TODO(real): streaming. The request already sets stream=false, so adding SSE
// means a second Generate path, not a change to this one.
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
		client:    &http.Client{Timeout: timeout},
		maxTokens: maxTokens,
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
