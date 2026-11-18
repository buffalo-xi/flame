<template>
  <div class="http-node-props">
    <div class="tabs">
      <button class="tab-btn" :class="{ active: activeTab === 'settings' }" @click="activeTab = 'settings'">配置</button>
      <button class="tab-btn" :class="{ active: activeTab === 'lastRun' }" @click="activeTab = 'lastRun'">执行结果</button>
    </div>

    <div class="tab-content">
      <div v-if="activeTab === 'settings'" class="settings-panel">
        <!-- 输入变量 -->
        <div class="props-section">
          <div class="section-title">
            <span>输入变量</span>
            <button class="add-variable-icon-btn" @click="handleAddInputVariable">
              <el-icon><Plus /></el-icon>
            </button>
          </div>
          <div class="input-variables-list">
            <div v-if="Object.keys(inputVariables).length === 0" class="input-empty-state">
              <p>暂无输入变量</p>
            </div>
            <div v-for="(variable, name) in inputVariables" :key="name" class="input-variable-row">
              <input type="text" :value="name" class="input-variable-name-input"
                @focus="handleInputVariableFocus(name)"
                @blur="handleUpdateInputVariableName(name, $event.target.value)" placeholder="变量名" />
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
                  <template v-else>选择变量</template>
                </button>
                <button class="input-delete-btn" @click="handleDeleteInputVariable(name)">
                  <el-icon><Delete /></el-icon>
                </button>
              </div>
            </div>
          </div>
        </div>

        <!-- 请求配置 -->
        <div class="props-section">
          <div class="section-title"><span>请求配置</span></div>
          <div class="request-config">
            <!-- Method + URL -->
            <div class="request-line">
              <el-select v-model="method" class="method-select" size="small">
                <el-option v-for="m in HTTP_METHODS" :key="m" :label="m" :value="m" />
              </el-select>
              <input type="text" :value="url" class="url-input" placeholder="https://api.example.com/path"
                @blur="handleUpdateUrl($event.target.value)" />
            </div>

            <!-- Query Params / Headers / Body 三 Tab -->
            <div class="sub-tabs">
              <button class="sub-tab-btn" :class="{ active: subTab === 'params' }" @click="subTab = 'params'">
                Params <span v-if="params.length" class="sub-tab-count">{{ params.length }}</span>
              </button>
              <button class="sub-tab-btn" :class="{ active: subTab === 'headers' }" @click="subTab = 'headers'">
                Headers <span v-if="headers.length" class="sub-tab-count">{{ headers.length }}</span>
              </button>
              <button class="sub-tab-btn" :class="{ active: subTab === 'body' }" @click="subTab = 'body'">
                Body
              </button>
            </div>

            <!-- Params 面板 -->
            <div v-if="subTab === 'params'" class="kv-panel">
              <div class="kv-list">
                <div v-if="params.length === 0" class="kv-empty">暂无参数</div>
                <div v-for="(item, idx) in params" :key="'p'+idx" class="kv-row">
                  <input type="text" :value="item.key" class="kv-key-input" placeholder="Key"
                    @blur="handleUpdateKV('params', idx, 'key', $event.target.value)" />
                  <input type="text" :value="item.value" class="kv-value-input" placeholder="Value (支持模板)"
                    @blur="handleUpdateKV('params', idx, 'value', $event.target.value)" />
                  <button class="kv-delete-btn" @click="handleDeleteKV('params', idx)">
                    <el-icon><Delete /></el-icon>
                  </button>
                </div>
              </div>
              <button class="kv-add-btn" @click="handleAddKV('params')">+ 添加参数</button>
            </div>

            <!-- Headers 面板 -->
            <div v-if="subTab === 'headers'" class="kv-panel">
              <div class="kv-list">
                <div v-if="headers.length === 0" class="kv-empty">暂无自定义 Header</div>
                <div v-for="(item, idx) in headers" :key="'h'+idx" class="kv-row">
                  <input type="text" :value="item.key" class="kv-key-input" placeholder="Key"
                    @blur="handleUpdateKV('headers', idx, 'key', $event.target.value)" />
                  <input type="text" :value="item.value" class="kv-value-input" placeholder="Value (支持模板)"
                    @blur="handleUpdateKV('headers', idx, 'value', $event.target.value)" />
                  <button class="kv-delete-btn" @click="handleDeleteKV('headers', idx)">
                    <el-icon><Delete /></el-icon>
                  </button>
                </div>
              </div>
              <button class="kv-add-btn" @click="handleAddKV('headers')">+ 添加 Header</button>
            </div>

            <!-- Body 面板 -->
            <div v-if="subTab === 'body'" class="body-panel">
              <div class="body-type-row">
                <span class="body-type-label">Body 类型</span>
                <el-select v-model="bodyType" class="body-type-select" size="small" @change="handleBodyTypeChange">
                  <el-option label="JSON" value="json" />
                  <el-option label="Form" value="form" />
                  <el-option label="Raw" value="raw" />
                </el-select>
              </div>
              <div v-if="bodyType === 'json'" class="body-editor-wrapper">
                <textarea :value="body" class="body-editor" placeholder='{"key": "value"}' rows="6"
                  @blur="handleUpdateBody($event.target.value)" spellcheck="false"></textarea>
              </div>
              <div v-else-if="bodyType === 'form'" class="kv-panel">
                <div class="kv-list">
                  <div v-if="formBody.length === 0" class="kv-empty">暂无表单字段</div>
                  <div v-for="(item, idx) in formBody" :key="'f'+idx" class="kv-row">
                    <input type="text" :value="item.key" class="kv-key-input" placeholder="Key"
                      @blur="handleUpdateFormBody(idx, 'key', $event.target.value)" />
                    <input type="text" :value="item.value" class="kv-value-input" placeholder="Value (支持模板)"
                      @blur="handleUpdateFormBody(idx, 'value', $event.target.value)" />
                    <button class="kv-delete-btn" @click="handleDeleteFormBody(idx)">
                      <el-icon><Delete /></el-icon>
                    </button>
                  </div>
                </div>
                <button class="kv-add-btn" @click="handleAddFormBody">+ 添加字段</button>
              </div>
              <div v-else class="body-editor-wrapper">
                <textarea :value="body" class="body-editor" placeholder="输入原始请求体..." rows="6"
                  @blur="handleUpdateBody($event.target.value)" spellcheck="false"></textarea>
              </div>
            </div>
          </div>
        </div>

        <!-- 响应模式 & 超时 -->
        <div class="props-section">
          <div class="section-title"><span>响应设置</span></div>
          <div class="response-settings">
            <div class="setting-row">
              <label class="setting-label">识别模式</label>
              <el-radio-group v-model="responseMode" size="small" class="response-mode-radio-group" @change="handleResponseModeChange">
                <el-radio value="">自动</el-radio>
                <el-radio value="complete">强制 Complete</el-radio>
                <el-radio value="sse_stream">强制 SSE</el-radio>
              </el-radio-group>
            </div>
            <div class="setting-row">
              <label class="setting-label">请求超时</label>
              <div class="timeout-row">
                <el-switch v-model="timeoutEnabled" size="small" @change="handleTimeoutToggle" />
                <input v-if="timeoutEnabled" type="number" :value="timeoutSeconds" class="timeout-input"
                  min="1" max="300" @blur="handleTimeoutChange($event.target.value)" />
                <span v-if="timeoutEnabled" class="timeout-unit">秒</span>
              </div>
            </div>
          </div>
        </div>

        <!-- Handler 脚本 -->
        <div class="props-section">
          <div class="section-title">
            <span>响应处理脚本（可选）</span>
            <el-switch v-model="handlerEnabled" size="small" @change="handleHandlerToggle" />
          </div>
          <div v-if="handlerEnabled" class="handler-config">
            <div class="handler-lang-row">
              <label class="handler-label">脚本语言</label>
              <el-select v-model="handlerLang" class="handler-lang-select" size="small" @change="handleHandlerLangChange">
                <el-option v-for="l in handlerLanguages" :key="l" :label="l" :value="l" />
              </el-select>
              <label class="handler-label">脚本超时</label>
              <el-switch v-model="handlerTimeoutEnabled" size="small" @change="handleHandlerTimeoutToggle" />
              <input v-if="handlerTimeoutEnabled" type="number" :value="handlerTimeoutSeconds" class="timeout-input"
                min="1" max="300" @blur="handleHandlerTimeoutChange($event.target.value)" />
              <span v-if="handlerTimeoutEnabled" class="timeout-unit">秒</span>
            </div>
            <div class="handler-hint">
              固定输出变量 <code>mode</code> / <code>status</code> / <code>headers</code> / <code>body</code> /
              <code>sse_events</code> / <code>stream_finished</code> 由节点自动写入（有没有脚本都一样），
              脚本里 return 这些名字会被忽略并记日志；脚本只用于处理自定义输出变量。
              注意 <code>input.headers</code> 是对象，输出变量 <code>headers</code> 是 JSON 字符串。
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
import { ref, watch, computed, inject } from 'vue'
import NodeLastRun from './NodeLastRun.vue'
import { Plus, Delete, Timer, Help, Link, MagicStick, Lightning, Promotion, RefreshRight, CircleClose, ChatLineSquare, Collection, User, Operation, Coin, Minus, Upload, CircleCheck, ScaleToOriginal, Monitor, Mouse, RefreshLeft, ChromeFilled, Notification, Tools } from '@element-plus/icons-vue'
import OutputVarsConfig from './OutputVarsConfig.vue'
import OnErrorConfig from './OnErrorConfig.vue'
import { resolveSourceIcon } from './nodeSourceIcons.js'
import { getDataTypeIcon, getDataTypeIconClass } from './dataTypeBadge.js'
import { HTTP_OUTPUTS } from '../nodeFactory.js'
import { useInputVariables } from './inputVariables.js'
import { useHandlerConfig } from './handlerConfig.js'

