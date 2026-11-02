// The pure half of the execution-sandbox block in the register drawer.
//
// The component owns no state and no rules: each control hands its raw input to
// one of these functions and receives the next `kind_config.sandbox` object.
// Keeping them here (rather than inline in the SFC) means the rules are testable
// without a browser, which is how the rest of this console is tested.
//
// Two conventions match internal/registry.SandboxConfig:
//   - the wire names are snake_case, exactly as the Go struct tags spell them;
//   - an empty value deletes the key, because "unset" is what makes the server
//     fall back to the instance default. The one exception is readonly_root,
//     where an explicit false is meaningful and must survive.

// Only the kinds that execute something get a sandbox policy: a model endpoint
// or a skill manual runs nowhere, and a block there would suggest a boundary
// that does not exist.
export const EXECUTING_KINDS = ['agent', 'mcp', 'cli']

export const NETWORK_MODES = ['none', 'egress_allowlist', 'bridge', 'platform']
export const ARGS_POLICIES = ['none', 'prefix', 'passthrough']

// The wire values are machine readable; these map them onto copy keys.
export const NETWORK_LABELS = {
  none: 'networkNone',
  egress_allowlist: 'networkAllowlist',
  bridge: 'networkBridge',
  platform: 'networkPlatform',
}

export const ARGS_LABELS = {
  none: 'argsNone',
  prefix: 'argsPrefix',
  passthrough: 'argsPassthrough',
}

export function showsSandbox(kind) {
  return EXECUTING_KINDS.includes(kind)
}

export function sandboxOf(kindConfig) {
  return kindConfig?.sandbox ?? {}
}

function withField(sandbox, field, value) {
  const next = { ...sandbox }
  if (value === '' || value === null || value === undefined) delete next[field]
  else next[field] = value
  return next
}

// A text field is trimmed: a work directory of "  /work  " would be stored as
// typed and then fail the server's absolute-path check for a reason the operator
// cannot see.
export function applyText(sandbox, field, text) {
  return withField(sandbox, field, typeof text === 'string' ? text.trim() : text)
}

export function applyNumber(sandbox, field, text, { float = false } = {}) {
  if (text === '' || text === null || text === undefined) return withField(sandbox, field, '')
  const parsed = float ? Number.parseFloat(text) : Number.parseInt(text, 10)
  if (Number.isNaN(parsed)) return withField(sandbox, field, '')
  return withField(sandbox, field, parsed)
}

// readonly_root is the one tri-state: '' inherits (the instance default is
// read-only), so only an explicit "false" is stored.
export function applyBool(sandbox, field, text) {
  if (text === '') return withField(sandbox, field, '')
  return withField(sandbox, field, text === 'true')
}

export function applyList(sandbox, field, text) {
  const list = String(text ?? '')
    .split(',')
    .map((item) => item.trim())
    .filter(Boolean)
  return withField(sandbox, field, list.length ? list : '')
}

// A choice outside the vocabulary is treated as "unset" rather than stored: the
// server would reject it, and a value the console cannot name is worse than no
// value at all.
export function applyChoice(sandbox, field, value, allowed) {
  if (!allowed.includes(value)) return withField(sandbox, field, '')
  return withField(sandbox, field, value)
}

export function sandboxEmpty(sandbox) {
  return !sandbox || Object.keys(sandbox).length === 0
}

// mergeSandbox is what the component emits: the sandbox key disappears when the
// block is empty, so a record that never configured a policy round trips without
// a stray `sandbox: {}`.
export function mergeSandbox(kindConfig, sandbox) {
  const next = { ...(kindConfig ?? {}) }
  if (sandboxEmpty(sandbox)) delete next.sandbox
  else next.sandbox = sandbox
  return next
}
