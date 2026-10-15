package adapter

import (
	"context"

	"agenthub/internal/a2a"
	"agenthub/internal/registry"
)

// mcpConnector is the "universal adapter" side of the platform: it only
// translates between A2A and MCP shapes and never touches business logic.
//
// TODO(real): speak MCP JSON-RPC over stdio or Streamable HTTP
// (tools/list + tools/call). Transport stays a descriptive field on the entry;
// the protocol logic is identical for both.
type mcpConnector struct{}

func (c *mcpConnector) Kind() registry.Kind { return registry.KindMCP }

func (c *mcpConnector) Invoke(ctx context.Context, req InvokeRequest) (InvokeResult, error) {
	if err := ctx.Err(); err != nil {
		return InvokeResult{}, err
	}
	transport := string(req.Entry.Transport)
	if transport == "" {
		transport = string(registry.TransportStreamableHTTP)
	}
	tools := []map[string]any{
		{"name": "list_resources", "description": "list_resources", "inputSchema": map[string]any{"type": "object"}},
		{"name": "call_action", "description": "call_action", "inputSchema": map[string]any{"type": "object"}},
		{"name": "read_document", "description": "read_document", "inputSchema": map[string]any{"type": "object"}},
	}
	selected := tools[pick(req.Prompt, len(tools))]
	out := "mcp=" + req.Entry.Name + " transport=" + transport + " tool=" + selected["name"].(string)
	data := map[string]any{
		"kind":      string(registry.KindMCP),
		"resource":  req.Entry.Name,
		"transport": transport,
		"tools":     tools,
		"call":      map[string]any{"tool": selected["name"], "arguments": map[string]any{"query": req.Prompt}, "result": map[string]any{"ok": true}},
	}
	return InvokeResult{
		Text:  out,
		Parts: []a2a.Part{a2a.NewTextPart(out), a2a.NewDataPart(data)},
		Artifacts: []a2a.Artifact{
			artifact("mcp.tools", 0, []a2a.Part{a2a.NewDataPart(map[string]any{"tools": tools})}, "kind", string(registry.KindMCP)),
			artifact("mcp.call", 1, []a2a.Part{a2a.NewDataPart(data["call"].(map[string]any))}, "transport", transport),
		},
	}, nil
}

func (c *mcpConnector) Health(ctx context.Context, e *registry.Entry) (registry.Health, error) {
	return mockHealth(e), nil
}
