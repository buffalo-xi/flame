<template>
  <teleport to="body">
    <div v-if="visible" class="settings-panel" :class="panelClass" :style="panelStyle">
      <div class="panel-resizer" @mousedown="$emit('resize', $event)"></div>
      <div class="panel-header">
        <span class="panel-title">{{ title }}</span>
        <span class="close-icon" @click="$emit('close')">&#xd7;</span>
      </div>
      <div v-loading="loading" class="panel-body">
        <slot />
      </div>
      <div v-if="$slots.footer" class="panel-footer">
        <slot name="footer" />
      </div>
    </div>
  </teleport>
</template>

<script setup>
/**
 * 设置页的右侧侧边面板外壳：只负责「长什么样」与「怎么关」。
 * 定位 / 拖宽 / 外部点击由 useSidePanel 算好，通过 panelStyle 传进来。
 * 内容样式（.settings-field / .editor-tip 等）留在使用方 —— slot 内容是父组件渲染的。
 */

import { useEscClose } from '../../composables/useEscClose'


const props = defineProps({
  visible: { type: Boolean, default: false },
  title: { type: String, default: '' },
  // 由 useSidePanel 的 styleFor(w) 计算：top / left / width / height
  panelStyle: { type: Object, default: () => ({}) },
  loading: { type: Boolean, default: false },
  // 使用方的定制类名。必须用 prop 传：组件根节点是 Teleport，class 透传落不到内层 div 上，
  // 使用方写的 `.scope-picker :deep(.panel-body)` 之类选择器会整段落空。
  panelClass: { type: String, default: '' }
})

const emit = defineEmits(['close', 'resize'])

// 设置页所有右侧面板共用这一层外壳：ESC 在这里关一次，各 tab 不用各写一遍
useEscClose(() => props.visible, () => emit('close'))
</script>

<style scoped>
.settings-panel {
  position: fixed;
  /* 高于 Element Plus 默认 2000 档浮层；面板内下拉需显式抬 z-index（settings-select-popper） */
  z-index: 3200;
  background: #ffffff;
  border-radius: 6px;
  box-shadow: -4px 0 20px rgba(0, 0, 0, 0.2);
  display: flex;
  flex-direction: column;
  overflow: hidden;
}

.panel-resizer {
  position: absolute;
  left: 0;
  top: 0;
  bottom: 0;
  width: 6px;
  cursor: col-resize;
  background: transparent;
  z-index: 10;
}

.panel-resizer:hover {
  background: #e0e0e0;
}

.panel-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 6px 16px;
  color: black;
  gap: 12px;
  flex-shrink: 0;
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
  overflow-y: auto;
  padding: 16px;
  min-height: 0;
  scrollbar-gutter: stable;
}

.panel-footer {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  padding: 12px 16px;
  border-top: 1px solid #f0f0f0;
  background: #fff;
  flex-shrink: 0;
}
</style>
