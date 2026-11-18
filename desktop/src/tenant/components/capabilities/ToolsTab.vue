<template>
  <!-- .tt-wrap 只当定位基准（详情面板贴它右缘），滚动仍然发生在里面的 .tt 上 ——
       否则面板会跟着列表一起滚走。 -->
  <div class="tt-wrap">
  <div class="tt">
    <!-- 顶部：计数与动作 -->
    <div class="tt-bar">
      <div class="tt-summary">
        共 <strong>{{ toolRefCount }}</strong> 个工具 · <strong>{{ customGroups.length }}</strong> 个工具组 ·
        <strong>{{ connGroups.length }}</strong> 个远程连接 · <strong>{{ localItems.length }}</strong> 个本地工作流端点
      </div>
      <div class="tt-actions">
        <el-button size="small" :loading="loading" @click="load">刷新</el-button>
        <el-button size="small" type="primary" @click="openNewGroup">新建工具组</el-button>
      </div>
    </div>

    <div v-if="loading" class="editor-tip">加载中...</div>
    <div v-else-if="loadError" class="editor-tip">{{ loadError }}</div>
    <template v-else>
      <!-- ① 工具组（可写） -->
      <section class="tt-section">
        <div class="tt-sec-title clickable" @click="toggleSection('group')">
          <el-icon class="tt-caret" :class="{ open: !isSectionCollapsed('group') }"><CaretRight /></el-icon>
          工具组（{{ customGroups.length }}）
          <span class="tt-sec-hint">把常用工具打包，便于给 agent 批量授权；成员只能是天然组或单个工具</span>
        </div>
        <div v-if="!customGroups.length" v-show="!isSectionCollapsed('group')" class="editor-tip">
          还没有工具组 —— 点右上「新建工具组」创建，例如 <code>fs-basic</code>（只含文件类工具）。
        </div>
        <div v-else v-show="!isSectionCollapsed('group')" class="tt-list">
          <div v-for="g in customGroups" :key="g.name" class="tt-row">
            <el-icon class="tt-icon group"><Folder /></el-icon>
            <div class="tt-main">
              <div class="tt-name">
                {{ g.name }}
                <span class="ref-tag">{{ (g.items || []).length }} 个成员</span>
                <span class="ref-tag">group:{{ g.name }}</span>
              </div>
              <div class="tt-desc" :title="g.description || refsText(g)">
                {{ g.description || refsText(g) || '无描述' }}
              </div>
            </div>
            <el-button size="small" text @click="showUsage('group:' + g.name)">引用</el-button>
            <el-button size="small" text @click="editGroup(g)">编辑</el-button>
          </div>
        </div>
      </section>

      <!-- ② 内置工具（只读） -->
      <section class="tt-section">
        <div class="tt-sec-title clickable" @click="toggleSection('builtin')">
          <el-icon class="tt-caret" :class="{ open: !isSectionCollapsed('builtin') }"><CaretRight /></el-icon>
          内置工具（{{ builtinCount }}）
          <span class="tt-sec-hint">代码内置，不可编辑；按类目分组</span>
        </div>
        <div v-for="g in builtinGroups" :key="g.ref" v-show="!isSectionCollapsed('builtin')" class="tt-group">
          <div class="tt-group-head" @click="toggle(g.ref)">
            <el-icon class="tt-caret" :class="{ open: !isCollapsed(g.ref) }"><CaretRight /></el-icon>
            <span class="tt-group-name">{{ g.name }}（{{ (g.items || []).length }}）</span>
            <span class="tt-group-ref">{{ g.ref }}</span>
            <span v-if="g.description" class="tt-group-desc">{{ g.description }}</span>
          </div>
          <div v-show="!isCollapsed(g.ref)" class="tt-list">
            <div v-for="t in (g.items || [])" :key="t.ref" class="tt-row sub">
              <el-icon class="tt-icon"><Tools /></el-icon>
              <div class="tt-main">
                <div class="tt-name">
                  {{ t.label || t.name }}
                  <span class="ref-tag">{{ t.ref }}</span>
                </div>
                <div class="tt-desc" :title="t.description">{{ t.description }}</div>
              </div>
              <el-button size="small" text class="tt-detail-btn" @click="showDetail(t, g.name)">详情</el-button>
              <el-button size="small" text @click="showUsage(t.ref)">引用</el-button>
            </div>
          </div>
        </div>
      </section>

      <!-- ③ 远程连接（远程 MCP / RPC，我们连出去；连接本身在设置页管理，这里只投影它加载到的工具） -->
      <section class="tt-section">
        <div class="tt-sec-title clickable" @click="toggleSection('conn')">
          <el-icon class="tt-caret" :class="{ open: !isSectionCollapsed('conn') }"><CaretRight /></el-icon>
          远程连接（{{ connGroups.length }}）
          <span class="tt-sec-hint">
            远程 MCP / RPC 服务，我们连出去用它的工具。连接本身在「设置 → MCP / RPC 连接」管理
          </span>
        </div>
        <div v-if="!connGroups.length" v-show="!isSectionCollapsed('conn')" class="editor-tip">
          还没有远程 MCP / RPC 连接。
          <el-button size="small" text type="primary" @click="$emit('nav', 'settings')">去设置页添加连接</el-button>
        </div>
        <div v-for="g in connGroups" :key="g.ref" v-show="!isSectionCollapsed('conn')" class="tt-group">
          <div class="tt-group-head" @click="toggle(g.ref)">
            <el-icon class="tt-caret" :class="{ open: !isCollapsed(g.ref) }"><CaretRight /></el-icon>
            <el-icon class="tt-icon"><Connection /></el-icon>
            <span class="tt-group-name">{{ g.name }}（{{ (g.items || []).length }}）</span>
            <span class="ref-tag">{{ kindLabel(g.kind) }}</span>
            <span v-if="g.available === false" class="ref-tag warn">未启用</span>
            <span class="tt-group-ref">{{ g.ref }}</span>
            <span v-if="g.refreshed_at" class="tt-group-ref">最后刷新：{{ formatTime(g.refreshed_at) }}</span>
            <el-button v-if="g.source_id" size="small" text
                       :loading="refreshing === g.ref" @click.stop="refreshTools(g)">刷新清单</el-button>
            <el-button size="small" text @click.stop="showUsage(g.ref, true)">引用</el-button>
            <el-button size="small" text @click.stop="$emit('nav', 'settings')">去设置</el-button>
          </div>
          <div v-show="!isCollapsed(g.ref)" class="tt-list">
            <div v-for="t in (g.items || [])" :key="t.ref" class="tt-row sub">
              <el-icon class="tt-icon"><Tools /></el-icon>
              <div class="tt-main">
                <div class="tt-name">
                  {{ t.label || t.name }}
                  <span class="ref-tag">{{ t.ref }}</span>
                  <span v-if="t.available === false" class="ref-tag warn">不可用</span>
                </div>
                <div class="tt-desc" :title="t.description">{{ t.description }}</div>
              </div>
              <el-button size="small" text class="tt-detail-btn" @click="showDetail(t, g.name)">详情</el-button>
              <el-button size="small" text @click="showUsage(t.ref)">引用</el-button>
            </div>
          </div>
        </div>
      </section>

      <!-- ④ 本地工作流（本地 RPC 端点，我们暴露出去；在「端点」页管理对外开放 / 收回）
           后端只产出一个 local:rpc 组，组头在这里是多余的一层（名字、类型、来源 ref 都与段标题重复），
           所以摊平成员直接列；组的描述与最后刷新时间提到段标题上。 -->
      <section class="tt-section">
        <div class="tt-sec-title clickable" @click="toggleSection('local')">
          <el-icon class="tt-caret" :class="{ open: !isSectionCollapsed('local') }"><CaretRight /></el-icon>
          本地工作流（{{ localItems.length }}）
          <span class="tt-sec-hint">
            本租户工作流对外开放的 rpc 端点，给别的 agent 当工具用；agent 之间的调用请走 call_agent。对外开放 / 收回在「端点」页
          </span>
          <span v-if="localRefreshedAt" class="tt-sec-hint">最后刷新：{{ formatTime(localRefreshedAt) }}</span>
          <el-button size="small" text @click.stop="$emit('nav', 'endpoints')">去端点页</el-button>
        </div>
        <div v-if="!localItems.length" v-show="!isSectionCollapsed('local')" class="editor-tip">
          还没有本地工作流端点 —— 工作流对外开放 RPC 端点后会出现在这里。
        </div>
        <div v-show="!isSectionCollapsed('local')" class="tt-list">
          <div v-for="t in localItems" :key="t.ref" class="tt-row sub">
            <el-icon class="tt-icon"><Tools /></el-icon>
            <div class="tt-main">
              <div class="tt-name">
                {{ t.label || t.name }}
                <span class="ref-tag">{{ t.ref }}</span>
                <!-- 本地端点：可用性即对外状态（Available = exposed），显示「对外开放 / 仅内部」更直观 -->
                <span class="ref-tag" :class="{ ok: t.available !== false, warn: t.available === false }">
                  {{ t.available === false ? '仅内部' : '对外开放' }}
                </span>
              </div>
              <div class="tt-desc" :title="t.description">{{ t.description }}</div>
            </div>
            <el-button size="small" text class="tt-detail-btn" @click="showDetail(t, '本地工作流')">详情</el-button>
            <el-button size="small" text @click="showUsage(t.ref)">引用</el-button>
          </div>
        </div>
      </section>
    </template>

    <GroupPanel ref="groupPanelRef" :groups="catalog.groups || []" @saved="load" />
    <UsageView ref="usageRef" />
  </div>

  <!-- 详情面板在滚动容器之外：右侧滑出、左缘可拖宽，与智能体配置同款 -->
  <ToolDetailPanel ref="detailRef" />
  </div>
