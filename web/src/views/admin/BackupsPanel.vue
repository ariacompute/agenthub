<script setup>
// The Backups tab: the overview strip, the schedule, the catalogue and the
// restore drawer.
//
// Everything destructive in here is deliberate twice: a checkpoint is taken
// with one click because it is harmless, a restore only after the operator has
// read the summary and typed the checkpoint id back.
import { computed, onMounted, ref } from 'vue'
import * as api from '../../lib/api.js'
import { errorMessage } from '../../lib/errors.js'
import { useCopy, enumLabel } from '../../lib/use-copy.js'
import { formatBytes, formatDuration, formatRows, formatTime } from '../../lib/format.js'
import { DatabaseBackup } from 'lucide-vue-next'
import BackupScheduleCard from './BackupScheduleCard.vue'
import BackupList from './BackupList.vue'
import RestoreDrawer from './RestoreDrawer.vue'

const { t } = useCopy('backup')

const state = ref({ enabled: false, reason: '', dir: '', engine: '', nextRun: '', checkpoints: [] })
const schedule = ref({})
const label = ref('')
const errorCode = ref('')
const loading = ref(false)
const working = ref(false)
const target = ref(null)

const errorText = computed(() => errorMessage(errorCode.value))

// The newest checkpoint is the last result the operator cares about: it is what
// the overview strip shows next to the time it took.
const last = computed(() => state.value.checkpoints?.[0] ?? null)

const nextRunText = computed(() => (state.value.nextRun ? formatTime(state.value.nextRun) : t('valueNever')))

async function load() {
  loading.value = true
  try {
    state.value = await api.listBackups()
    errorCode.value = ''
    // The overview already carries the retention policy; the schedule endpoint
    // only matters when checkpoints are switched on at all.
    if (state.value.enabled) {
      const data = await api.getBackupSchedule()
      schedule.value = data?.schedule ?? {}
    }
  } catch (error) {
    errorCode.value = error.code || 'internal_error'
  } finally {
    loading.value = false
  }
}

async function take() {
  working.value = true
  try {
    await api.createBackup(label.value.trim())
    label.value = ''
    errorCode.value = ''
    await load()
  } catch (error) {
    errorCode.value = error.code || 'internal_error'
  } finally {
    working.value = false
  }
}

async function saveSchedule(payload) {
  try {
    const data = await api.saveBackupSchedule(payload)
    schedule.value = data?.schedule ?? payload
    errorCode.value = ''
    await load()
  } catch (error) {
    errorCode.value = error.code || 'internal_error'
  }
}

async function download(checkpoint) {
  try {
    const { blob, filename } = await api.downloadBackup(checkpoint.id)
    api.saveBlob(blob, filename)
    errorCode.value = ''
  } catch (error) {
    errorCode.value = error.code || 'internal_error'
  }
}

async function remove(checkpoint) {
  try {
    await api.deleteBackup(checkpoint.id)
    errorCode.value = ''
    await load()
  } catch (error) {
    errorCode.value = error.code || 'internal_error'
  }
}

async function restore(confirmation) {
  const checkpoint = target.value
  if (!checkpoint) return
  working.value = true
  try {
    await api.restoreBackup(checkpoint.id, confirmation)
    target.value = null
    errorCode.value = ''
    await load()
  } catch (error) {
    errorCode.value = error.code || 'internal_error'
  } finally {
    working.value = false
  }
}

onMounted(load)
</script>

