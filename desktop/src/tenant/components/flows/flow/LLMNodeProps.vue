<template>
  <div class="llm-node-props">
    <div class="tabs">
      <button class="tab-btn" :class="{ active: activeTab === 'settings' }" @click="activeTab = 'settings'">配置</button>
      <button class="tab-btn" :class="{ active: activeTab === 'lastRun' }" @click="activeTab = 'lastRun'">执行结果</button>
    </div>

    <div class="tab-content">
      <div v-if="activeTab === 'settings'" class="settings-panel">
        <!-- 模型与响应模式 -->
        <div class="props-section">
          <div class="prop-group">
            <label class="prop-label">模型 (gateway_model)</label>
            <el-select
              v-model="gatewayModel"
              class="prop-select"
              @change="saveLLM"
            >
              <el-option label="缺省模型" value="__default__" />
              <el-option
                v-for="m in selectableModels"
                :key="m.gateway_model"
                :label="m.gateway_model"
                :value="m.gateway_model"
              />
            </el-select>
          </div>
          <div class="prop-group-horizontal">
            <div class="prop-item">
              <label class="prop-label">响应模式</label>
              <el-radio-group v-model="responseMode" @change="handleResponseModeChange">
                <el-radio value="complete">非流式</el-radio>
                <el-radio value="sse_stream">流式</el-radio>
              </el-radio-group>
            </div>
          </div>
        </div>

        <!-- 大模型参数 -->
        <div class="props-section">
          <div class="section-title"><span>大模型参数</span></div>
          <div class="prop-group">
            <div class="gen-param-row">
              <label class="prop-label">max_tokens</label>
              <el-switch v-model="genParams.maxTokens.enabled" size="small" @change="saveLLM" />
              <el-input-number v-model="genParams.maxTokens.value" :disabled="!genParams.maxTokens.enabled" :min="1" :max="200000" size="small" controls-position="right" @change="saveLLM" />
            </div>
            <div class="gen-param-row">
              <label class="prop-label">temperature</label>
              <el-switch v-model="genParams.temperature.enabled" size="small" @change="saveLLM" />
              <el-input-number v-model="genParams.temperature.value" :disabled="!genParams.temperature.enabled" :min="0" :max="2" :step="0.1" :precision="2" size="small" controls-position="right" @change="saveLLM" />
            </div>
            <div class="gen-param-row">
              <label class="prop-label">top_p</label>
              <el-switch v-model="genParams.topP.enabled" size="small" @change="saveLLM" />
              <el-input-number v-model="genParams.topP.value" :disabled="!genParams.topP.enabled" :min="0" :max="1" :step="0.1" :precision="2" size="small" controls-position="right" @change="saveLLM" />
            </div>
            <div class="gen-param-row gen-param-extra">
              <label class="prop-label">extra_params</label>
              <el-switch v-model="extraParamsEnabled" size="small" @change="onExtraParamsEnabledChange" />
            </div>
            <textarea
              v-if="extraParamsEnabled"
              class="prop-textarea gen-extra-textarea"
              v-model="extraParamsText"
              @blur="saveLLM"
              placeholder='附加参数 JSON，如 {"reasoning_effort":"low"}'
            ></textarea>
          </div>
        </div>

        <!-- 输入变量 -->
        <div class="props-section">
          <div class="section-title">
            <span>输入变量</span>
            <button class="add-variable-icon-btn" @click="handleAddInputVariable"><el-icon><Plus /></el-icon></button>
          </div>
          <div class="input-variables-list">
            <div v-if="Object.keys(inputVariables).length === 0" class="input-empty-state"><p>暂无输入变量</p></div>
            <div v-for="(variable, name) in inputVariables" :key="name" class="input-variable-row">
              <input
                type="text"
                :value="name"
                class="input-variable-name-input"
                @blur="handleUpdateInputVariableName(name, $event.target.value)"
                placeholder="变量名"
              />
              <div class="input-variable-content">
                <button class="show-content-btn input-variable-btn" @click="handleShowVariableSelect(name, $event, variable)">
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
                  <template v-else>选择变量</template>
                </button>
                <button class="input-delete-btn" @click="handleDeleteInputVariable(name)"><el-icon><Delete /></el-icon></button>
              </div>
            </div>
          </div>
        </div>

        <!-- 上下文 messages -->
        <div class="props-section">
          <div class="section-title">
            <span>上下文 (messages)</span>
            <button class="add-variable-icon-btn" @click="handleAddMessage"><el-icon><Plus /></el-icon></button>
          </div>
          <div class="messages-list">
            <div v-if="messages.length === 0" class="input-empty-state"><p>暂无消息</p></div>
            <div v-for="(msg, idx) in messages" :key="idx" class="message-row">
              <div class="message-row-head">
                <el-select v-model="msg.role" class="message-role-select" @change="saveLLM">
                  <el-option label="system" value="system" />
                  <el-option label="user" value="user" />
                  <el-option label="assistant" value="assistant" />
                </el-select>
                <button class="input-delete-btn" @click="handleDeleteMessage(idx)"><el-icon><Delete /></el-icon></button>
              </div>
              <textarea
                class="prop-textarea message-content"
                v-model="msg.content"
                @blur="saveLLM"
                placeholder="支持模板：{{.input.变量名}} 输入 / {{.local.变量名}} 局部 / {{.output.变量名}} 输出"
              ></textarea>
            </div>
          </div>
        </div>

        <!-- 响应处理脚本 -->
        <div class="props-section">
          <div class="section-title">
            <span>响应处理脚本（可选）</span>
            <el-switch v-model="handlerEnabled" @change="handleHandlerEnabledChange" />
          </div>
          <div v-if="handlerEnabled" class="prop-group">
            <div class="prop-group-horizontal handler-lang-row">
              <div class="prop-item">
                <label class="prop-label">脚本语言</label>
                <el-select v-model="handlerLang" class="prop-select" @change="saveLLM">
                  <el-option v-for="lang in handlerLanguages" :key="lang" :label="lang" :value="lang" />
                </el-select>
              </div>
              <div class="prop-item handler-timeout-item">
                <label class="prop-label">脚本超时</label>
                <el-switch v-model="handlerTimeoutEnabled" size="small" @change="saveLLM" />
                <input
                  v-if="handlerTimeoutEnabled"
                  type="number"
                  v-model.number="handlerTimeoutSeconds"
                  class="timeout-input"
                  min="1"
                  max="300"
                  @blur="saveLLM"
                />
                <span v-if="handlerTimeoutEnabled" class="timeout-unit">秒</span>
              </div>
            </div>
            <div class="handler-hint">
              固定输出变量 <code>content</code> / <code>reasoning_content</code> / <code>tool_calls</code> 由节点自动写入（有没有脚本都一样），
              脚本里 return 这三个名字会被忽略并记日志；脚本只用于处理自定义输出变量。
              流式模式下脚本逐帧调用且无状态，<code>input.content</code> 是本帧增量，累积由节点完成。
            </div>
            <textarea
              class="prop-textarea"
              v-model="handlerScript"
              @blur="saveLLM"
              :placeholder="handlerPlaceholder"
            ></textarea>
          </div>
          <div v-else class="hint-text">
            固定输出变量（content / reasoning_content / tool_calls）由程序自动写入；开启脚本只用于额外处理自定义输出变量。
          </div>
        </div>

        <!-- 输出变量（融合固定 + 自定义） -->
        <OutputVarsConfig
          :selected-node="selectedNode"
          :existing-names="existingOutputNames"
          :fixed-variables="FIXED_OUTPUT_VARS"
          @update-node-data="(data) => emit('update-node-data', data)"
        />

        <OnErrorConfig
          :selected-node="selectedNode"
          @update-node-data="(data) => emit('update-node-data', data)"
        />
      </div>

      <div v-if="activeTab === 'lastRun'">
        <NodeLastRun :selected-node="selectedNode" />
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref, watch, computed, inject, onMounted } from 'vue'
import NodeLastRun from './NodeLastRun.vue'
import { ElSelect, ElOption, ElRadioGroup, ElRadio, ElSwitch, ElInputNumber, ElMessage } from 'element-plus'
import { Plus, Delete, ChromeFilled, Notification, User, Timer, Help, ChatLineSquare, Collection, Operation, Coin, Minus, Upload, CircleCheck, ScaleToOriginal, Monitor, Mouse, RefreshLeft, Lightning, Tools } from '@element-plus/icons-vue'
import OnErrorConfig from './OnErrorConfig.vue'
import OutputVarsConfig from './OutputVarsConfig.vue'
import { getToken } from '@/tenant/utils/auth'
import adminApi from '@/tenant/config/admin'
import { resolveSourceIcon } from './nodeSourceIcons.js'
import { getDataTypeIcon, getDataTypeIconClass } from './dataTypeBadge.js'
import { useInputVariables } from './inputVariables.js'

