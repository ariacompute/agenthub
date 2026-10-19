package adapter

import (
	"context"
	"strings"

	"agenthub/internal/a2a"
	"agenthub/internal/httputil"
	"agenthub/internal/registry"
)

// cliConnector is the "walkie-talkie" of the platform: a command in, a
// structured receipt out. Safety is enforced here, in one place:
//
//  1. allowlist check before anything runs,
//  2. a single invocation timeout,
//  3. output truncation with the fact reported in the artifact metadata.
//
// TODO(real): exec.CommandContext with the same allowlist, timeout and
// truncation; the mock keeps the contract testable without a subprocess.
type cliConnector struct {
	opts Options
}

func (c *cliConnector) Kind() registry.Kind { return registry.KindCLI }

func (c *cliConnector) Invoke(ctx context.Context, req InvokeRequest) (InvokeResult, error) {
	command, args := commandFor(req)
	if command == "" {
		return InvokeResult{}, &httputil.Error{Code: CodeCommandNotAllowed}
	}
	if !c.allowed(command) {
		return InvokeResult{}, &httputil.Error{Code: CodeCommandNotAllowed}
	}

	ctx, cancel := boundedContext(ctx, c.opts.CLITimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return InvokeResult{}, &httputil.Error{Code: CodeAdapterTimeout}
	}

	stdout := "{\"command\":\"" + command + "\",\"args\":" + quoteArgs(args) + ",\"exit_code\":0,\"digest\":\"" + digest(command + " " + strings.Join(args, " "))[:16] + "\"}"
	stdout, truncated := truncate(stdout, c.opts.MaxOutputBytes)
	out := "cli=" + command + " exit=0 truncated=" + boolString(truncated)
	data := map[string]any{
		"kind":      string(registry.KindCLI),
		"resource":  req.Entry.Name,
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

func (c *cliConnector) Health(ctx context.Context, e *registry.Entry) (registry.Health, error) {
	return mockHealth(e), nil
}

func (c *cliConnector) allowed(command string) bool {
	for _, a := range c.opts.CLIAllowlist {
		if strings.EqualFold(strings.TrimSpace(a), command) {
			return true
		}
	}
	return false
}

// commandFor resolves the command line: explicit request fields win, otherwise
// the prompt's first token is the command and the rest are arguments.
func commandFor(req InvokeRequest) (string, []string) {
	command := strings.TrimSpace(req.Command)
	if command == "" {
		command = strings.TrimSpace(metaString(req.Metadata, "command"))
	}
	args := req.Args
	if command == "" {
		fields := strings.Fields(req.Prompt)
		if len(fields) == 0 {
			return "", nil
		}
		command = fields[0]
		args = fields[1:]
	}
	if args == nil {
		args = []string{}
	}
	return command, args
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
