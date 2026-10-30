<script setup>
import { computed, onMounted, reactive, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useAuthStore } from '../lib/auth-store.js'
import { useHubStore } from '../lib/store.js'
import { useCopy } from '../lib/use-copy.js'
import { errorMessage } from '../lib/errors.js'
import BrandMark from '../components/BrandMark.vue'
import LanguageSwitcher from '../components/LanguageSwitcher.vue'
import ThemeToggle from '../components/ThemeToggle.vue'
import { Github, KeyRound, LogIn, Mail } from 'lucide-vue-next'

const auth = useAuthStore()
const hub = useHubStore()
const router = useRouter()
const route = useRoute()
const { t } = useCopy('auth')
const shell = useCopy('shell')

const mode = ref('login')
const form = reactive({ email: '', password: '', confirm: '', name: '' })

const providers = computed(() => auth.providers)
// A provider without credentials is never rendered: the console must not offer
// a button that ends in "oauth_not_configured".
const oauthVisible = computed(() => providers.value.length > 0)

const errorText = computed(() => errorMessage(auth.errorCode || String(route.query.error || '')))

const build = computed(() => {
  const version = hub.health?.version ?? ''
  const commit = String(hub.health?.commit ?? '').slice(0, 7)
  return commit ? `${version}@${commit}` : version
})

onMounted(() => {
  auth.loadConfig()
  if (!hub.health) hub.refreshHealth()
})

function toggleMode() {
  mode.value = mode.value === 'login' ? 'register' : 'login'
  auth.clearError()
}

async function submit() {
  auth.clearError()
  const ok =
    mode.value === 'login'
      ? await auth.login(form.email, form.password)
      : await auth.register({ email: form.email, password: form.password, name: form.name })
  if (!ok) return
  if (mode.value === 'register') {
    // Registering does not open a session: sign in with the new credentials so
    // the console lands on an account it can render.
    if (!(await auth.login(form.email, form.password))) return
  }
  // The health chip was read while the visitor was still anonymous; re-read it
  // now that the session exists, or it would keep saying so.
  await hub.refreshHealth()
  const target = typeof route.query.redirect === 'string' ? route.query.redirect : '/registry'
  router.replace(target)
}
</script>

