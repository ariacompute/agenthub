package sandbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"
)

// MockProvider is the deterministic offline backend: it starts no process, opens
// no socket, and returns the same receipt for the same input.
//
// It exists for three reasons, and it is honest about all of them: the unit
// tests need a provider that never touches the host, `make web-dev` needs a
// working console without docker, and a demo must be able to show the flow
// without pretending something ran. Every receipt says provider=mock.
type MockProvider struct {
	now Clock
}

// NewMockProvider builds the offline provider.
func NewMockProvider() *MockProvider {
	return &MockProvider{now: time.Now}
}

// Name is the catalogue key.
func (m *MockProvider) Name() string { return ProviderMock }

// Describe reports the truth: no layer, no isolation.
func (m *MockProvider) Describe(context.Context) (Descriptor, error) {
	return Descriptor{
		Name:      ProviderMock,
		Layer:     LayerNone,
		Isolation: IsolationNone,
		Network:   []string{NetworkNone},
		Source:    SourceBuiltin,
		License:   "n/a",
		DocsURL:   "/docs",
	}, nil
}

// Probe is always available: the mock has no dependencies by construction.
func (m *MockProvider) Probe(context.Context) (Availability, error) {
	return Availability{Available: true, Reason: "deterministic offline provider: executes nothing"}, nil
}

// Create mints a stable reference derived from the lease id.
func (m *MockProvider) Create(_ context.Context, spec Spec) (Handle, error) {
	sum := sha256.Sum256([]byte(spec.LeaseID))
	return Handle{ID: spec.LeaseID, Provider: ProviderMock, Ref: "mock-" + hex.EncodeToString(sum[:6])}, nil
}

// Exec renders a receipt without running anything.
func (m *MockProvider) Exec(_ context.Context, h Handle, req ExecRequest) (ExecResult, error) {
	digest := sha256.Sum256([]byte(strings.Join(append([]string{req.Command}, req.Args...), " ")))
	payload := map[string]any{
		"provider":  ProviderMock,
		"command":   req.Command,
		"args":      len(req.Args),
		"exit_code": 0,
		"digest":    hex.EncodeToString(digest[:8]),
		"executed":  false,
	}
	body, _ := json.Marshal(payload)
	return ExecResult{
		ExitCode: 0,
		Stdout:   string(body),
		Provider: ProviderMock,
		Ref:      h.Ref,
		Receipt:  "json",
	}, nil
}

// Attach is not supported: there is no process to talk to.
func (m *MockProvider) Attach(context.Context, Handle, ExecRequest) (Session, error) {
	return nil, Error(CodeUnsupported)
}

// Destroy is a no-op on purpose.
func (m *MockProvider) Destroy(context.Context, Handle) error { return nil }

// Status is always ready.
func (m *MockProvider) Status(context.Context, Handle) (string, error) { return StateReady, nil }
