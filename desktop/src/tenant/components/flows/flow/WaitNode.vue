<template>
  <div class="wait-node" :class="{ 'selected': isSelected }" ref="nodeRef" @click="handleNodeClick">
    <div class="node-content">
      <div class="node-header">
        <div class="icon-wrapper icon-wait"><el-icon><Link /></el-icon></div>
        <div class="node-label">{{ data.label }}</div>
        <el-dropdown trigger="hover" placement="right">
          <span class="more-icon-wrapper"><el-icon class="more-icon"><MoreFilled /></el-icon></span>
          <template #dropdown>
            <el-dropdown-menu>
              <el-dropdown-item @click="deleteNode">
                <el-icon><Delete /></el-icon>
                <span>删除节点</span>
              </el-dropdown-item>
              <BreakpointMenuItem :id="id" />
            </el-dropdown-menu>
          </template>
        </el-dropdown>
      </div>
      <div class="fork-info">
        <span class="fork-label">并发组:</span>
        <span class="fork-value">{{ forkNodeName }}</span>
      </div>
    </div>
    <Handle type="source" position="right" :id="'source' + id + 'right'" class="custom-handle custom-handle-right custom-handle-source" />
    <Handle type="target" position="left" :id="'target' + id + 'left'" class="custom-handle custom-handle-left custom-handle-target" />
    <Handle type="target" position="top" :id="'target' + id + 'top'" class="custom-handle custom-handle-top custom-handle-target" />
    <Handle type="source" position="bottom" :id="'source' + id + 'bottom'" class="custom-handle custom-handle-bottom custom-handle-source" />
  </div>
</template>

<script setup>
import { ref, watch, onUnmounted, inject, computed } from 'vue'
import { Link, MoreFilled, Delete } from '@element-plus/icons-vue'
import { Handle, useVueFlow } from '@vue-flow/core'

const props = defineProps({
  data: {
    type: Object,
    default: () => ({ label: '等待' })
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

const emit = defineEmits(['handleClick', 'delete'])
const injectedSelectedNode = inject('selectedNode', null)
const FlowNodes = inject('FlowNodes', ref([]))
const nodeRef = ref(null)
const isSelected = ref(false)

const forkNodeName = computed(() => {
  const forkID = props.data?.config?.fork_node_id
  if (!forkID) return '未选择'
  const forkNode = FlowNodes.value.find(n => String(n.id) === String(forkID))
  return forkNode?.data?.label || forkID
})

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
    if (handleId && handleId.includes('source')) {
      emit('handleClick', props.id, handleId, event)
    }
  }
}

const deleteNode = () => {
  emit('delete', props.id)
}
</script>

<style scoped>
.wait-node {
  width: auto;
  min-width: 120px;
  height: auto;
  min-height: 48px;
  border: 1px solid #bbb;
  border-radius: 12px;
  background-color: #fff;
  display: flex;
  align-items: center;
  justify-content: flex-start;
  cursor: pointer;
  padding: 6px 10px;
  position: relative;
}

.node-content {
  display: flex;
  flex-direction: column;
  gap: 4px;
  flex: 1;
  padding-right: 5px;
}

.fork-info {
  display: flex;
  align-items: center;
  gap: 4px;
  font-size: 10px;
  color: #666;
  padding-left: 28px;
}

.fork-label {
  color: #999;
}

.fork-value {
  color: #333;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  max-width: 80px;
}

.wait-node:hover {
  box-shadow: 0 2px 12px rgba(156, 39, 176, 0.3);
}

.wait-node.selected {
  box-shadow: 0 0 0 1px #409eff;
}

.wait-node.selected:hover {
  box-shadow: 0 0 0 1px #409eff, 0 2px 12px rgba(64, 158, 255, 0.3);
}

.node-header {
  display: flex;
  flex-direction: row;
  align-items: center;
  gap: 8px;
  flex: 1;
  padding-right: 5px;
}

.more-icon-wrapper {
  margin-left: auto;
  opacity: 0;
  transition: opacity 0.2s;
  cursor: pointer;
}

.wait-node:hover .more-icon-wrapper {
  opacity: 1;
}

.more-icon {
  font-size: 16px;
  color: #999;
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

.icon-wait { background-color: #00bcd4; }

.icon-wait .el-icon {
  font-size: 16px;
  color: #ffffff;
}

.custom-handle {
  width: 16px !important;
  height: 16px !important;
  background-color: #00704A !important;
  border-radius: 50% !important;
  opacity: 0 !important;
  position: absolute !important;
}

.custom-handle::before {
  content: '+';
  color: white;
  font-size: 12px;
  font-weight: bold;
  position: absolute;
  top: 50%;
  left: 50%;
  transform: translate(-50%, -50%);
}

.wait-node:hover .custom-handle {
  opacity: 1 !important;
}

.custom-handle-top {
  top: -8px !important;
  left: 50% !important;
  transform: translateX(-50%);
}

.custom-handle-left {
  left: -8px !important;
  top: 50% !important;
  transform: translateY(-50%);
}

.custom-handle-bottom {
  bottom: -8px !important;
  left: 50% !important;
  transform: translateX(-50%);
}

.custom-handle-right {
  right: -8px !important;
  top: 50% !important;
  transform: translateY(-50%);
}

.custom-handle-target {
  background-color: #003366 !important;
}

.custom-handle-target::before {
  content: '-';
  color: white;
  font-size: 16px;
  font-weight: bold;
  position: absolute;
  top: 45%;
  left: 50%;
  transform: translate(-50%, -50%);
}

.handle-hidden {
  display: none;
}
</style>