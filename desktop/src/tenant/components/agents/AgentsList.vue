<template>
  <!-- 列表模式：筛选 / 搜索 / 分组卡片。数据由父组件给，动作原样抛回去 -->
  <!-- 智能体管理头部 -->
  <div
    ref="listHeaderRef"
    class="agents-header"
    :class="['header--' + listHeaderSize, { 'header--search-open': listSearchOpen }]"
  >
    <!-- 状态筛选：全部 / 正常 / 废弃（三按钮紧挨成组） -->
    <div class="status-filters">
      <el-button :class="{ active: statusFilter === 'all' }" @click="setStatusFilter('all')">
        <el-icon><Refresh /></el-icon>
        <span>{{ $t('agent.filterAll') }}{{ statusCounts.all ? ` (${statusCounts.all})` : '' }}</span>
      </el-button>
      <el-button class="btn-enable" :class="{ active: statusFilter === 'normal' }" @click="setStatusFilter('normal')">
        <el-icon><CircleCheck /></el-icon>
        <span>{{ $t('agent.filterNormal') }}{{ statusCounts.normal ? ` (${statusCounts.normal})` : '' }}</span>
      </el-button>
      <el-button class="btn-disable" :class="{ active: statusFilter === 'deprecated' }" @click="setStatusFilter('deprecated')">
        <el-icon><CircleClose /></el-icon>
        <span>{{ $t('agent.filterDeprecated') }}{{ statusCounts.deprecated ? ` (${statusCounts.deprecated})` : '' }}</span>
      </el-button>
    </div>
    <!-- 窄档：状态筛选合并成一个下拉 -->
    <el-dropdown class="status-filter-compact" trigger="click" @command="setStatusFilter">
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
    <!-- 显示/隐藏系统内置：与状态筛选正交（那是"什么状态"，这是"谁给的"），所以独立成开关。
         刻意用 el-switch 而不是按钮 —— 按钮的"关"态只能靠颜色/边框暗示，实测会被看成空框；
         开关是二值控件，与本页编辑模式里「允许发现未绑定的技能」那一排同一套。
         偏好存 localStorage，见 showSystem -->
    <span class="system-toggle" :title="$t('agent.systemTip')">
      <span class="system-toggle-label">{{ $t('agent.system') }}</span>
      <el-switch v-model="showSystem" size="small" />
    </span>
    <!-- 搜索框 -->
    <div class="search-box">
      <el-input
        ref="listSearchInputRef"
        v-model="searchQuery"
        :placeholder="$t('agent.searchPlaceholder')"
        prefix-icon="Search"
        clearable
        @keydown.esc="closeListSearch"
      />
    </div>
    <!-- 窄档：搜索收成图标，点击展开为整行搜索 -->
    <el-button
      class="search-toggle"
      :class="{ active: !!searchQuery }"
      :title="$t('agent.search')"
      @click="toggleListSearch"
    >
      <el-icon><component :is="listSearchOpen ? Close : Search" /></el-icon>
    </el-button>
    <!-- 页面级帮助：列表 / 编辑区 / 设置面板的分工（内容见 src/help/zh-CN/page-agents.md） -->
    <HelpButton topic="page-agents" />
    <!-- 导入：从资产包装一个智能体。次要动作，不抢「新增」的主色 -->
    <el-button class="header-import-btn" @click="emit('import')" round :title="$t('agent.importTip')">
      <el-icon><Upload /></el-icon>
      <span>{{ $t('agent.import') }}</span>
    </el-button>
    <!-- 新增智能体按钮（窄档 sm/xs 只留 +，文字由 CSS 收起；title 兜住语义） -->
    <el-button class="header-create-btn" type="primary" @click="emit('create')" round :title="$t('agent.createTip')">
      <el-icon><Plus /></el-icon>
      <span>{{ $t('agent.create') }}</span>
    </el-button>
  </div>

  
  <div class="agents-content">
    <div v-if="loading" class="loading">{{ $t('agent.loading') }}</div>
    <!-- 空态判据用 agentGroups 而不是 filteredAgents：关掉「系统」开关后可能一个都不剩，
         那时 filteredAgents 仍有内容、列表却是空的 -->
    <div v-else-if="agentGroups.length === 0" class="no-data">
      {{ emptyText }}
    </div>
    <div v-else class="agents-list">
      <!-- 分组置顶（与技能列表同口径）：系统内置单独成段固定在最前 —— main 是根 agent，
           混在 A-Z 里会被埋掉。筛选只过滤内容、组头计数跟着结果走；只有一组时不画组头 -->
      <template v-for="g in agentGroups" :key="g.key">
      <div v-if="g.headed" class="agent-group-head">
        <span class="agent-group-title">{{ g.label }}</span>
        <span class="agent-group-count">{{ g.items.length }}</span>
      </div>
      <!-- 悬停提示：鼠标进入整张卡片即显示。
           鼠标压到卡片体上的操作浮层时关掉它（见 actionsHovered），避免与按钮自己的 tip 打架 -->
      <el-tooltip v-for="instance in g.items" :key="instance.id"
                  :content="cardHoverTip(instance)" placement="top" :disabled="actionsHovered">
        <div class="agent-card" @click="emit('open', instance)"
             @mousemove="onCardMove" @mouseleave="actionsHovered = false">
        <div class="card-header">
          <div class="header-content">
            <div class="card-title" :class="titleClass(instance.status)">
              <span class="card-title-text">{{ instance.name }}</span>
              <!-- 超级权限身份（agents.is_supervisor，可授予多个）：盾牌 + 说明。
                   与「系统内置」分开 —— 后者由分组头表达，前者是权限，挤进状态行会与状态色撞。
                   盾牌必须带 tooltip：图标本身不自解释，而它代表的是能删掉别的 agent 的能力。 -->
              <el-tooltip v-if="instance.is_supervisor" placement="top" :show-after="150"
                          popper-class="supervisor-popper"
                          :content="$t('agent.supervisorTip')">
                <span class="badge-supervisor">
                  <svg class="icon-shield" viewBox="0 0 24 24" fill="none" stroke="currentColor"
                       stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round">
                    <path d="M12 2.5 4.5 5.6v5.9c0 4.5 3 8.3 7.5 10 4.5-1.7 7.5-5.5 7.5-10V5.6L12 2.5Z" />
                    <path d="m8.9 11.9 2.2 2.2 4-4" />
                  </svg>
                </span>
              </el-tooltip>
            </div>
            <div class="card-description">
              <!-- 「系统内置」不再单独出标：分组头（见 agentGroups）已把它与普通 agent 分开，
                   卡片上只保留 hover 说明（cardHoverTip），删除按钮照旧不渲染。 -->
              <el-tag size="small" effect="dark" :type="statusMeta(instance.status).tag">
                {{ statusMeta(instance.status).text }}
              </el-tag>
              <!-- 当前发布版本：0 表示还没发布（正式调用不可用，只能调试）。
                   用原生 title 而不是 el-tooltip —— 整卡已有一个 tooltip，嵌套 popper 会打架 -->
              <el-tag size="small" effect="plain" type="info" class="tag-version"
                      :title="instance.current_version ? $t('agent.versionCurrent', { version: instance.current_version }) : $t('agent.versionNoneTip')">
                {{ instance.current_version ? `v${instance.current_version}` : $t('agent.versionUnreleased') }}
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
              <span v-else>{{ $t('agent.noEndpoint') }}</span>
            </div>
          </div>
        </div>
        <div class="card-body">
          <div class="card-body-bottom">
            <div class="info-item">
              <span class="info-label">{{ $t('agent.labelDescription') }}</span>
              <span class="info-value description-value">{{ instance.description || $t('agent.descEmptyShort') }}</span>
            </div>
            <div class="info-item">
              <span class="info-label">{{ $t('agent.labelUpdated') }}</span>
              <span class="info-value">{{ instance.updated_by_name || $t('agent.unknown') }} · {{ formatTime(instance.updated_at) }}</span>
            </div>
          </div>
          <!-- 操作浮层：只覆盖「卡片头下方」那块（card-body），按钮居中（与技能卡片一致） -->
          <div class="card-actions" @click.stop>
            <!-- 两态：正常(1) / 废弃(3) -->
            <el-tooltip v-if="instance.status !== 1" :content="$t('agent.actRestore')" placement="top">
              <button class="card-action-btn" @click="emit('status', { instance, status: 1 })">
                <el-icon><CircleCheck /></el-icon>
              </button>
            </el-tooltip>
            <el-tooltip v-if="instance.status !== 3" :content="$t('agent.actDeprecate')" placement="top">
              <button class="card-action-btn" @click="emit('status', { instance, status: 3 })">
                <el-icon><CircleClose /></el-icon>
              </button>
            </el-tooltip>
            <el-tooltip :content="$t('agent.actEndpoints')" placement="top">
              <button class="card-action-btn" @click="emit('expose', instance)">
                <el-icon><Connection /></el-icon>
              </button>
            </el-tooltip>
            <el-tooltip :content="$t('agent.actExport')" placement="top">
              <button class="card-action-btn" @click="emit('export', instance)">
                <el-icon><Download /></el-icon>
              </button>
            </el-tooltip>
            <el-tooltip v-if="!instance.protected" :content="$t('agent.actDelete')" placement="top">
              <button class="card-action-btn danger" @click="emit('delete', instance)">
                <el-icon><Delete /></el-icon>
              </button>
            </el-tooltip>
          </div>
        </div>
        </div>
      </el-tooltip>
      </template>
    </div>
  </div>
