<template>
  <div class="assign-node-props">
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

        <!-- 赋值列表 -->
        <div class="props-section">
          <div class="section-title">
            <span>{{ t('flow.assignNode.listTitle') }}</span>
            <button class="add-variable-icon-btn add-assign-btn" ref="assignAddBtnRef" @click="toggleAssignDropdown">
              <el-icon><Plus /></el-icon>
            </button>
          </div>

          <div class="assignments-list">
            <div v-if="sortedAssignments.length === 0" class="input-empty-state">
              <p>{{ t('flow.assignNode.empty') }}</p>
            </div>
            <div v-for="assignment in sortedAssignments" :key="assignment._key" class="assignment-row">
              <!-- 第一行：字段 -->
              <div class="assignment-fields">
                <span class="assign-kind-tag" :class="assignment.kind === 'output' ? 'kind-output' : 'kind-target'">
                  {{ assignment.kind === 'output' ? t('flow.outputVars.title') : t('flow.assignNode.otherVar') }}
                </span>

                <!-- 变量列 -->
                <template v-if="assignment.kind === 'output'">
                  <input type="text" :value="assignment.target" class="assign-target-input"
                    @focus="handleAssignTargetFocus()"
                    @blur="handleUpdateAssignTarget(assignment, $event.target.value)" :placeholder="t('flow.common.varName')" />
                </template>
                <template v-else>
                  <button class="show-content-btn input-variable-btn" @click="handleShowTargetSelect(assignment, $event)">
                    <template v-if="getTargetDisplay(assignment)">
                      <span class="input-type-badge" :class="getDataTypeIconClass(getTargetType(assignment))">{{ getDataTypeIcon(getTargetType(assignment)) }}</span>
                      <span class="input-var-key">{{ getTargetDisplay(assignment) }}</span>
                      <template v-if="getTargetSourceIcon(assignment)">
                        <template v-if="getTargetSourceIcon(assignment).isNode">
                          <span class="input-node-icon" :class="getTargetSourceIcon(assignment).class">
                            <el-icon><component :is="getTargetSourceIcon(assignment).component" /></el-icon>
                          </span>
                          <span class="input-node-label">{{ getTargetSourceIcon(assignment).nodeLabel }}</span>
                        </template>
                        <template v-else>
                          <el-icon :class="getTargetSourceIcon(assignment).class">
                            <component :is="getTargetSourceIcon(assignment).component" />
                          </el-icon>
                        </template>
                      </template>
                    </template>
                    <template v-else>{{ t('flow.common.selectVar') }}</template>
                  </button>
                </template>

                <!-- 类型列（仅 output） -->
                <template v-if="assignment.kind === 'output'">
                  <el-dropdown v-if="isFirstOutputRow(assignment)"
                    @command="(t) => handleOutputTypeChange(assignment, t)"
                    trigger="click" placement="bottom-start">
                    <span class="assign-type-btn">
                      <span class="dropdown-icon" :class="typeIconClass(getOutputType(assignment.target))">{{ getDataTypeIcon(getOutputType(assignment.target)) }}</span>
                    </span>
                    <template #dropdown>
                      <el-dropdown-menu>
                        <el-dropdown-item v-for="t in OUTPUT_TYPES" :key="t" :command="t">
                          <span class="dropdown-icon" :class="typeIconClass(t)">{{ getDataTypeIcon(t) }}</span>{{ typeLabelShort(t) }}
                        </el-dropdown-item>
                      </el-dropdown-menu>
                    </template>
                  </el-dropdown>
                  <span v-else class="assign-type-readonly">
                    <span class="dropdown-icon" :class="typeIconClass(getOutputType(assignment.target))">{{ getDataTypeIcon(getOutputType(assignment.target)) }}</span>
                  </span>
                </template>

                <!-- 路径列（仅 output 显示在字段行内） -->
                <template v-if="assignment.kind === 'output'">
                  <input type="text" :value="assignment.path" class="assign-path-input"
                    :disabled="!isPathEditable(assignment)"
                    :placeholder="getPathPlaceholder(assignment)"
                    @blur="handleUpdateAssignPath(assignment, $event.target.value)" />
                </template>

                <!-- 删除 -->
                <button class="input-delete-btn" @click="handleDeleteAssignment(assignment)">
                  <el-icon><Delete /></el-icon>
                </button>
              </div>

              <!-- 其他变量路径（独占一行） -->
              <div v-if="assignment.kind === 'target'" class="assign-target-path-row">
                <span class="target-path-label">{{ t('flow.assignNode.targetPath') }}</span>
                <input type="text" :value="assignment.path" class="assign-path-input assign-path-full"
                  :disabled="!isPathEditable(assignment)"
                  :placeholder="getPathPlaceholder(assignment)"
                  @blur="handleUpdateAssignPath(assignment, $event.target.value)" />
              </div>

              <!-- 值编辑区 -->
              <div class="assign-value-row">
                <textarea :value="assignment.value" class="assign-value-input"
                  :placeholder="t('flow.assignNode.templatePlaceholder')" rows="2"
                  @blur="handleUpdateAssignValue(assignment, $event.target.value)"></textarea>
              </div>
            </div>
          </div>
        </div>
      </div>

      <!-- 执行结果 -->
      <div v-if="activeTab === 'lastRun'" class="last-run-panel">
        <div v-if="!assignResult" class="nr-empty">{{ t('flow.node.run.empty') }}</div>
        <div v-else class="props-section">
          <div class="result-item result-meta-row">
            <span class="meta-item">
              <label class="result-label">{{ t('flow.node.run.status') }}</label>
              <span class="result-value" :class="assignStatusClass">{{ assignStatusText }}</span>
            </span>
            <span class="meta-item">
              <label class="result-label">{{ t('flow.node.run.time') }}</label>
              <span class="result-value">{{ assignStartTimeText || '-' }}</span>
            </span>
            <span class="meta-item">
              <label class="result-label">{{ t('flow.node.run.duration') }}</label>
              <span class="result-value">{{ assignResult.duration_ms }}ms</span>
            </span>
          </div>

          <div class="result-item">
            <label class="result-label">{{ t('flow.node.run.input') }}</label>
            <pre class="result-output">{{ assignInputText }}</pre>
          </div>

          <div class="result-item">
            <label class="result-label">{{ t('flow.node.run.output') }}</label>
            <pre class="result-output">{{ assignOutputText }}</pre>
          </div>

          <div v-if="assignLocalText" class="result-item">
            <label class="result-label">{{ t('flow.node.run.localVars') }}</label>
            <pre class="result-output">{{ assignLocalText }}</pre>
          </div>

          <div v-if="assignLogs.length" class="result-item">
            <label class="result-label">{{ t('flow.node.run.logs') }}</label>
            <div class="nr-logs">
              <div v-for="(log, i) in assignLogs" :key="i" class="nr-log">{{ log }}</div>
            </div>
          </div>

          <div v-if="assignResult.err_msg" class="result-item">
            <label class="result-label">{{ t('flow.node.run.error') }}</label>
            <div class="nr-log nr-error">{{ assignResult.err_msg }}</div>
          </div>
        </div>
      </div>
    </div>

    <Teleport to="body">
      <div v-if="showAssignDropdown" class="assign-dropdown-overlay" @click="closeAssignDropdown">
        <div class="assign-dropdown-menu" :style="assignDropdownStyle" @click.stop>
          <div class="assign-dropdown-group-title">{{ t('flow.assignNode.outputGroup') }}</div>
          <div class="assign-dropdown-item" @click="handleAddAssignment('string')"><span class="dropdown-icon icon-string-inline">S</span>{{ t('flow.typeOptions.string') }}</div>
          <div class="assign-dropdown-item" @click="handleAddAssignment('number')"><span class="dropdown-icon icon-number-inline">N</span>{{ t('flow.typeOptions.number') }}</div>
          <div class="assign-dropdown-item" @click="handleAddAssignment('boolean')"><span class="dropdown-icon icon-boolean-inline">B</span>{{ t('flow.typeOptions.boolean') }}</div>
          <div class="assign-dropdown-item" @click="handleAddAssignment('object')"><span class="dropdown-icon icon-object-inline">O</span>{{ t('flow.typeOptions.object') }}</div>
          <div class="assign-dropdown-divider"></div>
          <div class="assign-dropdown-item" @click="handleAddAssignment('array[string]')"><span class="dropdown-icon icon-string-array-inline">S</span>{{ t('flow.typeOptions.arrayString') }}</div>
          <div class="assign-dropdown-item" @click="handleAddAssignment('array[number]')"><span class="dropdown-icon icon-number-array-inline">N</span>{{ t('flow.typeOptions.arrayNumber') }}</div>
          <div class="assign-dropdown-item" @click="handleAddAssignment('array[boolean]')"><span class="dropdown-icon icon-boolean-array-inline">B</span>{{ t('flow.typeOptions.arrayBoolean') }}</div>
          <div class="assign-dropdown-divider"></div>
          <div class="assign-dropdown-item" @click="handleAddAssignment('array[object]')"><span class="dropdown-icon icon-object-array-inline">O</span>{{ t('flow.typeOptions.arrayObject') }}</div>
          <div class="assign-dropdown-divider"></div>
          <div class="assign-dropdown-item" @click="handleAddAssignment('target')">{{ t('flow.codeNode.writeTargets') }}</div>
        </div>
      </div>
    </Teleport>
  </div>
