<template>
  <!-- 列表模式：筛选 / 搜索 / 分组卡片。数据由父组件给，动作原样抛回去 -->
  <!-- 头部工具栏 -->
  <div
    ref="listHeaderRef"
    class="agent-skills-header"
    :class="['header--' + listHeaderSize, { 'header--search-open': listSearchOpen }]"
  >
    <!-- 状态筛选：全部 / 正常 / 废弃（三按钮紧挨成组） -->
    <div class="status-filters">
      <el-button :class="{ active: statusFilter === 'all' }" @click="setStatusFilter('all')">
        <el-icon><Refresh /></el-icon>
        <span>{{ $t('skill.filterAll') }}{{ statusCounts.all ? ` (${statusCounts.all})` : '' }}</span>
      </el-button>
      <el-button class="btn-enable" :class="{ active: statusFilter === 'normal' }" @click="setStatusFilter('normal')">
        <el-icon><CircleCheck /></el-icon>
        <span>{{ $t('skill.filterNormal') }}{{ statusCounts.normal ? ` (${statusCounts.normal})` : '' }}</span>
      </el-button>
      <el-button class="btn-disable" :class="{ active: statusFilter === 'deprecated' }" @click="setStatusFilter('deprecated')">
        <el-icon><CircleClose /></el-icon>
        <span>{{ $t('skill.filterDeprecated') }}{{ statusCounts.deprecated ? ` (${statusCounts.deprecated})` : '' }}</span>
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
            <span>{{ statusLabel(opt.key) }}{{ statusCounts[opt.key] ? ` (${statusCounts[opt.key]})` : '' }}</span>
          </el-dropdown-item>
        </el-dropdown-menu>
      </template>
    </el-dropdown>
    <!-- 显示/隐藏系统内置：与状态筛选正交（那是"什么状态"，这是"谁给的"），所以独立成开关。
         刻意用 el-switch 而不是按钮 —— 按钮的"关"态只能靠颜色/边框暗示，实测会被看成空框；
         开关是二值控件，与智能体编辑里「允许发现未绑定的技能」那一排同一套。
         偏好存 localStorage，见 showSystem -->
    <span class="system-toggle" :title="$t('skill.systemToggleTip')">
      <span class="system-toggle-label">{{ $t('skill.systemLabel') }}</span>
      <el-switch v-model="showSystem" size="small" />
    </span>
    <div class="search-box">
      <el-input
        ref="listSearchInputRef"
        v-model="keyword"
        :placeholder="$t('skill.searchPlaceholder')"
        prefix-icon="Search"
        clearable
        @keyup.enter="emit('refresh')"
        @keydown.esc="closeListSearch"
      />
    </div>
    <!-- 窄档：搜索收成图标，点击展开为整行搜索 -->
    <el-button
      class="search-toggle"
      :class="{ active: !!keyword }"
      :title="$t('skill.searchTip')"
      @click="toggleListSearch"
    >
      <el-icon><component :is="listSearchOpen ? Close : Search" /></el-icon>
    </el-button>
    <!-- 导入：从资产包装一个技能。次要动作，不抢「新建」的主色 -->
    <el-button class="header-import-btn" @click="emit('import')" round :title="$t('skill.importTip')">
      <el-icon><Upload /></el-icon>
      <span>{{ $t('skill.import') }}</span>
    </el-button>
    <!-- 窄档（sm/xs）只留 +，文字由 CSS 收起；title 兜住"这是干嘛的" -->
    <el-button class="header-create-btn" type="primary" @click="emit('create')" round :title="$t('skill.createTip')">
      <el-icon><Plus /></el-icon>
      <span>{{ $t('skill.create') }}</span>
    </el-button>
  </div>

  <!-- 卡片列表 -->
  <div class="agent-skills-content">
    <div v-if="loading" class="loading">{{ $t('common.loading') }}</div>
    <!-- 空态判据用 skillGroups 而不是 filteredSkills：关掉「系统」开关后可能一个都不剩，
         那时 filteredSkills 仍有内容、列表却是空的 -->
    <div v-else-if="skillGroups.length === 0" class="no-data">{{ emptyText }}</div>
    <div v-else class="agent-skills-list">
      <template v-for="g in skillGroups" :key="g.key">
      <!-- 组头：系统内置固定在最前（grid-column 跨满整行），筛选只过滤内容、计数跟着结果走。
           只有两组同时存在时才画 —— 单一分组时组头纯属噪声，见 skillGroups -->
      <div v-if="g.headed" class="skill-group-head">
        <span class="skill-group-title">{{ g.label }}</span>
        <span class="skill-group-count">{{ g.items.length }}</span>
      </div>
      <!-- 悬停提示：鼠标进入整张卡片即显示。
           鼠标压到卡片底部操作条上时关掉它（见 actionsHovered），避免与按钮自己的 tip 打架 -->
      <el-tooltip v-for="skill in g.items" :key="skill.id"
                  :content="cardHoverTip(skill)" placement="top" :disabled="actionsHovered">
        <div class="agent-skill-card" @click="emit('open', skill)"
             @mousemove="onCardMove" @mouseleave="actionsHovered = false">
        <div class="card-header">
          <div class="header-content">
            <div class="card-title" :class="titleClass(skill.status)">
              <span class="card-title-text">{{ skill.name }}</span>
            </div>
            <div class="card-description">
              <!-- 系统内置（shared/models 的受保护名单）：不可删除/改名，内容与描述仍可编辑。
                   不标出来时「删除按钮凭空消失」会被当成故障，所以放在状态之前先自报身份。
                   说明文字走整卡 tooltip（cardHoverTip），这里不再套一层 el-tooltip ——
                   嵌套 tooltip 会和整卡那个同时弹出，两个 popper 叠在一起。 -->
              <el-tag v-if="skill.protected" size="small" effect="plain" type="primary"
                      class="tag-system">{{ $t('skill.systemTag') }}</el-tag>
              <el-tag size="small" effect="dark" :type="statusMeta(skill.status).tag">
                {{ statusMeta(skill.status).text }}
              </el-tag>
              <!-- 当前发布版本：租户级技能看自己的版本；私有技能没有独立版本号，
                   正文冻结在所属智能体的当前版本里，后端把 agent 的版本号填进来（version_source=agent） -->
              <el-tag size="small" effect="plain" type="info" class="tag-version"
                      :title="skill.agent_id
                        ? (skill.current_version ? $t('skill.versionOwnerPublished', { v: skill.current_version }) : $t('skill.versionOwner'))
                        : (skill.current_version ? $t('skill.versionPublished', { v: skill.current_version }) : $t('skill.versionUnreleased'))">
                {{ skill.current_version ? `v${skill.current_version}` : $t('skill.badgeUnreleased') }}
              </el-tag>
              <!-- 归属/可见性：私有 | 共享N（N = 能使用它的智能体数） -->
              <el-tag size="small" effect="light" :type="scopeTag(skill).type"
                      :class="{ 'tag-private': !!skill.agent_id }">
                {{ scopeTag(skill).text }}
              </el-tag>
            </div>
          </div>
        </div>
        <div class="card-body">
          <div class="card-body-bottom">
            <div class="info-item">
              <span class="info-label">{{ $t('skill.labelDesc') }}</span>
              <span class="info-value description-value" :title="skill.description">{{ skill.description || $t('skill.emptyDesc') }}</span>
            </div>
            <div v-if="skill.agent_id" class="info-item">
              <span class="info-label">{{ $t('skill.labelOwner') }}</span>
              <span class="info-value">{{ skill.agent_name || $t('skill.unknown') }}</span>
            </div>
            <div class="info-item">
              <span class="info-label">{{ $t('skill.labelUpdated') }}</span>
              <span class="info-value">{{ skill.updated_by_name || $t('skill.unknown') }} · {{ formatTime(skill.updated_at) }}</span>
            </div>
          </div>
          <!-- 操作浮层：只覆盖「卡片头下方」那块（即 card-body），hover 时浮出、按钮居中。
               整层 @click.stop，点按钮不会误触「打开编辑」。 -->
          <div class="card-actions" @click.stop>
            <!-- 两态：正常(1) / 废弃(3) -->
            <el-tooltip v-if="skill.status !== 1" :content="$t('skill.actionRestore')" placement="top">
              <button class="card-action-btn" @click="emit('status', { row: skill, status: 1 })">
                <el-icon><CircleCheck /></el-icon>
              </button>
            </el-tooltip>
            <el-tooltip v-if="skill.status !== 3" :content="$t('skill.actionDeprecate')" placement="top">
              <button class="card-action-btn" @click="emit('status', { row: skill, status: 3 })">
                <el-icon><CircleClose /></el-icon>
              </button>
            </el-tooltip>
            <el-tooltip :content="$t('skill.actionEdit')" placement="top">
              <button class="card-action-btn" @click="emit('open', skill)">
                <el-icon><Edit /></el-icon>
              </button>
            </el-tooltip>
            <el-tooltip
              :content="skill.agent_id ? $t('skill.exportPrivateTip') : $t('skill.exportTip')"
              placement="top"
            >
              <button class="card-action-btn" :disabled="!!skill.agent_id" @click="emit('export', skill)">
                <el-icon><Download /></el-icon>
              </button>
            </el-tooltip>
            <el-tooltip v-if="!skill.protected && skill.status === 1" :content="$t('skill.actionDelete')" placement="top">
              <button class="card-action-btn danger" @click="emit('delete', skill)">
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
import { Plus, Search, Refresh, Edit, Delete, CircleCheck, CircleClose, ArrowDown, Close, CaretRight, Upload, Download } from '@element-plus/icons-vue'
import { useHeaderCompact } from '../../utils/useHeaderCompact.js'

