import { randomBytes } from 'node:crypto'

/**
 * Every object a case creates is registered here. The runner drains the stack
 * in LIFO order in a finally block, so an aborted run still cleans up.
 */
export function createCleanup() {
  const stack = []
  return {
    add(label, fn) {
      stack.push({ label, fn })
    },
    get size() {
      return stack.length
    },
    async runAll(onFailure) {
      const failures = []
      while (stack.length) {
        const { label, fn } = stack.pop()
        try {
          await fn()
        } catch (error) {
          failures.push({ label, message: String(error?.message || error) })
          onFailure?.(label, error)
        }
      }
      return failures
    },
  }
}

/** Timestamp + randomness: two runs against the same instance never collide. */
export function uniqueId(prefix) {
  return `${prefix}-${Date.now().toString(36)}-${randomBytes(3).toString('hex')}`
}

/**
 * Delete is idempotent: 404 means somebody else already removed the object.
 * Degradation codes are tolerated too (the module may have been unwired mid-run).
 */
export async function deleteIfExists(api, path, label = path) {
  try {
    const res = await api('DELETE', path)
    if (res.status === 404 || res.status === 204 || res.status === 200) return
    if (res.code === 'feature_unavailable' || res.code === 'files_disabled') return
    // A cleanup failure is reported but must not mask the test result.
    throw new Error(`${label}: cleanup returned ${res.status} ${res.code || ''}`)
  } catch (error) {
    throw new Error(`${label}: cleanup failed — ${String(error?.message || error)}`)
  }
}
