<template>
  <div class="raise-node" :class="{ 'selected': isSelected }" ref="nodeRef" @click="handleNodeClick">
    <div class="node-header">
      <div class="icon-wrapper icon-raise"><el-icon><WarnTriangleFilled /></el-icon></div>
      <div class="node-label">{{ data.label || t('flow.node.defaultLabel.raise') }}</div>
      <NodeBadges :id="id" />
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
    <Handle type="target" position="right" :id="'target' + id + 'right'" class="custom-handle custom-handle-right custom-handle-target" />
    <Handle type="target" position="left" :id="'target' + id + 'left'" class="custom-handle custom-handle-left custom-handle-target" />
    <Handle type="target" position="top" :id="'target' + id + 'top'" class="custom-handle custom-handle-top custom-handle-target" />
    <Handle type="target" position="bottom" :id="'target' + id + 'bottom'" class="custom-handle custom-handle-bottom custom-handle-target" />
  </div>
</template>

<script setup>
import { ref, watch, inject } from 'vue'
import { WarnTriangleFilled, MoreFilled, Delete } from '@element-plus/icons-vue'
import { Handle } from '@vue-flow/core'
import { useI18n } from 'vue-i18n'
import NodeBadges from './NodeBadges.vue'

const { t } = useI18n()

const props = defineProps({
  data: {
    type: Object,
    default: () => ({})
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
/* Sink node: the four handles are targets only. A raise never succeeds, so an outgoing edge could never be taken. */
.raise-node {
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

.raise-node:hover {
  box-shadow: 0 2px 12px rgba(255, 152, 0, 0.3);
}

.raise-node.selected {
  box-shadow: 0 0 0 1px #409eff;
}

.raise-node.selected:hover {
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

.raise-node:hover .more-icon-wrapper {
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

.icon-raise { background-color: #f56c6c; }

.icon-raise .el-icon {
  font-size: 16px;
  color: #ffffff;
}

.custom-handle {
  width: 16px !important;
  height: 16px !important;
  background-color: #003366 !important;
  border-radius: 50% !important;
  opacity: 0 !important;
  position: absolute !important;
}

.custom-handle::before {
  content: '-';
  color: white;
  font-size: 16px;
  font-weight: bold;
  position: absolute;
  top: 45%;
  left: 50%;
  transform: translate(-50%, -50%);
}

.raise-node:hover .custom-handle {
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

.handle-hidden {
  display: none;
}
</style>
