package adapter

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"agenthub/internal/a2a"
	"agenthub/internal/httputil"
	"agenthub/internal/netutil"
	"agenthub/internal/registry"
)

// A skill is versioned process knowledge, not an ad-hoc prompt: it is a manual
// with a trigger, ordered steps and the tool list those steps expect.
//
// Invoke reads the manual and works through its declared steps locally. It
// deliberately does not recurse into the other connectors: a skill that called
// an agent which ran a skill would be a loop whose cost nobody can bound, and
// the step plan is the part a caller can inspect and assert on. Health measures
// the skill directory (file://) or the endpoint (http), and anything the adapter
// cannot reach stays unknown.
const (
	maxSkillManualBytes = 256 << 10
	maxSkillSteps       = 50
	maxSkillStepChars   = 200
)

// skillStepLine accepts the two shapes a SKILL.md uses for an ordered step: a
// numbered item ("1." / "1)") or a bullet ("-", "*", "+").
var skillStepLine = regexp.MustCompile(`^\s*(?:[-*+]|\d+[.)])\s+(.+?)\s*$`)

type skillConnector struct {
	client *http.Client
}

func newSkillConnector() *skillConnector {
	return &skillConnector{
		client: netutil.SafeClientWithAllowlist(DefaultModelTimeout, DefaultModelMaxBytes, nil),
	}
}

func (c *skillConnector) Kind() registry.Kind { return registry.KindSkill }

func (c *skillConnector) Invoke(ctx context.Context, req InvokeRequest) (InvokeResult, error) {
	if err := ctx.Err(); err != nil {
		return InvokeResult{}, err
	}
	manual, err := c.loadManual(ctx, &req.Entry)
	if err != nil {
		// An endpoint nobody serves is not a broken skill: like the MCP
		// connector, keep the deterministic step plan the offline catalogue
		// promises. A manual that is oversized is different — that record is
		// malformed and the caller has to hear about it.
		if httputil.AsError(err).Code != CodeResourceUnreachable {
			return InvokeResult{}, err
		}
		manual = nil
	}
	steps := skillDeclaredSteps(&req.Entry)
	if len(steps) == 0 && manual != nil {
		steps = manual.Steps
	}
	if len(steps) == 0 {
		steps = defaultSkillSteps()
	}
	steps = append(steps, capabilityStep(req.Entry.Capabilities, req.Prompt)...)

	provider := MockProviderName
	manualDigest := ""
	manualBytes := 0
	manualTitle := ""
	if manual != nil {
		provider = "skill:manual"
		manualDigest = digest(manual.Body)[:16]
		manualBytes = len(manual.Body)
		manualTitle = manual.Title
	}

	out := "skill=" + req.Entry.Name + " steps=" + itoa(len(steps)) + " provider=" + provider + " digest=" + digest(req.Prompt)[:16]
	data := map[string]any{
		"kind":     string(registry.KindSkill),
		"resource": req.Entry.Name,
		"provider": provider,
		"version":  req.Entry.Version,
		"trigger":  req.Prompt,
		"steps":    steps,
		"tools":    req.Entry.Capabilities,
		"digest":   digest(req.Prompt)[:16],
	}
	if manual != nil {
		data["manual_digest"] = manualDigest
		data["manual_bytes"] = manualBytes
		if manualTitle != "" {
			data["manual_title"] = manualTitle
		}
	}

	artifacts := make([]a2a.Artifact, 0, len(steps)+1)
	if manual != nil {
		// One artifact per resolved step, so a consumer can show progress
		// without re-parsing the manual.
		for i, step := range steps {
			stepData := map[string]any{
				"step":   i + 1,
				"action": step,
				"status": "resolved",
				"digest": digest(step)[:16],
			}
			artifacts = append(artifacts, artifact("skill.step", i, []a2a.Part{a2a.NewDataPart(stepData)},
				"kind", string(registry.KindSkill), "provider", provider))
		}
	}
	artifacts = append(artifacts, artifact("skill.manual", 0, []a2a.Part{a2a.NewDataPart(data)},
		"kind", string(registry.KindSkill), "provider", provider))
	return InvokeResult{
		Text:      out,
		Parts:     []a2a.Part{a2a.NewTextPart(out), a2a.NewDataPart(data)},
		Artifacts: artifacts,
	}, nil
}

func defaultSkillSteps() []string {
	return []string{
		"match_trigger",
		"load_manual",
		"collect_arguments",
		"execute_steps",
		"report_result",
	}
}

// capabilityStep keeps the historical behaviour: a request that reaches a skill
// with capabilities adds one deterministic step that names the one it applies.
func capabilityStep(capabilities []string, prompt string) []string {
	if len(capabilities) == 0 {
		return nil
	}
	return []string{"apply_capability:" + capabilities[pick(prompt, len(capabilities))]}
}

func skillDeclaredSteps(e *registry.Entry) []string {
	if e == nil || e.KindConfig == nil || e.KindConfig.Skill == nil {
		return nil
	}
	var out []string
	for _, s := range e.KindConfig.Skill.Steps {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
		if len(out) >= maxSkillSteps {
			break
		}
	}
	return out
}

// skillManual is the part of a manual the connector uses: a title for the
// artifact, the ordered steps it declares, and the raw body for the digest.
type skillManual struct {
	Title string
	Steps []string
	Body  string
}

