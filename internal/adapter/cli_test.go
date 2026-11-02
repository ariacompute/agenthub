package adapter

import (
	"context"
	"strings"
	"testing"

	"agenthub/internal/httputil"
	"agenthub/internal/registry"
	"agenthub/internal/sandbox"
)

// The two rules that make a CLI resource safe, pinned as regression tests:
//
//  1. the command comes from the record, never from the prompt;
//  2. the arguments come from the caller only as far as the record allows.
//
// Both are the fix for the escalation chain a security review found in the first
// release — an allowlisted `gh` that took its subcommand from the prompt could run
// anything. A later refactor must fail here, not in production.

func cliEntry(command string, cfg *registry.CLIConfig) registry.Entry {
	if cfg == nil {
		cfg = &registry.CLIConfig{}
	}
	cfg.Command = command
	return registry.Entry{
		Name:       "walkie",
		Kind:       registry.KindCLI,
		KindConfig: &registry.KindConfig{CLI: cfg},
	}
}

func cliOptions(allowlist ...string) Options {
	return Options{
		CLIAllowlist:   allowlist,
		MaxOutputBytes: 4096,
		CLITimeout:     0,
	}
}

func invokeCLI(t *testing.T, entry registry.Entry, opts Options, prompt string, args []string) (InvokeResult, error) {
	t.Helper()
	connector, err := For(&entry, opts)
	if err != nil {
		t.Fatalf("For: %v", err)
	}
	if connector.Kind() != registry.KindCLI {
		t.Fatalf("kind = %q, want %q", connector.Kind(), registry.KindCLI)
	}
	return connector.Invoke(context.Background(), InvokeRequest{
		Entry:  entry,
		Prompt: prompt,
		Args:   args,
	})
}

func wantCode(t *testing.T, err error, want httputil.Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected %q, got no error", want)
	}
	if got := httputil.AsError(err).Code; got != want {
		t.Fatalf("code = %q, want %q", got, want)
	}
}

func TestCLIRunsTheCommandTheRecordDeclares(t *testing.T) {
	entry := cliEntry("git", nil)
	res, err := invokeCLI(t, entry, cliOptions("git"), "", nil)
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if !strings.Contains(res.Text, "cli=git ") {
		t.Fatalf("receipt = %q, want it to name git", res.Text)
	}
}

func TestCLIIgnoresTheCommandThePromptAsksFor(t *testing.T) {
	entry := cliEntry("git", nil)
	// The prompt names a different program. Before the fix this word became the
	// command, which is exactly how an allowlisted binary became a shell.
	res, err := invokeCLI(t, entry, cliOptions("git"), "rm -rf / --no-preserve-root", nil)
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if !strings.Contains(res.Text, "cli=git ") {
		t.Fatalf("receipt = %q, want the record's command", res.Text)
	}
	if strings.Contains(res.Text, "rm ") {
		t.Fatalf("receipt = %q, the prompt must not choose the command", res.Text)
	}
}

func TestCLIRefusesACommandOutsideTheAllowlist(t *testing.T) {
	entry := cliEntry("curl", nil)
	_, err := invokeCLI(t, entry, cliOptions("git"), "", nil)
	wantCode(t, err, CodeCommandNotAllowed)
}

func TestCLIRefusesARecordThatDeclaresNoCommand(t *testing.T) {
	entry := cliEntry("", nil)
	_, err := invokeCLI(t, entry, cliOptions("git"), "git status", nil)
	wantCode(t, err, CodeCommandNotAllowed)
}

// The allowlist is an exact name, not a prefix and not a path: matching either
// would let `git-credential` or `/tmp/git` ride on the entry for `git`.
func TestCLIAllowlistMatchesAnExactName(t *testing.T) {
	for _, command := range []string{"git-credential", "/usr/bin/git", "./git", "git status"} {
		entry := cliEntry(command, nil)
		_, err := invokeCLI(t, entry, cliOptions("git"), "", nil)
		if err == nil || httputil.AsError(err).Code != CodeCommandNotAllowed {
			t.Fatalf("%q must not ride on the allowlist entry for git (err = %v)", command, err)
		}
	}
	// Case is not a security boundary: the platform resolves it for us.
	entry := cliEntry("GIT", nil)
	if _, err := invokeCLI(t, entry, cliOptions("git"), "", nil); err != nil {
		t.Fatalf("a case variant of an allowlisted command should run: %v", err)
	}
}

func TestCLIRefusesCallerArgumentsByDefault(t *testing.T) {
	entry := cliEntry("git", nil)
	_, err := invokeCLI(t, entry, cliOptions("git"), "", []string{"--upload-pack=calc"})
	wantCode(t, err, sandbox.CodePolicyDenied)
}

func TestCLIAllowsOnlyTheArgumentPrefixesTheRecordNames(t *testing.T) {
	entry := cliEntry("git", &registry.CLIConfig{
		ArgsPolicy:    "prefix",
		ArgsAllowlist: []string{"status", "--json"},
	})
	if _, err := invokeCLI(t, entry, cliOptions("git"), "", []string{"status", "--json"}); err != nil {
		t.Fatalf("allowlisted arguments should pass: %v", err)
	}
	_, err := invokeCLI(t, entry, cliOptions("git"), "", []string{"status", "--upload-pack=calc"})
	wantCode(t, err, sandbox.CodePolicyDenied)
}

func TestCLIPrefersTheKindConfigCommandOverMetadata(t *testing.T) {
	entry := cliEntry("git", nil)
	entry.Metadata = map[string]string{"command": "curl"}
	res, err := invokeCLI(t, entry, cliOptions("git", "curl"), "", nil)
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if !strings.Contains(res.Text, "cli=git ") {
		t.Fatalf("receipt = %q, want the kind_config command", res.Text)
	}
}

func TestCLIHealthReportsDegradedForANonAllowlistedCommand(t *testing.T) {
	entry := cliEntry("aria-memo", nil)
	connector, err := For(&entry, cliOptions("git"))
	if err != nil {
		t.Fatalf("For: %v", err)
	}
	// aria-memo is part of the default demo allowlist but not of this one, so the
	// only honest answer is "installed, but this instance would refuse it".
	health, err := connector.Health(context.Background(), &entry)
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if health == registry.HealthHealthy {
		t.Fatalf("health = %q, want degraded or unhealthy", health)
	}
}
