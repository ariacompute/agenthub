<script setup>
// Screen 5 — upstream registries and their synchronisation.
//
// A source is metadata only: the credential lives in an environment variable
// whose *name* is what the form collects. The offline fixture mode is offered
// explicitly because it is the only way to walk the whole pipeline — validation,
// dedup, curation — without reaching the network.
import { computed, onMounted, reactive, ref } from 'vue'
import { useCopy, enumLabel } from '../lib/use-copy.js'
import { useHubStore } from '../lib/store.js'
import { errorMessage } from '../lib/errors.js'
import { isDirty, snapshot, useEscape } from '../lib/use-dismiss.js'
import UnsavedPrompt from '../components/UnsavedPrompt.vue'
import { Plus, RefreshCw } from 'lucide-vue-next'

const store = useHubStore()
const { t } = useCopy('sources')
const shell = useCopy('shell')

const drawerOpen = ref(false)
const askSave = ref(false)
const baseline = ref(null)
const syncing = ref('')
const lastResult = ref(null)

const form = reactive({
  name: '',
  kind: 'mcp',
  url: '',
  namespace: 'default',
  authScheme: 'none',
  tokenEnv: '',
  schedule: '',
  fixture: true,
  enabled: true,
})

const errorMessageText = computed(() => errorMessage(store.errorCode))

onMounted(() => {
  store.refreshSources()
  store.refreshRegistries()
})

function openCreate() {
  Object.assign(form, {
    name: '',
    kind: 'mcp',
    url: '',
    namespace: 'default',
    authScheme: 'none',
    tokenEnv: '',
    schedule: '',
    fixture: true,
    enabled: true,
  })
  baseline.value = snapshot(form)
  drawerOpen.value = true
}

function closeDrawer() {
  drawerOpen.value = false
  askSave.value = false
  baseline.value = null
}

// Escape, the backdrop and the cancel button are one gesture with three
// entry points; only an edited form is worth interrupting for.
function requestClose() {
  if (isDirty(baseline.value, form)) {
    askSave.value = true
    return
  }
  closeDrawer()
}

useEscape(() => drawerOpen.value, requestClose)

// Saving from the prompt is the same save; the prompt steps aside so a
// failure is reported by the drawer's own error banner.
async function saveAndClose() {
  askSave.value = false
  await submit()
}

async function submit() {
  const ok = await store.saveSource({
    name: form.name.trim(),
    kind: form.kind,
    url: form.url.trim(),
    namespace: form.namespace.trim() || 'default',
    auth_scheme: form.authScheme,
    auth_token_env: form.authScheme === 'bearer' ? form.tokenEnv.trim() : '',
    schedule: form.schedule.trim(),
    fixture: form.fixture,
    enabled: form.enabled,
  })
  if (ok) closeDrawer()
}

async function sync(source) {
  syncing.value = source.id
  lastResult.value = null
  try {
    // A null result means the run failed; the store has already put the
    // machine readable code on errorCode, which the banner renders.
    lastResult.value = await store.syncSource(source.id)
  } finally {
    syncing.value = ''
  }
}

async function destroy(source) {
  if (!window.confirm(t('confirmDelete'))) return
  await store.removeSource(source.id)
}

function syncSummary(source) {
  if (!source.last_sync_status) return t('neverRun')
  return `${enumLabel('syncStatus', source.last_sync_status)} · ${source.last_sync_count}`
}
</script>

