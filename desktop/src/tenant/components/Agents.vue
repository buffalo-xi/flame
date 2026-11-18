<template>
  <div class="agents">
    <!-- ============ 全屏编辑模式（点击卡片 / 新建进入） ============ -->
    <div v-if="fullScreenMode" ref="editorAreaRef" class="full-screen-form">
      <div ref="headerRef" class="full-screen-header" :class="'header--' + headerSize">
        <div class="header-actions">
          <el-button size="small" @click="handleBack">
            <el-icon><ArrowLeft /></el-icon>返回列表
          </el-button>
        </div>
        <el-icon class="header-icon"><Cpu /></el-icon>
        <div class="header-info">
          <div class="header-name-wrapper" :class="{ locked: lockName, saving: renaming }">
            <el-tooltip v-if="lockName" :content="nameLockTip" placement="bottom">
              <span class="header-name">
                {{ editForm.name || (editingId ? '编辑智能体' : '新增智能体') }}
              </span>
            </el-tooltip>
            <template v-else>
              <span v-if="!isEditingName" class="header-name"
                    :title="editForm.name || (editingId ? '编辑智能体' : '新增智能体')"
                    @click="startEditName">
                {{ editForm.name || (editingId ? '编辑智能体' : '新增智能体') }}
              </span>
              <el-input v-else v-model="editName" size="small" class="header-name-input"
                        maxlength="64" placeholder="字母数字横线，≤64"
                        @blur="saveEditName" @keyup.enter="saveEditName" ref="nameInputRef" />
            </template>
          </div>
          <div class="header-desc-wrapper" :class="{ locked: lockDesc, saving: renaming }">
            <el-tooltip v-if="lockDesc" :content="descLockTip" placement="bottom">
              <span class="header-desc">{{ editForm.description || '作者很懒，什么都没有留下！' }}</span>
            </el-tooltip>
            <span v-else class="header-desc"
                  :title="editForm.description || '作者很懒，什么都没有留下！'"
                  @click="openDescDialog">
              {{ editForm.description || '作者很懒，什么都没有留下！' }}
            </span>
          </div>
        </div>
        <div class="header-buttons">
          <!-- 只读提示（参考工作流编辑器）：只读角标 + 悬停说明 -->
          <span v-if="exposedLocked" class="readonly-badge" :title="readonlyBadgeTitle">{{ readonlyBadgeText }}</span>
          <!-- 端点已对外开放：仍可编辑，但保存会立刻影响线上调用方（保存时二次确认） -->
          <span v-else-if="hasExposed" class="readonly-badge exposed-badge"
                title="有端点已对外开放：可以继续修改，保存后立即生效">对外 · 改动即时生效</span>
          <!-- 版本：vN / 未发布 / 有改动 三态，与工作流编辑器同口径 -->
          <VersionEntry v-if="editingId" kind="agent" :target="editingId"
                        :current-version="agentVersion" :dirty="agentDirty"
                        :can-release="!exposedLocked" @applied="onVersionApplied"
                        @changed="refreshVersionState" />
          <el-button size="small" class="header-action-btn refresh-toggle-btn"
                     :loading="refreshing" :disabled="!editingId" @click="refreshEditor">刷新</el-button>
          <el-button size="small" class="header-action-btn settings-toggle-btn" @click="openSettings">设置</el-button>
          <el-button size="small" type="primary" class="header-action-btn"
                    :loading="fileState.saving"
                    :disabled="exposedLocked || (editingId ? !fileState.canSave : false)"
                    @click="handleHeaderSave">
            保存
          </el-button>
          <!-- 窄档：次要操作收进「⋯」 -->
          <el-dropdown class="header-more" trigger="click" @command="onHeaderMore">
            <el-button size="small" class="header-action-btn more-btn" title="更多操作">
              <el-icon><MoreFilled /></el-icon>
            </el-button>
            <template #dropdown>
              <el-dropdown-menu>
                <el-dropdown-item command="refresh" :disabled="!editingId">刷新</el-dropdown-item>
                <el-dropdown-item command="settings">设置</el-dropdown-item>
              </el-dropdown-menu>
            </template>
          </el-dropdown>
        </div>
      </div>

      <div class="full-screen-content">
        <div class="agent-editor">
          <!-- key 随「载入版本」递增：版本内容整目录替换，必须重挂载才看得到新文件 -->
          <AgentFileEditor :key="editorEpoch" ref="fileEditorRef" :agent-id="editingId"
                           :locked="exposedLocked" :confirm-save="confirmIfExposed"
                           @state="onFileState" />
    <!-- 设置面板 / 工具授权侧栏（组件见 agents/AgentSettingsPanel.vue） -->
    <!-- key 随「载入版本」递增：载入会把 agent 元数据与私有技能元数据/依赖写回草稿 -->
    <AgentSettingsPanel :key="editorEpoch" :visible="settingsVisible" :anchor-el="editorAreaRef"
                         :agent-id="editingId" :agent="editForm"
                         :locked="exposedLocked" :supervisor="editingSupervisor"
                         :agents="agentList" :refresh-token="settingsRefreshToken"
                         @close="settingsVisible = false"
                         @supervisor-change="(v) => (editingSupervisor = v)"
                         @refresh-list="fetchAgents" />

        </div>
      </div>
    </div>

    <!-- 列表模式 -->
    <AgentsList v-else :agents="agentList" :loading="isLoading"
                @open="openEditEditor" @create="openCreateEditor"
                @import="importVisible = true"
                @export="(a) => exportEntity('agent', a.id, a.name)"
                @expose="handleExposeAgent" @delete="handleDeleteAgent"
                @status="({ instance, status }) => handleSetAgentStatus(instance, status)"
                @refresh="fetchAgents" />

    <!-- 端点面板 -->
    <AgentEndpointsPanel :visible="exposeVisible" :agent="currentExposeAgent"
                       @close="exposeVisible = false" @changed="onExposeChanged" />

    <!-- 资产包导入：装完刷新列表（新实体由后端生成 id，只能重拉） -->
    <AssetImportDialog v-model="importVisible" kind="agent" @imported="fetchAgents" />

    <!-- 描述单独用大框编辑：头部那行字号只有 12px、还被单行省略，长描述在原位输入框里基本没法写 -->
    <el-dialog v-model="descDialogVisible" title="编辑智能体描述" width="640px" append-to-body
               :close-on-click-modal="false" class="desc-dialog">
      <el-input v-model="descDraft" type="textarea" :rows="8" resize="vertical"
                placeholder="一句话说明这个智能体是干什么的：列表卡片、端点面板都靠它区分用途"
                @keydown.ctrl.enter="saveEditDesc" />
      <div class="desc-dialog-hint">
        描述会显示在智能体列表卡片上。点「确定」立即保存；Ctrl+Enter 可快速确定。
      </div>
      <template #footer>
        <el-button size="small" @click="descDialogVisible = false">取消</el-button>
        <el-button size="small" type="primary" :loading="renaming" @click="saveEditDesc">确定</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup>
