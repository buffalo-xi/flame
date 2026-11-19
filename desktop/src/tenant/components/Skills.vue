<template>
  <div class="agent-skills">
    <!-- ============ 列表模式 ============ -->
    <!-- 列表模式 -->
    <SkillList v-if="!fullScreen" :skills="skills" :loading="loading" :agent-count="agents.length"
               @open="openEditor" @create="openCreate"
               @import="importVisible = true"
               @export="(s) => exportEntity('skill', s.id, s.name)"
               @status="({ row, status }) => setStatus(row, status)"
               @delete="removeSkill" @refresh="fetchSkills" />

    <!-- ============ 全屏编辑模式（点击卡片 / 新建进入） ============ -->
    <div v-else class="full-screen-form">
      <div ref="headerRef" class="full-screen-header" :class="'header--' + headerSize">
        <div class="header-actions">
          <el-button size="small" @click="handleBack">
            <el-icon><ArrowLeft /></el-icon>{{ $t('skill.back') }}
          </el-button>
        </div>
        <el-icon class="header-icon"><Collection /></el-icon>
        <div class="header-info">
          <!-- 名称：点击可改名（内置受保护技能除外）；新建时直接输入 -->
          <div class="header-name-wrapper" :class="{ locked: editingProtected || editorReadonly }">
            <el-tooltip v-if="editingProtected || editorReadonly"
                        :content="editingProtected ? $t('skill.nameLockedProtected') : $t('agent.readonlyLockTip')"
                        placement="bottom">
              <span class="header-name">{{ form.name }}</span>
            </el-tooltip>
            <template v-else-if="editingId">
              <span v-if="!isEditingName" class="header-name"
                    :title="form.name ? $t('skill.renameTitle', { name: form.name }) : $t('skill.clickRename')"
                    @click="startEditName">
                {{ form.name }}
              </span>
              <el-input v-else v-model="editName" size="small" class="header-name-input" maxlength="64"
                        :placeholder="$t('skill.namePlaceholder')"
                        @blur="saveEditName" @keyup.enter="saveEditName" ref="nameInputRef" />
            </template>
            <el-input v-else v-model="form.name" size="small" class="header-name-input" maxlength="64"
                      :placeholder="$t('skill.namePlaceholder')" ref="nameInputRef" />
          </div>
          <!-- 描述：点击打开大框编辑（头部这行只有 12px，长描述在原位输入框里没法写） -->
          <div class="header-desc-wrapper" :class="{ locked: editorReadonly }">
            <el-tooltip v-if="editorReadonly" :content="$t('agent.readonlyLockTip')" placement="bottom">
              <span class="header-desc">{{ form.description || $t('skill.emptyDescLong') }}</span>
            </el-tooltip>
            <span v-else class="header-desc"
                  :title="form.description || $t('skill.emptyDescLong')"
                  @click="openDescDialog">
              {{ form.description || $t('skill.emptyDescLong') }}
            </span>
          </div>
        </div>
        <div class="header-buttons">
          <!-- 只读提示（参考工作流编辑器）：只读角标 + 悬停说明 -->
          <span v-if="editorReadonly" class="readonly-badge" :title="readonlyBadgeTitle">{{ readonlyBadgeText }}</span>
          <span v-if="fileState.hasDirty" class="dirty-tip">{{ $t('skill.dirty') }}</span>
          <!-- 版本：vN / 未发布 / 有改动 三态，与工作流编辑器同口径（私有技能显示「随所属智能体」） -->
          <VersionEntry v-if="editingId" kind="skill" :target="editingId"
                        :current-version="detailVersion" :dirty="detailDirty"
                        :can-release="!editorReadonly" :private-hint="form.scope === 'agent'"
                        @applied="onVersionApplied" @changed="refreshVersionState" />
          <el-button size="small" class="header-action-btn settings-toggle-btn"
                     :class="{ active: settingsVisible }" @click="toggleSettings">{{ $t('skill.settings') }}</el-button>
          <el-button size="small" type="primary" class="header-action-btn"
                    :loading="savingFile" :disabled="editorReadonly" @click="handleSaveContent">
            {{ editingId ? $t('skill.saveContent') : $t('skill.createSkill') }}
          </el-button>
          <!-- 窄档：设置收进「⋯」，保存/创建始终保留 -->
          <el-dropdown class="header-more" trigger="click" @command="onHeaderMore">
            <el-button size="small" class="header-action-btn more-btn" :title="$t('skill.more')">
              <el-icon><MoreFilled /></el-icon>
            </el-button>
            <template #dropdown>
              <el-dropdown-menu>
                <el-dropdown-item command="settings">{{ $t('skill.settings') }}</el-dropdown-item>
              </el-dropdown-menu>
            </template>
          </el-dropdown>
        </div>
      </div>

      <div class="full-screen-content">
        <!-- key 随「载入版本」递增：版本内容整目录替换，必须重挂载才看得到新文件 -->
        <SkillFileEditor :key="editorEpoch" ref="fileEditorRef" :skill-id="editingId"
                         :readonly="editorReadonly" @state="onFileState" />

        <!-- 设置面板（可见性 / 工具依赖）+ 依赖选择器：组件见 skills/SkillSettingsPanel.vue -->
        <!-- key 随「载入版本」递增：载入会把可见性 / 工具依赖也写回草稿，面板必须重读 -->
        <SkillSettingsPanel :key="editorEpoch" :visible="settingsVisible" :skill-id="editingId"
                             :form="form" :agents="agents" :readonly="editorReadonly"
                             @close="settingsVisible = false" />
      </div>
    </div>

    <!-- 资产包导入（对话框本体是四类资产共用的通用组件） -->
    <AssetImportDialog v-model="importVisible" kind="skill" @imported="fetchSkills" />

    <!-- 描述单独用大框编辑：头部那行字号只有 12px、还被单行省略，长描述在原位输入框里基本没法写 -->
    <el-dialog v-model="descDialogVisible" :title="$t('skill.descDialogTitle')" width="640px" append-to-body
               :close-on-click-modal="false" class="desc-dialog">
      <el-input v-model="descDraft" type="textarea" :rows="8" resize="vertical"
                :placeholder="$t('skill.descDialogPlaceholder')"
                @keydown.ctrl.enter="saveEditDesc" />
      <div class="desc-dialog-hint">
        {{ $t('skill.descDialogHint') }}
      </div>
      <template #footer>
        <el-button size="small" @click="descDialogVisible = false">{{ $t('common.cancel') }}</el-button>
        <el-button size="small" type="primary" @click="saveEditDesc">{{ $t('common.confirm') }}</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup>
