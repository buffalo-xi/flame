<template>
  <div ref="panelAnchorRef" class="ah">
    <!-- Filters: one set of conditions shared by the list / export / cleanup, so "seen", "exported" and
         "deleted" can never be three different sets. -->
    <div class="ah-filters">
      <el-select
        v-model="filter.agentId"
        class="ah-agent"
        size="small"
        clearable
        filterable
        :placeholder="$t('agentHistory.placeholderAgent')"
        @change="reload"
      >
        <el-option v-for="a in agents" :key="a.id" :label="a.name" :value="a.id" />
      </el-select>

      <!-- Caller filter: who triggered the run. The list is the tenant's users; a run with no identity is only
           reachable by leaving this empty, which is also how "all callers" is expressed. -->
      <el-select
        v-model="filter.userId"
        class="ah-user"
        size="small"
        clearable
        filterable
        :placeholder="$t('agentHistory.placeholderUser')"
        @change="reload"
      >
        <el-option v-for="u in userOptions" :key="u.id" :label="userOptionLabel(u)" :value="u.id" />
      </el-select>

      <!-- Session filter: exact match on the session uuid. It is normally reached from the detail drawer
           ("filter by this session") - nobody types a uuid from memory. -->
      <el-input
        v-model="filter.sessionId"
        class="ah-session"
        size="small"
        clearable
        :placeholder="$t('agentHistory.placeholderSession')"
        @change="reload"
        @clear="reload"
        @keyup.enter="reload"
      />

      <el-select
        v-model="filter.status"
        class="ah-status"
        size="small"
        clearable
        :placeholder="$t('agentHistory.placeholderStatus')"
        @change="reload"
      >
        <el-option :label="$t('agentHistory.statusOk')" value="ok" />
        <el-option :label="$t('agentHistory.statusStopped')" value="stopped" />
        <el-option :label="$t('agentHistory.statusFailed')" value="failed" />
      </el-select>

      <el-date-picker
        v-model="filter.span"
        class="ah-span"
        type="datetimerange"
        size="small"
        :shortcuts="TIME_SHORTCUTS"
        :default-time="DEFAULT_TIME"
        range-separator="→"
        :start-placeholder="$t('history.startPlaceholder')"
        :end-placeholder="$t('history.endPlaceholder')"
        @change="onSpanChange"
      />

      <el-button size="small" :loading="loading" @click="reload">{{ $t('agentHistory.query') }}</el-button>

      <span class="ah-total">
        {{ $t('agentHistory.total', { total }) }}<template v-if="items.length && items.length < total">{{ $t('agentHistory.loaded', { loaded: items.length }) }}</template>
      </span>

      <div class="ah-spacer" />

      <el-button size="small" :disabled="!total" @click="exportCsv">{{ $t('agentHistory.exportCsv') }}</el-button>
      <!-- The default 24-hour span does not count as a condition (see hasCondition): otherwise the first
           screen would allow wiping the last day in one click. -->
      <el-button size="small" type="danger" plain :disabled="!total || !hasCondition()" @click="cleanup">{{ $t('agentHistory.cleanup') }}</el-button>
    </div>

    <div class="ah-body">
      <!-- AutoResizer needs a container with a definite height, otherwise the table measures 0 and shows only
           the header (same reason as the workflow history). -->
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
            <el-table-column :label="$t('agentHistory.colStart')" width="168">
              <template #default="{ row }">{{ fmtTime(row.started_at) }}</template>
            </el-table-column>

            <el-table-column :label="$t('agentHistory.colAgent')" min-width="150" show-overflow-tooltip>
              <template #default="{ row }">
                <span class="ah-name">{{ row.agent_name || shortId(row.agent_id) }}</span>
              </template>
            </el-table-column>

            <el-table-column :label="$t('agentHistory.colSession')" min-width="150" show-overflow-tooltip>
              <template #default="{ row }">
                <span :class="{ 'ah-muted': !row.session_id }">{{ sessionLabel(row) }}</span>
              </template>
            </el-table-column>

            <el-table-column :label="$t('agentHistory.colUser')" min-width="120" show-overflow-tooltip>
              <template #default="{ row }">
                <span :class="{ 'ah-muted': !row.user_id }">{{ userText(row) }}</span>
              </template>
            </el-table-column>

            <el-table-column :label="$t('agentHistory.colEntry')" width="104">
              <template #default="{ row }">{{ runTypeLabel(row.run_type) }}</template>
            </el-table-column>

            <el-table-column :label="$t('agentHistory.colStatus')" width="96">
              <template #default="{ row }">
                <el-tag size="small" :type="statusType(row.status)" effect="plain">{{ statusText(row.status) }}</el-tag>
              </template>
            </el-table-column>

            <el-table-column :label="$t('agentHistory.colDuration')" width="96" align="right">
              <template #default="{ row }">{{ fmtDuration(row.duration_ms) }}</template>
            </el-table-column>

            <el-table-column :label="$t('agentHistory.colQuestion')" min-width="240" show-overflow-tooltip>
              <template #default="{ row }">{{ row.question || '—' }}</template>
            </el-table-column>

            <el-table-column :label="$t('agentHistory.colError')" min-width="160" show-overflow-tooltip>
              <template #default="{ row }">
                <span :class="{ 'ah-err': !!row.err_msg }">{{ row.err_msg || '—' }}</span>
              </template>
            </el-table-column>

            <el-table-column width="56" align="center">
              <template #default="{ row }">
                <el-icon class="ah-del" :title="$t('agentHistory.deleteRow')" @click.stop="deleteOne(row)">
                  <Delete />
                </el-icon>
              </template>
            </el-table-column>
          </el-table>
        </template>
      </el-auto-resizer>
    </div>

    <div class="ah-more">
      <el-button v-if="items.length < total" size="small" :loading="loadingMore" @click="loadMore">
        {{ $t('agentHistory.loadMore') }}
      </el-button>
      <span v-else-if="items.length" class="ah-end">{{ $t('agentHistory.noMore') }}</span>
      <span v-else-if="!loading" class="ah-end">{{ $t('agentHistory.noMatch') }}</span>
    </div>

    <!-- Detail panel: answers "what did this run actually do". Same shell and position as the agent settings window
         (agents/AgentSettingsPanel.vue): flush with the content area's right edge, the list's top and height.
         ESC / outside click closes. The answer shown here is the bounded preview stored in the log; the full text
         belongs to the conversation and is read on the chat page. -->
    <HistoryDetailPanel
      :visible="detailVisible"
      :anchor="panelAnchor || panelAnchorRef"
      :title="$t('agentHistory.detailTitle')"
      @close="detailVisible = false"
    >
      <div v-if="detail" class="ah-detail">
        <div class="ah-dt-row"><span>{{ $t('agentHistory.runId') }}</span><b class="ah-mono">{{ detail.run_id }}</b></div>
        <div class="ah-dt-row"><span>{{ $t('agentHistory.colAgent') }}</span><b>{{ detail.agent_name || shortId(detail.agent_id) }}</b></div>
        <div class="ah-dt-row">
          <span>{{ $t('agentHistory.colSession') }}</span>
          <b class="ah-dt-session">
            {{ sessionLabel(detail) }}
            <el-button
              v-if="detail.session_id"
              link
              type="primary"
              size="small"
              @click="filterBySession(detail.session_id)"
            >{{ $t('agentHistory.filterThisSession') }}</el-button>
          </b>
        </div>
        <div class="ah-dt-row">
          <span>{{ $t('agentHistory.colUser') }}</span>
          <b>{{ userText(detail) }}</b>
        </div>
        <div class="ah-dt-row"><span>{{ $t('agentHistory.colStatus') }}</span>
          <el-tag size="small" :type="statusType(detail.status)" effect="plain">{{ statusText(detail.status) }}</el-tag>
        </div>
        <div class="ah-dt-row"><span>{{ $t('agentHistory.colEntry') }}</span><b>{{ runTypeLabel(detail.run_type) }}</b></div>
        <div class="ah-dt-row"><span>{{ $t('agentHistory.startEnd') }}</span>
          <b>{{ fmtTime(detail.started_at) }} → {{ detail.finished_at ? fmtTime(detail.finished_at) : '—' }}</b>
        </div>
        <div class="ah-dt-row"><span>{{ $t('agentHistory.colDuration') }}</span><b>{{ fmtDuration(detail.duration_ms) }}</b></div>
        <div v-if="detail.err_msg" class="ah-dt-row"><span>{{ $t('agentHistory.colError') }}</span>
          <b class="ah-err">{{ detail.err_msg }}</b>
        </div>

        <div class="ah-dt-title">{{ $t('agentHistory.questionTitle') }}</div>
        <pre class="ah-pre">{{ detail.question || '—' }}</pre>

        <div class="ah-dt-title">{{ $t('agentHistory.answerTitle') }}</div>
        <pre class="ah-pre ah-pre--answer">{{ detail.answer || '—' }}</pre>
        <div v-if="detail.answer_truncated" class="ah-hint">
          {{ $t('agentHistory.answerTruncated', { size: fmtSize(detail.answer_size) }) }}
        </div>
      </div>
    </HistoryDetailPanel>
  </div>
