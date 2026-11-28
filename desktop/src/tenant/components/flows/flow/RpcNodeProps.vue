<template>
  <div class="rpc-node-props">
    <div class="tabs">
      <button class="tab-btn" :class="{ active: activeTab === 'settings' }" @click="activeTab = 'settings'">{{ t('flow.nodeProps.tabConfig') }}</button>
      <button class="tab-btn" :class="{ active: activeTab === 'lastRun' }" @click="activeTab = 'lastRun'">{{ t('flow.nodeProps.tabResult') }}</button>
    </div>

    <div class="tab-content">
      <div v-if="activeTab === 'settings'" class="settings-panel">
        <!-- 输入变量 -->
        <div class="props-section">
          <div class="section-title">
            <span>{{ t('flow.common.inputVars') }}</span>
            <button class="add-variable-icon-btn" @click="handleAddInputVariable">
              <el-icon><Plus /></el-icon>
            </button>
          </div>
          <div class="input-variables-list">
            <div v-if="Object.keys(inputVariables).length === 0" class="input-empty-state">
              <p>{{ t('flow.common.noInputVars') }}</p>
            </div>
            <div v-for="(variable, name) in inputVariables" :key="name" class="input-variable-row">
              <input type="text" :value="name" class="input-variable-name-input"
                @focus="handleInputVariableFocus(name)"
                @blur="handleUpdateInputVariableName(name, $event.target.value)" :placeholder="t('flow.common.varName')" />
              <div class="input-variable-content">
                <button class="show-content-btn input-variable-btn" @click="handleShowInputVariableSelect(name, $event)">
                  <template v-if="variable.source_key">
                    <span class="input-type-badge" :class="getDataTypeIconClass(variable.type)">{{ getDataTypeIcon(variable.type) }}</span>
                    <span class="input-var-key">{{ variable.source_key }}</span>
                    <template v-if="getSourceIcon(variable.source_type, variable.source_ref)">
                      <template v-if="variable.source_type === 'node'">
                        <span class="input-node-icon" :class="getSourceIcon(variable.source_type, variable.source_ref).class">
                          <el-icon><component :is="getSourceIcon(variable.source_type, variable.source_ref).component" /></el-icon>
                        </span>
                        <span class="input-node-label">{{ getSourceIcon(variable.source_type, variable.source_ref).nodeLabel }}</span>
                      </template>
                      <template v-else>
                        <el-icon :class="getSourceIcon(variable.source_type, variable.source_ref).class">
                          <component :is="getSourceIcon(variable.source_type, variable.source_ref).component" />
                        </el-icon>
                      </template>
                    </template>
                  </template>
                  <template v-else>{{ t('flow.common.selectVar') }}</template>
                </button>
                <button class="input-delete-btn" @click="handleDeleteInputVariable(name)">
                  <el-icon><Delete /></el-icon>
                </button>
              </div>
            </div>
          </div>
        </div>

        <!-- RPC 配置 -->
        <div class="props-section">
          <div class="section-title"><span>{{ t('flow.rpcNode.section') }}</span></div>
          <div class="rpc-config">
            <div class="config-row">
              <label class="config-label">{{ t('flow.rpcNode.targetType') }}</label>
              <el-select v-model="targetType" class="config-select" size="small" @change="handleTargetTypeChange">
                <el-option :label="t('flow.rpcNode.typeConnection')" value="connection" />
                <el-option :label="t('flow.rpcNode.typeEndpoint')" value="endpoint" />
              </el-select>
            </div>
            <template v-if="targetType === 'connection'">
              <div class="config-row">
                <label class="config-label">{{ t('flow.rpcNode.connection') }}</label>
                <el-select v-model="connectionId" class="config-select" size="small" :placeholder="t('flow.rpcNode.connectionPlaceholder')" filterable @change="handleConnectionChange">
                  <el-option v-for="c in connections" :key="c.id" :label="c.display_name || c.name" :value="c.id" />
                </el-select>
              </div>
              <div class="config-row">
                <label class="config-label">{{ t('flow.rpcNode.tool') }}</label>
                <el-select v-model="toolName" class="config-select" size="small" :placeholder="t('flow.rpcNode.toolPlaceholder')" filterable>
                  <el-option v-for="t in tools" :key="t.name" :label="t.name" :value="t.name" />
                </el-select>
                <button class="config-info-btn" :disabled="!selectedTool"
                  :title="t('flow.toolDetail.open')" @click="toolInfoVisible = true">
                  <el-icon><Help /></el-icon>
                </button>
              </div>
            </template>
            <template v-else>
              <div class="config-row">
                <label class="config-label">{{ t('flow.rpcNode.endpoint') }}</label>
                <el-select v-model="endpointId" class="config-select" size="small" :placeholder="t('flow.rpcNode.endpointPlaceholder')" filterable @change="handleEndpointChange">
                  <!-- 存端点名而不是 id：名字租户内唯一、改名同步，导出导入后也不会变 -->
                  <el-option v-for="ep in endpoints" :key="ep.id" :label="ep.name" :value="ep.name" />
                </el-select>
                <button class="config-info-btn" :disabled="!selectedEndpoint"
                  :title="t('flow.toolDetail.openEndpoint')" @click="endpointInfoVisible = true">
                  <el-icon><Help /></el-icon>
                </button>
              </div>
            </template>
            <div class="config-row">
              <label class="config-label">{{ t('flow.rpcNode.subscribeProgress') }}</label>
              <el-switch v-model="progressEnabled" size="small" @change="saveRpcConfig" />
            </div>
            <div class="config-hint">
              {{ t('flow.rpcNode.progressHint.p1') }}<code>progress</code>{{ t('flow.rpcNode.progressHint.p2') }}
              {{ t('flow.rpcNode.progressHint.p3') }}
            </div>
            <div class="config-row config-row-col">
              <label class="config-label">{{ t('flow.rpcNode.argsLabel') }}</label>
              <textarea v-model="argumentsText" class="arguments-editor" placeholder='{"city": "{{.input.city}}"}' rows="5"
                @blur="saveRpcConfig()" spellcheck="false"></textarea>
            </div>
          </div>
        </div>

        <!-- Handler 脚本 -->
        <div class="props-section">
          <div class="section-title">
            <span>{{ t('flow.httpNode.handlerSection') }}</span>
            <el-switch v-model="handlerEnabled" size="small" @change="handleHandlerToggle" />
          </div>
          <div v-if="handlerEnabled" class="handler-config">
            <div class="handler-lang-row">
              <label class="handler-label">{{ t('flow.common.scriptLanguage') }}</label>
              <el-select v-model="handlerLang" class="handler-lang-select" size="small" @change="handleHandlerLangChange">
                <el-option v-for="l in handlerLanguages" :key="l" :label="l" :value="l" />
              </el-select>
              <label class="handler-label">{{ t('flow.httpNode.handlerTimeout') }}</label>
              <el-switch v-model="handlerTimeoutEnabled" size="small" @change="handleHandlerTimeoutToggle" />
              <input v-if="handlerTimeoutEnabled" type="number" :value="handlerTimeoutSeconds" class="timeout-input"
                min="1" max="300" @blur="handleHandlerTimeoutChange($event.target.value)" />
              <span v-if="handlerTimeoutEnabled" class="timeout-unit">{{ t('flow.common.seconds') }}</span>
            </div>
            <div class="handler-hint">
              {{ t('flow.handlerHint.prefix') }}<code>result</code> / <code>progress</code> / <code>error</code>{{ t('flow.handlerHint.autoWrite') }}
            </div>
            <textarea :value="handlerScript" class="handler-editor" :placeholder="handlerPlaceholder"
              rows="8" @blur="handleHandlerScriptChange($event.target.value)" spellcheck="false"></textarea>
          </div>
        </div>

        <!-- 自定义输出变量（复用共享组件） -->
        <OutputVarsConfig
          :selected-node="selectedNode"
          :reserved-names="FIXED_OUTPUTS"
          :fixed-variables="FIXED_OUTPUTS"
          @update-node-data="(data) => emit('update-node-data', data)"
        />

        <!-- 错误处理（复用共享组件，放在输出变量下方） -->
        <OnErrorConfig
          :selected-node="selectedNode"
          @update-node-data="(data) => emit('update-node-data', data)"
        />
      </div>

      <!-- 上次运行 -->
      <div v-if="activeTab === 'lastRun'">
        <NodeLastRun :selected-node="selectedNode" />
      </div>
    </div>

    <ToolDetailDialog v-model="toolInfoVisible" :subject="selectedTool" :meta="selectedToolMeta" />
    <ToolDetailDialog v-model="endpointInfoVisible" :subject="selectedEndpoint" :meta="selectedEndpointMeta" />
  </div>
