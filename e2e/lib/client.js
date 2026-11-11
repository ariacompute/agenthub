/**
 * A thin HTTP client over built-in fetch. It deliberately sends no Origin
 * header: internal/middleware/csrf.go lets non-browser callers through, and a
 * forged cross-site Origin would make every write fail CSRF instead of testing
 * the feature.
 *
 * Four auth modes: 'session' (cookie jar / X-AgentHub-Session / bearer fallback),
 * 'anon' (nothing), 'key' (bearer only) and 'cookie' (cookie jar only).
 */
export function createClient(config) {
  let cookie = ''
  const identity = { sessionToken: config.sessionToken, apiKey: config.apiKey }

  function authHeaders(mode) {
    if (mode === 'anon') return {}
    if (mode === 'key') {
      if (!identity.apiKey) throw new Error('API key mode requested but AGENTHUB_API_KEY is unset')
      return { Authorization: `Bearer ${identity.apiKey}` }
    }
    if (mode === 'cookie') return cookie ? { Cookie: cookie } : {}
    // 'session': prefer the cookie we were handed, then an injected token,
    // then a long-lived key (the "API key only" run needs no login).
    if (cookie) return { Cookie: cookie }
    if (identity.sessionToken) return { 'X-AgentHub-Session': identity.sessionToken }
    if (identity.apiKey) return { Authorization: `Bearer ${identity.apiKey}` }
    return {}
  }

  function buildUrl(path, query) {
    const url = new URL(path.startsWith('/') ? path : `/${path}`, config.baseURL)
    if (query) {
      for (const [key, value] of Object.entries(query)) {
        if (value !== undefined && value !== null && value !== '') url.searchParams.set(key, String(value))
      }
    }
    return url.toString()
  }

  async function send(method, path, opts = {}) {
    const {
      body, query, mode = 'session', headers: extra = {},
      timeoutMs = config.maxTimeMs, contentType,
    } = opts
    const headers = { ...authHeaders(mode), ...extra }
    let payload
    if (body !== undefined && body !== null) {
      if (typeof FormData !== 'undefined' && body instanceof FormData) {
        payload = body // fetch adds the multipart boundary itself
      } else if (typeof body === 'string') {
        payload = body
        if ((method === 'POST' || method === 'PUT' || method === 'PATCH') && !headers['Content-Type'] && contentType !== null) {
          headers['Content-Type'] = contentType || 'application/json'
        }
      } else {
        payload = JSON.stringify(body)
        if (!headers['Content-Type']) headers['Content-Type'] = 'application/json'
      }
    }
    const url = buildUrl(path, query)
    const response = await fetch(url, {
      method,
      headers,
      body: payload,
      redirect: 'manual',
      signal: AbortSignal.timeout(timeoutMs),
    })
    absorbCookies(response)
    return { response, url }
  }

  function absorbCookies(response) {
    const raw = typeof response.headers.getSetCookie === 'function'
      ? response.headers.getSetCookie()
      : (response.headers.get('set-cookie') ? [response.headers.get('set-cookie')] : [])
    for (const line of raw) {
      const pair = String(line).split(';', 1)[0].trim()
      if (!pair) continue
      const name = pair.split('=', 1)[0]
      // Ignore deletions (Max-Age=0) so a logout does not clear a jar we still
      // need for cleanup.
      if (/Max-Age=0/i.test(String(line)) || /Expires=Thu, 01 Jan 1970/i.test(String(line))) continue
      if (name === 'agenthub_session') cookie = pair
    }
  }

  async function request(method, path, opts = {}) {
    const started = Date.now()
    const { response, url } = await send(method, path, opts)
    const headers = Object.fromEntries(response.headers.entries())
    let text = ''
    let size = 0
    if (opts.binary) {
      const buffer = await response.arrayBuffer()
      size = buffer.byteLength
    } else {
      text = await response.text()
      size = text.length
    }
    let json = null
    if (text) {
      try { json = JSON.parse(text) } catch { /* plain text or SSE */ }
    }
    return {
      method, url, status: response.status,
      ok: response.status >= 200 && response.status < 300,
      headers, location: headers.location || null,
      text, json, size,
      code: json?.code || json?.error?.message || null,
      durationMs: Date.now() - started,
    }
  }

  /**
   * Read an SSE body with bounded time and frame count. There is no [DONE]
   * sentinel in agenthub streams, so callers pass stopWhen() to break on the
   * terminal status frame and never wait for the 15s keep-alive.
   */
  async function collectStream(method, path, opts = {}) {
    const {
      maxFrames = 80, timeoutMs = Math.min(config.maxTimeMs, 15000),
      stopWhen, ...rest
    } = opts
    const { response, url } = await send(method, path, { ...rest, timeoutMs })
    const result = { method, url, status: response.status, frames: [], text: '', headers: Object.fromEntries(response.headers.entries()) }
    if (!response.body) return result
    const reader = response.body.getReader()
    const decoder = new TextDecoder()
    let buffer = ''
    const deadline = Date.now() + timeoutMs
    try {
      while (Date.now() < deadline && result.frames.length < maxFrames) {
        const remaining = Math.max(1, deadline - Date.now())
        const chunk = await Promise.race([
          reader.read(),
          new Promise((resolve) => setTimeout(() => resolve({ done: true, value: undefined, timedOut: true }), remaining)),
        ])
        if (chunk.done) break
        buffer += decoder.decode(chunk.value, { stream: true }).replace(/\r\n/g, '\n')
        let split
        while ((split = buffer.indexOf('\n\n')) !== -1) {
          const block = buffer.slice(0, split)
          buffer = buffer.slice(split + 2)
          if (block.startsWith(':')) continue // keep-alive comment
          const event = /^event:\s*(.+)$/m.exec(block)?.[1]?.trim() || 'message'
          const data = block.split('\n')
            .filter((line) => line.startsWith('data:'))
            .map((line) => line.slice(5).trimStart())
            .join('\n')
          if (!data) continue
          result.text += data + '\n'
          const frame = { event, data }
          result.frames.push(frame)
          if (stopWhen?.(frame, result)) { await reader.cancel().catch(() => {}); return result }
        }
      }
    } finally {
      await reader.cancel().catch(() => {})
    }
    return result
  }

  return {
    request, collectStream,
    getCookie: () => cookie,
    setCookie: (value) => { cookie = value },
    get identity() { return { ...identity } },
  }
}