import { ref, computed, onMounted, onActivated, onUnmounted, onDeactivated, nextTick, watch } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import AgentsList from './agents/AgentsList.vue'
import AgentFileEditor from './agents/AgentFileEditor.vue'
import AgentSettingsPanel from './agents/AgentSettingsPanel.vue'
import AgentEndpointsPanel from './agents/AgentEndpointsPanel.vue'
import AssetImportDialog from './AssetImportDialog.vue'
import VersionEntry from './versions/VersionEntry.vue'
import adminApi from '../config/admin.js'
import { getToken } from '../utils/auth.js'
import { exportEntity } from '../utils/assetTransfer.js'
import { useHeaderCompact } from '../utils/useHeaderCompact.js'

// 两态状态（与后端 shared/models.StatusXxx 一一对应）：
//   1 正常：可修改、可运行（新建默认）
//   3 废弃：不可修改、不可运行（只由手工设置）
const STATUS_META = {
  1: { text: '正常', tag: 'success' },
  3: { text: '废弃', tag: 'danger' }
}
const statusMeta = (status) => STATUS_META[status] || STATUS_META[1]

// 智能体实例列表（列表的筛选 / 搜索 / 分组都在 agents/AgentsList.vue）
const agentList = ref([])

// 跨页跳转（「工具与端点 → 端点 → 点目标名」，以及会话里建 / 改了 agent）：
// 切到本页后直接进入该 agent 的编辑页。
// id / name 都认：create_agent 回 id，update_agent 只回（可能是改名后的）名字。
// 父层每次传入新对象（含 ts），所以同一 agent 重复点也能触发。
const props = defineProps({ focus: { type: Object, default: null } })
watch(() => props.focus, async (f) => {
  if (!f || f.nav !== 'agents' || !(f.id || f.name)) return
  const find = () => agentList.value.find((a) =>
    (f.id && String(a.id) === String(f.id)) ||
    (f.name && String(a.name) === String(f.name)))
  let inst = find()
  // 会话里刚 create_agent：keep-alive 的列表还是旧快照，先刷一次再找
  if (!inst) {
    await fetchAgents()
    inst = find()
  }
  if (inst) openEditEditor(inst)
})
// 加载状态
const isLoading = ref(false)

// 端点面板：面板本体（状态 / 端点增删 / 表单）在 agents/AgentEndpointsPanel.vue，
// 这里只留「打开哪只 agent」与父级副作用（卡片色带、端点角标）
const exposeVisible = ref(false)
const currentExposeAgent = ref(null)

// 资产包导入（对话框本体是通用组件，这里只管开关）
const importVisible = ref(false)

const handleExposeAgent = (instance) => {
  currentExposeAgent.value = instance
  exposeVisible.value = true
}

// 面板内端点状态变了：回写卡片端点色带 + 刷新编辑器的只读角标（必要时再拉一次详情纠正状态）
const onExposeChanged = ({ agentId, endpoints, reloadStatus }) => {
  updateCardEndpoints(agentId, endpoints)
  refreshExposedLock()
  if (reloadStatus) refreshCardStatus(agentId)
}

