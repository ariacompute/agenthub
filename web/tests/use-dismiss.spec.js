import { describe, expect, it } from 'vitest'
import { isDirty, isEscapeKey, snapshot, topmostActive } from '../src/lib/use-dismiss.js'

// The rules behind "Escape closes it, and asks first when there are edits".
// They are pure so they can be asserted without a DOM: the console has no
// component test environment, and the behaviour that matters here is a
// decision, not a render.

describe('snapshot and dirtiness', () => {
  it('treats an untouched form as clean', () => {
    const form = { name: 'router', kind: 'model', tags: [] }
    const baseline = snapshot(form)
    expect(isDirty(baseline, form)).toBe(false)
  })

  it('treats a nested edit as dirty', () => {
    const form = { kind: 'model', kindConfig: { model: { modelId: 'a' } } }
    const baseline = snapshot(form)
    form.kindConfig.model.modelId = 'b'
    expect(isDirty(baseline, form)).toBe(true)
  })

  // Editing a field back is not an unsaved change, which is the whole reason
  // this compares snapshots instead of counting keystrokes.
  it('forgets an edit that was undone', () => {
    const form = { name: 'router' }
    const baseline = snapshot(form)
    form.name = 'gateway'
    expect(isDirty(baseline, form)).toBe(true)
    form.name = 'router'
    expect(isDirty(baseline, form)).toBe(false)
  })

  it('never asks before an overlay has been opened', () => {
    expect(snapshot(undefined)).toBe('null')
    expect(isDirty(null, { name: 'x' })).toBe(false)
    expect(isDirty(undefined, { name: 'x' })).toBe(false)
  })
})

describe('escape keys', () => {
  it('accepts both spellings', () => {
    expect(isEscapeKey({ key: 'Escape' })).toBe(true)
    expect(isEscapeKey({ key: 'Esc' })).toBe(true)
    expect(isEscapeKey({ code: 'Escape' })).toBe(true)
  })

  it('ignores every other key', () => {
    expect(isEscapeKey({ key: 'Enter' })).toBe(false)
    expect(isEscapeKey({ key: 'a' })).toBe(false)
    expect(isEscapeKey({})).toBe(false)
    expect(isEscapeKey(undefined)).toBe(false)
  })
})

describe('layer stack', () => {
  const layer = (active, name) => ({ isActive: () => active, handler: () => name, name })

  it('picks nothing when no layer is open', () => {
    expect(topmostActive([])).toBeNull()
    expect(topmostActive([layer(false, 'drawer')])).toBeNull()
  })

  // The unsaved prompt is registered after its drawer, so it is the layer that
  // answers Escape: cancelling the prompt must not close the drawer too.
  it('lets the last registered open layer win', () => {
    const layers = [layer(true, 'drawer'), layer(true, 'prompt')]
    expect(topmostActive(layers).name).toBe('prompt')
  })

  it('falls through to the drawer once the prompt is gone', () => {
    const layers = [layer(true, 'drawer'), layer(false, 'prompt')]
    expect(topmostActive(layers).name).toBe('drawer')
  })

  it('ignores a closed layer above an open one', () => {
    const layers = [layer(false, 'drawer'), layer(false, 'prompt')]
    expect(topmostActive(layers)).toBeNull()
  })
})
