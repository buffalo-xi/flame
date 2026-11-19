<template>
  <div class="module-section">
    <div class="module-head">
      <h3>{{ title }}</h3>
      <div class="head-actions">
        <el-button size="small" :icon="Refresh" :loading="loading" @click="fetchConnections">{{ t('settings.connection.refresh') }}</el-button>
        <el-button type="primary" size="small" :icon="Plus" @click="openAddDialog">{{ t('settings.connection.add') }}</el-button>
      </div>
    </div>

    <div v-loading="loading" class="card-grid">
      <div v-for="s in connections" :key="s.id" class="item-card">
        <div class="item-card-head">
          <span class="item-name text-ellipsis">{{ s.display_name || s.name }}</span>
          <div class="item-actions">
            <el-button
              v-if="s.is_active"
              link
              type="warning"
              :icon="Refresh"
              :loading="s._refreshing"
              :title="t('settings.connection.refreshTools')"
              @click="refreshTools(s)"
            />
            <el-button
              v-if="s.is_active"
              link
              type="danger"
              :icon="Close"
              :title="t('settings.connection.disable')"
              @click="setEnabled(s, false)"
            />
            <el-button
              v-else
              link
              type="success"
              :icon="Check"
              :title="t('settings.connection.enable')"
              @click="setEnabled(s, true)"
            />
            <el-button link type="primary" :icon="Edit" @click="openEditDialog(s)" />
            <el-button link type="danger" :icon="Delete" @click="removeConnection(s)" />
          </div>
        </div>
        <div class="item-tags">
          <el-tag size="small" :type="s.is_active ? 'success' : 'info'" effect="light">
            {{ s.is_active ? t('settings.connection.enabled') : t('settings.connection.disabled') }}
          </el-tag>
          <el-tag size="small" type="primary" effect="plain">{{ transportLabel(s.transport) }}</el-tag>
          <el-tag v-if="hasAuthTag(s.auth)" size="small" type="success" effect="plain">{{ authTypeLabel(s.auth) }}</el-tag>
          <el-tag v-if="s._toolCount !== undefined" size="small" type="warning" effect="plain">
            {{ t('settings.connection.toolCount', { n: s._toolCount }) }}
          </el-tag>
        </div>
        <!-- streamable_http 显示 URL -->
        <div v-if="s.transport === 'streamable_http' && s.url" class="item-meta">
          <span class="meta-label">{{ t('settings.connection.labelUrl') }}</span>
          <span class="meta-value text-ellipsis">{{ s.url }}</span>
        </div>
        <!-- stdio 显示 Command 和 Args -->
        <div v-if="s.transport === 'stdio' && s.command" class="item-meta">
          <span class="meta-label">{{ t('settings.connection.labelCommand') }}</span>
          <span class="meta-value text-ellipsis">{{ s.command }} {{ formatArgs(s.args) }}</span>
        </div>
        <!-- stdio 显示 WorkDir 和 Env（脱敏） -->
        <div v-if="s.transport === 'stdio' && s.work_dir" class="item-meta">
          <span class="meta-label">{{ t('settings.connection.labelWorkDir') }}</span>
          <span class="meta-value text-ellipsis">{{ s.work_dir }}</span>
        </div>
        <div v-if="s.transport === 'stdio' && s.env && s.env.length > 0" class="item-meta">
          <span class="meta-label">{{ t('settings.connection.labelEnv') }}</span>
          <span class="meta-value text-ellipsis">{{ s.env.map(e => maskEnv(e)).join('  ') }}</span>
        </div>
        <div v-if="s.description" class="item-note">{{ s.description }}</div>
        <!-- 工具预览（可展开） -->
        <div v-if="s._tools && s._tools.length > 0" class="item-tools-preview">
          <el-button link size="small" type="primary" @click="s._toolsExpanded = !s._toolsExpanded">
            <el-icon><ArrowRight :class="{ 'rotate-90': s._toolsExpanded }" /></el-icon>
            {{ s._toolsExpanded ? t('settings.connection.collapseTools') : t('settings.connection.expandTools') }}
          </el-button>
          <div v-if="s._toolsExpanded" class="tools-list">
            <div v-for="(tool, ti) in s._tools.slice(0, 10)" :key="ti" class="tool-item">
              <span class="tool-name">{{ tool.name }}</span>
              <span v-if="tool.description" class="tool-desc">{{ tool.description }}</span>
            </div>
            <div v-if="s._tools.length > 10" class="tool-more">
              {{ t('settings.connection.moreTools', { n: s._tools.length - 10 }) }}
            </div>
          </div>
        </div>
        <div class="item-meta">
          <span class="meta-label">{{ t('settings.connection.labelCreated') }}</span>
          <span class="meta-value">{{ formatDateTime(s.created_at) }}</span>
        </div>
      </div>

      <div v-if="!loading && connections.length === 0" class="empty-block">
        <el-empty :description="t('settings.connection.empty', { title })">
          <el-button type="primary" @click="openAddDialog">{{ t('settings.connection.add') }}</el-button>
        </el-empty>
      </div>
    </div>

    <SidePanel :visible="dialogVisible" :title="dialogTitle"
               :panel-style="sidePanel.styleFor(580)"
               @close="dialogVisible = false" @resize="sidePanel.startResize">
      <el-form label-position="top">
        <el-form-item :label="t('settings.connection.nameLabel')" required>
          <el-input v-model="settings.name" :placeholder="t('settings.connection.namePlaceholder')" />
        </el-form-item>
        <el-form-item :label="t('settings.connection.displayName')">
          <el-input v-model="settings.displayName" :placeholder="t('settings.connection.displayNamePlaceholder')" />
        </el-form-item>
        <el-form-item :label="t('settings.connection.transportLabel')" required>
          <el-select v-model="settings.transport" :placeholder="t('settings.connection.transportPlaceholder')"
                     popper-class="settings-select-popper" @change="onTransportChange">
            <el-option :label="t('settings.connection.transportHttp')" value="streamable_http" />
            <el-option :label="t('settings.connection.transportStdio')" value="stdio" />
          </el-select>
        </el-form-item>
        <!-- streamable_http 字段 -->
        <el-form-item v-if="settings.transport === 'streamable_http'" :label="t('settings.connection.urlLabel')" required>
          <el-input v-model="settings.url" :placeholder="urlPlaceholder" />
        </el-form-item>
        <!-- stdio 字段 -->
        <template v-if="settings.transport === 'stdio'">
          <el-form-item :label="t('settings.connection.commandLabel')" required>
            <el-input v-model="settings.command" :placeholder="t('settings.connection.commandPlaceholder')" />
          </el-form-item>
          <el-form-item :label="t('settings.connection.argsLabel')">
            <div class="args-editor">
              <div v-for="(arg, idx) in settings._argsList" :key="idx" class="arg-row">
                <el-input v-model="settings._argsList[idx]" :placeholder="t('settings.connection.argPlaceholder')" size="small" />
                <el-button link type="danger" :icon="Delete" @click="removeArg(idx)" />
              </div>
              <el-button link type="primary" size="small" @click="addArg">
                <el-icon><Plus /></el-icon> {{ t('settings.connection.addArg') }}
              </el-button>
            </div>
          </el-form-item>
          <el-form-item :label="t('settings.connection.workDirLabel')">
            <el-input v-model="settings.work_dir" :placeholder="t('settings.connection.workDirPlaceholder')" />
          </el-form-item>
          <el-form-item :label="t('settings.connection.envLabel')">
            <div class="args-editor">
              <div v-for="(env, idx) in settings._envList" :key="idx" class="arg-row">
                <el-input v-model="settings._envList[idx]" :placeholder="t('settings.connection.envPlaceholder')" size="small" />
                <el-button link type="danger" :icon="Delete" @click="removeEnv(idx)" />
              </div>
              <el-button link type="primary" size="small" @click="addEnv">
                <el-icon><Plus /></el-icon> {{ t('settings.connection.addEnv') }}
              </el-button>
            </div>
            <div class="form-hint">
              {{ t('settings.connection.envHint1') }} <code>KEY=VALUE</code> {{ t('settings.connection.envHint2') }}
            </div>
          </el-form-item>
        </template>
        <el-form-item :label="t('settings.connection.descLabel')">
          <el-input type="textarea" v-model="settings.description"
                    :placeholder="t('settings.connection.descPlaceholder')" :rows="2" />
        </el-form-item>
        <!-- 鉴权区块：stdio 为本地管道无 HTTP 层，不使用鉴权，凭证走 env -->
        <el-form-item v-if="settings.transport !== 'stdio'" :label="t('settings.connection.authLabel')">
          <el-select v-model="settings.authType" :placeholder="t('settings.connection.authPlaceholder')"
                     popper-class="settings-select-popper" @change="onAuthTypeChange">
            <el-option :label="t('settings.connection.authNone')" value="none" />
            <el-option :label="t('settings.connection.authBearer')" value="bearer" />
            <el-option :label="t('settings.connection.authOauth')" value="oauth" />
          </el-select>
        </el-form-item>
        <!-- Bearer Token 字段 -->
        <template v-if="settings.authType === 'bearer' && settings.transport !== 'stdio'">
          <el-form-item :label="t('settings.connection.tokenLabel')">
            <el-input v-model="settings.authToken" :placeholder="t('settings.connection.tokenPlaceholder')">
              <template #suffix>
                <el-button link :icon="settings._showToken ? Hide : View" @click="settings._showToken = !settings._showToken" />
              </template>
            </el-input>
          </el-form-item>
        </template>
        <!-- OAuth 字段 -->
        <template v-if="settings.authType === 'oauth' && settings.transport !== 'stdio'">
          <el-form-item :label="t('settings.connection.clientId')">
            <el-input v-model="settings.authClientId" :placeholder="t('settings.connection.clientIdPlaceholder')" />
          </el-form-item>
          <el-form-item :label="t('settings.connection.clientSecret')">
            <el-input v-model="settings.authClientSecret" :placeholder="t('settings.connection.clientSecretPlaceholder')">
              <template #suffix>
                <el-button link :icon="settings._showSecret ? Hide : View" @click="settings._showSecret = !settings._showSecret" />
              </template>
            </el-input>
          </el-form-item>
          <el-form-item v-if="withTokenUrl" :label="t('settings.connection.tokenUrl')">
            <el-input v-model="settings.authTokenUrl" :placeholder="t('settings.connection.tokenUrlPlaceholder')" />
          </el-form-item>
        </template>
        <!-- 自定义 Headers -->
        <el-form-item v-if="settings.transport !== 'stdio'" :label="t('settings.connection.headersLabel')">
          <div class="headers-editor">
            <div v-for="(h, idx) in settings._headersList" :key="idx" class="header-row">
              <el-input v-model="h.key" :placeholder="t('settings.connection.headerKeyPlaceholder')" size="small" />
              <el-input v-model="h.value" :placeholder="t('settings.connection.headerValuePlaceholder')" size="small" />
              <el-button link type="danger" :icon="Delete" @click="removeHeader(idx)" />
            </div>
            <el-button link type="primary" size="small" @click="addHeader">
              <el-icon><Plus /></el-icon> {{ t('settings.connection.addHeader') }}
            </el-button>
          </div>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dialogVisible = false">{{ t('settings.connection.cancel') }}</el-button>
        <el-button type="primary" @click="saveConnection">{{ t('settings.connection.save') }}</el-button>
      </template>
    </SidePanel>
  </div>
