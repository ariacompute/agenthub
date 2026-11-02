// Package adapter turns every kind of registered resource into one uniform
// invocation surface.
//
// A connector is real as far as its resource says where to reach it: model talks
// to an OpenAI compatible endpoint (model_openai.go), agent fetches an Agent Card
// and sends message/send (agent.go), and cli / mcp run inside a leased sandbox
// environment rather than in the host process (cli.go, mcp.go). skill, custom and
// speech are still deterministic mocks and each keeps its TODO(real) boundary.
// Every connector falls back to its mock when no sandbox or endpoint is wired, so
// an instance with nothing configured stays offline and assertable.
package adapter

import (
	"context"
	"net/http"
	"strings"
	"time"

	"agenthub/internal/a2a"
	"agenthub/internal/httputil"
	"agenthub/internal/registry"
	"agenthub/internal/sandbox"
)

// Machine readable error codes produced by adapters.
const (
	CodeUnknownKind         = httputil.Code("unknown_kind")
	CodeCommandNotAllowed   = httputil.Code("cli_command_not_allowed")
	CodeAdapterTimeout      = httputil.Code("adapter_timeout")
	CodeResourceUnreachable = httputil.Code("resource_unreachable")
)

// Options are the limits every adapter must honour, mock or real.
type Options struct {
	CLITimeout     time.Duration
	CLIAllowlist   []string
	MaxOutputBytes int
	// ModelRouterMode is the instance wide provider mode (AGENTHUB_MODEL_ROUTER_MODE)
	// model entries inherit when they do not name one themselves.
	ModelRouterMode string
	// AgentTimeout bounds one Agent Card fetch plus one message/send.
	AgentTimeout time.Duration
	// Sandbox is the execution environment control plane. When it is nil the
	// adapters keep their original offline behaviour, which is what keeps the
	// pre-sandbox unit tests meaningful: two behaviours, one signature.
	Sandbox *sandbox.Service
}

// DefaultAgentTimeout applies when no timeout is configured, so a card fetch can
// never hang a task indefinitely. It has to be generous: one A2A call covers the
// card fetch plus a whole agent turn, and an agent backed by a reasoning gateway
// was measured at ~40s. A shorter default turns a real answer into a timeout that
// looks exactly like an agent nobody deployed.
const DefaultAgentTimeout = 120 * time.Second

// InvokeRequest is one call to a registered resource.
type InvokeRequest struct {
	ResourceID string
	Entry      registry.Entry
	Prompt     string
	Parts      []a2a.Part
	Metadata   map[string]string
	Command    string
	Args       []string
	// OwnerID is who the execution is billed to and who may see the resulting
	// lease. Empty means anonymous, which deliberately never reuses a lease.
	OwnerID string
	// Scope is the lease reuse key: the router's ticket id when the call came
	// through a ticket, otherwise the conversation id.
	Scope string
}

// InvokeResult is the structured outcome of one call.
type InvokeResult struct {
	Text          string
	Parts         []a2a.Part
	Artifacts     []a2a.Artifact
	RequiresInput bool
	Prompt        string
}

// Connector is implemented once per resource kind.
type Connector interface {
	Kind() registry.Kind
	Invoke(ctx context.Context, req InvokeRequest) (InvokeResult, error)
	Health(ctx context.Context, e *registry.Entry) (registry.Health, error)
}

// For resolves the connector matching an entry's kind.
func For(e *registry.Entry, opts Options) (Connector, error) {
	if e == nil {
		return nil, &httputil.Error{Code: CodeUnknownKind}
	}
	switch e.Kind {
	case registry.KindModel:
		// A model resource carries its own provider chain: offline mock by
		// default, a real router with fallbacks when configured that way.
		mode := defaultMode(opts)
		return &modelConnector{router: newModelRouterFor(e, mode), defaultMode: mode}, nil
	case registry.KindAgent:
		// An entry that publishes an Agent Card is called for real; one that does
		// not has no endpoint to speak to and stays on the offline mock.
		timeout := opts.AgentTimeout
		if timeout <= 0 {
			timeout = DefaultAgentTimeout
		}
		cardURL, credentialEnv := "", ""
		var egress []string
		if e.KindConfig != nil && e.KindConfig.Agent != nil {
			cardURL = strings.TrimSpace(e.KindConfig.Agent.CardURL)
			credentialEnv = strings.TrimSpace(e.KindConfig.Agent.CredentialEnv)
			egress = e.KindConfig.Agent.EgressAllowlist
		}
		return &agentConnector{
			cardURL:       cardURL,
			client:        &http.Client{Timeout: timeout},
			timeout:       timeout,
			credentialEnv: credentialEnv,
			egress:        egress,
		}, nil
	case registry.KindMCP:
		return &mcpConnector{sandbox: opts.Sandbox}, nil
	case registry.KindSkill:
		return &skillConnector{}, nil
	case registry.KindCLI:
		return &cliConnector{opts: opts}, nil
	case registry.KindCustom:
		return &customConnector{}, nil
	default:
		return nil, &httputil.Error{Code: CodeUnknownKind}
	}
}
