<template>
  <div class="rpc-node" :class="{ 'selected': isSelected }" ref="nodeRef" @click="handleNodeClick">
    <div class="node-header">
      <div class="icon-wrapper icon-rpc"><el-icon><Mouse /></el-icon></div>
      <div class="node-label">{{ data.label }}</div>
      <el-dropdown trigger="hover" placement="right">
        <span class="more-icon-wrapper"><el-icon class="more-icon"><MoreFilled /></el-icon></span>
        <template #dropdown>
          <el-dropdown-menu>
            <el-dropdown-item @click="deleteNode">
              <el-icon><Delete /></el-icon>
              <span>{{ t('flow.node.action.delete') }}</span>
            </el-dropdown-item>
            <BreakpointMenuItem :id="id" />
          </el-dropdown-menu>
        </template>
      </el-dropdown>
    </div>
    <Handle type="source" position="right" :id="'source' + id + 'right'" class="custom-handle custom-handle-right custom-handle-source" />
    <Handle type="target" position="left" :id="'target' + id + 'left'" class="custom-handle custom-handle-left custom-handle-target" />
    <Handle type="target" position="top" :id="'target' + id + 'top'" class="custom-handle custom-handle-top custom-handle-target" />
    <Handle type="source" position="bottom" :id="'source' + id + 'bottom'" class="custom-handle custom-handle-bottom custom-handle-source" />
  </div>
</template>

<script setup>
import { ref, watch, inject } from 'vue'
import { Mouse, MoreFilled, Delete } from '@element-plus/icons-vue'
import { Handle } from '@vue-flow/core'
import { useI18n } from 'vue-i18n'

const { t } = useI18n()

const props = defineProps({
  data: {
    type: Object,
    default: () => ({ label: 'RPC' })
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

const emit = defineEmits(['handleClick', 'delete', 'updateData'])
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
.rpc-node {
  width: auto;
  min-width: 100px;
  height: 32px;
  border: 1px solid #bbb;
  border-radius: 16px;
  background-color: #fff;
  display: flex;
  align-items: center;
  justify-content: flex-start;
  cursor: pointer;
  padding: 0 10px;
  position: relative;
}

.rpc-node:hover {
  box-shadow: 0 2px 12px rgba(102, 126, 234, 0.3);
}

.rpc-node.selected {
  box-shadow: 0 0 0 1px #667eea;
}

.rpc-node.selected:hover {
  box-shadow: 0 0 0 1px #667eea, 0 2px 12px rgba(102, 126, 234, 0.3);
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

.rpc-node:hover .more-icon-wrapper {
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

.icon-rpc { background-color: #1890ff; }

.icon-rpc .el-icon {
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

.rpc-node:hover .custom-handle {
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
</style>
