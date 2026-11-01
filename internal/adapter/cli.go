package adapter

import (
	"context"
	"strings"

	"agenthub/internal/a2a"
	"agenthub/internal/httputil"
	"agenthub/internal/registry"
	"agenthub/internal/sandbox"
)

// cliConnector is the "walkie-talkie" of the platform: a command in, a
// structured receipt out.
//
// Two rules, and they are the whole security story for this kind:
//
//  1. The command comes from the resource record, never from the prompt. The
//     first release read the prompt's first word as the command and the rest as
//     arguments, which turned an allowlisted `gh` into an arbitrary code
//     execution primitive.
//  2. The arguments come from the caller, but only as far as the resource's
//     args_policy allows. The default policy allows none.
//
// Execution itself goes through sandbox.Manager. When no sandbox control plane is
// wired in, the connector keeps the original deterministic receipt: the two
// behaviours share one signature so the pre-sandbox tests still mean something.
type cliConnector struct {
	opts Options
}

func (c *cliConnector) Kind() registry.Kind { return registry.KindCLI }

func (c *cliConnector) Invoke(ctx context.Context, req InvokeRequest) (InvokeResult, error) {
	command := entryCommand(&req.Entry)
	if command == "" {
		return InvokeResult{}, &httputil.Error{Code: CodeCommandNotAllowed}
	}
	if !c.allowed(command) {
		return InvokeResult{}, &httputil.Error{Code: CodeCommandNotAllowed}
	}
	profile := policyProfile(&req.Entry)
	if !sandbox.ArgsAllowed(profile, req.Args) {
		return InvokeResult{}, &httputil.Error{Code: sandbox.CodePolicyDenied}
	}

	if c.opts.Sandbox == nil {
		return c.mockInvoke(command, req.Args, profile)
	}
	return c.sandboxedInvoke(ctx, command, req, profile)
}

// sandboxedInvoke runs the registered command inside a leased environment.
func (c *cliConnector) sandboxedInvoke(ctx context.Context, command string, req InvokeRequest, profile sandbox.Profile) (InvokeResult, error) {
	timeoutMS := profile.TimeoutMS
	if timeoutMS <= 0 {
		timeoutMS = int(c.opts.CLITimeout.Milliseconds())
	}
	execReq := sandbox.ExecRequest{
		Command:        command,
		Args:           req.Args,
		Stdin:          req.Prompt,
		Dir:            profile.WorkDir,
		TimeoutMS:      timeoutMS,
		MaxOutputBytes: profile.MaxOutputBytes,
	}
	leaseReq := sandbox.LeaseRequest{
		OwnerID:    req.OwnerID,
		ResourceID: req.ResourceID,
		Scope:      req.Scope,
		Profile:    profile,
	}
	res, lease, err := c.opts.Sandbox.ExecOn(ctx, leaseReq, execReq)
	if err != nil {
		return InvokeResult{}, err
	}
	return receiptResult(req, command, req.Args, res, lease), nil
}

// receiptResult turns a real execution into the artifact the A2A layer returns.
// The stdout becomes a text part; the receipt carries the identifiers an operator
// needs to find the audit row.
func receiptResult(req InvokeRequest, command string, args []string, res sandbox.ExecResult, lease *sandbox.Lease) InvokeResult {
	text := strings.TrimSpace(res.Stdout)
	if text == "" {
		text = strings.TrimSpace(res.Stderr)
	}
	out := "cli=" + command + " exit=" + itoa(res.ExitCode) + " truncated=" + boolString(res.Truncated)
	data := map[string]any{
		"kind":       string(registry.KindCLI),
		"resource":   req.Entry.Name,
		"provider":   res.Provider,
		"command":    command,
		"args":       args,
		"exit_code":  res.ExitCode,
		"stdout":     res.Stdout,
		"stderr":     res.Stderr,
		"truncated":  res.Truncated,
		"durationMs": res.DurationMS,
		"receipt":    res.Receipt,
	}
	if lease != nil {
		data["lease_id"] = lease.ID
		data["scope"] = lease.Scope
		data["expiresAt"] = lease.ExpiresAt
	}
	parts := []a2a.Part{a2a.NewDataPart(data)}
	if text != "" {
		parts = append([]a2a.Part{a2a.NewTextPart(text)}, parts...)
	}
	artifactMeta := []string{
		"command", command,
		"truncated", boolString(res.Truncated),
		"provider", res.Provider,
		"exit", itoa(res.ExitCode),
	}
	if lease != nil {
		artifactMeta = append(artifactMeta, "lease", lease.ID)
	}
	return InvokeResult{
		Text:      out,
		Parts:     parts,
		Artifacts: []a2a.Artifact{artifact("cli.receipt", 0, []a2a.Part{a2a.NewDataPart(data)}, artifactMeta...)},
	}
}

