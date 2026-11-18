<template>
  <div class="start-node" :class="{ 'selected': isSelected }" ref="nodeRef" @click="handleNodeClick">
    <div class="node-content">
      <div class="icon-wrapper icon-start"><el-icon><Timer /></el-icon></div>
      <div class="node-label">{{ data.label }}</div>
    </div>
    <Handle type="source" position="right" :id="'source' + id + 'right'" class="custom-handle" />
    <Handle type="source" position="bottom" :id="'source' + id + 'bottom'" class="custom-handle" />
    <Handle type="source" position="left" :id="'source' + id + 'left'" class="custom-handle" />
  </div>
</template>

<script setup>
import { ref, computed, nextTick, onMounted, onUnmounted, watch, inject } from 'vue'
import { Timer } from '@element-plus/icons-vue'
import { Handle, useVueFlow } from '@vue-flow/core'

const props = defineProps({
  data: {
    type: Object,
    default: () => ({ label: '开始' })
  },
  id: {
    type: String,
    required: true
  },
  getNodeById: {
    type: Function,
    default: null
  }
})

const emit = defineEmits(['handleClick'])
const injectedSelectedNode = inject('selectedNode', null)

const nodeRef = ref(null)
const isSelected = ref(false)

if (injectedSelectedNode) {
  watch(() => injectedSelectedNode.value, (newSelectedNode) => {
    isSelected.value = newSelectedNode?.id === props.id
  }, { immediate: true, deep: true })
}

const handleNodeClick = (event) => {
  const handleElement = event.target.closest('.custom-handle')
  if (handleElement) {
    event.stopPropagation()    
    const handleId = handleElement.getAttribute('data-handleid') || handleElement.id
    emit('handleClick', props.id, handleId, event)
  }
}
</script>

<style scoped>
.start-node {
  width: auto;
  min-width: 100px;
  height: 32px;
  border: 1px solid #00704A;
  border-radius: 16px;
  background-color: #fff;
  display: flex;
  align-items: center;
  justify-content: flex-start;
  cursor: pointer;
  padding: 0 10px;
  position: relative;
}

.start-node:hover {
  box-shadow: 0 2px 12px rgba(0, 112, 74, 0.3);
}

.start-node.selected {
  box-shadow: 0 0 0 1px #409eff;
}

.start-node.selected:hover {
  box-shadow: 0 0 0 1px #409eff, 0 2px 12px rgba(64, 158, 255, 0.3);
}

.node-content {
  display: flex;
  flex-direction: row;
  align-items: center;
  gap: 8px;
  flex: 1;
  padding-right: 5px;
}

.node-label {
  font-size: 11px;
  font-weight: 700;
  color: #333;
  white-space: nowrap;
}

.icon-wrapper {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 20px;
  height: 20px;
  border-radius: 8px;
  margin-right: 4px;
}

.icon-start { background-color: #00704A; }

.icon-start .el-icon {
  font-size: 16px;
  color: #ffffff;
}

.custom-handle {
  width: 16px;
  height: 16px;
  border-radius: 50%;
  background-color: #00704A;
  display: flex;
  align-items: center;
  justify-content: center;
  color: white;
  font-size: 12px;
  font-weight: bold;
  opacity: 0;
  z-index: 10;
  cursor: pointer;
}

.start-node:hover .custom-handle {
  opacity: 1;
}

.custom-handle::before {
  content: '+';
}

.handle-hidden {
  display: none;
}
</style>