<template>
  <div class="wf-instances">
    <!-- 全屏模式显示子组件 -->
    <div v-if="fullScreenMode" class="full-screen-form">
      <div ref="headerRef" class="full-screen-header" :class="'header--' + headerSize">
        <div class="header-actions">
          <el-button size="small" @click="handleBack">
            <el-icon><ArrowLeft /></el-icon>{{ t('flow.list.backToList') }}
          </el-button>
        </div>
        <el-icon class="header-icon"><Share /></el-icon>
        <div class="header-info">
          <div class="header-name-wrapper">
            <span v-if="!isEditingName" class="header-name"
                  :title="instanceProps.name || (currentWFId ? t('flow.list.editFlow') : t('flow.list.newFlow'))"
                  @click="startEditName">{{ instanceProps.name || (currentWFId ? t('flow.list.editFlow') : t('flow.list.newFlow')) }}</span>
            <el-input v-else v-model="editName" size="small" class="header-name-input"
                      maxlength="64" :placeholder="t('flow.list.namePlaceholder')"
                      @blur="saveEditName" @keyup.enter="saveEditName" ref="nameInputRef" />
          </div>
          <div class="header-desc-wrapper">
            <span class="header-desc"
                  :title="instanceProps.description || t('flow.list.noDescription')"
                  @click="openDescDialog">{{ instanceProps.description || t('flow.list.noDescription') }}</span>
          </div>
        </div>
        <div class="header-buttons">
          <span v-if="isDirty" class="dirty-indicator" :title="t('flow.list.dirtyTip')">{{ t('flow.list.unsaved') }}</span>
          <el-button size="small" class="header-action-btn run-panel-btn" @click="openRunPanel">{{ t('flow.list.testRun') }}</el-button>
          <div class="variables-card">
            <el-tooltip :content="t('flow.header.flowVars')" placement="bottom">
              <el-icon class="variable-icon" @click="flowRef.toggleVariablesPanel('flow')"><ChromeFilled /></el-icon>
            </el-tooltip>
            <el-tooltip :content="t('flow.header.envVars')" placement="bottom">
              <el-icon class="variable-icon" @click="flowRef.toggleVariablesPanel('environment')"><Notification /></el-icon>
            </el-tooltip>
            <el-tooltip :content="t('flow.header.globalVarsReadonly')" placement="bottom">
              <el-icon class="variable-icon" @click="flowRef.toggleVariablesPanel('global')"><Key /></el-icon>
            </el-tooltip>
          </div>
          <el-button size="small" class="header-action-btn" @click="handleSave">{{ t('flow.list.save') }}</el-button>
          <!-- 版本：正式调用只运行已发布的那一版，所以「现在是第几版」必须随时可见 -->
          <el-tooltip :content="versionTip" placement="bottom">
            <el-button size="small" class="header-action-btn version-btn"
                       :class="{ 'version-btn--draft': !releasedVersion, 'version-btn--dirty': draftDirty }"
                       @click="openVersionPanel">{{ currentVersionLabel }}</el-button>
          </el-tooltip>
          <el-button size="small" type="primary" class="header-action-btn" @click="handleExposeWF()">
            {{ t('flow.list.endpoints') }}
          </el-button>
          <!-- 窄档：只有变量面板收进「⋯」—— 试运行是主操作，始终平铺 -->
          <el-dropdown class="header-more" trigger="click" @command="onHeaderMore">
            <el-button size="small" class="header-action-btn more-btn" :title="t('flow.list.more')">
              <el-icon><MoreFilled /></el-icon>
            </el-button>
            <template #dropdown>
              <el-dropdown-menu>
                <el-dropdown-item command="vars-flow">{{ t('flow.header.flowVars') }}</el-dropdown-item>
                <el-dropdown-item command="vars-env">{{ t('flow.header.envVars') }}</el-dropdown-item>
                <el-dropdown-item command="vars-global">{{ t('flow.header.globalVars') }}</el-dropdown-item>
              </el-dropdown-menu>
            </template>
          </el-dropdown>
        </div>
      </div>
      <div class="full-screen-content">
        <Flow
          ref="flowRef"
          :key="flowRenderKey"
          v-model:instanceProps="instanceProps"
          @refreshList="fetchflows"
          @cancel="handleBack"
          @dirty-change="isDirty = $event"
        />

        <!-- 版本面板：与「试运行」同一个右侧浮层（同样的圆角 / 投影 / 上下留白），
             所以放在 .full-screen-content 里、按 .run-panel 的规则定位。
             不用 el-drawer —— 那是整屏抽屉，跟编辑器里的面板不是一套视觉语言 -->
        <div v-if="versionPanelVisible" class="version-panel">
          <div class="version-panel-header">
            <span class="version-panel-title">{{ t('flow.versionPanel.title') }}</span>
            <el-icon class="version-panel-close" @click="versionPanelVisible = false"><Close /></el-icon>
          </div>
          <div class="version-panel-body">
            <div class="ver-release">
              <el-input v-model="releaseNote" size="small" maxlength="200" clearable
                        :placeholder="t('flow.versionPanel.notePlaceholder')" />
              <el-button size="small" type="primary" :loading="releasing" @click="doRelease">
                {{ t('flow.versionPanel.publish') }}
              </el-button>
            </div>
            <div class="ver-hint">
              {{ t('flow.versionPanel.publishHint') }}
              {{ t('flow.versionPanel.publishHint2') }}
              {{ t('flow.versionPanel.publishHint3') }}
            </div>
            <el-empty v-if="!versions.length && !versionLoading" :description="t('flow.versionPanel.empty')" />
            <div v-else v-loading="versionLoading" class="ver-list">
              <div v-for="v in versions" :key="v.version" class="ver-item">
                <div class="ver-main">
                  <span class="ver-no">v{{ v.version }}</span>
                  <el-tag v-if="v.is_current" size="small" type="success">{{ t('flow.versionPanel.current') }}</el-tag>
                  <span class="ver-note">{{ v.note || t('flow.versionPanel.noNote') }}</span>
                </div>
                <div class="ver-meta">
                  <span>{{ v.created_by_name || '—' }}</span>
                  <span>{{ fmtVersionTime(v.created_at) }}</span>
                  <!-- 两个动作别混：「切到线上」改的是线上跑的版本；「载入画布」改的是我的草稿。
                       「载入画布」对当前版本同样有用：丢掉草稿、回到线上那一份 -->
                  <el-button v-if="!v.is_current" link type="primary" size="small"
                             :loading="rollingBack === v.version" @click="doSwitchVersion(v)">
                    {{ t('flow.versionPanel.switchToOnline') }}
                  </el-button>
                  <el-button link size="small"
                             :loading="applyingVersion === v.version" @click="doApplyVersion(v)">
                    {{ t('flow.versionPanel.loadToCanvas') }}
                  </el-button>
                  <!-- 删除只清历史行：线上（当前版本）与画布都不动，所以当前版本不给删 -->
                  <el-button v-if="!v.is_current" link type="danger" size="small"
                             :loading="deletingVersion === v.version" @click="doDeleteVersion(v)">
                    {{ t('flow.versionPanel.delete') }}
                  </el-button>
                </div>
              </div>
            </div>
          </div>
        </div>
      </div>
    </div>

    <!-- 正常模式显示工作流列表 -->
    <div v-else class="wf-instances-list-mode">
      <!-- 工作流管理头部 -->
      <div
        ref="listHeaderRef"
        class="wf-instances-header"
        :class="['header--' + listHeaderSize, { 'header--search-open': listSearchOpen }]"
      >
        <!-- 状态筛选：全部 / 正常 / 废弃（三按钮紧挨成组） -->
        <div class="status-filters">
          <el-button :class="{ active: selectedType === 'all' }" @click="selectType('all')">
            <el-icon><Refresh /></el-icon>
            <span>{{ t('flow.list.tabAll') }}{{ statusCounts.all ? ` (${statusCounts.all})` : '' }}</span>
          </el-button>
          <el-button class="btn-enable" :class="{ active: selectedType === 'normal' }" @click="selectType('normal')">
            <el-icon><CircleCheck /></el-icon>
            <span>{{ statusMeta(1).text }}{{ statusCounts.normal ? ` (${statusCounts.normal})` : '' }}</span>
          </el-button>
          <el-button class="btn-disable" :class="{ active: selectedType === 'deprecated' }" @click="selectType('deprecated')">
            <el-icon><CircleClose /></el-icon>
            <span>{{ statusMeta(3).text }}{{ statusCounts.deprecated ? ` (${statusCounts.deprecated})` : '' }}</span>
          </el-button>
        </div>
        <!-- 窄档：状态筛选合并成一个下拉 -->
        <el-dropdown class="status-filter-compact" trigger="click" @command="selectType">
          <el-button :class="['active', activeStatus.cls]">
            <el-icon><component :is="activeStatus.icon" /></el-icon>
            <span>{{ activeStatusLabel }}</span>
            <el-icon class="caret"><ArrowDown /></el-icon>
          </el-button>
          <template #dropdown>
            <el-dropdown-menu>
              <el-dropdown-item v-for="opt in STATUS_FILTERS" :key="opt.key" :command="opt.key">
                <el-icon><component :is="opt.icon" /></el-icon>
                <span>{{ opt.label }}{{ statusCounts[opt.key] ? ` (${statusCounts[opt.key]})` : '' }}</span>
              </el-dropdown-item>
            </el-dropdown-menu>
          </template>
        </el-dropdown>
        <!-- 搜索框 -->
        <div class="search-box">
          <el-input
            ref="listSearchInputRef"
            v-model="searchQuery"
            :placeholder="t('flow.list.searchPlaceholder')"
            prefix-icon="Search"
            clearable
            @keydown.esc="closeListSearch"
          />
        </div>
        <!-- 窄档：搜索收成图标，点击展开为整行搜索 -->
        <el-button
          class="search-toggle"
          :class="{ active: !!searchQuery }"
          :title="t('flow.list.search')"
          @click="toggleListSearch"
        >
          <el-icon><component :is="listSearchOpen ? Close : Search" /></el-icon>
        </el-button>
        <!-- 导入：从资产包装一个工作流。次要动作，不抢「新增」的主色 -->
        <el-button class="header-import-btn" @click="importVisible = true" round :title="t('flow.list.importFlow')">
          <el-icon><Upload /></el-icon>
          <span>{{ t('flow.list.import') }}</span>
        </el-button>
        <!-- 新增工作流按钮（窄档 sm/xs 只留 +，文字由 CSS 收起；title 兜住语义） -->
        <el-button class="header-create-btn" type="primary" @click="addFlow" round :title="t('flow.list.newFlow')">
          <el-icon><Plus /></el-icon>
          <span>{{ t('flow.list.newFlow') }}</span>
        </el-button>

      </div>
      
      <div class="wf-instances-content">
        <div v-if="isLoading" class="loading">{{ t('flow.list.loading') }}</div>
        <div v-else-if="filteredflows.length === 0" class="no-data">
          {{ emptyText }}
        </div>
        <div v-else class="wf-instances-list">
          <!-- 悬停提示：鼠标进入整张卡片即显示。
               鼠标压到卡片体上的操作浮层时关掉它（见 actionsHovered），避免与按钮自己的 tip 打架 -->
          <el-tooltip v-for="instance in filteredflows" :key="instance.id"
                      :content="instance.status === 3 ? t('flow.list.cardTipDeprecated') : t('flow.list.cardTipEditable')"
                      placement="top" :disabled="actionsHovered">
              <div class="wf-instance-card" @click="handleEditFlow(instance)"
                   @mousemove="onCardMove" @mouseleave="actionsHovered = false">
              <div class="card-header">
                <div class="header-content">
                  <div class="card-title" :class="titleClass(instance.status)">
                    <span class="card-title-text">{{ instance.name }}</span>
                  </div>
                  <div class="card-description">
                    <el-tag size="small" effect="dark" :type="statusMeta(instance.status).tag">
                      {{ statusMeta(instance.status).text }}
                    </el-tag>
                    <!-- 当前发布版本：0 表示还没发布（正式调用不可用，只能调试）。
                         用原生 title 而不是 el-tooltip —— 整卡已有一个 tooltip，嵌套 popper 会打架 -->
                    <el-tag size="small" effect="plain" type="info" class="tag-version"
                            :title="instance.current_version ? t('flow.list.versionTip', { version: instance.current_version }) : t('flow.list.versionTipNone')">
                      {{ instance.current_version ? `v${instance.current_version}` : t('flow.list.unpublished') }}
                    </el-tag>
                    <template v-if="(instance.endpoints || []).length > 0">
                      <span
                        v-for="p in instance.endpoints"
                        :key="p.protocol"
                        class="endpoint-tag"
                        :style="{ color: getEndpointTextColor(p) }"
                      >
                        {{ (p.protocol || '').toLowerCase() }}
                      </span>
                    </template>
                    <span v-else>{{ t('flow.list.noEndpoints') }}</span>
                  </div>
                </div>
            </div>
            <div class="card-body">
              <div class="card-body-bottom">
                <div class="info-item">
                  <span class="info-label">{{ t('flow.list.descLabel') }}</span>
                  <span class="info-value description-value">{{ instance.description || t('flow.list.emptyDesc') }}</span>
                </div>
                <div class="info-item">
                  <span class="info-label">{{ t('flow.list.updatedLabel') }}</span>
                  <span class="info-value">{{ instance.updated_by_name || t('flow.list.unknown') }} · {{ formatDate(instance.updated_at) }}</span>
                </div>
              </div>
              <!-- 操作浮层：只覆盖「卡片头下方」那块（card-body），按钮居中（与技能卡片一致） -->
              <div class="card-actions" @click.stop>
                <!-- 两态：正常(1) / 废弃(3)。「锁定」已取消：线上由版本管理，改草稿不影响线上 -->
                <el-tooltip v-if="instance.status !== 1" :content="t('flow.list.enableTip')" placement="top">
                  <button class="card-action-btn" @click="handleSetWFStatus(instance, 1)">
                    <el-icon><CircleCheck /></el-icon>
                  </button>
                </el-tooltip>
                <el-tooltip v-if="instance.status !== 3" :content="t('flow.list.disableTip')" placement="top">
                  <button class="card-action-btn" @click="handleSetWFStatus(instance, 3)">
                    <el-icon><CircleClose /></el-icon>
                  </button>
                </el-tooltip>
                <el-tooltip :content="t('flow.list.endpoints')" placement="top">
                  <button class="card-action-btn" @click="handleExposeWF(instance)">
                    <el-icon><Connection /></el-icon>
                  </button>
                </el-tooltip>
                <el-tooltip :content="t('flow.list.exportTip')" placement="top">
                  <button class="card-action-btn" @click="exportEntity('workflow', instance.id, instance.name)">
                    <el-icon><Download /></el-icon>
                  </button>
                </el-tooltip>
                <el-tooltip :content="t('flow.list.deleteTip')" placement="top">
                  <button class="card-action-btn danger" @click="handleDeleteFlow(instance)">
                    <el-icon><Delete /></el-icon>
                  </button>
                </el-tooltip>
              </div>
            </div>
            </div>
          </el-tooltip>
        </div>
      </div>
    </div>

    <FlowEndpointsPanel
      v-if="exposeDrawerVisible"
      :wf="currentExposeWF"
      :refresh-list="fetchflows"
      :sync-card="syncCardEndpoints"
      @close="exposeDrawerVisible = false"
    />

    <!-- 资产包导入（对话框本体是四类资产共用的通用组件） -->
    <AssetImportDialog v-model="importVisible" kind="workflow" @imported="fetchflows" />

    <!-- 描述单独用大框编辑：头部那行字号只有 12px、还被单行省略，长描述在原位输入框里基本没法看 -->
    <el-dialog v-model="descDialogVisible" :title="t('flow.descDialog.title')" width="640px" append-to-body
               :close-on-click-modal="false" class="wf-desc-dialog">
      <el-input v-model="descDraft" type="textarea" :rows="8" resize="vertical"
                :placeholder="t('flow.descDialog.placeholder')"
                @keydown.ctrl.enter="confirmDescDialog" />
      <div class="wf-desc-hint">
        {{ t('flow.descDialog.hint') }}
      </div>
      <template #footer>
        <el-button size="small" @click="descDialogVisible = false">{{ t('common.cancel') }}</el-button>
        <el-button size="small" type="primary" @click="confirmDescDialog">{{ t('common.confirm') }}</el-button>
      </template>
    </el-dialog>

  </div>
