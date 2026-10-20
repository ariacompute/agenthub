<script setup>
import { computed, onMounted, ref } from 'vue'
import { useHubStore, KINDS } from '../lib/store.js'
import { useCopy, enumLabel } from '../lib/use-copy.js'
import { getTaskSpans } from '../lib/api.js'
import StateDot from '../components/StateDot.vue'
import { BookOpen, Bot, Cpu, Plug, Terminal } from 'lucide-vue-next'

const store = useHubStore()
const { t } = useCopy('topology')
const shell = useCopy('shell')

const windowSize = ref(10)
const edges = ref([])

const KIND_ICONS = { model: Cpu, agent: Bot, mcp: Plug, skill: BookOpen, cli: Terminal }

const lanes = computed(() =>
  KINDS.map((kind) => ({
    kind,
    label: enumLabel('kind', kind),
    nodes: store.resources.filter((resource) => resource.kind === kind),
  })),
)

const selected = computed(() => store.resources.find((resource) => resource.id === store.selectedResourceID) ?? null)

const visibleEdges = computed(() =>
  windowSize.value === 0 ? edges.value : edges.value.slice(-windowSize.value),
)

async function loadEdges() {
  const collected = []
  for (const task of store.tasks) {
    try {
      const data = await getTaskSpans(task.id)
      const spans = data?.spans ?? []
      for (let i = 0; i < spans.length; i += 1) {
        const from = spans[i]
        const to = spans[i + 1]
        collected.push({
          id: `${task.id}-${from.id}`,
          taskId: task.id,
          from: from.resourceId,
          to: to ? to.resourceId : null,
          status: from.status,
          durationMs: from.durationMs,
        })
      }
    } catch (error) {
      console.error('agenthub: load spans failed', error)
    }
  }
  edges.value = collected
}

onMounted(async () => {
  // Same reason as the registry view: a run or probe rewrites health, so the
  // cached list is stale by the time this view is re-entered.
  await store.refreshResources()
  await store.refreshTasks()
  await loadEdges()
})

function iconFor(kind) {
  return KIND_ICONS[kind] ?? Cpu
}
</script>

<template>
  <section>
    <div class="page-head">
      <h1>{{ t('title') }}</h1>
      <p class="page-sub">{{ t('subtitle') }}</p>
    </div>

    <div class="toolbar">
      <div class="tabs">
        <button class="tab" :class="{ active: windowSize === 10 }" type="button" @click="windowSize = 10">
          {{ t('windowRecent') }}
        </button>
        <button class="tab" :class="{ active: windowSize === 0 }" type="button" @click="windowSize = 0">
          {{ t('windowAll') }}
        </button>
      </div>
      <div class="spacer"></div>
      <button class="btn btn-ghost" type="button" @click="loadEdges">{{ shell.t('actionRefresh') }}</button>
    </div>

    <div class="lanes">
      <div v-for="lane in lanes" :key="lane.kind" class="card lane">
        <div class="lane-title">
          <span class="kind-icon" :class="lane.kind">
            <component :is="iconFor(lane.kind)" :size="15" />
          </span>
          {{ lane.label }}
          <span class="tab-count">{{ lane.nodes.length }}</span>
        </div>
        <button
          v-for="node in lane.nodes"
          :key="node.id"
          class="node"
          :class="[node.kind, { selected: node.id === store.selectedResourceID }]"
          type="button"
          :title="node.endpoint || node.name"
          @click="store.selectResource(node.id)"
        >
          <component :is="iconFor(node.kind)" :size="14" />
          <span>{{ node.name }}</span>
        </button>
        <p v-if="!lane.nodes.length" class="muted" style="font-size: 12px">{{ t('empty') }}</p>
      </div>
    </div>

    <div class="card panel edges">
      <h2 style="margin-bottom: 10px">{{ t('edgeTitle') }}</h2>
      <div v-if="visibleEdges.length">
        <div v-for="edge in visibleEdges" :key="edge.id" class="edge-row">
          <span class="edge-line" :class="edge.status === 'error' ? 'failed' : 'active'"></span>
          <span class="mono">{{ edge.from }}</span>
          <span class="muted">→</span>
          <span class="mono">{{ edge.to || '—' }}</span>
          <span class="muted">{{ edge.durationMs }} ms</span>
          <span class="chip mono">{{ edge.status }}</span>
        </div>
      </div>
      <div v-else class="empty">{{ t('empty') }}</div>

      <div class="legend">
        <span class="legend-item"><span class="edge-line active"></span>{{ t('legendActive') }}</span>
        <span class="legend-item"><span class="edge-line"></span>{{ t('legendHistory') }}</span>
        <span class="legend-item"><span class="edge-line failed"></span>{{ t('legendFailed') }}</span>
        <span class="legend-item"><StateDot state="healthy" />{{ enumLabel('health', 'healthy') }}</span>
      </div>
    </div>

    <div v-if="selected" class="card panel" style="margin-top: 16px">
      <h2 style="margin-bottom: 10px">{{ t('detailTitle') }}</h2>
      <div class="tag-row">
        <span class="chip"><strong>{{ selected.name }}</strong></span>
        <span class="chip">{{ enumLabel('kind', selected.kind) }}</span>
        <span v-if="selected.transport" class="chip">{{ enumLabel('transport', selected.transport) }}</span>
        <StateDot :state="selected.health" />
      </div>
      <p class="muted mono" style="margin-top: 8px">{{ t('detailEndpoint') }}: {{ selected.endpoint || '—' }}</p>
      <p class="muted mono">{{ t('detailCapabilities') }}: {{ (selected.capabilities ?? []).join(', ') || '—' }}</p>
      <p class="muted mono">{{ t('updatedAt') }}: {{ selected.updatedAt }}</p>
    </div>
  </section>
</template>
