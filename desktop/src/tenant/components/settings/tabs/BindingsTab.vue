<template>
  <div class="module-section">
    <div class="module-head">
      <h3>{{ t('settings.tabs.svcauthbindings.title') }}</h3>
      <div class="head-actions">
        <el-button type="primary" size="small" :icon="Refresh" @click="loadBindings">{{ t('settings.bindings.refresh') }}</el-button>
      </div>
    </div>

    <div v-loading="bindingLoading" class="config-form">
      <el-alert type="info" :closable="false" show-icon class="config-tip">
        <template #title>{{ t('settings.bindings.tipTitle') }}</template>
        {{ t('settings.bindings.tipIntro') }}
        <br />
        {{ t('settings.bindings.tipCreate') }}
      </el-alert>

      <!-- 绑定列表：API Key / OAuth 两个 tab -->
      <el-tabs v-model="bindingTab" class="binding-tabs">
        <el-tab-pane label="API Key" name="apikey">
          <div class="client-cards">
            <div v-for="b in apikeyBindings" :key="b.id" class="client-card">
              <div class="client-card-head">
                <span class="binding-user" :title="userCardTitle(b.user_id)">
                  {{ userLabel(b.user_id) }}
                  <span v-if="userDisplayName(b.user_id)" class="binding-user-alias">{{ userDisplayName(b.user_id) }}</span>
                </span>
                <div class="card-head-actions">
                  <el-switch
                    :model-value="b.is_active"
                    size="small"
                    inline-prompt
                    :active-text="t('settings.bindings.switchOn')"
                    :inactive-text="t('settings.bindings.switchOff')"
                    @change="(v) => toggleBinding(b, v)"
                  />
                  <el-button
                    circle
                    size="small"
                    type="primary"
                    :icon="b.showSecret ? Hide : View"
                    :title="b.showSecret ? t('settings.bindings.hide') : t('settings.bindings.show')"
                    @click="b.showSecret = !b.showSecret"
                  />
                  <el-button circle size="small" :icon="CopyDocument" :title="t('settings.bindings.copy')"
                             @click="copyText(b.external_subject)" />
                  <el-button link type="danger" :icon="Delete" @click="removeBinding(b)" />
                </div>
              </div>
              <div class="client-card-body">
                <span class="apikey-label">API Key</span>
                <code class="apikey-value" :class="{ 'apikey-inactive': !b.is_active }">{{ b.showSecret ? b.external_subject : maskSecret(b.external_subject) }}</code>
              </div>
              <!-- 端点范围：三种状态都要能一眼分清 —— 不限制=全开，已限制但空=全禁（新建绑定的初始态） -->
              <div class="binding-scope">
                <span class="binding-scope-label">{{ t('settings.bindings.scopeLabel') }}</span>
                <span v-if="b.restrict_targets && b.target_count === 0" class="binding-scope-warn">{{ t('settings.bindings.scopeRestrictedEmpty') }}</span>
                <span v-else-if="b.restrict_targets" class="binding-scope-ok">{{ t('settings.bindings.scopeRestrictedCount', { n: b.target_count }) }}</span>
                <span v-else-if="b.exclude_supervisor" class="binding-scope-ok">{{ t('settings.bindings.scopeExcludeSupervisor') }}</span>
                <span v-else class="binding-scope-warn">{{ t('settings.bindings.scopeUnrestricted') }}</span>
                <el-button link type="primary" size="small" @click="openScopeDialog(b)">{{ t('settings.bindings.configure') }}</el-button>
              </div>
            </div>
            <div v-if="apikeyBindings.length === 0" class="binding-empty">{{ t('settings.bindings.empty') }}</div>
          </div>
          <div class="binding-add-row">
            <el-select v-model="newApiKeyUserId" :placeholder="t('settings.bindings.selectUser')" filterable size="small" style="width: 240px">
              <el-option v-for="u in userOptions" :key="u.id" :label="userOptionLabel(u)" :value="u.id" />
            </el-select>
            <el-button type="primary" size="small" :icon="Plus" :disabled="!newApiKeyUserId" @click="addApiKeyBinding">{{ t('settings.bindings.addApiKey') }}</el-button>
          </div>
        </el-tab-pane>

        <el-tab-pane label="OAuth" name="oauth">
          <div class="client-cards">
            <div v-for="b in oauthBindings" :key="b.id" class="client-card">
              <div class="client-card-head">
                <span class="binding-user" :title="userCardTitle(b.user_id)">
                  {{ userLabel(b.user_id) }}
                  <span v-if="userDisplayName(b.user_id)" class="binding-user-alias">{{ userDisplayName(b.user_id) }}</span>
                </span>
                <div class="card-head-actions">
                  <el-switch
                    :model-value="b.is_active"
                    size="small"
                    inline-prompt
                    :active-text="t('settings.bindings.switchOn')"
                    :inactive-text="t('settings.bindings.switchOff')"
                    @change="(v) => toggleBinding(b, v)"
                  />
                  <el-button link type="danger" :icon="Delete" @click="removeBinding(b)" />
                </div>
              </div>
              <div class="client-card-body binding-oauth-body">
                <div class="binding-oauth-row">
                  <span class="binding-oauth-label">{{ t('settings.bindings.issuerLabel') }}</span>
                  <code class="apikey-value">{{ b.external_issuer || '-' }}</code>
                </div>
                <div class="binding-oauth-row">
                  <span class="binding-oauth-label">{{ t('settings.bindings.subjectLabel') }}</span>
                  <code class="apikey-value">{{ b.external_subject }}</code>
                </div>
              </div>
              <div class="binding-scope">
                <span class="binding-scope-label">{{ t('settings.bindings.scopeLabel') }}</span>
                <span v-if="b.restrict_targets && b.target_count === 0" class="binding-scope-warn">{{ t('settings.bindings.scopeRestrictedEmpty') }}</span>
                <span v-else-if="b.restrict_targets" class="binding-scope-ok">{{ t('settings.bindings.scopeRestrictedCount', { n: b.target_count }) }}</span>
                <span v-else-if="b.exclude_supervisor" class="binding-scope-ok">{{ t('settings.bindings.scopeExcludeSupervisor') }}</span>
                <span v-else class="binding-scope-warn">{{ t('settings.bindings.scopeUnrestricted') }}</span>
                <el-button link type="primary" size="small" @click="openScopeDialog(b)">{{ t('settings.bindings.configure') }}</el-button>
              </div>
            </div>
            <div v-if="oauthBindings.length === 0" class="binding-empty">{{ t('settings.bindings.empty') }}</div>
          </div>
          <div class="binding-add-row">
            <el-select v-model="newOauthForm.user_id" :placeholder="t('settings.bindings.selectUser')" filterable size="small" style="width: 200px">
              <el-option v-for="u in userOptions" :key="u.id" :label="userOptionLabel(u)" :value="u.id" />
            </el-select>
            <el-input v-model="newOauthForm.external_issuer" :placeholder="t('settings.bindings.issuerPlaceholder')" size="small" style="width: 220px" />
            <el-input v-model="newOauthForm.external_subject" :placeholder="t('settings.bindings.subjectPlaceholder')" size="small" style="width: 200px" />
            <el-button
              type="primary"
              size="small"
              :icon="Plus"
              :disabled="!newOauthForm.user_id || !newOauthForm.external_subject"
              @click="addOauthBinding"
            >{{ t('settings.bindings.addOauth') }}</el-button>
          </div>
        </el-tab-pane>
      </el-tabs>
    </div>

    <!-- 端点访问范围：按 agent / flow 目标授权（覆盖该目标的 rpc 与 mcp 端点）。
         候选目标不用下拉：在配置面板左侧再开一个同高列表窗口，一次看全并勾选。 -->
    <SidePanel panel-class="scope-picker" :visible="scopeDialogVisible && scopePickerVisible"
               :title="t('settings.bindings.pickerTitle')" :panel-style="scopePickerStyle"
               :loading="scopeDialogLoading" @close="scopePickerVisible = false">
      <!-- 两块区域各自成卡片：普通端点在前，超级智能体（管理入口，警示色）单独一块放最后 -->
      <div v-for="g in scopeCandidateGroups" :key="g.key" class="scope-group"
           :class="`scope-group--${g.key}`">
        <div class="scope-group-title">
          {{ g.title }}<span class="scope-group-count">{{ g.items.length }}</span>
        </div>
        <!-- 超级智能体的端点等于租户管理入口：只提示，不隐藏（隐藏会让人以为"没有这个选项"） -->
        <div v-if="g.key === 'supervisor'" class="scope-group-warn">
          {{ t('settings.bindings.supervisorWarn') }}
        </div>
        <div class="scope-group-items">
          <div v-for="c in g.items" :key="scopeKey(c)" class="scope-item"
               :class="{ selected: scopeSelectedKeys.includes(scopeKey(c)), disabled: !scopeRestrict }"
               @click="toggleScopeTarget(c)">
            <span class="scope-item-check"><el-icon v-if="scopeSelectedKeys.includes(scopeKey(c))"><Check /></el-icon></span>
            <span class="scope-item-main">
              <span class="scope-item-name">{{ c.name || c.target_id }}</span>
              <span class="scope-item-meta">
                {{ c.target_type === 'agent' ? t('settings.bindings.targetTypeAgent') : t('settings.bindings.targetTypeFlow') }}
                · {{ (c.protocols || []).join(' / ') || t('settings.bindings.internalOnly') }}
              </span>
            </span>
          </div>
        </div>
      </div>
      <div v-if="!scopeDialogLoading && !scopeCandidates.length" class="scope-empty">
        {{ t('settings.bindings.pickerEmpty') }}
      </div>
    </SidePanel>

    <SidePanel :visible="scopeDialogVisible" :title="t('settings.bindings.panelTitle', { name: scopeBindingLabel })"
               :panel-style="sidePanel.styleFor(SCOPE_PANEL_WIDTH)"
               @close="scopeDialogVisible = false" @resize="sidePanel.startResize">
      <div class="settings-field">
        <div class="field-label">{{ t('settings.bindings.fieldScope') }}</div>
        <el-radio-group v-model="scopeMode">
          <el-radio value="all">{{ t('settings.bindings.modeAll') }}</el-radio>
          <el-radio value="exclude_supervisor">{{ t('settings.bindings.modeExclude') }}</el-radio>
          <el-radio value="selected">{{ t('settings.bindings.modeSelected') }}</el-radio>
        </el-radio-group>
        <div class="editor-tip">
          <template v-if="scopeMode === 'all'">
            {{ t('settings.bindings.tipAll1') }}<b>{{ t('settings.bindings.tipAllBold') }}</b>{{ t('settings.bindings.tipAll2') }}
          </template>
          <template v-else-if="scopeMode === 'exclude_supervisor'">
            {{ t('settings.bindings.tipExclude1') }}<b>{{ t('settings.bindings.tipExcludeBold') }}</b>{{ t('settings.bindings.tipExclude2') }}
          </template>
          <template v-else>
            {{ t('settings.bindings.tipSelected1') }}<b>{{ t('settings.bindings.tipSelectedBold') }}</b>{{ t('settings.bindings.tipSelected2') }}
          </template>
        </div>
      </div>

      <div class="settings-field">
        <div class="field-label">
          {{ t('settings.bindings.chosenLabel') }}
          <span class="scope-chosen-count">
            {{ t('settings.bindings.chosenCount', { n: scopeSelectedKeys.length, total: scopeCandidates.length }) }}
          </span>
        </div>

        <!-- 已选目标必须列出来：只给个数字看不出这条凭证到底能用哪些端点 -->
        <div v-if="scopeChosenItems.length" class="scope-chosen">
          <el-tag v-for="it in scopeChosenItems" :key="it.key" effect="light"
                  :type="it.supervisor ? 'warning' : 'primary'"
                  :closable="scopeRestrict" disable-transitions
                  @close="removeScopeTarget(it.key)">
            {{ it.name }}<span class="scope-chosen-type">{{
              it.type === 'agent' ? t('settings.bindings.targetTypeAgent') : t('settings.bindings.targetTypeFlow')
            }}{{ it.missing ? t('settings.bindings.missingExposed') : '' }}</span>
          </el-tag>
        </div>
        <div v-else class="scope-chosen-empty" :class="{ warn: scopeRestrict }">
          {{ scopeEmptyHint }}
        </div>

        <div class="scope-summary">
          <el-button size="small" type="primary" :disabled="!scopeRestrict"
                     @click="scopePickerVisible = !scopePickerVisible">
            {{ scopePickerVisible ? t('settings.bindings.collapseList') : t('settings.bindings.pickTargets') }}
          </el-button>
          <el-button size="small" :disabled="!scopeRestrict" @click="selectAllScope">{{ t('settings.bindings.selectNormal') }}</el-button>
          <el-button size="small" :disabled="!scopeRestrict" @click="clearScopeSelection">{{ t('settings.bindings.clearAll') }}</el-button>
        </div>
        <div class="editor-tip">
          {{ t('settings.bindings.pickHint') }}
        </div>

        <el-alert v-if="scopeRestrict && scopeSelectedKeys.length === 0" type="warning"
                  :closable="false" show-icon class="scope-alert">
          {{ t('settings.bindings.alertNoTarget1') }}<b>{{ t('settings.bindings.alertNoTargetBold') }}</b>{{ t('settings.bindings.alertNoTarget2') }}
        </el-alert>
        <el-alert v-else type="info" :closable="false" show-icon class="scope-alert">
          {{ t('settings.bindings.alertCandidates1') }}<b>{{ t('settings.bindings.alertCandidatesBold') }}</b>{{ t('settings.bindings.alertCandidates2') }}
        </el-alert>
      </div>
      <template #footer>
        <el-button @click="scopeDialogVisible = false">{{ t('settings.bindings.cancel') }}</el-button>
        <el-button type="primary" :loading="scopeDialogSaving" @click="saveScope">{{ t('settings.bindings.save') }}</el-button>
      </template>
    </SidePanel>
  </div>