</template>

<script setup>
import { ref, computed, onMounted, onActivated, onUnmounted, onDeactivated, nextTick, watch, provide } from 'vue'
import { useI18n } from 'vue-i18n'
import { ElButton, ElInput, ElIcon, ElMessage, ElMessageBox, ElDialog } from 'element-plus'
import { Plus, Search, Refresh, Delete, Grape, Magnet, Management, Paperclip, Connection, Link, HotWater, Share, ArrowLeft, ChromeFilled, Notification, CircleCheck, CircleClose, ArrowDown, Close, Upload, Download, Key } from '@element-plus/icons-vue'
import adminApi from '../config/admin.js'
import { getToken } from '../utils/auth.js'
import { exportEntity } from '../utils/assetTransfer.js'
import AssetImportDialog from './AssetImportDialog.vue'
import { useHeaderCompact } from '../utils/useHeaderCompact.js'
import {
  statusMeta, titleClass, NAME_RE,
  getProtocolColor, getEndpointTextColor, formatTime, formatDate
} from '../utils/flowFormat.js'
import Flow from './flows/Flow.vue'
import FlowEndpointsPanel from './flows/FlowEndpointsPanel.vue'
import { formatError } from '../../locales'
import { formatDateTime } from '@/utils/datetime.js'

const { t } = useI18n()

