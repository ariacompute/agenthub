import {
  expectAvailable, expectStatus, expectShape, expectArray, expectValue,
  expectRpcOk, requireState,
} from '../../lib/assertions.js'
import { messageParams, requireParked } from '../fixtures.js'

const group = 'task control plane'
const c = (id, route, run, extra = {}) => ({ id, group, route, ...extra, run })

async function freshParkedTask(ctx, salt) {
  const res = await ctx.rpc(50 + salt, 'message/send', messageParams(ctx.state.parkResource, `please confirm ${salt}`))
  const task = expectRpcOk(res, 'park for control-plane case')
  requireParked(task, 'control-plane park')
  return task.id
}

export default [
  c('tasks are listed', 'GET /api/tasks', async (ctx) => {
    const res = await ctx.api('GET', '/api/tasks')
    expectAvailable(res, 'task list')
    expectArray(res, 'tasks', 'task list')
  }),

  c('a task can be read by id', 'GET /api/tasks/{id}', async (ctx) => {
    const id = requireState(ctx, 'taskId')
    const res = await ctx.api('GET', `/api/tasks/${id}`)
    expectStatus(res, 200, 'get task')
    expectValue(res.json?.id, id, 'task id')
  }),

  c('a task exposes its trace spans', 'GET /api/tasks/{id}/spans', async (ctx) => {
    const id = requireState(ctx, 'taskId')
    const res = await ctx.api('GET', `/api/tasks/${id}/spans`)
    expectAvailable(res, 'task spans')
    expectArray(res, 'spans', 'task spans')
  }),

  c('an unknown task is a clean 404', 'GET /api/tasks/{id}', async (ctx) => {
    const res = await ctx.api('GET', '/api/tasks/e2e-no-task')
    expectStatus(res, 404, 'unknown task')
  }),

  c('control-plane cancel stops a parked task', 'POST /api/tasks/{id}/cancel', async (ctx) => {
    requireState(ctx, 'parkResource')
    const id = await freshParkedTask(ctx, 1)
    const res = await ctx.api('POST', `/api/tasks/${id}/cancel`, { body: {} })
    expectStatus(res, 200, 'cancel')
    expectValue(res.json?.status?.state, 'canceled', 'cancelled state')
  }),

  c('control-plane resume answers a parked task', 'POST /api/tasks/{id}/resume', async (ctx) => {
    requireState(ctx, 'parkResource')
    const id = await freshParkedTask(ctx, 2)
    const res = await ctx.api('POST', `/api/tasks/${id}/resume`, {
      body: { message: { role: 'user', parts: [{ type: 'text', text: 'approved' }] } },
    })
    expectStatus(res, 200, 'resume')
    expectValue(res.json?.status?.state, 'completed', 'resumed state')
  }),

  c('resuming a terminal task is refused', 'POST /api/tasks/{id}/resume', async (ctx) => {
    const id = requireState(ctx, 'taskId')
    const res = await ctx.api('POST', `/api/tasks/${id}/resume`, {
      body: { message: { role: 'user', parts: [{ type: 'text', text: 'approved' }] } },
    })
    expectStatus(res, [400, 404, 409], 'resume a terminal task')
  }),

  c('clearing finished tasks reports the counts', 'DELETE /api/tasks', async (ctx) => {
    const res = await ctx.api('DELETE', '/api/tasks', { body: {} })
    expectStatus(res, 200, 'clear tasks')
    expectShape(res, ['deleted', 'kept'], 'clear tasks result')
  }),
]
