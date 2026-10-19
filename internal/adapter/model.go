package adapter

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"

	"agenthub/internal/a2a"
	"agenthub/internal/httputil"
	"agenthub/internal/registry"
)

// ModelProvider is the single surface a model backend has to implement.
//
// Two implementations exist: mockModelProvider, which is deterministic and
// offline, and openAIProvider, which talks to any OpenAI compatible chat
// completions endpoint — the shape every model router (LiteLLM, OpenRouter, a
// self hosted gateway) exposes.
//
// TODO(real): streaming. Both providers request a non-streamed completion today;
// adding SSE means a second path beside Generate, not a change to this one.
type ModelProvider interface {
	Name() string
	Generate(ctx context.Context, prompt string) (string, error)
}

// mockModelProvider is deterministic: it renders a structured summary rather
// than prose so the console can display raw artifacts without localisation.
type mockModelProvider struct{}

func (mockModelProvider) Name() string { return MockProviderName }

func (mockModelProvider) Generate(ctx context.Context, prompt string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return "provider=mock tokens=" + itoa(tokenCount(prompt)) + " digest=" + digest(prompt)[:16], nil
}

// ModelRouter selects a provider and falls back down the chain on failure.
//
// The chain is explicit: a real provider failing does not quietly land on the
// mock unless the operator configured that, because a completion nobody produced
// is worse than an error somebody can act on.
type ModelRouter struct {
	providers []ModelProvider
}

// NewModelRouter builds the default router: offline, deterministic, no
// credentials and no network — which is what an instance without a model
// backend has to fall back to.
func NewModelRouter() *ModelRouter {
	return &ModelRouter{providers: []ModelProvider{mockModelProvider{}}}
}

// NewModelRouterFor builds the router a registered model resource asks for:
// router mode produces a real provider plus one provider per fallback target.
// instanceDefault is the mode entries inherit when they do not name one, so an
// adapter built without it keeps the offline behaviour.
func NewModelRouterFor(e *registry.Entry, instanceDefault string) *ModelRouter {
	return newModelRouterFor(e, instanceDefault)
}

// Generate walks the fallback chain until one provider answers.
func (r *ModelRouter) Generate(ctx context.Context, prompt string) (string, error) {
	out, _, err := r.GenerateWith(ctx, prompt)
	return out, err
}

// GenerateWith is Generate plus the name of the provider that answered, so the
// artifact can say which model produced a completion instead of hiding the
// fallback behind a single string.
func (r *ModelRouter) GenerateWith(ctx context.Context, prompt string) (string, string, error) {
	if r == nil || len(r.providers) == 0 {
		return "", "", &mockUnavailable{}
	}
	var lastErr error
	for _, p := range r.providers {
		out, err := p.Generate(ctx, prompt)
		if err == nil {
			return out, p.Name(), nil
		}
		lastErr = err
	}
	if lastErr != nil {
		return "", "", lastErr
	}
	return "", "", &mockUnavailable{}
}

type mockUnavailable struct{}

func (mockUnavailable) Error() string { return "no_model_provider" }

type modelConnector struct {
	router *ModelRouter
	// defaultMode is kept so a connector built without a router can rebuild one
	// against the same instance wide provider mode.
	defaultMode string
}

func (c *modelConnector) Kind() registry.Kind { return registry.KindModel }

func (c *modelConnector) Invoke(ctx context.Context, req InvokeRequest) (InvokeResult, error) {
	if c.router == nil {
		c.router = newModelRouterFor(&req.Entry, c.defaultMode)
	}
	if len(c.router.providers) == 0 {
		c.router = NewModelRouter()
	}
	out, provider, err := c.router.GenerateWith(ctx, req.Prompt)
	if err != nil {
		return InvokeResult{}, err
	}
	tokens := tokenCount(req.Prompt)
	data := map[string]any{
		"kind":     string(registry.KindModel),
		"resource": req.Entry.Name,
		"provider": provider,
		"tokens":   tokens,
		"digest":   digest(req.Prompt)[:16],
		"echo":     req.Prompt,
	}
	return InvokeResult{
		Text:      out,
		Parts:     []a2a.Part{a2a.NewTextPart(out), a2a.NewDataPart(data)},
		Artifacts: []a2a.Artifact{artifact("model.completion", 0, []a2a.Part{a2a.NewDataPart(data)}, "kind", string(registry.KindModel), "provider", provider)},
	}, nil
}

// Health asks the gateway the provider would call. An offline entry is not
// probed: nothing calls its endpoint, so a network answer would describe an
// address the adapter never uses.
func (c *modelConnector) Health(ctx context.Context, e *registry.Entry) (registry.Health, error) {
	if e == nil {
		return nothingMeasured, nil
	}
	cfg := modelConfigOf(e)
	if cfg.EffectiveProviderModeWith(c.defaultMode) != registry.ProviderModeOpenAICompatible {
		return nothingMeasured, nil
	}
	base := httpEndpoint(cfg.EffectiveEndpoint(e))
	if base == "" {
		return nothingMeasured, nil
	}
	headers := bearerHeaders(cfg.APIKeyEnv)

	// Listing models is free and every OpenAI compatible router serves it.
	health, err := probeHTTP(ctx, http.MethodGet, base+"/models", headers, nil)
	if err != nil {
		return "", err
	}
	if health != registry.HealthUnhealthy {
		return health, nil
	}
	// "unhealthy" can simply mean this router does not serve /models, so ask the
	// endpoint the adapter really calls. One token is a fair price for an answer
	// that is true, and it is bounded by max_tokens.
	payload, err := json.Marshal(map[string]any{
		"model":      cfg.EffectiveModelID(e),
		"messages":   []map[string]string{{"role": "user", "content": "ping"}},
		"max_tokens": 1,
	})
	if err != nil {
		return "", &httputil.Error{Code: CodeProviderMalformed}
	}
	headers["Content-Type"] = "application/json"
	return probeHTTP(ctx, http.MethodPost, base+"/chat/completions", headers, bytes.NewReader(payload))
}

// modelConfigOf never returns nil: an entry that filled only the generic
// columns still resolves through the instance wide provider mode.
func modelConfigOf(e *registry.Entry) *registry.ModelConfig {
	if e != nil && e.KindConfig != nil && e.KindConfig.Model != nil {
		return e.KindConfig.Model
	}
	return &registry.ModelConfig{}
}