<template>
  <div class="login">
    <header class="login-head">
      <div class="brand">
        <div class="brand-mark" aria-hidden="true">
          <BrandMark :size="30" />
        </div>
        <div>
          <div class="brand-name">agenthub</div>
          <div class="brand-tagline">{{ shell.t('brandTagline') }}</div>
        </div>
      </div>
      <div class="login-head-actions">
        <LanguageSwitcher />
        <ThemeToggle />
      </div>
    </header>

    <section class="login-card card">
      <div class="page-head">
        <h1>{{ t('title') }}</h1>
        <p class="page-sub">{{ t('subtitle') }}</p>
      </div>

      <div class="tabs" role="tablist">
        <button
          class="tab"
          type="button"
          role="tab"
          :class="{ active: mode === 'login' }"
          :aria-selected="mode === 'login'"
          @click="toggleMode"
        >
          {{ t('tabLogin') }}
        </button>
        <button
          class="tab"
          type="button"
          role="tab"
          :class="{ active: mode === 'register' }"
          :aria-selected="mode === 'register'"
          @click="toggleMode"
        >
          {{ t('tabRegister') }}
        </button>
      </div>

      <!-- Enter submits: the form is the only thing on the screen. -->
      <form class="login-form" @submit.prevent="submit">
        <div class="field">
          <label for="login-email">{{ t('fieldEmail') }}</label>
          <div class="input-wrap">
            <Mail :size="14" aria-hidden="true" />
            <input
              id="login-email"
              v-model="form.email"
              type="email"
              autocomplete="email"
              required
              :aria-label="t('fieldEmail')"
            />
          </div>
        </div>

        <div class="field">
          <label for="login-password">{{ t('fieldPassword') }}</label>
          <div class="input-wrap">
            <KeyRound :size="14" aria-hidden="true" />
            <input
              id="login-password"
              v-model="form.password"
              type="password"
              :autocomplete="mode === 'register' ? 'new-password' : 'current-password'"
              required
              :aria-label="t('fieldPassword')"
            />
          </div>
          <span v-if="mode === 'register'" class="muted">{{ t('passwordHint') }}</span>
        </div>

        <div v-if="mode === 'register'" class="field">
          <label for="login-confirm">{{ t('fieldConfirm') }}</label>
          <div class="input-wrap">
            <KeyRound :size="14" aria-hidden="true" />
            <input
              id="login-confirm"
              v-model="form.confirm"
              type="password"
              autocomplete="new-password"
              required
              :aria-label="t('fieldConfirm')"
            />
          </div>
        </div>

        <div v-if="mode === 'register'" class="field">
          <label for="login-name">{{ t('fieldName') }}</label>
          <input id="login-name" v-model="form.name" type="text" :aria-label="t('fieldName')" />
        </div>

        <p v-if="auth.errorCode || route.query.error" class="error-line">{{ errorText }}</p>

        <button class="btn btn-primary login-submit" type="submit" :disabled="auth.loading">
          <LogIn :size="15" />
          <span>{{ mode === 'login' ? t('actionLogin') : t('actionRegister') }}</span>
        </button>
      </form>

      <div v-if="oauthVisible" class="login-oauth">
        <span class="muted">{{ t('oauthLabel') }}</span>
        <a
          v-for="provider in providers"
          :key="provider"
          class="btn btn-ghost"
          :href="`/api/auth/oauth/${provider}`"
        >
          <Github v-if="provider === 'github'" :size="15" />
          <span>{{ provider }}</span>
        </a>
      </div>
      <p v-else class="muted login-hint">{{ t('oauthUnavailable') }}</p>

      <button class="login-switch" type="button" @click="toggleMode">
        {{ mode === 'login' ? t('switchToRegister') : t('switchToLogin') }}
      </button>
    </section>

    <footer class="login-foot">
      <span v-if="build" class="sidebar-build">{{ build }}</span>
      <span v-if="hub.health?.buildNote" class="sidebar-build sidebar-build-note">
        {{ hub.health.buildNote }}
      </span>
    </footer>
  </div>
</template>

<style scoped>
.login {
  min-height: 100vh;
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 20px;
  padding: 24px;
}

.login-head {
  width: min(880px, 100%);
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
}

.login-head-actions {
  display: flex;
  align-items: center;
  gap: 10px;
}

.login-card {
  /* The card is the reason the page exists, so it sits in the middle of the
     viewport instead of wherever the header happens to leave it. Auto margins
     rather than justify-content: when the form is taller than the window the
     margins collapse and the page scrolls from the first field, where a
     centred flex item would have its top clipped. */
  margin-block: auto;
  width: min(520px, 100%);
  padding: 24px;
  display: flex;
  flex-direction: column;
  gap: 16px;
  animation: drawer-in 0.18s ease;
}

.login-form {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

/* Icon inside the field, never a coloured affordance. */
.input-wrap {
  display: flex;
  align-items: center;
  gap: 8px;
  padding-inline: 10px;
  border: 1px solid var(--stroke-2);
  border-radius: var(--radius-sm);
  background: var(--bg-elevated);
  color: var(--text-3);
  transition: border-color 0.16s ease, box-shadow 0.16s ease;
}

.input-wrap:focus-within {
  border-color: var(--stroke-3);
  box-shadow: 0 0 0 3px rgba(255, 255, 255, 0.06);
}

.input-wrap input {
  flex: 1;
  min-width: 0;
  border: none;
  background: transparent;
  padding: 9px 0;
}

.input-wrap input:focus {
  border: none;
  box-shadow: none;
}

.login-submit {
  width: 100%;
}

.login-oauth {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
  padding-block-start: 4px;
  border-block-start: 1px solid var(--stroke-1);
}

.login-oauth a {
  text-decoration: none;
}

.login-hint {
  font-size: 12px;
}

.login-switch {
  align-self: flex-start;
  border: none;
  background: transparent;
  color: var(--text-2);
  padding: 0;
  text-decoration: underline;
  text-underline-offset: 3px;
  transition: color 0.16s ease;
}

.login-switch:hover {
  color: var(--text-1);
}

.login-foot {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 4px;
}
</style>
