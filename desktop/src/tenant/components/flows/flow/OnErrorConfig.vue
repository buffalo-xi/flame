<template>
  <div class="on-error-config" v-if="availableActions.length > 0 || supportsPost">
    <div class="section-header" @click="toggleExpanded">
      <span class="section-title-text">{{ t('flow.onError.title') }}</span>
      <span class="expand-icon">{{ expanded ? '▼' : '▶' }}</span>
    </div>

    <div v-if="expanded" class="section-body">
      <!-- 重试 -->
      <div class="error-section" v-if="canUseAction('retry')">
        <div class="section-row">
          <el-switch v-model="retryEnabled" @change="handleRetryEnabledChange" />
          <label class="field-label">{{ t('flow.onError.retryToggle') }}</label>
          <button v-if="retryEnabled && retryCodes.length < availableErrorCodes.length"
                  class="action-link-btn" @click="handleSelectAllCodes('retry')">{{ t('flow.onError.selectAll') }}</button>
          <button v-if="retryEnabled && retryCodes.length > 0"
                  class="action-link-btn" @click="handleDeselectAllCodes('retry')">{{ t('flow.onError.deselectAll') }}</button>
        </div>
        <div v-if="retryEnabled" class="section-content">
          <div class="error-codes">
            <label class="field-label">{{ t('flow.onError.errorCodes') }}</label>
            <div class="checkbox-group">
              <el-checkbox
                v-for="code in availableErrorCodes"
                :key="'retry-' + code"
                :model-value="retryCodes.includes(code)"
                @change="(val) => handleCodeToggle('retry', code, val)"
              >{{ code }}</el-checkbox>
            </div>
          </div>
          <div v-if="retryCodes.includes('other')" class="patterns-section">
            <label class="field-label">
              {{ t('flow.onError.patterns') }}
              <span class="hint-text">{{ t('flow.onError.patternHint') }}</span>
            </label>
            <div class="patterns-input">
              <el-tag
                v-for="(pattern, index) in retryPatterns"
                :key="index"
                closable
                @close="removePattern('retry', index)"
                class="pattern-tag"
              >{{ pattern }}</el-tag>
              <input
                type="text"
                class="pattern-input"
                v-model="retryPatternInput"
                @keydown.enter="addPattern('retry')"
                :placeholder="t('flow.onError.patternPlaceholder')"
              />
            </div>
          </div>
          <div class="retry-options">
            <div class="option-item">
              <label class="field-label">{{ t('flow.onError.maxRetries') }}</label>
              <el-input
                type="number"
                v-model.number="retryMax"
                class="small-input"
                @blur="saveConfig"
                :min="1"
                :max="10"
              />
            </div>
            <div class="option-item">
              <label class="field-label">{{ t('flow.onError.delayMs') }}</label>
              <el-input
                type="number"
                v-model.number="retryDelayMs"
                class="small-input"
                @blur="saveConfig"
                :min="0"
                :max="60000"
              />
            </div>
          </div>
        </div>
      </div>

      <!-- 跳过 -->
      <div class="error-section" v-if="canUseAction('skip')">
        <div class="section-row">
          <el-switch v-model="skipEnabled" @change="handleSkipEnabledChange" />
          <label class="field-label">{{ t('flow.onError.skipToggle') }}</label>
          <button v-if="skipEnabled && skipCodes.length < availableErrorCodes.length"
                  class="action-link-btn" @click="handleSelectAllCodes('skip')">{{ t('flow.onError.selectAll') }}</button>
          <button v-if="skipEnabled && skipCodes.length > 0"
                  class="action-link-btn" @click="handleDeselectAllCodes('skip')">{{ t('flow.onError.deselectAll') }}</button>
        </div>
        <div v-if="skipEnabled" class="section-content">
          <div class="error-codes">
            <label class="field-label">{{ t('flow.onError.errorCodes') }}</label>
            <div class="checkbox-group">
              <el-checkbox
                v-for="code in availableErrorCodes"
                :key="'skip-' + code"
                :model-value="skipCodes.includes(code)"
                @change="(val) => handleCodeToggle('skip', code, val)"
              >{{ code }}</el-checkbox>
            </div>
          </div>
          <div v-if="skipCodes.includes('other')" class="patterns-section">
            <label class="field-label">
              {{ t('flow.onError.patterns') }}
              <span class="hint-text">{{ t('flow.onError.patternHint') }}</span>
            </label>
            <div class="patterns-input">
              <el-tag
                v-for="(pattern, index) in skipPatterns"
                :key="index"
                closable
                @close="removePattern('skip', index)"
                class="pattern-tag"
              >{{ pattern }}</el-tag>
              <input
                type="text"
                class="pattern-input"
                v-model="skipPatternInput"
                @keydown.enter="addPattern('skip')"
                :placeholder="t('flow.onError.patternPlaceholder')"
              />
            </div>
          </div>
        </div>
      </div>

      <!-- 处理脚本 -->
      <div class="error-section" v-if="canUseAction('handler')">
        <div class="section-row">
          <el-switch v-model="handlerEnabled" @change="handleHandlerEnabledChange" />
          <label class="field-label">{{ t('flow.onError.handlerToggle') }}</label>
          <button v-if="handlerEnabled && handlerCodes.length < availableErrorCodes.length"
                  class="action-link-btn" @click="handleSelectAllCodes('handler')">{{ t('flow.onError.selectAll') }}</button>
          <button v-if="handlerEnabled && handlerCodes.length > 0"
                  class="action-link-btn" @click="handleDeselectAllCodes('handler')">{{ t('flow.onError.deselectAll') }}</button>
        </div>
        <div v-if="handlerEnabled" class="section-content">
          <div class="error-codes">
            <label class="field-label">{{ t('flow.onError.errorCodes') }}</label>
            <div class="checkbox-group">
              <el-checkbox
                v-for="code in availableErrorCodes"
                :key="'handler-' + code"
                :model-value="handlerCodes.includes(code)"
                @change="(val) => handleCodeToggle('handler', code, val)"
              >{{ code }}</el-checkbox>
            </div>
          </div>
          <div v-if="handlerCodes.includes('other')" class="patterns-section">
            <label class="field-label">
              {{ t('flow.onError.patterns') }}
              <span class="hint-text">{{ t('flow.onError.handlerPatternHint') }}</span>
            </label>
            <div class="patterns-input">
              <el-tag
                v-for="(pattern, index) in handlerPatterns"
                :key="index"
                closable
                @close="removePattern('handler', index)"
                class="pattern-tag"
              >{{ pattern }}</el-tag>
              <input
                type="text"
                class="pattern-input"
                v-model="handlerPatternInput"
                @keydown.enter="addPattern('handler')"
                :placeholder="t('flow.onError.patternPlaceholder')"
              />
            </div>
          </div>
          <div class="handler-options">
            <div class="option-item">
              <label class="field-label">{{ t('flow.onError.language') }}</label>
              <el-select v-model="handlerLang" class="small-select" @change="saveConfig">
                <el-option v-for="lang in handlerLanguages" :key="lang" :label="lang" :value="lang" />
              </el-select>
            </div>
          </div>
          <div class="script-section">
            <textarea
              class="script-textarea"
              v-model="handlerBody"
              @blur="saveConfig"
              @keydown="handleScriptKeydown"
              placeholder="if error_code == 'other' then&#10;  return 'skip'&#10;else&#10;  return 'fail'&#10;end"
            ></textarea>
            <div class="script-hints">
              <span>{{ t('flow.onError.availableVars') }}</span>
              <span>{{ t('flow.onError.returnValues') }}</span>
            </div>
          </div>
        </div>
      </div>

      <!-- 后处理 -->
      <div class="error-section" v-if="supportsPost">
        <div class="section-row">
          <el-switch v-model="postEnabled" @change="handlePostEnabledChange" />
          <label class="field-label">{{ t('flow.onError.postToggle') }}</label>
        </div>
        <div v-if="postEnabled" class="section-content">
          <div class="post-outputs">
            <div class="post-outputs-header">
              <label class="field-label">{{ t('flow.onError.setOutputs') }}</label>
              <button v-if="getAvailableOutputVarsForSelect('').length > 0" class="add-btn" @click="addPostOutput">{{ t('flow.onError.addVar') }}</button>
            </div>
            <div v-if="availableOutputVars.length === 0" class="empty-hint">
              {{ t('flow.onError.noOutputVars') }}
            </div>
            <div
              v-for="(item, index) in postOutputItems"
              :key="index"
              class="post-output-row"
            >
              <el-select
                v-model="item.name"
                class="output-var-select"
                :placeholder="t('flow.onError.selectVar')"
                @change="() => handlePostOutputNameChange(index)"
              >
                <el-option
                  v-for="v in getAvailableOutputVarsForSelect(item.name)"
                  :key="v"
                  :label="v"
                  :value="v"
                />
              </el-select>
              <span class="equals-sign">=</span>
              <el-input
                v-model="item.value"
                class="output-val-input"
                :placeholder="t('flow.onError.value')"
                @blur="saveConfig"
              />
              <button class="remove-btn" @click="removePostOutput(index)">×</button>
            </div>
          </div>

          <div class="post-script-section">
            <div class="section-row">
              <el-checkbox v-model="postScriptEnabled" @change="handlePostScriptEnabledChange">{{ t('flow.onError.script') }}</el-checkbox>
            </div>
            <div v-if="postScriptEnabled" class="script-section">
              <div class="option-item">
                <label class="field-label">{{ t('flow.onError.language') }}</label>
                <el-select v-model="postScriptLang" class="small-select" @change="saveConfig">
                  <el-option v-for="lang in handlerLanguages" :key="lang" :label="lang" :value="lang" />
                </el-select>
              </div>
              <textarea
                class="script-textarea"
                v-model="postScriptBody"
                @blur="saveConfig"
                @keydown="handleScriptKeydown"
                placeholder="return { error_summary = error_message }"
              ></textarea>
            </div>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref, watch, computed, onMounted, nextTick, inject } from 'vue'
