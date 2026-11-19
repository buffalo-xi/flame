<template>
  <div class="branch-node-props">
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
                @focus="handleInputVariableFocus(name, name)"
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

        <!-- 分支列表 -->
        <div class="props-section">
          <div class="section-title">
            <span>{{ t('flow.branchNode.listTitle') }}</span>
            <button class="add-variable-icon-btn" @click="handleAddBranch">
              <el-icon><Plus /></el-icon>
            </button>
          </div>

          <div class="branches-list">
            <div v-if="branches.length === 0" class="branches-empty-state">
              <p>{{ t('flow.branchNode.noBranches') }}</p>
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
                  @blur="saveBranches"
                  :placeholder="t('flow.branchNode.namePlaceholder')"
                />
                <button class="branch-delete-btn" @click="handleDeleteBranch(index)">
                  <el-icon><Delete /></el-icon>
                </button>
              </div>

              <div class="branch-content">
                <div class="prop-group">
                  <textarea
                    class="prop-textarea"
                    v-model="branch.condition"
                    @blur="saveBranches"
                    :placeholder="t('flow.branchNode.conditionPlaceholder')"
                  ></textarea>
                </div>
                <div class="prop-group-horizontal">
                  <div class="prop-item">
                    <label class="prop-label">{{ t('flow.branchNode.exclusive') }}</label>
                    <el-switch v-model="branch.exclusive" @change="saveBranches" />
                  </div>
                  <div class="prop-item">
                    <span class="exclusive-hint">{{ t('flow.branchNode.exclusiveHint') }}</span>
                  </div>
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
            <p>{{ t('flow.branchNode.defaultHint1') }}</p>
            <p class="default-branch-note">{{ t('flow.branchNode.defaultHint2') }}</p>
          </div>
        </div>

        <!-- 脚本配置 -->
        <div class="props-section">
          <div class="section-title">
            <span>{{ t('flow.branchNode.scriptSection') }}</span>
          </div>
          
          <div class="prop-group-horizontal">
            <div class="prop-item">
              <label class="prop-label">{{ t('flow.common.scriptLanguage') }}</label>
              <el-select v-model="conditionLanguage" class="prop-select" @change="handleLanguageChange">
                <el-option v-for="lang in scriptLanguages" :key="lang" :label="lang" :value="lang" />
              </el-select>
            </div>
            <div class="prop-item">
              <label class="prop-label">{{ t('flow.common.timeout') }}</label>
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
              <span v-if="timeoutEnabled" class="unit-text">{{ t('flow.common.seconds') }}</span>
            </div>
          </div>
        </div>
      </div>

      <div v-if="activeTab === 'lastRun'">
        <NodeLastRun :selected-node="selectedNode" />
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref, watch, computed, inject } from 'vue'
import NodeLastRun from './NodeLastRun.vue'
import { ElSelect, ElOption, ElInput, ElSwitch } from 'element-plus'
import { Plus, Delete, ChromeFilled, Notification, User, Timer, Help, Link, MagicStick, Lightning, Promotion, RefreshRight, CircleClose, ChatLineSquare, Collection, Operation, Coin, Minus, Upload, CircleCheck, ScaleToOriginal, Monitor, Mouse, RefreshLeft } from '@element-plus/icons-vue'
import random from '@/tenant/utils/random'
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
const scriptLanguages = inject('scriptLanguages', ref(['lua']))
const defaultLang = computed(() => scriptLanguages.value[0] || 'lua')

const activeTab = ref('settings')


const conditionLanguage = ref('lua')
const timeout = ref(30)
const timeoutEnabled = ref(false)

const inputVariables = ref({})
const branches = ref([])



watch(() => props.selectedNode, (node) => {
  if (node && node.data) {
    conditionLanguage.value = node.data.config?.branch?.lang || defaultLang.value
    timeout.value = node.data.config?.branch?.timeout?.seconds || 30
    timeoutEnabled.value = node.data.config?.branch?.timeout?.enabled ?? false
    
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

    // 后端缺省按 true（排他）处理，这里保持一致：只有显式 false 才显示为非排他
    branches.value = (node.data.config?.children || []).map(b => ({ ...b, exclusive: b.exclusive !== false }))
  }
}, { immediate: true })



// 变量来源图标：实现在 nodeSourceIcons.js（12 个面板共用，避免 source 类型再漂移）
const getSourceIcon = (sourceType, sourceRef) =>
  resolveSourceIcon(sourceType, sourceRef, FlowNodes.value)


// ── 输入变量：增删改 / 校验 / 快照统一在 inputVariables.js ──
const {
  snapshot: snapshotInputs,
  addInputVariable,
  updateInputVariableName,
  deleteInputVariable,
  applySelectedVariable
} = useInputVariables({
  inputVariables,
  onChange: () => saveConfig()
})

