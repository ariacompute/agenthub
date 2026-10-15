// Shared console state. Every failure keeps the backend's machine readable code
// in `error.code`; the copy layer turns it into a localised message.
import { defineStore } from 'pinia'
import * as api from './api.js'

export const KINDS = ['model', 'agent', 'mcp', 'skill', 'cli']

export const useHubStore = defineStore('hub', {
  state: () => ({
    resources: [],
    counts: {},
    healthCounts: {},
    tasks: [],
    health: null,
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
