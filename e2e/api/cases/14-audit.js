import {
  expectAvailable, expectStatus, expectShape, expectArray, requireState,
} from '../../lib/assertions.js'

const group = 'audit trail'
const c = (id, route, run, extra = {}) => ({ id, group, route, ...extra, run })

export default [
  c('audit events are listed with stats', 'GET /api/audit', async (ctx) => {
    const res = await ctx.api('GET', '/api/audit', { query: { limit: 20 } })
    expectAvailable(res, 'audit list')
    expectShape(res, ['events', 'stats'], 'audit list')
    expectArray(res, 'events', 'audit list')
    ctx.state.auditEventId = res.json.events?.[0]?.eventId || res.json.events?.[0]?.id
  }),

  c('the hash chain verifies', 'GET /api/audit/verify', async (ctx) => {
    const res = await ctx.api('GET', '/api/audit/verify')
    expectAvailable(res, 'audit verify')
    expectShape(res, ['ok', 'checked'], 'audit verify')
    if (res.json?.ok !== true && res.json?.brokenAt) {
      throw new Error(`the audit chain is broken at ${res.json.brokenAt}`)
    }
  }),

  c('an audit event detail is readable', 'GET /api/audit/{id}', async (ctx) => {
    const id = requireState(ctx, 'auditEventId', 'audit event')
    const res = await ctx.api('GET', `/api/audit/${encodeURIComponent(id)}`)
    expectStatus(res, 200, 'audit detail')
    expectShape(res, ['hash', 'httpMethod', 'path'], 'audit detail')
  }),

  c('an unknown audit event is a clean 404', 'GET /api/audit/{id}', async (ctx) => {
    const res = await ctx.api('GET', '/api/audit/e2e-no-event')
    expectStatus(res, 404, 'unknown audit event')
  }),
]
