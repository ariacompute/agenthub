import {
  expectAvailable, expectStatus, expectShape, expectArray,
  expectValue, requireState, skip,
} from '../../lib/assertions.js'
import { fileForm, uniqueName } from '../fixtures.js'
import { deleteIfExists } from '../../lib/cleanup.js'

const group = 'threads & speech'
const c = (id, route, run, extra = {}) => ({ id, group, route, ...extra, run })

export default [
  c('threads are listed', 'GET /api/threads', async (ctx) => {
    const res = await ctx.api('GET', '/api/threads')
    expectAvailable(res, 'thread list')
    expectArray(res, 'threads', 'thread list')
  }),

  c('create a thread', 'POST /api/threads', async (ctx) => {
    const res = await ctx.api('POST', '/api/threads', { body: { title: 'e2e thread' } })
    expectAvailable(res, 'create thread')
    expectStatus(res, 201, 'create thread')
    ctx.state.threadId = res.json?.id || res.json?.thread?.id
    requireState(ctx, 'threadId')
    ctx.cleanup(`thread ${ctx.state.threadId}`, () => deleteIfExists(ctx.api, `/api/threads/${ctx.state.threadId}`))
  }),

  c('a thread carries its message history', 'GET /api/threads/{id}', async (ctx) => {
    const res = await ctx.api('GET', `/api/threads/${requireState(ctx, 'threadId')}`)
    expectAvailable(res, 'get thread')
    expectShape(res, ['thread', 'messages'], 'get thread')
    expectArray(res, 'messages', 'get thread')
  }),

  c('post a message', 'POST /api/threads/{id}/messages', async (ctx) => {
    const res = await ctx.api('POST', `/api/threads/${requireState(ctx, 'threadId')}/messages`, {
      body: { body: 'hello from the e2e suite' },
    })
    expectStatus(res, 201, 'post message')
    expectShape(res, ['message', 'delegated', 'failed'], 'post message')
  }),

  c('an @mention of an unknown agent is reported as failed', 'POST /api/threads/{id}/messages', async (ctx) => {
    const res = await ctx.api('POST', `/api/threads/${requireState(ctx, 'threadId')}/messages`, {
      body: { body: 'delegating', mentions: ['e2e-no-agent'] },
    })
    expectStatus(res, 201, 'post message with mention')
    if (!Array.isArray(res.json?.failed)) throw new Error('failed mentions were not reported')
  }),

  c('thread messages can be cleared', 'DELETE /api/threads/{id}/messages', async (ctx) => {
    const res = await ctx.api('DELETE', `/api/threads/${requireState(ctx, 'threadId')}/messages`, { body: {} })
    expectStatus(res, 204, 'clear thread messages')
  }),

  c('an unknown thread is a clean 404', 'GET /api/threads/{id}', async (ctx) => {
    const res = await ctx.api('GET', '/api/threads/e2e-no-thread')
    expectStatus(res, 404, 'unknown thread')
  }),

  c('a voice clip is accepted or the feature is reported as off', 'POST /api/threads/{id}/audio', async (ctx) => {
    const res = await ctx.api('POST', `/api/threads/${requireState(ctx, 'threadId')}/audio`, {
      body: fileForm('clip.wav', 'RIFFfake-audio-bytes', 'audio/wav'),
    })
    if (res.status === 503 && res.code === 'speech_disabled') {
      skip('speech is not enabled on this instance (speech_disabled)')
    }
    expectAvailable(res, 'thread audio')
    expectShape(res, ['transcript'], 'thread audio')
  }),

  c('delete the thread', 'DELETE /api/threads/{id}', async (ctx) => {
    const res = await ctx.api('DELETE', `/api/threads/${requireState(ctx, 'threadId')}`)
    expectStatus(res, 204, 'delete thread')
    delete ctx.state.threadId
  }),
]
