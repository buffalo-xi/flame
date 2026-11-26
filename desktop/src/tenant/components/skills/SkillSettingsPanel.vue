<template>
  <!-- 设置面板：可见性 / 工具依赖 + 左侧「添加工具 / 组」选择器（teleport 到 body） -->
  <!-- 设置面板：可见性 / 工具依赖 / 属性 -->
  <div v-if="visible" ref="settingsPanelRef" class="settings-panel"
       :style="{ width: settingsPanelWidth + 'px' }">
    <div class="panel-resizer" @mousedown="startSettingsResize"></div>
    <div class="panel-header">
      <div class="settings-tabs">
        <button class="settings-tab-btn" :class="{ active: settingsTab === 'visibility' }"
                @click="switchSettingsTab('visibility')">{{ $t('skill.tabVisibility') }}</button>
        <button class="settings-tab-btn" :class="{ active: settingsTab === 'tools' }"
                @click="switchSettingsTab('tools')">{{ $t('skill.tabTools') }}</button>
        <button class="settings-tab-btn" :class="{ active: settingsTab === 'props' }"
                @click="switchSettingsTab('props')">{{ $t('skill.tabProps') }}</button>
      </div>
      <span class="close-icon" @click="$emit('close')">&#xd7;</span>
    </div>
    <div class="panel-body">
      <!-- tab 1：可见性（只决定「能否被发现」；是否进提示词由 agent 侧绑定决定） -->
      <template v-if="settingsTab === 'visibility'">
        <div v-if="!skillId" class="editor-tip">{{ $t('skill.needCreateVisibility') }}</div>
        <div v-else-if="form.scope === 'agent'" class="editor-tip">
          {{ $t('skill.privateSkillHint', { name: form.owner_agent_name || form.agent_id }) }}
        </div>
        <template v-else>
          <div class="editor-tip">
            {{ $t('skill.visibilityScopeHintPre') }}<strong>{{ $t('skill.visibilityScopeHintStrong') }}</strong>{{ $t('skill.visibilityScopeHintPost') }}
          </div>
          <div class="settings-field">
            <div class="field-label">{{ $t('skill.visibilityRange') }}</div>
            <el-radio-group v-model="form.restricted" :disabled="readonly" @change="saveVisibility">
              <el-radio :value="false">{{ $t('skill.scopeAll') }}</el-radio>
              <el-radio :value="true">{{ $t('skill.scopeWhitelist') }}</el-radio>
            </el-radio-group>
            <div class="editor-tip">
              <template v-if="form.restricted">{{ $t('skill.whitelistHint') }}</template>
              <template v-else>
                {{ $t('skill.allHintPre') }}<strong>{{ $t('skill.allHintStrong') }}</strong>{{ $t('skill.allHintPost') }}
              </template>
            </div>
          </div>
          <div v-if="form.restricted" class="settings-field">
            <div class="field-label">{{ $t('skill.visibleAgentsLabel') }}</div>
            <!-- 与智能体「谁能调用它」同一交互：点按钮从左侧拉出选择面板，点一项即加入 / 移出（改动即时保存） -->
            <div class="bind-row">
              <el-button size="small" :disabled="readonly || !skillId" @click="openAgentPicker">
                {{ $t('skill.selectAgent') }}
              </el-button>
              <div class="bind-tags">
                <el-tag v-for="id in visibleAgentIds" :key="id" size="small"
                        :closable="!readonly" :disable-transitions="true" @close="unbindAgent(id)">
                  {{ agentName(id) }}
                </el-tag>
                <span v-if="!visibleAgentIds.length" class="bind-tags-empty">{{ $t('skill.noneVisible') }}</span>
              </div>
            </div>
            <div class="editor-tip">
              {{ $t('skill.whitelistFooter') }}
            </div>
          </div>
        </template>
      </template>

      <!-- tab 2：工具依赖（skill 只声明「需要」，不产生授权） -->
      <template v-else-if="settingsTab === 'tools'">
        <div class="editor-tip">{{ $t('skill.toolsTabHint') }}</div>
        <div v-if="!skillId" class="editor-tip">{{ $t('skill.needCreateTools') }}</div>
        <template v-else>
          <div class="settings-field">
            <div class="field-label">{{ $t('skill.addLabel') }}</div>
            <el-button size="small" type="primary" :disabled="readonly" @click="openDepPicker">{{ $t('agentSettings.addGrants') }}</el-button>
            <div class="editor-tip">
              {{ $t('skill.depPickerHintPre') }}<strong>{{ $t('skill.depPickerHintStrong') }}</strong>{{ $t('skill.depPickerHintPost') }}
            </div>
          </div>

          <div class="settings-field">
            <div class="field-label">{{ $t('skill.declared', { n: depItems.length }) }}</div>
            <div v-if="!depItems.length" class="editor-tip">{{ $t('agentSettings.emptySection') }}</div>
            <div v-else class="tools-list">
              <div v-for="d in depSorted" :key="d.ref" class="tool-item">
                <el-icon v-if="d.item_type === 'group'" class="grant-group-icon"><Folder /></el-icon>
                <el-icon v-else class="grant-tool-icon"><Tools /></el-icon>
                <div class="tool-item-main">
                  <div class="tool-name">
                    {{ groupName(t, te, d) || d.ref }}
                    <span class="ref-tag">{{ d.item_type === 'group' ? $t('agentSettings.tagGroup') : $t('agentSettings.tagTool') }}</span>
                    <span v-if="d.count" class="ref-tag">{{ $t('agentSettings.countItems', { n: d.count }) }}</span>
                    <span v-if="d.supervisor_only || d.supervisor_only_count" class="ref-tag supervisor-only"
                          :title="$t('skill.supervisorOnlyTip')">{{ $t('skill.supervisorOnly') }}</span>
                  </div>
                  <div class="tool-desc" :title="d.ref">{{ shortDesc(d) || d.ref }}</div>
                </div>
                <el-icon v-if="!readonly" class="node-del" @click="removeDep(d.ref)"><Delete /></el-icon>
              </div>
            </div>
          </div>

          <div class="tools-save-bar">
            <el-button size="small" type="primary" :loading="savingDeps" :disabled="!skillId || readonly"
                       @click="saveDeps">{{ $t('agentSettings.saveGrants') }}</el-button>
            <span v-if="depDirty" class="dirty-tip">{{ $t('agentSettings.dirtyTip') }}</span>
            <span v-else class="tools-count">{{ $t('agentSettings.savedTip') }}</span>
          </div>
        </template>
      </template>

      <!-- tab 3：属性 -->
      <template v-else>
        <div class="settings-field">
          <div class="field-label">{{ $t('skill.ownerLabel') }}</div>
          <!-- 新建时可选；已创建后不可改（后端 UpdateSkill 也不支持改 agent_id） -->
          <template v-if="!skillId">
            <el-radio-group v-model="form.scope">
              <el-radio value="tenant">{{ $t('skill.tenantShared') }}</el-radio>
              <el-radio value="agent">{{ $t('skill.agentPrivate') }}</el-radio>
            </el-radio-group>
            <el-select v-if="form.scope === 'agent'" v-model="form.agent_id" size="small"
                       style="width:100%;margin-top:6px" :placeholder="$t('skill.selectOwnerAgent')"
                       popper-class="agent-select-popper">
              <el-option v-for="a in agents" :key="a.id" :label="a.name" :value="a.id">
                <div class="agent-option">
                  <div class="agent-option-name">{{ a.name }}</div>
                  <div class="agent-option-desc" :title="a.description || $t('agentSettings.noDescription')">{{ a.description || $t('agentSettings.noDescription') }}</div>
                </div>
              </el-option>
            </el-select>
            <div class="editor-tip">{{ $t('skill.ownerHint') }}</div>
          </template>
          <div v-else class="editor-tip">
            {{ $t('skill.ownerLocked', {
              scope: form.scope === 'agent'
                ? $t('skill.agentPrivate') + ': ' + (form.owner_agent_name || form.agent_id)
                : $t('skill.tenantShared')
            }) }}
          </div>
        </div>
        <div class="settings-field">
          <div class="field-label">{{ $t('skill.userInvocable') }}</div>
          <el-switch v-model="form.user_invocable" :disabled="!skillId || readonly" @change="saveProps" />
          <div class="editor-tip">{{ $t('skill.userInvocableHint') }}</div>
        </div>
        <div class="settings-field">
          <div class="field-label">{{ $t('skill.commandDispatch') }}</div>
          <el-select v-model="form.command_dispatch" size="small" style="width:100%" clearable
                     :placeholder="$t('skill.dispatchNone')" :disabled="!skillId || readonly" @change="onDispatchChange">
            <el-option :label="$t('skill.dispatchTool')" value="tool" />
          </el-select>
        </div>
        <div v-if="form.command_dispatch === 'tool'" class="settings-field">
          <div class="field-label">{{ $t('skill.targetTool') }}</div>
          <el-select v-model="form.command_tool" size="small" style="width:100%"
                     filterable allow-create default-first-option clearable
                     :disabled="!skillId || readonly"
                     :no-data-text="$t('skill.targetToolNoData')"
                     :placeholder="$t('skill.targetToolPlaceholder')" @change="saveProps">
            <el-option v-for="opt in commandToolOptions" :key="opt.ref"
                       :label="opt.label === opt.ref ? opt.ref : `${opt.label} · ${opt.ref}`"
                       :value="opt.ref" />
          </el-select>
          <div class="editor-tip">{{ $t('skill.targetToolHint') }}</div>
        </div>
      </template>
    </div>
  </div>

