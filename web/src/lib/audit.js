// Pure logic behind the audit screen.
//
// The view is deliberately thin: it owns the DOM and nothing else, so the two
// things that can be wrong — the query it sends and the reading it makes of one
// stored event — live here and are covered by unit tests. Keeping them out of
// the component is also what makes the "is this call a refusal or a failure?"
// question answerable in one place instead of in a template expression.

/** The time presets, in the order the segmented control shows them. */
export const AUDIT_RANGES = [
  { key: 'range1h', hours: 1 },
  { key: 'range24h', hours: 24 },
  { key: 'range7d', hours: 24 * 7 },
  { key: 'rangeAll', hours: 0 },
]

/** The page size the endpoint is asked for. */
export const AUDIT_PAGE = 100

const DEFAULT_RANGE = 'range24h'

/**
 * Turns a preset key into the RFC3339 window the endpoint parses.
 *
 * An unknown key falls back to 24 hours rather than to "everything": a screen
 * that silently widens its window would be a slow query on a real trail, and
 * "Everything" is a choice the operator can make explicitly.
 */
export function windowRange(rangeKey, now = new Date()) {
  const preset = AUDIT_RANGES.find((range) => range.key === rangeKey)
    || AUDIT_RANGES.find((range) => range.key === DEFAULT_RANGE)
  if (!preset.hours) return { from: '', to: '' }
  const to = now instanceof Date ? now : new Date(now)
  const from = new Date(to.getTime() - preset.hours * 3600 * 1000)
  return { from: from.toISOString(), to: to.toISOString() }
}

/**
 * The query string parameters for one page.
 *
 * `cursor` is the `seq` of the last row already shown; 0 (or nothing) asks for
 * the newest page. Every filter is omitted when empty so the URL stays readable
 * in a browser's network panel.
 */
export function auditFilters(state = {}, cursor = 0, now = new Date()) {
  const { from, to } = windowRange(state.rangeKey, now)
  const query = { from, to, limit: AUDIT_PAGE }
  const search = String(state.search ?? '').trim()
  if (state.outcome) query.outcome = state.outcome
  if (state.source) query.source = state.source
  if (search) query.q = search
  if (cursor) query.before = cursor
  return query
}

/**
 * Classifies one call by its status code.
 *
 * "denied" is separated from "failed" on purpose: a 403 is the authorization
 * layer working, and an operator investigating an incident reads the two very
 * differently — one is policy, the other is breakage.
 */
export function outcomeOf(event) {
  const status = Number(event?.status ?? 0)
  if (status >= 200 && status < 400) return 'ok'
  if (status === 401 || status === 403 || status === 429) return 'denied'
  return 'failed'
}

/**
 * The actor as the table shows it: the human label when the server resolved
 * one, the id otherwise, and a localised word for an anonymous caller.
 */
export function actorLabel(event, t = (key) => key) {
  if (!event?.actorId) return t('anonymous')
  return event.actorLabel ? `${event.actorLabel} · ${event.actorId}` : event.actorId
}

/** First eight characters of a request id — enough to recognise, not to copy. */
export function shortId(id) {
  return id ? String(id).slice(0, 8) : ''
}

/**
 * The chain indicator's state from the verifier's answer.
 *
 * A response without `ok: true` counts as broken: the whole point of the check
 * is that only an affirmative answer is reassuring.
 */
export function chainState(result) {
  if (result?.ok) {
    return { state: 'ok', checked: Number(result.checked ?? 0), brokenAt: '' }
  }
  return {
    state: 'broken',
    checked: Number(result?.checked ?? 0),
    brokenAt: result?.brokenAt ?? '',
  }
}

/** The next cursor from a page, or 0 when the trail is exhausted. */
export function nextCursor(payload) {
  const next = Number(payload?.next ?? 0)
  return Number.isFinite(next) && next > 0 ? next : 0
}

/** The counters above the table, with sane zeros for a failed first load. */
export function statsOf(payload) {
  const stats = payload?.stats ?? {}
  return {
    total: Number(stats.total ?? 0),
    failed: Number(stats.failed ?? 0),
    denied: Number(stats.denied ?? 0),
    written: Number(stats.written ?? 0),
  }
}
