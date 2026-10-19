<script setup>
// Screen 4 — namespaces and the curator's queue.
//
// A namespace is a catalogue with its own approval policy; the queue is every
// record waiting for a decision. They sit on one screen because approving
// something is meaningless without knowing which catalogue it belongs to.
import { computed, onMounted, reactive, ref } from 'vue'
import { useCopy, enumLabel } from '../lib/use-copy.js'
import { useHubStore } from '../lib/store.js'
import { errorMessage } from '../lib/errors.js'
import { isDirty, snapshot, useEscape } from '../lib/use-dismiss.js'
import UnsavedPrompt from '../components/UnsavedPrompt.vue'
import { Plus } from 'lucide-vue-next'

const store = useHubStore()
const { t } = useCopy('registries')
const shell = useCopy('shell')

const drawerOpen = ref(false)
const askSave = ref(false)
const baseline = ref(null)
const form = reactive({ id: '', name: '', description: '', autoApprove: true })

const errorMessageText = computed(() => errorMessage(store.errorCode))

onMounted(() => {
  store.refreshRegistries()
  store.refreshApprovals()
})

function openCreate() {
  Object.assign(form, { id: '', name: '', description: '', autoApprove: true })
  baseline.value = snapshot(form)
  drawerOpen.value = true
}

function closeDrawer() {
  drawerOpen.value = false
  askSave.value = false
  baseline.value = null
}

// Escape, the backdrop and the cancel button all land here; only a form that
// was actually edited earns the prompt.
function requestClose() {
  if (isDirty(baseline.value, form)) {
    askSave.value = true
    return
  }
  closeDrawer()
}

useEscape(() => drawerOpen.value, requestClose)

// Saving from the prompt is the same save; the prompt steps aside so a
// failure is reported by the drawer's own error banner.
async function saveAndClose() {
  askSave.value = false
  await submit()
}

async function submit() {
  const ok = await store.saveRegistry({
    id: form.id.trim(),
    name: form.name.trim() || form.id.trim(),
    description: form.description.trim(),
    auto_approve: form.autoApprove,
  })
  if (ok) closeDrawer()
}

async function destroy(namespace) {
  if (!window.confirm(t('confirmDelete'))) return
  await store.removeRegistry(namespace.id)
}

async function approve(record) {
  await store.curate(record.id, 'approved')
}

async function reject(record) {
  await store.curate(record.id, 'rejected')
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
      <div class="stats card">
        <span>{{ t('statsNamespaces') }} <b>{{ store.registries.length }}</b></span>
        <span>{{ t('statsPending') }} <b>{{ store.approvals.length }}</b></span>
      </div>
      <div class="spacer"></div>
      <button class="btn btn-primary" type="button" @click="openCreate">
        <Plus :size="14" />
        {{ t('actionNew') }}
      </button>
    </div>

    <div v-if="store.registries.length" class="grid">
      <article v-for="namespace in store.registries" :key="namespace.id" class="card resource-card">
        <div class="resource-head">
          <div style="min-width: 0">
            <div class="resource-title">{{ namespace.name }}</div>
            <div class="resource-endpoint mono">{{ namespace.id }}</div>
          </div>
        </div>
        <p v-if="namespace.description" class="muted">{{ namespace.description }}</p>
        <div class="tag-row">
          <span class="chip">{{ namespace.auto_approve ? t('autoApprove') : t('queueTitle') }}</span>
        </div>
        <div class="card-actions">
          <button
            class="btn btn-ghost"
            type="button"
            :disabled="namespace.id === 'default'"
            @click="destroy(namespace)"
          >
            {{ shell.t('actionDelete') }}
          </button>
        </div>
        <p v-if="namespace.id === 'default'" class="muted note">{{ t('defaultNote') }}</p>
      </article>
    </div>
    <div v-else class="empty">{{ t('empty') }}</div>

    <h2 class="queue-head">{{ t('queueTitle') }}</h2>
    <div v-if="store.approvals.length" class="queue">
      <article v-for="record in store.approvals" :key="record.id" class="card queue-row">
        <div class="queue-main">
          <span class="chip">{{ enumLabel('kind', record.kind) }}</span>
          <span class="queue-name">{{ record.name }}</span>
          <span class="mono muted">{{ record.version || '—' }}</span>
          <span class="mono muted">{{ record.namespace || 'default' }}</span>
        </div>
        <div class="card-actions">
          <button class="btn btn-primary" type="button" @click="approve(record)">{{ t('actionApprove') }}</button>
          <button class="btn btn-ghost" type="button" @click="reject(record)">{{ t('actionReject') }}</button>
        </div>
      </article>
    </div>
    <div v-else class="empty">{{ t('queueEmpty') }}</div>

    <div v-if="drawerOpen" class="drawer-backdrop" @click.self="requestClose">
      <form class="card drawer" @submit.prevent="submit">
        <div class="page-head">
          <h2>{{ t('drawerTitle') }}</h2>
        </div>
        <div class="field">
          <label for="n-id">{{ t('fieldId') }}</label>
          <input id="n-id" v-model="form.id" class="mono" type="text" required placeholder="team-ops" />
        </div>
        <div class="field">
          <label for="n-name">{{ t('fieldName') }}</label>
          <input id="n-name" v-model="form.name" type="text" />
        </div>
        <div class="field">
          <label for="n-desc">{{ t('fieldDescription') }}</label>
          <input id="n-desc" v-model="form.description" type="text" />
        </div>
        <label class="check">
          <input v-model="form.autoApprove" type="checkbox" />
          <span>{{ t('autoApprove') }}</span>
        </label>
        <p class="muted note">{{ t('autoApproveHint') }}</p>
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
.queue-head {
  margin-block: 24px 10px;
}
.queue {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.queue-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  flex-wrap: wrap;
}
.queue-main {
  display: flex;
  align-items: center;
  gap: 10px;
  min-width: 0;
  flex-wrap: wrap;
}
.queue-name {
  font-size: 13px;
  font-weight: 500;
  color: var(--text-1);
}
.note {
  margin-block-start: 8px;
  font-size: 11px;
}
.check {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 12px;
  color: var(--text-2);
}
</style>
