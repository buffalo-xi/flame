<template>
  <div class="code-node-props">
    <div class="tabs">
      <button
        class="tab-btn"
        :class="{ active: activeTab === 'settings' }"
        @click="activeTab = 'settings'"
      >
        配置
      </button>
      <button
        class="tab-btn"
        :class="{ active: activeTab === 'lastRun' }"
        @click="activeTab = 'lastRun'"
      >
        执行结果
      </button>
    </div>

    <div class="tab-content">
      <div v-if="activeTab === 'settings'" class="settings-panel">
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
            
            <div
              v-for="(variable, name) in inputVariables"
              :key="name"
              class="input-variable-row"
            >
              <input
                type="text"
                :value="name"
                class="input-variable-name-input"
                @focus="handleInputVariableFocus(name, name)"
                @blur="handleUpdateInputVariableName(name, $event.target.value)"
                placeholder="变量名"
              />
              <div class="input-variable-content">
                <button 
                  class="show-content-btn input-variable-btn" 
                  @click="handleShowVariableSelect(name, $event, variable)"
                >
                  <template v-if="variable.source_key">
                    <span class="input-type-badge" :class="getDataTypeIconClass(variable.type)">
                      {{ getDataTypeIcon(variable.type) }}
                    </span>
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
                  <template v-else>
                    选择变量
                  </template>
                </button>
                <button class="input-delete-btn" @click="handleDeleteInputVariable(name)">
                  <el-icon><Delete /></el-icon>
                </button>
              </div>
            </div>
          </div>
        </div>

        <div class="props-section">
          <div class="prop-group">
            <textarea
              class="prop-textarea"
              v-model="codeContent"
              @keydown="handleCodeKeydown"
              @blur="handleCodeChange"
              placeholder="输入要执行的代码..."
            ></textarea>
          </div>

          <div class="prop-group-horizontal">
            <div class="prop-item">
              <label class="prop-label">语言</label>
              <el-select v-model="codeLanguage" class="prop-select" @change="handleLanguageChange">
                <el-option v-for="lang in codeScriptLanguages" :key="lang" :label="lang" :value="lang" />
              </el-select>
            </div>
            <div class="prop-item">
              <label class="prop-label">超时控制</label>
              <el-switch
                v-model="timeoutEnabled"
                @change="handleTimeoutEnabledChange"
              />
              <el-input
                v-if="timeoutEnabled"
                type="number"
                v-model="timeout"
                class="prop-input timeout-input"
                @blur="handleTimeoutChange"
                :min="1"
                :max="3600"
                placeholder="30"
              />
              <span v-if="timeoutEnabled" class="unit-text">秒</span>
            </div>
          </div>
        </div>

        <div class="props-section">
          <div class="section-title">
            <span>写目标变量</span>
            <button class="add-variable-icon-btn" @click="handleAddTargetVariable">
              <el-icon><Plus /></el-icon>
            </button>
          </div>

          <div class="input-variables-list">
            <div v-if="Object.keys(targetVariables).length === 0" class="input-empty-state">
              <p>暂无写目标变量</p>
            </div>

            <div
              v-for="(variable, name) in targetVariables"
              :key="name"
              class="input-variable-row"
            >
              <input
                type="text"
                :value="name"
                class="input-variable-name-input"
                @focus="handleTargetVariableFocus(name, name)"
                @blur="handleUpdateTargetVariableName(name, $event.target.value)"
                placeholder="变量名"
              />
              <div class="input-variable-content">
                <button
                  class="show-content-btn input-variable-btn"
                  @click="handleShowTargetVariableSelect(name, $event, variable)"
                >
                  <template v-if="variable.source_key">
                    <span class="input-type-badge" :class="getDataTypeIconClass(variable.type)">
                      {{ getDataTypeIcon(variable.type) }}
                    </span>
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
                  <template v-else>
                    选择变量
                  </template>
                </button>
                <button class="input-delete-btn" @click="handleDeleteTargetVariable(name)">
                  <el-icon><Delete /></el-icon>
                </button>
              </div>
            </div>
          </div>
        </div>

        <OutputVarsConfig
          :selected-node="selectedNode"
          :existing-names="existingOutputNames"
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
import { ref, watch, computed, inject, onMounted, onUnmounted, nextTick } from 'vue'
import { ElSelect, ElOption, ElInput, ElRadio, ElRadioGroup, ElSwitch } from 'element-plus'
import { Plus, ArrowDown, Delete, ChromeFilled, Notification, User, Timer, Help, Link, MagicStick, Lightning, Promotion, RefreshRight, CircleClose, ChatLineSquare, Collection, Operation, Coin, Minus, Upload, CircleCheck, ScaleToOriginal, Monitor, Mouse, RefreshLeft } from '@element-plus/icons-vue'
import OnErrorConfig from './OnErrorConfig.vue'
import OutputVarsConfig from './OutputVarsConfig.vue'
import NodeLastRun from './NodeLastRun.vue'
import { resolveSourceIcon } from './nodeSourceIcons.js'
import { getDataTypeIcon, getDataTypeIconClass } from './dataTypeBadge.js'
import { useInputVariables } from './inputVariables.js'

