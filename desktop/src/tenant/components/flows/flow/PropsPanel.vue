<template>
  <div class="props-panel" :style="panelStyle">
    <div class="panel-resizer" @mousedown="startResize"></div>
    <div class="panel-header">
      <div v-if="selectedNode" class="node-info">
        <div class="node-icon" :class="`icon-${selectedNode.type || 'default'}`">
          <el-icon><component :is="getNodeIcon(selectedNode.type)" /></el-icon>
        </div>
        <template v-if="isEditingLabel">
          <input 
            ref="labelInputRef"
            class="node-name-input" 
            v-model="nodeLabel" 
            @blur="finishEditLabel"
            @keydown.enter="finishEditLabel"
            @keydown.esc="cancelEditLabel"
          />
        </template>
        <span 
          v-else
          class="node-name-display"
          @click="startEditLabel"
        >{{ nodeLabel }} <span class="node-name-id">({{ selectedNode.id }})</span></span>
      </div>
      <div v-else class="panel-title">{{ t('flow.propsPanel.title') }}</div>
      <span class="close-icon" @click="$emit('close')">×</span>
    </div>
    <div class="panel-body">
      <div v-if="selectedNode" class="note-edit-wrapper">
        <div
          class="note-edit"
          contenteditable="true"
          :data-placeholder="t('flow.propsPanel.notePlaceholder')"
          @blur="updateNodeNote"
        >{{ nodeNote }}</div>
      </div>
      <div class="panel-content">
        <!-- 流式输出绑定编辑视图（覆盖整个 panel-content） -->
        <div v-if="bindingEditState" key="binding-edit" class="node-props-inner">
          <OutputBindingConfig
            :varName="bindingEditState.varName"
            :varType="bindingEditState.varType"
            :binding="bindingEditState.binding"
            :selectedNode="selectedNode"
            @back="closeBindingEdit"
            @update-binding="handleBindingUpdate"
          />
        </div>
        <div v-else-if="selectedNode && selectedNode.type === 'start'" key="node-start" class="node-props-inner">
          <StartNodeProps 
            :selected-node="selectedNode" 
            :flow-arguments="flowArguments"
            :flow-limits="props.flowLimits"
            :flow-system-limits="props.flowSystemLimits"
            @update-node="handleUpdateNode"
            @update-node-data="handleUpdateNodeData"
            @update-arguments="handleUpdateArguments"
            @update-limits="handleUpdateLimits"
          />
        </div>
        <div v-else-if="selectedNode && selectedNode.type === 'code'" key="node-code" class="node-props-inner">
          <CodeNodeProps
            :selected-node="selectedNode"
            :flow-flow-variables="props.flowFlowVariables"
            :flow-environment-variables="props.flowEnvironmentVariables"
            :flow-arguments="props.flowArguments"
            @update-node-data="handleUpdateNodeData"
            @show-variable-select="handleShowVariableSelect"
          />
        </div>
        <div v-else-if="selectedNode && selectedNode.type === 'llm'" key="node-llm" class="node-props-inner">
          <LLMNodeProps
            :selected-node="selectedNode"
            :flow-flow-variables="props.flowFlowVariables"
            :flow-environment-variables="props.flowEnvironmentVariables"
            :flow-arguments="props.flowArguments"
            @update-node-data="handleUpdateNodeData"
            @show-variable-select="handleShowVariableSelect"
          />
        </div>
        <div v-else-if="selectedNode && selectedNode.type === 'loop'" key="node-loop" class="node-props-inner">
          <LoopNodeProps
            :selected-node="selectedNode"
            :flow-flow-variables="props.flowFlowVariables"
            :flow-environment-variables="props.flowEnvironmentVariables"
            :flow-arguments="props.flowArguments"
            @update-node-data="handleUpdateNodeData"
            @show-variable-select="handleShowVariableSelect"
          />
        </div>
        <div v-else-if="selectedNode && selectedNode.type === 'process'" key="node-process" class="node-props-inner">
          <ProcessNodeProps
            :selected-node="selectedNode"
            :flow-flow-variables="props.flowFlowVariables"
            :flow-environment-variables="props.flowEnvironmentVariables"
            :flow-arguments="props.flowArguments"
            @update-node-data="handleUpdateNodeData"
            @show-variable-select="handleShowVariableSelect"
          />
        </div>
        <div v-else-if="selectedNode && selectedNode.type === 'function'" key="node-function" class="node-props-inner">
          <FunctionNodeProps
            :selected-node="selectedNode"
            @update-node-data="handleUpdateNodeData"
            @show-variable-select="handleShowVariableSelect"
          />
        </div>
        <div v-else-if="selectedNode && selectedNode.type === 'call'" key="node-call" class="node-props-inner">
          <CallNodeProps
            :selected-node="selectedNode"
            @update-node-data="handleUpdateNodeData"
            @show-variable-select="handleShowVariableSelect"
          />
        </div>
        <div v-else-if="selectedNode && selectedNode.type === 'fork'" key="node-fork" class="node-props-inner">
          <ForkNodeProps
            :selected-node="selectedNode"
            @update-node-data="handleUpdateNodeData"
          />
        </div>
        <div v-else-if="selectedNode && selectedNode.type === 'wait'" key="node-wait" class="node-props-inner">
          <WaitNodeProps
            :selected-node="selectedNode"
            @update-node-data="handleUpdateNodeData"
          />
        </div>
        <div v-else-if="selectedNode && selectedNode.type === 'break'" key="node-break" class="node-props-inner">
          <BreakNodeProps :selected-node="selectedNode" />
        </div>
        <div v-else-if="selectedNode && selectedNode.type === 'continue'" key="node-continue" class="node-props-inner">
          <ContinueNodeProps :selected-node="selectedNode" />
        </div>
        <div v-else-if="selectedNode && selectedNode.type === 'return'" key="node-return" class="node-props-inner">
          <ReturnNodeProps :selected-node="selectedNode" />
        </div>
        <div v-else-if="selectedNode && selectedNode.type === 'exit'" key="node-exit" class="node-props-inner">
          <ExitNodeProps :selected-node="selectedNode" />
        </div>
        <div v-else-if="selectedNode && selectedNode.type === 'branch'" key="node-branch" class="node-props-inner">
          <BranchNodeProps
            :selected-node="selectedNode"
            :flow-flow-variables="props.flowFlowVariables"
            :flow-environment-variables="props.flowEnvironmentVariables"
            :flow-arguments="props.flowArguments"
            @update-node-data="handleUpdateNodeData"
            @show-variable-select="handleShowVariableSelect"
          />
        </div>
        <div v-else-if="selectedNode && selectedNode.type === 'intent'" key="node-intent" class="node-props-inner">
          <IntentNodeProps
            :selected-node="selectedNode"
            :flow-flow-variables="props.flowFlowVariables"
            :flow-environment-variables="props.flowEnvironmentVariables"
            :flow-arguments="props.flowArguments"
            @update-node-data="handleUpdateNodeData"
            @show-variable-select="handleShowVariableSelect"
          />
        </div>
        <div v-else-if="selectedNode && selectedNode.type === 'progress'" key="node-progress" class="node-props-inner">
          <ProgressNodeProps
            :selected-node="selectedNode"
            @update-node-data="handleUpdateNodeData"
            @show-variable-select="handleShowVariableSelect"
          />
        </div>
        <div v-else-if="selectedNode && selectedNode.type === 'result'" key="node-result" class="node-props-inner">
          <ResultNodeProps
            :selected-node="selectedNode"
            @update-node-data="handleUpdateNodeData"
            @show-variable-select="handleShowVariableSelect"
          />
        </div>
        <div v-else-if="selectedNode && selectedNode.type === 'http'" key="node-http" class="node-props-inner">
          <HttpNodeProps
            :selected-node="selectedNode"
            :flow-flow-variables="props.flowFlowVariables"
            :flow-environment-variables="props.flowEnvironmentVariables"
            :flow-arguments="props.flowArguments"
            @update-node-data="handleUpdateNodeData"
            @show-variable-select="handleShowVariableSelect"
          />
        </div>
        <div v-else-if="selectedNode && selectedNode.type === 'mcp'" key="node-mcp" class="node-props-inner">
          <McpNodeProps
            :selected-node="selectedNode"
            :flow-flow-variables="props.flowFlowVariables"
            :flow-environment-variables="props.flowEnvironmentVariables"
            :flow-arguments="props.flowArguments"
            @update-node-data="handleUpdateNodeData"
            @show-variable-select="handleShowVariableSelect"
          />
        </div>
        <div v-else-if="selectedNode && selectedNode.type === 'rpc'" key="node-rpc" class="node-props-inner">
          <RpcNodeProps
            :selected-node="selectedNode"
            :flow-id="props.flowId"
            :flow-flow-variables="props.flowFlowVariables"
            :flow-environment-variables="props.flowEnvironmentVariables"
            :flow-arguments="props.flowArguments"
            @update-node-data="handleUpdateNodeData"
            @show-variable-select="handleShowVariableSelect"
          />
        </div>
        <div v-else-if="selectedNode && selectedNode.type === 'memory'" key="node-memory" class="node-props-inner">
          <MemoryNodeProps
            :selected-node="selectedNode"
            :flow-system-limits="props.flowSystemLimits"
            @update-node-data="handleUpdateNodeData"
          />
        </div>
        <div v-else-if="selectedNode && selectedNode.type === 'assign'" key="node-assign" class="node-props-inner">
          <AssignNodeProps
            :selected-node="selectedNode"
            :flow-flow-variables="props.flowFlowVariables"
            :flow-environment-variables="props.flowEnvironmentVariables"
            :flow-arguments="props.flowArguments"
            @update-node-data="handleUpdateNodeData"
            @show-variable-select="handleShowVariableSelect"
          />
        </div>
        <div v-else-if="selectedNode" key="node-default" class="node-props-inner">
          <div class="default-props">
            <div class="prop-group">
              <label class="prop-label">{{ t('flow.propsPanel.nodeId') }}</label>
              <span class="prop-value">{{ selectedNode.id }}</span>
            </div>
          </div>
        </div>
        <div v-else key="empty-state" class="empty-state">
          <p>{{ t('flow.propsPanel.emptyTitle') }}</p>
          <p class="empty-hint">{{ t('flow.propsPanel.emptyHint') }}</p>
        </div>
      </div>
    </div>
    
    <Teleport to="body">
      <div v-if="showVariableModal" class="variable-select-overlay" @click="handleVariableModalClose">
        <VariableSelect
          :style="variablePanelStyle"
          v-model="selectedVariableForModal"
          :node-id="selectedNode?.id || ''"
          :nodes="FlowNodes"
          :edges="FlowEdges"
          :flow-variables="props.flowFlowVariables"
          :environment-variables="props.flowEnvironmentVariables"
          :arguments="props.flowArguments"
          :filter-type="currentFilterType"
          :select-mode="currentSelectMode"
          @change="handleVariableSelect"
          @close="handleVariableModalClose"
        />
      </div>
    </Teleport>
  </div>
