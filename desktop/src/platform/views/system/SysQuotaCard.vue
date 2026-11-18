<template>
  <div class="domain">
    <div class="d-sub">
      所有租户的硬顶：运行期取 min(租户值, 系统值)。留空 = 用兜底值，-1 = 不限制，0 = 一个都不许新建。
    </div>
    <div class="d-sub">
      计数按表内行数算、不看状态：已废弃 / 已锁定的智能体与技能照样占额度，删掉才释放。
      调低到低于当前用量不会删任何已有对象、也不会让它们失效 —— 只是该租户再也新建不了（从远端导入同样被拒），
      直到用量降回上限以内。表里那列「兜底值」是代码内置默认，只在系统行留空时生效，不可编辑。
    </div>

    <div v-loading="loading" class="cfg-table">
      <div class="cfg-head">
        <span>配置项</span>
        <span>兜底值</span>
        <span>系统级设置</span>
      </div>
      <div v-for="f in FIELDS" :key="f.key" class="cfg-row">
        <span class="cfg-name">{{ f.label }}</span>
        <span class="cfg-fallback">{{ fmtVal(f, fallback[f.key]) }}</span>
        <el-input v-model="form[f.key]" size="small" placeholder="留空 = 用兜底值" />
      </div>
    </div>

    <div class="blk-foot">
      <span class="tip">
        调低会截断已配更高值的租户（只影响运行期取值，不会改租户行，租户页上的数字也不会自动变）。保存后立即生效。
      </span>
      <el-button size="small" type="primary" :loading="saving" @click="save">保存</el-button>
    </div>
  </div>
</template>

<script setup>
import { onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { api } from '../../api'

// unit: 'mib' 表示后端存字节、界面按 MiB 输入/展示（-1 = 不限，留空 = 未设置）
const MIB = 1024 * 1024
const FIELDS = [
  { key: 'max_agents', label: '智能体上限' },
  { key: 'max_skills', label: '技能上限' },
  { key: 'max_flows', label: '工作流上限' },
  { key: 'max_session_storage_bytes', label: '单会话空间上限（MiB）', unit: 'mib' },
  { key: 'max_user_sessions', label: '单用户会话数上限（个）' }
]

// 兜底值展示：字节字段换算成 MiB
const fmtVal = (f, v) => {
  if (v === null || v === undefined) return '-'
  if (f.unit !== 'mib') return String(v)
  return v === -1 ? '-1（不限）' : `${Math.round(v / MIB)} MiB`
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
  for (const f of FIELDS) {
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
    ElMessage.error(err?.message || '加载失败')
  } finally {
    loading.value = false
  }
}

// '' → null（未设置）；非数字也按未设置处理（与租户侧同口径）
// MiB 字段在这里换算回字节，-1（不限）不换算。
function toBody() {
  const body = {}
  for (const f of FIELDS) {
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
  for (const f of FIELDS) {
    if (body[f.key] !== null && body[f.key] < -1) {
      ElMessage.error(`${f.label} 只能是 -1（不限制）或非负数`)
      return
    }
  }

  // 影响所有租户：确认框里逐项列出"旧 → 新"
  const changes = FIELDS.filter((f) => original[f.key] !== String(form[f.key] ?? '').trim()).map(
    (f) =>
      `${esc(f.label)}：${esc(original[f.key] || '未设置')} → ${esc(String(form[f.key] ?? '').trim() || '未设置')}`
  )
  if (!changes.length) {
    ElMessage.info('没有需要保存的改动')
    return
  }
  try {
    await ElMessageBox.confirm(
      `<div style="line-height:1.9">将修改<b>系统级</b>配额上限（影响所有租户）：<br/>${changes.join('<br/>')}</div>`,
      '保存系统配置',
      { dangerouslyUseHTMLString: true, type: 'warning', confirmButtonText: '确认保存' }
    )
  } catch {
    return
  }

  saving.value = true
  try {
    await api.setSystemQuota(body)
    ElMessage.success('系统配置已保存，立即生效')
    await load()
  } catch (err) {
    ElMessage.error(err?.message || '保存失败')
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