const { t } = useI18n()

const props = defineProps({
  skills: { type: Array, default: () => [] },
  loading: { type: Boolean, default: false },
  // 「共享 N」用的是能被使用的 agent 总数，父组件已经拉过那份列表
  agentCount: { type: Number, default: 0 }
})
// 列表只管展示与筛选；进入编辑 / 新建 / 导入 / 导出 / 改状态 / 删除 / 刷新都回给父组件
const emit = defineEmits(['open', 'create', 'import', 'export', 'status', 'delete', 'refresh'])

// 归属/可见性标签：
//   私有（agent 专属）→ 私有
//   租户级 → 共享N，N = 能使用它的智能体数：
//     全员可见（restricted=false）→ 全部 agent 数
//     白名单（restricted=true）  → 绑定的 agent 数（bound_count）
const scopeTag = (s) => {
  if (s.agent_id) return { type: 'success', text: t('skill.scopePrivate') }
  const n = s.restricted ? (s.bound_count || 0) : props.agentCount
  return {
    type: s.restricted && n === 0 ? 'danger' : 'info',
    text: t('skill.scopeShared', { n })
  }
}

const formatTime = (t) => {
  if (!t) return ''
  return String(t).replace('T', ' ').slice(0, 19)
}

const keyword = ref('')
// 两态状态（与后端 shared/models.StatusXxx 一一对应）：
//   1 正常：可修改、可运行（新建默认）
//   3 废弃：不可修改、不可运行
const statusMeta = (status) => (status === 3
  ? { text: t('skill.filterDeprecated'), tag: 'danger' }
  : { text: t('skill.filterNormal'), tag: 'success' })
