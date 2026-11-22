<template>
  <div ref="panelAnchorRef" class="rh">
    <!-- 筛选条：这里的条件是「查询 / 导出 / 清理」三者共用的同一份，
         否则很容易出现「看到 300 条、导出 20 条、清理全租户」这种自相矛盾。 -->
    <div class="rh-filters">
      <el-select
        v-model="filter.flowId"
        class="rh-flow"
        size="small"
        clearable
        filterable
        :placeholder="$t('history.placeholderFlow')"
        @change="reload"
      >
        <el-option v-for="f in flows" :key="f.id" :label="f.name" :value="f.id" />
      </el-select>

      <!-- Caller: who triggered this run. Empty = every caller, including runs with no identity and the history
           recorded before the column existed. -->
      <el-select
        v-model="filter.userId"
        class="rh-user"
        size="small"
        clearable
        filterable
        :placeholder="$t('history.placeholderUser')"
        @change="reload"
      >
        <el-option v-for="u in userOptions" :key="u.id" :label="userOptionLabel(u)" :value="u.id" />
      </el-select>

      <!-- Session filter: exact match on the session uuid. Most flow runs have no session at all (the debugger, a
           plain call), so an empty box is the normal case, not a missing one. -->
      <el-input
        v-model="filter.sessionId"
        class="rh-session"
        size="small"
        clearable
        :placeholder="$t('history.placeholderSession')"
        @change="reload"
        @clear="reload"
        @keyup.enter="reload"
      />

      <el-select v-model="filter.status" class="rh-status" size="small" clearable :placeholder="$t('history.placeholderStatus')" @change="reload">
        <el-option :label="$t('history.statusSuccess')" value="success" />
        <el-option :label="$t('history.statusFailed')" value="failed" />
        <el-option :label="$t('history.statusCancelled')" value="cancelled" />
      </el-select>

      <!-- 时间段：预设收进 shortcuts（点一下 = 从当前时刻往前推），也可以自己拖两个时间点。
           两者最终都落到同一对 start_from / start_to，所以列表 / 导出 / 清理自动同参 -->
      <el-date-picker
        v-model="filter.span"
        class="rh-span"
        type="datetimerange"
        size="small"
        :shortcuts="TIME_SHORTCUTS"
        :default-time="DEFAULT_TIME"
        range-separator="→"
        :start-placeholder="$t('history.startPlaceholder')"
        :end-placeholder="$t('history.endPlaceholder')"
        @change="onSpanChange"
      />

      <el-button size="small" :loading="loading" @click="reload">{{ $t('history.query') }}</el-button>

      <span class="rh-total">
        {{ $t('history.total', { total }) }}<template v-if="items.length && items.length < total">{{ $t('history.loaded', { loaded: items.length }) }}</template>
      </span>

      <div class="rh-spacer" />

      <el-button size="small" :disabled="!total" @click="exportCsv">{{ $t('history.exportCsv') }}</el-button>
      <!-- 没选任何条件时不让点：默认那 24 小时不算条件（见 hasCondition），
           否则进页面第一眼就能一键清掉最近一天 -->
      <el-button size="small" type="danger" plain :disabled="!total || !hasCondition()" @click="cleanup">{{ $t('history.cleanup') }}</el-button>
    </div>

    <div class="rh-body">
      <!-- 高度交给 AutoResizer 量：父级是 flex 布局，el-table 直接写 height="100%"
           在有的场景量不到（拿到 0 就只剩表头），这里按实际可视区给像素值 -->
      <el-auto-resizer>
        <template #default="{ height }">
          <el-table
            v-loading="loading"
            :data="items"
            size="small"
            :height="height"
            row-key="id"
            @row-click="openDetail"
          >
            <el-table-column :label="$t('history.colStart')" width="168">
              <template #default="{ row }">{{ fmtTime(row.started_at) }}</template>
            </el-table-column>

            <el-table-column :label="$t('history.colFlow')" min-width="180" show-overflow-tooltip>
              <template #default="{ row }">
                <span class="rh-name">{{ row.flow_name || shortId(row.flow_id) }}</span>
              </template>
            </el-table-column>

            <el-table-column :label="$t('history.colUser')" min-width="120" show-overflow-tooltip>
              <template #default="{ row }">
                <span :class="{ 'rh-muted': !row.user_id }">{{ userText(row) }}</span>
              </template>
            </el-table-column>

            <el-table-column :label="$t('history.colEntry')" width="96">
              <template #default="{ row }">{{ runTypeLabel(row.run_type) }}</template>
            </el-table-column>

            <el-table-column :label="$t('history.colStatus')" width="88">
              <template #default="{ row }">
                <el-tag size="small" :type="statusType(row.status)" effect="plain">{{ statusText(row.status) }}</el-tag>
              </template>
            </el-table-column>

            <el-table-column :label="$t('history.colDuration')" width="96" align="right">
              <template #default="{ row }">{{ fmtDuration(row.duration_ms) }}</template>
            </el-table-column>

            <el-table-column :label="$t('history.colNodes')" width="72" align="right">
              <template #default="{ row }">{{ row.node_count }}</template>
            </el-table-column>

            <el-table-column :label="$t('history.colError')" min-width="160" show-overflow-tooltip>
              <template #default="{ row }">
                <span :class="{ 'rh-err': !!row.err_msg }">{{ row.err_msg || '—' }}</span>
              </template>
            </el-table-column>

            <el-table-column width="56" align="center">
              <template #default="{ row }">
                <el-icon class="rh-del" :title="$t('history.deleteRow')" @click.stop="deleteOne(row)">
                  <Delete />
                </el-icon>
              </template>
            </el-table-column>
          </el-table>
        </template>
      </el-auto-resizer>
    </div>

    <div class="rh-more">
      <el-button v-if="items.length < total" size="small" :loading="loadingMore" @click="loadMore">
        {{ $t('history.loadMore') }}
      </el-button>
      <span v-else-if="items.length" class="rh-end">{{ $t('history.noMore') }}</span>
      <span v-else-if="!loading" class="rh-end">{{ $t('history.noMatch') }}</span>
    </div>

    <!-- Detail panel: only answers "what did this run actually do". Node-level variables and logs live in the flow
         editor's run panel (it has the full context); this page does not rebuild that. Same shell and position as
         the agent settings window. -->
    <HistoryDetailPanel
      :visible="detailVisible"
      :anchor="panelAnchor || panelAnchorRef"
      :title="$t('history.detailTitle')"
      @close="detailVisible = false"
    >
      <div v-if="detail" class="rh-detail">
        <div class="rh-dt-row"><span>{{ $t('history.runId') }}</span><b class="rh-mono">{{ detail.run.id }}</b></div>
        <div class="rh-dt-row"><span>{{ $t('history.colFlow') }}</span><b>{{ detail.flowName || shortId(detail.run.flow_id) }}</b></div>
        <div class="rh-dt-row"><span>{{ $t('history.colUser') }}</span><b>{{ userText(detail.run) }}</b></div>
        <div class="rh-dt-row">
          <span>{{ $t('history.colSession') }}</span>
          <b class="rh-dt-session">
            {{ detail.run.session_id ? shortId(detail.run.session_id) : $t('history.noSession') }}
            <el-button
              v-if="detail.run.session_id"
              link
              type="primary"
              size="small"
              @click="filterBySession(detail.run.session_id)"
            >{{ $t('history.filterThisSession') }}</el-button>
          </b>
        </div>
        <div class="rh-dt-row"><span>{{ $t('history.colStatus') }}</span>
          <el-tag size="small" :type="statusType(detail.run.status)" effect="plain">{{ statusText(detail.run.status) }}</el-tag>
        </div>
        <div class="rh-dt-row"><span>{{ $t('history.colEntry') }}</span><b>{{ runTypeLabel(detail.run.run_type) }}</b></div>
        <div class="rh-dt-row"><span>{{ $t('history.startEnd') }}</span>
          <b>{{ fmtTime(detail.run.started_at) }} → {{ detail.run.finished_at ? fmtTime(detail.run.finished_at) : '—' }}</b>
        </div>
        <div class="rh-dt-row"><span>{{ $t('history.colDuration') }}</span><b>{{ fmtDuration(detail.run.duration_ms) }}</b></div>
        <div v-if="detail.run.err_msg" class="rh-dt-row"><span>{{ $t('history.colError') }}</span>
          <b class="rh-err">{{ detail.run.err_msg }}</b>
        </div>

        <div class="rh-dt-title">{{ $t('history.nodesTitle', { count: detail.nodes.length }) }}</div>
        <el-table :data="detail.nodes" size="small" max-height="420">
          <el-table-column :label="$t('history.colNodes')" min-width="120" show-overflow-tooltip>
            <template #default="{ row }">{{ row.node_id }}<span class="rh-type"> · {{ row.node_type }}</span></template>
          </el-table-column>
          <el-table-column :label="$t('history.colStatus')" width="80">
            <template #default="{ row }">
              <span :class="statusClass(row.status)">{{ statusText(row.status) }}</span>
            </template>
          </el-table-column>
          <el-table-column :label="$t('history.colDuration')" width="80" align="right">
            <template #default="{ row }">{{ fmtDuration(row.duration_ms) }}</template>
          </el-table-column>
          <el-table-column :label="$t('history.colError')" min-width="140" show-overflow-tooltip>
            <template #default="{ row }">
              <span v-if="row.err_code || row.err_msg" class="rh-err">
                {{ row.err_code || '' }}{{ row.err_code && row.err_msg ? ': ' : '' }}{{ row.err_msg || '' }}
              </span>
              <span v-else>—</span>
            </template>
          </el-table-column>
        </el-table>
      </div>
    </HistoryDetailPanel>
  </div>