</template>

<script setup>
import { ref, watch, computed, inject, onMounted, onBeforeUnmount } from 'vue'
import { nodeResults } from '@/tenant/composables/runData.js'
import { Plus, Delete, Timer, Help, Link, MagicStick, Lightning, Promotion, RefreshRight, CircleClose, ChatLineSquare, Collection, User, Operation, Coin, Minus, Upload, CircleCheck, ScaleToOriginal, Monitor, Mouse, RefreshLeft, ChromeFilled, Notification } from '@element-plus/icons-vue'
import { resolveSourceIcon } from './nodeSourceIcons.js'
import { getDataTypeIcon, getDataTypeIconClass } from './dataTypeBadge.js'
import { useInputVariables } from './inputVariables.js'
import { useEscClose } from '../../../composables/useEscClose'
import { useI18n } from 'vue-i18n'
import { formatDateTime } from '@/utils/datetime.js'

const { t, te } = useI18n()

const props = defineProps({
  selectedNode: { type: Object, default: null },
  flowFlowVariables: { type: Object, default: () => ({}) },
  flowEnvironmentVariables: { type: Object, default: () => ({}) },
  flowArguments: { type: Object, default: () => ({}) }
})

const emit = defineEmits(['update-node-data', 'show-variable-select'])

const FlowNodes = inject('FlowNodes', ref([]))
const flowDebug = inject('flowDebug', null)

