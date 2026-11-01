import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'

const apiMock = vi.hoisted(() => {
  const make = () => vi.fn().mockResolvedValue({})
  return {
    listResources: make(),
    listTasks: make(),
    healthz: make(),
    createResource: make(),
    updateResource: make(),
    deleteResource: make(),
    probeResource: make(),
    listRegistries: make(),
    listApprovals: make(),
    createRegistry: make(),
    deleteRegistry: make(),
    setResourceStatus: make(),
    listSources: make(),
    createSource: make(),
    deleteSource: make(),
    syncSource: make(),
  }
})

vi.mock('../src/lib/api.js', () => apiMock)

import * as api from '../src/lib/api.js'
import { KINDS, STATUSES, useHubStore } from '../src/lib/store.js'

beforeEach(() => {
  setActivePinia(createPinia())
  for (const fn of Object.values(apiMock)) fn.mockReset().mockResolvedValue({})
})

describe('catalogue constants', () => {
  it('exposes the five protocol kinds plus custom', () => {
    expect(KINDS).toEqual(['model', 'agent', 'mcp', 'skill', 'cli', 'custom'])
  })
  it('exposes the curation statuses', () => {
    expect(STATUSES).toEqual(['draft', 'pending', 'approved', 'rejected', 'deprecated'])
  })
})

describe('refreshResources', () => {
  it('stores resources, counts and health splits', async () => {
    api.listResources.mockResolvedValue({
      resources: [{ id: 'r1' }],
      counts: { mcp: 1 },
      health: { healthy: 1 },
    })
    const store = useHubStore()
    await store.refreshResources()
    expect(store.resources).toEqual([{ id: 'r1' }])
    expect(store.counts).toEqual({ mcp: 1 })
    expect(store.healthCounts).toEqual({ healthy: 1 })
    expect(store.errorCode).toBe('')
  })

  it('records the code on failure', async () => {
    api.listResources.mockRejectedValue({ code: 'resource_not_found' })
    const store = useHubStore()
    await store.refreshResources()
    expect(store.errorCode).toBe('resource_not_found')
    expect(store.resources).toEqual([])
  })
})

describe('refreshTasks', () => {
  it('shows the newest task first', async () => {
    api.listTasks.mockResolvedValue({ tasks: [{ id: 'a' }, { id: 'b' }] })
    const store = useHubStore()
    await store.refreshTasks()
    expect(store.tasks.map((t) => t.id)).toEqual(['b', 'a'])
  })
})

describe('resource mutations', () => {
  it('registers and refreshes on success', async () => {
    api.createResource.mockResolvedValue({})
    api.listResources.mockResolvedValue({ resources: [{ id: 'r1' }] })
    const store = useHubStore()
    expect(await store.register({ name: 'x' })).toBe(true)
    expect(store.errorCode).toBe('')
    expect(store.resources).toEqual([{ id: 'r1' }])
  })

  it('returns false and records the code on register failure', async () => {
    api.createResource.mockRejectedValue({ code: 'invalid_entry' })
    const store = useHubStore()
    expect(await store.register({ name: 'x' })).toBe(false)
    expect(store.errorCode).toBe('invalid_entry')
  })

  it('removes and refreshes', async () => {
    api.deleteResource.mockResolvedValue({})
    const store = useHubStore()
    expect(await store.remove('r1')).toBe(true)
    expect(api.deleteResource).toHaveBeenCalledWith('r1')
  })

  it('probes a single resource', async () => {
    api.probeResource.mockResolvedValue({})
    const store = useHubStore()
    expect(await store.probe('r1')).toBe(true)
    expect(api.probeResource).toHaveBeenCalledWith('r1')
  })
})

describe('registry governance', () => {
  it('loads registries and the approval queue', async () => {
    api.listRegistries.mockResolvedValue({ registries: [{ id: 'ns1' }] })
    api.listApprovals.mockResolvedValue({ records: [{ id: 'rec1' }] })
    const store = useHubStore()
    await store.refreshRegistries()
    await store.refreshApprovals()
    expect(store.registries).toEqual([{ id: 'ns1' }])
    expect(store.approvals).toEqual([{ id: 'rec1' }])
  })

  it('saves and removes a registry', async () => {
    api.createRegistry.mockResolvedValue({})
    api.deleteRegistry.mockResolvedValue({})
    const store = useHubStore()
    expect(await store.saveRegistry({ id: 'ns1' })).toBe(true)
    expect(await store.removeRegistry('ns1')).toBe(true)
    expect(api.deleteRegistry).toHaveBeenCalledWith('ns1')
  })

  it('curates a record then refreshes both views', async () => {
    api.setResourceStatus.mockResolvedValue({})
    api.listResources.mockResolvedValue({ resources: [] })
    api.listApprovals.mockResolvedValue({ records: [] })
    const store = useHubStore()
    expect(await store.curate('r1', 'approved', 'looks good')).toBe(true)
    expect(api.setResourceStatus).toHaveBeenCalledWith('r1', 'approved', 'looks good')
  })
})

describe('upstream sources', () => {
  it('loads sources', async () => {
    api.listSources.mockResolvedValue({ sources: [{ id: 's1' }] })
    const store = useHubStore()
    await store.refreshSources()
    expect(store.sources).toEqual([{ id: 's1' }])
  })

  it('saves and removes a source', async () => {
    api.createSource.mockResolvedValue({})
    api.deleteSource.mockResolvedValue({})
    const store = useHubStore()
    expect(await store.saveSource({ name: 'up' })).toBe(true)
    expect(await store.removeSource('s1')).toBe(true)
  })

  it('returns the sync result to the caller', async () => {
    api.syncSource.mockResolvedValue({ status: 'ok', imported: 3 })
    api.listSources.mockResolvedValue({ sources: [] })
    api.listResources.mockResolvedValue({ resources: [] })
    const store = useHubStore()
    const result = await store.syncSource('s1')
    expect(result).toEqual({ status: 'ok', imported: 3 })
  })
})

describe('selection and health', () => {
  it('tracks the selected task and resource', () => {
    const store = useHubStore()
    store.selectTask('t1')
    store.selectResource('r1')
    expect(store.selectedTaskID).toBe('t1')
    expect(store.selectedResourceID).toBe('r1')
  })

  it('stores health on success and null on failure', async () => {
    api.healthz.mockResolvedValue({ status: 'ok' })
    const store = useHubStore()
    await store.refreshHealth()
    expect(store.health).toEqual({ status: 'ok' })

    api.healthz.mockRejectedValue({ code: 'internal_error' })
    await store.refreshHealth()
    expect(store.health).toBeNull()
  })

  it('clears the error code', async () => {
    const store = useHubStore()
    store.errorCode = 'boom'
    store.clearError()
    expect(store.errorCode).toBe('')
  })
})
