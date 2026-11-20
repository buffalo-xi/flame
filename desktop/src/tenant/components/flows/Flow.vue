<template>
  <div ref="flowRootRef" class="flow" @click="handleFlowClick">
    <div class="flow-toolbar">
      <el-tooltip :content="t('flow.canvas.addNode')" placement="right">
        <el-icon class="toolbar-icon" @click.stop="toggleNodePanel"><Plus /></el-icon>
      </el-tooltip>
      <el-tooltip :content="t('flow.canvas.addComment')" placement="right">
        <el-icon class="toolbar-icon" @click.stop="addNode('comment')"><ChatLineSquare /></el-icon>
      </el-tooltip>
      <el-tooltip :content="t('flow.list.testRun')" placement="right">
        <el-icon class="toolbar-icon" @click.stop="toggleRunPanel"><Tools /></el-icon>
      </el-tooltip>
      <el-tooltip :content="t('flow.header.flowVars')" placement="right">
        <el-icon class="toolbar-icon" @click.stop="toggleVariablesPanel('flow')"><ChromeFilled /></el-icon>
      </el-tooltip>
      <el-tooltip :content="t('flow.header.envVars')" placement="right">
        <el-icon class="toolbar-icon" @click.stop="toggleVariablesPanel('environment')"><Notification /></el-icon>
      </el-tooltip>
      <el-tooltip :content="t('flow.header.globalVarsReadonly')" placement="right">
        <el-icon class="toolbar-icon" @click.stop="toggleVariablesPanel('global')"><Key /></el-icon>
      </el-tooltip>
      <el-tooltip :content="t('flow.canvas.more')" placement="right">
        <el-icon class="toolbar-icon"><MoreFilled /></el-icon>
      </el-tooltip>
    </div>

    <NodePanel
      :visible="showNodePanel"
      :left="nodePanelLeft"
      @node-select="handleNodeSelectFromPanel"
      @close="showNodePanel = false"
    />

    <!-- Vue Flow 流程图 -->
    <div class="flow-container">
      <VueFlow
        ref="vueflowRef"
        v-model:nodes="nodes"
        v-model:edges="edges"
        class="vue-flow"
        :node-types="nodeTypes"
        :apply-defaults="false"
        :fit-view-on-init="false"
        :sorting="true"

  :animate="false"
  :drag-behavior="{ attachEdges: false }"
  
        @connect="onConnect"
        @edge-click="onEdgeClick"
        @click="handleCanvasClick"
        @node-click="handleNodeClick"
      >
        <Background />
        <Controls />
        <MiniMap />
      </VueFlow>
    </div>

    <!-- 右键菜单 -->
    <div
      v-if="showContextMenu"
      class="context-menu"
      :style="{ left: contextMenuPosition.x + 'px', top: contextMenuPosition.y + 'px' }"
      @mouseleave="hideContextMenu"
    >
      <div class="context-menu-item" @click.stop="deleteEdge"><el-icon><Delete /></el-icon> {{ t('flow.canvas.deleteEdge') }}</div>
    </div>

    <RunPanel v-if="showRunPanel" :panel-width="runPanelWidth" :flow-id="props.instanceProps?.id" :flow-arguments="flowArguments" @close="showRunPanel = false" @resize="runPanelWidth = $event" />
    <VariablesPanel
      v-if="showVariablesPanel"
      :active-tab="variablesPanelActiveTab"
      :run-panel-visible="showRunPanel"
      :run-panel-width="runPanelWidth"
      :props-panel-visible="showPropsPanel"
      :props-panel-width="propsPanelWidth"
      :width="variablesPanelWidth"
      :flow-variables="flowVariables"
      :environment-variables="environmentVariables"
      @close="showVariablesPanel = false"
      @resize="variablesPanelWidth = $event"
      @update:activeTab="variablesPanelActiveTab = $event"
      @update:flowVariables="handleUpdateFlowVariables"
      @update:environmentVariables="environmentVariables = $event"
    />
    <PropsPanel 
      v-if="showPropsPanel" 
      :selected-node="selectedNode"
      :flow-id="props.instanceProps?.id"
      :run-panel-visible="showRunPanel"
      :run-panel-width="runPanelWidth"
      :variables-panel-visible="showVariablesPanel"
      :variables-panel-width="variablesPanelWidth"
      :flow-arguments="flowArguments"
      :flow-limits="flowLimits"
      :flow-system-limits="systemLimits"
      :flow-flow-variables="flowVariables"
      :flow-environment-variables="environmentVariables"
      @close="showPropsPanel = false"
      @update-node="handleUpdateNode"
      @update-node-data="handleUpdateNodeData"
      @update-arguments="handleUpdateArguments"
      @update-limits="handleUpdateLimits"
    />

  </div>
</template>

<script setup>
import { ref, shallowRef, onMounted, onUnmounted, nextTick, watch, provide, computed } from 'vue'
import { Plus, ChatLineSquare, MoreFilled, Collection, Promotion, User, Connection, RefreshRight, Tools, Operation, Delete, ChromeFilled, Notification, Key } from '@element-plus/icons-vue'
import { ElMessage } from 'element-plus'
import { useI18n } from 'vue-i18n'
import RunPanel from './flow/RunPanel.vue'
import PropsPanel from './flow/PropsPanel.vue'
import VariablesPanel from './flow/VariablesPanel.vue'
import NodePanel from './flow/NodePanel.vue'
import { useEscClose } from '../../composables/useEscClose'
import { createNodeTypes } from './nodeTypes.js'
import { createNodeBuilders } from './nodeFactory.js'
import { VueFlow, useVueFlow } from '@vue-flow/core'
import { Background, Controls, MiniMap } from '@vue-flow/additional-components'
import '@vue-flow/core/dist/style.css'
import random from '@/tenant/utils/random'
import { uniqueLabel } from '@/tenant/utils/labels.js'
import { apiBase } from '@/tenant/config/base.js'
import { getAuthHeader } from '@/tenant/utils/auth.js'
import { useFlowDebugHighlight } from '@/tenant/composables/useFlowDebugHighlight.js'

const props = defineProps({
  instanceProps: {
    type: Object,
    default: () => ({})
  }
})

const emit = defineEmits(['update:diagram', 'dirty-change'])

const { t } = useI18n()

const showNodePanel = ref(false)
const showRunPanel = ref(false)
const showPropsPanel = ref(false)
const showVariablesPanel = ref(false)
const variablesPanelActiveTab = ref('flow')
const flowSubTab = ref('settings')
const vueflowRef = ref(null)
// 编辑器根容器（.flow）：节点面板是它内部的绝对定位元素，鼠标坐标必须换算到它的坐标系
const flowRootRef = ref(null)
const selectedNode = ref(null)
const runPanelWidth = ref(360)
const propsPanelWidth = ref(300)
const variablesPanelWidth = ref(380)
const nodePanelLeft = ref('60px')
const flowArguments = ref(props.instanceProps?.arguments || {})
const flowLimits = ref({
  timeout: props.instanceProps?.timeout ?? -1,
  max_recursion_depth: props.instanceProps?.max_recursion_depth ?? -1,
  max_node_count: props.instanceProps?.max_node_count ?? -1,
  max_exec_count: props.instanceProps?.max_exec_count ?? -1,
  max_run_records: props.instanceProps?.max_run_records ?? -1,
  max_session_records: props.instanceProps?.max_session_records ?? -1
})
const flowVariables = ref(props.instanceProps?.flow_vars || {})
const environmentVariables = ref(props.instanceProps?.env_vars || {})

provide('selectedNode', selectedNode)

const { setNodes, setEdges, updateNodeInternals, setViewport, setCenter, updateNode } = useVueFlow()