</template>

<script setup>
import { ref, watch, computed, inject, onMounted, nextTick } from 'vue'
import NodeLastRun from './NodeLastRun.vue'
import { Plus, Delete, Help, Timer, Link, MagicStick, Lightning, Promotion, RefreshRight, CircleClose, ChatLineSquare, Collection, User, Operation, Coin, Minus, Upload, CircleCheck, ScaleToOriginal, Monitor, Mouse, RefreshLeft, ChromeFilled, Notification, Tools } from '@element-plus/icons-vue'
import OutputVarsConfig from './OutputVarsConfig.vue'
import ToolDetailDialog from './ToolDetailDialog.vue'
import OnErrorConfig from './OnErrorConfig.vue'
import apiConfig from '../../../config/api.js'
import { getToken } from '../../../utils/auth.js'
import { resolveSourceIcon } from './nodeSourceIcons.js'
import { getDataTypeIcon, getDataTypeIconClass } from './dataTypeBadge.js'
import { RPC_OUTPUTS } from '../nodeFactory.js'
import { sampleArguments } from './toolSample.js'
import { useInputVariables } from './inputVariables.js'
import { useHandlerConfig } from './handlerConfig.js'
import { useI18n } from 'vue-i18n'

const { t } = useI18n()

const props = defineProps({
  selectedNode: { type: Object, default: null },
  flowId: { type: [String, Number], default: '' },
  flowFlowVariables: { type: Object, default: () => ({}) },
  flowEnvironmentVariables: { type: Object, default: () => ({}) },
  flowArguments: { type: Object, default: () => ({}) }
})

