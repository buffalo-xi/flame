<template>
  <div class="binding-config-panel">
    <div class="binding-panel-header">
      <button class="binding-back-btn" @click="handleBack">
        <el-icon><ArrowLeft /></el-icon>
        <span>返回</span>
      </button>
      <span class="binding-panel-title"> {{ varName }} <span class="binding-panel-type">({{ varType }}) 流式外发绑定</span></span>
    </div>
    
    <!-- Progress 通道 -->
    <div class="channel-section">
      <div class="channel-row">
        <span class="channel-label">进度输出（progress）</span>
        <el-switch v-model="progressEnabled" @change="handleChannelToggle('progress')" />
      </div>
      <div class="channel-hint">开启后该变量每次变化都逐条作为进度外发，不进入最终结果</div>
      <div v-if="progressEnabled" class="channel-config">
        <div class="channel-row">
          <span class="channel-label">类型</span>
          <el-select v-model="progressBinding.category" size="small" style="width: 170px">
            <el-option label="普通文本（text）" value="text" />
            <el-option label="思考（thinking）" value="thinking" />
          </el-select>
        </div>
        <div class="channel-hint">决定通知里的 _meta.category；选「思考」时客户端可折叠展示</div>
        <div class="transform-section">
          <div class="binding-field-row">
            <div class="binding-field binding-field-lang">
              <label>语言</label>
              <el-select v-model="progressBinding.transform.lang" placeholder="选择脚本语言" size="small">
                <el-option v-for="lang in bindingLanguages" :key="lang" :label="langLabel(lang)" :value="lang" />
              </el-select>
            </div>
            <div class="binding-field binding-field-timeout">
              <label>超时控制</label>
              <el-switch v-model="progressBinding.transform.timeout.enabled" :disabled="progressBinding.transform.lang === 'template'" />
              <el-input
                v-if="progressBinding.transform.timeout.enabled"
                type="number"
                v-model="progressBinding.transform.timeout.seconds"
                class="timeout-input"
                :min="1"
                :max="3600"
                placeholder="30"
              />
              <span v-if="progressBinding.transform.timeout.enabled" class="unit-text">秒</span>
            </div>
          </div>
          <div class="binding-field binding-field-script">
            <label>脚本</label>
            <textarea
              v-model="progressBinding.transform.script"
              class="script-textarea"
              rows="4"
            />
          </div>
          <div class="inputs-section">
            <div class="inputs-header">
              <span>输入变量</span>
              <button class="add-input-btn" @click="addInputParam(progressBinding)">
                <el-icon><Plus /></el-icon>
              </button>
            </div>
            <div class="binding-input-variables-list">
              <div v-for="(variable, name) in progressBinding.transform.inputs" :key="name" class="binding-input-variable-row">
                <input type="text" :value="name" class="binding-input-name" @blur="renameInputKey(progressBinding, name, $event.target.value)" placeholder="变量名" />
                <div class="binding-input-content">
                  <button class="show-content-btn binding-input-btn" @click="handleShowVariableSelect(name, $event, progressBinding)">
                    <template v-if="variable.source_key">
                      <span class="input-type-badge" :class="getTypeBadgeClass(variable.type)">{{ getTypeBadgeIcon(variable.type) }}</span>
                      <span class="input-var-key">{{ variable.source_key }}</span>
                    </template>
                    <template v-else>选择变量</template>
                  </button>
                  <button class="input-delete-btn" @click="removeInputParam(progressBinding, name)">
                    <el-icon><Delete /></el-icon>
                  </button>
                </div>
              </div>
              <div v-if="Object.keys(progressBinding.transform.inputs || {}).length === 0" class="inputs-empty">
                未配置输入变量（{{ varName }} 会自动注入）
              </div>
            </div>
          </div>
        </div>
      </div>
    </div>

    <!-- Result 通道 -->
    <div class="channel-section">
      <div class="channel-row">
        <span class="channel-label">结果输出（result）</span>
        <el-switch v-model="resultEnabled" @change="handleChannelToggle('result')" />
      </div>
      <div class="channel-hint">开启后该变量的变化会累积进最终结果（tool 返回值 / 运行记录）；关闭即解除该通道绑定</div>
      <div v-if="resultEnabled" class="channel-config">
        <div class="channel-row">
          <span class="channel-label">流式外发</span>
          <el-switch v-model="resultBinding.stream" size="small" />
        </div>
        <div class="channel-hint">关闭后只累积进最终结果，不逐条推送（等价于 result 节点关掉「随结果流外发」）</div>
        <div class="transform-section">
          <div class="binding-field-row">
            <div class="binding-field binding-field-lang">
              <label>语言</label>
              <el-select v-model="resultBinding.transform.lang" placeholder="选择脚本语言" size="small">
                <el-option v-for="lang in bindingLanguages" :key="lang" :label="langLabel(lang)" :value="lang" />
              </el-select>
            </div>
            <div class="binding-field binding-field-timeout">
              <label>超时控制</label>
              <el-switch v-model="resultBinding.transform.timeout.enabled" :disabled="resultBinding.transform.lang === 'template'" />
              <el-input
                v-if="resultBinding.transform.timeout.enabled"
                type="number"
                v-model="resultBinding.transform.timeout.seconds"
                class="timeout-input"
                :min="1"
                :max="3600"
                placeholder="30"
              />
              <span v-if="resultBinding.transform.timeout.enabled" class="unit-text">秒</span>
            </div>
          </div>
          <div class="binding-field binding-field-script">
            <label>脚本</label>
            <textarea
              v-model="resultBinding.transform.script"
              class="script-textarea"
              rows="4"
            />
          </div>
          <div class="inputs-section">
            <div class="inputs-header">
              <span>输入变量</span>
              <button class="add-input-btn" @click="addInputParam(resultBinding)">
                <el-icon><Plus /></el-icon>
              </button>
            </div>
            <div class="binding-input-variables-list">
              <div v-for="(variable, name) in resultBinding.transform.inputs" :key="name" class="binding-input-variable-row">
                <input type="text" :value="name" class="binding-input-name" @blur="renameInputKey(resultBinding, name, $event.target.value)" placeholder="变量名" />
                <div class="binding-input-content">
                  <button class="show-content-btn binding-input-btn" @click="handleShowVariableSelect(name, $event, resultBinding)">
                    <template v-if="variable.source_key">
                      <span class="input-type-badge" :class="getTypeBadgeClass(variable.type)">{{ getTypeBadgeIcon(variable.type) }}</span>
                      <span class="input-var-key">{{ variable.source_key }}</span>
                    </template>
                    <template v-else>选择变量</template>
                  </button>
                  <button class="input-delete-btn" @click="removeInputParam(resultBinding, name)">
                    <el-icon><Delete /></el-icon>
                  </button>
                </div>
              </div>
              <div v-if="Object.keys(resultBinding.transform.inputs || {}).length === 0" class="inputs-empty">
                未配置输入变量（{{ varName }} 会自动注入）
              </div>
            </div>
          </div>
        </div>
      </div>
    </div>
   
  </div>