// 提前声明 nodes/edges（useVueFlow 的 ref），使下方调试相关的 watch（含 immediate）
// 在 setup 同步执行阶段即可安全访问，避免 const 暂时性死区（TDZ）导致的
// "Cannot access 'nodes' before initialization"
// 注意：getDefaultNodes/getDefaultEdges 为 const 箭头函数（不会 hoist），需在 nodes 声明前先定义
const parseDiagram = (diagram) => {
  if (!diagram) {
    return { nodes: [], edges: [] }
  }
  let nodes = []
  let edges = []
  
  if (typeof diagram === 'object') {
    nodes = diagram.nodes || []
    edges = diagram.edges || []
  } else if (typeof diagram === 'string') {
    const trimmed = diagram.trim()
    if (!trimmed || trimmed === 'null') {
      return { nodes: [], edges: [] }
    }
    try {
      const parsed = JSON.parse(trimmed)
      nodes = parsed.nodes || []
      edges = parsed.edges || []
    } catch {
      return { nodes: [], edges: [] }
    }
  }
  
  // Deep copy nodes and edges to avoid mutating the original props
  const nodesCopy = JSON.parse(JSON.stringify(nodes))
  const edgesCopy = JSON.parse(JSON.stringify(edges))

  // Map snake_case (backend) → camelCase (Vue Flow), with backward compat for old camelCase data
  nodesCopy.forEach(node => {
    if (node.parent_node !== undefined && node.parentNode === undefined) {
      node.parentNode = node.parent_node
    }
    delete node.parent_node
  })
  edgesCopy.forEach(edge => {
    if (edge.source_handle !== undefined && edge.sourceHandle === undefined) {
      edge.sourceHandle = edge.source_handle
    }
    if (edge.target_handle !== undefined && edge.targetHandle === undefined) {
      edge.targetHandle = edge.target_handle
    }
    delete edge.source_handle
    delete edge.target_handle
  })

  // Adjust node positions so they start from near 0,0
  if (nodesCopy.length > 0) {
    const rootNodes = nodesCopy.filter(n => !n.parentNode)
    if (rootNodes.length > 0) {
      let minX = Math.min(...rootNodes.map(n => n.position.x))
      let minY = Math.min(...rootNodes.map(n => n.position.y))
      let offsetX = -minX + 100  // 左偏移 100 像素
      let offsetY = -minY + 30  // 上偏移 50 像素

      nodesCopy.forEach(node => {
        if (!node.parentNode) {
          node.position.x += offsetX
          node.position.y += offsetY
        }
      })
    }
  }
  
  return {
    nodes: nodesCopy,
    edges: edgesCopy
  }
}

function findRootNodeId(node, allNodes) {
  if (!node.parentNode) {
    return node.id
  }
  const parentNode = allNodes.find(n => n.id === node.parentNode)
  if (!parentNode) {
    return node.id
  }
  return findRootNodeId(parentNode, allNodes)
}

const getDefaultNodes = () => {
  const diagram = parseDiagram(props.instanceProps?.diagram)

  if (diagram.nodes.length > 0) {
    console.log('getDefaultNodes: diagram.nodes', diagram.nodes.map(n => ({ id: n.id, parentNode: n.parentNode })))
    const result = diagram.nodes.map(n => ({
      ...n,
      zIndexGroup: findRootNodeId(n, diagram.nodes)
    }))
    console.log('getDefaultNodes: result', result.map(n => ({ id: n.id, zIndexGroup: n.zIndexGroup })))
    return result
  }
  return [
    {
      id: '1',
      data: { label: t('flow.node.defaultLabel.start') },
      position: { x: 100, y: 50 },
      type: 'start',
      zIndexGroup: '1'
    }
  ]
}

const getDefaultEdges = () => {
  const diagram = parseDiagram(props.instanceProps?.diagram)
  return diagram.edges
}

const nodes = ref(getDefaultNodes())
const edges = ref(getDefaultEdges())

// ---- 调试通道（WS 对接后端 /debug/flow/:flowId） ----
import { useFlowDebug } from '@/tenant/composables/useFlowDebug.js'
import { formatError } from '../../../locales'
const flowDebug = useFlowDebug()
flowDebug.setFlowId(props.instanceProps?.id)
flowDebug.loadBreakpoints(props.instanceProps?.id)
// 打开流程时清空残留运行数据（runInfo/nodeResults 为模块级单例，跨流程共享）
flowDebug.resetRunData()
provide('flowDebug', flowDebug)
function openFlowVarsResult() {
  showVariablesPanel.value = true
  variablesPanelActiveTab.value = 'flow'
  flowSubTab.value = 'run'
}
provide('flowSubTab', flowSubTab)
provide('openFlowVarsResult', openFlowVarsResult)
// 工作流 id 变化时（如新建→保存后获得 id），同步 flowId 并重新加载断点
watch(() => props.instanceProps?.id, (id) => {
  flowDebug.setFlowId(id)
  flowDebug.loadBreakpoints(id)
  flowDebug.resetRunData()
})
// 注意：不在进入编辑器时自动 connect。调试通道延迟到用户在试运行面板里点「运行」时，
// 由 RunPanel 调用 flowDebug.run(input, flowId) 触发（run 内部按需 connect）。

// 调试态高亮（执行中 / 断点 / 已运行 / 出错 / 跳过）：实现见 composables/useFlowDebugHighlight.js
useFlowDebugHighlight({ flowDebug, nodes, updateNode })

const groupNodeTypes = new Set(['process', 'loop', 'function'])
provide('groupNodeTypes', groupNodeTypes)

const scriptLanguages = ref(['lua'])
provide('scriptLanguages', scriptLanguages)

const systemLimits = ref({
  max_timeout: 300,
  max_recursion_depth: 50,
  max_node_count: 1000,
  max_exec_count: 20000,
  max_run_records: 100,
  max_session_records: 50
})
provide('systemLimits', systemLimits)

// 右键菜单相关状态
const showContextMenu = ref(false)
const contextMenuPosition = ref({ x: 0, y: 0 })
const selectedEdge = ref(null)

// 添加节点相关状态
const isAddingNode = ref(false)
const currentNodeType = ref(null)
const isFromHandle = ref(false)

const selectedHandleNodeId = ref(null)
const selectedHandleId = ref(null)

// 处理边的更新
const updateEdges = (nodeId, newHandle) => {
  edges.value.forEach(edge => {
    if (edge.source === nodeId) {
      edge.sourceHandle = newHandle
    }
  })
}

// 获取节点信息
const getNodeById = (nodeId) => {
  return nodesMap.value.get(nodeId) || null
}

// 基本的流程图数据
//
// 节点 label 全图唯一：source_ref / call.target / fork_node_id 都能按 label 写，
// 重名会让引用指错节点。改名撞名时自动加 3 位后缀（与拖入节点同一套规则），空名直接忽略。
const updateNodeLabel = (nodeId, newLabel) => {
  const wanted = (newLabel || '').trim()
  if (!wanted) return
  const labelTaken = (label) => nodes.value.some(
    n => n.id !== nodeId && (n.data?.label || '').trim() === label
  )
  const label = uniqueLabel(wanted, labelTaken)
  if (label !== wanted) {
    ElMessage.warning(t('flow.canvas.nodeLabelTaken', { wanted, label }))
  }
  updateNodes(nodes.value.map(n => (
    n.id === nodeId ? { ...n, data: { ...n.data, label } } : n
  )))
}

// 调试：检查初始化的 nodes 和 edges
console.log('Flow nodes initialized:', nodes.value)
console.log('Flow edges initialized:', edges.value)

provide('FlowNodes', nodes)
provide('FlowEdges', edges)
provide('flowFlowVariables', flowVariables)
provide('flowEnvironmentVariables', environmentVariables)
provide('flowArguments', flowArguments)

const nodesMap = ref(createNodesMap(nodes.value))

function generateUniqueId(digits) {
  return random.generateUniqueId(digits, nodes.value, edges.value)
}

function createNodesMap(nodesArray) {
  const map = new Map()
  nodesArray.forEach(node => {
    map.set(node.id, node)
  })
  return map
}

function updateNodes(newNodes) {
  if (!newNodes || !Array.isArray(newNodes)) {
    console.error('Invalid nodes data in updateNodes:', newNodes)
    return
  }
  
  const validNodes = newNodes.filter(node => {
    if (!node || !node.id) {
      console.warn('Skipping invalid node:', node)
      return false
    }
    if (!node.data) {
      node.data = {}
    }
    return true
  })
  
  nodes.value = validNodes
  nodesMap.value = createNodesMap(validNodes)
}

