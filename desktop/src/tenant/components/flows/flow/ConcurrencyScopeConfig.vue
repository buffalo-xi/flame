<template>
  <div class="props-section">
    <div class="section-title">
      <span>{{ t('flow.nodeProps.scopeSection') }}</span>
    </div>

    <div class="prop-group-horizontal">
      <div class="prop-item">
        <label class="prop-label">{{ t('flow.nodeProps.maxConcurrency') }}</label>
        <el-switch v-model="limitEnabled" @change="handleLimitToggle" />
        <el-input
          v-if="limitEnabled"
          v-model="limitDraft"
          type="number"
          class="prop-input"
          :min="1"
          :max="ceiling"
          @change="handleLimitChange"
          @blur="handleLimitChange"
        />
        <span v-else class="scope-unlimited">{{ t('flow.nodeProps.scopeUnlimited', { max: ceiling }) }}</span>
      </div>
    </div>

    <div class="prop-group-horizontal">
      <div class="prop-item">
        <label class="prop-label">{{ t('flow.nodeProps.scopeTimeout') }}</label>
        <el-switch v-model="timeoutEnabled" @change="handleTimeoutToggle" />
        <el-input
          v-if="timeoutEnabled"
          v-model="timeoutDraft"
          type="number"
          class="prop-input"
          :min="1"
          :max="3600"
          placeholder="30"
          @change="handleTimeoutChange"
          @blur="handleTimeoutChange"
        />
        <span v-else class="scope-unlimited">{{ t('flow.nodeProps.scopeNoTimeout') }}</span>
      </div>
      <div class="prop-item">
        <label class="prop-label">{{ t('flow.nodeProps.joinMode') }}</label>
        <el-select v-model="joinMode" class="prop-select" @change="persist">
          <el-option :label="t('flow.nodeProps.joinCollect')" value="collect" />
          <el-option :label="t('flow.nodeProps.joinDrain')" value="drain" />
          <el-option :label="t('flow.nodeProps.joinAbort')" value="abort" />
        </el-select>
      </div>
    </div>

    <p class="scope-hint">{{ t('flow.nodeProps.scopeHint', { max: ceiling }) }}</p>
  </div>
</template>

