<template>
  <div ref="rootRef" class="et">
    <!-- 工具条：搜索 + 协议 / 目标 / 状态筛选 -->
    <div class="et-bar">
      <el-input v-model="q.name" size="small" :placeholder="t('endpointsTab.searchName')" clearable
                style="width:180px" @keyup.enter="reload" @clear="reload" />
      <el-select v-model="q.protocol" size="small" :placeholder="t('endpoint.protocolLabel')" clearable
                 style="width:110px" @change="reload">
        <el-option label="MCP" value="mcp" />
        <el-option label="RPC" value="rpc" />
      </el-select>
      <el-select v-model="q.target_type" size="small" :placeholder="t('endpointsTab.target')" clearable
                 style="width:110px" @change="reload">
        <el-option :label="t('assetImport.kindAgent')" value="agent" />
        <el-option :label="t('assetImport.kindWorkflow')" value="flow" />
      </el-select>
      <el-select v-model="q.exposed" size="small" :placeholder="t('endpointsTab.exposedState')" clearable
                 style="width:110px" @change="reload">
        <el-option :label="t('endpointsTab.exposed')" :value="true" />
        <el-option :label="t('endpointsTab.internal')" :value="false" />
      </el-select>
      <el-button size="small" :loading="loading" @click="reload">{{ t('common.refresh') }}</el-button>
      <el-button size="small" text :icon="Reading" @click="openGuide()">{{ t('endpointsTab.guide') }}</el-button>
      <span class="et-count">{{ t('endpointsTab.count', { n: total }) }}</span>
      <span class="et-hint">{{ t('endpointsTab.definition') }}</span>
      <el-popover placement="bottom-start" :width="380" trigger="click">
        <template #reference>
          <el-icon class="et-help" :title="t('endpointsTab.helpIcon')"><QuestionFilled /></el-icon>
        </template>
        <div class="et-help-body">
          <div class="et-help-sec">
            <div class="et-help-title">{{ t('endpointsTab.helpWhatTitle') }}</div>
            <p>{{ t('endpointsTab.helpWhatBody') }}</p>
          </div>
          <div class="et-help-sec">
            <div class="et-help-title">{{ t('endpointsTab.helpCreateTitle') }}</div>
            <p>{{ t('endpointsTab.helpCreateBody') }}</p>
          </div>
          <div class="et-help-sec">
            <div class="et-help-title">{{ t('endpointsTab.helpCallTitle') }}</div>
            <p>{{ t('endpointsTab.helpCallBody', {
              mcp: 'POST /service/mcp/{tenant}',
              rpc: 'POST /service/rpc/{tenant}',
              auth: 'Authorization: Bearer <credential>'
            }) }}</p>
            <p>{{ t('endpointsTab.helpCallMore') }}</p>
          </div>
          <div class="et-help-sec">
            <div class="et-help-title">{{ t('endpointsTab.helpExposeTitle') }}</div>
            <p>{{ t('endpointsTab.helpExposeBody') }}</p>
          </div>
          <div class="et-help-sec">
            <div class="et-help-title">{{ t('endpointsTab.helpToolTitle') }}</div>
            <p>{{ t('endpointsTab.helpToolBody', { ref: 'local:rpc:<name>' }) }}</p>
          </div>
        </div>
      </el-popover>
    </div>

    <div ref="tableWrapRef" class="et-table-wrap">
      <el-table :data="groupedRows" :span-method="spanMethod" :row-class-name="rowClassName"
                v-loading="loading" size="small" class="et-table"
                :height="tableHeight || undefined">
        <el-table-column :label="t('endpointsTab.colName')" min-width="170">
          <template #default="{ row }">
            <div class="et-name">
              {{ row.name }}<span class="et-kind">（<el-icon v-if="targetIcon(row)" class="et-kind-icon" :class="'is-' + row.target_type"><component :is="targetIcon(row)" /></el-icon>{{ targetKind(row) }}）</span>
            </div>
            <div class="et-sub" :title="row.description">{{ row.description || row.id }}</div>
          </template>
        </el-table-column>

        <el-table-column :label="t('endpointsTab.colProtocol')" width="86">
          <template #default="{ row }">
            <el-tag size="small" :type="protocolType(row.protocol)">{{ (row.protocol || '').toUpperCase() }}</el-tag>
          </template>
        </el-table-column>

        <el-table-column :label="t('endpointsTab.colAsTool')" min-width="180">
          <template #default="{ row }">
            <template v-if="usableAsTool(row)">
              <span class="et-tool-rpc">local:rpc:{{ row.name }}</span>
            </template>
            <span v-else-if="row.protocol === 'rpc'" class="et-dim">{{ t('endpointsTab.notToolFlowOnly') }}</span>
            <span v-else class="et-dim">{{ t('endpointsTab.notToolRpcOnly') }}</span>
          </template>
        </el-table-column>

        <el-table-column :label="t('endpointsTab.colExposed')" width="80" align="center">
          <template #default="{ row }">
            <el-switch :model-value="!!row.exposed" :loading="row.__busy"
                       @change="(v) => toggleExpose(row, v)" />
          </template>
        </el-table-column>

        <el-table-column :label="t('endpointsTab.colCreated')" width="160">
          <template #default="{ row }">{{ formatTime(row.created_at) }}</template>
        </el-table-column>

        <el-table-column :label="t('endpointsTab.colActions')" width="360" align="right">
          <template #default="{ row }">
            <div class="et-actions">
              <el-button size="small" text @click="showDetail(row)">{{ t('endpointsTab.detail') }}</el-button>
              <el-button size="small" text @click="openGuide(row)">{{ t('endpointsTab.usage') }}</el-button>
              <el-button size="small" text :disabled="!usableAsTool(row)" @click="showUsage(row)">{{ t('endpointsTab.referenced') }}</el-button>
              <el-button size="small" text @click="retractTarget(row)">{{ t('endpointsTab.retractTarget') }}</el-button>
              <el-button size="small" text type="danger"
                         :disabled="isManaged(row) || !!row.exposed"
                         :title="deleteHint(row)"
                         @click="remove(row)">{{ t('common.delete') }}</el-button>
            </div>
          </template>
        </el-table-column>

        <template #empty>
          <div class="et-empty">
            <div class="et-empty-title">{{ t('endpointsTab.emptyTitle') }}</div>
            <div class="editor-tip">{{ t('endpointsTab.emptyTip') }}</div>
            <div class="et-empty-actions">
              <el-button size="small" @click="emit('open', 'agents')">{{ t('endpointsTab.goAgents') }}</el-button>
              <el-button size="small" @click="emit('open', 'wf-instances')">{{ t('endpointsTab.goFlows') }}</el-button>
            </div>
          </div>
        </template>
      </el-table>
    </div>

    <div v-if="total > q.limit" class="et-pager">
      <el-pagination small background layout="prev, pager, next" :total="total"
                     :page-size="q.limit" :current-page="q.page" @current-change="onPageChange" />
    </div>

    <!-- 端点详情：input_schema 只读（它才是端点对外声明的入参） -->
    <el-dialog v-model="detailVisible"
               :title="detailRow ? t('endpointsTab.detailTitle', { name: detailRow.name }) : t('endpointsTab.detailTitlePlain')"
               width="640px" append-to-body>
      <div v-if="detailRow" class="et-detail">
        <div class="et-d-row"><span class="et-d-label">{{ t('endpointsTab.dProtocol') }}</span><span>{{ (detailRow.protocol || '').toUpperCase() }}</span></div>
        <div class="et-d-row"><span class="et-d-label">{{ t('endpointsTab.dTarget') }}</span><span>{{ targetKind(detailRow) }} · {{ targetName(detailRow) }}</span></div>
        <div class="et-d-row"><span class="et-d-label">{{ t('endpointsTab.dExposed') }}</span><span>{{ detailRow.exposed ? t('endpointsTab.exposedLong') : t('endpointsTab.internalLong') }}</span></div>
        <div class="et-d-row"><span class="et-d-label">{{ t('endpointsTab.dTimes') }}</span><span>{{ formatTime(detailRow.created_at) }} / {{ formatTime(detailRow.updated_at) }}</span></div>
        <div class="et-d-block">
          <div class="et-d-label">input_schema{{ detailRow.protocol === 'mcp' ? t('endpointsTab.mcpInput') : '' }}</div>
          <pre class="et-pre">{{ pretty(detailRow.input_schema) }}</pre>
        </div>
      </div>
      <template #footer>
        <el-button type="primary" plain @click="openGuide(detailRow)">{{ t('endpointsTab.examples') }}</el-button>
        <el-button @click="detailVisible = false">{{ t('common.close') }}</el-button>
      </template>
    </el-dialog>

    <!-- 用法说明：工具条入口传空（通用说明），行内入口传该行；面板贴本页右侧 -->
    <UseGuideDrawer ref="guideRef" :anchor-el="rootRef" @open="emit('open', $event)" />

    <UsageView ref="usageRef" />
  </div>
