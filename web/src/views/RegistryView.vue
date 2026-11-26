<script setup>
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
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
  // Publishing is an explicit act: a new record starts private and stays visible
  // only to its owner and to whoever holds a grant.
  visibility: 'restricted',
  kindConfig: {},
})

const errorMessageText = computed(() => errorMessage(store.errorCode))

// Paging is a server-side window: the console asks for one page and the answer
// carries the total, so a directory of thousands never lands in the browser. The
// counts stay whole-catalogue (the server computes them without the kind filter),
// which is why the tab strip still reads correctly while one kind is selected.
const PAGE_SIZES = [25, 50, 100]
const pageSize = ref(PAGE_SIZES[0])
const pageIndex = ref(0)

const tabs = computed(() => [
  { key: '', label: t('tabAll'), count: store.resourceTotal },
  ...KINDS.map((kind) => ({
    key: kind,
    label: enumLabel('kind', kind),
    count: store.counts[kind] ?? 0,
  })),
])

const namespaces = computed(() => store.registries.map((ns) => ns.id))

// rows is exactly the page the server sent: no second filter pass, so what the
// pager says and what the grid shows cannot disagree.
const rows = computed(() => store.resources)

const pageCount = computed(() => Math.max(1, Math.ceil(store.resourceTotal / pageSize.value)))
const rangeStart = computed(() => (store.resourceTotal === 0 ? 0 : pageIndex.value * pageSize.value + 1))
const rangeEnd = computed(() => Math.min(store.resourceTotal, pageIndex.value * pageSize.value + rows.value.length))
const canPrev = computed(() => pageIndex.value > 0)
const canNext = computed(() => pageIndex.value + 1 < pageCount.value)
const rangeLabel = computed(() =>
  t('pageRange')
    .replace('{from}', String(rangeStart.value))
    .replace('{to}', String(rangeEnd.value))
    .replace('{total}', String(store.resourceTotal)),
)

function load() {
  return store.refreshResources({
    kind: kindFilter.value,
    namespace: namespaceFilter.value,
    status: statusFilter.value,
    q: query.value.trim(),
    limit: pageSize.value,
    offset: pageIndex.value * pageSize.value,
  })
}

// A filter change starts at the first page: staying on page four of a result set
// that just shrank to two pages would show an empty grid and an honest "0 of 40".
function reloadFromFirstPage() {
  pageIndex.value = 0
  return load()
}

// The store re-reads through the remembered window after a write, so the only
// thing left to fix up is the page cursor: deleting the last record on the last
// page must step back one page instead of showing "no records" over a catalogue
// that still has some.
async function settlePage() {
  if (!rows.value.length && pageIndex.value > 0) {
    pageIndex.value = Math.min(pageIndex.value - 1, pageCount.value - 1)
    await load()
  }
}

watch([kindFilter, namespaceFilter, statusFilter], reloadFromFirstPage)
watch(pageSize, reloadFromFirstPage)

// The search runs against the backend, so one keystroke must not become one
// request: 250ms is short enough to feel live and long enough to type a word.
let searchTimer = null
watch(query, () => {
  clearTimeout(searchTimer)
  searchTimer = setTimeout(reloadFromFirstPage, 250)
})
onBeforeUnmount(() => clearTimeout(searchTimer))

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
  // Always re-read: health is written by the hub on every run and probe, so a
  // list cached from an earlier visit is stale the moment a task finishes. The
  // old guard ("only when empty") left a card showing "unknown" after a Try run
  // that had just proved it healthy, because coming back from the trace view
  // never refetched.
  load()
  store.refreshRegistries()
})

function iconFor(kind) {
  return KIND_ICONS[kind] ?? Cpu
}

function statusOf(resource) {
  return resource.status || 'approved'
}

// ---------------------------------------------------------------------------
// Ability distribution
//
// The bindings live on the agent (kind_config.agent.mcp / .skills) and are
// edited by ResourceKindForm. This half is the delivery: preview the package the
// server would hand over, and push it as a task.
// ---------------------------------------------------------------------------

const bindingsLoaded = ref(false)
const previewOpen = ref(false)
const pushing = ref(false)

const mcpOptions = computed(() => store.bindingOptions.mcp)
const skillOptions = computed(() => store.bindingOptions.skill)
// A package can only be delivered for a record that exists; a half-filled new
// agent has an empty id and nothing bound yet.
const canDistribute = computed(() => form.kind === 'agent' && !!editing.value && !!editing.value.id)
const distribution = computed(() => store.distribution)
const previewText = computed(() => (distribution.value ? JSON.stringify(distribution.value, null, 2) : ''))

// The option lists are read once per screen: they are the whole catalogue of one
// kind and the server already filtered them by what this caller may see.
async function ensureBindings() {
  if (bindingsLoaded.value) return
  bindingsLoaded.value = await store.refreshBindingOptions()
}

function resetDistribution() {
  store.clearDistribution()
  previewOpen.value = false
}

watch(
  () => form.kind,
  (kind) => {
    if (kind === 'agent') ensureBindings()
  },
)

