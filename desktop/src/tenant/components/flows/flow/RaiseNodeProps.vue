<template>
  <div class="raise-node-props">
    <div class="tabs">
      <button class="tab-btn" :class="{ active: activeTab === 'settings' }" @click="activeTab = 'settings'">
        {{ t('flow.nodeProps.tabConfig') }}
      </button>
      <button class="tab-btn" :class="{ active: activeTab === 'lastRun' }" @click="activeTab = 'lastRun'">
        {{ t('flow.nodeProps.tabResult') }}
      </button>
    </div>

    <div class="tab-content">
      <div v-if="activeTab === 'settings'" class="settings-panel">
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

        <div class="props-section">
          <div class="section-title">
            <span>{{ t('flow.raiseNode.code') }}</span>
          </div>
          <el-select :model-value="codeValue" class="prop-select" @change="handleCodeChange">
            <el-option v-for="c in raisableCodes" :key="c" :label="c" :value="c" />
          </el-select>
          <div class="field-hint">{{ t('flow.raiseNode.codeHint') }}</div>
        </div>

        <div class="props-section">
          <div class="section-title">
            <span>{{ t('flow.raiseNode.message') }}</span>
          </div>
          <div class="prop-group">
            <textarea
              v-model="messageDraft"
              class="prop-textarea raise-message-textarea"
              :placeholder="t('flow.raiseNode.messagePlaceholder')"
              @blur="handleMessageChange"
            ></textarea>
          </div>
          <div class="field-hint">{{ t('flow.raiseNode.messageHint') }}</div>
        </div>
      </div>

      <div v-if="activeTab === 'lastRun'" class="last-run-panel">
        <NodeLastRun :selected-node="selectedNode" />
      </div>
    </div>
  </div>
</template>

<script setup>
import { inject, onMounted, ref, watch } from 'vue'
import { ElOption, ElSelect } from 'element-plus'
import { Delete, Plus } from '@element-plus/icons-vue'
import { useI18n } from 'vue-i18n'
import NodeLastRun from './NodeLastRun.vue'
import { resolveSourceIcon } from './nodeSourceIcons.js'
import { getDataTypeIcon, getDataTypeIconClass } from './dataTypeBadge.js'
import { useInputVariables } from './inputVariables.js'
import { apiBase } from '@/tenant/config/base.js'
import { getAuthHeader } from '@/tenant/utils/auth.js'

const { t } = useI18n()

const props = defineProps({
  selectedNode: {
    type: Object,
    default: null
  }
})

const emit = defineEmits(['update-node-data', 'show-variable-select'])

const FlowNodes = inject('FlowNodes', ref([]))

const activeTab = ref('settings')
const raisableCodes = ref([])
const codeValue = ref('other')
const messageDraft = ref('')
const inputVariables = ref({})

// The picker takes the list the server enforces (raisable_codes) instead of keeping a copy of the code table on the
// client: a code the runner refuses must not be offerable here.
onMounted(async () => {
  try {
    const resp = await fetch(`${apiBase}/admin/flow/node-capabilities`, {
      headers: getAuthHeader()
    })
    if (resp.ok) {
      const res = await resp.json()
      const data = res && res.data ? res.data : {}
      raisableCodes.value = data.raisable_codes || []
    }
  } catch (e) {
    console.error('Failed to fetch raisable codes:', e)
  }
})

// inputs: add / rename / delete / snapshot all live in inputVariables.js, the single implementation every panel uses.
const {
  snapshot: buildInputsToSave,
  addInputVariable,
  updateInputVariableName,
  deleteInputVariable,
  applySelectedVariable
} = useInputVariables({
  inputVariables,
  onChange: () => saveConfig()
})

const handleInputVariableFocus = (name, value) => {}
const handleAddInputVariable = addInputVariable
const handleUpdateInputVariableName = updateInputVariableName
const handleDeleteInputVariable = deleteInputVariable

const handleShowVariableSelect = (name, event) => {
  emit('show-variable-select', event, name, (value) => applySelectedVariable(name, value))
}

// Variable source icon: implemented in nodeSourceIcons.js and shared by every panel, so source types do not drift.
const getSourceIcon = (sourceType, sourceRef) =>
  resolveSourceIcon(sourceType, sourceRef, FlowNodes.value)

// The single assembly point: config is exactly { inputs, raise: { code, message } }, the shape the executor reads.
const saveConfig = () => {
  if (!props.selectedNode) return
  emit('update-node-data', {
    nodeId: props.selectedNode.id,
    data: {
      config: {
        inputs: buildInputsToSave(),
        raise: {
          code: codeValue.value,
          message: messageDraft.value
        }
      }
    }
  })
}

const handleCodeChange = (value) => {
  codeValue.value = value
  saveConfig()
}

const handleMessageChange = () => saveConfig()

watch(() => props.selectedNode, (node) => {
  const cfg = node?.data?.config || {}

  const inputs = cfg.inputs || {}
  inputVariables.value = {}
  for (const name in inputs) {
    if (Object.prototype.hasOwnProperty.call(inputs, name)) {
      inputVariables.value[name] = { ...inputs[name], name }
    }
  }

  codeValue.value = cfg.raise?.code || 'other'
  messageDraft.value = cfg.raise?.message || ''
}, { immediate: true })
</script>

<style scoped>
.raise-node-props {
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
  /* Transparent placeholder scrollbar; it appears only when the pointer enters the panel, so its arrival does not
     shift the content. */
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

.input-variable-name-input:focus {
  outline: none;
}

.input-variable-name-input::placeholder {
  color: #bbb;
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

/* Node-source colours, identical to the other eleven panels. */
.input-variable-btn .icon-start { background-color: #00704A; }
.input-variable-btn .icon-branch { background-color: #00bcd4; }
.input-variable-btn .icon-wait { background-color: #00bcd4; }
.input-variable-btn .icon-code { background-color: #a0522d; }
.input-variable-btn .icon-http { background-color: #409eff; }
.input-variable-btn .icon-process { background-color: #00bcd4; }
.input-variable-btn .icon-result { background-color: #67c23a; }
.input-variable-btn .icon-progress { background-color: #eea236; }
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
.input-variable-btn .icon-default { background-color: #6b7280; }

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

.prop-group {
  display: flex;
  flex-direction: column;
  gap: 6px;
  margin-bottom: 12px;
}

.prop-group:last-child {
  margin-bottom: 0;
}

.prop-textarea {
  width: 100%;
  padding: 10px;
  border: 1px solid #d9d9d9;
  border-radius: 4px;
  font-size: 12px;
  line-height: 1.7;
  resize: vertical;
  outline: none;
  box-sizing: border-box;
  background-color: #eee;
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

/* The message is prose, not code: monospace at the code editor's height would be wrong for it. */
.raise-message-textarea {
  min-height: 90px;
  font-size: 13px;
  font-family: inherit;
  line-height: 1.6;
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

.field-hint {
  font-size: 12px;
  color: #999;
  line-height: 1.5;
  padding: 8px 4px 2px;
}

.last-run-panel {
  padding: 0;
}
</style>
