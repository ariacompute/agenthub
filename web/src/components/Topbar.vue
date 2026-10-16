<script setup>
import { computed } from 'vue'
import { useHubStore } from '../lib/store.js'
import { useCopy } from '../lib/use-copy.js'
import LanguageSwitcher from './LanguageSwitcher.vue'
import ThemeToggle from './ThemeToggle.vue'

const store = useHubStore()
const { t } = useCopy('shell')

const mode = computed(() => store.health?.mode ?? 'mock')
const version = computed(() => store.health?.version ?? '')
const auth = computed(() => store.health?.auth ?? '')
</script>

<template>
  <header class="topbar">
    <span class="chip"><strong>{{ mode }}</strong></span>
    <!-- config.Version carries the tag verbatim, prefix included; the sidebar
         foot shows the fuller "version@commit". -->
    <span class="chip">{{ version }}</span>
    <span class="chip">auth: <strong>{{ auth }}</strong></span>
    <div class="spacer"></div>
    <LanguageSwitcher />
    <ThemeToggle />
  </header>
</template>