</template>

<script setup>
import { ref, reactive, computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Delete } from '@element-plus/icons-vue'
import HistoryDetailPanel from './HistoryDetailPanel.vue'
import { getAuthHeader } from '@/tenant/utils/auth.js'
import adminApi from '@/tenant/config/admin.js'
import { userOptions, userOptionLabel, ensureUserOptions } from './settings/useUsers.js'
import { formatError } from '../../locales'

const { t } = useI18n()

// Page box the detail panel is aligned to (border box, padding included). Falls back to this list's own box.
const props = defineProps({
  panelAnchor: { type: Object, default: null }
})

const PAGE_SIZE = 20

// 时间范围：预设只负责「把区间填进去」（点一下 = 从当前时刻往前推），也可以自己选两个时间点。
// 查历史 99% 的场景是「刚才 / 今天 / 这周」，所以常用区间做成 shortcut，不逼用户手填时间戳。
// 用 computed 而不是常量：shortcut 的 text 是渲染文案，切换语言后要跟着变。
const TIME_SHORTCUTS = computed(() => [
  { text: t('history.shortcutLast1h'), value: () => [new Date(Date.now() - 3600e3), new Date()] },
  { text: t('history.shortcutLast24h'), value: () => [new Date(Date.now() - 24 * 3600e3), new Date()] },
  {
    text: t('history.shortcutToday'),
    value: () => {
      const start = new Date()
      start.setHours(0, 0, 0, 0)
      return [start, new Date()]
    }
  },
  { text: t('history.shortcutLast7d'), value: () => [new Date(Date.now() - 7 * 24 * 3600e3), new Date()] },
  { text: t('history.shortcutLast30d'), value: () => [new Date(Date.now() - 30 * 24 * 3600e3), new Date()] }
])

