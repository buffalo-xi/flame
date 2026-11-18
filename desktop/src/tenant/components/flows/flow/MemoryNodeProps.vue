<template>
  <div class="memory-node-props">
    <div class="tabs">
      <button class="tab-btn" :class="{ active: activeTab === 'settings' }" @click="activeTab = 'settings'">配置</button>
      <button class="tab-btn" :class="{ active: activeTab === 'lastRun' }" @click="activeTab = 'lastRun'">执行结果</button>
    </div>

    <div class="tab-content">
      <div v-if="activeTab === 'settings'" class="settings-panel">
        <!-- 输出变量（只读展示） -->
        <div class="props-section">
          <div class="section-title"><span>输出变量</span></div>
          <div class="field-row">
            <label class="field-label">输出变量</label>
            <span class="field-value">messages</span>
            <span class="field-type-tag">array[object]</span>
          </div>
          <div class="field-hint">每条为 { role, content, timestamp }，可直接拼进 LLM 上下文</div>
        </div>

        <!-- 条数限制 -->
        <div class="props-section">
          <div class="section-title"><span>条数限制</span></div>
          <div class="field-row">
            <label class="field-label">最多取 N 条<span class="limit-hint">/ 系统上限 {{ maxSessionRecords }}</span></label>
            <div class="config-controls">
              <el-input
                v-if="limitEnabled"
                :model-value="String(limit)"
                class="limit-input"
                type="number"
                :min="1"
                :max="maxSessionRecords"
                @update:model-value="handleUpdateLimit"
                @blur="clampLimitOnBlur"
              />
              <el-switch :model-value="limitEnabled" @update:model-value="toggleLimitEnabled" />
            </div>
          </div>
          <div class="field-hint">关闭 = 使用系统最大值（max_session_records）</div>
        </div>

        <!-- 时间窗口 -->
        <div class="props-section">
          <div class="section-title"><span>时间窗口</span></div>
          <div class="field-row">
            <label class="checkbox-label">
              <input type="checkbox" :checked="windowEnabled" @change="handleUpdateWindowEnabled($event.target.checked)" />
              仅取最近
            </label>
            <input type="number" :value="windowValue" class="field-input field-input-sm" min="1" :disabled="!windowEnabled"
              @blur="handleUpdateWindowValue($event.target.value)" />
            <select :value="windowUnit" class="field-select" :disabled="!windowEnabled"
              @change="handleUpdateWindowUnit($event.target.value)">
              <option value="second">秒</option>
              <option value="minute">分钟</option>
              <option value="hour">小时</option>
              <option value="day">天</option>
            </select>
          </div>
        </div>
      </div>

      <!-- 执行结果 -->
      <div v-if="activeTab === 'lastRun'" class="last-run-panel">
        <div v-if="!memoryResult" class="nr-empty">暂无运行数据</div>
        <div v-else class="props-section">
          <div class="result-item result-meta-row">
            <span class="meta-item">
              <label class="result-label">运行状态</label>
              <span class="result-value" :class="statusClass">{{ statusText }}</span>
            </span>
            <span class="meta-item">
              <label class="result-label">耗时</label>
              <span class="result-value">{{ memoryResult.duration_ms }}ms</span>
            </span>
          </div>
          <div class="result-item">
            <label class="result-label">输出</label>
            <pre class="result-output">{{ outputText }}</pre>
          </div>
          <div v-if="memoryResult.err_msg" class="result-item">
            <label class="result-label">错误</label>
            <div class="nr-log nr-error">{{ memoryResult.err_msg }}</div>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref, computed, watch, inject } from 'vue'
import { nodeResults } from '@/tenant/composables/runData.js'

const props = defineProps({
  selectedNode: { type: Object, default: null },
  flowSystemLimits: { type: Object, default: () => ({ max_session_records: 50 }) }
})

const emit = defineEmits(['update-node-data'])
const flowDebug = inject('flowDebug', null)

const activeTab = ref('settings')

// 当前配置
const limit = ref(10)
const windowEnabled = ref(false)
const windowValue = ref(24)
const windowUnit = ref('hour')

// 从 config 加载
const loadConfig = () => {
  const cfg = props.selectedNode?.data?.config || {}
  limit.value = cfg.limit ?? 10
  const w = cfg.window || {}
  windowEnabled.value = !!w.enabled
  windowValue.value = w.value ?? 24
  windowUnit.value = w.unit || 'hour'
}

watch(() => props.selectedNode, () => loadConfig(), { immediate: true })

const save = (patch) => {
  if (!props.selectedNode) return
  emit('update-node-data', { nodeId: props.selectedNode.id, data: { config: patch } })
}

// 系统上限（会话记忆条数）
const maxSessionRecords = computed(() => {
  const v = props.flowSystemLimits?.max_session_records
  return (v !== undefined && v !== null && Number(v) > 0) ? Number(v) : 50
})

// limit 开关：limit > 0 表示启用（取 N 条），0 表示关闭（用系统最大值）
const limitEnabled = computed(() => limit.value > 0)

const toggleLimitEnabled = (enabled) => {
  if (enabled) {
    // 打开：当前 0 则默认 10（不超过上限）
    const v = limit.value > 0 ? limit.value : 10
    const capped = Math.min(v, maxSessionRecords.value)
    limit.value = capped
    save({ limit: capped })
  } else {
    // 关闭：0 = 用系统最大值
    limit.value = 0
    save({ limit: 0 })
  }
}

const handleUpdateLimit = (value) => {
  let n = parseInt(value, 10)
  if (isNaN(n) || n < 1) n = 1
  if (n > maxSessionRecords.value) n = maxSessionRecords.value
  limit.value = n
  save({ limit: n })
}

