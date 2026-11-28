<template>
  <div class="props-section">
    <div class="section-title">
      <span>{{ t('flow.outputVars.title') }}</span>
      <button v-if="!readonly" class="add-variable-icon-btn" ref="addOutputBtnRef" @click="toggleOutputDropdown">
        <el-icon><Plus /></el-icon>
      </button>
    </div>

    <div v-if="bindingConflict" class="binding-conflict-hint">
      <code>content</code>{{ t('flow.outputVars.conflict.p1') }}<code>reasoning_content</code>{{ t('flow.outputVars.conflict.p2') }}
      {{ t('flow.outputVars.conflict.advice') }}
    </div>

    <div v-if="httpBodyBindingHint" class="binding-note">
      <code>body</code>{{ t('flow.outputVars.httpBody.p1') }}
      {{ t('flow.outputVars.httpBody.p2') }}
      {{ t('flow.outputVars.httpBody.p3') }}
    </div>

    <div v-if="builtinVariables.length > 0" class="binding-note">
      {{ t('flow.outputVars.builtinNote') }}
    </div>

    <div class="output-variables-cards">
      <div
        v-for="v in builtinVariables"
        :key="'builtin-' + v.name"
        class="output-variable-row fixed-variable-row"
      >
        <div class="output-variable-header">
          <span class="output-type-badge" :class="getOutputTypeClass(v.type)">
            {{ getOutputTypeIcon(v.type) }}
          </span>
          <span class="fixed-output-var-lock" :title="t('flow.outputVars.builtinVariable')"><el-icon><Lock /></el-icon></span>
          <span class="output-variable-name-readonly">{{ v.name }}</span>
        </div>
        <div class="output-variable-content">
          <span class="output-description-readonly">{{ v.description || getOutputTypeLabel(v.type) }}</span>
        </div>
      </div>

      <div v-if="builtinVariables.length === 0 && Object.keys(sortedOutputVariables).length === 0" class="output-empty-state">
        <p>{{ readonly ? (targetId ? t('flow.outputVars.emptyNoFunctionOutputs') : t('flow.outputVars.emptyNoTarget')) : t('flow.outputVars.emptyDefault') }}</p>
      </div>

      <div
        v-for="(config, key) in sortedOutputVariables"
        :key="key"
        class="output-variable-row"
        :class="{ 'fixed-variable-row': isFixedVariable(key) }"
      >
        <div class="output-variable-header">
          <span class="output-type-badge" :class="getOutputTypeClass(config.type)">
            {{ getOutputTypeIcon(config.type) }}
          </span>
          <button
            v-if="!readonly && !isFixedVariable(key)"
            class="output-delete-btn"
            @click="handleDeleteOutputVariable(key)"
          >
            <el-icon><Delete /></el-icon>
          </button>
          <span v-if="isFixedVariable(key)" class="fixed-output-var-lock" :title="t('flow.outputVars.fixedVariable')"><el-icon><Lock /></el-icon></span>
          <template v-if="readonly || isFixedVariable(key)">
            <span class="output-variable-name-readonly">{{ key }}</span>
          </template>
          <template v-else>
            <input
              type="text"
              :value="key"
              :data-variable-key="`output-${key}`"
              class="output-variable-name-input"
              @change="handleRenameOutputVariable(key, $event.target.value)"
              :placeholder="t('flow.common.varName')"
            />
          </template>
        </div>
        <div class="output-variable-content">
          <template v-if="readonly || isFixedVariable(key)">
            <span class="output-description-readonly">{{ config.description || getOutputTypeLabel(config.type) }}</span>
          </template>
          <template v-else>
            <input
              type="text"
              :value="config.description"
              class="output-description-input"
              :placeholder="t('flow.common.addDescription')"
              @change="handleUpdateOutputVariable(key, 'description', $event.target.value)"
            />
          </template>
          <button
            v-if="!readonly && supportsBinding(key, config.type)"
            class="output-binding-btn"
            :class="{ 'binding-active': config.binding }"
            @click="openBindingConfig(key)"
            :title="t('flow.outputVars.bindingTitle')"
          >
            <el-icon><Connection /></el-icon>
          </button>
        </div>
      </div>
    </div>

    <Teleport to="body">
      <div v-if="showOutputDropdown" class="output-dropdown-overlay" @click="closeOutputDropdown">
        <div class="output-dropdown-menu" :style="outputDropdownStyle" @click.stop>
          <div class="output-dropdown-item" @click="handleAddOutputVariable('string')"><span class="dropdown-icon icon-string-inline">S</span>{{ t('flow.typeOptions.string') }}</div>
          <div class="output-dropdown-item" @click="handleAddOutputVariable('number')"><span class="dropdown-icon icon-number-inline">N</span>{{ t('flow.typeOptions.number') }}</div>
          <div class="output-dropdown-item" @click="handleAddOutputVariable('boolean')"><span class="dropdown-icon icon-boolean-inline">B</span>{{ t('flow.typeOptions.boolean') }}</div>
          <div class="output-dropdown-item" @click="handleAddOutputVariable('object')"><span class="dropdown-icon icon-object-inline">O</span>{{ t('flow.typeOptions.object') }}</div>
          <div class="output-dropdown-divider"></div>
          <div class="output-dropdown-item" @click="handleAddOutputVariable('array[string]')"><span class="dropdown-icon icon-string-array-inline">S</span>{{ t('flow.typeOptions.arrayString') }}</div>
          <div class="output-dropdown-item" @click="handleAddOutputVariable('array[number]')"><span class="dropdown-icon icon-number-array-inline">N</span>{{ t('flow.typeOptions.arrayNumber') }}</div>
          <div class="output-dropdown-item" @click="handleAddOutputVariable('array[boolean]')"><span class="dropdown-icon icon-boolean-array-inline">B</span>{{ t('flow.typeOptions.arrayBoolean') }}</div>
          <div class="output-dropdown-divider"></div>
          <div class="output-dropdown-item" @click="handleAddOutputVariable('array[object]')"><span class="dropdown-icon icon-object-array-inline">O</span>{{ t('flow.typeOptions.arrayObject') }}</div>
        </div>
      </div>
    </Teleport>
  </div>
