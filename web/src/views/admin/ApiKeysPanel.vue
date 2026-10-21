<script setup>
// The API keys tab of the administration screen.
//
// A key is a long-lived bearer credential. It is minted with an explicit
// permission subset the caller may grant (never more than the caller holds), and
// the raw value is shown exactly once — after that only the prefix survives. A
// key can be revoked at any time, which turns the token dead without deleting
// the row, so the audit trail stays intact.
import { computed, ref } from 'vue'
import { useAuthStore, PERMISSIONS } from '../../lib/auth-store.js'
import { useCopy, enumLabel } from '../../lib/use-copy.js'
import { errorMessage } from '../../lib/errors.js'
import { formatTime } from '../../lib/format.js'
import { isDirty, snapshot, useEscape } from '../../lib/use-dismiss.js'
import { KeyRound, Plus, Trash2, Copy, Check } from 'lucide-vue-next'
import UnsavedPrompt from '../../components/UnsavedPrompt.vue'

const auth = useAuthStore()
const { t } = useCopy('apikeys')
const shell = useCopy('shell')

const drawerOpen = ref(false)
const creating = ref(false)
const name = ref('')
const scopes = ref([])
const expiresInDays = ref(0)
// The raw token is only ever known here, right after creation.
const createdToken = ref('')
const copied = ref(false)
const confirmRevoke = ref(null)

// Dismissing the create drawer goes through requestClose so a half-filled form
// prompts first; these back that up (the snapshot is taken when it opens).
const baseline = ref(null)
const askSave = ref(false)

const errorText = computed(() => errorMessage(auth.errorCode))
const myKeys = computed(() => auth.apiKeys ?? [])

// A key can only carry permissions the caller could use themselves, so the matrix
// hides anything the session cannot grant — an owner sees everything, a limited
// account sees only what it would be able to delegate.
const availableScopes = computed(() => PERMISSIONS.filter((code) => auth.can(code)))

// Every selectable scope ticked → the toolbar offers "deselect all"; otherwise it
// offers "select all". The label is the only translated bit, so this is language-neutral.
const allSelected = computed(
  () => availableScopes.value.length > 0 && availableScopes.value.every((code) => scopes.value.includes(code)),
)

const expiresOptions = [
  { value: 0, label: t('never') },
  { value: 7, label: t('days7') },
  { value: 30, label: t('days30') },
  { value: 90, label: t('days90') },
]

function fmtTime(value) {
  return value ? formatTime(value) : '—'
}

function openCreate() {
  name.value = ''
  scopes.value = []
  expiresInDays.value = 0
  createdToken.value = ''
  copied.value = false
  drawerOpen.value = true
  baseline.value = snapshot({ name: name.value, scopes: scopes.value, expiresInDays: expiresInDays.value })
}

function toggleScope(code) {
  const set = new Set(scopes.value)
  if (set.has(code)) set.delete(code)
  else set.add(code)
  scopes.value = [...set]
}

function toggleAll() {
  scopes.value = allSelected.value ? [] : [...availableScopes.value]
}

async function createKey() {
  if (!name.value.trim()) return
  creating.value = true
  const data = await auth.createAPIKey({
    name: name.value.trim(),
    scopes: scopes.value,
    expiresInDays: expiresInDays.value || undefined,
  })
  creating.value = false
  if (data) createdToken.value = data.token ?? ''
}

function closeDrawer() {
  drawerOpen.value = false
  createdToken.value = ''
}

// Escape, the backdrop and the cancel button all funnel through here, so the
// gesture is identical wherever it starts. A form with edits but no key yet asks
// first; once the token exists there is nothing to lose, so it closes outright.
function requestClose() {
  if (!createdToken.value && isDirty(baseline.value, { name: name.value, scopes: scopes.value, expiresInDays: expiresInDays.value })) {
    askSave.value = true
    return
  }
  closeDrawer()
}

// "Save" in the unsaved prompt mints the key but leaves the drawer open, so the
// one-time token stays on screen — closing would discard it, unlike a plain form.
async function saveAndClose() {
  askSave.value = false
  if (createdToken.value) return
  await createKey()
}

// Esc closes only the topmost overlay, so it never also dismisses a lower one.
useEscape(() => drawerOpen.value, requestClose)
useEscape(
  () => !!confirmRevoke.value,
  () => {
    confirmRevoke.value = null
  },
)

