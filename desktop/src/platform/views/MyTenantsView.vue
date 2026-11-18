<template>
  <!-- 「用户」身份的界面：我名下的租户。
       内容区结构与平台管理员的管理页同款：整个页面就两张卡 ——
       左 = 租户卡片列表（可点选），右 = 选中租户的详情。
       可自助：停用 / 启用、配额（五项，受系统顶约束）、工作流与智能体运行参数、primary 重置密码；
       归属与更上层的系统顶由平台管理员决定。顶栏由 PlatformApp 提供（本页只把租户数 emit 上去）。 -->
  <div class="section">
    <div class="split">
      <div class="list-pane">
        <!-- 计数 / 筛选 / 刷新：列表卡内的第一行（本地过滤，无分页）。
             控件尺寸与「租户管理」一致：small + 固定宽度 + 搜索带图标 -->
        <div class="lp-bar">
          <span class="lp-count">共 {{ filtered.length }} 个租户</span>
          <el-input
            v-model="q"
            class="f-search"
            size="small"
            placeholder="搜索 schema / 显示名"
            clearable
          >
            <template #prefix><el-icon><Search /></el-icon></template>
          </el-input>
          <el-select v-model="statusFilter" class="f-select" size="small" placeholder="全部状态" clearable>
            <el-option label="正常" value="active" />
            <el-option label="已停用" value="suspended" />
            <el-option label="待完成" value="pending" />
          </el-select>
          <el-button size="small" :icon="Refresh" :loading="loading" @click="load">刷新</el-button>
        </div>

        <div v-loading="loading" class="t-list">
          <el-empty
            v-if="!loading && !filtered.length"
            :description="items.length ? '没有匹配的租户' : '暂无负责的租户，请联系平台管理员'"
          />

          <!-- 网格流式：一行放多张，每张 200px 起、随行宽浮动 -->
          <div class="t-grid">
            <button
              v-for="t in filtered"
              :key="t.schema"
              type="button"
              class="t-card"
              :class="{ active: t.schema === selected?.schema }"
              @click="select(t)"
            >
              <!-- 头部：显示名 + schema + 状态（对齐租户端「智能体卡片」：头部带一条下分隔线） -->
              <div class="card-header">
                <div class="header-content">
                  <div class="card-title">{{ t.display_name || t.schema }}</div>
                  <div class="card-description">
                    <span class="mono tc-schema">{{ t.schema }}</span>
                    <el-tag size="small" :type="statusType(t.status)" effect="light">{{ statusText(t.status) }}</el-tag>
                  </div>
                </div>
              </div>
              <!-- 卡体：label/value 行（同智能体卡片的 .info-item） -->
              <div class="card-body">
                <div class="info-item">
                  <span class="info-label">用量：</span>
                  <span class="info-value tc-usage">
                    <span v-for="m in QUOTA_META" :key="m.key" class="tc-u" :class="{ over: isOver(t, m) }">
                      {{ m.short }} {{ t.usage[m.usageKey] }}/{{ limitBrief(t.quota[m.key]) }}
                    </span>
                  </span>
                </div>
                <!-- 备注：没有备注也占一行（兜底文案），有备注时可悬停看全文 -->
                <div class="info-item">
                  <span class="info-label">备注：</span>
                  <span
                    class="info-value description-value"
                    :class="{ empty: !t.remark }"
                    :title="t.remark || ''"
                  >{{ t.remark || EMPTY_REMARK }}</span>
                </div>
              </div>
            </button>
          </div>
        </div>
      </div>

      <section class="detail-pane">
        <el-empty v-if="!selected" description="左侧选择一个租户" />

        <template v-else>
          <!-- 卡头：身份 + 状态 + 启停（本页唯一的写操作） -->
          <div class="d-head">
            <div class="d-title">
              <span class="d-name">{{ selected.display_name || selected.schema }}</span>
              <span class="mono d-sub">{{ selected.schema }}</span>
              <el-tag size="small" :type="statusType(selected.status)">{{ statusText(selected.status) }}</el-tag>
              <span v-if="selected.plan" class="d-plan">套餐 {{ selected.plan }}</span>
            </div>
            <el-button
              size="small"
              :type="selected.status === 'active' ? 'danger' : 'success'"
              @click="toggleStatus(selected)"
            >
              {{ selected.status === 'active' ? '停用' : '启用' }}
            </el-button>
          </div>

          <div class="meta-grid">
            <div class="meta-cell">
              <div class="meta-label">创建时间</div>
              <div class="meta-value">{{ fmtTime(selected.created_at) }}</div>
            </div>
            <div class="meta-cell">
              <div class="meta-label">更新时间</div>
              <div class="meta-value">{{ fmtTime(selected.updated_at) }}</div>
            </div>
          </div>

          <!-- 可折叠卡片①：配额（五项都可自助修改，但都受系统顶约束）
               超系统顶后端直接拒（不像管理员那条截断），所以这里不显示"已截断"的提示。 -->
          <div class="blk">
            <button type="button" class="blk-head" @click="open.quota = !open.quota">
              <span class="blk-title">配额</span>
              <span class="blk-hint">可自助修改 · 受系统顶约束</span>
              <el-icon class="blk-arrow" :class="{ open: open.quota }"><ArrowRight /></el-icon>
            </button>
            <div v-show="open.quota" class="blk-body">
              <div v-loading="quotaLoading" class="quota-table">
                <div class="quota-head">
                  <span>配置项</span>
                  <span>当前用量</span>
                  <span>系统顶</span>
                  <span>本租户配置</span>
                </div>
                <div v-for="row in quotaEditRows" :key="row.key" class="quota-grid-row">
                  <span class="flow-name">{{ row.label }}</span>
                  <span class="q-usage" :class="{ over: row.over }">{{ row.used === null ? '—' : row.used }}</span>
                  <span class="flow-sys">{{ row.systemText }}</span>
                  <el-input v-model="tenantQuota[row.key]" size="small" placeholder="留空回退系统顶" />
                </div>
              </div>

              <div class="blk-tip">
                五项都能改，但都<b>不能超过系统顶</b>（系统顶为「不限」时不受限）；留空 = 回退系统顶，
                -1 = 不限制。空间按 MiB 填写，保存后立即生效。
              </div>
              <div class="blk-foot">
                <span class="tip">当前生效：{{ effectiveQuotaText }}</span>
                <el-button size="small" type="primary" :loading="quotaSaving" @click="submitQuota">
                  保存配额
                </el-button>
              </div>
            </div>
          </div>

          <!-- 可折叠卡片②：根账号密码（默认收起；忘记 primary 密码时的自助恢复口） -->
          <div class="blk">
            <button type="button" class="blk-head" @click="open.primaryPwd = !open.primaryPwd">
              <span class="blk-title">根账号密码</span>
              <span class="blk-hint">primary</span>
              <el-icon class="blk-arrow" :class="{ open: open.primaryPwd }"><ArrowRight /></el-icon>
            </button>
            <div v-show="open.primaryPwd" class="blk-body">
              <div class="form-grid">
                <label class="f-label">新密码</label>
                <el-input v-model="pwdForm.password" size="small" type="password" show-password placeholder="至少 8 位" />

                <label class="f-label">确认</label>
                <el-input
                  v-model="pwdForm.confirm"
                  size="small"
                  type="password"
                  show-password
                  placeholder="再输一次"
                  @keyup.enter="submitPrimaryPassword"
                />
              </div>

              <div class="blk-tip">
                只重置本租户根账号 <b>primary</b> 的密码，不动它的角色与状态；重置后请用新密码走「租户登录」。
              </div>
              <div class="blk-foot">
                <span class="tip">已登录的会话不会失效；需要立即踢下线就停用本租户。</span>
                <el-button size="small" type="primary" :loading="pwdSaving" @click="submitPrimaryPassword">
                  重置密码
                </el-button>
              </div>
            </div>
          </div>

          <!-- 可折叠卡片③：工作流运行参数（可自助修改；系统级只读对照） -->
          <div class="blk">
            <button type="button" class="blk-head" @click="open.flow = !open.flow">
              <span class="blk-title">工作流运行参数</span>
              <span class="blk-hint">可自助修改</span>
              <el-icon class="blk-arrow" :class="{ open: open.flow }"><ArrowRight /></el-icon>
            </button>
            <div v-show="open.flow" class="blk-body">
              <div v-loading="flowLoading" class="flow-table">
                <div class="flow-head">
                  <span>配置项</span>
                  <span>系统级</span>
                  <span>租户配置</span>
                </div>
                <div v-for="f in FLOW_FIELDS" :key="f.key" class="flow-row">
                  <span class="flow-name">{{ f.label }}</span>
                  <span class="flow-sys">{{ flowText(systemFlow[f.key]) }}</span>
                  <el-input v-model="tenantFlow[f.key]" size="small" placeholder="留空回退系统级" />
                </div>
              </div>

              <div class="blk-tip">
                留空 = 回退系统级；-1 = 不限制；租户值不能超过系统级。保存后立即生效。
              </div>
              <div class="blk-foot">
                <span class="tip">只影响本租户；系统级由平台管理员设定。</span>
                <el-button size="small" type="primary" :loading="flowSaving" @click="submitFlow">
                  保存工作流运行参数
                </el-button>
              </div>
            </div>
          </div>

          <!-- 可折叠卡片④：智能体运行参数（可自助修改；系统级只读对照） -->
          <div class="blk">
            <button type="button" class="blk-head" @click="open.agent = !open.agent">
              <span class="blk-title">智能体运行参数</span>
              <span class="blk-hint">可自助修改</span>
              <el-icon class="blk-arrow" :class="{ open: open.agent }"><ArrowRight /></el-icon>
            </button>
            <div v-show="open.agent" class="blk-body">
              <div v-loading="agentLoading" class="flow-table">
                <div class="flow-head">
                  <span>配置项</span>
                  <span>系统级</span>
                  <span>租户配置</span>
                </div>
                <div v-for="f in AGENT_FIELDS" :key="f.key" class="flow-row">
                  <span class="flow-name">{{ f.label }}</span>
                  <span class="flow-sys">{{ flowText(systemAgent[f.key]) }}</span>
                  <el-input v-model="tenantAgent[f.key]" size="small" placeholder="留空回退系统级" />
                </div>
              </div>

              <div class="blk-tip">
                留空 = 回退系统级；<b>本组配置没有 -1</b>（都是安全阀，调成「不限制」等于拆掉防线）；
                租户值不能超过系统级。保存后立即生效。
              </div>
              <div class="blk-foot">
                <span class="tip">只影响本租户；系统级由平台管理员设定。</span>
                <el-button size="small" type="primary" :loading="agentSaving" @click="submitAgent">
                  保存智能体运行参数
                </el-button>
              </div>
            </div>
          </div>

          <!-- 可折叠卡片⑤：租户信息（纯只读，不是输入框） -->
          <div class="blk">
            <button type="button" class="blk-head" @click="open.info = !open.info">
              <span class="blk-title">租户信息</span>
              <el-icon class="blk-arrow" :class="{ open: open.info }"><ArrowRight /></el-icon>
            </button>
            <div v-show="open.info" class="blk-body">
              <div v-for="row in infoRows" :key="row.label" class="kv">
                <span class="kv-label">{{ row.label }}</span>
                <span class="kv-value" :class="{ mono: row.mono }">{{ row.value || '—' }}</span>
              </div>
            </div>
          </div>

          <div class="tip">
            启停、工作流运行参数、根账号（primary）密码都可以在这里自助
            （停用后该租户立即无法登录控制台，MCP/RPC 调用也会被拒）；
            调整配额上限、转移归属请找平台管理员（他走「租户管理」页）。
          </div>
        </template>
      </section>
    </div>
  </div>