const emit = defineEmits(['update-node-data', 'show-variable-select'])

const FlowNodes = inject('FlowNodes', ref([]))

// ── 常量 ──
// 固定输出名由 nodeFactory 的默认定义派生，避免两处各列一遍
const FIXED_OUTPUTS = Object.keys(RPC_OUTPUTS)

// handler 语言：仅 lua/python/javascript，排除 template
const scriptLanguages = inject('scriptLanguages', ['lua', 'python', 'javascript'])
const handlerLanguages = computed(() => scriptLanguages.value.filter(l => l !== 'template'))

// ── tabs ──
const activeTab = ref('settings')

// ── 上次运行 ──


// ── 输入变量 ──
const inputVariables = ref({})

// ── RPC 配置 ──
const targetType = ref('connection')
const connections = ref([])
const tools = ref([])
const connectionId = ref('')
const toolName = ref('')
const endpoints = ref([])
const endpointId = ref('')
const argumentsText = ref('')
// 是否订阅进度（rpcCfg.progress）：默认关闭，关闭时节点同步执行、progress 输出恒为空
const progressEnabled = ref(false)

// ── Handler：状态、读写、保存入口统一在 handlerConfig.js ──
const {
  handlerEnabled,
  handlerScript,
  handlerLang,
  handlerTimeoutEnabled,
  handlerTimeoutSeconds,
  handlerLoad,
  handlerBuild,
  handleHandlerToggle,
  handleHandlerLangChange,
  handleHandlerScriptChange,
  handleHandlerTimeoutToggle,
  handleHandlerTimeoutChange
} = useHandlerConfig({
  onChange: () => saveRpcConfig()
})

const handlerPlaceholder = computed(() => t('flow.nodeProps.handlerPlaceholderToolCall'))

// ── 输入变量：增删改 / 校验 / 快照统一在 inputVariables.js ──
const {
  snapshot: buildInputsToSave,
  addInputVariable,
  updateInputVariableName,
  deleteInputVariable,
  applySelectedVariable
} = useInputVariables({
  inputVariables,
  onChange: () => saveRpcConfig()
})

const handleAddInputVariable = addInputVariable
const handleUpdateInputVariableName = updateInputVariableName
const handleDeleteInputVariable = deleteInputVariable
const handleInputVariableFocus = () => {}