// ── 常量 ──
const OUTPUT_TYPES = ['string', 'number', 'boolean', 'object', 'array[string]', 'array[number]', 'array[boolean]', 'array[object]']
const SCALAR_TYPES = ['string', 'number', 'boolean']

// ── tabs ──
const activeTab = ref('settings')

// ── assign 节点执行结果展示 ──
const assignResult = computed(() => {
  if (!props.selectedNode) return null
  return nodeResults.value?.[props.selectedNode.id] || null
})
// 选中节点变化或切到「执行结果」tab（组件挂载）时按需拉取该节点最新数据
watch(() => props.selectedNode?.id, (id) => {
  if (id && flowDebug && flowDebug.connected.value) {
    flowDebug.inspectNode(id)
  }
}, { immediate: true })
const assignStatusText = computed(() => {
  const status = assignResult.value?.status
  if (!status) return ''
  return te(`flow.node.run.statusMap.${status}`) ? t(`flow.node.run.statusMap.${status}`) : status
})
const assignStatusClass = computed(() => {
  const map = { success: 'status-success', failed: 'status-failed', running: 'status-running', skipped: 'status-default', cancelled: 'status-default' }
  return map[assignResult.value?.status] || 'status-default'
})
const assignStartTimeText = computed(() => {
  const t = assignResult.value?.start_time
  if (!t) return ''
  // 解析失败时退回原串（比印 "Invalid Date" 有意义）
  return formatDateTime(t) || t
})
const assignInputText = computed(() => {
  const v = assignResult.value?.input_vars
  return v && Object.keys(v).length ? JSON.stringify(v, null, 2) : '{}'
})
const assignOutputText = computed(() => {
  const v = assignResult.value?.output_vars
  return v && Object.keys(v).length ? JSON.stringify(v, null, 2) : '{}'
})
const assignLocalText = computed(() => {
  const v = assignResult.value?.local_vars
  return v && Object.keys(v).length ? JSON.stringify(v, null, 2) : ''
})
const assignLogs = computed(() => assignResult.value?.run_logs || [])