import { ElCheckbox, ElInput, ElSelect, ElOption, ElTag, ElSwitch } from 'element-plus'
import { apiBase } from '@/tenant/config/base.js'
import { getAuthHeader } from '@/tenant/utils/auth.js'
import { useI18n } from 'vue-i18n'

const { t } = useI18n()

const props = defineProps({
  selectedNode: {
    type: Object,
    default: null
  }
})

const emit = defineEmits(['update-node-data'])
const scriptLanguages = inject('scriptLanguages', ref(['lua']))
const defaultLang = computed(() => scriptLanguages.value[0] || 'lua')
// handler/post 语言：排除 template（需副作用回调，template 纯渲染）
const handlerLanguages = computed(() => scriptLanguages.value.filter(l => l !== 'template'))

const manualExpanded = ref(false)

// 有任何子项启用时，强制展开不允许折叠
const hasActiveConfig = computed(() => {
  return retryEnabled.value || skipEnabled.value || handlerEnabled.value || postEnabled.value
})

const expanded = computed(() => {
  return hasActiveConfig.value || manualExpanded.value
})

const toggleExpanded = () => {
  if (hasActiveConfig.value) return // 有配置时不允许折叠
  manualExpanded.value = !manualExpanded.value
}

// Capabilities
const nodeCapabilities = ref({})
const capabilitiesLoaded = ref(false)
const globalErrorCodes = ref([])
const availableErrorCodes = computed(() => {
  if (!props.selectedNode) {
    return globalErrorCodes.value.filter(c => c !== 'internal' && c !== 'config')
  }
  const caps = nodeCapabilities.value[props.selectedNode.type]
  if (caps && caps.error_codes && caps.error_codes.length > 0) {
    return caps.error_codes
  }
  return globalErrorCodes.value.filter(c => c !== 'internal' && c !== 'config')
})
const availableActions = computed(() => {
  if (!props.selectedNode) return []
  const caps = nodeCapabilities.value[props.selectedNode.type]
  return caps ? caps.actions || [] : []
})