</template>

<script setup>
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Refresh, Plus, Edit, Delete, Close, Check, ArrowRight, Hide, View } from '@element-plus/icons-vue'
import SidePanel from '../SidePanel.vue'
import { useSidePanelContext } from '../useSidePanel.js'
import { authHeaders, checkBizOk } from '../http.js'
import { maskEnv } from '../format.js'
import { formatDateTime } from '@/utils/datetime.js'

/**
 * 客户端连接管理（MCP / RPC 各一份）。
 *
 * 两边的列表、面板、transport 切换、auth 编辑、工具预览都是同一套交互，唯一差异是：
 * 接口地址、URL 占位文案、以及 RPC 的 OAuth 额外需要 Token URL。
 * 所以做成一个组件，差异用 props 表达 —— 抄两份的话，「切 transport 要清空另一组字段」
 * 这类规则迟早有一边漏改。
 */
const props = defineProps({
  active: { type: Boolean, default: false },
  // 'mcp' | 'rpc'：仅用于文案
  kind: { type: String, required: true },
  title: { type: String, required: true },
  // apiConfig.admin.mcpConnections / rpcConnections
  api: { type: Object, required: true },
  urlPlaceholder: { type: String, default: '' },
  // RPC 的 OAuth 需要 token_url，MCP 不需要
  withTokenUrl: { type: Boolean, default: false }
})