import { ref, computed, onMounted, onUnmounted, onDeactivated, nextTick, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { ElMessage, ElMessageBox } from 'element-plus'
// 图标走全局注册（main.js），模板只用到 ArrowLeft / Collection / MoreFilled
import SkillList from './skills/SkillList.vue'
import SkillFileEditor from './skills/SkillFileEditor.vue'
import SkillSettingsPanel from './skills/SkillSettingsPanel.vue'
import AssetImportDialog from './AssetImportDialog.vue'
import VersionEntry from './versions/VersionEntry.vue'
import adminApi from '../config/admin.js'
import { getToken } from '../utils/auth.js'
import { exportEntity } from '../utils/assetTransfer.js'
import { useHeaderCompact } from '../utils/useHeaderCompact.js'
import { currentLocale } from '../../locales'
import { formatError } from '../../locales'

// 脚本侧文案（标签、toast、确认框）走 t()
const { t } = useI18n()

const skills = ref([])
const loading = ref(false)

// 跨页跳转（会话里建 / 改了技能）：切到本页后直接进入该技能的全屏编辑页。
// id / name 都认：create_*_skill 回 id，update_*_skill 只回（可能是改名后的）名字。
// 父层每次传入新对象（含 ts），所以同一技能重复点也能触发。
const props = defineProps({ focus: { type: Object, default: null } })

// 资产包导入（对话框本体是通用组件，这里只管开关）
const importVisible = ref(false)

// agent / skill / flow 名称规则：字母或数字开头，后续只允许字母数字横线，长度 ≤64
const NAME_RE = /^[a-zA-Z0-9][a-zA-Z0-9-]*$/

// ============ 全屏编辑模式（与 agent 编辑器同构） ============
const fullScreen = ref(false)
const editingId = ref('')
// 只读模式：技能处于废弃(3)时允许打开查看，但禁止一切修改（后端写入同口径拦截）
const editorReadonly = ref(false)
// 编辑器（skills/SkillFileEditor.vue）自己持有内容目录与 tab；这里只留它的对外状态
const fileEditorRef = ref(null)
const fileState = ref({ canSave: false, hasDirty: false, saving: false })
const onFileState = (s) => { fileState.value = s }
// 新建技能（还没有 id）时的保存按钮 loading，与编辑器内部的文件保存无关
const savingFile = ref(false)
// 当前编辑技能的状态（用于只读角标文案）
const editorStatus = ref(1)
// 版本状态（后端详情接口算好）：current_version = 0 表示没发布过；dirty = 草稿与当前版本不一致
const detailVersion = ref(0)
const detailDirty = ref(false)
// 「载入版本」会整目录替换草稿：文件编辑器靠这个 key 重挂载
const editorEpoch = ref(0)
// 只读角标文案（参考工作流编辑器的「已废弃 · 只读」）
const readonlyBadgeText = computed(() => t('skill.readonlyBadge'))
const readonlyBadgeTitle = computed(() => t('skill.readonlyBadgeTip'))
const form = ref({
  name: '',
  description: '',
  scope: 'tenant',        // tenant 租户级 | agent 私有
  agent_id: '',
  owner_agent_name: '',
  restricted: true,       // 仅租户级：false 全体可见 | true 仅授权 agent
  bindingAgentIds: [],
  user_invocable: false,
  command_dispatch: '',
  command_tool: ''
})
// 头部窄屏分层降级：宽度不足时收起次要按钮（描述只截断，不收）
const headerRef = ref(null)
const headerSize = useHeaderCompact(headerRef, { md: 980, sm: 800 })
const onHeaderMore = (cmd) => { if (cmd === 'settings') toggleSettings() }

// 列表头部窄屏分层降级：先把状态筛选合并为下拉，仍不够再把搜索框收成图标（点击展开为整行搜索）
// 描述弹窗：头部那行只做单行省略显示，点开这里编辑
const descDialogVisible = ref(false)
const descDraft = ref('')
const nameInputRef = ref(null)
// 名称内联编辑（点击名称进入；受保护技能不允许改名）
const isEditingName = ref(false)
const editName = ref('')
const editingProtected = ref(false)

const authHeaders = () => ({
  'Content-Type': 'application/json',
  'Authorization': `Bearer ${getToken()}`
})

// agent 列表（用于「可见范围」的授权选择）
const agents = ref([])
const fetchAgents = async () => {
  try {
    const response = await fetch(adminApi.agents.base, {
      method: 'POST',
      headers: authHeaders(),
      body: JSON.stringify({ page: 1, limit: -1 })
    })
    if (!response.ok) return
    const data = await response.json()
    agents.value = data?.data?.items || []
  } catch {
    // 静默失败：不影响技能列表
  }
}

const formatTime = (t) => {
  if (!t) return ''
  return String(t).replace('T', ' ').slice(0, 19)
}


// 归属/可见性标签：
//   私有（agent 专属）→ 私有
//   租户级 → 共享N，N = 能使用它的智能体数：
//     全员可见（restricted=false）→ 全部 agent 数
//     白名单（restricted=true）  → 绑定的 agent 数（bound_count）
const scopeTag = (s) => {
  if (s.agent_id) return { type: 'success', text: t('skill.scopePrivate') }
  const n = s.restricted ? (s.bound_count || 0) : agents.value.length
  return {
    type: s.restricted && n === 0 ? 'danger' : 'info',
    text: t('skill.scopeShared', { n })
  }
}

const fetchSkills = async () => {
  loading.value = true
  try {
    const response = await fetch(adminApi.skills.base, {
      method: 'POST',
      headers: authHeaders(),
      body: JSON.stringify({ page: 1, limit: -1 })
    })
    if (!response.ok) throw new Error(t('skill.loadFailed'))
    const data = await response.json()
    const items = data?.data?.items || []
    items.forEach((s) => { s._statusLoading = false })
    skills.value = items
  } catch (e) {
    ElMessage.error(e.message || t('skill.loadFailed'))
  } finally {
    loading.value = false
  }
}

// ============ 进入 / 退出编辑 ============
const resetForm = () => {
  form.value = {
    name: '', description: '',
    scope: 'tenant', agent_id: '', owner_agent_name: '',
    restricted: true, bindingAgentIds: [], user_invocable: false,
    command_dispatch: '', command_tool: ''
  }
}
const resetEditorState = () => {
  // 内容目录 / 已开 tab 归编辑器组件管，可见性 / 工具依赖草稿归设置面板管
  // （两者都按 skillId 变化自己重置）
  descDialogVisible.value = false
  isEditingName.value = false
  editingProtected.value = false
  settingsVisible.value = false
  editorReadonly.value = false
  editorStatus.value = 1
  detailVersion.value = 0
  detailDirty.value = false
}

const openCreate = () => {
  editingId.value = ''
  resetForm()
  resetEditorState()
  fullScreen.value = true
}

const openEditor = async (row) => {
  // 废弃(3)：允许只读打开查看，但禁止修改（后端写入同口径拦截）
  const readonly = (row.status || 1) !== 1
  editingId.value = row.id
  resetForm()
  resetEditorState()
  // 每次点卡片都刷新「可见范围」的 agent 候选（工具目录缓存由设置面板自己按 skillId 失效）
  fetchAgents()
  editorReadonly.value = readonly
  editorStatus.value = row.status || 1
  fullScreen.value = true
  try {
    const res = await fetch(adminApi.skills.get(row.id), { headers: authHeaders() })
    const body = await res.json().catch(() => ({}))
    if (body.code !== 200) throw new Error(formatError(body) || t('skill.loadDetailFailed'))
    const d = body.data || {}
    editingProtected.value = !!d.protected
    // 版本状态由后端按「当前版本 vs 草稿」算好（含描述 / 依赖 / 文件），前端不自己推
    detailVersion.value = d.current_version || 0
    detailDirty.value = !!d.dirty
    form.value = {
      name: d.name || '',
      description: d.description || '',
      scope: d.agent_id ? 'agent' : 'tenant',
      agent_id: d.agent_id || '',
      owner_agent_name: d.agent_name || '',
      restricted: !!d.restricted,
      bindingAgentIds: [],
      user_invocable: !!d.user_invocable,
      command_dispatch: d.command_dispatch || '',
      command_tool: d.command_tool || ''
    }
    // 可见性 / 工具依赖由设置面板在打开或切 tab 时懒加载，
    // 内容目录与 SKILL.md 由编辑器组件按 skillId 自动加载
  } catch (e) {
    ElMessage.error(e.message || t('skill.loadDetailFailed'))
  }
}

// ============ 版本（vN / 未发布 / 有改动） ============
// 发布或载入之后：重读详情并重挂载文件树 —— 载入会把整份定义（元数据 + 文件）写回草稿
const onVersionApplied = async () => {
  if (!editingId.value) return
  editorEpoch.value++
  await openEditor({ id: editingId.value, status: editorStatus.value })
  fetchSkills()
}

// 草稿被改（存文件 / 存属性）后只刷版本徽标，不重载编辑器
const refreshVersionState = async () => {
  if (!editingId.value) return
  try {
    const res = await fetch(adminApi.skills.get(editingId.value), { headers: authHeaders() })
    const body = await res.json().catch(() => ({}))
    if (body.code !== 200) return
    detailVersion.value = body.data?.current_version || 0
    detailDirty.value = !!body.data?.dirty
  } catch {
    // 版本状态是锦上添花：拿不到就不更新，不影响编辑
  }
}

// 文件编辑器保存成功后 hasDirty 会回落：此刻草稿已变，版本徽标要跟着变
watch(() => fileState.value.hasDirty, (now, before) => {
  if (before && !now) refreshVersionState()
})

watch(() => props.focus, async (f) => {
  if (!f || f.nav !== 'agent-skills' || !(f.id || f.name)) return
  const find = () => skills.value.find((s) =>
    (f.id && String(s.id) === String(f.id)) ||
    (f.name && String(s.name) === String(f.name)))
  let row = find()
  // 会话里刚建的技能还没进本页的列表快照：先刷一次再找
  if (!row) {
    await fetchSkills()
    row = find()
  }
  if (row) openEditor(row)
})

const handleBack = () => {
  fullScreen.value = false
  editingId.value = ''
  resetForm()
  resetEditorState()
  fetchSkills()
}

// ============ 头部：名称 / 描述 / 设置 ============
// 改名：点击名称进编辑态；保存走后端 PUT（name = 新名，后端会校验重名并迁移内容目录）
const startEditName = () => {
  if (blockIfReadonly()) return
  if (!editingId.value || editingProtected.value) return
  editName.value = form.value.name
  isEditingName.value = true
  nextTick(() => nameInputRef.value?.focus())
}
const saveEditName = async () => {
  if (!isEditingName.value) return
  isEditingName.value = false
  const prev = form.value.name
  const next = (editName.value || '').trim()
  if (!next || next === prev) return
  if (!NAME_RE.test(next)) {
    ElMessage.warning(t('skill.nameRuleHint'))
    return
  }
  try {
    await putSkill({ name: next })
    form.value.name = next
    ElMessage.success(t('agent.renamed'))
    // 内容目录名随技能名变化：重列当前目录（条目路径是相对路径，已打开的 tab 不受影响）
    await fileEditorRef.value?.reloadDir()
    fetchSkills() // 刷新列表卡片上的名字
  } catch (e) {
    ElMessage.error(e.message || t('agent.renameFailed'))
  }
}

const openDescDialog = () => {
  if (blockIfReadonly()) return
  descDraft.value = form.value.description || ''
  descDialogVisible.value = true
}
// 描述修改后立即保存（PUT 需要带上 name）；失败不关弹窗，保留刚写的内容
const saveEditDesc = async () => {
  const next = (descDraft.value || '').trim()
  if (next === form.value.description) { descDialogVisible.value = false; return }
  const prev = form.value.description
  form.value.description = next
  if (!editingId.value) { descDialogVisible.value = false; return } // 未创建：等创建时一起提交
  try {
    await putSkill({ description: next })
    descDialogVisible.value = false
  } catch (e) {
    form.value.description = prev
    ElMessage.error(e.message || t('agent.descSaveFailed'))
  }
}

const settingsVisible = ref(false)
const toggleSettings = () => { settingsVisible.value = !settingsVisible.value }
// 设置面板 tab：切到工具依赖时按需加载；切走时收起左侧「添加工具 / 组」选择器

// ============ 保存（属性 / 可见性 / 工具依赖） ============
// 更新技能元数据：后端按全量处理（name / 命令字段，空值即清空），
// 所以这里必须把当前表单里的字段一并带上，避免只改描述却把命令配置抹掉。
// 只读模式下拦截任何修改操作（UI 已禁用，这里做兜底）
const blockIfReadonly = () => {
  if (!editorReadonly.value) return false
  ElMessage.warning({ message: t('skill.readonlyMode'), duration: 2000, offset: 20 })
  return true
}

const putSkill = async (patch) => {
  if (editorReadonly.value) throw new Error(t('skill.readonlySave'))
  const payload = {
    name: form.value.name,
    description: form.value.description,
    user_invocable: !!form.value.user_invocable,
    command_dispatch: form.value.command_dispatch || '',
    command_tool: form.value.command_tool || '',
    ...patch
  }
  const res = await fetch(adminApi.skills.update(editingId.value), {
    method: 'PUT',
    headers: authHeaders(),
    body: JSON.stringify(payload)
  })
  const body = await res.json().catch(() => ({}))
  if (body.code !== 200) throw new Error(formatError(body) || t('agent.saveFailed'))
  // 元数据也是定义的一部分（描述 / 命令 / 可见性）：存完徽标可能从「一致」变成「有改动」
  refreshVersionState()
}


// 头部保存：未创建 → 走创建；已创建 → 保存当前文件
const handleSaveContent = async () => {
  if (blockIfReadonly()) return
  if (!editingId.value) {
    const f = form.value
    f.name = (f.name || '').trim()
    if (!f.name) { ElMessage.warning(t('skill.nameRequired')); return }
    if (!NAME_RE.test(f.name)) {
      ElMessage.warning(t('skill.nameRuleHint'))
      return
    }
    if (f.scope === 'agent' && !f.agent_id) {
      ElMessage.warning(t('skill.ownerRequired'))
      return
    }
    savingFile.value = true
    try {
      const res = await fetch(adminApi.skills.create, {
        method: 'POST',
        headers: authHeaders(),
        body: JSON.stringify({
          name: f.name,
          description: f.description,
          agent_id: f.scope === 'agent' ? f.agent_id : '',
          content: '',
          restricted: !!f.restricted,
          // lang 只影响新建时 SKILL.md 骨架的语言，不落库；不传则由后端回落英文
          lang: currentLocale()
        })
      })
      const body = await res.json().catch(() => ({}))
      if (body.code !== 200) throw new Error(formatError(body) || t('agent.createFailed'))
      // 换成新 id 后，编辑器组件会自动 loadDir('') + 打开 SKILL.md
      editingId.value = body?.data?.id || ''
      ElMessage.success(t('agent.created'))
    } catch (e) {
      ElMessage.error(e.message || t('agent.createFailed'))
    } finally {
      savingFile.value = false
    }
    return
  }
  fileEditorRef.value?.saveCurrentFile()
}

// ============ 列表：状态切换（两态）/ 删除 ============
// 1 正常 / 3 废弃。skills 没有服务端点，所以这里没有"端点收回联动"；
// 但 3 会让技能变为只读（后端 UpdateSkill / DeleteSkill / SetSkillVisibility 都拦）。
// 列表的筛选/卡片展示在 skills/SkillList.vue，这里只留会改数据、要刷新的动作。
const statusMeta = (status) => (status === 3
  ? { text: t('skill.filterDeprecated'), tag: 'danger' }
  : { text: t('skill.filterNormal'), tag: 'success' })

const setStatus = async (row, status) => {
  const meta = statusMeta(status)
  // 只有「废弃」需要确认：它不可逆，且技能会从智能体视野里消失。
  // 切回「正常」能一键改回来，别拿弹窗拦人。
  if (status === 3) {
    try {
      await ElMessageBox.confirm(
        t('skill.deprecateConfirm', { name: row.name }),
        t('agent.prompt'),
        {
          type: 'warning',
          confirmButtonText: t('common.confirm'),
          cancelButtonText: t('common.cancel')
        }
      )
    } catch {
      return
    }
  }
  row._statusLoading = true
  try {
    const response = await fetch(adminApi.skills.status(row.id), {
      method: 'PUT',
      headers: authHeaders(),
      body: JSON.stringify({ status })
    })
    if (!response.ok) throw new Error(t('skill.statusUpdateFailed'))
    const body = await response.json().catch(() => ({}))
    if (body.code !== 200) throw new Error(formatError(body) || t('skill.statusUpdateFailed'))
    row.status = status
    ElMessage.success({ message: t('agent.statusChanged', { status: meta.text }), duration: 700 })
  } catch (e) {
    ElMessage.error(e.message || t('skill.statusUpdateFailed'))
  } finally {
    row._statusLoading = false
  }
}

const removeSkill = async (row) => {
  try {
    await ElMessageBox.confirm(
      t('skill.deleteConfirm', { name: row.name }),
      t('agent.prompt'),
      {
        type: 'warning',
        confirmButtonText: t('common.confirm'),
        cancelButtonText: t('common.cancel')
      }
    )
  } catch {
    return
  }
  try {
    const response = await fetch(adminApi.skills.delete(row.id), { method: 'DELETE', headers: authHeaders() })
    if (!response.ok) throw new Error(t('agent.deleteFailed'))
    ElMessage.success(t('skill.deleted'))
    await fetchSkills()
  } catch (e) {
    ElMessage.error(e.message || t('agent.deleteFailed'))
  }
}

onMounted(() => {
  fetchSkills()
  fetchAgents()
})
</script>

<style scoped>
.agent-skills {
  /* 全屏编辑模式 .full-screen-form 以它为定位基准（与 Agents.vue 的 .agents 一致）；
     缺了 position 时绝对定位会落到更外层容器，头部会被顶到可视区之外 */
  position: relative;
  width: 100%;
  height: 100%;
  display: flex;
  flex-direction: column;
  padding: 20px;
  background-color: #fafafa;
  overflow: hidden;
  box-sizing: border-box;
}


/* ============ 全屏编辑模式（对齐智能体编辑器） ============ */
.full-screen-form {
  position: absolute;
  inset: 0;
  display: flex;
  flex-direction: column;
  background: #fff;
  z-index: 10;
}

/* 头部：与智能体编辑页（Agents.vue）一致，高度同样锁 64px（对齐工作台 .wb-tabbar） */
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
  cursor: pointer; /* 点击改名 */
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

.header-name:hover,
.header-desc:hover {
  color: #1890ff;
}

/* 受保护技能（不可改名）：光标/颜色保持普通 */
.header-name-wrapper.locked,
.header-name-wrapper.locked * {
  cursor: default;
}

.header-name-wrapper.locked .header-name:hover {
  color: #333;
}

/* 只读时描述同样保持普通光标 */
.header-desc-wrapper.locked,
.header-desc-wrapper.locked * {
  cursor: default;
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

.header-name-input {
  width: min(200px, 100%);
}

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

.header-buttons .el-button {
  border-radius: 20px;
}

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

/* 窄档：设置收进「⋯」，保存/创建始终保留 */
.header--sm .settings-toggle-btn {
  display: none;
}
.header--sm .header-more {
  display: inline-flex;
}

.settings-toggle-btn.active {
  color: #409eff;
  border-color: #a0cfff;
  background: #ecf5ff;
}

.full-screen-content {
  flex: 1;
  min-height: 0;
  display: flex;
  gap: 6px; /* 两块白卡片之间的间距（对齐智能体编辑页） */
  /* 与智能体编辑页（Agents.vue）同口径：不加内边距，
     让左侧文件树卡片、右侧编辑器卡片的左右边缘直接贴齐头部栏的左右边缘。 */
  padding: 0;
  background: #f5f7fa;
  /* 注意：不能加 position: relative —— 设置面板要像智能体设置面板那样占满整个编辑区高度，
     需以 .full-screen-form（absolute）作为绝对定位基准 */
}


</style>
