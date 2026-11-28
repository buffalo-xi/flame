<template>
  <!-- Generic help drawer: the shell only, no content.
       Geometry matches the endpoint usage panel (UseGuideDrawer.vue): teleport to body + fixed,
       docked to the right edge of the container, no mask, width dragged from the left edge,
       click outside or ESC to close. Two consumers share it:
         HelpButton.vue  the "?" next to a screen title  → one topic
         HelpIndex.vue   the top-bar "?"                 → list, then one topic in place -->
  <teleport to="body">
    <div v-if="visible" ref="panelRef" class="hp-panel" :style="panelStyle">
      <div class="hp-resizer" @mousedown="startResize"></div>
      <div class="hp-header">
        <!-- Back link lives in the header, not at the bottom of the body: it has to stay reachable
             without scrolling a long topic to its end. Only the help center turns it on. -->
        <el-icon
          v-if="back"
          class="hp-back"
          role="button"
          tabindex="0"
          :title="backLabel"
          :aria-label="backLabel"
          @click="emit('back')"
          @keydown.enter.prevent="emit('back')"
        >
          <ArrowLeft />
        </el-icon>
        <span class="hp-title">{{ title }}</span>
        <span class="hp-close" role="button" tabindex="0" :title="t('help.close')" :aria-label="t('help.close')"
              @click="emit('close')" @keydown.enter.prevent="emit('close')">&#xd7;</span>
      </div>
      <div class="hp-body">
        <slot />
      </div>
    </div>
  </teleport>
</template>

<script setup>
import { computed, nextTick, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { ArrowLeft } from '@element-plus/icons-vue'
import { useEscClose } from '../composables/useEscClose'

const props = defineProps({
  visible: { type: Boolean, default: false },
  title: { type: String, default: '' },
  // Container the drawer docks to; null falls back to the whole viewport
  anchorEl: { type: Object, default: null },
  // Set by a consumer that has a level above the current content (the help center's topic list)
  back: { type: Boolean, default: false },
  backLabel: { type: String, default: '' },
})

const emit = defineEmits(['close', 'back'])

const { t } = useI18n()

useEscClose(() => props.visible, () => emit('close'))

// ===== Geometry =====
const panelRef = ref(null)
const panelWidth = ref(460)
const panelRect = ref({ top: 0, left: 0, width: 460, height: 600 })
const panelStyle = computed(() => ({
  top: panelRect.value.top + 'px',
  left: panelRect.value.left + 'px',
  width: panelRect.value.width + 'px',
  height: panelRect.value.height + 'px',
}))

const updateRect = () => {
  const el = props.anchorEl
  const r = el
    ? el.getBoundingClientRect()
    : { top: 0, right: window.innerWidth, height: window.innerHeight }
  // When the container is narrower than the panel, pin to the window's left edge so the whole
  // surface stays visible (the panel is teleported out of the container's clipping)
  const width = Math.min(panelWidth.value, window.innerWidth)
  panelRect.value = {
    top: r.top,
    left: Math.max(0, r.right - width),
    width,
    height: r.height || window.innerHeight,
  }
}

// Drag the left edge to widen (380-760)
const startResize = (e) => {
  const startX = e.clientX
  const startWidth = panelWidth.value
  const onMouseMove = (ev) => {
    const deltaX = startX - ev.clientX
    panelWidth.value = Math.max(380, Math.min(startWidth + deltaX, 760))
    updateRect()
  }
  const onMouseUp = () => {
    document.removeEventListener('mousemove', onMouseMove)
    document.removeEventListener('mouseup', onMouseUp)
  }
  document.addEventListener('mousemove', onMouseMove)
  document.addEventListener('mouseup', onMouseUp)
}

// Click outside closes; overlays and poppers teleported to body do not count as outside
const onOutsideClick = (e) => {
  const target = e.target
  if (target.closest && (target.closest('.el-overlay') || target.closest('.el-popper'))) return
  if (panelRef.value && !panelRef.value.contains(target)) emit('close')
}

watch(
  () => props.visible,
  (v) => {
    if (v) {
      // Bind on the next tick so the opening click does not immediately close the panel
      nextTick(() => {
        document.addEventListener('mousedown', onOutsideClick)
        updateRect()
      })
      window.addEventListener('resize', updateRect)
    } else {
      document.removeEventListener('mousedown', onOutsideClick)
      window.removeEventListener('resize', updateRect)
    }
  }
)

onUnmounted(() => {
  document.removeEventListener('mousedown', onOutsideClick)
  window.removeEventListener('resize', updateRect)
})
</script>

<style scoped>
.hp-panel {
  position: fixed;
  z-index: 3200;
  background: #ffffff;
  border-radius: 6px 0 0 6px;
  box-shadow: -4px 0 20px rgba(0, 0, 0, 0.2);
  display: flex;
  flex-direction: column;
  overflow: hidden;
}

.hp-resizer {
  position: absolute;
  left: 0;
  top: 0;
  bottom: 0;
  width: 6px;
  cursor: col-resize;
  background: transparent;
  z-index: 10;
}

.hp-resizer:hover {
  background: #e0e0e0;
}

.hp-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 6px 16px;
  color: #000;
  gap: 8px;
}

