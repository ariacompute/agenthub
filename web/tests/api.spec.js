import { afterEach, describe, expect, it, vi } from 'vitest'
import { listResources, sendTask } from '../src/lib/api.js'

afterEach(() => {
  vi.unstubAllGlobals()
})

function stubFetch(response) {
  const fetchMock = vi.fn(async () => response)
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

function jsonResponse(payload, status = 200) {
  return { ok: status >= 200 && status < 300, status, text: async () => JSON.stringify(payload) }
}

describe('api client', () => {
  it('requests the registry with kind and query filters', async () => {
    const fetchMock = stubFetch(jsonResponse({ resources: [{ id: 'demo-mcp', kind: 'mcp' }], counts: { mcp: 1 } }))
    const data = await listResources({ kind: 'mcp', q: 'hr' })
    expect(data.resources).toHaveLength(1)
    expect(fetchMock.mock.calls[0][0]).toBe('/api/resources?kind=mcp&q=hr')
  })

  it('keeps the machine readable code on failures', async () => {
    stubFetch(jsonResponse({ code: 'resource_not_found' }, 404))
    await expect(listResources()).rejects.toMatchObject({ code: 'resource_not_found' })
  })

  it('falls back to internal_error when the body is not JSON', async () => {
    stubFetch({ ok: false, status: 500, text: async () => 'boom' })
    await expect(listResources()).rejects.toMatchObject({ code: 'internal_error' })
  })

  it('unwraps the JSON-RPC result of message/send', async () => {
    stubFetch(jsonResponse({ jsonrpc: '2.0', id: 1, result: { id: 'task-1', status: { state: 'completed' } } }))
    const task = await sendTask({ resourceId: 'demo-mcp', text: 'hello' })
    expect(task.id).toBe('task-1')
    expect(task.status.state).toBe('completed')
  })

  it('surfaces a JSON-RPC error as an error with its code', async () => {
    stubFetch(jsonResponse({ jsonrpc: '2.0', id: 1, error: { code: -32006, message: 'resource_not_found' } }))
    await expect(sendTask({ resourceId: 'nope', text: 'hello' })).rejects.toMatchObject({
      code: 'resource_not_found',
    })
  })
})