// ── 输入变量：增删改 / 校验 / 快照统一在 inputVariables.js ──
const inputVariables = ref({})
const {
  snapshot: buildInputsToSave,
  addInputVariable,
  updateInputVariableName,
  deleteInputVariable,
  applySelectedVariable
} = useInputVariables({
  inputVariables,
  onChange: () => saveFull()
})

const handleAddInputVariable = addInputVariable
const handleUpdateInputVariableName = updateInputVariableName
const handleDeleteInputVariable = deleteInputVariable
const handleInputVariableFocus = () => {}

const handleShowInputVariableSelect = (name, event) => {
  emit('show-variable-select', event, name, (value) => applySelectedVariable(name, value))
}

// ── 赋值列表 ──
const assignments = ref([]) // 内部列表，带 _key 用于 v-for

let assignKeyCounter = 0
const nextAssignKey = () => `a${++assignKeyCounter}`

// 按 kind 排序：output 在前，target 在后
const sortedAssignments = computed(() => {
  return [...assignments.value].sort((a, b) => {
    if (a.kind === b.kind) return 0
    return a.kind === 'output' ? -1 : 1
  })
})

// 获取 output 变量定义
const getOutputVars = () => {
  const cf = props.selectedNode?.data?.config || {}
  return cf.outputs || {}
}

const getOutputType = (varName) => {
  const ov = getOutputVars()
  return ov[varName]?.type || ''
}

// 判断路径是否可编辑
const isPathEditable = (assignment) => {
  if (assignment.kind === 'output') {
    const t = getOutputType(assignment.target)
    return !!t && !SCALAR_TYPES.includes(t)
  }
  // target: 看 target 定义中的 type
  const t = getTargetType(assignment)
  return !!t && !SCALAR_TYPES.includes(t)
}

const getPathPlaceholder = (assignment) => {
  const type = assignment.kind === 'output' ? getOutputType(assignment.target) : getTargetType(assignment)
  if (type === 'object') return t('flow.assignNode.pathHintObject')
  if (type && type.startsWith('array[')) return t('flow.assignNode.pathHintArray')
  return '—'
}

// 判断是否为该变量首行（第一个出现的 output 行）
const isFirstOutputRow = (assignment) => {
  if (assignment.kind !== 'output') return false
  const sameVars = assignments.value.filter(a => a.kind === 'output' && a.target === assignment.target)
  if (sameVars.length <= 1) return true
  return sameVars[0]._key === assignment._key
}

// 短类型名（不含字母前缀，用于 el-dropdown 显示）
const typeLabelShort = (type) => {
  const labels = { string: 'string', number: 'number', boolean: 'boolean', object: 'object', 'array[string]': 'arrayString', 'array[number]': 'arrayNumber', 'array[boolean]': 'arrayBoolean', 'array[object]': 'arrayObject' }
  return labels[type] ? t(`flow.typeOptions.${labels[type]}`) : type
}

// 类型图标 CSS class（与 [+] 下拉菜单的 dropdown-icon 一致）
const typeIconClass = (t) => {
  const map = { string: 'icon-string-inline', number: 'icon-number-inline', boolean: 'icon-boolean-inline', object: 'icon-object-inline', 'array[string]': 'icon-string-array-inline', 'array[number]': 'icon-number-array-inline', 'array[boolean]': 'icon-boolean-array-inline', 'array[object]': 'icon-object-array-inline' }
  return map[t] || 'icon-object-inline'
}

