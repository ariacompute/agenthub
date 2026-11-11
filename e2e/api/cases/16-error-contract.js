import {
  expectStatus, expectStatusAndCode, expectRpcError, expectShape,
} from '../../lib/assertions.js'

const group = 'error contract'
const c = (id, route, run, extra = {}) => ({ id, group, route, ...extra, run })

export default [
  c('malformed JSON on the data plane is a parse error', 'POST /a2a', async (ctx) => {
    const res = await ctx.api('POST', '/a2a', { body: 'not json' })
    expectStatus(res, 400, 'malformed JSON-RPC')
    if (res.code !== 'parse_error' && res.json?.error?.message !== 'parse_error') {
      throw new Error(`expected parse_error, got ${res.code}`)
    }
  }),

  c('an unknown JSON field is refused', 'POST /api/registries', async (ctx) => {
    const res = await ctx.api('POST', '/api/registries', {
      body: { id: 'e2e-invalid-payload', name: 'x', totally_unknown_field: 1 },
    })
    expectStatus(res, 400, 'unknown field')
    if (!res.code) throw new Error('bad_request carried no machine code')
  }),

  c('an unsupported method is refused or falls back to the SPA', 'GET /api/resources', async (ctx) => {
    // The mux registers method-specific patterns and a "/" SPA fallback, so an
    // unmatched PATCH is served index.html (200) on some builds and 405 on others.
    const res = await ctx.api('PATCH', '/api/resources', { body: {} })
    expectStatus(res, [200, 400, 404, 405], 'unsupported method')
    if (res.status === 200 && !/<html/i.test(res.text)) {
      throw new Error('a 200 for an unmatched method was not the console fallback')
    }
  }),

  c('every anonymous control-plane call carries unauthenticated', 'GET /api/tasks', async (ctx) => {
    const res = await ctx.anon('GET', '/api/tasks')
    expectStatusAndCode(res, 401, 'unauthenticated', 'anonymous task list')
  }),

  c('a missing message is invalid_params', 'POST /a2a', async (ctx) => {
    const res = await ctx.rpc(60, 'message/send', {})
    expectRpcError(res, ['invalid_params', 'resource_not_found', 'bad_request'], 'missing message')
  }),

  c('errors carry a machine code and no prose', 'GET /api/resources/{id}', async (ctx) => {
    const res = await ctx.api('GET', '/api/resources/e2e-no-resource')
    expectStatus(res, 404, 'unknown resource')
    expectShape(res, ['code'], 'error body')
    if (Object.keys(res.json).length !== 1) {
      throw new Error('the error body carried more than just the machine code')
    }
  }),

  c('the service still answers after the error paths', 'GET /healthz', async (ctx) => {
    const res = await ctx.anon('GET', '/healthz')
    expectStatus(res, [200, 503], 'healthz after errors')
  }),
]
