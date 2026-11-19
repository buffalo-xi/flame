<template>
  <div class="branch-node" :class="{'selected': isSelected }" ref="nodeRef" @click="handleNodeClick">
    <div class="node-header">
      <Handle type="target" position="left" :id="'target' + id + 'left'" class="custom-handle custom-handle-left custom-handle-target" />
      <Handle type="target" position="top" :id="'target' + id + 'top'" class="custom-handle custom-handle-top custom-handle-target" />
      <Handle type="target" position="right" :id="'target' + id + 'right'" class="custom-handle custom-handle-right custom-handle-target" />
      <div class="icon-wrapper icon-start"><el-icon><Connection /></el-icon></div>
      <div class="node-label">{{ data.label || t('flow.node.defaultLabel.branch') }}</div>
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

    <div
      v-for="(child, index) in children"
      :key="id + '-' + child.id"
      class="node-content child-node"
      @click="handleNodeClick"     
    >
      <div class="child-content">
        <div class="child-label">
          <span class="exclusive-star" :class="{ 'is-exclusive': child.exclusive }" :title="child.exclusive ? t('flow.node.branch.exclusiveTitle') : t('flow.node.branch.inclusiveTitle')">*</span>
          {{ child.label || t('flow.node.branch.childName', { n: index + 1 }) }}
        </div>
        <el-dropdown trigger="hover" placement="right">
          <span class="more-icon-wrapper"><el-icon class="more-icon"><MoreFilled /></el-icon></span>
          <template #dropdown>
            <el-dropdown-menu>
              <el-dropdown-item @click="deleteChildNode(index)">
                <el-icon><Delete /></el-icon>
                <span>{{ t('flow.node.branch.deleteChild') }}</span>
              </el-dropdown-item>
            </el-dropdown-menu>
          </template>
        </el-dropdown>
      </div>
      <Handle
        type="source"
        position="right"
        :id="'source' + child.id + 'right'"
        class="custom-handle"
      />
      <Handle
        type="source"
        position="left"
        :id="'source' + child.id + 'left'"
        class="custom-handle"
      />
    </div>

    <div class="node-content child-node child-node-default">
      <Handle
        type="source"
        position="left"
        :id="'source' + id + 'left'"
        class="custom-handle custom-handle-left"
      />
      <div class="child-content">
        <div class="child-label child-label-default">{{ t('flow.node.branch.defaultBranch') }}</div>        
      </div>
      <Handle
        type="source"
        position="right"
        :id="'source' + id + 'right'"
        class="custom-handle custom-handle-right"
      />
      <Handle
        type="source"
        position="bottom"
        :id="'source' + id + 'bottom'"
        class="custom-handle custom-handle-bottom"
      />
    </div>
  </div>  
</template>

<script setup>
import { ref, computed, onMounted, watch, inject } from 'vue'
import { Connection, MoreFilled, Plus, Delete } from '@element-plus/icons-vue'
import { Handle, useVueFlow } from '@vue-flow/core'
import random from '@/tenant/utils/random'
import { useI18n } from 'vue-i18n'

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

const emit = defineEmits(['delete', 'handleClick', 'deleteChild', 'updateData'])

const nodeRef = ref(null)
const children = ref(props.data.config?.children || [])

// 同步外部（BranchNodeProps）对 children 的修改（增删分支、排他开关），保证节点显示实时更新
watch(
  () => props.data.config?.children,
  (newChildren) => {
    children.value = newChildren ? [...newChildren] : []
  },
  { deep: true }
)

const { updateNodeInternals} = useVueFlow()

const injectedSelectedNode = inject('selectedNode', null)

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
    // 只处理包含 source 的 handleId
    if (handleId && handleId.includes('source')) {
      emit('handleClick', props.id, handleId, event)
    }
  }
}

const addChild = () => {
  if (!props.data.config) props.data.config = {}
  if (!props.data.config.children) props.data.config.children = []
  const newId = random.randomNumberString(16)
  const newChild = { id: newId, label: t('flow.node.branch.newChildName', { n: props.data.config.children.length + 1 }), condition: '' }
  props.data.config.children.push(newChild)
  children.value = props.data.config.children
  emit('updateData', { config: { ...props.data.config } })
}

const addChildNode = (index) => {
  if (!props.data.config) props.data.config = {}
  if (!props.data.config.children) props.data.config.children = []
  const newId = random.randomNumberString(16)
  const newChild = { id: newId, label: t('flow.node.branch.newChildName', { n: props.data.config.children.length + 1 }), condition: '' }
  props.data.config.children.splice(index, 0, newChild)
  children.value = props.data.config.children
  emit('updateData', { config: { ...props.data.config } })
}

const addChildBeforeDefault = () => {
  if (!props.data.config) props.data.config = {}
  if (!props.data.config.children) props.data.config.children = []
  const newId = random.randomNumberString(16)
  
  const newChild = { id: newId, label: t('flow.node.branch.newChildName', { n: props.data.config.children.length + 1 }), condition: '' }
  props.data.config.children.push(newChild)
  children.value = props.data.config.children
  emit('updateData', { config: { ...props.data.config } })
}