// 卡片名样式：废弃=红（正常态不加类）
const titleClass = (status) => (status === 3 ? 'title-disabled' : '')
// 卡片悬停提示（参考工作流）：废弃时提示"查看"
const cardHoverTip = (skill) => {
  const st = skill.status || 1
  // 系统内置：删/改名的闸门在 shared/models 的名单里，卡片上只是把按钮藏了 ——
  // 不在这儿说出来，"删除按钮凭空消失"会被当成故障。
  if (skill.protected) {
    const why = t('skill.systemHoverWhy')
    return t(st === 3 ? 'skill.hoverSystemDeprecated' : 'skill.hoverSystemEdit', { why })
  }
  return t(st === 3 ? 'skill.hoverViewDeprecated' : 'skill.hoverEdit')
}

// 鼠标是否正压在卡片底部的操作条上：压上去时把「点击编辑技能」的 tip 关掉
// （el-tooltip 的 disabled 会立刻关闭已显示的 tip），否则外层 tip 会和按钮自己的 tip 一起冒出来。
// 用 mousemove + closest 判定：指针从卡片外直接进到按钮上时 mouseenter 不一定触发，
// 而卡片整体带 @mousemove，一定会经过。
const actionsHovered = ref(false)
const onCardMove = (e) => { actionsHovered.value = !!e.target?.closest?.('.card-actions') }
// 状态筛选：all 全部 | normal 正常(1) | deprecated 废弃(3)
const statusFilter = ref('all')

