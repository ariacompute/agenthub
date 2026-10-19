// Shared console state. Every failure keeps the backend's machine readable code
// in `error.code`; the copy layer turns it into a localised message.
import { defineStore } from 'pinia'
import * as api from './api.js'

// The five protocol shaped kinds plus the custom escape hatch, which mirrors
// the AWS CUSTOM record type: anything that is not one of the five.
export const KINDS = ['model', 'agent', 'mcp', 'skill', 'cli', 'custom']
export const STATUSES = ['draft', 'pending', 'approved', 'rejected', 'deprecated']

export const useHubStore = defineStore('hub', {
  state: () => ({
    resources: [],
    counts: {},
    healthCounts: {},
    tasks: [],
    health: null,
    // Registry governance: namespaces, the curation queue and the upstream
    // registries. They are separate slices because a failure in one must not
    // blank the others.
    registries: [],
    approvals: [],
    sources: [],
    selectedTaskID: '',
    selectedResourceID: '',
    errorCode: '',
    loading: false,
  }),
  actions: {
    async refreshResources() {
      this.loading = true
      try {
        const data = await api.listResources()
        this.resources = data?.resources ?? []
        this.counts = data?.counts ?? {}
        this.healthCounts = data?.health ?? {}
        this.errorCode = ''
      } catch (error) {
        this.errorCode = error.code || 'internal_error'
      } finally {
        this.loading = false
      }
    },
    async refreshTasks() {
      try {
        const data = await api.listTasks()
        this.tasks = (data?.tasks ?? []).slice().reverse()
        this.errorCode = ''
      } catch (error) {
        this.errorCode = error.code || 'internal_error'
      }
    },
    async refreshHealth() {
      try {
        const data = await api.healthz()
        this.health = data
      } catch (error) {
        this.health = null
        this.errorCode = error.code || 'internal_error'
      }
    },
    async register(entry) {
      try {
        await api.createResource(entry)
        await this.refreshResources()
        this.errorCode = ''
        return true
      } catch (error) {
        this.errorCode = error.code || 'internal_error'
        return false
      }
    },
    async update(id, entry) {
      try {
        await api.updateResource(id, entry)
        await this.refreshResources()
        this.errorCode = ''
        return true
      } catch (error) {
        this.errorCode = error.code || 'internal_error'
        return false
      }
    },
    async remove(id) {
      try {
        await api.deleteResource(id)
        await this.refreshResources()
        this.errorCode = ''
        return true
      } catch (error) {
        this.errorCode = error.code || 'internal_error'
        return false
      }
    },
    async refreshRegistries() {
      try {
        const data = await api.listRegistries()
        this.registries = data?.registries ?? []
        this.errorCode = ''
      } catch (error) {
        this.errorCode = error.code || 'internal_error'
      }
    },
    async refreshApprovals() {
      try {
        const data = await api.listApprovals()
        this.approvals = data?.records ?? []
        this.errorCode = ''
      } catch (error) {
        this.errorCode = error.code || 'internal_error'
      }
    },
    async saveRegistry(namespace) {
      try {
        await api.createRegistry(namespace)
        await this.refreshRegistries()
        this.errorCode = ''
        return true
      } catch (error) {
        this.errorCode = error.code || 'internal_error'
        return false
      }
    },
    async removeRegistry(id) {
      try {
        await api.deleteRegistry(id)
        await this.refreshRegistries()
        this.errorCode = ''
        return true
      } catch (error) {
        this.errorCode = error.code || 'internal_error'
        return false
      }
    },
    // Curate applies one transition. It refreshes both the catalogue and the
    // queue, because approving the last pending record empties the queue.
    async curate(id, status, reason = '') {
      try {
        await api.setResourceStatus(id, status, reason)
        await this.refreshResources()
        await this.refreshApprovals()
        this.errorCode = ''
        return true
      } catch (error) {
        this.errorCode = error.code || 'internal_error'
        return false
      }
    },
    async refreshSources() {
      try {
        const data = await api.listSources()
        this.sources = data?.sources ?? []
        this.errorCode = ''
      } catch (error) {
        this.errorCode = error.code || 'internal_error'
      }
    },
    async saveSource(source) {
      try {
        await api.createSource(source)
        await this.refreshSources()
        this.errorCode = ''
        return true
      } catch (error) {
        this.errorCode = error.code || 'internal_error'
        return false
      }
    },
    async removeSource(id) {
      try {
        await api.deleteSource(id)
        await this.refreshSources()
        this.errorCode = ''
        return true
      } catch (error) {
        this.errorCode = error.code || 'internal_error'
        return false
      }
    },
    // syncSource returns the run outcome so the console can show imported,
    // updated and skipped separately instead of a bare "done".
    async syncSource(id) {
      try {
        const result = await api.syncSource(id)
        await this.refreshSources()
        await this.refreshResources()
        this.errorCode = ''
        return result
      } catch (error) {
        this.errorCode = error.code || 'internal_error'
        return null
      }
    },
    selectTask(id) {
      this.selectedTaskID = id
    },
    selectResource(id) {
      this.selectedResourceID = id
    },
    clearError() {
      this.errorCode = ''
    },
  },
})
