<script setup>
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { useAuthStore, PERMISSIONS } from '../lib/auth-store.js'
import { useHubStore } from '../lib/store.js'
import { useCopy, enumLabel } from '../lib/use-copy.js'
import { errorMessage } from '../lib/errors.js'
import { userFieldErrors } from '../lib/user-form.js'
import { isDirty, snapshot, useEscape } from '../lib/use-dismiss.js'
import UnsavedPrompt from '../components/UnsavedPrompt.vue'
import { KeyRound, Plus, Shield, Trash2, UserPlus, Users, UsersRound, DatabaseBackup } from 'lucide-vue-next'
import BackupsPanel from './admin/BackupsPanel.vue'
import ApiKeysPanel from './admin/ApiKeysPanel.vue'

const auth = useAuthStore()
const hub = useHubStore()
const { t } = useCopy('admin')
const shell = useCopy('shell')
const account = useCopy('auth')

const tab = ref('users')
const query = ref('')
const drawerOpen = ref(false)
const editing = ref(null)
const confirmDelete = ref(null)
const askSave = ref(false)
const baseline = ref(null)
const matrix = ref({})

const form = reactive({
  id: '',
  email: '',
  name: '',
  password: '',
  status: 'active',
  roles: [],
  groups: [],
})

const grantForm = reactive({ resourceId: '', principalKind: 'user', principalId: '', level: 'read' })

// A group is the only membership a brand-new install cannot produce through the
// API, so the console owns its entry point. Without it the drawer's picker has
// nothing to choose from and every user ends up group-less. An empty id means
// "create", a set one means "rename / re-bundle".
const groupForm = reactive({ id: '', name: '', roles: [] })
const groupDrawerOpen = ref(false)
const groupBaseline = ref(null)
const askGroupSave = ref(false)
const confirmGroup = ref(null)

// Field level problems the drawer can name itself. The values are machine codes
// - the same ones the server returns - so both paths render identical copy from
// errors-copy.js and nothing here hardcodes prose.
const fieldErrors = reactive({ email: '', password: '' })

function clearFieldErrors() {
  fieldErrors.email = ''
  fieldErrors.password = ''
}

// Validates everything the client can judge before the request goes out, so a
// rejected save names every bad field at once instead of one round trip per rule.
function validateUser() {
  // An empty password box while editing means "keep the current one"; a password
  // that was typed still has to pass the rule the server enforces.
  const errors = userFieldErrors(form, { keepPassword: editing.value !== null && form.password === '' })
  fieldErrors.email = errors.email
  fieldErrors.password = errors.password
  return !errors.email && !errors.password
}

const errorText = computed(() => errorMessage(auth.errorCode))

// One column per role, so the matrix stays a table however many roles exist.
const matrixColumns = computed(
  () => `minmax(200px, 1.6fr) repeat(${auth.rolesCatalogue.length || 1}, minmax(90px, 1fr))`,
)

// The backups tab lives in its own component: it carries its own data, its own
// schedule form and a drawer, which is more than an inline section should own.
const backup = useCopy('backup')

// The API keys tab is always present: any signed-in user may manage their own
// keys, so reaching /admin must not depend on holding a management permission. The
// other tabs appear only when the user may act on them, so the screen stays
// honest about what the session can do.
const tabs = computed(() => {
  const items = []
  if (auth.can('users:manage')) items.push({ key: 'users', label: t('tabUsers'), icon: Users })
  if (auth.can('roles:manage')) items.push({ key: 'roles', label: t('tabRoles'), icon: Shield })
  // Groups are their own screen even though they need the same permission: a
  // group is a membership, not a permission set, and it is the principal the
  // grants screen points at.
  if (auth.can('roles:manage')) items.push({ key: 'groups', label: t('tabGroups'), icon: UsersRound })
  if (auth.can('grants:manage')) items.push({ key: 'grants', label: t('tabGrants'), icon: KeyRound })
  if (auth.can('backups:manage')) items.push({ key: 'backups', label: backup.t('tabBackups'), icon: DatabaseBackup })
  items.push({ key: 'apikeys', label: t('tabApiKeys'), icon: KeyRound })
  return items
})

