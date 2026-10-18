<script setup>
// The catalogue: one row per checkpoint, with the three things an operator
// does with one — download it, delete it, or rewind the database to it.
import { ref } from 'vue'
import { useCopy, enumLabel } from '../../lib/use-copy.js'
import { formatBytes, formatDuration, formatRows, formatTime } from '../../lib/format.js'
import { Download, RotateCcw, Trash2 } from 'lucide-vue-next'

const props = defineProps({
  checkpoints: { type: Array, default: () => [] },
  busy: { type: Boolean, default: false },
})
const emit = defineEmits(['download', 'delete', 'restore'])

const { t } = useCopy('backup')
const shell = useCopy('shell')
const pending = ref('')

const kindLabel = (kind) => enumLabel('backupKind', kind)
const statusLabel = (status) => enumLabel('backupStatus', status)
// The engine is a backend identifier that travels with the archive, so it is
// rendered verbatim: translating "pg_dump" would only hide what produced a file.

// A checkpoint that is not complete cannot be restored or downloaded: its
// archive is either still being written or was never finished.
const usable = (checkpoint) => checkpoint.status === 'ok'

function askDelete(checkpoint) {
  pending.value = pending.value === checkpoint.id ? '' : checkpoint.id
}

function confirmDelete(checkpoint) {
  pending.value = ''
  emit('delete', checkpoint)
}
</script>

<template>
  <div class="backup-list">
    <div class="admin-head">
      <h3>{{ t('listTitle') }}</h3>
    </div>

    <p v-if="!checkpoints.length" class="empty">{{ t('listEmpty') }}</p>

    <div v-else class="backup-table" role="table">
      <div class="backup-row backup-row-head" role="row">
        <span role="columnheader">{{ t('columnTime') }}</span>
        <span role="columnheader">{{ t('columnKind') }}</span>
        <span role="columnheader">{{ t('columnEngine') }}</span>
        <span class="num" role="columnheader">{{ t('columnSize') }}</span>
        <span class="num" role="columnheader">{{ t('columnRows') }}</span>
        <span role="columnheader">{{ t('columnStatus') }}</span>
        <span class="num" role="columnheader">{{ t('columnDuration') }}</span>
        <span role="columnheader"></span>
      </div>

      <div v-for="checkpoint in checkpoints" :key="checkpoint.id" class="backup-row" role="row">
        <span class="cell-time" role="cell">
          <span>{{ formatTime(checkpoint.createdAt) }}</span>
          <span class="mono muted">{{ checkpoint.id }}</span>
        </span>
        <span role="cell">
          <span class="chip">{{ kindLabel(checkpoint.kind) }}</span>
          <span v-if="checkpoint.label" class="cell-label">{{ checkpoint.label }}</span>
        </span>
        <span class="mono" role="cell">{{ checkpoint.engine }}</span>
        <span class="mono num" role="cell">{{ formatBytes(checkpoint.sizeBytes) }}</span>
        <span class="mono num" role="cell">{{ formatRows(checkpoint.rowCount) }}</span>
        <span role="cell">
          <!-- Status is shape and luminance, never a colour: a circle for a
               finished checkpoint, a triangle for one that did not make it. -->
          <span class="status" :class="{ failed: checkpoint.status === 'failed' || checkpoint.status === 'interrupted' }">
            <i class="mark" aria-hidden="true"></i>
            <span>{{ statusLabel(checkpoint.status) }}</span>
          </span>
          <span v-if="checkpoint.errorCode" class="mono muted">{{ checkpoint.errorCode }}</span>
        </span>
        <span class="mono num" role="cell">{{ formatDuration(checkpoint.durationMs) }}</span>

        <span class="backup-actions" role="cell">
          <button class="btn btn-ghost" type="button" :disabled="!usable(checkpoint) || busy" @click="emit('download', checkpoint)">
            <Download :size="14" />
            <span>{{ t('actionDownload') }}</span>
          </button>
          <button class="btn btn-ghost" type="button" :disabled="!usable(checkpoint) || busy" @click="emit('restore', checkpoint)">
            <RotateCcw :size="14" />
            <span>{{ t('actionRestore') }}</span>
          </button>
          <button class="btn btn-ghost" type="button" :disabled="busy" @click="askDelete(checkpoint)">
            <Trash2 :size="14" />
            <span>{{ shell.t('actionDelete') }}</span>
          </button>
        </span>

        <p v-if="pending === checkpoint.id" class="backup-confirm" role="cell">
          <span>{{ t('confirmDelete') }}</span>
          <button class="btn btn-primary" type="button" @click="confirmDelete(checkpoint)">{{ shell.t('actionConfirm') }}</button>
          <button class="btn btn-ghost" type="button" @click="pending = ''">{{ shell.t('actionCancel') }}</button>
        </p>
      </div>
    </div>
  </div>
</template>

<style scoped>
.backup-list {
  border-block-start: 1px solid var(--stroke-1);
  padding-block-start: 14px;
}
.admin-head {
  margin-block-end: 10px;
}
.admin-head h3 {
  font-size: 14px;
  font-weight: 500;
}
.backup-table {
  display: flex;
  flex-direction: column;
  gap: 6px;
}
.backup-row {
  display: grid;
  grid-template-columns: minmax(150px, 1.3fr) minmax(120px, 1fr) 90px 90px 90px minmax(110px, 0.9fr) 80px minmax(240px, 1.4fr);
  gap: 10px;
  align-items: center;
  padding: 10px 12px;
  border: 1px solid var(--stroke-1);
  border-radius: var(--radius-sm);
  background: var(--accent-softer);
  font-size: 12px;
}
.backup-row-head {
  border: 0;
  background: none;
  color: var(--text-3);
  font-size: 11px;
  text-transform: uppercase;
  letter-spacing: 0.04em;
  padding: 0 12px;
}
.num {
  text-align: end;
}
.cell-time {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}
.cell-label {
  display: block;
  color: var(--text-3);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.status {
  display: inline-flex;
  align-items: center;
  gap: 6px;
}
/* A disc for a finished checkpoint, a triangle for one that is not usable. */
.mark {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  background: var(--text-1);
  flex: none;
}
.status.failed .mark {
  border-radius: 0;
  background: none;
  border-inline: 4px solid transparent;
  border-block-end: 8px solid var(--text-3);
  width: 0;
  height: 0;
}
.backup-actions {
  display: flex;
  gap: 6px;
  flex-wrap: wrap;
  justify-content: flex-end;
}
.backup-confirm {
  grid-column: 1 / -1;
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
  padding-block-start: 8px;
  border-block-start: 1px dashed var(--stroke-1);
  color: var(--text-2);
  font-size: 12px;
}

@media (max-width: 1023px) {
  .backup-row {
    grid-template-columns: 1fr;
  }
  .backup-row-head {
    display: none;
  }
  .num {
    text-align: start;
  }
  .backup-actions {
    justify-content: flex-start;
  }
}
</style>