</template>

<script setup>
import { ref, computed, watch, nextTick } from 'vue'
import { useI18n } from 'vue-i18n'
import { Plus, Search, Delete, CircleCheck, CircleClose, Connection, ArrowDown, Close, Refresh, Upload, Download } from '@element-plus/icons-vue'
import { useHeaderCompact } from '../../utils/useHeaderCompact.js'
import HelpButton from '../../../help/HelpButton.vue'

// 脚本侧文案（状态元信息、悬停提示、分组名、空态）要在切换语言后重算，
// 所以它们都得走 t()，不能再用模块级常量存中文字符串。
const { t } = useI18n()

const props = defineProps({
  agents: { type: Array, default: () => [] },
  loading: { type: Boolean, default: false }
})
// 列表只管展示与筛选；进入编辑 / 新建 / 导入 / 导出 / 端点 / 删除 / 改状态 / 刷新都要回给父组件（数据在那边）
const emit = defineEmits(['open', 'create', 'import', 'export', 'expose', 'delete', 'status', 'refresh'])

// 搜索关键词
const searchQuery = ref('')

// 两态状态（与后端 shared/models.StatusXxx 一一对应）：
//   1 正常：可修改、可运行（新建默认）
//   3 废弃：不可修改、不可运行（只由手工设置）
// text 在调用时才取 t：这张表在模块初始化时就建好了，把中文写死进去，
// 切换语言后卡片上的状态标签不会跟着变。
const STATUS_TAGS = { 1: 'success', 3: 'danger' }
const statusMeta = (status) => {
  const s = STATUS_TAGS[status] ? status : 1
  return { text: s === 3 ? t('agent.statusDeprecated') : t('agent.statusNormal'), tag: STATUS_TAGS[s] }
}

