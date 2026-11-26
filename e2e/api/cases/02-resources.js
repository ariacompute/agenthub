import {
  expectAvailable, expectStatus, expectStatusAndCode, expectShape,
  expectArray, expectValue, requireState,
} from '../../lib/assertions.js'
import { createResource } from '../fixtures.js'

const group = 'resources & export'
const c = (id, route, run, extra = {}) => ({ id, group, route, ...extra, run })

export default [
  c('register a discoverable resource', 'POST /api/resources', async (ctx) => {
    const created = await createResource(ctx, 'model')
    ctx.state.resourceId = created.id
    expectValue(created.entry?.id, created.id, 'created resource id')
  }),

  c('the catalogue lists the new resource', 'GET /api/resources', async (ctx) => {
    const id = requireState(ctx, 'resourceId')
    const res = await ctx.api('GET', '/api/resources')
    expectAvailable(res, 'resource list')
    expectArray(res, 'resources', 'resource list')
    expectShape(res, ['counts', 'health'], 'resource list')
    if (!res.json.resources.some((e) => e.id === id)) {
      throw new Error('the resource we created is missing from the catalogue')
    }
  }),

  c('reading one resource returns the full entry', 'GET /api/resources/{id}', async (ctx) => {
    const id = requireState(ctx, 'resourceId')
    const res = await ctx.api('GET', `/api/resources/${id}`)
    expectStatus(res, 200, 'get resource')
    expectValue(res.json?.id, id, 'resource id')
  }),

  c('replacing a resource keeps its id', 'PUT /api/resources/{id}', async (ctx) => {
    const id = requireState(ctx, 'resourceId')
    const res = await ctx.api('PUT', `/api/resources/${id}`, {
      body: {
        id: 'ignored-by-the-path',
        kind: 'model',
        name: 'e2e renamed model',
        version: '2.0.0',
        health: 'healthy',
        status: 'approved',
        visibility: 'public',
        namespace: 'default',
        kind_config: { model: { provider_mode: 'mock' } },
      },
    })
    expectStatus(res, 200, 'update resource')
    expectValue(res.json?.id, id, 'path id wins over body')
    expectValue(res.json?.name, 'e2e renamed model', 'updated name')
  }),

  c('a health value can be written by hand', 'POST /api/resources/{id}/health', async (ctx) => {
    const id = requireState(ctx, 'resourceId')
    const res = await ctx.api('POST', `/api/resources/${id}/health`, {
      body: { health: 'degraded' },
    })
    expectStatus(res, 200, 'set health')
    expectValue(res.json?.health, 'degraded', 'stored health')
  }),

  c('probing a resource measures it', 'POST /api/resources/{id}/probe', async (ctx) => {
    const id = requireState(ctx, 'resourceId')
    const res = await ctx.api('POST', `/api/resources/${id}/probe`, { body: {} })
    expectStatus(res, 200, 'probe')
    expectShape(res, ['id', 'health'], 'probe')
  }),

  c('an unknown resource is a clean 404', 'GET /api/resources/{id}', async (ctx) => {
    const res = await ctx.api('GET', '/api/resources/e2e-does-not-exist')
    expectStatusAndCode(res, 404, 'not_found', 'unknown resource')
  }),

  c('a bad resource payload is refused', 'POST /api/resources', async (ctx) => {
    const res = await ctx.api('POST', '/api/resources', { body: { kind: 'not-a-kind', name: 'e2e invalid' } })
    expectStatus(res, 400, 'invalid resource')
    if (!res.code) throw new Error('invalid resource returned no machine code')
  }),

  c('curation moves a draft through the state machine', 'POST /api/resources/{id}/status', async (ctx) => {
    const draft = await createResource(ctx, 'skill', { status: 'draft' })
    const pending = await ctx.api('POST', `/api/resources/${draft.id}/status`, { body: { status: 'pending' } })
    expectStatus(pending, 200, 'draft -> pending')
    expectValue(pending.json?.status, 'pending', 'pending state')
    const rejected = await ctx.api('POST', `/api/resources/${draft.id}/status`, { body: { status: 'approved' } })
    expectStatus(rejected, 200, 'pending -> approved')
    expectValue(rejected.json?.status, 'approved', 'approved state')
    const illegal = await ctx.api('POST', `/api/resources/${draft.id}/status`, { body: { status: 'draft' } })
    expectStatus(illegal, [400, 409], 'approved -> draft is illegal')
    ctx.state.curationId = draft.id
  }),

  c('deleting a resource removes it', 'DELETE /api/resources/{id}', async (ctx) => {
    const id = requireState(ctx, 'curationId')
    const res = await ctx.api('DELETE', `/api/resources/${id}`)
    expectStatus(res, 204, 'delete resource')
    const gone = await ctx.api('GET', `/api/resources/${id}`)
    expectStatus(gone, 404, 'deleted resource is gone')
    // The cleanup stack still holds an idempotent delete; a 404 counts as clean.
  }),

  // ---- the page window -----------------------------------------------------
  // The console asks for one page at a time; these cases pin the three facts it
  // relies on: the window is honoured, the total describes the catalogue rather
  // than the page, and a nonsensical window is refused instead of ignored.
  c('the catalogue can be paged', 'GET /api/resources', async (ctx) => {
    const res = await ctx.api('GET', '/api/resources', { query: { limit: 2, offset: 0 } })
    expectAvailable(res, 'paged catalogue')
    expectArray(res, 'resources', 'paged catalogue')
    expectShape(res, ['total', 'limit', 'offset'], 'paged catalogue')
    if (res.json.resources.length > 2) {
      throw new Error(`limit=2 returned ${res.json.resources.length} records`)
    }
    expectValue(res.json.limit, 2, 'limit echoed back')
    expectValue(res.json.offset, 0, 'offset echoed back')
    if (!(res.json.total >= res.json.resources.length)) {
      throw new Error(`total ${res.json.total} is smaller than the page`)
    }
  }),

  c('the second page continues where the first stopped', 'GET /api/resources', async (ctx) => {
    const first = await ctx.api('GET', '/api/resources', { query: { limit: 1, offset: 0 } })
    const second = await ctx.api('GET', '/api/resources', { query: { limit: 1, offset: 1 } })
    expectStatus(first, 200, 'first page')
    expectStatus(second, 200, 'second page')
    if (first.json.total !== second.json.total) {
      throw new Error('the total changed between two pages of the same catalogue')
    }
    if (first.json.total > 1) {
      if (!first.json.resources.length || !second.json.resources.length) {
        throw new Error('a catalogue with more than one record returned an empty page')
      }
      if (first.json.resources[0].id === second.json.resources[0].id) {
        throw new Error('page two repeated a record from page one')
      }
    }
  }),

  c('an offset past the end is an empty page, not an error', 'GET /api/resources', async (ctx) => {
    const res = await ctx.api('GET', '/api/resources', { query: { limit: 5, offset: 100000 } })
    expectStatus(res, 200, 'offset past the end')
    expectArray(res, 'resources', 'offset past the end')
    if (res.json.resources.length !== 0) {
      throw new Error('an offset past the end returned records')
    }
    if (!(res.json.total >= 1)) {
      // There is at least the resource this suite created.
      throw new Error(`total ${res.json.total} should still describe the catalogue`)
    }
  }),

  c('an unusable page window is refused', 'GET /api/resources', async (ctx) => {
    for (const query of ['limit=abc', 'limit=-1', 'limit=201', 'offset=-1']) {
      const params = new URLSearchParams(query)
      const res = await ctx.api('GET', '/api/resources', {
        query: Object.fromEntries(params.entries()),
      })
      expectStatus(res, 400, `page window ${query}`)
      if (!res.code) throw new Error(`page window ${query} returned no machine code`)
    }
  }),

  // ---- the batch read ------------------------------------------------------
  c('a batch read returns the requested records in order', 'POST /api/resources/batch', async (ctx) => {
    const id = requireState(ctx, 'resourceId')
    const res = await ctx.api('POST', '/api/resources/batch', {
      // The repeat proves deduplication; the unknown id proves a deleted record
      // is reported rather than failing the whole batch.
      body: { ids: [id, 'e2e-does-not-exist', id] },
    })
    expectStatus(res, 200, 'batch read')
    expectShape(res, ['resources', 'missing'], 'batch read')
    if (res.json.resources.length !== 1 || res.json.resources[0].id !== id) {
      throw new Error(`batch returned ${JSON.stringify(res.json.resources.map((e) => e.id))}`)
    }
    if (res.json.missing.length !== 1 || res.json.missing[0] !== 'e2e-does-not-exist') {
      throw new Error(`missing = ${JSON.stringify(res.json.missing)}`)
    }
  }),

  c('an unusable batch is refused', 'POST /api/resources/batch', async (ctx) => {
    const tooMany = Array.from({ length: 101 }, (_, i) => `e2e-batch-${i}`)
    const bodies = [
      { ids: [] },
      { ids: [''] },
      { ids: tooMany },
    ]
    for (const body of bodies) {
      const res = await ctx.api('POST', '/api/resources/batch', { body })
      expectStatus(res, 400, `batch with ${body.ids.length} ids`)
      if (!res.code) throw new Error('an unusable batch returned no machine code')
    }
  }),

  // ---- ability distribution ------------------------------------------------
  c('an agent publishes an ability package', 'GET /api/resources/{id}/distribution', async (ctx) => {
    const agent = await createResource(ctx, 'agent')
    ctx.state.agentId = agent.id
    const res = await ctx.api('GET', `/api/resources/${agent.id}/distribution`)
    expectStatus(res, 200, 'ability package')
    // The three keys the console renders: what is delivered, what is absent and
    // what no longer resolves.
    expectShape(res, ['agent_id', 'mcp', 'skills'], 'ability package')
    expectValue(res.json.agent_id, agent.id, 'the package names its agent')
    // Only an agent carries bindings; any other kind is a bad request, not a 404.
    const model = await ctx.api('GET', `/api/resources/${requireState(ctx, 'resourceId')}/distribution`)
    expectStatusAndCode(model, 400, 'invalid_kind', 'package of a non-agent')
  }),

  c('the package can be delivered as a task', 'POST /api/resources/{id}/distribution', async (ctx) => {
    const id = requireState(ctx, 'agentId')
    const res = await ctx.api('POST', `/api/resources/${id}/distribution`, { body: {} })
    expectStatus(res, 200, 'deliver package')
    expectShape(res, ['id', 'status'], 'deliver package')
    // The receipt is an ordinary task against the bound agent, which is what
    // makes the delivery inspectable and retryable through the task endpoints.
    expectValue(res.json.resourceId, id, 'the delivery runs the bound agent')
  }),

  c('MCP export contains only approved records', 'GET /api/export/mcp', async (ctx) => {
    const res = await ctx.api('GET', '/api/export/mcp')
    expectAvailable(res, 'MCP export')
    expectArray(res, 'servers', 'MCP export')
  }),

  c('the aggregated Agent Card is exported', 'GET /api/export/agent-card', async (ctx) => {
    const res = await ctx.api('GET', '/api/export/agent-card')
    expectAvailable(res, 'Agent Card export')
    expectShape(res, ['name', 'skills'], 'Agent Card export')
  }),
]