const props = defineProps({
  selectedNode: { type: Object, default: null },
  flowFlowVariables: { type: Object, default: () => ({}) },
  flowEnvironmentVariables: { type: Object, default: () => ({}) },
  flowArguments: { type: Object, default: () => ({}) }
})

const emit = defineEmits(['update-node-data', 'show-variable-select'])

const FlowNodes = inject('FlowNodes', ref([]))
const scriptLanguages = inject('scriptLanguages', ref(['lua']))
const defaultLang = computed(() => scriptLanguages.value[0] || 'lua')

// handler 语言：后端 handler 走 worker 脚本池，不含 template
const handlerLanguages = computed(() => scriptLanguages.value.filter(l => l !== 'template'))

// 两种响应模式的固定输出变量一致：complete 模式遇到推理模型同样有 reasoning_content，
// 由执行器统一写入，不再按模式增删（避免出现"变量没声明但执行器要写"的情况）。
const FIXED_OUTPUT_VARS = ['content', 'reasoning_content', 'tool_calls']

const handlerPlaceholder = computed(() => {
  if (localResponseMode.value === 'sse_stream') {
    return `-- input.mode / input.content / input.reasoning_content / input.tool_calls / input.done
-- 固定变量 content / reasoning_content / tool_calls 由节点累积写入，脚本不要写它们
-- 逐帧调用、无状态：input.content 是本帧增量
-- 脚本只写自定义输出变量：string 追加、非 string 覆盖
return { answer = input.content }`
  }
  return `-- input.mode / input.content / input.reasoning_content / input.tool_calls / input.done
-- 固定变量 content / reasoning_content / tool_calls 由节点自动写入，脚本不要写它们
-- 脚本只写自定义输出变量：string 追加、非 string 覆盖
return { answer = input.content }`
})