</template>

<script setup>
import { ref, computed, onMounted } from 'vue'
import { ElMessage } from 'element-plus'
import { CaretRight, Folder, Tools, Connection } from '@element-plus/icons-vue'
import adminApi from '../../config/admin.js'
import { getToken } from '../../utils/auth.js'
import GroupPanel from './GroupPanel.vue'
import ToolDetailPanel from './ToolDetailPanel.vue'
import UsageView from './UsageView.vue'

const emit = defineEmits(['count', 'nav'])

const loading = ref(false)
const loadError = ref('')
const catalog = ref({ groups: [] })
const customGroups = ref([])
const collapsed = ref({})

// 四个大段（自定义组 / 内置工具 / 远程连接 / 本地工作流）的收起状态：缺省收起，
// 只有显式记成 false 才是展开（段标题仍显示计数与说明，收起也能看出内容概览）
const sectionCollapsed = ref({})
const isSectionCollapsed = (key) => sectionCollapsed.value[key] !== false
const toggleSection = (key) => {
  sectionCollapsed.value = { ...sectionCollapsed.value, [key]: !isSectionCollapsed(key) }
}
const groupPanelRef = ref(null)
const usageRef = ref(null)
const detailRef = ref(null)

const authHeaders = () => ({ Authorization: `Bearer ${getToken()}` })