<!-- 工具依赖：左侧选择器（与智能体「添加工具 / 组」同款；这里只有一种区，没有三类 tab）
     teleport 到 body：不被内容区 overflow 裁剪；位置/尺寸按设置面板的 rect 计算 -->
<teleport to="body">
  <div v-if="depPickerVisible" ref="depPickerRef" class="picker-panel" :style="pickerStyle">
    <div class="panel-header">
      <span class="panel-title">{{ $t('agentSettings.addGrants') }}</span>
      <span class="close-icon" @click="depPickerVisible = false">&#xd7;</span>
    </div>
    <div class="panel-body">
      <div class="picker-toolbar">
        <el-input v-model="depSearch" size="small" :placeholder="$t('agentSettings.searchGrantPlaceholder')" clearable style="width:100%" />
        <el-radio-group v-model="depFilterKind" size="small" class="picker-kinds">
          <el-radio-button value="">{{ $t('agentSettings.filterAll') }}</el-radio-button>
          <el-radio-button value="builtin">{{ $t('agentSettings.filterBuiltin') }}</el-radio-button>
          <el-radio-button value="preset">{{ $t('agentSettings.filterPreset') }}</el-radio-button>
          <el-radio-button value="ext">{{ $t('agentSettings.filterExt') }}</el-radio-button>
          <el-radio-button value="conn">{{ $t('agentSettings.filterConn') }}</el-radio-button>
        </el-radio-group>
      </div>

      <!-- 组卡片：点头部=整组声明（组内工具等同全选中）；点组内某行=只声明这个工具。
           synthetic（插件 / 自定义工具的类别容器）不可整组声明，只展开成员。 -->
      <div class="picker-cards">
        <div v-for="c in depCards" :key="c.ref" class="picker-card"
             :class="{ active: !c.synthetic && inDraft(c.ref) }">
          <div class="picker-card-head" @click="c.synthetic ? toggleDepCollapse(c.ref) : pickToggleGroup(c)">
            <el-icon class="picker-caret" :class="{ open: !isDepCollapsed(c.ref) }"
                     @click.stop="toggleDepCollapse(c.ref)"><CaretRight /></el-icon>
            <div class="tool-item-main">
              <div class="tool-name">
                {{ groupName(t, te, c) }}
                <span class="ref-tag">{{ c.synthetic ? $t('agentSettings.tagCategory') : $t('agentSettings.tagGroup') }}</span>
                <span class="ref-tag">{{ $t('agentSettings.countItems', { n: c.count }) }}</span>
                <!-- 类别卡的名字本身就是类别名（插件 / 自定义工具），不再重复标 kind -->
                <span v-if="!c.synthetic" class="ref-tag">{{ kindLabel(c.kind) }}</span>
                <span v-if="c.supervisor_only_count" class="ref-tag supervisor-only"
                      :title="$t('skill.supervisorOnlyTipExt')">{{ $t('skill.supervisorOnly') }}</span>
                <span v-if="c.available === false" class="ref-tag warn">{{ $t('skill.depUnavailable') }}</span>
              </div>
              <div class="tool-desc" :title="c.ref">{{ c.synthetic ? $t('skill.declareHint') : c.ref }}</div>
            </div>
            <el-icon v-if="!c.synthetic && inDraft(c.ref)" class="picker-check"><Check /></el-icon>
          </div>
          <div v-if="!isDepCollapsed(c.ref) && (c.items || []).length" class="picker-card-body">
            <div v-for="t in (c.items || [])" :key="t.ref" class="picker-tool"
                 :class="{ on: inDraft(t.ref), covered: !c.synthetic && inDraft(c.ref) }" @click="pickToggleTool(c, t)">
              <el-icon class="picker-tool-mark"><Check /></el-icon>
              <div class="tool-item-main">
                <div class="tool-name">{{ t.label || t.name }}<span class="ref-tag">{{ t.ref }}</span><span v-if="t.supervisor_only" class="ref-tag supervisor-only" :title="$t('skill.supervisorOnlyTip')">{{ $t('skill.supervisorOnly') }}</span></div>
                <div class="tool-desc" :title="t.description">{{ shortDesc(t) }}</div>
              </div>
            </div>
          </div>
        </div>
        <div v-if="!depCards.length" class="editor-tip">{{ $t('skill.noMatches') }}</div>
      </div>
    </div>
    <div class="picker-footer">
      <span style="font-size:12px;color:#909399">
        {{ $t('skill.declaredFooter', { n: depItems.length }) }}
      </span>
      <el-button size="small" type="primary" style="margin-left:auto"
                 @click="depPickerVisible = false">{{ $t('agentSettings.done') }}</el-button>
    </div>
  </div>
