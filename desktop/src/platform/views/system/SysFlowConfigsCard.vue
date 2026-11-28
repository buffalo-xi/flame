<template>
  <div class="domain">
    <div class="d-sub">
      {{ t('platform.system.flowConfigs.intro1') }}
      {{ t('platform.system.flowConfigs.intro2') }}
    </div>

    <div v-loading="loading" class="cfg-table">
      <div class="cfg-head">
        <span>{{ t('platform.system.card.colItem') }}</span>
        <span>{{ t('platform.system.card.colFallback') }}</span>
        <span>{{ t('platform.system.card.colSystem') }}</span>
      </div>
      <div v-for="f in FIELDS" :key="f.key" class="cfg-row">
        <span class="cfg-name">
          {{ f.label }}
          <span class="cfg-desc">{{ f.desc }}</span>
        </span>
        <span class="cfg-fallback">{{ fallback[f.key] ?? '-' }}</span>
        <el-input v-model="form[f.key]" size="small" :placeholder="t('platform.system.card.placeholderFallback')" />
      </div>
    </div>

    <div class="blk-foot">
      <span class="tip">
        {{ t('platform.system.card.saveHint') }}
      </span>
      <el-button size="small" type="primary" :loading="saving" @click="save">{{ t('platform.system.card.save') }}</el-button>
    </div>
  </div>
</template>

<script setup>
import { computed, onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { ElMessage, ElMessageBox } from 'element-plus'
import { api } from '../../api'

const { t } = useI18n()

// Same field set as the tenant card / tenant settings page.
// desc says "what this number actually governs": three of the eight names are near-synonyms (timeout / executions / loop
// iterations, run records / session memory), and a bare name forces the reader to guess before they touch it.
const FIELDS = computed(() => [
  { key: 'max_timeout', label: t('platform.system.flowConfigs.max_timeout'), desc: t('platform.system.flowConfigs.max_timeoutDesc') },
  { key: 'max_recursion_depth', label: t('platform.system.flowConfigs.max_recursion_depth'), desc: t('platform.system.flowConfigs.max_recursion_depthDesc') },
  { key: 'max_node_count', label: t('platform.system.flowConfigs.max_node_count'), desc: t('platform.system.flowConfigs.max_node_countDesc') },
  { key: 'max_exec_count', label: t('platform.system.flowConfigs.max_exec_count'), desc: t('platform.system.flowConfigs.max_exec_countDesc') },
  { key: 'max_loop_times', label: t('platform.system.flowConfigs.max_loop_times'), desc: t('platform.system.flowConfigs.max_loop_timesDesc') },
  { key: 'max_run_records', label: t('platform.system.flowConfigs.max_run_records'), desc: t('platform.system.flowConfigs.max_run_recordsDesc') },
  { key: 'max_session_records', label: t('platform.system.flowConfigs.max_session_records'), desc: t('platform.system.flowConfigs.max_session_recordsDesc') },
  { key: 'max_concurrency', label: t('platform.system.flowConfigs.max_concurrency'), desc: t('platform.system.flowConfigs.max_concurrencyDesc') }
])

const loading = ref(false)
const saving = ref(false)
const fallback = ref({})
const form = reactive({}) // 输入框里的字符串：'' = 未设置（NULL）
const original = reactive({}) // 上次读到的值，用来算"变了哪几项"

const esc = (s) =>
  String(s).replace(/[&<>"]/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' }[c]))

const toForm = (src) => {
  const o = {}
  for (const f of FIELDS.value) {
    const v = src?.[f.key]
    o[f.key] = v === null || v === undefined ? '' : String(v)
  }
  return o
}

async function load() {
  loading.value = true
  try {
    const data = await api.getSystemFlowConfigs()
    fallback.value = data?.fallback || {}
    Object.assign(form, toForm(data?.config))
    Object.assign(original, toForm(data?.config))
  } catch (err) {
    ElMessage.error(err?.message || t('platform.system.card.loadFailed'))
  } finally {
    loading.value = false
  }
}

// '' → null（未设置）；非数字也按未设置处理（与租户侧同口径）
function toBody() {
  const body = {}
  for (const f of FIELDS.value) {
    const raw = String(form[f.key] ?? '').trim()
    if (raw === '') {
      body[f.key] = null
      continue
    }
    const n = Number(raw)
    body[f.key] = Number.isNaN(n) ? null : n
  }
  return body
}

async function save() {
  const body = toBody()
  for (const f of FIELDS.value) {
    if (body[f.key] !== null && body[f.key] < -1) {
      ElMessage.error(t('platform.system.card.invalidNonNegative', { label: f.label }))
      return
    }
  }

  // 这是影响所有租户的写操作：确认框里逐项列出"旧 → 新"
  const changes = FIELDS.value.filter((f) => original[f.key] !== String(form[f.key] ?? '').trim()).map(
    (f) =>
      `${esc(f.label)}：${esc(original[f.key] || t('platform.system.card.unset'))} → ${esc(String(form[f.key] ?? '').trim() || t('platform.system.card.unset'))}`
  )
  if (!changes.length) {
    ElMessage.info(t('platform.system.card.noChanges'))
    return
  }
  try {
    await ElMessageBox.confirm(
      `<div style="line-height:1.9">${t('platform.system.card.confirmConfigPrefix')}<br/>${changes.join('<br/>')}</div>`,
      t('platform.system.card.confirmTitle'),
      { dangerouslyUseHTMLString: true, type: 'warning', confirmButtonText: t('platform.system.card.confirmSave') }
    )
  } catch {
    return
  }

  saving.value = true
  try {
    await api.setSystemFlowConfigs(body)
    ElMessage.success(t('platform.system.card.saved'))
    await load()
  } catch (err) {
    ElMessage.error(err?.message || t('platform.system.card.saveFailed'))
  } finally {
    saving.value = false
  }
}

onMounted(load)
</script>

<style scoped>
.d-sub {
  margin-bottom: 10px;
  font-size: 12px;
  color: #909399;
  line-height: 1.6;
}

.cfg-table {
  border: 1px solid #ebeef5;
  border-radius: 6px;
  overflow: hidden;
}

.cfg-head,
.cfg-row {
  display: grid;
  grid-template-columns: 1.2fr 80px 1fr;
  align-items: center;
  gap: 10px;
  padding: 8px 12px;
}

.cfg-head {
  background: #fafafa;
  font-size: 12px;
  font-weight: 600;
  color: #909399;
}

.cfg-row + .cfg-row {
  border-top: 1px solid #f0f2f5;
}

.cfg-name {
  font-size: 13px;
  color: #303133;
}

/* 字段说明压在名字下面一行：这一栏只有 1.2fr，横着接一句会把输入框挤窄 */
.cfg-desc {
  display: block;
  margin-top: 2px;
  font-size: 11px;
  line-height: 1.5;
  color: #a8abb2;
}

.cfg-fallback {
  font-size: 13px;
  color: #909399;
}

/* 卡片底部：说明占满左侧，保存按钮靠右 */
.blk-foot {
  display: flex;
  align-items: center;
  gap: 12px;
  margin-top: 12px;
}

.tip {
  flex: 1;
  font-size: 12px;
  color: #909399;
  line-height: 1.6;
}
</style>
