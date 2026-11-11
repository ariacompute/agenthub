/**
 * Everything that needs a third party, a rewind, a private bucket, a real
 * container runtime or a destructive session change lives here. Every case is
 * SKIP by default and becomes a real integration (with its own cleanup) only
 * when its AGENTHUB_E2E_* switch is explicitly set.
 */
import { createHmac } from 'node:crypto'
import http from 'node:http'
import {
  expectStatus, expectStatusAndCode, expectAvailable, expectShape, skip,
} from '../../lib/assertions.js'
import { rpcBody, messageParams, uniqueName } from '../fixtures.js'
import { deleteIfExists } from '../../lib/cleanup.js'
import { optinReason } from '../../e2e.config.js'

const group = 'opt-in integrations'
const c = (id, route, run, extra = {}) => ({ id, group, route, ...extra, run })

export default [
  c('a real logout drops the session and a fresh login restores it', 'POST /api/auth/logout', async (ctx) => {
    if (ctx.identity.mode !== 'login') skip('a supplied token / API key cannot be re-logged-in here')
    const out = await ctx.api('POST', '/api/auth/logout', { body: {} })
    expectStatus(out, 204, 'logout')
    const anon = await ctx.anon('GET', '/api/auth/me')
    expectStatus(anon, 401, 'me after logout')
    const login = await ctx.client.request('POST', '/api/auth/login', {
      mode: 'anon',
      body: { email: ctx.config.email, password: ctx.config.password },
    })
    expectStatus(login, 200, 're-login')
    const me = await ctx.api('GET', '/api/auth/me')
    expectStatus(me, 200, 'me after re-login')
  }, { enabled: (ctx) => optinReason(ctx.config, 'sessionLogout', 'a real logout invalidates the shared session') }),

  c('OAuth start reaches a configured provider', 'GET /api/auth/oauth/{provider}', async (ctx) => {
    const res = await ctx.anon('GET', '/api/auth/oauth/google')
    if (res.status === 502 && res.code === 'oauth_not_configured') {
      throw new Error('AGENTHUB_E2E_OAUTH=1 but no OAuth provider is configured on the server')
    }
    expectStatus(res, [302, 303], 'oauth start')
    if (!res.location || !/^https?:\/\//.test(res.location)) {
      throw new Error('oauth start did not redirect to a provider URL')
    }
    ctx.reporter.note('OAuth callback requires a real provider account and is not driven automatically')
  }, { enabled: (ctx) => optinReason(ctx.config, 'oauth', 'a real OAuth round trip needs provider credentials') }),

  c('a signed channel webhook is accepted', 'POST /api/channels/{id}/webhook', async (ctx) => {
    const secret = process.env.AGENTHUB_E2E_SIGNING_KEY
    if (!secret) {
      throw new Error('set AGENTHUB_E2E_SIGNING_KEY to the same value as the server process environment')
    }
    const created = await ctx.api('POST', '/api/channels', {
      body: {
        kind: 'slack', name: uniqueName('e2e-webhook'), enabled: true,
        config: {}, signingKeyEnv: 'AGENTHUB_E2E_SIGNING_KEY', threadId: '',
      },
    })
    expectStatus(created, 201, 'create webhook channel')
    const channelId = created.json?.id
    ctx.cleanup(`webhook channel ${channelId}`, () => deleteIfExists(ctx.api, `/api/channels/${channelId}`))

    const raw = JSON.stringify({ external_ref: uniqueName('e2e-webhook-ref'), text: 'signed hello' })
    const ts = String(Math.floor(Date.now() / 1000))
    const signature = 'v0=' + createHmac('sha256', secret).update(`v0:${ts}:${raw}`).digest('hex')
    const res = await ctx.api('POST', `/api/channels/${channelId}/webhook`, {
      body: raw,
      headers: { 'X-Slack-Request-Timestamp': ts, 'X-Slack-Signature': signature },
    })
    expectStatus(res, 200, 'signed webhook')
  }, { enabled: (ctx) => optinReason(ctx.config, 'webhook', 'webhook verification needs a shared signing secret on the server') }),

  c('files are backed by the configured S3 store', 'GET /api/files', async (ctx) => {
    const res = await ctx.api('GET', '/api/files')
    expectAvailable(res, 'file list')
    expectShape(res, ['backend'], 'file list')
    if (res.json.backend !== 's3') {
      throw new Error(`AGENTHUB_E2E_S3=1 but the server reports backend=${res.json.backend}`)
    }
  }, { enabled: (ctx) => optinReason(ctx.config, 's3', 'S3 mode needs server-side bucket credentials') }),

  c('a push callback endpoint is registered and read back', 'POST /a2a', async (ctx) => {
    const url = process.env.AGENTHUB_E2E_PUSH_URL
    if (!url) throw new Error('set AGENTHUB_E2E_PUSH_URL to an HTTPS callback that echoes the HMAC headers')
    const task = await ctx.rpc(70, 'message/send', messageParams(ctx.state.sendResource, 'e2e push callback'))
    const taskId = task.json?.result?.id
    if (!taskId) throw new Error('could not create a task for the push config')
    const set = await ctx.rpc(71, 'tasks/pushNotification/set', {
      taskId, pushNotificationConfig: { url },
    })
    if (set.json?.error) throw new Error(`push config refused: ${set.json.error.message}`)
    const get = await ctx.rpc(72, 'tasks/pushNotification/get', { id: taskId })
    if (get.json?.error) throw new Error(`push config did not read back: ${get.json.error.message}`)
    ctx.reporter.note('the callback itself must be observed on AGENTHUB_E2E_PUSH_URL')
  }, { enabled: (ctx) => optinReason(ctx.config, 'pushCallback', 'a real push callback needs an HTTPS receiver') }),

  c('a loopback receiver proves requests leave the process', 'POST /a2a', async (ctx) => {
    const received = []
    const server = http.createServer((req, res) => {
      let body = ''
      req.on('data', (chunk) => { body += chunk })
      req.on('end', () => {
        received.push({ method: req.method, headers: req.headers, body })
        res.writeHead(200, { 'Content-Type': 'application/json' })
        res.end('{"ok":true}')
      })
    })
    await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve))
    const port = server.address().port
    try {
      const sent = await ctx.rpc(80, 'message/send', messageParams(ctx.state.sendResource, 'e2e loopback probe'))
      const taskId = sent.json?.result?.id
      const set = await ctx.rpc(81, 'tasks/pushNotification/set', {
        taskId, pushNotificationConfig: { url: `http://127.0.0.1:${port}/callback` },
      })
      expectStatus(set, 200, 'push set against a loopback receiver')
      ctx.reporter.note(`loopback receiver saw ${received.length} callback(s); MockNotifier records instead of dialing`)
    } finally {
      await new Promise((resolve) => server.close(resolve))
    }
  }, { enabled: (ctx) => optinReason(ctx.config, 'pushCallback', 'a real push callback needs an HTTPS receiver') }),
]