const canUseAction = (action) => {
  return availableActions.value.includes(action)
}

const supportsPost = computed(() => {
  if (!props.selectedNode) return false
  const caps = nodeCapabilities.value[props.selectedNode.type]
  return caps ? !!caps.has_post : false
})

// Fetch capabilities
onMounted(async () => {
  try {
    const resp = await fetch(`${apiBase}/admin/flow/node-capabilities`, {
      headers: getAuthHeader()
    })
    if (resp.ok) {
      const res = await resp.json()
      const data = res && res.data ? res.data : {}
      globalErrorCodes.value = data.all_error_codes || []
      delete data.all_error_codes
      // Not node types: these two are rule tables owned by their own consumers (the raise picker, the concurrency
      // checkbox), and leaving them here would only invite a lookup against a type that does not exist.
      delete data.raisable_codes
      delete data.non_member_types
      nodeCapabilities.value = data
    }
  } catch (e) {
    console.error('Failed to fetch node capabilities:', e)
  } finally {
    capabilitiesLoaded.value = true
  }
})

// Retry state
const retryEnabled = ref(false)
const retryCodes = ref([])
const retryPatterns = ref([])
const retryMax = ref(3)
const retryDelayMs = ref(1000)
const retryPatternInput = ref('')

// Skip state
const skipEnabled = ref(false)
const skipCodes = ref([])
const skipPatterns = ref([])
const skipPatternInput = ref('')

