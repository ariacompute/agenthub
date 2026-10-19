// Dismissing an overlay: Escape handling and "you have unsaved edits".
//
// Every drawer, dialog and menu in the console closes the same way — Escape,
// the backdrop, or an explicit cancel — and a form that was edited but not
// saved asks before it disappears. Two rules make that safe to share:
//
//   1. Escape is handled by the *topmost* active overlay only. When the unsaved
//      prompt is open it sits above its drawer, so Escape cancels the prompt
//      and leaves the drawer alone. A stack, not stopPropagation, because the
//      listener is installed once and the ordering is registration order.
//   2. "Dirty" is decided by comparing a snapshot taken when the overlay was
//      opened, not by watching every keystroke. Editing a field back to its
//      original value is therefore not an unsaved change.
import { onMounted, onUnmounted } from 'vue'

/** Snapshot of an editable object, taken when its overlay opens. */
export function snapshot(value) {
  return JSON.stringify(value ?? null)
}

/** True when the current value differs from the snapshot it started from. */
export function isDirty(baseline, value) {
  if (baseline == null) return false
  return baseline !== snapshot(value)
}

/** Legacy browsers report the key as Esc; everything else says Escape. */
export function isEscapeKey(event) {
  const key = event?.key ?? event?.code
  return key === 'Escape' || key === 'Esc'
}

/**
 * The topmost layer that still wants to handle Escape, last registered first.
 * Pure so it can be tested without a DOM: `layers` is `[{ isActive, handler }]`.
 */
export function topmostActive(layers) {
  for (let i = layers.length - 1; i >= 0; i -= 1) {
    const layer = layers[i]
    if (layer && layer.isActive()) return layer
  }
  return null
}

// One listener for the whole console. It is installed when the first overlay
// registers and removed when the last one leaves, so an idle page does not
// keep a global handler alive.
const layers = []
let listening = false

function onKeydown(event) {
  if (!isEscapeKey(event)) return
  const layer = topmostActive(layers)
  if (!layer) return
  layer.handler()
}

function startListening() {
  if (listening || typeof document === 'undefined') return
  document.addEventListener('keydown', onKeydown)
  listening = true
}

function stopListening() {
  if (!listening || typeof document === 'undefined') return
  document.removeEventListener('keydown', onKeydown)
  listening = false
}

/**
 * Runs `handler` when Escape is pressed while this overlay is the topmost one.
 * `isActive` is a ref or a function returning a boolean.
 */
export function useEscape(isActive, handler) {
  const active = () => {
    if (typeof isActive === 'function') return !!isActive()
    if (isActive && typeof isActive === 'object' && 'value' in isActive) return !!isActive.value
    return !!isActive
  }
  const layer = { isActive: active, handler }

  onMounted(() => {
    layers.push(layer)
    startListening()
  })
  onUnmounted(() => {
    const index = layers.indexOf(layer)
    if (index >= 0) layers.splice(index, 1)
    if (layers.length === 0) stopListening()
  })
}

/** Test seam: the stack the composable is currently working with. */
export function escapeLayers() {
  return layers
}
