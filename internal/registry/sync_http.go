package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"agenthub/internal/httputil"
	"agenthub/internal/netutil"
)

// Upstream failure codes. They are deliberately generic: an upstream body is
// untrusted input and must never be echoed back to the console or the log.
const (
	CodeUpstreamUnreachable = httputil.Code("upstream_unreachable")
	CodeUpstreamMalformed   = httputil.Code("upstream_malformed")
	CodeUpstreamAuthMissing = httputil.Code("upstream_credentials_missing")
)

// Defaults for the real upstream client.
const (
	DefaultSyncTimeout = 15 * time.Second
	// DefaultSyncMaxBytes caps the response body. A registry that answers with
	// gigabytes must not take the process down with it.
	DefaultSyncMaxBytes = 8 << 20
)

// HTTPClient is the real upstream registry client.
//
// It speaks plain HTTPS GET and accepts the shapes the public registries
// actually return — a bare array, or an object wrapping the list under
// "servers", "items", "records" or "skills". Everything else about the run is
// identical to the fixture path, so a deployment can move between them without
// touching the pipeline.
type HTTPClient struct {
	HTTP     *http.Client
	MaxBytes int64
}

// NewHTTPClient builds the client with the conservative defaults. The transport
// is SSRF-guarded: it refuses redirects and resolves every address at dial time,
// blocking loopback/link-local/multicast. Private addresses are allowed because a
// registry source is operator-configured and explicitly trusted, but timeouts,
// a capped body and no redirect-following still apply.
func NewHTTPClient() *HTTPClient {
	return &HTTPClient{
		HTTP:     netutil.SafeClient(DefaultSyncTimeout, DefaultSyncMaxBytes, true),
		MaxBytes: DefaultSyncMaxBytes,
	}
}

// Fetch downloads one upstream catalogue and parses it into entries.
func (c *HTTPClient) Fetch(ctx context.Context, src RegistrySource) ([]*Entry, error) {
	if err := netutil.ValidateURL(strings.TrimSpace(src.URL), true); err != nil {
		return nil, &httputil.Error{Code: CodeUpstreamMalformed}
	}
	client := c.HTTP
	if client == nil {
		client = netutil.SafeClient(DefaultSyncTimeout, DefaultSyncMaxBytes, true)
	}
	maxBytes := c.MaxBytes
	if maxBytes <= 0 {
		maxBytes = DefaultSyncMaxBytes
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSpace(src.URL), nil)
	if err != nil {
		return nil, &httputil.Error{Code: CodeUpstreamMalformed}
	}
	req.Header.Set("Accept", "application/json")
	if strings.TrimSpace(src.AuthScheme) == AuthSchemeBearer {
		token := os.Getenv(strings.TrimSpace(src.AuthTokenEnv))
		if token == "" {
			// No credential, no request: failing the run honestly is better than
			// silently importing an empty catalogue.
			return nil, &httputil.Error{Code: CodeUpstreamAuthMissing}
		}
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, &httputil.Error{Code: CodeUpstreamUnreachable}
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &httputil.Error{Code: CodeUpstreamUnreachable}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes))
	if err != nil {
		return nil, &httputil.Error{Code: CodeUpstreamUnreachable}
	}
	records, err := decodeRecords(body)
	if err != nil {
		return nil, err
	}
	out := make([]*Entry, 0, len(records))
	for _, rec := range records {
		if e := entryFromRecord(src.Kind, rec); e != nil {
			out = append(out, e)
		}
	}
	return out, nil
}

// decodeRecords accepts the shapes a registry answers with. Unknown shapes are
// an error rather than an empty result: "the upstream changed format" must not
// look like "the upstream has nothing".
func decodeRecords(body []byte) ([]map[string]any, error) {
	var generic any
	if err := json.Unmarshal(body, &generic); err != nil {
		return nil, &httputil.Error{Code: CodeUpstreamMalformed}
	}
	switch v := generic.(type) {
	case []any:
		return recordsFromSlice(v), nil
	case map[string]any:
		for _, key := range []string{"servers", "items", "records", "skills", "data"} {
			if raw, ok := v[key]; ok {
				if list, ok := raw.([]any); ok {
					return recordsFromSlice(list), nil
				}
			}
		}
		// A single record is still a valid answer.
		return []map[string]any{v}, nil
	}
	return nil, &httputil.Error{Code: CodeUpstreamMalformed}
}