// 搜索关键词
const searchQuery = ref('')

// 鼠标是否正压在卡片体上的操作浮层：压上去时把「点击编辑工作流」的 tip 关掉
// （el-tooltip 的 disabled 会立刻关闭已显示的 tip），否则外层 tip 会和按钮自己的 tip 一起冒出来。
// 用 mousemove + closest 判定：指针从卡片外直接进到按钮上时 mouseenter 不一定触发，
// 而卡片整体带 @mousemove，一定会经过。
const actionsHovered = ref(false)
const onCardMove = (e) => { actionsHovered.value = !!e.target?.closest?.('.card-actions') }
// 选中的类型（状态筛选）：all 全部 | normal 正常(1) | deprecated 废弃(3)
// 缺省「全部」——废弃的如果默认藏起来，用户就找不到切回正常的入口了
const selectedType = ref('all')
// 工作流实例列表
const flowsList = ref([])
// 资产包导入（对话框本体是通用组件，这里只管开关）
const importVisible = ref(false)

// 跨页跳转（「工具与端点 → 端点 → 点目标名」）：切到本页后直接进入该工作流的编辑页。
// 父层每次传入新对象（含 ts），所以同一工作流重复点也能触发。
const props = defineProps({ focus: { type: Object, default: null } })
// focus.id 可能是 id，也可能是名称：会话联动的工具结果被截断时只剩参数里的 flow 名
const findFlowByRef = (ref) => flowsList.value.find((x) =>
  String(x.id) === String(ref) || (x.name && String(x.name) === String(ref)))
watch(() => props.focus, async (f) => {
  if (!f || f.nav !== 'wf-instances' || !f.id) return
  let inst = findFlowByRef(f.id)
  // 会话里刚 create_flow：keep-alive 的列表还是旧快照，先刷一次再找
  if (!inst) {
    await fetchflows()
    inst = findFlowByRef(f.id)
  }
  if (inst) handleEditFlow(inst, f.node_id || '')
})
// 状态数量统计（用于筛选按钮上显示；为 0 时模板不渲染数字）
const statusCounts = computed(() => {
  const list = flowsList.value
  return {
    all: list.length,
    normal: list.filter(instance => (instance.status || 1) === 1).length,
    deprecated: list.filter(instance => instance.status === 3).length
  }
})
// 过滤后的工作流列表（先按状态筛选，再按关键词）
const filteredflows = computed(() => {
  let list = flowsList.value
  if (selectedType.value === 'normal') {
    list = list.filter(instance => (instance.status || 1) === 1)
  } else if (selectedType.value === 'deprecated') {
    list = list.filter(instance => instance.status === 3)
  }
  if (!searchQuery.value) {
    return list
  }
  const query = searchQuery.value.toLowerCase()
  return list.filter(instance => {
    return instance.name.toLowerCase().includes(query) ||
           (instance.description && instance.description.toLowerCase().includes(query))
  })
})
// 空列表提示文案（区分「搜索无结果」与「该状态下暂无数据」）
const emptyText = computed(() => {
  if (searchQuery.value) {
    return t('flow.list.emptySearch')
  }
  if (selectedType.value === 'normal') {
    return t('flow.list.emptyNone')
  }
  if (selectedType.value === 'deprecated') {
    return t('flow.list.emptyDeprecated')
  }
  return t('flow.list.emptyNone')
})
// 加载状态
const isLoading = ref(false)

// 全屏模式
const fullScreenMode = ref(false)

// 头部窄屏分层降级：宽度不足时收起描述，并把变量面板收进「⋯」。
// 试运行是主操作，任何档位都平铺 —— 收进下拉等于每次多一次点击。
const headerRef = ref(null)
const headerSize = useHeaderCompact(headerRef, { md: 1100, sm: 900 })
const onHeaderMore = (cmd) => {
  if (cmd === 'vars-flow') flowRef.value && flowRef.value.toggleVariablesPanel('flow')
  else if (cmd === 'vars-env') flowRef.value && flowRef.value.toggleVariablesPanel('environment')
  else if (cmd === 'vars-global') flowRef.value && flowRef.value.toggleVariablesPanel('global')
}

// 列表头部窄屏分层降级：先把状态筛选合并为下拉，仍不够再把搜索框收成图标（点击展开为整行搜索）
const listHeaderRef = ref(null)
const listHeaderSize = useHeaderCompact(listHeaderRef, { md: 1050, sm: 880, xs: 500 })
const listSearchOpen = ref(false)
const listSearchInputRef = ref(null)
const toggleListSearch = () => {
  listSearchOpen.value = !listSearchOpen.value
  if (listSearchOpen.value) nextTick(() => listSearchInputRef.value && listSearchInputRef.value.focus())
}
const closeListSearch = () => { listSearchOpen.value = false }
// 离开最窄档后收起搜索展开态，避免隐藏状态下残留
watch(listHeaderSize, (s) => { if (s !== 'xs') listSearchOpen.value = false })
// 状态筛选项（紧凑下拉用；与模板里的平铺按钮一一对应）
const STATUS_FILTERS = computed(() => [
  { key: 'all', label: t('flow.list.tabAll'), icon: Refresh, cls: '' },
  { key: 'normal', label: statusMeta(1).text, icon: CircleCheck, cls: 'btn-enable' },
  { key: 'deprecated', label: statusMeta(3).text, icon: CircleClose, cls: 'btn-disable' }
])
const activeStatus = computed(() => STATUS_FILTERS.value.find(o => o.key === selectedType.value) || STATUS_FILTERS.value[0])
const activeStatusLabel = computed(() => {
  const o = activeStatus.value
  const n = statusCounts.value[o.key]
  return o.label + (n ? ` (${n})` : '')
})
// 当前编辑的工作流ID
const currentWFId = ref(null)
// 当前编辑工作流的状态（1 正常 / 3 废弃）。废弃的流程不允许进入编辑（见 handleEditFlow），
// 这里留着是为了「编辑期间状态被别处改掉」时保存前能提前拦一下（后端同样会拒）
const currentWFStatus = ref(1)
// Flow组件引用
const flowRef = ref(null)
// 换 key 强制重建 Flow：等价于「退出编辑再进一次」，用于 Agent 改完流程图后重画画布
const flowRenderKey = ref(0)
// 是否有未保存修改
const isDirty = ref(false)
// 编辑状态
const isEditingName = ref(false)
const editName = ref('')
const nameInputRef = ref(null)
// 描述走弹窗编辑：头部那行只有 12px 且单行省略，原位输入框对长描述不可用
const descDialogVisible = ref(false)
const descDraft = ref('')
// 打开时的原始名字：改名会改变对外工具名（local__<name>），保存前要据此提示。
// 新建流程时为空 —— 没有「旧名」也就没有对外改名的风险。
const originalWFName = ref('')
// 表单数据
const instanceProps = ref({
  name: '',
  diagram: '',
  description: '',
  arguments: '',
  outputs: '',
  env_vars: '',
  flow_vars: '',
  timeout: 300,
  max_recursion_depth: -1,
  max_node_count: -1,
  max_exec_count: -1,
  max_run_records: 50,
  max_session_records: 30
})

// 打开新增工作流抽屉
const addFlow = () => {
  // 重置当前编辑的工作流ID
  currentWFId.value = null
  currentWFStatus.value = 1
  originalWFName.value = ''
  // 重置表单
  instanceProps.value = {
    name: '',
    diagram: '',
    description: '',
    arguments: '',
    outputs: '',
    env_vars: '',
    flow_vars: '',
    timeout: 300,
    max_recursion_depth: -1,
    max_node_count: -1,
    max_exec_count: -1,
    max_run_records: 50,
    max_session_records: 30
  }
  // 进入全屏模式
  fullScreenMode.value = true
}

// 把后端返回的完整定义灌进编辑器（首次打开与「已打开时刷新」共用）。
const applyFlowDetail = (wfData) => {
  // 设置当前编辑的工作流ID / 状态（以后端返回为准，避免外部入口漏设）
  currentWFId.value = wfData.id
  currentWFStatus.value = wfData.status || 1
  // 记下原名：改名会改掉对外工具名（local__<name>），保存前据此提示
  originalWFName.value = wfData.name || ''
  // 填充表单数据（使用完整的后端数据）
  instanceProps.value = {
    id: wfData.id,
    name: wfData.name || '',
    diagram: wfData.diagram || '',
    description: wfData.description || '',
    arguments: wfData.arguments || '',
    outputs: wfData.outputs || '',
    env_vars: wfData.env_vars || '',
    flow_vars: wfData.flow_vars || '',
    timeout: wfData.timeout ?? -1,
    max_recursion_depth: wfData.max_recursion_depth ?? -1,
    max_node_count: wfData.max_node_count ?? -1,
    max_exec_count: wfData.max_exec_count ?? -1,
    max_run_records: wfData.max_run_records ?? -1,
    max_session_records: wfData.max_session_records ?? -1
  }
  // 版本状态以后端为准（头部「v3 / 未发布 / 有改动」不能拿本地状态糊弄）
  releasedVersion.value = wfData.current_version || 0
  draftDirty.value = !!wfData.dirty
  loadVersions()
}

