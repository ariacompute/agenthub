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
    // write runs one mutating call. A rejection becomes a machine readable code
    // on `errorCode` and `false`, so every action reports a failure the same way.
    async write(call) {
      try {
        await call()
        return true
      } catch (error) {
        this.errorCode = error.code || 'internal_error'
        return false
      }
    },
    // The refresh actions never throw — most callers are fire-and-forget and a
    // blank screen is worse than a stale one — but they do return whether the
    // read landed, so an action that mutates can refuse to claim success while
    // the list on screen is still the old one.
    async refreshResources() {
      this.loading = true
      try {
        const data = await api.listResources()
        this.resources = data?.resources ?? []
        this.counts = data?.counts ?? {}
        this.healthCounts = data?.health ?? {}
        this.errorCode = ''
        return true
      } catch (error) {
        this.errorCode = error.code || 'internal_error'
        return false
      } finally {
        this.loading = false
      }
    },
    async refreshTasks() {
      try {
        const data = await api.listTasks()
        this.tasks = (data?.tasks ?? []).slice().reverse()
        this.errorCode = ''
        return true
      } catch (error) {
        this.errorCode = error.code || 'internal_error'
        return false
      }
    },
    async refreshHealth() {
      try {
        const data = await api.healthz()
        this.health = data
        return true
      } catch (error) {
        this.health = null
        this.errorCode = error.code || 'internal_error'
        return false
      }
    },
    // These four return the re-read's verdict, not the write's: a `true` means
    // the row is saved *and* the catalogue shows it. When the write lands but
    // the read does not, the caller gets `false` and the banner still carries
    // the reason, instead of a success toast over a stale list.
    async register(entry) {
      if (!(await this.write(() => api.createResource(entry)))) return false
      return this.refreshResources()
    },
    async update(id, entry) {
      if (!(await this.write(() => api.updateResource(id, entry)))) return false
      return this.refreshResources()
    },
    async remove(id) {
      if (!(await this.write(() => api.deleteResource(id)))) return false
      return this.refreshResources()
    },
    // A probe measures one resource and stores what it found; the list is
    // re-read so the card's dot shows the measured value immediately.
    async probe(id) {
      if (!(await this.write(() => api.probeResource(id)))) return false
      return this.refreshResources()
    },
    async refreshRegistries() {
      try {
        const data = await api.listRegistries()
        this.registries = data?.registries ?? []
        this.errorCode = ''
        return true
      } catch (error) {
        this.errorCode = error.code || 'internal_error'
        return false
      }
    },
    async refreshApprovals() {
      try {
        const data = await api.listApprovals()
        this.approvals = data?.records ?? []
        this.errorCode = ''
        return true
      } catch (error) {
        this.errorCode = error.code || 'internal_error'
        return false
      }
    },
    async saveRegistry(namespace) {
      if (!(await this.write(() => api.createRegistry(namespace)))) return false
      return this.refreshRegistries()
    },
    async removeRegistry(id) {
      if (!(await this.write(() => api.deleteRegistry(id)))) return false
      return this.refreshRegistries()
    },
    // Curate applies one transition. It refreshes both the catalogue and the
    // queue, because approving the last pending record empties the queue. The
    // two reads are independent, so both are attempted and both must land.
    async curate(id, status, reason = '') {
      if (!(await this.write(() => api.setResourceStatus(id, status, reason)))) return false
      const catalogue = await this.refreshResources()
      const catalogueCode = catalogue ? '' : this.errorCode
      const queue = await this.refreshApprovals()
      // A successful read clears the banner, so the second one would erase the
      // reason the first failed; restore it before answering.
      this.errorCode = catalogueCode || this.errorCode
      return catalogue && queue
    },
    async refreshSources() {
      try {
        const data = await api.listSources()
        this.sources = data?.sources ?? []
        this.errorCode = ''
        return true
      } catch (error) {
        this.errorCode = error.code || 'internal_error'
        return false
      }
    },
    async saveSource(source) {
      if (!(await this.write(() => api.createSource(source)))) return false
      return this.refreshSources()
    },
    async removeSource(id) {
      if (!(await this.write(() => api.deleteSource(id)))) return false
      return this.refreshSources()
    },
    // syncSource returns the run outcome so the console can show imported,
    // updated and skipped separately instead of a bare "done". The outcome is
    // returned as it came: substituting `null` when the follow-up read fails
    // would hide counts the server did report, and `errorCode` already carries
    // the read failure to the banner.
    async syncSource(id) {
      let result
      try {
        result = await api.syncSource(id)
      } catch (error) {
        this.errorCode = error.code || 'internal_error'
        return null
      }
      const sources = await this.refreshSources()
      const sourcesCode = sources ? '' : this.errorCode
      await this.refreshResources()
      // Same reasoning as curate: the second read may clear the first failure.
      this.errorCode = sourcesCode || this.errorCode
      return result
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