// 获取智能体实例列表
const fetchAgents = async () => {
  isLoading.value = true
  try {
    const token = getToken()
    if (!token) return

    const response = await fetch(adminApi.agents.base, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        'Authorization': `Bearer ${token}`
      },
      body: JSON.stringify({ 
        page: 1, 
        limit: -1
      })
    })

    if (!response.ok) return

    try {
      const data = await response.json()
      if (data.code === 200) {
        agentList.value = data.data.items || []
      }
    } catch (jsonError) {
      console.error('解析响应 JSON 失败:', jsonError)
    }
  } catch (error) {
    console.error('获取智能体实例列表失败:', error)
  } finally {
    isLoading.value = false
  }
}

// ============ 全屏编辑模式（参考工作流：点击卡片 / 新建即进入） ============
const fullScreenMode = ref(false)

// 头部窄屏分层降级：宽度不足时依次收起 描述 → 锁标签文案 / 次要按钮
const headerRef = ref(null)
const headerSize = useHeaderCompact(headerRef, { md: 1000, sm: 820 })
const onHeaderMore = (cmd) => {
  if (cmd === 'settings') openSettings()
  else if (cmd === 'refresh') refreshEditor()
}

const editingId = ref('')
// 版本状态（后端详情接口算好）：0 = 从未发布；dirty = 草稿与当前版本不一致
// （agent 的「定义」含 AGENT.md + 私有技能文件 + 私有技能元数据，后端三段一起比）
const agentVersion = ref(0)
const agentDirty = ref(false)
// 「载入版本」会整目录替换草稿：文件编辑器靠这个 key 重挂载
const editorEpoch = ref(0)
const settingsVisible = ref(false)
// 设置面板的「重读」信号：手动刷新时自增，面板据此把关系 / 授权 / 生效信息重新拉一遍
const settingsRefreshToken = ref(0)
// 编辑区容器（设置面板以其为定位基准：同高、贴右边缘）
const editorAreaRef = ref(null)
const saving = ref(false)
// 编辑器（agents/AgentFileEditor.vue）自己持有文件树与 tab；这里只留它的对外状态：
// canSave = 当前 tab 可写，hasDirty = 有未保存改动，saving = 正在保存
const fileEditorRef = ref(null)
const fileState = ref({ canSave: false, hasDirty: false, saving: false })
const onFileState = (s) => { fileState.value = s }

// 打开设置面板（已打开时再次点击「设置」→ 关闭）
const openSettings = () => {
  settingsVisible.value = !settingsVisible.value
}
// ============ 定义文件：面包屑 + 当前目录列表（与工作台「工作区」同一套交互） ============
// 一次只看一层：目录点进去、面包屑回退；新建 / 上传的落点就是当前目录
// （不再依赖「树里选中的节点」那种界面上看不见的状态）。
const editForm = ref({
  name: '', description: '',
  status: 1, called_by_ids: [], can_call_ids: [],
  config: '', endpoints: '' // 隐藏字段：必须回传（UpdateAgent 是全字段覆盖式 UPDATE）
})
// 技能绑定 / 调用关系 / 工具授权都由 agents/AgentSettingsPanel.vue 自己加载与保存
// 「允许发现未绑定的技能」不再单列 ref：它是 agent 属性，随 editForm.skill_discovery 走
const isEditingName = ref(false)
// 内置受保护智能体（如 main）：名称不可改，描述可改
const editingProtected = ref(false)
// 超级权限身份（agents.is_supervisor；main 只是预置的那一个，可授予多个）：
// 与 protected 语义正交（见 shared/models/protected.go）。决定「内置工具是否固有基线」
// 「是否可被其它 agent 调用」这类超权行为，不能拿 protected 顶替。
const editingSupervisor = ref(false)
// 只读：状态为「废弃」(3) 时该 agent 不允许任何改动。
// 判据只有 status —— 端点对外开放不改状态，也不影响可编辑性（见下方 isReadonly）。
const exposedLocked = ref(false)
// 只读角标文案（参考工作流编辑器的「已废弃 · 只读」）
const readonlyBadgeText = computed(() => '已废弃 · 只读')
const readonlyBadgeTitle = computed(() => '智能体已废弃，不可保存、不可运行')
// 名称 / 描述是否锁定（名称：内置受保护 or 只读；描述：只读）
const lockName = computed(() => editingProtected.value || exposedLocked.value)
const lockDesc = computed(() => exposedLocked.value)
const nameLockTip = computed(() => editingProtected.value
  ? '内置智能体不允许改名'
  : '已废弃，请先切回「正常」状态再修改')
const descLockTip = computed(() => '已废弃，请先切回「正常」状态再修改')
// 头部名称内联编辑 / 描述弹窗编辑，两者共用同一个「保存中」标记
const renaming = ref(false)
const editName = ref('')
const nameInputRef = ref(null)
// 描述走弹窗：头部那行只有 12px 且单行省略，长描述在原位输入框里没法写
const descDialogVisible = ref(false)
const descDraft = ref('')
// agent / skill / flow 名称规则：字母或数字开头，后续只允许字母数字横线，长度 ≤64
const NAME_RE = /^[a-zA-Z0-9][a-zA-Z0-9-]*$/