// Handler state
const handlerEnabled = ref(false)
const handlerCodes = ref([])
const handlerPatterns = ref([])
const handlerLang = ref('lua')
const handlerBody = ref('')
const handlerPatternInput = ref('')

// Post state
const postEnabled = ref(false)
const postOutputItems = ref([])
const postScriptEnabled = ref(false)
const postScriptLang = ref('lua')
const postScriptBody = ref('')

// Available output variables from node config
const availableOutputVars = computed(() => {
  if (!props.selectedNode?.data?.config?.outputs) return []
  return Object.keys(props.selectedNode.data.config.outputs)
})

const getAvailableOutputVarsForSelect = (currentName) => {
  const used = postOutputItems.value
    .filter(item => item.name && item.name !== currentName)
    .map(item => item.name)
  return availableOutputVars.value.filter(v => !used.includes(v))
}

const resetAll = () => {
  manualExpanded.value = false
  retryEnabled.value = false
  retryCodes.value = []
  retryPatterns.value = []
  retryMax.value = 3
  retryDelayMs.value = 1000
  retryPatternInput.value = ''
  skipEnabled.value = false
  skipCodes.value = []
  skipPatterns.value = []
  skipPatternInput.value = ''
  handlerEnabled.value = false
  handlerCodes.value = []
  handlerPatterns.value = []
  handlerLang.value = defaultLang.value
  handlerBody.value = ''
  handlerPatternInput.value = ''
  postEnabled.value = false
  postOutputItems.value = []
  postScriptEnabled.value = false
  postScriptLang.value = defaultLang.value
  postScriptBody.value = ''
}

// Load config from node
watch(() => props.selectedNode, (node) => {
  if (!node?.data?.config?.on_error) {
    resetAll()
    return
  }
  const cfg = node.data.config.on_error

  // Retry
  if (cfg.retry) {
    retryEnabled.value = true
    retryCodes.value = cfg.retry.codes || []
    retryPatterns.value = cfg.retry.patterns || []
    retryMax.value = cfg.retry.max || 3
    retryDelayMs.value = cfg.retry.delay_ms || 1000
  } else {
    retryEnabled.value = false
    retryCodes.value = []
    retryPatterns.value = []
    retryMax.value = 3
    retryDelayMs.value = 1000
  }

  // Skip
  if (cfg.skip) {
    skipEnabled.value = true
    skipCodes.value = cfg.skip.codes || []
    skipPatterns.value = cfg.skip.patterns || []
  } else {
    skipEnabled.value = false
    skipCodes.value = []
    skipPatterns.value = []
  }

  // Handler
  if (cfg.handler) {
    handlerEnabled.value = true
    handlerCodes.value = cfg.handler.codes || []
    handlerPatterns.value = cfg.handler.patterns || []
    handlerLang.value = cfg.handler.lang || defaultLang.value
    handlerBody.value = cfg.handler.script || ''
  } else {
    handlerEnabled.value = false
    handlerCodes.value = []
    handlerPatterns.value = []
    handlerLang.value = defaultLang.value
    handlerBody.value = ''
  }

  // Post
  if (cfg.post) {
    postEnabled.value = true
    const outputs = cfg.post.set_outputs || {}
    postOutputItems.value = Object.entries(outputs).map(([name, value]) => ({
      name,
      value: String(value)
    }))
    if (cfg.post?.handler) {
      postScriptEnabled.value = true
      postScriptLang.value = cfg.post.handler.lang || defaultLang.value
      postScriptBody.value = cfg.post.handler.script || ''
    } else {
      postScriptEnabled.value = false
      postScriptLang.value = defaultLang.value
      postScriptBody.value = ''
    }
  } else {
    postEnabled.value = false
    postOutputItems.value = []
    postScriptEnabled.value = false
    postScriptLang.value = defaultLang.value
    postScriptBody.value = ''
  }

  retryPatternInput.value = ''
  skipPatternInput.value = ''
  handlerPatternInput.value = ''
}, { immediate: true })

