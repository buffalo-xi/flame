<template>
  <div class="intent-node-props">
    <div class="tabs">
      <button
        class="tab-btn"
        :class="{ active: activeTab === 'settings' }"
        @click="activeTab = 'settings'"
      >
        {{ t('flow.nodeProps.tabConfig') }}
      </button>
      <button
        class="tab-btn"
        :class="{ active: activeTab === 'lastRun' }"
        @click="activeTab = 'lastRun'"
      >
        {{ t('flow.nodeProps.tabResult') }}
      </button>
    </div>

    <div class="tab-content">
      <div v-if="activeTab === 'settings'" class="settings-panel">
        <!-- 模型配置 -->
        <div class="props-section">
          <div class="section-title"><span>{{ t('flow.intentNode.modelSection') }}</span></div>
          <div class="prop-group">
            <label class="prop-label">{{ t('flow.modelConfig.label') }}</label>
            <el-select v-model="gatewayModel" class="prop-select" @change="saveIntent">
              <el-option :label="t('flow.modelConfig.default')" value="__default__" />
              <el-option
                v-for="m in selectableModels"
                :key="m.gateway_model"
                :label="m.gateway_model"
                :value="m.gateway_model"
              />
            </el-select>
          </div>
        </div>

        <!-- 大模型参数（与 LLM 节点一致） -->
        <div class="props-section">
          <div class="section-title"><span>{{ t('flow.modelConfig.paramsSection') }}</span></div>
          <div class="prop-group">
            <div class="gen-param-row">
              <label class="prop-label">max_tokens</label>
              <el-switch v-model="genParams.maxTokens.enabled" size="small" @change="saveIntent" />
              <el-input-number v-model="genParams.maxTokens.value" :disabled="!genParams.maxTokens.enabled" :min="1" :max="200000" size="small" controls-position="right" @change="saveIntent" />
            </div>
            <div class="gen-param-row">
              <label class="prop-label">temperature</label>
              <el-switch v-model="genParams.temperature.enabled" size="small" @change="saveIntent" />
              <el-input-number v-model="genParams.temperature.value" :disabled="!genParams.temperature.enabled" :min="0" :max="2" :step="0.1" :precision="2" size="small" controls-position="right" @change="saveIntent" />
            </div>
            <div class="gen-param-row">
              <label class="prop-label">top_p</label>
              <el-switch v-model="genParams.topP.enabled" size="small" @change="saveIntent" />
              <el-input-number v-model="genParams.topP.value" :disabled="!genParams.topP.enabled" :min="0" :max="1" :step="0.1" :precision="2" size="small" controls-position="right" @change="saveIntent" />
            </div>
            <div class="gen-param-row gen-param-extra">
              <label class="prop-label">extra_params</label>
              <el-switch v-model="extraParamsEnabled" size="small" @change="onExtraParamsEnabledChange" />
            </div>
            <textarea
              v-if="extraParamsEnabled"
              class="prop-textarea gen-extra-textarea"
              v-model="extraParamsText"
              @blur="saveIntent"
              :placeholder="t('flow.modelConfig.paramsPlaceholder')"
            ></textarea>
          </div>
        </div>

        <!-- 输出变量（只读展示） -->
        <div class="props-section">
          <div class="section-title"><span>{{ t('flow.outputVars.title') }}</span></div>
          <div class="field-row">
            <label class="field-label">intent</label>
            <span class="field-type-tag">string</span>
            <span class="field-hint-inline">{{ t('flow.intentNode.hintLabel') }}</span>
          </div>
          <div class="field-row">
            <label class="field-label">index</label>
            <span class="field-type-tag">number</span>
            <span class="field-hint-inline">{{ t('flow.intentNode.hintBranch') }}</span>
          </div>
        </div>

        <!-- 输入变量配置 -->
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

            <div
              v-for="(variable, name) in inputVariables"
              :key="name"
              class="input-variable-row"
            >
              <input
                type="text"
                :value="name"
                class="input-variable-name-input"
                @blur="handleUpdateInputVariableName(name, $event.target.value)"
                :placeholder="t('flow.common.varName')"
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
                    {{ t('flow.common.selectVar') }}
                  </template>
                </button>
                <button class="input-delete-btn" @click="handleDeleteInputVariable(name)">
                  <el-icon><Delete /></el-icon>
                </button>
              </div>
            </div>
          </div>
        </div>

        <!-- 系统提示词 -->
        <div class="props-section">
          <div class="section-title"><span>{{ t('flow.intentNode.systemPrompt') }}</span></div>
          <div class="prop-group">
            <textarea
              class="prop-textarea system-prompt-textarea"
              v-model="systemPrompt"
              @blur="saveIntent"
              :placeholder="t('flow.intentNode.systemPromptPlaceholder')"
            ></textarea>
          </div>
        </div>

        <!-- 意图列表 -->
        <div class="props-section">
          <div class="section-title">
            <span>{{ t('flow.intentNode.listTitle') }}</span>
            <button class="add-variable-icon-btn" @click="handleAddBranch">
              <el-icon><Plus /></el-icon>
            </button>
          </div>

          <div class="branches-list">
            <div v-if="branches.length === 0" class="branches-empty-state">
              <p>{{ t('flow.intentNode.noIntents') }}</p>
            </div>

            <div
              v-for="(branch, index) in branches"
              :key="branch.id"
              class="branch-card"
            >
              <div class="branch-header">
                <input
                  type="text"
                  class="branch-label-input"
                  v-model="branch.label"
                  @blur="saveIntent"
                  :placeholder="t('flow.intentNode.namePlaceholder')"
                />
                <button class="branch-delete-btn" @click="handleDeleteBranch(index)">
                  <el-icon><Delete /></el-icon>
                </button>
              </div>

              <div class="branch-content">
                <div class="prop-group">
                  <textarea
                    class="prop-textarea"
                    v-model="branch.description"
                    @blur="saveIntent"
                    :placeholder="t('flow.intentNode.descPlaceholder')"
                  ></textarea>
                </div>
              </div>
            </div>
          </div>
        </div>

        <!-- 默认分支说明 -->
        <div class="props-section">
          <div class="section-title">
            <span>{{ t('flow.node.branch.defaultBranch') }}</span>
          </div>
          <div class="default-branch-info">
            <p>{{ t('flow.intentNode.defaultHint1') }}</p>
            <p class="default-branch-note">{{ t('flow.intentNode.defaultHint2') }}</p>
          </div>
        </div>

        <!-- 异常处理：只对意图节点自身错误生效（LLM 调用失败等），子链错误向上透传不触发 -->
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
import OnErrorConfig from './OnErrorConfig.vue'
import { ElSelect, ElOption, ElInputNumber, ElSwitch, ElMessage } from 'element-plus'
import { Plus, Delete, ChromeFilled, Notification, User, Timer, Help, Link, MagicStick, Lightning, Promotion, RefreshRight, CircleClose, ChatLineSquare, Collection, Operation, Coin, Minus, Upload, CircleCheck, ScaleToOriginal, Monitor, Mouse, RefreshLeft, Tools, Clock, Aim } from '@element-plus/icons-vue'
import { getToken } from '@/tenant/utils/auth'
import adminApi from '@/tenant/config/admin'
import { resolveSourceIcon } from './nodeSourceIcons.js'
import { getDataTypeIcon, getDataTypeIconClass } from './dataTypeBadge.js'
import { useInputVariables } from './inputVariables.js'
import { useI18n } from 'vue-i18n'

