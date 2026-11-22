<template>
  <teleport to="body">
    <div v-if="visible" class="history-panel" :style="panelStyle">
      <div class="panel-resizer" @mousedown="startResize"></div>
      <div class="panel-header">
        <span class="panel-title">{{ title }}</span>
        <span class="close-icon" @click="$emit('close')">&#xd7;</span>
      </div>
      <div class="panel-body">
        <slot />
      </div>
    </div>
  </teleport>
</template>

<script setup>
/**
 * Detail panel of the workflow / agent history pages.
 *
 * It is the agent settings window (agents/AgentSettingsPanel.vue) applied to a list page: fixed, anchored to the
 * list, flush with the content area's right edge, same height as the list, same header/body metrics. Kept as its
 * own shell instead of reusing settings/SidePanel.vue because that one floats with a 12px gap around the whole
 * window, while an editor's settings panel sits flush inside the content area.
 *
 * Closes on ESC (through useEscClose, so stacked panels close one at a time), on the header "x" and on a click
 * outside, and can be widened by dragging its left edge (same range as the settings panel).
 */
import { ref, computed, watch, onUnmounted } from 'vue'
import { useEscClose } from '../composables/useEscClose'

const props = defineProps({
  visible: { type: Boolean, default: false },
  title: { type: String, default: '' },
  // The element the panel is aligned to: same top, same height, right edge of its box.
  anchor: { type: Object, default: null },
  // Narrower than the first cut (620): the settings window this mirrors is 420, and the detail content is a
  // handful of label/value rows plus a four-column node table, which still fits without horizontal scroll.
  width: { type: Number, default: 480 },
  minWidth: { type: Number, default: 360 },
  maxWidth: { type: Number, default: 900 }
})

const emit = defineEmits(['close'])

useEscClose(() => props.visible, () => emit('close'))

// null until the user drags the edge: each opening starts at the default width, like the settings panel
const draggedWidth = ref(null)
const baseRect = ref({ top: 0, right: 0, width: 0, height: 0 })

const updateBase = () => {
  const el = props.anchor
  if (!el || !el.getBoundingClientRect) return
  const r = el.getBoundingClientRect()
  const next = { top: r.top, right: r.right, width: r.width, height: r.height }
  const cur = baseRect.value
  if (cur.top === next.top && cur.right === next.right && cur.width === next.width && cur.height === next.height) return
  baseRect.value = next
}

const panelStyle = computed(() => {
  const base = baseRect.value
  // Never wider than the list it shadows, and never pushed off the left edge of the window
  const w = Math.max(props.minWidth, Math.min(draggedWidth.value ?? props.width, base.width || props.width))
  return {
    top: base.top + 'px',
    left: Math.max(0, base.right - w) + 'px',
    width: w + 'px',
    height: base.height + 'px'
  }
})

// Left-edge drag: drag left to widen (same bounds as the settings panel, clamped to the list width)
const startResize = (e) => {
  const panelEl = e.currentTarget ? e.currentTarget.parentElement : null
  const startWidth = panelEl ? panelEl.getBoundingClientRect().width : (draggedWidth.value ?? props.width)
  const startX = e.clientX
  draggedWidth.value = Math.round(startWidth)
  const onMove = (ev) => {
    const max = baseRect.value.width ? Math.min(props.maxWidth, baseRect.value.width) : props.maxWidth
    draggedWidth.value = Math.max(props.minWidth, Math.min(startWidth + (startX - ev.clientX), max))
  }
  const onUp = () => {
    document.removeEventListener('mousemove', onMove)
    document.removeEventListener('mouseup', onUp)
  }
  document.addEventListener('mousemove', onMove)
  document.addEventListener('mouseup', onUp)
}

// Click outside closes it, except for Element Plus overlays (tooltips of the nodes table) and the panel itself
const onOutsideClick = (e) => {
  const t = e.target
  if (!t || !t.closest) return
  if (t.closest('.el-popper') || t.closest('.el-overlay') || t.closest('.el-select-dropdown') || t.closest('.el-message-box')) return
  if (t.closest('.history-panel')) return
  emit('close')
}

watch(() => props.visible, (visible) => {
  if (visible) {
    draggedWidth.value = null
    updateBase()
    window.addEventListener('resize', updateBase)
    // Capture the list's own scrolling too: the panel is fixed, its anchor is not
    window.addEventListener('scroll', updateBase, true)
    document.addEventListener('mousedown', onOutsideClick)
  } else {
    window.removeEventListener('resize', updateBase)
    window.removeEventListener('scroll', updateBase, true)
    document.removeEventListener('mousedown', onOutsideClick)
  }
}, { immediate: true })

onUnmounted(() => {
  window.removeEventListener('resize', updateBase)
  window.removeEventListener('scroll', updateBase, true)
  document.removeEventListener('mousedown', onOutsideClick)
})
</script>

<style scoped>
/* Same surface as the agent settings panel. z-index 2000 keeps it above the page (which has no stacking
   context above ~1000) while leaving Element Plus poppers - the table's overflow tooltips, allocated from 2000
   upwards - above the panel. */
.history-panel {
  position: fixed;
  z-index: 2000;
  display: flex;
  flex-direction: column;
  overflow: hidden;
  background: #ffffff;
  border-radius: 6px 0 0 6px;
  box-shadow: -4px 0 20px rgba(0, 0, 0, 0.2);
}

.panel-resizer {
  position: absolute;
  left: 0;
  top: 0;
  bottom: 0;
  width: 6px;
  z-index: 10;
  cursor: col-resize;
  background: transparent;
}

.panel-resizer:hover {
  background: #e0e0e0;
}

.panel-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 12px;
  flex-shrink: 0;
  padding: 6px 16px;
  color: black;
}

.panel-title {
  font-weight: 600;
  font-size: 14px;
}

.close-icon {
  cursor: pointer;
  padding: 4px;
  border-radius: 4px;
  font-size: 18px;
  line-height: 1;
}

.close-icon:hover {
  background: rgba(0, 0, 0, 0.1);
}

.panel-body {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  padding: 16px;
  scrollbar-gutter: stable;
  scrollbar-width: thin;
  scrollbar-color: transparent transparent;
}

.panel-body:hover {
  scrollbar-color: #c0c4cc transparent;
}

.panel-body::-webkit-scrollbar {
  width: 8px;
}

.panel-body::-webkit-scrollbar-thumb {
  background: transparent;
  border-radius: 4px;
}

.panel-body:hover::-webkit-scrollbar-thumb {
  background: #c0c4cc;
}
</style>
