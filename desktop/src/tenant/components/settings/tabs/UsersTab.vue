<template>
  <div class="module-section">
    <div class="module-head">
      <h3>用户管理</h3>
      <div class="head-actions">
        <el-button size="small" :icon="Refresh" @click="loadUsers">刷新</el-button>
        <el-button type="primary" size="small" :icon="Plus" @click="openUserDialog()">新建用户</el-button>
      </div>
    </div>

    <div class="editor-tip" style="margin-bottom:10px">
      <strong>管理用户</strong>＝可登录控制台并使用后台；<strong>服务用户</strong>＝不可登录，只作为 API 调用身份
      （在「服务凭证」里绑到 API Key / OAuth）。停用后该账号的登录与 API 调用同时失效。
      <br />
      配额「跟随租户」时用「租户配额」页的生效值；自定义只对该用户生效，且不得超过租户上限。
    </div>

    <el-table :data="users" v-loading="usersLoading" size="small" class="ut-table">
      <el-table-column label="登录名" min-width="150">
        <template #default="{ row }">
          <div>
            {{ row.name }}
            <!-- 平台开通租户时创建的根账号：不可编辑 / 不可停用（后端同样拦；删除接口本就不存在） -->
            <el-tag v-if="isPrimaryRow(row)" size="small" type="warning" effect="plain">根账号</el-tag>
          </div>
          <div style="font-size:12px;color:#909399">{{ row.display_name || '-' }}</div>
        </template>
      </el-table-column>
      <el-table-column label="角色" width="110">
        <template #default="{ row }">
          <el-tag size="small" :type="row.role === 'manage' ? 'primary' : 'info'">
            {{ row.role === 'manage' ? '管理用户' : row.role === 'service' ? '服务用户' : (row.role || '-') }}
          </el-tag>
        </template>
      </el-table-column>
      <el-table-column label="状态" width="100">
        <template #default="{ row }">
          <el-tag size="small"
                  :type="row.status === 'active' ? 'success' : (row.status === 'locked' ? 'warning' : 'danger')">
            {{ userStatusLabel(row.status) }}
          </el-tag>
        </template>
      </el-table-column>
      <el-table-column label="配额（会话 / 单会话空间）" min-width="200">
        <template #default="{ row }">
          <div class="quota-cell">
            <span :class="{ 'q-custom': row.max_sessions != null }">{{ quotaSessionsText(row) }}</span>
            <span :class="{ 'q-custom': row.max_session_storage_bytes != null }">{{ quotaStorageText(row) }}</span>
          </div>
        </template>
      </el-table-column>
      <el-table-column label="邮箱" min-width="180" prop="email" />
      <el-table-column label="操作" width="320">
        <template #default="{ row }">
          <el-button link type="primary" size="small"
                     :disabled="isPrimaryRow(row)"
                     :title="isPrimaryRow(row) ? 'primary 是平台开通时创建的根账号，不可修改' : ''"
                     @click="openUserDialog(row)">编辑</el-button>
          <el-button link type="primary" size="small"
                     :disabled="isPrimaryRow(row)"
                     :title="isPrimaryRow(row) ? 'primary 的配额只能由平台调整' : '为这个用户单独设置配额'"
                     @click="openQuotaDialog(row)">配额</el-button>
          <el-button v-if="row.role === 'manage'" link type="warning" size="small"
                     @click="openResetPwd(row)">重置密码</el-button>
          <el-button link :type="row.status === 'active' ? 'info' : 'success'" size="small"
                     :disabled="isPrimaryRow(row)"
                     :title="isPrimaryRow(row) ? 'primary 是根账号，不可停用' : ''"
                     @click="toggleUserActive(row)">
            {{ row.status === 'active' ? '停用' : '启用' }}
          </el-button>
        </template>
      </el-table-column>
    </el-table>

    <!-- 用户：新建 / 编辑 -->
    <SidePanel :visible="userDialogVisible" :title="userDialogTitle"
               :panel-style="sidePanel.styleFor(520)"
               @close="userDialogVisible = false" @resize="sidePanel.startResize">
      <el-form label-position="top">
        <el-form-item label="登录名（创建后不可改）">
          <el-input v-model="userForm.name" :disabled="!!userForm.id" placeholder="字母/数字/下划线/横线，≤64" />
        </el-form-item>
        <el-form-item label="显示名">
          <el-input v-model="userForm.display_name" />
        </el-form-item>
        <el-form-item label="邮箱">
          <el-input v-model="userForm.email" />
        </el-form-item>
        <el-form-item label="手机号">
          <el-input v-model="userForm.phone" />
        </el-form-item>
        <el-form-item label="角色">
          <el-radio-group v-model="userForm.role">
            <el-radio value="manage">管理用户（可登录控制台）</el-radio>
            <el-radio value="service">服务用户（不可登录，仅 API 身份）</el-radio>
          </el-radio-group>
        </el-form-item>
        <el-form-item v-if="userForm.role === 'manage'" :label="userForm.id ? '密码（留空表示不改）' : '密码'">
          <el-input v-model="userForm.password" type="password" show-password placeholder="至少 8 位" />
        </el-form-item>
        <el-form-item v-if="userForm.id" label="状态">
          <el-select v-model="userForm.status" style="width:180px" popper-class="settings-select-popper">
            <el-option label="正常" value="active" />
            <el-option label="停用" value="disabled" />
            <el-option label="锁定" value="locked" />
          </el-select>
        </el-form-item>
        <div class="editor-tip">
          服务用户不设密码（避免将来改成管理用户时“突然获得”登录能力）；改角色时后端会自动处理密码。
        </div>
      </el-form>
      <template #footer>
        <el-button @click="userDialogVisible = false">取消</el-button>
        <el-button type="primary" :loading="userSaving" @click="saveUser">保存</el-button>
      </template>
    </SidePanel>

    <!-- 用户：重置密码 -->
    <SidePanel :visible="resetPwdVisible"
               :title="`重置密码 · ${(resetPwdTarget && (resetPwdTarget.display_name || resetPwdTarget.name)) || ''}`"
               :panel-style="sidePanel.styleFor(420)"
               @close="resetPwdVisible = false" @resize="sidePanel.startResize">
      <el-input v-model="resetPwdValue" type="password" show-password placeholder="新密码（至少 8 位）" @keyup.enter="submitResetPwd" />
      <template #footer>
        <el-button @click="resetPwdVisible = false">取消</el-button>
        <el-button type="primary" @click="submitResetPwd">确定</el-button>
      </template>
    </SidePanel>

    <!-- 用户：单用户配额覆盖（会话数 / 单会话空间）。
         只写 user_quotas，不动 users；留空 = 跟随租户默认 -->
    <SidePanel :visible="quotaVisible" title="用户配额"
               :panel-style="sidePanel.styleFor(480)"
               @close="quotaVisible = false" @resize="sidePanel.startResize">
      <el-form label-position="top">
        <el-form-item label="设置方式">
          <el-radio-group v-model="quotaForm.mode">
            <el-radio value="follow">跟随租户默认</el-radio>
            <el-radio value="custom">自定义</el-radio>
          </el-radio-group>
        </el-form-item>
        <template v-if="quotaForm.mode === 'custom'">
          <el-form-item label="会话数上限（个）">
            <el-input v-model="quotaForm.sessions"
                      :placeholder="`留空 = 跟随租户（${quotaDefaults.sessionsText}）；-1 = 不限`" />
          </el-form-item>
          <el-form-item label="单会话空间上限（MiB）">
            <el-input v-model="quotaForm.storageMiB"
                      :placeholder="`留空 = 跟随租户（${quotaDefaults.storageText}）；-1 = 不限`" />
          </el-form-item>
        </template>
        <div class="quota-hint">
          不得超过租户上限：会话 {{ quotaDefaults.sessionsText }} / 空间 {{ quotaDefaults.storageText }}。
          任一框留空表示该项跟随租户；设为 0 表示该用户不能新建会话或写入文件。
        </div>
      </el-form>
      <template #footer>
        <el-button @click="quotaVisible = false">取消</el-button>
        <el-button type="primary" :loading="quotaSaving" @click="saveQuota">保存</el-button>
      </template>
    </SidePanel>
  </div>