// 拉一次最新定义。注意后端 xhttp.ErrJSON 的错误响应也是 HTTP 200，业务码在 body。
const fetchFlowDetail = async (flowId) => {
  const token = getToken()
  if (!token) return null
  const response = await fetch(`${adminApi.flows.base}${flowId}`, {
    method: 'GET',
    headers: {
      'Content-Type': 'application/json',
      'Authorization': `Bearer ${token}`
    }
  })
  if (!response.ok) {
    throw new Error(t('flow.save.loadFailed'))
  }
  const data = await response.json()
  if (data.code !== 200 || !data.data) {
    ElMessage.error(formatError(data) || t('flow.save.loadFailed'))
    return null
  }
  return data.data
}

// ===== 版本 =====
// 正式入口（MCP / RPC / 其它流程直调）只运行 is_current 那一版，草稿只在「试运行」里生效。
// 所以头部要一直显示「现在是第几版」，否则用户改完画布会误以为线上已经变了。
const versions = ref([])
const versionPanelVisible = ref(false)
const versionLoading = ref(false)
const releaseNote = ref('')
const releasing = ref(false)
const rollingBack = ref(0)
const applyingVersion = ref(0)
const deletingVersion = ref(0)

// 服务端算好的版本状态（GetFlow 返回的 current_version / dirty），
// 比自己从版本列表里推更可靠：发布、保存之后它和脏标记要一起变。
const releasedVersion = ref(0)
const draftDirty = ref(false)

const currentVersion = computed(() => versions.value.find(v => v.is_current) || null)
const currentVersionLabel = computed(() => {
  if (!releasedVersion.value) return t('flow.list.unpublished')
  return draftDirty.value
    ? t('flow.release.draftDirty', { version: releasedVersion.value })
    : `v${releasedVersion.value}`
})
const versionTip = computed(() => {
  if (!releasedVersion.value) return t('flow.release.notReleasedTip')
  if (draftDirty.value) return t('flow.release.dirtyTip', { version: releasedVersion.value })
  return t('flow.release.currentTip', { version: releasedVersion.value })
})

// 只刷新版本状态，不动画布（保存后调用：内容已经落库，但还没发布）
const refreshVersionState = async () => {
  if (!currentWFId.value) return
  try {
    const wfData = await fetchFlowDetail(currentWFId.value)
    if (!wfData) return
    releasedVersion.value = wfData.current_version || 0
    draftDirty.value = !!wfData.dirty
  } catch {
    // 版本状态拿不到不影响主流程，保持原值
  }
}

const fmtVersionTime = (value) => (value ? (formatDateTime(value) || '—') : '—')

// 后端错误响应也是 HTTP 200，业务码在 body 里（与 fetchFlowDetail 同口径）
const versionRequest = async (url, options = {}) => {
  const token = getToken()
  if (!token) throw new Error(t('flow.save.notSignedIn'))
  const response = await fetch(url, {
    ...options,
    headers: {
      'Content-Type': 'application/json',
      'Authorization': `Bearer ${token}`
    }
  })
  if (!response.ok) throw new Error(t('flow.save.requestFailed'))
  const data = await response.json()
  if (data.code !== 200) throw new Error(formatError(data) || t('flow.save.requestFailed'))
  return data.data
}

const loadVersions = async () => {
  if (!currentWFId.value) {
    versions.value = []
    return
  }
  versionLoading.value = true
  try {
    const data = await versionRequest(adminApi.flows.versions(currentWFId.value))
    versions.value = data?.versions || []
  } catch (error) {
    ElMessage.error(error.message)
  } finally {
    versionLoading.value = false
  }
}

const openVersionPanel = async () => {
  if (!currentWFId.value) {
    ElMessage.warning({ message: t('flow.save.saveFirstForVersion'), duration: 3000 })
    return
  }
  // 再点一次按钮就收起（外面那层 document 监听排除了按钮本身，所以这里自己 toggle）
  if (versionPanelVisible.value) {
    versionPanelVisible.value = false
    return
  }
  // 与「试运行」互斥：两个都是右侧浮层，同时开会叠在一起
  flowRef.value?.closeRunPanel?.()
  versionPanelVisible.value = true
  await loadVersions()
}

// 版本面板是「瞄一眼就走」的浮层：点面板外部任何地方都收起。
// 排除面板本身、打开它的按钮，以及 confirm / message（它们挂在 body 上，点确认不该连带把面板关掉）。
// 用 mousedown 而非 click：按下即关，不用等抬手，也不影响画布上的拖拽。
const onDocMouseDownCloseVersionPanel = (e) => {
  const target = e.target
  if (!(target instanceof Element)) return
  if (target.closest('.version-panel, .version-btn, .el-overlay, .el-message-box, .el-message')) return
  versionPanelVisible.value = false
}

watch(versionPanelVisible, (visible) => {
  if (visible) document.addEventListener('mousedown', onDocMouseDownCloseVersionPanel)
  else document.removeEventListener('mousedown', onDocMouseDownCloseVersionPanel)
})

onUnmounted(() => {
  document.removeEventListener('mousedown', onDocMouseDownCloseVersionPanel)
})

const doRelease = async () => {
  // 发布冻结的是「已保存」的内容：有未保存改动时先保存，否则发出去的不是眼前这份
  if (isDirty.value) {
    ElMessage.warning({ message: t('flow.save.dirtyBeforePublish'), duration: 3000 })
    return
  }
  releasing.value = true
  try {
    await versionRequest(adminApi.flows.versions(currentWFId.value), {
      method: 'POST',
      body: JSON.stringify({ note: (releaseNote.value || '').trim() })
    })
    releaseNote.value = ''
    await loadVersions()
    await refreshVersionState()
    ElMessage.success({
      message: t('flow.save.published', { version: currentVersion.value?.version ?? '' }),
      duration: 2500
    })
  } catch (error) {
    ElMessage.error(error.message)
  } finally {
    releasing.value = false
  }
}

// 切到线上：只改「正在跑的是哪一版」，画布草稿一动不动
const doSwitchVersion = async (v) => {
  try {
    await ElMessageBox.confirm(
      t('flow.save.switchConfirm', { version: v.version }),
      t('flow.save.switchConfirmTitle', { version: v.version }),
      { type: 'warning', confirmButtonText: t('flow.versionPanel.switchToOnline'), cancelButtonText: t('common.cancel') }
    )
  } catch {
    return
  }
  rollingBack.value = v.version
  try {
    await versionRequest(adminApi.flows.rollback(currentWFId.value, v.version), {
      method: 'POST',
      body: JSON.stringify({})
    })
    await loadVersions()
    await refreshVersionState()
    ElMessage.success({ message: t('flow.save.switched', { version: v.version }), duration: 2500 })
  } catch (error) {
    ElMessage.error(error.message)
  } finally {
    rollingBack.value = 0
  }
}

// 载入画布：只改草稿，线上不动；载入后是「有改动」，要发布才生效
const doApplyVersion = async (v) => {
  // 载入当前版本 = 丢掉草稿、回到线上那一份；载入旧版本 = 以旧版本为起点继续改
  const tip = v.is_current
    ? t('flow.save.loadConfirmDirty', { version: v.version })
    : t('flow.save.loadConfirmClean', { version: v.version })
  try {
    await ElMessageBox.confirm(tip, t('flow.save.loadTitle', { version: v.version }), {
      type: 'warning',
      confirmButtonText: t('flow.versionPanel.loadToCanvas'),
      cancelButtonText: t('common.cancel')
    })
  } catch {
    return
  }
  applyingVersion.value = v.version
  try {
    await versionRequest(adminApi.flows.applyVersion(currentWFId.value, v.version), {
      method: 'POST',
      body: JSON.stringify({})
    })
    // 画布以服务端为准：载入改的是工作副本，本地画布必须跟着重建
    const wfData = await fetchFlowDetail(currentWFId.value)
    if (wfData) applyFlowDetail(wfData)
    await nextTick()
    flowRef.value?.snapshotDiagram?.()
    ElMessage.success({
      message: v.is_current
        ? t('flow.save.loadedDirty', { version: v.version })
        : t('flow.save.loadedClean', { version: v.version }),
      duration: 2500
    })
  } catch (error) {
    ElMessage.error(error.message)
  } finally {
    applyingVersion.value = 0
  }
}

