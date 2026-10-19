package adapter

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"agenthub/internal/a2a"
	"agenthub/internal/httputil"
	"agenthub/internal/registry"
)

// agentConnector talks to an autonomous agent.
//
// When the entry publishes an Agent Card (kind_config.agent.card_url) the call is
// a real one: the card is fetched and the prompt is sent over A2A JSON-RPC, so a
// task only completes if an agent actually answered. Without a card the connector
// has no endpoint to speak to and falls back to the deterministic mock — which is
// why every mock result carries provider=mock (see MockProviderName): otherwise a
// fabricated answer is indistinguishable from a real one and a task against an
// agent nobody deployed reads "completed" with nothing to contradict it.
type agentConnector struct {
	cardURL string
	client  *http.Client
	timeout time.Duration
}

func (c *agentConnector) Kind() registry.Kind { return registry.KindAgent }

func (c *agentConnector) Invoke(ctx context.Context, req InvokeRequest) (InvokeResult, error) {
	if err := ctx.Err(); err != nil {
		return InvokeResult{}, err
	}
	if strings.TrimSpace(c.cardURL) != "" {
		return c.invokeA2A(ctx, req)
	}
	return mockAgentInvoke(req), nil
}

// mockAgentInvoke emulates an autonomous agent: it plans, executes steps and can
// park the task in input-required when the request needs approval.
//
// TODO(real): forward to aria-agent-cloud /v1/agents/sessions/{id}/events/stream
// and translate the agent.* SSE frames into A2A artifacts.
func mockAgentInvoke(req InvokeRequest) InvokeResult {
	if requiresApproval(req.Prompt) {
		return InvokeResult{
			Text:          "state=input-required resource=" + req.Entry.Name + " reason=approval",
			RequiresInput: true,
			Prompt:        "approval_required:" + req.ResourceID,
		}
	}
	steps := []string{"recall", "plan", "execute", "memorize"}
	if pick(req.Prompt, 2) == 0 {
		steps = append(steps, "verify")
	}
	out := "agent=" + req.Entry.Name + " steps=" + itoa(len(steps)) + " digest=" + digest(req.Prompt)[:16]
	data := map[string]any{
		"kind":     string(registry.KindAgent),
		"resource": req.Entry.Name,
		"provider": MockProviderName,
		"steps":    steps,
		"goal":     req.Prompt,
		"digest":   digest(req.Prompt)[:16],
	}
	return InvokeResult{
		Text:      out,
		Parts:     []a2a.Part{a2a.NewTextPart(out), a2a.NewDataPart(data)},
		Artifacts: []a2a.Artifact{artifact("agent.plan", 0, []a2a.Part{a2a.NewDataPart(data)}, "kind", string(registry.KindAgent), "provider", MockProviderName)},
	}
}

// agentCard is the subset of the A2A Agent Card this client needs: the endpoint to
// post to, and the identity to stamp on the result.
type agentCard struct {
	Name            string `json:"name"`
	Description     string `json:"description"`
	URL             string `json:"url"`
	Version         string `json:"version"`
	ProtocolVersion string `json:"protocolVersion"`
}

func (c *agentConnector) fetchCard(ctx context.Context) (*agentCard, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.cardURL, nil)
	if err != nil {
		return nil, &httputil.Error{Code: CodeResourceUnreachable}
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, &httputil.Error{Code: CodeResourceUnreachable}
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &httputil.Error{Code: CodeResourceUnreachable}
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, DefaultModelMaxBytes))
	if err != nil {
		return nil, &httputil.Error{Code: CodeResourceUnreachable}
	}
	var card agentCard
	if err := json.Unmarshal(raw, &card); err != nil {
		return nil, &httputil.Error{Code: CodeProviderMalformed}
	}
	return &card, nil
}