</template>

<script setup>
import { ref, watch, inject, computed, nextTick } from 'vue'
import { Plus, Delete, ArrowLeft } from '@element-plus/icons-vue'

const props = defineProps({
  varName: { type: String, default: '' },
  varType: { type: String, default: '' },
  binding: { type: [Array, Object], default: null },
  selectedNode: { type: Object, default: null }
})

const emit = defineEmits(['back', 'update-binding'])

const showVariableSelectFn = inject('showVariableSelectForBinding', null)

const scriptLanguages = inject('scriptLanguages', ref(['lua']))
// 绑定预处理语言：template 在最前（默认）+ 后端允许的 worker 脚本池语言
const bindingLanguages = computed(() => {
  const langs = ['template']
  scriptLanguages.value.forEach(l => {
    if (!langs.includes(l)) langs.push(l)
  })
  return langs
})

// 语言显示友好名称
const langLabel = (lang) => {
  const map = { template: 'Template', lua: 'Lua', python: 'Python', js: 'JavaScript' }
  return map[lang] || lang
}

// 两个通道的开关状态
const resultEnabled = ref(false)
const progressEnabled = ref(false)

// 各通道的 binding 配置（内部编辑用）
const createEmptyBinding = () => ({
  stream: true,
  category: 'text', // 仅 progress 通道用：text | thinking
  transform: { lang: 'template', script: '', inputs: {}, timeout: { enabled: false, seconds: 30 } }
})