async function revokeKey(key) {
  confirmRevoke.value = null
  await auth.revokeAPIKey(key.id)
}

async function copyToken() {
  if (!createdToken.value) return
  try {
    await navigator.clipboard.writeText(createdToken.value)
    copied.value = true
    setTimeout(() => (copied.value = false), 1500)
  } catch {
    // Copying can be blocked by the browser; the token is still on screen to select.
  }
}
</script>

<template>
  <section class="card panel">
    <div class="admin-head">
      <h2>{{ t('title') }}</h2>
      <button class="btn btn-primary" type="button" @click="openCreate">
        <Plus :size="15" />
        <span>{{ t('actionNew') }}</span>
      </button>
    </div>
    <p class="page-sub">{{ t('subtitle') }}</p>

    <p v-if="auth.errorCode" class="error-line">{{ errorText }}</p>

    <p v-if="!myKeys.length" class="empty">{{ t('empty') }}</p>
    <div v-else class="admin-table api-keys-table" role="table">
      <div class="admin-row admin-row-head" role="row">
        <span role="columnheader">{{ t('columnName') }}</span>
        <span role="columnheader">{{ t('columnPrefix') }}</span>
        <span role="columnheader">{{ t('columnScopes') }}</span>
        <span role="columnheader">{{ t('columnCreated') }}</span>
        <span role="columnheader">{{ t('columnLastUsed') }}</span>
        <span role="columnheader">{{ t('columnExpires') }}</span>
        <span role="columnheader">{{ t('columnStatus') }}</span>
        <span role="columnheader"></span>
      </div>
      <div v-for="key in myKeys" :key="key.id" class="admin-row" role="row">
        <span class="admin-cell-strong" role="cell">{{ key.name || '—' }}</span>
        <span class="mono" role="cell">{{ key.prefix }}</span>
        <span class="admin-tags" role="cell">
          <span v-for="scope in key.scopes ?? []" :key="scope" class="chip">{{ enumLabel('permission', scope) }}</span>
          <span v-if="!(key.scopes ?? []).length" class="muted">—</span>
        </span>
        <span role="cell">{{ fmtTime(key.createdAt) }}</span>
        <span role="cell">{{ fmtTime(key.lastUsedAt) }}</span>
        <span role="cell">{{ key.expiresAt ? fmtTime(key.expiresAt) : t('never') }}</span>
        <span role="cell">
          <span v-if="key.revoked" class="chip chip-muted">{{ t('statusRevoked') }}</span>
          <span v-else-if="key.expiresAt && new Date(key.expiresAt) < new Date()" class="chip chip-muted">{{ t('statusExpired') }}</span>
          <span v-else class="chip">{{ t('statusActive') }}</span>
        </span>
        <span class="admin-actions" role="cell">
          <button
            class="btn btn-ghost"
            type="button"
            :disabled="key.revoked"
            @click="confirmRevoke = key"
          >
            <Trash2 :size="14" />
            <span>{{ t('actionRevoke') }}</span>
          </button>
        </span>
      </div>
    </div>

    <Teleport to="body">
    <!-- ---------- create drawer ---------- -->
    <div v-if="drawerOpen" class="drawer-backdrop" @click.self="requestClose">
      <div class="drawer card">
        <div class="page-head">
          <h2>{{ t('drawerTitle') }}</h2>
        </div>

        <p v-if="createdToken" class="token-block">
          <span class="token-label">{{ t('tokenOnce') }}</span>
          <div class="token-row">
            <input class="mono token-input" :value="createdToken" readonly aria-label="API key" />
            <button class="btn btn-ghost" type="button" @click="copyToken">
              <component :is="copied ? Check : Copy" :size="14" />
              <span>{{ copied ? t('copied') : shell.t('actionCopy') }}</span>
            </button>
          </div>
          <button class="btn btn-primary" type="button" @click="closeDrawer">{{ t('actionDone') }}</button>
        </p>

        <form v-else class="drawer-form" @submit.prevent="createKey">
          <div class="field">
            <label for="key-name">{{ t('fieldName') }}</label>
            <input id="key-name" v-model="name" type="text" required :aria-label="t('fieldName')" />
          </div>

          <div class="field">
            <label>{{ t('fieldScopes') }}</label>
            <p class="muted field-hint">{{ t('scopesHint') }}</p>
            <div class="scope-toolbar">
              <button class="btn btn-ghost" type="button" :disabled="!availableScopes.length" @click="toggleAll">
                <component :is="allSelected ? Check : Plus" :size="14" />
                <span>{{ allSelected ? t('deselectAll') : t('selectAll') }}</span>
              </button>
            </div>
            <div class="scope-matrix">
              <label v-for="code in availableScopes" :key="code" class="scope-row">
                <input
                  type="checkbox"
                  :checked="scopes.includes(code)"
                  :aria-label="enumLabel('permission', code)"
                  @change="toggleScope(code)"
                />
                <span>{{ enumLabel('permission', code) }}</span>
              </label>
              <p v-if="!availableScopes.length" class="muted">{{ t('noScopes') }}</p>
            </div>
          </div>

          <div class="field">
            <label for="key-expires">{{ t('fieldExpires') }}</label>
            <select id="key-expires" v-model.number="expiresInDays" :aria-label="t('fieldExpires')">
              <option v-for="opt in expiresOptions" :key="opt.value" :value="opt.value">{{ opt.label }}</option>
            </select>
          </div>

          <div class="drawer-actions">
            <button class="btn btn-primary" type="submit" :disabled="creating || !name.trim()">
              {{ t('actionCreate') }}
            </button>
            <button class="btn btn-ghost" type="button" @click="requestClose">
              {{ shell.t('actionCancel') }}
            </button>
            <span class="muted note">{{ shell.t('escHint') }}</span>
          </div>
        </form>
      </div>
    </div>
    </Teleport>

    <Teleport to="body">
    <UnsavedPrompt
      v-if="askSave"
      :title="shell.t('unsavedTitle')"
      :body="shell.t('unsavedBody')"
      @save="saveAndClose"
      @discard="closeDrawer"
      @cancel="askSave = false"
    />
    </Teleport>

    <Teleport to="body">
    <!-- ---------- revoke confirm ---------- -->
    <div v-if="confirmRevoke" class="drawer-backdrop" @click.self="confirmRevoke = null">
      <div class="drawer card">
        <div class="page-head">
          <h2>{{ t('confirmRevoke') }}</h2>
          <p class="page-sub">{{ confirmRevoke.name }}</p>
        </div>
        <div class="drawer-actions">
          <button class="btn btn-primary" type="button" @click="revokeKey(confirmRevoke)">
            {{ shell.t('actionConfirm') }}
          </button>
          <button class="btn btn-ghost" type="button" @click="confirmRevoke = null">
            {{ shell.t('actionCancel') }}
          </button>
        </div>
      </div>
    </div>
    </Teleport>
  </section>
