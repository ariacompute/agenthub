import { SkipError, redact } from './assertions.js'

/**
 * Sequential runner. Cases share state (a created resource id feeds the A2A
 * cases, a parked task id feeds resume/cancel), so concurrency would be wrong
 * here. A failing case does not abort the run: the operator gets the full
 * picture in one pass.
 */
export async function runCases({ cases, ctx, reporter }) {
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
      continue
    }
    if (enabled === false || typeof enabled === 'string') {
      reporter.skip(item.id, typeof enabled === 'string' ? enabled : 'disabled by configuration')
      continue
    }
    const started = Date.now()
    try {
      await item.run(ctx)
      reporter.pass(item.id, Date.now() - started)
    } catch (error) {
      const elapsed = Date.now() - started
      if (error instanceof SkipError) {
        reporter.skip(item.id, error.message)
      } else {
        reporter.fail(item.id, new Error(redact(error?.message || String(error))), elapsed)
        // Keep shared state honest: dependent cases will SKIP rather than
        // cascade into misleading failures.
        if (item.provides) delete ctx.state[item.provides]
      }
    }
  }
}
