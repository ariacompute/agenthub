package sandbox

import (
	"context"
	"encoding/json"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// DeclarativeSpec is one operator-supplied provider, read from
// AGENTHUB_SANDBOX_PROVIDERS. It exists so that adding nsjail, isolate, gVisor
// or an internal wrapper does not need a Go release.
//
// The safety boundary is here, not in a comment: templates are expanded one argv
// element at a time (never concatenated into a shell string), the placeholder
// vocabulary is closed, and an unknown placeholder fails at load time so a typo
// cannot silently become a literal "{command}" passed to a binary.
type DeclarativeSpec struct {
	Name        string     `json:"name"`
	Layer       string     `json:"layer"`
	Isolation   string     `json:"isolation"`
	Binary      string     `json:"binary"`
	License     string     `json:"license,omitempty"`
	DocsURL     string     `json:"docsUrl,omitempty"`
	ProbeArgs   [][]string `json:"probeArgs,omitempty"`
	CreateArgs  [][]string `json:"createArgs,omitempty"`
	ExecArgs    [][]string `json:"execArgs"`
	DestroyArgs [][]string `json:"destroyArgs,omitempty"`
}

// DeclarativeProvider runs an operator-defined sandbox tool.
type DeclarativeProvider struct {
	spec    DeclarativeSpec
	probe   []string
	create  []string
	execPre []string
	execSuf []string
	destroy []string
}

// Placeholders is the closed vocabulary. {command} and {arg} are positional;
// everything else describes the policy.
var placeholders = map[string]bool{
	"workdir": true, "image": true, "cpu": true, "memory_mb": true, "pids": true,
	"network": true, "timeout_s": true, "command": true, "arg": true,
	"lease_id": true, "owner_id": true, "resource_id": true, "scope": true,
}

var placeholderPattern = regexp.MustCompile(`\{([a-zA-Z0-9_:]+)\}`)

// NewDeclarativeProvider validates a spec and returns a usable provider. Every
// failure here is a startup failure: a half-valid template would only show up as
// a confusing execution error later.
func NewDeclarativeProvider(spec DeclarativeSpec) (*DeclarativeProvider, error) {
	spec.Name = strings.TrimSpace(spec.Name)
	if spec.Name == "" || spec.Name == ProviderOff || spec.Name == ProviderMock {
		return nil, Error(CodePolicyDenied)
	}
	bin := strings.TrimSpace(spec.Binary)
	if bin == "" || !filepath.IsAbs(bin) {
		// A bare name would be resolved through PATH, which an attacker who can
		// write to PATH controls. Absolute or nothing.
		return nil, Error(CodePolicyDenied)
	}
	if len(spec.ExecArgs) == 0 || len(spec.ExecArgs) > 2 {
		// One segment (the whole command line, args appended verbatim) or two
		// (prefix containing {command}, suffix expanding {arg} once per argument).
		return nil, Error(CodePolicyDenied)
	}
	p := &DeclarativeProvider{spec: spec}
	var err error
	if p.probe, err = singleArgv(spec.ProbeArgs); err != nil {
		return nil, err
	}
	if p.create, err = singleArgv(spec.CreateArgs); err != nil {
		return nil, err
	}
	if p.destroy, err = singleArgv(spec.DestroyArgs); err != nil {
		return nil, err
	}
	if p.execPre, err = validateArgv(spec.ExecArgs[0]); err != nil {
		return nil, err
	}
	if len(spec.ExecArgs) == 2 {
		if p.execSuf, err = validateArgv(spec.ExecArgs[1]); err != nil {
			return nil, err
		}
	}
	return p, nil
}

// Spec exposes the definition, for the catalogue and for tests.
func (d *DeclarativeProvider) Spec() DeclarativeSpec { return d.spec }

// Name is the catalogue key.
func (d *DeclarativeProvider) Name() string { return d.spec.Name }

// Describe reports what the operator declared.
func (d *DeclarativeProvider) Describe(context.Context) (Descriptor, error) {
	layer := Layer(d.spec.Layer)
	if layer == "" {
		layer = LayerProcess
	}
	isolation := d.spec.Isolation
	if isolation == "" {
		isolation = IsolationNamespace
	}
	licence := d.spec.License
	if licence == "" {
		licence = "unknown"
	}
	return Descriptor{
		Name:      d.spec.Name,
		Layer:     layer,
		Isolation: isolation,
		Requires:  []string{d.spec.Binary},
		Source:    SourceDeclarative,
		License:   licence,
		DocsURL:   d.spec.DocsURL,
	}, nil
}

// Probe runs the declared probe argv, if any. No probe means the operator
// asserts the binary is there; the first execution will say otherwise.
func (d *DeclarativeProvider) Probe(ctx context.Context) (Availability, error) {
	if len(d.probe) == 0 {
		return Availability{Available: true, Reason: "declarative provider: no probe configured"}, nil
	}
	res, err := runExternal(ctx, runSpec{
		bin:     d.spec.Binary,
		args:    d.probe,
		timeout: 2 * time.Second,
		cap:     2 << 10,
	})
	if err != nil || res.exit != 0 {
		return Availability{Available: false, Reason: d.spec.Binary + " probe failed"}, nil
	}
	return Availability{Available: true, Reason: strings.TrimSpace(res.stdout)}, nil
}

// Create runs the create argv, or records the lease id when there is nothing to
// create (a process sandbox wraps one command at a time).
func (d *DeclarativeProvider) Create(ctx context.Context, spec Spec) (Handle, error) {
	h := Handle{ID: spec.LeaseID, Provider: d.spec.Name, Ref: spec.LeaseID, Spec: spec}
	if len(d.create) == 0 {
		return h, nil
	}
	argv, err := expandArgv(d.create, varsFor(spec, "", nil))
	if err != nil {
		return Handle{}, err
	}
	res, err := runExternal(ctx, runSpec{
		bin:     d.spec.Binary,
		args:    argv,
		timeout: 30 * time.Second,
		cap:     4 << 10,
	})
	if err != nil {
		return Handle{}, err
	}
	if ref := strings.TrimSpace(res.stdout); ref != "" {
		h.Ref = ref
	}
	return h, nil
}

// Exec expands and runs the exec argv.
func (d *DeclarativeProvider) Exec(ctx context.Context, h Handle, req ExecRequest) (ExecResult, error) {
	spec := h.Spec
	argv, err := expandArgv(d.execPre, varsFor(spec, req.Command, req.Env))
	if err != nil {
		return ExecResult{}, err
	}
	if len(d.execSuf) > 0 {
		for _, arg := range req.Args {
			expanded, err := expandArgv(d.execSuf, varsFor(spec, req.Command, req.Env), arg)
			if err != nil {
				return ExecResult{}, err
			}
			argv = append(argv, expanded...)
		}
	} else {
		argv = append(argv, req.Args...)
	}

	res, err := runExternal(ctx, runSpec{
		bin:     d.spec.Binary,
		args:    argv,
		dir:     req.Dir,
		stdin:   req.Stdin,
		timeout: requestTimeout(req),
		cap:     req.OutputCap(),
	})
	out := ExecResult{
		ExitCode:   res.exit,
		Stdout:     res.stdout,
		Stderr:     res.stderr,
		Truncated:  res.truncated,
		DurationMS: res.duration.Milliseconds(),
		Provider:   d.spec.Name,
		Ref:        h.Ref,
		Receipt:    "exit",
	}
	if err != nil {
		return out, err
	}
	return withReceipt(out), nil
}

// Attach is refused: the declarative contract is one command at a time, and
// pretending otherwise would break the MCP session in ways that are hard to see.
func (d *DeclarativeProvider) Attach(context.Context, Handle, ExecRequest) (Session, error) {
	return nil, Error(CodeUnsupported)
}

// Destroy runs the destroy argv when one is configured.
func (d *DeclarativeProvider) Destroy(ctx context.Context, h Handle) error {
	if len(d.destroy) == 0 {
		return nil
	}
	argv, err := expandArgv(d.destroy, varsFor(h.Spec, "", nil))
	if err != nil {
		return err
	}
	_, _ = runExternal(ctx, runSpec{
		bin:     d.spec.Binary,
		args:    argv,
		timeout: 15 * time.Second,
		cap:     2 << 10,
	})
	return nil
}

// Status cannot be known without a status argv, so it reports ready and lets the
// next Exec be the truth.
func (d *DeclarativeProvider) Status(context.Context, Handle) (string, error) {
	return StateReady, nil
}

// withReceipt upgrades a plain exit-code result to a structured one when the
// tool answered with JSON. Anything unparsable is marked as such rather than
// being silently treated as plain text.
func withReceipt(out ExecResult) ExecResult {
	trimmed := strings.TrimSpace(out.Stdout)
	if !strings.HasPrefix(trimmed, "{") {
		return out
	}
	var payload struct {
		ExitCode   *int   `json:"exit_code"`
		Stdout     string `json:"stdout"`
		Stderr     string `json:"stderr"`
		Truncated  bool   `json:"truncated"`
		DurationMS int64  `json:"duration_ms"`
	}
	if err := json.Unmarshal([]byte(trimmed), &payload); err != nil {
		out.Receipt = "unparsed"
		return out
	}
	if payload.ExitCode != nil {
		out.ExitCode = *payload.ExitCode
	}
	if payload.Stdout != "" {
		out.Stdout = payload.Stdout
	}
	if payload.Stderr != "" {
		out.Stderr = payload.Stderr
	}
	out.Truncated = out.Truncated || payload.Truncated
	if payload.DurationMS > 0 {
		out.DurationMS = payload.DurationMS
	}
	out.Receipt = "json"
	return out
}

// varsFor builds the substitution table.
func varsFor(spec Spec, command string, env map[string]string) map[string]string {
	p := spec.Profile
	vars := map[string]string{
		"workdir":     p.WorkDir,
		"image":       p.Image,
		"cpu":         strconv.FormatFloat(p.CPU, 'f', -1, 64),
		"memory_mb":   strconv.Itoa(p.MemoryMB),
		"pids":        strconv.Itoa(p.PidsLimit),
		"network":     NormalizeNetwork(p.Network),
		"timeout_s":   strconv.Itoa(int(p.Timeout().Seconds())),
		"command":     command,
		"lease_id":    spec.LeaseID,
		"owner_id":    spec.OwnerID,
		"resource_id": spec.ResourceID,
		"scope":       spec.Scope,
	}
	for name, value := range env {
		vars["env:"+name] = value
	}
	return vars
}

// expandArgv expands every element, substituting {arg} with the current argument.
func expandArgv(tmpl []string, vars map[string]string, args ...string) ([]string, error) {
	arg := ""
	if len(args) > 0 {
		arg = args[0]
	}
	if a, ok := vars["arg"]; ok {
		arg = a
	}
	out := make([]string, 0, len(tmpl))
	for _, element := range tmpl {
		expanded, err := expandElement(element, vars, arg)
		if err != nil {
			return nil, err
		}
		out = append(out, expanded)
	}
	return out, nil
}

// expandElement substitutes placeholders inside one argv element. An unresolved
// placeholder is a load-time bug brought forward to run time, so it fails.
func expandElement(element string, vars map[string]string, arg string) (string, error) {
	var failure error
	out := placeholderPattern.ReplaceAllStringFunc(element, func(token string) string {
		name := strings.TrimSuffix(strings.TrimPrefix(token, "{"), "}")
		if name == "arg" {
			return arg
		}
		value, ok := vars[name]
		if !ok {
			failure = Error(CodePolicyDenied)
			return token
		}
		return value
	})
	if failure != nil {
		return "", failure
	}
	return out, nil
}

// validateArgv checks the placeholder vocabulary at load time.
func validateArgv(argv []string) ([]string, error) {
	if len(argv) == 0 {
		return nil, Error(CodePolicyDenied)
	}
	for _, element := range argv {
		for _, match := range placeholderPattern.FindAllStringSubmatch(element, -1) {
			name := match[1]
			if strings.HasPrefix(name, "env:") {
				if len(strings.TrimPrefix(name, "env:")) == 0 {
					return nil, Error(CodePolicyDenied)
				}
				continue
			}
			if !placeholders[name] {
				return nil, Error(CodePolicyDenied)
			}
		}
	}
	return argv, nil
}

func singleArgv(templates [][]string) ([]string, error) {
	if len(templates) == 0 {
		return nil, nil
	}
	// Only the first template is used for probe/create/destroy: one invocation is
	// the contract, and extra entries would be dead configuration nobody tests.
	return validateArgv(templates[0])
}