</template>

<script setup>
import { ref, watch, computed, inject, onMounted, onBeforeUnmount } from 'vue'
import { Plus, Delete, Connection, Lock } from '@element-plus/icons-vue'
import { getDataTypeIcon, getDataTypeIconClass, getDataTypeLabel } from './dataTypeBadge.js'
import { useEscClose } from '../../../composables/useEscClose'
import { useI18n } from 'vue-i18n'

const { t } = useI18n()

const props = defineProps({
  selectedNode: {
    type: Object,
    default: null
  },
  readonly: {
    type: Boolean,
    default: false
  },
  existingNames: {
    type: Array,
    default: () => []
  },
  reservedNames: {
    type: Array,
    default: () => ['index', 'item']
  },
  fixedVariables: {
    type: Array,
    default: () => []
    // 格式：['mode', 'status', 'headers', ...]
  },
  // Variables the node writes itself and that are NOT config.outputs entries: [{ name, type, description }]. Displayed as
  // locked rows and never saved into the config - a loop's iterations belongs here, because a declared output would be
  // initialised to the type default and shadow the real value (NodeResult.GetVariable reads OutputVars first).
  builtinVariables: {
    type: Array,
    default: () => []
  }
})

const emit = defineEmits(['update-node-data'])

const outputVariables = ref({})

const isFixedVariable = (key) => props.fixedVariables.includes(key)

const sortedOutputVariables = computed(() => {
  const vars = outputVariables.value
  const fixedOrder = props.fixedVariables
  const result = {}

  // Fixed variables first, in declaration order
  fixedOrder.forEach(name => {
    if (vars[name]) {
      result[name] = vars[name]
    }
  })

  // Custom variables sorted by time
  Object.keys(vars)
    .filter(key => !fixedOrder.includes(key))
    .sort((a, b) => (vars[a].time || 0) - (vars[b].time || 0))
    .forEach(key => {
      result[key] = vars[key]
    })

  return result
})

const isNameTaken = (name) => {
  if (props.existingNames.includes(name)) return true
  if (outputVariables.value[name]) return true
  return false
}

const validateOutputKey = (key) => {
  const regex = /^[a-zA-Z][a-zA-Z0-9_]*$/
  return regex.test(key)
}

const getOutputTypeClass = getDataTypeIconClass
const getOutputTypeIcon = getDataTypeIcon