// ── 本地状态 ──
const activeTab = ref('settings')


const gatewayModel = ref('')
const responseMode = ref('complete')
const localResponseMode = ref('complete')
const inputVariables = ref({})
const messages = ref([])

// 可选模型下拉（provider.enabled=true 且 gateway_id 非空）
const selectableModels = ref([])
const loadSelectableModels = async () => {
  try {
    const resp = await fetch(adminApi.llmModels.base + '/selectable', {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        'Authorization': 'Bearer ' + getToken()
      }
    })
    const data = await resp.json()
    if (data && data.data && Array.isArray(data.data.items)) {
      selectableModels.value = data.data.items.map(m => ({ gateway_model: m.gateway_model }))
    }
  } catch (e) {
    console.error('加载可选模型失败', e)
  }
}

// 大模型参数（与配置模型界面一致：开关启用，config.llm.params 含键=启用）
const genParams = ref({
  maxTokens: { enabled: false, value: 4096 },
  temperature: { enabled: false, value: 0.7 },
  topP: { enabled: false, value: 1 }
})
const extraParamsEnabled = ref(false)
const extraParamsText = ref('')

const handlerEnabled = ref(false)
const handlerLang = ref('lua')
const handlerScript = ref('')
const handlerTimeoutEnabled = ref(false)
const handlerTimeoutSeconds = ref(30)

const getOutputVarNames = () => Object.keys(props.selectedNode?.data?.config?.outputs || {})

const existingOutputNames = computed(() => Object.keys(inputVariables.value))

// ── 图标辅助（与 CodeNodeProps 一致） ──
// 变量来源图标：实现在 nodeSourceIcons.js（12 个面板共用，避免 source 类型再漂移）
const getSourceIcon = (sourceType, sourceRef) =>
  resolveSourceIcon(sourceType, sourceRef, FlowNodes.value)

