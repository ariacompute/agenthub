<script setup>
// The restore confirmation.
//
// Restoring is the only action in the console that destroys data, so the drawer
// does three things before it lets anybody through: it shows what will be
// loaded, it spells out the guardrails, and it makes the operator type the
// checkpoint id — the last chance to notice the wrong row.
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useCopy, enumLabel } from '../../lib/use-copy.js'
import { formatBytes, formatDuration, formatRows, formatTime } from '../../lib/format.js'
import { RotateCcw, X } from 'lucide-vue-next'

const props = defineProps({
  checkpoint: { type: Object, required: true },
  busy: { type: Boolean, default: false },
})
const emit = defineEmits(['close', 'confirm'])

const { t } = useCopy('backup')
const shell = useCopy('shell')

const typed = ref('')
const input = ref(null)

const shortId = computed(() => String(props.checkpoint.id).slice(-8))

// The full id or the short form printed in the table: asking an operator to
// retype a uuid by hand would invite the very mistake this box exists to catch.
const confirmed = computed(() => {
  const value = typed.value.trim()
  return value !== '' && (value === props.checkpoint.id || value === shortId.value)
})

const migrations = computed(() => {
  const raw = props.checkpoint.schema || ''
  const list = raw.split(',').filter(Boolean)
  return list.length ? list[list.length - 1] : '—'
})

function close() {
  if (props.busy) return
  emit('close')
}

function onKey(event) {
  if (event.key === 'Escape') close()
}

onMounted(() => {
  document.addEventListener('keydown', onKey)
  input.value?.focus()
})
onUnmounted(() => document.removeEventListener('keydown', onKey))
</script>

<template>
  <div class="drawer-backdrop" @click.self="close">
    <div class="drawer card restore-drawer" role="dialog" aria-modal="true" :aria-label="t('restoreTitle')">
      <div class="restore-head">
        <h3>{{ t('restoreTitle') }}</h3>
        <button class="btn btn-ghost restore-close" type="button" :disabled="busy" @click="close">
          <X :size="14" />
          <span>{{ shell.t('actionClose') }}</span>
        </button>
      </div>

      <div class="restore-body">
        <div class="restore-summary">
          <h4>{{ t('restoreSummary') }}</h4>
          <dl>
            <div><dt class="muted">{{ t('columnTime') }}</dt><dd>{{ formatTime(checkpoint.createdAt) }}</dd></div>
            <div><dt class="muted">{{ t('columnKind') }}</dt><dd>{{ enumLabel('backupKind', checkpoint.kind) }}</dd></div>
            <div><dt class="muted">{{ t('columnEngine') }}</dt><dd class="mono">{{ checkpoint.engine }}</dd></div>
            <div>
              <dt class="muted">{{ t('unitTables') }}</dt>
              <dd class="mono">{{ checkpoint.tableCount }} · {{ formatRows(checkpoint.rowCount) }} {{ t('unitRows') }}</dd>
            </div>
            <div><dt class="muted">{{ t('columnSize') }}</dt><dd class="mono">{{ formatBytes(checkpoint.sizeBytes) }}</dd></div>
            <div><dt class="muted">{{ t('columnDuration') }}</dt><dd class="mono">{{ formatDuration(checkpoint.durationMs) }}</dd></div>
            <div><dt class="muted">{{ t('columnSchema') }}</dt><dd class="mono">{{ migrations }}</dd></div>
            <div><dt class="muted">{{ 'sha256' }}</dt><dd class="mono">{{ checkpoint.checksum }}</dd></div>
          </dl>
          <p class="restore-id">
            <span class="muted">{{ 'id' }}</span>
            <span class="mono">{{ checkpoint.id }}</span>
          </p>
        </div>

        <div class="restore-guards">
          <h4>{{ t('restoreGuardTitle') }}</h4>
          <ol>
            <li>{{ t('restoreGuardRollback') }}</li>
            <li>{{ t('restoreGuardDrain') }}</li>
            <li>{{ t('restoreGuardReload') }}</li>
          </ol>
          <p class="restore-danger">{{ t('restoreDanger') }}</p>
        </div>
      </div>

      <form class="restore-confirm" @submit.prevent="confirmed && emit('confirm', typed.trim())">
        <div class="field">
          <label for="restore-confirm-input">{{ t('restoreConfirmLabel') }}</label>
          <input id="restore-confirm-input" ref="input" v-model="typed" type="text" :placeholder="shortId" :disabled="busy" autocomplete="off" />
          <span class="field-hint">{{ t('restoreConfirmHint') }}</span>
        </div>
        <div class="restore-actions">
          <button class="btn btn-ghost" type="button" :disabled="busy" @click="close">{{ shell.t('actionCancel') }}</button>
          <button class="btn btn-primary" type="submit" :disabled="!confirmed || busy">
            <RotateCcw :size="15" />
            <span>{{ busy ? t('restoreBusy') : t('actionRestore') }}</span>
          </button>
        </div>
      </form>
    </div>
  </div>
</template>

<style scoped>
.restore-drawer {
  max-width: 760px;
  width: calc(100% - 32px);
}
.restore-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin-block-end: 14px;
}
.restore-head h3 {
  font-size: 16px;
  font-weight: 600;
}
.restore-body {
  display: grid;
  grid-template-columns: minmax(240px, 1fr) minmax(240px, 1.1fr);
  gap: 18px;
}
.restore-body h4 {
  font-size: 12px;
  font-weight: 500;
  color: var(--text-3);
  text-transform: uppercase;
  letter-spacing: 0.04em;
  margin-block-end: 10px;
}
dl {
  display: flex;
  flex-direction: column;
  gap: 6px;
  margin: 0;
}
dl > div {
  display: flex;
  justify-content: space-between;
  gap: 12px;
  font-size: 12px;
}
dd {
  margin: 0;
  text-align: end;
  word-break: break-all;
}
.restore-id {
  display: flex;
  justify-content: space-between;
  gap: 12px;
  margin-block-start: 10px;
  padding-block-start: 10px;
  border-block-start: 1px solid var(--stroke-1);
  font-size: 12px;
}
.restore-guards ol {
  margin: 0;
  padding-inline-start: 18px;
  display: flex;
  flex-direction: column;
  gap: 8px;
  font-size: 12px;
  color: var(--text-2);
}
.restore-danger {
  margin-block-start: 12px;
  padding: 10px 12px;
  border: 1px solid var(--stroke-2);
  border-radius: var(--radius-sm);
  background: var(--accent-softer);
  font-size: 12px;
}
.restore-confirm {
  display: flex;
  align-items: end;
  gap: 12px;
  margin-block-start: 18px;
  padding-block-start: 14px;
  border-block-start: 1px solid var(--stroke-1);
}
.restore-confirm .field {
  flex: 1;
}
.field-hint {
  font-size: 11px;
  color: var(--text-3);
}
.restore-actions {
  display: flex;
  gap: 8px;
}

@media (max-width: 719px) {
  .restore-body,
  .restore-confirm {
    grid-template-columns: 1fr;
    flex-direction: column;
    align-items: stretch;
  }
}
</style>
