<template>
  <div class="variable-select-panel" @click.stop :style="$attrs.style">
    <div class="panel-header">
      <div class="search-box">
        <el-icon class="search-icon"><Search /></el-icon>
        <input 
          v-model="searchKeyword" 
          type="text" 
          class="search-input" 
          :placeholder="t('flow.varSource.searchPlaceholder')"
        />
      </div>
    </div>
    
    <div class="panel-body">
      <!-- target 模式：显示祖先容器的 outputs + locals，以及流程变量（三者都可写）；
           env / argument / builtin 是只读输入，不在此模式显示 -->
      <template v-if="selectMode === 'target'">
        <div
          v-for="item in parentNodesWithVariables"
          :key="'parent-' + item.node.id"
          class="variable-group"
        >
          <div class="group-header" @click="toggleGroup('parent-' + item.node.id)">
            <div class="node-icon" :class="getNodeIconClass(item.node.type)">
              <el-icon><component :is="getNodeIcon(item.node.type)" /></el-icon>
            </div>
            <span class="group-title">{{ item.node.data.label || item.node.id }}</span>
            <el-icon class="collapse-icon" :class="{ 'expanded': expandedGroups['parent-' + item.node.id] !== false }">
              <ArrowDown />
            </el-icon>
          </div>
          <div v-if="expandedGroups['parent-' + item.node.id] !== false" class="variable-list">
            <div
              v-for="(v, key) in item.variables"
              :key="'parent-var-' + item.node.id + '-' + key"
              class="variable-item flow-variable-item"
              @click="selectVariable('node', { nodeId: item.node.id, key, type: v.type })"
            >
              <span class="type-badge" :class="getTypeClass(v.type)">
                {{ getTypeIcon(v.type) }}
              </span>
              <span class="variable-name" :class="{ 'local-variable': v.source === 'local' }">{{ key }}</span>
              <span class="variable-type">{{ getTypeLabel(v.type) }}</span>
            </div>
          </div>
        </div>
      </template>

      <!-- 原有上游节点变量（非 target 模式） -->
      <template v-else>
      <div
        v-for="node in upstreamNodesWithVariables"
        :key="'upstream-' + node.id"
        class="variable-group"
      >
        <div class="group-header" @click="toggleGroup('upstream-' + node.id)">
          <div class="node-icon" :class="getNodeIconClass(node.type)">
            <el-icon><component :is="getNodeIcon(node.type)" /></el-icon>
          </div>
          <span class="group-title">{{ node.data.label || node.id }}</span>
          <el-icon class="collapse-icon" :class="{ 'expanded': expandedGroups['upstream-' + node.id] }">
            <ArrowDown />
          </el-icon>
        </div>
        <div v-if="expandedGroups['upstream-' + node.id]" class="variable-list">
          <div
            v-for="(output, key) in getFilteredNodeVariables(node)"
            :key="'node-' + node.id + '-' + key"
            class="variable-item flow-variable-item"
            @click="selectVariable('node', { nodeId: node.id, key, type: output.type })"
          >
            <span class="type-badge" :class="getVariableTypeClass(output)">
              {{ getVariableTypeIcon(output) }}
            </span>
            <span class="variable-name" :class="{ 'local-variable': output.variableSource === 'local', 'loop-variable': output.variableSource === 'loop' }">{{ key }}</span>
            <span class="variable-type">{{ getVariableTypeLabel(output) }}</span>
          </div>
        </div>
      </div>
      </template>

      <div v-if="selectMode !== 'target' && upstreamFunctionParams.length > 0" class="variable-group">
        <div class="group-header" @click="toggleGroup('param')">
          <el-icon class="group-icon"><ScaleToOriginal /></el-icon>
          <span class="group-title">{{ t('flow.common.functionParams') }}</span>
          <el-icon class="collapse-icon" :class="{ 'expanded': expandedGroups.param }">
            <ArrowDown />
          </el-icon>
        </div>
        <div v-if="expandedGroups.param" class="variable-list">
          <div
            v-for="(param, index) in upstreamFunctionParams"
            :key="'param-' + index"
            class="variable-item flow-variable-item"
            @click="selectVariable('node', { nodeId: param.nodeId, key: param.key, type: param.type })"
          >
            <span class="type-badge" :class="getTypeClass(param.type)">
              {{ getTypeIcon(param.type) }}
            </span>
            <span class="variable-name param-variable">{{ param.key }}</span>
            <span class="variable-type">
              {{ getTypeLabel(param.type) }}
              <span v-if="param.required" style="color: #f56c6c; margin-left: 4px;">*</span>
            </span>
          </div>
        </div>
      </div>

      <div v-if="Object.keys(filteredFlowVariables).length > 0" class="variable-group">
        <div class="group-header" @click="toggleGroup('flow')">
          <el-icon class="group-icon"><ChromeFilled /></el-icon>
          <span class="group-title">{{ t('flow.varSource.flowVars') }}</span>
          <el-icon class="collapse-icon" :class="{ 'expanded': expandedGroups.flow }">
            <ArrowDown />
          </el-icon>
        </div>
        <div v-if="expandedGroups.flow" class="variable-list">
          <div
            v-for="(config, key) in filteredFlowVariables"
            :key="'flow-' + key"
            class="variable-item flow-variable-item"
            @click="selectVariable('flow', { key, type: config.type })"
          >
            <span class="type-badge" :class="getTypeClass(config.type)">
              {{ getTypeIcon(config.type) }}
            </span>
            <span class="variable-name">{{ key }}</span>
            <span class="variable-type">{{ getTypeLabel(config.type) }}</span>
          </div>
        </div>
      </div>

      <div v-if="selectMode !== 'target' && Object.keys(filteredEnvironmentVariables).length > 0" class="variable-group">
        <div class="group-header" @click="toggleGroup('env')">
          <el-icon class="group-icon"><Notification /></el-icon>
          <span class="group-title">{{ t('flow.varSource.envVars') }}</span>
          <el-icon class="collapse-icon" :class="{ 'expanded': expandedGroups.env }">
            <ArrowDown />
          </el-icon>
        </div>
        <div v-if="expandedGroups.env" class="variable-list">
          <div
            v-for="(config, key) in filteredEnvironmentVariables"
            :key="'env-' + key"
            class="variable-item flow-variable-item"
            @click="selectVariable('env', { key, type: config.type })"
          >
            <span class="type-badge" :class="getTypeClass(config.type)">
              {{ getTypeIcon(config.type) }}
            </span>
            <span class="variable-name">{{ key }}</span>
            <span class="variable-type">{{ getTypeLabel(config.type) }}</span>
          </div>
        </div>
      </div>

      <!-- 全局变量（租户级）：与环境变量、流程变量一样是输入来源，值在「设置 → 全局变量」里 -->
      <div v-if="selectMode !== 'target' && filteredGlobalVars.length > 0" class="variable-group">
        <div class="group-header" @click="toggleGroup('global')">
          <el-icon class="group-icon"><Collection /></el-icon>
          <span class="group-title">{{ t('flow.varSource.globalVars') }}</span>
          <el-icon class="collapse-icon" :class="{ 'expanded': expandedGroups.global }">
            <ArrowDown />
          </el-icon>
        </div>
        <div v-if="expandedGroups.global" class="variable-list">
          <div
            v-for="gv in filteredGlobalVars"
            :key="'global-' + gv.name"
            class="variable-item flow-variable-item"
            :title="gv.description || ''"
            @click="selectVariable('global', { key: gv.name, type: globalVarType(gv.kind) })"
          >
            <span class="type-badge" :class="getTypeClass(globalVarType(gv.kind))">
              {{ getTypeIcon(globalVarType(gv.kind)) }}
            </span>
            <span class="variable-name">{{ gv.name }}</span>
            <span class="variable-type">{{ getTypeLabel(globalVarType(gv.kind)) }}</span>
          </div>
        </div>
      </div>

      <div v-if="selectMode !== 'target' && parsedArguments.length > 0" class="variable-group">
        <div class="group-header" @click="toggleGroup('argument')">
          <el-icon class="group-icon"><User /></el-icon>
          <span class="group-title">{{ t('flow.varSource.entryArgs') }}</span>
          <el-icon class="collapse-icon" :class="{ 'expanded': expandedGroups.argument }">
            <ArrowDown />
          </el-icon>
        </div>
        <div v-if="expandedGroups.argument" class="variable-list">
          <div
            v-for="(arg, index) in parsedArguments"
            :key="'arg-' + index"
            class="variable-item flow-variable-item"
            @click="selectVariable('argument', { key: arg.key, type: arg.type })"
          >
            <span class="type-badge" :class="getTypeClass(arg.type)">
              {{ getTypeIcon(arg.type) }}
            </span>
            <span class="variable-name">{{ arg.key }}</span>
            <span class="variable-type">
              {{ getTypeLabel(arg.type) }}
              <span v-if="arg.required" style="color: #f56c6c; margin-left: 4px;">*</span>
            </span>
          </div>
        </div>
      </div>

      <!-- 运行上下文（builtin 源）：与拓扑无关，任何位置的节点都能引用。
           固定放在分组末尾 —— 它是本次运行的常量，不是「上游节点的产出」，
           优先级排在所有变量数据之后，需要的人再展开（缺省收起） -->
      <div v-if="selectMode !== 'target' && filteredBuiltinVars.length > 0" class="variable-group">
        <div class="group-header" @click="toggleGroup('builtin')">
          <el-icon class="group-icon"><Tools /></el-icon>
          <span class="group-title">{{ t('flow.varSource.runContext') }}</span>
          <el-icon class="collapse-icon" :class="{ 'expanded': expandedGroups.builtin }">
            <ArrowDown />
          </el-icon>
        </div>
        <div v-if="expandedGroups.builtin" class="variable-list">
          <div
            v-for="bv in filteredBuiltinVars"
            :key="'builtin-' + bv.key"
            class="variable-item flow-variable-item"
            :title="t(`flow.builtinVar.${bv.key}.hint`)"
            @click="selectVariable('builtin', { key: bv.key, type: bv.type })"
          >
            <span class="type-badge" :class="getTypeClass(bv.type)">
              {{ getTypeIcon(bv.type) }}
            </span>
            <span class="variable-name">{{ bv.key }}</span>
            <span class="variable-type">{{ t(`flow.builtinVar.${bv.key}.label`) }}</span>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref, computed, watch, h, onMounted } from 'vue'
