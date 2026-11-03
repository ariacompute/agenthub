import { afterEach, describe, expect, it, vi } from 'vitest'
import { downloadBackup, listResources, sendTask } from '../src/lib/api.js'

// The behaviour pinned here is what the client does when the request is well
// formed but the answer is thinner than expected: an omitted filter, a flow that
// was never chosen, a JSON-RPC error without a message, a failure body that is
// not the envelope the console expects.

afterEach(() => {
  vi.unstubAllGlobals()
})

function jsonResponse(payload, status = 200) {
  return { ok: status >= 200 && status < 300, status, text: async () => JSON.stringify(payload) }
}

function stubFetch(response) {
  const fetchMock = vi.fn(async () => response)
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

describe('listResources', () => {
  it('carries the curation status filter', async () => {
    const fetchMock = stubFetch(jsonResponse({ resources: [] }))
    await listResources({ status: 'approved' })
    expect(fetchMock.mock.calls[0][0]).toBe('/api/resources?status=approved')
  })

  it('asks for everything when no filter is given', async () => {
    const fetchMock = stubFetch(jsonResponse({ resources: [] }))
    await listResources()
    expect(fetchMock.mock.calls[0][0]).toBe('/api/resources')
  })
})

describe('sendTask', () => {
  it('carries the flow when one was chosen', async () => {
    const fetchMock = stubFetch(jsonResponse({ result: { id: 'task-1' } }))
    await sendTask({ text: 'go', resourceId: 'r1', flow: [{ op: 'then', resource: 'r1' }] })
    const params = JSON.parse(fetchMock.mock.calls[0][1].body).params
    expect(params.metadata.flow).toEqual([{ op: 'then', resource: 'r1' }])
  })

  it('sends neither a resource nor a flow when none were chosen', async () => {
    const fetchMock = stubFetch(jsonResponse({ result: { id: 'task-2' } }))
    await sendTask({ text: 'hello' })
    const params = JSON.parse(fetchMock.mock.calls[0][1].body).params
    expect(params.metadata).toEqual({})
  })

  it('falls back to internal_error when the error carries no message', async () => {
    stubFetch(jsonResponse({ error: {} }))
    await expect(sendTask({ text: 'go' })).rejects.toMatchObject({ code: 'internal_error' })
  })

  it('surfaces the code the server sent', async () => {
    stubFetch(jsonResponse({ error: { message: 'resource_unavailable' } }))
    await expect(sendTask({ text: 'go' })).rejects.toMatchObject({ code: 'resource_unavailable' })
  })
})

describe('downloadBackup', () => {
  it('keeps the code the failure body carries', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => ({ ok: false, status: 404, json: async () => ({ code: 'backup_not_found' }) })),
    )
    await expect(downloadBackup('bkp-1')).rejects.toMatchObject({
      code: 'backup_not_found',
      status: 404,
    })
  })

  it('falls back to internal_error when the body has no code', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => ({ ok: false, status: 500, json: async () => ({}) })))
    await expect(downloadBackup('bkp-1')).rejects.toMatchObject({ code: 'internal_error' })
  })

  it('survives a failure body that is not JSON', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => ({
        ok: false,
        status: 502,
        json: async () => {
          throw new SyntaxError('not json')
        },
      })),
    )
    await expect(downloadBackup('bkp-1')).rejects.toMatchObject({ code: 'internal_error', status: 502 })
  })

  it('returns the bytes and the filename the server chose', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => ({
        ok: true,
        status: 200,
        blob: async () => 'bytes',
        headers: { get: () => 'attachment; filename="cp-1.tar.gz"' },
      })),
    )
    const out = await downloadBackup('cp-1')
    expect(out).toEqual({ blob: 'bytes', filename: 'cp-1.tar.gz' })
  })
})
