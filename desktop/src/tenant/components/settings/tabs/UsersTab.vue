<template>
  <div class="module-section">
    <div class="module-head">
      <h3>{{ t('settings.tabs.users.title') }}</h3>
      <div class="head-actions">
        <el-button size="small" :icon="Refresh" @click="loadUsers">{{ t('settings.users.refresh') }}</el-button>
        <el-button type="primary" size="small" :icon="Plus" @click="openUserDialog()">{{ t('settings.users.create') }}</el-button>
      </div>
    </div>

    <div class="editor-tip" style="margin-bottom:10px">
      <strong>{{ t('settings.users.introRoleManage') }}</strong>{{ t('settings.users.introRoleManageRest') }}<strong>{{ t('settings.users.introRoleService') }}</strong>{{ t('settings.users.introRoleServiceRest') }}
      <br />
      {{ t('settings.users.introQuota') }}
    </div>

    <el-table :data="users" v-loading="usersLoading" size="small" class="ut-table">
      <el-table-column :label="t('settings.users.colLogin')" min-width="150">
        <template #default="{ row }">
          <div>
            {{ row.name }}
            <!-- 平台开通租户时创建的根账号：不可编辑 / 不可停用（后端同样拦；删除接口本就不存在） -->
            <el-tag v-if="isPrimaryRow(row)" size="small" type="warning" effect="plain">{{ t('settings.users.rootTag') }}</el-tag>
          </div>
          <div style="font-size:12px;color:#909399">{{ row.display_name || '-' }}</div>
        </template>
      </el-table-column>
      <el-table-column :label="t('settings.users.colRole')" width="110">
        <template #default="{ row }">
          <el-tag size="small" :type="row.role === 'manage' ? 'primary' : 'info'">
            {{ row.role === 'manage' ? t('settings.users.roleManage')
              : row.role === 'service' ? t('settings.users.roleService') : (row.role || '-') }}
          </el-tag>
        </template>
      </el-table-column>
      <el-table-column :label="t('settings.users.colStatus')" width="100">
        <template #default="{ row }">
          <el-tag size="small"
                  :type="row.status === 'active' ? 'success' : (row.status === 'locked' ? 'warning' : 'danger')">
            {{ userStatusLabel(row.status) }}
          </el-tag>
        </template>
      </el-table-column>
      <el-table-column :label="t('settings.users.colQuota')" min-width="200">
        <template #default="{ row }">
          <div class="quota-cell">
            <span :class="{ 'q-custom': row.max_sessions != null }">{{ quotaSessionsText(row) }}</span>
            <span :class="{ 'q-custom': row.max_session_storage_bytes != null }">{{ quotaStorageText(row) }}</span>
          </div>
        </template>
      </el-table-column>
      <el-table-column :label="t('settings.users.colEmail')" min-width="180" prop="email" />
      <el-table-column :label="t('settings.users.colActions')" width="320">
        <template #default="{ row }">
          <el-button link type="primary" size="small"
                     :disabled="isPrimaryRow(row)"
                     :title="isPrimaryRow(row) ? t('settings.users.primaryNoEditHint') : ''"
                     @click="openUserDialog(row)">{{ t('settings.users.edit') }}</el-button>
          <el-button link type="primary" size="small"
                     :disabled="isPrimaryRow(row)"
                     :title="isPrimaryRow(row) ? t('settings.users.primaryQuotaHint') : t('settings.users.quotaActionHint')"
                     @click="openQuotaDialog(row)">{{ t('settings.users.quotaAction') }}</el-button>
          <el-button v-if="row.role === 'manage'" link type="warning" size="small"
                     @click="openResetPwd(row)">{{ t('settings.users.resetPwd') }}</el-button>
          <el-button link :type="row.status === 'active' ? 'info' : 'success'" size="small"
                     :disabled="isPrimaryRow(row)"
                     :title="isPrimaryRow(row) ? t('settings.users.primaryDisableHint') : ''"
                     @click="toggleUserActive(row)">
            {{ row.status === 'active' ? t('settings.users.disable') : t('settings.users.enable') }}
          </el-button>
        </template>
      </el-table-column>
    </el-table>

    <!-- 用户：新建 / 编辑 -->
    <SidePanel :visible="userDialogVisible" :title="userDialogTitle"
               :panel-style="sidePanel.styleFor(520)"
               @close="userDialogVisible = false" @resize="sidePanel.startResize">
      <el-form label-position="top">
        <el-form-item :label="t('settings.users.loginLabel')">
          <el-input v-model="userForm.name" :disabled="!!userForm.id" :placeholder="t('settings.users.loginPlaceholder')" />
        </el-form-item>
        <el-form-item :label="t('settings.users.displayName')">
          <el-input v-model="userForm.display_name" />
        </el-form-item>
        <el-form-item :label="t('settings.users.email')">
          <el-input v-model="userForm.email" />
        </el-form-item>
        <el-form-item :label="t('settings.users.phone')">
          <el-input v-model="userForm.phone" />
        </el-form-item>
        <el-form-item :label="t('settings.users.roleLabel')">
          <el-radio-group v-model="userForm.role">
            <el-radio value="manage">{{ t('settings.users.roleManageRadio') }}</el-radio>
            <el-radio value="service">{{ t('settings.users.roleServiceRadio') }}</el-radio>
          </el-radio-group>
        </el-form-item>
        <el-form-item v-if="userForm.role === 'manage'"
                      :label="userForm.id ? t('settings.users.passwordEditLabel') : t('settings.users.passwordLabel')">
          <el-input v-model="userForm.password" type="password" show-password
                    :placeholder="t('settings.users.passwordPlaceholder')" />
        </el-form-item>
        <el-form-item v-if="userForm.id" :label="t('settings.users.statusLabel')">
          <el-select v-model="userForm.status" style="width:180px" popper-class="settings-select-popper">
            <el-option :label="t('settings.users.statusNormal')" value="active" />
            <el-option :label="t('settings.users.statusDisabledOpt')" value="disabled" />
            <el-option :label="t('settings.users.statusLockedOpt')" value="locked" />
          </el-select>
        </el-form-item>
        <div class="editor-tip">
          {{ t('settings.users.serviceUserTip') }}
        </div>
      </el-form>
      <template #footer>
        <el-button @click="userDialogVisible = false">{{ t('settings.users.cancel') }}</el-button>
        <el-button type="primary" :loading="userSaving" @click="saveUser">{{ t('settings.users.save') }}</el-button>
      </template>
    </SidePanel>

    <!-- 用户：重置密码 -->
    <SidePanel :visible="resetPwdVisible"
               :title="t('settings.users.resetPwdTitle', { name: resetPwdTargetName })"
               :panel-style="sidePanel.styleFor(420)"
               @close="resetPwdVisible = false" @resize="sidePanel.startResize">
      <el-input v-model="resetPwdValue" type="password" show-password
                :placeholder="t('settings.users.resetPwdPlaceholder')" @keyup.enter="submitResetPwd" />
      <template #footer>
        <el-button @click="resetPwdVisible = false">{{ t('settings.users.cancel') }}</el-button>
        <el-button type="primary" @click="submitResetPwd">{{ t('settings.users.confirm') }}</el-button>
      </template>
    </SidePanel>

    <!-- 用户：单用户配额覆盖（会话数 / 单会话空间）。
         只写 user_quotas，不动 users；留空 = 跟随租户默认 -->
    <SidePanel :visible="quotaVisible" :title="t('settings.users.quotaPanelTitle')"
               :panel-style="sidePanel.styleFor(480)"
               @close="quotaVisible = false" @resize="sidePanel.startResize">
      <el-form label-position="top">
        <el-form-item :label="t('settings.users.quotaModeLabel')">
          <el-radio-group v-model="quotaForm.mode">
            <el-radio value="follow">{{ t('settings.users.modeFollow') }}</el-radio>
            <el-radio value="custom">{{ t('settings.users.modeCustom') }}</el-radio>
          </el-radio-group>
        </el-form-item>
        <template v-if="quotaForm.mode === 'custom'">
          <el-form-item :label="t('settings.users.sessionsLabel')">
            <el-input v-model="quotaForm.sessions"
                      :placeholder="t('settings.users.sessionsPlaceholder', { text: quotaDefaults.sessionsText })" />
          </el-form-item>
          <el-form-item :label="t('settings.users.storageLabel')">
            <el-input v-model="quotaForm.storageMiB"
                      :placeholder="t('settings.users.storagePlaceholder', { text: quotaDefaults.storageText })" />
          </el-form-item>
        </template>
        <div class="quota-hint">
          {{ t('settings.users.quotaHint', { sessions: quotaDefaults.sessionsText, storage: quotaDefaults.storageText }) }}
        </div>
      </el-form>
      <template #footer>
        <el-button @click="quotaVisible = false">{{ t('settings.users.cancel') }}</el-button>
        <el-button type="primary" :loading="quotaSaving" @click="saveQuota">{{ t('settings.users.save') }}</el-button>
      </template>
    </SidePanel>
  </div>