const handleShowInputVariableSelect = (name, event) => {
  emit('show-variable-select', event, name, (value) => applySelectedVariable(name, value))
}

// ── RPC 配置方法 ──
const fetchConnections = async () => {
  try {
    const token = getToken()
    const response = await fetch(apiConfig.admin.rpcConnections.base, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        'Authorization': `Bearer ${token}`
      },
      body: JSON.stringify({ page: 1, limit: -1, sort_by: ['name'], sort_order: ['desc'] })
    })
    if (!response.ok) throw new Error(t('flow.message.rpcConnectionsFailed'))
    const data = await response.json()
    connections.value = data?.data?.items || []
  } catch (e) {
    console.error(t('flow.message.rpcConnectionsFailed') + ':', e)
  }
}

// tools.value belongs to which connection: the same connection is never refetched.
// Flow.vue replaces selectedNode on every save, so the loading watcher re-runs on each edit; when it
// refetched unconditionally and cleared the list first, the tool dropdown was empty until the
// response arrived - and a tool switch landing in that window produced no sample at all.
const toolsConnId = ref('')

const fetchTools = async (connId) => {
  if (!connId) {
    tools.value = []
    toolsConnId.value = ''
    return
  }
  if (toolsConnId.value === connId) return
  tools.value = []
  try {
    const token = getToken()
    const response = await fetch(apiConfig.admin.rpcConnections.tools(connId), {
      method: 'GET',
      headers: { 'Authorization': `Bearer ${token}` }
    })
    if (!response.ok) throw new Error(t('flow.message.toolsFailed'))
    const data = await response.json()
    const items = data?.data?.items || data?.data || []
    tools.value = Array.isArray(items) ? items : []
    toolsConnId.value = connId
  } catch (e) {
    // Keep the previous list: an empty dropdown is worse than a stale one.
    console.error(t('flow.message.toolsFailed') + ':', e)
  }
}

const fetchEndpoints = async () => {
  endpoints.value = []
  try {
    const token = getToken()
    const response = await fetch(apiConfig.admin.svcEndpoints.base, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        'Authorization': `Bearer ${token}`
      },
      body: JSON.stringify({ page: 1, limit: 200, protocol: 'rpc', sort_by: ['name'], sort_order: ['asc'] })
    })
    if (!response.ok) throw new Error(t('flow.message.endpointsFailed'))
    const data = await response.json()
    const items = data?.data?.items || data?.data || []
    const list = Array.isArray(items) ? items : []
    endpoints.value = props.flowId
      ? list.filter(ep => !(ep.target_type === 'flow' && String(ep.target_id) === String(props.flowId)))
      : list
  } catch (e) {
    console.error(t('flow.message.endpointsFailed') + ':', e)
  }
}

const handleTargetTypeChange = () => {
  saveRpcConfig()
}

const handleConnectionChange = () => {
  // The new connection has its own tools; a name carried over from the old one cannot be resolved,
  // so no sample would ever be produced for it again.
  toolName.value = ''
  fetchTools(connectionId.value)
  saveRpcConfig()
}

// Endpoint mode picks its arguments the same way a tool does: the endpoint carries the input_schema
// (that is what the node palette generates a sample from when the node is created).
const handleEndpointChange = () => {
  const ep = endpoints.value.find(e => e.name === endpointId.value)
  applySample(sampleArguments(ep))
}

const toolKey = (tool) => tool?.name || tool?.tool_name || ''

// Switching the tool or the endpoint always rewrites the arguments: the old text describes the old
// interface, so keeping it is worse than losing it. An interface that declares no arguments yields
// an empty skeleton, which reads as "nothing to fill in" better than a blank box.
const applySample = (sample) => {
  argumentsText.value = sample || '{}'
  saveRpcConfig()
}

const loadingNode = ref(false)

// Rebuild the sample when the tool changes. watch rather than the select's @change: the event and
// the v-model write are not ordered, so reading the ref there could still see the previous tool.
watch(toolName, (next) => {
  if (loadingNode.value) return
  applySample(sampleArguments(tools.value.find(t => toolKey(t) === next)))
})

