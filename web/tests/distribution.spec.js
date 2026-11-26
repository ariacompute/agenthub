import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'

// The store is the only place that knows whether a failed preview keeps the last
// good package, so these cases assert state, not just that a request happened.
const apiMock = vi.hoisted(() => {
  const make = () => vi.fn().mockResolvedValue({})
  return {
    listResources: make(),
    getResourceDistribution: make(),
    pushResourceDistribution: make(),
  }
})

vi.mock('../src/lib/api.js', () => apiMock)

import * as api from '../src/lib/api.js'
import { useHubStore } from '../src/lib/store.js'

beforeEach(() => {
  setActivePinia(createPinia())
  for (const fn of Object.values(apiMock)) fn.mockReset().mockResolvedValue({})
})

describe('binding options', () => {
  it('loads the whole catalogue of both kinds', async () => {
    api.listResources.mockImplementation(async ({ kind }) => ({
      resources: [{ id: `${kind}-1`, kind }],
    }))
    const store = useHubStore()
    expect(await store.refreshBindingOptions()).toBe(true)
    expect(store.bindingOptions.mcp.map((entry) => entry.id)).toEqual(['mcp-1'])
    expect(store.bindingOptions.skill.map((entry) => entry.id)).toEqual(['skill-1'])
    // The picker offers every record of the kind, not one page: the store must
    // not send a window here.
    expect(api.listResources.mock.calls.map(([query]) => query.limit)).toEqual([undefined, undefined])
  })

  it('keeps the previous options when the read fails', async () => {
    const store = useHubStore()
    store.bindingOptions = { mcp: [{ id: 'kept' }], skill: [] }
    api.listResources.mockRejectedValue(Object.assign(new Error('boom'), { code: 'internal_error' }))
    expect(await store.refreshBindingOptions()).toBe(false)
    expect(store.bindingOptions.mcp.map((entry) => entry.id)).toEqual(['kept'])
    expect(store.errorCode).toBe('internal_error')
  })
})

describe('distribution preview', () => {
  it('stores the package the server resolved', async () => {
    api.getResourceDistribution.mockResolvedValue({ agent_id: 'a1', mcp: [{ id: 'm1' }], skills: [] })
    const store = useHubStore()
    expect(await store.loadDistribution('a1')).toBe(true)
    expect(api.getResourceDistribution).toHaveBeenCalledWith('a1')
    expect(store.distribution.agent_id).toBe('a1')
    expect(store.errorCode).toBe('')
  })

  it('keeps the last good package when a refresh fails', async () => {
    const store = useHubStore()
    store.distribution = { agent_id: 'a1', skills: [{ id: 's1' }] }
    api.getResourceDistribution.mockRejectedValue(Object.assign(new Error('nope'), { code: 'resource_not_found' }))
    expect(await store.loadDistribution('a1')).toBe(false)
    // A transient failure must not make the panel say "nothing is bound".
    expect(store.distribution.skills).toHaveLength(1)
    expect(store.errorCode).toBe('resource_not_found')
  })

  it('falls back to a code when the failure carries none', async () => {
    api.getResourceDistribution.mockRejectedValue(new Error('network'))
    const store = useHubStore()
    expect(await store.loadDistribution('a1')).toBe(false)
    expect(store.errorCode).toBe('internal_error')
  })
})

describe('distribution push', () => {
  it('records the task id as the receipt', async () => {
    api.pushResourceDistribution.mockResolvedValue({ id: 'task-9', status: { state: 'working' } })
    const store = useHubStore()
    const task = await store.pushDistribution('a1')
    expect(task.id).toBe('task-9')
    expect(store.distributionTaskID).toBe('task-9')
    expect(store.errorCode).toBe('')
  })

  it('keeps the earlier receipt when a second push fails', async () => {
    const store = useHubStore()
    store.distributionTaskID = 'task-1'
    api.pushResourceDistribution.mockRejectedValue(Object.assign(new Error('denied'), { code: 'forbidden' }))
    expect(await store.pushDistribution('a1')).toBeNull()
    // The first delivery did happen; losing its id would leave the operator
    // unable to find it again.
    expect(store.distributionTaskID).toBe('task-1')
    expect(store.errorCode).toBe('forbidden')
  })

  it('treats a response without an id as an empty receipt', async () => {
    api.pushResourceDistribution.mockResolvedValue({})
    const store = useHubStore()
    await store.pushDistribution('a1')
    expect(store.distributionTaskID).toBe('')
  })
})

describe('clearing', () => {
  it('drops the package and the receipt together', async () => {
    const store = useHubStore()
    store.distribution = { agent_id: 'a1' }
    store.distributionTaskID = 'task-1'
    store.clearDistribution()
    expect(store.distribution).toBeNull()
    expect(store.distributionTaskID).toBe('')
  })
})
