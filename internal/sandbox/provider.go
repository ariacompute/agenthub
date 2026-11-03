package sandbox

import (
	"context"
	"time"
)

// Clock is the time source, injectable so tests can drive lease expiry without
// sleeping. Same shape as router.Clock, for the same reason.
type Clock func() time.Time

// Session is an attached, interactive environment: the MCP stdio transport needs
// to keep a process alive across several request/response pairs, which one-shot
// Exec cannot express.
//
// RoundTrip takes one complete JSON-RPC frame and returns one complete reply.
// Implementations are line delimited on the wire (MCP stdio framing) and must
// honour the context deadline by killing the process group, not by leaking it.
type Session interface {
	RoundTrip(ctx context.Context, payload []byte) ([]byte, error)
	Close() error
}

// Provider is the single seam between the control plane and a real execution
// backend. Everything that differs between docker, bubblewrap, nsjail and a
// remote node lives behind this interface, and nothing above it branches on the
// provider name.
type Provider interface {
	// Name is the catalogue key, e.g. "docker" or a declarative provider's name.
	Name() string
	// Describe is static: it must not touch the filesystem, the network or a
	// subprocess, because the catalogue is rendered on every console load.
	Describe(ctx context.Context) (Descriptor, error)
	// Probe answers "can this run here". It may execute one short command and is
	// therefore cached by the Registry; a probe never returns an error for an
	// unavailable backend, it returns Available=false with a reason.
	Probe(ctx context.Context) (Availability, error)
	// Create allocates one environment. It is called once per lease.
	Create(ctx context.Context, spec Spec) (Handle, error)
	// Exec runs one command inside an existing environment.
	Exec(ctx context.Context, h Handle, req ExecRequest) (ExecResult, error)
	// Attach opens an interactive session. Providers that cannot keep a process
	// alive return CodeUnsupported rather than faking it.
	Attach(ctx context.Context, h Handle, req ExecRequest) (Session, error)
	// Destroy releases the environment. It must be idempotent: a lease that the
	// provider already reaped is not an error.
	Destroy(ctx context.Context, h Handle) error
	// Status reports the provider's own view of the environment.
	Status(ctx context.Context, h Handle) (string, error)
}

// providerFunc is a convenience for tests that need a one-method provider.
type providerFunc struct {
	name string
	desc Descriptor
}

func (p providerFunc) Name() string { return p.name }

// Describe fills in a name and, when a test left it empty, the whole network
// vocabulary.
//
// The default is deliberate: this stub is not a boundary, so declaring nothing
// would make every case that builds a lease fail the capability gate in
// Manager.Acquire and prove nothing about the sink under test. A test that wants
// to exercise the gate sets Network itself.
func (p providerFunc) Describe(context.Context) (Descriptor, error) {
	desc := p.desc
	if desc.Name == "" {
		desc.Name = p.name
	}
	if desc.Network == nil {
		desc.Network = []string{NetworkNone, NetworkBridge, NetworkPlatform}
	}
	return desc, nil
}

func (p providerFunc) Probe(context.Context) (Availability, error) {
	return Availability{Available: true}, nil
}

func (p providerFunc) Create(_ context.Context, spec Spec) (Handle, error) {
	return Handle{ID: spec.LeaseID, Provider: p.name, Ref: spec.LeaseID}, nil
}

func (p providerFunc) Exec(context.Context, Handle, ExecRequest) (ExecResult, error) {
	return ExecResult{Provider: p.name, Receipt: "exit"}, nil
}

func (p providerFunc) Attach(context.Context, Handle, ExecRequest) (Session, error) {
	return nil, Error(CodeUnsupported)
}

func (p providerFunc) Destroy(context.Context, Handle) error { return nil }

func (p providerFunc) Status(context.Context, Handle) (string, error) { return StateReady, nil }