</template>

<script setup>
import { computed, ref, watch } from 'vue'
import { ElMessage } from 'element-plus'
import { Plus, Refresh } from '@element-plus/icons-vue'
import apiConfig from '../../../config/api.js'
import { getToken } from '../../../utils/auth.js'
import SidePanel from '../SidePanel.vue'
import { useSidePanelContext } from '../useSidePanel.js'
import { useUsers } from '../useUsers.js'

const props = defineProps({
  active: { type: Boolean, default: false }
})

const { users, usersLoading, userQuotaDefaults, loadUsers } = useUsers()
const sidePanel = useSidePanelContext()

// ===== 根账号保护 =====
// 平台开通租户时创建的根账号（与后端 shared/tenants.PrimaryUserName 同值）：
// 不可编辑、不可停用；删除接口本就不存在。重置密码仍可用。
const PRIMARY_NAME = 'primary'
const isPrimaryRow = (row) => !!row && row.name === PRIMARY_NAME

const userStatusLabel = (s) =>
  s === 'active' ? '正常' : s === 'locked' ? '已锁定' : s === 'disabled' ? '已停用' : (s || '-')

// ===== 单用户配额（user_quotas 覆盖；无行 = 跟随租户） =====
const MIB = 1024 * 1024
const fmtBytes = (n) => {
  if (!n || n < 0) return '0 B'
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB']
  let v = n
  let i = 0
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return `${v >= 10 || i === 0 ? Math.round(v) : v.toFixed(1)} ${units[i]}`
}