</template>

<script setup>
import { ref, computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { ElMessage } from 'element-plus'
import { Plus, Delete, View, Hide, CopyDocument, Refresh, Check } from '@element-plus/icons-vue'
import apiConfig from '../../../config/api.js'
import { getToken } from '../../../utils/auth.js'
import SidePanel from '../SidePanel.vue'
import { useSidePanelContext } from '../useSidePanel.js'
import { useUsers } from '../useUsers.js'
import { useBindings } from '../useBindings.js'
import { maskSecret, copyText } from '../format.js'
import { formatError } from '../../../../locales'

const props = defineProps({
  // 当前 tab 是否激活：用它做惰性加载，避免进设置页就把绑定全拉一遍
  active: { type: Boolean, default: false }
})

const { userOptions, userLabel, userOptionLabel, userDisplayName, userCardTitle, ensureUserOptions } = useUsers()
const { apikeyBindings, oauthBindings, bindingLoading, loadBindings } = useBindings()
// 侧边面板坐标由父组件（Settings.vue）统一提供，父子面板才能对齐
const sidePanel = useSidePanelContext()

const { t } = useI18n()

// ===== 列表 / 新增 =====
const bindingTab = ref('apikey')
const newApiKeyUserId = ref('')
const newOauthForm = ref({ user_id: '', external_issuer: '', external_subject: '' })

// 每次切到本 tab 都刷一次：绑定可能被其他端/其他会话改动，列表必须能跟上
watch(() => props.active, (active) => {
  if (!active) return
  loadBindings()
  ensureUserOptions()
}, { immediate: true })

const addApiKeyBinding = async () => {
  if (!newApiKeyUserId.value) return
  try {
    const token = getToken()
    const resp = await fetch(apiConfig.admin.svcAuthBindings.create, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        'Authorization': `Bearer ${token}`
      },
      body: JSON.stringify({ auth_type: 'apikey', user_id: newApiKeyUserId.value })
    })
    if (!resp.ok) throw new Error(t('settings.bindings.addFailed'))
    const data = await resp.json()
    if (data.code === 200) {
      // 新建即受限（白名单为空 = 全禁），不提示的话会以为是 key 有问题
      ElMessage.success({
        message: t('settings.bindings.addedApiKey'),
        offset: 20,
        duration: 6000
      })
      newApiKeyUserId.value = ''
      await loadBindings()
    } else {
      ElMessage.error({ message: formatError(data) || t('settings.bindings.addFailed'), offset: 20 })
    }
  } catch (e) {
    ElMessage.error({ message: t('settings.bindings.addFailedDetail', { msg: e.message }), offset: 20 })
  }
}

