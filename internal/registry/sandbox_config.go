package registry

import (
	"strings"

	"agenthub/internal/sandbox"
)

// SandboxConfig is the per-resource execution policy.
//
// Every field is optional and every zero value means "inherit the instance
// default": a resource that names only a timeout keeps every other limit the
// operator set, which is what stops a resource from widening its own sandbox by
// omission.
type SandboxConfig struct {
	// Provider overrides AGENTHUB_SANDBOX_PROVIDER for this resource.
	Provider string `json:"provider,omitempty"`
	// Runtime selects a container runtime for the container providers: "runsc"
	// for gVisor, "kata" for Kata Containers.
	Runtime string `json:"runtime,omitempty"`
	Image   string `json:"image,omitempty"`
	WorkDir string `json:"work_dir,omitempty"`
	// ReadonlyRoot defaults to true; an explicit false is the only opt-out.
	ReadonlyRoot *bool `json:"readonly_root,omitempty"`
	// Network is none | egress_allowlist | bridge. Empty means none.
	Network        string   `json:"network,omitempty"`
	EgressAllow    []string `json:"egress_allowlist,omitempty"`
	CPU            float64  `json:"cpu,omitempty"`
	MemoryMB       int      `json:"memory_mb,omitempty"`
	PidsLimit      int      `json:"pids_limit,omitempty"`
	TimeoutMS      int      `json:"timeout_ms,omitempty"`
	MaxOutputBytes int      `json:"max_output_bytes,omitempty"`
	EnvAllow       []string `json:"env_allowlist,omitempty"`
	// ArgsPolicy is none | prefix | passthrough. It governs arguments the caller
	// may supply to a CLI resource.
	ArgsPolicy     string   `json:"args_policy,omitempty"`
	ArgsAllowlist  []string `json:"args_allowlist,omitempty"`
	ReuseScope     string   `json:"reuse_scope,omitempty"`
	IdleTTLSeconds int      `json:"idle_ttl_seconds,omitempty"`
}

// Profile converts the resource block into the domain policy. It is a pure
// conversion: validation happens in ValidateProfile, not here.
func (c *SandboxConfig) Profile() sandbox.Profile {
	if c == nil {
		return sandbox.Profile{}
	}
	return sandbox.Profile{
		Provider:       strings.TrimSpace(c.Provider),
		Runtime:        strings.TrimSpace(c.Runtime),
		Image:          strings.TrimSpace(c.Image),
		WorkDir:        strings.TrimSpace(c.WorkDir),
		ReadonlyRoot:   c.ReadonlyRoot,
		Network:        c.Network,
		EgressAllow:    c.EgressAllow,
		CPU:            c.CPU,
		MemoryMB:       c.MemoryMB,
		PidsLimit:      c.PidsLimit,
		TimeoutMS:      c.TimeoutMS,
		MaxOutputBytes: c.MaxOutputBytes,
		EnvAllow:       c.EnvAllow,
		ArgsPolicy:     c.ArgsPolicy,
		ArgsAllow:      c.ArgsAllowlist,
		ReuseScope:     c.ReuseScope,
		IdleTTLSeconds: c.IdleTTLSeconds,
	}
}

// EffectiveArgsPolicy merges the CLI block and the sandbox block. The sandbox
// block wins when both are set, because it is the newer, wider contract; the CLI
// one is kept so an entry written before this existed keeps its meaning.
func (c *SandboxConfig) EffectiveArgsPolicy(cli *CLIConfig) string {
	if c != nil && strings.TrimSpace(c.ArgsPolicy) != "" {
		return sandbox.NormalizeArgsPolicy(c.ArgsPolicy)
	}
	if cli != nil {
		return sandbox.NormalizeArgsPolicy(cli.ArgsPolicy)
	}
	return sandbox.ArgsNone
}

// validateSandbox rejects a policy that cannot be honoured. It runs at
// registration time so an operator learns about a bad limit while writing it.
func validateSandbox(c *SandboxConfig) error {
	if c == nil {
		return nil
	}
	profile := c.Profile()
	if err := sandbox.ValidateProfile(profile); err != nil {
		return kindConfigErr("sandbox")
	}
	if strings.TrimSpace(c.Provider) != "" && strings.ContainsAny(c.Provider, " \t/\\") {
		return kindConfigErr("sandbox.provider")
	}
	if c.TimeoutMS < 0 || c.MaxOutputBytes < 0 || c.CPU < 0 || c.MemoryMB < 0 || c.PidsLimit < 0 || c.IdleTTLSeconds < 0 {
		return kindConfigErr("sandbox")
	}
	if sandbox.NormalizeArgsPolicy(c.ArgsPolicy) == sandbox.ArgsPrefix && len(c.ArgsAllowlist) == 0 {
		return kindConfigErr("sandbox.args_allowlist")
	}
	return nil
}