// 多 tab 编辑器

const authHeaders = () => ({
  'Content-Type': 'application/json',
  'Authorization': `Bearer ${getToken()}`
})

const resetEditForm = () => {
  editForm.value = {
    name: '', description: '',
    status: 1, called_by_ids: [], can_call_ids: [], config: '', endpoints: '',
    skill_discovery: false, is_supervisor: false
  }
}

// config / endpoints 必须始终以合法 JSON 回传：后端 PUT 是全字段覆盖式 UPDATE，
// 空串会被落成 NULL 并触发 config 的 NOT NULL 约束（"null value in column config"）。
const toJSONField = (v, fallback) => {
  if (v === null || v === undefined || v === '') return fallback
  return typeof v === 'object' ? JSON.stringify(v) : String(v)
}

// 新建 → 直接进全屏（替代原弹窗）
const openCreateEditor = () => {
  editingId.value = ''
  editingProtected.value = false
  editingSupervisor.value = false
  exposedLocked.value = false
  agentVersion.value = 0
  agentDirty.value = false
  resetEditForm()
  // 编辑器按 agentId 感知切换：'' 就是空态（清 tab、清目录）
  fullScreenMode.value = true
}

// 点卡片 → 拉详情 + 授权 + 文件树 → 进全屏
//
// 废弃(3) 也允许进入 —— 只是**整屏只读**：exposedLocked 会禁用所有写入入口
// （正文 readonly、各 select / switch 禁用、树的增删图标隐藏、名称 / 描述内联编辑被
// lockName / lockDesc 挡住、保存按钮禁用），后端也会拦。头部有警示 tag 说明
// 「需先切回正常状态才能修改」，所以这里不再提前 return。
const openEditEditor = async (instance) => {
  const token = getToken()
  if (!token) return
  try {
    const res = await fetch(adminApi.agents.get + instance.id, { headers: authHeaders() })
    if (!res.ok) throw new Error('加载失败')
    const data = await res.json()
    const d = data.data || {}
    editingId.value = d.id
    editingProtected.value = !!d.protected
    editingSupervisor.value = !!d.is_supervisor
    exposedLocked.value = isReadonly(d)
    // 版本状态由后端按「当前版本 vs 草稿」算好（文件 + 私有技能元数据），前端不自己推
    agentVersion.value = d.current_version || 0
    agentDirty.value = !!d.dirty
    resetEditForm()
    editForm.value = {
      name: d.name || '',
      description: d.description || '',
      status: d.status || 1,
      called_by_ids: [],
      can_call_ids: [],
      config: toJSONField(d.config, '{}'),
      endpoints: toJSONField(d.endpoints, '[]'),
      skill_discovery: !!d.skill_discovery,
      is_supervisor: !!d.is_supervisor
    }
    fullScreenMode.value = true
    // 文件树 + 入口文件由编辑器自己按 agentId 加载（见 agents/AgentFileEditor.vue）；
    // 调用关系 / 技能绑定 / 工具授权同理，由设置面板懒加载
  } catch (error) {
    console.error('加载智能体失败:', error)
    ElMessage.error('加载失败，请稍后重试')
  }
}

const handleBack = () => {
  if (fileState.value.hasDirty) {
    ElMessage.warning({ message: '有未保存的文件修改', duration: 2000, offset: 20 })
  }
  fullScreenMode.value = false
  editingId.value = ''
  editingProtected.value = false
  editingSupervisor.value = false
  exposedLocked.value = false
  resetEditForm()
  // 编辑器里可能新建 / 收回过端点，卡片上的端点与状态要整表刷新才准
  fetchAgents()
}

// ============ 版本（vN / 未发布 / 有改动） ============
// 发布或载入之后：整份定义可能被替换（载入 = 版本写回草稿），必须重挂载文件树 + 重读详情
const onVersionApplied = async () => {
  if (!editingId.value) return
  editorEpoch.value++
  await openEditEditor({ id: editingId.value })
  fetchAgents()
}

// 草稿被改（存文件 / 存设置）后只刷版本徽标，不重载编辑器
const refreshVersionState = async () => {
  if (!editingId.value) return
  try {
    const res = await fetch(adminApi.agents.get + editingId.value, { headers: authHeaders() })
    const body = await res.json().catch(() => ({}))
    if (body.code !== 200 || !body.data) return
    agentVersion.value = body.data.current_version || 0
    agentDirty.value = !!body.data.dirty
  } catch {
    // 版本状态是锦上添花：拿不到就不更新，不影响编辑
  }
}

// 文件保存成功后 hasDirty 会回落：此刻草稿已变，版本徽标要跟着变
watch(() => fileState.value.hasDirty, (now, before) => {
  if (before && !now) refreshVersionState()
})
// 设置面板里的改动（配置 / 可见性 / 私有技能）也是定义的一部分
watch(() => settingsVisible.value, (now, before) => {
  if (before && !now) refreshVersionState()
})