async function togglePreview() {
  if (!canDistribute.value) return
  if (previewOpen.value) {
    previewOpen.value = false
    return
  }
  // A failed preview shows the error banner and leaves the panel closed: an
  // empty JSON view would read like "this agent has nothing bound".
  if (await store.loadDistribution(editing.value.id)) previewOpen.value = true
}

async function refreshPreview() {
  if (!canDistribute.value) return
  await store.loadDistribution(editing.value.id)
}

async function pushDistribution() {
  if (!canDistribute.value || pushing.value) return
  pushing.value = true
  try {
    await store.pushDistribution(editing.value.id)
  } finally {
    pushing.value = false
  }
}

function openDistributionTrace() {
  if (!store.distributionTaskID) return
  store.selectTask(store.distributionTaskID)
  router.push('/trace')
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
    visibility: 'restricted',
    kindConfig: {},
  })
}

function openCreate() {
  editing.value = null
  resetForm()
  resetDistribution()
  baseline.value = snapshot(form)
  drawerOpen.value = true
}

function openEdit(resource) {
  editing.value = resource
  resetDistribution()
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
    visibility: resource.visibility ?? 'restricted',
    kindConfig: resource.kind_config ?? {},
  })
  // The picker needs the catalogues that exist right now, including ones
  // registered since the drawer was last opened.
  if (resource.kind === 'agent') ensureBindings()
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
  visibility: form.visibility,
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
  if (!ok) return
  closeDrawer()
  await settlePage()
}

async function destroy(resource) {
  if (!window.confirm(t('confirmDelete'))) return
  await store.remove(resource.id)
  await settlePage()
}

async function curate(resource, status) {
  if (status === 'deprecated' && !window.confirm(t('confirmDeprecate'))) return
  await store.curate(resource.id, status)
  await settlePage()
}

function labelFor(status) {
  const key = { pending: 'actionSubmit', approved: 'actionApprove', rejected: 'actionReject', deprecated: 'actionDeprecate' }
  return t(key[status] ?? 'actionSubmit')
}