const props = defineProps({
  selectedNode: {
    type: Object,
    default: null
  },
  flowFlowVariables: {
    type: Object,
    default: () => ({})
  },
  flowEnvironmentVariables: {
    type: Object,
    default: () => ({})
  },
  flowArguments: {
    type: Object,
    default: () => ({})
  }
})

const emit = defineEmits(['update-node-data', 'show-variable-select'])

const FlowNodes = inject('FlowNodes', ref([]))
const scriptLanguages = inject('scriptLanguages', ref(['lua']))
const defaultLang = computed(() => scriptLanguages.value[0] || 'lua')

// Code 节点只用沙箱提供的语言（真源 = sandboxd 的 script_langs）。
// template 不走 worker、且无超时/隔离，已从 Code 节点移除 —— 它更适合
// Result/Progress 这类纯格式化节点（那两处仍保留）。
// 显式 filter 一下，与 RpcNodeProps 的 handlerLanguages 保持同一种写法。
const codeScriptLanguages = computed(() => scriptLanguages.value.filter(l => l !== 'template'))

const handleShowVariableSelect = (name, event) => {
  emit('show-variable-select', event, name, (value) => applySelectedVariable(name, value))
}
const FlowEdges = inject('FlowEdges', ref([]))



// 变量来源图标：实现在 nodeSourceIcons.js（12 个面板共用，避免 source 类型再漂移）
const getSourceIcon = (sourceType, sourceRef) =>
  resolveSourceIcon(sourceType, sourceRef, FlowNodes.value)

const activeTab = ref('settings')


const codeContent = ref('')
const codeLanguage = ref('lua')
const timeout = ref(30)
const timeoutEnabled = ref(false)

const inputVariables = ref({})

const getOutputVarNames = () => Object.keys(props.selectedNode?.data?.config?.outputs || {})

const existingOutputNames = computed(() => [
  ...Object.keys(inputVariables.value),
  ...Object.keys(targetVariables.value)
])

const targetVariables = ref({})

const generateTargetVariableName = () => {
  let counter = 1
  const inputNames = Object.keys(inputVariables.value)
  const outputNames = getOutputVarNames()
  while (true) {
    const name = `target${counter}`
    if (!targetVariables.value[name] && !inputNames.includes(name) && !outputNames.includes(name)) {
      return name
    }
    counter++
  }
}

const isTargetVariableNameExists = (name, excludeName = '') => {
  if (name === excludeName) return false
  if (targetVariables.value[name] !== undefined) return true
  if (inputVariables.value[name] !== undefined) return true
  if (getOutputVarNames().includes(name)) return true
  return false
}

const handleShowTargetVariableSelect = (name, event, variable) => {
  const callback = (value) => {
    try {
      const data = JSON.parse(value)
      targetVariables.value[name] = {
        ...targetVariables.value[name],
        source_key: data.source_key,
        source_ref: data.source_ref,
        source_type: data.source_type,
        type: data.variableType || ''
      }
      saveTargetVariables()
    } catch (e) {
      console.error('Failed to parse variable value:', e)
    }
  }
  emit('show-variable-select', event, name, callback, 'all', 'target')
}

const handleTargetVariableFocus = (name, value) => {
}