// ============ 手动刷新（原地重拉，不退出编辑器） ============
// 与「点卡片进入」不同：保留当前打开的 tab 与设置面板展开状态，只把数据按服务端现状重读一遍。
// 已打开文件里**有未保存修改的跳过**，避免刷新把用户正在写的内容冲掉。
const refreshing = ref(false)
const refreshEditor = async () => {
  if (!editingId.value || refreshing.value) return
  refreshing.value = true
  try {
    const res = await fetch(adminApi.agents.get + editingId.value, { headers: authHeaders() })
    const body = await res.json().catch(() => ({}))
    if (body.code !== 200) throw new Error(body.msg || '刷新失败')
    const d = body.data || {}
    // 元数据以服务端为准；can_call_ids / called_by_ids 由 loadRelations 回填
    editForm.value = {
      ...editForm.value,
      name: d.name || '',
      description: d.description || '',
      status: d.status || 1,
      config: toJSONField(d.config, '{}'),
      endpoints: toJSONField(d.endpoints, '[]'),
      skill_discovery: !!d.skill_discovery,
      is_supervisor: !!d.is_supervisor
    }
    editingProtected.value = !!d.protected
    editingSupervisor.value = !!d.is_supervisor
    exposedLocked.value = isReadonly(d)
    agentVersion.value = d.current_version || 0
    agentDirty.value = !!d.dirty
    await fileEditorRef.value?.reloadDir()
    const skipped = (await fileEditorRef.value?.reloadOpenTabs()) || 0
    // 关系 / 授权 / 生效信息由设置面板自己感知 refreshToken 重拉
    settingsRefreshToken.value++
    if (skipped) {
      ElMessage.warning({ message: `已刷新（${skipped} 个文件有未保存修改，已跳过）`, duration: 2500 })
    } else {
      ElMessage.success({ message: '已刷新', duration: 700 })
    }
  } catch (e) {
    ElMessage.error(e.message || '刷新失败')
  } finally {
    refreshing.value = false
  }
}

// header 内联编辑名称 / 描述
const startEditName = () => {
  if (lockName.value) return
  editName.value = editForm.value.name
  isEditingName.value = true
  nextTick(() => nameInputRef.value?.focus())
}
// 名称内联编辑：失焦/回车即保存（后端同步迁移工作区目录 + 更新数据库 name）
const saveEditName = async () => {
  if (!isEditingName.value) return
  const prev = editForm.value.name
  const next = (editName.value || '').trim()
  isEditingName.value = false
  if (!next || next === prev) return
  if (editingProtected.value) return                          // 内置智能体不允许改名
  if (!NAME_RE.test(next)) {                                  // 名称规则：字母数字横线，≤64
    ElMessage.warning('名称只允许字母数字横线，且不能以横线开头（≤64）')
    return
  }
  if (!editingId.value) { editForm.value.name = next; return } // 未创建：等设置抽屉统一创建

  renaming.value = true
  try {
    const res = await fetch(adminApi.agents.update + editingId.value, {
      method: 'PUT',
      headers: authHeaders(),
      body: JSON.stringify({ ...editForm.value, name: next })
    })
    const body = await res.json().catch(() => ({}))
    if (body.code !== 200) throw new Error(body.msg || '改名失败')
    editForm.value.name = next
    ElMessage.success('已改名')
    // 局部更新卡片名称（不整表刷新）
    const card = agentList.value.find(a => String(a.id) === String(editingId.value))
    if (card) card.name = next
  } catch (e) {
    ElMessage.error(e.message || '改名失败')
  } finally {
    renaming.value = false
  }
}
const openDescDialog = () => {
  if (lockDesc.value) return
  descDraft.value = editForm.value.description || ''
  descDialogVisible.value = true
}
// 点「确定」即保存：与原内联编辑同口径（那边是失焦/回车立刻 PUT）。
// 失败时不关弹窗 —— 用户刚写的一段话不能因为一次网络抖动就丢掉。
const saveEditDesc = async () => {
  const prev = editForm.value.description
  const next = (descDraft.value || '').trim()
  if (next === prev) { descDialogVisible.value = false; return }
  if (!editingId.value) { editForm.value.description = next; descDialogVisible.value = false; return }

  renaming.value = true
  try {
    const res = await fetch(adminApi.agents.update + editingId.value, {
      method: 'PUT',
      headers: authHeaders(),
      body: JSON.stringify({ ...editForm.value, description: next })
    })
    const body = await res.json().catch(() => ({}))
    if (body.code !== 200) throw new Error(body.msg || '描述保存失败')
    editForm.value.description = next
    descDialogVisible.value = false
  } catch (e) {
    ElMessage.error(e.message || '描述保存失败')
  } finally {
    renaming.value = false
  }
}



