/**
 * Assertions for the API suite. Everything here throws; the runner turns the
 * throw into a PASS/FAIL/SKIP line. Deliberately small: cases read like a
 * specification, not like a test framework.
 */

export class AssertionError extends Error {
  constructor(message) {
    super(message)
    this.name = 'AssertionError'
  }
}

export class SkipError extends Error {
  constructor(message) {
    super(message)
    this.name = 'SkipError'
  }
}

export class PreflightError extends Error {
  constructor(message) {
    super(message)
    this.name = 'PreflightError'
  }
}

export function skip(reason) {
  throw new SkipError(reason)
}

// Deliberately narrow: only JSON keys in quotes, cookie/header forms and
// key=value pairs. A broad `session: <word>` rule would redact ordinary prose
// such as "session: set AGENTHUB_E2E_* to enable".
const SECRET_PATTERNS = [
  [/"((?:password|passwd|secret|token|apiKey|api_key|sessionToken|session_token|session|cookie|signature|authorization|credential))"\s*:\s*"[^"]*"/gi, '"$1":"***"'],
  [/\bahk_[A-Za-z0-9._-]+/g, 'ahk_***'],
  [/agenthub_session=[^;,\s]+/gi, 'agenthub_session=***'],
  [/Bearer\s+[A-Za-z0-9._-]+/gi, 'Bearer ***'],
  [/(X-AgentHub-Session:\s*)(\S+)/gi, '$1***'],
  [/\b(password|passwd|token|secret)=([^\s&"']+)/gi, '$1=***'],
]

/** Strip credentials and truncate, so a failure line can never leak a secret. */
export function redact(text, limit = 512) {
  let out = String(text ?? '')
  for (const [pattern, replacement] of SECRET_PATTERNS) out = out.replace(pattern, replacement)
  if (out.length > limit) out = out.slice(0, limit) + '…(truncated)'
  return out
}

function brief(res) {
  if (!res) return 'no response'
  return `status=${res.status} code=${res.code ?? '-'} body=${redact(res.text || '')}`
}

/** Codes the server uses to say "this feature is not wired up on this instance". */
const DEGRADED_CODES = new Set([
  'feature_unavailable',
  'files_disabled',
  'speech_disabled',
  'sandbox_node_offline',
  'sandbox_unsupported',
  'sandbox_disabled',
  'backup_engine_unavailable',
  'restore_busy',
  'restore_in_progress',
  'backups_disabled',
  'oauth_not_configured',
  'oauth_state_mismatch',
])

export function isDegraded(res) {
  return Boolean(res && DEGRADED_CODES.has(res.code))
}

/**
 * Assert a 2xx, but treat a documented feature-downgrade as SKIP rather than
 * FAIL. requirements.md §8 says a missing capability must be skipped, never
 * faked. opts.allow can list extra acceptable statuses.
 */
export function expectAvailable(res, label = 'request', opts = {}) {
  if (!res) throw new AssertionError(`${label}: no response`)
  if (isDegraded(res)) {
    skip(`${label}: feature disabled on this instance (${res.code})`)
  }
  if (res.status === 401) {
    throw new AssertionError(`${label}: session rejected — ${brief(res)}`)
  }
  if (res.status === 403) {
    throw new AssertionError(`${label}: forbidden, the run identity lacks this permission — ${brief(res)}`)
  }
  const allowed = opts.allow || []
  if (!((res.status >= 200 && res.status < 300) || allowed.includes(res.status))) {
    throw new AssertionError(`${label}: expected 2xx, got ${brief(res)}`)
  }
  return res
}

export function expectStatus(res, wanted, label = 'request') {
  const list = Array.isArray(wanted) ? wanted : [wanted]
  if (!res || !list.includes(res.status)) {
    throw new AssertionError(`${label}: expected ${list.join('|')}, got ${brief(res)}`)
  }
  return res
}

/** Error responses are always {"code":"..."}. */
export function expectCode(res, code, label = 'request') {
  if (!res || res.code !== code) {
    const codes = Array.isArray(code) ? code : [code]
    if (!res || !codes.includes(res.code)) {
      throw new AssertionError(`${label}: expected code ${codes.join('|')}, got ${brief(res)}`)
    }
  }
  return res
}

export function expectStatusAndCode(res, status, code, label = 'request') {
  expectStatus(res, status, label)
  return expectCode(res, code, label)
}

export function expectValue(actual, expected, label = 'value') {
  if (actual !== expected) {
    throw new AssertionError(`${label}: expected ${JSON.stringify(expected)}, got ${JSON.stringify(actual)}`)
  }
}

export function expectTruthy(value, label = 'value') {
  if (!value) throw new AssertionError(`${label}: expected a non-empty value`)
  return value
}

export function expectEqual(actual, expected, label = 'value') {
  if (JSON.stringify(actual) !== JSON.stringify(expected)) {
    throw new AssertionError(`${label}: expected ${JSON.stringify(expected)}, got ${JSON.stringify(actual)}`)
  }
}

/** Assert the response body is an object carrying the listed keys. */
export function expectShape(res, keys, label = 'body') {
  const body = res?.json
  if (!body || typeof body !== 'object' || Array.isArray(body)) {
    throw new AssertionError(`${label}: expected a JSON object, got ${brief(res)}`)
  }
  for (const key of keys) {
    if (!(key in body)) throw new AssertionError(`${label}: missing key ${key} — ${brief(res)}`)
  }
  return body
}

export function expectArray(res, key, label = 'body') {
  const body = res?.json
  const value = key ? body?.[key] : body
  if (!Array.isArray(value)) {
    throw new AssertionError(`${label}: expected ${key || 'body'} to be an array — ${brief(res)}`)
  }
  return value
}

/** POST /a2a answers HTTP 200 even for errors; assert the envelope, not the status. */
export function expectRpcOk(res, label = 'JSON-RPC') {
  expectStatus(res, 200, label)
  const body = res.json
  if (!body || body.jsonrpc !== '2.0') {
    throw new AssertionError(`${label}: not a JSON-RPC 2.0 envelope — ${brief(res)}`)
  }
  if (body.error) {
    throw new AssertionError(`${label}: unexpected error ${redact(JSON.stringify(body.error))}`)
  }
  if (!('result' in body)) {
    throw new AssertionError(`${label}: missing result — ${brief(res)}`)
  }
  return body.result
}

/** Machine codes live in error.message; error.code is the numeric JSON-RPC code. */
export function expectRpcError(res, codes, label = 'JSON-RPC') {
  if (res.status !== 200 && res.status !== 400 && res.status !== 404) {
    throw new AssertionError(`${label}: unexpected HTTP ${res.status} — ${brief(res)}`)
  }
  const wanted = Array.isArray(codes) ? codes : [codes]
  const message = res.json?.error?.message || ''
  const numeric = res.json?.error?.code
  if (!wanted.some((code) => message === code || String(message).includes(code) || numeric === code)) {
    throw new AssertionError(`${label}: expected ${wanted.join('|')}, got ${redact(JSON.stringify(res.json?.error || res.text))}`)
  }
  return res.json.error
}

export function expectTaskState(result, state, label = 'task') {
  if (result?.status?.state !== state) {
    throw new AssertionError(`${label}: expected state ${state}, got ${result?.status?.state ?? 'none'}`)
  }
  return result
}

/** A state value must be present in ctx.state or the dependent case skips. */
export function requireState(ctx, key, label = key) {
  if (ctx.state[key] === undefined || ctx.state[key] === null || ctx.state[key] === '') {
    skip(`${label} unavailable: no ${key} was produced by an earlier case`)
  }
  return ctx.state[key]
}