// 删除历史版本：只清历史行，线上（当前版本）与画布都不动。
// 当前版本按钮上不出现，接口侧也会拦（ErrCannotDeleteCurrentVersion）。
const doDeleteVersion = async (v) => {
  try {
    await ElMessageBox.confirm(
      t('flow.save.deleteVersionConfirm', { version: v.version }),
      t('flow.save.deleteVersionTitle', { version: v.version }),
      { type: 'warning', confirmButtonText: t('flow.versionPanel.delete'), cancelButtonText: t('common.cancel') }
    )
  } catch {
    return
  }
  deletingVersion.value = v.version
  try {
    // DELETE 不带 body；versionRequest 只负责把后端业务码翻成异常
    await versionRequest(adminApi.flows.deleteVersion(currentWFId.value, v.version), {
      method: 'DELETE'
    })
    await loadVersions()
    ElMessage.success({ message: t('flow.save.versionDeleted', { version: v.version }), duration: 2500 })
  } catch (error) {
    ElMessage.error(error.message)
  } finally {
    deletingVersion.value = 0
  }
}

// Flow 换 key 后新旧实例会交接一下，等新实例的 ref 就绪再动它
const waitForFlowRef = async (tries = 20) => {
  for (let i = 0; i < tries; i++) {
    if (flowRef.value) return true
    await new Promise(r => setTimeout(r, 50))
  }
  return false
}

// 编辑器已经开着这条工作流时，按服务端最新定义重画。
// 之前这里只定位不刷新，画布留的还是内存里的旧 nodes：Agent 新增的看不到、删掉的还在，
// 必须退出重进才正常 —— 漏的就是这一步。
// 会话联动一律直接刷新：用户在对话里让 Agent 改流程，画布就该以服务端为准，
// 不再弹「是否放弃未保存改动」的确认（本地未保存的改动会被丢弃）。
//
// 重画方式改成「换 key 重建」：增量刷新（Flow 的 diagram watch 里重建 nodes/edges）实测会
// 残留旧布局 —— 容器尺寸与边锚点跟新数据对不上；重新挂载与「退出编辑再进一次」完全等价。
const refreshOpenFlow = async (instance, focusNodeId = '') => {
  try {
    const wfData = await fetchFlowDetail(instance.id)
    if (!wfData) return
    applyFlowDetail(wfData)
    // 同一个 tick 内换 key：老实例卸载，新实例带着新 instanceProps 直接挂载
    flowRenderKey.value += 1
    await nextTick()
    await waitForFlowRef()
    // 等首帧把节点尺寸量完（容器组件会自己撑一下）再重置「未保存」基线
    await new Promise(r => setTimeout(r, 200))
    flowRef.value?.snapshotDiagram?.()
    if (focusNodeId) flowRef.value?.focusNode(String(focusNodeId))
  } catch (error) {
    ElMessage.error(error.message)
    if (focusNodeId) flowRef.value?.focusNode(String(focusNodeId))
  }
}

// 处理编辑工作流
// focusNodeId：会话里改了某个节点时带过来的定位目标（空 = 只打开工作流）。
const handleEditFlow = async (instance, focusNodeId = '') => {
  // 废弃（status=3）不可进入编辑（后端 UpdateFlow 同样会拦截写入）。
  // 「锁定」态已取消：线上由版本管理，改草稿不影响线上，不需要再挡编辑
  if ((instance.status || 1) === 3) {
    ElMessage.warning({ message: t('flow.save.deprecatedCannotEdit', { name: instance.name }), duration: 3000 })
    return
  }
  // 已经在这个工作流的编辑页：Agent 可能刚改过服务端 diagram，必须重新拉一次再定位
  if (fullScreenMode.value && String(currentWFId.value) === String(instance.id)) {
    await refreshOpenFlow(instance, focusNodeId)
    return
  }
  currentWFStatus.value = instance.status || 1
  try {
    const wfData = await fetchFlowDetail(instance.id)
    if (!wfData) return
    applyFlowDetail(wfData)
    // 进入全屏模式
    fullScreenMode.value = true
    // 会话联动：等 Flow 挂载后定位到被改的节点（Flow 内部还会重试等 diagram 渲染完）
    if (focusNodeId) {
      await nextTick()
      flowRef.value?.focusNode(String(focusNodeId))
    }
  } catch (error) {
    ElMessage.error(error.message)
  }
}

// 开始编辑名字
const startEditName = () => {
  editName.value = instanceProps.value.name || ''
  isEditingName.value = true
  nextTick(() => {
    nameInputRef.value?.focus()
  })
}

// 保存名字编辑
const saveEditName = () => {
  instanceProps.value.name = editName.value.trim() || (currentWFId.value ? t('flow.list.editFlow') : t('flow.list.newFlow'))
  isEditingName.value = false
}

// 点头部描述那行：打开大框编辑
const openDescDialog = () => {
  descDraft.value = instanceProps.value.description || ''
  descDialogVisible.value = true
}

// 只改本地表单，落库仍走「保存」—— 这样不会绕过废弃态与「未保存」提示
const confirmDescDialog = () => {
  const next = (descDraft.value || '').trim()
  if (!next) {
    ElMessage.warning({ message: t('flow.descDialog.empty'), duration: 3000 })
    return
  }
  instanceProps.value.description = next
  descDialogVisible.value = false
}

// 处理返回
const handleBack = () => {
  fullScreenMode.value = false
  currentWFId.value = null
  // 重置表单
  instanceProps.value = {
    name: '',
    diagram: '',
    description: '',
    arguments: '',
    outputs: '',
    env_vars: '',
    flow_vars: '',
    timeout: 300,
    max_recursion_depth: -1,
    max_node_count: -1,
    max_exec_count: -1,
    max_run_records: 50,
    max_session_records: 30
  }
  // 编辑器里可能新建 / 收回过端点，卡片上的端点与状态要整表刷新才准
  fetchflows()
}

// 端点面板状态
const exposeDrawerVisible = ref(false)
const currentExposeWF = ref(null)

// 端点变更后局部更新对应卡片的端点显示（不刷新整个列表）。
// 端点数据在端点面板内部，这里只接收快照，避免把 exposedEndpoints 提到父级。
const syncCardEndpoints = (wfId, endpoints) => {
  const target = flowsList.value.find(item => String(item.id) === String(wfId))
  if (!target) return
  target.endpoints = endpoints
}

// 打开端点面板：只负责「定住是哪一个 + 打开面板」，
// 端点加载与表单初值由面板自己的力量在 onMounted 里做。
const handleExposeWF = (instance) => {
  const wf = instance || { ...instanceProps.value, id: currentWFId.value }
  if (!wf.id) {
    ElMessage.error({ message: t('flow.save.saveFirstForEndpoints'), duration: 3000, offset: 20, showClose: true })
    return
  }
  currentExposeWF.value = wf
  exposeDrawerVisible.value = true
}

// 试运行（只开面板，真正执行是面板里的「运行」按钮）
const openRunPanel = () => {
  flowRef.value?.toggleRunPanel()
}

// 保存工作流
// 保存工作流。
// 返回值：保存成功 → 工作流 id；校验不过或保存失败 → null。
// 「新建未保存就点运行」要靠这个返回值判断（见 RunPanel 的 onRun），
// 所以不能再是 void —— 否则调用方拿到 undefined，分不清「失败」和「还没到那一步」。
// 当前工作流是否有端点对外开放：保存前提醒用。
// 数据取 instanceProps.endpoints，缺失时回落到列表里同 id 的那一项。
// 注意：端点对外开放**不影响**状态（见 shared/models 的状态注释），所以它不会被状态挡在门外。
const hasExposedEndpoint = computed(() => {
  const props = instanceProps.value?.endpoints
  const eps = Array.isArray(props) && props.length
    ? props
    : (flowsList.value.find(f => String(f.id) === String(currentWFId.value))?.endpoints || [])
  return Array.isArray(eps) && eps.some(e => e && e.exposed)
})

