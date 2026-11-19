<template>
  <div class="domain">
    <div class="d-sub">
      {{ t('platform.system.quota.intro1') }}
    </div>
    <div class="d-sub">
      {{ t('platform.system.quota.intro2') }}
    </div>

    <div v-loading="loading" class="cfg-table">
      <div class="cfg-head">
        <span>{{ t('platform.system.card.colItem') }}</span>
        <span>{{ t('platform.system.card.colFallback') }}</span>
        <span>{{ t('platform.system.card.colSystem') }}</span>
      </div>
      <div v-for="f in FIELDS" :key="f.key" class="cfg-row">
        <span class="cfg-name">{{ f.label }}</span>
        <span class="cfg-fallback">{{ fmtVal(f, fallback[f.key]) }}</span>
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

// unit: 'mib' 表示后端存字节、界面按 MiB 输入/展示（-1 = 不限，留空 = 未设置）
const MIB = 1024 * 1024
const FIELDS = computed(() => [
  { key: 'max_agents', label: t('platform.system.quota.max_agents') },
  { key: 'max_skills', label: t('platform.system.quota.max_skills') },
  { key: 'max_flows', label: t('platform.system.quota.max_flows') },
  { key: 'max_session_storage_bytes', label: t('platform.system.quota.max_session_storage_bytes'), unit: 'mib' },
  { key: 'max_user_sessions', label: t('platform.system.quota.max_user_sessions') }
])

// 兜底值展示：字节字段换算成 MiB
const fmtVal = (f, v) => {
  if (v === null || v === undefined) return '-'
  if (f.unit !== 'mib') return String(v)
  return v === -1 ? t('platform.system.quota.unlimited') : `${Math.round(v / MIB)} MiB`
}

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
    if (v === null || v === undefined) {
      o[f.key] = ''
    } else if (f.unit === 'mib') {
      // 字节 → MiB；-1（不限）原样保留，不做换算
      o[f.key] = v === -1 ? '-1' : String(Math.round(v / MIB))
    } else {
      o[f.key] = String(v)
    }
  }
  return o
}

async function load() {
  loading.value = true
  try {
    const data = await api.getSystemQuota()
    fallback.value = data?.fallback || {}
    Object.assign(form, toForm(data?.quota))
    Object.assign(original, toForm(data?.quota))
  } catch (err) {
    ElMessage.error(err?.message || t('platform.system.card.loadFailed'))
  } finally {
    loading.value = false
  }
}

// '' → null（未设置）；非数字也按未设置处理（与租户侧同口径）
// MiB 字段在这里换算回字节，-1（不限）不换算。
function toBody() {
  const body = {}
  for (const f of FIELDS.value) {
    const raw = String(form[f.key] ?? '').trim()
    if (raw === '') {
      body[f.key] = null
      continue
    }
    const n = Number(raw)
    if (Number.isNaN(n)) {
      body[f.key] = null
    } else if (f.unit === 'mib' && n !== -1) {
      body[f.key] = n * MIB
    } else {
      body[f.key] = n
    }
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

  // 影响所有租户：确认框里逐项列出"旧 → 新"
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
      `<div style="line-height:1.9">${t('platform.system.card.confirmQuotaPrefix')}<br/>${changes.join('<br/>')}</div>`,
      t('platform.system.card.confirmTitle'),
      { dangerouslyUseHTMLString: true, type: 'warning', confirmButtonText: t('platform.system.card.confirmSave') }
    )
  } catch {
    return
  }

  saving.value = true
  try {
    await api.setSystemQuota(body)
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