// 左侧导航显示「MCP 连接 / RPC 连接」条数，由本组件加载后回报
const emit = defineEmits(['count'])

const sidePanel = useSidePanelContext()

const { t } = useI18n()

const connections = ref([])
const loading = ref(false)
const dialogVisible = ref(false)
const current = ref(null)
// 标题跟着 current 判断（有值 = 编辑）：做成 computed，切语言时才跟着变
const dialogTitle = computed(() =>
  current.value
    ? t('settings.connection.dialogEdit', { title: props.title })
    : t('settings.connection.dialogAdd', { title: props.title }))

sidePanel.register(dialogVisible)

const emptySettings = () => ({
  name: '',
  displayName: '',
  transport: 'streamable_http',
  url: '',
  command: '',
  work_dir: '',
  description: '',
  // auth UI 字段
  authType: 'none',
  authToken: '',
  authClientId: '',
  authClientSecret: '',
  authTokenUrl: '',
  // UI 辅助
  _argsList: [],
  _envList: [],
  _headersList: [],
  _showToken: false,
  _showSecret: false
})

const settings = ref(emptySettings())

// ===== 展示辅助 =====
const transportLabel = (tr) => {
  if (tr === 'stdio') return t('settings.connection.transportLabelStdio')
  return t('settings.connection.transportLabelHttp')
}

