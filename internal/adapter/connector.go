// Package adapter turns every kind of registered resource into one uniform
// invocation surface.
//
// The first release ships deterministic mock connectors only: no network, no
// subprocess, no filesystem. Each real implementation is marked with a
// TODO(real) boundary so the swap is a localised change.
package adapter

import (
	"context"
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
}

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
		return &modelConnector{router: newModelRouterFor(e)}, nil
	case registry.KindAgent:
		return &agentConnector{}, nil
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
