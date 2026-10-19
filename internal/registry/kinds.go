package registry

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"agenthub/internal/httputil"
)

// CodeInvalidKindConfig is returned when the per-kind configuration block does
// not match the shape its kind promises. The console renders the code next to
// the offending field, so it has to be machine readable rather than prose.
const CodeInvalidKindConfig = httputil.Code("invalid_kind_config")

// Provider modes a model resource can run in.
const (
	// ProviderModeMock is the deterministic offline provider: no network, no
	// credentials, the same prompt always renders the same completion.
	ProviderModeMock = "mock"
	// ProviderModeOpenAICompatible points the resource at an OpenAI compatible
	// chat completions endpoint, which is what a model router (LiteLLM,
	// OpenRouter, a self hosted gateway) exposes.
	ProviderModeOpenAICompatible = "openai_compatible"
)

// ProviderModes lists every accepted provider mode.
var ProviderModes = []string{ProviderModeMock, ProviderModeOpenAICompatible}

// Agent capabilities an Agent Card may advertise.
const (
	AgentCapStreaming              = "streaming"
	AgentCapPushNotifications      = "pushNotifications"
	AgentCapStateTransitionHistory = "stateTransitionHistory"
)

// AgentCapabilities lists the capability keys an agent resource may declare.
var AgentCapabilities = []string{AgentCapStreaming, AgentCapPushNotifications, AgentCapStateTransitionHistory}

// KindConfig carries the configuration that only makes sense for one kind.
//
// The generic columns (name / endpoint / version / tags …) stay on Entry so
// discovery keeps working across kinds; everything type specific lives here and
// is persisted as one jsonb column. AWS models the same split: a record has a
// generic envelope plus a resource type specific descriptor.
type KindConfig struct {
	Model  *ModelConfig  `json:"model,omitempty"`
	Agent  *AgentConfig  `json:"agent,omitempty"`
	MCP    *MCPConfig    `json:"mcp,omitempty"`
	Skill  *SkillConfig  `json:"skill,omitempty"`
	CLI    *CLIConfig    `json:"cli,omitempty"`
	Custom *CustomConfig `json:"custom,omitempty"`
}

// ModelTarget is one hop of the fallback chain.
type ModelTarget struct {
	Name   string `json:"name"`
	Model  string `json:"model,omitempty"`
	Weight int    `json:"weight,omitempty"`
}

// ModelConfig is the model specific descriptor. ProviderMode decides whether
// the resource talks to a real router or stays offline; Targets is the ordered
// fallback chain the router walks until one provider answers.
type ModelConfig struct {
	ProviderMode string `json:"provider_mode,omitempty"`
	Endpoint     string `json:"endpoint,omitempty"`
	ModelID      string `json:"model_id,omitempty"`
	// APIKeyEnv names the environment variable holding the credential. The
	// secret itself is never stored, logged or returned.
	APIKeyEnv string        `json:"api_key_env,omitempty"`
	Targets   []ModelTarget `json:"targets,omitempty"`
	TimeoutMS int           `json:"timeout_ms,omitempty"`
	// FallbackMock appends the offline provider at the end of the chain. It is
	// off by default on purpose: answering a failed real call with a mock
	// completion would be a fabricated result, so it has to be switched on
	// deliberately for a walkthrough or a test.
	FallbackMock bool `json:"fallback_mock,omitempty"`
}

// AgentConfig is the A2A Agent Card descriptor.
type AgentConfig struct {
	CardURL         string   `json:"card_url,omitempty"`
	ProtocolVersion string   `json:"protocol_version,omitempty"`
	Capabilities    []string `json:"capabilities,omitempty"`
	Skills          []string `json:"skills,omitempty"`
	AuthScheme      string   `json:"auth_scheme,omitempty"`
}

// MCPConfig is the MCP server descriptor: transport decides whether the server
// is reached through a stdio command or over HTTP.
type MCPConfig struct {
	Transport     string            `json:"transport,omitempty"`
	Command       string            `json:"command,omitempty"`
	Args          []string          `json:"args,omitempty"`
	URL           string            `json:"url,omitempty"`
	ToolAllowlist []string          `json:"tool_allowlist,omitempty"`
	AuthSchemes   []string          `json:"auth_schemes,omitempty"`
	Env           map[string]string `json:"env,omitempty"`
}

// SkillConfig is the "job manual" descriptor: what triggers it, which steps it
// runs, which tools it expects, and the markdown documentation.
type SkillConfig struct {
	Version    string   `json:"version,omitempty"`
	Repository string   `json:"repository,omitempty"`
	Package    string   `json:"package,omitempty"`
	Triggers   []string `json:"triggers,omitempty"`
	Steps      []string `json:"steps,omitempty"`
	Tools      []string `json:"tools,omitempty"`
	Docs       string   `json:"docs,omitempty"`
}