import { Connection, ChromeFilled, Notification, User, Search, ArrowDown, Timer, Help, Link, MagicStick, Lightning, Promotion, RefreshRight, CircleClose, ChatLineSquare, Collection, Operation, Coin, Minus, Upload, CircleCheck, ScaleToOriginal, Monitor, Mouse, RefreshLeft, Tools } from '@element-plus/icons-vue'
import { BUILTIN_VARS } from './builtinVars.js'
import { getNodeIconComponent as getNodeIcon, getNodeIconClass } from './nodeIcons.js'
import { getDataTypeIcon, getDataTypeIconClass, getDataTypeLabel } from './dataTypeBadge.js'
import { useEscClose } from '../../../composables/useEscClose'
import adminApi from '../../../config/admin.js'
import { authHeaders, checkBizOk } from '../../settings/http.js'
import { useI18n } from 'vue-i18n'

const { t } = useI18n()

const props = defineProps({
  modelValue: {
    type: String,
    default: ''
  },
  nodeId: {
    type: [String, Number],
    required: true
  },
  nodes: {
    type: Array,
    default: () => []
  },
  edges: {
    type: Array,
    default: () => []
  },
  flowVariables: {
    type: Object,
    default: () => ({})
  },
  environmentVariables: {
    type: Object,
    default: () => ({})
  },
  arguments: {
    type: Object,
    default: () => ({})
  },
  filterType: {
    type: String,
    default: 'all',
    validator: (value) => {
      return ['all', 'array', 'non-array'].includes(value)
    }
  },
  selectMode: {
    type: String,
    default: 'all',
    validator: (value) => {
      return ['all', 'target'].includes(value)
    }
  }
})

