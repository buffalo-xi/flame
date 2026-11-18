<template>
  <div class="module-section">
    <div class="module-head">
      <h3>租户配额</h3>
      <div class="head-actions">
        <el-button size="small" :icon="Refresh" @click="loadTenantQuota">刷新</el-button>
      </div>
    </div>

    <div class="editor-tip quota-intro">
      本租户的默认资源上限，受平台<b>系统顶</b>约束。「用户管理」里单用户配额选「跟随租户」时，
      用的就是下面「当前生效」那一行的值。
    </div>

    <div v-loading="tqLoading" class="tq-table">
      <div class="tq-row tq-row-head">
        <span>配置项</span><span>当前用量</span><span>系统顶</span><span>本租户配置</span>
      </div>
      <div v-for="f in TQ_FIELDS" :key="f.key" class="tq-row" :data-field="f.key">
        <span class="tq-name">{{ f.label }}</span>
        <span class="tq-used">{{ tqUsed(f) }}</span>
        <span class="tq-sys">{{ tqSysText(f) }}</span>
        <el-input v-model="tqForm[f.key]" size="small" class="tq-input" placeholder="留空回退系统顶" />
      </div>
    </div>

    <div class="tq-tip">
      留空 = 回退系统顶；-1 = 不限制；<b>不能超过系统顶</b>。空间按 MiB 填写，保存后立即生效。
    </div>

    <div class="tq-foot">
      <span class="tq-eff">当前生效：{{ tqEffectiveText }}</span>
      <el-button size="small" type="primary" :loading="tqSaving" @click="saveTenantQuota">保存租户配额</el-button>
    </div>
  </div>
</template>

<script setup>
import { computed, ref, watch } from 'vue'
import { ElMessage } from 'element-plus'
import { Refresh } from '@element-plus/icons-vue'
import apiConfig from '../../../config/api.js'
import { getToken } from '../../../utils/auth.js'
import { useUsers } from '../useUsers.js'

const props = defineProps({
  // 当前 tab 是否激活：切到本页才拉数据，别进设置页就多打一个请求
  active: { type: Boolean, default: false }
})

// 保存后要顺带刷用户列表：用户行里的「跟随租户（N）」显示的就是这里的生效值
const { loadUsers } = useUsers()

const MIB = 1024 * 1024

// ===== 租户级配额（platform.quota_configs 的租户行） =====
// 五项都能改，但不能超过系统顶（后端硬校验，超了直接拒）。用户行里的「跟随租户」
// 用的就是这里的生效值，所以保存后要顺带重拉用户列表（defaults 会变）。
const TQ_FIELDS = [
  { key: 'max_agents', label: '智能体（个）', unit: 'count', usageKey: 'agents', short: '智能体' },
  { key: 'max_skills', label: '技能（个）', unit: 'count', usageKey: 'skills', short: '技能' },
  { key: 'max_flows', label: '工作流（个）', unit: 'count', usageKey: 'flows', short: '工作流' },
  { key: 'max_session_storage_bytes', label: '单会话空间上限（MiB）', unit: 'mib', short: '会话空间' },
  { key: 'max_user_sessions', label: '单用户会话数上限（个）', unit: 'count', short: '会话数' }
]

const tqLoading = ref(false)
const tqSaving = ref(false)
const tqSys = ref({})
const tqFb = ref({})
const tqForm = ref({})
const tqEff = ref({})
const tqUsage = ref({})

// 用量只有前三项有（会话两项要扫存储才知道，显示 —）
const tqUsed = (f) => (f.usageKey ? (tqUsage.value[f.usageKey] ?? 0) : '—')

const tqLimitText = (f, v) => {
  if (v === null || v === undefined) return '跟随更上层'
  if (v === -1) return '不限'
  return f.unit === 'mib' ? `${Math.round(v / MIB)} MiB` : `${v} 个`
}

// 系统顶 = 系统行，未设置时退到代码兜底（标注「兜底」）
const tqSysText = (f) => {
  const v = tqSys.value[f.key]
  if (v === null || v === undefined) return `${tqLimitText(f, tqFb.value[f.key])}（兜底）`
  return tqLimitText(f, v)
}

const tqEffectiveText = computed(() =>
  TQ_FIELDS.map((f) => `${f.short} ${tqLimitText(f, tqEff.value[f.key])}`).join(' · ')
)