</template>

<style scoped>
.mono {
  font-family: var(--font-mono, ui-monospace, SFMono-Regular, Menlo, monospace);
  font-size: 12px;
}

/* This panel is a child component, so AdminView's scoped .admin-head does not
   reach it — without this the title and the "新建密钥" button render as stacked
   blocks with no gap. Mirror the shared header layout: one row, title left and
   action right, with breathing room below before the intro line. Pure layout,
   so it holds for every language regardless of label length. */
.admin-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin-block-end: 12px;
}

/* The panel title row (title + "新建密钥") sits flush against the intro line
   below it; give the subtitle breathing room from both. Pure CSS — no text
   involved — so it holds for every language. (Scoped to this panel, so the
   revoke drawer's own .page-sub is untouched.) */
.panel > .page-sub {
  margin-block: 16px;
}

.chip-muted {
  opacity: 0.6;
}

.scope-toolbar {
  display: flex;
  justify-content: flex-end;
  margin-block: 2px 8px;
}

.scope-matrix {
  display: grid;
  gap: 6px;
  margin-block-start: 6px;
  max-height: 240px;
  overflow-y: auto;
}

.scope-row {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 13px;
  cursor: pointer;
}

.field-hint {
  margin-block-end: 0;
  font-size: 12px;
}

.token-block {
  display: grid;
  gap: 10px;
}

.token-label {
  font-size: 12px;
  font-weight: 600;
}

.token-row {
  display: flex;
  gap: 8px;
  align-items: center;
}

.token-input {
  flex: 1;
  padding: 8px 10px;
  background: var(--bg-2, #f5f5f5);
  border: 1px solid var(--stroke-1);
  border-radius: 6px;
  color: var(--text-1);
}
</style>