const props = defineProps({
  selectedNode: { type: Object, default: null },
  flowFlowVariables: { type: Object, default: () => ({}) },
  flowEnvironmentVariables: { type: Object, default: () => ({}) },
  flowArguments: { type: Object, default: () => ({}) }
})

const emit = defineEmits(['update-node-data', 'show-variable-select'])

const FlowNodes = inject('FlowNodes', ref([]))
// ── 常量 ──
const HTTP_METHODS = ['GET', 'POST', 'PUT', 'DELETE', 'PATCH']
// 固定输出名由 nodeFactory 的默认定义派生，避免两处各列一遍
const FIXED_OUTPUTS = Object.keys(HTTP_OUTPUTS)

// scriptLanguages from provide
const scriptLanguages = inject('scriptLanguages', ['lua', 'python', 'javascript', 'template'])
// handler 语言：仅 lua/python/javascript，排除 template（handler 需副作用回调）
const handlerLanguages = computed(() => scriptLanguages.value.filter(l => l !== 'template'))

// ── tabs ──
const activeTab = ref('settings')
const subTab = ref('params')

// ── 上次运行 ──


// ── 输入变量 ──
const inputVariables = ref({})

// ── 请求配置 ──
const method = ref('GET')
const url = ref('')
const params = ref([])
const headers = ref([])
const bodyType = ref('json')
const body = ref('')
const formBody = ref([])