</template>

<script setup>
import { ref, reactive, computed, onMounted, onUnmounted, nextTick } from 'vue'
import { useI18n } from 'vue-i18n'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Cpu, Share, QuestionFilled, Reading } from '@element-plus/icons-vue'
import adminApi from '../../config/admin.js'
import { getToken } from '../../utils/auth.js'
import UsageView from './UsageView.vue'
import UseGuideDrawer from './UseGuideDrawer.vue'
import { formatError } from '../../../locales'

const emit = defineEmits(['count', 'open'])

const { t } = useI18n()

const loading = ref(false)
const rows = ref([])
const total = ref(0)
const usageRef = ref(null)
// 用法说明面板：工具条入口不传行（通用说明），行内「用法」传该行
const guideRef = ref(null)
// 本页根元素：用法面板按它的 rect 贴右侧（与智能体设置面板同一套定位）
const rootRef = ref(null)
// 详情弹窗（input_schema 只读）
const detailVisible = ref(false)
const detailRow = ref(null)
// 目标实体名映射：`${target_type}:${target_id}` → 名字（端点上只存 id）
const nameMap = ref({})

const q = reactive({
  page: 1,
  limit: 20,
  name: '',
  protocol: '',
  target_type: '',
  exposed: undefined // undefined 时不出现在请求体里（= 不过滤）
})