// 节点既不支持动作也不支持后处理时，清理残留的 on_error 配置（等 capabilities 加载完成后生效，避免初始 race condition）
watch([availableActions, supportsPost], () => {
  if (!capabilitiesLoaded.value || !props.selectedNode) return
  if (availableActions.value.length === 0 && !supportsPost.value && props.selectedNode?.data?.config?.on_error) {
    resetAll()
    emit('update-node-data', {
      nodeId: props.selectedNode.id,
      data: { config: { on_error: undefined } }
    })
  }
})

// Handlers
const handleRetryEnabledChange = () => {
  if (!retryEnabled.value) {
    retryCodes.value = []
    retryPatterns.value = []
  }
  saveConfig()
}

const handleSkipEnabledChange = () => {
  if (!skipEnabled.value) {
    skipCodes.value = []
    skipPatterns.value = []
  }
  saveConfig()
}

const handleHandlerEnabledChange = () => {
  if (!handlerEnabled.value) {
    handlerCodes.value = []
    handlerPatterns.value = []
    handlerBody.value = ''
  }
  saveConfig()
}

const handlePostEnabledChange = () => {
  if (!postEnabled.value) {
    postOutputItems.value = []
    postScriptEnabled.value = false
    postScriptBody.value = ''
  }
  saveConfig()
}

const handlePostScriptEnabledChange = () => {
  if (!postScriptEnabled.value) {
    postScriptBody.value = ''
  }
  saveConfig()
}

const handleCodeToggle = (section, code, checked) => {
  const codesMap = { retry: retryCodes, skip: skipCodes, handler: handlerCodes }
  const codes = codesMap[section]
  if (checked) {
    if (!codes.value.includes(code)) {
      codes.value.push(code)
    }
  } else {
    codes.value = codes.value.filter(c => c !== code)
  }
  saveConfig()
}

const handleSelectAllCodes = (section) => {
  const codesMap = { retry: retryCodes, skip: skipCodes, handler: handlerCodes }
  codesMap[section].value = [...availableErrorCodes.value]
  saveConfig()
}

const handleDeselectAllCodes = (section) => {
  const codesMap = { retry: retryCodes, skip: skipCodes, handler: handlerCodes }
  codesMap[section].value = []
  saveConfig()
}

const addPattern = (section) => {
  const inputMap = { retry: retryPatternInput, skip: skipPatternInput, handler: handlerPatternInput }
  const patternsMap = { retry: retryPatterns, skip: skipPatterns, handler: handlerPatterns }
  const input = inputMap[section]
  const patterns = patternsMap[section]
  const val = input.value.trim()
  if (val && !patterns.value.includes(val)) {
    patterns.value.push(val)
  }
  input.value = ''
  saveConfig()
}

const removePattern = (section, index) => {
  const patternsMap = { retry: retryPatterns, skip: skipPatterns, handler: handlerPatterns }
  patternsMap[section].value.splice(index, 1)
  saveConfig()
}

const addPostOutput = () => {
  const available = getAvailableOutputVarsForSelect('')
  if (available.length > 0) {
    postOutputItems.value.push({ name: available[0], value: '' })
    saveConfig()
  }
}

const removePostOutput = (index) => {
  postOutputItems.value.splice(index, 1)
  saveConfig()
}

const handlePostOutputNameChange = (index) => {
  saveConfig()
}

const handleScriptKeydown = (event) => {
  if (event.key === 'Tab') {
    event.preventDefault()
    const textarea = event.target
    const start = textarea.selectionStart
    const end = textarea.selectionEnd
    textarea.value = textarea.value.substring(0, start) + '   ' + textarea.value.substring(end)
    nextTick(() => {
      textarea.selectionStart = textarea.selectionEnd = start + 3
    })
  }
}