// endpointFor resolves the JSON-RPC endpoint. The card's own url wins; when it is
// absent the card was served from the well-known path, so the same origin is where
// the A2A server listens.
func endpointFor(card *agentCard, cardURL string) string {
	if card != nil && isHTTP(card.URL) {
		return strings.TrimSuffix(card.URL, "/")
	}
	if i := strings.Index(cardURL, "/.well-known/"); i > 0 {
		return strings.TrimSuffix(cardURL[:i], "/")
	}
	return strings.TrimSuffix(cardURL, "/")
}

func (c *agentConnector) invokeA2A(ctx context.Context, req InvokeRequest) (InvokeResult, error) {
	ctx, cancel := boundedContext(ctx, c.timeout)
	defer cancel()

	card, err := c.fetchCard(ctx)
	if err != nil {
		return InvokeResult{}, err
	}
	text, state, err := c.sendMessage(ctx, endpointFor(card, c.cardURL), req.Prompt)
	if err != nil {
		return InvokeResult{}, err
	}

	// An agent that parked in input-required answered; the task must wait for a
	// human, not be reported as finished work.
	if state == "input-required" {
		return InvokeResult{
			Text:          text,
			RequiresInput: true,
			Prompt:        "approval_required:" + req.ResourceID,
		}, nil
	}

	provider := "a2a"
	if card.Name != "" {
		provider = "a2a:" + card.Name
	}
	out := text
	if out == "" {
		out = "agent=" + req.Entry.Name + " state=" + state + " (no text part returned)"
	}
	data := map[string]any{
		"kind":     string(registry.KindAgent),
		"resource": req.Entry.Name,
		"provider": provider,
		"card":     c.cardURL,
		"state":    state,
	}
	return InvokeResult{
		Text:      out,
		Parts:     []a2a.Part{a2a.NewTextPart(out), a2a.NewDataPart(data)},
		Artifacts: []a2a.Artifact{artifact("agent.a2a", 0, []a2a.Part{a2a.NewDataPart(data)}, "kind", string(registry.KindAgent), "provider", provider, "state", state)},
	}, nil
}

func (c *agentConnector) sendMessage(ctx context.Context, endpoint, prompt string) (string, string, error) {
	// The message is written in the 1.0 shape on purpose. agenthub's own wire
	// format names the discriminator "type", and a 1.0 server validates "kind"
	// as a required field — send only what this repo speaks and every remote
	// agent answers "missing messageId" while the local tests pass, which is
	// exactly the kind of false negative this client exists to avoid.
	message := outboundMessage{
		Kind:      "message",
		Role:      a2a.RoleUser,
		MessageID: newMessageID(),
		Parts:     []outboundPart{textPart(prompt)},
	}
	body, err := json.Marshal(map[string]any{
		"jsonrpc": a2a.JSONRPCVersion,
		"id":      "agenthub-1",
		"method":  a2a.MethodMessageSend,
		"params":  map[string]any{"message": message},
	})
	if err != nil {
		return "", "", &httputil.Error{Code: CodeProviderMalformed}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(body)))
	if err != nil {
		return "", "", &httputil.Error{Code: CodeResourceUnreachable}
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return "", "", &httputil.Error{Code: CodeResourceUnreachable}
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, DefaultModelMaxBytes))
	if err != nil {
		return "", "", &httputil.Error{Code: CodeResourceUnreachable}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", "", &httputil.Error{Code: CodeResourceUnreachable}
	}

	var rpc struct {
		Result json.RawMessage `json:"result"`
		Error  *a2a.Error      `json:"error"`
	}
	if err := json.Unmarshal(raw, &rpc); err != nil {
		return "", "", &httputil.Error{Code: CodeProviderMalformed}
	}
	if rpc.Error != nil {
		return "", "", &httputil.Error{Code: CodeResourceUnreachable}
	}
	return textFromSendResult(rpc.Result)
}

