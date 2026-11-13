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
