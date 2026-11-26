package adapter

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"agenthub/internal/a2a"
	"agenthub/internal/httputil"
	"agenthub/internal/netutil"
	"agenthub/internal/registry"
)

// customDispatchKey is the schema extension a custom resource uses to say how it
// wants to be called. Keeping it inside the schema means the record stays
// self-describing and the registry contract does not grow a second place to
// declare an endpoint.
const customDispatchKey = "x-agenthub-dispatch"

// customMaxResponseExcerpt bounds the part of an upstream body that is surfaced
// in the artifact. The digest always covers the whole body, so truncation is
// visible rather than silently lossy.
const customMaxResponseExcerpt = 4 << 10

// customConnector is the escape hatch kind: a record that is not a model, an
// agent, an MCP server, a skill or a CLI carries its own JSON schema and
// agenthub does not presume to know what to do with it.
//
// With a dispatch descriptor and an endpoint the call is real: the declared
// method and relative path are validated, then issued through the SSRF-safe
// client. Without one the connector does the only honest thing available
// offline: it echoes the record back with a digest, so a caller can see the
// schema was reachable.
type customConnector struct {
	client *http.Client
}

func newCustomConnector() *customConnector {
	return &customConnector{
		client: netutil.SafeClientWithAllowlist(DefaultModelTimeout, DefaultModelMaxBytes, nil),
	}
}

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
	dispatch, ok, err := dispatchOf(schema)
	if err != nil {
		return InvokeResult{}, err
	}
	base := endpointOf(&req.Entry)
	if !ok || base == "" {
		return customEcho(req, schema), nil
	}
	return c.dispatch(ctx, req, dispatch, base)
}

// dispatchOf reads the optional invocation descriptor out of a declared schema.
// A schema without the extension is not an error: it is a record that never
// asked to be called, so the echo mock stays the answer.
func dispatchOf(schema string) (customDispatch, bool, error) {
	if strings.TrimSpace(schema) == "" {
		return customDispatch{}, false, nil
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal([]byte(schema), &doc); err != nil {
		// The registry validated the JSON at registration; a schema that no
		// longer parses is simply not a descriptor this connector understands.
		return customDispatch{}, false, nil
	}
	raw, ok := doc[customDispatchKey]
	if !ok {
		return customDispatch{}, false, nil
	}
	var d customDispatch
	if err := json.Unmarshal(raw, &d); err != nil {
		return customDispatch{}, false, &httputil.Error{Code: CodeProviderMalformed}
	}
	method := strings.ToUpper(strings.TrimSpace(d.Method))
	if method == "" {
		method = http.MethodPost
	}
	if method != http.MethodGet && method != http.MethodPost {
		return customDispatch{}, false, &httputil.Error{Code: CodeProviderMalformed}
	}
	path := strings.TrimSpace(d.Path)
	if !safeRelativePath(path) {
		return customDispatch{}, false, &httputil.Error{Code: CodeProviderMalformed}
	}
	return customDispatch{Method: method, Path: path}, true, nil
}

type customDispatch struct {
	Method string `json:"method"`
	Path   string `json:"path"`
}

// safeRelativePath keeps a schema from rewriting the URL: an absolute path,
// a traversal segment or any control character is refused before a request is
// built, so the call cannot leave the endpoint the record declared.
func safeRelativePath(p string) bool {
	if p == "" {
		return true
	}
	if strings.HasPrefix(p, "/") || strings.HasPrefix(p, "\\") {
		return false
	}
	if strings.Contains(p, "..") || strings.Contains(p, "\\") || strings.Contains(p, "//") {
		return false
	}
	for _, r := range p {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func (c *customConnector) dispatch(ctx context.Context, req InvokeRequest, d customDispatch, base string) (InvokeResult, error) {
	target := base
	if d.Path != "" {
		target = strings.TrimRight(base, "/") + "/" + d.Path
	}
	if err := netutil.ValidateURL(target, true); err != nil {
		return InvokeResult{}, &httputil.Error{Code: CodeProviderMalformed}
	}

	var body io.Reader
	if d.Method == http.MethodPost {
		payload, _ := json.Marshal(map[string]any{
			"input":    req.Prompt,
			"resource": req.Entry.Name,
			"metadata": req.Metadata,
		})
		body = bytes.NewReader(payload)
	}
	// ValidateURL above already refused everything NewRequest would reject
	// (unparsable target, unsupported scheme), so there is no second error to
	// branch on here.
	httpReq, _ := http.NewRequestWithContext(ctx, d.Method, target, body)
	if d.Method == http.MethodPost {
		httpReq.Header.Set("Content-Type", "application/json")
	}
	httpReq.Header.Set("Accept", "application/json")

	client := c.client
	if client == nil {
		client = newCustomConnector().client
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return InvokeResult{}, &httputil.Error{Code: CodeResourceUnreachable}
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return InvokeResult{}, &httputil.Error{Code: CodeProviderUnauthorized}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return InvokeResult{}, &httputil.Error{Code: CodeResourceUnreachable}
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, DefaultModelMaxBytes))
	if err != nil {
		return InvokeResult{}, &httputil.Error{Code: CodeResourceUnreachable}
	}

	excerpt, truncated := truncate(string(raw), customMaxResponseExcerpt)
	digestResponse := digest(string(raw))[:16]
	out := "custom=" + req.Entry.Name + " status=" + itoa(resp.StatusCode) +
		" bytes=" + itoa(len(raw)) + " digest=" + digestResponse
	data := map[string]any{
		"kind":            string(registry.KindCustom),
		"resource":        req.Entry.Name,
		"provider":        "custom-http",
		"method":          d.Method,
		"path":            d.Path,
		"status":          resp.StatusCode,
		"bytes":           len(raw),
		"digest":          digest(req.Prompt)[:16],
		"response_digest": digestResponse,
		"response":        excerpt,
		"truncated":       truncated,
	}
	return InvokeResult{
		Text:      out,
		Parts:     []a2a.Part{a2a.NewTextPart(out), a2a.NewDataPart(data)},
		Artifacts: []a2a.Artifact{artifact("custom.result", 0, []a2a.Part{a2a.NewDataPart(data)}, "kind", string(registry.KindCustom), "provider", "custom-http")},
	}, nil
}

// customEcho is the offline answer: it proves the schema was reachable without
// pretending an endpoint was called.
func customEcho(req InvokeRequest, schema string) InvokeResult {
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
	}
}

// Health asks the endpoint the declared schema is resolved against. A record
// with no endpoint gives the probe nothing to call, so it stays unknown.
func (c *customConnector) Health(ctx context.Context, e *registry.Entry) (registry.Health, error) {
	url := endpointOf(e)
	if url == "" {
		return nothingMeasured, nil
	}
	return probeHTTP(ctx, http.DefaultClient, http.MethodGet, url, nil, nil)
}