// 卡片名样式：废弃=红（正常态不加类）
const titleClass = (status) => (status === 3 ? 'title-disabled' : '')

// 卡片悬停提示（参考工作流）：废弃时提示"查看"
const cardHoverTip = (instance) => {
  const st = instance.status || 1
  // 两个判据语义正交，可能同时成立：main 既是系统内置（受保护名单），也是 supervisor 之一
  const head = []
  if (instance.protected) head.push(t('agent.hoverProtected'))
  if (instance.is_supervisor) head.push(t('agent.hoverSupervisor'))
  const tail = st === 3 ? t('agent.hoverViewDeprecated') : t('agent.hoverEdit')
  return head.length ? `${head.join(' · ')} · ${tail}` : tail
}

// 鼠标是否正压在卡片体上的操作浮层、或标题行的盾牌上：压上去时把整卡 tip 关掉
// （el-tooltip 的 disabled 会立刻关闭已显示的 tip），否则外层 tip 会和内层 tip 一起冒出来。
// 用 mousemove + closest 判定：指针从卡片外直接进到按钮上时 mouseenter 不一定触发，
// 而卡片整体带 @mousemove，一定会经过。
const actionsHovered = ref(false)
const onCardMove = (e) => {
  actionsHovered.value = !!e.target?.closest?.('.card-actions, .badge-supervisor')
}

