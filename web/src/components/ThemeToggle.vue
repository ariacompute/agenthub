<script setup>
// Monochrome only: the toggle flips the grey scale, never the hue.
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { useCopy } from '../lib/use-copy.js'
import { Moon, Sun } from 'lucide-vue-next'

const STORAGE_KEY = 'agenthub.theme'

// Whatever the pre-paint script in index.html resolved: the stored choice if
// there is one, the OS preference otherwise.
const theme = ref(
  (typeof document !== 'undefined' && document.documentElement.dataset.theme) || 'dark',
)
const media =
  typeof window !== 'undefined' && window.matchMedia
    ? window.matchMedia('(prefers-color-scheme: light)')
    : null
const { t } = useCopy('shell')

function apply(next, persist = true) {
  theme.value = next
  if (typeof document !== 'undefined') {
    document.documentElement.dataset.theme = next
    // Only a deliberate choice is remembered. Following the OS writes nothing,
    // so a preference flipped at dusk is not frozen at what it was at boot.
    if (persist) window.localStorage.setItem(STORAGE_KEY, next)
  }
}

// A stored choice outranks the OS; with none, the console tracks it live.
function followSystem() {
  if (window.localStorage.getItem(STORAGE_KEY)) return
  apply(media?.matches ? 'light' : 'dark', false)
}

onMounted(() => {
  media?.addEventListener('change', followSystem)
})

onBeforeUnmount(() => {
  media?.removeEventListener('change', followSystem)
})
</script>

<template>
  <button
    class="chip"
    type="button"
    :aria-label="t('themeLabel')"
    :title="t('themeLabel')"
    @click="apply(theme === 'dark' ? 'light' : 'dark')"
  >
    <component :is="theme === 'dark' ? Moon : Sun" :size="14" />
  </button>
</template>
