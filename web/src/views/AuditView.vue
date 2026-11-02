<script setup>
// The audit trail is a forensic table, not a dashboard: it is read top to
// bottom, newest first, and every pixel that is not a fact is noise. The layout
// is therefore a filter rail, one line of counters whose only job is to say how
// bad the window is, and the table itself.
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useCopy } from '../lib/use-copy.js'
import { formatTime } from '../lib/format.js'
import {
  AUDIT_RANGES as RANGES,
  actorLabel as labelFor,
  auditFilters,
  chainState,
  nextCursor,
  outcomeOf,
  shortId,
  statsOf,
  windowRange,
} from '../lib/audit.js'
import { getAudit, listAudit, verifyAudit } from '../lib/api.js'
import { useEscape } from '../lib/use-dismiss.js'

const { t } = useCopy('audit')

const rangeKey = ref('range24h')
const outcome = ref('')
const source = ref('')
const search = ref('')

const events = ref([])
const stats = ref({ total: 0, failed: 0, denied: 0, written: 0 })
const next = ref(0)
const loading = ref(false)
const loadingMore = ref(false)
const errorCode = ref('')

const detail = ref(null)
const detailLoading = ref(false)
const chain = ref({ state: 'idle', checked: 0, brokenAt: '' })

useEscape(
  () => !!detail.value,
  () => (detail.value = null),
)

/** The filters as the endpoint wants them; the rules live in lib/audit.js. */
function filters(cursor) {
  return auditFilters(
    { rangeKey: rangeKey.value, outcome: outcome.value, source: source.value, search: search.value },
    cursor,
  )
}

async function load() {
  loading.value = true
  errorCode.value = ''
  try {
    const payload = await listAudit(filters(0))
    events.value = payload?.events ?? []
    stats.value = statsOf(payload)
    next.value = nextCursor(payload)
  } catch (err) {
    errorCode.value = err.code || 'internal_error'
    events.value = []
  } finally {
    loading.value = false
  }
}

async function loadMore() {
  if (!next.value || loadingMore.value) return
  loadingMore.value = true
  try {
    const payload = await listAudit(filters(next.value))
    events.value = [...events.value, ...(payload?.events ?? [])]
    next.value = nextCursor(payload)
  } catch (err) {
    errorCode.value = err.code || 'internal_error'
  } finally {
    loadingMore.value = false
  }
}

async function checkChain() {
  chain.value = { state: 'checking', checked: 0, brokenAt: '' }
  const { from, to } = windowRange(rangeKey.value)
  try {
    chain.value = chainState(await verifyAudit({ from, to }))
  } catch (err) {
    errorCode.value = err.code || 'internal_error'
    chain.value = { state: 'idle', checked: 0, brokenAt: '' }
  }
}

async function openDetail(event) {
  // The row already carries every field; the fetch exists so the drawer shows
  // what is stored rather than what the list happened to project.
  detail.value = event
  detailLoading.value = true
  try {
    const fresh = await getAudit(event.eventId)
    if (fresh) detail.value = fresh
  } catch (err) {
    errorCode.value = err.code || 'internal_error'
  } finally {
    detailLoading.value = false
  }
}

const chainLabel = computed(() => {
  switch (chain.value.state) {
    case 'ok':
      return t('verified')
    case 'broken':
      return t('broken')
    case 'checking':
      return '…'
    default:
      return t('chain')
  }
})

const rows = computed(() => events.value)

/** Both the human and the machine answer, side by side. */
function actorLabel(event) {
  return labelFor(event, t)
}

const short = shortId

function onSearchKey(event) {
  // "/" jumps to the search box from anywhere, the way every log viewer does.
  if (event.key === '/' && document.activeElement?.tagName !== 'INPUT') {
    event.preventDefault()
    document.getElementById('audit-search')?.focus()
  }
}

onMounted(() => {
  load()
  window.addEventListener('keydown', onSearchKey)
})
onBeforeUnmount(() => window.removeEventListener('keydown', onSearchKey))
</script>

