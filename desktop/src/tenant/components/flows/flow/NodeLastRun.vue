<template>
  <div class="last-run-panel">
    <div v-if="!nodeResult" class="nr-empty">暂无运行数据</div>
    <div v-else class="props-section">
      <div class="result-item result-meta-row">
        <span class="meta-item">
          <label class="result-label">运行状态</label>
          <span class="result-value" :class="statusClass">{{ statusText }}</span>
        </span>
        <span class="meta-item">
          <label class="result-label">运行时间</label>
          <span class="result-value">{{ startTimeText || '-' }}</span>
        </span>
        <span class="meta-item">
          <label class="result-label">耗时</label>
          <span class="result-value">{{ nodeResult.duration_ms }}ms</span>
        </span>
      </div>

      <!-- 输出变量：按节点声明的 outputs 逐项列出，声明了但没赋值的也留着（标灰虚线），
           这样"该有值却没有"一眼可见；原先整块 JSON dump 看不出这一点 -->
      <div v-if="outputRows.length" class="result-item">
        <label class="result-label">输出变量{{ nodeResult.output_vars_truncated ? '（已截断）' : '' }}</label>
        <VarList :rows="outputRows" />
      </div>

      <!-- 输入变量：默认收起 —— 排查"节点实际拿到了什么"时才需要 -->
      <div v-if="inputRows.length" class="result-item">
        <div class="nr-section-head">
          <label class="result-label">输入变量{{ nodeResult.input_vars_truncated ? '（已截断）' : '' }}</label>
          <a class="nr-toggle" @click="showInputVars = !showInputVars">{{ showInputVars ? '收起' : '展开' }}</a>
        </div>
        <VarList v-show="showInputVars" :rows="inputRows" />
      </div>

      <div v-if="localText" class="result-item">
        <label class="result-label">本地变量{{ nodeResult.local_vars_truncated ? '（已截断）' : '' }}</label>
        <pre class="result-output">{{ localText }}</pre>
      </div>

      <div v-if="logs.length" class="result-item">
        <label class="result-label">日志</label>
        <div class="nr-logs">
          <div v-for="(log, i) in logs" :key="i" class="nr-log">{{ log }}</div>
        </div>
      </div>

      <div v-if="nodeResult.err_msg" class="result-item">
        <label class="result-label">错误</label>
        <div class="nr-log nr-error">{{ nodeResult.err_msg }}</div>
        <div v-if="nodeResult.err_stack" class="nr-log nr-stack">{{ nodeResult.err_stack }}</div>
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref, computed, inject, watch } from 'vue'
import { nodeResults } from '@/tenant/composables/runData.js'
import VarList from './VarList.vue'

const props = defineProps({
  selectedNode: { type: Object, default: null }
})

const flowDebug = inject('flowDebug', null)

// 该节点的最新执行数据（调试 node_snapshot/node_inspect 与历史 GetRunDetail 统一写入共享 store）
const nodeResult = computed(() => {
  if (!props.selectedNode) return null
  return nodeResults.value?.[props.selectedNode.id] || null
})

const statusText = computed(() => {
  const map = { success: '成功', failed: '失败', skipped: '跳过', running: '运行中', cancelled: '已取消' }
  return map[nodeResult.value?.status] || nodeResult.value?.status || ''
})

const statusClass = computed(() => {
  const map = {
    success: 'status-success',
    failed: 'status-failed',
    running: 'status-running',
    skipped: 'status-default',
    cancelled: 'status-default'
  }
  return map[nodeResult.value?.status] || 'status-default'
})

const startTimeText = computed(() => {
  const t = nodeResult.value?.start_time
  if (!t) return ''
  try { return new Date(t).toLocaleString() } catch (e) { return t }
})

const showInputVars = ref(false)

// 值的展示形态：字符串原样（避免多一层引号与转义），对象/数组缩进 JSON
const formatVar = (v) => {
  if (v === null) return 'null'
  if (v === undefined) return '（未赋值）'
  if (typeof v === 'string') return v
  return JSON.stringify(v, null, 2)
}