const normalizeBinding = (b) => {
  const t = b.transform?.timeout
  const timeout = (typeof t === 'object' && t !== null)
    ? { enabled: !!t.enabled, seconds: t.seconds || 30 }
    : { enabled: false, seconds: t || 30 }
  return {
    stream: b.stream !== false, // 缺省 true，兼容没有该字段的旧配置
    category: b.category || 'text', // 缺省 text，兼容没有该字段的旧配置
    transform: {
      lang: b.transform?.lang || 'template',
      script: b.transform?.script || '',
      inputs: b.transform?.inputs ? { ...b.transform.inputs } : {},
      timeout
    }
  }
}

const resultBinding = ref(createEmptyBinding())
const progressBinding = ref(createEmptyBinding())

const isLoading = ref(true)
const isSelfUpdate = ref(false) // 防止自身保存触发 loadFromProps 重置输入

const buildBindingOutput = (binding, channel) => {
  const result = { channel }
  // 只在显式关闭时落库；缺省（true）不写，保持旧配置干净
  if (channel === 'result' && binding.stream === false) {
    result.stream = false
  }
  // 只在不等于缺省（text）时落库，保持旧配置干净
  if (channel === 'progress' && binding.category && binding.category !== 'text') {
    result.category = binding.category
  }
  // 有脚本内容时才包含 transform
  if (binding.transform.script?.trim()) {
    result.transform = { ...binding.transform }
  }
  return result
}

// 从 props 加载数据
const loadFromProps = () => {
  if (isSelfUpdate.value) return // 自身保存触发的 props 变化，跳过
  isLoading.value = true
  const b = props.binding
  resultEnabled.value = false
  progressEnabled.value = false
  resultBinding.value = createEmptyBinding()
  progressBinding.value = createEmptyBinding()

  if (!b) {
    isLoading.value = false
    return
  }

  const items = Array.isArray(b) ? b : [b]
  for (const item of items) {
    const normalized = normalizeBinding(item)
    if (item.channel === 'result') {
      resultEnabled.value = true
      resultBinding.value = normalized
    } else if (item.channel === 'progress') {
      progressEnabled.value = true
      progressBinding.value = normalized
    }
  }
  isLoading.value = false
}

watch(() => props.binding, loadFromProps, { immediate: true })

// 自动计算当前 binding 输出
const currentBindingOutput = computed(() => {
  const bindings = []
  if (resultEnabled.value) {
    bindings.push(buildBindingOutput(resultBinding.value, 'result'))
  }
  if (progressEnabled.value) {
    bindings.push(buildBindingOutput(progressBinding.value, 'progress'))
  }
  return bindings.length === 0 ? null : bindings.length === 1 ? bindings[0] : bindings
})

const isPlaceholderScript = (script, varName) => {
  const stripped = script?.replace(/\s/g, '') || ''
  if (!stripped) return true
  const placeholders = [
    `{{.input.${varName}}}`,
    `return input.${varName}`,
    `return input["${varName}"]`,
    `return input.${varName}`
  ]
  return new Set(placeholders.map(p => p.replace(/\s/g, ''))).has(stripped)
}

// 语言切换为 template 时自动关闭超时，脚本为 placeholder 时自动填入新语言示例
watch(() => resultBinding.value.transform.lang, (lang) => {
  if (lang === 'template') {
    resultBinding.value.transform.timeout.enabled = false
  }
  if (isPlaceholderScript(resultBinding.value.transform.script, props.varName)) {
    resultBinding.value.transform.script = getScriptPlaceholder(lang)
  }
})

watch(() => progressBinding.value.transform.lang, (lang) => {
  if (lang === 'template') {
    progressBinding.value.transform.timeout.enabled = false
  }
  if (isPlaceholderScript(progressBinding.value.transform.script, props.varName)) {
    progressBinding.value.transform.script = getScriptPlaceholder(lang)
  }
})

// 自动保存：任何配置变化时实时 emit
watch(currentBindingOutput, (output) => {
  if (isLoading.value) return
  isSelfUpdate.value = true
  emit('update-binding', { varName: props.varName, binding: output })
  nextTick(() => { isSelfUpdate.value = false })
}, { deep: true })