// 租户级默认值（用户没配覆盖时生效）；-1 = 不限
const quotaDefaults = computed(() => {
  const d = userQuotaDefaults.value || {}
  const s = d.sessions ?? 50
  const b = d.session_storage_bytes ?? 0
  return {
    sessionsText: s === -1 ? '不限' : `${s} 个`,
    storageText: b === -1 ? '不限' : fmtBytes(b),
  }
})

const quotaSessionsText = (row) =>
  row.max_sessions == null
    ? `会话：跟随租户（${quotaDefaults.value.sessionsText}）`
    : `会话：自定义 ${row.max_sessions === -1 ? '不限' : `${row.max_sessions} 个`}`

const quotaStorageText = (row) =>
  row.max_session_storage_bytes == null
    ? `空间：跟随租户（${quotaDefaults.value.storageText}）`
    : `空间：自定义 ${row.max_session_storage_bytes === -1 ? '不限' : fmtBytes(row.max_session_storage_bytes)}`

const quotaVisible = ref(false)
const quotaSaving = ref(false)
const quotaTarget = ref(null)
const quotaForm = ref({ mode: 'follow', sessions: '', storageMiB: '' })

const openQuotaDialog = (row) => {
  if (isPrimaryRow(row)) {
    return ElMessage.warning('primary 是平台开通时创建的根账号，配额只能由平台调整')
  }
  quotaTarget.value = row
  const hasOverride = row.max_sessions != null || row.max_session_storage_bytes != null
  quotaForm.value = {
    mode: hasOverride ? 'custom' : 'follow',
    sessions: row.max_sessions == null ? '' : String(row.max_sessions),
    storageMiB:
      row.max_session_storage_bytes == null
        ? ''
        : row.max_session_storage_bytes === -1
          ? '-1'
          : String(Math.round(row.max_session_storage_bytes / MIB)),
  }
  quotaVisible.value = true
}