async function tryRun(resource) {
  running.value = resource.id
  try {
    let taskID = ''
    // Jump to the task trace view first, then let the run stream live there
    // instead of blocking on the registry card until it reaches a terminal state.
    // The task is selected the moment its id arrives so the trace view can attach
    // its live feed to exactly this run, in every language.
    router.push('/trace')
    await streamTask({ resourceId: resource.id, text: `probe ${resource.name}` }, (frame) => {
      const data = frame.data ?? {}
      if (data.taskId) {
        taskID = data.taskId
        store.selectTask(taskID)
      }
      if (data.kind === 'status-update') taskID = data.taskId || taskID
    })
    await store.refreshTasks()
    if (taskID) store.selectTask(taskID)
  } catch (error) {
    console.error('agenthub: try run failed', error)
  } finally {
    // The run itself is the measurement: the hub records healthy or unhealthy
    // from what the call proved, so the list has to be re-read for the card's
    // dot to show it instead of the stale "unknown". The call passes no window,
    // so the store re-reads the page this screen asked for.
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

    <div v-if="rows.length" class="grid">
      <article v-for="resource in rows" :key="resource.id" class="card resource-card">
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
        <span
          v-if="(resource.visibility || 'restricted') === 'public'"
          class="chip"
          :title="t('fieldVisibilityHint')"
        >{{ enumLabel('visibility', 'public') }}</span>
          <span v-if="resource.transport" class="chip">{{ enumLabel('transport', resource.transport) }}</span>
          <span v-if="resource.namespace && resource.namespace !== 'default'" class="chip mono">{{ resource.namespace }}</span>
          <span v-for="tag in resource.tags ?? []" :key="tag" class="chip">#{{ tag }}</span>
        </div>

        <StateDot :state="resource.health" />

        <div class="card-actions">
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

        <!-- Try run leads the curation row, ahead of Deprecate/Approve/Reject.
             The row is no longer conditional on there being a transition: every
             status has one today, but hiding the row would hide the run button
             with it, and a run is the only action that is always available. -->
        <div class="card-actions curate">
          <button
            class="btn"
            type="button"
            :disabled="running === resource.id"
            @click="tryRun(resource)"
          >
            {{ t('actionTryRun') }}
          </button>
          <button
            v-for="next in curationActions[statusOf(resource)] ?? []"
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

    <!-- The pager states the window it is showing and how much exists: a page of
         25 out of 137 is a fact the server reports, not one the console infers. -->
    <nav v-if="store.resourceTotal > 0" class="pager" :aria-label="t('paginationLabel')">
      <button
        class="btn btn-ghost pager-btn"
        type="button"
        :disabled="!canPrev"
        :aria-label="t('pagePrev')"
        @click="pageIndex = Math.max(0, pageIndex - 1)"
      >
        {{ t('pagePrev') }}
      </button>
      <span class="pager-range mono">{{ rangeLabel }}</span>
      <button
        class="btn btn-ghost pager-btn"
        type="button"
        :disabled="!canNext"
        :aria-label="t('pageNext')"
        @click="pageIndex = pageIndex + 1"
      >
        {{ t('pageNext') }}
      </button>
      <label class="pager-size">
        <span>{{ t('pageSize') }}</span>
        <select v-model.number="pageSize" :aria-label="t('pageSize')">
          <option v-for="size in PAGE_SIZES" :key="size" :value="size">{{ size }}</option>
        </select>
      </label>
    </nav>

    <div class="stats card">
      <span>{{ t('statsTotal') }} <b>{{ store.resourceTotal }}</b></span>
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
          <label for="f-visibility">{{ t('fieldVisibility') }}</label>
          <select id="f-visibility" v-model="form.visibility">
            <option value="restricted">{{ enumLabel('visibility', 'restricted') }}</option>
            <option value="public">{{ enumLabel('visibility', 'public') }}</option>
          </select>
          <p class="audit-empty-hint">{{ t('fieldVisibilityHint') }}</p>
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
        <ResourceKindForm
          v-model="form.kindConfig"
          :kind="form.kind"
          :mcp-options="mcpOptions"
          :skill-options="skillOptions"
        />

        <template v-if="canDistribute">
          <h3 class="section-title">{{ t('distributionTitle') }}</h3>
          <div class="distribution">
            <p class="muted note">{{ t('distributionHint') }}</p>

            <div class="distribution-actions">
              <button class="btn btn-ghost" type="button" @click="togglePreview">
                {{ t('distributionPreview') }}
              </button>
              <button class="btn btn-ghost" type="button" :disabled="!previewOpen" @click="refreshPreview">
                {{ t('distributionRefresh') }}
              </button>
              <button class="btn btn-primary" type="button" :disabled="pushing" @click="pushDistribution">
                {{ pushing ? t('distributionPushing') : t('distributionPush') }}
              </button>
            </div>

            <p v-if="!distribution && !previewOpen" class="muted note">{{ t('distributionEmpty') }}</p>

            <div v-if="previewOpen && distribution" class="distribution-preview">
              <div class="distribution-counts">
                <span class="mono">{{ enumLabel('kind', 'mcp') }} {{ distribution.mcp?.length ?? 0 }}</span>
                <span class="mono">{{ enumLabel('kind', 'skill') }} {{ distribution.skills?.length ?? 0 }}</span>
                <span v-if="distribution.missing?.length" class="mono">
                  {{ t('distributionMissing') }} {{ distribution.missing.length }}
                </span>
                <span v-if="distribution.unresolved?.length" class="mono">
                  {{ t('distributionUnresolved') }} {{ distribution.unresolved.length }}
                </span>
              </div>
              <p v-if="distribution.truncated" class="muted note">{{ t('distributionTruncated') }}</p>
              <pre class="mono distribution-json">{{ previewText }}</pre>
            </div>

            <p v-if="store.distributionTaskID" class="distribution-receipt">
              {{ t('distributionPushed') }}
              <span class="mono">{{ store.distributionTaskID }}</span>
              <button class="btn btn-ghost" type="button" @click="openDistributionTrace">
                {{ t('distributionViewTrace') }}
              </button>
            </p>
          </div>
        </template>

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

.distribution {
  display: flex;
  flex-direction: column;
  gap: 8px;
  min-width: 0;
}

.distribution-actions {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
}

/* The drawer card can be 300px wide: the buttons wrap instead of pushing the
   card's edge out, and no child keeps an intrinsic width larger than it. */
.distribution-actions .btn {
  min-width: 0;
}

.distribution-counts {
  display: flex;
  flex-wrap: wrap;
  gap: 10px;
  font-size: 12px;
  color: var(--text-3);
}

.distribution-preview {
  display: flex;
  flex-direction: column;
  gap: 6px;
  min-width: 0;
}

.distribution-json {
  max-height: 240px;
  overflow: auto;
  margin: 0;
  padding: 10px;
  border: 1px solid var(--stroke-2);
  border-radius: var(--radius-sm);
  background: var(--bg-elevated);
  font-size: 11px;
  line-height: 1.5;
  white-space: pre-wrap;
  word-break: break-word;
  min-width: 0;
}

.distribution-receipt {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
  font-size: 12px;
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

/* The pager sits above the stats strip: same row rhythm as the toolbars, logical
   properties throughout so the Arabic layout mirrors without a second rule. */
.pager {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 10px;
  margin-block: 12px 0;
}

.pager-btn {
  min-block-size: 44px;
  min-inline-size: 96px;
}

.pager-range {
  font-size: 12px;
  color: var(--text-3);
}

.pager-size {
  display: flex;
  align-items: center;
  gap: 6px;
  margin-inline-start: auto;
  font-size: 12px;
  color: var(--text-3);
}

.pager-size select {
  min-block-size: 44px;
  padding-inline: 8px;
  background: var(--bg-elevated);
  border: 1px solid var(--stroke-2);
  border-radius: var(--radius-sm);
  color: var(--text-1);
  font: inherit;
  font-size: 12px;
}
</style>