// output 类型变更（级联）
const handleOutputTypeChange = (assignment, newType) => {
  const oldType = getOutputType(assignment.target)
  if (oldType === newType) return

  const wasComplex = oldType && !SCALAR_TYPES.includes(oldType)
  const isSimple = SCALAR_TYPES.includes(newType)

  if (wasComplex && isSimple) {
    const sameRows = assignments.value.filter(a => a.kind === 'output' && a.target === assignment.target && a._key !== assignment._key && a.path)
    if (sameRows.length > 0) {
      if (!confirm(t('flow.assignNode.typeChangeConfirm', { name: assignment.target, oldType, newType, count: sameRows.length }))) {
        // 取消：不做任何变更，select 值通过 v-bind:value 自然恢复
        return
      }
      sameRows.forEach(a => { a.path = '' })
    }
  }

  // 更新 config.outputs
  const outputs = { ...getOutputVars() }
  outputs[assignment.target] = { type: newType }
  emit('update-node-data', { nodeId: props.selectedNode.id, data: { config: { outputs } } })
  saveFull()
}

// [+] 下拉处理
const handleAddAssignment = (command) => {
  closeAssignDropdown()
  if (command === 'target') {
    // 创建空的 target 定义：source_type 留空，等用户从选择器里挑一个可写目标
    // （选择器提供祖先容器的输出/局部变量和流程变量；env/argument/builtin 只读，不参与目标选择）
    const targets = { ...(props.selectedNode?.data?.config?.targets || {}) }
    let counter = 1
    while (targets[`target${counter}`]) counter++
    const targetName = `target${counter}`
    const newAssign = { _key: nextAssignKey(), kind: 'target', target: targetName, path: '', value: '', _targetName: targetName }
    targets[targetName] = { type: '', source_type: '', source_ref: '', source_key: '' }
    assignments.value.push(newAssign)
    emit('update-node-data', { nodeId: props.selectedNode.id, data: { config: { targets: targets } } })
    saveFull()
    return
  }

  // output 类型
  const newName = generateOutputVarName()
  const newAssign = { _key: nextAssignKey(), kind: 'output', target: newName, path: '', value: '' }
  assignments.value.push(newAssign)
  // 写入 config.outputs
  updateOutputVar(newName, command)
  saveFull()
}

const generateOutputVarName = () => {
  let counter = 1
  const existing = Object.keys(getOutputVars())
  while (existing.includes(`var${counter}`)) counter++
  return `var${counter}`
}

const updateOutputVar = (name, type) => {
  const outputs = { ...getOutputVars() }
  if (!outputs[name]) {
    outputs[name] = { type }
  }
  emit('update-node-data', { nodeId: props.selectedNode.id, data: { config: { outputs } } })
}

// 变量名变更
const handleAssignTargetFocus = () => {}

const handleUpdateAssignTarget = (assignment, value) => {
  const trimmed = value.trim()
  if (!trimmed || trimmed === assignment.target) return

  // 检查是否已存在同名 output
  if (assignments.value.some(a => a.kind === 'output' && a.target === trimmed && a._key !== assignment._key)) {
    alert(t('flow.assignNode.nameExists'))
    return
  }

  // 从 outputs 移除旧名，加新名
  const outputs = { ...getOutputVars() }
  const oldType = outputs[assignment.target]?.type || ''
  delete outputs[assignment.target]
  outputs[trimmed] = { type: oldType }
  assignment.target = trimmed
  emit('update-node-data', { nodeId: props.selectedNode.id, data: { config: { outputs } } })
  saveFull()
}

const handleUpdateAssignPath = (assignment, value) => {
  assignment.path = value.trim()
  saveFull()
}

const handleUpdateAssignValue = (assignment, value) => {
  assignment.value = value
  saveFull()
}