const kindLabel = (k) => ({ builtin: '内置', mcp: 'MCP', rpc: 'RPC', local: '本地端点', group: '工具组', preset: '预设' }[k] || k)

// synthetic = 类别容器（插件 / 自定义工具）。它们各有独立 tab，一律不在工具目录里渲染 ——
// 除了 kind 白名单，再加一道 synthetic 兜底：即使后端某天把容器的 kind 写错，
// 也不会让 custom__xxx 这种模型侧名字漏到「内置工具」列表里。
const builtinGroups = computed(() => (catalog.value.groups || [])
  .filter((g) => g.kind === 'builtin' && !g.synthetic))
// 远程连接 = 远程 MCP / RPC 服务，我们连出去用它的工具
const connGroups = computed(() => (catalog.value.groups || []).filter((g) => ['mcp', 'rpc'].includes(g.kind)))
// 本地工作流 = 本地 RPC 服务端点，我们发出去、被别的 agent 当工具用
const localGroups = computed(() => (catalog.value.groups || []).filter((g) => g.kind === 'local'))
// 后端只产出一个 local:rpc 组（成员就是全部 rpc 端点），所以直接摊平成员，
// 段标题里的数字数的是「端点数」而不是「组数」（后者恒为 1，没有意义）。
const localItems = computed(() => localGroups.value.flatMap((g) => g.items || []))
const localRefreshedAt = computed(() => localGroups.value.reduce(
  (latest, g) => (g.refreshed_at && g.refreshed_at > latest ? g.refreshed_at : latest), ''))