const authTypeLabel = (auth) => {
  if (!auth) return ''
  const obj = typeof auth === 'string' ? (() => { try { return JSON.parse(auth) } catch { return {} } })() : auth
  if (obj.type === 'bearer') return t('settings.connection.authBearer')
  if (obj.type === 'oauth') return t('settings.connection.authOauth')
  return t('settings.connection.authConfigured')
}

const hasAuthTag = (auth) => {
  if (!auth) return false
  const obj = typeof auth === 'string' ? (() => { try { return JSON.parse(auth) } catch { return null } })() : auth
  if (!obj) return false
  if (obj.type === 'none' || obj.type === 'noauth') return false
  return !!(obj.type || obj.token || obj.client_id)
}

const formatArgs = (args) => {
  if (!args || !Array.isArray(args)) return ''
  return args.join(' ')
}



// ===== 列表 =====
const fetchConnections = async () => {
  loading.value = true
  try {
    const response = await fetch(props.api.base, {
      method: 'POST',
      headers: authHeaders(true),
      body: JSON.stringify({ page: 1, limit: -1, sort_by: ['name'], sort_order: ['desc'] })
    })
    if (!response.ok) throw new Error(t('settings.connection.loadFailed', { title: props.title }))
    const data = await response.json()
    const items = data?.data?.items || []
    // 为每个连接附加 UI 状态
    items.forEach((s) => {
      s._tools = []
      s._toolCount = undefined
      s._toolsExpanded = false
      s._refreshing = false
    })
    connections.value = items
    emit('count', items.length)
    // 异步加载每个连接的工具数量
    items.forEach((s) => fetchToolCount(s))
  } catch (error) {
    ElMessage.error({
      message: t('settings.connection.loadFailedDetail', { title: props.title, msg: error.message }),
      duration: 5000,
      offset: 20,
      showClose: true
    })
    console.error(`load ${props.title} failed:`, error)
  } finally {
    loading.value = false
  }
}