// 存储上次通知调整大小的时间，用于防抖
const lastResizeNotify = ref({})
const resizeObservers = new Map()
const observedNodeIds = new Set()


watch(() => props.instanceProps?.diagram, (newDiagram) => {
  const diagram = parseDiagram(newDiagram)
  // Compare with current nodes/edges to avoid unnecessary updates
  const currentNodesStr = JSON.stringify(nodes.value)
  const newNodesStr = JSON.stringify(diagram.nodes)
  const currentEdgesStr = JSON.stringify(edges.value)
  const newEdgesStr = JSON.stringify(diagram.edges)
  
  // 切换工作流时重置状态
  selectedNode.value = null
  showPropsPanel.value = false
  showNodePanel.value = false
  showRunPanel.value = false
  lastResizeNotify.value = {}
  
  if (diagram.nodes.length > 0) {
    if (currentNodesStr !== newNodesStr) {
      // 需要添加 zIndexGroup，使用 findRootNodeId 处理
      const nodesWithZIndex = diagram.nodes.map(n => ({
        ...n,
        zIndexGroup: findRootNodeId(n, diagram.nodes)
      }))
      updateNodes(nodesWithZIndex)
      // 只写 nodes.value 要靠 v-model 回灌 store，实测「已打开时刷新」这条路径上
      // 画布不跟着变（必须退出重进才正常）；handleUpdateNode 那边就是双写的，这里对齐。
      setNodes(nodesWithZIndex)
    }
    if (currentEdgesStr !== newEdgesStr) {
      edges.value = diagram.edges
      setEdges(diagram.edges)
    }
  } else {
    const defaultNodes = getDefaultNodes()
    const defaultEdges = getDefaultEdges()
    if (currentNodesStr !== JSON.stringify(defaultNodes)) {
      updateNodes(defaultNodes)
      setNodes(defaultNodes)
    }
    if (currentEdgesStr !== JSON.stringify(defaultEdges)) {
      edges.value = defaultEdges
      setEdges(defaultEdges)
    }
  }
  // 坐标 / 容器尺寸都换了：全量重算一次尺寸与 handle，否则边还挂在旧锚点上。
  // 无参调用 = 更新全部节点（Vue Flow 内部按 [] 处理）。
  nextTick(() => {
    updateNodeInternals()
    if (vueflowRef.value) {
      setViewport({ x: 0, y: 0, zoom: 1 }, { duration: 0 })
    }
  })
}, { immediate: true })

// 监听 instanceProps 的变化，更新 flowArguments
watch(() => props.instanceProps?.arguments, (newArgs) => {
  flowArguments.value = newArgs || {}
}, { immediate: true, deep: true })

// 监听 instanceProps 的变化，更新 flowLimits
watch(() => [props.instanceProps?.timeout, props.instanceProps?.max_recursion_depth, props.instanceProps?.max_node_count, props.instanceProps?.max_exec_count, props.instanceProps?.max_run_records, props.instanceProps?.max_session_records], () => {
  flowLimits.value = {
    timeout: props.instanceProps?.timeout ?? -1,
    max_recursion_depth: props.instanceProps?.max_recursion_depth ?? -1,
    max_node_count: props.instanceProps?.max_node_count ?? -1,
    max_exec_count: props.instanceProps?.max_exec_count ?? -1,
    max_run_records: props.instanceProps?.max_run_records ?? -1,
    max_session_records: props.instanceProps?.max_session_records ?? -1
  }
}, { immediate: true })

// 监听 instanceProps 的变化，更新 flowVariables
watch(() => props.instanceProps?.flow_vars, (newFlowVars) => {
  flowVariables.value = newFlowVars || {}
}, { immediate: true, deep: true })

// 监听 instanceProps 的变化，更新 environmentVariables
watch(() => props.instanceProps?.env_vars, (newEnvVars) => {
  environmentVariables.value = newEnvVars || {}
}, { immediate: true, deep: true })


// 监控 nodes 变化，设置或更新 resize observers
watch(nodes, () => {
  nextTick(() => {
    setupResizeObservers()
  })
}, { deep: false })

// 通知所有需要调整大小的父节点
const notifyAllParentsNeedResize = () => {
  const now = Date.now()
  const parentsToUpdate = new Set()

  // 遍历所有有 parentNode 的节点，检查它们的位置和尺寸
  nodes.value.forEach(node => {
    if (node.parentNode) {
      const parentNode = nodes.value.find(n => n.id === node.parentNode)
      if (parentNode) {
        const lastTime = lastResizeNotify.value[parentNode.id] || 0
        if (now - lastTime >= 50) {
          parentsToUpdate.add(parentNode.id)
        }
      }
    }
  })

  // 更新收集到的父节点
  if (parentsToUpdate.size > 0) {
    const newNodes = nodes.value.map(n => {
      if (parentsToUpdate.has(n.id)) {
        lastResizeNotify.value[n.id] = now
        return {
          ...n,
          data: {
            ...n.data,
            _needsResize: now
          }
        }
      }
      return n
    })
    updateNodes(newNodes)
  }
}

// 在特定操作后调用此函数来通知父节点调整大小
const scheduleParentResizeCheck = () => {
  nextTick(() => {
    notifyAllParentsNeedResize()
  })
}

// 检查并设置 ResizeObserver 来监控子节点尺寸变化
const setupResizeObservers = () => {
  // 获取当前所有子节点 id
  const currentChildNodeIds = new Set(
    nodes.value
      .filter(n => n.parentNode)
      .map(n => n.id)
  )

  // 移除不再存在的节点的 observer
  const nodeIdsToRemove = []
  observedNodeIds.forEach(id => {
    if (!currentChildNodeIds.has(id)) {
      nodeIdsToRemove.push(id)
    }
  })

  nodeIdsToRemove.forEach(id => {
    const observer = resizeObservers.get(id)
    if (observer) {
      observer.disconnect()
      resizeObservers.delete(id)
    }
    observedNodeIds.delete(id)
  })

  // 为新的子节点添加 observer
  nodes.value.forEach(node => {
    if (node.parentNode && !observedNodeIds.has(node.id)) {
      const observer = new ResizeObserver(() => {
        // 当子节点尺寸变化时，触发父节点调整
        nextTick(() => {
          notifyAllParentsNeedResize()
        })
      })

      resizeObservers.set(node.id, observer)
      observedNodeIds.add(node.id)

      // 尝试找到并观察 DOM 元素
      nextTick(() => {
        const element = document.querySelector(`[data-id="${node.id}"]`)
        if (element) {
          observer.observe(element)
        }
      })
    }
  })
}

// 清理 resize observers
const cleanupResizeObservers = () => {
  resizeObservers.forEach(observer => observer.disconnect())
  resizeObservers.clear()
  observedNodeIds.clear()
}

// 判断 handle 类型（source 或 target）
const getHandleType = (handleId) => {
  if (!handleId) return null
  // source 类型的 handle ID 包含 "source"
  if (handleId.includes('source')) {
    return 'source'
  }
  return 'target'
}

// 检测是否会形成环
const wouldCreateCycle = (source, target, currentEdges) => {
  // 使用深度优先搜索检测环
  const visited = new Set()
  
  const dfs = (nodeId) => {
    if (nodeId === source) {
      return true
    }
    if (visited.has(nodeId)) {
      return false
    }
    visited.add(nodeId)
    
    // 找到所有从当前节点出发的边
    const outgoingEdges = currentEdges.filter(edge => edge.source === nodeId)
    
    for (const edge of outgoingEdges) {
      if (dfs(edge.target)) {
        return true
      }
    }
    
    return false
  }
  
  // 检查从 target 出发是否能回到 source
  return dfs(target)
}