// 只选日期时给起止补默认时刻：否则「11-17 到 11-17」会变成那一刻到那一刻，等于空区间
const DEFAULT_TIME = [new Date(2000, 0, 1, 0, 0, 0), new Date(2000, 0, 1, 23, 59, 59)]

// 落库的 run_type 只有三种（见 executors.runTypeOf）：call = 正式调用（入口进线），
// debug = 编辑页调试器（可断点/单步），preview = 试跑（跑草稿但没有调试器）
const RUN_TYPE_KEYS = {
  call: 'history.runTypeCall',
  debug: 'history.runTypeDebug',
  preview: 'history.runTypePreview'
}
// 参数名不能叫 t —— 会遮蔽 useI18n 的 t，变成翻译函数调用自己
const runTypeLabel = (v) => (RUN_TYPE_KEYS[v] ? t(RUN_TYPE_KEYS[v]) : v || '—')

const filter = reactive({
  flowId: '',
  // Caller filter: which user triggered the run. Empty = every caller, including runs with no identity.
  userId: '',
  // Session filter: which runs belong to this conversation. Exact uuid; empty means every session, including the
  // runs that have none.
  sessionId: '',
  status: '',
  // 默认最近 24 小时。注意这是「进页面那一刻」算出的固定区间，挂着不会自己往后滑；
  // 想重新对齐到当前时刻，点一次快捷键即可 —— 界面上看到的始终是两个明确的时间点。
  span: [new Date(Date.now() - 24 * 3600e3), new Date()]
})

