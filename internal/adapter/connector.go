// Package adapter turns every kind of registered resource into one uniform
// invocation surface.
//
// The first release ships deterministic mock connectors only: no network, no
// subprocess, no filesystem. Each real implementation is marked with a
// TODO(real) boundary so the swap is a localised change.
package adapter

import (
	"context"
	"net/http"
	"strings"
	"time"

	"agenthub/internal/a2a"
	"agenthub/internal/httputil"
	"agenthub/internal/registry"
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
		cardURL := ""
		if e.KindConfig != nil && e.KindConfig.Agent != nil {
			cardURL = strings.TrimSpace(e.KindConfig.Agent.CardURL)
		}
		return &agentConnector{cardURL: cardURL, client: &http.Client{Timeout: timeout}, timeout: timeout}, nil
	case registry.KindMCP:
		return &mcpConnector{}, nil
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
