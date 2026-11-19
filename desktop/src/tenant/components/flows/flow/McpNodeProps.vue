<template>
  <div class="mcp-node-props">
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

        <!-- MCP 配置 -->
        <div class="props-section">
          <div class="section-title"><span>{{ t('flow.mcpNode.section') }}</span></div>
          <div class="mcp-config">
            <div class="config-row">
              <label class="config-label">{{ t('flow.mcpNode.connection') }}</label>
              <el-select v-model="connectionId" class="config-select" size="small" :placeholder="t('flow.mcpNode.connectionPlaceholder')" filterable @change="handleConnectionChange">
                <el-option v-for="c in connections" :key="c.id" :label="c.display_name || c.name" :value="c.id" />
              </el-select>
            </div>
            <div class="config-row">
              <label class="config-label">{{ t('flow.mcpNode.tool') }}</label>
              <el-select v-model="toolName" class="config-select" size="small" :placeholder="t('flow.mcpNode.toolPlaceholder')" filterable @change="saveMcpConfig">
                <el-option v-for="t in tools" :key="t.name" :label="t.name" :value="t.name" />
              </el-select>
            </div>
            <div class="config-row">
              <label class="config-label">{{ t('flow.mcpNode.subscribeProgress') }}</label>
              <el-switch v-model="progressEnabled" size="small" @change="saveMcpConfig" />
            </div>
            <div class="config-hint">
              {{ t('flow.mcpNode.progressHint.p1') }}<code>progress</code>{{ t('flow.mcpNode.progressHint.p2') }}
              {{ t('flow.mcpNode.progressHint.p3') }}
            </div>
            <div class="config-row config-row-col">
              <label class="config-label">{{ t('flow.mcpNode.argsLabel') }}</label>
              <textarea :value="argumentsText" class="arguments-editor" placeholder='{"city": "{{.input.city}}"}' rows="5"
                @blur="handleArgumentsChange($event.target.value)" spellcheck="false"></textarea>
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
              {{ t('flow.mcpNode.hint.p3') }}<code>error</code>{{ t('flow.mcpNode.hint.p4') }}
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
  </div>
</template>

<script setup>
import { ref, watch, computed, inject, onMounted } from 'vue'
import NodeLastRun from './NodeLastRun.vue'
import { Plus, Delete, Help, Link, MagicStick, Lightning, Promotion, RefreshRight, CircleClose, ChatLineSquare, Collection, User, Operation, Coin, Minus, Upload, CircleCheck, ScaleToOriginal, Monitor, Mouse, RefreshLeft, ChromeFilled, Notification, Tools } from '@element-plus/icons-vue'
import OutputVarsConfig from './OutputVarsConfig.vue'
import OnErrorConfig from './OnErrorConfig.vue'
import apiConfig from '../../../config/api.js'
import { getToken } from '../../../utils/auth.js'
import { resolveSourceIcon } from './nodeSourceIcons.js'
import { getDataTypeIcon, getDataTypeIconClass } from './dataTypeBadge.js'
import { MCP_OUTPUTS } from '../nodeFactory.js'
import { useInputVariables } from './inputVariables.js'
import { useHandlerConfig } from './handlerConfig.js'
import { useI18n } from 'vue-i18n'

const { t } = useI18n()

const props = defineProps({
  selectedNode: { type: Object, default: null },
  flowFlowVariables: { type: Object, default: () => ({}) },
  flowEnvironmentVariables: { type: Object, default: () => ({}) },
  flowArguments: { type: Object, default: () => ({}) }
})

const emit = defineEmits(['update-node-data', 'show-variable-select'])

const FlowNodes = inject('FlowNodes', ref([]))

// ── 常量 ──
// 固定输出名由 nodeFactory 的默认定义派生，避免两处各列一遍
const FIXED_OUTPUTS = Object.keys(MCP_OUTPUTS)

// handler 语言：仅 lua/python/javascript，排除 template
const scriptLanguages = inject('scriptLanguages', ['lua', 'python', 'javascript'])
const handlerLanguages = computed(() => scriptLanguages.value.filter(l => l !== 'template'))

// ── tabs ──
const activeTab = ref('settings')

// ── 上次运行 ──


// ── 输入变量 ──
const inputVariables = ref({})