const addOauthBinding = async () => {
  const f = newOauthForm.value
  if (!f.user_id || !f.external_subject) return
  try {
    const token = getToken()
    const resp = await fetch(apiConfig.admin.svcAuthBindings.create, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        'Authorization': `Bearer ${token}`
      },
      body: JSON.stringify({
        auth_type: 'oauth',
        user_id: f.user_id,
        external_issuer: f.external_issuer,
        external_subject: f.external_subject
      })
    })
    if (!resp.ok) throw new Error(t('settings.bindings.addFailed'))
    const data = await resp.json()
    if (data.code === 200) {
      ElMessage.success({
        message: t('settings.bindings.addedOauth'),
        offset: 20,
        duration: 6000
      })
      newOauthForm.value = { user_id: '', external_issuer: '', external_subject: '' }
      await loadBindings()
    } else {
      ElMessage.error({ message: formatError(data) || t('settings.bindings.addFailed'), offset: 20 })
    }
  } catch (e) {
    ElMessage.error({ message: t('settings.bindings.addFailedDetail', { msg: e.message }), offset: 20 })
  }
}

const removeBinding = async (b) => {
  try {
    const token = getToken()
    const resp = await fetch(apiConfig.admin.svcAuthBindings.delete(b.id), {
      method: 'DELETE',
      headers: { 'Authorization': `Bearer ${token}` }
    })
    if (!resp.ok) throw new Error(t('settings.bindings.deleteFailed'))
    const data = await resp.json()
    if (data.code === 200) {
      ElMessage.success({ message: t('settings.bindings.deleted'), offset: 20 })
      await loadBindings()
    }
  } catch (e) {
    ElMessage.error({ message: t('settings.bindings.deleteFailedDetail', { msg: e.message }), offset: 20 })
  }
}

