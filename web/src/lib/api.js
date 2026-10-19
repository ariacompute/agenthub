// Console API client. Every error keeps the backend's machine readable code in
// `error.code`; localisation happens in the copy layer, never here.
const BASE = ''

async function request(path, options = {}) {
  const res = await fetch(BASE + path, {
    // Sessions are HttpOnly cookies, so every call has to be allowed to carry
    // them — including the ones the console makes before it knows who you are.
    credentials: 'include',
    headers: { 'Content-Type': 'application/json', ...(options.headers || {}) },
    ...options,
  })
  if (res.status === 204) return null
  const text = await res.text()
  let payload = null
  if (text) {
    try {
      payload = JSON.parse(text)
    } catch (parseError) {
      console.error('agenthub: response is not JSON', parseError)
    }
  }
  if (!res.ok) {
    const error = new Error(payload?.code || 'internal_error')
    error.code = payload?.code || 'internal_error'
    error.status = res.status
    throw error
  }
  return payload
}

function body(data) {
  return { body: JSON.stringify(data) }
}

export function healthz() {
  return request('/healthz')
}

export function listResources({ kind = '', q = '', namespace = '', status = '' } = {}) {
  const params = new URLSearchParams()
  if (kind) params.set('kind', kind)
  if (q) params.set('q', q)
  if (namespace) params.set('namespace', namespace)
  if (status) params.set('status', status)
  const query = params.toString()
  return request(`/api/resources${query ? `?${query}` : ''}`)
}

export function createResource(entry) {
  return request('/api/resources', { method: 'POST', ...body(entry) })
}

export function updateResource(id, entry) {
  return request(`/api/resources/${encodeURIComponent(id)}`, { method: 'PUT', ...body(entry) })
}

export function deleteResource(id) {
  return request(`/api/resources/${encodeURIComponent(id)}`, { method: 'DELETE' })
}

export function setResourceHealth(id, health) {
  return request(`/api/resources/${encodeURIComponent(id)}/health`, { method: 'POST', ...body({ health }) })
}

// Curation: submitting, approving, rejecting or deprecating a record. The
// reason is operator text about a record and travels with the transition only.
export function setResourceStatus(id, status, reason = '') {
  return request(`/api/resources/${encodeURIComponent(id)}/status`, {
    method: 'POST',
    ...body(reason ? { status, reason } : { status }),
  })
}

export function listApprovals() {
  return request('/api/registries/approvals')
}

export function listRegistries() {
  return request('/api/registries')
}

export function createRegistry(namespace) {
  return request('/api/registries', { method: 'POST', ...body(namespace) })
}

export function updateRegistry(id, namespace) {
  return request(`/api/registries/${encodeURIComponent(id)}`, { method: 'PUT', ...body(namespace) })
}

export function deleteRegistry(id) {
  return request(`/api/registries/${encodeURIComponent(id)}`, { method: 'DELETE' })
}

export function listSources(namespace = '') {
  const params = new URLSearchParams()
  if (namespace) params.set('namespace', namespace)
  const query = params.toString()
  return request(`/api/registry-sources${query ? `?${query}` : ''}`)
}

export function createSource(source) {
  return request('/api/registry-sources', { method: 'POST', ...body(source) })
}

export function updateSource(id, source) {
  return request(`/api/registry-sources/${encodeURIComponent(id)}`, { method: 'PUT', ...body(source) })
}

export function deleteSource(id) {
  return request(`/api/registry-sources/${encodeURIComponent(id)}`, { method: 'DELETE' })
}

export function syncSource(id) {
  return request(`/api/registry-sources/${encodeURIComponent(id)}/sync`, { method: 'POST', ...body({}) })
}

export function listTasks() {
  return request('/api/tasks')
}

export function getTask(id) {
  return request(`/api/tasks/${encodeURIComponent(id)}`)
}

export function getTaskSpans(id) {
  return request(`/api/tasks/${encodeURIComponent(id)}/spans`)
}

export function cancelTask(id) {
  return request(`/api/tasks/${encodeURIComponent(id)}/cancel`, { method: 'POST', ...body({}) })
}