// 设置保存：新建 POST / 更新 PUT（含调用授权）
const handleSaveSettings = async () => {
  const f = editForm.value
  f.name = (f.name || '').trim()
  if (!f.name) { ElMessage.warning('请输入智能体名称'); return }
  // 名称规则：字母或数字开头，后续只允许字母数字横线，≤64
  if (!NAME_RE.test(f.name)) { ElMessage.warning('名称只允许字母数字横线，且不能以横线开头（≤64）'); return }
  saving.value = true
  try {
    let id = editingId.value
    if (id) {
      const res = await fetch(adminApi.agents.update + id, {
        method: 'PUT',
        headers: authHeaders(),
        body: JSON.stringify(f)
      })
      const body = await res.json().catch(() => ({}))
      if (body.code !== 200) throw new Error(body.msg || '保存失败')
    } else {
      const res = await fetch(adminApi.agents.create, {
        method: 'POST',
        headers: authHeaders(),
        body: JSON.stringify(f)
      })
      const body = await res.json().catch(() => ({}))
      if (body.code !== 200) throw new Error(body.msg || '创建失败')
      id = body?.data?.id
      editingId.value = id // 留在编辑页，后续修改走 PUT
      // 新建时未填这两个隐藏字段（后端已落默认值），补成合法 JSON，
      // 否则紧接着的改名/改描述会把空串 PUT 上去
      editForm.value.config = toJSONField(editForm.value.config, '{}')
      editForm.value.endpoints = toJSONField(editForm.value.endpoints, '[]')
    }
    if (id) {
      const rr = await fetch(adminApi.agents.relations(id), {
        method: 'PUT',
        headers: authHeaders(),
        body: JSON.stringify({ can_call_ids: f.can_call_ids, called_by_ids: f.called_by_ids })
      })
      if (!rr.ok) throw new Error('授权保存失败')
    }
    ElMessage.success(editingId.value ? '已保存' : '已创建')
    settingsVisible.value = false
    fetchAgents()
  } catch (error) {
    console.error('保存智能体失败:', error)
    ElMessage.error(error.message || '保存失败')
  } finally {
    saving.value = false
  }
}

// Ctrl+S 保存当前文件
const onAgentKeydown = (e) => {
  if ((e.ctrlKey || e.metaKey) && e.key === 's' && fullScreenMode.value) {
    e.preventDefault()
    fileEditorRef.value?.saveCurrentFile()
  }
}
onMounted(() => window.addEventListener('keydown', onAgentKeydown))
onUnmounted(() => window.removeEventListener('keydown', onAgentKeydown))

// endpoints 形如 [{"protocol":"mcp","exposed":true}]
const hasEnabledEndpoint = (endpoints) => {
  const list = Array.isArray(endpoints) ? endpoints : []
  return list.some(e => e && e.exposed)
}

// 只读判定：只看状态（3 废弃）。端点对外开放**不**影响可编辑性 ——
// 「对外提供入口」与「定义能不能改」是两件事（见 shared/models 的状态注释）。
const isReadonly = (d) => (d?.status || 1) !== 1

// 当前 agent 是否有端点对外开放：存在时编辑界面在保存前提醒（不是禁止保存）。
// editForm.endpoints 是 JSON 字符串（经 toJSONField），这里解一次。
const hasExposed = computed(() => {
  try { return hasEnabledEndpoint(JSON.parse(editForm.value.endpoints || '[]')) } catch { return false }
})

// 端点已对外开放时，写定义会立刻影响线上调用方 —— 保存前确认一次。
// 这不是权限校验（后端不会因端点对外而冻结定义），只是让人别误点。
const confirmIfExposed = async (action) => {
  if (!hasExposed.value) return true
  try {
    await ElMessageBox.confirm(
      `该智能体有端点已对外开放，${action}会立即影响线上调用方。确认继续？`,
      '端点已对外开放',
      { confirmButtonText: '确认保存', cancelButtonText: '取消', type: 'warning' }
    )
    return true
  } catch { return false }
}

// 状态变化后刷新只读锁（只与 status 有关；端点对外开放不改状态）
const refreshExposedLock = async () => {
  if (!editingId.value || !fullScreenMode.value) return
  try {
    const res = await fetch(adminApi.agents.get + editingId.value, { headers: authHeaders() })
    const body = await res.json().catch(() => ({}))
    if (body.code === 200) exposedLocked.value = isReadonly(body.data)
  } catch { /* 忽略：取不到时保持当前只读状态 */ }
}

// 局部更新端点面板对应卡片的端点状态（不整表刷新列表）
const updateCardEndpoints = (agentId, endpoints) => {
  if (!agentId) return
  const target = agentList.value.find(item => String(item.id) === String(agentId))
  if (!target) return
  target.endpoints = (endpoints || []).map(ep => ({
    protocol: ep.protocol,
    exposed: !!ep.exposed
  }))
}