</template>

<script setup>
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { ArrowRight, Refresh, Search } from '@element-plus/icons-vue'
import { api } from '../api'

// count：把租户数上抛给顶栏导航的标签（「我的租户 (N)」，与管理员页同款）
const emit = defineEmits(['count', 'session-expired'])

const items = ref([])
const loading = ref(false)
// 本地过滤：/platform/me/tenants 一次给全量（没有分页接口），所以不走 page/page_size
const q = ref('')
const statusFilter = ref('')
const selected = ref(null)
// 详情卡里四块折叠卡的展开状态：**默认全部收起**（先让操作者看到租户身份与信息行，
// 需要看/改哪块再展开）。`open` 的键与 `.blk` 一一对应。
const open = reactive({ quota: false, primaryPwd: false, flow: false, agent: false, info: false })

// 根账号（primary）重置密码的输入态
const pwdForm = reactive({ password: '', confirm: '' })
const pwdSaving = ref(false)

const QUOTA_META = [
  { key: 'max_agents', usageKey: 'agents', label: '智能体', short: '智' },
  { key: 'max_skills', usageKey: 'skills', label: '技能', short: '技' },
  { key: 'max_flows', usageKey: 'flows', label: '工作流', short: '流' }
]

// 备注为空时的兜底文案（卡片与右卡「租户信息」共用一份，别写两遍）
const EMPTY_REMARK = '创建者很懒，什么都没写！'