const authHeaders = () => ({
  'Content-Type': 'application/json',
  'Authorization': `Bearer ${getToken()}`
})

const protocolType = (p) => ({ mcp: 'primary', rpc: 'success', http: 'info', llm: 'warning' }[p] || 'info')

const formatTime = (t) => (t ? String(t).replace('T', ' ').slice(0, 19) : '—')

// 优先用后端 JOIN 出的 target_name；取不到时回退本地映射 / 原始 id
const targetName = (row) => row.target_name || nameMap.value[`${row.target_type}:${row.target_id}`] || row.target_id

// 目标类型文案（折叠进「端点名」后面的括号里显示）
const targetKind = (row) => (row.target_type === 'agent'
  ? t('assetImport.kindAgent')
  : row.target_type === 'flow' ? t('assetImport.kindWorkflow') : row.target_type)

// 目标类型图标（与主窗口导航一致：智能体 = Cpu，工作流 = Share）
const targetIcon = (row) => (row.target_type === 'agent' ? Cpu : row.target_type === 'flow' ? Share : null)

// ===== 同名端点合并展示 =====
// 合并要求同名行相邻，所以先把同一页内同名的端点聚到一起（按名字首次出现的顺序分组）。
// 第 0 列「端点名」与第 1 列「目标」用 rowspan 合并；协议 / 作为工具 / 对外 / 操作等列保持逐行独立。
const groupedRows = computed(() => {
  const order = []
  const groups = new Map()
  for (const r of rows.value) {
    if (!groups.has(r.name)) {
      groups.set(r.name, [])
      order.push(r.name)
    }
    groups.get(r.name).push(r)
  }
  return order.flatMap((n) => groups.get(n))
})

// 行 → 合并列 rowspan：同名组首行为「组大小」，组内其余行为 0（该单元格不再渲染，等价合并）
const nameRowSpan = computed(() => {
  const span = new Map()
  const groups = new Map()
  for (const r of groupedRows.value) {
    if (!groups.has(r.name)) groups.set(r.name, [])
    groups.get(r.name).push(r)
  }
  for (const g of groups.values()) {
    g.forEach((r, i) => span.set(r, i === 0 ? g.length : 0))
  }
  return span
})

