<template>
  <div class="running">
    <!-- 两个 tab 数据源与刷新方式都不一样，所以不混在一张表里：
         运行中 = 后端跨实例台账（Redis），活着才在，靠轮询刷新；
         运行历史 = flow_run_records（跑完才落库、长期保留），靠手动查询分页。 -->
    <el-tabs v-model="tab" class="running-tabs">
      <el-tab-pane label="运行中" name="running" />
      <el-tab-pane label="运行历史" name="history" />
    </el-tabs>

    <!-- 运行台账 = 租户里「正在跑什么」：agent 与 flow 都在这里。
         数据来自后端跨实例台账（Redis），不是各页的内存状态，所以刷新/换页面都还在。 -->
    <template v-if="tab === 'running'">
      <!-- 工具条：与「能力 → 端点」的 .et-bar 同一张白卡片（背景 / 边框 / 圆角 / 内边距都对齐）。
           说明文字也放进卡片里（端点页的 .et-hint 就是这么处理的），
           否则一排控件摊在灰底上、左边再飘一行小字，和其它页面不像一个站。 -->
      <div class="running-actions">
        <el-radio-group v-model="kind" size="small">
          <el-radio-button value="">全部</el-radio-button>
          <el-radio-button value="agent">智能体</el-radio-button>
          <el-radio-button value="flow">工作流</el-radio-button>
        </el-radio-group>

        <!-- 排序：两个方向都在前端算（每条数据里已有 started_at），
             started_at 越小 = 跑得越久，所以「最久优先」就是升序 -->
        <el-radio-group v-model="sortOrder" size="small">
          <el-radio-button value="longest">最久优先</el-radio-button>
          <el-radio-button value="newest">最新优先</el-radio-button>
        </el-radio-group>

        <!-- 耗时下限：长时间挂着不结束的 run 才是异常信号，比「哪个最久」更直接 -->
        <el-select v-model="minDuration" size="small" style="width: 132px">
          <el-option v-for="o in DURATION_FILTERS" :key="o.value" :label="o.label" :value="o.value" />
        </el-select>

        <el-button size="small" :loading="loading" @click="load">刷新</el-button>

        <span class="running-count">
          共 <strong>{{ rows.length }}</strong> 条<template v-if="visibleRows.length !== rows.length">，显示 {{ visibleRows.length }}</template>
        </span>

        <span class="running-hint">
          本租户当前在跑的智能体与工作流，跨实例汇总；默认最久优先 —— 跑得越久越该先看
        </span>

        <el-switch v-model="autoRefresh" size="small" active-text="自动刷新" />
      </div>

      <!-- 台账没读全时如实说明：此时列表只剩本实例在跑的，别让「只看到一半」被当成事实 -->
      <el-alert
        v-if="!complete"
        class="running-alert"
        type="warning"
        :closable="false"
        show-icon
        title="运行台账未读全（Redis 未启用或不可用）"
        :description="`下面只包含本实例（${instance || '未知'}）在跑的运行。`"
      />

      <div class="running-body">
        <!-- 虚拟滚动：只渲染可视区那十几行，条目再多也不卡。
             代价行是等高 div 网格，做不出端点页那套「卡片行」，所以视觉退回规整网格。 -->
        <el-auto-resizer>
          <template #default="{ height, width }">
            <el-table-v2
              :columns="columns"
              :data="visibleRows"
              :width="width"
              :height="height"
              :row-height="ROW_HEIGHT"
              :header-height="40"
              :row-key="'run_id'"
              :row-class="rowClass"
              fixed
            >
              <template #empty>
                <div v-loading="loading" class="running-empty">
                  <div class="running-empty-title">{{ emptyTitle }}</div>
                  <div v-if="emptyTip" class="running-empty-tip">{{ emptyTip }}</div>
                </div>
              </template>
            </el-table-v2>
          </template>
        </el-auto-resizer>
      </div>
    </template>

    <!-- 历史 tab 首次进入才挂载（挂载即自己拉一次），之后 v-show 保住筛选与已加载的页，
         切回来只做一次轻量刷新，不重建整个列表 -->
    <template v-if="historyMounted">
      <RunHistory v-show="tab === 'history'" ref="historyRef" class="running-history" />
    </template>
  </div>