// 是否显示系统内置（受保护名单）：这是纯视图偏好，存 localStorage ——
// 否则每次进页面都要重新点一次。默认显示。
const showSystem = ref(localStorage.getItem('skills.show-system') !== '0')
watch(showSystem, (v) => localStorage.setItem('skills.show-system', v ? '1' : '0'))

// 状态数量统计（用于筛选按钮上显示；为 0 时模板不渲染数字）
// 跟随 showSystem：藏起来的不该还算进计数，否则「全部 (6)」下面只有 3 张卡
const statusCounts = computed(() => {
  const list = showSystem.value ? props.skills : props.skills.filter(s => !s.protected)
  return {
    all: list.length,
    normal: list.filter(s => (s.status || 1) === 1).length,
    deprecated: list.filter(s => s.status === 3).length
  }
})
// 过滤后的技能列表（先按状态筛选，再按关键词：技能名 / 描述 / 归属智能体名）
const filteredSkills = computed(() => {
  let list = props.skills
  if (statusFilter.value === 'normal') {
    list = list.filter(s => (s.status || 1) === 1)
  } else if (statusFilter.value === 'deprecated') {
    list = list.filter(s => s.status === 3)
  }
  if (!keyword.value) {
    return list
  }
  const q = keyword.value.toLowerCase()
  return list.filter(s =>
    (s.name || '').toLowerCase().includes(q) ||
    (s.description || '').toLowerCase().includes(q) ||
    (s.agent_name || '').toLowerCase().includes(q)
  )
})

// 分组置顶：系统内置（受保护名单）单独成段固定在最前，其余一组。
// 与卡片 tag 的分工：tag 回答"这一个是什么"，分组回答"整体怎么分区"。
// 组是筛完之后的结果切分 —— 顶部状态筛选与关键词只过滤内容，组头计数跟着变；
// 空组不产出（一个系统技能都没有时不该出现空标题）。
const skillGroups = computed(() => {
  const list = filteredSkills.value
  // 关掉「系统」开关时整组不产出，而不是渲染后再隐藏 —— 组头计数也就自然消失
  const sys = showSystem.value ? list.filter(s => s.protected) : []
  const rest = list.filter(s => !s.protected)
  const groups = []
  if (sys.length) groups.push({ key: 'system', label: t('skill.groupSystem'), items: sys })
  if (rest.length) groups.push({ key: 'other', label: t('skill.groupOther'), items: rest })
  // 只有一组时不画组头：本来就是一个扁平列表，再加个标题纯是噪声
  const headed = groups.length > 1
  return groups.map(g => ({ ...g, headed }))
})

// 空列表提示文案（区分「搜索无结果」与「该状态下暂无数据」）
const emptyText = computed(() => {
  if (keyword.value) {
    return t('skill.emptySearch')
  }
  // 系统内置被开关藏起来了：说清为什么空，而不是笼统报「暂无技能」
  if (!showSystem.value && props.skills.some(s => s.protected)) {
    return t('skill.emptySystemHidden')
  }
  if (statusFilter.value === 'deprecated') {
    return t('skill.emptyDeprecated')
  }
  return t('skill.emptyNone')
})