// mockInvoke is the pre-sandbox behaviour, kept for tests and for an instance
// that runs without a control plane.
func (c *cliConnector) mockInvoke(command string, args []string, profile sandbox.Profile) (InvokeResult, error) {
	stdout := "{\"command\":\"" + command + "\",\"args\":" + quoteArgs(args) + ",\"exit_code\":0,\"digest\":\"" + digest(command + " " + strings.Join(args, " "))[:16] + "\"}"
	cap := profile.MaxOutputBytes
	if cap <= 0 {
		cap = c.opts.MaxOutputBytes
	}
	stdout, truncated := truncate(stdout, cap)
	out := "cli=" + command + " exit=0 truncated=" + boolString(truncated)
	data := map[string]any{
		"kind":      string(registry.KindCLI),
		"resource":  "",
		"provider":  MockProviderName,
		"command":   command,
		"args":      args,
		"exit_code": 0,
		"stdout":    stdout,
		"truncated": truncated,
	}
	return InvokeResult{
		Text:      out,
		Parts:     []a2a.Part{a2a.NewTextPart(out), a2a.NewDataPart(data)},
		Artifacts: []a2a.Artifact{artifact("cli.receipt", 0, []a2a.Part{a2a.NewDataPart(data)}, "command", command, "truncated", boolString(truncated), "provider", MockProviderName)},
	}, nil
}

// Health answers two different questions depending on how the instance is wired.
//
// With a sandbox, the binary lives inside the environment, so the only honest
// measurement is whether the provider can run here. Without one, the original
// probe still applies: is the command on PATH, and would the allowlist let it
// through?
func (c *cliConnector) Health(ctx context.Context, e *registry.Entry) (registry.Health, error) {
	command := entryCommand(e)
	if command == "" {
		return nothingMeasured, nil
	}
	if c.opts.Sandbox != nil {
		profile := policyProfile(e)
		provider := strings.TrimSpace(profile.Provider)
		if provider == "" {
			provider = c.opts.Sandbox.Registry().Default()
		}
		info, err := c.opts.Sandbox.Registry().Info(ctx, provider)
		if err != nil || !info.Available {
			return registry.HealthUnhealthy, nil
		}
		return registry.HealthHealthy, nil
	}
	if _, err := lookPath(command); err != nil {
		return registry.HealthUnhealthy, nil
	}
	if !c.allowed(command) {
		return registry.HealthDegraded, nil
	}
	return registry.HealthHealthy, nil
}

// entryCommand is the command a record declares: kind_config.cli first, then the
// metadata the examples use. It never reads the prompt — that fallback was the
// escalation chain.
func entryCommand(e *registry.Entry) string {
	if e == nil {
		return ""
	}
	if e.KindConfig != nil && e.KindConfig.CLI != nil {
		if command := strings.TrimSpace(e.KindConfig.CLI.Command); command != "" {
			return command
		}
	}
	return strings.TrimSpace(metaString(e.Metadata, "command"))
}

func (c *cliConnector) allowed(command string) bool {
	for _, a := range c.opts.CLIAllowlist {
		if strings.EqualFold(strings.TrimSpace(a), command) {
			return true
		}
	}
	return false
}

func quoteArgs(args []string) string {
	quoted := make([]string, 0, len(args))
	for _, a := range args {
		quoted = append(quoted, "\""+strings.ReplaceAll(a, "\"", "\\\"")+"\"")
	}
	return "[" + strings.Join(quoted, ",") + "]"
}

func boolString(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
