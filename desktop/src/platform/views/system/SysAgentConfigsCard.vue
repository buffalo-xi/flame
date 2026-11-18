<template>
  <div class="domain">
    <div class="d-sub">
      所有租户的硬顶：运行期取 min(租户值, 系统值)。留空 = 用兜底值。
      <b>本组配置没有 -1</b>——四项都是安全阀，调成「不限制」等于把防线拆掉。
    </div>
    <div class="d-sub">
      会话数量与会话空间不在本组：它们属于资源配额，见「配额上限」（那两项允许 -1，且可按用户覆盖）。
    </div>

    <div v-loading="loading" class="cfg-table">
      <div class="cfg-head">
        <span>配置项</span>
        <span>兜底值</span>
        <span>系统级设置</span>
      </div>
      <div v-for="f in FIELDS" :key="f.key" class="cfg-row">
        <span class="cfg-name">{{ f.label }}</span>
        <span class="cfg-fallback">{{ fallback[f.key] ?? '-' }}</span>
        <el-input v-model="form[f.key]" size="small" :placeholder="f.ph || '留空 = 用兜底值'" />
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

// 与租户卡片 / 租户设置页同一套字段（platform.agent_configs）
const FIELDS = [
  { key: 'max_iterations', label: '最大运行轮次（轮）' },
  { key: 'max_call_depth', label: '最大调用深度（层）' },
  { key: 'max_tools', label: '工具数量上限（个）' },
  { key: 'max_output_bytes', label: '命令输出上限（字节）', ph: '留空 = 65536（64 KB）' }
]

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
    o[f.key] = v === null || v === undefined ? '' : String(v)
  }
  return o
}

async function load() {
  loading.value = true
  try {
    const data = await api.getSystemAgentConfigs()
    fallback.value = data?.fallback || {}
    Object.assign(form, toForm(data?.config))
    Object.assign(original, toForm(data?.config))
  } catch (err) {
    ElMessage.error(err?.message || '加载失败')
  } finally {
    loading.value = false
  }
}

// '' → null（未设置）；非数字也按未设置处理（与租户侧同口径）
function toBody() {
  const body = {}
  for (const f of FIELDS) {
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
  // 关键差异：这里不接受 -1，只接受 >= 1（后端同样会拒）
  for (const f of FIELDS) {
    if (body[f.key] !== null && body[f.key] < 1) {
      ElMessage.error(`${f.label}必须是 >= 1 的整数（该配置不支持 -1 不限制）`)
      return
    }
  }

  // 这是影响所有租户的写操作：确认框里逐项列出"旧 → 新"
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
      `<div style="line-height:1.9">将修改<b>系统级</b>配置（影响所有租户）：<br/>${changes.join('<br/>')}</div>`,
      '保存系统配置',
      { dangerouslyUseHTMLString: true, type: 'warning', confirmButtonText: '确认保存' }
    )
  } catch {
    return
  }

  saving.value = true
  try {
    await api.setSystemAgentConfigs(body)
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
