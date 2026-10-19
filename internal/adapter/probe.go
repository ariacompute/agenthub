package adapter

import (
	"context"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"agenthub/internal/httputil"
	"agenthub/internal/registry"
)

// DefaultProbeTimeout bounds one health probe. A probe runs outside a task, but
// it still has to give up: an endpoint that never answers must not leave a
// request behind forever.
const DefaultProbeTimeout = 10 * time.Second

// nothingMeasured is what a probe answers when the resource offers no address
// the adapter could reach — a stdio MCP server, a CLI with no command, a skill
// with no endpoint. Claiming healthy or unhealthy there would be a guess, and a
// guess is exactly what health stopped being: the record says the only true
// thing, which is that nothing was measured.
const nothingMeasured = registry.HealthUnknown

// probeHTTP makes one real request and translates the answer into a health:
// 2xx is healthy; a refused credential is degraded, because the endpoint
// answered and simply will not serve us; any other answer is unhealthy; and a
// request that never got an answer at all is an error, so the caller records
// unhealthy instead of keeping a value the probe just disproved.
func probeHTTP(ctx context.Context, method, url string, headers map[string]string, body io.Reader) (registry.Health, error) {
	ctx, cancel := context.WithTimeout(ctx, DefaultProbeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return "", &httputil.Error{Code: CodeResourceUnreachable}
	}
	for name, value := range headers {
		if strings.TrimSpace(value) != "" {
			req.Header.Set(name, value)
		}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", &httputil.Error{Code: CodeResourceUnreachable}
	}
	defer func() { _ = resp.Body.Close() }()
	// The body is irrelevant to health, but it has to be drained for the
	// connection to be reusable — bounded, so a chatty endpoint cannot stall it.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64*1024))
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return registry.HealthHealthy, nil
	case resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden:
		return registry.HealthDegraded, nil
	default:
		return registry.HealthUnhealthy, nil
	}
}

// httpEndpoint returns the address when it is one the adapter could actually
// call, and an empty string otherwise, so a probe never dials a file path or a
// shell command by mistake.
func httpEndpoint(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if !isHTTP(trimmed) {
		return ""
	}
	return strings.TrimRight(trimmed, "/")
}

// endpointOf is the address the kind's own TODO(real) will resolve against:
// the entry's endpoint, when it is one the adapter could call.
func endpointOf(e *registry.Entry) string {
	if e == nil {
		return ""
	}
	return httpEndpoint(e.Endpoint)
}

// bearerHeaders carries the credential the way the real provider does: the
// entry names an environment variable and the value is read here, so a key
// never travels in a payload, a log or the database.
func bearerHeaders(envName string) map[string]string {
	headers := map[string]string{}
	if envName = strings.TrimSpace(envName); envName != "" {
		if key := strings.TrimSpace(os.Getenv(envName)); key != "" {
			headers["Authorization"] = "Bearer " + key
		}
	}
	return headers
}
