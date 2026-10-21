import { createRouter, createWebHistory } from 'vue-router'
import RegistryView from './views/RegistryView.vue'
import TaskTraceView from './views/TaskTraceView.vue'
import TopologyView from './views/TopologyView.vue'
import LoginView from './views/LoginView.vue'
import AdminView from './views/AdminView.vue'
import RegistriesView from './views/RegistriesView.vue'
import SourcesView from './views/SourcesView.vue'
import PlaygroundView from './views/PlaygroundView.vue'
import DocsView from './views/DocsView.vue'
import { useAuthStore } from './lib/auth-store.js'

const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/', redirect: '/registry' },
    { path: '/login', name: 'login', component: LoginView, meta: { public: true } },
    { path: '/registry', name: 'registry', component: RegistryView },
    { path: '/trace', name: 'trace', component: TaskTraceView },
    { path: '/topology', name: 'topology', component: TopologyView },
    // Namespaces double as the curator's queue, so either permission opens it:
    // a curator who may approve records is not necessarily the one creating
    // namespaces.
    {
      path: '/registries',
      name: 'registries',
      component: RegistriesView,
      meta: { permissions: ['registries:manage', 'registry:curate'] },
    },
    { path: '/sources', name: 'sources', component: SourcesView, meta: { permissions: ['sources:manage'] } },
    // The playground runs tasks, so it answers to the same permission as
    // cancelling and resuming one rather than to anything registry specific.
    { path: '/playground', name: 'playground', component: PlaygroundView, meta: { permissions: ['tasks:write'] } },
    // The administration screen carries several tabs, each gated by its own
    // permission inside AdminView: the API keys tab is reachable by any signed-in
    // user (they manage their own keys), so the route itself only needs a session.
    {
      path: '/admin',
      name: 'admin',
      component: AdminView,
      meta: {},
    },
    // Documentation is deliberately ungated: it explains what the console does,
    // so anyone who can sign in should be able to read it. Leaving
    // meta.permissions off means the guard only checks for a session.
    { path: '/docs', name: 'docs', component: DocsView },
  ],
})

// The console has no way to render anything useful without a session, so an
// anonymous visit lands on the login screen. The check runs against the store,
// which has already asked /api/auth/me once at boot.
router.beforeEach(async (to) => {
  const auth = useAuthStore()
  if (!auth.user && !auth.errorCode) await auth.loadMe()
  if (to.meta.public) {
    return auth.signedIn ? { name: 'registry' } : true
  }
  if (!auth.signedIn) {
    return { name: 'login', query: { redirect: to.fullPath } }
  }
  // A route may name one permission or a list; any of them is enough.
  const permissions = to.meta.permissions ?? (to.meta.permission ? [to.meta.permission] : [])
  if (permissions.length && !permissions.some((permission) => auth.can(permission))) {
    return { name: 'registry' }
  }
  return true
})

export default router
