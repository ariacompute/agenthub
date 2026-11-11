import {
  expectAvailable, expectStatus, expectShape, expectArray, requireState, skip,
} from '../../lib/assertions.js'
import { uniqueName } from '../fixtures.js'
import { deleteIfExists } from '../../lib/cleanup.js'
import { optinReason } from '../../e2e.config.js'

const group = 'execution sandboxes'
const c = (id, route, run, extra = {}) => ({ id, group, route, ...extra, run })

function moduleOrSkip(res, label) {
  if (res.status === 503 || res.code === 'feature_unavailable' ||
      res.code === 'sandbox_unsupported' || res.code === 'sandbox_disabled') {
    skip(`${label}: sandbox module is not enabled on this instance (${res.code || res.status})`)
  }
}

export default [
  c('leases are listed', 'GET /api/sandboxes', async (ctx) => {
    const res = await ctx.api('GET', '/api/sandboxes')
    moduleOrSkip(res, 'sandbox list')
    expectAvailable(res, 'sandbox list')
    expectShape(res, ['leases', 'default', 'live'], 'sandbox list')
  }),

  c('sandbox water level is reported', 'GET /api/sandboxes/stats', async (ctx) => {
    const res = await ctx.api('GET', '/api/sandboxes/stats')
    moduleOrSkip(res, 'sandbox stats')
    expectAvailable(res, 'sandbox stats')
    expectShape(res, ['activeLeases', 'recentExecs'], 'sandbox stats')
  }),

  c('sandbox events are an SSE stream', 'GET /api/sandboxes/events', async (ctx) => {
    const res = await ctx.stream('GET', '/api/sandboxes/events', {
      query: { since: 0 }, timeoutMs: 2500, maxFrames: 4,
      stopWhen: (frame, collected) => collected.frames.length >= 2,
    })
    if (res.status === 503 || res.status === 409) skip('sandbox events: module disabled')
    expectStatus(res, 200, 'sandbox events')
  }),

  c('sandbox providers are enumerable', 'GET /api/sandbox-providers', async (ctx) => {
    const res = await ctx.api('GET', '/api/sandbox-providers')
    moduleOrSkip(res, 'provider list')
    expectAvailable(res, 'provider list')
    expectShape(res, ['providers', 'default'], 'provider list')
  }),

  c('re-probing providers answers the catalogue', 'POST /api/sandbox-providers/refresh', async (ctx) => {
    const res = await ctx.api('POST', '/api/sandbox-providers/refresh', { body: {} })
    moduleOrSkip(res, 'provider refresh')
    expectAvailable(res, 'provider refresh')
  }),

  c('execution nodes are listed', 'GET /api/sandbox-nodes', async (ctx) => {
    const res = await ctx.api('GET', '/api/sandbox-nodes')
    moduleOrSkip(res, 'node list')
    expectAvailable(res, 'node list')
    expectArray(res, 'nodes', 'node list')
  }),

  c('enrol an execution node', 'POST /api/sandbox-nodes', async (ctx) => {
    const id = uniqueName('e2e-node')
    const res = await ctx.api('POST', '/api/sandbox-nodes', {
      body: { id, name: 'E2E node', capabilities: ['remote'] },
    })
    moduleOrSkip(res, 'register node')
    expectStatus(res, [200, 201], 'register node')
    ctx.state.nodeId = id
    ctx.cleanup(`node ${id}`, () => deleteIfExists(ctx.api, `/api/sandbox-nodes/${id}`))
  }),

  c('an invalid node id is refused', 'POST /api/sandbox-nodes', async (ctx) => {
    const res = await ctx.api('POST', '/api/sandbox-nodes', { body: { id: 'not a valid id!' } })
    moduleOrSkip(res, 'invalid node id')
    expectStatus(res, 400, 'invalid node id')
  }),

  c('a node heartbeat answers its poll hint', 'POST /api/sandbox-nodes/{id}/heartbeat', async (ctx) => {
    const res = await ctx.api('POST', `/api/sandbox-nodes/${requireState(ctx, 'nodeId')}/heartbeat`, { body: {} })
    moduleOrSkip(res, 'node heartbeat')
    expectAvailable(res, 'node heartbeat')
    expectShape(res, ['node', 'pollHint'], 'node heartbeat')
  }),

  c('long polling for a job answers 204 when the queue is empty', 'GET /api/sandbox-nodes/{id}/jobs', async (ctx) => {
    const res = await ctx.api('GET', `/api/sandbox-nodes/${requireState(ctx, 'nodeId')}/jobs`, {
      query: { wait: 1 },
    })
    moduleOrSkip(res, 'claim node job')
    expectStatus(res, [200, 204], 'claim node job')
  }),

  c('reporting an unknown job is a clean 404', 'POST /api/sandbox-nodes/{id}/jobs/{job}', async (ctx) => {
    const res = await ctx.api('POST', `/api/sandbox-nodes/${requireState(ctx, 'nodeId')}/jobs/e2e-no-job`, {
      body: { exitCode: 0, stdout: 'e2e', durationMs: 1 },
    })
    moduleOrSkip(res, 'report node job')
    expectStatus(res, [200, 404], 'report node job')
  }),

  c('remove the execution node', 'DELETE /api/sandbox-nodes/{id}', async (ctx) => {
    const res = await ctx.api('DELETE', `/api/sandbox-nodes/${requireState(ctx, 'nodeId')}`)
    expectStatus(res, 204, 'delete node')
    delete ctx.state.nodeId
  }),

  c('an unknown lease reads as not found', 'GET /api/sandboxes/{id}', async (ctx) => {
    const res = await ctx.api('GET', '/api/sandboxes/e2e-no-lease')
    moduleOrSkip(res, 'unknown lease')
    expectStatus(res, 404, 'unknown lease')
  }),

  c('executing in an unknown lease is refused', 'POST /api/sandboxes/{id}/exec', async (ctx) => {
    const res = await ctx.api('POST', '/api/sandboxes/e2e-no-lease/exec', { body: { command: 'echo' } })
    moduleOrSkip(res, 'unknown lease exec')
    expectStatus(res, [404, 409], 'unknown lease exec')
  }),

  c('renewing an unknown lease is refused', 'POST /api/sandboxes/{id}/renew', async (ctx) => {
    const res = await ctx.api('POST', '/api/sandboxes/e2e-no-lease/renew', { body: {} })
    moduleOrSkip(res, 'unknown lease renew')
    expectStatus(res, [404, 409], 'unknown lease renew')
  }),

  c('terminating an unknown lease is a clean 404', 'DELETE /api/sandboxes/{id}', async (ctx) => {
    const res = await ctx.api('DELETE', '/api/sandboxes/e2e-no-lease', { body: {} })
    moduleOrSkip(res, 'unknown lease delete')
    expectStatus(res, 404, 'unknown lease delete')
  }),

  c('a real command inside a live lease', 'POST /api/sandboxes/{id}/exec', async (ctx) => {
    const leases = await ctx.api('GET', '/api/sandboxes')
    const lease = leases.json?.leases?.[0]
    if (!lease) skip('no live lease to execute in')
    const res = await ctx.api('POST', `/api/sandboxes/${lease.id}/exec`, {
      body: { command: 'echo', args: ['e2e'], timeoutMs: 2000 },
    })
    expectAvailable(res, 'sandbox exec')
    expectShape(res, ['exitCode', 'durationMs'], 'sandbox exec')
  }, { enabled: (ctx) => optinReason(ctx.config, 'sandboxExec', 'real sandbox execution needs a container runtime') }),
]