func recordsFromSlice(list []any) []map[string]any {
	out := make([]map[string]any, 0, len(list))
	for _, item := range list {
		if m, ok := item.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// entryFromRecord maps one upstream record onto an entry. A record without a
// name is dropped: there is nothing to dedup or display.
func entryFromRecord(kind string, rec map[string]any) *Entry {
	name := strings.TrimSpace(stringField(rec, "name", "title", "id"))
	if name == "" {
		return nil
	}
	e := &Entry{
		Name:        name,
		Description: strings.TrimSpace(stringField(rec, "description", "summary")),
		Version:     strings.TrimSpace(stringField(rec, "version", "recordVersion", "record_version")),
		Repository:  repositoryOf(rec),
		Endpoint:    strings.TrimSpace(stringField(rec, "url", "endpoint", "homepage")),
		Tags:        stringSlice(rec["tags"]),
	}
	if t := strings.TrimSpace(stringField(rec, "transport")); t != "" {
		if parsed, err := ParseTransport(t); err == nil {
			e.Transport = parsed
		}
	}
	if caps := stringSlice(rec["capabilities"]); len(caps) > 0 {
		e.Capabilities = caps
	} else if tools := toolNames(rec["tools"]); len(tools) > 0 {
		e.Capabilities = tools
	}
	if schemes := stringSlice(rec["auth_schemes"]); len(schemes) > 0 {
		e.AuthSchemes = schemes
	}
	switch kind {
	case SourceKindMCP:
		cfg := &MCPConfig{
			URL:           e.Endpoint,
			ToolAllowlist: toolNames(rec["tools"]),
		}
		if e.Transport == TransportStdio {
			cfg.Command = strings.TrimSpace(stringField(rec, "command"))
			cfg.Args = stringSlice(rec["args"])
		} else {
			cfg.Transport = string(TransportStreamableHTTP)
		}
		e.KindConfig = &KindConfig{MCP: cfg}
	case SourceKindSkill:
		e.KindConfig = &KindConfig{Skill: &SkillConfig{
			Version:    e.Version,
			Repository: e.Repository,
			Package:    strings.TrimSpace(stringField(rec, "package")),
			Tools:      toolNames(rec["tools"]),
			Docs:       strings.TrimSpace(stringField(rec, "docs", "documentation")),
		}}
	}
	return e
}

func stringField(rec map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := rec[k]; ok {
			if s, ok := v.(string); ok {
				return s
			}
		}
	}
	return ""
}

func stringSlice(v any) []string {
	list, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(list))
	for _, item := range list {
		switch t := item.(type) {
		case string:
			if s := strings.TrimSpace(t); s != "" {
				out = append(out, s)
			}
		case map[string]any:
			if s := strings.TrimSpace(stringField(t, "name")); s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}

// toolNames accepts both a list of names and a list of MCP tool objects.
func toolNames(v any) []string {
	list, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(list))
	for _, item := range list {
		switch t := item.(type) {
		case string:
			if s := strings.TrimSpace(t); s != "" {
				out = append(out, s)
			}
		case map[string]any:
			if s := strings.TrimSpace(stringField(t, "name")); s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}

// repositoryOf accepts either a plain string or the MCP Registry's nested
// {"url": ..., "source": ...} shape.
func repositoryOf(rec map[string]any) string {
	if s := strings.TrimSpace(stringField(rec, "repository")); s != "" {
		return s
	}
	if m, ok := rec["repository"].(map[string]any); ok {
		return strings.TrimSpace(stringField(m, "url"))
	}
	return ""
}

// UpstreamLabel names the client in logs without leaking the endpoint or any
// credential that went with it.
func UpstreamLabel(src RegistrySource) string {
	return fmt.Sprintf("%s source %s", src.Kind, src.ID)
}