const toggleBinding = async (b, val) => {
  try {
    const token = getToken()
    const resp = await fetch(apiConfig.admin.svcAuthBindings.update(b.id), {
      method: 'PUT',
      headers: {
        'Content-Type': 'application/json',
        'Authorization': `Bearer ${token}`
      },
      body: JSON.stringify({ user_id: b.user_id, is_active: val })
    })
    if (!resp.ok) throw new Error(t('settings.bindings.updateFailed'))
    const data = await resp.json()
    if (data.code === 200) {
      b.is_active = val
      ElMessage.success({
        message: val ? t('settings.bindings.enabled') : t('settings.bindings.disabled'),
        offset: 20
      })
    } else {
      ElMessage.error({ message: formatError(data) || t('settings.bindings.updateFailed'), offset: 20 })
    }
  } catch (e) {
    ElMessage.error({ message: t('settings.bindings.updateFailedDetail', { msg: e.message }), offset: 20 })
  }
}

// ===== 端点访问范围（svc_auth_binding_targets）=====
// 授权按 agent / flow 目标而不是端点：一个目标通常有 rpc 与 mcp 两条端点，
// 按端点授权会出现「只勾了 rpc，MCP 连接就全空」。
const SCOPE_PANEL_WIDTH = 460

const scopeDialogVisible = ref(false)
// 左侧候选列表是「按需打开」的第二层窗口：默认不出现，点右侧面板的「选择目标」才显示
const scopePickerVisible = ref(false)
const scopeDialogLoading = ref(false)
const scopeDialogSaving = ref(false)
const scopeBindingId = ref('')
const scopeBindingLabel = ref('')
const scopeRestrict = ref(false)
// 「不限制」的中间档：放行全部端点，但排除超级智能体。只在 scopeRestrict=false 时有意义，
// 两档互斥 —— 后端 SetTargets 在 restrict=true 时会把这一列强制写成 false。
const scopeExcludeSupervisor = ref(false)
const scopeCandidates = ref([])     // [{target_type, target_id, name, protocols, is_supervisor}]
const scopeSelectedKeys = ref([])   // ["agent:<uuid>", "flow:<uuid>"]