// 只对第 0 列（端点名）应用合并；{ rowspan: 0, colspan: 0 } 表示该单元格被合并掉、不再渲染
const spanMethod = ({ row, columnIndex }) => {
  if (columnIndex !== 0) return
  const rowspan = nameRowSpan.value.get(row)
  if (rowspan === undefined) return
  return rowspan === 0 ? { rowspan: 0, colspan: 0 } : { rowspan, colspan: 1 }
}

// 端点组首行（nameRowSpan > 0）：组内协议之间留细缝，组与组之间留大缝
const rowClassName = ({ row }) => (nameRowSpan.value.get(row) > 0 ? 'et-group-start' : '')



// 端点列表（POST 分页 + 过滤）
const load = async () => {
  loading.value = true
  try {
    const body = { page: q.page, limit: q.limit }
    if (q.name) body.name = q.name
    if (q.protocol) body.protocol = q.protocol
    if (q.target_type) body.target_type = q.target_type
    if (q.exposed !== undefined && q.exposed !== null) body.exposed = q.exposed

    const res = await fetch(adminApi.svcEndpoints.base, {
      method: 'POST', headers: authHeaders(), body: JSON.stringify(body)
    })
    const data = await res.json().catch(() => ({}))
    if (data.code !== 200) throw new Error(formatError(data) || t('endpointsTab.loadFailed'))
    rows.value = (data.data?.items || []).map((r) => ({ ...r, __busy: false }))
    total.value = data.data?.total || 0
    emit('count', total.value)
  } catch (e) {
    ElMessage.error(e.message || t('endpointsTab.loadFailed'))
  } finally {
    loading.value = false
  }
}

// 目标实体名映射（一次性拉全量 agents / flows）
const loadNames = async () => {
  const map = {}
  try {
    const [aRes, fRes] = await Promise.all([
      fetch(adminApi.agents.base, {
        method: 'POST', headers: authHeaders(), body: JSON.stringify({ page: 1, limit: -1 })
      }),
      fetch(adminApi.flows.base, {
        method: 'POST', headers: authHeaders(), body: JSON.stringify({ page: 1, limit: -1 })
      })
    ])
    const aBody = await aRes.json().catch(() => ({}))
    const fBody = await fRes.json().catch(() => ({}))
    for (const a of (aBody.data?.items || [])) map[`agent:${a.id}`] = a.name
    for (const f of (fBody.data?.items || [])) map[`flow:${f.id}`] = f.name
  } catch {
    // 静默失败：取不到就显示 id
  }
  nameMap.value = map
}

const reload = () => {
  q.page = 1
  load()
}

const onPageChange = (p) => {
  q.page = p
  load()
}

// 对外开放 / 收回（后端 Update 支持只传 exposed，字段名暂沿用）
const toggleExpose = async (row, exposed) => {
  row.__busy = true
  try {
    const res = await fetch(`${adminApi.svcEndpoints.base}/${row.id}`, {
      method: 'PUT', headers: authHeaders(), body: JSON.stringify({ exposed })
    })
    const data = await res.json().catch(() => ({}))
    if (data.code !== 200) throw new Error(formatError(data) || t('endpoint.updateFailed'))
    row.exposed = exposed
    ElMessage.success({
      message: exposed ? t('endpoint.exposedOk') : t('endpoint.retractedOk'),
      duration: 700
    })
  } catch (e) {
    ElMessage.error(e.message || t('endpoint.updateFailed'))
  } finally {
    row.__busy = false
  }
}

// 托管端点：工作流的 rpc 端点由建流程时自动补（仓储 CreateManagedRPC），
// 名字 / 描述 / input_schema 全派生自实体，接口侧也拒绝单独删除。
const isManaged = (row) => row.target_type === 'flow' && row.protocol === 'rpc'

const deleteHint = (row) => {
  if (isManaged(row)) return t('endpointsTab.deleteManaged')
  if (row.exposed) return t('endpoint.tipDeleteBlocked')
  return t('endpoint.tipDelete')
}

