<script setup>
import { computed, onMounted, reactive, ref } from 'vue'
import { useRouter } from 'vue-router'
import { useCopy, enumLabel } from '../lib/use-copy.js'
import { useHubStore, KINDS } from '../lib/store.js'
import { errorMessage } from '../lib/errors.js'
import { streamTask } from '../lib/api.js'
import StateDot from '../components/StateDot.vue'
import { BookOpen, Bot, Cpu, Plug, Plus, Search, Terminal } from 'lucide-vue-next'

const store = useHubStore()
const router = useRouter()
const { t } = useCopy('registry')
const shell = useCopy('shell')

const kindFilter = ref('')
const query = ref('')
const drawerOpen = ref(false)
const editing = ref(null)
const running = ref('')

const KIND_ICONS = { model: Cpu, agent: Bot, mcp: Plug, skill: BookOpen, cli: Terminal }

const form = reactive({
  id: '',
  kind: 'model',
  name: '',
  endpoint: '',
  transport: '',
  version: '',
  tags: '',
  capabilities: '',
  health: 'healthy',
})

const errorMessageText = computed(() => errorMessage(store.errorCode))

const tabs = computed(() => [
  { key: '', label: t('tabAll'), count: store.resources.length },
  ...KINDS.map((kind) => ({
    key: kind,
    label: enumLabel('kind', kind),
    count: store.counts[kind] ?? 0,
  })),
])

const visible = computed(() => {
  const q = query.value.trim().toLowerCase()
  return store.resources.filter((resource) => {
    if (kindFilter.value && resource.kind !== kindFilter.value) return false
    if (!q) return true
    return [resource.name, resource.endpoint, ...(resource.tags ?? [])]
      .filter(Boolean)
      .some((field) => String(field).toLowerCase().includes(q))
  })
})

onMounted(() => {
  if (!store.resources.length) store.refreshResources()
})

function iconFor(kind) {
  return KIND_ICONS[kind] ?? Cpu
}

function openCreate() {
  editing.value = null
  Object.assign(form, {
    id: '',
    kind: 'model',
    name: '',
    endpoint: '',
    transport: '',
    version: '',
    tags: '',
    capabilities: '',
    health: 'healthy',
  })
  drawerOpen.value = true
}

function openEdit(resource) {
  editing.value = resource
  Object.assign(form, {
    id: resource.id,
    kind: resource.kind,
    name: resource.name,
    endpoint: resource.endpoint ?? '',
    transport: resource.transport ?? '',
    version: resource.version ?? '',
    tags: (resource.tags ?? []).join(', '),
    capabilities: (resource.capabilities ?? []).join(', '),
    health: resource.health ?? 'healthy',
  })
  drawerOpen.value = true
}

function splitList(value) {
  return value
    .split(',')
    .map((item) => item.trim())
    .filter(Boolean)
}

async function submit() {
  const payload = {
    kind: form.kind,
    name: form.name.trim(),
    endpoint: form.endpoint.trim(),
    version: form.version.trim(),
    tags: splitList(form.tags),
    capabilities: splitList(form.capabilities),
    health: form.health,
  }
  if (form.transport) payload.transport = form.transport
  const ok = editing.value ? await store.update(editing.value.id, payload) : await store.register(payload)
  if (ok) drawerOpen.value = false
}

async function destroy(resource) {
  if (!window.confirm(t('confirmDelete'))) return
  await store.remove(resource.id)
}