// loadManual reads the manual from the place the record points at: a local
// directory (file://) or the endpoint itself (http/s). A record with no
// endpoint has no manual, which is not an error — the offline step plan is the
// answer. A record that does point somewhere and cannot be read is an error,
// because silently mocking it would hide a broken skill.
func (c *skillConnector) loadManual(ctx context.Context, e *registry.Entry) (*skillManual, error) {
	if e == nil || strings.TrimSpace(e.Endpoint) == "" {
		return nil, nil
	}
	endpoint := strings.TrimSpace(e.Endpoint)
	if strings.HasPrefix(endpoint, "file://") {
		dir, err := localSkillDir(endpoint)
		if err != nil || !skillManualPresent(dir) {
			return nil, nil
		}
		raw, err := readSkillFile(filepath.Join(dir, "SKILL.md"))
		if err != nil {
			return nil, err
		}
		return parseSkillManual(raw), nil
	}
	if !isHTTP(endpoint) {
		return nil, nil
	}
	client := c.client
	if client == nil {
		client = newSkillConnector().client
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, httpEndpoint(endpoint), nil)
	if err != nil {
		return nil, &httputil.Error{Code: CodeResourceUnreachable}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, &httputil.Error{Code: CodeResourceUnreachable}
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &httputil.Error{Code: CodeResourceUnreachable}
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxSkillManualBytes+1))
	if err != nil {
		return nil, &httputil.Error{Code: CodeResourceUnreachable}
	}
	if len(raw) > maxSkillManualBytes {
		return nil, &httputil.Error{Code: CodeProviderMalformed}
	}
	return parseSkillManual(string(raw)), nil
}

func readSkillFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", &httputil.Error{Code: CodeResourceUnreachable}
	}
	defer func() { _ = f.Close() }()
	raw, err := io.ReadAll(io.LimitReader(f, maxSkillManualBytes+1))
	if err != nil {
		return "", &httputil.Error{Code: CodeResourceUnreachable}
	}
	if len(raw) > maxSkillManualBytes {
		return "", &httputil.Error{Code: CodeProviderMalformed}
	}
	return string(raw), nil
}

// parseSkillManual extracts the title and ordered steps. The parser is
// deliberately literal: it reads the same markdown a human reads, and anything
// it cannot classify simply does not become a step.
func parseSkillManual(body string) *skillManual {
	manual := &skillManual{Body: body}
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimRight(line, "\r")
		trimmed := strings.TrimSpace(line)
		if manual.Title == "" && strings.HasPrefix(trimmed, "# ") {
			manual.Title = strings.TrimSpace(strings.TrimPrefix(trimmed, "# "))
		}
		m := skillStepLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		step := strings.TrimSpace(m[1])
		step = strings.TrimSpace(strings.TrimPrefix(step, "[ ]"))
		step = strings.TrimSpace(strings.TrimPrefix(step, "[x]"))
		step = strings.TrimSpace(strings.TrimPrefix(step, "[X]"))
		if step == "" {
			continue
		}
		step, _ = truncate(step, maxSkillStepChars)
		manual.Steps = append(manual.Steps, step)
		if len(manual.Steps) >= maxSkillSteps {
			break
		}
	}
	return manual
}

// Health measures a skill the way the connector can: a file:// endpoint names
// the local skill directory and is read directly, an http endpoint is probed
// like any other, and anything else is an address the adapter cannot reach, so
// it stays unknown. A skill with no endpoint has nothing to measure, hence
// unknown rather than a healthy nobody checked.
func (c *skillConnector) Health(ctx context.Context, e *registry.Entry) (registry.Health, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if e == nil || strings.TrimSpace(e.Endpoint) == "" {
		return nothingMeasured, nil
	}
	endpoint := strings.TrimSpace(e.Endpoint)
	if isHTTP(endpoint) {
		return probeHTTP(ctx, http.DefaultClient, http.MethodGet, httpEndpoint(endpoint), nil, nil)
	}
	if strings.HasPrefix(endpoint, "file://") {
		dir, err := localSkillDir(endpoint)
		if err != nil {
			return nothingMeasured, nil
		}
		if skillManualPresent(dir) {
			return registry.HealthHealthy, nil
		}
		return registry.HealthUnhealthy, nil
	}
	return nothingMeasured, nil
}

// localSkillDir turns a file:// endpoint into a filesystem path. file paths are
// not dialable, so this is only used to read the local skill directory; a parse
// error returns an empty path so the caller answers unknown instead of guessing.
func localSkillDir(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if !strings.HasPrefix(trimmed, "file://") {
		return "", errors.New("not a file:// endpoint")
	}
	rest := strings.TrimPrefix(trimmed, "file://")
	// A Windows path arrives as file://C:\dir (backslashes are not URL
	// separators), which url.Parse rejects as an invalid host. Recognising the
	// drive letter up front keeps the local manual readable on every platform.
	if len(rest) >= 2 && rest[1] == ':' {
		return filepath.FromSlash(rest), nil
	}
	u, err := url.Parse(trimmed)
	if err != nil {
		return "", err
	}
	p := u.Path
	// On Windows file:///C:/x parses to Path "/C:/x"; drop the leading slash so
	// it resolves as C:\x. Other platforms keep the absolute path untouched.
	if runtime.GOOS == "windows" && len(p) >= 3 && p[0] == '/' && p[2] == ':' {
		p = p[1:]
	}
	return filepath.FromSlash(p), nil
}

// skillManualPresent reports whether dir is a directory that holds a SKILL.md
// file — the shape a skill is required to have.
func skillManualPresent(dir string) bool {
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return false
	}
	md, err := os.Stat(filepath.Join(dir, "SKILL.md"))
	return err == nil && !md.IsDir()
}
