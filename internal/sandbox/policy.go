package sandbox

import (
	"fmt"
	"strings"
	"time"
)

// Defaults is the instance wide policy from AGENTHUB_SANDBOX_*: what a resource
// inherits when it names nothing. It is deliberately a value type the caller
// builds, so a test never depends on the environment.
type Defaults struct {
	Provider       string
	Runtime        string
	Image          string
	Network        string
	EgressAllow    []string
	CPU            float64
	MemoryMB       int
	PidsLimit      int
	TimeoutMS      int
	MaxOutputBytes int
	EnvAllow       []string
	ArgsPolicy     string
	ReuseScope     string
	IdleTTL        time.Duration
}

// Profile turns the defaults into the base profile.
func (d Defaults) Profile() Profile {
	p := Profile{
		Provider:       strings.TrimSpace(d.Provider),
		Runtime:        strings.TrimSpace(d.Runtime),
		Image:          strings.TrimSpace(d.Image),
		Network:        NormalizeNetwork(d.Network),
		EgressAllow:    cloneStrings(d.EgressAllow),
		CPU:            d.CPU,
		MemoryMB:       d.MemoryMB,
		PidsLimit:      d.PidsLimit,
		TimeoutMS:      d.TimeoutMS,
		MaxOutputBytes: d.MaxOutputBytes,
		EnvAllow:       cloneStrings(d.EnvAllow),
		ArgsPolicy:     NormalizeArgsPolicy(d.ArgsPolicy),
		ReuseScope:     strings.TrimSpace(d.ReuseScope),
	}
	if d.IdleTTL > 0 {
		p.IdleTTLSeconds = int(d.IdleTTL / time.Second)
	}
	if p.Provider == "" {
		p.Provider = ProviderDocker
	}
	if p.Image == "" {
		p.Image = DefaultImage
	}
	return p
}

// ProviderOff is the explicit "execute nothing" setting. It is not a backend: it
// makes every surface refuse, which is what an operator who does not trust
// execution at all wants to write down.
const ProviderOff = "off"

// ProviderMock is the deterministic offline backend: it runs nothing and says so
// in every receipt. Tests and offline demos set it explicitly, because the
// default is real execution.
const ProviderMock = "mock"

// ProviderDocker is the instance default: the only isolation layer an operator
// is likely to already have.
const ProviderDocker = "docker"

// DefaultImage is what container providers run when a resource names no image.
// A slim Debian base keeps `sh`, `git` and the usual CLI tools available; a pin
// by digest is an operator decision, documented in requirements-sandbox.md.
const DefaultImage = "debian:bookworm-slim"

// Resolve merges a resource's override onto the instance defaults. Zero values
// mean "inherit": a resource that names only a timeout keeps every other limit
// the instance set, which is the property that stops a resource from widening
// its own sandbox.
func Resolve(base Profile, override Profile) Profile {
	out := base
	if v := strings.TrimSpace(override.Provider); v != "" {
		out.Provider = v
	}
	if v := strings.TrimSpace(override.Runtime); v != "" {
		out.Runtime = v
	}
	if v := strings.TrimSpace(override.Image); v != "" {
		out.Image = v
	}
	if v := strings.TrimSpace(override.WorkDir); v != "" {
		out.WorkDir = v
	}
	if override.ReadonlyRoot != nil {
		out.ReadonlyRoot = override.ReadonlyRoot
	}
	if v := strings.TrimSpace(override.Network); v != "" {
		out.Network = NormalizeNetwork(v)
	}
	if len(override.EgressAllow) > 0 {
		out.EgressAllow = cloneStrings(override.EgressAllow)
	}
	if override.CPU > 0 {
		out.CPU = override.CPU
	}
	if override.MemoryMB > 0 {
		out.MemoryMB = override.MemoryMB
	}
	if override.PidsLimit > 0 {
		out.PidsLimit = override.PidsLimit
	}
	if override.TimeoutMS > 0 {
		out.TimeoutMS = override.TimeoutMS
	}
	if override.MaxOutputBytes > 0 {
		out.MaxOutputBytes = override.MaxOutputBytes
	}
	if len(override.EnvAllow) > 0 {
		out.EnvAllow = cloneStrings(override.EnvAllow)
	}
	if v := strings.TrimSpace(override.ArgsPolicy); v != "" {
		out.ArgsPolicy = NormalizeArgsPolicy(v)
	}
	if len(override.ArgsAllow) > 0 {
		out.ArgsAllow = cloneStrings(override.ArgsAllow)
	}
	if v := strings.TrimSpace(override.ReuseScope); v != "" {
		out.ReuseScope = v
	}
	if override.IdleTTLSeconds > 0 {
		out.IdleTTLSeconds = override.IdleTTLSeconds
	}
	if out.Provider == "" {
		out.Provider = ProviderDocker
	}
	if out.Image == "" {
		out.Image = DefaultImage
	}
	if out.TimeoutMS <= 0 {
		out.TimeoutMS = int(time.Minute / time.Millisecond)
	}
	if out.MaxOutputBytes <= 0 {
		out.MaxOutputBytes = 64 << 10
	}
	return out
}

// Readonly reports the resolved read-only root flag: unset means read-only.
func (p Profile) Readonly() bool {
	return p.ReadonlyRoot == nil || *p.ReadonlyRoot
}