// sendPart is a tolerant read of one A2A part. The specification names the
// discriminator "kind"; agenthub's own wire format uses "type". Accepting both is
// what lets this client talk to somebody else's server instead of silently
// dropping an answer it cannot classify.
type sendPart struct {
	Kind     string         `json:"kind"`
	Type     string         `json:"type"`
	Text     string         `json:"text"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

// isAnswer reports whether a part carries the agent's answer. ADK publishes the
// model's reasoning as a part flagged adk_thought; echoing that back as the
// reply would present thinking as output.
func (p sendPart) isAnswer() bool {
	if p.Metadata != nil {
		if thought, ok := p.Metadata["adk_thought"]; ok && thought == true {
			return false
		}
	}
	return p.isText()
}

func (p sendPart) isText() bool {
	if strings.TrimSpace(p.Type) != "" {
		return p.Type == a2a.PartTypeText
	}
	return p.Kind == a2a.PartTypeText
}

// outboundPart is a part written the way a 1.0 server expects. Both
// discriminators are sent — "kind" for the specification, "type" for agenthub's
// own format — because the remote end may be either generation and guessing
// wrong turns a real answer into "(no text part returned)".
type outboundPart struct {
	Kind string         `json:"kind"`
	Type string         `json:"type,omitempty"`
	Text string         `json:"text,omitempty"`
	Data map[string]any `json:"data,omitempty"`
}

// outboundMessage is the message payload of one message/send call.
type outboundMessage struct {
	Kind      string         `json:"kind"`
	Role      string         `json:"role"`
	MessageID string         `json:"messageId"`
	Parts     []outboundPart `json:"parts"`
}

func textPart(text string) outboundPart {
	return outboundPart{Kind: a2a.PartTypeText, Type: a2a.PartTypeText, Text: text}
}

// newMessageID mints the correlation id A2A 1.0 requires on every message.
func newMessageID() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return fmt.Sprintf("agenthub-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf[:])
}

// textFromSendResult reads the agent's answer out of a message/send result, which
// is either a Task (artifacts and/or a status message) or a bare Message.
func textFromSendResult(raw json.RawMessage) (string, string, error) {
	var result struct {
		Status *struct {
			State   string `json:"state"`
			Message struct {
				Parts []sendPart `json:"parts"`
			} `json:"message"`
		} `json:"status"`
		Artifacts []struct {
			Parts []sendPart `json:"parts"`
		} `json:"artifacts"`
		Parts []sendPart `json:"parts"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return "", "", &httputil.Error{Code: CodeProviderMalformed}
	}
	state := "completed"
	if result.Status != nil && result.Status.State != "" {
		state = result.Status.State
	}
	var b strings.Builder
	appendParts(&b, result.Parts)
	for _, art := range result.Artifacts {
		appendParts(&b, art.Parts)
	}
	if result.Status != nil {
		appendParts(&b, result.Status.Message.Parts)
	}
	return b.String(), state, nil
}

func appendParts(b *strings.Builder, parts []sendPart) {
	for _, p := range parts {
		if p.isAnswer() && strings.TrimSpace(p.Text) != "" {
			b.WriteString(p.Text)
		}
	}
}

// Health fetches the Agent Card the entry publishes. A card that cannot be
// fetched means the agent is not there, however healthy the record claims to be.
// An entry with no card is a non-A2A control: there is no A2A endpoint to call,
// so the answer is unknown rather than a value the connector made up from the
// endpoint column.
func (c *agentConnector) Health(ctx context.Context, e *registry.Entry) (registry.Health, error) {
	cardURL := httpEndpoint(c.cardURL)
	if cardURL == "" {
		return nothingMeasured, nil
	}
	health, err := probeHTTP(ctx, http.MethodGet, cardURL, nil, nil)
	if err != nil {
		return "", err
	}
	if health != registry.HealthHealthy {
		return health, nil
	}
	// A 200 that is not a card is an agent that answered but cannot be used, so
	// it lands on degraded instead of being called healthy on a status code.
	ctx, cancel := boundedContext(ctx, c.timeout)
	defer cancel()
	if _, err := c.fetchCard(ctx); err != nil {
		return registry.HealthDegraded, nil
	}
	return registry.HealthHealthy, nil
}

// isHTTP is the adapter-local half of registry's URL check: the Agent Card's url
// comes from a remote party and is only trusted enough to be posted to when it
// names a scheme this client speaks.
func isHTTP(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
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
