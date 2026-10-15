// Console API client. Every error keeps the backend's machine readable code in
// `error.code`; localisation happens in the copy layer, never here.
const BASE = ''

async function request(path, options = {}) {
  const res = await fetch(BASE + path, {
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

export function listResources({ kind = '', q = '' } = {}) {
  const params = new URLSearchParams()
  if (kind) params.set('kind', kind)
  if (q) params.set('q', q)
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