// 时间段是不是用户自己选的。默认那 24 小时只是「先给个合理起点」，
// 不能因此让「清理」在什么都没选时就可点 —— 那等于一键删掉最近一天的全部运行记录。
const spanTouched = ref(false)

const flows = ref([])
const items = ref([])
const total = ref(0)
const loading = ref(false)
const loadingMore = ref(false)
const offset = ref(0)

const detailVisible = ref(false)
const detail = ref(null)

// Anchors the detail panel: it takes this list's top, height and right edge, the way the agent settings
// window takes the editor's (see HistoryDetailPanel.vue).
const panelAnchorRef = ref(null)

const shortId = (id) => (id ? String(id).slice(0, 8) : '—')

// Prefer the name the backend joined in; fall back to the short id (the user was deleted after the run) or a muted
// "no identity" (an API-key / scheduled trigger, or a record written before the column had a value).
const userNameOf = (id) => {
  const u = userOptions.value.find((x) => x.id === id)
  return u ? userOptionLabel(u) : ''
}
// The list endpoint already joins the name; the detail payload carries only the id, so it falls back to the module-level
// user list loaded on mount (and then to the short id).
const userText = (row) =>
  row.user_name || (row.user_id ? userNameOf(row.user_id) || shortId(row.user_id) : t('history.noUser'))

const STATUS_KEYS = {
  success: 'history.statusSuccess',
  failed: 'history.statusFailed',
  cancelled: 'history.statusCancelled'
}
const statusText = (s) => (STATUS_KEYS[s] ? t(STATUS_KEYS[s]) : s || '-')
const statusType = (s) => ({ success: 'success', failed: 'danger', cancelled: 'info' }[s] || 'info')
const statusClass = (s) => ({ success: 'rh-ok', failed: 'rh-err', cancelled: 'rh-cancel' }[s] || '')

const fmtTime = (v) => {
  if (!v) return '—'
  const d = new Date(v)
  if (Number.isNaN(d.getTime())) return '—'
  const p = (n) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
}

const fmtDuration = (ms) => {
  if (!Number.isFinite(ms) || ms < 0) return '—'
  const s = Math.floor(ms / 1000)
  if (s < 60) return `${s}s`
  const m = Math.floor(s / 60)
  if (m < 60) return `${m}m ${s % 60}s`
  return `${Math.floor(m / 60)}h ${m % 60}m`
}

// 清理确认框要说清「删的是哪一段」：选了区间就把起止都写出来，没选就是全部时间。
// 不复用快捷键文案 —— 同一条「最近 24 小时」在选完之后已经是两个固定时间点，说法要跟数据一致。
const spanText = () => {
  const [from, to] = filter.span || []
  if (!from || !to) return t('history.all')
  return `${fmtTime(from)} → ${fmtTime(to)}`
}

// buildParams 是查询 / 导出 / 清理的唯一出口：三处必须同参，否则「看到的」与「删掉的」不是一批
const buildParams = () => {
  const p = new URLSearchParams()
  if (filter.flowId) p.set('flow_id', filter.flowId)
  if (filter.userId) p.set('user_id', filter.userId)
  const sid = filter.sessionId.trim()
  if (sid) p.set('session_id', sid)
  if (filter.status) p.set('status', filter.status)
  // 统一 toISOString（UTC，Z 结尾）：后端按 time.RFC3339 解析、timestamptz 比较，前端时区不影响结果
  const [from, to] = filter.span || []
  if (from) p.set('start_from', new Date(from).toISOString())
  if (to) p.set('start_to', new Date(to).toISOString())
  return p
}

// 清理的保护：至少有一个条件。时间段只在「用户自己动过」时才算条件 ——
// 默认的 24 小时不算，否则一进页面就能清掉最近一天
const hasCondition = () => !!(filter.flowId || filter.userId || filter.sessionId.trim() || filter.status
  || (spanTouched.value && filter.span?.length === 2))

const loadFlows = async () => {
  try {
    const resp = await fetch(adminApi.flows.base, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', ...getAuthHeader() },
      body: JSON.stringify({ page: 1, limit: -1 })
    })
    const data = await resp.json()
    if (data.code === 200) flows.value = data.data?.items || []
  } catch {
    // 拉不到工作流列表不影响历史本身：下拉留空、按 flow_id 一样能筛
  }
}