</template>

<script setup>
import { ref, reactive, computed, watch, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Delete } from '@element-plus/icons-vue'
import HistoryDetailPanel from './HistoryDetailPanel.vue'
import { getAuthHeader } from '@/tenant/utils/auth.js'
import adminApi from '@/tenant/config/admin.js'
import { userOptions, userOptionLabel, ensureUserOptions } from './settings/useUsers.js'
import { formatError } from '../../locales'

const { t } = useI18n()

// presetSession comes from a cross-page jump (the chat's "view this session's runs"): { session_id, ts }.
// The object is rebuilt on every request so the same session can be requested twice.
const props = defineProps({
  presetSession: { type: Object, default: null },
  // Page box the detail panel is aligned to (border box, padding included). Falls back to this list's own box.
  panelAnchor: { type: Object, default: null }
})

const PAGE_SIZE = 20

// Range presets only fill the picker in; both a preset and a hand-picked range end up as the same
// start_from / start_to pair, so the list, the export and the cleanup always share their conditions.
// Wrapped in computed so the labels follow a language switch.
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

// A date-only pick would otherwise mean "that instant to that instant" (an empty range).
const DEFAULT_TIME = [new Date(2000, 0, 1, 0, 0, 0), new Date(2000, 0, 1, 23, 59, 59)]