// ── 响应配置 ──
const responseMode = ref('')
const timeoutEnabled = ref(false)
const timeoutSeconds = ref(30)

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
  onChange: () => saveHttpConfig()
})

const handlerPlaceholder = computed(() => {
  return `-- 响应数据通过 input 传入：
-- input.mode     - "complete" | "sse_stream"
-- input.status   - HTTP 状态码
-- input.headers  - 响应头（对象）
-- input.body     - 响应体
-- input.event    - SSE 事件对象 {id, event, data}
-- input.done     - 流关闭标记 (true/false)
--
-- 固定输出变量 mode / status / headers / body / sse_events / stream_finished
-- 由节点自动写入，脚本不要写它们；脚本只写自定义输出变量：
-- return { text = input.body }   (string 追加、非 string 覆盖)`
})

// ── 输入变量：增删改 / 校验 / 快照统一在 inputVariables.js ──
const {
  snapshot: buildInputsToSave,
  addInputVariable,
  updateInputVariableName,
  deleteInputVariable,
  applySelectedVariable
} = useInputVariables({
  inputVariables,
  onChange: () => saveHttpConfig()
})

const handleAddInputVariable = addInputVariable
const handleUpdateInputVariableName = updateInputVariableName
const handleDeleteInputVariable = deleteInputVariable
const handleInputVariableFocus = () => {}

const handleShowInputVariableSelect = (name, event) => {
  emit('show-variable-select', event, name, (value) => applySelectedVariable(name, value))
}

// ── 请求配置方法 ──
const handleUpdateUrl = (value) => {
  url.value = value
  saveHttpConfig()
}

const handleAddKV = (type) => {
  if (type === 'params') {
    params.value.push({ key: '', value: '' })
  } else {
    headers.value.push({ key: '', value: '' })
  }
  saveHttpConfig()
}

const handleUpdateKV = (type, idx, field, value) => {
  const list = type === 'params' ? params.value : headers.value
  if (!list[idx]) return
  list[idx][field] = value
  saveHttpConfig()
}

const handleDeleteKV = (type, idx) => {
  if (type === 'params') {
    params.value.splice(idx, 1)
  } else {
    headers.value.splice(idx, 1)
  }
  saveHttpConfig()
}