const builtinCount = computed(() => builtinGroups.value.reduce((n, g) => n + (g.items || []).length, 0))

// 工具总数 = 本 tab 实际展示的叶子工具 ref 去重（内置 + 连接 + 端点）。
// 插件与自定义工具各有独立 tab（且本页不渲染它们），不重复计入 ——
// 否则顶部数字会大于列表里的条目，看起来像「工具丢了」或「工具目录里混进了自定义工具」。
// 注意 custom 也必须排除：它同样不在这里渲染，算进去就是"数字里有、列表里没有"。
const toolRefCount = computed(() => {
  const set = new Set()
  for (const g of catalog.value.groups || []) {
    if (['plugin', 'script', 'custom'].includes(g.kind)) continue
    for (const t of (g.items || [])) set.add(t.ref)
  }
  return set.size
})

const isCollapsed = (ref) => collapsed.value[ref] !== false
const toggle = (ref) => {
  collapsed.value = { ...collapsed.value, [ref]: !isCollapsed(ref) }
}

const refsText = (g) => (g.items || []).map((i) => i.ref).join('、')

const load = async () => {
  loading.value = true
  loadError.value = ''
  try {
    const [cRes, gRes] = await Promise.all([
      fetch(adminApi.toolCatalog.list(), { headers: authHeaders() }),
      fetch(adminApi.toolGroups.list, { headers: authHeaders() })
    ])
    const cBody = await cRes.json().catch(() => ({}))
    const gBody = await gRes.json().catch(() => ({}))
    if (cBody.code !== 200) throw new Error(cBody.msg || '读取工具目录失败')
    catalog.value = cBody.data || { groups: [] }
    customGroups.value = gBody.code === 200 ? (gBody.data?.items || []) : []
    emit('count', toolRefCount.value)
  } catch (e) {
    loadError.value = e.message || '读取工具目录失败'
    ElMessage.error(loadError.value)
  } finally {
    loading.value = false
  }
}

const openNewGroup = () => groupPanelRef.value?.open(null)
const editGroup = (g) => groupPanelRef.value?.open(g)
const showUsage = (ref, prefix = false) => usageRef.value?.open(ref, prefix)
// 工具详情：本地工作流 / 内置 / 连接三类工具行共用同一个抽屉，
// 第二参数是「归属」文案（组名或段落名），让人知道这个工具从哪来。
const showDetail = (tool, from = '') => detailRef.value?.open(tool, from)

const formatTime = (t) => (t ? String(t).replace('T', ' ').slice(0, 19) : '—')

// 刷新连接的远端工具清单（持久化到本地表后，目录里的工具列表才会更新）
const refreshing = ref('')
const refreshTools = async (g) => {
  refreshing.value = g.ref
  try {
    const url = g.kind === 'mcp'
      ? adminApi.mcpConnections.refreshTools(g.source_id)
      : adminApi.rpcConnections.refreshTools(g.source_id)
    const res = await fetch(url, { method: 'POST', headers: authHeaders() })
    const body = await res.json().catch(() => ({}))
    if (body.code !== 200) throw new Error(body.msg || '刷新失败')
    ElMessage.success('工具清单已刷新')
    await load()
  } catch (e) {
    ElMessage.error(e.message || '刷新失败')
  } finally {
    refreshing.value = ''
  }
}

onMounted(load)
</script>

<style scoped>
/* 定位基准：详情面板 absolute 贴它的右缘、占满高度（滚动在 .tt 上，见模板注释） */
.tt-wrap {
  position: relative;
  flex: 1;
  min-height: 0;
  display: flex;
  flex-direction: column;
}