// Entry labels come from running.runType.* (call / rpc / mcp / agent / flow), the same words the live
// ledger uses. The parameter must not be called t - it would shadow the translation function.
const RUN_TYPE_KEYS = ['call', 'rpc', 'mcp', 'agent', 'flow']
const runTypeLabel = (v) => (RUN_TYPE_KEYS.includes(v) ? t(`running.runType.${v}`) : v || '—')

const filter = reactive({
  agentId: '',
  // Caller filter: "which runs did this user trigger". Empty = every caller, including the runs with no identity.
  userId: '',
  // Session filter: which runs belong to this conversation. Exact uuid match; empty means every session,
  // including the stateless runs that have none.
  sessionId: '',
  status: '',
  // Last 24 hours as a starting point; it is a fixed range computed at mount, so the screen always shows two
  // explicit instants. Pick a shortcut again to re-align with "now".
  span: [new Date(Date.now() - 24 * 3600e3), new Date()]
})

// Whether the range was chosen by the user. The default 24 hours must not count as a filter, otherwise the
// cleanup button would be clickable before anything was selected.
const spanTouched = ref(false)

const agents = ref([])
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

// Prefer the name the backend joined in; fall back to the short id when the user was deleted after the run, and to a
// muted "no identity" when the caller never had one (an anonymous RPC / MCP call).
const userNameOf = (id) => {
  const u = userOptions.value.find((x) => x.id === id)
  return u ? userOptionLabel(u) : ''
}
// The list endpoint already joins the name; the detail payload carries only the id, so it falls back to the module-level
// user list loaded on mount (and then to the short id).
const userText = (row) =>
  row.user_name || (row.user_id ? userNameOf(row.user_id) || shortId(row.user_id) : t('agentHistory.noUser'))

