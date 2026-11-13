import { expectStatus, requireState, skip, expectSseAvailable } from '../../lib/assertions.js'
import { rpcBody, messageParams } from '../fixtures.js'
import { deleteIfExists } from '../../lib/cleanup.js'

const group = 'streaming & event replay'
const c = (id, route, run, extra = {}) => ({ id, group, route, ...extra, run })

const atTerminal = (frame) => {
  try {
    const envelope = JSON.parse(frame.data)
    const result = envelope.result || {}
    const state = result.status?.state
    return result.final === true || result.status?.final === true ||
      ['completed', 'failed', 'canceled', 'input-required'].includes(state)
  } catch { return false }
}

export default [
  c('message/stream ends on a terminal frame with no sentinel', 'POST /a2a', async (ctx) => {
    const resourceId = requireState(ctx, 'sendResource')
    const stream = await ctx.stream('POST', '/a2a', {
      body: rpcBody(7, 'message/stream', messageParams(resourceId, 'apply half-day annual leave tomorrow')),
      timeoutMs: 12000,
      maxFrames: 60,
      stopWhen: atTerminal,
    })
    expectSseAvailable(stream, 'message/stream')
    if (!stream.frames.length) throw new Error('no SSE frames were received')
    if (stream.text.includes('DONE]')) throw new Error('the stream emitted a [DONE] sentinel, which the contract forbids')
    // Assert the frame envelope, not a hard-coded event name or JSON spacing:
    // builds may emit status updates, artifact updates or a terminal frame.
    let echoed = 0
    let payloads = 0
    for (const frame of stream.frames) {
      let envelope
      try { envelope = JSON.parse(frame.data) } catch { continue }
      if (envelope.id === 7) echoed += 1
      if (envelope.result !== undefined || envelope.error !== undefined) payloads += 1
    }
    if (!echoed) throw new Error('frames do not echo the JSON-RPC request id')
    if (!payloads) throw new Error('stream frames carried no JSON-RPC result or error payload')
  }),

  c('GET /a2a/stream replays persisted events', 'GET /a2a/stream', async (ctx) => {
    const taskId = requireState(ctx, 'taskId')
    const stream = await ctx.stream('GET', '/a2a/stream', {
      query: { task: taskId, since: 0 },
      timeoutMs: 4000, maxFrames: 12,
      stopWhen: (frame, collected) => collected.frames.length >= 2,
    })
    expectSseAvailable(stream, 'a2a stream')
    if (!stream.frames.length) skip('no persisted events to replay for this task yet')
  }),

  c('task event replay answers an SSE stream', 'GET /api/tasks/{id}/events', async (ctx) => {
    const taskId = requireState(ctx, 'taskId')
    const stream = await ctx.stream('GET', `/api/tasks/${taskId}/events`, {
      query: { since: 0 }, timeoutMs: 4000, maxFrames: 12,
      stopWhen: (frame, collected) => collected.frames.length >= 2,
    })
    expectSseAvailable(stream, 'task events')
    if (!stream.frames.length) skip('no events persisted for this task')
  }),

  c('thread event replay answers an SSE stream', 'GET /api/threads/{id}/events', async (ctx) => {
    const created = await ctx.api('POST', '/api/threads', { body: { title: 'e2e stream' } })
    if (created.status === 503) skip('threads are not wired up on this instance')
    expectStatus(created, 201, 'create thread for events')
    const id = created.json?.id
    ctx.cleanup(`thread ${id}`, () => deleteIfExists(ctx.api, `/api/threads/${id}`))
    const stream = await ctx.stream('GET', `/api/threads/${id}/events`, {
      timeoutMs: 3000, maxFrames: 6,
      stopWhen: (frame, collected) => collected.frames.length >= 2,
    })
    expectSseAvailable(stream, 'thread events')
  }),

  c('sandbox events answer an SSE stream when enabled', 'GET /api/sandboxes/events', async (ctx) => {
    const stream = await ctx.stream('GET', '/api/sandboxes/events', {
      query: { since: 0 }, timeoutMs: 3000, maxFrames: 6,
      stopWhen: (frame, collected) => collected.frames.length >= 2,
    })
    expectSseAvailable(stream, 'sandbox events')
  }),

  c('router events answer an SSE stream when enabled', 'GET /api/router/events', async (ctx) => {
    const stream = await ctx.stream('GET', '/api/router/events', {
      query: { since: 0 }, timeoutMs: 3000, maxFrames: 6,
      stopWhen: (frame, collected) => collected.frames.length >= 2,
    })
    expectSseAvailable(stream, 'router events')
  }),

  c('the v0.1 tasks/sendSubscribe alias streams', 'POST /a2a', async (ctx) => {
    const resourceId = requireState(ctx, 'sendResource')
    const stream = await ctx.stream('POST', '/a2a', {
      body: rpcBody(9, 'tasks/sendSubscribe', messageParams(resourceId, 'legacy streaming')),
      timeoutMs: 12000, maxFrames: 60, stopWhen: atTerminal,
    })
    expectSseAvailable(stream, 'tasks/sendSubscribe')
    if (!stream.frames.length) throw new Error('the legacy streaming alias emitted no frames')
  }, { enabled: (ctx) => ctx.state.legacyMethods ? true : 'v0.1 aliases are disabled on this deployment' }),
]