</teleport>

<!-- 可见性白名单：左侧选择面板（与「添加工具 / 组」同款几何与卡片样式），点一项即加入 / 移出并落库 -->
<teleport to="body">
  <div v-if="agentPickerVisible" ref="agentPickerRef" class="picker-panel" :style="pickerStyle">
    <div class="panel-header">
      <span class="panel-title">{{ $t('skill.visibleAgentsTitle') }}</span>
      <span class="close-icon" @click="agentPickerVisible = false">&#xd7;</span>
    </div>
    <div class="panel-body">
      <div class="picker-block-title">
        {{ $t('skill.toggleVisibleHint') }}
        <span class="picker-block-hint">{{ $t('skill.selectedAgents', { n: visibleAgentIds.length }) }}</span>
      </div>
      <div class="picker-toolbar">
        <el-input v-model="agentSearch" size="small" :placeholder="$t('agentSettings.searchAgentPlaceholder')"
                  clearable style="width:100%" />
      </div>
      <div class="picker-cards">
        <div v-for="a in filteredAgents" :key="a.id" class="picker-card"
             :class="{ active: isAgentVisible(a.id) }">
          <div class="picker-card-head" @click="toggleAgentVisibility(a.id)">
            <div class="tool-item-main">
              <div class="tool-name">
                {{ a.name }}
                <span v-if="a.is_supervisor" class="ref-tag supervisor-only">supervisor</span>
                <span v-if="isAgentVisible(a.id)" class="ref-tag core">{{ $t('skill.selectedTag') }}</span>
              </div>
              <div class="tool-desc" :title="a.description || $t('agentSettings.noDescription')">{{ a.description || $t('agentSettings.noDescription') }}</div>
            </div>
            <el-icon class="picker-check">
              <Check v-if="isAgentVisible(a.id)" /><Plus v-else />
            </el-icon>
          </div>
        </div>
        <div v-if="!filteredAgents.length" class="editor-tip">{{ $t('agentSettings.noMatchingAgents') }}</div>
      </div>
    </div>
    <div class="picker-footer">
      <span class="tools-count">{{ $t('agentSettings.savedImmediately') }}</span>
      <el-button size="small" type="primary" @click="agentPickerVisible = false">{{ $t('agentSettings.done') }}</el-button>
    </div>
  </div>
</teleport>
</template>