</template>

<script setup>
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { ElMessage } from 'element-plus'
import { Plus, Refresh } from '@element-plus/icons-vue'
import apiConfig from '../../../config/api.js'
import { getToken } from '../../../utils/auth.js'
import SidePanel from '../SidePanel.vue'
import { useSidePanelContext } from '../useSidePanel.js'
import { useUsers } from '../useUsers.js'
import { formatError } from '../../../../locales'

const props = defineProps({
  active: { type: Boolean, default: false }
})

const { users, usersLoading, userQuotaDefaults, loadUsers } = useUsers()
const sidePanel = useSidePanelContext()

const { t } = useI18n()

// ===== 根账号保护 =====
// 平台开通租户时创建的根账号（与后端 shared/tenants.PrimaryUserName 同值）：
// 不可编辑、不可停用；删除接口本就不存在。重置密码仍可用。
const PRIMARY_NAME = 'primary'
const isPrimaryRow = (row) => !!row && row.name === PRIMARY_NAME

const userStatusLabel = (s) => {
  if (s === 'active') return t('settings.users.statusActive')
  if (s === 'locked') return t('settings.users.statusLocked')
  if (s === 'disabled') return t('settings.users.statusDisabled')
  return s || '-'
}

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
    sessionsText: s === -1 ? t('settings.users.unlimited') : t('settings.users.countUnit', { n: s }),
    storageText: b === -1 ? t('settings.users.unlimited') : fmtBytes(b),
  }
})

