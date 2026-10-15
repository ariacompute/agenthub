package adapter

import (
	"context"
	"strings"

	"agenthub/internal/a2a"
	"agenthub/internal/registry"
)

// agentConnector emulates an autonomous agent: it plans, executes steps and can
// park the task in input-required when the request needs approval.
//
// TODO(real): forward to aria-agent-cloud /v1/agents/sessions/{id}/events/stream
// and translate the agent.* SSE frames into A2A artifacts.
type agentConnector struct{}

func (c *agentConnector) Kind() registry.Kind { return registry.KindAgent }

func (c *agentConnector) Invoke(ctx context.Context, req InvokeRequest) (InvokeResult, error) {
	if err := ctx.Err(); err != nil {
		return InvokeResult{}, err
	}
	if requiresApproval(req.Prompt) {
		return InvokeResult{
			Text:          "state=input-required resource=" + req.Entry.Name + " reason=approval",
			RequiresInput: true,
			Prompt:        "approval_required:" + req.ResourceID,
		}, nil
	}
	steps := []string{"recall", "plan", "execute", "memorize"}
	if pick(req.Prompt, 2) == 0 {
		steps = append(steps, "verify")
	}
	out := "agent=" + req.Entry.Name + " steps=" + itoa(len(steps)) + " digest=" + digest(req.Prompt)[:16]
	data := map[string]any{
		"kind":     string(registry.KindAgent),
		"resource": req.Entry.Name,
		"steps":    steps,
		"goal":     req.Prompt,
		"digest":   digest(req.Prompt)[:16],
	}
	return InvokeResult{
		Text:      out,
		Parts:     []a2a.Part{a2a.NewTextPart(out), a2a.NewDataPart(data)},
		Artifacts: []a2a.Artifact{artifact("agent.plan", 0, []a2a.Part{a2a.NewDataPart(data)}, "kind", string(registry.KindAgent))},
	}, nil
}

func (c *agentConnector) Health(ctx context.Context, e *registry.Entry) (registry.Health, error) {
	return mockHealth(e), nil
}

// requiresApproval parks the task when the request asks for an explicit
// approval. The match is word level so an answer like "approved" resumes
// instead of re-triggering the same question.
func requiresApproval(prompt string) bool {
	lower := strings.ToLower(prompt)
	if strings.Contains(lower, "approval") {
		return true
	}
	for _, field := range strings.Fields(lower) {
		if field == "approve" || field == "confirm" || strings.HasPrefix(field, "confirm") {
			return true
		}
	}
	return false
}