const clampLimitOnBlur = (e) => {
  const val = e?.target?.value
  if (val === undefined || val === null || val === '') return
  const num = Number(val)
  if (Number.isFinite(num) && num > maxSessionRecords.value) {
    handleUpdateLimit(String(maxSessionRecords.value))
  }
}

const handleUpdateWindowEnabled = (enabled) => {
  windowEnabled.value = enabled
  save({ window: { enabled, unit: windowUnit.value, value: windowValue.value } })
}

const handleUpdateWindowValue = (value) => {
  const n = parseInt(value, 10)
  const v = isNaN(n) ? 0 : n
  windowValue.value = v
  save({ window: { enabled: windowEnabled.value, unit: windowUnit.value, value: v } })
}

const handleUpdateWindowUnit = (unit) => {
  windowUnit.value = unit
  save({ window: { enabled: windowEnabled.value, unit, value: windowValue.value } })
}

// 执行结果
// 选中节点变化或切到「执行结果」tab（组件挂载）时：运行中（ws 已连接）则按需拉取该节点最新数据。
// 断点挂起时后端不主动推 node_snapshot，必须由前端点节点/切 tab 触发 inspect_node 拉取。
watch(() => props.selectedNode?.id, (id) => {
  if (id && flowDebug && flowDebug.connected.value) {
    flowDebug.inspectNode(id)
  }
}, { immediate: true })

const memoryResult = computed(() => {
  if (!props.selectedNode) return null
  return nodeResults.value?.[props.selectedNode.id] || null
})
const statusText = computed(() => {
  const map = { success: '成功', failed: '失败', running: '运行中', skipped: '跳过', cancelled: '已取消' }
  return map[memoryResult.value?.status] || memoryResult.value?.status || ''
})
const statusClass = computed(() => {
  const map = { success: 'status-success', failed: 'status-failed', running: 'status-running' }
  return map[memoryResult.value?.status] || 'status-default'
})
const outputText = computed(() => {
  const v = memoryResult.value?.output_vars
  return v && Object.keys(v).length ? JSON.stringify(v, null, 2) : '{}'
})
</script>

<style scoped>
.memory-node-props {
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
  margin-right: 20px;
}

.tab-btn.active { color: #1890ff; font-weight: 500; }
.tab-btn.active::after { content: ''; position: absolute; bottom: -1px; left: 0; right: 0; height: 1px; background: #1890ff; }

.tab-content { flex: 1; overflow-y: auto; padding: 8px; }
.settings-panel { display: flex; flex-direction: column; gap: 8px; }

.props-section { background: #fafafa; border-radius: 8px; padding: 10px; }

.section-title {
  font-size: 13px;
  font-weight: 600;
  color: #333;
  padding-bottom: 10px;
  border-bottom: 1px solid #e8e8e8;
  margin-bottom: 12px;
}

.field-row {
  display: flex;
  align-items: center;
  gap: 8px;
}

.field-label { font-size: 12px; color: #666; flex-shrink: 0; white-space: nowrap; }

.limit-hint { font-size: 11px; color: #999; margin-left: 2px; }

.config-controls {
  display: flex;
  align-items: center;
  gap: 8px;
  flex: 1;
  justify-content: flex-end;
}

.field-input {
  flex: 1;
  min-width: 0;
  height: 30px;
  box-sizing: border-box;
  padding: 4px 8px;
  border: 1px solid #e8e8e8;
  background: #fff;
  border-radius: 4px;
  font-size: 12px;
  outline: none;
}
.field-input:focus { border-color: #1890ff; }
.field-input:disabled { background: #f0f0f0; color: #ccc; }

.field-input-sm { max-width: 80px; }

.limit-input {
  flex: 1;
  min-width: 0;
  max-width: 120px;
}
.limit-input :deep(.el-input__wrapper) {
  padding: 0 8px;
  box-shadow: none;
}

.field-select {
  height: 30px;
  box-sizing: border-box;
  border: 1px solid #e8e8e8;
  background: #fff;
  border-radius: 4px;
  font-size: 12px;
  outline: none;
  cursor: pointer;
}
.field-select:disabled { background: #f0f0f0; color: #ccc; cursor: not-allowed; }

.checkbox-label {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  font-size: 12px;
  color: #666;
  cursor: pointer;
  white-space: nowrap;
}

.field-hint { font-size: 11px; color: #999; margin-top: 6px; }

.field-value { font-size: 13px; color: #333; font-weight: 600; }

.field-type-tag {
  font-size: 11px;
  color: #909399;
  background: #f0f0f0;
  border-radius: 3px;
  padding: 1px 6px;
}

/* 执行结果 */
.last-run-panel { padding: 0; }
.last-run-panel .props-section { padding: 16px 12px; }
.result-item { display: flex; flex-direction: column; gap: 8px; margin-bottom: 16px; }
.result-meta-row { flex-direction: row; align-items: center; flex-wrap: wrap; gap: 16px; }
.result-meta-row .meta-item { display: inline-flex; align-items: center; gap: 8px; }
.result-label { font-size: 12px; color: #333; font-weight: 600; }
.result-value { font-size: 13px; color: #333; }
.nr-empty { padding: 24px 0; text-align: center; color: #999; font-size: 13px; }
.nr-log { font-size: 12px; color: #555; line-height: 1.6; word-break: break-all; }
.nr-error { color: #f56565; }
.status-success { color: #67c23a; }
.status-failed { color: #f56565; }
.status-running { color: #409eff; }
.status-default { color: #999; }
.result-output {
  width: 100%; min-height: 100px; padding: 10px; border: 1px solid #d9d9d9; border-radius: 4px; font-size: 12px; color: #333; background: #fafafa; overflow-x: auto; white-space: pre-wrap; word-break: break-all;
}
</style>