const remove = async (row) => {
  try {
    await ElMessageBox.confirm(
      t('endpointsTab.deleteBody', { name: row.name, protocol: (row.protocol || '').toUpperCase() }),
      t('endpoint.deleteTitle'),
      { type: 'warning', confirmButtonText: t('common.delete'), cancelButtonText: t('common.cancel') }
    )
  } catch {
    return
  }
  try {
    const res = await fetch(`${adminApi.svcEndpoints.base}/${row.id}`, {
      method: 'DELETE', headers: authHeaders()
    })
    const data = await res.json().catch(() => ({}))
    if (data.code !== 200) throw new Error(formatError(data) || t('endpoint.deleteFailed'))
    ElMessage.success(t('endpoint.deleteOk'))
    load()
  } catch (e) {
    ElMessage.error(e.message || t('endpoint.deleteFailed'))
  }
}

// 是否为「会进授权体系的端点」：只有工作流的 rpc 端点（local:rpc:<端点名>）。
// agent 端点不进工具目录 —— agent 之间的调用走 call_agent（agent_relations 等闸门），
// 且端点名恒等于实体名，agent 与 flow 同名会撞 ref。
const usableAsTool = (row) => row.protocol === 'rpc' && row.target_type === 'flow'

// 被谁引用：只有工作流 rpc 端点会作为工具出现在授权体系里（local:rpc:<端点名>）
const showUsage = (row) => {
  if (!usableAsTool(row)) return
  usageRef.value?.open(`local:rpc:${row.name}`)
}

// 怎么用：不传 row → 通用说明（地址模板 + 两种协议）；传 row → 按该行协议与入参生成可复制示例
const openGuide = (row = null) => guideRef.value?.open(row)

// JSON 美化（input_schema 可能是对象或已序列化字符串）
const pretty = (v) => {
  if (v === null || v === undefined || v === '') return t('endpointsTab.emptyValue')
  try {
    return typeof v === 'string' ? JSON.stringify(JSON.parse(v), null, 2) : JSON.stringify(v, null, 2)
  } catch {
    return String(v)
  }
}

const showDetail = (row) => {
  detailRow.value = row
  detailVisible.value = true
}

// 打开目标实体：切到「智能体 / 工作流」页并进入该实体的编辑页
const openTarget = (row) => {
  if (!row.target_id) return
  emit('open', { nav: row.target_type === 'agent' ? 'agents' : 'wf-instances', id: row.target_id })
}

// 按目标批量收回：该实体的所有协议端点一起关掉「对外」（记录与配置保留，租户内仍可调用）
const retractTarget = async (row) => {
  try {
    await ElMessageBox.confirm(
      t('endpointsTab.retractConfirm', { name: targetName(row) }),
      t('endpointsTab.retractTitle'),
      { type: 'warning', confirmButtonText: t('endpointsTab.retractBtn'), cancelButtonText: t('common.cancel') }
    )
  } catch {
    return
  }
  try {
    const res = await fetch(adminApi.svcEndpoints.retract, {
      method: 'POST', headers: authHeaders(),
      body: JSON.stringify({ target_type: row.target_type, target_id: row.target_id })
    })
    const data = await res.json().catch(() => ({}))
    if (data.code !== 200) throw new Error(formatError(data) || t('endpointsTab.retractFailed'))
    ElMessage.success(t('endpointsTab.retractedAll'))
    load()
  } catch (e) {
    ElMessage.error(e.message || t('endpointsTab.retractFailed'))
  }
}

// 表格高度：Element Plus 只接受数值（传百分比会让内部高度算成 NaN，表头固定与内部滚动都会失效），
// 所以量外层容器的可用高度喂给 :height —— 这样表头固定、横向滚动条常驻在表格底部
const tableWrapRef = ref(null)
const tableHeight = ref(0)
const syncTableHeight = () => {
  const el = tableWrapRef.value
  if (!el) return
  const h = Math.round(el.clientHeight)
  if (h > 0 && Math.abs(h - tableHeight.value) > 1) tableHeight.value = h
}

