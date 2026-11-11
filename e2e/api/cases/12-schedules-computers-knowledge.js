import {
  expectAvailable, expectStatus, expectShape, expectValue, requireState, skip,
} from '../../lib/assertions.js'
import { uniqueName } from '../fixtures.js'
import { deleteIfExists } from '../../lib/cleanup.js'

const group = 'schedules, computers & knowledge'
const c = (id, route, run, extra = {}) => ({ id, group, route, ...extra, run })

function moduleOrSkip(res, label) {
  if (res.status === 503 || res.code === 'feature_unavailable' || res.code === 'sandbox_unsupported') {
    skip(`${label}: module disabled on this instance (${res.code || res.status})`)
  }
}

export default [
  c('schedules are listed', 'GET /api/schedules', async (ctx) => {
    const res = await ctx.api('GET', '/api/schedules')
    moduleOrSkip(res, 'schedule list')
    expectAvailable(res, 'schedule list')
    if (!Array.isArray(res.json)) throw new Error('schedule list is not an array')
  }),

  c('create a schedule', 'POST /api/schedules', async (ctx) => {
    const res = await ctx.api('POST', '/api/schedules', {
      body: { goal: 'e2e periodic check', trigger: 'manual', cronExpr: '', enabled: false },
    })
    moduleOrSkip(res, 'create schedule')
    expectStatus(res, 201, 'create schedule')
    ctx.state.scheduleId = res.json?.id
    requireState(ctx, 'scheduleId')
    ctx.cleanup(`schedule ${ctx.state.scheduleId}`, () => deleteIfExists(ctx.api, `/api/schedules/${ctx.state.scheduleId}`))
  }),

  c('a schedule can be read', 'GET /api/schedules/{id}', async (ctx) => {
    const res = await ctx.api('GET', `/api/schedules/${requireState(ctx, 'scheduleId')}`)
    expectStatus(res, 200, 'get schedule')
    expectValue(res.json?.id, ctx.state.scheduleId, 'schedule id')
  }),

  c('a schedule can be replaced', 'PUT /api/schedules/{id}', async (ctx) => {
    const res = await ctx.api('PUT', `/api/schedules/${requireState(ctx, 'scheduleId')}`, {
      body: { goal: 'e2e periodic check renamed', trigger: 'manual', cronExpr: '', enabled: false },
    })
    expectStatus(res, 200, 'update schedule')
    expectValue(res.json?.goal, 'e2e periodic check renamed', 'schedule goal')
  }),

  c('an empty goal is refused', 'POST /api/schedules', async (ctx) => {
    const res = await ctx.api('POST', '/api/schedules', { body: { goal: '', trigger: 'manual' } })
    moduleOrSkip(res, 'empty schedule goal')
    expectStatus(res, 400, 'empty schedule goal')
  }),

  c('firing a schedule needs the router', 'POST /api/schedules/{id}/run', async (ctx) => {
    const res = await ctx.api('POST', `/api/schedules/${requireState(ctx, 'scheduleId')}/run`, { body: {} })
    if (res.status === 503 || res.code === 'feature_unavailable') {
      skip('scheduler router is disabled (AGENTHUB_ROUTER_ENABLED=false)')
    }
    expectStatus(res, [200, 202], 'run schedule')
  }),

  c('delete the schedule', 'DELETE /api/schedules/{id}', async (ctx) => {
    const res = await ctx.api('DELETE', `/api/schedules/${requireState(ctx, 'scheduleId')}`)
    expectStatus(res, 204, 'delete schedule')
    delete ctx.state.scheduleId
  }),

  c('computers are listed', 'GET /api/computers', async (ctx) => {
    const res = await ctx.api('GET', '/api/computers')
    moduleOrSkip(res, 'computer list')
    expectAvailable(res, 'computer list')
    if (!Array.isArray(res.json)) throw new Error('computer list is not an array')
  }),

  c('create a computer card', 'POST /api/computers', async (ctx) => {
    const agentId = ctx.state.parkResource || requireState(ctx, 'resourceId')
    const res = await ctx.api('POST', '/api/computers', {
      body: { agentId, kind: 'browser', status: 'idle' },
    })
    if (res.status === 409 || res.status === 503) {
      skip(`computer creation needs a sandbox lease (${res.code || res.status})`)
    }
    expectStatus(res, 201, 'create computer')
    ctx.state.computerId = res.json?.id
    requireState(ctx, 'computerId')
    ctx.cleanup(`computer ${ctx.state.computerId}`, () => deleteIfExists(ctx.api, `/api/computers/${ctx.state.computerId}`))
  }),

  c('a computer card can be read', 'GET /api/computers/{id}', async (ctx) => {
    const res = await ctx.api('GET', `/api/computers/${requireState(ctx, 'computerId')}`)
    expectStatus(res, 200, 'get computer')
  }),

  c('an unknown computer is a clean 404', 'GET /api/computers/{id}', async (ctx) => {
    const res = await ctx.api('GET', '/api/computers/e2e-no-computer')
    moduleOrSkip(res, 'unknown computer')
    expectStatus(res, 404, 'unknown computer')
  }),

  c('delete the computer card', 'DELETE /api/computers/{id}', async (ctx) => {
    const res = await ctx.api('DELETE', `/api/computers/${requireState(ctx, 'computerId')}`)
    expectStatus(res, 204, 'delete computer')
    delete ctx.state.computerId
  }),

  c('knowledge chunks are readable', 'GET /api/knowledge', async (ctx) => {
    const res = await ctx.api('GET', '/api/knowledge', { query: { agentId: ctx.state.parkResource || '' } })
    moduleOrSkip(res, 'knowledge list')
    expectAvailable(res, 'knowledge list')
    if (!Array.isArray(res.json)) throw new Error('knowledge list is not an array')
  }),
]