const STATUS_KEYS = {
  ok: 'agentHistory.statusOk',
  stopped: 'agentHistory.statusStopped',
  failed: 'agentHistory.statusFailed'
}
const statusText = (s) => (STATUS_KEYS[s] ? t(STATUS_KEYS[s]) : s || '-')
const statusType = (s) => ({ ok: 'success', stopped: 'info', failed: 'danger' }[s] || 'info')

const sessionLabel = (row) => row.session_title || (row.session_id ? shortId(row.session_id) : t('agentHistory.noSession'))

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
  const h = Math.floor(m / 60)
  return `${h}h ${m % 60}m`
}

// Character count, rounded for display: the exact number lives in answer_size.
const fmtSize = (n) => {
  if (!Number.isFinite(n) || n <= 0) return '0'
  if (n < 1000) return `${n}`
  return `${(n / 1000).toFixed(1)}k`
}

// buildParams is the only place that turns the filters into a query string: the list, the export and the
// cleanup must send the same conditions.
const buildParams = () => {
  const p = new URLSearchParams()
  if (filter.agentId) p.set('agent_id', filter.agentId)
  if (filter.userId) p.set('user_id', filter.userId)
  const sid = filter.sessionId.trim()
  if (sid) p.set('session_id', sid)
  if (filter.status) p.set('status', filter.status)
  // Always UTC (toISOString): the backend parses RFC3339 and compares timestamptz, so the browser timezone
  // never changes the result.
  const [from, to] = filter.span || []
  if (from) p.set('start_from', new Date(from).toISOString())
  if (to) p.set('start_to', new Date(to).toISOString())
  return p
}

// At least one condition must be set. The range counts only after the user touched it.
const hasCondition = () => !!(filter.agentId || filter.userId || filter.sessionId.trim() || filter.status
  || (spanTouched.value && filter.span?.length === 2))

const loadAgents = async () => {
  try {
    const resp = await fetch(adminApi.agents.base, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', ...getAuthHeader() },
      body: JSON.stringify({ page: 1, limit: -1 })
    })
    const data = await resp.json()
    if (data.code === 200) agents.value = data.data?.items || []
  } catch {
    // A missing agent list does not break the page: rows still show the agent id.
  }
}

const fetchPage = async (reset) => {
  const params = buildParams()
  params.set('limit', String(PAGE_SIZE))
  params.set('offset', String(reset ? 0 : offset.value))

  const resp = await fetch(adminApi.agentHistory(params.toString()), { headers: { ...getAuthHeader() } })
  const data = await resp.json()
  if (data.code !== 200) throw new Error(formatError(data) || t('agentHistory.queryFailed'))

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
    ElMessage.error(e?.message || t('agentHistory.queryFailed'))
  } finally {
    loading.value = false
  }
}

// Any range change means "the user chose it": the picker's change event fires only after confirmation, so a
// shortcut, a hand-picked range and a cleared range all arrive here.
const onSpanChange = () => {
  spanTouched.value = true
  reload()
}

// Point the filter at one conversation. The time range is cleared on purpose: "this session" plus the default
// 24h window would silently hide the conversation's older runs.
const applySessionFilter = (sid) => {
  if (!sid) return
  filter.sessionId = sid
  filter.span = null
  spanTouched.value = false
}

// Cross-page jump: the chat hands a session over, and the page opens already filtered. During setup the query
// has not run yet (onMounted does it with the filter applied), so only a later jump needs its own reload.
const mountedOnce = ref(false)
watch(() => props.presetSession, (p) => {
  if (!p?.session_id) return
  applySessionFilter(p.session_id)
  if (mountedOnce.value) reload()
}, { immediate: true })

// From the drawer: the same filter, plus close the drawer and re-query.
const filterBySession = (sid) => {
  if (!sid) return
  applySessionFilter(sid)
  detailVisible.value = false
  reload()
}

const loadMore = async () => {
  loadingMore.value = true
  try {
    await fetchPage(false)
  } catch (e) {
    ElMessage.error(e?.message || t('agentHistory.loadFailed'))
  } finally {
    loadingMore.value = false
  }
}