let tableRO = null

onMounted(() => {
  load()
  loadNames()
  nextTick(syncTableHeight)
  if (window.ResizeObserver && tableWrapRef.value) {
    tableRO = new ResizeObserver(syncTableHeight)
    tableRO.observe(tableWrapRef.value)
  }
  window.addEventListener('resize', syncTableHeight)
})

onUnmounted(() => {
  tableRO?.disconnect()
  window.removeEventListener('resize', syncTableHeight)
})
</script>

<style scoped>
.et {
  flex: 1;
  min-height: 0;
  display: flex;
  flex-direction: column;
  gap: 12px;
}

/* 表格测量容器：吃掉工具条与分页条之间的剩余空间（min-height:0 才会真正收缩） */
.et-table-wrap {
  flex: 1;
  min-height: 0;
}

.et-bar {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
  background: #fff;
  border: 1px solid #ebeef5;
  border-radius: 8px;
  padding: 10px 14px;
}

.et-count {
  font-size: 13px;
  color: #606266;
}

.et-hint {
  font-size: 12px;
  color: #909399;
  min-width: 0;   /* 允许在 flex 行内收缩，不把右侧的 ? 挤走 */
}

/* 帮助入口：跟在定义后面，点开是术语解释（协议 / 作为工具 / 对外态这些黑话） */
.et-help {
  font-size: 14px;
  color: #909399;
  cursor: pointer;
  flex-shrink: 0;
}

.et-help:hover {
  color: #1890ff;
}

.et-help-sec + .et-help-sec {
  margin-top: 10px;
}

.et-help-title {
  font-size: 13px;
  font-weight: 600;
  color: #303133;
  margin-bottom: 2px;
}

.et-help-body p {
  margin: 0;
  font-size: 12px;
  line-height: 1.6;
  color: #606266;
}

.et-help-body code {
  background: #f5f7fa;
  border-radius: 3px;
  padding: 0 4px;
  font-size: 11px;
}

/* 空态：新人第一眼看到的地方，重点给「该去哪」，不是「端点是什么」 */
.et-empty {
  padding: 8px 0 4px;
}

.et-empty-title {
  font-size: 14px;
  font-weight: 600;
  color: #606266;
  margin-bottom: 6px;
}

.et-empty-actions {
  margin-top: 10px;
}

/* ===== 行卡片化 =====
   一行 = 一张卡片：去掉网格线与单元格分隔线，间距用「透明上边框 + 背景裁剪」实现 ——
   组内协议之间留细缝，端点与端点之间留大缝。
   border-spacing 对所有行一视同仁，做不到这种分层，所以不用它。 */
.et-table {
  background: #f5f7fa;
  border-radius: 8px;
  box-shadow: 0 2px 8px rgba(15, 23, 42, 0.06);
}

.et-table :deep(.el-table__body) {
  border-collapse: separate;
  border-spacing: 0;
}

/* 行高：small 尺寸默认只有 6px 内边距，一屏十几个端点会糊成一片 */
.et-table :deep(.el-table__cell) {
  padding: 10px 0;
}

/* 表头也是一张卡片：白底 + 两端圆角 + 轻阴影，
   与第一行之间靠组首行的 10px 缝隙分开 */
.et-table :deep(.el-table__header .el-table__cell) {
  background: #fff;
  font-weight: 600;
  color: #303133;
  box-shadow: 0 1px 3px rgba(15, 23, 42, 0.05);
}

.et-table :deep(.el-table__header tr > th.el-table__cell:first-child) {
  border-top-left-radius: 8px;
  border-bottom-left-radius: 8px;
}

.et-table :deep(.el-table__header tr > th.el-table__cell:last-child) {
  border-top-right-radius: 8px;
  border-bottom-right-radius: 8px;
}

/* 卡片本体：白底 + 无底边线；上边框与容器同色，只留纯留白。
   3px = 同一端点内相邻协议之间，行与行之间不投阴影。 */
.et-table :deep(.el-table__body .el-table__row > td.el-table__cell) {
  background-color: #fff;
  border-bottom: none;
  border-top: 3px solid #f5f7fa;
}