// 输出变量按"声明清单 ∪ 实际值"列出：声明了但没赋值也要占一行（标灰），
// 否则只看 JSON 会误以为这个节点只输出这么几个
const outputRows = computed(() => {
  const vars = nodeResult.value?.output_vars || {}
  const declared = props.selectedNode?.data?.config?.outputs || {}
  const names = Array.from(new Set([...Object.keys(declared), ...Object.keys(vars)]))
  return names.map((name) => {
    const def = declared[name] && typeof declared[name] === 'object' ? declared[name] : {}
    const has = Object.prototype.hasOwnProperty.call(vars, name)
    return {
      name,
      type: def.type || '',
      desc: def.description || '',
      text: has ? formatVar(vars[name]) : '（未赋值）',
      empty: !has
    }
  })
})

const inputRows = computed(() => {
  const vars = nodeResult.value?.input_vars || {}
  return Object.keys(vars).map((name) => ({ name, text: formatVar(vars[name]) }))
})

const localText = computed(() => {
  const v = nodeResult.value?.local_vars
  return v && Object.keys(v).length ? JSON.stringify(v, null, 2) : ''
})
const logs = computed(() => nodeResult.value?.run_logs || [])

// 选中节点变化或切到「执行结果」tab（组件挂载）时：运行中（ws 已连接）则按需拉取该节点最新数据。
// immediate 补齐「切 tab 时组件挂载但 selectedNode.id 未变」这一缺口：
// 断点挂起时后端不主动推 node_snapshot，必须由前端点节点/切 tab 触发 inspect_node 拉取。
watch(() => props.selectedNode?.id, (id) => {
  if (id && flowDebug && flowDebug.connected.value) {
    flowDebug.inspectNode(id)
  }
}, { immediate: true })
</script>

<style scoped>
.last-run-panel {
  padding: 12px;
}
.nr-empty {
  padding: 24px 0;
  text-align: center;
  color: #999;
  font-size: 13px;
}
.props-section {
  background: #fafafa;
  border-radius: 8px;
  padding: 16px;
}
.section-title {
  font-size: 13px;
  font-weight: 600;
  color: #333;
  padding-bottom: 10px;
  border-bottom: 1px solid #e8e8e8;
  margin-bottom: 12px;
}
.result-item {
  display: flex;
  flex-direction: column;
  gap: 8px;
  margin-bottom: 16px;
}
.result-meta-row {
  flex-direction: row;
  align-items: center;
  flex-wrap: wrap;
  gap: 16px;
}
.result-meta-row .meta-item {
  display: inline-flex;
  align-items: center;
  gap: 8px;
}
.result-label {
  font-size: 12px;
  color: #333;
  font-weight: 600;
}
.result-value {
  font-size: 13px;
  color: #333;
}
.status-success { color: #67c23a; }
.status-failed { color: #f56565; }
.status-running { color: #409eff; }
.status-default { color: #999; }
.result-output {
  width: 100%;
  min-height: 60px;
  max-height: 240px;
  overflow: auto;
  padding: 10px;
  border: 1px solid #d9d9d9;
  border-radius: 4px;
  font-size: 12px;
  color: #333;
  background: #fff;
  white-space: pre-wrap;
  word-break: break-all;
  box-sizing: border-box;
}
/* ===== 变量清单（输出 / 输入）===== */
.nr-section-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}
.nr-toggle {
  font-size: 12px;
  color: #1890ff;
  cursor: pointer;
  user-select: none;
}
/* 变量清单（.ov-*）的样式在 VarList.vue 里，输入 / 输出与历史运行共用一份 */

.nr-logs {
  max-height: 200px;
  overflow: auto;
  padding: 8px 10px;
  background: #fff;
  border: 1px solid #d9d9d9;
  border-radius: 4px;
}
.nr-log {
  font-size: 12px;
  color: #555;
  line-height: 1.6;
  word-break: break-all;
}
.nr-error { color: #f56565; }
.nr-stack { color: #f56565; font-family: monospace; }
</style>