</template>

<script setup>
import { ref, watch, computed, h, onMounted, onUnmounted, inject, provide, nextTick } from 'vue'
import { getNodeIconComponent } from './nodeIcons.js'
import StartNodeProps from './StartNodeProps.vue'
import CodeNodeProps from './CodeNodeProps.vue'
import LoopNodeProps from './LoopNodeProps.vue'
import ProcessNodeProps from './ProcessNodeProps.vue'
import LLMNodeProps from './LLMNodeProps.vue'
import FunctionNodeProps from './FunctionNodeProps.vue'
import ForkNodeProps from './ForkNodeProps.vue'
import WaitNodeProps from './WaitNodeProps.vue'
import CallNodeProps from './CallNodeProps.vue'
import BreakNodeProps from './BreakNodeProps.vue'
import ContinueNodeProps from './ContinueNodeProps.vue'
import ReturnNodeProps from './ReturnNodeProps.vue'
import ExitNodeProps from './ExitNodeProps.vue'
import BranchNodeProps from './BranchNodeProps.vue'
import IntentNodeProps from './IntentNodeProps.vue'
import ProgressNodeProps from './ProgressNodeProps.vue'
import ResultNodeProps from './ResultNodeProps.vue'
import AssignNodeProps from './AssignNodeProps.vue'
import HttpNodeProps from './HttpNodeProps.vue'
import McpNodeProps from './McpNodeProps.vue'
import RpcNodeProps from './RpcNodeProps.vue'
import MemoryNodeProps from './MemoryNodeProps.vue'
import VariableSelect from './VariableSelect.vue'
import OutputBindingConfig from './OutputBindingConfig.vue'
import { useEscClose } from '../../../composables/useEscClose'
import { useI18n } from 'vue-i18n'