</template>

<script setup>
import { ref, computed, h, watch, onUnmounted, onActivated, onDeactivated } from 'vue'
import { ElMessage, ElMessageBox, ElButton, ElTag } from 'element-plus'
import adminApi from '../config/admin.js'
import { getToken } from '../utils/auth.js'
import RunHistory from './RunHistory.vue'

// tab：running = 实时台账（轮询）| history = 历史运行记录（按需查询）
const tab = ref('running')
// 历史 tab 惰性挂载：没人点进来就不该在进页面时白查一次库；挂载后靠 v-show 留住筛选
const historyMounted = ref(false)
const historyRef = ref(null)

// run_id → 正在终止中（按钮 loading）
const stopping = ref({})

const rows = ref([])
const loading = ref(false)
// complete=false：台账（Redis）没读全，列表只剩本实例的
const complete = ref(true)
const instance = ref('')
const kind = ref('')
const autoRefresh = ref(true)
// 每秒跳动一次，让「已运行」不是死的
const now = ref(Date.now())

// ===== 排序 / 筛选（纯前端：started_at 已在每条数据里，不动后端）=====
const sortOrder = ref('longest')  // longest | newest
const minDuration = ref(0)        // ms，0 = 不筛

const DURATION_FILTERS = [
  { label: '全部耗时', value: 0 },
  { label: '超过 1 分钟', value: 60 * 1000 },
  { label: '超过 5 分钟', value: 5 * 60 * 1000 },
  { label: '超过 30 分钟', value: 30 * 60 * 1000 }
]

// 超过这个时长就标橙：长时间不结束的运行才是运维真正要盯的那类。
// 与 DURATION_FILTERS 的最后一项刻意同值 —— 筛出来的一屏应该整屏都是橙的。
const OVERDUE_MS = 30 * 60 * 1000

// 行高固定：虚拟滚动按「可视高度 ÷ 行高」算渲染窗口，两行文本（名称 + run id）刚好够
const ROW_HEIGHT = 60

const POLL_MS = 5000
// 条目多时降频：全量台账的拉取与解析本身就有成本，5 秒一次的收益远小于开销
const POLL_MS_BUSY = 15000
const BUSY_THRESHOLD = 200

let pollTimer = null
let clockTimer = null
// 当前生效的轮询间隔（随条目数在 POLL_MS / POLL_MS_BUSY 之间切换）
let pollMs = POLL_MS

const authHeaders = () => ({ Authorization: `Bearer ${getToken()}` })

const shortId = (id) => (id ? String(id).slice(0, 8) : '—')

// 入口：与后端 run_type 同一套词（agent 的 call/rpc/mcp/agent；flow 的 call/rpc/mcp/flow/debug/preview）
const RUN_TYPE_LABELS = {
  call: '对话',
  rpc: 'RPC',
  mcp: 'MCP',
  agent: '智能体调用',
  flow: '流程调用',
  debug: '调试',
  preview: '试跑'
}
const runTypeLabel = (t) => RUN_TYPE_LABELS[t] || t || '—'

const fmtDuration = (ms) => {
  if (!Number.isFinite(ms) || ms < 0) return '—'
  const s = Math.floor(ms / 1000)
  if (s < 60) return `${s}s`
  const m = Math.floor(s / 60)
  if (m < 60) return `${m}m ${s % 60}s`
  const h = Math.floor(m / 60)
  return `${h}h ${m % 60}m`
}

