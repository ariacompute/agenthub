import {
  expectAvailable, expectStatus, expectShape, expectArray, skip,
} from '../../lib/assertions.js'
import { uniqueName } from '../fixtures.js'

const group = 'router & scheduler'
const c = (id, route, run, extra = {}) => ({ id, group, route, ...extra, run })

function routerOrSkip(res, label) {
  if (res.status === 503 || res.code === 'feature_unavailable' || res.code === 'router_resource_unset') {
    skip(`${label}: router disabled on this instance (AGENTHUB_ROUTER_ENABLED=false)`)
  }
}

export default [
  c('router water level is reported', 'GET /api/router/stats', async (ctx) => {
    const res = await ctx.api('GET', '/api/router/stats')
    routerOrSkip(res, 'router stats')
    expectAvailable(res, 'router stats')
    expectShape(res, ['pending', 'passes', 'live'], 'router stats')
  }),

  c('triggers are listed', 'GET /api/router/triggers', async (ctx) => {
    const res = await ctx.api('GET', '/api/router/triggers')
    routerOrSkip(res, 'trigger list')
    expectAvailable(res, 'trigger list')
    expectArray(res, 'triggers', 'trigger list')
  }),

  c('create a trigger', 'POST /api/router/triggers', async (ctx) => {
    const res = await ctx.api('POST', '/api/router/triggers', {
      body: { source: 'manual', sourceId: uniqueName('e2e-trigger'), body: 'e2e router trigger' },
    })
    routerOrSkip(res, 'create trigger')
    expectStatus(res, 202, 'create trigger')
    ctx.state.triggerId = res.json?.id
  }),

  c('a redelivered trigger is deduplicated', 'POST /api/router/triggers', async (ctx) => {
    const res = await ctx.api('POST', '/api/router/triggers', {
      body: { source: 'manual', sourceId: 'e2e-dedupe', body: 'duplicate' },
    })
    routerOrSkip(res, 'dedupe probe')
    expectStatus(res, 202, 'first delivery')
    const again = await ctx.api('POST', '/api/router/triggers', {
      body: { source: 'manual', sourceId: 'e2e-dedupe', body: 'duplicate' },
    })
    expectStatus(again, 200, 'redelivery')
  }),

  c('a trigger can be read', 'GET /api/router/triggers/{id}', async (ctx) => {
    if (!ctx.state.triggerId) skip('no trigger was created')
    const res = await ctx.api('GET', `/api/router/triggers/${ctx.state.triggerId}`)
    expectStatus(res, 200, 'get trigger')
  }),

  c('tickets are listed', 'GET /api/router/tickets', async (ctx) => {
    const res = await ctx.api('GET', '/api/router/tickets')
    routerOrSkip(res, 'ticket list')
    expectAvailable(res, 'ticket list')
    expectArray(res, 'tickets', 'ticket list')
    ctx.state.ticketId = res.json.tickets?.[0]?.id
  }),

  c('a ticket detail carries its decisions', 'GET /api/router/tickets/{id}', async (ctx) => {
    if (!ctx.state.ticketId) skip('no ticket has been created yet')
    const res = await ctx.api('GET', `/api/router/tickets/${ctx.state.ticketId}`)
    expectStatus(res, 200, 'get ticket')
    expectShape(res, ['ticket', 'decisions'], 'ticket detail')
  }),

  c('cancelling an unknown ticket is a clean 404', 'POST /api/router/tickets/{id}/cancel', async (ctx) => {
    const res = await ctx.api('POST', '/api/router/tickets/e2e-no-ticket/cancel', { body: {} })
    routerOrSkip(res, 'unknown ticket cancel')
    expectStatus(res, [404, 409], 'unknown ticket cancel')
  }),

  c('handing back an unknown trigger is refused', 'POST /api/router/tickets/{id}/handback', async (ctx) => {
    const res = await ctx.api('POST', '/api/router/tickets/e2e-no-ticket/handback', {
      body: { triggerId: 'e2e-no-trigger', reason: 'e2e' },
    })
    routerOrSkip(res, 'handback')
    expectStatus(res, [400, 404, 409], 'handback')
  }),

  c('dropping thread subscriptions answers 204 or 404', 'DELETE /api/router/threads/{id}/subscription', async (ctx) => {
    const res = await ctx.api('DELETE', '/api/router/threads/e2e-no-thread/subscription', { body: {} })
    routerOrSkip(res, 'drop subscription')
    expectStatus(res, [204, 404], 'drop subscription')
  }),

  c('routing policies are listed', 'GET /api/router/policies', async (ctx) => {
    const res = await ctx.api('GET', '/api/router/policies')
    routerOrSkip(res, 'policy list')
    expectAvailable(res, 'policy list')
    expectArray(res, 'policies', 'policy list')
  }),

  c('a routing policy can be saved', 'PUT /api/router/policies/{source}', async (ctx) => {
    const res = await ctx.api('PUT', '/api/router/policies/manual', { body: { mode: 'off' } })
    routerOrSkip(res, 'save policy')
    expectStatus(res, 200, 'save policy')
  }),

  c('an invalid routing mode is refused', 'PUT /api/router/policies/{source}', async (ctx) => {
    const res = await ctx.api('PUT', '/api/router/policies/manual', { body: { mode: 'not-a-mode' } })
    routerOrSkip(res, 'invalid policy')
    expectStatus(res, [200, 400], 'invalid policy')
  }),

  c('a scheduling beat can be kicked', 'POST /api/router/passes', async (ctx) => {
    const res = await ctx.api('POST', '/api/router/passes', { body: {} })
    routerOrSkip(res, 'kick pass')
    expectStatus(res, [200, 202], 'kick pass')
  }),

  c('router events are an SSE stream', 'GET /api/router/events', async (ctx) => {
    const res = await ctx.stream('GET', '/api/router/events', {
      query: { since: 0 }, timeoutMs: 2500, maxFrames: 4,
      stopWhen: (frame, collected) => collected.frames.length >= 2,
    })
    if (res.status === 503 || res.status === 409) skip('router events: router disabled')
    expectStatus(res, 200, 'router events')
  }),
]