async function tryRun(resource) {
  running.value = resource.id
  try {
    let taskID = ''
    await streamTask({ resourceId: resource.id, text: `probe ${resource.name}` }, (frame) => {
      const data = frame.data ?? {}
      if (data.taskId) taskID = data.taskId
      if (data.kind === 'status-update') taskID = data.taskId || taskID
    })
    await store.refreshTasks()
    store.selectTask(taskID)
    router.push('/trace')
  } catch (error) {
    console.error('agenthub: try run failed', error)
  } finally {
    running.value = ''
  }
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

    <div class="toolbar">
      <div class="tabs">
        <button
          v-for="tab in tabs"
          :key="tab.key"
          class="tab"
          :class="{ active: kindFilter === tab.key }"
          type="button"
          @click="kindFilter = tab.key"
        >
          {{ tab.label }}
          <span class="tab-count">{{ tab.count }}</span>
        </button>
      </div>
      <div class="spacer"></div>
      <div class="search-wrap">
        <Search :size="14" />
        <input v-model="query" type="search" :placeholder="t('searchPlaceholder')" />
      </div>
      <button class="btn btn-primary" type="button" @click="openCreate">
        <Plus :size="14" />
        {{ t('actionRegister') }}
      </button>
    </div>

    <div v-if="visible.length" class="grid">
      <article v-for="resource in visible" :key="resource.id" class="card resource-card">
        <div class="resource-head">
          <span class="kind-icon" :class="resource.kind">
            <component :is="iconFor(resource.kind)" :size="16" />
          </span>
          <div style="min-width: 0">
            <div class="resource-title">{{ resource.name }}</div>
            <div class="resource-endpoint mono">{{ resource.endpoint || '—' }}</div>
          </div>
        </div>

        <div class="tag-row">
          <span class="chip">{{ enumLabel('kind', resource.kind) }}</span>
          <span v-if="resource.transport" class="chip">{{ enumLabel('transport', resource.transport) }}</span>
          <span v-for="tag in resource.tags ?? []" :key="tag" class="chip">#{{ tag }}</span>
        </div>

        <StateDot :state="resource.health" />

        <div class="card-actions">
          <button
            class="btn"
            type="button"
            :disabled="running === resource.id"
            @click="tryRun(resource)"
          >
            {{ t('actionTryRun') }}
          </button>
          <button class="btn btn-ghost" type="button" @click="openEdit(resource)">
            {{ shell.t('actionSave') }}
          </button>
          <button class="btn btn-ghost" type="button" @click="destroy(resource)">
            {{ shell.t('actionDelete') }}
          </button>
        </div>
      </article>
    </div>
    <div v-else class="empty">{{ t('empty') }}</div>

    <div class="stats card">
      <span>{{ t('statsTotal') }} <b>{{ store.resources.length }}</b></span>
      <span>{{ t('statsHealthy') }} <b>{{ store.healthCounts.healthy ?? 0 }}</b></span>
      <span class="mono">{{ t('healthLabel') }}: {{ JSON.stringify(store.healthCounts) }}</span>
    </div>

    <div v-if="drawerOpen" class="drawer-backdrop" @click.self="drawerOpen = false">
      <form class="card drawer" @submit.prevent="submit">
        <div class="page-head">
          <h2>{{ t('drawerTitle') }}</h2>
        </div>

        <div class="field">
          <label for="f-kind">{{ t('fieldKind') }}</label>
          <select id="f-kind" v-model="form.kind">
            <option v-for="kind in KINDS" :key="kind" :value="kind">{{ enumLabel('kind', kind) }}</option>
          </select>
        </div>
        <div class="field">
          <label for="f-name">{{ t('fieldName') }}</label>
          <input id="f-name" v-model="form.name" type="text" required />
        </div>
        <div class="field">
          <label for="f-endpoint">{{ t('fieldEndpoint') }}</label>
          <input id="f-endpoint" v-model="form.endpoint" type="text" />
        </div>
        <div class="field">
          <label for="f-transport">{{ t('fieldTransport') }}</label>
          <select id="f-transport" v-model="form.transport">
            <option value="">—</option>
            <option value="stdio">stdio</option>
            <option value="streamable-http">streamable-http</option>
            <option value="sse-legacy">sse-legacy</option>
          </select>
        </div>
        <div class="field">
          <label for="f-version">{{ t('fieldVersion') }}</label>
          <input id="f-version" v-model="form.version" type="text" />
        </div>
        <div class="field">
          <label for="f-tags">{{ t('fieldTags') }}</label>
          <input id="f-tags" v-model="form.tags" type="text" placeholder="hr, legacy" />
        </div>

        <div style="display: flex; gap: 8px; margin-top: 8px">
          <button class="btn btn-primary" type="submit">{{ shell.t('actionSave') }}</button>
          <button class="btn btn-ghost" type="button" @click="drawerOpen = false">
            {{ shell.t('actionCancel') }}
          </button>
        </div>
      </form>
    </div>
  </section>
</template>

<style scoped>
.search-wrap {
  display: flex;
  align-items: center;
  gap: 7px;
  padding: 0 10px;
  border: 1px solid var(--stroke-2);
  border-radius: var(--radius-sm);
  background: var(--bg-elevated);
  color: var(--text-3);
}

.search-wrap input {
  border: none;
  background: transparent;
  padding-inline: 0;
}

.search-wrap input:focus {
  box-shadow: none;
}
</style>