// silent：轮询用。不加遮罩、不弹错误（每 5 秒一次，抖动时不该反复糊用户一脸），
// 手动刷新 / 首次进入才走非 silent 版本。
const load = async (silent = false) => {
  if (!silent) loading.value = true
  try {
    const res = await fetch(adminApi.running(kind.value), { headers: authHeaders() })
    const body = await res.json().catch(() => ({}))
    if (body.code !== 200) throw new Error(body.msg || '读取运行列表失败')
    rows.value = body.data?.runs || []
    complete.value = body.data?.complete !== false
    instance.value = body.data?.instance || ''
    now.value = Date.now()
    // 条目数变了就换轮询间隔（重生定时器，别让旧间隔一直跑）
    const next = rows.value.length > BUSY_THRESHOLD ? POLL_MS_BUSY : POLL_MS
    if (next !== pollMs) {
      pollMs = next
      schedulePoll()
    }
  } catch (e) {
    if (!silent) ElMessage.error(e.message || '读取运行列表失败')
  } finally {
    if (!silent) loading.value = false
  }
}

// 终止一条运行：先确认，再交给后端（本实例命中就直接停，没命中会广播给所有实例）。
// reason 说明结果：stopped 已停 / broadcast 运行在别的实例上、已广播 / unreachable 都没成。
const stopRun = async (row) => {
  try {
    await ElMessageBox.confirm(
      `终止「${row.target_name || row.target_id}」这一轮运行？已产生的输出会保留。`,
      '终止运行',
      { type: 'warning', confirmButtonText: '终止', cancelButtonText: '取消' }
    )
  } catch {
    return
  }
  stopping.value = { ...stopping.value, [row.run_id]: true }
  try {
    const res = await fetch(adminApi.runningStop, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', ...authHeaders() },
      body: JSON.stringify({ kind: row.kind, run_id: row.run_id, session_id: row.session_id })
    })
    const body = await res.json().catch(() => ({}))
    if (body.code !== 200) throw new Error(body.msg || '终止失败')

    const reason = body.data?.reason
    if (reason === 'stopped') {
      ElMessage.success('已终止')
    } else if (reason === 'broadcast') {
      ElMessage.success('已广播给所有实例，运行在别处，稍后会自动消失')
    } else {
      ElMessage.warning('没找到在跑的运行（可能刚结束），且广播不可用')
    }
    load(true)
  } catch (e) {
    ElMessage.error(e.message || '终止失败')
  } finally {
    const next = { ...stopping.value }
    delete next[row.run_id]
    stopping.value = next
  }
}

// 列表可见的行：筛选 → 排序 → 折进「耗时 / 是否终止中」两个派生字段。
//
// 为什么把 elapsed / busy 折进行对象，而不是在 cellRenderer 里读 ref：
// el-table-v2 的 cellRenderer 由组件内部调用，当时没有我们的渲染上下文，
// 读 now.value / stopping.value 不会建立依赖 —— 界面就不会跟着更新。
// 让它们成为 data 的一部分，computed 一变，v2 自然重渲染那一屏。
const visibleRows = computed(() => {
  const nowTs = now.value
  const busy = stopping.value

  let list = rows.value
  if (minDuration.value > 0) {
    list = list.filter((r) => nowTs - r.started_at >= minDuration.value)
  }

  // started_at 越小 = 跑得越久；slice() 避免就地改后端给的顺序
  const sorted = list.slice().sort((a, b) =>
    sortOrder.value === 'longest' ? a.started_at - b.started_at : b.started_at - a.started_at
  )

  return sorted.map((r) => ({
    ...r,
    elapsed: nowTs - r.started_at,
    busy: !!busy[r.run_id]
  }))
})

// 空态分两行（标题 + 说明），与端点页的空态同一套排版
const emptyTitle = computed(() => {
  // 首次进来列表还是空的：先说「加载中」，别先闪一句「没有在跑的运行」
  if (loading.value && !rows.value.length) return '加载中…'
  return rows.value.length ? '没有符合筛选条件的运行' : '当前没有在跑的运行'
})

const emptyTip = computed(() => {
  if (loading.value) return ''
  return rows.value.length
    ? '可放宽「耗时」或「类型」筛选'
    : '本租户暂时没有执行中的智能体或工作流；有调用时会自动出现在这里'
})

// 跑太久的行加类：左侧一条橙线，扫一眼就知道该终止哪条
const rowClass = ({ rowData }) => (rowData.elapsed >= OVERDUE_MS ? 'is-overdue' : '')