const emit = defineEmits(['update:modelValue', 'change', 'close'])

// 父级 v-if 挂载：挂上即开着。搜索框里打字时 ESC 也直接收起（这是选择器，不是编辑区）
useEscClose(true, () => emit('close'))

const searchKeyword = ref('')

// 各分组的展开/收起状态
const expandedGroups = ref({
  upstream: true,
  flow: true,
  env: true,
  global: true,
  argument: true,
  param: true,
  // 运行上下文缺省收起：大多数节点不需要它，展开只留给真正要用的少数
  builtin: false
})

const isArrayType = (type) => {
  return typeof type === 'string' && type.startsWith('array[')
}

const filterByType = (type) => {
  if (props.filterType === 'all') return true
  if (props.filterType === 'array') return isArrayType(type)
  if (props.filterType === 'non-array') return !isArrayType(type)
  return true
}

// 监听 props.nodes 和 props.edges 的变化
watch([() => props.nodes, () => props.edges], () => {
  console.log('VariableSelect watch - props.nodes:', props.nodes)
  console.log('VariableSelect watch - props.edges:', props.edges)
}, { immediate: true, deep: true })

const toggleGroup = (groupName) => {
  expandedGroups.value[groupName] = !expandedGroups.value[groupName]
}

// 处理 nodes 和 edges，如果是 ref 则取 .value
const nodesData = computed(() => {
  return props.nodes?.value ?? props.nodes ?? []
})

