package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// PlatformPreset describes a hosted sandbox platform (E2B, microsandbox,
// Daytona, CubeSandbox, OpenHands) as a catalogue entry agenthub can talk to
// without embedding anybody's SDK.
//
// The contract is deliberately tiny and documented in requirements-sandbox.md:
//
//	GET    {base}/healthz                     -> 200 when the shim is up
//	POST   {base}/sandboxes                   -> {"id": "..."}
//	POST   {base}/sandboxes/{id}/exec         -> ExecResult
//	DELETE {base}/sandboxes/{id}              -> 204
//	GET    {base}/sandboxes/{id}              -> {"state": "ready"}
//
// Operators run a thin adapter (or the vendor's own sidecar) that speaks it.
// That keeps the vendor's lifecycle vocabulary out of agenthub and keeps the
// credential a reference to an environment variable.
type PlatformPreset struct {
	Name       string
	Layer      Layer
	Isolation  string
	EnvBaseURL string
	EnvToken   string
	License    string
	DocsURL    string
}

// PlatformPresets is the shipped list. None of them is usable until the operator
// sets the base URL, and the catalogue says exactly that.
func PlatformPresets() []PlatformPreset {
	return []PlatformPreset{
		{Name: "e2b", Layer: LayerPlatform, Isolation: IsolationPlatform, EnvBaseURL: "E2B_SHIM_URL", EnvToken: "E2B_SHIM_TOKEN", License: "Apache-2.0", DocsURL: "https://github.com/e2b-dev/E2B"},
		{Name: "microsandbox", Layer: LayerPlatform, Isolation: IsolationPlatform, EnvBaseURL: "MICROSANDBOX_SHIM_URL", EnvToken: "MICROSANDBOX_SHIM_TOKEN", License: "Apache-2.0", DocsURL: "https://github.com/microsandbox/microsandbox"},
		{Name: "daytona", Layer: LayerPlatform, Isolation: IsolationPlatform, EnvBaseURL: "DAYTONA_SHIM_URL", EnvToken: "DAYTONA_SHIM_TOKEN", License: "Apache-2.0", DocsURL: "https://github.com/daytonaio/daytona"},
		{Name: "cubesandbox", Layer: LayerPlatform, Isolation: IsolationPlatform, EnvBaseURL: "CUBESANDBOX_SHIM_URL", EnvToken: "CUBESANDBOX_SHIM_TOKEN", License: "Apache-2.0", DocsURL: "https://github.com/tencent-ailab/CubeSandbox"},
		{Name: "openhands", Layer: LayerPlatform, Isolation: IsolationPlatform, EnvBaseURL: "OPENHANDS_RUNTIME_URL", EnvToken: "OPENHANDS_RUNTIME_TOKEN", License: "MIT", DocsURL: "https://github.com/All-Hands-AI/OpenHands"},
	}
}

// HTTPProvider is the normalized client for a platform shim.
type HTTPProvider struct {
	preset PlatformPreset
	base   string
	token  string
	client *http.Client
}