.tt {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  /* 槽位常驻（条宽固定 6px）：滚动条的显隐不会把内容挤动 */
  scrollbar-gutter: stable;
  display: flex;
  flex-direction: column;
  gap: 14px;
  /* 滚动条「位置常驻、悬浮才上色」：不悬浮时拇指透明 = 看不见，鼠标悬浮才给色。
     与 Chat.vue、平台页（platform/README.md 记录的统一约定）同一套。 */
  scrollbar-width: thin;
  scrollbar-color: transparent transparent;
}

.tt::-webkit-scrollbar {
  width: 6px;
  height: 6px;
}

.tt::-webkit-scrollbar-track {
  background: transparent;
}

.tt::-webkit-scrollbar-thumb {
  background: transparent;
  border-radius: 3px;
}

.tt:hover {
  scrollbar-color: #c1c1c1 transparent;
}

.tt:hover::-webkit-scrollbar-thumb {
  background: #c1c1c1;
}

.tt-bar {
  display: flex;
  align-items: center;
  gap: 12px;
  background: #fff;
  border: 1px solid #ebeef5;
  border-radius: 8px;
  padding: 10px 14px;
}

.tt-summary {
  font-size: 13px;
  color: #606266;
}

.tt-actions {
  margin-left: auto;
  display: flex;
  gap: 8px;
}

.tt-section {
  background: #fff;
  border: 1px solid #ebeef5;
  border-radius: 8px;
  padding: 12px 14px;
}

.tt-sec-title {
  font-size: 14px;
  font-weight: 600;
  color: #303133;
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
  margin-bottom: 8px;
}

/* 可点收起的大段标题：整行可点，箭头旋转表示展开/收起 */
.tt-sec-title.clickable {
  cursor: pointer;
  user-select: none;
}

/* hover 时标题文字变蓝（.tt-sec-hint 自带颜色，不受影响） */
.tt-sec-title.clickable:hover {
  color: #409eff;
}

.tt-sec-hint {
  font-size: 12px;
  font-weight: 400;
  color: #909399;
}

.tt-group {
  border-top: 1px solid #f5f5f5;
}

.tt-group:first-of-type {
  border-top: none;
}

.tt-group-head {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 8px 4px;
  cursor: pointer;
  font-size: 13px;
  color: #303133;
}

.tt-group-head:hover {
  background: #f7f9fc;
}

.tt-caret {
  font-size: 12px;
  color: #909399;
  transition: transform 0.2s;
}

.tt-caret.open {
  transform: rotate(90deg);
}

.tt-group-name {
  font-weight: 500;
}

.tt-group-ref,
.tt-group-desc {
  font-size: 12px;
  color: #909399;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.tt-group-ref {
  font-family: ui-monospace, Menlo, Consolas, monospace;
}

.tt-group-desc {
  min-width: 0;
}

.tt-list {
  display: flex;
  flex-direction: column;
}

.tt-row {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 7px 4px;
  border-radius: 4px;
}

.tt-row.sub {
  padding-left: 22px;
}

.tt-row:hover {
  background: #f7f9fc;
}

.tt-icon {
  color: #909399;
  flex-shrink: 0;
}

.tt-icon.group {
  color: #e6a23c;
}

.tt-main {
  flex: 1;
  min-width: 0;
}

.tt-name {
  font-size: 13px;
  color: #303133;
  display: flex;
  align-items: center;
  gap: 6px;
  flex-wrap: wrap;
}

.tt-desc {
  font-size: 12px;
  color: #909399;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.ref-tag {
  font-size: 11px;
  padding: 1px 5px;
  border-radius: 3px;
  color: #909399;
  background: #f4f4f5;
}

.ref-tag.warn {
  color: #e6a23c;
  background: #fdf6ec;
}

/* 对外开放（本地端点成员） */
.ref-tag.ok {
  color: #67c23a;
  background: #f0f9eb;
}

.editor-tip {
  font-size: 12px;
  color: #909399;
  line-height: 1.6;
}
</style>
