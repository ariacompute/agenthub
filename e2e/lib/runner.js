import { SkipError, redact } from './assertions.js'

/**
 * Sequential runner. Cases share state (a created resource id feeds the A2A
 * cases, a parked task id feeds resume/cancel), so concurrency would be wrong
 * here. A failing case does not abort the run: the operator gets the full
 * picture in one pass.
 */
export async function runCases({ cases, ctx, reporter }) {
  // A skipped case is the reason a dependent one cannot run, so the reason is
  // carried forward under the state key the case would have produced. Without
  // this, "channelId unavailable" hides the actionable "channels are disabled on
  // this instance" reason printed one line above it.
  //
  // `provides` names those keys. It is declared only on the cases that can skip
  // for a configuration reason (a disabled module, a missing opt-in switch),
  // because those are the SKIPs whose reason a later case needs; a case that
  // fails instead of skipping is explained by its own FAIL line.
  const keysOf = (item) => (Array.isArray(item.provides) ? item.provides : item.provides ? [item.provides] : [])
  const recordSkip = (item, reason) => {
    const keys = keysOf(item)
    if (!keys.length) return
    if (!ctx.state.skipReasons) ctx.state.skipReasons = {}
    for (const key of keys) ctx.state.skipReasons[key] = reason
  }
  const clearSkip = (item) => {
    if (!ctx.state.skipReasons) return
    for (const key of keysOf(item)) delete ctx.state.skipReasons[key]
  }
  let lastGroup = ''
  for (const item of cases) {
    const group = item.group || 'general'
    if (group !== lastGroup) {
      reporter.group(group)
      lastGroup = group
    }
    let enabled = true
    try {
      enabled = typeof item.enabled === 'function' ? await item.enabled(ctx) : (item.enabled ?? true)
    } catch (error) {
      reporter.fail(item.id, new Error(`enabled() failed: ${error.message}`), 0)
      clearSkip(item)
      continue
    }
    if (enabled === false || typeof enabled === 'string') {
      const reason = typeof enabled === 'string' ? enabled : 'disabled by configuration'
      recordSkip(item, reason)
      reporter.skip(item.id, reason)
      continue
    }
    const started = Date.now()
    try {
      await item.run(ctx)
      // The key exists now, so a reason left by an earlier run of this case
      // would be stale.
      clearSkip(item)
      reporter.pass(item.id, Date.now() - started)
    } catch (error) {
      const elapsed = Date.now() - started
      if (error instanceof SkipError) {
        recordSkip(item, error.message)
        reporter.skip(item.id, error.message)
      } else {
        reporter.fail(item.id, new Error(redact(error?.message || String(error))), elapsed)
        // Keep shared state honest: dependent cases will SKIP rather than
        // cascade into misleading failures.
        for (const key of keysOf(item)) delete ctx.state[key]
        clearSkip(item)
      }
    }
  }
}
