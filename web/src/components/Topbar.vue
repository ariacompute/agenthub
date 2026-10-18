<script setup>
import { computed } from 'vue'
import { useRouter } from 'vue-router'
import { useHubStore } from '../lib/store.js'
import { useAuthStore } from '../lib/auth-store.js'
import { useCopy } from '../lib/use-copy.js'
import { LogOut } from 'lucide-vue-next'
import LanguageSwitcher from './LanguageSwitcher.vue'
import ThemeToggle from './ThemeToggle.vue'

const store = useHubStore()
const auth = useAuthStore()
const router = useRouter()
const { t } = useCopy('shell')
const account = useCopy('auth')

const mode = computed(() => store.health?.mode ?? 'mock')
const version = computed(() => store.health?.version ?? '')
const authMode = computed(() => store.health?.auth ?? '')

async function signOut() {
  await auth.logout()
  router.replace({ name: 'login' })
}
</script>

<template>
  <header class="topbar">
    <span class="chip"><strong>{{ mode }}</strong></span>
    <!-- config.Version carries the tag verbatim, prefix included; the sidebar
         foot shows the fuller "version@commit". -->
    <span class="chip">{{ version }}</span>
    <span class="chip">auth: <strong>{{ authMode }}</strong></span>
    <div class="spacer"></div>
    <template v-if="auth.signedIn">
      <span class="chip account">
        <strong>{{ auth.user.email }}</strong>
        <span v-if="auth.isOwner" class="chip">{{ account.t('ownerBadge') }}</span>
      </span>
      <button class="btn btn-ghost" type="button" @click="signOut">
        <LogOut :size="14" />
        <span>{{ account.t('actionLogout') }}</span>
      </button>
    </template>
    <LanguageSwitcher />
    <ThemeToggle />
  </header>
</template>

<style scoped>
.account {
  max-width: 280px;
}

.account strong {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.btn {
  min-width: 0;
}
</style>