const handleAddTargetVariable = () => {
  const newName = generateTargetVariableName()
  // 写目标不应预设来源：由用户在选择器里挑祖先容器的输出/局部变量或流程变量；
  // env / argument / builtin 是运行期只读输入，不作为候选
  targetVariables.value[newName] = {
    source_type: '',
    source_ref: '',
    source_key: '',
    type: ''
  }
  saveTargetVariables()
}

const handleUpdateTargetVariableName = (oldName, value) => {
  const trimmedValue = value.trim()

  if (trimmedValue === '') {
    targetVariables.value = { ...targetVariables.value }
    return
  }

  if (trimmedValue.includes(' ')) {
    targetVariables.value = { ...targetVariables.value }
    return
  }

  if (!validateInputKey(trimmedValue)) {
    targetVariables.value = { ...targetVariables.value }
    return
  }

  if (isTargetVariableNameExists(trimmedValue, oldName)) {
    targetVariables.value = { ...targetVariables.value }
    return
  }

  if (oldName === trimmedValue) {
    targetVariables.value = { ...targetVariables.value }
    return
  }

  const newVariables = { ...targetVariables.value }
  newVariables[trimmedValue] = { ...newVariables[oldName] }
  delete newVariables[oldName]
  targetVariables.value = newVariables
  saveTargetVariables()
}

const handleDeleteTargetVariable = (name) => {
  const newVariables = { ...targetVariables.value }
  delete newVariables[name]
  targetVariables.value = newVariables
  saveTargetVariables()
}

const saveTargetVariables = () => {
  const targetsToSave = {}
  Object.keys(targetVariables.value).forEach(name => {
    const v = targetVariables.value[name]
    // targets 是写目标，不能默认成 argument（运行上下文只读），未选来源时保持空串
    targetsToSave[name] = {
      source_type: v.source_type || '',
      source_ref: v.source_ref || '',
      source_key: v.source_key || '',
      type: v.type || ''
    }
  })

  emit('update-node-data', {
    nodeId: props.selectedNode.id,
    data: {
      config: {
        targets: targetsToSave
      }
    }
  })
}



watch(() => props.selectedNode, (node) => {
  if (node && node.data) {
    codeContent.value = node.data.config?.code?.script || ''
    codeLanguage.value = node.data.config?.code?.lang || defaultLang.value
    timeout.value = node.data.config?.code?.timeout?.seconds || 30
    timeoutEnabled.value = node.data.config?.code?.timeout?.enabled ?? false
    
    const inputs = node.data.config?.inputs || {}
    inputVariables.value = {}
    for (const name in inputs) {
      if (inputs.hasOwnProperty(name)) {
        inputVariables.value[name] = {
          ...inputs[name],
          name: name
        }
      }
    }

    const targets = node.data.config?.targets || {}
    targetVariables.value = {}
    for (const name in targets) {
      if (targets.hasOwnProperty(name)) {
        targetVariables.value[name] = {
          ...targets[name],
          name: name
        }
      }
    }
  }
}, { immediate: true })

const handleCodeKeydown = (event) => {
  if (event.key === 'Tab') {
    event.preventDefault()
    const textarea = event.target
    const start = textarea.selectionStart
    const end = textarea.selectionEnd
    const spaces = '   '
    codeContent.value = codeContent.value.substring(0, start) + spaces + codeContent.value.substring(end)
    nextTick(() => {
      textarea.selectionStart = textarea.selectionEnd = start + spaces.length
    })
  }
}

// 节点配置的唯一组装点：code 块（lang/script/timeout）+ 顶层 inputs。
// inputs 只放顶层：后端 parseScriptNode 读的是 config.inputs；
// 早先塞进 code.inputs 的那份副本没有任何读取方，已去掉。
const buildConfig = () => {
  const inputs = snapshotInputs()

  const seconds = Math.max(1, Math.floor(timeout.value))
  if (seconds !== timeout.value) timeout.value = seconds

  return {
    inputs,
    code: {
      lang: codeLanguage.value,
      script: codeContent.value,
      timeout: {
        enabled: timeoutEnabled.value,
        seconds
      }
    }
  }
}

const saveConfig = () => {
  emit('update-node-data', {
    nodeId: props.selectedNode.id,
    data: { config: buildConfig() }
  })
}