const deleteChildNode = (index) => {
  if (!props.data.config?.children) return
  const deletedChild = props.data.config.children[index]
  if (deletedChild) {
    emit('deleteChild', deletedChild.id)
  }
  props.data.config.children.splice(index, 1)
  children.value = props.data.config.children
  emit('updateData', { config: { ...props.data.config } })
}

const deleteNode = () => {
  emit('delete', props.id)
}


</script>

<style scoped>
:deep(.branch-node) {
  width: 300px;
  min-width: 300px;
  border-radius: 0;
  cursor: pointer;
  position: relative;
  overflow:visible !important;
}

:deep(.more-icon-wrapper) {
  padding: 8px;
  cursor: pointer;
}

:deep(.more-icon) {
  color: #00704A;
  font-size: 13px;
  opacity: 0;
  cursor: pointer;
}

:deep(.node-header:hover .more-icon) {
  opacity: 1;
}

:deep(.branch-node:hover) {
  box-shadow: 0 2px 12px rgba(0, 112, 74, 0.3);
}

.branch-node.selected {
  box-shadow: 0 0 0 1px #409eff;
  border-radius: 16px;
}

.branch-node.selected:hover {
  box-shadow: 0 0 0 1px #409eff, 0 2px 12px rgba(64, 158, 255, 0.3);
  border-radius: 16px;
}

:deep(.node-header) {
  display: flex;
  flex-direction: row;
  align-items: center;
  gap: 8px;
  padding: 0px 12px;
  border: 1px solid #bbb;
  border-bottom: none;
  border-radius: 16px 16px 0 0;
  position: relative;
  background-color: #ffffff;
  z-index: 1;
}

:deep(.node-label) {
  font-size: 11px;
  font-weight: 700;
  color: #333;
  flex: 1;
}

:deep(.icon-wrapper) {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 20px;
  height: 20px;
  border-radius: 8px;
  margin-right: 4px;
}

:deep(.icon-start) { background-color: #00bcd4; }

:deep(.icon-start .el-icon) {
  font-size: 16px;
  color: #ffffff;
}

.node-content {
  display: flex;
  flex-direction: column;
  padding: 0px 10px;
  position: relative;
  z-index: 1;
  border-left: 1px solid #bbb;
  border-right: 1px solid #bbb;
}

.child-node {
  display: flex;
  align-items: center;
  height: 28px;
  padding: 0 5px;
  background-color: #fbfbfb;
  border-bottom: 1px solid #e1e1e1;
  transition: background-color 0.2s;
}

:deep(.child-node:hover) {
  background-color: #e6f0f8;
}

:deep(.child-node-default) {
  position: relative;
  z-index: 1;
  border-bottom: 1px solid #bbb;
  border-radius: 0 0 16px 16px;
}

:deep(.child-node-default:hover .more-icon) {
  opacity: 1;
}

:deep(.child-node-default .custom-handle) {
  opacity: 0 !important;
}

:deep(.child-node-default:hover .custom-handle) {
  opacity: 1 !important;
}

:deep(.child-content) {
  display: flex;
  flex-direction: row;
  align-items: center;
  gap: 8px;
  flex: 1;
  padding: 0 15px;
  width: 100%;
}

:deep(.child-label) {
  font-size: 11px;
  color: #333;
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

:deep(.child-label-default) {
  color: #00704A;
  font-weight: 500;
}

:deep(.exclusive-star) {
  margin-right: 4px;
  font-size: 13px;
  font-weight: 700;
  color: #dcdfe6;
  flex-shrink: 0;
}

:deep(.exclusive-star.is-exclusive) {
  color: #f56c6c;
}

:deep(.child-node .more-icon) {
  opacity: 0;
  margin-left: auto;
}

:deep(.child-node:hover .more-icon) {
  opacity: 1;
}

:deep(.child-node .custom-handle) {
  opacity: 0 !important;
}

:deep(.child-node:hover .custom-handle) {
  opacity: 1 !important;
}

:deep(.custom-handle) {
  width: 16px !important;
  height: 16px !important;
  background-color: #00704A !important;
  border-radius: 50% !important;
  opacity: 1 !important;
  position: absolute !important;
}

:deep(.custom-handle::before) {
  content: '+';
  color: white;
  font-size: 12px;
  font-weight: bold;
  position: absolute;
  top: 50%;
  left: 50%;
  transform: translate(-50%, -50%);
}

:deep(.custom-handle-top) {
  top: -8px !important;
  left: 50% !important;
  transform: translateX(-50%);
}

:deep(.custom-handle-left) {
  left: -8px !important;
  top: 50% !important;
  transform: translateY(-50%);
}

:deep(.custom-handle-bottom) {
  bottom: -8px !important;
  left: 50% !important;
  transform: translateX(-50%);
}

:deep(.custom-handle-right) {
  right: -8px !important;
  top: 50% !important;
  transform: translateY(-50%);
}

:deep(.custom-handle-target) {
  background-color: #003366 !important;
  opacity: 0 !important;
}

:deep(.node-header:hover .custom-handle-target) {
  opacity: 1 !important;
}

:deep(.custom-handle-target::before) {
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