// 列定义。v2 没有模板式列，全部用渲染函数；宽度合计约 870px，窄窗口横向滚动，
// 操作列 fixed 在右侧 —— 终止是破坏性操作，不放行首（误触），与全站列表一致在行尾。
const columns = [
  {
    key: 'kind',
    dataKey: 'kind',
    title: '类型',
    width: 92,
    cellRenderer: ({ rowData }) =>
      h(ElTag, {
        size: 'small',
        type: rowData.kind === 'agent' ? 'primary' : 'success',
        effect: 'light'
      }, rowData.kind === 'agent' ? '智能体' : '工作流')
  },
  {
    key: 'name',
    dataKey: 'target_name',
    title: '名称',
    width: 200,
    flexGrow: 1,
    cellRenderer: ({ rowData }) =>
      h('div', { class: 'run-cell' }, [
        h('div', { class: 'run-name', title: rowData.target_name || rowData.target_id },
          rowData.target_name || rowData.target_id || '—'),
        h('div', { class: 'run-sub' }, `run ${shortId(rowData.run_id)}`)
      ])
  },
  {
    key: 'run_type',
    dataKey: 'run_type',
    title: '入口',
    width: 96,
    cellRenderer: ({ rowData }) =>
      h(ElTag, { size: 'small', type: 'info', effect: 'plain' }, runTypeLabel(rowData.run_type))
  },
  {
    // 用户与会话合成一格、上下两行：两者都是「谁在跑」的标识，拆成两列白占 120px，
    // 而会话 id 只在核对时用得到，降为次要信息正合适。
    key: 'user_id',
    dataKey: 'user_id',
    title: '用户 / 会话',
    width: 200,
    cellRenderer: ({ rowData }) =>
      h('div', { class: 'run-cell' }, [
        // 上行：显示名字（后端读台账时按 user_id 批量补 user_name）；
        // 名字会重名，排查时还得看 id，所以 title 仍放 user_id
        h('div', { class: 'run-user', title: rowData.user_id || '' },
          rowData.user_name || rowData.user_id || '—'),
        // 下行：会话 id 截前 8 位，完整值放 title
        h('div', { class: 'run-sub', title: rowData.session_id || '' },
          shortId(rowData.session_id))
      ])
  },
  {
    // 已运行并进实例列：两者都是「这一轮跑在哪、跑了多久」的运行环境信息，
    // 都属次要信息，拆两列白占 110px。
    // 上行放耗时（运维最关心的信号，超时标橙），下行放实例标识（多副本时才用得上）。
    key: 'instance',
    dataKey: 'instance',
    title: '已运行 / 实例',
    width: 190,
    cellRenderer: ({ rowData }) =>
      h('div', { class: 'run-cell' }, [
        h('div', { class: rowData.elapsed >= OVERDUE_MS ? 'run-dur is-over' : 'run-dur' },
          fmtDuration(rowData.elapsed)),
        h('div', { class: 'run-instance' }, [
          h('span', { class: 'run-sub', title: rowData.instance }, rowData.instance || '—'),
          rowData.local ? h(ElTag, { size: 'small', effect: 'plain', class: 'run-local' }, '本机') : null
        ])
      ])
  },
  {
    key: 'actions',
    title: '操作',
    width: 88,
    align: 'right',
    fixed: 'right',
    cellRenderer: ({ rowData }) =>
      h('div', { class: 'run-actions' }, [
        h(ElButton, {
          text: true,
          type: 'danger',
          size: 'small',
          loading: !!rowData.busy,
          onClick: () => stopRun(rowData)
        }, '终止')
      ])
  }
]

function schedulePoll() {
  if (pollTimer) {
    clearInterval(pollTimer)
    pollTimer = null
  }
  // 只在「运行中」tab 上轮询：切到历史后台账不可见，接口没必要每 5 秒再跑一次
  if (!autoRefresh.value || tab.value !== 'running') return
  pollTimer = setInterval(() => load(true), pollMs)
}

