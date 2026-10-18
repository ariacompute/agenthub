<script setup>
import { computed } from 'vue'
import { RouterLink, useRoute } from 'vue-router'
import { useCopy } from '../lib/use-copy.js'
import { useHubStore } from '../lib/store.js'
import { useAuthStore } from '../lib/auth-store.js'
import { Boxes, Network, Shield, Workflow } from 'lucide-vue-next'
import StateDot from './StateDot.vue'

const route = useRoute()
const store = useHubStore()
const auth = useAuthStore()
const { t } = useCopy('shell')

const items = computed(() => {
  const base = [
    { to: '/registry', label: t('navRegistry'), icon: Boxes },
    { to: '/trace', label: t('navTrace'), icon: Workflow },
    { to: '/topology', label: t('navTopology'), icon: Network },
  ]
  // The administration screen is only offered to accounts that can use it:
  // a link that answers 403 is worse than no link.
  if (auth.can('users:manage') || auth.can('roles:manage') || auth.can('grants:manage') || auth.can('backups:manage')) {
    base.push({ to: '/admin', label: t('navAdmin'), icon: Shield })
  }
  return base
})

const online = computed(() => Boolean(store.health))

// "version@commit", e.g. v0.2.0@1a2b3c4. The server stamps both at build time;
// the commit is trimmed here so a longer hash can never overflow the foot.
const build = computed(() => {
  const version = store.health?.version ?? ''
  const commit = String(store.health?.commit ?? '').slice(0, 7)
  return commit ? `${version}@${commit}` : version
})

// Divergence from the tag, e.g. "+2" and/or "dirty". Empty when the build sits
// exactly on a clean tag. Both halves are server-side enum-ish data, so they
// are rendered verbatim and never translated.
const buildNote = computed(() => store.health?.buildNote ?? '')
const buildLabel = computed(() => (buildNote.value ? `${build.value} · ${buildNote.value}` : build.value))
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
      <div class="sidebar-status">
        <StateDot :state="online ? 'healthy' : 'unknown'" :show-label="false" />
        <span>{{ t(online ? 'online' : 'offline') }}</span>
      </div>
      <!-- The note sits on its own line: appending it inline would be
           ellipsized away at the default sidebar width. -->
      <span v-if="build" class="sidebar-build" :title="buildLabel">{{ build }}</span>
      <span v-if="buildNote" class="sidebar-build sidebar-build-note" :title="buildLabel">{{ buildNote }}</span>
    </div>
  </aside>
</template>
