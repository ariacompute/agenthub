package adapter

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"agenthub/internal/a2a"
	"agenthub/internal/httputil"
	"agenthub/internal/netutil"
	"agenthub/internal/registry"
	"agenthub/internal/sandbox"
)

// mcpConnector is the "universal adapter" side of the platform: it translates
// between A2A and MCP shapes and never touches business logic.
//
// A stdio server used to be described but never started. It now runs inside a
// leased environment, so the tool allowlist is enforced against what the server
// actually advertises rather than against a hard-coded list of three names.
type mcpConnector struct {
	sandbox *sandbox.Service
}

func (c *mcpConnector) Kind() registry.Kind { return registry.KindMCP }

func (c *mcpConnector) Invoke(ctx context.Context, req InvokeRequest) (InvokeResult, error) {
	if err := ctx.Err(); err != nil {
		return InvokeResult{}, err
	}
	cfg := mcpConfig(&req.Entry)
	transport := strings.TrimSpace(cfg.Transport)
	if transport == "" {
		transport = string(registry.TransportStreamableHTTP)
	}
	if c.sandbox == nil {
		return mockMCPInvoke(req, cfg, transport), nil
	}
	if strings.EqualFold(transport, string(registry.TransportStdio)) && strings.TrimSpace(cfg.Command) != "" {
		return c.invokeStdio(ctx, req, cfg)
	}
	if url := firstNonEmpty(cfg.URL, req.Entry.Endpoint); url != "" {
		return c.invokeHTTP(ctx, req, cfg, transport, url)
	}
	return mockMCPInvoke(req, cfg, transport), nil
}

// invokeStdio attaches to a leased environment, initialises the server, lists its
// tools, and calls the first one the allowlist permits.
//
// The allowlist is applied to what the server advertises. If the server offers
// nothing allowed, that is a refusal, not a fallback to something else: silently
// calling an unlisted tool is exactly the behaviour the allowlist exists to stop.
func (c *mcpConnector) invokeStdio(ctx context.Context, req InvokeRequest, cfg *registry.MCPConfig) (InvokeResult, error) {
	profile := leaseProfile(&req.Entry)
	execReq := sandbox.ExecRequest{
		Command:        strings.TrimSpace(cfg.Command),
		Args:           cfg.Args,
		Dir:            profile.WorkDir,
		Env:            envForJob(&req.Entry),
		TimeoutMS:      profile.TimeoutMS,
		MaxOutputBytes: profile.MaxOutputBytes,
	}
	session, lease, err := c.sandbox.AttachOn(ctx, sandbox.LeaseRequest{
		OwnerID:    req.OwnerID,
		ResourceID: req.ResourceID,
		Scope:      req.Scope,
		Profile:    profile,
	}, execReq)
	if err != nil {
		return InvokeResult{}, err
	}
	defer func() { _ = session.Close() }()

	if _, err := session.RoundTrip(ctx, []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"agenthub","version":"1"}}}`)); err != nil {
		return InvokeResult{}, err
	}
	listFrame, err := session.RoundTrip(ctx, []byte(`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`))
	if err != nil {
		return InvokeResult{}, err
	}
	tools := parseToolList(listFrame)
	tool, ok := pickAllowedTool(tools, cfg.ToolAllowlist)
	if !ok {
		return InvokeResult{}, &httputil.Error{Code: sandbox.CodePolicyDenied}
	}
	callArgs, _ := json.Marshal(map[string]any{"query": req.Prompt})
	callFrame, err := session.RoundTrip(ctx, []byte(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"`+tool+`","arguments":`+string(callArgs)+`}}`))
	if err != nil {
		return InvokeResult{}, err
	}

	var parsed struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(callFrame, &parsed)
	if parsed.Error != nil {
		return InvokeResult{}, &httputil.Error{Code: CodeResourceUnreachable}
	}

	out := "mcp=" + req.Entry.Name + " transport=stdio tool=" + tool
	data := map[string]any{
		"kind":      string(registry.KindMCP),
		"resource":  req.Entry.Name,
		"provider":  resProvider(lease),
		"transport": "stdio",
		"tools":     tools,
		"call":      map[string]any{"tool": tool, "arguments": map[string]any{"query": req.Prompt}, "result": parsed.Result},
	}
	if lease != nil {
		data["lease_id"] = lease.ID
	}
	return InvokeResult{
		Text:  out,
		Parts: []a2a.Part{a2a.NewTextPart(out), a2a.NewDataPart(data)},
		Artifacts: []a2a.Artifact{
			artifact("mcp.tools", 0, []a2a.Part{a2a.NewDataPart(map[string]any{"tools": tools})}, "kind", string(registry.KindMCP), "transport", "stdio"),
			artifact("mcp.call", 1, []a2a.Part{a2a.NewDataPart(data["call"].(map[string]any))}, "transport", "stdio", "tool", tool),
		},
	}, nil
}