const openDetail = async (row) => {
  detail.value = null
  detailVisible.value = true
  try {
    const resp = await fetch(adminApi.agentHistoryDetail(row.id), { headers: { ...getAuthHeader() } })
    const data = await resp.json()
    if (data.code === 200) {
      detail.value = data.data.run
    } else {
      ElMessage.error(formatError(data) || t('agentHistory.detailFailed'))
    }
  } catch (e) {
    ElMessage.error(e?.message || t('agentHistory.detailFailed'))
  }
}

const deleteOne = async (row) => {
  try {
    await ElMessageBox.confirm(
      t('agentHistory.deleteConfirm', {
        name: row.agent_name || shortId(row.agent_id),
        time: fmtTime(row.started_at)
      }),
      t('agentHistory.deleteTitle'), { type: 'warning' }
    )
  } catch { return }

  try {
    const resp = await fetch(adminApi.agentHistoryDelete(row.id), {
      method: 'DELETE',
      headers: { ...getAuthHeader() }
    })
    const data = await resp.json()
    if (data.code !== 200) throw new Error(formatError(data) || t('agentHistory.deleteFailed'))

    // Drop the row locally, then refill one page: a plain reload would throw the user back to the top.
    // This table is the run log only, so nothing is removed from the conversation.
    items.value = items.value.filter((r) => r.id !== row.id)
    total.value = Math.max(0, total.value - 1)
    offset.value = Math.max(0, offset.value - 1)
    ElMessage.success(t('agentHistory.deleted'))
    if (items.value.length < total.value) {
      fetchPage(false).catch(() => {})
    }
  } catch (e) {
    ElMessage.error(e?.message || t('agentHistory.deleteFailed'))
  }
}

const cleanup = async () => {
  if (!hasCondition()) {
    ElMessage.warning(t('agentHistory.cleanupNoCondition'))
    return
  }
  const desc = [
    filter.agentId
      ? t('agentHistory.descAgent', { name: agents.value.find((a) => a.id === filter.agentId)?.name || shortId(filter.agentId) })
      : t('agentHistory.descAgent', { name: t('agentHistory.all') }),
    filter.userId
      ? t('agentHistory.descUser', { name: userNameOf(filter.userId) || shortId(filter.userId) })
      : t('agentHistory.descUser', { name: t('agentHistory.all') }),
    filter.status
      ? t('agentHistory.descStatus', { name: statusText(filter.status) })
      : t('agentHistory.descStatus', { name: t('agentHistory.all') }),
    t('agentHistory.descTime', { value: spanText() })
  ].join('; ')

  try {
    await ElMessageBox.confirm(
      t('agentHistory.cleanupConfirm', { total: total.value, desc }),
      t('agentHistory.cleanupTitle'), { type: 'warning', confirmButtonText: t('agentHistory.cleanupOk') }
    )
  } catch { return }

  try {
    const resp = await fetch(adminApi.agentHistory(buildParams().toString()), {
      method: 'DELETE',
      headers: { ...getAuthHeader() }
    })
    const data = await resp.json()
    if (data.code !== 200) throw new Error(formatError(data) || t('agentHistory.cleanupFailed'))
    ElMessage.success(t('agentHistory.cleanupDone', { count: data.data?.deleted ?? 0 }))
    reload()
  } catch (e) {
    ElMessage.error(e?.message || t('agentHistory.cleanupFailed'))
  }
}

// Say exactly which range is deleted: a shortcut has become two fixed instants by then, so reusing the
// shortcut label would be a lie.
const spanText = () => {
  const [from, to] = filter.span || []
  if (!from || !to) return t('agentHistory.allTime')
  return `${fmtTime(from)} → ${fmtTime(to)}`
}

const exportCsv = async () => {
  try {
    const resp = await fetch(adminApi.agentHistoryExport(buildParams().toString()), {
      headers: { ...getAuthHeader() }
    })
    // A backend error comes back as a JSON envelope (HTTP 200 + code 400): check the type before treating
    // the body as a file, otherwise an error JSON gets saved as an empty .csv.
    const ct = resp.headers.get('content-type') || ''
    if (!ct.includes('csv')) {
      const data = await resp.json()
      throw new Error(formatError(data) || t('agentHistory.exportFailed'))
    }
    const blob = await resp.blob()
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = `agent-runs-${new Date().toISOString().slice(0, 19).replace(/[:T]/g, '')}.csv`
    a.click()
    URL.revokeObjectURL(url)
  } catch (e) {
    ElMessage.error(e?.message || t('agentHistory.exportFailed'))
  }
}