/* 新端点组的第一行：与上一组拉开距离（10px），组内仍是上面的 3px */
.et-table :deep(.el-table__body .el-table__row.et-group-start > td.el-table__cell) {
  border-top-width: 10px;
  border-top-color: #e8ecf2;
}

/* 悬停整行提亮：操作按钮都在这行，鼠标扫过要能确认落在哪一条 */
.et-table :deep(.el-table__body .el-table__row:hover > td.el-table__cell) {
  background-color: #f0f6ff;
}

.et-name {
  font-size: 13px;
  font-weight: 600;
  color: #303133;
}

/* 端点名后面的目标类型说明（图标 + 智能体 / 工作流） */
.et-kind {
  font-size: 12px;
  color: #909399;
}

.et-kind-icon {
  font-size: 12px;
  vertical-align: -2px; /* 与括号内文字基线对齐 */
  margin-right: 2px;
}

/* 类型配色：智能体 = 红，工作流 = 绿 */
.et-kind-icon.is-agent {
  color: #f56c6c;
}

.et-kind-icon.is-flow {
  color: #389e0d;
}

.et-sub {
  font-size: 12px;
  color: #909399;
  /* 占满单元格宽度，最多两行，超出省略 */
  display: -webkit-box;
  -webkit-box-orient: vertical;
  -webkit-line-clamp: 2;
  line-clamp: 2;
  overflow: hidden;
}

/* 操作列：五个按钮始终排在同一行（不换行） */
.et-actions {
  display: flex;
  align-items: center;
  justify-content: flex-end;
  gap: 4px;
  white-space: nowrap;
}

/* 用 gap 控制间距，清掉 Element Plus 给相邻按钮的默认左外边距 */
.et-actions :deep(.el-button + .el-button) {
  margin-left: 0;
}

.et-dim {
  font-size: 12px;
  color: #909399;
  font-family: ui-monospace, Menlo, Consolas, monospace;
}

/* 「作为工具」列里真正可用的工具名（local:rpc:xxx）：加到 #303133 + 500，
   其余说明性文字仍走 .et-dim 的浅灰，一眼能看出哪一行真能当工具用 */
.et-tool-rpc {
  font-size: 12px;
  font-family: ui-monospace, Menlo, Consolas, monospace;
  font-weight: 500;
  color: #303133;
}

.et-pager {
  display: flex;
  justify-content: flex-end;
}

.ref-tag {
  font-size: 11px;
  padding: 1px 5px;
  border-radius: 3px;
  color: #909399;
  background: #f4f4f5;
  margin-right: 6px;
}

.editor-tip {
  font-size: 12px;
  color: #909399;
  line-height: 1.6;
  padding: 8px 0;
}

.et-link {
  cursor: pointer;
  color: #409eff;
}

.et-link:hover {
  text-decoration: underline;
}

.et-detail {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.et-d-row {
  display: flex;
  gap: 10px;
  font-size: 13px;
  color: #303133;
}

.et-d-block {
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.et-d-label {
  min-width: 84px;
  font-size: 12px;
  color: #909399;
}

.et-pre {
  margin: 0;
  max-height: 220px;
  overflow: auto;
  scrollbar-gutter: stable;
  background: #f7f9fc;
  border: 1px solid #ebeef5;
  border-radius: 6px;
  padding: 8px 10px;
  font-size: 12px;
  line-height: 1.5;
  font-family: ui-monospace, Menlo, Consolas, monospace;
  color: #303133;
  white-space: pre-wrap;
  word-break: break-all;
  /* 滚动条「位置常驻、悬浮才上色」（同 .tt / Chat.vue） */
  scrollbar-width: thin;
  scrollbar-color: transparent transparent;
}

.et-pre::-webkit-scrollbar {
  width: 6px;
  height: 6px;
}

.et-pre::-webkit-scrollbar-track {
  background: transparent;
}

.et-pre::-webkit-scrollbar-thumb {
  background: transparent;
  border-radius: 3px;
}

.et-pre:hover {
  scrollbar-color: #c1c1c1 transparent;
}

.et-pre:hover::-webkit-scrollbar-thumb {
  background: #c1c1c1;
}
</style>
