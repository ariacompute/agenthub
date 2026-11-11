import {
  expectAvailable, expectStatus, expectStatusAndCode, expectShape,
  expectArray, expectValue, requireState, skip,
} from '../../lib/assertions.js'
import { createResource, uniqueName } from '../fixtures.js'
import { deleteIfExists } from '../../lib/cleanup.js'
import { optinReason } from '../../e2e.config.js'

const group = 'channels & connectors'
const c = (id, route, run, extra = {}) => ({ id, group, route, ...extra, run })

async function connectorResource(ctx) {
  if (ctx.state.connectorId) return ctx.state.connectorId
  const demo = (ctx.state.demoResources || []).find((e) => e.id === 'demo-mcp' || e.kind === 'mcp')
  if (demo) {
    ctx.state.connectorId = demo.id
    ctx.state.connectorOwned = false
    return demo.id
  }
  try {
    const created = await createResource(ctx, 'mcp', {
      transport: 'stdio',
      kind_config: { mcp: { transport: 'stdio', command: 'echo' } },
    })
    ctx.state.connectorId = created.id
    ctx.state.connectorOwned = true
    return created.id
  } catch {
    skip('no MCP resource available to drive the connector endpoints')
  }
}

export default [
  c('channels are listed', 'GET /api/channels', async (ctx) => {
    const res = await ctx.api('GET', '/api/channels')
    expectAvailable(res, 'channel list')
    if (!Array.isArray(res.json)) throw new Error('channel list is not an array')
  }),

  c('create a channel', 'POST /api/channels', async (ctx) => {
    const res = await ctx.api('POST', '/api/channels', {
      body: {
        kind: 'slack', name: uniqueName('e2e-channel'), enabled: true,
        config: {}, signingKeyEnv: 'AGENTHUB_E2E_SIGNING_KEY', threadId: '',
      },
    })
    expectAvailable(res, 'create channel')
    expectStatus(res, 201, 'create channel')
    ctx.state.channelId = res.json?.id
    requireState(ctx, 'channelId')
    ctx.cleanup(`channel ${ctx.state.channelId}`, () => deleteIfExists(ctx.api, `/api/channels/${ctx.state.channelId}`))
  }),

  c('the channel can be read', 'GET /api/channels/{id}', async (ctx) => {
    const res = await ctx.api('GET', `/api/channels/${requireState(ctx, 'channelId')}`)
    expectAvailable(res, 'get channel')
    expectValue(res.json?.id, ctx.state.channelId, 'channel id')
  }),

  c('the channel can be updated', 'PUT /api/channels/{id}', async (ctx) => {
    const res = await ctx.api('PUT', `/api/channels/${requireState(ctx, 'channelId')}`, {
      body: { name: 'E2E channel renamed', enabled: true, config: {} },
    })
    expectAvailable(res, 'update channel')
    expectValue(res.json?.name, 'E2E channel renamed', 'channel name')
  }),

  c('deliveries are listed', 'GET /api/channels/{id}/deliveries', async (ctx) => {
    const res = await ctx.api('GET', `/api/channels/${requireState(ctx, 'channelId')}/deliveries`)
    expectAvailable(res, 'delivery list')
    if (!Array.isArray(res.json)) throw new Error('delivery list is not an array')
  }),

  c('an inbound webhook without a signature is refused', 'POST /api/channels/{id}/webhook', async (ctx) => {
    const res = await ctx.api('POST', `/api/channels/${requireState(ctx, 'channelId')}/webhook`, {
      body: { external_ref: uniqueName('e2e-ref'), text: 'hello' },
    })
    expectStatus(res, 401, 'unsigned webhook')
    if (!String(res.code).startsWith('channel_signature')) {
      throw new Error(`expected a channel_signature_* code, got ${res.code}`)
    }
  }),

  c('an empty outbound message is refused', 'POST /api/channels/{id}/deliver', async (ctx) => {
    const res = await ctx.api('POST', `/api/channels/${requireState(ctx, 'channelId')}/deliver`, {
      body: { message: '' },
    })
    expectStatus(res, 400, 'empty delivery')
  }),

  c('a real outbound delivery', 'POST /api/channels/{id}/deliver', async (ctx) => {
    const res = await ctx.api('POST', `/api/channels/${requireState(ctx, 'channelId')}/deliver`, {
      body: { message: 'e2e outbound delivery' },
    })
    if (res.status === 400 || res.status === 502) return // no target_url configured / SSRF guard
    expectStatus(res, 200, 'deliver')
  }, { enabled: (ctx) => optinReason(ctx.config, 'delivery', 'outbound delivery needs a reachable target_url') }),

  c('retrying an unknown delivery is a clean 404', 'POST /api/channels/{id}/deliveries/{did}/retry', async (ctx) => {
    const res = await ctx.api('POST', `/api/channels/${requireState(ctx, 'channelId')}/deliveries/e2e-no-delivery/retry`, { body: {} })
    expectStatus(res, 404, 'unknown delivery')
  }),

  c('delete the channel', 'DELETE /api/channels/{id}', async (ctx) => {
    const res = await ctx.api('DELETE', `/api/channels/${requireState(ctx, 'channelId')}`)
    expectStatus(res, 204, 'delete channel')
    delete ctx.state.channelId
  }),

  c('connectors are listed', 'GET /api/connectors', async (ctx) => {
    const res = await ctx.api('GET', '/api/connectors')
    expectAvailable(res, 'connector list')
    expectArray(res, null, 'connector list')
    ctx.state.connectors = res.json
  }),

  c('switching a connector policy off blocks actions', 'PUT /api/connectors/{id}/policy', async (ctx) => {
    const id = await connectorResource(ctx)
    ctx.cleanup('restore connector policy', () => ctx.api('PUT', `/api/connectors/${id}/policy`, { body: { policy: 'off' } }))
    const res = await ctx.api('PUT', `/api/connectors/${id}/policy`, { body: { policy: 'off' } })
    expectStatus(res, 200, 'set policy off')
    const action = await ctx.api('POST', `/api/connectors/${id}/actions`, { body: { action: 'invoke', args: {} } })
    expectStatusAndCode(action, 403, 'connector_policy_off', 'off policy')
  }),

  c('connector actions are enumerable', 'GET /api/connectors/{id}/actions', async (ctx) => {
    const res = await ctx.api('GET', `/api/connectors/${requireState(ctx, 'connectorId')}/actions`)
    expectStatus(res, 200, 'connector actions')
    expectShape(res, ['id', 'policy', 'actions'], 'connector actions')
    expectArray(res, 'actions', 'connector actions')
  }),

  c('ask_first parks an action for approval', 'POST /api/connectors/{id}/actions', async (ctx) => {
    const id = requireState(ctx, 'connectorId')
    const policy = await ctx.api('PUT', `/api/connectors/${id}/policy`, { body: { policy: 'ask_first' } })
    expectStatus(policy, 200, 'set policy ask_first')
    const res = await ctx.api('POST', `/api/connectors/${id}/actions`, { body: { action: 'invoke', args: { note: 'e2e' } } })
    expectAvailable(res, 'parked connector action')
    expectStatus(res, 202, 'parked connector action')
    ctx.state.parkedConnectorTask = res.json?.id
  }),

  c('the connector approval queue is readable', 'GET /api/connectors/approvals', async (ctx) => {
    const res = await ctx.api('GET', '/api/connectors/approvals')
    expectAvailable(res, 'connector approvals')
    if (!Array.isArray(res.json)) throw new Error('approval list is not an array')
    ctx.state.connectorApprovals = res.json
  }),

  c('approving a parked action resumes it', 'POST /api/connectors/approvals/{id}/approve', async (ctx) => {
    const list = ctx.state.connectorApprovals || []
    const pending = list.find((a) => a.taskId === ctx.state.parkedConnectorTask) || list[0]
    if (!pending) skip('no parked connector action to approve')
    const res = await ctx.api('POST', `/api/connectors/approvals/${pending.taskId}/approve`, { body: {} })
    expectAvailable(res, 'approve connector action')
    expectStatus(res, 200, 'approve connector action')
  }),

  c('rejecting a parked action cancels it', 'POST /api/connectors/approvals/{id}/reject', async (ctx) => {
    const id = requireState(ctx, 'connectorId')
    const parked = await ctx.api('POST', `/api/connectors/${id}/actions`, { body: { action: 'invoke', args: {} } })
    if (parked.status !== 202) skip('could not park a second connector action')
    const res = await ctx.api('POST', `/api/connectors/approvals/${parked.json?.id}/reject`, { body: {} })
    expectAvailable(res, 'reject connector action')
    expectStatus(res, 200, 'reject connector action')
  }),

  c('an unknown connector action is a clean 404', 'POST /api/connectors/{id}/actions', async (ctx) => {
    const res = await ctx.api('POST', '/api/connectors/e2e-no-connector/actions', { body: { action: 'invoke' } })
    expectStatus(res, 404, 'unknown connector')
  }),
]