// Keep the active tab valid: a session whose permissions change (or simply cannot
// see a tab) falls back to the first one it can.
watch(
  tabs,
  (list) => {
    if (!list.some((item) => item.key === tab.value)) {
      tab.value = list[0]?.key ?? 'apikeys'
    }
  },
  { immediate: true },
)

const statusLabel = (value) => enumLabel('userStatus', value)
const permissionLabel = (code) => enumLabel('permission', code)
const roleName = (id) => auth.rolesCatalogue.find((role) => role.id === id)?.name ?? id
const groupName = (id) => auth.groupsCatalogue.find((group) => group.id === id)?.name ?? id
const resourceName = (id) => hub.resources.find((resource) => resource.id === id)?.name ?? id

const users = computed(() => {
  const q = query.value.trim().toLowerCase()
  if (!q) return auth.users
  return auth.users.filter((user) =>
    [user.email, user.name].filter(Boolean).some((field) => String(field).toLowerCase().includes(q)),
  )
})

// The matrix is edited locally and saved per role: a half-ticked matrix must
// never be sent for a role the operator did not touch.
//
// The first tick seeds the row from what the role already holds, so ticking one
// box keeps the others — a row starting from "everything off" would silently
// strip permissions nobody asked to remove.
function roleRow(roleID) {
  const role = auth.rolesCatalogue.find((item) => item.id === roleID)
  const row = {}
  for (const code of PERMISSIONS) row[code] = Boolean(role?.permissions?.includes(code))
  return row
}

function toggle(roleID, permission) {
  const row = { ...(matrix.value[roleID] ?? roleRow(roleID)) }
  row[permission] = !row[permission]
  matrix.value = { ...matrix.value, [roleID]: row }
}

function checked(roleID, permission) {
  const row = matrix.value[roleID]
  if (row) return Boolean(row[permission])
  return roleRow(roleID)[permission]
}

async function saveRole(role) {
  const permissions = PERMISSIONS.filter((code) => checked(role.id, code))
  await auth.updateRole(role.id, { id: role.id, name: role.name, permissions })
}

function resetMatrix() {
  matrix.value = {}
}

function openCreate() {
  editing.value = null
  // An error left over from a previous save belongs to that attempt, not this
  // one; the drawer would otherwise open already shouting about it.
  auth.clearError()
  clearFieldErrors()
  Object.assign(form, { id: '', email: '', name: '', password: '', status: 'active', roles: [], groups: [] })
  baseline.value = snapshot(form)
  drawerOpen.value = true
}

function openEdit(user) {
  editing.value = user
  auth.clearError()
  clearFieldErrors()
  Object.assign(form, {
    id: user.id,
    email: user.email,
    name: user.name ?? '',
    password: '',
    status: user.status ?? 'active',
    roles: [...(user.roles ?? [])],
    groups: [...(user.groups ?? [])],
  })
  baseline.value = snapshot(form)
  drawerOpen.value = true
}

function closeDrawer() {
  drawerOpen.value = false
  askSave.value = false
  baseline.value = null
}

// One gesture, three entry points: Escape, the backdrop and Cancel all ask
// here, and only when the form actually changed.
function requestClose() {
  if (isDirty(baseline.value, form)) {
    askSave.value = true
    return
  }
  closeDrawer()
}

useEscape(() => drawerOpen.value, requestClose)
useEscape(() => groupDrawerOpen.value, requestGroupClose)

// The delete confirmation holds no editable state, so Escape simply dismisses
// it — asking before closing a box that only has Confirm and Cancel would be
// a question about nothing.
useEscape(
  () => confirmDelete.value !== null,
  () => {
    confirmDelete.value = null
  },
)

useEscape(
  () => confirmGroup.value !== null,
  () => {
    confirmGroup.value = null
  },
)

// Saving from the prompt is the same save; the prompt steps aside so a
// failure is reported by the drawer's own error banner.
async function saveAndClose() {
  askSave.value = false
  await submitUser()
}