const { t } = useI18n()

// 从父组件获取 nodes 和 edges
const FlowNodes = inject('FlowNodes', ref([]))
const FlowEdges = inject('FlowEdges', ref([]))



const props = defineProps({
  selectedNode: Object,
  flowId: {
    type: [String, Number],
    default: ''
  },
  runPanelVisible: Boolean,
  runPanelWidth: {
    type: Number,
    default: 300
  },
  variablesPanelVisible: Boolean,
  variablesPanelWidth: {
    type: Number,
    default: 300
  },
  flowArguments: {
    type: Object,
    default: () => ({})
  },
  flowLimits: {
    type: Object,
    default: () => ({ timeout: -1, max_exec_count: -1, store_runs: true, session_memory: true })
  },
  flowSystemLimits: {
    type: Object,
    default: () => ({ max_timeout: 300, max_exec_count: 20000, max_run_records: 100, max_session_records: 50 })
  },
  flowFlowVariables: {
    type: Object,
    default: () => ({})
  },
  flowEnvironmentVariables: {
    type: Object,
    default: () => ({})
  }
})

const emit = defineEmits(['close', 'updateNode', 'updateNodeData', 'updateArguments', 'updateLimits'])

// 父级 v-if 挂载：挂上即开着。skipWhenEditable：正在行内改节点名 / 变量时，
// ESC 归输入框（取消编辑），别把整个属性面板关掉
useEscClose(true, () => emit('close'), { skipWhenEditable: true })

