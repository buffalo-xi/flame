<template>
  <div class="call-node-props">
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
        <!-- 目标函数 -->
        <div class="props-section">
          <div class="section-title"><span>{{ t('flow.call.targetSection') }}</span></div>
          <div class="target-select-row">
            <el-select
              :model-value="targetId"
              @change="handleTargetChange"
              class="prop-select"
              :placeholder="t('flow.call.selectPlaceholder')"
              filterable
            >
              <el-option
                v-for="fn in functionNodes"
                :key="fn.id"
                :label="fn.data?.label ? `${fn.data.label} (${fn.id})` : fn.id"
                :value="fn.id"
              />
            </el-select>
            <button
              v-if="targetId"
              class="clear-btn"
              :title="t('flow.common.clearSelection')"
              @click="clearTargetSelection"
            >
              ✕
            </button>
          </div>
        </div>

        <!-- 参数绑定 -->
        <div class="props-section">
          <div class="section-title"><span>{{ t('flow.call.paramSection') }}</span></div>
          <div class="input-variables-list">
            <div v-if="!targetId" class="input-empty-state">
              <p>{{ t('flow.call.selectTargetFirst') }}</p>
            </div>
            <div v-else-if="Object.keys(targetParams).length === 0" class="input-empty-state">
              <p>{{ t('flow.call.noParams') }}</p>
            </div>

            <div
              v-for="(param, name) in targetParams"
              :key="name"
              class="input-variable-row"
            >
              <div class="param-name-box" :title="param.description || ''">
                <span class="input-type-badge" :class="getDataTypeIconClass(param.type)">{{ getDataTypeIcon(param.type) }}</span>
                <span class="param-name-text">{{ name }}</span>
                <span v-if="param.required" class="required-dot" :title="t('flow.common.required')">*</span>
              </div>
              <div class="input-variable-content">
                <button
                  class="show-content-btn input-variable-btn"
                  @click="handleShowVariableSelect(name, $event)"
                >
                  <template v-if="argsVariables[name] && argsVariables[name].source_key">
                    <span class="input-type-badge" :class="getDataTypeIconClass(argsVariables[name].type)">{{ getDataTypeIcon(argsVariables[name].type) }}</span>
                    <span class="input-var-key">{{ argsVariables[name].source_key }}</span>
                    <template v-if="getSourceIcon(argsVariables[name].source_type, argsVariables[name].source_ref)">
                      <template v-if="argsVariables[name].source_type === 'node'">
                        <span class="input-node-icon" :class="getSourceIcon(argsVariables[name].source_type, argsVariables[name].source_ref).class">
                          <el-icon><component :is="getSourceIcon(argsVariables[name].source_type, argsVariables[name].source_ref).component" /></el-icon>
                        </span>
                        <span class="input-node-label">{{ getSourceIcon(argsVariables[name].source_type, argsVariables[name].source_ref).nodeLabel }}</span>
                      </template>
                      <template v-else>
                        <el-icon :class="getSourceIcon(argsVariables[name].source_type, argsVariables[name].source_ref).class">
                          <component :is="getSourceIcon(argsVariables[name].source_type, argsVariables[name].source_ref).component" />
                        </el-icon>
                      </template>
                    </template>
                  </template>
                  <template v-else>
                    {{ t('flow.common.selectVar') }}
                  </template>
                </button>
                <button
                  v-if="argsVariables[name] && argsVariables[name].source_key"
                  class="input-delete-btn"
                  :title="t('flow.common.clearBinding')"
                  @click="handleClearArg(name)"
                >
                  <el-icon><Delete /></el-icon>
                </button>
              </div>
            </div>
          </div>
        </div>

        <!-- 输出变量（只读，来自目标函数定义） -->
        <OutputVarsConfig
          :selected-node="selectedNode"
          :readonly="true"
          @update-node-data="(data) => emit('update-node-data', data)"
        />

        <!-- 错误处理 -->
        <OnErrorConfig
          :selectedNode="selectedNode"
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
import { ref, watch, computed, inject } from 'vue'
import NodeLastRun from './NodeLastRun.vue'
import { ElSelect, ElOption } from 'element-plus'
import { Delete, ChromeFilled, Notification, User, Help, Timer, Link, MagicStick, Lightning, Promotion, RefreshRight, CircleClose, ChatLineSquare, Collection, Operation, Coin, Minus, Upload, CircleCheck, ScaleToOriginal, Monitor, Mouse, RefreshLeft } from '@element-plus/icons-vue'
import OnErrorConfig from './OnErrorConfig.vue'
import OutputVarsConfig from './OutputVarsConfig.vue'
import { resolveSourceIcon } from './nodeSourceIcons.js'
import { getDataTypeIcon, getDataTypeIconClass } from './dataTypeBadge.js'
import { useInputVariables } from './inputVariables.js'
import { useI18n } from 'vue-i18n'

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
const targetId = ref('')
const argsVariables = ref({})


// 流程内所有 function 节点
const functionNodes = computed(() => {
  return (FlowNodes.value || []).filter(n => n.type === 'function')
})

// 目标 function 节点
const targetNode = computed(() => {
  if (!targetId.value) return null
  return (FlowNodes.value || []).find(n => String(n.id) === String(targetId.value)) || null
})

// 目标函数的参数定义（兼容 required 缺失默认为 true）
const targetParams = computed(() => {
  const params = targetNode.value?.data?.config?.params
  if (!params) return {}
  const result = {}
  Object.keys(params).forEach(key => {
    const v = params[key]
    result[key] = {
      ...v,
      required: v.required === undefined ? true : v.required
    }
  })
  return result
})



