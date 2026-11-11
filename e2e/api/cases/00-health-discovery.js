import {
  expectAvailable, expectStatus, expectStatusAndCode, expectShape, expectArray, skip,
} from '../../lib/assertions.js'

const group = 'health & discovery'
const c = (id, route, run, extra = {}) => ({ id, group, route, ...extra, run })

export default [
  c('healthz reports a live service', 'GET /healthz', async (ctx) => {
    const res = await ctx.anon('GET', '/healthz')
    // A 503 here means "listener up, database down": still a reachable service.
    expectStatus(res, [200, 503], 'healthz')
    expectShape(res, ['status', 'methods', 'auth', 'database'], 'healthz')
    if (!Array.isArray(res.json.methods) || res.json.methods.length < 7) {
      throw new Error('healthz does not advertise the A2A method set')
    }
    ctx.state.legacyMethods = Boolean(res.json.legacyMethods)
    ctx.state.pushEnabled = Boolean(res.json.pushEnabled)
  }),

  c('the public Agent Card is discoverable', 'GET /.well-known/agent.json', async (ctx) => {
    const res = await ctx.anon('GET', '/.well-known/agent.json')
    expectAvailable(res, 'well-known agent card')
    expectShape(res, ['name', 'protocolVersion', 'skills'], 'agent card')
    expectArray(res, 'skills', 'agent card')
  }),

  c('the second Agent Card path answers identically', 'GET /a2a/agent-card', async (ctx) => {
    const first = await ctx.anon('GET', '/.well-known/agent.json')
    const second = await ctx.anon('GET', '/a2a/agent-card')
    expectAvailable(second, 'a2a agent card')
    if (first.json?.name !== second.json?.name) {
      throw new Error('the two discovery paths disagree on the card name')
    }
  }),

  c('the control plane is closed to anonymous callers', 'GET /api/resources', async (ctx) => {
    const res = await ctx.anon('GET', '/api/resources')
    expectStatusAndCode(res, 401, 'unauthenticated', 'anonymous resource list')
  }),

  c('the workspace aggregate lists visible agents', 'GET /api/workspace', async (ctx) => {
    const res = await ctx.api('GET', '/api/workspace')
    expectAvailable(res, 'workspace')
    expectArray(res, 'agents', 'workspace')
  }),

  c('the console bundle is served', null, async (ctx) => {
    const res = await ctx.anon('GET', '/')
    if (res.status !== 200) {
      skip(`no built console on this instance (status ${res.status}); run make web-build`)
    }
    if (!/^\s*<(?:!doctype|html)/i.test(res.text)) {
      throw new Error('root path did not return an HTML document')
    }
  }),
]