const nodeLabel = ref('')
const nodeNote = ref('')
const isEditingLabel = ref(false)
const labelInputRef = ref(null)
// 属性面板最小宽度 = 初始宽度：再窄各节点属性表单就会挤成多行。
// 初始宽度固定取下限，不再从 sessionStorage 恢复上次拖拽的宽度。
const MIN_PANEL_WIDTH = 430
const panelWidth = ref(MIN_PANEL_WIDTH)
const isResizing = ref(false)

const showVariableModal = ref(false)
const selectedVariableForModal = ref('')
const currentVariableName = ref('')
const currentFilterType = ref('all')
const currentSelectMode = ref('all')
let variableSelectCallback = null // 使用普通变量而非响应式 ref

// ── Binding 编辑状态（覆盖整个 panel-content） ──
const bindingEditState = ref(null) // { varName, varType, binding }
const isBindingUpdate = ref(false) // 防止 binding 保存时触发 watch 关闭面板

const openBindingEdit = (info) => {
  bindingEditState.value = info
}

const closeBindingEdit = () => {
  bindingEditState.value = null
}

const handleBindingUpdate = ({ varName, binding }) => {
  // 更新节点数据中的 outputs[varName].binding（不关闭面板）
  if (props.selectedNode) {
    const outputs = { ...(props.selectedNode.data?.config?.outputs || {}) }
    if (outputs[varName]) {
      if (binding && (Array.isArray(binding) ? binding.length > 0 : binding.channel)) {
        outputs[varName] = { ...outputs[varName], binding }
      } else {
        const { binding: _, ...rest } = outputs[varName]
        outputs[varName] = rest
      }
      isBindingUpdate.value = true
      emit('updateNodeData', {
        nodeId: props.selectedNode.id,
        data: { config: { outputs } }
      })
      nextTick(() => { isBindingUpdate.value = false })
    }
  }
}

