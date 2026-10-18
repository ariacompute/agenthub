// Identity and administration state.
//
// The console never guesses what a user may do: it asks /api/auth/me once and
// then reads the permission list the backend resolved. Every failure keeps the
// backend's machine readable code in `errorCode`; the copy layer localises it.
import { defineStore } from 'pinia'
import * as api from './api.js'

// The permission vocabulary, in the order the matrix renders it. It mirrors
// internal/authz.AllPermissions — a code the backend does not know is never
// granted, so this list is the contract the screen renders against.
export const PERMISSIONS = [
  'resources:read',
  'resources:write',
  'tasks:read',
  'tasks:write',
  'users:manage',
  'roles:manage',
  'grants:manage',
  'sessions:manage',
  'backups:manage',
]

function code(error) {
  return error?.code || 'internal_error'
}

export const useAuthStore = defineStore('auth', {
  state: () => ({
    user: null,
    permissions: [],
    roles: [],
    groups: [],
    providers: [],
    configured: false,
    sessions: [],
    users: [],
    rolesCatalogue: [],
    groupsCatalogue: [],
    permissionsCatalogue: [],
    grants: [],
    errorCode: '',
    loading: false,
  }),
  getters: {
    signedIn: (state) => Boolean(state.user?.id),
    isOwner: (state) => Boolean(state.user?.isOwner),
    /** The owner holds every permission, so the console never hides a screen from it. */
    can: (state) => (permission) => {
      if (state.user?.isOwner) return PERMISSIONS.includes(permission)
      return state.permissions.includes(permission)
    },
  },
  actions: {
    async loadConfig() {
      try {
        const data = await api.authConfig()
        this.providers = (data?.providers ?? []).map((provider) => provider.name)
        this.configured = Boolean(data?.configured)
        this.errorCode = ''
      } catch (error) {
        this.errorCode = code(error)
      }
    },
    async loadMe() {
      try {
        const data = await api.me()
        this.user = data?.user ?? null
        this.permissions = data?.permissions ?? []
        this.roles = data?.user?.roles ?? []
        this.groups = data?.user?.groups ?? []
        this.errorCode = ''
        return Boolean(this.user?.id)
      } catch (error) {
        this.user = null
        this.permissions = []
        this.errorCode = code(error)
        return false
      }
    },
    async login(email, password) {
      this.loading = true
      try {
        await api.login(email, password)
        this.errorCode = ''
        await this.loadMe()
        return true
      } catch (error) {
        this.errorCode = code(error)
        return false
      } finally {
        this.loading = false
      }
    },
    async register(payload) {
      this.loading = true
      try {
        await api.register(payload)
        this.errorCode = ''
        return true
      } catch (error) {
        this.errorCode = code(error)
        return false
      } finally {
        this.loading = false
      }
    },
    async logout() {
      try {
        await api.logout()
      } catch (error) {
        // A session that is already gone is not worth showing an error for.
        this.errorCode = ''
      }
      this.user = null
      this.permissions = []
      this.sessions = []
    },
    async loadSessions() {
      try {
        const data = await api.listSessions()
        this.sessions = data?.sessions ?? []
        this.errorCode = ''
      } catch (error) {
        this.errorCode = code(error)
      }
    },
    async revokeSession(id) {
      try {
        await api.revokeSession(id)
        await this.loadSessions()
        return true
      } catch (error) {
        this.errorCode = code(error)
        return false
      }
    },

    // --- administration ----------------------------------------------------
    async loadUsers(q = '') {
      try {
        const data = await api.listUsers(q)
        this.users = data?.users ?? []
        this.errorCode = ''
      } catch (error) {
        this.errorCode = code(error)
      }
    },
    async createUser(payload) {
      try {
        await api.createUser(payload)
        await this.loadUsers()
        this.errorCode = ''
        return true
      } catch (error) {
        this.errorCode = code(error)
        return false
      }
    },
    async updateUser(id, payload) {
      try {
        await api.updateUser(id, payload)
        await this.loadUsers()
        this.errorCode = ''
        return true
      } catch (error) {
        this.errorCode = code(error)
        return false
      }
    },
    async deleteUser(id) {
      try {
        await api.deleteUser(id)
        await this.loadUsers()
        this.errorCode = ''
        return true
      } catch (error) {
        this.errorCode = code(error)
        return false
      }
    },
    async revokeUserSessions(id) {
      try {
        await api.revokeUserSessions(id)
        this.errorCode = ''
        return true
      } catch (error) {
        this.errorCode = code(error)
        return false
      }
    },
    async loadRoles() {
      try {
        const data = await api.listRoles()
        this.rolesCatalogue = data?.roles ?? []
        this.errorCode = ''
      } catch (error) {
        this.errorCode = code(error)
      }
    },
    async createRole(payload) {
      try {
        await api.createRole(payload)
        await this.loadRoles()
        this.errorCode = ''
        return true
      } catch (error) {
        this.errorCode = code(error)
        return false
      }
    },
    async updateRole(id, payload) {
      try {
        await api.updateRole(id, payload)
        await this.loadRoles()
        this.errorCode = ''
        return true
      } catch (error) {
        this.errorCode = code(error)
        return false
      }
    },
    async deleteRole(id) {
      try {
        await api.deleteRole(id)
        await this.loadRoles()
        this.errorCode = ''
        return true
      } catch (error) {
        this.errorCode = code(error)
        return false
      }
    },
    async loadGroups() {
      try {
        const data = await api.listGroups()
        this.groupsCatalogue = data?.groups ?? []
        this.errorCode = ''
      } catch (error) {
        this.errorCode = code(error)
      }
    },
    async createGroup(payload) {
      try {
        await api.createGroup(payload)
        await this.loadGroups()
        this.errorCode = ''
        return true
      } catch (error) {
        this.errorCode = code(error)
        return false
      }
    },
    async deleteGroup(id) {
      try {
        await api.deleteGroup(id)
        await this.loadGroups()
        this.errorCode = ''
        return true
      } catch (error) {
        this.errorCode = code(error)
        return false
      }
    },
    async loadPermissions() {
      try {
        const data = await api.listPermissions()
        this.permissionsCatalogue = data?.permissions ?? []
        this.errorCode = ''
      } catch (error) {
        this.errorCode = code(error)
      }
    },
    async loadGrants() {
      try {
        const data = await api.listGrants()
        this.grants = data?.grants ?? []
        this.errorCode = ''
      } catch (error) {
        this.errorCode = code(error)
      }
    },
    async createGrant(payload) {
      try {
        await api.createGrant(payload)
        await this.loadGrants()
        this.errorCode = ''
        return true
      } catch (error) {
        this.errorCode = code(error)
        return false
      }
    },
    async deleteGrant(id) {
      try {
        await api.deleteGrant(id)
        await this.loadGrants()
        this.errorCode = ''
        return true
      } catch (error) {
        this.errorCode = code(error)
        return false
      }
    },
    clearError() {
      this.errorCode = ''
    },
  },
})