const fetchPage = async (reset) => {
  const params = buildParams()
  params.set('limit', String(PAGE_SIZE))
  params.set('offset', String(reset ? 0 : offset.value))

  const resp = await fetch(adminApi.runningHistory(params.toString()), { headers: { ...getAuthHeader() } })
  const data = await resp.json()
  if (data.code !== 200) throw new Error(formatError(data) || t('history.queryFailed'))

  const page = data.data?.items || []
  total.value = data.data?.total || 0
  items.value = reset ? page : items.value.concat(page)
  offset.value = items.value.length
}

const reload = async () => {
  loading.value = true
  try {
    await fetchPage(true)
  } catch (e) {
    ElMessage.error(e?.message || t('history.queryFailed'))
  } finally {
    loading.value = false
  }
}

// 时间段一变就记下「用户动过」：date-picker 的 change 只在确认后触发，
// 点快捷键、手选区间、清空都会走到这里
const onSpanChange = () => {
  spanTouched.value = true
  reload()
}

// Jump from "this run" to "this conversation": the drawer is where the uuid is at hand, and it is the only sane
// entry point for the filter. The time range is cleared on purpose, because "this session" plus the default 24h
// window would silently hide the conversation's older runs.
const filterBySession = (sid) => {
  if (!sid) return
  filter.sessionId = sid
  filter.span = null
  spanTouched.value = false
  detailVisible.value = false
  reload()
}

const loadMore = async () => {
  loadingMore.value = true
  try {
    await fetchPage(false)
  } catch (e) {
    ElMessage.error(e?.message || t('history.loadFailed'))
  } finally {
    loadingMore.value = false
  }
}

const openDetail = async (row) => {
  detail.value = null
  detailVisible.value = true
  try {
    const resp = await fetch(adminApi.flows.run(row.flow_id, row.id), { headers: { ...getAuthHeader() } })
    const data = await resp.json()
    if (data.code === 200) {
      detail.value = { run: data.data.run, nodes: data.data.nodes || [], flowName: row.flow_name }
    } else {
      ElMessage.error(formatError(data) || t('history.detailFailed'))
    }
  } catch (e) {
    ElMessage.error(e?.message || t('history.detailFailed'))
  }
}

const deleteOne = async (row) => {
  try {
    await ElMessageBox.confirm(
      t('history.deleteConfirm', {
        name: row.flow_name || shortId(row.flow_id),
        time: fmtTime(row.started_at)
      }),
      t('history.deleteTitle'), { type: 'warning' }
    )
  } catch { return }

  try {
    const resp = await fetch(adminApi.flows.run(row.flow_id, row.id), {
      method: 'DELETE',
      headers: { ...getAuthHeader() }
    })
    const data = await resp.json()
    if (data.code !== 200) throw new Error(formatError(data) || t('history.deleteFailed'))

    // 本地先剔除再回拉一页补齐：直接 reload 会把用户从列表深处弹回顶部
    items.value = items.value.filter((r) => r.id !== row.id)
    total.value = Math.max(0, total.value - 1)
    offset.value = Math.max(0, offset.value - 1)
    ElMessage.success(t('history.deleted'))
    if (items.value.length < total.value) {
      // 补一页补齐列表：失败也不该盖掉上面那条「已删除」，尾部少一条下次刷新就回来了
      fetchPage(false).catch(() => {})
    }
  } catch (e) {
    ElMessage.error(e?.message || t('history.deleteFailed'))
  }
}