// 处理连线创建
const onConnect = (params) => {
  // 禁止同一个节点的两个 handle 相连
  if (params.source === params.target) {
    console.log('cannot connect two handles of the same node')
    return
  }

  // 判断 handle 类型
  const sourceHandleType = getHandleType(params.sourceHandle)
  const targetHandleType = getHandleType(params.targetHandle)

  // 禁止 source 类型和 source 类型相连
  if (sourceHandleType === 'source' && targetHandleType === 'source') {
    console.log('cannot connect a source handle to another source handle')
    return
  }

  // 禁止 target 类型和 target 类型相连
  if (sourceHandleType === 'target' && targetHandleType === 'target') {
    console.log('cannot connect a target handle to another target handle')
    return
  }

  // 归一化连接方向：params.source 只是拖拽起点，
  // 若起点是 target handle、终点是 source handle，说明数据流方向相反，需要交换两端
  const isReversed = sourceHandleType === 'target' && targetHandleType === 'source'
  const sourceId = isReversed ? params.target : params.source
  const targetId = isReversed ? params.source : params.target
  const sourceHandle = isReversed ? params.targetHandle : params.sourceHandle
  const targetHandle = isReversed ? params.sourceHandle : params.targetHandle

  // 检查 source 和 target 节点的 parentNode 是否相同
  const sourceNode = nodes.value.find(n => n.id === sourceId)
  const targetNode = nodes.value.find(n => n.id === targetId)
  if (sourceNode && targetNode && sourceNode.parentNode !== targetNode.parentNode) {
    console.log('different parentNode: connection not allowed')
    return
  }

  // 检测是否会形成环（从 source 出发经过 target 能否回到 source）
  if (wouldCreateCycle(sourceId, targetId, edges.value)) {
    console.log('would form a cycle: connection not allowed')
    return
  }
  
  // 如果不存在相同的连线，则添加新连线
  const getHandleBase = (handle) => {
    if (!handle) return ''
    handle = handle.replace('target', '')
    handle = handle.replace('source', '')
    handle = handle.replace('left', '')
    handle = handle.replace('right', '')
    handle = handle.replace('top', '')
    handle = handle.replace('bottom', '')
    return handle
  }
  const sourceBase = getHandleBase(sourceHandle)
  
  let newEdges = [...edges.value]

  // 每个 sourceBase 只允许一个发出边，删除同一 sourceBase 的其他出边
  newEdges = newEdges.filter(edge => {
    const edgeSourceBase = getHandleBase(edge.sourceHandle)
    return !(edge.source === sourceId && edgeSourceBase === sourceBase)
  })

  // 每个目标节点只允许一个接入边，删除目标节点的其他入边
  newEdges = newEdges.filter(edge => edge.target !== targetId)
  
  // 添加新边
  // console.log('onConnect - before push edge, nodes:', JSON.stringify(nodes.value, null, 2))
  
  newEdges.push({
    id: generateUniqueId(5),
    source: sourceId,
    target: targetId,
    sourceHandle,
    targetHandle
  })
  
  // 替换整个数组以触发响应式更新
  edges.value = newEdges
  
  // console.log('onConnect - after push edge, nodes:', JSON.stringify(nodes.value, null, 2))

  // 刷新节点内部状态，确保handle位置正确计算
  nextTick(() => {
    // console.log('onConnect - in nextTick, nodes:', JSON.stringify(nodes.value, null, 2))
    if (vueflowRef.value) {
      // 刷新源节点和目标节点的内部状态
      vueflowRef.value.updateNodeInternals(sourceId)
      vueflowRef.value.updateNodeInternals(targetId)
    }
  })
}

// 处理边的点击事件
const onEdgeClick = (...args) => {
  console.log('onEdgeClick called with args:', args)
  
  // 尝试从不同的参数位置获取edge
  let edge = args[1] || args[0]?.edge
  console.log('Edge obtained:', edge)
  
  // 如果没有edge，或者edge是undefined，不处理
  if (!edge) return
  
  // 设置选中的边
  selectedEdge.value = edge
  console.log('selectedEdge set', selectedEdge.value)
  
  // 设置菜单位置
  // 尝试从不同的属性中获取坐标
  let x = 100
  let y = 100
  
  // 尝试从事件对象中获取坐标
  const event = args[0]
  if (event) {
    // 尝试从originalEvent中获取坐标
    if (event.originalEvent) {
      x = event.originalEvent.clientX || event.originalEvent.pageX || 100
      y = event.originalEvent.clientY || event.originalEvent.pageY || 100
    }
    // 尝试从MouseEvent中获取坐标
    else if (event instanceof MouseEvent) {
      x = event.clientX || event.pageX || 100
      y = event.clientY || event.pageY || 100
    }
    // 尝试从事件对象的其他属性中获取坐标
    else if (event.position) {
      x = event.position.x || 100
      y = event.position.y || 100
    }
    // 尝试从全局鼠标事件中获取坐标
    else {
      // 使用当前鼠标位置
      x = window.event?.clientX || 100
      y = window.event?.clientY || 100
    }
  }
  
  contextMenuPosition.value = {
    x: x,
    y: y
  }
  
  // 显示菜单
  showContextMenu.value = true
  //console.log('showContextMenu', showContextMenu.value)
  //console.log('contextMenuPosition', contextMenuPosition.value)
  // console.log('selectedEdge after showContextMenu', selectedEdge.value)
}

// 删除选中的边
const deleteEdge = () => {
  // console.log('deleteEdge called', selectedEdge.value)
  if (selectedEdge.value) {
    // 从edges数组中移除选中的边
    const index = edges.value.findIndex(edge => edge.id === selectedEdge.value.id)
    // console.log('Found edge at index', index)
    if (index !== -1) {
      edges.value = edges.value.filter((_, i) => i !== index)
      // console.log('Edge deleted', edges.value)
    }
    
    // 隐藏右键菜单
    hideContextMenu()
  }
  else {
    console.log('No edge selected')
  }
}

// 隐藏右键菜单
const hideContextMenu = () => {
  showContextMenu.value = false
  selectedEdge.value = null
}

// 右键菜单也是浮层：ESC 收起（此前只能点别处）
useEscClose(showContextMenu, () => hideContextMenu())

// 点击外部关闭右键菜单
const handleClickOutside = (event) => {
  if (!event.target.closest('.context-menu') && !event.target.closest('.vue-flow__edge')) {
    showContextMenu.value = false
    selectedEdge.value = null
  }
  else {
    console.log('Not closing menu')
  }
}

// 注册点击外部关闭右键菜单的事件监听
onMounted(() => {
  document.addEventListener('click', handleClickOutside)

  // 获取支持的脚本语言
  fetch(`${apiBase}/admin/flow/script-languages`, {
    headers: getAuthHeader()
  })
    .then(res => res.ok ? res.json() : {})
    .then(res => {
      const data = res && res.data
      if (Array.isArray(data) && data.length > 0) {
        scriptLanguages.value = data
      }
    })
    .catch(() => {})

  // 获取系统级工作流运行参数
  fetch(`${apiBase}/admin/flow/limits`, {
    headers: getAuthHeader()
  })
    .then(res => res.ok ? res.json() : null)
    .then(res => {
      const data = res && res.data
      if (data && typeof data === 'object') {
        systemLimits.value = data
      }
    })
    .catch(() => {})

  // 将视口固定在 0,0 位置，不自动 fitView
  nextTick(() => {
    if (vueflowRef.value) {
      setViewport({ x: 0, y: 0, zoom: 1 }, { duration: 0 })
    }
  })

  // 记录初始 diagram 基线（延迟到初始渲染/节点自动调整完成后，避免把自动调整误判为「未保存」）
  setTimeout(() => {
    snapshotDiagram()
  }, 500)

  // 设置 resize observers
  nextTick(() => {
    setupResizeObservers()
  })
})

// 组件卸载时清理资源
onUnmounted(() => {
  cleanupResizeObservers()
  document.removeEventListener('click', handleClickOutside)
  flowDebug.disconnect()
})


// 检查祖先节点中是否存在指定类型的节点
const hasAncestorOfType = (nodeId, targetType) => {
  const node = nodes.value.find(n => n.id === nodeId)
  if (!node) return false
  if (node.type === targetType) return true
  if (node.parentNode) {
    return hasAncestorOfType(node.parentNode, targetType)
  }
  return false
}

// 节点创建（模板数据 + 挂载）：实现见 flow/nodeFactory.js，这里只注入运行时依赖
const { createChildNode, createNode } = createNodeBuilders({ generateUniqueId, hasAncestorOfType, nodes, updateNodes, ElMessage })