watch(() => props.selectedNode, (node) => {
  if (node && node.data) {
    targetId.value = node.data.config?.target || ''
    const args = node.data.config?.args || {}
    argsVariables.value = {}
    Object.keys(args).forEach(name => {
      argsVariables.value[name] = { ...args[name] }
    })

  }
}, { immediate: true })

// 保存配置（保留 config 其他字段）
const saveConfig = (patch) => {
  const newConfig = { ...(props.selectedNode.data?.config || {}) }
  Object.keys(patch).forEach(k => {
    newConfig[k] = patch[k]
  })
  emit('update-node-data', {
    nodeId: props.selectedNode.id,
    data: { config: newConfig }
  })
}

// args 的快照复用输入变量那套实现（字段一致，只是 ref 名字不同）
const { snapshot: buildArgsToSave } = useInputVariables({ inputVariables: argsVariables })

const saveArgs = () => {
  saveConfig({ args: buildArgsToSave() })
}

// 切换目标函数：清理不再匹配新参数的绑定，保留匹配的；若无自定义输出则从函数输出初始化
const handleTargetChange = (newId) => {
  targetId.value = newId
  // 根据新函数的 params 重建 args（仅保留仍存在的参数名绑定）
  const newParams = {}
  const funcNode = (FlowNodes.value || []).find(n => String(n.id) === String(newId))
  const params = funcNode?.data?.config?.params || {}
  Object.keys(params).forEach(key => {
    newParams[key] = params[key]
  })
  const cleanedArgs = {}
  Object.keys(newParams).forEach(name => {
    if (argsVariables.value[name]) {
      cleanedArgs[name] = argsVariables.value[name]
    } else {
      cleanedArgs[name] = {
        source_type: 'argument',
        source_ref: '',
        source_key: '',
        type: newParams[name].type || ''
      }
    }
  })
  argsVariables.value = cleanedArgs

  // 始终将目标函数的输出定义同步到 call 节点的 outputs
  const newOutputs = {}
  if (funcNode?.data?.config?.outputs) {
    Object.keys(funcNode.data.config.outputs).forEach(key => {
      newOutputs[key] = { ...funcNode.data.config.outputs[key] }
    })
  }

  saveConfig({ target: newId, args: buildArgsToSave(), outputs: newOutputs })
}

const clearTargetSelection = () => {
  handleTargetChange('')
}

const handleShowVariableSelect = (name, event) => {
  const callback = (value) => {
    try {
      const data = JSON.parse(value)
      argsVariables.value[name] = {
        ...(argsVariables.value[name] || {}),
        source_key: data.source_key,
        source_ref: data.source_ref,
        source_type: data.source_type,
        type: data.variableType || ''
      }
      saveArgs()
    } catch (e) {
      console.error('Failed to parse variable value:', e)
    }
  }
  emit('show-variable-select', event, name, callback)
}

const handleClearArg = (name) => {
  if (argsVariables.value[name]) {
    argsVariables.value[name] = {
      ...(argsVariables.value[name] || {}),
      source_key: '',
      source_ref: '',
      source_type: 'argument',
      type: ''
    }
    saveArgs()
  }
}

// ============ 辅助函数（与 CodeNodeProps 保持一致的变量显示） ============



// 变量来源图标：实现在 nodeSourceIcons.js（12 个面板共用，避免 source 类型再漂移）
const getSourceIcon = (sourceType, sourceRef) =>
  resolveSourceIcon(sourceType, sourceRef, FlowNodes.value)
</script>

<style scoped>
.call-node-props {
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

.prop-select {
  width: 100%;
}

.target-select-row {
  display: flex;
  align-items: center;
  gap: 8px;
}

.target-select-row .prop-select {
  flex: 1;
}

.clear-btn {
  width: 28px;
  height: 28px;
  flex-shrink: 0;
  border: 1px solid #ddd;
  border-radius: 4px;
  background: #fff;
  color: #999;
  font-size: 12px;
  cursor: pointer;
  display: flex;
  align-items: center;
  justify-content: center;
  transition: all 0.2s;
}

.clear-btn:hover {
  border-color: #f56565;
  color: #f56565;
  background: #fff5f5;
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

.param-name-box {
  flex: 1;
  min-width: 0;
  max-width: 40%;
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 10px 10px;
  background-color: #eeeeee;
  border-radius: 4px;
  font-size: 13px;
  color: #333;
}

.param-name-text {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-weight: 500;
}

.required-dot {
  color: #f56c6c;
  font-weight: bold;
  flex-shrink: 0;
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

.input-variable-btn:hover {
  background: #e6f7ff;
}

.input-variable-btn .input-var-key {
  font-size: 12px;
  color: #333;
  flex-shrink: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
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

.input-type-badge {
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

.input-type-badge.type-string { background: #67c23a; }
.input-type-badge.type-number { background: #409eff; }
.input-type-badge.type-boolean { background: #9c27b0; }
.input-type-badge.type-object { background: #909399; }
.input-type-badge.type-array-string { background: #f56c6c; border-radius: 4px; }
.input-type-badge.type-array-number { background: #e6a23c; border-radius: 4px; }
.input-type-badge.type-array-boolean { background: #9c27b0; border-radius: 4px; }
.input-type-badge.type-array-object { background: #909399; border-radius: 4px; }

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
</style>