const handleDeleteAssignment = (assignment) => {
  assignments.value = assignments.value.filter(a => a._key !== assignment._key)
  if (assignment.kind === 'output') {
    // 如果 outputs 中该变量无其他行引用，删除 outputs 条目
    const stillUsed = assignments.value.some(a => a.kind === 'output' && a.target === assignment.target)
    if (!stillUsed) {
      const outputs = { ...getOutputVars() }
      delete outputs[assignment.target]
      emit('update-node-data', { nodeId: props.selectedNode.id, data: { config: { outputs } } })
    }
  } else if (assignment.kind === 'target') {
    // 删除对应 target 定义
    const targets = { ...(props.selectedNode?.data?.config?.targets || {}) }
    delete targets[assignment.target]
    emit('update-node-data', { nodeId: props.selectedNode.id, data: { config: { targets: targets } } })
  }
  saveFull()
}

// target 选择
const getTargetType = (assignment) => {
  if (assignment.kind !== 'target') return ''
  const targets = props.selectedNode?.data?.config?.targets || {}
  return targets[assignment.target]?.type || ''
}

const getTargetDisplay = (assignment) => {
  if (assignment.kind !== 'target') return ''
  const targets = props.selectedNode?.data?.config?.targets || {}
  const s = targets[assignment.target]
  if (!s || !s.source_key) return ''
  return s.source_key
}

const getTargetSourceIcon = (assignment) => {
  if (assignment.kind !== 'target') return null
  const targets = props.selectedNode?.data?.config?.targets || {}
  const s = targets[assignment.target]
  if (!s || !s.source_type) return null
  return getSourceIcon(s.source_type, s.source_ref)
}

// ── 自定义赋值下拉菜单（完全控制定位） ──
const showAssignDropdown = ref(false)
const assignAddBtnRef = ref(null)
const assignDropdownPos = ref({ left: 0, top: 0 })

const assignDropdownStyle = computed(() => ({
  position: 'fixed',
  left: assignDropdownPos.value.left + 'px',
  top: assignDropdownPos.value.top + 'px',
  zIndex: 9999
}))

let assignDropdownRafId = null

const updateAssignDropdownPosition = () => {
  if (!showAssignDropdown.value) return
  const btn = assignAddBtnRef.value
  const propsPanel = document.querySelector('.props-panel')
  if (!btn || !propsPanel) return
  const panelRect = propsPanel.getBoundingClientRect()
  const btnRect = btn.getBoundingClientRect()
  const menuEl = document.querySelector('.assign-dropdown-menu')
  const menuWidth = (menuEl && menuEl.offsetWidth > 0) ? menuEl.offsetWidth : 160
  assignDropdownPos.value = {
    left: panelRect.right - menuWidth,
    top: btnRect.bottom + 4
  }
  assignDropdownRafId = requestAnimationFrame(updateAssignDropdownPosition)
}

const toggleAssignDropdown = () => {
  showAssignDropdown.value = !showAssignDropdown.value
  if (showAssignDropdown.value) {
    const btn = assignAddBtnRef.value
    const propsPanel = document.querySelector('.props-panel')
    if (btn && propsPanel) {
      const btnRect = btn.getBoundingClientRect()
      const panelRect = propsPanel.getBoundingClientRect()
      assignDropdownPos.value = { left: panelRect.right - 160, top: btnRect.bottom + 4 }
    }
    assignDropdownRafId = requestAnimationFrame(updateAssignDropdownPosition)
  } else {
    if (assignDropdownRafId) cancelAnimationFrame(assignDropdownRafId)
  }
}

const closeAssignDropdown = () => {
  showAssignDropdown.value = false
  if (assignDropdownRafId) cancelAnimationFrame(assignDropdownRafId)
}

// 自定义下拉浮层：ESC 收下拉，属性面板本身不会跟着关
useEscClose(showAssignDropdown, closeAssignDropdown)

onMounted(() => {
  window.addEventListener('resize', closeAssignDropdown)
})

onBeforeUnmount(() => {
  if (assignDropdownRafId) cancelAnimationFrame(assignDropdownRafId)
  window.removeEventListener('resize', closeAssignDropdown)
})