const { t } = useI18n()

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
const FlowEdges = inject('FlowEdges', ref([]))

const activeTab = ref('settings')

const gatewayModel = ref('')
const systemPrompt = ref('')

// 大模型参数（与 LLM 节点一致：开关启用，config.llm.params 含键=启用）
const genParams = ref({
  maxTokens: { enabled: false, value: 4096 },
  temperature: { enabled: false, value: 0.7 },
  topP: { enabled: false, value: 1 }
})
const extraParamsEnabled = ref(false)
const extraParamsText = ref('')

const inputVariables = ref({})
const branches = ref([])

// 可选模型下拉
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
    console.error(t('flow.message.loadModelsFailed'), e)
  }
}

watch(() => props.selectedNode, (node) => {
  if (node && node.data) {
    const llm = node.data.config?.llm || {}
    gatewayModel.value = llm.gateway_model ? llm.gateway_model : '__default__'
    fillGenParamsFromNode(llm.params)
    systemPrompt.value = llm.system_prompt || ''

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

    branches.value = (node.data.config?.children || []).map(b => ({
      id: b.id,
      label: b.label || '',
      description: b.description || ''
    }))
  }
}, { immediate: true })

onMounted(() => {
  loadSelectableModels()
})

// ── 图标辅助 ──


// 变量来源图标：实现在 nodeSourceIcons.js（12 个面板共用，避免 source 类型再漂移）
const getSourceIcon = (sourceType, sourceRef) =>
  resolveSourceIcon(sourceType, sourceRef, FlowNodes.value)

