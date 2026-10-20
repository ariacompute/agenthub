<script setup>
// The console documents itself. Prose comes from the docs copy namespace and
// everything machine-shaped — methods, paths, permission codes, field names and
// JSON samples — comes from docs-content.js and is rendered verbatim in every
// locale.
import { computed, onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import { enumLabel, useCopy } from '../lib/use-copy.js'
import { useHubStore } from '../lib/store.js'
import StateDot from '../components/StateDot.vue'
import {
  adapterCodes,
  a2aAliases,
  a2aMethods,
  a2aShapes,
  docsSections,
  endpointGroups,
  errorGroups,
  healthStates,
  httpEndpoints,
  recordStatuses,
  resourceKinds,
  taskStates,
} from '../lib/docs-content.js'

const store = useHubStore()
const { t } = useCopy('docs')

const active = ref(docsSections[0].id)
const open = reactive({})

/** Endpoint rows are templated: only the verb needs a noun substituted in. */
function descFor(endpoint) {
  const template = t(endpoint.descKey)
  if (!endpoint.nounKey) return template
  return template.replace('{n}', t(endpoint.nounKey))
}

function permissionFor(endpoint) {
  return endpoint.permission ? enumLabel('permission', endpoint.permission) : '—'
}

const grouped = computed(() =>
  endpointGroups.map((group) => ({
    id: group.id,
    label: t(group.groupKey),
    rows: httpEndpoints.filter((endpoint) => endpoint.group === group.id),
  })),
)

const rowKey = (endpoint) => `${endpoint.method} ${endpoint.path}`
const isOpen = (endpoint) => Boolean(open[rowKey(endpoint)])
function toggle(endpoint) {
  const key = rowKey(endpoint)
  open[key] = !open[key]
}

// "version@commit", the same stamp the sidebar foot shows, so the reader can
// tell which build these endpoints belong to.
const build = computed(() => {
  const version = store.health?.version ?? ''
  const commit = String(store.health?.commit ?? '').slice(0, 7)
  return commit ? `${version}@${commit}` : version
})

// Scroll spy: the last section whose top has passed the reading line wins, so
// the index follows the reader rather than snapping to the next heading early.
const READING_LINE = 160
function onScroll() {
  let current = docsSections[0].id
  for (const section of docsSections) {
    const el = document.getElementById(`sec-${section.id}`)
    if (el && el.getBoundingClientRect().top <= READING_LINE) current = section.id
  }
  active.value = current
}

function jump(id) {
  document.getElementById(`sec-${id}`)?.scrollIntoView({ behavior: 'smooth', block: 'start' })
}

onMounted(() => {
  window.addEventListener('scroll', onScroll, { passive: true })
  onScroll()
})
onBeforeUnmount(() => window.removeEventListener('scroll', onScroll))
</script>

<template>
  <div class="docs">
    <div class="page-head">
      <div class="docs-title">
        <h2>{{ t('pageTitle') }}</h2>
        <span v-if="build" class="chip mono">{{ build }}</span>
      </div>
      <div class="page-sub">{{ t('pageSub') }} {{ t('versionNote') }}</div>
    </div>

    <div class="docs-body">
      <aside class="docs-index card panel">
        <div class="index-title">{{ t('indexTitle') }}</div>
        <button
          v-for="section in docsSections"
          :key="section.id"
          class="index-item"
          :class="{ active: active === section.id }"
          type="button"
          @click="jump(section.id)"
        >
          {{ t(section.titleKey) }}
        </button>
      </aside>

      <div class="docs-content">
        <!-- Concepts -->
        <section :id="`sec-concepts`" class="card panel">
          <h3>{{ t('sectionConcepts') }}</h3>
          <p class="lede">{{ t('conceptsBody') }}</p>

          <h4>{{ t('kindsTitle') }}</h4>
          <dl class="kinds">
            <div v-for="lane in resourceKinds" :key="lane.kind" class="kind-row">
              <dt><span class="chip">{{ enumLabel('kind', lane.kind) }}</span></dt>
              <dd>{{ t(lane.descKey) }}</dd>
            </div>
          </dl>

          <h4>{{ t('lifecycleTitle') }}</h4>
          <div class="flow">
            <template v-for="(state, index) in taskStates" :key="state">
              <span class="chip">{{ enumLabel('taskState', state) }}</span>
              <span v-if="index < taskStates.length - 1" class="arrow" aria-hidden="true">→</span>
            </template>
          </div>
          <p class="note">{{ t('lifecycleNote') }}</p>

          <h4>{{ t('healthTitle') }}</h4>
          <div class="flow">
            <StateDot v-for="state in healthStates" :key="state" :state="state" />
          </div>
          <p class="note">{{ t('healthNote') }}</p>

          <h4>{{ t('statusTitle') }}</h4>
          <div class="flow">
            <template v-for="(status, index) in recordStatuses" :key="status">
              <span class="chip">{{ enumLabel('status', status) }}</span>
              <span v-if="index < recordStatuses.length - 1" class="arrow" aria-hidden="true">→</span>
            </template>
          </div>
          <p class="note">{{ t('statusNote') }}</p>
        </section>

        <!-- A2A -->
        <section :id="`sec-a2a`" class="card panel">
          <h3>{{ t('sectionA2a') }}</h3>
          <p class="lede">{{ t('a2aBody') }}</p>

          <div class="table-scroll">
            <table class="docs-table">
              <thead>
                <tr>
                  <th>{{ t('colMethod') }}</th>
                  <th>{{ t('colDescription') }}</th>
                  <th>{{ t('colParams') }}</th>
                  <th>{{ t('colResponse') }}</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="method in a2aMethods" :key="method.method">
                  <td class="mono">{{ method.method }}</td>
                  <td>{{ t(method.descKey) }}</td>
                  <td class="mono dim">{{ method.params }}</td>
                  <td class="mono dim">{{ method.response }}</td>
                </tr>
              </tbody>
            </table>
          </div>
          <p class="note">{{ t('methodsNote') }}</p>

          <h4>{{ t('aliasesTitle') }}</h4>
          <div class="flow">
            <span v-for="alias in a2aAliases" :key="alias.alias" class="chip mono">
              {{ alias.alias }} → {{ alias.method }}
            </span>
          </div>

          <h4>{{ t('shapesTitle') }}</h4>
          <div v-for="shape in a2aShapes" :key="shape.title" class="shape">
            <div class="shape-title mono">{{ shape.title }}</div>
            <pre class="code mono">{{ shape.body }}</pre>
          </div>
        </section>

        <!-- HTTP API -->
        <section :id="`sec-http`" class="card panel">
          <h3>{{ t('sectionHttp') }}</h3>
          <p class="lede">{{ t('httpBody') }}</p>

          <div v-for="group in grouped" :key="group.id" class="endpoint-group">
            <h4>{{ group.label }}</h4>
            <div class="table-scroll">
              <table class="docs-table endpoints">
                <thead>
                  <tr>
                    <th>{{ t('colMethod') }}</th>
                    <th>{{ t('colPath') }}</th>
                    <th>{{ t('colPermission') }}</th>
                    <th>{{ t('colDescription') }}</th>
                    <th></th>
                  </tr>
                </thead>
                <tbody>
                  <template v-for="endpoint in group.rows" :key="rowKey(endpoint)">
                    <tr class="endpoint-row" @click="toggle(endpoint)">
                      <td><span class="verb mono">{{ endpoint.method }}</span></td>
                      <td class="mono">{{ endpoint.path }}</td>
                      <td class="mono dim">{{ permissionFor(endpoint) }}</td>
                      <td>{{ descFor(endpoint) }}</td>
                      <td class="toggle-cell">
                        <button class="btn btn-ghost compact" type="button">
                          {{ isOpen(endpoint) ? t('hideExample') : t('showExample') }}
                        </button>
                      </td>
                    </tr>
                    <tr v-if="isOpen(endpoint)" class="endpoint-detail">
                      <td colspan="5">
                        <div class="detail">
                          <div v-if="endpoint.request" class="detail-block">
                            <div class="detail-label">{{ t('colRequest') }}</div>
                            <pre class="code mono">{{ endpoint.request }}</pre>
                          </div>
                          <div class="detail-block">
                            <div class="detail-label">{{ t('colResponse') }}</div>
                            <pre class="code mono">{{ endpoint.response }}</pre>
                          </div>
                        </div>
                      </td>
                    </tr>
                  </template>
                </tbody>
              </table>
            </div>
          </div>
          <p class="note">{{ t('endpointsNote') }}</p>
        </section>

        <!-- Authentication -->
        <section :id="`sec-auth`" class="card panel">
          <h3>{{ t('sectionAuth') }}</h3>
          <p class="lede">{{ t('authBody') }}</p>
          <h4>{{ t('authSessionTitle') }}</h4>
          <p>{{ t('authSessionBody') }}</p>
          <h4>{{ t('authPermTitle') }}</h4>
          <p>{{ t('authPermBody') }}</p>
          <p class="note">{{ t('authOwnerNote') }}</p>
        </section>

        <!-- Errors -->
        <section :id="`sec-errors`" class="card panel">
          <h3>{{ t('sectionErrors') }}</h3>
          <p class="lede">{{ t('errorsBody') }}</p>

          <div class="table-scroll">
            <table class="docs-table">
              <thead>
                <tr>
                  <th>{{ t('colStatus') }}</th>
                  <th>{{ t('colMeaning') }}</th>
                  <th>{{ t('colCode') }}</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="group in errorGroups" :key="group.status">
                  <td class="mono">{{ group.status }}</td>
                  <td>{{ group.reason }}</td>
                  <td class="mono dim">{{ group.codes }}</td>
                </tr>
              </tbody>
            </table>
          </div>
          <p class="note">{{ t('errorBodyNote') }}</p>

          <h4>{{ t('adapterTitle') }}</h4>
          <div class="table-scroll">
            <table class="docs-table">
              <thead>
                <tr>
                  <th>{{ t('colCode') }}</th>
                  <th>{{ t('colMeaning') }}</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="row in adapterCodes" :key="row.code">
                  <td class="mono">{{ row.code }}</td>
                  <td>{{ t(row.meaningKey) }}</td>
                </tr>
              </tbody>
            </table>
          </div>
        </section>
      </div>
    </div>
  </div>
</template>

<style scoped>
.docs-body {
  display: grid;
  grid-template-columns: 200px minmax(0, 1fr);
  gap: 20px;
  align-items: start;
}

.docs-title {
  display: flex;
  align-items: baseline;
  gap: 10px;
}

.index-title {
  font-size: 11px;
  letter-spacing: 0.08em;
  text-transform: uppercase;
  color: var(--text-3);
  margin-block-end: 10px;
}

.docs-index {
  position: sticky;
  inset-block-start: calc(var(--topbar-h) + 24px);
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.index-item {
  text-align: start;
  padding: 7px 10px;
  border: 0;
  border-radius: var(--radius-sm);
  background: transparent;
  color: var(--text-2);
  font: inherit;
  font-size: 13px;
  cursor: pointer;
  transition: background 0.14s ease, color 0.14s ease;
}

.index-item:hover {
  background: var(--accent-softer);
  color: var(--text-1);
}

.index-item.active {
  background: var(--accent-soft);
  color: var(--text-1);
  font-weight: 600;
}

.docs-content {
  display: flex;
  flex-direction: column;
  gap: 20px;
  min-width: 0;
}

h3 {
  font-size: 18px;
  font-weight: 600;
  margin: 0 0 10px;
}

h4 {
  font-size: 13px;
  font-weight: 600;
  color: var(--text-1);
  margin: 22px 0 8px;
}

h4:first-of-type {
  margin-block-start: 16px;
}

p {
  margin: 0 0 10px;
  font-size: 14px;
  color: var(--text-2);
  line-height: 1.65;
}

.lede {
  color: var(--text-1);
}

.note {
  color: var(--text-3);
  font-size: 13px;
}

.kinds {
  margin: 0;
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.kind-row {
  display: grid;
  grid-template-columns: 120px minmax(0, 1fr);
  gap: 12px;
  align-items: baseline;
}

.kind-row dt {
  margin: 0;
}

.kind-row dd {
  margin: 0;
  font-size: 13px;
  color: var(--text-2);
}

.flow {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
}

.arrow {
  color: var(--text-3);
  font-size: 12px;
}

.table-scroll {
  overflow-x: auto;
}

.docs-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 13px;
}

.docs-table th {
  text-align: start;
  font-weight: 600;
  font-size: 11px;
  letter-spacing: 0.06em;
  text-transform: uppercase;
  color: var(--text-3);
  padding: 6px 10px;
  border-block-end: 1px solid var(--stroke-2);
  white-space: nowrap;
}

.docs-table td {
  padding: 9px 10px;
  border-block-end: 1px solid var(--stroke-1);
  color: var(--text-2);
  vertical-align: top;
}

.docs-table tbody tr:hover td {
  background: var(--accent-softer);
}

.dim {
  color: var(--text-3);
}

.verb {
  display: inline-block;
  padding: 2px 7px;
  border: 1px solid var(--stroke-2);
  border-radius: 6px;
  font-size: 11px;
  color: var(--text-1);
  white-space: nowrap;
}

.endpoint-group {
  margin-block-end: 18px;
}

.endpoint-row {
  cursor: pointer;
}

.toggle-cell {
  text-align: end;
  width: 1%;
  white-space: nowrap;
}

.btn.compact {
  min-width: 0;
  padding: 4px 10px;
  font-size: 12px;
}

.detail {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(260px, 1fr));
  gap: 14px;
  padding: 12px 2px;
}

.detail-label {
  font-size: 11px;
  letter-spacing: 0.06em;
  text-transform: uppercase;
  color: var(--text-3);
  margin-block-end: 6px;
}

.code {
  margin: 0;
  padding: 12px;
  background: var(--bg-surface);
  border: 1px solid var(--stroke-1);
  border-radius: var(--radius-sm);
  overflow-x: auto;
  line-height: 1.6;
  white-space: pre;
}

.shape {
  margin-block-end: 12px;
}

.shape-title {
  color: var(--text-3);
  margin-block-end: 6px;
}

/* The index becomes a horizontal strip of labels: a 200px rail is unusable at
   phone width, and the content should keep the full width. */
@media (max-width: 900px) {
  .docs-body {
    grid-template-columns: minmax(0, 1fr);
  }

  .docs-index {
    position: static;
    flex-direction: row;
    flex-wrap: wrap;
    gap: 6px;
  }

  .index-title {
    width: 100%;
  }
}
</style>