const handleShowTargetSelect = (assignment, event) => {
  const callback = (value) => {
    try {
      const data = JSON.parse(value)
      const targets = { ...(props.selectedNode?.data?.config?.targets || {}) }
      targets[assignment.target] = {
        type: data.variableType || '',
        source_type: data.source_type || '',
        source_ref: data.source_ref || '',
        source_key: data.source_key || ''
      }
      emit('update-node-data', { nodeId: props.selectedNode.id, data: { config: { targets: targets } } })
      saveFull()
    } catch (e) { console.error('Failed to parse target value:', e) }
  }
  emit('show-variable-select', event, assignment.target, callback, 'all', 'target')
}

// ── 保存 ──
const saveFull = () => {
  // 构建 assignments 存储（去掉 _key）
  const assignToSave = assignments.value.map(a => ({
    kind: a.kind,
    target: a.target,
    path: a.path || '',
    value: a.value || ''
  }))

  emit('update-node-data', {
    nodeId: props.selectedNode.id,
    data: {
      config: {
        inputs: buildInputsToSave(),
        assignments: assignToSave
      }
    }
  })
}

// ── 工具函数 ──

// 变量来源图标：实现在 nodeSourceIcons.js（12 个面板共用，避免 source 类型再漂移）
const getSourceIcon = (sourceType, sourceRef) =>
  resolveSourceIcon(sourceType, sourceRef, FlowNodes.value)

// ── status ──


// ── watch 加载已有数据 ──
watch(() => props.selectedNode, (node) => {
  if (node && node.data) {
    const cfg = node.data.config || {}

    // 输入变量
    const inputs = cfg.inputs || {}
    inputVariables.value = {}
    for (const name in inputs) {
      if (inputs.hasOwnProperty(name)) {
        inputVariables.value[name] = { ...inputs[name], name }
      }
    }

    // 赋值列表
    const raw = cfg.assignments || []
    assignments.value = raw.map(a => ({ ...a, _key: nextAssignKey() }))
  }
}, { immediate: true })
</script>