const quotaLimitText = (v) => (v === -1 ? t('settings.users.unlimited') : t('settings.users.countUnit', { n: v }))

const quotaSessionsText = (row) =>
  row.max_sessions == null
    ? t('settings.users.sessionsFollow', { text: quotaDefaults.value.sessionsText })
    : t('settings.users.sessionsCustom', { text: quotaLimitText(row.max_sessions) })

const quotaStorageText = (row) =>
  row.max_session_storage_bytes == null
    ? t('settings.users.storageFollow', { text: quotaDefaults.value.storageText })
    : t('settings.users.storageCustom', {
      text: row.max_session_storage_bytes === -1
        ? t('settings.users.unlimited')
        : fmtBytes(row.max_session_storage_bytes)
    })

const quotaVisible = ref(false)
const quotaSaving = ref(false)
const quotaTarget = ref(null)
const quotaForm = ref({ mode: 'follow', sessions: '', storageMiB: '' })

const openQuotaDialog = (row) => {
  if (isPrimaryRow(row)) {
    return ElMessage.warning(t('settings.users.primaryQuotaOnly'))
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
      return ElMessage.warning(t('settings.users.customAtLeastOne'))
    }
    if (sRaw !== '') {
      const n = Number(sRaw)
      if (!Number.isInteger(n) || n < -1) return ElMessage.warning(t('settings.users.sessionsInvalid'))
      body.max_sessions = n
    }
    if (mRaw !== '') {
      const n = Number(mRaw)
      if (!Number.isInteger(n) || n < -1) return ElMessage.warning(t('settings.users.storageInvalid'))
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
    if (data.code !== 200) throw new Error(formatError(data) || t('settings.users.saveFailed'))
    ElMessage.success(t('settings.users.quotaSaved'))
    quotaVisible.value = false
    await loadUsers()
  } catch (e) {
    ElMessage.error(e.message || t('settings.users.saveFailed'))
  } finally {
    quotaSaving.value = false
  }
}

