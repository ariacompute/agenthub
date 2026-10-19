<script setup>
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { useAuthStore, PERMISSIONS } from '../lib/auth-store.js'
import { useHubStore } from '../lib/store.js'
import { useCopy, enumLabel } from '../lib/use-copy.js'
import { errorMessage } from '../lib/errors.js'
import { KeyRound, Plus, Shield, Trash2, UserPlus, Users, DatabaseBackup } from 'lucide-vue-next'
import BackupsPanel from './admin/BackupsPanel.vue'

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

const errorText = computed(() => errorMessage(auth.errorCode))

// One column per role, so the matrix stays a table however many roles exist.
const matrixColumns = computed(
  () => `minmax(200px, 1.6fr) repeat(${auth.rolesCatalogue.length || 1}, minmax(90px, 1fr))`,
)

// The backups tab lives in its own component: it carries its own data, its own
// schedule form and a drawer, which is more than an inline section should own.
const backup = useCopy('backup')

const tabs = computed(() => [
  { key: 'users', label: t('tabUsers'), icon: Users },
  { key: 'roles', label: t('tabRoles'), icon: Shield },
  { key: 'grants', label: t('tabGrants'), icon: KeyRound },
  { key: 'backups', label: backup.t('tabBackups'), icon: DatabaseBackup },
])

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
  Object.assign(form, { id: '', email: '', name: '', password: '', status: 'active', roles: [], groups: [] })
  drawerOpen.value = true
}

function openEdit(user) {
  editing.value = user
  Object.assign(form, {
    id: user.id,
    email: user.email,
    name: user.name ?? '',
    password: '',
    status: user.status ?? 'active',
    roles: [...(user.roles ?? [])],
    groups: [...(user.groups ?? [])],
  })
  drawerOpen.value = true
}

async function submitUser() {
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
  if (ok) drawerOpen.value = false
}

async function remove(user) {
  confirmDelete.value = null
  await auth.deleteUser(user.id)
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
  await Promise.all([auth.loadUsers(), auth.loadRoles(), auth.loadGroups(), auth.loadGrants(), auth.loadPermissions()])
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
              :disabled="user.isOwner"
              @click="confirmDelete = user"
            >
              {{ user.status === 'disabled' ? t('actionEnable') : t('actionDisable') }}
            </button>
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

      <div class="admin-head admin-head-sub">
        <h2>{{ t('groupsTitle') }}</h2>
      </div>
      <div class="admin-tags">
        <span v-for="group in auth.groupsCatalogue" :key="group.id" class="chip">
          {{ group.name }}
        </span>
        <span v-if="!auth.groupsCatalogue.length" class="muted">—</span>
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
        <button class="btn btn-primary" type="submit" :disabled="!grantForm.resourceId || !grantForm.principalId">
          <Plus :size="15" />
          <span>{{ t('actionGrant') }}</span>
        </button>
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

    <!-- ---------- user drawer ---------- -->
    <div v-if="drawerOpen" class="drawer-backdrop" @click.self="drawerOpen = false">
      <form class="drawer card" @submit.prevent="submitUser">
        <div class="page-head">
          <h2>{{ t('drawerTitle') }}</h2>
        </div>

        <div class="field">
          <label for="user-email">{{ t('fieldEmail') }}</label>
          <input id="user-email" v-model="form.email" type="email" required :aria-label="t('fieldEmail')" />
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
          />
          <span v-if="editing" class="muted">{{ t('passwordHint') }}</span>
        </div>
        <div class="field">
          <label for="user-status">{{ t('fieldStatus') }}</label>
          <select id="user-status" v-model="form.status" :aria-label="t('fieldStatus')">
            <option value="active">{{ t('statusActive') }}</option>
            <option value="disabled">{{ t('statusDisabled') }}</option>
          </select>
        </div>
        <div class="field">
          <label for="user-roles">{{ t('fieldRoles') }}</label>
          <select id="user-roles" v-model="form.roles" multiple :aria-label="t('fieldRoles')">
            <option v-for="role in auth.rolesCatalogue" :key="role.id" :value="role.id">{{ role.name }}</option>
          </select>
        </div>
        <div class="field">
          <label for="user-groups">{{ t('fieldGroups') }}</label>
          <select id="user-groups" v-model="form.groups" multiple :aria-label="t('fieldGroups')">
            <option v-for="group in auth.groupsCatalogue" :key="group.id" :value="group.id">
              {{ group.name }}
            </option>
          </select>
        </div>

        <div class="drawer-actions">
          <button class="btn btn-primary" type="submit">{{ shell.t('actionSave') }}</button>
          <button class="btn btn-ghost" type="button" @click="drawerOpen = false">
            {{ shell.t('actionCancel') }}
          </button>
        </div>
      </form>
    </div>

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

.admin-table {
  /* One grid for every row, so the header and the body share a single set of
     tracks. Every row used to be its own grid, and the trailing `auto` column
     then measured the action buttons in the body and nothing at all in the
     header: different free space, different fr widths, and each heading sat a
     little further right than its column — 40px off by the second column and
     141px by the last. The offset is whatever the buttons measure, so no two
     languages were wrong by the same amount. */
  display: grid;
  grid-template-columns: 1.6fr 1fr 0.7fr 1.2fr 1.2fr auto;
  gap: 10px;
  align-items: center;
  font-size: 13px;
}

.admin-row {
  /* Transparent: the cells become items of the table grid above. Colour, size
     and case are inherited and still reach the cells; the row's own box does
     not exist, so its padding and rule live on the cells instead. */
  display: contents;
}

.admin-row > * {
  min-width: 0;
  padding-block: 10px;
  border-block-end: 1px solid var(--stroke-1);
}

/* The row's 8px inset, kept by the outermost cells now that the row has no
   box of its own. The column pin is what makes one grid behave like a table:
   without it the cells simply fill the tracks in document order, and a row
   with fewer cells than the grid has columns — the grants table has four —
   is continued by the next row instead of starting one. */
.admin-row > *:first-child {
  grid-column: 1;
  padding-inline-start: 8px;
}

.admin-row > *:last-child {
  padding-inline-end: 8px;
}

.admin-row-head {
  color: var(--text-3);
  font-size: 11.5px;
  text-transform: uppercase;
  letter-spacing: 0.05em;
}

.admin-cell-strong {
  display: flex;
  align-items: center;
  gap: 8px;
  font-weight: 600;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.admin-tags {
  display: flex;
  gap: 6px;
  flex-wrap: wrap;
  align-items: center;
}

.admin-actions {
  display: flex;
  gap: 8px;
  justify-content: flex-end;
}

.admin-actions .btn {
  min-width: 0;
}

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

.drawer-actions {
  display: flex;
  gap: 10px;
  margin-block-start: 18px;
}

@media (max-width: 1023px) {
  /* Narrow screens stack the table into cards instead of scrolling sideways,
     which means the rows need their boxes back: one column each, no shared
     tracks to line up with. */
  .admin-table {
    display: flex;
    flex-direction: column;
    align-items: stretch;
    gap: 0;
  }
  .admin-row {
    display: grid;
    grid-template-columns: 1fr;
    gap: 6px;
    padding: 12px 8px;
    border-block-end: 1px solid var(--stroke-1);
  }
  .admin-row > * {
    padding: 0;
    border-block-end: none;
  }
  .admin-row-head {
    display: none;
  }
  .admin-actions {
    justify-content: flex-start;
  }
}
</style>
