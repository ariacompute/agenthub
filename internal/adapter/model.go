package adapter

import (
	"context"

	"agenthub/internal/a2a"
	"agenthub/internal/registry"
)

// ModelProvider is the single surface a model backend has to implement.
//
// TODO(real): add an OpenAI-compatible provider (chat completions + streaming)
// and register it first in NewModelRouter; the mock stays as the fallback so a
// deployment without credentials still runs end to end.
type ModelProvider interface {
	Name() string
	Generate(ctx context.Context, prompt string) (string, error)
}

// mockModelProvider is deterministic: it renders a structured summary rather
// than prose so the console can display raw artifacts without localisation.
type mockModelProvider struct{}

func (mockModelProvider) Name() string { return "mock" }

func (mockModelProvider) Generate(ctx context.Context, prompt string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return "provider=mock tokens=" + itoa(tokenCount(prompt)) + " digest=" + digest(prompt)[:16], nil
}

// ModelRouter selects a provider and falls back down the chain on failure.
type ModelRouter struct {
	providers []ModelProvider
}

// NewModelRouter builds the default router.
func NewModelRouter() *ModelRouter {
	return &ModelRouter{providers: []ModelProvider{mockModelProvider{}}}
}

// Generate walks the fallback chain until one provider answers.
func (r *ModelRouter) Generate(ctx context.Context, prompt string) (string, error) {
	if len(r.providers) == 0 {
		return "", &mockUnavailable{}
	}
	var lastErr error
	for _, p := range r.providers {
		out, err := p.Generate(ctx, prompt)
		if err == nil {
			return out, nil
		}
		lastErr = err
	}
	if lastErr != nil {
		return "", lastErr
	}
	return "", &mockUnavailable{}
}

type mockUnavailable struct{}

func (mockUnavailable) Error() string { return "no_model_provider" }

type modelConnector struct {
	router *ModelRouter
}

func (c *modelConnector) Kind() registry.Kind { return registry.KindModel }

func (c *modelConnector) Invoke(ctx context.Context, req InvokeRequest) (InvokeResult, error) {
	if c.router == nil {
		c.router = NewModelRouter()
	}
	out, err := c.router.Generate(ctx, req.Prompt)
	if err != nil {
		return InvokeResult{}, err
	}
	tokens := tokenCount(req.Prompt)
	data := map[string]any{
		"kind":     string(registry.KindModel),
		"resource": req.Entry.Name,
		"provider": "mock",
		"tokens":   tokens,
		"digest":   digest(req.Prompt)[:16],
		"echo":     req.Prompt,
	}
	return InvokeResult{
		Text:      out,
		Parts:     []a2a.Part{a2a.NewTextPart(out), a2a.NewDataPart(data)},
		Artifacts: []a2a.Artifact{artifact("model.completion", 0, []a2a.Part{a2a.NewDataPart(data)}, "kind", string(registry.KindModel))},
	}, nil
}

func (c *modelConnector) Health(ctx context.Context, e *registry.Entry) (registry.Health, error) {
	return mockHealth(e), nil
}