// 三档视图（存两列布尔，界面上只给一个三选一，避免「两个开关四种组合」的第四种歧义）
const scopeMode = computed({
  get() {
    if (scopeRestrict.value) return 'selected'
    return scopeExcludeSupervisor.value ? 'exclude_supervisor' : 'all'
  },
  set(mode) {
    scopeRestrict.value = mode === 'selected'
    scopeExcludeSupervisor.value = mode === 'exclude_supervisor'
  }
})

// 「已选目标」为空时的说明：三档各有各的含义，不能都写成「不限制」
const scopeEmptyHint = computed(() => {
  if (scopeRestrict.value) return t('settings.bindings.emptyHintRestrict')
  if (scopeExcludeSupervisor.value) return t('settings.bindings.emptyHintExclude')
  return t('settings.bindings.emptyHintAll')
})

// 混进父级的面板互斥集合：否则点外部时这两个面板不会跟着关
sidePanel.register(scopeDialogVisible)
sidePanel.register(scopePickerVisible)

const scopeKey = (target) => `${target.target_type}:${target.target_id}`

// 候选目标分两组：普通端点在前，超级智能体单独成组放在最后 ——
// 它是租户管理入口，不该混在常规选项里被顺手勾上。组内按名字排。
// 中文名用 localeCompare('zh') 排，避免默认码点序把中文排成乱序。
const scopeCandidateGroups = computed(() => {
  const sup = []
  const normal = []
  for (const c of scopeCandidates.value) {
    if (c.target_type === 'agent' && c.is_supervisor) sup.push(c)
    else normal.push(c)
  }
  const byName = (a, b) =>
    String(a.name || a.target_id).localeCompare(String(b.name || b.target_id), 'zh')
  sup.sort(byName)
  normal.sort(byName)
  return [
    { key: 'normal', title: t('settings.bindings.groupNormal'), items: normal },
    { key: 'supervisor', title: t('settings.bindings.groupSupervisor'), items: sup }
  ].filter((g) => g.items.length)
})

const toggleScopeTarget = (c) => {
  if (!scopeRestrict.value) return
  const k = scopeKey(c)
  const i = scopeSelectedKeys.value.indexOf(k)
  if (i >= 0) scopeSelectedKeys.value.splice(i, 1)
  else scopeSelectedKeys.value.push(k)
}