// 从 config.llm.params 读取参数开关与值（与 LLM 节点一致）
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

// ── 保存 ──
const buildIntentConfig = () => {
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
      ElMessage.error(t('flow.message.extraParamsInvalid'))
      return null
    }
  }

  const llm = {
    gateway_model: gatewayModel.value === '__default__' ? '' : gatewayModel.value,
    system_prompt: systemPrompt.value,
    params
  }

  return {
    inputs: snapshotInputs(),
    llm,
    children: branches.value.map(b => ({ id: b.id, label: b.label, description: b.description }))
  }
}

const saveIntent = () => {
  if (!props.selectedNode) return
  const config = buildIntentConfig()
  if (!config) return
  emit('update-node-data', {
    nodeId: props.selectedNode.id,
    data: { config }
  })
}

const onExtraParamsEnabledChange = () => {
  if (extraParamsEnabled.value && extraParamsText.value === '') {
    extraParamsText.value = '{}'
  }
  saveIntent()
}

// ── 输入变量：增删改 / 校验 / 快照统一在 inputVariables.js ──
const {
  snapshot: snapshotInputs,
  addInputVariable,
  updateInputVariableName,
  deleteInputVariable,
  applySelectedVariable
} = useInputVariables({
  inputVariables,
  onChange: () => saveIntent()
})

const handleShowVariableSelect = (name, event) => {
  emit('show-variable-select', event, name, (value) => applySelectedVariable(name, value))
}

const handleAddInputVariable = addInputVariable
const handleUpdateInputVariableName = updateInputVariableName
const handleDeleteInputVariable = deleteInputVariable

// ── 意图分支 ──
const generateBranchId = () => {
  return Math.random().toString(36).slice(2, 10)
}

const handleAddBranch = () => {
  branches.value.push({
    id: generateBranchId(),
    label: t('flow.node.intent.childName', { n: branches.value.length + 1 }),
    description: ''
  })
  saveIntent()
}

const handleDeleteBranch = (index) => {
  const branch = branches.value[index]
  if (!branch) return

  const branchId = branch.id

  // 删除与该分支关联的出边（与分支节点一致：source{branchId}left / right）
  const leftHandle = 'source' + branchId + 'left'
  const rightHandle = 'source' + branchId + 'right'

  FlowEdges.value = FlowEdges.value.filter(edge => {
    return edge.sourceHandle !== leftHandle && edge.sourceHandle !== rightHandle
  })

  branches.value.splice(index, 1)
  saveIntent()
}
</script>

