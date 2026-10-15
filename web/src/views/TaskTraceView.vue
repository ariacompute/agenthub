<script setup>
import { computed, onMounted, ref, watch } from 'vue'
import { useHubStore } from '../lib/store.js'
import { useCopy, enumLabel } from '../lib/use-copy.js'
import { errorMessage } from '../lib/errors.js'
import { cancelTask, getTaskSpans, resumeTask, subscribeTaskEvents } from '../lib/api.js'
import StateDot from '../components/StateDot.vue'
import { AlertTriangle, Copy, RefreshCw } from 'lucide-vue-next'

const store = useHubStore()
const { t } = useCopy('trace')
const shell = useCopy('shell')

const stateFilter = ref('')
const spans = ref([])
const live = ref('idle')
const resumeText = ref('')
const copied = ref(false)
const closeStream = ref(null)

const STATES = ['submitted', 'working', 'input-required', 'completed', 'failed', 'canceled']

const selected = computed(() => store.tasks.find((task) => task.id === store.selectedTaskID) ?? null)

const visibleTasks = computed(() =>
  stateFilter.value ? store.tasks.filter((task) => task.status?.state === stateFilter.value) : store.tasks,
)

const timeline = computed(() => {
  const nodes = []
  for (const span of spans.value) {
    nodes.push({ kind: 'span', at: span.endAt || span.startAt, label: span.label, detail: `${span.durationMs} ms · ${span.status}` })
  }
  if (selected.value) {
    nodes.push({
      kind: 'status',
      at: selected.value.status?.timestamp,
      label: enumLabel('taskState', selected.value.status?.state),
      detail: selected.value.id,
    })
    for (const message of selected.value.history ?? []) {
      nodes.push({ kind: 'message', at: '', label: enumLabel('kind', 'agent'), detail: message.parts?.[0]?.text ?? '' })
    }
  }
  return nodes
})

const artifacts = computed(() => selected.value?.artifacts ?? [])

const errorMessageText = computed(() => errorMessage(store.errorCode))

async function loadSpans(id) {
  if (!id) {
    spans.value = []
    return
  }
  try {
    const data = await getTaskSpans(id)
    spans.value = data?.spans ?? []
  } catch (error) {
    console.error('agenthub: load spans failed', error)
  }
}

function attachStream(id) {
  if (closeStream.value) {
    closeStream.value()
    closeStream.value = null
  }
  if (!id) return
  live.value = 'connected'
  closeStream.value = subscribeTaskEvents(id, (frame) => {
    if (frame.event === 'task_span_update') loadSpans(id)
    if (frame.event === 'task_status_update') store.refreshTasks()
  })
}

watch(
  () => store.selectedTaskID,
  async (id) => {
    await loadSpans(id)
    attachStream(id)
  },
)

onMounted(async () => {
  await store.refreshTasks()
  if (!store.selectedTaskID && store.tasks.length) store.selectTask(store.tasks[0].id)
  else await loadSpans(store.selectedTaskID)
})

async function refresh() {
  await store.refreshTasks()
  await loadSpans(store.selectedTaskID)
}

async function cancel() {
  if (!selected.value) return
  try {
    await cancelTask(selected.value.id)
    await refresh()
  } catch (error) {
    console.error('agenthub: cancel failed', error)
  }
}

async function resume() {
  if (!selected.value) return
  try {
    await resumeTask(selected.value.id, resumeText.value || 'approved')
    resumeText.value = ''
    await refresh()
  } catch (error) {
    console.error('agenthub: resume failed', error)
  }
}

async function copyRaw(text) {
  try {
    await navigator.clipboard.writeText(text)
    copied.value = true
    setTimeout(() => (copied.value = false), 1200)
  } catch (error) {
    console.error('agenthub: clipboard failed', error)
  }
}

function rawOf(artifact) {
  return JSON.stringify(artifact, null, 2)
}
</script>

<template>
  <section>
    <div class="page-head">
      <h1>{{ t('title') }}</h1>
      <p class="page-sub">{{ t('subtitle') }}</p>
    </div>

    <div v-if="store.errorCode" class="error-line">
      <AlertTriangle :size="14" />
      <span>{{ errorMessageText }}</span>
    </div>

    <div class="toolbar">
      <span class="chip">
        <StateDot :state="live === 'connected' ? 'healthy' : 'unknown'" :show-label="false" />
        {{ live === 'connected' ? t('sseConnected') : t('sseReconnecting') }}
      </span>
      <select v-model="stateFilter">
        <option value="">{{ t('filterAll') }}</option>
        <option v-for="state in STATES" :key="state" :value="state">{{ enumLabel('taskState', state) }}</option>
      </select>
      <div class="spacer"></div>
      <button class="btn btn-ghost" type="button" @click="refresh">
        <RefreshCw :size="14" />
        {{ shell.t('actionRefresh') }}
      </button>
    </div>

    <div class="trace">
      <div class="card panel">
        <h2 style="margin-bottom: 10px">{{ t('taskList') }}</h2>
        <div v-if="visibleTasks.length" class="task-list">
          <button
            v-for="task in visibleTasks"
            :key="task.id"
            class="task-row"
            :class="{ selected: task.id === store.selectedTaskID }"
            type="button"
            @click="store.selectTask(task.id)"
          >
            <span class="task-row-top">
              <span class="mono">{{ task.id }}</span>
              <span class="chip">{{ enumLabel('taskState', task.status?.state) }}</span>
            </span>
            <span class="muted">{{ task.resourceId || '—' }}</span>
          </button>
        </div>
        <div v-else class="empty">{{ t('empty') }}</div>
      </div>

      <div class="card panel">
        <h2 style="margin-bottom: 10px">{{ t('timeline') }}</h2>
        <div v-if="selected && timeline.length" class="timeline">
          <div v-for="(node, index) in timeline" :key="index" class="tl-node" :class="node.kind">
            <div class="tl-title">
              <strong>{{ node.label }}</strong>
              <span class="chip mono">{{ node.kind }}</span>
              <span class="muted mono">{{ node.at }}</span>
            </div>
            <div class="muted">{{ node.detail }}</div>
          </div>
        </div>
        <div v-else class="empty">{{ t('noTaskSelected') }}</div>

        <div v-if="selected?.status?.state === 'input-required'" class="resume card panel" style="margin-top: 16px">
          <h2 style="margin-bottom: 10px">{{ t('resumeTitle') }}</h2>
          <div style="display: flex; gap: 8px">
            <input v-model="resumeText" type="text" :placeholder="t('resumePlaceholder')" style="flex: 1" />
            <button class="btn btn-primary" type="button" @click="resume">{{ t('resumeSubmit') }}</button>
            <button class="btn btn-ghost" type="button" @click="cancel">{{ shell.t('actionCancel') }}</button>
          </div>
        </div>

        <div class="artifacts">
          <h2>{{ t('artifactsTitle') }}</h2>
          <div v-for="artifact in artifacts" :key="artifact.artifactId + artifact.index" class="card panel">
            <div class="tl-title" style="margin-bottom: 8px">
              <strong>{{ artifact.name }}</strong>
              <span class="chip mono">{{ artifact.artifactId }}</span>
              <button class="btn btn-ghost" type="button" @click="copyRaw(rawOf(artifact))">
                <Copy :size="14" />
                {{ copied ? t('copyDone') : shell.t('actionCopy') }}
              </button>
            </div>
            <pre class="artifact-raw">{{ rawOf(artifact) }}</pre>
          </div>
        </div>
      </div>
    </div>
  </section>
</template>

<style scoped>
.resume {
  border-color: var(--stroke-2);
}
</style>