// 留空 = 该项跟随租户（提交 null）；-1 = 不限。两项都空 = 删除覆盖行。
const saveQuota = async () => {
  const row = quotaTarget.value
  if (!row) return
  const body = { max_sessions: null, max_session_storage_bytes: null }
  if (quotaForm.value.mode === 'custom') {
    const sRaw = String(quotaForm.value.sessions ?? '').trim()
    const mRaw = String(quotaForm.value.storageMiB ?? '').trim()
    if (sRaw === '' && mRaw === '') {
      return ElMessage.warning('自定义模式下至少填一项，或改回「跟随租户默认」')
    }
    if (sRaw !== '') {
      const n = Number(sRaw)
      if (!Number.isInteger(n) || n < -1) return ElMessage.warning('会话数需为 ≥ -1 的整数')
      body.max_sessions = n
    }
    if (mRaw !== '') {
      const n = Number(mRaw)
      if (!Number.isInteger(n) || n < -1) return ElMessage.warning('空间需为 ≥ -1 的整数（MiB）')
      body.max_session_storage_bytes = n === -1 ? -1 : n * MIB
    }
  }
  quotaSaving.value = true
  try {
    const token = getToken()
    const resp = await fetch(apiConfig.admin.users.quota(row.id), {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${token}` },
      body: JSON.stringify(body)
    })
    const data = await resp.json().catch(() => ({}))
    if (data.code !== 200) throw new Error(data.msg || '保存失败')
    ElMessage.success('配额已更新')
    quotaVisible.value = false
    await loadUsers()
  } catch (e) {
    ElMessage.error(e.message || '保存失败')
  } finally {
    quotaSaving.value = false
  }
}

// ===== 新建 / 编辑 =====
const userDialogVisible = ref(false)
const userDialogTitle = ref('新建用户')
const userSaving = ref(false)
const userForm = ref({ id: '', name: '', display_name: '', email: '', phone: '', role: 'manage', password: '', status: 'active' })
const resetPwdVisible = ref(false)
const resetPwdTarget = ref(null)
const resetPwdValue = ref('')

// 两个面板加入父级的互斥集合（点外部关闭、拖宽共用同一套坐标）
sidePanel.register(userDialogVisible)
sidePanel.register(resetPwdVisible)
sidePanel.register(quotaVisible)

watch(() => props.active, (active) => {
  if (!active) return
  loadUsers()
}, { immediate: true })

const openUserDialog = (row) => {
  if (isPrimaryRow(row)) return ElMessage.warning('primary 是平台开通时创建的根账号，不可修改')
  userDialogTitle.value = row ? '编辑用户' : '新建用户'
  userForm.value = row
    ? {
        id: row.id, name: row.name, display_name: row.display_name || '', email: row.email || '',
        phone: row.phone || '', role: row.role || 'manage', password: '', status: row.status || 'active'
      }
    : { id: '', name: '', display_name: '', email: '', phone: '', role: 'manage', password: '', status: 'active' }
  userDialogVisible.value = true
}

const saveUser = async () => {
  const f = userForm.value
  if (!f.name) return ElMessage.warning('请填写登录名')
  if (f.id && f.name === PRIMARY_NAME) return ElMessage.warning('primary 是平台开通时创建的根账号，不可修改')
  if (!f.id && f.role === 'manage' && (f.password || '').length < 8) {
    return ElMessage.warning('管理用户密码至少 8 位')
  }
  const isCreate = !f.id
  userSaving.value = true
  try {
    const token = getToken()
    const payload = isCreate
      ? {
          name: f.name, display_name: f.display_name, email: f.email, phone: f.phone,
          role: f.role, password: f.role === 'manage' ? f.password : ''
        }
      : {
          display_name: f.display_name, email: f.email, phone: f.phone,
          role: f.role, status: f.status, password: f.password || ''
        }
    const resp = await fetch(isCreate ? apiConfig.admin.users.create : apiConfig.admin.users.update(f.id), {
      method: isCreate ? 'POST' : 'PUT',
      headers: { 'Content-Type': 'application/json', 'Authorization': `Bearer ${token}` },
      body: JSON.stringify(payload)
    })
    const data = await resp.json().catch(() => ({}))
    if (data.code !== 200) throw new Error(data.msg || '保存失败')
    ElMessage.success({ message: isCreate ? '用户已创建' : '用户已更新', duration: 800 })
    userDialogVisible.value = false
    await loadUsers()
  } catch (e) {
    ElMessage.error(e.message || '保存失败')
  } finally {
    userSaving.value = false
  }
}

// 启停：整行字段一起提交（后端 PUT 要求 role / status 必填）
const toggleUserActive = async (row) => {
  if (isPrimaryRow(row)) return ElMessage.warning('primary 是平台开通时创建的根账号，不可停用/启用')
  const nextStatus = row.status === 'active' ? 'disabled' : 'active'
  try {
    const token = getToken()
    const resp = await fetch(apiConfig.admin.users.update(row.id), {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json', 'Authorization': `Bearer ${token}` },
      body: JSON.stringify({
        display_name: row.display_name || '', email: row.email || '', phone: row.phone || '',
        role: row.role, status: nextStatus
      })
    })
    const data = await resp.json().catch(() => ({}))
    if (data.code !== 200) throw new Error(data.msg || '操作失败')
    ElMessage.success({ message: nextStatus === 'active' ? '已启用' : '已停用', duration: 800 })
    await loadUsers()
  } catch (e) {
    ElMessage.error(e.message || '操作失败')
  }
}

const openResetPwd = (row) => {
  resetPwdTarget.value = row
  resetPwdValue.value = ''
  resetPwdVisible.value = true
}

const submitResetPwd = async () => {
  const pw = resetPwdValue.value || ''
  if (pw.length < 8) return ElMessage.warning('密码至少 8 位')
  try {
    const token = getToken()
    const resp = await fetch(apiConfig.admin.users.password(resetPwdTarget.value.id), {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', 'Authorization': `Bearer ${token}` },
      body: JSON.stringify({ password: pw })
    })
    const data = await resp.json().catch(() => ({}))
    if (data.code !== 200) throw new Error(data.msg || '重置失败')
    ElMessage.success({ message: '密码已重置', duration: 800 })
    resetPwdVisible.value = false
  } catch (e) {
    ElMessage.error(e.message || '重置失败')
  }
}

// 这里不再提供「生成 API Key」：新建的绑定默认 restrict_targets=true 且白名单为空
// （一个端点都调不到），而白名单只能在「设置 → 服务凭证」里配。
// 两处都能建、只有一处能配全，很容易产出"生成好了却用不了"的 key，所以入口统一收到绑定页。
</script>

<style scoped>
.module-section {
  background: #fff;
  border: 1px solid #e0e0e0;
  border-radius: 12px;
  padding: 20px 24px 24px;
}

/* 配额列：两行小字；有自定义值时用橙色，和「跟随租户」区分开 */
.quota-cell {
  display: flex;
  flex-direction: column;
  gap: 2px;
  font-size: 12px;
  line-height: 1.5;
  color: #909399;
}
.quota-cell .q-custom {
  color: #e6a23c;
}

.quota-hint {
  font-size: 12px;
  color: #909399;
  line-height: 1.6;
  margin-top: 4px;
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
  line-height: 1.5;
  color: #909399;
  margin-top: 6px;
}

/* ===== 表格卡片化（与端点页同一套观感）=====
   一行 = 一张卡片：去掉网格线与单元格分隔线，行间用上边框留白；
   表头是独立的白色卡片（两端圆角 + 轻阴影），整块浮在浅灰底上。 */
.ut-table {
  background: #f5f7fa;
  border-radius: 8px;
  box-shadow: 0 2px 8px rgba(15, 23, 42, 0.06);
}

.ut-table :deep(.el-table__body) {
  border-collapse: separate;
  border-spacing: 0;
}

/* 行高：small 尺寸默认只有 6px 内边距，一屏十几个用户会糊成一片 */
.ut-table :deep(.el-table__cell) {
  padding: 10px 0;
}

.ut-table :deep(.el-table__header .el-table__cell) {
  background: #fff;
  font-weight: 600;
  color: #303133;
  box-shadow: 0 1px 3px rgba(15, 23, 42, 0.05);
}

/* 表头只圆上面两角：下沿要与首行卡片贴齐，圆角会在缝隙处露出一块灰缺口 */
.ut-table :deep(.el-table__header tr > th.el-table__cell:first-child) {
  border-top-left-radius: 8px;
}

.ut-table :deep(.el-table__header tr > th.el-table__cell:last-child) {
  border-top-right-radius: 8px;
}

/* 卡片本体：白底、无底边线；上边框与容器同色，形成 4px 行间缝隙 */
.ut-table :deep(.el-table__body .el-table__row > td.el-table__cell) {
  background-color: #fff;
  border-bottom: none;
  border-top: 4px solid #f5f7fa;
}

/* 最后一行补圆角，表格底部不至于是一条硬白边 */
.ut-table :deep(.el-table__body tr:last-child > td.el-table__cell:first-child) {
  border-bottom-left-radius: 8px;
}

.ut-table :deep(.el-table__body tr:last-child > td.el-table__cell:last-child) {
  border-bottom-right-radius: 8px;
}

/* 悬停整行提亮：操作按钮都在这行，鼠标扫过要能确认落在哪一条 */
.ut-table :deep(.el-table__body .el-table__row:hover > td.el-table__cell) {
  background-color: #f0f6ff;
}
</style>
