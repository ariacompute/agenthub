package adapter

import (
	"sort"
	"strings"

	"agenthub/internal/registry"
	"agenthub/internal/sandbox"
)

// leaseProfile resolves the execution policy an entry asks for.
//
// The sandbox block is the contract; the older per-kind blocks still contribute
// their timeouts and output caps so an entry written before the sandbox existed
// keeps behaving the way it was configured. Merging here, once, is what keeps the
// connectors from each inventing their own precedence.
func leaseProfile(e *registry.Entry) sandbox.Profile {
	if e == nil || e.KindConfig == nil {
		return sandbox.Profile{}
	}
	var profile sandbox.Profile
	if e.KindConfig.Sandbox != nil {
		profile = e.KindConfig.Sandbox.Profile()
	}
	switch {
	case e.KindConfig.CLI != nil:
		cli := e.KindConfig.CLI
		if profile.TimeoutMS == 0 {
			profile.TimeoutMS = cli.TimeoutMS
		}
		if profile.MaxOutputBytes == 0 {
			profile.MaxOutputBytes = cli.MaxOutputBytes
		}
		if profile.WorkDir == "" {
			profile.WorkDir = strings.TrimSpace(cli.WorkDir)
		}
		if len(profile.EnvAllow) == 0 && len(cli.Env) > 0 {
			profile.EnvAllow = sortedKeys(cli.Env)
		}
	case e.KindConfig.MCP != nil:
		mcp := e.KindConfig.MCP
		if len(profile.EnvAllow) == 0 && len(mcp.Env) > 0 {
			profile.EnvAllow = sortedKeys(mcp.Env)
		}
	}
	return profile
}

// argsPolicy resolves what a caller may pass to a CLI resource.
func argsPolicy(e *registry.Entry) string {
	if e == nil || e.KindConfig == nil {
		return sandbox.ArgsNone
	}
	if e.KindConfig.Sandbox != nil {
		if policy := strings.TrimSpace(e.KindConfig.Sandbox.ArgsPolicy); policy != "" {
			return sandbox.NormalizeArgsPolicy(policy)
		}
	}
	if e.KindConfig.CLI != nil {
		return sandbox.NormalizeArgsPolicy(e.KindConfig.CLI.ArgsPolicy)
	}
	return sandbox.ArgsNone
}

// argsAllowlist merges the two places an operator may declare argument prefixes.
func argsAllowlist(e *registry.Entry) []string {
	if e == nil || e.KindConfig == nil {
		return nil
	}
	var out []string
	if e.KindConfig.Sandbox != nil {
		out = append(out, e.KindConfig.Sandbox.ArgsAllowlist...)
	}
	if e.KindConfig.CLI != nil {
		out = append(out, e.KindConfig.CLI.ArgsAllowlist...)
	}
	return out
}

// policyProfile is the profile used for policy decisions, with the argument
// vocabulary resolved from both blocks.
func policyProfile(e *registry.Entry) sandbox.Profile {
	profile := leaseProfile(e)
	profile.ArgsPolicy = argsPolicy(e)
	profile.ArgsAllow = argsAllowlist(e)
	return profile
}

// envForJob keeps only the variables the profile allows, so an MCP server does
// not inherit the agenthub process's environment by accident.
func envForJob(e *registry.Entry) map[string]string {
	if e == nil || e.KindConfig == nil || e.KindConfig.MCP == nil {
		return nil
	}
	allowed := map[string]bool{}
	if e.KindConfig.Sandbox != nil {
		for _, name := range e.KindConfig.Sandbox.EnvAllow {
			allowed[name] = true
		}
	}
	out := map[string]string{}
	for name, value := range e.KindConfig.MCP.Env {
		if allowed[name] {
			out[name] = value
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
