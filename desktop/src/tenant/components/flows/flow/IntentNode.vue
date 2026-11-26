<template>
  <div class="intent-node" :class="{'selected': isSelected }" ref="nodeRef" @click="handleNodeClick">
    <div class="node-header">
      <Handle type="target" position="left" :id="'target' + id + 'left'" class="custom-handle custom-handle-left custom-handle-target" />
      <Handle type="target" position="top" :id="'target' + id + 'top'" class="custom-handle custom-handle-top custom-handle-target" />
      <Handle type="target" position="right" :id="'target' + id + 'right'" class="custom-handle custom-handle-right custom-handle-target" />
      <div class="icon-wrapper icon-intent"><el-icon><Aim /></el-icon></div>
      <div class="node-label">{{ data.label || t('flow.node.defaultLabel.intent') }}</div>
      <NodeFindingsBadge :id="id" />
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
          {{ child.label || t('flow.node.intent.childName', { n: index + 1 }) }}
        </div>
        <el-dropdown trigger="hover" placement="right">
          <span class="more-icon-wrapper"><el-icon class="more-icon"><MoreFilled /></el-icon></span>
          <template #dropdown>
            <el-dropdown-menu>
              <el-dropdown-item @click="deleteChildNode(index)">
                <el-icon><Delete /></el-icon>
                <span>{{ t('flow.node.intent.deleteChild') }}</span>
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
import { ref, watch, inject } from 'vue'
import { Aim, MoreFilled, Delete } from '@element-plus/icons-vue'
import { Handle, useVueFlow } from '@vue-flow/core'
import { useI18n } from 'vue-i18n'
import NodeFindingsBadge from './NodeFindingsBadge.vue'

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

// 同步外部（IntentNodeProps）对 children 的修改（增删意图分支），保证节点显示实时更新
watch(
  () => props.data.config?.children,
  (newChildren) => {
    children.value = newChildren ? [...newChildren] : []
  },
  { deep: true }
)

const { updateNodeInternals } = useVueFlow()

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
.intent-node {
  /* 160px is the floor, not the width: the title decides how wide the node gets, because a badge in the header used to
     take room from the label and wrap it onto a second line. max-width keeps an absurdly long label from growing without
     bound. */
  width: max-content;
  min-width: 160px;
  max-width: 360px;
  /* The card's corners are 16px (header top, last row bottom): the root has to match, or the hover shadow paints a
     square silhouette whose four corners show beside the rounded ones. */
  border-radius: 16px;
  cursor: pointer;
  position: relative;
  overflow: visible !important;
}

.more-icon-wrapper {
  padding: 8px;
  cursor: pointer;
}

.more-icon {
  color: #9c27b0;
  font-size: 13px;
  opacity: 0;
  cursor: pointer;
}

.node-header:hover .more-icon {
  opacity: 1;
}

.intent-node:hover {
  box-shadow: 0 2px 12px rgba(156, 39, 176, 0.3);
}

.intent-node.selected {
  box-shadow: 0 0 0 1px #409eff;
  border-radius: 16px;
}

.intent-node.selected:hover {
  box-shadow: 0 0 0 1px #409eff, 0 2px 12px rgba(64, 158, 255, 0.3);
  border-radius: 16px;
}

.node-header {
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

.node-label {
  font-size: 11px;
  font-weight: 700;
  color: #333;
  flex: 1;
  min-width: 0;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
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

.icon-intent { background-color: #9c27b0; }

.icon-intent .el-icon {
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

.child-node:hover {
  background-color: #f3e8f7;
}

.child-node-default {
  position: relative;
  z-index: 1;
  border-bottom: 1px solid #bbb;
  border-radius: 0 0 16px 16px;
}

.child-node-default:hover .more-icon {
  opacity: 1;
}

.child-node-default .custom-handle {
  opacity: 0 !important;
}

.child-node-default:hover .custom-handle {
  opacity: 1 !important;
}

.child-content {
  display: flex;
  flex-direction: row;
  align-items: center;
  gap: 8px;
  flex: 1;
  padding: 0 15px;
  width: 100%;
}

.child-label {
  font-size: 11px;
  color: #333;
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.child-label-default {
  color: #9c27b0;
  font-weight: 500;
}

.child-node .more-icon {
  opacity: 0;
  margin-left: auto;
}

.child-node:hover .more-icon {
  opacity: 1;
}

.child-node .custom-handle {
  opacity: 0 !important;
}

.child-node:hover .custom-handle {
  opacity: 1 !important;
}

.custom-handle {
  width: 16px !important;
  height: 16px !important;
  background-color: #9c27b0 !important;
  border-radius: 50% !important;
  opacity: 1 !important;
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
  opacity: 0 !important;
}

.node-header:hover .custom-handle-target {
  opacity: 1 !important;
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