const handleChannelToggle = (channel) => {
  if (channel === 'result' && resultEnabled.value && isPlaceholderScript(resultBinding.value.transform.script, props.varName)) {
    resultBinding.value.transform.script = getScriptPlaceholder(resultBinding.value.transform.lang)
  }
  if (channel === 'progress' && progressEnabled.value && isPlaceholderScript(progressBinding.value.transform.script, props.varName)) {
    progressBinding.value.transform.script = getScriptPlaceholder(progressBinding.value.transform.lang)
  }
}

const addInputParam = (binding) => {
  if (!binding.transform.inputs) {
    binding.transform.inputs = {}
  }
  let name = 'param_1'
  let counter = 1
  while (binding.transform.inputs[name] !== undefined) {
    counter++
    name = `param_${counter}`
  }
  binding.transform.inputs[name] = { source_type: '', source_key: '', source_ref: '', type: '' }
}

const removeInputParam = (binding, inputKey) => {
  delete binding.transform.inputs[inputKey]
}

const renameInputKey = (binding, oldKey, newKey) => {
  const trimmed = newKey.trim()
  if (!trimmed || trimmed === oldKey) return
  if (binding.transform.inputs[trimmed] !== undefined) return
  const entries = Object.entries(binding.transform.inputs)
  const newInputs = {}
  for (const [k, v] of entries) {
    newInputs[k === oldKey ? trimmed : k] = v
  }
  binding.transform.inputs = newInputs
}

const handleShowVariableSelect = (inputName, event, binding) => {
  if (!showVariableSelectFn) return
  const callback = (value) => {
    try {
      const data = JSON.parse(value)
      binding.transform.inputs[inputName] = {
        source_key: data.source_key,
        source_ref: data.source_ref,
        source_type: data.source_type,
        type: data.variableType || ''
      }
    } catch (e) {
      console.error('Failed to parse variable value:', e)
    }
  }
  showVariableSelectFn(event, inputName, callback)
}

const getTypeBadgeIcon = (type) => {
  const map = { string: 'S', number: 'N', boolean: 'B', object: 'O' }
  return map[type] || '?'
}

const getTypeBadgeClass = (type) => {
  const map = { string: 'type-string', number: 'type-number', boolean: 'type-boolean', object: 'type-object' }
  return map[type] || 'type-object'
}

const getScriptPlaceholder = (lang) => {
  switch (lang) {
    case 'template': return `{{.input.${props.varName}}}`
    case 'lua': return `return input.${props.varName}`
    case 'python': return `return input["${props.varName}"]`
    case 'js': return `return input.${props.varName}`
    default: return ''
  }
}

const handleBack = () => {
  emit('back')
}
</script>

<style scoped>
.binding-config-panel {
  display: flex;
  flex-direction: column;
}

.binding-panel-header {
  display: flex;
  align-items: center;
  gap: 8px;
  padding-top: 8px;
  padding-bottom: 10px;
  border-bottom: 1px solid #e8e8e8;
  margin-bottom: 12px;
}

.binding-back-btn {
  display: flex;
  align-items: center;
  gap: 2px;
  padding: 4px 8px;
  border: none;
  border-radius: 4px;
  font-size: 12px;
  color: #1890ff;
  background: transparent;
  cursor: pointer;
  transition: all 0.2s;
}

.binding-back-btn:hover {
  background: #e6f7ff;
}

.binding-panel-title {
  font-size: 13px;
  font-weight: 600;
  color: #333;
}

.binding-panel-type {
  font-weight: normal;
  color: #999;
  font-size: 12px;
}

/* 通道区域 */
.channel-section {
  border: 1px solid #e8e8e8;
  border-radius: 6px;
  padding: 10px;
  background: #fff;
  margin-bottom: 10px;
}

.channel-row {
  display: flex;
  justify-content: space-between;
  align-items: center;
}

.channel-label {
  font-size: 13px;
  font-weight: 600;
  color: #333;
}

/* 通道说明：把两个开关的语义差别写在明面上（外发 vs 累积） */
.channel-hint {
  margin-top: 4px;
  font-size: 12px;
  color: #999;
  line-height: 1.5;
}

