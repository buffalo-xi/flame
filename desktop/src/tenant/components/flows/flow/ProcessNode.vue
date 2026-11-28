<template>
  <div class="process-node" :class="{ 'selected': isSelected }" ref="nodeRef" @click="handleNodeClick">
    <div class="node-header">
      <Handle type="target" position="left" :id="'target' + id + 'left'" class="custom-handle custom-handle-left custom-handle-target" />
      <Handle type="target" position="top" :id="'target' + id + 'top'" class="custom-handle custom-handle-top custom-handle-target" />
      <Handle type="target" position="right" :id="'target' + id + 'right'" class="custom-handle custom-handle-right custom-handle-target" />
      <div class="icon-wrapper icon-process"><el-icon><Help /></el-icon></div>
      <div class="node-label" ref="labelRef">{{ data.label || t('flow.node.fallbackLabel.process') }}</div>
      <NodeBadges :id="id" />
      <el-dropdown trigger="hover" placement="right">
        <span class="more-icon-wrapper" @click.stop @mousedown.stop @pointerdown.stop><el-icon class="more-icon"><MoreFilled /></el-icon></span>
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
      <Handle type="source" position="left" :id="'source' + id + 'left'" class="custom-handle custom-handle-left" />
      <Handle type="source" position="right" :id="'source' + id + 'right'" class="custom-handle custom-handle-right" />
      <Handle type="source" position="bottom" :id="'source' + id + 'bottom'" class="custom-handle custom-handle-bottom" />
    </div>

    <div class="resize-handle" @mousedown.stop="startResize">
      <div class="resize-icon"></div>
    </div>
    <div class="resize-indicator"></div>
  </div>
</template>

<script setup>
import { ref, onMounted, watch, inject } from 'vue'
import { Help, MoreFilled, Delete } from '@element-plus/icons-vue'
import { Handle, useVueFlow } from '@vue-flow/core'
import { useI18n } from 'vue-i18n'
import NodeBadges from './NodeBadges.vue'

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

const emit = defineEmits(['delete', 'handleClick', 'updateData'])

const nodeRef = ref(null)
const contentRef = ref(null)
const labelRef = ref(null)

const injectedSelectedNode = inject('selectedNode', null)
const groupNodeTypes = inject('groupNodeTypes', new Set(['process', 'loop', 'function']))
const { updateNodeInternals, nodes, getViewport } = useVueFlow()

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

const getHeaderMinWidth = () => {
  const iconWidth = 24
  const labelWidth = labelRef.value ? labelRef.value.offsetWidth : 40
  const moreBtnWidth = 29
  const gap = 8
  const paddingTotal = 12 * 2
  
  const minWidth = iconWidth + labelWidth + moreBtnWidth + gap * 2 + paddingTotal

  return minWidth
}

const getContentBounds = () => {
  const headerMinWidth = getHeaderMinWidth()

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
    return { minX: 0, minY: 0, maxX: headerMinWidth, maxY: 77 }
  }

  const childNodes = nodes.value.filter(n => n.parentNode === props.id)

  if (childNodes.length === 0) {
    return { minX: 0, minY: 0, maxX: headerMinWidth, maxY: 77 }
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

  return { minX: 0, minY: 0, maxX: Math.max(headerMinWidth, maxX + 5), maxY: Math.max(77, maxY + 5) }
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
          emit('updateData', { nodeId: props.id, data: { config: { size: { width: newWidth + 'px', height: newHeight + 'px' } } } })
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
      const updates = { config: { size: {} } }
      let needsUpdate = false
      
      if (!props.data.config) props.data.config = {}
      if (!props.data.config.size) props.data.config.size = {}
      
      if (!props.data.config.size.width || props.data.config.size.width !== newWidth) {
        props.data.config.size.width = newWidth
        updates.config.size.width = newWidth
        needsUpdate = true
      }
      if (!props.data.config.size.height || props.data.config.size.height !== newHeight) {
        props.data.config.size.height = newHeight
        updates.config.size.height = newHeight
        needsUpdate = true
      }
      
      if (needsUpdate) {
        emit('updateData', { nodeId: props.id, data: updates })
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

watch(() => props.data?.label, () => {
  nextTick(() => {
    const headerMinWidth = getHeaderMinWidth()
    if (nodeRef.value) {
      const currentWidth = parseInt(nodeRef.value.style.width) || nodeRef.value.offsetWidth
      if (currentWidth < headerMinWidth) {
        nodeRef.value.style.width = headerMinWidth + 'px'
        nodeRef.value.style.minWidth = headerMinWidth + 'px'
        if (props.data) {
          if (!props.data.config) props.data.config = {}
          if (!props.data.config.size) props.data.config.size = {}
          props.data.config.size.width = headerMinWidth + 'px'
        }
        updateNodeInternals(props.id)
      }
    }
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
:deep(.process-node) {
  width: 300px;
  min-width: 300px;
  height: 100px;
  min-height: 100px;
  /* border: 0px solid #00704A; */
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
  color: #00704A;
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

:deep(.process-node:hover) {
  box-shadow: 0 2px 12px rgba(0, 112, 74, 0.3);
}

.process-node.selected {
  box-shadow: 0 0 0 1px #409eff;
  border-radius: 16px;
}

.process-node.selected:hover {
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
  white-space: nowrap;
  /* Take what the header has left instead of pushing the extra item (findings badge) past the border. */
  flex: 1;
  min-width: 0;
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

:deep(.icon-process) { background-color: #00bcd4; }

:deep(.icon-process .el-icon) {
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
  background-color: #f6f9f6;
  z-index: 1;
  height: calc(100% - 32px);
}

:deep(.resize-handle) {
  position: absolute;
  right: -8px;
  bottom: -8px;
  width: 30px;
  height: 30px;
  background-color: #00704A;
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

:deep(.process-node:hover .resize-handle) {
  opacity: 1;
}

:deep(.resize-handle:hover) {
  background-color: #005a38;
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

.process-node:hover .resize-indicator {
  opacity: 1;
}

:deep(.custom-handle) {
  width: 16px !important;
  height: 16px !important;
  background-color: #00704A !important;
  border-radius: 50% !important;
  opacity: 0 !important;
  position: absolute !important;
  z-index: 10 !important;
}

:deep(.process-node:hover .custom-handle) {
  opacity: 1 !important;
}

:deep(.node-content:hover .custom-handle) {
  opacity: 1 !important;
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

:deep(.custom-handle-right) {
  right: -8px !important;
  top: 50% !important;
  transform: translateY(-50%);
}

:deep(.custom-handle-bottom) {
  bottom: -8px !important;
  left: 50% !important;
  transform: translateX(-50%);
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