// 已选目标 → 展示用的名称/类型。候选列表里找不到的（端点未对外开放、目标被删）也要显示，
// 否则「已选 3 个」却只列出 2 个，会被当成界面丢数据。
const scopeChosenItems = computed(() => {
  const byKey = new Map()
  for (const c of scopeCandidates.value) byKey.set(scopeKey(c), c)
  return scopeSelectedKeys.value.map((k) => {
    const c = byKey.get(k)
    const idx = k.indexOf(':')
    const type = idx >= 0 ? k.slice(0, idx) : 'agent'
    const id = idx >= 0 ? k.slice(idx + 1) : k
    if (c) {
      return {
        key: k,
        name: c.name || c.target_id,
        type: c.target_type,
        supervisor: !!c.is_supervisor,
        missing: false
      }
    }
    return { key: k, name: id, type, supervisor: false, missing: true }
  })
})

const removeScopeTarget = (key) => {
  const i = scopeSelectedKeys.value.indexOf(key)
  if (i >= 0) scopeSelectedKeys.value.splice(i, 1)
}

// 左侧候选列表：贴在配置面板左边（同 top / 同高）。
// 宽度取「配置面板左侧还有多少空间」，窗口太窄时跟着收窄，避免压住配置面板。
const scopePickerStyle = computed(() => {
  const panelW = sidePanel.width.value ?? SCOPE_PANEL_WIDTH
  const panelLeft = Math.max(0, sidePanel.baseRect.value.right - panelW)
  const width = Math.min(340, Math.max(220, panelLeft - 8))
  return {
    top: sidePanel.baseRect.value.top + 'px',
    left: Math.max(0, panelLeft - width - 8) + 'px',
    width: width + 'px',
    height: sidePanel.baseRect.value.height + 'px'
  }
})

const openScopeDialog = async (b) => {
  scopeBindingId.value = b.id
  scopeBindingLabel.value = userLabel(b.user_id)
  scopeRestrict.value = !!b.restrict_targets
  scopeExcludeSupervisor.value = !b.restrict_targets && !!b.exclude_supervisor
  scopeSelectedKeys.value = []
  scopePickerVisible.value = false // 每次打开都从「只有配置面板」开始
  scopeDialogVisible.value = true
  scopeDialogLoading.value = true
  try {
    const token = getToken()
    const headers = { Authorization: `Bearer ${token}` }
    const [candResp, curResp] = await Promise.all([
      fetch(apiConfig.admin.svcAuthBindings.targetCandidates, { headers }),
      fetch(apiConfig.admin.svcAuthBindings.targets(b.id), { headers })
    ])
    const cand = await candResp.json().catch(() => ({}))
    const cur = await curResp.json().catch(() => ({}))
    if (cand.code === 200) {
      scopeCandidates.value = cand.data.items || []
    } else {
      ElMessage.error(formatError(cand) || t('settings.bindings.loadCandidatesFailed'))
    }
    if (cur.code === 200) {
      // 以服务端为准（列表里的 target_count 只是摘要）
      scopeRestrict.value = !!cur.data.restrict
      scopeExcludeSupervisor.value = !cur.data.restrict && !!cur.data.exclude_supervisor
      scopeSelectedKeys.value = (cur.data.targets || []).map(scopeKey)
    }
  } catch (e) {
    ElMessage.error(t('settings.bindings.loadScopeFailed', { msg: e.message }))
  } finally {
    scopeDialogLoading.value = false
  }
}