const edgesData = computed(() => {
  return props.edges?.value ?? props.edges ?? []
})

const ancestorNodeIds = computed(() => {
  const ancestors = new Set()
  let currentId = String(props.nodeId)
  
  while (currentId) {
    const currentNode = nodesData.value.find(n => String(n.id) === currentId)
    if (!currentNode) break
    
    const parentId = currentNode.parentNode
    if (!parentId) break
    
    const parentIdStr = String(parentId)
    ancestors.add(parentIdStr)
    currentId = parentIdStr
  }
  
  return ancestors
})

const upstreamNodes = computed(() => {
  console.log('VariableSelect nodesData:', nodesData.value)
  console.log('VariableSelect edgesData:', edgesData.value)
  console.log('VariableSelect props.nodeId:', props.nodeId)
  
  const visited = new Set()
  const result = []
  const queue = [String(props.nodeId)]
  
  while (queue.length > 0) {
    const currentId = queue.shift()
    if (visited.has(currentId)) continue
    visited.add(currentId)
    
    const incomingEdges = edgesData.value.filter(e => e.target === currentId)
    
    if (incomingEdges.length === 0) {
      const currentNode = nodesData.value.find(n => String(n.id) === currentId)
      if (currentNode && currentNode.parentNode) {
        const parentId = String(currentNode.parentNode)
        if (!visited.has(parentId)) {
          const parentNode = nodesData.value.find(n => String(n.id) === parentId)
          if (parentNode) {
            result.push(parentNode)
            queue.push(parentId)
          }
        }
      }
    } else {
      for (const edge of incomingEdges) {
        const sourceId = String(edge.source)
        if (!visited.has(sourceId)) {
          const sourceNode = nodesData.value.find(n => String(n.id) === sourceId)
          if (sourceNode) {
            result.push(sourceNode)
            queue.push(sourceId)
          }
        }
      }
    }
  }
  
  return result
})

const upstreamNodesWithVariables = computed(() => {
  console.log('upstreamNodesWithVariables computed triggered')
  console.log('upstreamNodes.value:', upstreamNodes.value)
  console.log('ancestorNodeIds.value:', ancestorNodeIds.value)
  
  const nodes = upstreamNodes.value.filter(node => {
    const nodeId = String(node.id)
    const outputs = getNodeOutputs(node)
    
    if (outputs && Object.keys(outputs).length > 0) {
      if (props.filterType === 'all') return true
      const filteredOutputs = Object.values(outputs).filter(output => filterByType(getVariableType(output)))
      if (filteredOutputs.length > 0) return true
    }
    
    if (ancestorNodeIds.value.has(nodeId)) {
      const config = node.data?.config || {}
      
      const locals = config.locals || {}
      const loopVars = config.loop_vars || {}
      
      let hasFilteredLocals = false
      let hasFilteredLoops = false
      
      if (Object.keys(locals).length > 0) {
        if (props.filterType === 'all') {
          hasFilteredLocals = true
        } else {
          hasFilteredLocals = Object.values(locals).some(local => filterByType(local.type))
        }
      }
      
      if (Object.keys(loopVars).length > 0) {
        if (props.filterType === 'all') {
          hasFilteredLoops = true
        } else {
          hasFilteredLoops = Object.values(loopVars).some(loop => filterByType(loop.type))
        }
      }
      
      if (hasFilteredLocals || hasFilteredLoops) {
        return true
      }
    }
    
    return false
  })
  
  console.log('filtered nodes with variables:', nodes)
  return nodes
})