// 处理画布点击事件
const handleCanvasClick = (event) => {
  
  if (isAddingNode.value && currentNodeType.value) {
    // 如果是从工具栏点击添加节点，在点击位置创建新节点
    // 获取VueFlow的视口信息
    const viewport = vueflowRef.value?.viewport || { x: 0, y: 0, zoom: 1 }
    
    // 计算点击位置在VueFlow坐标系统中的位置
    const flowContainer = document.querySelector('.vue-flow')
    if (flowContainer) {

      // 判断是否点击到了节点
      if (event.target && event.target.closest('.vue-flow__node')) {
        // 获取点击的节点数据
        const nodeElement = event.target.closest('.vue-flow__node')
        const nodeId = nodeElement?.getAttribute('data-id')
        
        if (nodeId) {
          const node = nodes.value.find(n => n.id === nodeId)
          if (node) {
            // 检查节点类型是否在 groupNodeTypes 中
            if (groupNodeTypes.has(node.type)) {
              console.log('clicked a group node:', node.type)
              // 计算鼠标相对于分组节点左上角的位置
              const nodeRect = nodeElement.getBoundingClientRect()
              const relativeX = event.clientX - nodeRect.left
              const relativeY = event.clientY - nodeRect.top
              
              // 如果相对高度小于50，使用50
              const adjustedY = relativeY < 55 ? 55 : relativeY
              console.log('pointer relative to the group node - X:', relativeX, 'Y:', adjustedY, '(raw Y:', relativeY, ')')
              createChildNode(node.id, currentNodeType.value, relativeX, adjustedY)              
              isAddingNode.value = false
              currentNodeType.value = null
              return
            }
            // 检查节点是否有 parentNode
            if (node.parentNode) {
              console.log('clicked a child node, its parent is:', node.parentNode)
              const parentNode = nodes.value.find(n => n.id === node.parentNode)
              if (parentNode) {
                // 获取父节点的DOM元素
                const parentNodeElement = document.querySelector(`[data-id="${node.parentNode}"]`)
                if (parentNodeElement) {
                  // 计算鼠标相对于父节点左上角的位置
                  const parentNodeRect = parentNodeElement.getBoundingClientRect()
                  const relativeX = event.clientX - parentNodeRect.left
                  const relativeY = event.clientY - parentNodeRect.top
                  
                  // 如果相对高度小于50，使用50
                  const adjustedY = relativeY < 55 ? 55 : relativeY                  
                  console.log('pointer relative to the parent node - X:', relativeX, 'Y:', adjustedY, '(raw Y:', relativeY, ')')
                  createChildNode(parentNode.id, currentNodeType.value, relativeX, adjustedY)                                
                }
              }
              isAddingNode.value = false
              currentNodeType.value = null
              return
            }
          }
        }
      }

      const rect = flowContainer.getBoundingClientRect()
      const x = (event.clientX - rect.left - viewport.x) / viewport.zoom
      const y = (event.clientY - rect.top - viewport.y) / viewport.zoom
      
      // 创建新节点
      createNode(currentNodeType.value, x, y)
    }    
    // 重置添加状态
    isAddingNode.value = false
    currentNodeType.value = null
  }
}

// 处理节点的handle点击事件
const handleNodeHandleClick = (nodeId, handleId, event) => {
  // console.log('handleNodeHandleClick called', nodeId, handleId, event)

  // 保存选中的节点和handle位置
  //if (!handleId.includes('target')) return

  selectedHandleNodeId.value = nodeId
  selectedHandleId.value = handleId

  // 标记当前是从handle点击打开的面板
  isFromHandle.value = true

  // 面板位置：鼠标点在哪个 + 号，就在那儿弹出来。
  //
  // 关键：NodePanel 是 .flow 内的 position:absolute 元素，而鼠标事件给的是**视口坐标**。
  // 两者不同源，必须先减掉 .flow 的 left；编辑器嵌在对话窗口/工作台里（左侧还有会话列表）时，
  // 不换算会整体右偏一个侧栏宽度 —— 正是"位置错误"的来源。
  // 边界也用 .flow 自己的宽度算，而不是 window.innerWidth。
  if (event && typeof event.clientX === 'number') {
    const panelWidth = 300
    const rect = flowRootRef.value?.getBoundingClientRect()
    const rootLeft = rect?.left ?? 0
    const rootWidth = rect?.width ?? window.innerWidth
    const maxLeft = Math.max(0, rootWidth - panelWidth - 6)
    const left = Math.max(0, Math.min(event.clientX - rootLeft, maxLeft))
    nodePanelLeft.value = `${left}px`
  }

  // 显示左侧节点面板
  showNodePanel.value = true
  // console.log('showNodePanel set to', showNodePanel.value)
  // console.log('panelStyle', panelStyle.value)
}

// 处理从 NodePanel 选择节点的事件
const handleNodeSelectFromPanel = (nodeType) => {
  // 如果是从 handle 点击打开面板的情况
  if (isFromHandle.value) {
    addNode(nodeType, 'handle')
  } else {
    // 如果是从工具栏打开面板的情况
    addNode(nodeType, 'toolbar')
  }
}

// 添加新节点
const addNode = (nodeType, source = 'toolbar') => {
  // 根据isFromHandle的值来决定使用什么source参数
  const actualSource = isFromHandle.value ? 'handle' : source
  
  if (actualSource === 'handle') {
    // 如果是点击handle出现的选择窗口，直接增加到handle节点的右边，并进行连线
    if (!selectedHandleNodeId.value) return
    
    // 获取源节点
    const sourceNode = nodes.value.find(n => n.id === selectedHandleNodeId.value)
    if (!sourceNode) return
    
    let newNode
       // 计算新节点的位置
    //假设这是右边的handle被点击
    let x = sourceNode.position.x+sourceNode.dimensions.width + 100
    let y = sourceNode.position.y    
    if (sourceNode.parentNode){
      if (selectedHandleId.value?.includes('bottom')) {
        x = sourceNode.position.x
        y = sourceNode.position.y+sourceNode.dimensions.height + 100      
      }
      if (selectedHandleId.value?.includes('top')) {
        x = sourceNode.position.x+100
        y = sourceNode.position.y - 200      
        if(y<37) y=37
      }
      if (selectedHandleId.value?.includes('left')) {
        x = sourceNode.position.x - 200
        y = sourceNode.position.y      
        if(x<100) x=100
      }
      newNode = createChildNode(sourceNode.parentNode, nodeType, x, y)
    }else{
      if (selectedHandleId.value?.includes('bottom')) {
        x = sourceNode.position.x+100
        y = sourceNode.position.y+sourceNode.dimensions.height + 100      
      }
      if (selectedHandleId.value?.includes('top')) {
        x = sourceNode.position.x+100
        y = sourceNode.position.y- 200      
      }
      if (selectedHandleId.value?.includes('left')) {
        x = sourceNode.position.x - 200
        y = sourceNode.position.y      
      }
      newNode = createNode(nodeType, x, y)
    }     
    // 如果 newNode 为空，返回
    if (newNode && nodeType !== 'function' && nodeType !== 'comment') {
      let newEdge
      // 创建连接边
      if (selectedHandleId.value?.includes('left')) {
        newEdge = {
          id: generateUniqueId(5),
          source: selectedHandleNodeId.value,
          target: newNode.id,
          sourceHandle: selectedHandleId.value,
          targetHandle: 'target'+newNode.id+'right'
        }   
      }else if (selectedHandleId.value?.includes('right')) {
        newEdge = {
          id: generateUniqueId(5),
          source: selectedHandleNodeId.value,
          target: newNode.id,
          sourceHandle: selectedHandleId.value,
          targetHandle: 'target'+newNode.id+'left'
        }
      }else{
        newEdge = {
          id: generateUniqueId(5),
          source: selectedHandleNodeId.value,
          target: newNode.id,
          sourceHandle: selectedHandleId.value,
          targetHandle: 'target'+newNode.id+'top'
        }
      } 

      // 获取 sourceHandle 的 base 值
      const getHandleBase = (handle) => {
        if (!handle) return ''
        let base = handle.replace('target', '')
        base = base.replace('source', '')
        base = base.replace('left', '')
        base = base.replace('right', '')
        base = base.replace('top', '')
        base = base.replace('bottom', '')
        return base
      }
      const sourceBase = getHandleBase(selectedHandleId.value)

      // 删除同一 sourceBase 的其他出边
      const newEdges = edges.value.filter(edge => {
        const edgeSourceBase = getHandleBase(edge.sourceHandle)
        return !(edge.source === selectedHandleNodeId.value && edgeSourceBase === sourceBase)
      })

      // 添加新边到edges数组（使用扩展运算符触发响应式更新）
      edges.value = [...newEdges, newEdge]
      
    }
       
    // 重置选中状态
    selectedHandleNodeId.value = null
    selectedHandleId.value = null
    isFromHandle.value = false
    
    // 隐藏选择窗口
    showNodePanel.value = false
  } else if (actualSource === 'toolbar') {
    // 如果是点击工具栏出现的选择窗口，设置添加状态，等鼠标点击画布时把节点增加到鼠标点击位置
    isAddingNode.value = true
    currentNodeType.value = nodeType
    showNodePanel.value = false
  }
}