// 向下提供 binding 编辑函数
provide('openBindingEdit', openBindingEdit)
const variablePanelPosition = ref({
  x: 0,
  y: 0,
  width: 200,
  maxHeight: 400,
  fixedOffsetX: 0
})

let animationFrameId = null

let lastBindingNodeId = null
watch(() => props.selectedNode, (node) => {
  // 仅切换节点(id 变化)时关闭 binding 编辑面板。
  // 同一节点的字段保存、容器节点(循环/分支等)的 _needsResize 级联都会替换
  // selectedNode 引用，若按引用判断会误判为"切换节点"把刚打开的绑定面板清空。
  const nodeId = node?.id ?? null
  if (nodeId !== lastBindingNodeId) {
    bindingEditState.value = null
    lastBindingNodeId = nodeId
  }
  if (node) {
    nodeLabel.value = node.data?.label || ''
    nodeNote.value = node.data?.note || ''
  } else {
    nodeLabel.value = ''
    nodeNote.value = ''
  }
}, { immediate: true })

const panelStyle = computed(() => {
  let rightOffset = 0
  if (props.runPanelVisible) {
    rightOffset += props.runPanelWidth + 6
  }
  if (props.variablesPanelVisible) {
    rightOffset += props.variablesPanelWidth + 6
  }
  return {
    right: rightOffset + 'px',
    width: panelWidth.value + 'px'
  }
})

const startResize = (e) => {
  isResizing.value = true
  document.addEventListener('mousemove', onResize)
  document.addEventListener('mouseup', stopResize)
  e.preventDefault()
}

const onResize = (e) => {
  if (!isResizing.value) return
  let offset = 0
  if (props.runPanelVisible) {
    offset += props.runPanelWidth + 3
  }
  if (props.variablesPanelVisible) {
    offset += props.variablesPanelWidth + 3
  }
  const newWidth = window.innerWidth - e.clientX - offset
  panelWidth.value = Math.max(MIN_PANEL_WIDTH, Math.min(newWidth, 1000))
}

const stopResize = () => {
  isResizing.value = false
  document.removeEventListener('mousemove', onResize)
  document.removeEventListener('mouseup', stopResize)
}

onUnmounted(() => {
  document.removeEventListener('mousemove', onResize)
  document.removeEventListener('mouseup', stopResize)
})

// 节点图标与各 *NodeProps 面板共用一张表（nodeIcons.js）
const getNodeIcon = getNodeIconComponent

const startEditLabel = () => {
  isEditingLabel.value = true
  nextTick(() => {
    labelInputRef.value?.focus()
    labelInputRef.value?.select()
  })
}

const finishEditLabel = () => {
  isEditingLabel.value = false
  updateNodeLabel()
}

const cancelEditLabel = () => {
  isEditingLabel.value = false
  if (props.selectedNode) {
    nodeLabel.value = props.selectedNode.data?.label || ''
  }
}

const updateNodeLabel = () => {
  if (props.selectedNode && nodeLabel.value !== props.selectedNode.data?.label) {
    emit('updateNode', {
      ...props.selectedNode,
      data: {
        ...props.selectedNode.data,
        label: nodeLabel.value
      }
    })
  }
}

const updateNodeNote = (event) => {
  const newNote = event.target.innerText.trim()
  if (newNote !== nodeNote.value && newNote !== t('flow.propsPanel.notePlaceholder')) {
    nodeNote.value = newNote
    emit('updateNodeData', {
      nodeId: props.selectedNode.id,
      data: {
        note: newNote
      }
    })
  }
}

const variablePanelStyle = computed(() => ({
  position: 'fixed',
  left: `${variablePanelPosition.value.x}px`,
  top: `${variablePanelPosition.value.y}px`,
  width: `${variablePanelPosition.value.width}px`,
  maxHeight: `${variablePanelPosition.value.maxHeight}px`,
  height: 'auto'
}))

