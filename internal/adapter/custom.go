package adapter

import (
	"context"
	"net/http"

	"agenthub/internal/a2a"
	"agenthub/internal/registry"
)

// customConnector is the escape hatch kind: a record that is not a model, an
// agent, an MCP server, a skill or a CLI carries its own JSON schema and
// agenthub does not presume to know what to do with it.
//
// The mock therefore does the only honest thing available offline: it echoes the
// record back with a digest, so a caller can see the schema was reachable.
// TODO(real): resolve the declared schema against the resource endpoint and
// dispatch to whatever the schema describes.
type customConnector struct{}

func (c *customConnector) Kind() registry.Kind { return registry.KindCustom }

func (c *customConnector) Invoke(ctx context.Context, req InvokeRequest) (InvokeResult, error) {
	if err := ctx.Err(); err != nil {
		return InvokeResult{}, err
	}
	schema := ""
	if req.Entry.KindConfig != nil && req.Entry.KindConfig.Custom != nil {
		schema = req.Entry.KindConfig.Custom.Schema
	}
	if schema == "" {
		schema = "{}"
	}
	out := "custom=" + req.Entry.Name + " bytes=" + itoa(len(schema)) + " digest=" + digest(req.Prompt)[:16]
	data := map[string]any{
		"kind":     string(registry.KindCustom),
		"resource": req.Entry.Name,
		"provider": MockProviderName,
		"schema":   schema,
		"digest":   digest(req.Prompt)[:16],
	}
	return InvokeResult{
		Text:      out,
		Parts:     []a2a.Part{a2a.NewTextPart(out), a2a.NewDataPart(data)},
		Artifacts: []a2a.Artifact{artifact("custom.schema", 0, []a2a.Part{a2a.NewDataPart(data)}, "kind", string(registry.KindCustom), "provider", MockProviderName)},
	}, nil
}

// Health asks the endpoint the declared schema is resolved against. A record
// with no endpoint gives the probe nothing to call, so it stays unknown.
func (c *customConnector) Health(ctx context.Context, e *registry.Entry) (registry.Health, error) {
	url := endpointOf(e)
	if url == "" {
		return nothingMeasured, nil
	}
	return probeHTTP(ctx, http.MethodGet, url, nil, nil)
}