const toggleNodePanel = () => {
  showNodePanel.value = !showNodePanel.value
  if (showNodePanel.value) {
    // 从工具栏打开时，恢复默认位置
    nodePanelLeft.value = '60px'
  }
}

const toggleRunPanel = () => {
  showRunPanel.value = !showRunPanel.value
}

// 版本面板与试运行都是右侧浮层：打开版本面板时先把试运行面板收起来，避免叠在一起
const closeRunPanel = () => {
  showRunPanel.value = false
}

const toggleVariablesPanel = (type) => {
  if (!showVariablesPanel.value) {
    showVariablesPanel.value = true
    variablesPanelActiveTab.value = type
  } else {
    if (variablesPanelActiveTab.value === type) {
      showVariablesPanel.value = false
    } else {
      variablesPanelActiveTab.value = type
    }
  }
}

const handleFlowClick = () => {
  if (showNodePanel.value) {
    showNodePanel.value = false
  }
}

const handleNodeClick = (event) => {
  // console.log('handleNodeClick', event.node)
  if (event.node.type === 'entry' || (event.node.type === 'layout' && event.node.parentNode)) {
    const parentNodeId = event.node.parentNode
    const parentNode = getNodeById(parentNodeId)
    if (parentNode) {
      selectedNode.value = parentNode
    }
    const newNodes = nodes.value.map(n => {
      if (n.id === parentNodeId) {
        return { ...n, selected: true }
      } else {
        return { ...n, selected: false }
      }
    })
    updateNodes(newNodes)
    setNodes(newNodes)
    showPropsPanel.value = true
    return
  }   
  if (event.node.type === 'layout') {
    return
  }
  if (event.node.type === 'comment') {
    return
  }
  selectedNode.value = event.node
  const newNodes = nodes.value.map(n => {
    if (n.id === event.node.id) {
      return { ...n, selected: true }
    } else {
      return { ...n, selected: false }
    }
  })
  updateNodes(newNodes)
  setNodes(newNodes)
  showPropsPanel.value = true
}

// 节点在画布坐标系里的绝对位置：容器内子节点存的是相对父容器的坐标。
const absNodePosition = (node) => {
  let x = node.position?.x || 0
  let y = node.position?.y || 0
  let pid = node.parentNode
  let guard = 0
  while (pid && guard++ < 10) {
    const p = nodes.value.find((it) => it.id === pid)
    if (!p) break
    x += p.position?.x || 0
    y += p.position?.y || 0
    pid = p.parentNode
  }
  return { x, y }
}

// 从会话联动过来时定位到某个节点：视口居中 + 选中 + 打开属性面板。
// 刚 fetch 出来的 diagram 可能还没渲染完，所以带重试；entry / 容器内 layout 落到父容器上
// （与点击它们时的行为一致，用户看到的是「那块区域」而不是一个看不见的锚点）。
const focusNode = (nodeId) => {
  const id = String(nodeId || '')
  if (!id) return
  let tries = 0
  const run = () => {
    const n = nodes.value.find((it) => String(it.id) === id)
    if (!n) {
      if (tries++ < 40) setTimeout(run, 100) // 最多等 4s：fetch + 渲染
      return
    }
    const target = (n.type === 'entry' || (n.type === 'layout' && n.parentNode))
      ? (nodes.value.find((it) => it.id === n.parentNode) || n)
      : n

    // 优先用 DOM 实测位置：节点宽高由 CSS 决定，模型里没有
    let cx = null
    let cy = null
    const el = flowRootRef.value?.querySelector(`.vue-flow__node[data-id="${target.id}"]`)
    const pane = flowRootRef.value?.querySelector('.vue-flow__transformationpane')
    const zoom = vueflowRef.value?.viewport?.zoom || 1
    if (el && pane) {
      const er = el.getBoundingClientRect()
      const pr = pane.getBoundingClientRect()
      cx = (er.left + er.width / 2 - pr.left) / zoom
      cy = (er.top + er.height / 2 - pr.top) / zoom
    }
    if (cx === null) {
      const p = absNodePosition(target)
      cx = p.x + (target.width || 160) / 2
      cy = p.y + (target.height || 60) / 2
    }

    if (typeof setCenter === 'function') {
      setCenter(cx, cy, { zoom: 1, duration: 300 })
    } else {
      const canvas = flowRootRef.value?.querySelector('.vue-flow')
      const w = canvas?.clientWidth || 800
      const h = canvas?.clientHeight || 600
      setViewport({ x: w / 2 - cx, y: h / 2 - cy, zoom: 1 }, { duration: 300 })
    }

    const newNodes = nodes.value.map((it) => ({ ...it, selected: String(it.id) === String(target.id) }))
    updateNodes(newNodes)
    setNodes(newNodes)
    selectedNode.value = { ...target, selected: true }
    showPropsPanel.value = true
  }
  run()
}

// 属性面板改名走这里（PropsPanel → emit('updateNode')）。
// 节点 label 全图唯一：撞名自动加 3 位后缀并提示，与拖入节点同一套规则。
const handleUpdateNode = (updatedNode) => {
  const data = { ...updatedNode.data }
  const wanted = (data.label || '').trim()
  if (wanted) {
    const labelTaken = (label) => nodes.value.some(
      n => n.id !== updatedNode.id && (n.data?.label || '').trim() === label
    )
    const label = uniqueLabel(wanted, labelTaken)
    if (label !== wanted) {
      ElMessage.warning(t('flow.canvas.nodeLabelTaken', { wanted, label }))
    }
    data.label = label
  }
  const newNodes = nodes.value.map(n => (
    n.id === updatedNode.id ? { ...n, data } : n
  ))
  updateNodes(newNodes)
  setNodes(newNodes)
}

const handleUpdateNodeData = (updateData) => {
  const newNodes = nodes.value.map(n => {
    if (n.id === updateData.nodeId) {
      const currentData = n.data || {}
      let mergedData = { ...currentData, ...updateData.data }

      // 深度合并 config：全量构造的块（见 REPLACE_CONFIG_KEYS）整体覆盖，其余递归合并。
      // 覆盖规则集中声明在 deepMergeConfig 上方，不再散落特例。
      if (currentData.config && updateData.data.config) {
        mergedData.config = deepMergeConfig(currentData.config, updateData.data.config)

        // 显式传 undefined 的键视为删除（如 loop_vars、全部关闭的 on_error）
        Object.keys(currentData.config).forEach(key => {
          if (key in updateData.data.config && updateData.data.config[key] === undefined) {
            delete mergedData.config[key]
          }
        })
      }

      return {
        ...n,
        data: mergedData
      }
    }
    return n
  })
  updateNodes(newNodes)

  // 同步选中节点引用，否则属性面板子组件（基于 selectedNode 引用的 watch）不会重新加载最新数据
  if (selectedNode.value && selectedNode.value.id === updateData.nodeId) {
    const updated = newNodes.find(n => n.id === updateData.nodeId)
    if (updated) {
      selectedNode.value = updated
    }
  }

  scheduleParentResizeCheck()
}

