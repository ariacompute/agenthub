package sandbox

import (
	"fmt"
	"path"
	"path/filepath"
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

// Limits is the instance ceiling on the resources a sandbox may consume. It is
// the second half of "a resource cannot widen its own sandbox": Defaults says
// what a resource inherits, Limits says how far past that it may go.
//
// The zero value means "no ceiling", so a hand-built ManagerOptions in a unit
// test behaves as before and an instance that never set AGENTHUB_SANDBOX_MAX_*
// is not silently given one.
type Limits struct {
	CPU       float64
	MemoryMB  int
	PidsLimit int
}

// Clamp lowers a profile to the ceiling and reports whether it had to. It only
// ever tightens: a profile below the ceiling passes through untouched, and a
// zero field in the ceiling means "unbounded" rather than "zero".
func (l Limits) Clamp(p Profile) (Profile, bool) {
	changed := false
	if l.CPU > 0 && p.CPU > l.CPU {
		p.CPU = l.CPU
		changed = true
	}
	if l.MemoryMB > 0 && p.MemoryMB > l.MemoryMB {
		p.MemoryMB = l.MemoryMB
		changed = true
	}
	if l.PidsLimit > 0 && p.PidsLimit > l.PidsLimit {
		p.PidsLimit = l.PidsLimit
		changed = true
	}
	return p, changed
}

// Exceeds reports a profile that asks for more than the ceiling. It is the
// registration-time half of Clamp: the operator hears about an impossible limit
// while writing the record instead of seeing it silently reduced at run time.
func (l Limits) Exceeds(p Profile) error {
	if (l.CPU > 0 && p.CPU > l.CPU) ||
		(l.MemoryMB > 0 && p.MemoryMB > l.MemoryMB) ||
		(l.PidsLimit > 0 && p.PidsLimit > l.PidsLimit) {
		return Error(CodePolicyDenied)
	}
	return nil
}

// String renders the ceiling for a log line or a startup summary.
func (l Limits) String() string {
	return fmt.Sprintf("cpu=%.2f mem=%dMB pids=%d", l.CPU, l.MemoryMB, l.PidsLimit)
}

// HostPolicy is the instance's answer to "where may a sandbox touch the host
// filesystem". It is operator configuration, never resource configuration: a
// resource names a directory, the instance decides whether that directory is
// reachable at all. Both fields are tightening knobs with a safe zero value, so
// a hand-built ManagerOptions in a unit test keeps working.
type HostPolicy struct {
	// WorkDirRoots is the host tree a resource's work_dir must live under. An
	// empty list means "the shipped denylist only": existing records that already
	// name a directory keep working, and the system trees below stay refused. Set
	// it to the real work root (for example /srv/agenthub/work) to turn
	// work_dir into an allowlist.
	WorkDirRoots []string
	// AllowWritableRoot permits a resource to opt out of the read-only root. It is
	// false by default on purpose: "read-only unless the operator says otherwise"
	// is the only default that survives a resource written by somebody else.
	AllowWritableRoot bool
}

// hostDeniedPrefixes are the host trees a sandbox never gets a handle on, even
// as a read-only bind: they hold credentials (/etc, /root, /var/lib), kernel
// interfaces (/proc, /sys, /dev) or the binaries the sandbox is already running
// from. The list is about *identity and integrity*, not convenience — a work
// directory belongs in a data path.
var hostDeniedPrefixes = []string{
	"/proc", "/sys", "/dev", "/boot", "/etc", "/root", "/run",
	"/usr", "/bin", "/sbin", "/lib", "/lib32", "/lib64",
	"/var/lib", "/var/run", "/var/log",
}

// ValidateHostPolicy refuses a profile whose filesystem request the instance
// does not allow. It covers the two questions a profile can ask about the host:
//
//   - may the root filesystem be writable at all (AllowWritableRoot), and
//   - is the named work directory a place this instance shares (WorkDirRoots plus
//     the denylist).
//
// The checks are deliberately structural rather than best-effort: a relative
// path, a comma (the separator in `--mount` options, so it would smuggle another
// option in), a control character, a volume root or a system tree is refused
// outright, because every one of them has been a container-escape primitive
// somewhere. Traversal is normalised before the comparison rather than refused,
// so "a/b/../etc" is judged as "/etc" — the path the runtime would mount.
// Symlinks are not resolved here: the providers hand the path to the runtime, and
// following a link is the runtime's decision — an operator who pins WorkDirRoots
// has already bounded where the link may point.
func ValidateHostPolicy(p Profile, hp HostPolicy) error {
	if !p.Readonly() && !hp.AllowWritableRoot {
		// The root is read-only unless a resource explicitly opts out; allowing
		// that opt-out with no operator gate would make the flag advisory.
		return Error(CodePolicyDenied)
	}
	dir := strings.TrimSpace(p.WorkDir)
	if dir == "" {
		// No host directory named: the provider hands out an in-sandbox tmpfs,
		// which is the safest possible answer and needs no further checks.
		return nil
	}
	cleaned, err := CleanWorkDir(dir)
	if err != nil {
		return err
	}
	if len(hp.WorkDirRoots) == 0 {
		return nil
	}
	for _, root := range hp.WorkDirRoots {
		cleanRoot, ok := cleanHostPath(strings.TrimSpace(root))
		if !ok {
			// An operator-supplied root that cannot pass the same checks is a
			// misconfiguration, not a match: skipping it keeps a bad root from
			// accidentally widening the allowlist to "/".
			continue
		}
		if pathWithin(cleaned, cleanRoot) {
			return nil
		}
	}
	return Error(CodePolicyDenied)
}

// CleanWorkDir normalises a host directory and applies the structural checks
// that hold regardless of instance configuration: absolute, no option separator,
// no control characters, not a volume root, not a system tree. It is exported so
// registration can reject the legacy cli.work_dir field as well — the lease-time
// gate in ValidateHostPolicy is what additionally applies the instance allowlist.
func CleanWorkDir(raw string) (string, error) {
	cleaned, ok := cleanHostPath(strings.TrimSpace(raw))
	if !ok {
		return "", Error(CodePolicyDenied)
	}
	for _, denied := range hostDeniedPrefixes {
		if pathWithin(cleaned, denied) {
			return "", Error(CodePolicyDenied)
		}
	}
	return cleaned, nil
}

// cleanHostPath normalises a host path and reports whether it is a usable
// directory reference rather than a root, a traversal or an option injection.
func cleanHostPath(raw string) (string, bool) {
	if raw == "" || strings.ContainsAny(raw, ",\x00") {
		return "", false
	}
	for _, r := range raw {
		if r < 0x20 || r == 0x7f {
			return "", false
		}
	}
	slashed := filepath.ToSlash(strings.TrimSpace(raw))
	if !filepath.IsAbs(raw) && !strings.HasPrefix(slashed, "/") {
		// Windows volumes are absolute through filepath; a bare relative name is
		// never acceptable because its meaning depends on the daemon's cwd.
		return "", false
	}
	// Clean resolves ".." before the checks, so "/srv/x/../etc" is judged as
	// "/srv/etc" — the path the runtime will actually mount, rather than the
	// string somebody wrote.
	cleaned := path.Clean(slashed)
	if cleaned == "/" || cleaned == "." || cleaned == ".." {
		return "", false
	}
	// "C:" alone is a Windows volume root; path.Clean leaves it as "C:".
	if len(cleaned) == 2 && cleaned[1] == ':' {
		return "", false
	}
	return cleaned, true
}

// pathWithin reports whether candidate is prefix itself or lives under it, by
// whole path segments: /etc is not the parent of /etcetera.
func pathWithin(candidate, prefix string) bool {
	if candidate == prefix {
		return true
	}
	if !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	return strings.HasPrefix(candidate, prefix)
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