// Save config to node
const saveConfig = () => {
  if (!props.selectedNode) return

  const onError = {}

  if (retryEnabled.value) {
    const retry = {}
    if (retryCodes.value.length > 0) retry.codes = [...retryCodes.value]
    if (!retryCodes.value.includes('other')) {
      retryPatterns.value = []
    }
    if (retryPatterns.value.length > 0) retry.patterns = [...retryPatterns.value]
    retry.max = retryMax.value
    retry.delay_ms = retryDelayMs.value
    onError.retry = retry
  }

  if (skipEnabled.value) {
    const skip = {}
    if (skipCodes.value.length > 0) skip.codes = [...skipCodes.value]
    if (!skipCodes.value.includes('other')) {
      skipPatterns.value = []
    }
    if (skipPatterns.value.length > 0) skip.patterns = [...skipPatterns.value]
    onError.skip = skip
  }

  if (handlerEnabled.value) {
    const handler = {}
    if (handlerCodes.value.length > 0) handler.codes = [...handlerCodes.value]
    if (!handlerCodes.value.includes('other')) {
      handlerPatterns.value = []
    }
    if (handlerPatterns.value.length > 0) handler.patterns = [...handlerPatterns.value]
    handler.lang = handlerLang.value
    handler.script = handlerBody.value
    onError.handler = handler
  }

  if (postEnabled.value) {
    const post = {}
    if (postOutputItems.value.length > 0) {
      const setOutputs = {}
      postOutputItems.value.forEach(item => {
        if (item.name) {
          let val = item.value
          // Try to parse as number or boolean
          if (val === 'true') val = true
          else if (val === 'false') val = false
          else if (!isNaN(val) && val !== '') val = Number(val)
          setOutputs[item.name] = val
        }
      })
      if (Object.keys(setOutputs).length > 0) {
        post.set_outputs = setOutputs
      }
    }
    if (postScriptEnabled.value) {
      post.handler = {
        lang: postScriptLang.value,
        script: postScriptBody.value
      }
    }
    onError.post = post
  }

  const hasConfig = Object.keys(onError).length > 0

  emit('update-node-data', {
    nodeId: props.selectedNode.id,
    data: {
      config: {
        on_error: hasConfig ? onError : undefined
      }
    }
  })
}
</script>

<style scoped>
.on-error-config {
  background: #fafafa;
  border-radius: 8px;
  margin-top: 8px;
  overflow: hidden;
}

.section-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 10px 6px;
  cursor: pointer;
  user-select: none;
  border-bottom: 1px solid transparent;
  transition: background-color 0.2s;
}

.section-header:hover {
  background: #f0f0f0;
}

.section-title-text {
  font-size: 13px;
  font-weight: 600;
  color: #00704A;
}

.expand-icon {
  font-size: 10px;
  color: #999;
}

.section-body {
  padding: 8px 12px 12px;
  border-top: 1px solid #e8e8e8;
}

.error-section {
  margin-bottom: 10px;
  padding-bottom: 10px;
  border-bottom: 1px solid #f0f0f0;
}

.error-section:last-child {
  margin-bottom: 0;
  padding-bottom: 0;
  border-bottom: none;
}

.section-row {
  display: flex;
  align-items: center;
  gap: 8px;
}

.section-row :deep(.el-switch) {
  height: 18px;
}

.section-row :deep(.el-switch__core) {
  height: 18px;
  min-width: 32px;
  border-radius: 9px;
}

.section-row :deep(.el-switch__core .el-switch__action) {
  width: 14px;
  height: 14px;
}

.section-content {
  margin-top: 8px;
  padding-left: 4px;
}

.field-label {
  font-size: 12px;
  color: #666;
  font-weight: 500;
  display: block;
  margin-bottom: 4px;
  padding-top: 3px;
}

.section-row > .field-label {
  margin-bottom: 0;
  padding-top: 0;
}

.error-codes {
  display: flex;
  align-items: center;
  gap: 8px;
}

.error-codes > .field-label {
  display: inline-block;
  margin-bottom: 0;
  white-space: nowrap;
  line-height: 24px;
  flex-shrink: 0;
}