watch(() => props.active, (active) => {
  if (active) fetchConnections()
}, { immediate: true })

const fetchToolCount = async (conn) => {
  try {
    const response = await fetch(props.api.tools(conn.id), { headers: authHeaders() })
    if (response.ok) {
      const data = await response.json()
      const tools = data?.data?.items || data?.data || []
      conn._tools = Array.isArray(tools) ? tools : []
      conn._toolCount = conn._tools.length
    }
  } catch (e) {
    conn._toolCount = undefined
  }
}

const refreshTools = async (conn) => {
  conn._refreshing = true
  try {
    const response = await fetch(props.api.refreshTools(conn.id), {
      method: 'POST',
      headers: authHeaders()
    })
    if (!response.ok) throw new Error(t('settings.connection.refreshFailed'))
    await checkBizOk(response)
    ElMessage.success({ message: t('settings.connection.toolsRefreshed'), duration: 1000 })
    await fetchToolCount(conn)
  } catch (error) {
    ElMessage.error({
      message: t('settings.connection.refreshToolsFailed', { msg: error.message }),
      duration: 3000
    })
  } finally {
    conn._refreshing = false
  }
}

// 卡片按钮调用：轻量状态切换并刷新工具
const setEnabled = async (conn, enabled) => {
  try {
    const response = await fetch(`${props.api.update}${conn.id}/enabled`, {
      method: 'PUT',
      headers: authHeaders(true),
      body: JSON.stringify({ is_active: enabled })
    })
    if (!response.ok) throw new Error(t('settings.connection.netError'))
    await checkBizOk(response)
    conn.is_active = enabled
    await fetchToolCount(conn)
    ElMessage.success(enabled ? t('settings.connection.enabledAndRefreshed') : t('settings.connection.disabled'))
  } catch (error) {
    ElMessage.error({ message: t('settings.connection.opFailed', { msg: error.message }), duration: 3000 })
  }
}

// ===== 编辑面板 =====
// transport 切换时清空无关字段
const onTransportChange = () => {
  const s = settings.value
  if (s.transport === 'streamable_http') {
    s.command = ''
    s.work_dir = ''
    s._argsList = []
    s._envList = []
  } else {
    s.url = ''
    s.authType = 'none'
    s.authToken = ''
    s.authClientId = ''
    s.authClientSecret = ''
    s.authTokenUrl = ''
    s._headersList = []
  }
}

// auth type 切换时清空无关字段
const onAuthTypeChange = () => {
  const s = settings.value
  s.authToken = ''
  s.authClientId = ''
  s.authClientSecret = ''
  s.authTokenUrl = ''
  s._headersList = []
  s._showToken = false
  s._showSecret = false
}

const addArg = () => { settings.value._argsList.push('') }
const removeArg = (idx) => { settings.value._argsList.splice(idx, 1) }
const addEnv = () => { settings.value._envList.push('') }
const removeEnv = (idx) => { settings.value._envList.splice(idx, 1) }
const addHeader = () => { settings.value._headersList.push({ key: '', value: '' }) }
const removeHeader = (idx) => { settings.value._headersList.splice(idx, 1) }

const openAddDialog = () => {
  current.value = null
  settings.value = { ...emptySettings(), enabled: false }
  dialogVisible.value = true
}