// ===== 工作流运行参数（platform.flow_configs 的租户行；系统级只读对照） =====
const FLOW_FIELDS = [
  { key: 'max_timeout', label: '最大超时（秒）' },
  { key: 'max_recursion_depth', label: '最大递归深度' },
  { key: 'max_node_count', label: '最大节点数' },
  { key: 'max_exec_count', label: '最大执行次数' },
  { key: 'max_loop_times', label: '最大循环次数' },
  { key: 'max_run_records', label: '最大运行记录数' },
  { key: 'max_session_records', label: '最大会话记忆' }
]
const flowLoading = ref(false)
const flowSaving = ref(false)
const systemFlow = ref({})
const tenantFlow = ref({})
const flowText = (v) => (v === null || v === undefined || v === '' ? '未设置' : String(v))

const statusText = (s) => ({ active: '正常', suspended: '已停用', deleted: '已删除', pending: '待完成' }[s] || s)
const statusType = (s) => ({ active: 'success', suspended: 'warning', deleted: 'info', pending: 'info' }[s] || 'info')
const limitBrief = (v) => (v === -1 ? '∞' : String(v ?? '-'))
// 列表里的用量摘要：上限 >= 0 且用量超出才标红（-1 = 不限制）
const isOver = (t, m) => t.quota[m.key] >= 0 && t.usage[m.usageKey] > t.quota[m.key]