const updateVariablePanelPosition = () => {
  if (!showVariableModal.value) return
  
  const propsPanel = document.querySelector('.props-panel')
  const propsPanelRect = propsPanel ? propsPanel.getBoundingClientRect() : null
  
  if (!propsPanelRect) return
  
  const currentX = variablePanelPosition.value.x
  const currentY = variablePanelPosition.value.y
  const currentWidth = variablePanelPosition.value.width
  
  const fixedOffsetX = variablePanelPosition.value.fixedOffsetX || 0
  const newX = propsPanelRect.left + fixedOffsetX
  const newY = propsPanelRect.top
  const newWidth = Math.max(200, propsPanelRect.right - newX)
  
  const newMaxHeight = propsPanelRect.bottom - propsPanelRect.top
  
  if (Math.abs(currentX - newX) > 1 || Math.abs(currentY - newY) > 1 || Math.abs(currentWidth - newWidth) > 1) {
    variablePanelPosition.value = {
      ...variablePanelPosition.value,
      x: newX,
      y: newY,
      width: newWidth,
      maxHeight: Math.max(100, newMaxHeight)
    }
  }
  
  animationFrameId = requestAnimationFrame(updateVariablePanelPosition)
}

const handleShowVariableSelect = (event, variableName, callback, filterType = 'all', selectMode = 'all') => {
  try {
    if (!event || !event.currentTarget) {
      console.error('Invalid event in handleShowVariableSelect')
      return
    }
    const rect = event.currentTarget.getBoundingClientRect()
    if (!rect || isNaN(rect.left) || isNaN(rect.top) || isNaN(rect.width)) {
      console.error('Invalid bounding rect:', rect)
      return
    }
    const propsPanel = document.querySelector('.props-panel')
    const propsPanelRect = propsPanel ? propsPanel.getBoundingClientRect() : null
    
    let propsPanelBottom = window.innerHeight * 0.98
    let propsPanelTop = 0
    let propsPanelLeft = 0
    if (propsPanelRect) {
      propsPanelBottom = propsPanelRect.bottom
      propsPanelTop = propsPanelRect.top
      propsPanelLeft = propsPanelRect.left
    }
    
    const top = propsPanelTop
    const maxHeight = propsPanelRect ? propsPanelRect.bottom - propsPanelRect.top : propsPanelBottom - top
    const x = rect.left
    
    let width = rect.width
    if (propsPanelRect) {
      width = propsPanelRect.right - rect.left
    }
    
    const panelWidth = Math.max(200, Math.min(500, width))
    const panelMaxHeight = Math.max(100, Math.min(600, maxHeight))
    const panelX = Math.max(0, x)
    const panelY = Math.max(0, top)
    const panelFixedOffsetX = x - propsPanelLeft
    
    variablePanelPosition.value = {
      x: panelX,
      y: panelY,
      width: panelWidth,
      maxHeight: panelMaxHeight,
      fixedOffsetX: panelFixedOffsetX
    }
    currentVariableName.value = variableName
    currentFilterType.value = filterType
    currentSelectMode.value = selectMode
    variableSelectCallback = callback
    selectedVariableForModal.value = ''
    showVariableModal.value = true
    
    animationFrameId = requestAnimationFrame(updateVariablePanelPosition)
  } catch (e) {
    console.error('Error in handleShowVariableSelect:', e)
  }
}

// 向下提供变量选择函数给 binding 配置面板
provide('showVariableSelectForBinding', handleShowVariableSelect)

const handleVariableModalClose = () => {
  showVariableModal.value = false
  selectedVariableForModal.value = ''
  currentSelectMode.value = 'all'
  if (animationFrameId) {
    cancelAnimationFrame(animationFrameId)
    animationFrameId = null
  }
}

const handleVariableSelect = (value) => {
  // 只调用回调函数，由调用者决定如何处理数据
  if (variableSelectCallback) {
    variableSelectCallback(value)
    variableSelectCallback = null
  }
  
  handleVariableModalClose()
}