<script setup>
import { computed, inject, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'

const { t } = useI18n()

// The tenant ceiling, provided by the editor (GET /admin/flow/limits). The input is bounded by it so the author cannot
// type a number the runtime would silently lower; when the system value is missing the built-in fallback is shown, which
// matches the server's own fallback.
const DEFAULT_CEILING = 16
const systemLimits = inject('systemLimits', null)
const ceiling = computed(() => {
  const raw = Number(systemLimits?.value?.max_concurrency)
  return Number.isFinite(raw) && raw > 0 ? Math.floor(raw) : DEFAULT_CEILING
})

const props = defineProps({
  selectedNode: {
    type: Object,
    default: null
  }
})

const emit = defineEmits(['update-node-data'])

// limitEnabled is the switch: off means "no author limit" (the runtime still applies the system ceiling), on means a
// number is written to config.parallel.max_concurrency. "Unlimited" is the absence of the key, so the switch state is
// derived from whether the key exists rather than stored as a second flag that could disagree with it.
const limitEnabled = ref(false)
const limitDraft = ref(8)
const joinMode = ref('collect')
// Same rule for the deadline: the {enabled, seconds} block the other nodes use, where "off" is no key at all.
const timeoutEnabled = ref(false)
const timeoutDraft = ref(30)

// 8 matches what a new process node is created with, so the panel never shows a number the node does not have.
const DEFAULT_LIMIT = 8
const DEFAULT_TIMEOUT = 30
const MAX_TIMEOUT = 3600

// Read the host block once per selection. A body without config.parallel is still a scope, so the controls show the
// defaults (no author limit, collect, no deadline) instead of an empty panel.
watch(() => props.selectedNode, (node) => {
  const parallel = node?.data?.config?.parallel || {}
  const max = Number(parallel.max_concurrency)
  const limited = Number.isFinite(max) && max > 0
  limitEnabled.value = limited
  limitDraft.value = limited ? Math.floor(max) : DEFAULT_LIMIT
  joinMode.value = parallel.join === 'drain' ? 'drain' : parallel.join === 'abort' ? 'abort' : 'collect'

  const timeout = parallel.timeout || {}
  const seconds = Number(timeout.seconds)
  const hasTimeout = timeout.enabled === true && Number.isFinite(seconds) && seconds > 0
  timeoutEnabled.value = hasTimeout
  timeoutDraft.value = hasTimeout ? Math.floor(seconds) : DEFAULT_TIMEOUT
}, { immediate: true })

const handleLimitToggle = () => {
  // Off discards the number, on starts from the default rather than from whatever was typed before: the two states
  // mean "no limit" and "a limit", and a stale number left in the box would make the switch look like it does nothing.
  if (!limitEnabled.value) {
    limitDraft.value = DEFAULT_LIMIT
  }
  persist()
}

const handleLimitChange = () => {
  let value = Math.floor(Number(limitDraft.value))
  if (!Number.isFinite(value) || value < 1) {
    value = 1
  }
  if (value > ceiling.value) {
    value = ceiling.value
  }
  limitDraft.value = value
  persist()
}

const handleTimeoutToggle = () => {
  // Off means "no deadline", so the number is discarded; on starts from the default for the same reason as the limit.
  if (!timeoutEnabled.value) {
    timeoutDraft.value = DEFAULT_TIMEOUT
  }
  persist()
}

const handleTimeoutChange = () => {
  let value = Math.floor(Number(timeoutDraft.value))
  if (!Number.isFinite(value) || value < 1) {
    value = 1
  }
  if (value > MAX_TIMEOUT) {
    value = MAX_TIMEOUT
  }
  timeoutDraft.value = value
  persist()
}

// Only the keys this panel owns are written back: the rest of config (locals, outputs, on_error) stays untouched, and an
// all-default block is dropped instead of stored as {max_concurrency: 0, join: "collect"}.
const persist = () => {
  if (!props.selectedNode) return
  const config = { ...(props.selectedNode.data?.config || {}) }
  const parallel = { ...(config.parallel || {}) }

  if (limitEnabled.value) {
    parallel.max_concurrency = Math.floor(Number(limitDraft.value)) || 1
  } else {
    delete parallel.max_concurrency
  }

  if (joinMode.value === 'drain' || joinMode.value === 'abort') {
    parallel.join = joinMode.value
  } else {
    delete parallel.join
  }

  if (timeoutEnabled.value) {
    parallel.timeout = {
      enabled: true,
      seconds: Math.floor(Number(timeoutDraft.value)) || DEFAULT_TIMEOUT
    }
  } else {
    delete parallel.timeout
  }

  // An all-default block is removed, not stored. The canvas merges config deeply (only adds), so the block is sent as an
  // explicit undefined: a missing key would leave the previous {max_concurrency} in place and the switch would spring back.
  if (Object.keys(parallel).length > 0) {
    config.parallel = parallel
  } else {
    config.parallel = undefined
  }

  emit('update-node-data', { nodeId: props.selectedNode.id, data: { config } })
}
</script>

<style scoped>
/* The panel's section styling is duplicated per sub-panel (OutputVarsConfig / OnErrorConfig do the same): a scoped style
   in the parent does not reach into a child component's own elements, so this component has to carry it. */
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

.prop-group-horizontal {
  display: flex;
  gap: 16px;
  margin-bottom: 10px;
}

.prop-item {
  flex: 1;
  display: flex;
  flex-direction: row;
  align-items: center;
  gap: 8px;
  flex-wrap: nowrap;
  min-width: 0;
}

.prop-label {
  font-size: 12px;
  color: #666;
  font-weight: 500;
  flex-shrink: 0;
  min-width: 60px;
}

.prop-input {
  width: 100%;
  min-width: 0;
}

.prop-select {
  width: 100%;
  min-width: 0;
}

.prop-input :deep(.el-input__wrapper) {
  background-color: #eee;
  border: none;
  box-shadow: none;
}

.prop-input :deep(.el-input__wrapper:hover),
.prop-input :deep(.el-input__wrapper.is-focus) {
  border: none;
  box-shadow: none;
}

.prop-select :deep(.el-select__wrapper) {
  background-color: #eee;
  border: none;
  box-shadow: none;
}

.prop-select :deep(.el-select__wrapper:hover),
.prop-select :deep(.el-select__wrapper.is-focus) {
  border: none;
  box-shadow: none;
}

.scope-unlimited {
  font-size: 12px;
  color: #999;
  flex-shrink: 0;
}

.scope-hint {
  margin: 0;
  padding: 0 2px 2px;
  font-size: 12px;
  line-height: 1.6;
  color: #999;
}
</style>