/* The title owns the leftover width, so the close button stays pinned to the right whatever the
   topic name is; a long title truncates instead of pushing it out of the panel. */
.hp-title {
  flex: 1;
  min-width: 0;
  font-weight: 600;
  font-size: 14px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.hp-back {
  flex-shrink: 0;
  font-size: 15px;
  color: #606266;
  cursor: pointer;
  border-radius: 4px;
}

.hp-back:hover,
.hp-back:focus-visible {
  color: #409eff;
  background: rgba(64, 158, 255, 0.1);
  outline: none;
}

.hp-close {
  cursor: pointer;
  padding: 4px;
  border-radius: 4px;
  font-size: 18px;
  line-height: 1;
}

.hp-close:hover {
  background: rgba(0, 0, 0, 0.1);
}

.hp-body {
  flex: 1;
  overflow-y: auto;
  padding: 16px;
  min-height: 0;
  /* Reserve the scrollbar gutter so switching topics does not shift the text sideways */
  scrollbar-gutter: stable;
  /* Bar space stays reserved, the thumb is painted only while the pointer is inside the drawer:
     a permanently drawn bar makes this overlay read like a page (same rule as .et-pre / Chat.vue) */
  scrollbar-width: thin;
  scrollbar-color: transparent transparent;
}

.hp-body::-webkit-scrollbar { width: 6px; height: 6px; }
.hp-body::-webkit-scrollbar-track { background: transparent; }
.hp-body::-webkit-scrollbar-thumb { background: transparent; border-radius: 3px; }
.hp-body:hover { scrollbar-color: #c1c1c1 transparent; }
.hp-body:hover::-webkit-scrollbar-thumb { background: #c1c1c1; }

/* ===== Rendered Markdown =====
   Markdown output is injected with v-html by the consumers, so those elements never receive this
   component's scope attribute; :deep() is what makes the rules apply. Styling lives here (not in the
   consumers) so every topic looks the same. */
.hp-body :deep(.hp-doc) {
  font-size: 13px;
  line-height: 1.75;
  color: #303133;
}

.hp-body :deep(.hp-doc h1) {
  display: none; /* the drawer header already shows this heading as its title */
}

.hp-body :deep(.hp-doc h2) {
  font-size: 13px;
  font-weight: 600;
  margin: 18px 0 6px;
  padding-left: 8px;
  border-left: 3px solid #409eff;
  line-height: 1.3;
}

.hp-body :deep(.hp-doc h3) {
  font-size: 13px;
  font-weight: 600;
  margin: 14px 0 4px;
  color: #303133;
}

.hp-body :deep(.hp-doc p) {
  margin: 0 0 10px;
}

.hp-body :deep(.hp-doc ul),
.hp-body :deep(.hp-doc ol) {
  margin: 0 0 10px;
  padding-left: 20px;
}

.hp-body :deep(.hp-doc li) {
  margin: 2px 0;
}

.hp-body :deep(.hp-doc code) {
  font-family: ui-monospace, Menlo, Consolas, monospace;
  font-size: 12px;
  padding: 1px 5px;
  border-radius: 3px;
  background: #f4f4f5;
  color: #c7254e;
  word-break: break-all;
}

.hp-body :deep(.hp-doc pre) {
  margin: 0 0 10px;
  padding: 10px 12px;
  background: #f6f8fa;
  border: 1px solid #ebeef5;
  border-radius: 4px;
  overflow: auto;
  /* Same rule as the panel body. Only the standard properties: a ::-webkit-scrollbar rule behind
     :deep() would compile to a selector nobody notices is wrong if it ever stops matching. */
  scrollbar-width: thin;
  scrollbar-color: transparent transparent;
}

.hp-body :deep(.hp-doc pre:hover) {
  scrollbar-color: #c1c1c1 transparent;
}

.hp-body :deep(.hp-doc pre code) {
  background: transparent;
  color: #303133;
  padding: 0;
}

.hp-body :deep(.hp-doc table) {
  width: 100%;
  margin: 0 0 10px;
  border-collapse: collapse;
  font-size: 12px;
}

.hp-body :deep(.hp-doc th),
.hp-body :deep(.hp-doc td) {
  border: 1px solid #ebeef5;
  padding: 5px 8px;
  text-align: left;
  vertical-align: top;
}

.hp-body :deep(.hp-doc th) {
  background: #fafafa;
  font-weight: 600;
}

.hp-body :deep(.hp-doc blockquote) {
  margin: 0 0 10px;
  padding: 6px 12px;
  background: #fafafa;
  border-left: 3px solid #e4e7ed;
  color: #606266;
}

.hp-body :deep(.hp-doc a) {
  color: #409eff;
}

.hp-body :deep(.hp-doc strong) {
  color: #303133;
}
</style>
