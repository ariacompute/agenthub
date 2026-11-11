import {
  expectStatus, expectRpcOk, expectRpcError, expectTaskState, expectValue, skip,
} from '../../lib/assertions.js'
import {
  messageParams, resumeParams, pickSendableResource, pickParkResource, requireParked, DEMO,
} from '../fixtures.js'

const group = 'A2A protocol'
const c = (id, route, run, extra = {}) => ({ id, group, route, ...extra, run })

export default [
  c('resolve a resource to send to', null, async (ctx) => {
    const sendable = await pickSendableResource(ctx)
    const parkable = await pickParkResource(ctx)
    ctx.state.sendResource = sendable.id
    ctx.state.parkResource = parkable.id
    ctx.reporter.note(`A2A targets: send=${sendable.origin}, park=${parkable.origin}`)
  }),

  c('message/send completes a task', 'POST /a2a', async (ctx) => {
    const res = await ctx.rpc(1, 'message/send', messageParams(ctx.state.sendResource, 'look up last week leave records'))
    const task = expectRpcOk(res, 'message/send')
    expectTaskState(task, 'completed', 'sent task')
    ctx.state.taskId = task.id
  }, { provides: 'taskId' }),

  c('tasks/get reads the task back', 'POST /a2a', async (ctx) => {
    const res = await ctx.rpc(2, 'tasks/get', { id: ctx.state.taskId })
    const task = expectRpcOk(res, 'tasks/get')
    expectValue(task.id, ctx.state.taskId, 'task id')
  }),

  c('historyLength trims the transcript', 'POST /a2a', async (ctx) => {
    const res = await ctx.rpc(3, 'tasks/get', { id: ctx.state.taskId, historyLength: 1 })
    const task = expectRpcOk(res, 'tasks/get with historyLength')
    if (Array.isArray(task.history) && task.history.length > 1) {
      throw new Error('historyLength=1 did not trim the transcript')
    }
  }),

  c('a completed task cannot be cancelled twice', 'POST /a2a', async (ctx) => {
    const res = await ctx.rpc(4, 'tasks/cancel', { id: ctx.state.taskId })
    expectRpcError(res, ['task_not_cancelable', 'invalid_state_transition'], 'cancel terminal task')
  }),

  c('a completed task cannot be resumed', 'POST /a2a', async (ctx) => {
    const res = await ctx.rpc(5, 'tasks/resume', resumeParams(ctx.state.taskId))
    expectRpcError(res, ['task_not_resumable', 'invalid_state_transition'], 'resume terminal task')
  }),

  c('pushNotification/set accepts a config', 'POST /a2a', async (ctx) => {
    if (!ctx.state.pushEnabled) skip('push notifications are disabled on this instance')
    const res = await ctx.rpc(20, 'tasks/pushNotification/set', {
      taskId: ctx.state.taskId,
      pushNotificationConfig: { url: 'http://127.0.0.1:9/callback' },
    })
    expectRpcOk(res, 'pushNotification/set')
  }),

  c('pushNotification/get reads the config back', 'POST /a2a', async (ctx) => {
    if (!ctx.state.pushEnabled) skip('push notifications are disabled on this instance')
    const res = await ctx.rpc(21, 'tasks/pushNotification/get', { id: ctx.state.taskId })
    expectRpcOk(res, 'pushNotification/get')
  }),

  c('an empty push config is rejected', 'POST /a2a', async (ctx) => {
    if (!ctx.state.pushEnabled) skip('push notifications are disabled on this instance')
    const res = await ctx.rpc(22, 'tasks/pushNotification/set', {
      taskId: ctx.state.taskId, pushNotificationConfig: {},
    })
    expectRpcError(res, ['invalid_params', 'invalid_push_config'], 'empty push config')
  }),

  c('an approving prompt parks a task in input-required', 'POST /a2a', async (ctx) => {
    const res = await ctx.rpc(10, 'message/send', messageParams(ctx.state.parkResource, 'please approve the change'))
    const task = expectRpcOk(res, 'parking send')
    requireParked(task)
    ctx.state.parkedId = task.id
  }, { provides: 'parkedId' }),

  c('tasks/resume completes the parked task', 'POST /a2a', async (ctx) => {
    if (!ctx.state.parkedId) skip('no parked task was produced')
    const res = await ctx.rpc(11, 'tasks/resume', resumeParams(ctx.state.parkedId, 'approved'))
    const task = expectRpcOk(res, 'tasks/resume')
    expectTaskState(task, 'completed', 'resumed task')
  }),

  c('tasks/cancel stops a parked task', 'POST /a2a', async (ctx) => {
    const parked = await ctx.rpc(13, 'message/send', messageParams(ctx.state.parkResource, 'confirm the rollback'))
    const task = expectRpcOk(parked, 'second parking send')
    requireParked(task, 'second park')
    const res = await ctx.rpc(14, 'tasks/cancel', { id: task.id })
    const cancelled = expectRpcOk(res, 'tasks/cancel')
    expectTaskState(cancelled, 'canceled', 'cancelled task')
  }),

  c('an unknown resource is refused', 'POST /a2a', async (ctx) => {
    const res = await ctx.rpc(33, 'message/send', messageParams('e2e-no-such-resource', 'hello'))
    expectRpcError(res, ['resource_not_found', 'no_resource_available'], 'unknown resource')
  }),

  c('an unknown task id is refused', 'POST /a2a', async (ctx) => {
    const res = await ctx.rpc(30, 'tasks/get', { id: 'task_missing' })
    expectRpcError(res, ['task_not_found'], 'unknown task')
  }),

  c('a missing task id is refused', 'POST /a2a', async (ctx) => {
    const res = await ctx.rpc(31, 'tasks/get', {})
    expectRpcError(res, ['task_not_found', 'invalid_params'], 'missing task id')
  }),

  c('an unknown JSON-RPC method is refused', 'POST /a2a', async (ctx) => {
    const res = await ctx.rpc(32, 'nope', {})
    expectRpcError(res, ['method_not_found'], 'unknown method')
  }),

  c('the v0.1 tasks/send alias still works', 'POST /a2a', async (ctx) => {
    const res = await ctx.rpc(34, 'tasks/send', messageParams(ctx.state.sendResource, 'alias smoke'))
    const task = expectRpcOk(res, 'tasks/send alias')
    expectTaskState(task, 'completed', 'alias task')
  }, { enabled: (ctx) => ctx.state.legacyMethods ? true : 'v0.1 aliases are disabled on this deployment' }),

  c('every seeded adapter kind answers a send', 'POST /a2a', async (ctx) => {
    const kinds = [DEMO.model, DEMO.send, DEMO.skill, DEMO.cli, DEMO.park]
    const seen = []
    for (const [index, name] of kinds.entries()) {
      const entry = (ctx.state.demoResources || []).find((e) => e.id === name)
      if (!entry) continue
      const res = await ctx.rpc(40 + index, 'message/send', messageParams(entry.id, 'e2e kind coverage'))
      const task = expectRpcOk(res, `send via ${name}`)
      if (!task?.status?.state) throw new Error(`${name} returned no task state`)
      seen.push(name)
    }
    if (!seen.length) skip('no demo-* resources registered; per-kind coverage is exercised by the API catalogue instead')
    ctx.reporter.note(`per-kind sends: ${seen.join(', ')}`)
  }),
]