// 切换状态筛选（「全部」同时承担刷新）
const setStatusFilter = (filter) => {
  statusFilter.value = filter
  emit('refresh')
}

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
const STATUS_FILTERS = [
  { key: 'all', icon: Refresh, cls: '' },
  { key: 'normal', icon: CircleCheck, cls: 'btn-enable' },
  { key: 'deprecated', icon: CircleClose, cls: 'btn-disable' }
]
// 标签不放进常量里：切语言时 STATUS_FILTERS 不会重建，label 会停在旧语言
const statusLabel = (key) => t(key === 'normal'
  ? 'skill.filterNormal'
  : key === 'deprecated' ? 'skill.filterDeprecated' : 'skill.filterAll')
const activeStatus = computed(() => STATUS_FILTERS.find(o => o.key === statusFilter.value) || STATUS_FILTERS[0])
const activeStatusLabel = computed(() => {
  const o = activeStatus.value
  const n = statusCounts.value[o.key]
  return statusLabel(o.key) + (n ? ` (${n})` : '')
})
</script>

<style scoped>
/* 头部工具栏（与智能体页同款：筛选按钮组靠左，搜索 + 新建靠右） */
.agent-skills-header {
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

/* 状态语义色：正常=绿、废弃=红。只作用于按钮内的图标与文字，
   不受按钮自身 hover / active 文字色（#333）影响。 */
.status-filters :deep(.el-button.btn-enable .el-icon),
.status-filters :deep(.el-button.btn-enable span) {
  color: #389e0d;
}

.status-filters :deep(.el-button.btn-disable .el-icon),
.status-filters :deep(.el-button.btn-disable span) {
  color: #f56c6c;
}

.agent-skills-header .el-button {
  border-radius: 10px;
  padding: 6px 16px;
  font-size: 14px;
  background: none;
  border: 2px solid white;
  color: #666;
  transition: all 0.3s ease;
}

.agent-skills-header .el-button:hover {
  background-color: #e6e6e6;
  color: #333;
  border: 2px solid white;
}

.agent-skills-header .el-button.active {
  background-color: #e0e0e0;
  color: #333;
  border: 2px solid white;
}

/* 「系统」开关：用 el-switch 表达二值（与编辑区的开关同源）。
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

/* 新建按钮 */
.agent-skills-header .el-button--primary {
  background-color: #1890ff;
  color: white;
  border: 2px solid white;
}

.agent-skills-header .el-button--primary:hover {
  background-color: #40a9ff;
  color: white;
  border: 2px solid white;
}

/* 搜索框：margin-left:auto 把左侧空间留给筛选按钮组，自己与新建按钮贴右 */
.search-box {
  margin-left: auto;
  width: 200px;
}

.search-box :deep(.el-input__wrapper) {
  border-radius: 10px;
  border: 2px solid transparent;
}

.search-box :deep(.el-input__wrapper.is-focus),
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

/* 窄档起「新建技能」只留 +：文字让位给筛选与搜索。
   padding 要同步收紧 —— 只藏文字不改内边距，46px 的左右留白会让它比带字时还宽。
   「导入」与「新建」同一套收缩规则：都是右上角的创建类动作。 */
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

.agent-skills-content {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
}

/* 技能卡片列表 / 卡片：与智能体卡片同款边框阴影，但列更窄 ——
   技能卡只有名字 + 描述 + 几个标签，用智能体的 270–330 会显得空。
   200 这一档能让 1200px 宽的内容区排下 5 列（240 只能排 4 列）。 */
.agent-skills-list {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(200px, 1fr));
  gap: 14px;
  align-content: start;
}

/* 分组的段标题：跨满整行（列表本身是 grid），把「系统内置 / 其他技能」切成两段。
   只有两组同时存在时才渲染（见 skillGroups 的 headed）——单一分组时它只是噪声。 */