async function submitUser() {
  auth.clearError()
  if (!validateUser()) return
  const payload = {
    email: form.email,
    name: form.name,
    status: form.status,
    roles: form.roles,
    groups: form.groups,
  }
  if (form.password) payload.password = form.password
  const ok = editing.value
    ? await auth.updateUser(editing.value.id, payload)
    : await auth.createUser({ ...payload, password: form.password })
  if (ok) {
    closeDrawer()
    return
  }
  // The server is the final judge: move its verdict next to the field it is
  // about (an address already taken, a password it still refuses) and leave
  // everything else in the banner - a role deleted since the drawer opened, a
  // request the API rejected outright.
  const code = auth.errorCode
  if (code === 'invalid_email' || code === 'email_taken') fieldErrors.email = code
  else if (code === 'weak_password') fieldErrors.password = code
  if (fieldErrors.email || fieldErrors.password) auth.clearError()
}

async function remove(user) {
  confirmDelete.value = null
  await auth.deleteUser(user.id)
}

// Enable and Disable are reversible: they only flip the account's ability to
// sign in, never the row. Delete (above) is the soft removal that keeps the
// audit trail but marks the user removed.
async function enable(user) {
  await auth.updateUser(user.id, { status: 'active' })
}

async function disable(user) {
  await auth.updateUser(user.id, { status: 'disabled' })
}

function openGroupCreate() {
  auth.clearError()
  groupForm.id = ''
  groupForm.name = ''
  groupForm.roles = []
  groupBaseline.value = snapshot(groupForm)
  groupDrawerOpen.value = true
}

function openGroupEdit(group) {
  auth.clearError()
  groupForm.id = group.id
  groupForm.name = group.name ?? ''
  // Copy: the checkbox list must not write into the cached catalogue entry.
  groupForm.roles = [...(group.roles ?? [])]
  groupBaseline.value = snapshot(groupForm)
  groupDrawerOpen.value = true
}

function closeGroupDrawer() {
  groupDrawerOpen.value = false
  askGroupSave.value = false
  groupBaseline.value = null
}

// Same three-way gesture as the user drawer: Escape, the backdrop and Cancel all
// land here, and only a form that actually changed gets a question.
function requestGroupClose() {
  if (isDirty(groupBaseline.value, groupForm)) {
    askGroupSave.value = true
    return
  }
  closeGroupDrawer()
}

// Saving from the prompt is the same save; the prompt steps aside so a failure
// is reported by the drawer's own banner.
async function saveGroupAndClose() {
  askGroupSave.value = false
  await submitGroup()
}

async function submitGroup() {
  const name = groupForm.name.trim()
  if (!name) return
  auth.clearError()
  const payload = { name, roles: groupForm.roles }
  // Both calls reload the catalogue, which is also what the user drawer reads,
  // so the result is pickable the moment this returns.
  const ok = groupForm.id ? await auth.updateGroup(groupForm.id, payload) : await auth.createGroup(payload)
  if (ok) closeGroupDrawer()
  // A failure (name already in use, role deleted meanwhile) stays in the drawer
  // banner, next to the button that failed.
}

async function removeGroup() {
  const group = confirmGroup.value
  confirmGroup.value = null
  if (group) await auth.deleteGroup(group.id)
}

async function submitGrant() {
  const ok = await auth.createGrant({
    resourceId: grantForm.resourceId,
    principalKind: grantForm.principalKind,
    principalId: grantForm.principalId,
    level: grantForm.level,
  })
  if (ok) grantForm.principalId = ''
}

onMounted(async () => {
  await Promise.all([
    auth.loadUsers(),
    auth.loadRoles(),
    auth.loadGroups(),
    auth.loadGrants(),
    auth.loadPermissions(),
    auth.loadAPIKeys(),
  ])
  if (!hub.resources.length) hub.refreshResources()
})

// The search runs against the backend so a large directory is never paged into
// the browser; debouncing keeps one keystroke from becoming one request burst.
let timer = null
watch(query, (value) => {
  clearTimeout(timer)
  timer = setTimeout(() => auth.loadUsers(value), 250)
})
</script>

