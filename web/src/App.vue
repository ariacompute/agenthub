<script setup>
import { onMounted, watch } from 'vue'
import { RouterView } from 'vue-router'
import Sidebar from './components/Sidebar.vue'
import Topbar from './components/Topbar.vue'
import { useHubStore } from './lib/store.js'
import { setLang, useLang } from './lib/use-copy.js'
import { isRTL } from './lib/languages.js'

const store = useHubStore()
const lang = useLang()

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
  await Promise.all([store.refreshHealth(), store.refreshResources(), store.refreshTasks()])
  setInterval(() => store.refreshTasks(), 5000)
})
</script>

<template>
  <div class="shell">
    <Sidebar />
    <div class="main">
      <Topbar />
      <main class="content">
        <RouterView />
      </main>
    </div>
  </div>
</template>
