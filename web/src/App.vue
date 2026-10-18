<script setup>
import { computed, onMounted, watch } from 'vue'
import { RouterView, useRoute } from 'vue-router'
import Sidebar from './components/Sidebar.vue'
import Topbar from './components/Topbar.vue'
import { useHubStore } from './lib/store.js'
import { useAuthStore } from './lib/auth-store.js'
import { setLang, useLang } from './lib/use-copy.js'
import { isRTL } from './lib/languages.js'

const store = useHubStore()
const auth = useAuthStore()
const lang = useLang()
const route = useRoute()

// The login screen is standalone: an anonymous visitor has no sidebar to look
// at and no account to show.
const isLogin = computed(() => route.name === 'login')

// Mirror the resolved language onto <html lang|dir> so RTL (Arabic) layouts flip.
watch(
  lang,
  (value) => {
    document.documentElement.lang = value
    document.documentElement.dir = isRTL(value) ? 'rtl' : 'ltr'
  },
  { immediate: true },
)

onMounted(async () => {
  setLang(lang.value)
  // Who the caller is decides what the rest of the console may show, so it is
  // resolved first; the router guard then redirects an anonymous visit.
  await Promise.all([auth.loadMe(), store.refreshHealth()])
  await Promise.all([store.refreshResources(), store.refreshTasks()])
  setInterval(() => store.refreshTasks(), 5000)
})
</script>

<template>
  <RouterView v-if="isLogin" />
  <div v-else class="shell">
    <Sidebar />
    <div class="main">
      <Topbar />
      <main class="content">
        <RouterView />
      </main>
    </div>
  </div>
</template>