const handleSave = async () => {
  // 编辑期间被别处切成了废弃：后端也会拦，这里提前给一句能纠正的提示
  if (currentWFStatus.value === 3) {
    ElMessage.warning({ message: t('flow.save.deprecatedBeforeSave', { status: statusMeta(1).text }), duration: 3000 })
    return null
  }
  const flowName = (instanceProps.value.name || '').trim()
  if (!flowName) {
    ElMessage.error({
      message: t('flow.save.nameRequired'),
      duration: 3000,
      offset: 20,
      showClose: true
    })
    return null
  }
  // 名称规则：字母或数字开头，后续只允许字母数字横线，≤64。
  // 不是起名洁癖：这个名字就是端点名，也就是 MCP 的 tool.name 与 agent 侧的
  // local__<name>，后者只认 ASCII —— 中文写描述。
  if (!NAME_RE.test(flowName)) {
    ElMessage.error({
      message: t('flow.save.nameInvalid'),
      duration: 5000,
      offset: 20,
      showClose: true
    })
    return null
  }
  instanceProps.value.name = flowName

  // 端点已对外开放：保存会立即影响线上调用方，先提醒一次（不是禁止，确认即可继续）。
  // 改名还要多说一句 —— 对外工具名会跟着变，外部接入方得同步更新。
  if (hasExposedEndpoint.value) {
    const renamed = !!currentWFId.value && !!originalWFName.value && flowName !== originalWFName.value
    try {
      await ElMessageBox.confirm(
        renamed
          ? t('flow.save.renameExposedConfirm', { old: originalWFName.value, new: flowName })
          : t('flow.save.exposedSaveConfirm'),
        renamed ? t('flow.save.renameExposedTitle') : t('flow.save.exposedTitle'),
        { confirmButtonText: t('flow.save.confirmSave'), cancelButtonText: t('common.cancel'), type: 'warning' }
      )
    } catch {
      return null
    }
  }

  try {
    const token = getToken()
    if (!token) {
      ElMessage.error({
        message: t('flow.save.notSignedIn'),
        duration: 5000,
        offset: 20,
        showClose: true
      })
      return null
    }

    const diagramData = flowRef.value?.getDiagramData?.() || { nodes: [], edges: [] }
    const flowArguments = flowRef.value?.getFlowArguments?.() || {}
    const flowLimits = flowRef.value?.getFlowLimits?.() || {}
    const flowVariables = flowRef.value?.getFlowVariables?.() || {}
    const environmentVariables = flowRef.value?.getEnvironmentVariables?.() || {}
    // instanceProps.value.diagram = JSON.stringify(diagramData) // 不更新本地 props，避免触发 watch 刷新界面

    let url, method
    if (currentWFId.value) {
      url = `${adminApi.flows.base}${currentWFId.value}`
      method = 'PUT'
    } else {
      url = adminApi.flows.create
      method = 'POST'
    }

    const response = await fetch(url, {
      method: method,
      headers: {
        'Content-Type': 'application/json',
        'Authorization': `Bearer ${token}`
      },
      body: JSON.stringify({
        name: instanceProps.value.name,
        diagram: JSON.stringify(diagramData),
        description: instanceProps.value.description,
        arguments: flowArguments,
        env_vars: environmentVariables,
        flow_vars: flowVariables,
        timeout: flowLimits.timeout ?? -1,
        max_recursion_depth: flowLimits.max_recursion_depth ?? -1,
        max_node_count: flowLimits.max_node_count ?? -1,
        max_exec_count: flowLimits.max_exec_count ?? -1,
        max_run_records: flowLimits.max_run_records ?? -1,
        max_session_records: flowLimits.max_session_records ?? -1
      })
    })

    if (!response.ok) {
      throw new Error(t('flow.save.saveFailed'))
    }

    const data = await response.json()
    if (data.code === 200) {
      ElMessage.success({
        message: currentWFId.value ? t('flow.save.savedUpdated') : t('flow.save.savedCreated'),
        duration: 700
      })
      // 新建成功后必须把 id 回写进 instanceProps：试运行面板的 flowId 取自它
      // （Flow.vue 里 :flow-id="props.instanceProps?.id"）。只更新 currentWFId 的话，
      // 界面显示「创建成功」，但点运行会报「缺少工作流实例 ID（flowId）」。
      // 回写后 Flow.vue 的 watch(props.instanceProps.id) 会顺带同步调试通道的 flowId 与断点。
      // 更新分支的响应也带 id；两个分支都兜住，返回值才稳定
      const savedId = data.data?.id || currentWFId.value
      if (!currentWFId.value && savedId) {
        currentWFId.value = savedId
      }
      if (savedId && instanceProps.value.id !== savedId) {
        instanceProps.value = { ...instanceProps.value, id: savedId }
      }
      // 保存成功后刷新画布的「未保存修改」基线
      flowRef.value?.snapshotDiagram?.()
      // 名字已落库：后续再改一次时的对比基准要跟着走，否则同一处改动会重复弹窗提示
      originalWFName.value = flowName
      fetchflows()
      // 保存只改工作副本：头部要立刻显示「v3 · 有改动」，提示还没发布
      refreshVersionState()
      return savedId || null
    }
    ElMessage.error({
      message: t('flow.save.saveFailedReason', { msg: formatError(data) }),
      duration: 5000,
      offset: 20,
      showClose: true
    })
    return null
  } catch (error) {
    console.error(t('flow.save.saveFailedLog'), error)
    ElMessage.error({
      message: t('flow.save.saveFailedRetry'),
      duration: 5000,
      offset: 20,
      showClose: true
    })
    return null
  }
}

// 「运行前确保已保存」：新建工作流还没落库时，试运行面板点「运行」会先调它拿 id。
// RunPanel 是 Flow.vue 的子组件，这里的 provide 对整条后代链可见，不必逐层转交。
provide('ensureFlowSaved', handleSave)

// 选择类型
const selectType = (type) => {
  selectedType.value = type
  fetchflows()
}

// 状态切换（两态）：1 正常 / 3 废弃。锁定态已取消（线上由版本管理）。
// 只有切「废弃」会由后端自动收回该工作流的全部端点对外（记录保留）；
// 切回「正常」不会收回端点 —— 端点对外与生命周期是两件事。
const handleSetWFStatus = async (instance, status) => {
  const meta = statusMeta(status)
  if (status === 3) {
    try {
      await ElMessageBox.confirm(
        t('flow.save.deprecateConfirm', { name: instance.name }),
        t('flow.save.confirmTitle'),
        { type: status === 1 ? 'info' : 'warning' }
      )
    } catch {
      return
    }
  }
  try {
    const token = getToken()
    if (!token) {
      ElMessage.error({ message: t('flow.save.notSignedIn'), duration: 5000, offset: 20, showClose: true })
      return
    }
    const response = await fetch(adminApi.flows.status(instance.id), {
      method: 'PUT',
      headers: {
        'Content-Type': 'application/json',
        'Authorization': `Bearer ${token}`
      },
      body: JSON.stringify({ status })
    })
    if (!response.ok) throw new Error(t('flow.save.updateFailed'))
    const data = await response.json()
    if (data.code === 200) {
      instance.status = status
      ElMessage.success({ message: t('flow.save.statusChanged', { status: meta.text }), duration: 700 })
      await fetchflows()
    } else {
      ElMessage.error({ message: t('flow.save.updateFailedReason', { msg: formatError(data) }), duration: 5000, offset: 20, showClose: true })
    }
  } catch (error) {
    console.error(t('flow.save.statusToggleLog'), error)
    ElMessage.error({ message: t('flow.save.updateFailedRetry'), duration: 5000, offset: 20, showClose: true })
  }
}

