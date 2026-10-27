package channels

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"agenthub/internal/netutil"
)

// maxOutboundBody caps how much of a provider's response we echo back to the
// caller. The spec keeps it short (<=512B) so a verbose provider cannot flood
// the response body.
const maxOutboundBody = 512

// DeliverResult is the raw provider response after an outbound send: its HTTP
// status and a truncated copy of the body. We never buffer or retry.
type DeliverResult struct {
	Status int    `json:"status"`
	Raw    string `json:"raw"`
}

// Deliver POSTs body to the channel's configured target URL (config.target_url),
// using an SSRF-safe client that refuses private hosts, caps the response body
// and follows no redirects. It returns the provider's raw response verbatim and
// never retries or buffers. A channel with no target_url is a client error.
func Deliver(ctx context.Context, ch *Channel, body string) (*DeliverResult, error) {
	target, _ := ch.Config["target_url"].(string)
	if target == "" {
		return nil, fmt.Errorf("channel %s has no target_url", ch.ID)
	}
	// HTTPS only: provider webhooks carry secrets, and an http target would leak
	// them on the wire. The IP allow/deny decision happens later, at dial time.
	if err := netutil.ValidateURL(target, false); err != nil {
		return nil, err
	}
	client := netutil.SafeClient(15*time.Second, netutil.DefaultSafeMaxBytes, false)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewBufferString(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	buf, _ := io.ReadAll(io.LimitReader(resp.Body, int64(maxOutboundBody)+1))
	out := string(buf)
	if len(buf) > maxOutboundBody {
		out = string(buf[:maxOutboundBody]) + "…"
	}
	return &DeliverResult{Status: resp.StatusCode, Raw: out}, nil
}
