<template>
  <div class="function-node" :class="{ 'selected': isSelected }" ref="nodeRef">
    <div class="node-header">
      <div class="icon-wrapper icon-function"><el-icon><ScaleToOriginal /></el-icon></div>
      <div class="node-label">{{ data.label || t('flow.node.defaultLabel.function') }}</div>
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

    <div class="node-content" ref="contentRef">
    </div>

    <div class="resize-handle" @mousedown.stop="startResize">
      <div class="resize-icon"></div>
    </div>
    <div class="resize-indicator"></div>
  </div>
</template>

<script setup>
import { ref, computed, onMounted, watch, inject } from 'vue'
import { ScaleToOriginal, MoreFilled, Delete } from '@element-plus/icons-vue'
import { useVueFlow } from '@vue-flow/core'
import { useI18n } from 'vue-i18n'
import NodeFindingsBadge from './NodeFindingsBadge.vue'

const { t } = useI18n()

const props = defineProps({
  data: {
    type: Object,
    default: () => ({ children: [] })
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

const emit = defineEmits(['delete'])

const nodeRef = ref(null)
const contentRef = ref(null)

const injectedSelectedNode = inject('selectedNode', null)
const groupNodeTypes = inject('groupNodeTypes', new Set(['process', 'loop', 'function']))

const { updateNodeInternals, nodes, getViewport } = useVueFlow()

const isSelected = ref(false)

if (injectedSelectedNode) {
  watch(() => injectedSelectedNode.value, (newSelectedNode) => {
    isSelected.value = newSelectedNode?.id === props.id
  }, { immediate: true, deep: true })
}

const deleteNode = () => {
  emit('delete', props.id)
}

const getContentBounds = () => {
  const getNodeBounds = (nodeId) => {
    const childNodes = nodes.value.filter(n => n.parentNode === nodeId)

    if (childNodes.length === 0) {
      return { maxX: 0, maxY: 0 }
    }

    let maxX = 0
    let maxY = 0

    childNodes.forEach(child => {
      let nodeWidth = child.dimensions?.width || 100
      let nodeHeight = child.dimensions?.height || 50

      if (groupNodeTypes.has(child.type)) {
        const childBounds = getNodeBounds(child.id)
        nodeWidth = Math.max(nodeWidth, childBounds.maxX + 5)
        nodeHeight = Math.max(nodeHeight, childBounds.maxY + 5)
      }

      const right = child.position.x + nodeWidth
      const bottom = child.position.y + nodeHeight
      if (right > maxX) maxX = right
      if (bottom > maxY) maxY = bottom
    })

    return { maxX, maxY }
  }

  if (!nodes.value) {
    return { minX: 0, minY: 0, maxX: 200, maxY: 77 }
  }

  const childNodes = nodes.value.filter(n => n.parentNode === props.id)

  if (childNodes.length === 0) {
    return { minX: 0, minY: 0, maxX: 200, maxY: 77 }
  }

  let maxX = 0
  let maxY = 0

  childNodes.forEach(child => {
    let nodeWidth = child.dimensions?.width || 100
    let nodeHeight = child.dimensions?.height || 50

    if (groupNodeTypes.has(child.type)) {
      const childBounds = getNodeBounds(child.id)
      nodeWidth = Math.max(nodeWidth, childBounds.maxX + 5)
      nodeHeight = Math.max(nodeHeight, childBounds.maxY + 5)
    }

    const right = child.position.x + nodeWidth
    const bottom = child.position.y + nodeHeight
    if (right > maxX) maxX = right
    if (bottom > maxY) maxY = bottom
  })

  return { minX: 0, minY: 0, maxX: Math.max(200, maxX + 5), maxY: Math.max(77, maxY + 5) }
}

const adjustSizeToFitChildren = () => {
  const doAdjust = () => {
    const bounds = getContentBounds()
    if (nodeRef.value) {
      const currentWidth = parseInt(nodeRef.value.style.width) || 0
      const currentHeight = parseInt(nodeRef.value.style.height) || 0

      const newWidth = Math.max(currentWidth, bounds.maxX)
      const newHeight = Math.max(currentHeight, bounds.maxY)

      if (newWidth !== currentWidth || newHeight !== currentHeight) {
        nodeRef.value.style.width = newWidth + 'px'
        nodeRef.value.style.minWidth = newWidth + 'px'
        nodeRef.value.style.height = newHeight + 'px'
        nodeRef.value.style.minHeight = newHeight + 'px'

        if (props.data) {
          if (!props.data.config) props.data.config = {}
          if (!props.data.config.size) props.data.config.size = {}
          props.data.config.size.width = newWidth + 'px'
          props.data.config.size.height = newHeight + 'px'
        }

        nextTick(() => {
          if (props.id) {
            updateNodeInternals(props.id)
          }
        })
      }
    }
  }
  doAdjust()
  setTimeout(doAdjust, 30)
}

const startResize = (event) => {
  if (!nodeRef.value) return undefined
  
  const startX = event.clientX
  const startY = event.clientY
  const viewport = getViewport()
  const scale = viewport?.zoom || 1

  const startWidth = parseFloat(nodeRef.value.style.width) || nodeRef.value.offsetWidth / scale
  const startHeight = parseFloat(nodeRef.value.style.height) || nodeRef.value.offsetHeight / scale

  const bounds = getContentBounds()

  const onMove = (e) => {
    const deltaX = (e.clientX - startX) / scale
    const deltaY = (e.clientY - startY) / scale

    const newWidth = Math.max(bounds.maxX, startWidth + deltaX)
    const newHeight = Math.max(bounds.maxY, startHeight + deltaY)

    if (nodeRef.value) {
      nodeRef.value.style.width = newWidth + 'px'
      nodeRef.value.style.minWidth = newWidth + 'px'
      nodeRef.value.style.height = newHeight + 'px'
      nodeRef.value.style.minHeight = newHeight + 'px'
    }

    nextTick(() => {
      if (props.id) {
        updateNodeInternals(props.id)
      }
    })
    return undefined
  }

  const onUp = () => {
    document.removeEventListener('mousemove', onMove)
    document.removeEventListener('mouseup', onUp)
    document.body.style.cursor = 'default'

    if (nodeRef.value && props.data) {
      const newWidth = nodeRef.value.style.width
      const newHeight = nodeRef.value.style.height
      
      if (!props.data.config) props.data.config = {}
      if (!props.data.config.size) props.data.config.size = {}
      
      if (!props.data.config.size.width || props.data.config.size.width !== newWidth) {
        props.data.config.size.width = newWidth
      }
      if (!props.data.config.size.height || props.data.config.size.height !== newHeight) {
        props.data.config.size.height = newHeight
      }
    }
    return undefined
  }

  document.addEventListener('mousemove', onMove)
  document.addEventListener('mouseup', onUp)
  document.body.style.cursor = 'se-resize'
  return undefined
}

const nextTick = (fn) => {
  setTimeout(fn, 0)
}

watch(() => props.data?._needsResize, () => {
  nextTick(() => {
    adjustSizeToFitChildren()
  })
})

// 外部改了 config.size（智能体重排 / 重新拉 diagram）时要把尺寸重新写回 DOM：
// 节点组件按 id 复用，onMounted 只跑一次，之后再变就没人管了 —— 数据变了框不变，
// 子节点还挤在旧尺寸里。Vue Flow 侧有 ResizeObserver，DOM 真变了会自动重算锚点。
const applyStoredSize = () => {
  if (!nodeRef.value || !props.data) return
  const rawW = props.data.config?.size?.width || props.data.width
  const rawH = props.data.config?.size?.height || props.data.height
  // 后端写的是 "300px"，这里兜一下纯数字：style.width = 300 会被忽略
  const width = typeof rawW === 'number' ? `${rawW}px` : rawW
  const height = typeof rawH === 'number' ? `${rawH}px` : rawH

  if (width) {
    nodeRef.value.style.width = width
    nodeRef.value.style.minWidth = width
  }
  if (height) {
    nodeRef.value.style.height = height
    nodeRef.value.style.minHeight = height
  }
  nextTick(() => {
    if (props.id) updateNodeInternals(props.id)
  })
}

onMounted(applyStoredSize)

watch(
  () => `${props.data?.config?.size?.width || ''}x${props.data?.config?.size?.height || ''}`,
  () => applyStoredSize()
)
</script>

<style scoped>
:deep(.function-node) {
  width: 300px;
  min-width: 300px;
  height: 100px;
  min-height: 100px;
  /* The card's corners are 16px (header top, last row bottom): the root has to match, or the hover shadow paints a
     square silhouette whose four corners show beside the rounded ones. */
  border-radius: 16px;
  cursor: pointer;
  position: relative;
  overflow:visible !important;
}

:deep(.more-icon-wrapper) {
  padding: 8px;
  cursor: pointer;
}

:deep(.more-icon) {
  color: #667eea;
  font-size: 13px;
  opacity: 0;
  cursor: pointer;
}

:deep(.node-header .more-icon) {
  opacity: 0;
}

:deep(.node-header:hover .more-icon) {
  opacity: 1;
}

:deep(.function-node:hover) {
  box-shadow: 0 2px 12px rgba(102, 126, 234, 0.3);
}

.function-node.selected {
  box-shadow: 0 0 0 1px #409eff;
  border-radius: 16px;
}

.function-node.selected:hover {
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
  /* The header can gain an extra item (findings badge): the label shortens instead of wrapping, like child-label. */
  min-width: 0;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
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

:deep(.icon-function) { background-color: #667eea; }

:deep(.icon-function .el-icon) {
  font-size: 16px;
  color: #ffffff;
}

.node-content {
  position: relative;
  padding: 0px;
  border-left: 1px solid #bbb;
  border-right: 1px solid #bbb;
  border-bottom: 1px solid #bbb;
  border-radius: 0 0 16px 16px;

  min-height: 30px;
  background-color: #f6f6ff;
  z-index: 1;
  height: calc(100% - 32px);
}

:deep(.resize-handle) {
  position: absolute;
  right: -8px;
  bottom: -8px;
  width: 30px;
  height: 30px;
  background-color: #667eea;
  border-radius: 50%;
  cursor: se-resize;
  z-index: 100;
  display: flex;
  align-items: center;
  justify-content: center;
  box-shadow: 0 2px 4px rgba(0, 0, 0, 0.2);
  opacity: 0;
  transition: opacity 0.2s, transform 0.2s;
}

:deep(.function-node:hover .resize-handle) {
  opacity: 1;
}

:deep(.resize-handle:hover) {
  background-color: #5a6fd6;
  transform: scale(1.1);
}

:deep(.resize-icon) {
  width: 10px;
  height: 10px;
  border-right: 2px solid #fff;
  border-bottom: 2px solid #fff;
  transform: rotate(45deg);
  margin-top: 2px;
}

.resize-indicator {
  position: absolute;
  right: 2px;
  bottom: 2px;
  width: 18px;
  height: 18px;
  opacity: 0;
  transition: opacity 0.2s;
  pointer-events: none;
  z-index: 50;
}

.resize-indicator:after {
  content: '';
  position: absolute;
  right: 0;
  bottom: 0;
  width: 12px;
  height: 12px;
  border-right: 2px solid #bbb;
  border-bottom: 2px solid #bbb;
  border-radius: 0 0 6px 0;
}

.function-node:hover .resize-indicator {
  opacity: 1;
}
</style>