.channel-config {
  margin-top: 10px;
  padding-top: 8px;
  border-top: 1px solid #f0f0f0;
}

.binding-field {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 6px;
}

.binding-field label {
  font-size: 12px;
  color: #666;
  width: 50px;
  flex-shrink: 0;
}

.binding-field :deep(.el-select) {
  flex: 1;
}

.binding-field :deep(.el-select .el-select__wrapper) {
  background-color: #eee;
  border: none;
  box-shadow: none;
}

.binding-field :deep(.el-select .el-select__wrapper:hover) {
  border: none;
  box-shadow: none;
}

.binding-field :deep(.el-select .el-select__wrapper.is-focus) {
  border: none;
  box-shadow: none;
}

.binding-field-row {
  display: flex;
  gap: 16px;
  margin-bottom: 6px;
}

.binding-field-lang {
  flex: 1;
  min-width: 0;
}

.binding-field-script {
  flex-direction: column;
  align-items: flex-start;
}

.binding-field-script label {
  margin-bottom: 4px;
}

.script-textarea {
  width: 100%;
  padding: 6px 8px;
  border: 1px solid #d9d9d9;
  border-radius: 4px;
  font-size: 12px;
  font-family: monospace;
  resize: vertical;
  outline: none;
  box-sizing: border-box;
  background: #eee;
}

.script-textarea:focus {
  border-color: #409eff;
}

.binding-field-timeout {
  display: flex;
  align-items: center;
  gap: 8px;
}

.binding-field-timeout .timeout-input {
  width: 64px;
  flex-shrink: 0;
}

.binding-field-timeout .timeout-input :deep(.el-input__wrapper) {
  background-color: #eee;
  border: none;
  box-shadow: none;
}

.binding-field-timeout .timeout-input :deep(.el-input__wrapper:hover) {
  border: none;
  box-shadow: none;
}

.binding-field-timeout .timeout-input :deep(.el-input__wrapper.is-focus) {
  border: none;
  box-shadow: none;
}

.binding-field-timeout .unit-text {
  font-size: 12px;
  color: #666;
  flex-shrink: 0;
}

.transform-section {
  margin-top: 6px;
  padding: 8px;
  background: #fafafa;
  border-radius: 4px;
  border: 1px solid #f0f0f0;
}

.inputs-section {
  margin-top: 8px;
}

.inputs-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 6px;
}

.inputs-header span {
  font-size: 12px;
  font-weight: 600;
  color: #666;
}

.add-input-btn {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 20px;
  height: 20px;
  border: none;
  border-radius: 4px;
  background: #1890ff;
  color: white;
  cursor: pointer;
  font-size: 12px;
  transition: background-color 0.2s;
}

.add-input-btn:hover {
  background: #40a9ff;
}

.binding-input-variables-list {
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.binding-input-variable-row {
  display: flex;
  align-items: center;
  gap: 6px;
}

.binding-input-name {
  flex: 1;
  min-width: 0;
  max-width: 30%;
  padding: 8px 8px;
  border: 0px solid #d9d9d9;
  background-color: #eeeeee;
  border-radius: 4px;
  font-size: 12px;
  outline: none;
}

.binding-input-name:focus { outline: none; }
.binding-input-name::placeholder { color: #bbb; }

.binding-input-content {
  flex: 2;
  display: flex;
  align-items: center;
  gap: 6px;
}

.show-content-btn {
  flex: 1;
  min-width: 0;
  padding: 8px 10px;
  border: 0px solid #1890ff;
  border-radius: 4px;
  background: #eee;
  color: #1890ff;
  font-size: 12px;
  cursor: pointer;
  transition: all 0.2s;
  text-align: left;
}

.show-content-btn:hover {
  background: #e6f7ff;
}

.binding-input-btn {
  display: flex;
  align-items: center;
  gap: 6px;
  overflow: hidden;
}

.binding-input-btn .input-var-key {
  font-size: 12px;
  color: #333;
  flex-shrink: 0;
}

.input-type-badge {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 16px;
  height: 16px;
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

.inputs-empty {
  font-size: 11px;
  color: #bbb;
  padding: 4px 0;
}
</style>