<template>
  <section>
    <div class="page-head">
      <h1>{{ t('title') }}</h1>
      <p class="page-sub">{{ t('subtitle') }}</p>
    </div>

    <div v-if="store.errorCode" class="error-line">
      <span>{{ errorMessageText }}</span>
      <button class="btn btn-ghost" type="button" @click="store.clearError()">
        {{ shell.t('actionClose') }}
      </button>
    </div>

    <div v-if="lastResult" class="stats card">
      <span>{{ t('imported') }} <b>{{ lastResult.imported }}</b></span>
      <span>{{ t('updated') }} <b>{{ lastResult.updated }}</b></span>
      <span>{{ t('skipped') }} <b>{{ lastResult.skipped }}</b></span>
    </div>

    <div class="toolbar">
      <div class="spacer"></div>
      <button class="btn btn-primary" type="button" @click="openCreate">
        <Plus :size="14" />
        {{ t('actionAdd') }}
      </button>
    </div>

    <div v-if="store.sources.length" class="grid">
      <article v-for="source in store.sources" :key="source.id" class="card resource-card">
        <div class="resource-head">
          <div style="min-width: 0">
            <div class="resource-title">{{ source.name }}</div>
            <div class="resource-endpoint mono">{{ source.url }}</div>
          </div>
        </div>
        <div class="tag-row">
          <span class="chip">{{ enumLabel('kind', source.kind) }}</span>
          <span class="chip mono">{{ source.namespace }}</span>
          <span class="chip">{{ enumLabel('sourceAuth', source.auth_scheme || 'none') }}</span>
          <span v-if="source.fixture" class="chip">{{ t('fieldFixture') }}</span>
          <span class="chip">{{ source.enabled ? t('fieldEnabled') : enumLabel('syncStatus', 'failed') }}</span>
        </div>
        <p class="muted note">{{ t('lastSync') }}: {{ syncSummary(source) }}</p>
        <div class="card-actions">
          <button class="btn" type="button" :disabled="syncing === source.id" @click="sync(source)">
            <RefreshCw :size="14" />
            {{ syncing === source.id ? t('syncRunning') : t('actionSync') }}
          </button>
          <button class="btn btn-ghost" type="button" @click="destroy(source)">
            {{ shell.t('actionDelete') }}
          </button>
        </div>
      </article>
    </div>
    <div v-else class="empty">{{ t('empty') }}</div>

    <div v-if="drawerOpen" class="drawer-backdrop" @click.self="requestClose">
      <form class="card drawer" @submit.prevent="submit">
        <div class="page-head">
          <h2>{{ t('drawerTitle') }}</h2>
        </div>
        <div class="field">
          <label for="s-name">{{ t('fieldName') }}</label>
          <input id="s-name" v-model="form.name" type="text" required />
        </div>
        <div class="field">
          <label for="s-kind">{{ t('fieldKind') }}</label>
          <select id="s-kind" v-model="form.kind">
            <option value="mcp">{{ enumLabel('kind', 'mcp') }}</option>
            <option value="skill">{{ enumLabel('kind', 'skill') }}</option>
          </select>
        </div>
        <div class="field">
          <label for="s-url">{{ t('fieldUrl') }}</label>
          <input id="s-url" v-model="form.url" class="mono" type="text" required placeholder="https://…" />
        </div>
        <div class="field">
          <label for="s-ns">{{ t('fieldTargetNamespace') }}</label>
          <input id="s-ns" v-model="form.namespace" class="mono" type="text" />
        </div>
        <div class="field">
          <label for="s-auth">{{ t('fieldAuthScheme') }}</label>
          <select id="s-auth" v-model="form.authScheme">
            <option value="none">{{ enumLabel('sourceAuth', 'none') }}</option>
            <option value="bearer">{{ enumLabel('sourceAuth', 'bearer') }}</option>
          </select>
        </div>
        <div v-if="form.authScheme === 'bearer'" class="field">
          <label for="s-token">{{ t('fieldTokenEnv') }}</label>
          <input id="s-token" v-model="form.tokenEnv" class="mono" type="text" required placeholder="AGENTHUB_UPSTREAM_TOKEN" />
          <p class="muted note">{{ t('tokenHint') }}</p>
        </div>
        <div class="field">
          <label for="s-schedule">{{ t('fieldSchedule') }}</label>
          <input id="s-schedule" v-model="form.schedule" class="mono" type="text" placeholder="0 3 * * *" />
        </div>
        <label class="check">
          <input v-model="form.fixture" type="checkbox" />
          <span>{{ t('fieldFixture') }}</span>
        </label>
        <p v-if="form.fixture" class="muted note">{{ t('fixtureHint') }}</p>
        <label class="check">
          <input v-model="form.enabled" type="checkbox" />
          <span>{{ t('fieldEnabled') }}</span>
        </label>
        <div class="drawer-actions">
          <button class="btn btn-primary" type="submit">{{ shell.t('actionSave') }}</button>
          <button class="btn btn-ghost" type="button" @click="requestClose">
            {{ shell.t('actionCancel') }}
          </button>
          <span class="muted note">{{ shell.t('escHint') }}</span>
        </div>
      </form>
    </div>

    <UnsavedPrompt
      v-if="askSave"
      :title="shell.t('unsavedTitle')"
      :body="shell.t('unsavedBody')"
      @save="saveAndClose"
      @discard="closeDrawer"
      @cancel="askSave = false"
    />
  </section>
</template>

<style scoped>
.note {
  margin-block-start: 6px;
  font-size: 11px;
}
.check {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 12px;
  color: var(--text-2);
}
</style>