onMounted(() => {
  mountedOnce.value = true
  loadAgents()
  // Shared with the settings pages: one module-level list, so a user created elsewhere shows up here too.
  ensureUserOptions()
  reload()
})

// The parent calls this when switching back to this tab, so an idle tab does not query the database.
// applySessionFilter lets the chat jump straight to "this conversation's runs".
defineExpose({ reload, applySessionFilter })
</script>

<style scoped>
.ah {
  display: flex;
  flex-direction: column;
  height: 100%;
  min-height: 0;
  gap: 12px;
}

/* The same white card as the live ledger and the workflow history: switching tabs must not change the look */
.ah-filters {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
  padding: 10px 14px;
  background: #fff;
  border: 1px solid #ebeef5;
  border-radius: 8px;
}

.ah-agent { width: 160px; }
.ah-user { width: 150px; }
.ah-session { width: 210px; }
.ah-status { width: 110px; }
/* Two timestamps plus the separator: 360px fits on one line, anything narrower truncates to "…" */
.ah-span { width: 360px; }
.ah-total { font-size: 13px; color: #606266; font-variant-numeric: tabular-nums; }
.ah-spacer { flex: 1; }

.ah-body { flex: 1; min-height: 0; }

/* AutoResizer measures this container; without a definite height the table gets 0 and shows only the header */
.ah-body :deep(.el-auto-resizer) {
  height: 100%;
}

.ah-name { font-size: 13px; color: #303133; }
.ah-muted { color: #909399; }
.ah-err { color: #f56c6c; }

.ah-del {
  cursor: pointer;
  color: #c0c4cc;
}
.ah-del:hover { color: #f56c6c; }

.ah-more {
  display: flex;
  justify-content: center;
  padding-bottom: 4px;
}
.ah-end { font-size: 12px; color: #909399; }

.ah-detail { display: flex; flex-direction: column; gap: 8px; }
.ah-dt-row { display: flex; gap: 10px; font-size: 13px; }
.ah-dt-row > span { flex: 0 0 92px; color: #909399; }
.ah-dt-row > b { font-weight: 500; color: #303133; word-break: break-all; }
.ah-mono { font-family: Consolas, Monaco, monospace; font-size: 12px; }
/* Session label plus its "filter by this session" action on one line */
.ah-dt-session { display: inline-flex; align-items: center; gap: 8px; }
.ah-dt-title { margin-top: 10px; font-size: 13px; font-weight: 600; color: #303133; }
.ah-pre {
  margin: 0;
  padding: 8px 10px;
  max-height: 220px;
  overflow: auto;
  font-size: 12px;
  line-height: 1.6;
  color: #303133;
  background: #f5f7fa;
  border: 1px solid #ebeef5;
  border-radius: 6px;
  white-space: pre-wrap;
  word-break: break-word;
}
/* The answer is the reason this panel exists: 220px put a scrollbar on an ordinary answer. Let it grow to most
   of the viewport and let the panel body scroll instead, so only an extreme answer gets its own scroll area. */
.ah-pre--answer {
  max-height: 60vh;
}
/* When a preview does scroll, keep the bar out of the way: thin, invisible until the pointer is over it */
.ah-pre {
  scrollbar-width: thin;
  scrollbar-color: transparent transparent;
}
.ah-pre:hover {
  scrollbar-color: #c0c4cc transparent;
}
.ah-pre::-webkit-scrollbar {
  width: 8px;
  height: 8px;
}
.ah-pre::-webkit-scrollbar-thumb {
  background: transparent;
  border-radius: 4px;
}
.ah-pre:hover::-webkit-scrollbar-thumb {
  background: #c0c4cc;
}
.ah-hint { font-size: 12px; color: #909399; }
</style>