const stopTimers = () => {
  if (pollTimer) {
    clearInterval(pollTimer)
    pollTimer = null
  }
  if (clockTimer) {
    clearInterval(clockTimer)
    clockTimer = null
  }
}

const startTimers = () => {
  stopTimers()
  clockTimer = setInterval(() => {
    now.value = Date.now()
  }, 1000)
  schedulePoll()
}

watch(kind, () => load())
watch(autoRefresh, schedulePoll)

// 切 tab：台账把秒表和轮询停掉（看不见就别空跑），历史只在已挂载时刷一次
watch(tab, (v) => {
  if (v === 'running') {
    load()
    startTimers()
    return
  }
  stopTimers()
  if (historyMounted.value) {
    historyRef.value?.reload()
  } else {
    // 首次进入：挂载时组件的 onMounted 自己会拉，不必在这里再调一次
    historyMounted.value = true
  }
})

// 页面在 keep-alive 里：进入时拉一次并开表，离开时停表（别在后台空跑轮询）
onActivated(() => {
  if (tab.value === 'running') {
    load()
    startTimers()
  } else if (historyMounted.value) {
    historyRef.value?.reload()
  }
})
onDeactivated(stopTimers)
onUnmounted(stopTimers)
</script>

<style scoped>
/* 页面留白与智能体 / 工具目录页同一套：padding 20px + #fafafa */
.running {
  position: relative;
  width: 100%;
  height: 100%;
  display: flex;
  flex-direction: column;
  gap: 12px;
  padding: 20px;
  background: #fafafa;
  overflow: hidden;
  box-sizing: border-box;
}

/* 页签：el-tabs 头部自带 15px 下边距，清零后由 .running 的 gap: 12px 统一给间距 */
.running-tabs {
  flex: none;
}

.running-tabs :deep(.el-tabs__header) {
  margin: 0;
}

.running-tabs :deep(.el-tabs__nav-wrap::after) {
  height: 1px;
  background-color: #ebeef5;
}

/* 历史 tab 自己撑满剩余高度（内部是「筛选条 + 表格 + 加载更多」三段） */
.running-history {
  flex: 1;
  min-height: 0;
}

/* 与端点页 .et-hint 同款：12px 灰字，跟在计数后面；min-width: 0 才能在 flex 行里收缩 */
.running-hint {
  font-size: 12px;
  color: #909399;
  min-width: 0;
}

/* 筛选条：与端点页的 .et-bar 同一张白卡片（边框 / 圆角 / 内边距都对齐），
   否则一排控件直接摊在灰底上，和下面那套卡片行不像一个页面 */
.running-actions {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
  padding: 10px 14px;
  background: #fff;
  border: 1px solid #ebeef5;
  border-radius: 8px;
}

/* 与端点页 .et-count 同款：13px + #606266，数字加粗 */
.running-count {
  font-size: 13px;
  color: #606266;
  font-variant-numeric: tabular-nums;
}

.running-alert {
  flex: none;
}

/* ===== 表格：行沿用「端点」页那套卡片化（见 capabilities/EndpointsTab.vue 的 .et-table）
   一行 = 一张卡片：容器是灰底、行是白底，行与行之间靠 3px 的「上边框」留缝。
   端点用 td 的上边框做同一件事，这里换成 v2 的 div 行，手法不变。
   差别只在 v2 靠 row-height 固定行高（虚拟滚动要它算渲染窗口），
   所以留缝吃掉的 3px 从行内高度里出，不能像 td 那样自然撑开。
   表头不跟着卡片化：改成「运行历史」那种标准表头（白底 / 灰字 / 1px 下边线），
   否则同一个站点里表头一套、表体一套，两种观感。 */
.running-body {
  position: relative;
  flex: 1;
  min-height: 0;
  background: #f5f7fa;
  border-radius: 8px;
  box-shadow: 0 2px 8px rgba(15, 23, 42, 0.06);
  overflow: hidden;
}

/* AutoResizer 靠这个容器的确定高度量出可视区，量不到高度虚拟滚动就没有窗口 */
.running-body :deep(.el-auto-resizer) {
  height: 100%;
}

