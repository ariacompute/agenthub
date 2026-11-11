import { uniqueId, deleteIfExists } from '../lib/cleanup.js'
import { expectStatus, skip } from '../lib/assertions.js'

export const DEMO = {
  send: 'demo-mcp',
  park: 'demo-agent',
  skill: 'demo-skill',
  model: 'demo-model',
  cli: 'demo-cli',
}

export function rpcBody(id, method, params) {
  return { jsonrpc: '2.0', id, method, params }
}

export function messageParams(resourceId, text, extraMetadata = {}) {
  return {
    message: { role: 'user', parts: [{ type: 'text', text }] },
    metadata: { resourceId, ...extraMetadata },
  }
}

export function resumeParams(id, text = 'approved') {
  return { id, message: { role: 'user', parts: [{ type: 'text', text }] } }
}

/** Body for a resource that is immediately discoverable by A2A. */
export function resourcePayload(kind, overrides = {}) {
  const id = uniqueId(`e2e-${kind}`)
  const payload = {
    id,
    kind,
    name: `e2e ${kind} ${id.slice(-6)}`,
    version: '1.0.0',
    health: 'healthy',
    status: 'approved',
    visibility: 'public',
    namespace: 'default',
    ...overrides,
  }
  if (kind === 'model' && !payload.kind_config) {
    payload.kind_config = { model: { provider_mode: 'mock' } }
  }
  if (kind === 'mcp' && !payload.kind_config) {
    payload.endpoint = payload.endpoint || 'https://example.test/mcp'
    payload.transport = payload.transport || 'streamable-http'
  }
  return { id, payload }
}

/** Create a resource and register its removal with the LIFO cleanup stack. */
export async function createResource(ctx, kind = 'model', overrides = {}) {
  const { id, payload } = resourcePayload(kind, overrides)
  const res = await ctx.api('POST', '/api/resources', { body: payload })
  expectStatus(res, 201, `create ${kind} resource`)
  ctx.cleanup(`resource ${id}`, () => deleteIfExists(ctx.api, `/api/resources/${id}`))
  return { id, entry: res.json, payload }
}

/** Prefer the seeded demo resources; create an equivalent one when absent. */
export async function pickSendableResource(ctx) {
  const seeded = ctx.state.demoResources || []
  const demo = seeded.find((entry) => entry.id === DEMO.send || String(entry.name || '').startsWith('demo-mcp'))
  if (demo) return { id: demo.id, origin: 'seed' }
  const created = await createResource(ctx, 'model')
  return { id: created.id, origin: 'created' }
}

/** demo-agent parks on "approve"/"confirm" prompts (internal/adapter/agent.go). */
export async function pickParkResource(ctx) {
  const seeded = ctx.state.demoResources || []
  const demo = seeded.find((entry) => entry.id === DEMO.park || String(entry.name || '').startsWith('demo-agent'))
  if (demo) return { id: demo.id, origin: 'seed' }
  const created = await createResource(ctx, 'agent')
  return { id: created.id, origin: 'created' }
}

export function requireParked(result, label = 'HITL') {
  if (result?.status?.state !== 'input-required') {
    skip(`${label}: adapter did not park the task (state ${result?.status?.state || 'none'}); needs an approving agent resource`)
  }
  return result
}

export function fileForm(filename, content, mime = 'text/plain') {
  const form = new FormData()
  form.append('file', new Blob([content], { type: mime }), filename)
  return form
}

export function uniqueName(prefix) {
  return uniqueId(prefix)
}
