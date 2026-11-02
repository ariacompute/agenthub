import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createRenderer, defineComponent, ref } from 'vue'
import { escapeLayers, useEscape } from '../src/lib/use-dismiss.js'

// The pure rules live in use-dismiss.spec.js. This file drives the other half:
// the one global keydown listener, its install/removal around the overlay's
// lifetime, and the stack order. The console has no jsdom, so the renderer is a
// hand-rolled one and the document is a stub — that is enough to exercise the
// lifecycle hooks, which is what the composable actually owns.

const renderer = createRenderer({
  createElement: (tag) => ({ tag, children: [] }),
  createText: (text) => ({ text }),
  createComment: (text) => ({ text }),
  setText: () => {},
  setElementText: () => {},
  insert: () => {},
  remove: () => {},
  parentNode: () => null,
  nextSibling: () => null,
  patchProp: () => {},
})

function fakeDocument() {
  const handlers = []
  return {
    handlers,
    addEventListener: vi.fn((type, fn) => {
      if (type === 'keydown') handlers.push(fn)
    }),
    removeEventListener: vi.fn((type, fn) => {
      if (type !== 'keydown') return
      const index = handlers.indexOf(fn)
      if (index >= 0) handlers.splice(index, 1)
    }),
  }
}

// mountOverlay mounts one component that registers an overlay and returns the
// handle plus the unmount function, so a test can close it at the moment it
// cares about.
function mountOverlay(isActive, handler) {
  const Overlay = defineComponent({
    setup() {
      useEscape(isActive, handler)
      return () => null
    },
  })
  const app = renderer.createApp(Overlay)
  app.mount({ children: [] })
  return app
}

let doc

beforeEach(() => {
  doc = fakeDocument()
  vi.stubGlobal('document', doc)
})

afterEach(() => {
  // Leave the module-level stack empty for the next file in the same process.
  while (escapeLayers().length > 0) escapeLayers().pop()
  vi.unstubAllGlobals()
})

describe('the global Escape listener', () => {
  it('installs one keydown listener for the first overlay only', () => {
    const first = mountOverlay(ref(true), vi.fn())
    const second = mountOverlay(ref(true), vi.fn())

    expect(escapeLayers()).toHaveLength(2)
    // A second overlay must not add a second listener: the stack decides who
    // answers, not the number of handlers on the document.
    expect(doc.addEventListener).toHaveBeenCalledTimes(1)
    expect(doc.handlers).toHaveLength(1)

    first.unmount()
    second.unmount()
  })

  it('runs the topmost open handler on Escape', () => {
    const drawer = vi.fn()
    const prompt = vi.fn()
    const app = mountOverlay(ref(true), drawer)
    // The prompt is registered after the drawer, so it is the one that answers.
    const inner = defineComponent({
      setup() {
        useEscape(ref(true), prompt)
        return () => null
      },
    })
    const promptApp = renderer.createApp(inner)
    promptApp.mount({ children: [] })

    doc.handlers[0]({ key: 'Escape' })

    expect(prompt).toHaveBeenCalledTimes(1)
    expect(drawer).not.toHaveBeenCalled()

    app.unmount()
    promptApp.unmount()
  })

  it('ignores every other key', () => {
    const handler = vi.fn()
    const app = mountOverlay(ref(true), handler)

    doc.handlers[0]({ key: 'Enter' })
    doc.handlers[0]({ key: 'a' })
    expect(handler).not.toHaveBeenCalled()

    doc.handlers[0]({ key: 'Esc' })
    expect(handler).toHaveBeenCalledTimes(1)

    app.unmount()
  })

  it('does nothing while every overlay is closed', () => {
    const handler = vi.fn()
    const app = mountOverlay(ref(false), handler)

    doc.handlers[0]({ key: 'Escape' })
    expect(handler).not.toHaveBeenCalled()

    app.unmount()
  })

  it('removes the listener when the last overlay unmounts', () => {
    const first = mountOverlay(ref(true), vi.fn())
    const second = mountOverlay(ref(true), vi.fn())

    first.unmount()
    expect(doc.removeEventListener).not.toHaveBeenCalled()
    expect(escapeLayers()).toHaveLength(1)

    second.unmount()
    expect(doc.removeEventListener).toHaveBeenCalledTimes(1)
    expect(doc.handlers).toHaveLength(0)
    expect(escapeLayers()).toHaveLength(0)
  })
})

describe('the three ways to say "active"', () => {
  it('reads a ref', () => {
    const handler = vi.fn()
    const active = ref(false)
    const app = mountOverlay(active, handler)

    doc.handlers[0]({ key: 'Escape' })
    expect(handler).not.toHaveBeenCalled()

    active.value = true
    doc.handlers[0]({ key: 'Escape' })
    expect(handler).toHaveBeenCalledTimes(1)

    app.unmount()
  })

  it('reads a function, including a falsey one', () => {
    const handler = vi.fn()
    let open = false
    const app = mountOverlay(() => open, handler)

    doc.handlers[0]({ key: 'Escape' })
    expect(handler).not.toHaveBeenCalled()

    open = true
    doc.handlers[0]({ key: 'Escape' })
    expect(handler).toHaveBeenCalledTimes(1)

    app.unmount()
  })

  it('reads a plain boolean', () => {
    const handler = vi.fn()
    const app = mountOverlay(true, handler)

    doc.handlers[0]({ key: 'Escape' })
    expect(handler).toHaveBeenCalledTimes(1)

    app.unmount()
  })
})

describe('without a document', () => {
  it('mounts and unmounts without touching a global it does not have', () => {
    vi.stubGlobal('document', undefined)
    const handler = vi.fn()
    const app = mountOverlay(ref(true), handler)

    // The overlay is still on the stack; only the listener is skipped.
    expect(escapeLayers()).toHaveLength(1)
    app.unmount()
    expect(escapeLayers()).toHaveLength(0)
  })
})