// ── 加载 ──
watch(() => props.selectedNode, (node) => {
  if (node && node.data) {
    const llm = node.data.config?.llm || {}
    gatewayModel.value = llm.gateway_model ? llm.gateway_model : '__default__'
    localResponseMode.value = llm.response_mode || 'complete'
    responseMode.value = localResponseMode.value

    const inputs = node.data.config?.inputs || {}
    inputVariables.value = {}
    for (const name in inputs) {
      if (inputs.hasOwnProperty(name)) inputVariables.value[name] = { ...inputs[name] }
    }

    messages.value = Array.isArray(llm.messages) ? llm.messages.map(m => ({ role: m.role || 'user', content: m.content || '' })) : []

    const h = llm.handler
    handlerEnabled.value = !!h
    handlerLang.value = h?.lang || defaultLang.value
    handlerScript.value = h?.script || ''
    const ht = h?.timeout || {}
    handlerTimeoutEnabled.value = !!ht.enabled
    handlerTimeoutSeconds.value = ht.seconds || 30

    // 大模型参数
    fillGenParamsFromNode(llm.params)

    // 确保固定输出变量存在于 config.outputs，否则 OutputVarsConfig 不显示
    const config = node.data.config || (node.data.config = {})
    const outputsNow = config.outputs || (config.outputs = {})
    const fixedNow = FIXED_OUTPUT_VARS
    let addedFixed = false
    fixedNow.forEach(name => {
      if (!outputsNow[name]) {
        outputsNow[name] = { type: name === 'tool_calls' ? 'array[object]' : 'string', description: '' }
        addedFixed = true
      }
    })
    if (addedFixed) {
      emit('update-node-data', {
        nodeId: node.id,
        data: { config: { outputs: { ...outputsNow } } }
      })
    }
  }
}, { immediate: true })

onMounted(() => {
  loadSelectableModels()
})

// 从节点 config.llm.params 读取参数开关与值（用 function 声明以支持 immediate watch 提前调用）
function fillGenParamsFromNode(params) {
  const p = params || {}
  genParams.value.maxTokens.enabled = Object.prototype.hasOwnProperty.call(p, 'max_tokens')
  if (genParams.value.maxTokens.enabled) genParams.value.maxTokens.value = Number(p.max_tokens)
  genParams.value.temperature.enabled = Object.prototype.hasOwnProperty.call(p, 'temperature')
  if (genParams.value.temperature.enabled) genParams.value.temperature.value = Number(p.temperature)
  genParams.value.topP.enabled = Object.prototype.hasOwnProperty.call(p, 'top_p')
  if (genParams.value.topP.enabled) genParams.value.topP.value = Number(p.top_p)
  extraParamsEnabled.value = Object.prototype.hasOwnProperty.call(p, 'extra_params')
  if (extraParamsEnabled.value) {
    extraParamsText.value = JSON.stringify(p.extra_params, null, 2)
  } else {
    extraParamsText.value = ''
  }
}

// ── 保存 llm 配置 ──
const buildLLMConfig = () => {
  const llm = {
    gateway_model: gatewayModel.value === '__default__' ? '' : gatewayModel.value,
    response_mode: responseMode.value,
    messages: messages.value.map(m => ({ role: m.role, content: m.content }))
  }

  if (handlerEnabled.value) {
    llm.handler = {
      lang: handlerLang.value,
      script: handlerScript.value,
      timeout: { enabled: handlerTimeoutEnabled.value, seconds: handlerTimeoutSeconds.value }
    }
  }

  // 大模型参数：仅写入启用的键
  const params = {}
  if (genParams.value.maxTokens.enabled) params.max_tokens = genParams.value.maxTokens.value
  if (genParams.value.temperature.enabled) params.temperature = genParams.value.temperature.value
  if (genParams.value.topP.enabled) params.top_p = genParams.value.topP.value
  if (extraParamsEnabled.value) {
    try {
      const parsed = extraParamsText.value.trim() === '' ? {} : JSON.parse(extraParamsText.value)
      params.extra_params = parsed
    } catch (e) {
      ElMessage.error('extra_params 不是合法 JSON')
      return null
    }
  }
  llm.params = params

  return llm
}