.skill-group-head {
  grid-column: 1 / -1;
  display: flex;
  align-items: center;
  gap: 6px;
  padding-bottom: 6px;
  border-bottom: 1px solid #ebeef5;
}

.skill-group-title {
  font-size: 12px;
  font-weight: 600;
  color: #606266;
}

.skill-group-count {
  font-size: 11px;
  color: #909399;
}

.agent-skill-card {
  position: relative;
  cursor: pointer; /* 整卡可点：点击任意位置进入编辑 */
  width: 100%;
  min-width: 200px;
  max-width: 250px;
  background-color: white;
  border: 1px solid #e8e8e8;
  border-radius: 12px;
  padding: 16px;
  box-sizing: border-box;
  box-shadow: 0 2px 8px rgba(0, 0, 0, 0.1);
  transition: all 0.3s ease;
}

.agent-skill-card:hover {
  box-shadow: 0 4px 16px rgba(0, 0, 0, 0.15);
}

.card-header {
  position: relative; /* 保留定位基准（头部内的绝对定位子元素依赖它） */
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 10px;
  padding-bottom: 8px;
  border-bottom: 1px solid #f0f0f0;
  cursor: pointer;
}

/* 名字 + 状态：头部横向空间全给它（原来的 max-width:180px 是给按钮让位留下的，已去掉） */
.header-content {
  flex: 1;
  min-width: 0; /* 允许收缩，长名字才会正确出省略号而不是把卡片撑破 */
  overflow: hidden;
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

/* 废弃（status=3）名字标红：既不可改也不可用 */
.header-content .card-title.title-disabled {
  color: #f56c6c;
}

.card-description {
  display: flex;
  align-items: center;
  gap: 4px;
  margin-top: 4px;
  flex-wrap: wrap;
}

/* 第二行的「编辑 / 共享」标记：Element 的 small 在 200px 窄卡里仍然偏大，
   压到 16px 高、10px 字的胶囊（border-radius 取半高以上即全圆角） */
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

/* 「私有」是这张卡片最需要一眼看到的归属信息：同一排标签里再加一档权重 */
.card-description :deep(.tag-private) {
  /* 10px 字号下 600 会糊成一团，降到 500 仍能靠底色区分 */
  font-weight: 500;
  background-color: var(--el-color-success-light-8);
}

/* 「系统」= 受保护名单里的内置技能（不可删除/改名）。
   色板已被占满：绿=正常/私有、红=废弃/无人可见、灰=共享N —— 蓝色没人用，
   正好留给"系统"。刻意不用 plain 的默认灰（与 scope 的 light 只差一圈边框深浅，
   14px 小胶囊上根本看不出来）。目的是让"这是系统给的"扫视时立住，
   否则它只是又一个灰标签，删除按钮消失依然无解。 */
.card-description :deep(.tag-system) {
  font-weight: 500;
  color: var(--el-color-primary);
  border-color: var(--el-color-primary-light-5);
  background-color: var(--el-color-primary-light-9);
}

/* 卡片体：操作浮层的定位基准（浮层只覆盖头部下方这块，不压名字与标签） */
.card-body {
  position: relative;
}

/* 卡片操作浮层：hover 时浮在卡片体上（inset:0 相对 .card-body），按钮居中。
   不占布局空间 —— 名字与描述保持原样；背景透明，靠按钮自身的实心底色立住。
   点击由容器的 @click.stop 吃掉，不会误触「打开编辑」。 */
.card-actions {
  position: absolute;
  inset: 0;
  z-index: 2;
  display: flex;
  align-items: center;
  justify-content: center;
  /* 200px 窄卡的正文只有 ~168px：5 个按钮压到 24px + 4px 间距才排得下 */
  gap: 4px;
  border-radius: 8px; /* 卡片体内侧，不必跟卡片外圆角 */
  /* 蒙层完全透明：按钮直接浮在卡片体上。实心蓝/红按钮自带对比度，
     不需要压暗背景 —— 按钮之间的缝隙里仍能看到描述文字，属于预期效果。 */
  background: transparent;
  opacity: 0;
  pointer-events: none; /* 隐藏时不参与命中测试，否则会挡住整卡的点击 */
  transition: opacity 0.2s ease;
}

.agent-skill-card:hover .card-actions {
  opacity: 1;
  pointer-events: auto;
}

.card-action-btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 24px;
  height: 24px;
  /* 边框要比底色深一档（#d9ecff 与 #ecf5ff 几乎同色，等于没有边界），
     这样按钮压在透明蒙层的文字上时轮廓清楚 */
  border: 1px solid #a0cfff;
  border-radius: 6px;
  /* 淡底 + 彩色图标：蒙层透明后按钮直接压在文字上，实心深色过重。
     用不透明的浅色底（不是半透明）才能挡住背后的描述文字。hover 再填实。 */
  background: #ecf5ff;
  color: #409eff;
  cursor: pointer;
  transition: all 0.2s;
}