const openEditDialog = (svc) => {
  current.value = svc

  // 解析 auth JSONB
  let authObj = {}
  if (svc.auth) {
    authObj = typeof svc.auth === 'string' ? (() => { try { return JSON.parse(svc.auth) } catch { return {} } })() : svc.auth
  }
  const authType = authObj.type || 'none'

  const argsArr = Array.isArray(svc.args) ? [...svc.args] : []
  // env JSONB（stdio 用，["K=V", ...]）
  const envArr = Array.isArray(svc.env) ? [...svc.env] : []
  // headers 存在 auth.headers 里
  const headersArr = (svc.transport !== 'stdio' && authObj.headers && typeof authObj.headers === 'object')
    ? Object.entries(authObj.headers).map(([key, value]) => ({ key, value }))
    : []

  settings.value = {
    name: svc.name || '',
    displayName: svc.display_name || '',
    transport: svc.transport || 'streamable_http',
    url: svc.url || '',
    command: svc.command || '',
    work_dir: svc.work_dir || '',
    description: svc.description || '',
    authType,
    authToken: authObj.token || '',
    authClientId: authObj.client_id || '',
    authClientSecret: authObj.client_secret || '',
    authTokenUrl: authObj.token_url || '',
    enabled: svc.is_active !== false,
    _argsList: argsArr,
    _envList: envArr,
    _headersList: headersArr,
    _showToken: false,
    _showSecret: false
  }
  dialogVisible.value = true
}

const saveConnection = async () => {
  const s = settings.value
  if (!s.name) return ElMessage.warning(t('settings.connection.needName'))
  if (s.transport === 'streamable_http' && !s.url) return ElMessage.warning(t('settings.connection.needUrl'))
  if (s.transport === 'stdio' && !s.command) return ElMessage.warning(t('settings.connection.needCommand'))

  // 构造 auth 对象（stdio 不使用鉴权）
  let authObj = null
  if (s.transport !== 'stdio' && s.authType !== 'none') {
    authObj = { type: s.authType }
    if (s.authType === 'bearer') {
      authObj.token = s.authToken
    } else if (s.authType === 'oauth') {
      authObj.client_id = s.authClientId
      authObj.client_secret = s.authClientSecret
      if (props.withTokenUrl && s.authTokenUrl) {
        authObj.token_url = s.authTokenUrl
      }
    }
    // 自定义 headers
    const validHeaders = s._headersList.filter(h => h.key.trim())
    if (validHeaders.length > 0) {
      const headersMap = {}
      validHeaders.forEach(h => { headersMap[h.key.trim()] = h.value })
      authObj.headers = headersMap
    }
  }

  const args = s._argsList.filter(a => a.trim() !== '')
  const envs = s._envList.filter(e => e.trim() !== '')

  const payload = {
    name: s.name,
    display_name: s.displayName || s.name,
    transport: s.transport,
    url: s.transport === 'streamable_http' ? s.url : '',
    command: s.transport === 'stdio' ? s.command : '',
    work_dir: s.transport === 'stdio' ? (s.work_dir || '') : '',
    args: s.transport === 'stdio' ? (args.length > 0 ? args : null) : null,
    env: s.transport === 'stdio' ? (envs.length > 0 ? envs : null) : null,
    description: s.description,
    auth: authObj
  }

  loading.value = true
  try {
    const response = current.value
      ? await fetch(`${props.api.update}${current.value.id}`, {
          method: 'PUT',
          headers: authHeaders(true),
          body: JSON.stringify(payload)
        })
      : await fetch(props.api.create, {
          method: 'POST',
          headers: authHeaders(true),
          body: JSON.stringify(payload)
        })

    if (!response.ok) throw new Error(t('settings.connection.saveFailed', { title: props.title }))
    await checkBizOk(response)
    ElMessage.success({
      message: current.value
        ? t('settings.connection.updated', { title: props.title })
        : t('settings.connection.added', { title: props.title }),
      duration: 700
    })
    dialogVisible.value = false
    await fetchConnections()
  } catch (error) {
    ElMessage.error({
      message: t('settings.connection.saveFailedDetail', { title: props.title, msg: error.message }),
      duration: 5000,
      offset: 20,
      showClose: true
    })
    console.error(`save ${props.title} failed:`, error)
  } finally {
    loading.value = false
  }
}