const saveLLM = () => {
  emit('update-node-data', {
    nodeId: props.selectedNode.id,
    data: { config: { inputs: buildInputsToSave(), llm: buildLLMConfig() } }
  })
}

// ── 切换响应模式：固定输出变量两模式一致，只需补齐，不再按模式删除 ──
const applyModeFixes = () => {
  const outputs = { ...(props.selectedNode?.data?.config?.outputs || {}) }

  FIXED_OUTPUT_VARS.forEach(name => {
    if (!outputs[name]) {
      outputs[name] = { type: name === 'tool_calls' ? 'array[object]' : 'string', description: '' }
    }
  })

  emit('update-node-data', {
    nodeId: props.selectedNode.id,
    data: { config: { outputs } }
  })
}

const handleResponseModeChange = () => {
  localResponseMode.value = responseMode.value
  applyModeFixes()
  saveLLM()
}

// ── 输入变量：增删改 / 校验 / 快照统一在 inputVariables.js ──
const {
  snapshot: buildInputsToSave,
  addInputVariable,
  updateInputVariableName,
  deleteInputVariable,
  applySelectedVariable
} = useInputVariables({
  inputVariables,
  // 输入名不能和节点输出重名
  reservedNames: () => getOutputVarNames(),
  onChange: () => saveLLM()
})

const handleShowVariableSelect = (name, event) => {
  emit('show-variable-select', event, name, (value) => applySelectedVariable(name, value))
}

const handleAddInputVariable = addInputVariable
const handleUpdateInputVariableName = updateInputVariableName
const handleDeleteInputVariable = deleteInputVariable

// ── messages ──
const handleAddMessage = () => {
  messages.value.push({ role: 'user', content: '' })
  saveLLM()
}
const handleDeleteMessage = (idx) => {
  messages.value.splice(idx, 1)
  saveLLM()
}

// ── handler ──
const handleHandlerEnabledChange = () => {
  if (handlerEnabled.value && handlerScript.value.trim() === '') {
    // 启用时若脚本为空，给一个默认占位，否则用户还没机会输入脚本就因空脚本被判定为未启用
    handlerScript.value = defaultLang.value === 'lua'
      ? '-- 在这里编写响应处理脚本\nreturn { content = input.content }'
      : ''
  }
  if (!handlerEnabled.value) handlerScript.value = ''
  saveLLM()
}

// ── extra_params 开关 ──
const onExtraParamsEnabledChange = () => {
  if (extraParamsEnabled.value && extraParamsText.value === '') {
    extraParamsText.value = '{}'
  }
  saveLLM()
}
</script>