// Tool details: the full contract of the selected tool, read without leaving the panel.
const toolInfoVisible = ref(false)
const selectedTool = computed(() => tools.value.find(it => toolKey(it) === toolName.value) || null)
const selectedToolMeta = computed(() => {
  const conn = connections.value.find(it => it.id === connectionId.value)
  return conn ? [{ label: t('flow.toolDetail.connection'), value: conn.display_name || conn.name || '' }] : []
})

// Endpoint details: an exposed endpoint declares the same input_schema, so the same dialog serves it.
const endpointInfoVisible = ref(false)
const selectedEndpoint = computed(() => endpoints.value.find(it => it.name === endpointId.value) || null)
const selectedEndpointMeta = computed(() => {
  const ep = selectedEndpoint.value
  if (!ep) return []
  return [
    {
      label: t('flow.rpcNode.targetType'),
      value: ep.target_type === 'agent' ? t('flow.nodePanel.targetAgent') : t('flow.nodePanel.targetFlow')
    },
    {
      label: t('flow.toolDetail.exposure'),
      value: ep.exposed ? t('flow.toolDetail.exposed') : t('flow.toolDetail.retracted')
    }
  ]
})


// ── 保存 ──
const saveRpcConfig = () => {
  const rpcCfg = {
    target_type: targetType.value,
    progress: progressEnabled.value === true,
    arguments: argumentsText.value || ''
  }

  if (targetType.value === 'endpoint') {
    rpcCfg.endpoint = endpointId.value
  } else {
    rpcCfg.connection_id = connectionId.value
    rpcCfg.tool_name = toolName.value
  }

  rpcCfg.handler = handlerBuild()

  const inputs = buildInputsToSave()

  emit('update-node-data', {
    nodeId: props.selectedNode.id,
    data: { config: { rpc: rpcCfg, inputs } }
  })
}

// ── 工具函数 ──

// 变量来源图标：实现在 nodeSourceIcons.js（12 个面板共用，避免 source 类型再漂移）
const getSourceIcon = (sourceType, sourceRef) =>
  resolveSourceIcon(sourceType, sourceRef, FlowNodes.value)

// ── 状态 ──


// ── watch 加载已有数据 ──
watch(() => props.selectedNode, (node) => {
  if (node && node.data) {
    // Restoring a node must not look like "the author switched tools", otherwise opening the panel
    // would write a sample back into the node.
    loadingNode.value = true
    const cfg = node.data.config || {}

    const inputs = cfg.inputs || {}
    inputVariables.value = {}
    for (const name in inputs) {
      if (inputs.hasOwnProperty(name)) {
        inputVariables.value[name] = { ...inputs[name] }
      }
    }

    const rpcCfg = cfg.rpc || {}
    targetType.value = rpcCfg.target_type || 'connection'
    connectionId.value = rpcCfg.connection_id || ''
    toolName.value = rpcCfg.tool_name || ''
    endpointId.value = rpcCfg.endpoint || ''
    argumentsText.value = rpcCfg.arguments || ''
    progressEnabled.value = rpcCfg.progress === true

    // 没有 handler 时同样重置，避免残留上一个节点的脚本
    handlerLoad(rpcCfg.handler)

    if (connectionId.value) {
      fetchTools(connectionId.value)
    }
    nextTick(() => { loadingNode.value = false })
  }
}, { immediate: true })

onMounted(() => {
  fetchConnections()
  fetchEndpoints()
})
</script>

<style scoped>
.rpc-node-props {
  display: flex;
  flex-direction: column;
  height: 100%;
  overflow: hidden;
}

.tabs {
  display: flex;
  border-bottom: 1px solid #eee;
  flex-shrink: 0;
}

.tab-btn {
  padding: 10px;
  border: none;
  background: transparent;
  font-size: 13px;
  color: #666;
  cursor: pointer;
  position: relative;
  transition: color 0.2s;
  margin-right: 20px;
}

