/**
 * ESC support for hand-drawn windows (panels / drawers / popovers rendered via teleport + fixed).
 *
 * Element Plus handles ESC for el-dialog / el-drawer / ElMessageBox, but this project draws a lot of
 * its own windows (.settings-panel / .expose-panel / .ug-panel / .hp-panel …) with nothing but a × in
 * the corner, leaving keyboard users with the mouse as their only way out.
 *
 * A stack is used instead of one document listener per component: when the agent settings panel, a
 * tool-grant sidebar and a variable picker are all open, one ESC must close only the topmost layer,
 * otherwise a single press wipes all three.
 *
 * Usage:
 *   useEscClose(() => props.visible, () => emit('close'))
 *   useEscClose(grantPickerVisible, () => { grantPickerVisible.value = false })
 *   useEscClose(visible, close, { skipWhenEditable: true })   // the panel has an inline rename/editor
 *
 * Lives at src/composables (not src/tenant/composables) because both subtrees use it and
 * src/platform/** must not depend on src/tenant/**. The old path re-exports this module, so the
 * stack stays a single instance.
 */

import { onBeforeUnmount, unref, watch } from 'vue'

// Windows currently open, in opening order; only the top of the stack answers ESC
const stack = []
let bound = false

const isEditable = (el) => {
  if (!el || el.nodeType !== 1) return false
  return el.isContentEditable || ['INPUT', 'TEXTAREA', 'SELECT'].includes(el.tagName)
}

// Bubble-phase listener: element-level @keydown.esc handlers and el-select's own ESC handling run
// first, and if they stopPropagation (el-select does) this handler never sees the event.
const onKeydown = (e) => {
  if (e.key !== 'Escape' && e.key !== 'Esc') return
  // In an IME composition ESC cancels the candidate window; do not close the panel as a side effect
  if (e.isComposing) return
  const top = stack[stack.length - 1]
  if (!top) return
  if (!top.enabled()) return
  // With an ElMessageBox on top ("unsaved changes, close anyway?") ESC belongs to it: otherwise it
  // closes, the catch returns, and we pop the confirmation again — every press adds one more.
  if (document.querySelector('.el-message-box')) return
  // While typing, ESC belongs to the input (cancel the inline edit) instead of the whole panel
  if (top.skipWhenEditable && isEditable(e.target)) return
  top.close(e)
}

const bind = () => {
  if (bound) return
  document.addEventListener('keydown', onKeydown)
  bound = true
}

export function useEscClose(open, close, options = {}) {
  const { skipWhenEditable = false, enabled = () => true } = options
  const isOpen = () => (typeof open === 'function' ? open() : unref(open))
  const isEnabled = () => (typeof enabled === 'function' ? enabled() : unref(enabled))

  let token = null
  const push = () => {
    if (token) return
    token = { close, skipWhenEditable, enabled: isEnabled }
    stack.push(token)
    bind()
  }
  const pop = () => {
    if (!token) return
    const i = stack.indexOf(token)
    if (i >= 0) stack.splice(i, 1)
    token = null
  }

  watch(() => isOpen() === true, (v) => (v ? push() : pop()), { immediate: true })
  // A panel ripped out by a parent v-if must leave the stack too, or the top entry stays a dead window
  onBeforeUnmount(pop)
}