<template>
  <div>
    <div class="page-head">
      <h1>{{ t('title') }}</h1>
    </div>
    <p class="page-sub">{{ t('subtitle') }}</p>

    <div class="audit-filters">
      <div class="segmented" role="group" :aria-label="t('range')">
        <button
          v-for="preset in RANGES"
          :key="preset.key"
          type="button"
          class="segmented-item"
          :class="{ active: rangeKey === preset.key }"
          :aria-pressed="rangeKey === preset.key"
          @click="rangeKey = preset.key; load()"
        >
          {{ t(preset.key) }}
        </button>
      </div>

      <select v-model="outcome" class="audit-select" :aria-label="t('outcome')" @change="load">
        <option value="">{{ t('outcomeAll') }}</option>
        <option value="ok">{{ t('outcomeOk') }}</option>
        <option value="denied">{{ t('outcomeDenied') }}</option>
        <option value="failed">{{ t('outcomeFailed') }}</option>
      </select>

      <select v-model="source" class="audit-select" :aria-label="t('source')" @change="load">
        <option value="">{{ t('sourceAll') }}</option>
        <option value="api">{{ t('sourceApi') }}</option>
        <option value="a2a">{{ t('sourceA2a') }}</option>
      </select>

      <input
        id="audit-search"
        v-model="search"
        class="audit-search mono"
        type="search"
        :placeholder="t('search')"
        :aria-label="t('search')"
        @keyup.enter="load"
      />

      <div class="spacer"></div>

      <button class="btn" type="button" :disabled="chain.state === 'checking'" @click="checkChain">
        {{ t('verify') }}
      </button>
    </div>

    <div v-if="errorCode" class="error-line">{{ errorCode }}</div>

    <div class="audit-stats">
      <div class="audit-stat">
        <span class="audit-stat-label">{{ t('total') }}</span>
        <span class="audit-stat-value mono">{{ stats.total }}</span>
      </div>
      <div class="audit-stat">
        <span class="audit-stat-label">{{ t('denied') }}</span>
        <span class="audit-stat-value mono">{{ stats.denied }}</span>
      </div>
      <div class="audit-stat">
        <span class="audit-stat-label">{{ t('chain') }}</span>
        <span class="audit-stat-value audit-chain" :data-state="chain.state">
          <span class="audit-dot" aria-hidden="true"></span>
          <span class="mono">{{ chainLabel }}</span>
          <span v-if="chain.state === 'ok'" class="audit-chain-count mono">
            {{ chain.checked }}
          </span>
          <span v-if="chain.state === 'broken'" class="audit-chain-broken mono">
            {{ short(chain.brokenAt) }}
          </span>
        </span>
      </div>
    </div>

    <div v-if="rows.length" class="audit-table" role="table">
      <div class="audit-row audit-row-head" role="row">
        <span role="columnheader">{{ t('time') }}</span>
        <span role="columnheader">{{ t('actor') }}</span>
        <span role="columnheader">{{ t('action') }}</span>
        <span role="columnheader">{{ t('status') }}</span>
        <span role="columnheader">{{ t('latency') }}</span>
      </div>
      <button
        v-for="event in rows"
        :key="event.eventId"
        type="button"
        class="audit-row"
        :data-outcome="outcomeOf(event)"
        role="row"
        @click="openDetail(event)"
      >
        <span class="mono audit-time" role="cell">{{ formatTime(event.eventTime) }}</span>
        <span class="audit-actor" role="cell">{{ actorLabel(event) }}</span>
        <span class="audit-action mono" role="cell">{{ event.action }}</span>
        <span class="mono audit-status" role="cell">{{ event.status }}</span>
        <span class="audit-latency" role="cell">
          <span class="audit-bar" :style="{ width: Math.min(100, event.latencyMs) + '%' }"></span>
          <span class="mono">{{ event.latencyMs }}</span>
        </span>
      </button>
    </div>

    <div v-else-if="!loading" class="empty">
      <p>{{ t('empty') }}</p>
      <p class="audit-empty-hint">{{ t('emptyHint') }}</p>
    </div>

    <div v-if="next" class="audit-more">
      <button class="btn" type="button" :disabled="loadingMore" @click="loadMore">{{ t('more') }}</button>
    </div>

    <div v-if="detail" class="drawer-backdrop" @click.self="detail = null">
      <aside class="card drawer audit-drawer" :aria-label="t('detail')">
        <div class="page-head">
          <h2>{{ t('detail') }}</h2>
          <button class="btn" type="button" @click="detail = null">✕</button>
        </div>
        <dl class="audit-facts">
          <dt>{{ t('time') }}</dt>
          <dd class="mono">{{ formatTime(detail.eventTime) }}</dd>
          <dt>{{ t('actor') }}</dt>
          <dd>{{ actorLabel(detail) }}</dd>
          <dt>{{ t('action') }}</dt>
          <dd class="mono">{{ detail.action }}</dd>
          <dt>{{ t('request') }}</dt>
          <dd class="mono">{{ detail.requestId }}</dd>
          <dt v-if="detail.resourceId">{{ t('resource') }}</dt>
          <dd v-if="detail.resourceId" class="mono">{{ detail.resourceId }}</dd>
          <dt>{{ t('status') }}</dt>
          <dd class="mono">{{ detail.status }} {{ detail.errorCode }}</dd>
          <dt>{{ t('latency') }}</dt>
          <dd class="mono">{{ detail.latencyMs }} ms</dd>
          <dt>{{ t('prevHash') }}</dt>
          <dd class="mono audit-hash">{{ detail.prevHash || '—' }}</dd>
          <dt>{{ t('hash') }}</dt>
          <dd class="mono audit-hash">{{ detail.hash || '—' }}</dd>
        </dl>
        <p v-if="detailLoading" class="audit-empty-hint">…</p>
      </aside>
    </div>
  </div>
</template>