const save = () => {
  const outputForSave = {}

  Object.keys(outputVariables.value).forEach(key => {
    const { time, ...rest } = outputVariables.value[key]
    outputForSave[key] = rest
  })

  emit('update-node-data', {
    nodeId: props.selectedNode.id,
    data: { config: { outputs: outputForSave } }
  })
}

// ── 自定义输出变量下拉菜单 ──
const showOutputDropdown = ref(false)
const addOutputBtnRef = ref(null)
const outputDropdownPos = ref({ left: 0, top: 0 })

const outputDropdownStyle = computed(() => ({
  position: 'fixed',
  left: outputDropdownPos.value.left + 'px',
  top: outputDropdownPos.value.top + 'px',
  zIndex: 9999
}))

let outputDropdownRafId = null

const updateOutputDropdownPosition = () => {
  if (!showOutputDropdown.value) return
  const btn = addOutputBtnRef.value
  const propsPanel = document.querySelector('.props-panel')
  if (!btn || !propsPanel) return
  const panelRect = propsPanel.getBoundingClientRect()
  const btnRect = btn.getBoundingClientRect()
  const menuEl = document.querySelector('.output-dropdown-menu')
  const menuWidth = (menuEl && menuEl.offsetWidth > 0) ? menuEl.offsetWidth : 160
  outputDropdownPos.value = {
    left: panelRect.right - menuWidth,
    top: btnRect.bottom + 4
  }
  outputDropdownRafId = requestAnimationFrame(updateOutputDropdownPosition)
}

const toggleOutputDropdown = () => {
  showOutputDropdown.value = !showOutputDropdown.value
  if (showOutputDropdown.value) {
    const btn = addOutputBtnRef.value
    const propsPanel = document.querySelector('.props-panel')
    if (btn && propsPanel) {
      const btnRect = btn.getBoundingClientRect()
      const panelRect = propsPanel.getBoundingClientRect()
      outputDropdownPos.value = { left: panelRect.right - 160, top: btnRect.bottom + 4 }
    }
    outputDropdownRafId = requestAnimationFrame(updateOutputDropdownPosition)
  } else {
    if (outputDropdownRafId) cancelAnimationFrame(outputDropdownRafId)
  }
}

const closeOutputDropdown = () => {
  showOutputDropdown.value = false
  if (outputDropdownRafId) cancelAnimationFrame(outputDropdownRafId)
}

// 自定义下拉浮层：ESC 收下拉，属性面板本身不会跟着关
useEscClose(showOutputDropdown, closeOutputDropdown)

onMounted(() => {
  window.addEventListener('resize', closeOutputDropdown)
})

onBeforeUnmount(() => {
  if (outputDropdownRafId) cancelAnimationFrame(outputDropdownRafId)
  window.removeEventListener('resize', closeOutputDropdown)
})

const handleAddOutputVariable = (type) => {
  closeOutputDropdown()
  const sanitizedType = type.replace('[', '_').replace(']', '')

  let fieldName = `${sanitizedType}_1`
  let counter = 1

  while (isNameTaken(fieldName)) {
    fieldName = `${sanitizedType}_${counter}`
    counter++
  }

  const newVariables = { ...outputVariables.value }
  newVariables[fieldName] = {
    type,
    description: '',
    time: Date.now()
  }

  outputVariables.value = newVariables
  save()
}

const handleUpdateOutputVariable = (key, field, value) => {
  const newVariables = { ...outputVariables.value }
  newVariables[key] = {
    ...newVariables[key],
    [field]: value
  }
  outputVariables.value = newVariables
  save()
}

const handleRenameOutputVariable = (oldKey, newKey) => {
  if (isFixedVariable(oldKey)) return

  const trimmedKey = newKey.trim()

  if (!trimmedKey) {
    outputVariables.value = { ...outputVariables.value }
    return
  }

  if (!validateOutputKey(trimmedKey)) {
    outputVariables.value = { ...outputVariables.value }
    return
  }

  if (props.reservedNames.includes(trimmedKey.toLowerCase())) {
    const inputElement = document.querySelector(`input[data-variable-key="output-${oldKey}"]`)
    if (inputElement) {
      inputElement.value = oldKey
    }
    return
  }

  if (oldKey === trimmedKey) return

  if (isNameTaken(trimmedKey)) {
    outputVariables.value = { ...outputVariables.value }
    return
  }

  const newVariables = { ...outputVariables.value }
  newVariables[trimmedKey] = { ...newVariables[oldKey] }
  delete newVariables[oldKey]

  outputVariables.value = newVariables
  save()
}