defineExpose({
  showVariableSelect: handleShowVariableSelect,
  closeVariableModal: handleVariableModalClose
})

const handleUpdateNode = (node) => {
  emit('updateNode', node)
}

const handleUpdateNodeData = (updateData) => {
  emit('updateNodeData', updateData)
}

const handleUpdateArguments = (argumentsData) => {
  emit('updateArguments', argumentsData)
}

const handleUpdateLimits = (limitsData) => {
  emit('updateLimits', limitsData)
}
</script>

<style scoped>
.props-panel {
  position: absolute;
  z-index: 98;
  top: 2%;
  bottom: 2%;
  background: #ffffff;
  border-radius: 8px;
  box-shadow: -4px 0 20px rgba(0, 0, 0, 0.2);
  display: flex;
  flex-direction: column;
  overflow: hidden;
  min-width: 430px;
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
  background: #e5e7eb;
}

.panel-resizer:active {
  background: #9ca3af;
}

.panel-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 6px 16px;
  /* background: linear-gradient(135deg, #10b981 0%, #059669 100%); */
  color: black;
  gap: 12px;
}

.panel-title {
  font-weight: 600;
  font-size: 14px;
}

.node-info {
  display: flex;
  align-items: center;
  gap: 8px;
  flex: 1;
}

.node-icon {
  width: 23px;
  height: 23px;
  border-radius: 8px;
  display: flex;
  align-items: center;
  justify-content: center;
  color: #ffffff;
}

.node-icon.el-icon {
  font-size: 10px;
}

.node-icon.icon-start {
  background-color: #00704A;
}

.node-icon.icon-branch {
  background-color: #00bcd4;
}

.node-icon.icon-wait {
  background-color: #00bcd4;
}

.node-icon.icon-code {
  background-color: #f56565;
}

.node-icon.icon-http {
  background-color: #409eff;
}

.node-icon.icon-process {
  background-color: #00bcd4;
}
.node-icon.icon-end {
  background-color: #dc2626;
}

.node-icon.icon-result {
  background-color: #67c23a;
}

.node-icon.icon-progress {
  background-color: #eea236;
}

.node-icon.icon-exit {
  background-color: #f56565;
}


.node-icon.icon-llm {
  background-color: #409eff;
}

.node-icon.icon-knowledge {
  background-color: #67c23a;
}

.node-icon.icon-agent {
  background-color: #f56c6c;
}

.node-icon.icon-subflow {
  background-color: #667eea;
}

.node-icon.icon-assign {
  background-color: #ff9800;
}

.node-icon.icon-break {
  background-color: #ff9800;
}

.node-icon.icon-continue {
   background-color: #4caf50;
}

.node-icon.icon-return {
  background-color: #9c27b0;
}

.node-icon.icon-function {
  background-color: #667eea;
}

.node-icon.icon-fork {
  background-color: #4bc0c0;
}

.node-icon.icon-call {
  background-color: #ffc107;
}

.node-icon.icon-loop {
  background-color: #7b1fa2;
}

.node-icon.icon-mcp {
  background-color: #e6a23c;
}

.node-icon.icon-rpc {
  background-color: #1890ff;
}

.node-icon.icon-memory {
  background-color: #673ab7;
}

.node-icon.icon-intent {
  background-color: #9c27b0;
}

.node-icon.icon-default {
  background-color: #6b7280;
}