export function resumeTask(id, text) {
  return request(`/api/tasks/${encodeURIComponent(id)}/resume`, {
    method: 'POST',
    ...body({ message: { role: 'user', parts: [{ type: 'text', text }] } }),
  })
}

// --- A2A ------------------------------------------------------------------

function sendParams({ resourceId, text, flow }) {
  const params = { message: { role: 'user', parts: [{ type: 'text', text }] }, metadata: {} }
  if (resourceId) params.metadata.resourceId = resourceId
  if (flow) params.metadata.flow = flow
  return params
}

/** Runs a task and waits for the terminal state. */
export async function sendTask(options) {
  const res = await request('/a2a', {
    method: 'POST',
    ...body({ jsonrpc: '2.0', id: Date.now(), method: 'message/send', params: sendParams(options) }),
  })
  if (res?.error) {
    const error = new Error(res.error.message || 'internal_error')
    error.code = res.error.message || 'internal_error'
    throw error
  }
  return res?.result
}

/**
 * Runs a task over SSE. onEvent receives {event, data} for every frame; the
 * stream ends with the terminal status update (there is no sentinel frame).
 */
export async function streamTask(options, onEvent) {
  const res = await fetch(BASE + '/a2a', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      jsonrpc: '2.0',
      id: Date.now(),
      method: 'message/stream',
      params: sendParams(options),
    }),
  })
  if (!res.ok || !res.body) {
    const error = new Error('stream_unavailable')
    error.code = 'stream_unavailable'
    throw error
  }
  const reader = res.body.getReader()
  const decoder = new TextDecoder()
  let buffer = ''
  for (;;) {
    const { value, done } = await reader.read()
    if (done) break
    buffer += decoder.decode(value, { stream: true })
    const chunks = buffer.split('\n\n')
    buffer = chunks.pop() || ''
    for (const chunk of chunks) {
      const eventName = /^event:\s*(.+)$/m.exec(chunk)?.[1]?.trim()
      const dataLine = /^data:\s*(.+)$/m.exec(chunk)?.[1]
      if (!dataLine) continue
      try {
        const payload = JSON.parse(dataLine.replace(/\\n/g, '\n'))
        onEvent({ event: eventName, data: payload?.result ?? payload?.error ?? payload })
      } catch (parseError) {
        console.error('agenthub: SSE frame is not JSON', parseError)
      }
    }
  }
}

/** Subscribes to the live broker feed of an existing task. */
export function subscribeTaskEvents(id, onEvent) {
  const source = new EventSource(`${BASE}/api/tasks/${encodeURIComponent(id)}/events`)
  for (const name of ['task_status_update', 'task_artifact_update', 'message', 'task_span_update']) {
    source.addEventListener(name, (evt) => {
      try {
        onEvent({ event: name, data: JSON.parse(evt.data) })
      } catch (parseError) {
        console.error('agenthub: SSE frame is not JSON', parseError)
      }
    })
  }
  return () => source.close()
}

export function mcpExport() {
  return request('/api/export/mcp')
}

// --- identity --------------------------------------------------------------

export function authConfig() {
  return request('/api/auth/config')
}

export function register(payload) {
  return request('/api/auth/register', { method: 'POST', ...body(payload) })
}

export function login(email, password) {
  return request('/api/auth/login', { method: 'POST', ...body({ email, password }) })
}

export function logout() {
  return request('/api/auth/logout', { method: 'POST', ...body({}) })
}

export function me() {
  return request('/api/auth/me')
}

export function listSessions() {
  return request('/api/auth/sessions')
}

export function revokeSession(id) {
  return request(`/api/auth/sessions/${encodeURIComponent(id)}`, { method: 'DELETE' })
}

// --- administration --------------------------------------------------------

export function listUsers(q = '') {
  const query = q ? `?${new URLSearchParams({ q }).toString()}` : ''
  return request(`/api/users${query}`)
}

export function getUser(id) {
  return request(`/api/users/${encodeURIComponent(id)}`)
}