const handleBodyTypeChange = () => {
  if (bodyType.value === 'form') {
    // 尝试将 JSON body 解析为 form
    if (body.value) {
      try {
        const obj = JSON.parse(body.value)
        formBody.value = Object.keys(obj).map(k => ({ key: k, value: String(obj[k] ?? '') }))
      } catch {
        formBody.value = []
      }
    }
  } else if (bodyType.value === 'json') {
    // 将 form 转回 JSON
    if (formBody.value.length > 0) {
      const obj = {}
      formBody.value.forEach(item => { if (item.key) obj[item.key] = item.value })
      body.value = JSON.stringify(obj, null, 2)
      formBody.value = []
    }
  }
  saveHttpConfig()
}

const handleUpdateBody = (value) => {
  body.value = value
  saveHttpConfig()
}

const handleUpdateFormBody = (idx, field, value) => {
  if (!formBody.value[idx]) return
  formBody.value[idx][field] = value
  saveHttpConfig()
}

const handleDeleteFormBody = (idx) => {
  formBody.value.splice(idx, 1)
  saveHttpConfig()
}

const handleAddFormBody = () => {
  formBody.value.push({ key: '', value: '' })
  saveHttpConfig()
}

// ── 响应配置方法 ──
const handleResponseModeChange = () => {
  saveHttpConfig()
}

const handleTimeoutToggle = () => {
  saveHttpConfig()
}

const handleTimeoutChange = (value) => {
  const num = parseInt(value)
  if (!isNaN(num) && num > 0) timeoutSeconds.value = num
  saveHttpConfig()
}

// ── 保存 ──
const saveHttpConfig = () => {
  // 构建 http 配置
  const httpCfg = {
    method: method.value,
    url: url.value,
    params: params.value.filter(p => p.key),
    headers: headers.value.filter(h => h.key),
    body_type: bodyType.value,
    body: bodyType.value === 'form'
      ? formBody.value.filter(f => f.key).map(f => `${encodeURIComponent(f.key)}=${encodeURIComponent(f.value)}`).join('&')
      : body.value,
    response_mode: responseMode.value,
    timeout: {
      enabled: timeoutEnabled.value,
      seconds: timeoutSeconds.value
    }
  }

  httpCfg.handler = handlerBuild()

  const inputs = buildInputsToSave()

  emit('update-node-data', {
    nodeId: props.selectedNode.id,
    data: { config: { http: httpCfg, inputs } }
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

    // 输入变量
    const inputs = cfg.inputs || {}
    inputVariables.value = {}
    for (const name in inputs) {
      if (inputs.hasOwnProperty(name)) {
        inputVariables.value[name] = { ...inputs[name] }
      }
    }

    // HTTP 配置
    const httpCfg = cfg.http || {}
    method.value = httpCfg.method || 'GET'
    url.value = httpCfg.url || ''
    params.value = (httpCfg.params || []).map(p => ({ key: p.key || '', value: p.value || '' }))
    headers.value = (httpCfg.headers || []).map(h => ({ key: h.key || '', value: h.value || '' }))
    bodyType.value = httpCfg.body_type || 'json'
    body.value = httpCfg.body || ''
    responseMode.value = httpCfg.response_mode || ''

    // 超时
    if (httpCfg.timeout) {
      timeoutEnabled.value = !!httpCfg.timeout.enabled
      timeoutSeconds.value = httpCfg.timeout.seconds || 30
    }

    // Handler（没有 handler 时同样重置，避免残留上一个节点的脚本）
    handlerLoad(httpCfg.handler)

    // Body type 为 form 时尝试解析
    if (bodyType.value === 'form' && body.value) {
      try {
        formBody.value = body.value.split('&').filter(Boolean).map(pair => {
          const [k, v] = pair.split('=')
          return { key: decodeURIComponent(k || ''), value: decodeURIComponent(v || '') }
        })
      } catch { formBody.value = [] }
    }
  }
}, { immediate: true })
</script>