const handleShowVariableSelect = (name, event) => {
  emit('show-variable-select', event, name, (value) => applySelectedVariable(name, value))
}

const handleInputVariableFocus = (name, value) => {}
const handleAddInputVariable = addInputVariable
const handleUpdateInputVariableName = updateInputVariableName
const handleDeleteInputVariable = deleteInputVariable

// 节点配置的唯一组装点：inputs + 脚本块（lang/timeout）+ 分支列表。
// saveInputVariables / saveBranches 及各处 handler 共用，避免某处漏字段。
const buildConfig = () => {
  const inputs = snapshotInputs()

  const seconds = Math.max(1, Math.floor(timeout.value))
  if (seconds !== timeout.value) timeout.value = seconds

  return {
    inputs,
    branch: {
      lang: conditionLanguage.value,
      timeout: {
        enabled: timeoutEnabled.value,
        seconds
      }
    },
    children: branches.value
  }
}

const saveConfig = () => {
  emit('update-node-data', {
    nodeId: props.selectedNode.id,
    data: { config: buildConfig() }
  })
}

const saveInputVariables = () => saveConfig()

const generateBranchId = () => {
  return random.generateUniqueId(5, FlowNodes.value, FlowEdges.value)
}

const handleAddBranch = () => {
  branches.value.push({
    id: generateBranchId(),
    label: t('flow.node.branch.childName', { n: branches.value.length + 1 }),
    condition: '',
    exclusive: true
  })
  saveBranches()
}

const handleDeleteBranch = (index) => {
  const branch = branches.value[index]
  if (!branch) return
  
  const branchId = branch.id
  
  // 删除与该分支关联的出边（参考 Flow.vue 中的 onBranchDeleteChild 实现）
  const leftHandle = 'source' + branchId + 'left'
  const rightHandle = 'source' + branchId + 'right'

  FlowEdges.value = FlowEdges.value.filter(edge => {
    return edge.sourceHandle !== leftHandle && edge.sourceHandle !== rightHandle
  })
  
  branches.value.splice(index, 1)
  saveBranches()
}

const saveBranches = () => saveConfig()

const handleLanguageChange = () => {
  saveBranches()
}

const handleTimeoutChange = () => {
  let validTimeout = Math.floor(timeout.value)
  if (validTimeout < 1) {
    validTimeout = 1
    timeout.value = validTimeout
  }
  saveBranches()
}

const handleTimeoutEnabledChange = () => {
  let validTimeout = Math.floor(timeout.value)
  if (validTimeout < 1) {
    validTimeout = 1
    timeout.value = validTimeout
  }
  saveBranches()
}
</script>

<style scoped>
.branch-node-props {
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

.exclusive-hint {
  font-size: 12px;
  color: #e6a23c;
  white-space: normal;
}

.prop-select {
  width: 100%;
}

.prop-select :deep(.el-select__wrapper) {
  background-color: #eee;
  border: none;
  box-shadow: none;
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

.unit-text {
  font-size: 12px;
  color: #666;
  flex-shrink: 0;
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
  transition: all 0.2s;
  flex-shrink: 0;
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
  font-weight: 200;
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
  font-weight: normal;
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
  transition: all 0.2s;
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
.input-variable-btn .icon-source-argument {
  font-size: 14px;
  color: #1890ff;
  flex-shrink: 0;
}

/* 变量来源里的节点图标配色：class 由 nodeIcons.js 给出，这里负责上色 */
.input-variable-btn .icon-default { background-color: #6b7280; }
.input-variable-btn .icon-start { background-color: #00704A; }
.input-variable-btn .icon-branch { background-color: #00bcd4; }
.input-variable-btn .icon-wait { background-color: #00bcd4; }
.input-variable-btn .icon-code { background-color: #f56565; }
.input-variable-btn .icon-http { background-color: #409eff; }
.input-variable-btn .icon-process { background-color: #00bcd4; }
.input-variable-btn .icon-result { background-color: #67c23a; }
.input-variable-btn .icon-progress { background-color: #eea236; }
.input-variable-btn .icon-llm { background-color: #409eff; }
.input-variable-btn .icon-knowledge { background-color: #67c23a; }
.input-variable-btn .icon-assign { background-color: #ff9800; }
.input-variable-btn .icon-break { background-color: #ff9800; }
.input-variable-btn .icon-continue { background-color: #4caf50; }
.input-variable-btn .icon-return { background-color: #9c27b0; }
.input-variable-btn .icon-function { background-color: #667eea; }
.input-variable-btn .icon-fork { background-color: #4bc0c0; }
.input-variable-btn .icon-call { background-color: #ffc107; }
.input-variable-btn .icon-loop { background-color: #7b1fa2; }
</style>