// NewHTTPProvider builds a platform provider. An unset base URL is not an error:
// the entry still appears in the catalogue, marked unavailable, so an operator
// can see that the platform is known to agenthub but not wired up yet.
func NewHTTPProvider(preset PlatformPreset, lookup func(string) string) *HTTPProvider {
	if lookup == nil {
		lookup = func(string) string { return "" }
	}
	return &HTTPProvider{
		preset: preset,
		base:   strings.TrimRight(strings.TrimSpace(lookup(preset.EnvBaseURL)), "/"),
		token:  strings.TrimSpace(lookup(preset.EnvToken)),
		client: &http.Client{
			Timeout: 0,
			// A platform shim that redirects is a misconfiguration; following it
			// would let a compromised shim point agenthub at an internal host.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

// Name is the catalogue key.
func (h *HTTPProvider) Name() string { return h.preset.Name }

// Describe reports the declared layer.
func (h *HTTPProvider) Describe(context.Context) (Descriptor, error) {
	return Descriptor{
		Name:      h.preset.Name,
		Layer:     h.preset.Layer,
		Isolation: h.preset.Isolation,
		Network:   []string{NetworkPlatform},
		Requires:  []string{h.preset.EnvBaseURL},
		Source:    SourceDeclarative,
		License:   h.preset.License,
		DocsURL:   h.preset.DocsURL,
	}, nil
}

// Probe asks the shim for /healthz.
func (h *HTTPProvider) Probe(ctx context.Context) (Availability, error) {
	if h.base == "" {
		return Availability{Available: false, Reason: h.preset.EnvBaseURL + " is not set"}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.base+"/healthz", nil)
	if err != nil {
		return Availability{Available: false, Reason: "invalid " + h.preset.EnvBaseURL}, nil
	}
	h.auth(req)
	resp, err := h.client.Do(req)
	if err != nil {
		return Availability{Available: false, Reason: h.preset.Name + " shim is unreachable"}, nil
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		return Availability{Available: false, Reason: h.preset.Name + " shim answered " + resp.Status}, nil
	}
	return Availability{Available: true, Reason: h.base}, nil
}

// Create asks the platform for one sandbox.
func (h *HTTPProvider) Create(ctx context.Context, spec Spec) (Handle, error) {
	if h.base == "" {
		return Handle{}, Error(CodeUnsupported)
	}
	body := map[string]any{"leaseId": spec.LeaseID, "profile": spec.Profile}
	var out struct {
		ID string `json:"id"`
	}
	if err := h.do(ctx, http.MethodPost, "/sandboxes", body, &out); err != nil {
		return Handle{}, err
	}
	if strings.TrimSpace(out.ID) == "" {
		return Handle{}, Error(CodeUnsupported)
	}
	return Handle{ID: spec.LeaseID, Provider: h.preset.Name, Ref: out.ID, Spec: spec}, nil
}

// Exec runs one command on the platform.
func (h *HTTPProvider) Exec(ctx context.Context, handle Handle, req ExecRequest) (ExecResult, error) {
	var out ExecResult
	if err := h.do(ctx, http.MethodPost, "/sandboxes/"+handle.Ref+"/exec", req, &out); err != nil {
		return ExecResult{}, err
	}
	out.Provider = h.preset.Name
	if out.Ref == "" {
		out.Ref = handle.Ref
	}
	return out, nil
}

// Attach is not part of the normalized contract: a platform that supports an
// interactive session exposes it, but agenthub will not pretend every one does.
func (h *HTTPProvider) Attach(context.Context, Handle, ExecRequest) (Session, error) {
	return nil, Error(CodeUnsupported)
}

// Destroy releases the platform sandbox.
func (h *HTTPProvider) Destroy(ctx context.Context, handle Handle) error {
	if h.base == "" {
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, h.base+"/sandboxes/"+handle.Ref, nil)
	if err != nil {
		return Error(CodeUnsupported)
	}
	h.auth(req)
	resp, err := h.client.Do(req)
	if err != nil {
		return nil
	}
	_ = resp.Body.Close()
	return nil
}

// Status reads the platform's own view.
func (h *HTTPProvider) Status(ctx context.Context, handle Handle) (string, error) {
	var out struct {
		State string `json:"state"`
	}
	if err := h.do(ctx, http.MethodGet, "/sandboxes/"+handle.Ref, nil, &out); err != nil {
		return StateFailed, nil
	}
	if out.State == "" {
		return StateReady, nil
	}
	return out.State, nil
}

func (h *HTTPProvider) do(ctx context.Context, method, path string, body, out any) error {
	if h.base == "" {
		return Error(CodeUnsupported)
	}
	var reader *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return Error(CodePolicyDenied)
		}
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(ctx, method, h.base+path, reader)
	if err != nil {
		return Error(CodeUnsupported)
	}
	req.Header.Set("content-type", "application/json")
	h.auth(req)
	resp, err := h.client.Do(req)
	if err != nil {
		return Error(CodeNodeOffline)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return Error(CodeNotFound)
	}
	if resp.StatusCode/100 != 2 {
		return Error(CodeUnsupported)
	}
	if out == nil {
		return nil
	}
	dec := json.NewDecoder(resp.Body)
	if err := dec.Decode(out); err != nil {
		return Error(CodeUnsupported)
	}
	return nil
}

func (h *HTTPProvider) auth(req *http.Request) {
	if h.token != "" {
		req.Header.Set("authorization", "Bearer "+h.token)
	}
}
