<script setup>
import { computed, onMounted, reactive, ref } from 'vue'
import { useRouter } from 'vue-router'
import { useCopy, enumLabel } from '../lib/use-copy.js'
import { useHubStore, KINDS } from '../lib/store.js'
import { errorMessage } from '../lib/errors.js'
import { streamTask } from '../lib/api.js'
import { isDirty, snapshot, useEscape } from '../lib/use-dismiss.js'
import ResourceKindForm from '../components/ResourceKindForm.vue'
import UnsavedPrompt from '../components/UnsavedPrompt.vue'
import StateDot from '../components/StateDot.vue'
import { BookOpen, Bot, Boxes, Cpu, Plug, Plus, Search, Terminal } from 'lucide-vue-next'

const store = useHubStore()
const router = useRouter()
const { t } = useCopy('registry')
const shell = useCopy('shell')

const kindFilter = ref('')
const namespaceFilter = ref('')
const statusFilter = ref('')
const query = ref('')
const drawerOpen = ref(false)
const editing = ref(null)
const running = ref('')
const probing = ref('')
const askSave = ref(false)
const baseline = ref(null)

const KIND_ICONS = { model: Cpu, agent: Bot, mcp: Plug, skill: BookOpen, cli: Terminal, custom: Boxes }

const form = reactive({
  id: '',
  kind: 'model',
  name: '',
  displayName: '',
  description: '',
  namespace: 'default',
  endpoint: '',
  transport: '',
  version: '',
  tags: '',
  capabilities: '',
  health: 'healthy',
  kindConfig: {},
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

const namespaces = computed(() => store.registries.map((ns) => ns.id))

const visible = computed(() => {
  const q = query.value.trim().toLowerCase()
  return store.resources.filter((resource) => {
    if (kindFilter.value && resource.kind !== kindFilter.value) return false
    if (namespaceFilter.value && (resource.namespace || 'default') !== namespaceFilter.value) return false
    if (statusFilter.value && (resource.status || 'approved') !== statusFilter.value) return false
    if (!q) return true
    return [resource.name, resource.endpoint, ...(resource.tags ?? [])]
      .filter(Boolean)
      .some((field) => String(field).toLowerCase().includes(q))
  })
})

// Curation is a state machine, so the console only offers the edges that exist:
// a draft is submitted, a pending record is approved or rejected, an approved
// one is deprecated and a withdrawn one resubmitted.
const curationActions = {
  draft: ['pending'],
  pending: ['approved', 'rejected'],
  approved: ['deprecated'],
  rejected: ['pending'],
  deprecated: ['pending'],
}

onMounted(() => {
  if (!store.resources.length) store.refreshResources()
  store.refreshRegistries()
})

function iconFor(kind) {
  return KIND_ICONS[kind] ?? Cpu
}

function statusOf(resource) {
  return resource.status || 'approved'
}

function resetForm() {
  Object.assign(form, {
    id: '',
    kind: 'model',
    name: '',
    displayName: '',
    description: '',
    namespace: namespaceFilter.value || 'default',
    endpoint: '',
    transport: '',
    version: '',
    tags: '',
    capabilities: '',
    health: 'healthy',
    kindConfig: {},
  })
}

function openCreate() {
  editing.value = null
  resetForm()
  baseline.value = snapshot(form)
  drawerOpen.value = true
}

function openEdit(resource) {
  editing.value = resource
  Object.assign(form, {
    id: resource.id,
    kind: resource.kind,
    name: resource.name,
    displayName: resource.display_name ?? '',
    description: resource.description ?? '',
    namespace: resource.namespace ?? 'default',
    endpoint: resource.endpoint ?? '',
    transport: resource.transport ?? '',
    version: resource.version ?? '',
    tags: (resource.tags ?? []).join(', '),
    capabilities: (resource.capabilities ?? []).join(', '),
    health: resource.health ?? 'healthy',
    kindConfig: resource.kind_config ?? {},
  })
  baseline.value = snapshot(form)
  drawerOpen.value = true
}

function closeDrawer() {
  drawerOpen.value = false
  askSave.value = false
  baseline.value = null
}

// Closing is the same gesture wherever it starts — Escape, the backdrop, the
// cancel button — so all three funnel through here and only differ in whether
// they are allowed to ask first.
function requestClose() {
  if (isDirty(baseline.value, form)) {
    askSave.value = true
    return
  }
  closeDrawer()
}

useEscape(() => drawerOpen.value, requestClose)

function splitList(value) {
  return value
    .split(',')
    .map((item) => item.trim())
    .filter(Boolean)
}

function payload() {
  const out = {
    kind: form.kind,
    name: form.name.trim(),
    endpoint: form.endpoint.trim(),
    version: form.version.trim(),
    tags: splitList(form.tags),
    capabilities: splitList(form.capabilities),
    health: form.health,
    namespace: form.namespace.trim() || 'default',
  }
  if (form.displayName.trim()) out.display_name = form.displayName.trim()
  if (form.description.trim()) out.description = form.description.trim()
  if (form.transport) out.transport = form.transport
  // An empty descriptor is left out entirely rather than sent as {}.
  if (form.kindConfig && Object.keys(form.kindConfig).length) out.kind_config = form.kindConfig
  return out
}

// Saving from the prompt is the same save; the prompt steps aside so a
// failure is reported by the drawer's own error banner.
async function saveAndClose() {
  askSave.value = false
  await submit()
}

async function submit() {
  const ok = editing.value ? await store.update(editing.value.id, payload()) : await store.register(payload())
  if (ok) closeDrawer()
}

async function destroy(resource) {
  if (!window.confirm(t('confirmDelete'))) return
  await store.remove(resource.id)
}

async function curate(resource, status) {
  if (status === 'deprecated' && !window.confirm(t('confirmDeprecate'))) return
  await store.curate(resource.id, status)
}

function labelFor(status) {
  const key = { pending: 'actionSubmit', approved: 'actionApprove', rejected: 'actionReject', deprecated: 'actionDeprecate' }
  return t(key[status] ?? 'actionSubmit')
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
    // The run itself is the measurement: the hub records healthy or unhealthy
    // from what the call proved, so the list has to be re-read for the card's
    // dot to show it instead of the stale "unknown".
    await store.refreshResources()
    running.value = ''
  }
}

async function probe(resource) {
  probing.value = resource.id
  try {
    await store.probe(resource.id)
  } finally {
    probing.value = ''
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

    <div class="toolbar secondary">
      <select v-model="namespaceFilter" :aria-label="t('fieldNamespace')">
        <option value="">{{ t('fieldNamespace') }}: {{ t('tabAll') }}</option>
        <option v-for="ns in namespaces" :key="ns" :value="ns">{{ ns }}</option>
      </select>
      <select v-model="statusFilter" :aria-label="t('fieldStatus')">
        <option value="">{{ t('fieldStatus') }}: {{ t('tabAll') }}</option>
        <option v-for="status in ['draft', 'pending', 'approved', 'rejected', 'deprecated']" :key="status" :value="status">
          {{ enumLabel('status', status) }}
        </option>
      </select>
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
          <span class="chip">{{ enumLabel('status', statusOf(resource)) }}</span>
          <span v-if="resource.transport" class="chip">{{ enumLabel('transport', resource.transport) }}</span>
          <span v-if="resource.namespace && resource.namespace !== 'default'" class="chip mono">{{ resource.namespace }}</span>
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
          <button
            class="btn btn-ghost"
            type="button"
            :disabled="probing === resource.id"
            @click="probe(resource)"
          >
            {{ t('actionProbe') }}
          </button>
          <button class="btn btn-ghost" type="button" @click="openEdit(resource)">
            {{ shell.t('actionEdit') }}
          </button>
          <button class="btn btn-ghost" type="button" @click="destroy(resource)">
            {{ shell.t('actionDelete') }}
          </button>
        </div>

        <div v-if="(curationActions[statusOf(resource)] || []).length" class="card-actions curate">
          <button
            v-for="next in curationActions[statusOf(resource)]"
            :key="next"
            class="btn btn-ghost"
            type="button"
            @click="curate(resource, next)"
          >
            {{ labelFor(next) }}
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

    <div v-if="drawerOpen" class="drawer-backdrop" @click.self="requestClose">
      <form class="card drawer" @submit.prevent="submit">
        <div class="page-head">
          <h2>{{ t('drawerTitle') }}</h2>
        </div>

        <div class="field">
          <label>{{ t('fieldKind') }}</label>
          <div class="segmented">
            <button
              v-for="kind in KINDS"
              :key="kind"
              type="button"
              class="segment"
              :class="{ active: form.kind === kind }"
              @click="form.kind = kind"
            >
              {{ enumLabel('kind', kind) }}
            </button>
          </div>
        </div>

        <h3 class="section-title">{{ t('sectionCommon') }}</h3>
        <div class="field">
          <label for="f-name">{{ t('fieldName') }}</label>
          <input id="f-name" v-model="form.name" type="text" required />
        </div>
        <div class="field">
          <label for="f-display">{{ t('fieldDisplayName') }}</label>
          <input id="f-display" v-model="form.displayName" type="text" />
        </div>
        <div class="field">
          <label for="f-namespace">{{ t('fieldNamespace') }}</label>
          <input id="f-namespace" v-model="form.namespace" class="mono" type="text" />
        </div>
        <div class="field">
          <label for="f-description">{{ t('fieldDescription') }}</label>
          <input id="f-description" v-model="form.description" type="text" />
        </div>
        <div class="field">
          <label for="f-endpoint">{{ t('fieldEndpoint') }}</label>
          <input id="f-endpoint" v-model="form.endpoint" class="mono" type="text" />
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
          <input id="f-version" v-model="form.version" class="mono" type="text" />
        </div>
        <div class="field">
          <label for="f-tags">{{ t('fieldTags') }}</label>
          <input id="f-tags" v-model="form.tags" type="text" placeholder="hr, legacy" />
        </div>

        <h3 class="section-title">{{ t('sectionKindConfig') }}</h3>
        <ResourceKindForm v-model="form.kindConfig" :kind="form.kind" />

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

.toolbar.secondary {
  margin-block-start: -6px;
  gap: 8px;
}

.toolbar.secondary select {
  max-width: 220px;
}

.section-title {
  margin-block: 4px 2px;
  font-size: 12px;
  font-weight: 500;
  color: var(--text-3);
  text-transform: uppercase;
  letter-spacing: 0.04em;
}

.segmented {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}

.segment {
  padding: 7px 10px;
  font-size: 12px;
  color: var(--text-2);
  background: var(--bg-elevated);
  border: 1px solid var(--stroke-2);
  border-radius: var(--radius-sm);
  transition: border-color 0.16s ease, color 0.16s ease;
}

.segment.active {
  color: var(--text-1);
  border-color: var(--stroke-3);
}

.curate {
  margin-block-start: 6px;
  padding-block-start: 8px;
  border-top: 1px solid var(--stroke-1);
}
</style>