<template>
  <div>
    <div class="page-head">
      <h1>{{ t('title') }}</h1>
      <p class="page-sub">{{ t('subtitle') }}</p>
    </div>

    <div class="toolbar">
      <div class="tabs" role="tablist">
        <button
          v-for="item in tabs"
          :key="item.key"
          class="tab"
          type="button"
          role="tab"
          :class="{ active: tab === item.key }"
          :aria-selected="tab === item.key"
          @click="tab = item.key"
        >
          <component :is="item.icon" :size="14" />
          <span>{{ item.label }}</span>
        </button>
      </div>
      <div class="spacer"></div>
      <input
        v-if="tab === 'users'"
        v-model="query"
        class="admin-search"
        type="search"
        :placeholder="t('searchPlaceholder')"
        :aria-label="t('searchPlaceholder')"
      />
    </div>

    <p v-if="auth.errorCode" class="error-line">{{ errorText }}</p>

    <!-- ---------- users ---------- -->
    <section v-if="tab === 'users'" class="card panel">
      <div class="admin-head">
        <h2>{{ t('tabUsers') }}</h2>
        <button class="btn btn-primary" type="button" @click="openCreate">
          <UserPlus :size="15" />
          <span>{{ t('actionNew') }}</span>
        </button>
      </div>

      <p v-if="!users.length" class="empty">{{ t('emptyUsers') }}</p>
      <div v-else class="admin-table" role="table">
        <div class="admin-row admin-row-head" role="row">
          <span role="columnheader">{{ t('columnEmail') }}</span>
          <span role="columnheader">{{ t('columnName') }}</span>
          <span role="columnheader">{{ t('columnStatus') }}</span>
          <span role="columnheader">{{ t('columnRoles') }}</span>
          <span role="columnheader">{{ t('columnGroups') }}</span>
          <span role="columnheader"></span>
        </div>
        <div v-for="user in users" :key="user.id" class="admin-row" role="row">
          <span class="admin-cell-strong" role="cell">
            {{ user.email }}
            <span v-if="user.isOwner" class="chip">{{ account.t('ownerBadge') }}</span>
          </span>
          <span role="cell">{{ user.name || '—' }}</span>
          <span role="cell">{{ statusLabel(user.status) }}</span>
          <span class="admin-tags" role="cell">
            <span v-for="role in user.roles ?? []" :key="role" class="chip">{{ roleName(role) }}</span>
            <span v-if="!(user.roles ?? []).length" class="muted">—</span>
          </span>
          <span class="admin-tags" role="cell">
            <span v-for="group in user.groups ?? []" :key="group" class="chip">{{ groupName(group) }}</span>
            <span v-if="!(user.groups ?? []).length" class="muted">—</span>
          </span>
          <span class="admin-actions" role="cell">
            <button class="btn btn-ghost" type="button" @click="openEdit(user)">{{ t('actionEdit') }}</button>
            <button
              class="btn btn-ghost"
              type="button"
              :disabled="user.isOwner || user.status === 'active'"
              @click="enable(user)"
            >{{ t('actionEnable') }}</button>
            <button
              class="btn btn-ghost"
              type="button"
              :disabled="user.isOwner || user.status === 'disabled' || user.status === 'deleted'"
              @click="disable(user)"
            >{{ t('actionDisable') }}</button>
            <button
              class="btn btn-ghost"
              type="button"
              :disabled="user.isOwner || user.status === 'deleted'"
              @click="confirmDelete = user"
            >{{ t('actionDelete') }}</button>
          </span>
        </div>
      </div>
      <p class="muted admin-foot">{{ t('ownerHint') }}</p>
    </section>

    <!-- ---------- roles and permissions ---------- -->
    <section v-if="tab === 'roles'" class="card panel">
      <div class="admin-head">
        <h2>{{ t('rolesTitle') }}</h2>
        <button class="btn btn-ghost" type="button" @click="resetMatrix">{{ shell.t('actionRefresh') }}</button>
      </div>
      <p class="page-sub">{{ t('rolesHint') }}</p>

      <div class="matrix" role="table">
        <div class="matrix-row matrix-row-head" role="row" :style="{ gridTemplateColumns: matrixColumns }">
          <span role="columnheader"></span>
          <!-- Centred like the checkbox below it: the column is far wider than
               either, so a left-aligned label reads as belonging to the column
               on its left. -->
          <span
            v-for="role in auth.rolesCatalogue"
            :key="role.id"
            class="matrix-cell-head"
            role="columnheader"
          >
            {{ role.name }}
            <span v-if="role.builtin" class="chip">{{ t('builtinBadge') }}</span>
          </span>
        </div>
        <div
          v-for="permission in PERMISSIONS"
          :key="permission"
          class="matrix-row"
          role="row"
          :style="{ gridTemplateColumns: matrixColumns }"
        >
          <span class="matrix-permission" role="cell">{{ permissionLabel(permission) }}</span>
          <span v-for="role in auth.rolesCatalogue" :key="role.id" class="matrix-cell" role="cell">
            <input
              type="checkbox"
              :checked="checked(role.id, permission)"
              :aria-label="`${role.name} ${permissionLabel(permission)}`"
              @change="toggle(role.id, permission)"
            />
          </span>
        </div>
      </div>

      <div class="matrix-save">
        <button
          v-for="role in auth.rolesCatalogue"
          :key="role.id"
          class="btn btn-ghost"
          type="button"
          @click="saveRole(role)"
        >
          {{ shell.t('actionSave') }} · {{ role.name }}
        </button>
      </div>

    </section>

    <!-- ---------- groups ---------- -->
    <section v-if="tab === 'groups'" class="card panel">
      <div class="admin-head">
        <h2>{{ t('groupsTitle') }}</h2>
        <button class="btn btn-primary" type="button" @click="openGroupCreate">
          <Plus :size="15" />
          <span>{{ t('actionNewGroup') }}</span>
        </button>
      </div>
      <p class="page-sub">{{ t('groupsHint') }}</p>

      <div class="group-list">
        <div v-for="group in auth.groupsCatalogue" :key="group.id" class="group-row">
          <div class="group-main">
            <span class="group-name">{{ group.name }}</span>
            <span v-for="role in group.roles ?? []" :key="role" class="chip">{{ roleName(role) }}</span>
            <span v-if="!(group.roles ?? []).length" class="muted">—</span>
          </div>
          <div class="group-actions">
            <button class="btn btn-ghost" type="button" @click="openGroupEdit(group)">
              {{ t('actionEdit') }}
            </button>
            <button class="btn btn-ghost" type="button" @click="confirmGroup = group">
              {{ t('actionDelete') }}
            </button>
          </div>
        </div>
        <p v-if="!auth.groupsCatalogue.length" class="muted">—</p>
      </div>
    </section>

    <!-- ---------- resource grants ---------- -->
    <section v-if="tab === 'grants'" class="card panel">
      <div class="admin-head">
        <h2>{{ t('grantsTitle') }}</h2>
      </div>
      <p class="page-sub">{{ t('grantsHint') }}</p>

      <form class="grant-form" @submit.prevent="submitGrant">
        <div class="field">
          <label for="grant-resource">{{ t('columnResource') }}</label>
          <select id="grant-resource" v-model="grantForm.resourceId" :aria-label="t('columnResource')">
            <option v-for="resource in hub.resources" :key="resource.id" :value="resource.id">
              {{ resource.name }} · {{ resource.id }}
            </option>
          </select>
        </div>
        <div class="field">
          <label for="grant-kind">{{ t('columnPrincipal') }}</label>
          <select id="grant-kind" v-model="grantForm.principalKind" :aria-label="t('columnPrincipal')">
            <option value="user">{{ t('principalUser') }}</option>
            <option value="group">{{ t('principalGroup') }}</option>
          </select>
        </div>
        <div class="field">
          <label for="grant-principal">{{ t('columnPrincipal') }}</label>
          <select id="grant-principal" v-model="grantForm.principalId" :aria-label="t('columnPrincipal')">
            <template v-if="grantForm.principalKind === 'user'">
              <option v-for="user in auth.users" :key="user.id" :value="user.id">{{ user.email }}</option>
            </template>
            <template v-else>
              <option v-for="group in auth.groupsCatalogue" :key="group.id" :value="group.id">
                {{ group.name }}
              </option>
            </template>
          </select>
        </div>
        <div class="field">
          <label for="grant-level">{{ t('columnLevel') }}</label>
          <select id="grant-level" v-model="grantForm.level" :aria-label="t('columnLevel')">
            <option value="read">{{ t('levelRead') }}</option>
            <option value="write">{{ t('levelWrite') }}</option>
          </select>
        </div>
        <div class="field">
          <label aria-hidden="true">&nbsp;</label>
          <button
            class="btn btn-primary"
            type="submit"
            :aria-label="t('actionGrant')"
            :disabled="!grantForm.resourceId || !grantForm.principalId"
          >
            <Plus :size="15" />
            <span>{{ t('actionGrant') }}</span>
          </button>
        </div>
      </form>

      <p v-if="!auth.grants.length" class="empty">{{ t('grantsEmpty') }}</p>
      <div v-else class="admin-table" role="table">
        <div class="admin-row admin-row-head" role="row">
          <span role="columnheader">{{ t('columnResource') }}</span>
          <span role="columnheader">{{ t('columnPrincipal') }}</span>
          <span role="columnheader">{{ t('columnLevel') }}</span>
          <span role="columnheader"></span>
        </div>
        <div v-for="grant in auth.grants" :key="grant.id" class="admin-row" role="row">
          <span role="cell">{{ resourceName(grant.resourceId) }}</span>
          <span role="cell">
            <span class="chip">
              {{ grant.principalKind === 'user' ? t('principalUser') : t('principalGroup') }}
            </span>
            {{
              grant.principalKind === 'user'
                ? auth.users.find((user) => user.id === grant.principalId)?.email ?? grant.principalId
                : groupName(grant.principalId)
            }}
          </span>
          <span role="cell">{{ grant.level === 'write' ? t('levelWrite') : t('levelRead') }}</span>
          <span class="admin-actions" role="cell">
            <button class="btn btn-ghost" type="button" @click="auth.deleteGrant(grant.id)">
              <Trash2 :size="14" />
              <span>{{ t('actionRevoke') }}</span>
            </button>
          </span>
        </div>
      </div>
    </section>

    <!-- ---------- database checkpoints ---------- -->
    <BackupsPanel v-if="tab === 'backups'" />

    <!-- ---------- API keys ---------- -->
    <ApiKeysPanel v-if="tab === 'apikeys'" />

    <!-- ---------- user drawer ---------- -->
    <div v-if="drawerOpen" class="drawer-backdrop" @click.self="requestClose">
      <!-- novalidate: the browser's own bubbles are neither localised by the
           console nor able to name more than one field at a time, so validation
           is ours (validateUser) and the messages come from errors-copy. -->
      <form class="drawer card" novalidate @submit.prevent="submitUser">
        <div class="page-head">
          <h2>{{ t('drawerTitle') }}</h2>
        </div>

        <div class="field">
          <label for="user-email">{{ t('fieldEmail') }}</label>
          <input
            id="user-email"
            v-model="form.email"
            type="email"
            :aria-label="t('fieldEmail')"
            :aria-invalid="Boolean(fieldErrors.email)"
            :aria-describedby="fieldErrors.email ? 'user-email-error' : undefined"
            @input="fieldErrors.email = ''"
          />
          <span v-if="fieldErrors.email" id="user-email-error" class="field-error">
            {{ errorMessage(fieldErrors.email) }}
          </span>
        </div>
        <div class="field">
          <label for="user-name">{{ t('fieldName') }}</label>
          <input id="user-name" v-model="form.name" type="text" :aria-label="t('fieldName')" />
        </div>
        <div class="field">
          <label for="user-password">{{ t('fieldPassword') }}</label>
          <input
            id="user-password"
            v-model="form.password"
            type="password"
            autocomplete="new-password"
            :aria-label="t('fieldPassword')"
            :aria-invalid="Boolean(fieldErrors.password)"
            :aria-describedby="fieldErrors.password ? 'user-password-error' : undefined"
            @input="fieldErrors.password = ''"
          />
          <!-- Create has no existing password to keep, so the hint has to state
               the rule the server actually enforces (weak_password). -->
          <span class="muted">{{ editing ? t('passwordHint') : account.t('passwordHint') }}</span>
          <span v-if="fieldErrors.password" id="user-password-error" class="field-error">
            {{ errorMessage(fieldErrors.password) }}
          </span>
        </div>
        <div class="field">
          <label for="user-status">{{ t('fieldStatus') }}</label>
          <select id="user-status" v-model="form.status" :aria-label="t('fieldStatus')">
            <option value="active">{{ t('statusActive') }}</option>
            <option value="disabled">{{ t('statusDisabled') }}</option>
          </select>
        </div>
        <!-- Membership is rendered as ticked chips, not a native multi-select:
             a listbox always lists the whole catalogue, so an account with no
             role looked exactly like one holding every role. Here an assigned
             role is a box with a tick in it, and an unassigned one is an empty
             box. -->
        <div class="field">
          <span class="field-label">{{ t('fieldRoles') }}</span>
          <div class="chip-picker" role="group" :aria-label="t('fieldRoles')">
            <label
              v-for="role in auth.rolesCatalogue"
              :key="role.id"
              class="chip chip-pick"
              :class="{ 'chip-pick-on': form.roles.includes(role.id) }"
            >
              <input v-model="form.roles" type="checkbox" :value="role.id" />
              <span>{{ role.name }}</span>
            </label>
            <span v-if="!auth.rolesCatalogue.length" class="muted">—</span>
          </div>
        </div>
        <div class="field">
          <span class="field-label">{{ t('fieldGroups') }}</span>
          <div class="chip-picker" role="group" :aria-label="t('fieldGroups')">
            <label
              v-for="group in auth.groupsCatalogue"
              :key="group.id"
              class="chip chip-pick"
              :class="{ 'chip-pick-on': form.groups.includes(group.id) }"
            >
              <input v-model="form.groups" type="checkbox" :value="group.id" />
              <span>{{ group.name }}</span>
            </label>
            <span v-if="!auth.groupsCatalogue.length" class="muted">—</span>
          </div>
        </div>

        <!-- The page level banner sits behind the backdrop, so a rejected save
             has to say what went wrong here, next to the button that failed. -->
        <p v-if="auth.errorCode" class="error-line">{{ errorText }}</p>

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

    <!-- ---------- group drawer ---------- -->
    <div v-if="groupDrawerOpen" class="drawer-backdrop" @click.self="requestGroupClose">
      <form class="drawer card" novalidate @submit.prevent="submitGroup">
        <div class="page-head">
          <h2>{{ t('groupsTitle') }}</h2>
        </div>

        <div class="field">
          <label for="group-name">{{ t('fieldGroupName') }}</label>
          <input id="group-name" v-model="groupForm.name" type="text" :aria-label="t('fieldGroupName')" />
        </div>
        <div class="field">
          <span class="field-label">{{ t('fieldRoles') }}</span>
          <div class="chip-picker" role="group" :aria-label="t('fieldRoles')">
            <label
              v-for="role in auth.rolesCatalogue"
              :key="role.id"
              class="chip chip-pick"
              :class="{ 'chip-pick-on': groupForm.roles.includes(role.id) }"
            >
              <input v-model="groupForm.roles" type="checkbox" :value="role.id" />
              <span>{{ role.name }}</span>
            </label>
          </div>
        </div>

        <p v-if="auth.errorCode" class="error-line">{{ errorText }}</p>

        <div class="drawer-actions">
          <button class="btn btn-primary" type="submit" :disabled="!groupForm.name.trim()">
            {{ shell.t('actionSave') }}
          </button>
          <button class="btn btn-ghost" type="button" @click="requestGroupClose">
            {{ shell.t('actionCancel') }}
          </button>
        </div>
      </form>
    </div>

    <UnsavedPrompt
      v-if="askGroupSave"
      :title="shell.t('unsavedTitle')"
      :body="shell.t('unsavedBody')"
      @save="saveGroupAndClose"
      @discard="closeGroupDrawer"
      @cancel="askGroupSave = false"
    />

    <!-- ---------- confirm ---------- -->
    <div v-if="confirmDelete" class="drawer-backdrop" @click.self="confirmDelete = null">
      <div class="drawer card">
        <div class="page-head">
          <h2>{{ t('confirmDelete') }}</h2>
          <p class="page-sub">{{ confirmDelete.email }}</p>
        </div>
        <div class="drawer-actions">
          <button class="btn btn-primary" type="button" @click="remove(confirmDelete)">
            {{ shell.t('actionConfirm') }}
          </button>
          <button class="btn btn-ghost" type="button" @click="confirmDelete = null">
            {{ shell.t('actionCancel') }}
          </button>
        </div>
      </div>
    </div>

    <div v-if="confirmGroup" class="drawer-backdrop" @click.self="confirmGroup = null">
      <div class="drawer card">
        <div class="page-head">
          <h2>{{ t('confirmDeleteGroup') }}</h2>
          <p class="page-sub">{{ confirmGroup.name }}</p>
        </div>
        <div class="drawer-actions">
          <button class="btn btn-primary" type="button" @click="removeGroup">
            {{ shell.t('actionConfirm') }}
          </button>
          <button class="btn btn-ghost" type="button" @click="confirmGroup = null">
            {{ shell.t('actionCancel') }}
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.admin-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin-block-end: 12px;
}