// 各处保存入口共用一份组装，避免再出现「某个入口少写一个字段」
const handleCodeChange = () => saveConfig()
const handleLanguageChange = () => saveConfig()
const handleTimeoutChange = () => saveConfig()
const handleTimeoutEnabledChange = () => saveConfig()

// ── 输入变量：增删改 / 校验 / 快照统一在 inputVariables.js ──
const {
  snapshot: snapshotInputs,
  validateInputKey,
  addInputVariable,
  updateInputVariableName,
  deleteInputVariable,
  applySelectedVariable
} = useInputVariables({
  inputVariables,
  // 输入名不能和写目标、节点输出重名
  reservedNames: () => [...Object.keys(targetVariables.value), ...getOutputVarNames()],
  onChange: () => saveConfig()
})

const handleInputVariableFocus = (name, value) => {}
const handleAddInputVariable = addInputVariable
const handleUpdateInputVariableName = updateInputVariableName
const handleDeleteInputVariable = deleteInputVariable

const saveInputVariables = () => saveConfig()

</script>

<style scoped>
.code-node-props {
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

.tab-btn:hover {
  color: #1890ff;
}

.tab-btn.active {
  color: #1890ff;
  font-weight: 500;
}

.tab-btn.active::after {
  content: '';
  position: absolute;
  bottom: -1px;
  left: 0;
  right: 0;
  height: 1px;
  background: #1890ff;
}

.tab-content {
  flex: 1;
  overflow-y: auto;
  padding: 8px;
  /* 滚动条默认透明占位，鼠标进入面板才显示：避免滚动条出现时把内容挤动 */
  scrollbar-width: thin;
  scrollbar-color: transparent transparent;
}

.tab-content:hover {
  scrollbar-color: #c1c1c1 transparent;
}

.tab-content::-webkit-scrollbar {
  width: 8px;
  height: 8px;
}

.tab-content::-webkit-scrollbar-track {
  background: transparent;
}

.tab-content::-webkit-scrollbar-thumb {
  background: transparent;
  border-radius: 4px;
}

.tab-content:hover::-webkit-scrollbar-thumb {
  background: #c1c1c1;
}

.settings-panel {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.props-section {
  background: #fafafa;
  border-radius: 8px;
  padding: 5px;
}

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

.add-variable-icon-btn:hover {
  background: #40a9ff;
}

.prop-group {
  display: flex;
  flex-direction: column;
  gap: 6px;
  margin-bottom: 12px;
}

.prop-group-horizontal {
  display: flex;
  gap: 16px;
  margin-bottom: 12px;
}

.prop-item {
  flex: 1;
  display: flex;
  flex-direction: row;
  align-items: center;
  gap: 8px;
  flex-wrap: nowrap;
  white-space: nowrap;
}

.prop-label {
  font-size: 12px;
  color: #666;
  font-weight: 500;
  flex-shrink: 0;
}

.prop-group:last-child {
  margin-bottom: 0;
}

.prop-textarea {
  width: 100%;
  min-height: 170px;
  padding: 10px;
  border: 1px solid #d9d9d9;
  border-radius: 4px;
  font-size: 12px;
  font-family: 'Monaco', 'Menlo', monospace;
  line-height: 1.7;
  resize: vertical;
  outline: none;
  box-sizing: border-box;
  background-color: #eee;
  /* 编辑框自己的滚动条：只有鼠标进到框里（或正在编辑）才显示 */
  scrollbar-width: thin;
  scrollbar-color: transparent transparent;
}

.prop-textarea:hover,
.prop-textarea:focus {
  scrollbar-color: #c1c1c1 transparent;
}

.prop-textarea::-webkit-scrollbar {
  width: 8px;
  height: 8px;
}

.prop-textarea::-webkit-scrollbar-track {
  background: transparent;
}

.prop-textarea::-webkit-scrollbar-thumb {
  background: transparent;
  border-radius: 4px;
}

.prop-textarea:hover::-webkit-scrollbar-thumb,
.prop-textarea:focus::-webkit-scrollbar-thumb {
  background: #c1c1c1;
}

.prop-textarea:focus {
  border-color: #91caff;
}

.prop-textarea::placeholder {
  color: #bbb;
}

.prop-select {
  width: 100%;
}

.prop-select :deep(.el-select__wrapper) {
  background-color: #eee;
  border: none;
  box-shadow: none;
}

.prop-select :deep(.el-select__wrapper:hover) {
  border: none;
  box-shadow: none;
}

.prop-select :deep(.el-select__wrapper.is-focus) {
  border: none;
  box-shadow: none;
}

.prop-input {
  width: 100%;
}

.timeout-input {
  width: 80px;
  flex-shrink: 0;
}

.timeout-input :deep(.el-input__wrapper) {
  background-color: #eee;
  border: none;
  box-shadow: none;
}

.timeout-input :deep(.el-input__wrapper:hover) {
  border: none;
  box-shadow: none;
}

.timeout-input :deep(.el-input__wrapper.is-focus) {
  border: none;
  box-shadow: none;
}

.unit-text {
  font-size: 12px;
  color: #666;
  flex-shrink: 0;
}

.radio-group {
  display: flex;
  gap: 20px;
}

:deep(.el-switch) {
  margin-top: 4px;
}

.last-run-panel {
  padding: 0;
}

.result-item {
  display: flex;
  flex-direction: column;
  gap: 6px;
  margin-bottom: 12px;
}

.result-label {
  font-size: 12px;
  color: #666;
}

.result-value {
  font-size: 13px;
  color: #333;
}

.status-success {
  color: #67c23a;
}

.status-failed {
  color: #f56565;
}

.status-running {
  color: #409eff;
}

.status-default {
  color: #999;
}

.result-output {
  width: 100%;
  min-height: 100px;
  padding: 10px;
  border: 1px solid #d9d9d9;
  border-radius: 4px;
  font-size: 12px;
  color: #333;
  background: #fafafa;
  overflow-x: auto;
  white-space: pre-wrap;
  word-break: break-all;
}

.input-variables-list {
  display: flex;
  flex-direction: column;
  gap: 6px;
  margin-top: 8px;
}

.input-empty-state {
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 3px;
  text-align: center;
}

.input-empty-state p {
  color: #999;
  margin: 0;
  font-size: 12px;
}

.input-variable-row {
  display: flex;
  align-items: center;
  gap: 8px;
  /* background: #ffffff; */
  /* border: 1px solid #e8e8e8; */
  border-radius: 6px;
  padding: 0px 0px;
}

.input-variable-name-input {
  flex: 1;
  min-width: 0;
  max-width: 33.33%;
  padding: 10px 10px;
  border: 0px solid #d9d9d9;
  background-color: #eeeeee;
  border-radius: 4px;
  font-size: 13px;
  outline: none;
  transition: border-color 0.2s;
}

.input-variable-content {
  flex: 2;
  display: flex;
  align-items: center;
  gap: 8px;
}

.show-content-btn {
  flex: 1;
  min-width: 0;
}

.input-variable-name-input:focus {
  outline: none;
}

.input-variable-name-input::placeholder {
  color: #bbb;
}

.show-content-btn {
  padding: 10px 12px;
  border: 0px solid #1890ff;
  border-radius: 4px;
  background: #eee;
  color: #1890ff;
  font-size: 12px;
  cursor: pointer;
  transition: all 0.2s;
  flex-shrink: 0;
}

.show-content-btn:hover:not(:disabled) {
  background: #e6f7ff;
}

.show-content-btn:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}

.input-delete-btn {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 20px;
  height: 20px;
  padding: 0;
  border: none;
  border-radius: 3px;
  font-size: 12px;
  color: #999;
  background: transparent;
  cursor: pointer;
  transition: all 0.2s;
  flex-shrink: 0;
}

.input-delete-btn:hover {
  background: #fff5f5;
  color: #f56c6c;
}

.dropdown-icon {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 14px;
  height: 14px;
  font-size: 10px;
  font-weight: bold;
  font-family: Arial, sans-serif;
  color: #fff;
  margin-right: 6px;
  flex-shrink: 0;
}

.dropdown-icon.icon-number-inline {
  background: #409eff;
  border-radius: 50%;
}

.dropdown-icon.icon-string-inline {
  background: #67c23a;
  border-radius: 50%;
}

.dropdown-icon.icon-boolean-inline {
  background: #9c27b0;
  border-radius: 50%;
}

.dropdown-icon.icon-object-inline {
  background: #909399;
  border-radius: 50%;
}

.dropdown-icon.icon-string-array-inline {
  background: #f56c6c;
  border-radius: 4px;
}

.dropdown-icon.icon-number-array-inline {
  background: #e6a23c;
  border-radius: 4px;
}

.dropdown-icon.icon-boolean-array-inline {
  background: #9c27b0;
  border-radius: 4px;
}

.dropdown-icon.icon-object-array-inline {
  background: #909399;
  border-radius: 4px;
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

.input-variable-btn {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 8px 10px;
  max-width: 100%;
  overflow: hidden;
}

.input-variable-btn .input-var-key {
  font-size: 12px;
  color: #333;
  flex-shrink: 0;
}

.input-variable-btn .input-node-label {
  font-size: 12px;
  color: #666;
  flex-shrink: 0;
  max-width: 80px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.input-variable-btn .input-node-icon {
  width: 16px;
  height: 16px;
  border-radius: 4px;
  display: flex;
  align-items: center;
  justify-content: center;
  color: #ffffff;
  flex-shrink: 0;
}

.input-variable-btn .input-node-icon.el-icon {
  font-size: 10px;
}

.input-variable-btn .icon-source-flow,
.input-variable-btn .icon-source-env,
.input-variable-btn .icon-source-argument,
.input-variable-btn .icon-source-builtin {
  font-size: 14px;
  color: #1890ff;
  flex-shrink: 0;
}

.input-variable-btn .icon-start {
  background-color: #00704A;
}

.input-variable-btn .icon-branch {
  background-color: #00bcd4;
}

.input-variable-btn .icon-wait {
  background-color: #00bcd4;
}

.input-variable-btn .icon-code {
  background-color: #f56565;
}

.input-variable-btn .icon-http {
  background-color: #409eff;
}

.input-variable-btn .icon-process {
  background-color: #00bcd4;
}

.input-variable-btn .icon-result {
  background-color: #67c23a;
}

.input-variable-btn .icon-progress {
  background-color: #eea236;
}

.input-variable-btn .icon-llm {
  background-color: #409eff;
}

.input-variable-btn .icon-knowledge {
  background-color: #67c23a;
}

.input-variable-btn .icon-agent {
  background-color: #f56c6c;
}

.input-variable-btn .icon-subflow {
  background-color: #667eea;
}

.input-variable-btn .icon-assign {
  background-color: #ff9800;
}

.input-variable-btn .icon-break {
  background-color: #ff9800;
}

.input-variable-btn .icon-continue {
  background-color: #4caf50;
}

.input-variable-btn .icon-return {
  background-color: #9c27b0;
}

.input-variable-btn .icon-function {
  background-color: #667eea;
}

.input-variable-btn .icon-fork {
  background-color: #4bc0c0;
}

.input-variable-btn .icon-call {
  background-color: #ffc107;
}

.input-variable-btn .icon-loop {
  background-color: #7b1fa2;
}

.input-variable-btn .icon-default {
  background-color: #6b7280;
}

.input-variable-btn .input-type-badge {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 18px;
  height: 18px;
  font-size: 10px;
  font-weight: bold;
  font-family: Arial, sans-serif;
  color: #fff;
  flex-shrink: 0;
  border-radius: 50%;
}

.input-variable-btn .input-type-badge.type-string {
  background: #67c23a;
}

.input-variable-btn .input-type-badge.type-number {
  background: #409eff;
}

.input-variable-btn .input-type-badge.type-boolean {
  background: #9c27b0;
}

.input-variable-btn .input-type-badge.type-object {
  background: #909399;
}

.input-variable-btn .input-type-badge.type-array-string {
  background: #f56c6c;
  border-radius: 4px;
}

.input-variable-btn .input-type-badge.type-array-number {
  background: #e6a23c;
  border-radius: 4px;
}

.input-variable-btn .input-type-badge.type-array-boolean {
  background: #9c27b0;
  border-radius: 4px;
}

.input-variable-btn .input-type-badge.type-array-object {
  background: #909399;
  border-radius: 4px;
}
</style>

<style>
/* 下拉菜单右侧与窗口对齐 */
.dropdown-align-right {
  margin-right: 8px !important;
}
</style>