.tab-btn:hover { color: #1890ff; }
.tab-btn.active { color: #1890ff; font-weight: 500; }
.tab-btn.active::after { content: ''; position: absolute; bottom: -1px; left: 0; right: 0; height: 1px; background: #1890ff; }

.tab-content { flex: 1; overflow-y: auto; padding: 8px; }

.settings-panel { display: flex; flex-direction: column; gap: 8px; }

.props-section { background: #fafafa; border-radius: 8px; padding: 5px; }

.section-title {
  display: flex;
  justify-content: space-between;
  align-items: center;
  font-size: 13px;
  font-weight: 600;
  color: #333;
  padding-bottom: 10px;
  border-bottom: 1px solid #e8e8e8;
  margin-bottom: 12px;
}

.add-variable-icon-btn {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 24px;
  height: 24px;
  border: none;
  border-radius: 4px;
  background: #1890ff;
  color: white;
  cursor: pointer;
  font-size: 14px;
  transition: background-color 0.2s;
}
.add-variable-icon-btn:hover { background: #40a9ff; }

/* ── 输入变量 ── */
.input-variables-list { display: flex; flex-direction: column; gap: 6px; margin-top: 8px; }
.input-empty-state { display: flex; align-items: center; justify-content: center; padding: 3px; text-align: center; }
.input-empty-state p { color: #999; margin: 0; font-size: 12px; }

.input-variable-row { display: flex; align-items: center; gap: 8px; border-radius: 6px; padding: 0; }
.input-variable-name-input {
  flex: 1; min-width: 0; max-width: 33.33%; padding: 10px 10px; border: 0; background: #eee; border-radius: 4px; font-size: 13px; outline: none;
}
.input-variable-name-input::placeholder { color: #bbb; }
.input-variable-content { flex: 2; display: flex; align-items: center; gap: 8px; }

.show-content-btn {
  flex: 1; min-width: 0; padding: 10px 12px; border: 0; border-radius: 4px; background: #eee; color: #1890ff; font-size: 12px; cursor: pointer; transition: all 0.2s;
}
.show-content-btn:hover:not(:disabled) { background: #e6f7ff; }

.input-delete-btn {
  display: flex; align-items: center; justify-content: center; width: 24px; height: 24px; padding: 0; border: none; border-radius: 3px; font-size: 12px; color: #999; background: transparent; cursor: pointer; flex-shrink: 0;
  margin-left: auto;
}
.input-delete-btn:hover { background: #fff5f5; color: #f56c6c; }

.input-variable-btn { display: flex; align-items: center; gap: 6px; padding: 8px 10px; max-width: 100%; overflow: hidden; }
.input-variable-btn .input-var-key { font-size: 12px; color: #333; flex-shrink: 0; }
.input-variable-btn .input-node-label { font-size: 12px; color: #666; flex-shrink: 0; max-width: 80px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.input-variable-btn .input-node-icon {
  width: 16px; height: 16px; border-radius: 4px; display: flex; align-items: center; justify-content: center; color: #fff; flex-shrink: 0; font-size: 10px;
}
.input-variable-btn .icon-source-flow, .input-variable-btn .icon-source-env, .input-variable-btn .icon-source-argument { font-size: 14px; color: #1890ff; flex-shrink: 0; }

/* ── RPC 配置 ── */
.rpc-config { margin-top: 8px; display: flex; flex-direction: column; gap: 8px; }
.config-row { display: flex; align-items: center; gap: 8px; }
.config-row-col { flex-direction: column; align-items: stretch; }
.config-label { font-size: 12px; color: #666; min-width: 70px; flex-shrink: 0; }
.config-hint { font-size: 11px; line-height: 1.5; color: #909399; }
.config-hint code { font-family: Monaco, Menlo, monospace; color: #b8860b; }
.config-select { flex: 1; min-width: 0; }
.config-select :deep(.el-select__wrapper) { background: #eee; box-shadow: none; border-radius: 4px; }
.config-select :deep(.el-input__wrapper) { background: #eee; box-shadow: none; border-radius: 4px; }
/* Tool details sit next to the tool it describes; disabled until a tool is chosen. */
.config-info-btn {
  display: flex; align-items: center; justify-content: center;
  width: 26px; height: 26px; padding: 0; flex-shrink: 0;
  border: 0; border-radius: 4px; background: #eee; color: #1890ff;
  font-size: 14px; cursor: pointer; transition: background-color 0.2s, color 0.2s;
}
.config-info-btn:hover:not(:disabled) { background: #e6f7ff; }
.config-info-btn:disabled { color: #c0c4cc; cursor: not-allowed; }
.arguments-editor {
  width: 100%;
  padding: 8px 10px;
  border: 0;
  background: #eee;
  border-radius: 4px;
  font-size: 12px;
  font-family: Monaco, Menlo, monospace;
  line-height: 1.5;
  resize: vertical;
  outline: none;
  box-sizing: border-box;
}
.arguments-editor:focus { background: #fafafa; }
.arguments-editor::placeholder { color: #bbb; }

/* ── Handler ── */
.handler-config { margin-top: 8px; display: flex; flex-direction: column; gap: 8px; }
.handler-lang-row { display: flex; align-items: center; gap: 8px; }
.handler-label { font-size: 12px; color: #666; min-width: 60px; }
.handler-lang-select { width: 110px; }
.handler-lang-select :deep(.el-select__wrapper) { background: #eee; box-shadow: none; border-radius: 4px; }
.handler-lang-select :deep(.el-input__wrapper) { background: #900; box-shadow: none; border-radius: 4px; }
.handler-editor {
  width: 100%;
  padding: 8px 10px;
  border: 0;
  background: #eee;
  color: #333;
  border-radius: 4px;
  font-size: 12px;
  font-family: Monaco, Menlo, monospace;
  line-height: 1.5;
  resize: vertical;
  outline: none;
  box-sizing: border-box;
}
.handler-editor::placeholder { color: #666; }
.handler-hint {
  font-size: 11px;
  line-height: 1.5;
  color: #8a6d3b;
  background: #fdf6ec;
  border-radius: 4px;
  padding: 6px 8px;
}
.handler-hint code {
  font-family: Monaco, Menlo, monospace;
  color: #b8860b;
}

.timeout-input {
  width: 60px; height: 24px; padding: 2px 6px;
  border: 0; background: #eee; border-radius: 4px;
  font-size: 12px; text-align: center; outline: none;
}
.timeout-input:focus { background: #fafafa; }
.timeout-unit { font-size: 12px; color: #999; }

/* ── type badge ── */
.input-type-badge {
  display: inline-flex; align-items: center; justify-content: center; width: 18px; height: 18px; font-size: 10px; font-weight: bold; font-family: Arial, sans-serif; color: #fff; flex-shrink: 0; border-radius: 50%;
}
.type-string { background: #67c23a; }
.type-number { background: #409eff; }
.type-boolean { background: #9c27b0; }
.type-object { background: #909399; }
.type-array-string { background: #f56c6c; border-radius: 4px; }
.type-array-number { background: #e6a23c; border-radius: 4px; }
.type-array-boolean { background: #9c27b0; border-radius: 4px; }
.type-array-object { background: #909399; border-radius: 4px; }

/* ── last run ── */
.last-run-panel { padding: 0; }
.result-item { display: flex; flex-direction: column; gap: 6px; margin-bottom: 12px; }
.result-label { font-size: 12px; color: #666; }
.result-value { font-size: 13px; color: #333; }
.status-success { color: #67c23a; }
.status-failed { color: #f56565; }
.status-running { color: #409eff; }
.status-default { color: #999; }
.result-output {
  width: 100%; min-height: 100px; padding: 10px; border: 1px solid #d9d9d9; border-radius: 4px; font-size: 12px; color: #333; background: #fafafa; overflow-x: auto; white-space: pre-wrap; word-break: break-all;
}

/* ── node icons ── */
.icon-start { background-color: #00704A; }
.icon-branch { background-color: #00bcd4; }
.icon-wait { background-color: #00bcd4; }
.icon-code { background-color: #a0522d; }
.icon-http { background-color: #409eff; }
.icon-process { background-color: #00bcd4; }
.icon-result { background-color: #67c23a; }
.icon-progress { background-color: #eea236; }
.icon-llm { background-color: #409eff; }
.icon-knowledge { background-color: #67c23a; }
.icon-agent { background-color: #f56c6c; }
.icon-subflow { background-color: #667eea; }
.icon-assign { background-color: #ff9800; }
.icon-break { background-color: #ff9800; }
.icon-continue { background-color: #4caf50; }
.icon-return { background-color: #9c27b0; }
.icon-function { background-color: #667eea; }
.icon-fork { background-color: #4bc0c0; }
.icon-call { background-color: #ffc107; }
.icon-loop { background-color: #7b1fa2; }
.icon-mcp { background-color: #e6a23c; }
.icon-rpc { background-color: #1890ff; }
.icon-default { background-color: #6b7280; }
</style>
