import { createRouter, createWebHistory } from 'vue-router'
import RegistryView from './views/RegistryView.vue'
import TaskTraceView from './views/TaskTraceView.vue'
import TopologyView from './views/TopologyView.vue'
import LoginView from './views/LoginView.vue'
import AdminView from './views/AdminView.vue'
import { useAuthStore } from './lib/auth-store.js'

const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/', redirect: '/registry' },
    { path: '/login', name: 'login', component: LoginView, meta: { public: true } },
    { path: '/registry', name: 'registry', component: RegistryView },
    { path: '/trace', name: 'trace', component: TaskTraceView },
    { path: '/topology', name: 'topology', component: TopologyView },
    { path: '/admin', name: 'admin', component: AdminView, meta: { permission: 'users:manage' } },
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
  const permission = to.meta.permission
  if (permission && !auth.can(permission)) {
    return { name: 'registry' }
  }
  return true
})

export default router