// 上游 function 节点的函数参数（config.params）
const upstreamFunctionParams = computed(() => {
  const result = []
  for (const node of upstreamNodes.value) {
    if (node.type === 'function') {
      const params = node.data?.config?.params || {}
      for (const [key, config] of Object.entries(params)) {
        const type = config.type
        if (filterByType(type)) {
          result.push({
            nodeId: node.id,
            nodeLabel: node.data?.label || node.id,
            key,
            type,
            required: config.required === undefined ? true : config.required,
            description: config.description
          })
        }
      }
    }
  }
  return result
})

const getFilteredNodeVariables = (node) => {
  const nodeId = String(node.id)
  const outputs = getNodeOutputs(node)
  const filtered = {}
  
  for (const [key, output] of Object.entries(outputs)) {
    if (filterByType(getVariableType(output))) {
      filtered[key] = { ...output, variableSource: 'output' }
    }
  }
  
  if (ancestorNodeIds.value.has(nodeId)) {
    const config = node.data?.config || {}
    const locals = config.locals || {}
    const loopVars = config.loop_vars || {}
    
    for (const [key, local] of Object.entries(locals)) {
      if (filterByType(local.type)) {
        filtered[key] = { ...local, variableSource: 'local' }
      }
    }
    
    for (const [key, loop] of Object.entries(loopVars)) {
      if (filterByType(loop.type)) {
        filtered[key] = { ...loop, variableSource: 'loop' }
      }
    }
  }
  
  return filtered
}

const parentNodesWithVariables = computed(() => {
  const result = []
  let currentId = String(props.nodeId)

  while (currentId) {
    const currentNode = nodesData.value.find(n => String(n.id) === currentId)
    if (!currentNode) break

    const parentId = currentNode.parentNode
    if (!parentId) break

    const parentIdStr = String(parentId)
    const parentNode = nodesData.value.find(n => String(n.id) === parentIdStr)
    if (!parentNode) break

    const variables = {}
    const outputs = getNodeOutputs(parentNode)
    for (const [key, output] of Object.entries(outputs)) {
      if (filterByType(getVariableType(output))) {
        variables[key] = { ...output, source: 'output' }
      }
    }

    const config = parentNode.data?.config || {}
    const locals = config.locals || {}
    for (const [key, local] of Object.entries(locals)) {
      if (filterByType(local.type)) {
        variables[key] = { ...local, source: 'local' }
      }
    }

    if (Object.keys(variables).length > 0) {
      result.push({ node: parentNode, variables })
    }

    currentId = parentIdStr
  }

  return result
})

const hasStartNode = computed(() => {
  return upstreamNodes.value.some(n => String(n.id) === '1') || String(props.nodeId) === '1'
})

// 解析 arguments 数据
const parsedArguments = computed(() => {
  const result = []
  if (!props.arguments || typeof props.arguments !== 'object') {
    return result
  }
  
  const required = props.arguments.required || []
  const properties = props.arguments.properties || {}
  
  for (const key in properties) {
    const config = properties[key]
    let type = config.type
    
    // 处理数组类型
    if (type === 'array' && config.items) {
      type = `array[${config.items.type}]`
    }
    
    if (filterByType(type)) {
      result.push({
        key,
        type,
        required: required.includes(key),
        config
      })
    }
  }
  
  return result
})

// 运行上下文：按节点的接收类型过滤（如 llm 只收 array 时这一组就不出现）
const filteredBuiltinVars = computed(() => {
  return BUILTIN_VARS.filter(item => filterByType(item.type))
})

const filteredFlowVariables = computed(() => {
  const filtered = {}
  for (const [key, config] of Object.entries(props.flowVariables)) {
    if (filterByType(config.type)) {
      filtered[key] = config
    }
  }
  return filtered
})

const filteredEnvironmentVariables = computed(() => {
  const filtered = {}
  for (const [key, config] of Object.entries(props.environmentVariables)) {
    if (filterByType(config.type)) {
      filtered[key] = config
    }
  }
  return filtered
})

// ===== 全局变量（租户级输入来源）=====
// 变量名与值都在「设置 → 全局变量」里维护，这里只取名字/类型/描述（接口本身不回值）。
// 选择器在 10 个节点面板里都用，每开一次都重新拉太浪费 —— 按租户缓存 30 秒。
const GLOBAL_VARS_TTL = 30 * 1000
let globalVarsCache = null
let globalVarsCachedAt = 0