<script setup>
import { ref, computed, watch, nextTick, onUnmounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { ElMessage } from 'element-plus'
import { Tools, Check } from '@element-plus/icons-vue'
import adminApi from '../../config/admin.js'
import { getToken } from '../../utils/auth.js'
import { useEscClose } from '../../composables/useEscClose'
import { formatError } from '../../../locales'
import { groupName, groupDesc } from '../../utils/groupText.js'

const props = defineProps({
  visible: { type: Boolean, default: false },
  skillId: { type: String, default: '' },
  // 技能表单（父组件持有；可见范围 / 命令字段就地写回，保存后父组件也能看到）
  form: { type: Object, default: () => ({}) },
  // 「可见该技能的 agent」候选
  agents: { type: Array, default: () => [] },
  // 废弃(3)：面板整体只读
  readonly: { type: Boolean, default: false }
})
const emit = defineEmits(['close'])

// 脚本侧文案（toast、校验）走 t()
const { t, te } = useI18n()

const authHeaders = () => ({
  'Content-Type': 'application/json',
  'Authorization': `Bearer ${getToken()}`
})

// 只读闸（与父组件同语义）：拦截并提示
const blockIfReadonly = () => {
  if (!props.readonly) return false
  ElMessage.warning({ message: t('skill.readonlyMode'), duration: 2000, offset: 20 })
  return true
}

// 更新技能元数据：后端按全量处理（name / 命令字段，空值即清空），
// 所以这里必须把当前表单里的字段一并带上，避免只改描述却把命令配置抹掉。
const putSkill = async (patch) => {
  if (props.readonly) throw new Error(t('skill.readonlySave'))
  const payload = {
    name: props.form.name,
    description: props.form.description,
    user_invocable: !!props.form.user_invocable,
    command_dispatch: props.form.command_dispatch || '',
    // 分发方式不是 tool 时目标工具必须随空值一起提交：那个输入框此时已隐藏，
    // 残留的旧值用户看不见也清不掉，留着只会让后端把这次保存当成半套配置。
    command_tool: props.form.command_dispatch === 'tool' ? (props.form.command_tool || '') : '',
    ...patch
  }
  const res = await fetch(adminApi.skills.update(props.skillId), {
    method: 'PUT',
    headers: authHeaders(),
    body: JSON.stringify(payload)
  })
  const body = await res.json().catch(() => ({}))
  if (body.code !== 200) throw new Error(formatError(body) || t('agent.saveFailed'))
}

//（选择面板是 4000 的 body 级浮层，悬空会压住面板里的 Element Plus 下拉）
const switchSettingsTab = async (tab) => {
  settingsTab.value = tab
  if (tab !== 'tools') {
    depPickerVisible.value = false
    agentPickerVisible.value = false
  }
  // 「属性」页的「目标工具」候选来自工具依赖，所以两个页签都要先拿到依赖
  // （loadDeps 自带缓存与草稿保护，切回来不会冲掉未保存的勾选）
  if (tab === 'tools' || tab === 'props') {
    await loadDeps()
  }
}
// 设置面板宽度拖拽
const settingsPanelRef = ref(null)
const settingsPanelWidth = ref(360)

// 设置面板：显示开关 + 当前 tab（visibility 可见性 / tools 工具依赖 / props 属性）
// 注意：这两个 ref 是编辑器进入流程（resetEditorState）和模板都要用的，缺一不可
const settingsTab = ref('visibility')

// 点击设置面板外部时自动关闭（与智能体设置面板同款）
const handleSettingsPanelOutsideClick = (e) => {
  const target = e.target
  // 忽略 Element Plus 浮层（下拉/提示等 teleport 到 body 的元素）的点击
  if (target.closest && (target.closest('.el-overlay') || target.closest('.el-select-dropdown') || target.closest('.el-popper'))) {
    return
  }
  // 点「设置」按钮本身交给按钮的 click（toggle 逻辑），这里不处理
  if (target.closest && target.closest('.settings-toggle-btn')) {
    return
  }
  // 点左侧「添加工具 / 组」选择器不算外部点击（它是设置面板的兄弟面板）
  if (depPickerRef.value && depPickerRef.value.contains(target)) {
    return
  }
  // 「可见该技能的 agent」选择面板同理
  if (agentPickerRef.value && agentPickerRef.value.contains(target)) {
    return
  }
  if (settingsPanelRef.value && !settingsPanelRef.value.contains(target)) {
    emit('close')
  }
}
watch(() => props.visible, (visible) => {
  if (visible) {
    // nextTick 延迟绑定，避免打开按钮的 mousedown 立即触发关闭
    nextTick(() => document.addEventListener('mousedown', handleSettingsPanelOutsideClick))
  } else {
    document.removeEventListener('mousedown', handleSettingsPanelOutsideClick)
    depPickerVisible.value = false // 设置面板关闭时，一并关掉左侧的选择面板
    agentPickerVisible.value = false
  }
})
onUnmounted(() => {
  document.removeEventListener('mousedown', handleSettingsPanelOutsideClick)
  window.removeEventListener('resize', updatePickerRect)
})
const startSettingsResize = (e) => {
  const startX = e.clientX
  const startWidth = settingsPanelWidth.value
  const onMouseMove = (ev) => {
    settingsPanelWidth.value = Math.max(300, Math.min(startWidth + (startX - ev.clientX), 720))
  }
  const onMouseUp = () => {
    document.removeEventListener('mousemove', onMouseMove)
    document.removeEventListener('mouseup', onMouseUp)
  }
  document.addEventListener('mousemove', onMouseMove)
  document.addEventListener('mouseup', onMouseUp)
}
const saveProps = async () => {
  if (!props.skillId) return
  try {
    await putSkill({})
    ElMessage.success({ message: t('agent.saved'), duration: 700 })
  } catch (e) {
    ElMessage.error(e.message || t('agent.saveFailed'))
  }
}

// 分发方式变化：取消直达时把目标工具一并清掉 —— 输入框随之隐藏，残留值用户看不见也改不了。
// 切成 tool 时目标工具通常还是空的，后端把它当「未完成的草稿」收下，填完（blur）才真正生效。
const onDispatchChange = async (value) => {
  if (value !== 'tool') props.form.command_tool = ''
  await saveProps()
}

const loadVisibility = async () => {
  if (!props.skillId || props.form.scope === 'agent') return
  try {
    const res = await fetch(adminApi.skills.visibility(props.skillId), { headers: authHeaders() })
    const body = await res.json().catch(() => ({}))
    if (body.code !== 200) return
    props.form.restricted = !!body.data?.restricted
    props.form.bindingAgentIds = (body.data?.items || []).map((x) => x.agent_id)
  } catch { /* 忽略：取不到时保留默认（收窄） */ }
}
// 白名单里已选的 agent id（空值兜底成空数组：tag 循环与提交都直接用它）
const visibleAgentIds = computed(() => props.form.bindingAgentIds || [])
const saveVisibility = async () => {
  if (blockIfReadonly()) return
  if (!props.skillId || props.form.scope === 'agent') return
  try {
    const res = await fetch(adminApi.skills.visibility(props.skillId), {
      method: 'PUT',
      headers: authHeaders(),
      body: JSON.stringify({ restricted: !!props.form.restricted, agent_ids: visibleAgentIds.value })
    })
    const body = await res.json().catch(() => ({}))
    if (body.code !== 200) throw new Error(formatError(body) || t('skill.visibilitySaveFailed'))
    ElMessage.success({ message: t('skill.visibilitySaved'), duration: 700 })
  } catch (e) {
    ElMessage.error(e.message || t('skill.visibilitySaveFailed'))
    // 落库失败 → 回到服务端的真实状态，别让 tag 与服务端漂移
    await loadVisibility()
  }
}

// ===== 可见性白名单：左侧选择面板（与智能体「谁能调用它」同一交互） =====
// 按钮 + 标签；点开左侧面板挑 agent，点一项即加入 / 移出并立即保存。
const agentPickerVisible = ref(false)
const agentPickerRef = ref(null)
const agentSearch = ref('')

// tag 里显示名字：agent 被删后 id 还在库里时退回显示 id，至少不空白
const agentName = (id) => props.agents.find((a) => String(a.id) === String(id))?.name || id
const isAgentVisible = (id) => visibleAgentIds.value.some((x) => String(x) === String(id))
const filteredAgents = computed(() => {
  const q = agentSearch.value.trim().toLowerCase()
  if (!q) return props.agents
  return props.agents.filter((a) =>
    String(a.name || '').toLowerCase().includes(q) ||
    String(a.description || '').toLowerCase().includes(q))
})

const openAgentPicker = () => {
  if (blockIfReadonly()) return
  if (!props.skillId) { ElMessage.warning(t('skill.needCreate')); return }
  agentSearch.value = ''
  depPickerVisible.value = false // 两个选择面板互斥，避免叠在一起
  updatePickerRect()
  agentPickerVisible.value = true
}

// 点一项：已选就移出，未选就加入；立即保存（与「可见范围」单选同款）
const toggleAgentVisibility = async (id) => {
  if (blockIfReadonly()) return
  const cur = visibleAgentIds.value
  props.form.bindingAgentIds = cur.some((x) => String(x) === String(id))
    ? cur.filter((x) => String(x) !== String(id))
    : [...cur, id]
  await saveVisibility()
}

// 关掉 tag = 从白名单移出
const unbindAgent = async (id) => {
  if (blockIfReadonly()) return
  props.form.bindingAgentIds = visibleAgentIds.value.filter((x) => String(x) !== String(id))
  await saveVisibility()
}

// ============ 工具依赖（skill 声明「需要」；不允许引用工具组） ============
// depItems 同时就是「草稿」：左侧选择器每次勾选/取消都实时写进来（右侧列表同步刷新），
// 只有点设置面板底部的「保存」才 PUT 到后端 —— 对齐智能体「工具配置」，避免中间态直接落库。
const depItems = ref([])
const depDirty = ref(false)     // 有未保存的改动
const savingDeps = ref(false)
const depsLoaded = ref(false)   // 该 skill 的依赖是否已加载（切 tab 回来不覆盖草稿）
const depsSkillId = ref('')     // 已加载依赖的 skill id
const depSearch = ref('')
const toolCatalog = ref([])
const depCollapsed = ref({})

// 展示顺序：组在前、单个工具在后；同类型内按名称排序（与智能体「工具配置」一致）。
// 只影响渲染，depItems 本身保持插入顺序，提交给后端的就是这个集合。
const depSorted = computed(() => [...depItems.value].sort((a, b) => {
  if (a.item_type !== b.item_type) return a.item_type === 'group' ? -1 : 1
  return String(a.label || a.ref || '').localeCompare(String(b.label || b.ref || ''), 'zh')
}))

const KIND_LABEL_KEYS = {
  builtin: 'kindBuiltin', mcp: 'kindMcp', rpc: 'kindRpc', local: 'kindLocal',
  group: 'kindGroup', preset: 'kindPreset', plugin: 'kindPlugin', custom: 'kindCustom'
}
const kindLabel = (k) => (KIND_LABEL_KEYS[k] ? t(`agentSettings.${KIND_LABEL_KEYS[k]}`) : k)
// 工具/组简述：最多 20 字（超出截断，完整内容放 title）
const shortDesc = (item) => {
  const s = String(item?.description || '').trim()
  if (!s) return ''
  return s.length > 20 ? s.slice(0, 19) + '…' : s
}

const loadToolCatalog = async () => {
  if (toolCatalog.value.length) return
  try {
    const res = await fetch(adminApi.toolCatalog.list(), { headers: authHeaders() })
    const body = await res.json().catch(() => ({}))
    if (body.code === 200) toolCatalog.value = body.data?.groups || []
  } catch (e) {
    console.error('failed to load the tool catalog:', e)
  }
}

const describeRef = (ref, itemType) => {
  for (const g of toolCatalog.value) {
    if (itemType !== 'tool' && g.ref === ref) {
      return { ref, item_type: 'group', label: groupName(t, te, g) || ref, kind: g.kind,
               available: g.available, count: (g.items || []).length, description: groupDesc(t, te, g),
               supervisor_only_count: (g.items || []).filter((t) => t.supervisor_only).length }
    }
    for (const t of (g.items || [])) {
      if (t.ref === ref) {
        return { ref, item_type: 'tool', label: t.label || t.name, kind: g.kind,
                 available: t.available, count: 0, description: t.description || '',
                 supervisor_only: !!t.supervisor_only }
      }
    }
  }
  return { ref, item_type: itemType || 'tool', label: ref, kind: '', available: null, count: 0, description: '',
           supervisor_only: false, supervisor_only_count: 0 }
}

const loadDeps = async (force = false) => {
  if (!props.skillId) return
  // 已有草稿且是同一个 skill：不再重复拉取，避免切 tab 回来把未保存的改动冲掉
  if (!force && depsLoaded.value && depsSkillId.value === props.skillId) return
  await loadToolCatalog()
  try {
    const res = await fetch(adminApi.skills.toolGrants(props.skillId), { headers: authHeaders() })
    const body = await res.json().catch(() => ({}))
    if (body.code !== 200) throw new Error(formatError(body) || t('skill.loadDepsFailed'))
    depItems.value = (body.data?.items || []).map((it) => describeRef(it.ref, it.item_type))
    depsLoaded.value = true
    depsSkillId.value = props.skillId
    depDirty.value = false
  } catch (e) {
    ElMessage.error(e.message || t('skill.loadDepsFailed'))
  }
}

// 「目标工具」的候选：该技能声明的工具依赖（一个直达命令通常就是其中某一个的薄壳），
// 外加当前已保存的值 —— 它可能不在依赖里（以前手填过），不补进来就会显示成一个来历不明的 ref。
//
// 依赖也可能是「整组」（builtin:exec / mcp:fs / preset:xxx）：组本身不是一个可直达的工具，所以展开成组内的
// 每个单工具再作为候选。面板本来就允许整组声明（分类组的 ref 与组内同名工具的 ref 会重合，比如 builtin:exec
// 既是 exec 分类也是 exec 工具），只挑 item_type==='tool' 会让这类依赖一个候选都产不出来，下拉于是显示「无数据」。
//
// 仍然允许手填：命令可以直达没写进依赖的工具，能不能跑由运行时该 agent 的授权决定，这里不替后端下结论。
const commandToolOptions = computed(() => {
  const opts = []
  const seen = new Set()
  const add = (ref, label) => {
    if (!ref || seen.has(ref)) return
    seen.add(ref)
    opts.push({ ref, label: label || ref })
  }
  for (const it of depItems.value) {
    if (it.item_type === 'tool') {
      add(it.ref, it.label)
      continue
    }
    const group = toolCatalog.value.find((g) => g.ref === it.ref)
    for (const t of (group?.items || [])) add(t.ref, t.label || t.name)
  }
  const cur = props.form.command_tool
  if (cur && !seen.has(cur)) {
    const d = describeRef(cur, 'tool')
    opts.unshift({ ref: cur, label: d.label || cur })
  }
  return opts
})

// 保存（全量替换）：显式点「保存」才提交，避免左侧勾选过程中的中间态直接落库
const saveDeps = async () => {
  if (blockIfReadonly()) return
  if (!props.skillId) return
  savingDeps.value = true
  try {
    const res = await fetch(adminApi.skills.toolGrants(props.skillId), {
      method: 'PUT',
      headers: authHeaders(),
      body: JSON.stringify({
        // 只提交 ref：item_type 由服务端按 ref 形态推导
        items: depItems.value.map((d) => ({ ref: d.ref }))
      })
    })
    const body = await res.json().catch(() => ({}))
    if (body.code !== 200) throw new Error(formatError(body) || t('skill.saveDepsFailed'))
    depDirty.value = false
    ElMessage.success({ message: t('skill.depsSaved'), duration: 700 })
  } catch (e) {
    ElMessage.error(e.message || t('skill.saveDepsFailed'))
  } finally {
    savingDeps.value = false
  }
}

const depCards = computed(() => {
  const kw = depSearch.value.trim().toLowerCase()
  const hit = (text) => (text || '').toLowerCase().includes(kw)
  const out = []
  for (const g of toolCatalog.value) {
    if (g.kind === 'group') continue // skill 侧不允许引用工具组
    // 来源筛选：全部 / 内置 / 预设 / 插件·脚本 / 连接·端点
    // 插件与自定义工具单独成类：混进「连接/端点」会让人以为要去设置页配连接
    const bucket = ['builtin', 'preset'].includes(g.kind)
      ? g.kind
      : (['plugin', 'script'].includes(g.kind) ? 'ext' : 'conn')
    if (depFilterKind.value && depFilterKind.value !== bucket) continue
    const items = (g.items || []).map((t) => ({
      ref: t.ref, item_type: 'tool', label: t.label || t.name, description: t.description || '',
      supervisor_only: !!t.supervisor_only
    }))
    // 译名也要能搜到：英文界面下搜 built-in 应该命中「Built-in · file」
    const groupHit = !kw || hit(groupName(t, te, g)) || hit(g.name) || hit(g.ref)
    const matched = groupHit ? items : items.filter((t) => hit(t.label) || hit(t.ref))
    if (kw && !groupHit && !matched.length) continue
    out.push({
      ref: g.ref, item_type: 'group', name: g.name, kind: g.kind,
      count: (g.items || []).length, items, available: g.available,
      // 类别容器（插件 / 自定义工具）：不可整组声明，勾选落到成员的工具 ref 上
      synthetic: !!g.synthetic,
      supervisor_only_count: items.filter((t) => t.supervisor_only).length
    })
  }
  return out
})

// 缺省收起；类别容器（插件 / 自定义工具，synthetic）例外缺省展开 —— 成员就是全部内容，
// 收起后卡片看着像空的。用户手动收过之后以用户操作为准。
const isSyntheticRef = (ref) => toolCatalog.value.some((g) => g.synthetic && g.ref === ref)
const isDepCollapsed = (ref) => depCollapsed.value[ref] ?? !isSyntheticRef(ref)
const toggleDepCollapse = (ref) => {
  depCollapsed.value = { ...depCollapsed.value, [ref]: !isDepCollapsed(ref) }
}
// 删除一项：只改草稿，点「保存」才生效
const removeDep = (ref) => {
  if (blockIfReadonly()) return
  depItems.value = depItems.value.filter((d) => d.ref !== ref)
  depDirty.value = true
}

// ===== 工具依赖：左侧选择器（与智能体「添加工具 / 组」同款，只有一种区） =====
// 选择器直接操作 depItems（草稿）：勾选/取消即时反映到右侧「已声明」列表；
// 「完成」只关闭不提交，提交靠设置面板底部的「保存」。
const depPickerVisible = ref(false)
const depFilterKind = ref('')  // '' 全部 | builtin 内置 | preset 预设 | conn 连接·端点
const depPickerRef = ref(null)
// 两个左侧选择面板（工具依赖 / 白名单）共用一套几何：贴在设置面板左侧
const pickerRect = ref({ top: 0, left: 0, width: 420, height: 600 })
const pickerStyle = computed(() => ({
  top: pickerRect.value.top + 'px',
  left: pickerRect.value.left + 'px',
  width: pickerRect.value.width + 'px',
  height: pickerRect.value.height + 'px'
}))

// ESC 关闭：选择器开着时先退选择器，再按一次才退设置面板（栈顶优先，见 useEscClose）
useEscClose(() => props.visible, () => emit('close'))
useEscClose(depPickerVisible, () => { depPickerVisible.value = false })
useEscClose(agentPickerVisible, () => { agentPickerVisible.value = false })
// 几何：贴在设置面板左侧；左侧空间不足时收缩（下限 280），再不够才贴窗口左边
const updatePickerRect = () => {
  const el = settingsPanelRef.value
  if (!el) return
  const r = el.getBoundingClientRect()
  const width = Math.min(r.width, Math.max(280, r.left))
  pickerRect.value = { top: r.top, left: Math.max(0, r.left - width), width, height: r.height }
}
const anyPickerVisible = computed(() => depPickerVisible.value || agentPickerVisible.value)
// 几何跟随：设置面板宽度可拖拽、浏览器窗口可缩放，两者发生时都要重算选择器位置，
// 否则选择器会跟面板「脱开」（错位、压住内容）。
watch(settingsPanelWidth, () => {
  if (anyPickerVisible.value) nextTick(updatePickerRect)
})
watch(anyPickerVisible, (visible) => {
  if (visible) {
    nextTick(updatePickerRect) // 布局完成后按设置面板实际 rect 定位
    window.addEventListener('resize', updatePickerRect)
  } else {
    window.removeEventListener('resize', updatePickerRect)
  }
})
const inDraft = (ref) => depItems.value.some((d) => d.ref === ref)
const openDepPicker = () => {
  if (blockIfReadonly()) return
  if (!props.skillId) { ElMessage.warning(t('skill.needCreate')); return }
  depSearch.value = ''
  depFilterKind.value = ''
  agentPickerVisible.value = false // 两个选择面板互斥，避免叠在一起
  updatePickerRect()
  depPickerVisible.value = true   // 不再拷贝草稿：选择器直接改 depItems
}
// 整组声明：加组时移除此前单独加入的同组成员，避免语义重复
const pickToggleGroup = (c) => {
  if (blockIfReadonly()) return
  if (inDraft(c.ref)) {
    depItems.value = depItems.value.filter((d) => d.ref !== c.ref)
    depDirty.value = true
    return
  }
  const members = new Set((c.items || []).map((t) => t.ref))
  depItems.value = depItems.value.filter((d) => !(d.item_type === 'tool' && members.has(d.ref)))
  depItems.value.push({
    ref: c.ref, item_type: 'group', label: c.name, kind: c.kind, count: c.count || 0, description: ''
  })
  depDirty.value = true
}
// 单个工具：整组已声明时忽略（已在组内）
const pickToggleTool = (c, t) => {
  if (blockIfReadonly()) return
  if (inDraft(c.ref)) return
  const exist = depItems.value.find((d) => d.ref === t.ref)
  if (exist) depItems.value = depItems.value.filter((d) => d.ref !== t.ref)
  else depItems.value.push({
    ref: t.ref, item_type: 'tool', label: t.label || t.name, kind: c.kind, count: 0,
    description: t.description || ''
  })
  depDirty.value = true
}

// 打开面板：读一次可见范围（白名单勾选项就靠它回填），回到「可见性」tab
watch(() => props.visible, async (visible) => {
  if (!visible) return
  settingsTab.value = 'visibility'
  depPickerVisible.value = false
  agentPickerVisible.value = false
  if (!props.skillId) return
  await loadVisibility()
}, { immediate: true })

// 切回「全员可见」：白名单区整块消失，选择面板也就没有意义了
watch(() => props.form.restricted, (restricted) => {
  if (!restricted) agentPickerVisible.value = false
})

// 换 skill：草稿 / 工具目录缓存全部失效（父组件重开卡片也不会读到旧目录）
watch(() => props.skillId, async () => {
  depItems.value = []
  depDirty.value = false
  depsLoaded.value = false
  depsSkillId.value = ''
  depSearch.value = ''
  agentSearch.value = ''
  toolCatalog.value = []
  depPickerVisible.value = false
  agentPickerVisible.value = false
  if (props.visible && props.skillId) await loadVisibility()
})

</script>

<style scoped>
/* ============ 设置面板 ============ */
.settings-panel {
  position: absolute;
  z-index: 100;
  right: 0;
  top: 0;
  bottom: 0;
  background: #ffffff;
  border-radius: 6px 0 0 6px;
  box-shadow: -4px 0 20px rgba(0, 0, 0, 0.2);
  display: flex;
  flex-direction: column;
  overflow: hidden;
}

.panel-resizer {
  position: absolute;
  left: 0;
  top: 0;
  bottom: 0;
  width: 6px;
  cursor: col-resize;
  background: transparent;
  z-index: 10;
}

.panel-resizer:hover {
  background: #e0e0e0;
}

.panel-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 6px 16px;
  color: black;
  gap: 12px;
}