const filtered = computed(() => {
  const kw = q.value.trim().toLowerCase()
  return items.value.filter((t) => {
    if (statusFilter.value && t.status !== statusFilter.value) return false
    if (!kw) return true
    return `${t.schema} ${t.display_name || ''}`.toLowerCase().includes(kw)
  })
})

// 只读键值：schema / 显示名 / 套餐 / 状态 / 备注
const infoRows = computed(() => {
  const t = selected.value
  if (!t) return []
  return [
    { label: 'schema', value: t.schema, mono: true },
    { label: '显示名', value: t.display_name },
    { label: '套餐', value: t.plan },
    { label: '状态', value: statusText(t.status) },
    { label: '备注', value: t.remark || EMPTY_REMARK }
  ]
})

const fmtTime = (s) => {
  if (!s) return '—'
  const d = new Date(s)
  if (Number.isNaN(d.getTime())) return String(s).replace('T', ' ').slice(0, 16)
  const p = (n) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`
}

function select(t) {
  if (!t || t.schema === selected.value?.schema) return
  selected.value = t
}

// 选中项一变就重新拉它的工作流运行参数（select / load 都会经过这里）
watch(
  () => selected.value?.schema,
  (schema) => {
    if (!schema) return
    loadFlowConfigs(schema)
    loadAgentConfigs(schema)
    loadQuota(schema)
  }
)

function handleError(err) {
  if (err?.status === 401) {
    emit('session-expired')
    return
  }
  ElMessage.error(err?.message || '请求失败')
}

async function load() {
  loading.value = true
  try {
    const data = await api.myTenants()
    items.value = data?.items || []
    // 选中项还在就同步成新数据；不在（或还没选）则退回第一个
    const hit = items.value.find((t) => t.schema === selected.value?.schema)
    selected.value = hit || items.value[0] || null
    // 顶栏标签上的数字（与管理员页的 (N) 同源）
    emit('count', items.value.length)
  } catch (err) {
    handleError(err)
  } finally {
    loading.value = false
  }
}

// 卡头快捷启停：只切 active ⇄ suspended（与后端同口径；pending / deleted 不给写）。
// 后端只允许操作"自己名下"的租户，不是自己的一律 403。
async function toggleStatus(t) {
  if (!t) return
  const next = t.status === 'active' ? 'suspended' : 'active'
  const action = next === 'suspended' ? '停用' : '启用'
  try {
    await ElMessageBox.confirm(
      next === 'suspended'
        ? `停用租户 ${t.schema} 后：该租户将无法登录控制台，MCP/RPC 调用也会被拒（立即生效）。`
        : `启用租户 ${t.schema}？`,
      `${action}确认`,
      { type: next === 'suspended' ? 'warning' : 'info' }
    )
  } catch {
    return
  }
  try {
    await api.setMyTenantStatus(t.schema, next)
    ElMessage.success(`已${action}`)
    await load()
  } catch (err) {
    handleError(err)
  }
}

// 拉某租户的工作流运行参数：system 一并回带（只作对照），tenant 值统一转成字符串给输入框
async function loadFlowConfigs(schema) {
  flowLoading.value = true
  try {
    const data = await api.getMyTenantFlowConfigs(schema)
    systemFlow.value = data?.system || {}
    const t = data?.tenant || {}
    const obj = {}
    for (const f of FLOW_FIELDS) {
      const v = t[f.key]
      obj[f.key] = v === null || v === undefined ? '' : String(v)
    }
    tenantFlow.value = obj
  } catch (err) {
    handleError(err)
  } finally {
    flowLoading.value = false
  }
}

// 保存租户行：空串 → null（未设置）；前端先按"不超过系统级"拦一道（文案与后端同口径）
async function submitFlow() {
  const t = selected.value
  if (!t) return

  for (const f of FLOW_FIELDS) {
    const sysVal = systemFlow.value[f.key]
    if (sysVal === null || sysVal === undefined || sysVal === -1) continue
    const raw = tenantFlow.value[f.key]
    if (raw === '' || raw === null || raw === undefined) continue
    const num = Number(raw)
    if (Number.isNaN(num) || num === -1) continue
    if (num > sysVal) {
      ElMessage.error(`${f.label}不能超过系统级值 ${sysVal}`)
      return
    }
  }

  const body = {}
  for (const f of FLOW_FIELDS) {
    const raw = tenantFlow.value[f.key]
    if (raw === '' || raw === null || raw === undefined) {
      body[f.key] = null
    } else {
      const n = Number(raw)
      body[f.key] = Number.isNaN(n) ? null : n
    }
  }

  flowSaving.value = true
  try {
    await api.setMyTenantFlowConfigs(t.schema, body)
    ElMessage.success('工作流运行参数已保存，立即生效')
    await loadFlowConfigs(t.schema)
  } catch (err) {
    handleError(err)
  } finally {
    flowSaving.value = false
  }
}

// ===== 智能体运行参数（platform.agent_configs 的租户行；与工作流运行参数同一套交互） =====
// 唯一差异：本表没有 -1 —— 四项都是安全阀，只接受 >= 1（后端同样会拒）。
const AGENT_FIELDS = [
  { key: 'max_iterations', label: '最大运行轮次（轮）' },
  { key: 'max_call_depth', label: '最大调用深度（层）' },
  { key: 'max_tools', label: '工具数量上限（个）' },
  { key: 'max_output_bytes', label: '命令输出上限（字节）' }
]
const agentLoading = ref(false)
const agentSaving = ref(false)
const systemAgent = ref({})
const tenantAgent = ref({})

async function loadAgentConfigs(schema) {
  agentLoading.value = true
  try {
    const data = await api.getMyTenantAgentConfigs(schema)
    systemAgent.value = data?.system || {}
    const t = data?.tenant || {}
    const obj = {}
    for (const f of AGENT_FIELDS) {
      const v = t[f.key]
      obj[f.key] = v === null || v === undefined ? '' : String(v)
    }
    tenantAgent.value = obj
  } catch (err) {
    handleError(err)
  } finally {
    agentLoading.value = false
  }
}

async function submitAgent() {
  const t = selected.value
  if (!t) return

  for (const f of AGENT_FIELDS) {
    const raw = tenantAgent.value[f.key]
    if (raw === '' || raw === null || raw === undefined) continue
    const num = Number(raw)
    if (Number.isNaN(num)) {
      ElMessage.error(`${f.label}必须填数字`)
      return
    }
    if (num < 1) {
      ElMessage.error(`${f.label}必须 >= 1（本组配置不支持 -1 不限制）`)
      return
    }
    const sysVal = systemAgent.value[f.key]
    if (sysVal !== null && sysVal !== undefined && num > sysVal) {
      ElMessage.error(`${f.label}不能超过系统级值 ${sysVal}`)
      return
    }
  }

  const body = {}
  for (const f of AGENT_FIELDS) {
    const raw = tenantAgent.value[f.key]
    if (raw === '' || raw === null || raw === undefined) {
      body[f.key] = null
    } else {
      const n = Number(raw)
      body[f.key] = Number.isNaN(n) ? null : n
    }
  }

  agentSaving.value = true
  try {
    await api.setMyTenantAgentConfigs(t.schema, body)
    ElMessage.success('智能体运行参数已保存，立即生效')
    await loadAgentConfigs(t.schema)
  } catch (err) {
    handleError(err)
  } finally {
    agentSaving.value = false
  }
}

// ===== 配额（platform.quota_configs 的租户行，五项都可自助修改） =====
// 五项都受系统顶约束（后端硬校验，超了直接拒）；空间界面按 MiB 输入，提交换算成字节，
// -1（不限）原样传，不参与换算。usageKey 只有前三项有「当前用量」可看。
const MIB = 1024 * 1024
const QUOTA_FIELDS = [
  { key: 'max_agents', label: '智能体（个）', unit: 'count', usageKey: 'agents', short: '智能体' },
  { key: 'max_skills', label: '技能（个）', unit: 'count', usageKey: 'skills', short: '技能' },
  { key: 'max_flows', label: '工作流（个）', unit: 'count', usageKey: 'flows', short: '工作流' },
  { key: 'max_session_storage_bytes', label: '单会话空间上限（MiB）', unit: 'mib', short: '会话空间' },
  { key: 'max_user_sessions', label: '单用户会话数上限（个）', unit: 'count', short: '会话数' }
]
const quotaLoading = ref(false)
const quotaSaving = ref(false)
const systemQuota = ref({})
const fallbackQuota = ref({})
const tenantQuota = ref({})
const effectiveQuota = ref({})

// 字节 → 界面上的 MiB 文本（非整 MiB 保留一位小数；-1 当「不限」）
function bytesToMibText(v) {
  if (typeof v !== 'number' || v < 0) return '不限'
  return Number.isInteger(v / MIB) ? String(v / MIB) : (v / MIB).toFixed(1)
}

// 「系统限额」列的文本：null = 更上层也没设（实际走代码兜底，这里只表达"跟随"）
function quotaText(f, v) {
  if (v === null || v === undefined) return '跟随更上层'
  if (v === -1) return '不限'
  return f.unit === 'mib' ? `${bytesToMibText(v)} MiB` : `${v} 个`
}

// 系统限额 = 系统行，未设置时退到代码兜底值（并标注"兜底"，让人知道这不是谁配的）
function systemLimitText(f) {
  const v = systemQuota.value[f.key]
  if (v === null || v === undefined) {
    const fb = fallbackQuota.value[f.key]
    return quotaText(f, fb) + '（兜底）'
  }
  return quotaText(f, v)
}

// 配额表的行：标签 / 当前用量 / 系统顶文本 / 是否超限（用量超过生效值标红）
const quotaEditRows = computed(() => {
  const t = selected.value
  if (!t) return []
  return QUOTA_FIELDS.map((f) => {
    const used = f.usageKey ? (t.usage?.[f.usageKey] ?? 0) : null
    const limit = t.quota?.[f.key]
    return {
      ...f,
      used,
      over: used !== null && typeof limit === 'number' && limit >= 0 && used > limit,
      systemText: systemLimitText(f)
    }
  })
})

// 「当前生效」一行：跟着后端算好的 effective 走，前端不重算 min 规则
const effectiveQuotaText = computed(() => {
  const e = effectiveQuota.value || {}
  return QUOTA_FIELDS.map((f) => {
    const v = e[f.key]
    const text =
      v === -1
        ? '不限'
        : v === null || v === undefined
          ? '—'
          : f.unit === 'mib'
            ? bytesToMibText(v) + ' MiB'
            : String(v)
    return `${f.short} ${text}`
  }).join(' · ')
})

// 拉本租户配额：原始行（灌输入框）+ 系统限额 + 生效值（展示用，后端算好回带）
async function loadQuota(schema) {
  quotaLoading.value = true
  try {
    const data = await api.getMyTenantQuota(schema)
    systemQuota.value = data?.system || {}
    fallbackQuota.value = data?.fallback || {}
    effectiveQuota.value = data?.effective || {}
    const row = data?.quota || {}
    const obj = {}
    for (const f of QUOTA_FIELDS) {
      const v = row[f.key]
      obj[f.key] = v === null || v === undefined ? '' : f.unit === 'mib' ? bytesToMibText(v) : String(v)
    }
    tenantQuota.value = obj
  } catch (err) {
    handleError(err)
  } finally {
    quotaLoading.value = false
  }
}

// 保存：空串 → null（回退系统限额）；-1 原样；空间 MiB → 字节
async function submitQuota() {
  const t = selected.value
  if (!t) return
  const body = {}
  for (const f of QUOTA_FIELDS) {
    const raw = String(tenantQuota.value[f.key] ?? '').trim()
    if (raw === '') {
      body[f.key] = null
      continue
    }
    if (!/^-?\d+$/.test(raw)) {
      ElMessage.error('只能填整数（-1 表示不限，留空表示跟随系统限额）')
      return
    }
    const n = Number(raw)
    body[f.key] = f.unit === 'mib' ? (n === -1 ? -1 : n * MIB) : n
  }
  quotaSaving.value = true
  try {
    await api.setMyTenantQuota(t.schema, body)
    ElMessage.success('会话配额已保存，立即生效')
    await loadQuota(t.schema)
  } catch (err) {
    handleError(err)
  } finally {
    quotaSaving.value = false
  }
}

// 给本租户根账号 primary 重置密码：只改密码（角色/状态不碰），校验与租户侧一致（≥8 位、两次一致）
async function submitPrimaryPassword() {
  const t = selected.value
  if (!t) return
  const pwd = pwdForm.password
  if (!pwd || pwd.length < 8) return ElMessage.error('新密码至少 8 位')
  if (pwd !== pwdForm.confirm) return ElMessage.error('两次输入的新密码不一致')

  pwdSaving.value = true
  try {
    await api.resetMyTenantPrimaryPassword(t.schema, pwd)
    ElMessage.success('primary 密码已重置，请用新密码走「租户登录」')
    pwdForm.password = ''
    pwdForm.confirm = ''
  } catch (err) {
    handleError(err)
  } finally {
    pwdSaving.value = false
  }
}

onMounted(load)
</script>

<style scoped>
/* ===== 三层灰阶（与「租户管理」同款）=====
   大背景 #f0f2f5 → 两张灰卡 .list-pane / .detail-pane（#fafafa）→ 白卡 .t-card / .blk */
.section {
  flex: 1;
  min-height: 0;
  display: flex;
  flex-direction: column;
  padding: 0;
  box-sizing: border-box;
  overflow: hidden;
  background: #f0f2f5;
  border-radius: 12px;
}

/* ===== 主从布局：左右各一张灰卡，间隙 6px（两卡同色，这条缝是唯一分界） ===== */
.split {
  flex: 1;
  min-height: 0;
  display: flex;
  gap: 6px;
  padding: 6px 0;
}

/* 左：列表灰卡（自由宽）。比大背景提亮一档，白卡再浮在它上面 */
.list-pane {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
  padding: 8px 8px 4px;
  box-sizing: border-box;
  overflow: hidden;
  background: #fafafa;
  border-radius: 12px;
}

/* 列表卡内的控制行：计数 / 筛选 / 刷新 */
.lp-bar {
  flex-shrink: 0;
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
  padding-bottom: 10px;
}

.lp-count {
  font-size: 13px;
  color: #909399;
  white-space: nowrap;
}

/* 筛选控件宽固定：选项文案长短不会把这行挤变形（与「租户管理」同款） */
.f-select {
  width: 128px;
  flex-shrink: 0;
}

.f-search {
  width: 200px;
  flex-shrink: 0;
}

/* 租户卡片列表：本页的列表滚动容器 */
.t-list {
  flex: 1;
  min-height: 0;
  overflow: auto;
  scrollbar-gutter: stable;
}

/* 卡片滚动条（列表与详情两处共用一套）：位置常驻（一直 6px），
   只是拇指透明 → 悬浮才上色，这样悬浮时内容不会横向跳动。 */
.t-list,
.detail-pane {
  scrollbar-width: thin;
  scrollbar-color: transparent transparent;
}

.t-list::-webkit-scrollbar,
.detail-pane::-webkit-scrollbar {
  width: 6px;
  height: 6px;
}

.t-list::-webkit-scrollbar-track,
.detail-pane::-webkit-scrollbar-track {
  background: transparent;
}

.t-list::-webkit-scrollbar-thumb,
.detail-pane::-webkit-scrollbar-thumb {
  background: transparent;
  border-radius: 3px;
}

.t-list:hover,
.detail-pane:hover {
  scrollbar-color: rgba(144, 147, 153, 0.4) transparent;
}

.t-list:hover::-webkit-scrollbar-thumb,
.detail-pane:hover::-webkit-scrollbar-thumb {
  background: rgba(144, 147, 153, 0.4);
}

/* ===== 租户卡片网格：与租户端「智能体卡片」同款（一行多张，每张 270px 起、随行宽浮动） ===== */
.t-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(200px, 1fr));
  gap: 14px;
  align-content: start;
}

/* 租户卡片：一个租户一张，整块可点。样式对齐租户端「智能体卡片」——
   白底 + 1px 边框 + 12px 圆角 + 同款阴影，在同样是白色的列表卡上靠边框与阴影分界；
   选中态是本页特有（智能体卡片没有"选中"概念）：改用实蓝边 + 蓝色调阴影。 */
.t-card {
  position: relative;
  display: block;
  width: 100%;
  min-width: 200px;
  max-width: 250px;
  box-sizing: border-box;
  text-align: left;
  font-family: inherit;
  background-color: #fff;
  border: 1px solid #e8e8e8;
  border-radius: 12px;
  padding: 16px;
  cursor: pointer;
  transition: all 0.3s ease;
  box-shadow: 0 2px 8px rgba(0, 0, 0, 0.1);
}

/* 悬浮：阴影加重（与智能体卡片同款，不换底色） */
.t-card:hover {
  box-shadow: 0 4px 16px rgba(0, 0, 0, 0.15);
}

/* 选中：蓝边 + 浅蓝底（灰底上只换边框看不出选的是哪张） */
.t-card.active {
  border-color: #409eff;
  background: #ecf5ff;
  box-shadow: 0 2px 8px rgba(64, 158, 255, 0.18);
}

/* 悬停在已选中的那张上：蓝边不变，阴影再重一点 */
.t-card.active:hover {
  border-color: #409eff;
  box-shadow: 0 4px 16px rgba(64, 158, 255, 0.24);
}

/* ===== 卡片头部：与智能体卡片同款（一条下分隔线；名字 14/600，第二行 schema + 状态） ===== */
.card-header {
  display: flex;
  align-items: center;
  margin-bottom: 10px;
  padding-bottom: 8px;
  border-bottom: 1px solid #f0f0f0;
}

.header-content {
  flex: 1;
  min-width: 0;
}

.card-title {
  font-size: 14px;
  font-weight: 600;
  color: #555;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.card-description {
  display: flex;
  align-items: center;
  gap: 6px;
  margin-top: 4px;
  font-size: 12px;
  color: #999;
}

/* 副行里的 schema（名字）小字 */
.tc-schema {
  flex-shrink: 0;
  font-size: 12px;
  color: #909399;
}

/* ===== 卡体：label/value 行（同智能体卡片的 .info-item / .card-body-bottom） ===== */
.card-body {
  font-size: 14px;
  color: #666;
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.info-item {
  display: flex;
  align-items: baseline;
  gap: 4px;
}

.info-label {
  font-size: 12px;
  color: #999;
  font-weight: 500;
  flex-shrink: 0;
}

.info-value {
  font-size: 13px;
  color: #333;
  word-break: break-all;
}

/* 备注：最多两行，超出省略（完整内容看 title / 右卡「租户信息」） */
.description-value {
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow: hidden;
  text-overflow: ellipsis;
  line-height: 1.5;
}

/* 没写备注时的兜底文案：淡一点 + 斜体，别让人当成真备注 */
.info-value.empty {
  color: #c0c4cc;
  font-style: italic;
}

/* 用量：一行内多个「智 3/10」小项，超限标红（由 .tc-u.over 上色） */
.tc-usage {
  display: flex;
  flex-wrap: wrap;
  gap: 4px 10px;
  font-size: 12px;
  color: #606266;
}

.tc-u.over {
  color: #f56c6c;
}

/* ===== 右：详情灰卡（固定宽，与「租户管理」同款） =====
   680px：里面不只是只读信息，还有配额表（四列）与三组可编辑参数；
   400px 时配额输入框会被挤到只剩几个字符。 */
.detail-pane {
  width: 680px;
  flex-shrink: 0;
  overflow: auto;
  padding: 10px 12px;
  box-sizing: border-box;
  background: #fafafa;
  border-radius: 12px;
}

.d-head {
  display: flex;
  align-items: center;
  gap: 10px;
}

.d-title {
  display: flex;
  align-items: center;
  gap: 8px;
  flex: 1;
  min-width: 0;
  flex-wrap: wrap;
}

.d-name {
  font-size: 16px;
  font-weight: 600;
  color: #303133;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.d-sub,
.d-plan {
  font-size: 12px;
  color: #909399;
}

/* 信息行：与「租户管理」同款的 flex + 40px 间隔（日期那种长值也不会折行） */
.meta-grid {
  display: flex;
  gap: 40px;
  margin-top: 14px;
  padding: 12px 0;
  border-top: 1px solid #f0f2f5;
  border-bottom: 1px solid #f0f2f5;
}

.meta-label {
  font-size: 12px;
  color: #909399;
  margin-bottom: 4px;
}

.meta-value {
  font-size: 14px;
  color: #303133;
}

/* ===== 可折叠卡片：一个区块一张卡 ===== */
/* 折叠区块在灰底上要自己立起来，否则 block 头（#fafafa）与页面底色糊成一片 */
.blk {
  margin-top: 12px;
  background: #fff;
  border: 1px solid #ebeef5;
  border-radius: 8px;
  overflow: hidden;
}

.blk-head {
  width: 100%;
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 9px 12px;
  background: #fafafa;
  border: none;
  font-family: inherit;
  text-align: left;
  cursor: pointer;
  transition: background 0.15s;
}

.blk-head:hover {
  background: #f5f7fa;
}

.blk-title {
  font-size: 13px;
  font-weight: 500;
  color: #303133;
}

.blk-hint {
  font-size: 12px;
  color: #a8abb2;
}

.blk-arrow {
  margin-left: auto;
  font-size: 12px;
  color: #909399;
  transition: transform 0.2s;
}

.blk-arrow.open {
  transform: rotate(90deg);
}

.blk-body {
  padding: 12px;
}

/* 卡片底部：tip 靠左、按钮靠右（本页之前缺这条，按钮一直掉在下一行） */
.blk-foot {
  display: flex;
  align-items: center;
  gap: 10px;
  justify-content: flex-end;
  margin-top: 12px;
}

.blk-foot .tip {
  margin-right: auto;
}

.blk-tip {
  margin-top: 10px;
  font-size: 12px;
  color: #909399;
  line-height: 1.6;
}

/* 用量与配额：名称 / 用量 / 进度（只读，没有输入框） */
/* 配额：名称 150px（比租户管理的 132px 宽一点 —— 这里的标签多带了「上限」二字） */
.quota-head,
.quota-grid-row {
  display: grid;
  grid-template-columns: 150px 64px 92px 1fr;
  align-items: center;
  gap: 10px;
  padding: 8px 12px;
}

.quota-head {
  background: #fafafa;
  font-size: 12px;
  font-weight: 600;
  color: #909399;
}

.quota-grid-row + .quota-grid-row {
  border-top: 1px solid #f0f2f5;
}

.q-usage {
  font-size: 13px;
  color: #303133;
}

.q-usage.over {
  color: #f56c6c;
}

/* 租户信息：左标签 + 右值（纯阅读，不是输入框） */
.kv {
  display: grid;
  grid-template-columns: 56px 1fr;
  gap: 10px;
  align-items: baseline;
  padding: 5px 0;
}

.kv-label {
  font-size: 13px;
  color: #909399;
}

.kv-value {
  font-size: 13px;
  color: #303133;
  word-break: break-all;
}

/* 工作流运行参数：配置项 / 系统级（只读）/ 租户配置（输入框） 三列 */
.flow-table {
  border: 1px solid #ebeef5;
  border-radius: 6px;
  overflow: hidden;
}

.flow-head,
.flow-row {
  display: grid;
  grid-template-columns: 1.2fr 72px 1fr;
  align-items: center;
  gap: 10px;
  padding: 8px 12px;
}

.flow-head {
  background: #fafafa;
  font-size: 12px;
  font-weight: 600;
  color: #909399;
}

.flow-row + .flow-row {
  border-top: 1px solid #f0f2f5;
}

.flow-name {
  font-size: 13px;
  color: #303133;
  white-space: nowrap;
}

.flow-sys {
  font-size: 13px;
  color: #606266;
}

/* 根账号密码：左标签 + 右输入框 */
.form-grid {
  display: grid;
  grid-template-columns: 56px 1fr;
  align-items: center;
  gap: 10px;
}

.f-label {
  font-size: 13px;
  color: #606266;
}

.mono {
  font-family: Consolas, Monaco, monospace;
  color: #303133;
}

.tip {
  margin-top: 12px;
  font-size: 12px;
  color: #909399;
  line-height: 1.6;
}
</style>
