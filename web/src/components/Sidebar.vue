<script setup>
import { computed } from 'vue'
import { RouterLink, useRoute } from 'vue-router'
import { useCopy } from '../lib/use-copy.js'
import { useHubStore } from '../lib/store.js'
import { Boxes, Network, Workflow } from 'lucide-vue-next'
import StateDot from './StateDot.vue'

const route = useRoute()
const store = useHubStore()
const { t } = useCopy('shell')

const items = computed(() => [
  { to: '/registry', label: t('navRegistry'), icon: Boxes },
  { to: '/trace', label: t('navTrace'), icon: Workflow },
  { to: '/topology', label: t('navTopology'), icon: Network },
])

const online = computed(() => Boolean(store.health))
</script>

<template>
  <aside class="sidebar">
    <div class="brand">
      <div class="brand-mark" aria-hidden="true"></div>
      <div>
        <div class="brand-name">agenthub</div>
        <div class="brand-tagline">{{ t('brandTagline') }}</div>
      </div>
    </div>

    <nav class="nav">
      <RouterLink
        v-for="item in items"
        :key="item.to"
        :to="item.to"
        class="nav-item"
        :class="{ active: route.path === item.to }"
      >
        <component :is="item.icon" :size="16" />
        <span>{{ item.label }}</span>
      </RouterLink>
    </nav>

    <div class="sidebar-foot">
      <StateDot :state="online ? 'healthy' : 'unknown'" :show-label="false" />
      <span>{{ t(online ? 'online' : 'offline') }}</span>
    </div>
  </aside>
</template>