// CLIConfig is the command descriptor. The allowlist, timeout and output cap
// are the safety rails the CLI adapter enforces at run time.
type CLIConfig struct {
	Command        string            `json:"command,omitempty"`
	Allowlist      []string          `json:"allowlist,omitempty"`
	TimeoutMS      int               `json:"timeout_ms,omitempty"`
	MaxOutputBytes int               `json:"max_output_bytes,omitempty"`
	WorkDir        string            `json:"work_dir,omitempty"`
	Env            map[string]string `json:"env,omitempty"`
}

// CustomConfig holds an arbitrary, caller defined JSON schema. It is the
// agenthub equivalent of the AWS CUSTOM record type: anything that is not a
// model, agent, MCP server, skill or CLI.
type CustomConfig struct {
	Schema string `json:"schema,omitempty"`
}

// EffectiveProviderMode returns the provider mode, defaulting to the offline
// mock so an entry registered before the field existed keeps behaving exactly
// as it did.
func (c *ModelConfig) EffectiveProviderMode() string {
	if c == nil || strings.TrimSpace(c.ProviderMode) == "" {
		return ProviderModeMock
	}
	return strings.ToLower(strings.TrimSpace(c.ProviderMode))
}

// EffectiveTransport resolves the MCP transport: the kind config wins, the
// generic entry column is the fallback.
func (c *MCPConfig) EffectiveTransport(e *Entry) Transport {
	if c != nil && strings.TrimSpace(c.Transport) != "" {
		if t, err := ParseTransport(c.Transport); err == nil {
			return t
		}
	}
	if e != nil && e.Transport != "" {
		return e.Transport
	}
	return TransportStreamableHTTP
}

// EffectiveEndpoint resolves the endpoint a model provider should call: the
// kind config endpoint first, the generic entry endpoint second.
func (c *ModelConfig) EffectiveEndpoint(e *Entry) string {
	if c != nil && strings.TrimSpace(c.Endpoint) != "" {
		return strings.TrimSpace(c.Endpoint)
	}
	if e != nil {
		return strings.TrimSpace(e.Endpoint)
	}
	return ""
}

// EffectiveModelID falls back to the entry version so an operator who only
// filled the generic fields still gets a usable model name.
func (c *ModelConfig) EffectiveModelID(e *Entry) string {
	if c != nil && strings.TrimSpace(c.ModelID) != "" {
		return strings.TrimSpace(c.ModelID)
	}
	if e != nil {
		if strings.TrimSpace(e.Version) != "" {
			return strings.TrimSpace(e.Version)
		}
		return strings.TrimSpace(e.Name)
	}
	return ""
}

// ValidateKind checks the per-kind configuration block of an entry. A nil block
// is always valid: the generic columns alone are enough to register a resource,
// which is what every entry created before this existed looks like.
func ValidateKind(e *Entry) error {
	if e == nil {
		return &httputil.Error{Code: CodeInvalidEntry}
	}
	if e.KindConfig == nil {
		return nil
	}
	switch e.Kind {
	case KindModel:
		return validateModel(e.KindConfig.Model)
	case KindAgent:
		return validateAgent(e.KindConfig.Agent)
	case KindMCP:
		return validateMCP(e.KindConfig.MCP, e)
	case KindSkill:
		return validateSkill(e.KindConfig.Skill)
	case KindCLI:
		return validateCLI(e.KindConfig.CLI)
	case KindCustom:
		return validateCustom(e.KindConfig.Custom)
	default:
		return &httputil.Error{Code: CodeInvalidKind}
	}
}

func kindConfigErr(field string) error {
	return newFieldError(CodeInvalidKindConfig, field)
}

func validateModel(c *ModelConfig) error {
	if c == nil {
		return nil
	}
	mode := c.EffectiveProviderMode()
	known := false
	for _, m := range ProviderModes {
		if mode == m {
			known = true
			break
		}
	}
	if !known {
		return kindConfigErr("provider_mode")
	}
	if mode == ProviderModeOpenAICompatible {
		if strings.TrimSpace(c.Endpoint) == "" {
			return kindConfigErr("endpoint")
		}
		if !isHTTPURL(c.Endpoint) {
			return kindConfigErr("endpoint")
		}
		if strings.TrimSpace(c.ModelID) == "" {
			return kindConfigErr("model_id")
		}
	}
	if strings.TrimSpace(c.APIKeyEnv) != "" && !validEnvName(c.APIKeyEnv) {
		return kindConfigErr("api_key_env")
	}
	if c.TimeoutMS < 0 {
		return kindConfigErr("timeout_ms")
	}
	for _, t := range c.Targets {
		if strings.TrimSpace(t.Name) == "" {
			return kindConfigErr("targets.name")
		}
		if t.Weight < 0 {
			return kindConfigErr("targets.weight")
		}
	}
	return nil
}