/* tab 样式对齐智能体设置面板（下划线式） */
.settings-tabs {
  display: flex;
  gap: 16px;
  flex: 1;
}

.settings-tab-btn {
  padding: 6px 4px;
  border: none;
  background: transparent;
  font-size: 14px;
  font-weight: 600;
  color: #666;
  cursor: pointer;
  position: relative;
}

.settings-tab-btn:hover {
  color: #1890ff;
}

.settings-tab-btn.active {
  color: #1890ff;
}

.settings-tab-btn.active::after {
  content: '';
  position: absolute;
  bottom: -1px;
  left: 0;
  right: 0;
  height: 2px;
  background: #1890ff;
}

.close-icon {
  cursor: pointer;
  padding: 4px;
  border-radius: 4px;
  font-size: 18px;
}

.close-icon:hover {
  background: rgba(0, 0, 0, 0.1);
}

.panel-body {
  flex: 1;
  overflow-y: auto;
  padding: 16px;
  min-height: 0;
  scrollbar-gutter: stable;
}

.settings-field {
  margin-bottom: 16px;
}

.settings-field .field-label {
  font-size: 13px;
  color: #606266;
  margin-bottom: 6px;
}

/* 白名单绑定行：左侧按钮拉出选择面板，右侧平铺已选 agent（与技能 / 调用授权同一套样式） */
.bind-row {
  display: flex;
  align-items: flex-start;
  gap: 8px;
}
.bind-tags {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  padding-top: 2px;
}
.bind-tags-empty {
  font-size: 12px;
  color: #b1b3b8;
  line-height: 24px;
}