export function createUser(payload) {
  return request('/api/users', { method: 'POST', ...body(payload) })
}

export function updateUser(id, payload) {
  return request(`/api/users/${encodeURIComponent(id)}`, { method: 'PUT', ...body(payload) })
}

export function deleteUser(id) {
  return request(`/api/users/${encodeURIComponent(id)}`, { method: 'DELETE' })
}

export function revokeUserSessions(id) {
  return request(`/api/users/${encodeURIComponent(id)}/sessions`, { method: 'DELETE' })
}

export function listPermissions() {
  return request('/api/permissions')
}

export function listRoles() {
  return request('/api/roles')
}

export function createRole(payload) {
  return request('/api/roles', { method: 'POST', ...body(payload) })
}

export function updateRole(id, payload) {
  return request(`/api/roles/${encodeURIComponent(id)}`, { method: 'PUT', ...body(payload) })
}

export function deleteRole(id) {
  return request(`/api/roles/${encodeURIComponent(id)}`, { method: 'DELETE' })
}

export function listGroups() {
  return request('/api/groups')
}

export function createGroup(payload) {
  return request('/api/groups', { method: 'POST', ...body(payload) })
}

export function updateGroup(id, payload) {
  return request(`/api/groups/${encodeURIComponent(id)}`, { method: 'PUT', ...body(payload) })
}

export function deleteGroup(id) {
  return request(`/api/groups/${encodeURIComponent(id)}`, { method: 'DELETE' })
}

export function listGrants(resource = '') {
  const query = resource ? `?${new URLSearchParams({ resource }).toString()}` : ''
  return request(`/api/grants${query}`)
}

export function createGrant(payload) {
  return request('/api/grants', { method: 'POST', ...body(payload) })
}

export function deleteGrant(id) {
  return request(`/api/grants/${encodeURIComponent(id)}`, { method: 'DELETE' })
}

// --- database checkpoints -------------------------------------------------

export function listBackups() {
  return request('/api/backups')
}

export function createBackup(label = '') {
  return request('/api/backups', { method: 'POST', ...body({ label }) })
}

export function getBackupSchedule() {
  return request('/api/backups/schedule')
}

export function saveBackupSchedule(payload) {
  return request('/api/backups/schedule', { method: 'PUT', ...body(payload) })
}

export function deleteBackup(id) {
  return request(`/api/backups/${encodeURIComponent(id)}`, { method: 'DELETE' })
}

// restoreBackup needs the checkpoint id echoed back: it is the last chance to
// notice the wrong row before every table is rewritten.
export function restoreBackup(id, confirm) {
  return request(`/api/backups/${encodeURIComponent(id)}/restore`, { method: 'POST', ...body({ confirm }) })
}

// downloadBackup is the only endpoint that answers with a file instead of JSON,
// so it cannot go through `request`: the name comes from the header the server
// sends, and the bytes stay a blob until the browser saves them.
export async function downloadBackup(id) {
  const res = await fetch(`${BASE}/api/backups/${encodeURIComponent(id)}/download`, {
    credentials: 'include',
  })
  if (!res.ok) {
    let failure = 'internal_error'
    try {
      failure = (await res.json())?.code || failure
    } catch (parseError) {
      console.error('agenthub: error body is not JSON', parseError)
    }
    const error = new Error(failure)
    error.code = failure
    error.status = res.status
    throw error
  }
  return { blob: await res.blob(), filename: filenameFrom(res.headers.get('Content-Disposition')) }
}

function filenameFrom(header) {
  const match = /filename="?([^";]+)"?/.exec(header || '')
  return match ? match[1].trim() : 'checkpoint.tar.gz'
}

// saveBlob hands a downloaded archive to the browser. The object URL is revoked
// straight after the click so a large checkpoint does not stay pinned in memory.
export function saveBlob(blob, filename) {
  const url = URL.createObjectURL(blob)
  const link = document.createElement('a')
  link.href = url
  link.download = filename
  document.body.appendChild(link)
  link.click()
  link.remove()
  URL.revokeObjectURL(url)
}