func validateAgent(c *AgentConfig) error {
	if c == nil {
		return nil
	}
	if strings.TrimSpace(c.CardURL) != "" && !isHTTPURL(c.CardURL) {
		return kindConfigErr("card_url")
	}
	for _, cap := range c.Capabilities {
		known := false
		for _, k := range AgentCapabilities {
			if cap == k {
				known = true
				break
			}
		}
		if !known {
			return kindConfigErr("capabilities")
		}
	}
	return nil
}

func validateMCP(c *MCPConfig, e *Entry) error {
	if c == nil {
		return nil
	}
	t := c.EffectiveTransport(e)
	switch t {
	case TransportStdio:
		if strings.TrimSpace(c.Command) == "" {
			return kindConfigErr("command")
		}
	case TransportStreamableHTTP, TransportSSELegacy:
		if strings.TrimSpace(c.URL) == "" && strings.TrimSpace(e.Endpoint) == "" {
			return kindConfigErr("url")
		}
		if strings.TrimSpace(c.URL) != "" && !isHTTPURL(c.URL) {
			return kindConfigErr("url")
		}
	default:
		return kindConfigErr("transport")
	}
	for _, tool := range c.ToolAllowlist {
		if strings.TrimSpace(tool) == "" {
			return kindConfigErr("tool_allowlist")
		}
	}
	return nil
}

func validateSkill(c *SkillConfig) error {
	if c == nil {
		return nil
	}
	for _, t := range c.Triggers {
		if strings.TrimSpace(t) == "" {
			return kindConfigErr("triggers")
		}
	}
	for _, s := range c.Steps {
		if strings.TrimSpace(s) == "" {
			return kindConfigErr("steps")
		}
	}
	if strings.TrimSpace(c.Repository) != "" && !isHTTPURL(c.Repository) {
		return kindConfigErr("repository")
	}
	return nil
}

func validateCLI(c *CLIConfig) error {
	if c == nil {
		return nil
	}
	if strings.TrimSpace(c.Command) == "" {
		return kindConfigErr("command")
	}
	if c.TimeoutMS < 0 {
		return kindConfigErr("timeout_ms")
	}
	if c.MaxOutputBytes < 0 {
		return kindConfigErr("max_output_bytes")
	}
	for _, a := range c.Allowlist {
		if strings.TrimSpace(a) == "" {
			return kindConfigErr("allowlist")
		}
	}
	return nil
}

func validateCustom(c *CustomConfig) error {
	if c == nil || strings.TrimSpace(c.Schema) == "" {
		return nil
	}
	var v any
	if err := json.Unmarshal([]byte(c.Schema), &v); err != nil {
		return kindConfigErr("schema")
	}
	return nil
}

var envNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func validEnvName(s string) bool { return envNamePattern.MatchString(strings.TrimSpace(s)) }

func isHTTPURL(s string) bool {
	u, err := url.Parse(strings.TrimSpace(s))
	if err != nil {
		return false
	}
	return (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

// KindConfigSummary renders a one line, log safe description of a kind config.
// It never contains credentials: only the provider mode, the target count and
// whether a key is referenced.
func KindConfigSummary(c *KindConfig) string {
	if c == nil {
		return ""
	}
	switch {
	case c.Model != nil:
		return fmt.Sprintf("model(%s, targets=%d, key=%t)",
			c.Model.EffectiveProviderMode(), len(c.Model.Targets), strings.TrimSpace(c.Model.APIKeyEnv) != "")
	case c.Agent != nil:
		return fmt.Sprintf("agent(caps=%d, skills=%d)", len(c.Agent.Capabilities), len(c.Agent.Skills))
	case c.MCP != nil:
		return fmt.Sprintf("mcp(tools=%d)", len(c.MCP.ToolAllowlist))
	case c.Skill != nil:
		return fmt.Sprintf("skill(steps=%d)", len(c.Skill.Steps))
	case c.CLI != nil:
		return fmt.Sprintf("cli(allowlist=%d)", len(c.CLI.Allowlist))
	case c.Custom != nil:
		return "custom"
	}
	return ""
}