const globalVars = ref([])

// kind 是变量侧的叫法（text/number/bool/secret），面板徽标用数据类型（string/number/boolean/secret）
const globalVarType = (kind) =>
  ({ text: 'string', number: 'number', bool: 'boolean', secret: 'secret' }[kind] || 'string')

const loadGlobalVars = async () => {
  if (globalVarsCache && Date.now() - globalVarsCachedAt < GLOBAL_VARS_TTL) {
    globalVars.value = globalVarsCache
    return
  }
  try {
    const res = await fetch(adminApi.globalVars.list, { headers: authHeaders() })
    const data = await checkBizOk(res)
    globalVars.value = data.data?.items || []
    globalVarsCache = globalVars.value
    globalVarsCachedAt = Date.now()
  } catch (e) {
    // 拉不到就只是不出这一组：手填模板仍然可用，不能因此打断整个选择器
    globalVars.value = []
  }
}

onMounted(loadGlobalVars)

const filteredGlobalVars = computed(() => {
  const q = searchKeyword.value.trim().toLowerCase()
  return globalVars.value.filter((v) => {
    if (!filterByType(globalVarType(v.kind))) return false
    if (!q) return true
    return String(v.name).toLowerCase().includes(q) ||
      String(v.description || '').toLowerCase().includes(q)
  })
})

const getNodeOutputs = (node) => {
  if (!node || !node.data) {
    return {}
  }
  
  const outputs = node.data.config?.outputs
  if (typeof outputs === 'object' && outputs !== null) {
    return outputs
  }
  
  return {}
}

const formatValue = (value) => {
  if (value === null) return 'null'
  if (value === undefined) return 'undefined'
  if (typeof value === 'object') {
    return JSON.stringify(value).slice(0, 40) + (JSON.stringify(value).length > 40 ? '...' : '')
  }
  const strValue = String(value)
  return strValue.length > 40 ? strValue.slice(0, 40) + '...' : strValue
}

// 获取输出的类型
const getVariableType = (output) => {
  if (!output) return 'object'
  if (typeof output === 'object' && output.type) {
    return output.type
  }
  return typeof output
}

// 获取输出类型标签
const getVariableTypeLabel = (output) => {
  const type = getVariableType(output)
  return getTypeLabel(type)
}

// 获取输出类型图标
const getVariableTypeIcon = (output) => {
  const type = getVariableType(output)
  return getTypeIcon(type)
}

const isLocalOrLoopVariable = (output) => {
  return output.variableSource === 'local' || output.variableSource === 'loop'
}

// 获取输出类型样式类
const getVariableTypeClass = (output) => {
  const type = getVariableType(output)
  return getTypeClass(type)
}

// 类型徽标的文案 / 配色 / 字母统一在 dataTypeBadge.js（模板里沿用这些旧名字）
const getTypeLabel = getDataTypeLabel
const getTypeClass = getDataTypeIconClass
const getTypeIcon = getDataTypeIcon

const selectVariable = (type, data) => {
  const result = {
    source_key: data.key || '',
    source_ref: type === 'node' ? (data.nodeId || '') : '',
    source_type: type,
    variableType: data.type || ''
  }
  const value = JSON.stringify(result)
  emit('update:modelValue', value)
  emit('change', value)
  emit('close')
}
</script>

<style scoped>
.variable-select-panel {
  position: fixed;
  border-radius: 8px;
  background: #ffffff;
  box-shadow: 0 2px 12px rgba(0, 0, 0, 0.15);
  display: flex;
  flex-direction: column;
  overflow: hidden;
  z-index: 1000;
  width: 100%;
  height: auto;
}

.panel-header {
  flex-shrink: 0;
  display: flex;
  justify-content: center;
  align-items: center;
  padding: 10px 12px;
  background-color: #fff;
  border-bottom: 1px solid #e8e8e8;
}

.search-box {
  display: flex;
  align-items: center;
  width: 100%;
  max-width: 300px;
  height: 32px;
  background: #eee;
  border: none;
  border-radius: 4px;
  padding: 0 12px;
  transition: all 0.2s;
}

