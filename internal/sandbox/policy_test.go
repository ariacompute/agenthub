package sandbox

import (
	"context"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// The policy layer is where "a resource cannot widen its own sandbox" is either
// true or a comment. Everything here is pure, so the cases are exhaustive and
// cheap.

func fullBaseProfile() Profile {
	return Profile{
		Provider:       ProviderMock,
		Runtime:        "runsc",
		Image:          "alpine:3",
		WorkDir:        "/host/work",
		Network:        NetworkBridge,
		EgressAllow:    []string{"example.com"},
		CPU:            2,
		MemoryMB:       512,
		PidsLimit:      64,
		TimeoutMS:      5000,
		MaxOutputBytes: 8192,
		EnvAllow:       []string{"PATH"},
		ArgsPolicy:     ArgsPrefix,
		ArgsAllow:      []string{"--"},
		ReuseScope:     "ticket",
		IdleTTLSeconds: 120,
	}
}

func TestResolveInheritsEverythingTheOverrideLeavesAlone(t *testing.T) {
	base := fullBaseProfile()
	only := Resolve(base, Profile{TimeoutMS: 9000})

	if only.TimeoutMS != 9000 {
		t.Fatalf("timeout = %d, want the override", only.TimeoutMS)
	}
	// Every other ceiling has to come from the instance: that is what stops a
	// resource from widening its own sandbox by naming one field.
	if only.CPU != base.CPU || only.MemoryMB != base.MemoryMB || only.PidsLimit != base.PidsLimit {
		t.Fatalf("limits were not inherited: %+v", only)
	}
	if only.MaxOutputBytes != base.MaxOutputBytes || only.IdleTTLSeconds != base.IdleTTLSeconds {
		t.Fatalf("caps were not inherited: %+v", only)
	}
	if only.Provider != base.Provider || only.ArgsPolicy != base.ArgsPolicy || only.ReuseScope != base.ReuseScope {
		t.Fatalf("identity fields were not inherited: %+v", only)
	}

	// A blank override is "inherit", not "clear"; a pointer is the only way to
	// say read-write explicitly.
	blank := Resolve(base, Profile{
		Provider: "   ", Runtime: " ", Image: "  ", WorkDir: " ", Network: " ",
		ArgsPolicy: "\t", ReuseScope: " ",
	})
	if blank.Provider != base.Provider || blank.Image != base.Image || blank.WorkDir != base.WorkDir {
		t.Fatalf("blank overrides must not clear anything: %+v", blank)
	}
	if !blank.Readonly() {
		t.Fatal("an unset ReadonlyRoot means read-only")
	}
	readWrite := false
	if Resolve(base, Profile{ReadonlyRoot: &readWrite}).Readonly() {
		t.Fatal("an explicit false is the only way to opt out of a read-only root")
	}

	// Arrays are copied, not shared with the caller's slice.
	overridden := Resolve(base, Profile{EgressAllow: []string{"a"}, EnvAllow: []string{"B"}, ArgsAllow: []string{"c"}})
	if len(overridden.EgressAllow) != 1 || overridden.EgressAllow[0] != "a" {
		t.Fatalf("egress = %v", overridden.EgressAllow)
	}
	base.EgressAllow[0] = "mutated"
	if overridden.EgressAllow[0] == "mutated" {
		t.Fatal("the resolved profile must own its slices")
	}
}

func TestResolveHonoursEveryExplicitOverride(t *testing.T) {
	readWrite := false
	override := Profile{
		Provider:       "bwrap",
		Runtime:        "runsc",
		Image:          "alpine:3.20",
		WorkDir:        "/srv/work",
		ReadonlyRoot:   &readWrite,
		Network:        "allowlist",
		EgressAllow:    []string{"api.example"},
		CPU:            4,
		MemoryMB:       2048,
		PidsLimit:      256,
		TimeoutMS:      30000,
		MaxOutputBytes: 1 << 20,
		EnvAllow:       []string{"TOKEN"},
		ArgsPolicy:     ArgsPassthrough,
		ArgsAllow:      []string{"--x"},
		ReuseScope:     "ticket",
		IdleTTLSeconds: 600,
	}
	out := Resolve(Profile{}, override)

	if out.Provider != "bwrap" || out.Runtime != "runsc" || out.Image != "alpine:3.20" || out.WorkDir != "/srv/work" {
		t.Fatalf("identity overrides were dropped: %+v", out)
	}
	if out.Readonly() {
		t.Fatal("an explicit read-write override must survive Resolve")
	}
	if out.Network != NetworkAllowlist || len(out.EgressAllow) != 1 {
		t.Fatalf("network overrides were dropped: %+v", out)
	}
	if out.CPU != 4 || out.MemoryMB != 2048 || out.PidsLimit != 256 {
		t.Fatalf("resource overrides were dropped: %+v", out)
	}
	if out.TimeoutMS != 30000 || out.MaxOutputBytes != 1<<20 || out.IdleTTLSeconds != 600 {
		t.Fatalf("budget overrides were dropped: %+v", out)
	}
	if len(out.EnvAllow) != 1 || out.EnvAllow[0] != "TOKEN" {
		t.Fatalf("env allowlist = %v", out.EnvAllow)
	}
	if out.ArgsPolicy != ArgsPassthrough || len(out.ArgsAllow) != 1 || out.ReuseScope != "ticket" {
		t.Fatalf("argument overrides were dropped: %+v", out)
	}
}

func TestResolveFillsTheDefaultsForAZeroProfile(t *testing.T) {
	out := Resolve(Profile{}, Profile{})
	if out.Provider != ProviderDocker {
		t.Fatalf("provider = %q, want the container default", out.Provider)
	}
	if out.Image != DefaultImage {
		t.Fatalf("image = %q", out.Image)
	}
	if out.TimeoutMS != int(time.Minute/time.Millisecond) {
		t.Fatalf("timeout = %d", out.TimeoutMS)
	}
	if out.MaxOutputBytes != 64<<10 {
		t.Fatalf("cap = %d", out.MaxOutputBytes)
	}
	// Resolve keeps the empty string; every provider normalises it at the boundary
	// (docker turns anything that is not bridge into --network none).
	if NormalizeNetwork(out.Network) != NetworkNone {
		t.Fatalf("network = %q, want it to normalise to none", out.Network)
	}
}

func TestDefaultsProfileNormalizesAndCopies(t *testing.T) {
	defaults := Defaults{
		Network:     " HOST ",
		ArgsPolicy:  "PREFIX",
		EgressAllow: []string{"a.example"},
		EnvAllow:    []string{"PATH"},
		IdleTTL:     90 * time.Second,
	}
	profile := defaults.Profile()
	if profile.Network != NetworkBridge {
		t.Fatalf("network = %q", profile.Network)
	}
	if profile.ArgsPolicy != ArgsPrefix {
		t.Fatalf("args policy = %q", profile.ArgsPolicy)
	}
	if profile.IdleTTLSeconds != 90 {
		t.Fatalf("idle ttl = %d seconds", profile.IdleTTLSeconds)
	}
	defaults.EgressAllow[0] = "mutated"
	if profile.EgressAllow[0] == "mutated" {
		t.Fatal("the profile must not alias the instance defaults")
	}

	zero := Defaults{}.Profile()
	if zero.Provider != ProviderDocker || zero.Image != DefaultImage {
		t.Fatalf("a zero Defaults must still be usable: %+v", zero)
	}
	if zero.IdleTTLSeconds != 0 {
		t.Fatalf("an unset idle ttl must stay unset, got %d", zero.IdleTTLSeconds)
	}
}

func TestNormalizeNetworkAndArgsPolicy(t *testing.T) {
	networks := map[string]string{
		"":                    NetworkNone,
		" none ":              NetworkNone,
		"egress":              NetworkAllowlist,
		"ALLOWLIST":           NetworkAllowlist,
		NetworkAllowlist:      NetworkAllowlist,
		"host":                NetworkBridge,
		"BRIDGE":              NetworkBridge,
		NetworkPlatform:       NetworkPlatform,
		"something-unrelated": NetworkNone,
	}
	for in, want := range networks {
		if got := NormalizeNetwork(in); got != want {
			t.Fatalf("NormalizeNetwork(%q) = %q, want %q", in, got, want)
		}
	}

	policies := map[string]string{
		"":             ArgsNone,
		"PREFIX":       ArgsPrefix,
		" passthrough": ArgsPassthrough,
		"anything":     ArgsNone,
	}
	for in, want := range policies {
		if got := NormalizeArgsPolicy(in); got != want {
			t.Fatalf("NormalizeArgsPolicy(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestGrantsNetworkIsTheSingleAnswer: the allowlist value stays in the
// vocabulary (records reference it) but must never grant network on its own.
func TestGrantsNetworkIsTheSingleAnswer(t *testing.T) {
	cases := map[string]bool{
		"":                    false,
		NetworkNone:           false,
		NetworkAllowlist:      false,
		"egress":              false,
		"allowlist":           false,
		" bridge ":            true,
		"BRIDGE":              true,
		"host":                true,
		NetworkPlatform:       true,
		"something-unrelated": false,
	}
	for in, want := range cases {
		if got := GrantsNetwork(Profile{Network: in}); got != want {
			t.Fatalf("GrantsNetwork(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestValidateProfileRejectsImpossibleLimits(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*Profile)
	}{
		{"negative cpu", func(p *Profile) { p.CPU = -1 }},
		{"negative memory", func(p *Profile) { p.MemoryMB = -1 }},
		{"negative pids", func(p *Profile) { p.PidsLimit = -1 }},
		{"negative timeout", func(p *Profile) { p.TimeoutMS = -1 }},
		{"negative output cap", func(p *Profile) { p.MaxOutputBytes = -1 }},
		{"negative idle ttl", func(p *Profile) { p.IdleTTLSeconds = -1 }},
		// Note: an unknown network or args policy is not a rejection — both are
		// normalised to the strict value, so a typo can only ever narrow access.
		// A prefix policy without prefixes would refuse every argument, which is
		// indistinguishable from a misconfiguration.
		{"prefix policy without prefixes", func(p *Profile) {
			p.ArgsPolicy = ArgsPrefix
			p.ArgsAllow = nil
		}},
	}
	for _, tc := range cases {
		profile := Profile{Network: NetworkNone, ArgsPolicy: ArgsNone}
		tc.mut(&profile)
		if err := ValidateProfile(profile); err == nil {
			t.Fatalf("%s must be refused at registration", tc.name)
		} else if code := codeOf(t, err); code != CodePolicyDenied {
			t.Fatalf("%s: expected %s, got %s", tc.name, CodePolicyDenied, code)
		}
	}

	valid := []Profile{
		{Network: NetworkNone, ArgsPolicy: ArgsNone},
		{Network: NetworkBridge, ArgsPolicy: ArgsPassthrough},
		{Network: "host", ArgsPolicy: ArgsPrefix, ArgsAllow: []string{"--"}},
	}
	for _, profile := range valid {
		if err := ValidateProfile(profile); err != nil {
			t.Fatalf("profile %+v must be accepted: %v", profile, err)
		}
	}
}

func TestArgsAllowedHonoursThePolicy(t *testing.T) {
	none := Profile{ArgsPolicy: ArgsNone}
	if !ArgsAllowed(none, nil) {
		t.Fatal("the default policy allows the command with no arguments")
	}
	if ArgsAllowed(none, []string{"--exec=evil"}) {
		t.Fatal("the default policy must refuse every caller-supplied argument")
	}

	prefix := Profile{ArgsPolicy: ArgsPrefix, ArgsAllow: []string{"--safe", "input/"}}
	if !ArgsAllowed(prefix, []string{"--safe", "input/data"}) {
		t.Fatal("allowlisted arguments must pass")
	}
	if ArgsAllowed(prefix, []string{"--safe", "--exec=evil"}) {
		t.Fatal("one disallowed argument must refuse the whole call")
	}

	passthrough := Profile{ArgsPolicy: ArgsPassthrough}
	if !ArgsAllowed(passthrough, []string{"anything", "at", "all"}) {
		t.Fatal("passthrough means passthrough")
	}

	unknown := Profile{ArgsPolicy: "what"}
	if ArgsAllowed(unknown, []string{"x"}) {
		t.Fatal("an unknown policy must degrade to the strict one, never the loose one")
	}
}

func TestArgumentChecksOneExecutableArgument(t *testing.T) {
	if err := Argument(Profile{ArgsPolicy: ArgsPassthrough}, "--exec=evil"); err != nil {
		t.Fatalf("passthrough: %v", err)
	}
	prefix := Profile{ArgsPolicy: ArgsPrefix, ArgsAllow: []string{"--safe"}}
	if err := Argument(prefix, "--safe=1"); err != nil {
		t.Fatalf("allowed prefix: %v", err)
	}
	if err := Argument(prefix, "--exec=evil"); err == nil {
		t.Fatal("a non-allowlisted argument must be refused")
	} else if code := codeOf(t, err); code != CodePolicyDenied {
		t.Fatalf("expected %s, got %s", CodePolicyDenied, code)
	}
	if err := Argument(Profile{ArgsPolicy: ArgsNone}, "x"); err == nil {
		t.Fatal("the strict policy refuses every argument")
	}
}

func TestHasPrefixAnyIgnoresEmptyEntries(t *testing.T) {
	if !hasPrefixAny("--safe", []string{"", "--safe"}) {
		t.Fatal("an empty prefix must not match everything, but the real one must match")
	}
	if hasPrefixAny("--evil", []string{"", "  "}) {
		t.Fatal("blank prefixes must never match")
	}
	if hasPrefixAny("x", nil) {
		t.Fatal("no prefixes means no match")
	}
}

func TestProfileStringCarriesIdentifiersOnly(t *testing.T) {
	line := Profile{Provider: "docker", Image: "alpine", EnvAllow: []string{"SECRET_TOKEN"}}.String()
	if !strings.Contains(line, "provider=docker") || !strings.Contains(line, "image=alpine") {
		t.Fatalf("log line = %q", line)
	}
	if strings.Contains(line, "SECRET_TOKEN") {
		t.Fatal("a log line must never carry an environment value or name list")
	}
}

func TestPresetsAreLoadableTemplates(t *testing.T) {
	presets := PresetSpecs()
	if len(presets) == 0 {
		t.Fatal("the shipped presets must not be empty")
	}
	for _, spec := range presets {
		// The presets are POSIX paths by design; a Windows host cannot even tell
		// that "/usr/bin/nsjail" is absolute, so the construction half only runs
		// where the preset could actually be installed.
		if runtime.GOOS != "windows" {
			if _, err := NewDeclarativeProvider(spec); err != nil {
				t.Fatalf("preset %q must be a valid template: %v", spec.Name, err)
			}
		}
		looked, ok := PresetByName(spec.Name)
		if !ok || looked.Binary != spec.Binary {
			t.Fatalf("PresetByName(%q) = %+v ok=%v", spec.Name, looked, ok)
		}
	}
	if _, ok := PresetByName("no-such-preset"); ok {
		t.Fatal("an unknown preset must report false")
	}
}

func TestDeclarativeTemplatesAreCheckedBeforeTheyRun(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "tool")

	// The argument suffix is validated at load time too: a typo there would
	// otherwise only surface as a refused execution much later.
	broken := DeclarativeSpec{
		Name:     "tpl",
		Binary:   bin,
		ExecArgs: [][]string{{bin, "{command}"}, {bin, "{nope}"}},
	}
	if _, err := NewDeclarativeProvider(broken); err == nil {
		t.Fatal("an unknown placeholder in the argument suffix must be refused")
	}

	// {env:NAME} is the documented way to pass one variable through, so it has to
	// load and substitute at run time.
	good := DeclarativeSpec{
		Name:     "tpl",
		Binary:   bin,
		ExecArgs: [][]string{{bin, "{command}", "{env:TOKEN}"}},
	}
	provider, err := NewDeclarativeProvider(good)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	argv, err := expandArgv(provider.execPre, varsFor(Spec{}, "echo", map[string]string{"TOKEN": "s3cret"}))
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	if len(argv) != 3 || argv[2] != "s3cret" {
		t.Fatalf("argv = %v", argv)
	}
}

func TestProviderFuncDefaultsAreHonest(t *testing.T) {
	p := providerFunc{name: "stub", desc: Descriptor{Layer: LayerProcess}}
	ctx := context.Background()

	if _, err := p.Exec(ctx, Handle{}, ExecRequest{}); err != nil {
		t.Fatalf("exec: %v", err)
	}
	if _, err := p.Attach(ctx, Handle{}, ExecRequest{}); err == nil {
		t.Fatal("a stub must not pretend to hold a session")
	} else if code := codeOf(t, err); code != CodeUnsupported {
		t.Fatalf("expected %s, got %s", CodeUnsupported, code)
	}
	if err := p.Destroy(ctx, Handle{}); err != nil {
		t.Fatalf("destroy: %v", err)
	}
	if state, err := p.Status(ctx, Handle{}); err != nil || state != StateReady {
		t.Fatalf("status = %q (%v)", state, err)
	}
}