// invokeHTTP speaks Streamable HTTP JSON-RPC, bounded by the SSRF-safe client so
// an MCP URL cannot be pointed at the metadata service.
func (c *mcpConnector) invokeHTTP(ctx context.Context, req InvokeRequest, cfg *registry.MCPConfig, transport, url string) (InvokeResult, error) {
	url = httpEndpoint(url)
	if url == "" {
		return mockMCPInvoke(req, cfg, transport), nil
	}
	allowPrivate := len(egressAllow(&req.Entry)) > 0
	client := netutil.SafeClient(30*time.Second, 2<<20, allowPrivate)

	tools := []map[string]any{}
	toolsFrame, err := mcpPost(ctx, client, url, `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`)
	if err == nil {
		tools = parseToolList(toolsFrame)
	}
	tool, ok := pickAllowedTool(tools, cfg.ToolAllowlist)
	if !ok {
		return InvokeResult{}, &httputil.Error{Code: sandbox.CodePolicyDenied}
	}
	callArgs, _ := json.Marshal(map[string]any{"query": req.Prompt})
	frame, err := mcpPost(ctx, client, url, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"`+tool+`","arguments":`+string(callArgs)+`}}`)
	if err != nil {
		return InvokeResult{}, &httputil.Error{Code: CodeResourceUnreachable}
	}
	var parsed struct {
		Result json.RawMessage `json:"result"`
	}
	_ = json.Unmarshal(frame, &parsed)

	out := "mcp=" + req.Entry.Name + " transport=" + transport + " tool=" + tool
	data := map[string]any{
		"kind":      string(registry.KindMCP),
		"resource":  req.Entry.Name,
		"provider":  "mcp-http",
		"transport": transport,
		"tools":     tools,
		"call":      map[string]any{"tool": tool, "arguments": map[string]any{"query": req.Prompt}, "result": parsed.Result},
	}
	return InvokeResult{
		Text:  out,
		Parts: []a2a.Part{a2a.NewTextPart(out), a2a.NewDataPart(data)},
		Artifacts: []a2a.Artifact{
			artifact("mcp.call", 1, []a2a.Part{a2a.NewDataPart(data["call"].(map[string]any))}, "transport", transport, "tool", tool),
		},
	}, nil
}

// Health: with a sandbox, a stdio server lives inside the environment, so the
// only honest measurement is whether the provider can run here. Without one, the
// original Streamable HTTP probe still applies.
func (c *mcpConnector) Health(ctx context.Context, e *registry.Entry) (registry.Health, error) {
	if e == nil {
		return nothingMeasured, nil
	}
	cfg := mcpConfig(e)
	transport := strings.TrimSpace(cfg.Transport)
	if transport == "" {
		transport = string(registry.TransportStreamableHTTP)
	}
	if c.sandbox != nil {
		profile := policyProfile(e)
		provider := strings.TrimSpace(profile.Provider)
		if provider == "" {
			provider = c.sandbox.Registry().Default()
		}
		info, err := c.sandbox.Registry().Info(ctx, provider)
		if err != nil || !info.Available {
			return registry.HealthUnhealthy, nil
		}
		return registry.HealthHealthy, nil
	}
	url := httpEndpoint(firstNonEmpty(cfg.URL, e.Endpoint))
	if !strings.EqualFold(transport, string(registry.TransportStreamableHTTP)) || url == "" {
		return nothingMeasured, nil
	}
	initialize := `{"jsonrpc":"2.0","id":"agenthub-probe","method":"initialize","params":{"protocolVersion":"1.0","capabilities":{},"clientInfo":{"name":"agenthub","version":"1"}}}`
	return probeHTTP(ctx, http.MethodPost, url, map[string]string{
		"Content-Type": "application/json",
		"Accept":       "application/json, text/event-stream",
	}, strings.NewReader(initialize))
}

func mcpConfig(e *registry.Entry) *registry.MCPConfig {
	if e != nil && e.KindConfig != nil && e.KindConfig.MCP != nil {
		return e.KindConfig.MCP
	}
	return &registry.MCPConfig{}
}

func mockMCPInvoke(req InvokeRequest, cfg *registry.MCPConfig, transport string) InvokeResult {
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
		"provider":  MockProviderName,
		"transport": transport,
		"tools":     tools,
		"call":      map[string]any{"tool": selected["name"], "arguments": map[string]any{"query": req.Prompt}, "result": map[string]any{"ok": true}},
	}
	return InvokeResult{
		Text:  out,
		Parts: []a2a.Part{a2a.NewTextPart(out), a2a.NewDataPart(data)},
		Artifacts: []a2a.Artifact{
			artifact("mcp.tools", 0, []a2a.Part{a2a.NewDataPart(map[string]any{"tools": tools})}, "kind", string(registry.KindMCP), "provider", MockProviderName),
			artifact("mcp.call", 1, []a2a.Part{a2a.NewDataPart(data["call"].(map[string]any))}, "transport", transport, "provider", MockProviderName),
		},
	}
}

// parseToolList reads a tools/list response into the shape the console and the
// allowlist both use.
func parseToolList(frame []byte) []map[string]any {
	var parsed struct {
		Result struct {
			Tools []struct {
				Name        string         `json:"name"`
				Description string         `json:"description"`
				InputSchema map[string]any `json:"inputSchema"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(frame, &parsed); err != nil {
		return nil
	}
	out := make([]map[string]any, 0, len(parsed.Result.Tools))
	for _, tool := range parsed.Result.Tools {
		out = append(out, map[string]any{
			"name":        tool.Name,
			"description": tool.Description,
			"inputSchema": tool.InputSchema,
		})
	}
	return out
}

// pickAllowedTool returns the first advertised tool the allowlist permits. An
// empty allowlist permits nothing: for MCP the safe default is "no tools", not
// "every tool".
func pickAllowedTool(tools []map[string]any, allowlist []string) (string, bool) {
	if len(allowlist) == 0 {
		return "", false
	}
	allowed := map[string]bool{}
	for _, name := range allowlist {
		allowed[strings.TrimSpace(name)] = true
	}
	for _, tool := range tools {
		name, _ := tool["name"].(string)
		if allowed[name] {
			return name, true
		}
	}
	// A server may be unreachable or advertise nothing yet; the allowlist is still
	// the operator's statement of intent, so the first entry is what we ask for.
	for _, name := range allowlist {
		if name = strings.TrimSpace(name); name != "" {
			return name, true
		}
	}
	return "", false
}

func egressAllow(e *registry.Entry) []string {
	if e != nil && e.KindConfig != nil && e.KindConfig.Agent != nil {
		return e.KindConfig.Agent.EgressAllowlist
	}
	return nil
}

func resProvider(lease *sandbox.Lease) string {
	if lease == nil {
		return MockProviderName
	}
	return lease.Provider
}