// ===== 新建 / 编辑 =====
const userDialogVisible = ref(false)
const userSaving = ref(false)
const userForm = ref({ id: '', name: '', display_name: '', email: '', phone: '', role: 'manage', password: '', status: 'active' })
// 标题跟着表单判断（有 id = 编辑）：做成 computed，切语言时才跟着变
const userDialogTitle = computed(() =>
  userForm.value.id ? t('settings.users.dialogEdit') : t('settings.users.dialogCreate'))
const resetPwdVisible = ref(false)
const resetPwdTarget = ref(null)
const resetPwdValue = ref('')
const resetPwdTargetName = computed(() => {
  const row = resetPwdTarget.value
  return row ? (row.display_name || row.name) : ''
})

// 两个面板加入父级的互斥集合（点外部关闭、拖宽共用同一套坐标）
sidePanel.register(userDialogVisible)
sidePanel.register(resetPwdVisible)
sidePanel.register(quotaVisible)

watch(() => props.active, (active) => {
  if (!active) return
  loadUsers()
}, { immediate: true })

const openUserDialog = (row) => {
  if (isPrimaryRow(row)) return ElMessage.warning(t('settings.users.primaryNoEdit'))
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
  if (!f.name) return ElMessage.warning(t('settings.users.fillLogin'))
  if (f.id && f.name === PRIMARY_NAME) return ElMessage.warning(t('settings.users.primaryNoEdit'))
  if (!f.id && f.role === 'manage' && (f.password || '').length < 8) {
    return ElMessage.warning(t('settings.users.managePwdMin'))
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
    if (data.code !== 200) throw new Error(formatError(data) || t('settings.users.saveFailed'))
    ElMessage.success({
      message: isCreate ? t('settings.users.userCreated') : t('settings.users.userUpdated'),
      duration: 800
    })
    userDialogVisible.value = false
    await loadUsers()
  } catch (e) {
    ElMessage.error(e.message || t('settings.users.saveFailed'))
  } finally {
    userSaving.value = false
  }
}

// 启停：整行字段一起提交（后端 PUT 要求 role / status 必填）
const toggleUserActive = async (row) => {
  if (isPrimaryRow(row)) return ElMessage.warning(t('settings.users.primaryNoToggle'))
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
    if (data.code !== 200) throw new Error(formatError(data) || t('settings.users.opFailed'))
    ElMessage.success({
      message: nextStatus === 'active' ? t('settings.users.enabled') : t('settings.users.disabled'),
      duration: 800
    })
    await loadUsers()
  } catch (e) {
    ElMessage.error(e.message || t('settings.users.opFailed'))
  }
}

const openResetPwd = (row) => {
  resetPwdTarget.value = row
  resetPwdValue.value = ''
  resetPwdVisible.value = true
}

const submitResetPwd = async () => {
  const pw = resetPwdValue.value || ''
  if (pw.length < 8) return ElMessage.warning(t('settings.users.pwdMin'))
  try {
    const token = getToken()
    const resp = await fetch(apiConfig.admin.users.password(resetPwdTarget.value.id), {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', 'Authorization': `Bearer ${token}` },
      body: JSON.stringify({ password: pw })
    })
    const data = await resp.json().catch(() => ({}))
    if (data.code !== 200) throw new Error(formatError(data) || t('settings.users.resetFailed'))
    ElMessage.success({ message: t('settings.users.pwdReset'), duration: 800 })
    resetPwdVisible.value = false
  } catch (e) {
    ElMessage.error(e.message || t('settings.users.resetFailed'))
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