.admin-head-sub {
  margin-block-start: 22px;
  padding-block-start: 16px;
  border-block-start: 1px solid var(--stroke-1);
}

.admin-search {
  min-width: 240px;
}

.admin-foot {
  margin-block-start: 12px;
  font-size: 12px;
}

/* ---------- membership picker ---------- */
.field-label {
  font-size: 12px;
  color: var(--text-3);
}

/* Field level validation. The palette is monochrome, so the mark is a rule and
   full-brightness text rather than a colour. */
.field-error {
  padding-inline-start: 8px;
  border-inline-start: 2px solid var(--text-1);
  color: var(--text-1);
  font-size: 11.5px;
}

.chip-picker {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
}

.chip-pick {
  cursor: pointer;
  border-color: var(--stroke-1);
  background: transparent;
  color: var(--text-3);
  font-size: 12px;
  transition: border-color 0.16s ease, background 0.16s ease, color 0.16s ease;
}

.chip-pick input {
  width: 13px;
  height: 13px;
  margin: 0;
  cursor: pointer;
  accent-color: var(--text-1);
}

.chip-pick:hover {
  border-color: var(--stroke-2);
}

/* A ticked chip is filled and bright: assignment is the one thing this control
   must never leave ambiguous. */
.chip-pick-on {
  border-color: var(--stroke-3);
  background: var(--accent-soft);
  color: var(--text-1);
}