/* 必须覆盖 .card-header .el-icon 的 40px，否则会撑爆 28px 按钮 */
.card-action-btn .el-icon {
  font-size: 14px !important;
  color: inherit;
}

.card-action-btn:hover {
  background: #409eff;
  border-color: #409eff;
  color: #fff;
}

/* 禁用态（私有技能的导出）：仍占位，但明显不可点 */
.card-action-btn:disabled,
.card-action-btn:disabled:hover {
  background: #f5f7fa;
  border-color: #e4e7ed;
  color: #c0c4cc;
  cursor: not-allowed;
}

/* 删除是破坏性动作：淡红底，与其余蓝色区分开 */
.card-action-btn.danger {
  background: #fef0f0;
  border-color: #fab6b6; /* 与蓝色按钮同口径：边框比底色深一档 */
  color: #f56c6c;
}

.card-action-btn.danger:hover {
  background: #f56c6c;
  border-color: #f56c6c;
  color: #fff;
}

.card-body-bottom {
  display: flex;
  flex-direction: column;
  gap: 6px;
}

/* 窄卡（200px）下描述必须限高：不限的话一条长描述能把卡片撑到两三倍高，
   一排卡片参差不齐。最多 2 行（与智能体 / 工作流卡片同口径），全文靠 title 悬停看。

   选择器必须带 .info-value 前缀（0,2,0）：.info-value 里的 white-space:nowrap 在
   文件里定义得比这里晚，同特异性下它赢 —— 那样 -webkit-line-clamp 失效，描述只剩
   一行省略。加前缀把优先级抬起来，normal 才真正生效。 */
.info-value.description-value {
  display: -webkit-box;
  -webkit-box-orient: vertical;
  -webkit-line-clamp: 2;
  overflow: hidden;
  white-space: normal;
  text-overflow: ellipsis;
  line-height: 1.5;
  max-height: 3em;
  flex: 1;
  min-width: 0;
}

.info-item {
  display: flex;
  gap: 4px;
  font-size: 12px;
  color: #909399;
  min-width: 0;
}

.info-value {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.no-data,
.loading {
  padding: 40px;
  text-align: center;
  color: #909399;
}

/* ===== 主列表滚动条：位置常驻、悬浮才上色 =====
   不悬浮时拇指透明（看不见），鼠标进入列表区域才显示 6px 灰色拇指；
   槽位常驻 6px，条的显隐不会把内容挤动。约定见 platform/README.md，实现同 Chat.vue。 */
.agent-skills-content {
  scrollbar-gutter: stable;
  scrollbar-width: thin;
  scrollbar-color: transparent transparent;
}

.agent-skills-content::-webkit-scrollbar {
  width: 6px;
  height: 6px;
}

.agent-skills-content::-webkit-scrollbar-track {
  background: transparent;
}

.agent-skills-content::-webkit-scrollbar-thumb {
  background: transparent;
  border-radius: 3px;
}

.agent-skills-content:hover {
  scrollbar-color: #c1c1c1 transparent;
}

.agent-skills-content:hover::-webkit-scrollbar-thumb {
  background: #c1c1c1;
}
</style>