// 端点对外状态变了：卡片的端点色带需要同步（定义状态不随端点对外联动）。
// 只拉单条详情，避免整表刷新打断用户当前浏览位置。
const refreshCardStatus = async (agentId) => {
  const id = agentId
  if (!id) return
  try {
    const res = await fetch(adminApi.agents.get + id, { headers: authHeaders() })
    const body = await res.json().catch(() => ({}))
    if (body.code !== 200 || !body.data) return
    const target = agentList.value.find(item => String(item.id) === String(id))
    if (target) {
      target.status = body.data.status
      if (body.data.endpoints) target.endpoints = body.data.endpoints
    }
    // 编辑器开着同一个 agent 时，只读角标也要跟着变
    if (String(editingId.value) === String(id)) exposedLocked.value = isReadonly(body.data)
  } catch { /* 忽略：下次整表刷新会纠正 */ }
}


// 头部保存：未创建 → 走创建流程；已存在 → 保存当前文件
const handleHeaderSave = async () => {
  if (!editingId.value) { await handleSaveSettings(); return }
  fileEditorRef.value?.saveCurrentFile()
}


// 处理删除智能体
const handleDeleteAgent = (instance) => {
  // 弹出确认对话框
  ElMessageBox.confirm(
    `确定要删除智能体 "${instance.name}" 吗？此操作不可恢复。`,
    '删除确认',
    {
      confirmButtonText: '确定',
      cancelButtonText: '取消',
      type: 'warning'
    }
  ).then(async () => {
    try {
      const token = getToken()
      if (!token) {
        ElMessage.error({
        message: '请先登录',
        duration: 5000,
        offset: 20,
        showClose: true
      })
        return
      }

      const response = await fetch(`${adminApi.agents.base}/${instance.id}`, {
        method: 'DELETE',
        headers: {
          'Content-Type': 'application/json',
          'Authorization': `Bearer ${token}`
        }
      })

      if (!response.ok) {
        throw new Error('删除失败')
      }

      const data = await response.json()
      if (data.code === 200) {
        ElMessage.success({
          message: '智能体删除成功',
          duration: 700
        })
        // 刷新智能体列表
        fetchAgents()
      } else {
        ElMessage.error({
          message: `删除失败: ${data.msg}`,
          duration: 5000,
          offset: 20,
          showClose: true
        })
      }
    } catch (error) {
      console.error('删除智能体失败:', error)
      ElMessage.error({
        message: '删除失败，请稍后重试',
        duration: 5000,
        offset: 20,
        showClose: true
      })
    }
  }).catch(() => {
    // 取消删除操作
  })
}



// 状态切换（两态）：1 正常 / 3 废弃。
// 切「废弃」会由后端自动收回该 agent 的全部端点对外（记录保留），所以那一步先确认；
// 切回「正常」不会收回端点 —— 端点对外与定义可编辑是两件事。
const handleSetAgentStatus = async (instance, status) => {
  const meta = statusMeta(status)
  if (status === 3) {
    try {
      await ElMessageBox.confirm(
        `废弃「${instance.name}」？废弃后不可修改、不可运行，并自动收回全部端点的对外。`,
        '提示',
        { type: 'warning' }
      )
    } catch {
      return
    }
  }
  try {
    const token = getToken()
    if (!token) {
      ElMessage.error({ message: '请先登录', duration: 5000, offset: 20, showClose: true })
      return
    }
    const response = await fetch(adminApi.agents.status(instance.id), {
      method: 'PUT',
      headers: {
        'Content-Type': 'application/json',
        'Authorization': `Bearer ${token}`
      },
      body: JSON.stringify({ status })
    })
    if (!response.ok) throw new Error('更新失败')
    const data = await response.json()
    if (data.code === 200) {
      instance.status = status
      ElMessage.success({ message: `已切到「${meta.text}」`, duration: 700 })
      await fetchAgents()
    } else {
      ElMessage.error({ message: `更新失败: ${data.msg}`, duration: 5000, offset: 20, showClose: true })
    }
  } catch (error) {
    console.error('切换 agent 状态失败:', error)
    ElMessage.error({ message: '更新失败，请稍后重试', duration: 5000, offset: 20, showClose: true })
  }
}

// 组件挂载时获取数据
onMounted(() => {
  fetchAgents()
})

// 组件激活时获取数据
onActivated(() => {
  fetchAgents()
})
</script>

<style scoped>
/* ============ 全屏编辑模式（点击卡片 / 新建进入） ============ */
.full-screen-form {
  position: absolute;
  inset: 0;
  display: flex;
  flex-direction: column;
  background: #fff;
  z-index: 10;
}
/* 头部：对齐工作流编辑页（Flows.vue）的样式；
   高度锁 64px，与工作台标签条（Workbench 的 .wb-tabbar）一致 —— 两者是页面上同一层的头部，
   高度不同会显得一高一低。上下留白交给 align-items 居中：原来 16px 上下边距 + 40px 图标 = 72px，
   再怎么调对齐也会比 64px 高。高度固定后，窄档收起描述也不会让头部抖动。 */