// 获取工作流实例列表
const fetchflows = async () => {
  isLoading.value = true
  try {
    const token = getToken()
    if (!token) return

    const response = await fetch(adminApi.flows.base, {
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

    if (!response.ok) {
      console.log('fetchflows response status:', response.status, response.statusText)
      console.log('fetchflows response headers:', [...response.headers.entries()])
      return
    }

    try {
      console.log('fetchflows response status:', response.status, response.statusText)
      console.log('fetchflows response headers:', [...response.headers.entries()])

      const responseBody = await response.text()
      console.log('fetchflows response body length:', responseBody.length)
      console.log('fetchflows response body:', responseBody)

      const data = JSON.parse(responseBody)

      if (data.code === 200) {
        flowsList.value = data.data.items || []
      }
    } catch (jsonError) {
      console.error(t('flow.save.parseJsonFailed'), jsonError)
    }
  } catch (error) {
    console.error(t('flow.save.loadListFailed'), error)
  } finally {
    isLoading.value = false
  }
}

// 处理编辑工作流


// 处理删除工作流
const handleDeleteFlow = (instance) => {
  // 弹出确认对话框
  ElMessageBox.confirm(
    t('flow.save.deleteConfirm', { name: instance.name }),
    t('flow.save.deleteConfirmTitle'),
    {
      confirmButtonText: t('common.confirm'),
      cancelButtonText: t('common.cancel'),
      type: 'warning'
    }
  ).then(async () => {
    try {
      const token = getToken()
      if (!token) {
        ElMessage.error({
        message: t('flow.save.notSignedIn'),
        duration: 5000,
        offset: 20,
        showClose: true
      })
        return
      }

      const response = await fetch(`${adminApi.flows.base}/${instance.id}`, {
        method: 'DELETE',
        headers: {
          'Content-Type': 'application/json',
          'Authorization': `Bearer ${token}`
        }
      })

      if (!response.ok) {
        throw new Error(t('flow.save.deleteFailed'))
      }

      const data = await response.json()
      if (data.code === 200) {
        ElMessage.success({
          message: t('flow.save.deleted'),
          duration: 700
        })
        // 刷新工作流列表
        fetchflows()
      } else {
        ElMessage.error({
          message: t('flow.save.deleteFailedReason', { msg: formatError(data) }),
          duration: 5000,
          offset: 20,
          showClose: true
        })
      }
    } catch (error) {
      console.error(t('flow.save.deleteFailedLog'), error)
      ElMessage.error({
        message: t('flow.save.deleteFailedRetry'),
        duration: 5000,
        offset: 20,
        showClose: true
      })
    }
  }).catch(() => {
    // 取消删除操作
  })
}

// 页面加载时获取工作流实例列表
onMounted(() => {
  fetchflows()
})

// 页面激活时获取工作流实例列表
onActivated(() => {
  fetchflows()
})
</script>

<style scoped>
.wf-instances {
  position: relative;
  width: 100%;
  height: 100%;
  display: flex;
  flex-direction: column;
  /* 保持 0：全屏编辑表单（.full-screen-form）是流内元素，若这里加内边距会把它整体缩小。
     列表页的 20px 留白改放在 .wf-instances-list-mode 上，与智能体/技能页视觉一致 */
  padding: 0px;
  background-color: #fafafa;
  overflow: hidden;
}

.wf-instances-list-mode {
  flex: 1;
  min-height: 0;
  display: flex;
  flex-direction: column;
  padding: 20px; /* 列表页四周留白（与 .agents / .agent-skills 的根容器留白等效） */
  overflow: hidden;
}

.full-screen-form {
  width: 100%;
  height: 100%;
  display: flex;
  flex-direction: column;
  background-color: #fafafa;
}

/* 高度锁 64px：与智能体 / 技能编辑页、工作台标签条（.wb-tabbar）同一套头部尺寸 */
.full-screen-header {
  display: flex;
  align-items: center;
  justify-content: flex-start;
  gap: 8px;
  height: 64px;
  box-sizing: border-box;
  padding: 0 20px;
  border-bottom: 1px solid #f0f0f0;
  flex-shrink: 0; /* 锁死 64px：内容超高时不让头部被挤压（与 Agents / Skills 同口径） */
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

.header-name {
  font-size: 14px;
  font-weight: 600;
  color: #333;
}

.header-desc {
  font-size: 12px;
  color: #999;
}

.header-info {
  display: flex;
  flex-direction: column;
  gap: 4px;
  min-width: 0;
  /* 占满按钮区之外的剩余宽度并允许压缩：名称/描述都靠这一层的宽度做单行省略，
     不收起来也不换行 —— 窗口变窄时文字是「越挤越短」，不是整行消失 */
  flex: 1 1 auto;
}

.header-name-wrapper, .header-desc-wrapper {
  cursor: pointer;
  min-width: 0;   /* 原来是 100px：父级变窄时子项会溢出、压到右侧按钮上 */
}

/* 单行省略：截断而不是溢出遮挡 */
.header-name, .header-desc {
  display: block;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.header-name:hover, .header-desc:hover {
  color: #1890ff;
}

.header-name-input {
  width: min(200px, 100%);
}

.header-name-input :deep(.el-input__wrapper) {
  border-radius: 4px;
  box-shadow: 0 0 0 1px #1890ff;
}

/* 描述弹窗里的说明行 */
.wf-desc-hint {
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
  flex-shrink: 0;  /* 窄档下按钮组不被压缩（与 Agents / Skills 同口径） */
}

.dirty-indicator {
  font-size: 11px;
  color: #fff;
  background: #fa8c16;
  padding: 2px 12px;
  border-radius: 999px;
  white-space: nowrap;
}

.header-buttons .el-button {
  border-radius: 20px;
}

.header-action-btn {
  border-radius: 20px;
  height: 36px;
  padding: 0 16px;
}

/* 版本入口：未发布用暖色描边提醒「线上还跑不了」，发布后就是普通按钮 */
.version-btn {
  font-variant-numeric: tabular-nums;
  min-width: 68px;
}

.version-btn--draft {
  color: #d46b08;
  border-color: #ffd591;
  background: #fff7e6;
}

/* 已发布但有未发布的改动：与「未发布」区分——那是有版本只是落后，这是压根没有版本 */
.version-btn--dirty {
  color: #ad6800;
  border-color: #ffe58f;
  background: #fffbe6;
}

/* ===== 版本面板 ===== */
/* 这里的一整套数值都抄自 RunPanel 的 .run-panel：
   同一个位置（右侧贴边）、同样的上下留白、同样的圆角与左侧投影，两个浮层才能互换而不跳动 */
.version-panel {
  position: absolute;
  z-index: 100;
  /* right 留出 .full-screen-content 的 padding-right，右边缘与画布齐平 */
  right: 10px;
  top: 2%;
  bottom: 2%;
  width: 360px;  /* 与 RunPanel 的默认宽度一致 */
  background: #ffffff;
  border-radius: 8px 0 0 8px;
  box-shadow: -4px 0 20px rgba(0, 0, 0, 0.2);
  display: flex;
  flex-direction: column;
  overflow: hidden;
}

.version-panel-header {
  flex-shrink: 0;
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 6px 16px;
  gap: 8px;
}

.version-panel-title {
  flex: 1;
  font-weight: 600;
  font-size: 14px;
  color: #000;
}

.version-panel-close {
  cursor: pointer;
  padding: 4px;
  border-radius: 4px;
  font-size: 16px;
  color: #666;
}

.version-panel-close:hover {
  background: #f0f0f0;
  color: #333;
}

.version-panel-body {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  padding: 4px 16px 16px;
}

.ver-release {
  display: flex;
  gap: 8px;
  align-items: center;
  margin-bottom: 8px;
}

.ver-hint {
  font-size: 12px;
  line-height: 1.6;
  color: #8c8c8c;
  margin-bottom: 16px;
}

.ver-list {
  display: flex;
  flex-direction: column;
  gap: 8px;
  min-height: 80px;
}

.ver-item {
  border: 1px solid #f0f0f0;
  border-radius: 8px;
  padding: 10px 12px;
}

.ver-main {
  display: flex;
  align-items: center;
  gap: 8px;
}

.ver-no {
  font-weight: 600;
  font-variant-numeric: tabular-nums;
  color: #262626;
}

.ver-note {
  font-size: 12px;
  color: #595959;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.ver-meta {
  display: flex;
  align-items: center;
  gap: 12px;
  margin-top: 6px;
  font-size: 12px;
  color: #8c8c8c;
}

/* ===== 窄屏分层降级（档位由 useHeaderCompact 计算） ===== */
.header-buttons {
  flex-shrink: 0;   /* 按钮区不参与压缩，宽度不足时靠档位收起次要操作 */
}
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


/* 窄档：只把变量面板收进「⋯」。试运行是主操作，任何档位都平铺（保存 / 端点同理） */
.header--sm .variables-card {
  display: none;
}
.header--sm .header-more {
  display: inline-flex;
}

.variables-card {
  display: flex;
  align-items: center;
  gap: 4px;
  padding: 4px 12px;
  background-color: #ffffff;
  border-radius: 20px;
}

.variable-icon {
  font-size: 22px;
  color: #666;
  cursor: pointer;
  padding: 4px;
  border-radius: 4px;
  transition: all 0.2s;
}

.variable-icon:hover {
  background-color: #e0e0e0;
  color: #333;
}

.full-screen-content {
  flex: 1;
  min-height: 0;  /* 关键：否则内容超高时撑破容器，把固定的 64px 头部一起挤扁 */
  overflow-y: hidden;
  padding-right: 10px;
  padding-top: 16px;
  /* 版本面板的定位基准：它就是编辑器里 .run-panel 在 .flow 上的那一套绝对定位 */
  position: relative;
}

.wf-instances-header {
  display: flex;
  justify-content: flex-end;
  align-items: center;
  gap: 12px;
  margin-bottom: 20px;
  box-sizing: border-box;
}

/* 状态筛选按钮组：三个按钮紧挨成一组（间距 4px，并清掉 el-button 默认的左间距） */
.status-filters {
  display: flex;
  align-items: center;
  gap: 4px;
}

.status-filters :deep(.el-button + .el-button) {
  margin-left: 0;
}

/* 状态语义色：启用=绿、禁止=红。只作用于按钮内的图标与文字，
   不受按钮自身 hover / active 文字色（#333）影响。 */
.status-filters :deep(.el-button.btn-enable .el-icon),
.status-filters :deep(.el-button.btn-enable span) {
  color: #389e0d;
}

.status-filters :deep(.el-button.btn-disable .el-icon),
.status-filters :deep(.el-button.btn-disable span) {
  color: #f56c6c;
}

.wf-instances-header .el-button {
  border-radius: 10px;
  padding: 6px 16px;
  font-size: 14px;
  background: none;
  border: 2px solid white;
  color: #666;
  transition: all 0.3s ease;
}

.wf-instances-header .el-button:hover {
  background-color: #e6e6e6;
  color: #333;
  border: 2px solid white;
}

.wf-instances-header .el-button.active {
  background-color: #e0e0e0;
  color: #333;
  border: 2px solid white;
}

.wf-instances-header .el-button--primary {
  background-color: #1890ff;
  color: white;
  border: 2px solid white;
}

.wf-instances-header .el-button--primary:hover {
  background-color: #40a9ff;
  border: 2px solid white;
  color: white;
}

.search-box {
  margin-left: auto;
  width: 200px;
}

.search-box :deep(.el-input__wrapper) {
  border-radius: 10px;
  border: 2px solid transparent;
}

.search-box :deep(.el-input__wrapper.is-focus) {
  border-color: transparent;
  box-shadow: none;
}

.search-box :deep(.el-input__wrapper:hover) {
  border-color: transparent;
  box-shadow: none;
}

/* ===== 列表头部窄屏分层降级（档位由 useHeaderCompact 计算） =====
   wide：状态筛选平铺 + 搜索框(200) + 新建；
   md  ：状态筛选平铺 + 搜索框(160) + 新建；
   sm  ：状态筛选合并为下拉，搜索框(160) 仍保持展开；
   xs  ：再把搜索框收成图标，点击图标展开为整行搜索。 */
.status-filter-compact,
.search-toggle {
  display: none;
}

.status-filter-compact .caret {
  font-size: 12px;
  margin-left: 2px;
  opacity: 0.7;
}

/* 中/窄档：搜索框收窄，给筛选按钮组让位 */
.header--md .search-box,
.header--sm .search-box {
  width: 160px;
}

/* 窄档 sm：只把平铺筛选收进下拉，搜索框继续展开 */
.header--sm .status-filters {
  display: none;
}
.header--sm .status-filter-compact {
  display: inline-flex;
}

/* 极窄档 xs：再把搜索框收成图标。
   注意 xs 与 sm 是互斥类名（不会同时挂），筛选那两条规则必须重复声明 */
.header--xs .status-filters {
  display: none;
}
.header--xs .status-filter-compact {
  display: inline-flex;
}
.header--xs .search-box {
  display: none;
}
.header--xs .search-toggle {
  display: inline-flex;
  margin-left: auto; /* 压缩后的筛选下拉靠左，搜索 + 新建贴右 */
  padding: 6px 10px;
}

/* 窄档起「新增工作流」只留 +：文字让位给筛选与搜索。
   padding 要同步收紧 —— 只藏文字不改内边距，46px 的左右留白会让它比带字时还宽。
   「导入」与「新增」同一套收缩规则：都是右上角的创建类动作。 */
.header--sm .header-create-btn span,
.header--xs .header-create-btn span,
.header--sm .header-import-btn span,
.header--xs .header-import-btn span {
  display: none;
}
.header--sm .header-create-btn,
.header--xs .header-create-btn,
.header--sm .header-import-btn,
.header--xs .header-import-btn {
  padding: 6px 10px;
}

/* 搜索展开态：整行让给搜索框，其余操作临时收起 */
.header--search-open .status-filter-compact,
.header--search-open .header-create-btn,
.header--search-open .header-import-btn {
  display: none;
}
.header--search-open .search-box {
  display: block;
  flex: 1 1 auto;
  width: auto;
  margin-left: 0;
}
.header--search-open .search-toggle {
  display: inline-flex;
  margin-left: 8px;
}

.wf-instances-content {
  flex: 1;
  min-height: 0;
  background-color: transparent;
  padding: 0px;
  overflow-y: auto;
}

.loading, .no-data {
  text-align: center;
  padding: 40px;
  color: #999;
  font-size: 14px;
}

/* 工作流实例列表：与技能卡片同口径（200 这一档能让 1200px 内容区排下 5 列） */
.wf-instances-list {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(200px, 1fr));
  gap: 14px;
  align-content: start;
}

.wf-instance-card {
  position: relative;
  cursor: pointer; /* 整卡可点：点击任意位置进入编辑 */
  background-color: white;
  border: 1px solid #e8e8e8;
  border-radius: 12px;
  padding: 16px;
  box-shadow: 0 2px 8px rgba(0, 0, 0, 0.1);
  width: 100%;
  min-width: 200px;
  max-width: 250px;
  transition: all 0.3s ease;
}

.wf-instance-card:hover {
  box-shadow: 0 4px 16px rgba(0, 0, 0, 0.15);
}

.card-header {
  position: relative;
  display: flex;
  align-items: center;
  margin-bottom: 10px;
  padding-bottom: 8px;
  border-bottom: 1px solid #f0f0f0;
}

/* 名字 + 状态：头部横向空间全给它 */
.header-content {
  flex: 1;
  min-width: 0; /* 允许收缩，长名字才会正确出省略号而不是把卡片撑破 */
  overflow: hidden;
}

/* 废弃（status=3）名字标红：既不可改也不可运行（锁定态已取消） */

.header-content .card-title.title-disabled {
  color: #f56c6c;
}

.header-content .card-title {
  display: flex;
  align-items: center;
  gap: 4px;
  min-width: 0;
  font-size: 15px;
  font-weight: 600;
  color: #333;
}

/* 省略号要落在文字上：title 本体成了 flex 容器，自身 ellipsis 对子项无效 */
.card-title-text {
  min-width: 0;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.header-content .card-description {
  font-size: 12px;
  color: #909399; /* 与技能卡片的次要文字同色 */
  margin: 4px 0 0 0;
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
}

/* 第二行的状态标记：压到 14px 高、10px 字的胶囊（与技能卡片一致） */
.card-description :deep(.el-tag) {
  height: 14px;
  padding: 0 6px;
  font-size: 10px;
  line-height: 12px;
  border-radius: 999px;
}

/* 版本胶囊：等宽数字，v1 → v12 不会左右跳；颜色沿用 plain 的次要灰 */
.card-description :deep(.tag-version) {
  font-variant-numeric: tabular-nums;
}

/* 卡片操作浮层：hover 时浮在卡片体上（inset:0 相对 .card-body），按钮居中。
   不占布局空间 —— 名字与描述保持原样；背景透明，靠按钮自身的浅色底立住。 */
.card-actions {
  position: absolute;
  inset: 0;
  z-index: 2;
  display: flex;
  align-items: center;
  justify-content: center;
  /* 200px 窄卡的正文只有 ~168px：5 个按钮压到 24px + 4px 间距才排得下 */
  gap: 4px;
  border-radius: 8px;
  background: transparent;
  opacity: 0;
  pointer-events: none; /* 隐藏时不参与命中测试，否则会挡住整卡的点击 */
  transition: opacity 0.2s ease;
}

.wf-instance-card:hover .card-actions {
  opacity: 1;
  pointer-events: auto;
}

.card-action-btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 24px;
  height: 24px;
  /* 浅色胶囊：底色不透明才挡得住背后的描述文字，hover 再填实 */
  border: 1px solid #a0cfff;
  border-radius: 6px;
  background: #ecf5ff;
  color: #409eff;
  cursor: pointer;
  transition: all 0.2s;
}

.card-action-btn .el-icon {
  font-size: 14px !important;
  color: inherit;
}

.card-action-btn:hover {
  background: #409eff;
  border-color: #409eff;
  color: #fff;
}

/* 删除是破坏性动作：淡红底 */
.card-action-btn.danger {
  background: #fef0f0;
  border-color: #fab6b6;
  color: #f56c6c;
}

.card-action-btn.danger:hover {
  background: #f56c6c;
  border-color: #f56c6c;
  color: #fff;
}

.card-body {
  display: flex;
  flex-direction: column;
  gap: 12px;
  /* 操作浮层的定位基准；不再左右缩进，与技能卡片的正文对齐 */
  position: relative;
}

.card-body-bottom {
  display: flex;
  flex-direction: column;
  gap: 6px; /* 与技能卡片一致 */
}

/* 信息行：字号与灰色统一照技能卡片（12px / #909399） */
.info-item {
  display: flex;
  align-items: baseline;
  gap: 4px;
  font-size: 12px;
  color: #909399;
}

.info-label {
  font-size: 12px;
  color: #909399;
  font-weight: 500;
  flex-shrink: 0;
}

.info-value {
  font-size: 12px;
  color: #909399;
  word-break: break-all;
}

.description-value {
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow: hidden;
  text-overflow: ellipsis;
  line-height: 1.5;
  max-height: 3em;
  flex: 1;
  min-width: 0;
}

/* ===== 主列表滚动条：位置常驻、悬浮才上色 =====
   不悬浮时拇指透明（看不见），鼠标进入列表区域才显示 6px 灰色拇指；
   槽位常驻 6px，条的显隐不会把内容挤动。约定见 platform/README.md，实现同 Chat.vue。 */
.wf-instances-content {
  scrollbar-gutter: stable;
  scrollbar-width: thin;
  scrollbar-color: transparent transparent;
}

.wf-instances-content::-webkit-scrollbar {
  width: 6px;
  height: 6px;
}

.wf-instances-content::-webkit-scrollbar-track {
  background: transparent;
}

.wf-instances-content::-webkit-scrollbar-thumb {
  background: transparent;
  border-radius: 3px;
}

.wf-instances-content:hover {
  scrollbar-color: #c1c1c1 transparent;
}

.wf-instances-content:hover::-webkit-scrollbar-thumb {
  background: #c1c1c1;
}
</style>