const cleanup = async () => {
  if (!hasCondition()) {
    ElMessage.warning(t('history.cleanupNoCondition'))
    return
  }
  const desc = [
    filter.flowId
      ? t('history.descFlow', { name: flows.value.find((f) => f.id === filter.flowId)?.name || shortId(filter.flowId) })
      : t('history.descFlow', { name: t('history.all') }),
    filter.userId
      ? t('history.descUser', { name: userNameOf(filter.userId) || shortId(filter.userId) })
      : t('history.descUser', { name: t('history.all') }),
    filter.status
      ? t('history.descStatus', { name: statusText(filter.status) })
      : t('history.descStatus', { name: t('history.all') }),
    t('history.descTime', { value: spanText() })
  ].join('; ')

  try {
    await ElMessageBox.confirm(
      t('history.cleanupConfirm', { total: total.value, desc }),
      t('history.cleanupTitle'), { type: 'warning', confirmButtonText: t('history.cleanupOk') }
    )
  } catch { return }

  try {
    const resp = await fetch(adminApi.runningHistory(buildParams().toString()), {
      method: 'DELETE',
      headers: { ...getAuthHeader() }
    })
    const data = await resp.json()
    if (data.code !== 200) throw new Error(formatError(data) || t('history.cleanupFailed'))
    ElMessage.success(t('history.cleanupDone', { count: data.data?.deleted ?? 0 }))
    reload()
  } catch (e) {
    ElMessage.error(e?.message || t('history.cleanupFailed'))
  }
}

const exportCsv = async () => {
  try {
    const resp = await fetch(adminApi.runningHistoryExport(buildParams().toString()), {
      headers: { ...getAuthHeader() }
    })
    // 后端出错时返回的是 JSON 信封（HTTP 200 + code 400），先看类型再当文件下载，
    // 否则会把一段错误 JSON 存成 .csv，用户打开才发现是空的
    const ct = resp.headers.get('content-type') || ''
    if (!ct.includes('csv')) {
      const data = await resp.json()
      throw new Error(formatError(data) || t('history.exportFailed'))
    }
    const blob = await resp.blob()
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = `runs-${new Date().toISOString().slice(0, 19).replace(/[:T]/g, '')}.csv`
    a.click()
    URL.revokeObjectURL(url)
  } catch (e) {
    ElMessage.error(e?.message || t('history.exportFailed'))
  }
}

onMounted(() => {
  loadFlows()
  // Shared with the settings pages: one module-level list, so a user created elsewhere shows up here too.
  ensureUserOptions()
  reload()
})

// 供父组件在切到本 tab 时主动刷新，避免停在别的 tab 时白跑查询
defineExpose({ reload })
</script>

<style scoped>
.rh {
  display: flex;
  flex-direction: column;
  height: 100%;
  min-height: 0;
  gap: 12px;
}

/* 与运行中 / 端点页同一张白卡片：两个 tab 切来切去，工具条外观必须一模一样 */
.rh-filters {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
  padding: 10px 14px;
  background: #fff;
  border: 1px solid #ebeef5;
  border-radius: 8px;
}

.rh-flow { width: 160px; }
.rh-user { width: 150px; }
.rh-session { width: 210px; }
.rh-status { width: 110px; }
.rh-muted { color: #a8abb2; }
/* 起止两个时间 + 分隔符：360px 刚好一屏放得下，再窄就会截断成「…」 */
.rh-span { width: 360px; }
.rh-total { font-size: 13px; color: #606266; font-variant-numeric: tabular-nums; }
.rh-spacer { flex: 1; }

.rh-body { flex: 1; min-height: 0; }

/* AutoResizer 靠这个容器的确定高度量出可视区，量不到就没有表格高度（只剩表头） */
.rh-body :deep(.el-auto-resizer) {
  height: 100%;
}

.rh-name { font-size: 13px; color: #303133; }
.rh-err { color: #f56c6c; }
.rh-ok { color: #67c23a; }
.rh-cancel { color: #909399; }
.rh-type { color: #909399; font-size: 12px; }

.rh-del {
  cursor: pointer;
  color: #c0c4cc;
}
.rh-del:hover { color: #f56c6c; }

.rh-more {
  display: flex;
  justify-content: center;
  padding-bottom: 4px;
}
.rh-end { font-size: 12px; color: #909399; }

.rh-detail { display: flex; flex-direction: column; gap: 8px; }
.rh-dt-row { display: flex; gap: 10px; font-size: 13px; }
.rh-dt-row > span { flex: 0 0 92px; color: #909399; }
.rh-dt-row > b { font-weight: 500; color: #303133; word-break: break-all; }
.rh-mono { font-family: Consolas, Monaco, monospace; font-size: 12px; }
/* Session label plus its "filter by this session" action on one line */
.rh-dt-session { display: inline-flex; align-items: center; gap: 8px; }
.rh-dt-title { margin-top: 10px; font-size: 13px; font-weight: 600; color: #303133; }
</style>