/* 表头：对齐 el-table（运行历史页那张表）的默认观感 —— 白底、12px / #909399 / 600、
   1px 下边线。去掉原来的白卡片 + 轻阴影 + 两端圆角，那套把表头做成了「浮起来的一行」，
   和下面对齐到边界的表体不是一路。 */
.running-body :deep(.el-table-v2__header) {
  background: #fff;
  box-shadow: none;
}

.running-body :deep(.el-table-v2__header-row) {
  border-bottom: 1px solid #ebeef5;
}

.running-body :deep(.el-table-v2__header-cell) {
  background: #fff;
  font-size: 12px;
  font-weight: 600;
  color: #909399;
  border-bottom: none;
}

/* 行卡片：白底、无下边线；3px 上边框与容器同色 = 纯留白 */
.running-body :deep(.el-table-v2__row) {
  box-sizing: border-box;
  background: #fff;
  border-bottom: none;
  border-top: 3px solid #f5f7fa;
}

/* 悬停整行提亮：终止按钮就在这一行，鼠标扫过要能确认落在哪一条。
   .is-hovered 必须一起写：固定列是另一层渲染的行，v2 靠这个类把「那一行」同步亮起来，
   只写 :hover 会出现「鼠标在右侧固定列上、左侧行不亮」的半截效果。 */
.running-body :deep(.el-table-v2__row:hover),
.running-body :deep(.el-table-v2__row.is-hovered) {
  background: #f0f6ff;
}

/* 跑太久的行：左侧一条橙线（inset 阴影不占列宽，不会挤歪对齐） */
.running-body :deep(.el-table-v2__row.is-overdue) {
  box-shadow: inset 3px 0 0 #f59e0b;
}

/* 单元格：纵向不再加 padding（行高由 row-height 控），横向留 12px 呼吸。
   注意类名是 __row-cell —— v2 没有 __cell 这个类 */
.running-body :deep(.el-table-v2__row-cell) {
  display: flex;
  align-items: center;
  padding: 0 12px;
  overflow: hidden;
}

/* 空态：与端点页同一套（标题 + 说明，纵向居中） */
.running-empty {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 6px;
  height: 100%;
}

.running-empty-title {
  font-size: 14px;
  font-weight: 600;
  color: #606266;
}

.running-empty-tip {
  font-size: 12px;
  color: #909399;
}

/* ---- 单元格内的排版（cellRenderer 渲染的节点在本组件作用域外，统一走 :deep） ---- */
.running-body :deep(.run-cell) {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}

.running-body :deep(.run-name) {
  font-size: 13px;
  font-weight: 600;
  color: #303133;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

/* 次要信息（run id / 会话 / 实例）：几列都是 id，保留等宽字体才数得清前缀 */
.running-body :deep(.run-sub) {
  font-size: 12px;
  color: #909399;
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

/* 用户列显示名字（不是 id），所以用正文字体，不用等宽 */
.running-body :deep(.run-user) {
  font-size: 13px;
  color: #606266;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.running-body :deep(.run-dur) {
  font-size: 13px;
  color: #606266;
  font-variant-numeric: tabular-nums;
}

/* 超时那一格变橙：与行左的橙线一起指向「这条该看看了」 */
.running-body :deep(.run-dur.is-over) {
  color: #f59e0b;
  font-weight: 600;
}

.running-body :deep(.run-instance) {
  display: flex;
  align-items: center;
  min-width: 0;
}

/* 实例标识在 flex 行里：不给 min-width: 0 它会按内容撑开，
   overflow + ellipsis 就失效（长 host:port 会顶出去，把「本机」标签挤掉） */
.running-body :deep(.run-instance .run-sub) {
  min-width: 0;
}

.running-body :deep(.run-local) {
  margin-left: 6px;
  flex-shrink: 0;
}

/* 操作列：按钮靠右 */
.running-body :deep(.run-actions) {
  display: flex;
  align-items: center;
  justify-content: flex-end;
  width: 100%;
  white-space: nowrap;
}
</style>
