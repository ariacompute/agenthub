package adapter

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
)

// lookPath is a seam: it keeps the CLI connector free of a direct os/exec import
// so a test can reason about health without the host's PATH, and it is the only
// place in the adapter package that asks where a binary is. Actual execution
// happens in the sandbox package, never here.
func lookPath(name string) (string, error) { return exec.LookPath(name) }

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// mcpPost is one JSON-RPC request over Streamable HTTP. It is deliberately
// unaware of retries: an MCP call that fails is reported, not repeated, because
// repeating a tool call is a side effect, not a transport concern.
func mcpPost(ctx context.Context, client *http.Client, url string, payload string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("mcp: status %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 2<<20))
}