const saveScope = async () => {
  if (!scopeBindingId.value) return
  scopeDialogSaving.value = true
  try {
    const token = getToken()
    const targets = scopeSelectedKeys.value.map((k) => {
      const idx = k.indexOf(':')
      return { target_type: k.slice(0, idx), target_id: k.slice(idx + 1) }
    })
    const resp = await fetch(apiConfig.admin.svcAuthBindings.targets(scopeBindingId.value), {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${token}` },
      body: JSON.stringify({
        restrict: scopeRestrict.value,
        exclude_supervisor: scopeExcludeSupervisor.value,
        targets
      })
    })
    const data = await resp.json().catch(() => ({}))
    if (data.code !== 200) throw new Error(formatError(data) || t('settings.bindings.saveFailed'))
    // 三档各有各的结果，提示不能合并：尤其「限制 + 空列表」是全禁，不能显示成不限制
    if (scopeRestrict.value) {
      if (targets.length === 0) {
        ElMessage.warning(t('settings.bindings.savedRestrictedEmpty'))
      } else {
        ElMessage.success(t('settings.bindings.savedRestrictedCount', { n: targets.length }))
      }
    } else if (scopeExcludeSupervisor.value) {
      ElMessage.success(t('settings.bindings.savedExclude'))
    } else {
      ElMessage.success(t('settings.bindings.savedAll'))
    }
    scopeDialogVisible.value = false
    await loadBindings()
  } catch (e) {
    ElMessage.error(e.message || t('settings.bindings.saveFailed'))
  } finally {
    scopeDialogSaving.value = false
  }
}

// 全选只覆盖普通端点：超级智能体必须逐个勾（一次点击把管理能力开给外部 key 太便宜了）。
// 已勾的超级智能体不会被顺手清掉。
const selectAllScope = () => {
  const picked = new Set(scopeSelectedKeys.value)
  for (const c of scopeCandidates.value) {
    if (c.target_type === 'agent' && c.is_supervisor) continue
    picked.add(scopeKey(c))
  }
  scopeSelectedKeys.value = [...picked]
}

const clearScopeSelection = () => {
  scopeSelectedKeys.value = []
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

.config-form {
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.config-tip {
  margin-bottom: 12px;
}

/* ===== 绑定卡片 ===== */
.client-cards {
  display: flex;
  flex-wrap: wrap;
  gap: 16px;
  width: 100%;
}
.client-card {
  width: 240px;
  border: 1px solid var(--el-border-color, #dcdfe6);
  border-radius: 8px;
  padding: 14px;
  background: var(--el-fill-color-blank, #fff);
  box-sizing: border-box;
  display: flex;
  flex-direction: column;
  gap: 10px;
}
.client-card-head {
  position: relative; /* 操作区绝对定位的锚点 */
  display: flex;
  align-items: center;
  gap: 8px;
}
.client-card-body {
  display: flex;
  align-items: center;
  gap: 8px;
  background: var(--el-fill-color-light, #f5f7fa);
  border-radius: 6px;
  padding: 8px 10px;
}
.apikey-label {
  font-size: 12px;
  color: var(--el-text-color-secondary, #909399);
  flex-shrink: 0;
}
.apikey-value {
  flex: 1;
  font-size: 12px;
  word-break: break-all;
  font-family: monospace;
  color: var(--el-text-color-primary, #303133);
}
.apikey-inactive {
  color: var(--el-color-danger, #f56c6c) !important;
}

/* API Key 用户绑定 */
.binding-user {
  /* 占满可用宽度，并允许被压缩到 0：否则 min-width:auto 会按"不换行文本宽度"卡住，
     ellipsis 不生效或把右侧按钮挤出卡片 */
  flex: 1 1 auto;
  min-width: 0;
  font-size: 13px;
  font-weight: 600;
  color: var(--el-text-color-primary, #303133);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

/* 卡片上附带的显示名（次要信息，浅灰小字） */
.binding-user-alias {
  margin-left: 6px;
  font-weight: 400;
  font-size: 12px;
  color: var(--el-text-color-secondary, #909399);
}

/* 端点范围：卡片上的第三行。「不限制」是全开、「已限制但空集」是全禁，两个都用警示色 —— 都不是配好的状态 */
.binding-scope {
  display: flex;
  align-items: center;
  gap: 6px;
  padding-top: 8px;
  border-top: 1px dashed var(--el-border-color-lighter, #ebeef5);
  font-size: 12px;
  color: var(--el-text-color-secondary, #909399);
}
.binding-scope-label {
  flex: none;
}
.binding-scope-warn {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--el-color-warning, #e6a23c);
  font-weight: 500;
}
.binding-scope-ok {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--el-color-success, #67c23a);
  font-weight: 500;
}
.binding-scope .el-button {
  flex: none;
  padding: 0;
}

.binding-empty {
  width: 100%;
  padding: 12px 0;
  text-align: center;
  font-size: 13px;
  color: var(--el-text-color-secondary, #909399);
}

.binding-add-row {
  display: flex;
  align-items: center;
  gap: 12px;
  margin-top: 12px;
}

/* 服务凭证：tab 页 */
.binding-tabs {
  margin-top: 8px;
}
/* 操作区：默认不占位（否则会把用户名挤没），悬停时以浮层盖在名字右侧。
   紧凑化：清掉 EP 相邻按钮的 12px 左间距（已由 gap 控制）、按钮缩到 22px、间距 4px。 */
.card-head-actions {
  position: absolute;
  right: 0;
  top: 50%;
  transform: translateY(-50%);
  display: flex;
  align-items: center;
  gap: 4px;
  padding-left: 10px;
  background: var(--el-fill-color-blank, #fff); /* 浮在名字上时不留叠字 */
  border-radius: 6px;
  z-index: 1; /* 浮层压在用户名之上 */
  opacity: 0;
  pointer-events: none;
  transition: opacity 0.2s;
}
.client-card:hover .card-head-actions {
  opacity: 1;
  pointer-events: auto;
}
.card-head-actions :deep(.el-button + .el-button) {
  margin-left: 0;
}
.card-head-actions :deep(.el-button) {
  width: 22px;
  height: 22px;
  padding: 0;
  font-size: 13px;
}

.binding-oauth-body {
  flex-direction: column;
  align-items: stretch;
  gap: 6px;
}
.binding-oauth-row {
  display: flex;
  align-items: center;
  gap: 8px;
}
.binding-oauth-label {
  flex-shrink: 0;
  width: 52px;
  font-size: 12px;
  color: var(--el-text-color-secondary, #909399);
}

/* ===== 端点范围面板 ===== */
.settings-field {
  margin-bottom: 16px;
}
.settings-field .field-label {
  font-size: 13px;
  color: #606266;
  margin-bottom: 6px;
}
.editor-tip {
  font-size: 12px;
  line-height: 1.5;
  color: #909399;
  margin-top: 6px;
}

/* 「已选目标」是这块面板的主要结论，做成带边框的卡片，别淹没在灰字里 */
.scope-chosen-count {
  margin-left: 6px;
  padding: 0 7px;
  border-radius: 9px;
  background: var(--el-fill-color, #f0f2f5);
  color: var(--el-text-color-regular, #606266);
  font-size: 12px;
  font-weight: 600;
  line-height: 18px;
}
.scope-chosen {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  margin-top: 8px;
  padding: 10px;
  border: 1px solid var(--el-border-color-lighter, #ebeef5);
  border-radius: 6px;
  background: transparent;
}
.scope-chosen-type {
  margin-left: 4px;
  font-size: 11px;
  opacity: 0.7;
}
.scope-chosen-empty {
  margin-top: 8px;
  padding: 10px 12px;
  border: 1px dashed var(--el-border-color, #dcdfe6);
  border-radius: 6px;
  font-size: 12px;
  line-height: 1.5;
  color: var(--el-text-color-secondary, #909399);
}
.scope-chosen-empty.warn {
  border-color: var(--el-color-warning-light-5, #f5dab1);
  background: var(--el-color-warning-light-9, #fdf6ec);
  color: var(--el-color-warning-dark-2, #b88230);
}
.scope-summary {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
  margin-top: 10px;
}
.scope-alert {
  margin-top: 12px;
}

/* 左侧候选列表窗口：.panel-body 由 SidePanel 渲染，必须 :deep */
.scope-picker :deep(.panel-body) {
  padding: 12px 10px;
}
.scope-group {
  margin-bottom: 12px;
  border: 1px solid var(--el-border-color-lighter, #ebeef5);
  border-radius: 6px;
  background: #fff;
  overflow: hidden;
}
.scope-group:last-child {
  margin-bottom: 0;
}
/* 超级智能体那一块用警示色整块区分：它和管理入口是同一件事 */
.scope-group--supervisor {
  border-color: var(--el-color-warning-light-5, #f5dab1);
}
.scope-group-title {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 8px 10px;
  background: var(--el-fill-color-light, #f5f7fa);
  border-bottom: 1px solid var(--el-border-color-lighter, #ebeef5);
  font-size: 12px;
  font-weight: 600;
  color: var(--el-text-color-regular, #606266);
}
.scope-group--supervisor .scope-group-title {
  background: var(--el-color-warning-light-9, #fdf6ec);
  color: var(--el-color-warning-dark-2, #b88230);
}
.scope-group-count {
  font-weight: 400;
  color: var(--el-text-color-secondary, #909399);
}
.scope-group-warn {
  padding: 8px 10px 0;
  font-size: 12px;
  line-height: 1.5;
  color: var(--el-color-warning, #e6a23c);
}
.scope-group-items {
  padding: 6px;
}
.scope-item {
  display: flex;
  align-items: flex-start;
  gap: 8px;
  padding: 6px 8px;
  border-radius: 4px;
  cursor: pointer;
}
.scope-item:hover {
  background: var(--el-fill-color-light, #f5f7fa);
}
.scope-item.selected {
  background: var(--el-color-primary-light-9, #ecf5ff);
}
.scope-item.disabled {
  cursor: not-allowed;
  opacity: 0.55;
}
.scope-item-check {
  display: flex;
  flex: none;
  align-items: center;
  justify-content: center;
  width: 16px;
  height: 16px;
  margin-top: 2px;
  border: 1px solid var(--el-border-color, #dcdfe6);
  border-radius: 3px;
  background: #fff;
  color: #fff;
  font-size: 12px;
}
.scope-item.selected .scope-item-check {
  border-color: var(--el-color-primary, #409eff);
  background: var(--el-color-primary, #409eff);
}
.scope-item-main {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}
.scope-item-name {
  font-size: 13px;
  line-height: 1.4;
  color: var(--el-text-color-primary, #303133);
  word-break: break-all;
}
.scope-item-meta {
  font-size: 12px;
  color: var(--el-text-color-secondary, #909399);
}
.scope-empty {
  padding: 16px 0;
  text-align: center;
  font-size: 12px;
  color: var(--el-text-color-secondary, #909399);
}
</style>