.full-screen-header {
  display: flex;
  align-items: center;
  justify-content: flex-start;
  gap: 8px;
  height: 64px;
  box-sizing: border-box;
  padding: 0 20px;
  border-bottom: 1px solid #f0f0f0;
  background: #fff;
  flex-shrink: 0;
  min-width: 0;   /* 允许头部参与收缩，子项才能按档位收起 */
}
.header-actions {
  margin-right: 16px;
  flex-shrink: 0;
}
.header-actions .el-button {
  border-radius: 20px;
  height: 36px;
  padding: 0 16px;
}
.header-icon {
  font-size: 40px;
  color: #9dc4c4;
  flex-shrink: 0;
}
.header-info {
  display: flex;
  flex-direction: column;
  gap: 4px;
  min-width: 0;
  /* 占满按钮区之外的剩余宽度并允许压缩：名称/描述靠这一层的宽度做单行省略，
     窗口变窄时是文字变短 + 省略号，不是整行消失 */
  flex: 1 1 auto;
}
.header-name {
  font-size: 14px;
  font-weight: 600;
  color: #333;
}
.header-desc {
  font-size: 12px;
  color: #999;
}
.header-name-wrapper,
.header-desc-wrapper {
  cursor: pointer;
  min-width: 0;   /* 原来是 100px：父级变窄时子项会溢出、压到右侧按钮上 */
}

/* 单行省略：截断而不是溢出遮挡 */
.header-name,
.header-desc {
  display: block;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
/* 只读提示角标（参考工作流编辑器） */
.readonly-badge {
  font-size: 12px;
  font-weight: 500;
  color: #d48806;
  background: #fff7e6;
  border: 1px solid #ffd591;
  border-radius: 10px;
  padding: 2px 8px;
  white-space: nowrap;
}
/* 「对外」角标：与「只读」区分开 —— 它不禁止操作，只是提醒改动会即时生效 */
.readonly-badge.exposed-badge {
  color: #1677ff;
  background: #e6f4ff;
  border-color: #91caff;
}
.header-name-wrapper.locked {
  cursor: not-allowed;
}
.header-name-wrapper.locked .header-name:hover {
  color: #333;
}
.header-name-wrapper.saving,
.header-desc-wrapper.saving {
  opacity: 0.6;
}
.header-name:hover,
.header-desc:hover {
  color: #1890ff;
}
.header-name-input { width: min(200px, 100%); }
.header-name-input :deep(.el-input__wrapper) {
  border-radius: 4px;
  box-shadow: 0 0 0 1px #1890ff;
}

/* 描述弹窗里的说明行 */
.desc-dialog-hint {
  margin-top: 8px;
  font-size: 12px;
  color: #909399;
  line-height: 1.5;
}
.header-buttons {
  margin-left: auto;
  display: flex;
  gap: 8px;
  align-items: center;
  flex-shrink: 0;
}
.header-buttons .el-button { border-radius: 20px; }
.header-action-btn {
  border-radius: 20px;
  height: 36px;
  padding: 0 16px;
}

/* ===== 窄屏分层降级（档位由 useHeaderCompact 计算） ===== */
/* 「⋯」默认隐藏，只在窄档出现 */
.header-more {
  display: none;
}
.header-more .el-button {
  border-radius: 20px;
  height: 36px;
  padding: 0 10px;
}

/* 描述在任何档位都不收起：靠 .header-info 的宽度收缩 + 单行省略跟着变短，
   点一行就能打开弹窗改成大段文字，比「窄了就不显示」更有用 */

/* 窄档：「刷新」「设置」收进「⋯」 */
.header--sm .refresh-toggle-btn,
.header--sm .settings-toggle-btn {
  display: none;
}
.header--sm .header-more {
  display: inline-flex;
}

.full-screen-content {
  flex: 1;
  min-height: 0;
  /* 不加内边距：左右两块白卡片直接贴齐头部栏的左右边缘（与技能编辑页 Skills.vue 同口径）。 */
  padding: 0px 0px;
  background: #f5f7fa;
}

/* 卡片间距 6px：与技能编辑页（Skills.vue 的 .full-screen-content gap）看齐 */
.agent-editor { display: flex; gap: 6px; height: 100%; position: relative; }
.agents {
  position: relative;
  width: 100%;
  height: 100%;
  display: flex;
  flex-direction: column;
  padding: 20px;
  background-color: #fafafa;
  overflow: hidden;
}

.message-time {
  font-size: 12px;
  color: #999;
  margin-right: 10px;
}

.badge-text {
  color: white;
  font-size: 12px;
  font-weight: bold;
  line-height: 1;
}

/* 内联端点对外标记 */
.exposed-badge-inline {
  width: 16px;
  height: 16px;
  border-radius: 50%;
  background-color: #00704A;
  display: flex;
  align-items: center;
  justify-content: center;
  box-shadow: 0 1px 2px rgba(0, 0, 0, 0.1);
  flex-shrink: 0;
}

.exposed-badge-inline .badge-text {
  font-size: 10px;
}

/* 表单样式（面板外壳的 .panel-* / .close-icon 已随设置面板迁到 agents/AgentSettingsPanel.vue） */
:deep(.el-form-item__label) {
  font-weight: 500;
  color: #333;
}

:deep(.el-input__wrapper) {
  border-radius: 8px;
}

:deep(.el-textarea__inner) {
  border-radius: 8px;
  font-family: 'Courier New', Courier, monospace;
}


</style>