.search-box:focus-within {
  border-color: #1890ff;
  box-shadow: 0 0 0 2px rgba(24, 144, 255, 0.1);
}

.search-icon {
  font-size: 14px;
  color: #999;
  margin-right: 8px;
}

.search-input {
  flex: 1;
  height: 100%;
  border: none;
  outline: none;
  font-size: 13px;
  background: transparent;
}

.search-input::placeholder {
  color: #ccc;
}

.panel-body {
  flex: 1;
  overflow-y: auto;
  padding: 12px;
}

.variable-group {
  margin-bottom: 16px;
}

.group-header {
  display: flex;
  align-items: center;
  margin-bottom: 8px;
  padding: 6px 8px;
  background: #fafafa;
  border-radius: 4px;
  cursor: pointer;
  transition: background 0.2s;
}

.group-header:hover {
  background: #f0f0f0;
}

.collapse-icon {
  margin-left: auto;
  transition: transform 0.2s;
  font-size: 12px;
  color: #999;
}

.collapse-icon.expanded {
  transform: rotate(180deg);
}

.group-icon {
  margin-right: 8px;
  font-size: 14px;
  color: #1890ff;
}

.node-icon {
  width: 20px;
  height: 20px;
  border-radius: 6px;
  display: flex;
  align-items: center;
  justify-content: center;
  color: #ffffff;
  margin-right: 8px;
  flex-shrink: 0;
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

.node-icon.icon-default {
  background-color: #6b7280;
}

.group-title {
  font-size: 13px;
  font-weight: 500;
  color: #666;
}

.variable-list {
  padding-left: 8px;
}

.variable-item {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 8px 12px;
  margin-bottom: 4px;
  background: #fafafa;
  border-radius: 4px;
  cursor: pointer;
  transition: all 0.2s;
}

.variable-item:hover {
  background: #e6f7ff;
}

.variable-name {
  font-size: 13px;
  color: #333;
}

.variable-name.local-variable {
  color: #b96868;
}

.variable-name.loop-variable {
  color: #00704A;
}

.variable-name.param-variable {
  color: #667eea;
}

.variable-value {
  font-size: 12px;
  color: #999;
  font-family: monospace;
}

.flow-variable-item {
  justify-content: flex-start;
  gap: 10px;
}

.type-badge {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 20px;
  height: 20px;
  font-size: 11px;
  font-weight: bold;
  font-family: Arial, sans-serif;
  color: #fff;
  flex-shrink: 0;
  border-radius: 50%;
}

.type-badge.type-string {
  background: #67c23a;
}

.type-badge.type-number {
  background: #409eff;
}

.type-badge.type-boolean {
  background: #9c27b0;
}

.type-badge.type-object {
  background: #909399;
}

/* 密钥类全局变量：方形徽标，与「非密钥」的圆点区分开 */
.type-badge.type-secret {
  background: #f56c6c;
  border-radius: 4px;
}

.type-badge.type-array-string,
.type-badge.type-array-number,
.type-badge.type-array-boolean,
.type-badge.type-array-object {
  border-radius: 4px;
}

.type-badge.type-array-string {
  background: #f56c6c;
}

.type-badge.type-array-number {
  background: #e6a23c;
}

.type-badge.type-array-boolean {
  background: #9c27b0;
}

.type-badge.type-array-object {
  background: #909399;
}

.variable-type {
  font-size: 12px;
  color: #999;
  flex-shrink: 0;
  margin-left: auto;
}
</style>

<!-- 与 PropsPanel 同一套口径：滚动条常驻占位但透明，鼠标进面板才显形。
     它 teleport 到 body，不在 .props-panel 内，所以需要单独一份 -->
<style>
.variable-select-panel * {
  scrollbar-width: thin;
  scrollbar-color: transparent transparent;
}
.variable-select-panel *:hover {
  scrollbar-color: #c1c1c1 transparent;
}
.variable-select-panel *::-webkit-scrollbar {
  width: 8px;
  height: 8px;
}
.variable-select-panel *::-webkit-scrollbar-track {
  background: transparent;
}
.variable-select-panel *::-webkit-scrollbar-thumb {
  background: transparent;
  border-radius: 4px;
}
.variable-select-panel *:hover::-webkit-scrollbar-thumb {
  background: #c1c1c1;
}
</style>