const handleDeleteOutputVariable = (key) => {
  if (isFixedVariable(key)) return
  const newVariables = { ...outputVariables.value }
  delete newVariables[key]
  outputVariables.value = newVariables
  save()
}

// ── Binding 配置 ──
const openBindingEditFn = inject('openBindingEdit', null)

// 只有"正文类"固定输出变量才允许绑定 result / progress。
// 把 status / headers / mode / stream_finished 这类元数据绑到 result，会把 "200"、"true"
// 这类值直接拼进最终返回值（AddResult 是裸拼接），属于污染结果，从入口上关掉。
// LLM 的 reasoning_content 同理排除：思考内容帧先于正文到达，绑 result 会把推理过程拼进最终答案。
// 未登记的节点类型（code / call / loop / process 等）输出变量都由用户自行声明，不限制。
const TEXT_FIXED_FIELDS = {
  http: ['body'],
  llm: ['content'],
  rpc: ['result'],
  mcp: ['result']
}

const nodeType = computed(() => props.selectedNode?.type || '')

// ── 历史配置兜底：入口禁用不会清理已存在的 binding ──
// 老节点可能已经把 reasoning_content 绑到 result（此前是允许的），运行时照样会触发，
// 所以编辑期给一条可见的警告，而不是让它静默污染最终答案。
const bindingList = (binding) => (Array.isArray(binding) ? binding : (binding ? [binding] : []))
const bindsResult = (key) => {
  const config = outputVariables.value[key]
  if (!config) return false
  return bindingList(config.binding).some(b => b && b.channel === 'result')
}

const bindingConflict = computed(() => {
  if (nodeType.value !== 'llm') return false
  return bindsResult('content') && bindsResult('reasoning_content')
})

// HTTP 的 body 只在 complete 模式写入，SSE 模式下 body 为空、绑定静默不触发；
// 另外非 2xx 响应也会写 body（节点仍判成功），需要用户自己判断状态码。
const httpBodyBindingHint = computed(() => {
  if (nodeType.value !== 'http') return false
  const config = outputVariables.value['body']
  return !!config && bindingList(config.binding).length > 0
})

const supportsBinding = (key, type) => {
  if (!['string', 'number', 'boolean'].includes(type)) return false
  const textFields = TEXT_FIXED_FIELDS[nodeType.value]
  if (!textFields) return true           // 未登记类型：保持原有行为
  if (!isFixedVariable(key)) return true // 用户自定义的输出变量：不限制
  return textFields.includes(key)
}

const openBindingConfig = (key) => {
  const config = outputVariables.value[key]
  if (openBindingEditFn) {
    openBindingEditFn({
      varName: key,
      varType: config.type,
      binding: config.binding || null
    })
  }
}

const getOutputTypeLabel = getDataTypeLabel

const loadFromNode = (node) => {
  if (node?.data) {
    outputVariables.value = node.data.config?.outputs || {}
  }
}

watch(() => props.selectedNode, loadFromNode, { immediate: true })
</script>

<style scoped>
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

