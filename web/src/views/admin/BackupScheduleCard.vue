<script setup>
// The checkpoint policy: when one is taken, and how long it is kept.
//
// The form validates locally before it saves, because a policy that silently
// never runs is worse than one the console refused to accept — the backend
// would reject it anyway, but only after a round trip.
import { computed, reactive, watch } from 'vue'
import { useCopy, enumLabel } from '../../lib/use-copy.js'

const props = defineProps({
  schedule: { type: Object, default: () => ({}) },
  nextRun: { type: String, default: '' },
  saving: { type: Boolean, default: false },
})
const emit = defineEmits(['save', 'reset'])

const shell = useCopy('shell')
const { t } = useCopy('backup')

const MODES = ['off', 'interval', 'daily', 'cron']
const timePattern = /^([01]\d|2[0-3]):([0-5]\d)$/

const form = reactive({
  mode: 'off',
  enabled: false,
  intervalSec: 86400,
  dailyTime: '03:00',
  timezone: 'UTC',
  cronExpr: '',
  keepLast: 7,
  maxAgeDays: 30,
})

// The stored policy is the source of truth; the form is a copy of it, so
// discarding a half-typed change is just a reload away.
watch(
  () => props.schedule,
  (value) => {
    form.mode = value?.mode ?? 'off'
    form.enabled = Boolean(value?.enabled)
    form.intervalSec = Number(value?.intervalSec) || 86400
    form.dailyTime = value?.dailyTime || '03:00'
    form.timezone = value?.timezone || 'UTC'
    form.cronExpr = value?.cronExpr || ''
    form.keepLast = Number(value?.keepLast) || 7
    form.maxAgeDays = Number(value?.maxAgeDays) || 30
  },
  { immediate: true, deep: true },
)

const modeLabel = (mode) => enumLabel('backupMode', mode)
const intervalMinutes = computed({
  get: () => Math.round(form.intervalSec / 60),
  set: (minutes) => {
    form.intervalSec = Math.max(1, Number(minutes) || 1) * 60
  },
})

// One message per broken field, so the operator never has to guess which input
// the refusal is about.
const problems = computed(() => {
  const found = []
  if (form.mode === 'interval' && (!Number.isFinite(intervalMinutes.value) || intervalMinutes.value < 1)) {
    found.push(t('fieldInterval'))
  }
  if (form.mode === 'daily' && !timePattern.test(form.dailyTime)) {
    found.push(t('fieldDailyTime'))
  }
  if (form.mode === 'cron' && !form.cronExpr.trim()) {
    found.push(t('fieldCron'))
  }
  if (!Number.isFinite(form.keepLast) || form.keepLast < 1) found.push(t('fieldKeepLast'))
  if (!Number.isFinite(form.maxAgeDays) || form.maxAgeDays < 1) found.push(t('fieldMaxAge'))
  return found
})

const valid = computed(() => problems.value.length === 0)

function submit() {
  if (!valid.value || props.saving) return
  emit('save', {
    mode: form.mode,
    // "Off" means no schedule: keeping the switch on would only confuse the
    // next reader of the row.
    enabled: form.enabled && form.mode !== 'off',
    intervalSec: form.intervalSec,
    dailyTime: form.dailyTime,
    timezone: form.timezone,
    cronExpr: form.cronExpr,
    keepLast: form.keepLast,
    maxAgeDays: form.maxAgeDays,
  })
}
</script>

<template>
  <div class="schedule-card">
    <div class="schedule-head">
      <h3>{{ t('scheduleTitle') }}</h3>
      <p class="page-sub">{{ t('scheduleHint') }}</p>
    </div>

    <form class="schedule-grid" @submit.prevent="submit">
      <div class="field">
        <label for="backup-mode">{{ t('fieldMode') }}</label>
        <select id="backup-mode" v-model="form.mode" :aria-label="t('fieldMode')">
          <option v-for="mode in MODES" :key="mode" :value="mode">{{ modeLabel(mode) }}</option>
        </select>
      </div>

      <div v-if="form.mode === 'interval'" class="field">
        <label for="backup-interval">{{ t('fieldInterval') }}</label>
        <input id="backup-interval" v-model.number="intervalMinutes" type="number" min="1" step="1" />
      </div>

      <div v-if="form.mode === 'daily'" class="field">
        <label for="backup-time">{{ t('fieldDailyTime') }}</label>
        <input id="backup-time" v-model="form.dailyTime" type="time" />
      </div>

      <div v-if="form.mode === 'daily'" class="field">
        <label for="backup-tz">{{ t('fieldTimezone') }}</label>
        <input id="backup-tz" v-model="form.timezone" type="text" />
      </div>

      <div v-if="form.mode === 'cron'" class="field schedule-wide">
        <label for="backup-cron">{{ t('fieldCron') }}</label>
        <input id="backup-cron" v-model="form.cronExpr" type="text" placeholder="0 3 * * *" />
        <span class="field-hint">{{ t('cronHint') }}</span>
      </div>

      <div class="field">
        <label for="backup-keep">{{ t('fieldKeepLast') }}</label>
        <input id="backup-keep" v-model.number="form.keepLast" type="number" min="1" step="1" />
      </div>

      <div class="field">
        <label for="backup-age">{{ t('fieldMaxAge') }}</label>
        <input id="backup-age" v-model.number="form.maxAgeDays" type="number" min="1" step="1" />
      </div>

      <label v-if="form.mode !== 'off'" class="schedule-switch" for="backup-enabled">
        <input id="backup-enabled" v-model="form.enabled" type="checkbox" />
        <span>{{ t('fieldEnabled') }}</span>
      </label>

      <div class="schedule-actions">
        <button class="btn btn-primary" type="submit" :disabled="!valid || saving">
          <span>{{ shell.t('actionSave') }}</span>
        </button>
        <button class="btn btn-ghost" type="button" :disabled="saving" @click="emit('reset')">
          <span>{{ shell.t('actionCancel') }}</span>
        </button>
      </div>
    </form>

    <p v-if="!valid" class="error-line">{{ t('invalidHint') }}</p>
    <p v-if="nextRun" class="schedule-next">
      <span class="muted">{{ t('nextRunAt') }}</span>
      <span class="mono">{{ nextRun }}</span>
    </p>
  </div>
</template>

<style scoped>
.schedule-card {
  border-block-start: 1px solid var(--stroke-1);
  padding-block-start: 14px;
}
.schedule-head {
  margin-block-end: 12px;
}
.schedule-head h3 {
  font-size: 14px;
  font-weight: 500;
}
.schedule-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(180px, 1fr));
  gap: 12px;
  align-items: end;
}
.schedule-wide {
  grid-column: 1 / -1;
}
.field-hint {
  font-size: 11px;
  color: var(--text-3);
}
.schedule-switch {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  font-size: 12px;
  color: var(--text-2);
}
.schedule-switch input {
  width: auto;
  /* Left alone, the browser paints this control in its own accent hue — the
     one colour this console must never show. Accent from the token keeps it
     inside the palette. */
  accent-color: var(--accent);
}
.schedule-actions {
  display: flex;
  gap: 8px;
}
.schedule-next {
  display: flex;
  gap: 8px;
  align-items: baseline;
  margin-block-start: 12px;
  font-size: 12px;
}
</style>