// 由面板全量构造的 config 键：合并时必须整体覆盖。
// deepMerge 只增不删，残留键会让「关掉的参数 / 切换类型后的旧字段」重新生效；
// 变量容器（inputs/targets/locals/loop_vars/params）同理，删掉的变量不能留。
// 注意：按类型只写部分键的块（如 loop：count 只写 total_times、each 只写 source_key）不要放进来，
// 否则切回原类型会丢配置。
const REPLACE_CONFIG_KEYS = new Set([
  // 变量容器 / 固定输出
  'inputs', 'targets', 'locals', 'loop_vars', 'params', 'outputs',
  // 错误处理
  'on_error',
  // 各类节点的行为块（面板每次 emit 都写全）
  'llm', 'rpc', 'mcp', 'code', 'http', 'branch'
])

// 深度合并 config（plain object 递归，数组直接覆盖）。
// REPLACE_CONFIG_KEYS 只作用于 config 第一层：嵌套对象里的同名键（如脚本入参里的 inputs）
// 仍然按普通字段处理，避免误伤。
const deepMergeConfig = (base, override, isRoot = true) => {
  const result = { ...base }
  Object.keys(override).forEach(key => {
    const ov = override[key]
    const bv = result[key]
    if (isRoot && REPLACE_CONFIG_KEYS.has(key)) {
      result[key] = ov
    } else if (ov && typeof ov === 'object' && !Array.isArray(ov) &&
        bv && typeof bv === 'object' && !Array.isArray(bv)) {
      result[key] = deepMergeConfig(bv, ov, false)
    } else {
      result[key] = ov
    }
  })
  return result
}

const handleUpdateArguments = (argumentsData) => {
  flowArguments.value = argumentsData
}

const handleUpdateLimits = (limitsData) => {
  flowLimits.value = limitsData
}

const handleUpdateFlowVariables = (flowVariablesData) => {
  flowVariables.value = flowVariablesData
}

const getFlowVariables = () => {
  return flowVariables.value
}

const getEnvironmentVariables = () => {
  return environmentVariables.value
}

const getDiagramData = () => {
  return {
    nodes: nodes.value.map(node => {
      const { _needsResize, ...data } = node.data
      return {
        id: node.id,
        data,
        position: node.position,
        type: node.type,
        ...(node.parentNode && { parent_node: node.parentNode }),
        ...(node.extent && { extent: node.extent })
      }
    }),
    edges: edges.value.map(edge => ({
      id: edge.id,
      source: edge.source,
      target: edge.target,
      source_handle: edge.sourceHandle,
      target_handle: edge.targetHandle
    }))
  }
}

// 稳定序列化（排序对象键，避免字段顺序影响比较）
function stableStringify(obj) {
  if (Array.isArray(obj)) return '[' + obj.map(stableStringify).join(',') + ']'
  if (obj && typeof obj === 'object') {
    return '{' + Object.keys(obj).sort()
      .map(k => JSON.stringify(k) + ':' + stableStringify(obj[k])).join(',') + '}'
  }
  return JSON.stringify(obj)
}

// 一次保存真正会提交的内容：diagram + 入参 + 流程/环境变量 + 运行限额。
// 「未保存」必须按这份整体比对 —— 只比 diagram 会漏掉参数编辑（改完不显示未保存、
// 运行前也不会自动保存，后端跑的还是库里那份旧入参）。
// 断点刻意不算：它每设一次就即时落库，算进来会让 header 一直挂着「未保存」。
function buildSavedState() {
  return stableStringify({
    diagram: getDiagramData(),
    arguments: flowArguments.value || {},
    flow_vars: flowVariables.value || {},
    env_vars: environmentVariables.value || {},
    limits: flowLimits.value || {}
  })
}

// 基线快照：进入编辑器时记录一次，每次保存成功后刷新（内容见 buildSavedState）
const baselineDiagram = ref('')
// 是否有未保存修改
const isDirty = computed(() => {
  return buildSavedState() !== baselineDiagram.value
})
// 上报「未保存修改」状态给父组件（用于 header 顶部提示）
watch(isDirty, (val) => {
  emit('dirty-change', val)
})
// 记录「当前这份内容」为已保存基线（不只是画布，见 buildSavedState）
function snapshotDiagram() {
  baselineDiagram.value = buildSavedState()
}

const getFlowArguments = () => {
  return flowArguments.value
}

// 运行前自动保存工作流（完整字段，与 flows.handleSave 对齐），
// 保证后端读到最新 diagram，且不会因全量覆盖清空 name/arguments 等字段。
// 供 RunPanel 在点「运行」时先调用，避免跑的是旧图。
const saveDiagram = async () => {
  const flowId = props.instanceProps?.id
  if (!flowId) return true // 无 id（尚未创建）时跳过，由上层 handleSave 处理
  try {
    const diagramData = getDiagramData()
    const flowArguments = getFlowArguments()
    const flowLimits = getFlowLimits()
    const flowVariables = getFlowVariables()
    const environmentVariables = getEnvironmentVariables()
    const resp = await fetch(`${apiBase}/admin/flows/${flowId}`, {
      method: 'PUT',
      headers: {
        'Content-Type': 'application/json',
        ...getAuthHeader()
      },
      body: JSON.stringify({
        name: props.instanceProps?.name || '',
        diagram: JSON.stringify(diagramData),
        description: props.instanceProps?.description || '',
        arguments: flowArguments,
        breakpoints: flowDebug.breakpointIds(),
        env_vars: environmentVariables,
        flow_vars: flowVariables,
        timeout: flowLimits.timeout ?? -1,
        max_recursion_depth: flowLimits.max_recursion_depth ?? -1,
        max_node_count: flowLimits.max_node_count ?? -1,
        max_exec_count: flowLimits.max_exec_count ?? -1,
        max_run_records: flowLimits.max_run_records ?? -1,
        max_session_records: flowLimits.max_session_records ?? -1
      })
    })
    // 注意：后端业务错误的响应也是 HTTP 200——成败看 body.code，具体编码看 body.error。
    // 因此不能只看 resp.ok，必须解析 body 检查 code === 200。
    const data = await resp.json()
    if (data.code !== 200) {
      ElMessage.error(t('flow.canvas.saveBeforeRunFailed', { msg: formatError(data) || t('flow.canvas.unknownError') }))
      return false
    }
    snapshotDiagram()
    return true
  } catch (e) {
    ElMessage.error(t('flow.canvas.saveBeforeRunFailed', { msg: e?.message || e }))
    return false
  }
}
// 运行前自动保存画布的能力，供 RunPanel 点「运行」时调用
provide('saveDiagram', saveDiagram)
// 未保存修改状态，供 RunPanel 判断保存失败时是否阻断运行
provide('isDirty', isDirty)

const getFlowLimits = () => {
  return flowLimits.value
}

const deleteNode = (nodeId) => {  const node = nodes.value.find(n => n.id === nodeId)
  if (!node) return
  
  const nodesToDelete = new Set([nodeId])
  const deleteGroupChildren = (parentId) => {
    const children = nodes.value.filter(n => n.parentNode === parentId)
    children.forEach(child => {
      nodesToDelete.add(child.id)
      if (groupNodeTypes.has(child.type)) {
        deleteGroupChildren(child.id)
      }
    })
  }
  
  if (groupNodeTypes.has(node.type)) {
    deleteGroupChildren(nodeId)
  }
  
  const newNodes = nodes.value.filter(n => !nodesToDelete.has(n.id))
  updateNodes(newNodes)
  scheduleParentResizeCheck()
  
  edges.value = edges.value.filter(edge => 
    !nodesToDelete.has(edge.source) && !nodesToDelete.has(edge.target)
  )
  
  if (nodesToDelete.has(selectedNode.value?.id)) {
    selectedNode.value = null
    showPropsPanel.value = false
  }

  // 清理被删节点的断点（本地 + WS 同步 + 落库）
  flowDebug.removeBreakpoints([...nodesToDelete])
}

const onBranchDeleteChild = (childId) => {
  const leftHandle = 'source' + childId + 'left'
  const rightHandle = 'source' + childId + 'right'

  edges.value = edges.value.filter(edge => {
    return edge.sourceHandle !== leftHandle && edge.sourceHandle !== rightHandle
  })
}

