package adapter

import (
	"context"
	"net/http"

	"agenthub/internal/a2a"
	"agenthub/internal/registry"
)

// skillConnector loads a "job manual": trigger, ordered steps, knowledge and the
// tool list the manual expects. A skill is versioned process knowledge, not an
// ad-hoc prompt.
//
// TODO(real): read the manual from the resource endpoint (or a local directory)
// and execute its declared steps through the matching connectors.
type skillConnector struct{}

func (c *skillConnector) Kind() registry.Kind { return registry.KindSkill }

func (c *skillConnector) Invoke(ctx context.Context, req InvokeRequest) (InvokeResult, error) {
	if err := ctx.Err(); err != nil {
		return InvokeResult{}, err
	}
	steps := []string{
		"match_trigger",
		"load_manual",
		"collect_arguments",
		"execute_steps",
		"report_result",
	}
	if len(req.Entry.Capabilities) > 0 {
		steps = append(steps, "apply_capability:"+req.Entry.Capabilities[pick(req.Prompt, len(req.Entry.Capabilities))])
	}
	out := "skill=" + req.Entry.Name + " steps=" + itoa(len(steps)) + " digest=" + digest(req.Prompt)[:16]
	data := map[string]any{
		"kind":     string(registry.KindSkill),
		"resource": req.Entry.Name,
		"provider": MockProviderName,
		"version":  req.Entry.Version,
		"trigger":  req.Prompt,
		"steps":    steps,
		"tools":    req.Entry.Capabilities,
		"digest":   digest(req.Prompt)[:16],
	}
	return InvokeResult{
		Text:      out,
		Parts:     []a2a.Part{a2a.NewTextPart(out), a2a.NewDataPart(data)},
		Artifacts: []a2a.Artifact{artifact("skill.manual", 0, []a2a.Part{a2a.NewDataPart(data)}, "kind", string(registry.KindSkill), "provider", MockProviderName)},
	}, nil
}

// Health fetches the manual the way the connector will once it reads one. A
// skill is process knowledge, not a service: with no endpoint there is nothing
// to reach, so the answer is unknown rather than a healthy nobody checked.
func (c *skillConnector) Health(ctx context.Context, e *registry.Entry) (registry.Health, error) {
	url := endpointOf(e)
	if url == "" {
		return nothingMeasured, nil
	}
	return probeHTTP(ctx, http.MethodGet, url, nil, nil)
}