<style scoped>
.intent-node-props {
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

.prop-group:last-child {
  margin-bottom: 0;
}

.prop-label {
  font-size: 12px;
  color: #666;
  font-weight: 500;
}

.prop-select {
  width: 100%;
}

.prop-select :deep(.el-select__wrapper) {
  background-color: #eee;
  border: none;
  box-shadow: none;
}

.prop-textarea {
  width: 100%;
  min-height: 80px;
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
}

.prop-textarea:focus {
  border-color: #91caff;
}

.prop-textarea::placeholder {
  color: #bbb;
  font-size: 11px;
}

.field-row {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 4px 0;
}

.field-label {
  font-size: 12px;
  color: #666;
  flex-shrink: 0;
  min-width: 60px;
}

.field-type-tag {
  font-size: 11px;
  color: #909399;
  background: #f0f0f0;
  border-radius: 3px;
  padding: 1px 6px;
}

.field-hint-inline {
  font-size: 11px;
  color: #999;
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
  border-radius: 6px;
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

.input-variable-btn {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 8px 10px;
  max-width: 100%;
  overflow: hidden;
  border: 0px solid #1890ff;
  border-radius: 4px;
  background: #eee;
  color: #1890ff;
  font-size: 12px;
  cursor: pointer;
  transition: all 0.2s;
  flex-shrink: 0;
}

.input-variable-btn:hover:not(:disabled) {
  background: #e6f7ff;
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
}

.input-delete-btn:hover {
  background: #fff5f5;
  color: #f56c6c;
}

.branches-list {
  display: flex;
  flex-direction: column;
  gap: 12px;
  margin-top: 8px;
}

.branches-empty-state {
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 20px;
  text-align: center;
}

.branches-empty-state p {
  color: #999;
  margin: 0;
  font-size: 12px;
}

.branch-card {
  border: 1px solid #e8e8e8;
  border-radius: 6px;
  background: #ffffff;
  overflow: hidden;
}

.branch-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 4px 12px;
  background: #f5f5f5;
  border-bottom: 1px solid #e8e8e8;
}

.branch-label-input {
  flex: 1;
  padding: 4px 8px;
  border: 1px solid transparent;
  border-radius: 4px;
  font-size: 12px;
  color: #333;
  background: transparent;
  outline: none;
  transition: all 0.2s;
}

.branch-label-input:hover {
  border-color: #d9d9d9;
}

.branch-label-input:focus {
  border-color: #91caff;
  background: #fff;
}

.branch-label-input::placeholder {
  color: #bbb;
}

.branch-delete-btn {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 24px;
  height: 24px;
  padding: 0;
  border: none;
  border-radius: 3px;
  font-size: 12px;
  color: #999;
  background: transparent;
  cursor: pointer;
}

.branch-delete-btn:hover {
  background: #fff5f5;
  color: #f56c6c;
}

.branch-content {
  padding: 12px;
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.default-branch-info {
  padding: 8px 0;
}

.default-branch-info p {
  margin: 4px 0;
  font-size: 12px;
  color: #666;
  line-height: 1.6;
}

.default-branch-info .default-branch-note {
  color: #e6a23c;
  font-weight: 500;
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

.input-variable-btn .input-type-badge.type-string { background: #67c23a; }
.input-variable-btn .input-type-badge.type-number { background: #409eff; }
.input-variable-btn .input-type-badge.type-boolean { background: #9c27b0; }
.input-variable-btn .input-type-badge.type-object { background: #909399; }
.input-variable-btn .input-type-badge.type-array-string { background: #f56c6c; border-radius: 4px; }
.input-variable-btn .input-type-badge.type-array-number { background: #e6a23c; border-radius: 4px; }
.input-variable-btn .input-type-badge.type-array-boolean { background: #9c27b0; border-radius: 4px; }
.input-variable-btn .input-type-badge.type-array-object { background: #909399; border-radius: 4px; }

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

.input-variable-btn .icon-default { background-color: #6b7280; }
.input-variable-btn .icon-start { background-color: #00704A; }
.input-variable-btn .icon-branch { background-color: #00bcd4; }
.input-variable-btn .icon-wait { background-color: #00bcd4; }
.input-variable-btn .icon-code { background-color: #a0522d; }
.input-variable-btn .icon-http { background-color: #409eff; }
.input-variable-btn .icon-process { background-color: #00bcd4; }
.input-variable-btn .icon-result { background-color: #67c23a; }
.input-variable-btn .icon-progress { background-color: #eea236; }
.input-variable-btn .icon-exit { background-color: #f56565; }
.input-variable-btn .icon-terminate { background-color: #f56565; }
.input-variable-btn .icon-llm { background-color: #409eff; }
.input-variable-btn .icon-knowledge { background-color: #67c23a; }
.input-variable-btn .icon-agent { background-color: #f56c6c; }
.input-variable-btn .icon-subflow { background-color: #667eea; }
.input-variable-btn .icon-assign { background-color: #ff9800; }
.input-variable-btn .icon-break { background-color: #ff9800; }
.input-variable-btn .icon-continue { background-color: #4caf50; }
.input-variable-btn .icon-return { background-color: #9c27b0; }
.input-variable-btn .icon-function { background-color: #667eea; }
.input-variable-btn .icon-fork { background-color: #4bc0c0; }
.input-variable-btn .icon-call { background-color: #ffc107; }
.input-variable-btn .icon-loop { background-color: #7b1fa2; }
.input-variable-btn .icon-mcp { background-color: #e6a23c; }
.input-variable-btn .icon-memory { background-color: #673ab7; }
.input-variable-btn .icon-intent { background-color: #9c27b0; }

.input-variable-btn .icon-source-flow,
.input-variable-btn .icon-source-env,
.input-variable-btn .icon-source-argument {
  font-size: 14px;
  color: #1890ff;
  flex-shrink: 0;
}

.gen-param-row { display: flex; align-items: center; gap: 10px; margin-bottom: 10px; }
.gen-param-row .prop-label { min-width: 90px; }
.gen-param-row .el-input-number { flex: 1; max-width: 180px; }
.gen-param-row.gen-param-extra { margin-bottom: 4px; }
.gen-extra-textarea { min-height: 70px; background-color: #eee; }
.gen-extra-textarea:focus { border-color: #91caff; }

.system-prompt-textarea { min-height: 180px; }
</style>
