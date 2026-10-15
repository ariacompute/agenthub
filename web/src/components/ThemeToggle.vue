<script setup>
// Monochrome only: the toggle flips the grey scale, never the hue.
import { onMounted, ref } from 'vue'
import { useCopy } from '../lib/use-copy.js'
import { Moon, Sun } from 'lucide-vue-next'

const STORAGE_KEY = 'agenthub.theme'
const theme = ref('dark')
const { t } = useCopy('shell')

function apply(next) {
  theme.value = next
  if (typeof document !== 'undefined') {
    document.documentElement.dataset.theme = next
    window.localStorage.setItem(STORAGE_KEY, next)
  }
}

onMounted(() => {
  const stored = typeof window !== 'undefined' ? window.localStorage.getItem(STORAGE_KEY) : null
  apply(stored === 'light' ? 'light' : 'dark')
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