<style scoped>
.assign-node-props {
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
.add-assign-btn { background: #ff9800; }
.add-assign-btn:hover { background: #ffb74d; }

/* 输入变量 */
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

/* 赋值列表 */
.assignments-list { display: flex; flex-direction: column; gap: 8px; margin-top: 8px; }

.assignment-row {
  display: flex; flex-direction: column; gap: 8px;
  padding: 10px 12px;
  background: #fff;
  border: 1px solid #e8e8e8;
  border-radius: 6px;
  transition: border-color 0.2s;
}
.assignment-row:hover { border-color: #d0d0d0; }

/* 字段行 */
.assignment-fields {
  display: flex; align-items: center; gap: 8px;
}

.assign-kind-tag {
  flex-shrink: 0;
  display: inline-flex;
  align-items: center;
  height: 30px;
  box-sizing: border-box;
  padding: 0 8px;
  border-radius: 3px;
  font-size: 11px;
  font-weight: 500;
  white-space: nowrap;
  line-height: 1;
}
.kind-output { background: #e8e8e8; color: #1565c0; }
.kind-target { background: #e8e8e8; color: #00704a; }

.assign-target-input {
  flex: 1; min-width: 0; max-width: 110px;
  height: 30px; box-sizing: border-box; padding: 4px 8px;
  border: 0; background: #eee; border-radius: 4px;
  font-size: 12px; outline: none;
}
.assign-target-input:focus { background: #fafafa; }
.assign-target-input::placeholder { color: #bbb; }


.assign-type-btn {
  flex-shrink: 0;
  display: inline-flex; align-items: center; justify-content: center;
  width: 30px; height: 30px; border: 1px solid #e8e8e8; background: #fafafa;
  border-radius: 4px; cursor: pointer; transition: border-color 0.2s;
}
.assign-type-btn:hover { border-color: #1890ff; }
.assign-type-btn .dropdown-icon { margin-right: 0; width: 16px; height: 16px; font-size: 10px; }

.assign-type-readonly {
  flex-shrink: 0;
  display: inline-flex; align-items: center; justify-content: center;
  width: 30px; height: 30px;
}
.assign-type-readonly .dropdown-icon { margin-right: 0; width: 16px; height: 16px; font-size: 10px; }

.assign-path-input {
  flex: 1; min-width: 0;
  height: 30px; box-sizing: border-box; padding: 4px 8px;
  border: 0; background: #eee; border-radius: 4px;
  font-size: 12px; outline: none;
}
.assign-path-input:focus { background: #fafafa; }
.assign-path-input:disabled { background: #f0f0f0; color: #ccc; cursor: not-allowed; }
.assign-path-input::placeholder { color: #bbb; }

/* 其他变量路径行 */
.assign-target-path-row {
  display: flex; align-items: center; gap: 8px;
}
.target-path-label {
  flex-shrink: 0; font-size: 12px; color: #999; width: 28px;
}
.assign-path-full {
  flex: 1; max-width: none !important;
}

/* 值编辑行（独占一行） */
/* assignment-fields 内的 target 选择按钮与 input 统一高度 */
.assignment-fields > .input-variable-btn {
  height: 30px;
  box-sizing: border-box;
  padding: 4px 8px;
}

.assign-value-row { display: flex; align-items: flex-start; gap: 4px; }

.assign-value-input {
  flex: 1; min-width: 0; padding: 8px 10px;
  border: 0; background: #eee; border-radius: 4px;
  font-size: 12px; font-family: Monaco, Menlo, monospace;
  line-height: 1.5; resize: vertical; outline: none; min-height: 34px;
}
.assign-value-input:focus { background: #fafafa; }
.assign-value-input::placeholder { color: #bbb; }


/* dropdown */
.group-title { color: #999 !important; font-size: 12px; cursor: default !important; }
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

/* 自定义赋值下拉菜单 */
.assign-dropdown-overlay {
  position: fixed;
  top: 0; left: 0; right: 0; bottom: 0;
  z-index: 9998;
  background: transparent;
}
.assign-dropdown-menu {
  background: #fff;
  border-radius: 4px;
  box-shadow: 0 2px 12px rgba(0, 0, 0, 0.15);
  padding: 4px 0;
  min-width: 140px;
  font-size: 13px;
}
.assign-dropdown-group-title {
  padding: 4px 16px;
  color: #999;
  font-size: 12px;
  cursor: default;
  user-select: none;
}
.assign-dropdown-item {
  padding: 6px 16px;
  cursor: pointer;
  color: #333;
  display: flex;
  align-items: center;
  white-space: nowrap;
  transition: background 0.15s;
}
.assign-dropdown-item:hover {
  background: #f5f7fa;
  color: #1890ff;
}
.assign-dropdown-divider {
  height: 1px;
  background: #ebeef5;
  margin: 4px 0;
}

/* type badge */
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

/* last run */
.last-run-panel { padding: 0; }
.last-run-panel .props-section { padding: 16px 12px; }
.result-item { display: flex; flex-direction: column; gap: 8px; margin-bottom: 16px; }
.result-meta-row { flex-direction: row; align-items: center; flex-wrap: wrap; gap: 16px; }
.result-meta-row .meta-item { display: inline-flex; align-items: center; gap: 8px; }
.result-label { font-size: 12px; color: #333; font-weight: 600; }
.result-value { font-size: 13px; color: #333; }
.nr-empty { padding: 24px 0; text-align: center; color: #999; font-size: 13px; }
.nr-logs { max-height: 200px; overflow: auto; padding: 8px 10px; background: #fff; border: 1px solid #d9d9d9; border-radius: 4px; }
.nr-log { font-size: 12px; color: #555; line-height: 1.6; word-break: break-all; }
.nr-error { color: #f56565; }
.status-success { color: #67c23a; }
.status-failed { color: #f56565; }
.status-running { color: #409eff; }
.status-default { color: #999; }
.result-output {
  width: 100%; min-height: 100px; padding: 10px; border: 1px solid #d9d9d9; border-radius: 4px; font-size: 12px; color: #333; background: #fafafa; overflow-x: auto; white-space: pre-wrap; word-break: break-all;
}

/* node icons */
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
.icon-default { background-color: #6b7280; }
</style>