/* Admin table styles (`.admin-table` and friends, plus the narrow-screen
   media query) now live in style.css, because ApiKeysPanel and other child
   panels render those elements and a parent's scoped styles would not reach
   them. Kept here only as a pointer, not as rules. */

/* ---------- permission matrix ---------- */
.matrix {
  margin-block-start: 14px;
  overflow-x: auto;
}

.matrix-row {
  display: grid;
  /* The column count follows the role catalogue at runtime, not a hard-coded grid. */
  gap: 8px;
  align-items: center;
  padding: 9px 6px;
  border-block-end: 1px solid var(--stroke-1);
}

.matrix-row-head {
  color: var(--text-3);
  font-size: 12px;
}

.matrix-permission {
  font-size: 13px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

/* The role names sit over their own column's checkbox, which .matrix-cell
   centres; the label has to be centred the same way or it drifts left of the
   ticks it names. Flex, not grid: the name and the builtin chip are one line,
   and a grid container would stack them. */
.matrix-cell-head {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 6px;
  flex-wrap: wrap;
  /* A role name longer than its column wraps instead of dragging the label off
     centre: one unbreakable word wider than the track overflows, and an
     overflowing centred item does not stay centred. */
  min-width: 0;
  overflow-wrap: anywhere;
  text-align: center;
}

.matrix-cell {
  display: grid;
  place-items: center;
}

.matrix-cell input {
  width: 16px;
  height: 16px;
  accent-color: #ffffff;
  cursor: pointer;
}

.matrix-save {
  display: flex;
  gap: 8px;
  flex-wrap: wrap;
  margin-block-start: 14px;
}

.grant-form {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(170px, 1fr));
  gap: 12px;
  align-items: end;
  margin-block: 14px;
}

/* One line per group: the name and its role bundle carry the row, the buttons
   sit at the far end so nothing destructive is the first thing a stray click
   meets. */
.group-list {
  display: flex;
  flex-direction: column;
  gap: 8px;
  margin-block-start: 12px;
}

.group-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 8px 12px;
  border: 1px solid var(--stroke-1);
  border-radius: var(--radius-sm, 6px);
}

.group-main {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
}

.group-name {
  font-size: 13px;
  color: var(--text-1);
}

.group-actions {
  display: flex;
  gap: 8px;
}

/* Narrow-screen admin table rules moved to style.css alongside the rest. */
</style>
