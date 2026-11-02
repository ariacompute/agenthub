import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { saveBlob, streamTask, subscribeTaskEvents } from '../src/lib/api.js'

// The streaming halves of the client are the ones the console cannot fake at
// runtime: a task's live frames, the broker feed for a finished task, and the
// download of an artifact. Each one is exercised against a hand-written stub of
// the browser object it talks to, because the suite runs without jsdom.

const jsonResponse = (payload, status = 200) => ({
  ok: status >= 200 && status < 300,
  status,
  text: async () => JSON.stringify(payload),
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

describe('request edge cases', () => {
  it('reads a 204 as "no payload"', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => ({ ok: true, status: 204, text: async () => '' })))
    const api = await import('../src/lib/api.js')
    await expect(api.deleteResource('r1')).resolves.toBeNull()
  })

  it('survives a body that is not JSON', async () => {
    const logged = vi.spyOn(console, 'error').mockImplementation(() => {})
    vi.stubGlobal('fetch', vi.fn(async () => ({
      ok: true,
      status: 200,
      text: async () => 'not json at all',
    })))
    const api = await import('../src/lib/api.js')
    await expect(api.healthz()).resolves.toBeNull()
    expect(logged).toHaveBeenCalled()
  })

  it('invents internal_error when the failure carries no code', async () => {
    vi.spyOn(console, 'error').mockImplementation(() => {})
    vi.stubGlobal('fetch', vi.fn(async () => ({
      ok: false,
      status: 502,
      text: async () => 'gateway said no',
    })))
    const api = await import('../src/lib/api.js')
    await expect(api.healthz()).rejects.toMatchObject({ code: 'internal_error', status: 502 })
  })
})

// readFrames builds a body whose getReader walks the frames given, then ends.
function readFrames(frames) {
  let i = 0
  const cancel = vi.fn(async () => {})
  return {
    cancel,
    getReader: () => ({
      cancel,
      read: async () => {
        if (i >= frames.length) return { value: undefined, done: true }
        const value = new TextEncoder().encode(frames[i])
        i += 1
        return { value, done: false }
      },
    }),
  }
}

describe('streamTask', () => {
  it('delivers every frame to the caller', async () => {
    const body = readFrames([
      'event: task_status_update\ndata: {"result":{"kind":"status"}}\n\n',
      'event: task_artifact_update\ndata: {"result":{"kind":"artifact"}}\n\n',
    ])
    vi.stubGlobal('fetch', vi.fn(async () => ({ ok: true, status: 200, body })))

    const seen = []
    await streamTask({ resourceId: 'r1', text: 'go' }, (frame) => seen.push(frame))

    expect(seen).toEqual([
      { event: 'task_status_update', data: { kind: 'status' } },
      { event: 'task_artifact_update', data: { kind: 'artifact' } },
    ])
  })

  it('unwraps an error frame instead of the envelope', async () => {
    const body = readFrames(['event: error\ndata: {"error":{"code":"resource_not_found"}}\n\n'])
    vi.stubGlobal('fetch', vi.fn(async () => ({ ok: true, status: 200, body })))

    const seen = []
    await streamTask({ resourceId: 'r1', text: 'go' }, (frame) => seen.push(frame))
    expect(seen[0].data).toEqual({ code: 'resource_not_found' })
  })

  it('logs a frame that is not JSON and keeps reading', async () => {
    const logged = vi.spyOn(console, 'error').mockImplementation(() => {})
    const body = readFrames([
      'event: task_status_update\ndata: {not json\n\n',
      'event: task_status_update\ndata: {"result":"ok"}\n\n',
    ])
    vi.stubGlobal('fetch', vi.fn(async () => ({ ok: true, status: 200, body })))

    const seen = []
    await streamTask({}, (frame) => seen.push(frame))
    expect(seen).toEqual([{ event: 'task_status_update', data: 'ok' }])
    expect(logged).toHaveBeenCalled()
  })

  it('skips a frame with no data line and passes an unenveloped payload through', async () => {
    // An event-only frame (a keep-alive, a comment) carries nothing to hand over;
    // a payload that is neither result nor error must reach the caller unchanged,
    // otherwise a custom frame would be swallowed as undefined.
    const body = readFrames([
      'event: task_status_update\n\n',
      'event: message\ndata: {"kind":"message","text":"raw"}\n\n',
    ])
    vi.stubGlobal('fetch', vi.fn(async () => ({ ok: true, status: 200, body })))

    const seen = []
    await streamTask({}, (frame) => seen.push(frame))
    expect(seen).toEqual([{ event: 'message', data: { kind: 'message', text: 'raw' } }])
  })

  it('reports stream_unavailable when the server refuses to stream', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => jsonResponse({ code: 'internal_error' }, 500)))
    await expect(streamTask({}, () => {})).rejects.toMatchObject({ code: 'stream_unavailable' })
  })

  it('reports stream_unavailable when the body is missing', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => ({ ok: true, status: 200, body: null })))
    await expect(streamTask({}, () => {})).rejects.toMatchObject({ code: 'stream_unavailable' })
  })

  it('cancels the reader when the caller aborted', async () => {
    const body = readFrames(['event: task_status_update\ndata: {"result":1}\n\n'])
    vi.stubGlobal('fetch', vi.fn(async () => ({ ok: true, status: 200, body })))

    const seen = []
    await streamTask({}, (frame) => seen.push(frame), { aborted: true })

    // Nothing is delivered after the abort, and the stream is released.
    expect(seen).toEqual([])
    expect(body.cancel).toHaveBeenCalled()
  })
})