defineExpose({
  getDiagramData,
  getFlowArguments,
  getFlowLimits,
  getFlowVariables,
  getEnvironmentVariables,
  toggleRunPanel,
  closeRunPanel,
  toggleVariablesPanel,
  snapshotDiagram,
  focusNode
})
// 节点类型映射：26 个节点组件只在映射里用到，实现见 flow/nodeTypes.js
const nodeTypes = createNodeTypes({ deleteNode, getNodeById, handleNodeHandleClick, handleUpdateNodeData, onBranchDeleteChild, updateEdges, updateNodeLabel })

</script>

<style scoped>
.flow {
  position: relative;
  width: 100%;
  height: 100%;
  margin: 0;
  padding: 0;
  overflow: hidden;
}

.flow-container {
  width: 100%;
  height: 100%;
  position: relative;
  z-index: 1;
  margin: 0;
  padding: 0;
  overflow: hidden;
}

.vue-flow {
  width: 100%;
  height: 100%;
  margin: 0;
  padding: 0;
  position: absolute;
  top: 0;
  left: 0;
}

.flow-toolbar {
  position: absolute;
  left: 10px;
  top: 39%;
  transform: translateY(-50%);
  display: flex;
  flex-direction: column;
  gap: 12px;
  padding: 12px 2px;
  background-color: #ffffff;
  border-radius: 8px;
  box-shadow: 0 2px 12px rgba(0, 0, 0, 0.1);
  z-index: 1000;
}

.dirty-indicator {
  font-size: 11px;
  color: #fa8c16;
  white-space: nowrap;
  text-align: center;
  padding: 4px 0;
  cursor: default;
}

.toolbar-icon {
  font-size: 32px;
  color: #666;
  cursor: pointer;
  padding: 8px;
  border-radius: 4px;
  transition: all 0.2s;
}

.toolbar-icon:hover {
  background-color: #f0f2f5;
  color: #1890ff;
}

/* 右键菜单样式 */
.context-menu {
  position: fixed;
  background-color: #ffffff;
  border-radius: 4px;
  box-shadow: 0 2px 12px rgba(0, 0, 0, 0.15);
  z-index: 10000;
  min-width: 100px;
  padding: 4px 0;
}

.context-menu-item {
  padding: 8px 16px;
  cursor: pointer;
  font-size: 14px;
  color: #333;
  transition: background-color 0.2s;
  display: flex;
  align-items: center;
  gap: 8px;
}

.context-menu-item:hover {
  background-color: #f0f2f5;
  color: #1890ff;
}

.context-menu-item:active {
  background-color: #e6f7ff;
}

/* 选择节点窗口样式 */
.node-select-window {
  position: fixed;
  background-color: #ffffff;
  border-radius: 8px;
  box-shadow: 0 2px 12px rgba(0, 0, 0, 0.15);
  z-index: 10000;
  min-width: 200px;
  overflow: hidden;
}

.node-select-header {
  padding: 12px 16px;
  background-color: #f5f5f5;
  font-size: 14px;
  font-weight: 600;
  color: #333;
  border-bottom: 1px solid #e0e0e0;
}

.node-select-content {
  padding: 8px 0;
}

.node-select-item {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 8px 16px;
  cursor: pointer;
  transition: background-color 0.2s;
}

.node-select-item:hover {
  background-color: #f0f2f5;
  color: #1890ff;
}

.node-select-item .icon-wrapper {
  margin-right: 8px;
}
</style>

<style>
.vue-flow__edges {
  z-index: 1000000000 !important;
}

/* 调试：已运行过节点（绿色边框，最低优先级） */
.vue-flow__node.debug-visited > * {
  box-shadow: 0 0 0 1px #52c41a !important;
  border-radius: 8px;
}
/* 调试：当前执行/挂起节点（黄色边框，优先级高于已运行） */
.vue-flow__node.debug-active > * {
  box-shadow: 0 0 0 1px #faad14 !important;
  border-radius: 8px;
}
/* 调试：断点挂起节点右上角 pause 图标（用 ::before 避免与断点红点 ::after 冲突） */
.vue-flow__node.debug-active::before {
  content: '❚❚';
  position: absolute;
  top: -8px;
  right: -8px;
  width: 14px;
  height: 14px;
  line-height: 14px;
  text-align: center;
  font-size: 9px;
  font-weight: 700;
  color: #fa541c;
  background: #fff;
  border: 2px solid #fa541c;
  border-radius: 50%;
  z-index: 22;
}
/* 调试：当前执行中节点（黄色边框 + 右上角旋转 loading 图标）。
   debug-running 与 debug-active 互斥：运行态转圈，断点挂起态转 pause。 */
.vue-flow__node.debug-running > * {
  box-shadow: 0 0 0 1px #faad14 !important;
  border-radius: 8px;
}
.vue-flow__node.debug-running::before {
  content: '';
  position: absolute;
  top: -8px;
  right: -8px;
  width: 14px;
  height: 14px;
  border: 3px solid #ffd591;
  border-top-color: #fa541c;
  border-radius: 50%;
  background: #fff;
  animation: dbg-spin 0.7s linear infinite;
  z-index: 22;
}
@keyframes dbg-spin {
  to { transform: rotate(360deg); }
}
/* 调试：跳过节点（褐色边框，优先级高于已运行） */
.vue-flow__node.debug-skipped > * {
  box-shadow: 0 0 0 1px #a67c52 !important;
  border-radius: 8px;
}
/* 调试：跳过节点右下角跳过图标（褐底 + 白色箭头）。
   用 ::before 避免与断点红点 has-breakpoint::after（右上角）冲突 */
.vue-flow__node.debug-skipped::before {
  content: '↷';
  position: absolute;
  bottom: -6px;
  right: -6px;
  width: 16px;
  height: 16px;
  line-height: 14px;
  text-align: center;
  font-size: 12px;
  font-weight: 700;
  color: #fff;
  background: #a67c52;
  border: 2px solid #fff;
  border-radius: 50%;
  box-shadow: 0 0 4px rgba(166, 124, 82, .5);
  z-index: 22;
}
/* 调试：出错节点（红色边框，优先级最高） */
.vue-flow__node.debug-error > * {
  box-shadow: 0 0 0 1px #f56c6c !important;
  border-radius: 8px;
}
/* 调试：出错节点右下角错误图标（红色圆底 + 白色感叹号）。
   用 ::before 而非 ::after，避免与断点红点 has-breakpoint::after（右上角）冲突。
   与 debug-skipped::before 共用 ::before，但错误与跳过互斥，且本规则在跳过规则之后，故错误优先。 */
.vue-flow__node.debug-error::before {
  content: '!';
  position: absolute;
  bottom: -6px;
  right: -6px;
  width: 16px;
  height: 16px;
  line-height: 16px;
  text-align: center;
  font-size: 12px;
  font-weight: 700;
  color: #fff;
  background: #f56c6c;
  border: 2px solid #fff;
  border-radius: 50%;
  box-shadow: 0 0 4px rgba(245, 108, 108, .6);
  z-index: 23;
}
/* 调试：断点红点（也可由各节点组件自渲染 .bp-dot 覆盖） */
.vue-flow__node.has-breakpoint::after {
  content: '';
  position: absolute;
  top: -5px;
  right: -5px;
  width: 12px;
  height: 12px;
  border-radius: 50%;
  background: #f56c6c;
  border: 2px solid #fff;
  box-shadow: 0 0 4px rgba(245, 108, 108, .6);
  z-index: 20;
}
/* 调试：节点内断点红点（推荐由节点组件渲染，独立于 class 方案） */
.bp-dot {
  position: absolute;
  top: -5px;
  right: -5px;
  width: 12px;
  height: 12px;
  border-radius: 50%;
  background: #f56c6c;
  border: 2px solid #fff;
  box-shadow: 0 0 4px rgba(245, 108, 108, .6);
  z-index: 20;
}
.vue-flow__node { 
  top: var(--vf-node-y) !important;
  left: var(--vf-node-x) !important;
}
.vue-flow__viewport {
  transform-origin: 0 0 !important;
}
.vue-flow__pane {
  transform-origin: 0 0 !important;
}
</style>