.output-variables-cards {
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.binding-conflict-hint {
  font-size: 11px;
  line-height: 1.5;
  color: #8a6d3b;
  background: #fdf6ec;
  border-radius: 4px;
  padding: 6px 8px;
  margin-bottom: 8px;
}

.binding-conflict-hint code {
  font-family: 'Monaco', 'Menlo', monospace;
  color: #b8860b;
}

.binding-note {
  font-size: 11px;
  line-height: 1.5;
  color: #5a7ea8;
  background: #f4f8ff;
  border-radius: 4px;
  padding: 6px 8px;
  margin-bottom: 8px;
}

.binding-note code {
  font-family: 'Monaco', 'Menlo', monospace;
  color: #409eff;
}

.output-empty-state {
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 8px;
  text-align: center;
}

.output-empty-state p {
  color: #999;
  margin: 0;
  font-size: 12px;
}

.output-variable-row {
  display: flex;
  align-items: center;
  gap: 8px;
  border: 0px solid #e8e8e8;
  border-radius: 6px;
  padding: 0px 0px;
}

.output-variable-header {
  display: flex;
  align-items: center;
  gap: 8px;
  /* 固定占比：说明列收窄省下的空间给变量名，不再按 1:2 被内容列挤压 */
  flex: 0 0 45%;
  min-width: 0;
  max-width: 45%;
}

.output-variable-content {
  flex: 2;
  display: flex;
  align-items: center;
  gap: 8px;
}

.output-type-badge {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 18px;
  height: 18px;
  font-size: 12px;
  font-weight: bold;
  font-family: Arial, sans-serif;
  color: #fff;
  flex-shrink: 0;
}

.output-type-badge.type-string {
  background: #67c23a;
  border-radius: 50%;
}

.output-type-badge.type-number {
  background: #409eff;
  border-radius: 50%;
}

.output-type-badge.type-boolean {
  background: #9c27b0;
  border-radius: 50%;
}

.output-type-badge.type-object {
  background: #909399;
  border-radius: 50%;
}

.output-type-badge.type-array-string {
  background: #f56c6c;
  border-radius: 4px;
}

.output-type-badge.type-array-number {
  background: #e6a23c;
  border-radius: 4px;
}

.output-type-badge.type-array-boolean {
  background: #9c27b0;
  border-radius: 4px;
}

.output-type-badge.type-array-object {
  background: #909399;
  border-radius: 4px;
}

.output-variable-name-input {
  flex: 1;
  min-width: 0;
  padding: 12px 10px;
  border: 0px solid #d9d9d9;
  background-color: #eeeeee;
  border-radius: 4px;
  font-size: 13px;
  outline: none;
  transition: border-color 0.2s;
}

.output-variable-name-input:focus {
  outline: none;
}

.output-variable-name-input::placeholder {
  color: #bbb;
}

.output-variable-name-readonly {
  flex: 1;
  min-width: 0;
  padding: 10px 10px;
  background-color: #eeeeee;
  border-radius: 4px;
  font-size: 13px;
  font-weight: 500;
  color: #333;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.fixed-variable-row {
  opacity: 0.85;
}

.fixed-output-var-lock {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 20px;
  height: 20px;
  color: #999;
  font-size: 12px;
  flex-shrink: 0;
}

.output-description-input {
  flex: 1;
  min-width: 0;
  padding: 12px 10px;
  border: 0px solid #d9d9d9;
  border-radius: 4px;
  font-size: 12px;
  color: #666;
  background-color: #eeeeee;
  outline: none;
  transition: border-color 0.2s;
}

.output-description-input:focus {
  outline: none;
}

.output-description-input::placeholder {
  color: #bbb;
}

.output-description-readonly {
  flex: 1;
  min-width: 0;
  padding: 10px 10px;
  background-color: #f5f5f5;
  border-radius: 4px;
  font-size: 12px;
  color: #666;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.output-delete-btn {
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

.output-delete-btn:hover {
  background: #fff5f5;
  color: #f56c6c;
}

.output-binding-btn {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 20px;
  height: 20px;
  padding: 0;
  border: none;
  border-radius: 3px;
  font-size: 12px;
  color: #bbb;
  background: transparent;
  cursor: pointer;
  transition: all 0.2s;
  flex-shrink: 0;
  margin-left: auto; /* 说明列收窄后靠右对齐，避免按钮浮在中间 */
}

.output-binding-btn:hover {
  background: #e6f7ff;
  color: #1890ff;
}

.output-binding-btn.binding-active {
  color: #1890ff;
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

/* 自定义输出变量下拉菜单 */
.output-dropdown-overlay {
  position: fixed;
  top: 0; left: 0; right: 0; bottom: 0;
  z-index: 9998;
  background: transparent;
}
.output-dropdown-menu {
  background: #fff;
  border-radius: 4px;
  box-shadow: 0 2px 12px rgba(0, 0, 0, 0.15);
  padding: 4px 0;
  min-width: 140px;
  font-size: 13px;
}
.output-dropdown-item {
  padding: 6px 16px;
  cursor: pointer;
  color: #333;
  display: flex;
  align-items: center;
  white-space: nowrap;
  transition: background 0.15s;
}
.output-dropdown-item:hover {
  background: #f5f7fa;
  color: #1890ff;
}
.output-dropdown-divider {
  height: 1px;
  background: #ebeef5;
  margin: 4px 0;
}
</style>
