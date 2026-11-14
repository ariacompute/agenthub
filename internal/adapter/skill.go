package adapter

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"agenthub/internal/a2a"
	"agenthub/internal/registry"
)

// skillConnector loads a "job manual": trigger, ordered steps, knowledge and the
// tool list the manual expects. A skill is versioned process knowledge, not an
// ad-hoc prompt.
//
// Health is implemented: a file:// endpoint names the local skill directory and
// is measured directly (present with SKILL.md → healthy; otherwise → unhealthy).
// Invoke still stages the steps without reading an actual manual.
// TODO(real): read the manual from the resource endpoint (or a local directory)
// and execute its declared steps through the matching connectors.
type skillConnector struct{}

func (c *skillConnector) Kind() registry.Kind { return registry.KindSkill }

func (c *skillConnector) Invoke(ctx context.Context, req InvokeRequest) (InvokeResult, error) {
	if err := ctx.Err(); err != nil {
		return InvokeResult{}, err
	}
	steps := []string{
		"match_trigger",
		"load_manual",
		"collect_arguments",
		"execute_steps",
		"report_result",
	}
	if len(req.Entry.Capabilities) > 0 {
		steps = append(steps, "apply_capability:"+req.Entry.Capabilities[pick(req.Prompt, len(req.Entry.Capabilities))])
	}
	out := "skill=" + req.Entry.Name + " steps=" + itoa(len(steps)) + " digest=" + digest(req.Prompt)[:16]
	data := map[string]any{
		"kind":     string(registry.KindSkill),
		"resource": req.Entry.Name,
		"provider": MockProviderName,
		"version":  req.Entry.Version,
		"trigger":  req.Prompt,
		"steps":    steps,
		"tools":    req.Entry.Capabilities,
		"digest":   digest(req.Prompt)[:16],
	}
	return InvokeResult{
		Text:      out,
		Parts:     []a2a.Part{a2a.NewTextPart(out), a2a.NewDataPart(data)},
		Artifacts: []a2a.Artifact{artifact("skill.manual", 0, []a2a.Part{a2a.NewDataPart(data)}, "kind", string(registry.KindSkill), "provider", MockProviderName)},
	}, nil
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
		return probeHTTP(ctx, http.MethodGet, httpEndpoint(endpoint), nil, nil)
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