.hint-text {
  font-size: 11px;
  color: #999;
  font-weight: 400;
}

.checkbox-group {
  display: flex;
  flex-wrap: wrap;
  gap: 4px 12px;
}

.checkbox-group :deep(.el-checkbox) {
  margin-right: 0;
}

.checkbox-group :deep(.el-checkbox__label) {
  font-size: 12px;
}

.checkbox-actions {
  display: flex;
  gap: 8px;
  margin-top: 2px;
}

.action-link-btn {
  border: none;
  background: transparent;
  color: #00704a;
  font-size: 11px;
  cursor: pointer;
  padding: 0;
  line-height: 1;
  padding-top: 3px;
}

.action-link-btn:hover {
  color: #009966;
  text-decoration: underline;
}

.patterns-section {
  margin-top: 8px;
}

.patterns-input {
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
  align-items: center;
  padding: 4px 8px;
  border: 1px solid #dcdfe6;
  border-radius: 4px;
  background: #fff;
  min-height: 32px;
}

.pattern-tag {
  font-size: 11px;
}

.pattern-input {
  border: none;
  outline: none;
  font-size: 12px;
  flex: 1;
  min-width: 80px;
  padding: 2px 4px;
  background: transparent;
}

.pattern-input::placeholder {
  color: #c0c4cc;
}

.retry-options, .handler-options {
  display: flex;
  gap: 16px;
  margin-top: 8px;
}

.option-item {
  display: flex;
  align-items: center;
  gap: 6px;
}

.option-item .field-label {
  margin-bottom: 0;
  white-space: nowrap;
}

.small-input {
  width: 80px;
}

.small-input :deep(.el-input__wrapper) {
  background-color: #eee;
  border: none;
  box-shadow: none;
}

.small-select {
  width: 100px;
}

.small-select :deep(.el-select__wrapper) {
  background-color: #eee;
  border: none;
  box-shadow: none;
}

.script-section {
  margin-top: 8px;
}

.script-textarea {
  width: 100%;
  min-height: 120px;
  padding: 8px 10px;
  border: 1px solid #d9d9d9;
  border-radius: 4px;
  font-size: 12px;
  font-family: 'Monaco', 'Menlo', monospace;
  line-height: 1.6;
  resize: vertical;
  outline: none;
  box-sizing: border-box;
  background-color: #f5f5f5;
}

.script-textarea:focus {
  border-color: #91caff;
}

.script-textarea::placeholder {
  color: #bbb;
}

.script-hints {
  display: flex;
  flex-direction: column;
  gap: 2px;
  margin-top: 4px;
}

.script-hints span {
  font-size: 11px;
  color: #999;
}

.post-outputs {
  margin-bottom: 8px;
}

.post-outputs-header {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 6px;
}

.post-outputs-header .field-label {
  margin-bottom: 0;
}

.post-output-row {
  display: flex;
  align-items: center;
  gap: 6px;
  margin-bottom: 6px;
}

.output-var-select {
  width: 120px;
}

.output-var-select :deep(.el-select__wrapper) {
  background-color: #eee;
  border: none;
  box-shadow: none;
}

.equals-sign {
  color: #999;
  font-size: 14px;
}

.output-val-input {
  flex: 1;
}

.output-val-input :deep(.el-input__wrapper) {
  background-color: #eee;
  border: none;
  box-shadow: none;
}

.remove-btn {
  width: 20px;
  height: 20px;
  border: none;
  border-radius: 3px;
  background: transparent;
  color: #999;
  cursor: pointer;
  font-size: 14px;
  display: flex;
  align-items: center;
  justify-content: center;
}

.remove-btn:hover {
  background: #fff5f5;
  color: #f56c6c;
}

.add-post-output {
  margin-top: 4px;
}

.add-btn {
  padding: 4px 10px;
  border: 1px dashed #d9d9d9;
  border-radius: 4px;
  background: transparent;
  color: #666;
  font-size: 12px;
  cursor: pointer;
}

.add-btn:hover {
  border-color: #1890ff;
  color: #1890ff;
}

.empty-hint {
  font-size: 12px;
  color: #999;
  padding: 4px 0;
}

.post-script-section {
  margin-top: 8px;
}
</style>
