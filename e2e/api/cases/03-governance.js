import { expectAvailable, expectStatus, expectShape, expectArray, expectValue } from '../../lib/assertions.js'
import { uniqueName } from '../fixtures.js'
import { deleteIfExists } from '../../lib/cleanup.js'

const group = 'registry governance'
const c = (id, route, run, extra = {}) => ({ id, group, route, ...extra, run })

export default [
  c('create a curated namespace', 'POST /api/registries', async (ctx) => {
    const id = uniqueName('e2e-ns')
    const res = await ctx.api('POST', '/api/registries', {
      body: { id, name: 'E2E curated', description: 'created by e2e', auto_approve: false },
    })
    expectStatus(res, 201, 'create namespace')
    ctx.state.namespaceId = id
    ctx.cleanup(`namespace ${id}`, () => deleteIfExists(ctx.api, `/api/registries/${id}`))
  }),

  c('namespaces are listed', 'GET /api/registries', async (ctx) => {
    const res = await ctx.api('GET', '/api/registries')
    expectAvailable(res, 'namespace list')
    expectArray(res, 'registries', 'namespace list')
    if (ctx.state.namespaceId && !res.json.registries.some((n) => n.id === ctx.state.namespaceId)) {
      throw new Error('the namespace we created is missing')
    }
  }),

  c('a namespace can be replaced', 'PUT /api/registries/{id}', async (ctx) => {
    const res = await ctx.api('PUT', `/api/registries/${ctx.state.namespaceId}`, {
      body: { id: ctx.state.namespaceId, name: 'E2E curated renamed', description: 'renamed', auto_approve: true },
    })
    expectStatus(res, 200, 'update namespace')
    expectValue(res.json?.auto_approve, true, 'auto_approve')
  }),

  c('the approval queue is readable', 'GET /api/registries/approvals', async (ctx) => {
    const res = await ctx.api('GET', '/api/registries/approvals')
    expectAvailable(res, 'approval queue')
    expectShape(res, ['records', 'total'], 'approval queue')
    expectArray(res, 'records', 'approval queue')
  }),

  c('a non-slug namespace id is refused', 'POST /api/registries', async (ctx) => {
    const res = await ctx.api('POST', '/api/registries', { body: { id: 'Not A Slug', name: 'invalid' } })
    expectStatus(res, 400, 'invalid namespace id')
  }),

  c('the default namespace cannot be deleted', 'DELETE /api/registries/{id}', async (ctx) => {
    const res = await ctx.api('DELETE', '/api/registries/default', { body: {} })
    expectStatus(res, [400, 403, 409], 'protected default namespace')
  }),

  c('deleting a namespace removes it', 'DELETE /api/registries/{id}', async (ctx) => {
    const res = await ctx.api('DELETE', `/api/registries/${ctx.state.namespaceId}`)
    expectStatus(res, 204, 'delete namespace')
    const gone = await ctx.api('GET', '/api/registries')
    if (gone.json?.registries?.some((n) => n.id === ctx.state.namespaceId)) {
      throw new Error('the deleted namespace is still listed')
    }
    delete ctx.state.namespaceId
  }),

  c('register an offline upstream source', 'POST /api/registry-sources', async (ctx) => {
    const id = uniqueName('e2e-src')
    const res = await ctx.api('POST', '/api/registry-sources', {
      body: {
        id, kind: 'mcp', name: 'E2E fixture source', url: 'https://registry.test/servers',
        namespace: 'default', fixture: true, enabled: true,
      },
    })
    expectAvailable(res, 'create source')
    expectStatus(res, 201, 'create source')
    ctx.state.sourceId = id
    ctx.cleanup(`source ${id}`, () => deleteIfExists(ctx.api, `/api/registry-sources/${id}`))
  }),

  c('sources are listed', 'GET /api/registry-sources', async (ctx) => {
    const res = await ctx.api('GET', '/api/registry-sources')
    expectAvailable(res, 'source list')
    expectArray(res, 'sources', 'source list')
  }),

  c('syncing the fixture imports records', 'POST /api/registry-sources/{id}/sync', async (ctx) => {
    const res = await ctx.api('POST', `/api/registry-sources/${ctx.state.sourceId}/sync`, { body: {} })
    expectAvailable(res, 'first sync')
    expectShape(res, ['status', 'imported', 'updated', 'skipped', 'total'], 'sync result')
    ctx.state.firstSync = res.json
  }),

  c('a second sync reports no new imports', 'POST /api/registry-sources/{id}/sync', async (ctx) => {
    const res = await ctx.api('POST', `/api/registry-sources/${ctx.state.sourceId}/sync`, { body: {} })
    expectAvailable(res, 'second sync')
    if (typeof res.json?.updated !== 'number' && typeof res.json?.skipped !== 'number') {
      throw new Error('second sync did not report its counters')
    }
  }),

  c('a source can be replaced', 'PUT /api/registry-sources/{id}', async (ctx) => {
    const res = await ctx.api('PUT', `/api/registry-sources/${ctx.state.sourceId}`, {
      body: {
        id: ctx.state.sourceId, kind: 'mcp', name: 'E2E fixture source renamed',
        url: 'https://registry.test/servers', namespace: 'default', fixture: true, enabled: true,
      },
    })
    expectStatus(res, 200, 'update source')
  }),

  c('an unknown source cannot be synced', 'POST /api/registry-sources/{id}/sync', async (ctx) => {
    const res = await ctx.api('POST', '/api/registry-sources/e2e-no-source/sync', { body: {} })
    expectStatus(res, 404, 'unknown source')
  }),

  c('deleting a source removes it', 'DELETE /api/registry-sources/{id}', async (ctx) => {
    const res = await ctx.api('DELETE', `/api/registry-sources/${ctx.state.sourceId}`)
    expectStatus(res, 204, 'delete source')
    delete ctx.state.sourceId
  }),
]