/* 白名单下拉：选项两行（名字 + 描述），与智能体「它能调用谁」保持一致 */
.agent-option {
  display: flex;
  flex-direction: column;
  gap: 2px;
  padding: 2px 0;
}

.agent-option-name {
  font-size: 13px;
  font-weight: 600;
  color: #303133;
}

.agent-option-desc {
  font-size: 12px;
  color: #909399;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

/* 下拉浮层 teleport 到 body，必须用 :global 才能命中；把默认定高放开成自适应 */
:global(.agent-select-popper .el-select-dropdown__item) {
  height: auto;
  line-height: 1.4;
  padding: 6px 12px;
}

:global(.agent-select-popper .el-select-dropdown__item.is-selected .agent-option-name) {
  color: #409eff;
}

/* 触发器高一些：多选 tag 可换行，内容始终垂直居中（与智能体一致） */
.settings-field :deep(.el-select__wrapper) {
  min-height: 44px;
  padding: 4px 12px;
  align-items: center;
}

.editor-tip {
  font-size: 12px;
  color: #909399;
  margin-top: 6px;
  line-height: 1.6;
}

.dirty-tip {
  font-size: 12px;
  color: #e6a23c;
}

/* 工具依赖保存条（对齐智能体「工具配置」） */
.tools-save-bar {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-top: 14px;
  padding-top: 12px;
  border-top: 1px solid #f0f0f0;
}

.tools-count {
  margin-left: auto;
  font-size: 12px;
  color: #909399;
}

/* 工具 / 组行 */
.tools-list {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.tool-item {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 8px 10px;
  border: 1px solid #f0f0f0;
  border-radius: 6px;
}

.tool-item-main {
  flex: 1;
  min-width: 0;
}

.tool-name {
  font-size: 13px;
  font-weight: 600;
  color: #333;
}

.tool-desc {
  font-size: 12px;
  color: #909399;
  margin-top: 2px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.grant-group-icon {
  flex-shrink: 0;
  color: #909399;
}

.grant-tool-icon {
  flex-shrink: 0;
  color: #c0c4cc;
}

.ref-tag {
  display: inline-block;
  margin-left: 6px;
  padding: 0 5px;
  font-size: 11px;
  font-weight: 400;
  line-height: 16px;
  color: #909399;
  background: #f4f4f5;
  border-radius: 3px;
}

/* 「仅 supervisor」徽标：提示该（组内）工具只对带超级权限的 agent 实际生效 */
.ref-tag.supervisor-only {
  color: #b88230;
  background: #fdf6ec;
}

/* 主色徽标：白名单面板里的「已选」 */
.ref-tag.core {
  color: #409eff;
  background: #ecf5ff;
}

/* ============ 工具依赖：左侧选择器（与智能体「添加工具 / 组」同款，只有一种区） ============ */
.picker-panel {
  position: fixed;
  z-index: 4000; /* body 级浮层：高于 Element Plus 动态弹层（2000 起）与设置面板 */
  background: #ffffff;
  border-radius: 6px;
  box-shadow: -4px 0 20px rgba(0, 0, 0, 0.2);
  display: flex;
  flex-direction: column;
  overflow: hidden;
}

.picker-panel .panel-body {
  display: flex;
  flex-direction: column; /* 整体滚动（.panel-body 自带 overflow-y:auto） */
}

.picker-block-title {
  margin: 0 0 6px;
  font-size: 12px;
  font-weight: 600;
  color: #606266;
}
.picker-block-hint {
  margin-left: 6px;
  font-weight: 400;
  color: #909399;
}

.picker-toolbar {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-bottom: 10px;
  flex-wrap: wrap;
}

.picker-kinds {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}

.picker-cards {
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.picker-card {
  border: 1px solid #ebeef5;
  border-radius: 8px;
  overflow: hidden;
}

.picker-card.active {
  border-color: #409eff;
}

.picker-card-head {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 8px 10px;
  cursor: pointer;
  background: #fafafa;
}

.picker-card-head:hover {
  background: #f5faff;
}

.picker-card.active .picker-card-head {
  background: #ecf5ff;
}

/* 收起/展开箭头：点箭头只切展开，不影响整组选中 */
.picker-caret {
  color: #909399;
  cursor: pointer;
  transition: transform 0.2s;
}

.picker-caret.open {
  transform: rotate(90deg);
}

.picker-card-body {
  display: flex;
  flex-direction: column;
}

.picker-tool {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 5px 10px 5px 22px;
  font-size: 12px;
  color: #606266;
  cursor: pointer;
  border-top: 1px solid #f5f5f5;
}

.picker-tool:hover {
  background: #f5faff;
}

.picker-tool.on {
  color: #409eff;
}

.picker-tool.covered {
  color: #67c23a;
  cursor: default;
}

.picker-tool-mark {
  color: #c0c4cc;
}

.picker-tool.on .picker-tool-mark {
  color: #409eff;
}

.picker-tool.covered .picker-tool-mark {
  color: #67c23a;
}

.picker-check {
  color: #409eff;
}

.picker-footer {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
  padding: 10px 16px;
  border-top: 1px solid #f0f0f0;
}

</style>