// 状态筛选：all 全部 | normal 正常(1) | deprecated 废弃(3)
const statusFilter = ref('all')

// 是否显示系统内置（受保护名单）：纯视图偏好，存 localStorage ——
// 否则每次进页面都要重新点一次。默认显示。
const showSystem = ref(localStorage.getItem('agents.show-system') !== '0')
watch(showSystem, (v) => localStorage.setItem('agents.show-system', v ? '1' : '0'))

// 状态数量统计（为 0 时模板不渲染数字）
// 跟随 showSystem：藏起来的不该还算进计数，否则「全部 (5)」下面只有 4 张卡
const statusCounts = computed(() => {
  const list = showSystem.value ? props.agents : props.agents.filter(a => !a.protected)
  return {
    all: list.length,
    normal: list.filter(instance => (instance.status || 1) === 1).length,
    deprecated: list.filter(instance => instance.status === 3).length
  }
})

// 过滤后的智能体列表（先按状态筛选，再按关键词）
const filteredAgents = computed(() => {
  let list = props.agents
  if (statusFilter.value === 'normal') {
    list = list.filter(instance => (instance.status || 1) === 1)
  } else if (statusFilter.value === 'deprecated') {
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

// 分组置顶（与技能列表同口径）：系统内置（受保护名单，当前只有 main）单独成段固定在最前。
// 这一组的价值不在数量 —— main 是根 agent，混在 A-Z 里会被埋掉，它本来就该在入口位置。
// 组是筛完之后的结果切分；空组不产出，只有一组时不画组头（那本来就是一个扁平列表）。
const agentGroups = computed(() => {
  const list = filteredAgents.value
  // 关掉「系统」开关时整组不产出，而不是渲染后再隐藏 —— 组头计数也就自然消失
  const sys = showSystem.value ? list.filter(a => a.protected) : []
  const rest = list.filter(a => !a.protected)
  const groups = []
  if (sys.length) groups.push({ key: 'system', label: t('agent.groupSystem'), items: sys })
  if (rest.length) groups.push({ key: 'other', label: t('agent.groupOther'), items: rest })
  // 卡片上已不再单独标「系统」，组头是它唯一的身份出口 ——
  // 只剩这一组时（租户只有 main、或筛选后只剩它）也必须画，否则标识全无
  const headed = groups.length > 1 || groups[0]?.key === 'system'
  return groups.map(g => ({ ...g, headed }))
})

// 空列表提示文案（区分「搜索无结果」与「该状态下暂无数据」）
const emptyText = computed(() => {
  if (searchQuery.value) {
    return t('agent.emptySearch')
  }
  // 系统内置被开关藏起来了：说清为什么空，而不是笼统报「暂无智能体」
  if (!showSystem.value && props.agents.some(a => a.protected)) {
    return t('agent.emptyHidden')
  }
  if (statusFilter.value === 'deprecated') {
    return t('agent.emptyDeprecated')
  }
  return t('agent.emptyNone')
})

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
// label 走 computed：模块级数组里写死中文，切换语言后紧凑下拉不会变
const STATUS_FILTERS = computed(() => [
  { key: 'all', label: t('agent.filterAll'), icon: Refresh, cls: '' },
  { key: 'normal', label: t('agent.filterNormal'), icon: CircleCheck, cls: 'btn-enable' },
  { key: 'deprecated', label: t('agent.filterDeprecated'), icon: CircleClose, cls: 'btn-disable' }
])
const activeStatus = computed(() => {
  const opts = STATUS_FILTERS.value
  return opts.find(o => o.key === statusFilter.value) || opts[0]
})
const activeStatusLabel = computed(() => {
  const o = activeStatus.value
  const n = statusCounts.value[o.key]
  return o.label + (n ? ` (${n})` : '')
})

// 切换状态筛选（「全部」同时承担刷新）
const setStatusFilter = (filter) => {
  statusFilter.value = filter
  emit('refresh')
}

// 格式化时间
const formatTime = (time) => {
  if (!time) return '-'
  const date = new Date(time)
  if (isNaN(date.getTime())) return '-'
  const year = date.getFullYear()
  const month = String(date.getMonth() + 1).padStart(2, '0')
  const day = String(date.getDate()).padStart(2, '0')
  const hours = String(date.getHours()).padStart(2, '0')
  const minutes = String(date.getMinutes()).padStart(2, '0')
  return `${year}/${month}/${day} ${hours}:${minutes}`
}

// 协议颜色映射
const getProtocolColor = (protocol) => {
  const map = { mcp: '#52c41a', rpc: '#1890ff', llm: '#fa8c16' }
  return map[protocol] || '#909399'
}

// 端点文字颜色：对外开放按协议配色，仅内部灰色
const getEndpointTextColor = (p) => {
  if (!p.exposed) return '#909399'
  return getProtocolColor(p.protocol)
}
</script>

<style scoped>
/* 智能体管理头部 */
.agents-header {
  display: flex;
  justify-content: flex-end;
  align-items: center;
  gap: 12px;
  margin-bottom: 20px;
  padding: 0 0 0px 0;
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

.agents-header .el-button {
  border-radius: 10px;
  padding: 6px 16px;
  font-size: 14px;
  background: none;
  border: 2px solid white;
  color: #666;
  transition: all 0.3s ease;
}

.agents-header .el-button:hover {
  background-color: #e6e6e6;
  color: #333;
  border: 2px solid white;
}

.agents-header .el-button.active {
  background-color: #e0e0e0;
  color: #333;
  border: 2px solid white;
}

/* 「系统」开关：用 el-switch 表达二值（与本页编辑区的开关同源）。
   之前做成按钮，关态只能靠颜色 + 虚线边框暗示，实测直接被看成"一个空框、图标都没了"——
   按钮的视觉语言本来不承载开关语义，换回开关就一步到位。 */
.system-toggle {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 0 2px;
  font-size: 13px;
  color: #666;
  white-space: nowrap;
}

.system-toggle-label {
  user-select: none;
}

/* 新增按钮样式 */
.agents-header .el-button--primary {
  background-color: #1890ff;
  color: white;
  border: 2px solid white;
}

.agents-header .el-button--primary:hover {
  background-color: #40a9ff;
  border: 2px solid white;
  color: white;
}

/* 搜索框样式 */
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

/* 窄档起「新增智能体」只留 +：文字让位给筛选与搜索。
   padding 要同步收紧 —— 只藏文字不改内边距，46px 的左右留白会让它比带字时还宽。
   「导入」与「新增」同一套收缩规则：两个都是右上角的创建类动作。 */
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

/* 搜索展开态：整行让给搜索框，其余操作临时收起（含「系统」开关） */
.header--search-open .status-filter-compact,
.header--search-open .system-toggle,
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

.agents-content {
  flex: 1;
  min-height: 0;
  background-color: transparent;
  padding: 0px;
  overflow-y: auto;
}

.loading {
  text-align: center;
  padding: 40px;
  color: #999;
  font-size: 14px;
}

.no-data {
  text-align: center;
  padding: 40px;
  color: #999;
  font-size: 14px;
}

/* 智能体实例列表：与技能卡片同口径（200 这一档能让 1200px 内容区排下 5 列） */
.agents-list {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(200px, 1fr));
  gap: 14px;
  align-content: start;
}

/* 分组的段标题：跨满整行（列表本身是 grid），把「系统内置 / 其他智能体」切成两段。
   只有两组同时存在时才渲染（见 agentGroups 的 headed）——与技能列表同款。 */
.agent-group-head {
  grid-column: 1 / -1;
  display: flex;
  align-items: center;
  gap: 6px;
  padding-bottom: 6px;
  border-bottom: 1px solid #ebeef5;
}

.agent-group-title {
  font-size: 12px;
  font-weight: 600;
  color: #606266;
}

.agent-group-count {
  font-size: 11px;
  color: #909399;
}

/* 智能体实例卡片 */
.agent-card {
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

.agent-card:hover {
  box-shadow: 0 4px 16px rgba(0, 0, 0, 0.15);
}

/* 卡片头部 */
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

/* 卡片操作浮层：hover 时浮在卡片体上（inset:0 相对 .card-body），按钮居中。
   不占布局空间 —— 名字与描述保持原样；背景透明，靠按钮自身的浅色底立住。 */
.card-actions {
  position: absolute;
  inset: 0;
  z-index: 2;
  display: flex;
  align-items: center;
  justify-content: center;
  /* 200px 窄卡的正文只有 ~168px：6 个按钮必须压到 24px + 4px 间距才排得下 */
  gap: 4px;
  border-radius: 8px;
  background: transparent;
  opacity: 0;
  pointer-events: none; /* 隐藏时不参与命中测试，否则会挡住整卡的点击 */
  transition: opacity 0.2s ease;
}

.agent-card:hover .card-actions {
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

/* 废弃（status=3）名字标红：既不可改也不可运行 */
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
  display: flex;
  align-items: center;
  gap: 4px;
  flex-wrap: wrap;
  font-size: 12px;
  color: #909399; /* 与技能卡片的次要文字同色 */
  margin: 4px 0 0 0;
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

/* 超级权限盾牌：权限身份，放在名字后而不是状态行 ——
   状态行已被绿=正常 / 红=废弃占满，权限挤进去会与状态色撞。
   color 必须写死，不能靠继承：.card-title.title-disabled 会把 color 染成红色，
   继承下来会把这枚"权限"图标误读成"废弃"标记。

   视觉：紫色纯图标。不加底色块 —— 卡片里已经全是胶囊和色块，
   再叠一个实底方块反而显脏；靠「紫 + 16px + 2.4 线宽」取得分量。
   选紫而不选红/黄：状态色已占满绿=正常、红=废弃，紫是唯一
   既语义空闲、对比度又够（这里取亮紫 #8b5cf6，白底约 4.1:1；再亮就掉到 3:1 以下）。 */
.header-content .badge-supervisor {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  flex: none;               /* 名字过长时可收缩的是文字，盾牌不许被压掉 */
  margin-left: 2px;
  color: #8b5cf6;
  cursor: help;
  transition: color .15s ease, transform .15s ease;
}

.header-content .badge-supervisor:hover {
  color: #7c3aed;
  transform: scale(1.1);
}

.badge-supervisor .icon-shield {
  display: block;
  width: 16px;
  height: 16px;
}

/* 盾牌说明文案较长，限制宽度免得 popper 被撑成一条横线 */
:global(.supervisor-popper) {
  max-width: 280px;
  line-height: 1.5;
}

/* 端点协议标签：颜色由 getEndpointTextColor 动态给（对外开放=协议色，仅内部=灰） */
.endpoint-tag {
  font-size: 10px;
  font-weight: 600;
  white-space: nowrap;
}

/* 卡片内容 */
.card-body {
  display: flex;
  flex-direction: column;
  gap: 12px;
  /* 操作浮层的定位基准；不再左右缩进，与技能卡片的正文对齐 */
  position: relative;
}

/* 浮动运行按钮 */
.run-button-float {
  position: absolute;
  bottom: 8px;
  right: 8px;
  z-index: 10;
}

.card-body-top {
  display: flex;
  justify-content: space-between;
  gap: 12px;
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

/* 响应式调整 */
@media (max-width: 768px) {
  .agents-list {
    grid-template-columns: 1fr;
  }
  
  .card-body-top {
    flex-direction: column;
    gap: 8px;
  }
}


/* ===== 主列表滚动条：位置常驻、悬浮才上色 =====
   不悬浮时拇指透明（看不见），鼠标进入列表区域才显示 6px 灰色拇指；
   槽位常驻 6px，条的显隐不会把内容挤动。约定见 platform/README.md，实现同 Chat.vue。 */
.agents-content {
  scrollbar-gutter: stable;
  scrollbar-width: thin;
  scrollbar-color: transparent transparent;
}

.agents-content::-webkit-scrollbar {
  width: 6px;
  height: 6px;
}

.agents-content::-webkit-scrollbar-track {
  background: transparent;
}

.agents-content::-webkit-scrollbar-thumb {
  background: transparent;
  border-radius: 3px;
}

.agents-content:hover {
  scrollbar-color: #c1c1c1 transparent;
}

.agents-content:hover::-webkit-scrollbar-thumb {
  background: #c1c1c1;
}
</style>