<style scoped>
.llm-node-props { display: flex; flex-direction: column; height: 100%; overflow: hidden; }
.tabs { display: flex; border-bottom: 1px solid #eee; flex-shrink: 0; }
.tab-btn { padding: 10px; border: none; background: transparent; font-size: 13px; color: #666; cursor: pointer; position: relative; transition: color 0.2s; margin-right: 20px; }
.tab-btn:hover { color: #1890ff; }
.tab-btn.active { color: #1890ff; font-weight: 500; }
.tab-btn.active::after { content: ''; position: absolute; bottom: -1px; left: 0; right: 0; height: 1px; background: #1890ff; }
.tab-content { flex: 1; overflow-y: auto; padding: 8px; }
.settings-panel { display: flex; flex-direction: column; gap: 8px; }
.props-section { background: #fafafa; border-radius: 8px; padding: 5px; }
.section-title { display: flex; justify-content: space-between; align-items: center; font-size: 13px; font-weight: 600; color: #333; padding-bottom: 10px; border-bottom: 1px solid #e8e8e8; margin-bottom: 12px; }
.add-variable-icon-btn { display: flex; align-items: center; justify-content: center; width: 24px; height: 24px; border: none; border-radius: 4px; background: #1890ff; color: white; cursor: pointer; font-size: 14px; transition: background-color 0.2s; }
.add-variable-icon-btn:hover { background: #40a9ff; }
.prop-group { display: flex; flex-direction: column; gap: 6px; margin-bottom: 12px; }
.prop-group-horizontal { display: flex; gap: 16px; margin-bottom: 12px; }
.prop-item { flex: 1; display: flex; flex-direction: row; align-items: center; gap: 8px; flex-wrap: nowrap; white-space: nowrap; }
.prop-label { font-size: 12px; color: #666; font-weight: 500; flex-shrink: 0; min-width: 60px; }
.prop-select { width: 100%; }
.prop-select :deep(.el-select__wrapper) { background-color: #eee; border: none; box-shadow: none; }
.prop-plain-input { width: 100%; padding: 10px; border: 0px solid #d9d9d9; background-color: #eee; border-radius: 4px; font-size: 13px; outline: none; box-sizing: border-box; }
.prop-plain-input:focus { outline: none; }
.prop-textarea { width: 100%; min-height: 120px; padding: 10px; border: 1px solid #d9d9d9; border-radius: 4px; font-size: 12px; font-family: 'Monaco', 'Menlo', monospace; line-height: 1.7; resize: vertical; outline: none; box-sizing: border-box; background-color: #eee; }
.prop-textarea:focus { border-color: #91caff; }
.prop-textarea::placeholder { color: #bbb; }
.hint-text { font-size: 12px; color: #999; line-height: 1.6; }
.handler-hint {
  font-size: 11px;
  line-height: 1.5;
  color: #8a6d3b;
  background: #fdf6ec;
  border-radius: 4px;
  padding: 6px 8px;
  margin-bottom: 6px;
}
.handler-hint code {
  font-family: 'Monaco', 'Menlo', monospace;
  color: #b8860b;
}
.last-run-panel { padding: 0; }
.result-item { display: flex; flex-direction: column; gap: 6px; margin-bottom: 12px; }
.result-label { font-size: 12px; color: #666; }
.result-value { font-size: 13px; color: #333; }
.status-success { color: #67c23a; }
.status-failed { color: #f56565; }
.status-running { color: #409eff; }
.status-default { color: #999; }
.result-output { width: 100%; min-height: 100px; padding: 10px; border: 1px solid #d9d9d9; border-radius: 4px; font-size: 12px; color: #333; background: #fafafa; overflow-x: auto; white-space: pre-wrap; word-break: break-all; }
.input-variables-list { display: flex; flex-direction: column; gap: 6px; margin-top: 8px; }
.input-empty-state { display: flex; align-items: center; justify-content: center; padding: 3px; text-align: center; }
.input-empty-state p { color: #999; margin: 0; font-size: 12px; }
.input-variable-row { display: flex; align-items: center; gap: 8px; border-radius: 6px; padding: 0px 0px; }
.input-variable-name-input { flex: 1; min-width: 0; max-width: 33.33%; padding: 10px 10px; border: 0px solid #d9d9d9; background-color: #eeeeee; border-radius: 4px; font-size: 13px; outline: none; }
.input-variable-name-input:focus { outline: none; }
.input-variable-content { flex: 2; display: flex; align-items: center; gap: 8px; }
.show-content-btn { flex: 1; min-width: 0; padding: 10px 12px; border: 0px solid #1890ff; border-radius: 4px; background: #eee; color: #1890ff; font-size: 12px; cursor: pointer; transition: all 0.2s; flex-shrink: 0; }
.show-content-btn:hover { background: #e6f7ff; }
.input-delete-btn { display: flex; align-items: center; justify-content: center; width: 20px; height: 20px; padding: 0; border: none; border-radius: 3px; font-size: 12px; color: #999; background: transparent; cursor: pointer; }
.input-delete-btn:hover { background: #fff5f5; color: #f56c6c; }
.input-variable-btn { display: flex; align-items: center; gap: 6px; padding: 8px 10px; max-width: 100%; overflow: hidden; }
.input-variable-btn .input-var-key { font-size: 12px; color: #333; flex-shrink: 0; }
.input-variable-btn .input-node-label { font-size: 12px; color: #666; flex-shrink: 0; max-width: 80px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.input-variable-btn .input-node-icon { width: 16px; height: 16px; border-radius: 4px; display: flex; align-items: center; justify-content: center; color: #ffffff; flex-shrink: 0; }
.input-variable-btn .icon-source-flow, .input-variable-btn .icon-source-env, .input-variable-btn .icon-source-argument { font-size: 14px; color: #1890ff; flex-shrink: 0; }
.input-variable-btn .icon-llm { background-color: #409eff; }
.input-variable-btn .icon-code { background-color: #f56565; }
.input-variable-btn .icon-http { background-color: #409eff; }
/* 其余节点图标配色：class 由 nodeIcons.js 给出 */
.input-variable-btn .icon-default { background-color: #6b7280; }
.input-variable-btn .icon-start { background-color: #00704A; }
.input-variable-btn .icon-branch { background-color: #00bcd4; }
.input-variable-btn .icon-wait { background-color: #00bcd4; }
.input-variable-btn .icon-process { background-color: #00bcd4; }
.input-variable-btn .icon-result { background-color: #67c23a; }
.input-variable-btn .icon-progress { background-color: #eea236; }
.input-variable-btn .icon-knowledge { background-color: #67c23a; }
.input-variable-btn .icon-assign { background-color: #ff9800; }
.input-variable-btn .icon-break { background-color: #ff9800; }
.input-variable-btn .icon-continue { background-color: #4caf50; }
.input-variable-btn .icon-return { background-color: #9c27b0; }
.input-variable-btn .icon-function { background-color: #667eea; }
.input-variable-btn .icon-fork { background-color: #4bc0c0; }
.input-variable-btn .icon-call { background-color: #ffc107; }
.input-variable-btn .icon-loop { background-color: #7b1fa2; }
.input-variable-btn .input-type-badge { display: inline-flex; align-items: center; justify-content: center; width: 18px; height: 18px; font-size: 10px; font-weight: bold; color: #fff; flex-shrink: 0; border-radius: 50%; }
.input-variable-btn .input-type-badge.type-string { background: #67c23a; }
.input-variable-btn .input-type-badge.type-number { background: #409eff; }
.input-variable-btn .input-type-badge.type-boolean { background: #9c27b0; }
.input-variable-btn .input-type-badge.type-object { background: #909399; }
.input-variable-btn .input-type-badge.type-array-string { background: #f56c6c; border-radius: 4px; }
.input-variable-btn .input-type-badge.type-array-number { background: #e6a23c; border-radius: 4px; }
.input-variable-btn .input-type-badge.type-array-boolean { background: #9c27b0; border-radius: 4px; }
.input-variable-btn .input-type-badge.type-array-object { background: #909399; border-radius: 4px; }
.messages-list { display: flex; flex-direction: column; gap: 8px; }
.message-row { background: #fff; border: 1px solid #e8e8e8; border-radius: 6px; padding: 6px; }
.message-row-head { display: flex; align-items: center; gap: 8px; margin-bottom: 6px; }
.message-role-select { width: 140px; }
.message-role-select :deep(.el-select__wrapper) { background-color: #eee; border: none; box-shadow: none; }
.message-content { min-height: 60px; }
.handler-lang-row { align-items: center; }
/* 语言编辑框固定宽度，不被 flex 压窄，与 HTTP 节点一致 */
.handler-lang-row .prop-item:first-child { flex: 0 0 auto; }
.handler-lang-row .prop-select { width: 110px; }
.handler-timeout-item { flex: 1; gap: 10px; }
.timeout-input { width: 70px; padding: 6px 8px; border: 1px solid #d9d9d9; border-radius: 4px; font-size: 12px; outline: none; box-sizing: border-box; background-color: #eee; }
.timeout-input:focus { border-color: #91caff; }
.timeout-unit { font-size: 12px; color: #666; flex-shrink: 0; }
.gen-param-row { display: flex; align-items: center; gap: 10px; margin-bottom: 10px; }
.gen-param-row .prop-label { min-width: 90px; }
.gen-param-row .el-input-number { flex: 1; max-width: 180px; }
.gen-param-row.gen-param-extra { margin-bottom: 4px; }
.gen-extra-textarea { min-height: 70px; background-color: #eee; }
.gen-extra-textarea:focus { border-color: #91caff; }
</style>