// ── MCP 配置 ──
const connections = ref([])
const tools = ref([])
const connectionId = ref('')
const toolName = ref('')
const argumentsText = ref('')
// 是否订阅进度（mcpCfg.progress）：默认关闭，关闭时节点同步执行、progress 输出恒为空
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
  onChange: () => saveMcpConfig()
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
  onChange: () => saveMcpConfig()
})

const handleAddInputVariable = addInputVariable
const handleUpdateInputVariableName = updateInputVariableName
const handleDeleteInputVariable = deleteInputVariable
const handleInputVariableFocus = () => {}

const handleShowInputVariableSelect = (name, event) => {
  emit('show-variable-select', event, name, (value) => applySelectedVariable(name, value))
}

// ── MCP 配置方法 ──
const fetchConnections = async () => {
  try {
    const token = getToken()
    const response = await fetch(apiConfig.admin.mcpConnections.base, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        'Authorization': `Bearer ${token}`
      },
      body: JSON.stringify({ page: 1, limit: -1, sort_by: ['name'], sort_order: ['desc'] })
    })
    if (!response.ok) throw new Error(t('flow.message.mcpConnectionsFailed'))
    const data = await response.json()
    connections.value = data?.data?.items || []
  } catch (e) {
    console.error(t('flow.message.mcpConnectionsFailed') + ':', e)
  }
}

const fetchTools = async (connId) => {
  tools.value = []
  if (!connId) return
  try {
    const token = getToken()
    const response = await fetch(apiConfig.admin.mcpConnections.tools(connId), {
      method: 'GET',
      headers: { 'Authorization': `Bearer ${token}` }
    })
    if (!response.ok) throw new Error(t('flow.message.toolsFailed'))
    const data = await response.json()
    const items = data?.data?.items || data?.data || []
    tools.value = Array.isArray(items) ? items : []
  } catch (e) {
    console.error(t('flow.message.toolsFailed') + ':', e)
  }
}

const handleConnectionChange = () => {
  fetchTools(connectionId.value)
  saveMcpConfig()
}

const handleArgumentsChange = (value) => {
  argumentsText.value = value
  saveMcpConfig()
}

// ── 保存 ──
const saveMcpConfig = () => {
  const mcpCfg = {
    connection_id: connectionId.value,
    tool_name: toolName.value,
    progress: progressEnabled.value === true,
    arguments: argumentsText.value || ''
  }

  mcpCfg.handler = handlerBuild()

  const inputs = buildInputsToSave()

  emit('update-node-data', {
    nodeId: props.selectedNode.id,
    data: { config: { mcp: mcpCfg, inputs } }
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
    const cfg = node.data.config || {}

    const inputs = cfg.inputs || {}
    inputVariables.value = {}
    for (const name in inputs) {
      if (inputs.hasOwnProperty(name)) {
        inputVariables.value[name] = { ...inputs[name] }
      }
    }

    const mcpCfg = cfg.mcp || {}
    connectionId.value = mcpCfg.connection_id || ''
    toolName.value = mcpCfg.tool_name || ''
    argumentsText.value = mcpCfg.arguments || ''
    progressEnabled.value = mcpCfg.progress === true

    // 没有 handler 时同样重置，避免残留上一个节点的脚本
    handlerLoad(mcpCfg.handler)

    if (connectionId.value) {
      fetchTools(connectionId.value)
    }
  }
}, { immediate: true })

onMounted(() => {
  fetchConnections()
})
</script>

<style scoped>
.mcp-node-props {
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

/* ── MCP 配置 ── */
.mcp-config { margin-top: 8px; display: flex; flex-direction: column; gap: 8px; }
.config-row { display: flex; align-items: center; gap: 8px; }
.config-row-col { flex-direction: column; align-items: stretch; }
.config-label { font-size: 12px; color: #666; min-width: 70px; flex-shrink: 0; }
.config-hint { font-size: 11px; line-height: 1.5; color: #909399; }
.config-hint code { font-family: Monaco, Menlo, monospace; color: #b8860b; }
.config-select { flex: 1; min-width: 0; }
.config-select :deep(.el-select__wrapper) { background: #eee; box-shadow: none; border-radius: 4px; }
.config-select :deep(.el-input__wrapper) { background: #eee; box-shadow: none; border-radius: 4px; }
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
.handler-lang-select :deep(.el-input__wrapper) { background: #eee; box-shadow: none; border-radius: 4px; }
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
.icon-code { background-color: #f56565; }
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
.icon-default { background-color: #6b7280; }
</style>