.node-name-display {
  flex: 1;
  font-size: 14px;
  font-weight: 600;
  color: #333;
  padding: 4px 0;
  cursor: text;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.node-name-display:hover {
  color: #1890ff;
}

.node-name-id {
  color: #999;
  font-weight: 400;
}

.node-name-input {
  flex: 1;
  font-size: 14px;
  font-weight: 600;
  color: #333;
  background: transparent;
  border: none;
  outline: none;
  box-shadow: none;
  padding: 4px 0;
  cursor: text;
}

.node-name-input::placeholder {
  color: #999;
}

.close-icon {
  cursor: pointer;
  padding: 4px;
  border-radius: 4px;
  font-size: 18px;
}

.close-icon:hover {
  background: rgba(255, 255, 255, 0.2);
}

.panel-body {
  flex: 1;
  display: flex;
  flex-direction: column;
  overflow: hidden;
  padding: 16px;
  padding-top: 0;
}

.note-edit-wrapper {
  flex-shrink: 0;
  padding-top: 0px;
}

.panel-content {
  flex: 1;
  overflow-y: auto;
  padding-top: 0px;
  border-top: 1px solid #eee;
}

.node-props-inner {
  height: 100%;
}

.on-error-wrapper {
  flex-shrink: 0;
  border-top: 1px solid #eee;
  padding-top: 4px;
}

.default-props {
  height: 100%;
}

.note-edit {
  min-height: 20px;
  padding: 10px 12px;
  /* background: #fffbe6; */
  /* border: 1px dashed #ffc517; */
  /* border-radius: 6px; */
  font-size: 11px;
  color: #777;
  cursor: text;
  /* margin-bottom: 16px; */
  white-space: pre-wrap;
  word-break: break-word;
}

.note-edit:empty:before {
  content: attr(data-placeholder);
  color: #777;
}

.note-edit:focus {
  outline: none;
  border-color: #ffb800;
  background: #fffef0;
}

.node-props {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.prop-group {
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.prop-label {
  font-size: 12px;
  color: #666;
  font-weight: 500;
}

.prop-value {
  font-size: 14px;
  color: #333;
  padding: 8px 12px;
  background: #f8f9fa;
  border-radius: 4px;
}

.prop-input {
  padding: 8px 12px;
  border: 1px solid #e0e0e0;
  border-radius: 4px;
  font-size: 14px;
  outline: none;
}

.prop-input:focus {
  border-color: #10b981;
}

.children-list {
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.child-item {
  display: flex;
  align-items: center;
  padding: 6px 12px;
  background: #f8f9fa;
  border-radius: 4px;
}

.child-index {
  font-size: 12px;
  color: #999;
  margin-right: 8px;
}

.child-label {
  font-size: 14px;
  color: #333;
}

.empty-state {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  height: 100%;
  text-align: center;
}

.empty-state p {
  margin: 4px 0;
  color: #999;
}

.empty-hint {
  font-size: 12px;
  color: #ccc;
}

.variable-select-overlay {
  position: fixed;
  top: 0;
  left: 0;
  right: 0;
  bottom: 0;
  z-index: 999;
  background: transparent;
}

.variable-select-overlay > :deep(.variable-select-panel),
.variable-select-overlay > .variable-select-panel {
  position: absolute;
  overflow: hidden;
}
</style>

<!-- 非 scoped：22 个 *NodeProps 各自的滚动容器与编辑框都在子组件里，scoped 够不到，
     这里用 .props-panel 前缀统一兜住（以后新增的节点类型也自动生效）。
     规则只做一件事：滚动条常驻占位但透明 —— 鼠标不进面板就不出现，进了才显形。 -->
<style>
.props-panel * {
  scrollbar-width: thin;
  scrollbar-color: transparent transparent;
}
/* 用元素自身的 :hover 而不是 .props-panel:hover *：
   鼠标在面板上但没进编辑框时，textarea 不匹配 :hover，框内滚动条就不会冒出来 */
.props-panel *:hover {
  scrollbar-color: #c1c1c1 transparent;
}

/* 旧版 Chrome 不认 scrollbar-color，用 ::-webkit-scrollbar 兜底（两者并存是标准做法） */
.props-panel *::-webkit-scrollbar {
  width: 8px;
  height: 8px;
}
.props-panel *::-webkit-scrollbar-track {
  background: transparent;
}
.props-panel *::-webkit-scrollbar-thumb {
  background: transparent;
  border-radius: 4px;
}
.props-panel *:hover::-webkit-scrollbar-thumb {
  background: #c1c1c1;
}
</style>