<template>
  <section class="card panel backups">
    <div class="admin-head">
      <h2>
        <DatabaseBackup :size="15" />
        <span>{{ t('title') }}</span>
      </h2>
      <div class="head-actions">
        <input
          v-if="state.enabled"
          v-model="label"
          class="backup-label"
          type="text"
          :placeholder="t('fieldLabel')"
          :aria-label="t('fieldLabel')"
        />
        <button v-if="state.enabled" class="btn btn-primary" type="button" :disabled="working || loading" @click="take">
          <DatabaseBackup :size="15" />
          <span>{{ working ? t('actionCheckpointing') : t('actionCheckpoint') }}</span>
        </button>
      </div>
    </div>
    <p class="page-sub">{{ t('hint') }}</p>

    <p v-if="errorCode" class="error-line">{{ errorText }}</p>

    <!-- An instance with no backup volume is a supported configuration: the
         tab says why it is empty instead of failing a request. -->
    <div v-if="!state.enabled" class="backup-disabled">
      <p class="strong">{{ t('disabledTitle') }}</p>
      <p class="page-sub">{{ t('disabledHint') }}</p>
      <p v-if="state.reason" class="muted backup-reason">
        {{ t('disabledReason') }}: <span class="mono">{{ errorMessage(state.reason) }}</span>
      </p>
    </div>

    <template v-else>
      <div class="backup-overview">
        <div class="overview-cell">
          <span class="muted">{{ t('overviewDir') }}</span>
          <span class="mono overview-value" :title="state.dir">{{ state.dir || t('valueNotConfigured') }}</span>
        </div>
        <div class="overview-cell">
          <span class="muted">{{ t('overviewEngine') }}</span>
          <span class="mono overview-value">{{ state.engine || t('valueNone') }}</span>
        </div>
        <div class="overview-cell">
          <span class="muted">{{ t('overviewNextRun') }}</span>
          <span class="mono overview-value">{{ nextRunText }}</span>
        </div>
        <div class="overview-cell">
          <span class="muted">{{ t('overviewLastRun') }}</span>
          <span v-if="last" class="overview-value">
            <span class="status" :class="{ failed: last.status === 'failed' || last.status === 'interrupted' }">
              <i class="mark" aria-hidden="true"></i>
              <span>{{ enumLabel('backupStatus', last.status) }}</span>
            </span>
            <span class="mono muted">
              {{ formatTime(last.createdAt) }} · {{ formatDuration(last.durationMs) }} ·
              {{ formatBytes(last.sizeBytes) }} · {{ formatRows(last.rowCount) }} {{ t('unitRows') }}
            </span>
          </span>
          <span v-else class="mono overview-value">{{ t('valueNone') }}</span>
        </div>
      </div>

      <BackupScheduleCard
        :schedule="schedule"
        :next-run="state.nextRun ? formatTime(state.nextRun) : ''"
        :saving="working"
        @save="saveSchedule"
        @reset="load"
      />

      <BackupList
        :checkpoints="state.checkpoints"
        :busy="working"
        @download="download"
        @delete="remove"
        @restore="target = $event"
      />
    </template>

    <RestoreDrawer
      v-if="target"
      :checkpoint="target"
      :busy="working"
      @close="target = null"
      @confirm="restore"
    />
  </section>
</template>

<style scoped>
.admin-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  flex-wrap: wrap;
}
.admin-head h2 {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  font-size: 16px;
  font-weight: 600;
}
.head-actions {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}
.backup-label {
  min-width: 180px;
}
/* The intro hint sits flush against the title row and the overview below it;
   give it breathing room from both (language-independent — no text involved). */
.backups > .page-sub {
  margin-block: 16px;
}
.backup-disabled {
  padding: 18px;
  border: 1px dashed var(--stroke-2);
  border-radius: var(--radius-sm);
  display: flex;
  flex-direction: column;
  gap: 6px;
}
.strong {
  font-weight: 500;
}
.backup-reason {
  font-size: 12px;
}
.backup-overview {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(180px, 1fr));
  gap: 12px;
  margin-block: 14px;
}
.overview-cell {
  display: flex;
  flex-direction: column;
  gap: 4px;
  padding: 12px;
  border: 1px solid var(--stroke-1);
  border-radius: var(--radius-sm);
  background: var(--accent-softer);
  font-size: 12px;
  min-width: 0;
}
.overview-value {
  display: flex;
  flex-direction: column;
  gap: 2px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.status {
  display: inline-flex;
  align-items: center;
  gap: 6px;
}
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
</style>