const removeConnection = async (svc) => {
  try {
    await ElMessageBox.confirm(t('settings.connection.deleteConfirm', { title: props.title }), t('settings.connection.deleteTitle'), {
      confirmButtonText: t('settings.connection.delete'),
      cancelButtonText: t('settings.connection.cancel'),
      type: 'warning'
    })
  } catch {
    return
  }
  loading.value = true
  try {
    const response = await fetch(`${props.api.delete}${svc.id}`, {
      method: 'DELETE',
      headers: authHeaders()
    })
    if (!response.ok) throw new Error(t('settings.connection.deleteFailed', { title: props.title }))
    ElMessage.success({ message: t('settings.connection.deleted', { title: props.title }), duration: 700 })
    await fetchConnections()
  } catch (error) {
    ElMessage.error({
      message: t('settings.connection.deleteFailedDetail', { title: props.title, msg: error.message }),
      duration: 5000,
      offset: 20,
      showClose: true
    })
    console.error(`delete ${props.title} failed:`, error)
  } finally {
    loading.value = false
  }
}
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

.card-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(320px, 1fr));
  gap: 16px;
  width: 100%;
}

.item-card {
  border: 1px solid #e8eaed;
  border-radius: 10px;
  padding: 14px 16px;
  background: #fff;
  display: flex;
  flex-direction: column;
  gap: 8px;
  transition: box-shadow 0.2s ease, border-color 0.2s ease;
}
.item-card:hover {
  border-color: #c6e2ff;
  box-shadow: 0 2px 10px rgba(64, 158, 255, 0.12);
}

.item-card-head {
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 8px;
}

.item-name {
  font-size: 14px;
  font-weight: 600;
  color: #202124;
  min-width: 0;
}
.text-ellipsis {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.item-actions {
  display: flex;
  align-items: center;
  gap: 2px;
  flex: none;
}
.item-actions :deep(.el-button + .el-button) {
  margin-left: 0;
}

.item-tags {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}

.item-meta {
  display: flex;
  align-items: baseline;
  gap: 8px;
  font-size: 12px;
  min-width: 0;
}
.meta-label {
  flex: none;
  width: 56px;
  color: #909399;
}
.meta-value {
  color: #606266;
  min-width: 0;
}

.item-note {
  font-size: 12px;
  color: #909399;
  line-height: 1.5;
}

.item-tools-preview {
  border-top: 1px dashed #ebeef5;
  padding-top: 6px;
}
.tools-list {
  display: flex;
  flex-direction: column;
  gap: 4px;
  max-height: 200px;
  overflow-y: auto;
  margin-top: 4px;
}
.tool-item {
  display: flex;
  gap: 8px;
  font-size: 12px;
  min-width: 0;
}
.tool-name {
  color: #303133;
  font-family: monospace;
  flex: none;
}
.tool-desc {
  color: #909399;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.tool-more {
  font-size: 12px;
  color: #909399;
}
.rotate-90 {
  transform: rotate(90deg);
}

.empty-block {
  grid-column: 1 / -1;
  width: 100%;
}

.args-editor,
.headers-editor {
  display: flex;
  flex-direction: column;
  gap: 6px;
  width: 100%;
}
.arg-row {
  display: flex;
  align-items: center;
  gap: 6px;
}
.header-row {
  display: flex;
  align-items: center;
  gap: 6px;
}
.form-hint {
  font-size: 12px;
  color: #909399;
  line-height: 1.5;
  margin-top: 4px;
}
</style>