describe('subscribeTaskEvents', () => {
  function stubEventSource() {
    const sources = []
    class FakeEventSource {
      constructor(url) {
        this.url = url
        this.handlers = {}
        this.close = vi.fn()
        sources.push(this)
      }

      addEventListener(name, fn) {
        this.handlers[name] = fn
      }
    }
    vi.stubGlobal('EventSource', FakeEventSource)
    return sources
  }

  it('subscribes to all four broker frames', () => {
    const sources = stubEventSource()
    subscribeTaskEvents('task-1', () => {})

    expect(sources).toHaveLength(1)
    expect(sources[0].url).toBe('/api/tasks/task-1/events')
    expect(Object.keys(sources[0].handlers).sort()).toEqual([
      'message', 'task_artifact_update', 'task_span_update', 'task_status_update',
    ])
  })

  it('parses each frame and closes the stream on dispose', () => {
    const sources = stubEventSource()
    const seen = []
    const dispose = subscribeTaskEvents('task/1', (frame) => seen.push(frame))

    sources[0].handlers.task_status_update({ data: '{"state":"working"}' })
    expect(seen).toEqual([{ event: 'task_status_update', data: { state: 'working' } }])
    // The id is encoded, so a slash never becomes a new path segment.
    expect(sources[0].url).toBe('/api/tasks/task%2F1/events')

    dispose()
    expect(sources[0].close).toHaveBeenCalled()
  })

  it('logs a frame the broker mangled instead of throwing', () => {
    const logged = vi.spyOn(console, 'error').mockImplementation(() => {})
    const sources = stubEventSource()
    const seen = []
    subscribeTaskEvents('task-1', (frame) => seen.push(frame))

    expect(() => sources[0].handlers.message({ data: '{oops' })).not.toThrow()
    expect(seen).toEqual([])
    expect(logged).toHaveBeenCalled()
  })
})

describe('saveBlob', () => {
  it('clicks a temporary link and revokes the object URL', () => {
    const link = { href: '', download: '', click: vi.fn(), remove: vi.fn() }
    const appendChild = vi.fn()
    vi.stubGlobal('document', { createElement: () => link, body: { appendChild } })
    const createObjectURL = vi.fn(() => 'blob:fake')
    const revokeObjectURL = vi.fn()
    vi.stubGlobal('URL', { createObjectURL, revokeObjectURL })

    const blob = { size: 3 }
    saveBlob(blob, 'trace.json')

    expect(createObjectURL).toHaveBeenCalledWith(blob)
    expect(link.href).toBe('blob:fake')
    expect(link.download).toBe('trace.json')
    expect(appendChild).toHaveBeenCalledWith(link)
    expect(link.click).toHaveBeenCalled()
    expect(link.remove).toHaveBeenCalled()
    // The URL is released after the click, or the blob leaks for the page's life.
    expect(revokeObjectURL).toHaveBeenCalledWith('blob:fake')
  })
})