async function loadTenantQuota() {
  tqLoading.value = true
  try {
    const token = getToken()
    const resp = await fetch(apiConfig.admin.tenantQuota, { headers: { Authorization: `Bearer ${token}` } })
    if (!resp.ok) return
    const data = await resp.json()
    if (data.code !== 200) return
    const d = data.data || {}
    tqSys.value = d.system || {}
    tqFb.value = d.fallback || {}
    tqEff.value = d.effective || {}
    tqUsage.value = d.usage || {}
    const row = d.quota || {}
    const form = {}
    for (const f of TQ_FIELDS) {
      const v = row[f.key]
      form[f.key] =
        v === null || v === undefined ? '' : f.unit === 'mib' ? String(Math.round(v / MIB)) : String(v)
    }
    tqForm.value = form
  } catch (e) {
    console.error('获取租户配额失败:', e)
  } finally {
    tqLoading.value = false
  }
}

async function saveTenantQuota() {
  const body = {}
  for (const f of TQ_FIELDS) {
    const raw = String(tqForm.value[f.key] ?? '').trim()
    if (raw === '') {
      body[f.key] = null
      continue
    }
    if (!/^-?\d+$/.test(raw)) {
      return ElMessage.warning('配额只能填整数（-1 表示不限，留空表示回退系统顶）')
    }
    const n = Number(raw)
    body[f.key] = f.unit === 'mib' ? (n === -1 ? -1 : n * MIB) : n
  }
  tqSaving.value = true
  try {
    const token = getToken()
    const resp = await fetch(apiConfig.admin.tenantQuota, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${token}` },
      body: JSON.stringify(body)
    })
    const data = await resp.json().catch(() => ({}))
    if (data.code !== 200) throw new Error(data.msg || '保存失败')
    ElMessage.success('租户配额已保存，立即生效')
    await loadTenantQuota()
    await loadUsers() // 「跟随租户」的默认值要跟着变
  } catch (e) {
    ElMessage.error(e.message || '保存失败')
  } finally {
    tqSaving.value = false
  }
}

watch(() => props.active, (active) => {
  if (!active) return
  loadTenantQuota()
}, { immediate: true })
</script>

<style scoped>
.module-section {
  background: #fff;
  border: 1px solid #e0e0e0;
  border-radius: 12px;
  padding: 20px 24px 24px;
}

.module-head {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 18px;
}

.module-head h3 {
  font-size: 16px;
  font-weight: 500;
  color: #202124;
  margin: 0;
}

.head-actions {
  display: flex;
  align-items: center;
  gap: 8px;
}
.head-actions :deep(.el-button + .el-button) {
  margin-left: 0;
}

.editor-tip {
  font-size: 12px;
  line-height: 1.6;
  color: #909399;
}

/* 说明文字与表格之间留白：这块不再是页头小字，是独立页的导语 */
.quota-intro {
  margin-bottom: 14px;
}

/* ===== 配额表（配置项 / 当前用量 / 系统顶 / 本租户配置） =====
   升格为整页主内容后自带边框，行距比原来那块嵌套小卡片放宽一档 */
.tq-table {
  border: 1px solid #ebeef5;
  border-radius: 8px;
  overflow: hidden;
}

.tq-row,
.tq-row-head {
  display: grid;
  grid-template-columns: 168px 72px 96px 1fr;
  align-items: center;
  gap: 10px;
  padding: 10px 12px;
}

.tq-row-head {
  font-size: 12px;
  font-weight: 600;
  color: #909399;
  background: #fafafa;
}

.tq-row-head + .tq-row,
.tq-row + .tq-row {
  border-top: 1px solid #f0f2f5;
}

.tq-name {
  font-size: 13px;
  color: #303133;
  white-space: nowrap;
}

.tq-used,
.tq-sys {
  font-size: 13px;
  color: #303133;
}

.tq-sys {
  color: #606266;
}

/* 整页宽度下别让输入框拉成一条 */
.tq-input {
  max-width: 220px;
}

.tq-tip {
  padding: 10px 2px 0;
  font-size: 12px;
  color: #909399;
  line-height: 1.6;
}

.tq-foot {
  display: flex;
  align-items: center;
  gap: 10px;
  padding-top: 14px;
}

.tq-eff {
  margin-right: auto;
  font-size: 12px;
  color: #909399;
}
</style>