// NormalizeNetwork maps an unknown or empty mode onto the conservative default.
// Anything the vocabulary does not recognise becomes "none": a typo must not
// become "the sandbox has network access".
func NormalizeNetwork(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case NetworkAllowlist, "egress", "allowlist":
		return NetworkAllowlist
	case NetworkBridge, "host":
		return NetworkBridge
	case NetworkPlatform:
		return NetworkPlatform
	default:
		return NetworkNone
	}
}

// GrantsNetwork reports whether a profile may reach the network at all.
//
// Only "bridge" (a NAT'd interface) and "platform" (whatever the host's default
// is) grant it. "egress_allowlist" deliberately does not: the vocabulary keeps
// the value so records written against the documented modes still parse, but no
// provider implements host filtering yet — that is an egress sidecar's job
// (TODO(real)). Until one exists the value is read as "none", which is the
// fail-closed answer.
//
// This answers "does the profile ask for a network", not "can the provider give
// it one". The second question belongs to the provider's Descriptor and is
// answered by SupportsNetwork, which Manager.Acquire consults before creating
// anything.
func GrantsNetwork(p Profile) bool {
	switch NormalizeNetwork(p.Network) {
	case NetworkBridge, NetworkPlatform:
		return true
	}
	return false
}

// SupportsNetwork reports whether a provider's declaration covers the mode a
// profile asks for.
//
// This is the function that makes Descriptor.Network load-bearing. Until it
// existed the declaration was decoration: the catalogue rendered it, one test
// read it, and every provider accepted every mode. That is how bubblewrap could
// answer a "bridge" request with the host network namespace while docker cut the
// container off for the same policy — two providers, two answers, one label.
//
// A provider that declares NetworkDelegated forwards the profile to a layer that
// does enforce it (an execution node, a hosted runtime) and therefore covers any
// concrete mode. Everything else must name the mode exactly.
func SupportsNetwork(desc Descriptor, p Profile) bool {
	want := NormalizeNetwork(p.Network)
	for _, have := range desc.Network {
		switch strings.ToLower(strings.TrimSpace(have)) {
		case NetworkDelegated:
			return true
		case want:
			return true
		}
	}
	return false
}

// NormalizeArgsPolicy maps an unknown or empty policy onto "none".
func NormalizeArgsPolicy(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case ArgsPrefix:
		return ArgsPrefix
	case ArgsPassthrough:
		return ArgsPassthrough
	default:
		return ArgsNone
	}
}

// ValidateProfile rejects a profile that cannot be honoured. It is called when a
// resource is registered, so an operator learns about a bad limit at the moment
// they write it rather than on the first execution.
func ValidateProfile(p Profile) error {
	if p.CPU < 0 || p.MemoryMB < 0 || p.PidsLimit < 0 {
		return Error(CodePolicyDenied)
	}
	if p.TimeoutMS < 0 || p.MaxOutputBytes < 0 || p.IdleTTLSeconds < 0 {
		return Error(CodePolicyDenied)
	}
	switch NormalizeNetwork(p.Network) {
	case NetworkNone, NetworkAllowlist, NetworkBridge, NetworkPlatform:
	default:
		return Error(CodePolicyDenied)
	}
	switch NormalizeArgsPolicy(p.ArgsPolicy) {
	case ArgsNone, ArgsPrefix, ArgsPassthrough:
	default:
		return Error(CodePolicyDenied)
	}
	if NormalizeArgsPolicy(p.ArgsPolicy) == ArgsPrefix && len(p.ArgsAllow) == 0 {
		// A prefix policy with no prefixes would refuse everything, which is
		// indistinguishable from a misconfiguration; say so at registration.
		return Error(CodePolicyDenied)
	}
	return nil
}

// ArgsAllowed reports whether an argument list passes the policy. The default
// policy is "none": no argument may come from the caller at all.
func ArgsAllowed(p Profile, args []string) bool {
	switch NormalizeArgsPolicy(p.ArgsPolicy) {
	case ArgsPassthrough:
		return true
	case ArgsPrefix:
		for _, a := range args {
			if !hasPrefixAny(a, p.ArgsAllow) {
				return false
			}
		}
		return true
	default:
		return len(args) == 0
	}
}

// Argument checks one executable argument that is not the command itself. It is
// the last line of defence against "the command is on the allowlist, the
// arguments are not": an argument that rewrites the command's behaviour
// ( --exec, -c, --pager, -oProxyCommand) is refused under strict policies.
func Argument(p Profile, arg string) error {
	switch NormalizeArgsPolicy(p.ArgsPolicy) {
	case ArgsPassthrough:
		return nil
	case ArgsPrefix:
		if hasPrefixAny(arg, p.ArgsAllow) {
			return nil
		}
		return Error(CodePolicyDenied)
	default:
		return Error(CodePolicyDenied)
	}
}

func hasPrefixAny(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if p != "" && strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

func cloneStrings(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, len(in))
	copy(out, in)
	return out
}

// String renders a profile for a log line: identifiers and counters only, never
// an environment value.
func (p Profile) String() string {
	return fmt.Sprintf("provider=%s runtime=%s image=%s network=%s cpu=%.2f mem=%dMB pids=%d timeout=%dms cap=%dB",
		p.Provider, p.Runtime, p.Image, NormalizeNetwork(p.Network), p.CPU, p.MemoryMB, p.PidsLimit, p.TimeoutMS, p.OutputCap())
}