<style scoped>
.http-node-props {
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
.add-output-btn { background: #409eff; }
.add-output-btn:hover { background: #66b1ff; }

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

/* ── 请求配置 ── */
.request-config { margin-top: 8px; }
.request-line {
  display: flex; align-items: center; gap: 8px;
}
.method-select {
  flex-shrink: 0;
  width: 90px;
}
.url-input {
  flex: 1;
  height: 32px;
  padding: 4px 10px;
  border: 0;
  background: #eee;
  border-radius: 4px;
  font-size: 13px;
  font-family: Monaco, Menlo, monospace;
  outline: none;
}
.url-input:focus { background: #fafafa; }
.url-input::placeholder { color: #bbb; }

.sub-tabs {
  display: flex;
  gap: 4px;
  margin-top: 8px;
  border-bottom: 1px solid #e8e8e8;
  padding-bottom: 8px;
}
.sub-tab-btn {
  padding: 4px 12px;
  border: none;
  background: transparent;
  font-size: 12px;
  color: #666;
  cursor: pointer;
  border-radius: 4px;
  transition: all 0.2s;
}
.sub-tab-btn:hover { background: #f0f0f0; color: #333; }
.sub-tab-btn.active { background: #e6f7ff; color: #1890ff; font-weight: 500; }
.sub-tab-count {
  display: inline-flex; align-items: center; justify-content: center;
  min-width: 16px; height: 16px; padding: 0 4px;
  background: #1890ff; color: #fff; font-size: 10px; border-radius: 8px; margin-left: 4px;
}

/* ── KV 面板 ── */
.kv-panel { margin-top: 8px; }
.kv-list { display: flex; flex-direction: column; gap: 4px; }
.kv-empty { color: #999; font-size: 12px; padding: 8px; text-align: center; }
.kv-row { display: flex; align-items: center; gap: 4px; }
.kv-key-input, .kv-value-input {
  flex: 1;
  height: 28px;
  padding: 2px 8px;
  border: 0;
  background: #eee;
  border-radius: 4px;
  font-size: 12px;
  outline: none;
}
.kv-key-input:focus, .kv-value-input:focus { background: #fafafa; }
.kv-key-input::placeholder, .kv-value-input::placeholder { color: #bbb; }
.kv-delete-btn {
  display: flex; align-items: center; justify-content: center; width: 24px; height: 24px; padding: 0; border: none; border-radius: 3px; color: #999; background: transparent; cursor: pointer; flex-shrink: 0;
}
.kv-delete-btn:hover { background: #fff5f5; color: #f56c6c; }
.kv-add-btn {
  margin-top: 6px;
  padding: 4px 12px;
  border: 1px dashed #ccc;
  background: transparent;
  border-radius: 4px;
  font-size: 12px;
  color: #1890ff;
  cursor: pointer;
  transition: all 0.2s;
}
.kv-add-btn:hover { border-color: #1890ff; background: #e6f7ff; }

/* ── Body 面板 ── */
.body-panel { margin-top: 8px; }
.body-type-row { display: flex; align-items: center; gap: 8px; margin-bottom: 8px; }
.body-type-label { font-size: 12px; color: #666; flex-shrink: 0; }
.body-type-select { width: 100px; }
.body-editor {
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
.body-editor:focus { background: #fafafa; }
.body-editor::placeholder { color: #bbb; }

/* ── 响应设置 ── */
.response-settings { margin-top: 8px; display: flex; flex-direction: column; gap: 8px; }
.setting-row { display: flex; align-items: center; gap: 8px; }
.setting-label { font-size: 12px; color: #666; min-width: 70px; flex-shrink: 0; }
.setting-select { flex: 1; min-width: 0; }
.response-mode-radio-group {
  display: flex;
  gap: 12px;
  flex-wrap: wrap;
  flex: 1;
}

.timeout-row { display: flex; align-items: center; gap: 6px; }
.timeout-input {
  width: 60px; height: 24px; padding: 2px 6px;
  border: 0; background: #eee; border-radius: 4px;
  font-size: 12px; text-align: center; outline: none;
}
.timeout-input:focus { background: #fafafa; }
.timeout-unit { font-size: 12px; color: #999; }

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

/* ── Handler ── */
.dropdown-icon {
  display: inline-flex; align-items: center; justify-content: center; width: 14px; height: 14px; font-size: 10px; font-weight: bold; font-family: Arial, sans-serif; color: #fff; margin-right: 6px; flex-shrink: 0;
}
.icon-string-inline { background: #67c23a; border-radius: 50%; }
.icon-number-inline { background: #409eff; border-radius: 50%; }
.icon-boolean-inline { background: #9c27b0; border-radius: 50%; }
.icon-object-inline { background: #909399; border-radius: 50%; }
.icon-string-array-inline { background: #f56c6c; border-radius: 4px; }
.icon-number-array-inline { background: #e6a23c; border-radius: 4px; }
.icon-boolean-array-inline { background: #9c27b0; border-radius: 4px; }
.icon-object-array-inline { background: #909399; border-radius: 4px; }

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
