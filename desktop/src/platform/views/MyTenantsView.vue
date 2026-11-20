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
          <span class="lp-count">{{ t('platform.myTenants.count', { n: filtered.length }) }}</span>
          <el-input
            v-model="q"
            class="f-search"
            size="small"
            :placeholder="t('platform.myTenants.searchPlaceholder')"
            clearable
          >
            <template #prefix><el-icon><Search /></el-icon></template>
          </el-input>
          <el-select v-model="statusFilter" class="f-select" size="small" :placeholder="t('platform.users.statusAll')" clearable>
            <el-option :label="t('platform.users.statusNormal')" value="active" />
            <el-option :label="t('platform.users.statusDisabled')" value="suspended" />
            <el-option :label="t('platform.users.statusPending')" value="pending" />
          </el-select>
          <el-button size="small" :icon="Refresh" :loading="loading" @click="load">{{ t('common.refresh') }}</el-button>
        </div>

        <div v-loading="loading" class="t-list">
          <el-empty
            v-if="!loading && !filtered.length"
            :description="items.length ? t('platform.myTenants.emptyFiltered') : t('platform.myTenants.emptyNone')"
          />

          <!-- 网格流式：一行放多张，每张 200px 起、随行宽浮动 -->
          <div class="t-grid">
            <button
              v-for="tn in filtered"
              :key="tn.schema"
              type="button"
              class="t-card"
              :class="{ active: tn.schema === selected?.schema }"
              @click="select(tn)"
            >
              <!-- 头部：显示名 + schema + 状态（对齐租户端「智能体卡片」：头部带一条下分隔线） -->
              <div class="card-header">
                <div class="header-content">
                  <div class="card-title">{{ tn.display_name || tn.schema }}</div>
                  <div class="card-description">
                    <span class="mono tc-schema">{{ tn.schema }}</span>
                    <el-tag size="small" :type="statusType(tn.status)" effect="light">{{ statusText(tn.status) }}</el-tag>
                  </div>
                </div>
              </div>
              <!-- 卡体：label/value 行（同智能体卡片的 .info-item） -->
              <div class="card-body">
                <div class="info-item">
                  <span class="info-label">{{ t('platform.myTenants.labelUsage') }}</span>
                  <span class="info-value tc-usage">
                    <span v-for="m in QUOTA_META" :key="m.key" class="tc-u" :class="{ over: isOver(tn, m) }">
                      {{ m.short }} {{ tn.usage[m.usageKey] }}/{{ limitBrief(tn.quota[m.key]) }}
                    </span>
                  </span>
                </div>
                <!-- 备注：没有备注也占一行（兜底文案），有备注时可悬停看全文 -->
                <div class="info-item">
                  <span class="info-label">{{ t('platform.myTenants.labelRemarkColon') }}</span>
                  <span
                    class="info-value description-value"
                    :class="{ empty: !tn.remark }"
                    :title="tn.remark || ''"
                  >{{ tn.remark || EMPTY_REMARK }}</span>
                </div>
              </div>
            </button>
          </div>
        </div>
      </div>

      <section class="detail-pane">
        <el-empty v-if="!selected" :description="t('platform.myTenants.selectTenant')" />

        <template v-else>
          <!-- 卡头：身份 + 状态 + 启停（本页唯一的写操作） -->
          <div class="d-head">
            <div class="d-title">
              <span class="d-name">{{ selected.display_name || selected.schema }}</span>
              <span class="mono d-sub">{{ selected.schema }}</span>
              <el-tag size="small" :type="statusType(selected.status)">{{ statusText(selected.status) }}</el-tag>
              <span v-if="selected.plan" class="d-plan">{{ t('platform.myTenants.planTag', { plan: selected.plan }) }}</span>
            </div>
            <el-button
              size="small"
              :type="selected.status === 'active' ? 'danger' : 'success'"
              @click="toggleStatus(selected)"
            >
              {{ selected.status === 'active' ? t('platform.users.disable') : t('platform.users.enable') }}
            </el-button>
          </div>

          <div class="meta-grid">
            <div class="meta-cell">
              <div class="meta-label">{{ t('platform.users.labelCreatedAt')}}</div>
              <div class="meta-value">{{ fmtTime(selected.created_at) }}</div>
            </div>
            <div class="meta-cell">
              <div class="meta-label">{{ t('platform.users.labelUpdatedAt')}}</div>
              <div class="meta-value">{{ fmtTime(selected.updated_at) }}</div>
            </div>
          </div>

          <!-- 可折叠卡片①：配额（五项都可自助修改，但都受系统顶约束）
               超系统顶后端直接拒（不像管理员那条截断），所以这里不显示"已截断"的提示。 -->
          <div class="blk">
            <button type="button" class="blk-head" @click="open.quota = !open.quota">
              <span class="blk-title">{{ t('platform.myTenants.quotaTitle')}}</span>
              <span class="blk-hint">{{ t('platform.myTenants.quotaHint')}}</span>
              <el-icon class="blk-arrow" :class="{ open: open.quota }"><ArrowRight /></el-icon>
            </button>
            <div v-show="open.quota" class="blk-body">
              <div v-loading="quotaLoading" class="quota-table">
                <div class="quota-head">
                  <span>{{ t('platform.myTenants.quotaColItem') }}</span>
                  <span>{{ t('platform.myTenants.quotaColUsed') }}</span>
                  <span>{{ t('platform.myTenants.quotaColSystem') }}</span>
                  <span>{{ t('platform.myTenants.quotaColTenant') }}</span>
                </div>
                <div v-for="row in quotaEditRows" :key="row.key" class="quota-grid-row">
                  <span class="flow-name">{{ row.label }}</span>
                  <span class="q-usage" :class="{ over: row.over }">{{ row.used === null ? '—' : row.used }}</span>
                  <span class="flow-sys">{{ row.systemText }}</span>
                  <el-input v-model="tenantQuota[row.key]" size="small" :placeholder="t('platform.myTenants.phFallbackSystem')" />
                </div>
              </div>

              <div class="blk-tip">
                {{ t('platform.myTenants.quotaTip1') }}<b>{{ t('platform.myTenants.quotaTipBold') }}</b>{{ t('platform.myTenants.quotaTip2') }}
              </div>
              <div class="blk-foot">
                <span class="tip">{{ t('platform.myTenants.currentEffective', { text: effectiveQuotaText }) }}</span>
                <el-button size="small" type="primary" :loading="quotaSaving" @click="submitQuota">
                  {{ t('platform.myTenants.saveQuota') }}
                </el-button>
              </div>
            </div>
          </div>

          <!-- 可折叠卡片②：根账号密码（默认收起；忘记 primary 密码时的自助恢复口） -->
          <div class="blk">
            <button type="button" class="blk-head" @click="open.primaryPwd = !open.primaryPwd">
              <span class="blk-title">{{ t('platform.myTenants.primaryPwdTitle')}}</span>
              <span class="blk-hint">primary</span>
              <el-icon class="blk-arrow" :class="{ open: open.primaryPwd }"><ArrowRight /></el-icon>
            </button>
            <div v-show="open.primaryPwd" class="blk-body">
              <div class="form-grid">
                <label class="f-label">{{ t('password.new') }}</label>
                <el-input v-model="pwdForm.password" size="small" type="password" show-password :placeholder="t('password.newPlaceholder')" />

                <label class="f-label">{{ t('platform.users.labelConfirm')}}</label>
                <el-input
                  v-model="pwdForm.confirm"
                  size="small"
                  type="password"
                  show-password
                  :placeholder="t('platform.users.phConfirm')"
                  @keyup.enter="submitPrimaryPassword"
                />
              </div>

              <div class="blk-tip">
                {{ t('platform.myTenants.primaryPwdTip1') }}<b>primary</b>{{ t('platform.myTenants.primaryPwdTip2') }}
              </div>
              <div class="blk-foot">
                <span class="tip">{{ t('platform.myTenants.primaryPwdSessionTip')}}</span>
                <el-button size="small" type="primary" :loading="pwdSaving" @click="submitPrimaryPassword">
                  {{ t('platform.myTenants.resetPrimaryPwd') }}
                </el-button>
              </div>
            </div>
          </div>

          <!-- 可折叠卡片③：工作流运行参数（可自助修改；系统级只读对照） -->
          <div class="blk">
            <button type="button" class="blk-head" @click="open.flow = !open.flow">
              <span class="blk-title">{{ t('platform.myTenants.flowConfigsTitle')}}</span>
              <span class="blk-hint">{{ t('platform.myTenants.selfService')}}</span>
              <el-icon class="blk-arrow" :class="{ open: open.flow }"><ArrowRight /></el-icon>
            </button>
            <div v-show="open.flow" class="blk-body">
              <div v-loading="flowLoading" class="flow-table">
                <div class="flow-head">
                  <span>{{ t('platform.myTenants.colItem') }}</span>
                  <span>{{ t('platform.myTenants.colSystemLevel') }}</span>
                  <span>{{ t('platform.myTenants.colTenantValue') }}</span>
                </div>
                <div v-for="f in FLOW_FIELDS" :key="f.key" class="flow-row">
                  <span class="flow-name">{{ f.label }}</span>
                  <span class="flow-sys">{{ flowText(systemFlow[f.key]) }}</span>
                  <el-input v-model="tenantFlow[f.key]" size="small" :placeholder="t('platform.myTenants.phFallbackSystemLevel')" />
                </div>
              </div>

              <div class="blk-tip">
                {{ t('platform.myTenants.flowTip') }}
              </div>
              <div class="blk-foot">
                <span class="tip">{{ t('platform.myTenants.scopeTip')}}</span>
                <el-button size="small" type="primary" :loading="flowSaving" @click="submitFlow">
                  {{ t('platform.myTenants.saveFlowConfigs') }}
                </el-button>
              </div>
            </div>
          </div>

          <!-- 可折叠卡片④：智能体运行参数（可自助修改；系统级只读对照） -->
          <div class="blk">
            <button type="button" class="blk-head" @click="open.agent = !open.agent">
              <span class="blk-title">{{ t('platform.myTenants.agentConfigsTitle')}}</span>
              <span class="blk-hint">{{ t('platform.myTenants.selfService')}}</span>
              <el-icon class="blk-arrow" :class="{ open: open.agent }"><ArrowRight /></el-icon>
            </button>
            <div v-show="open.agent" class="blk-body">
              <div v-loading="agentLoading" class="flow-table">
                <div class="flow-head agent-head">
                  <span>{{ t('platform.myTenants.colItem') }}</span>
                  <span>{{ t('platform.myTenants.colSystemLevel') }}</span>
                  <span>{{ t('platform.myTenants.colTenantValue') }}</span>
                  <span :title="t('platform.myTenants.colEffectiveHint')">{{ t('platform.myTenants.colEffective') }}</span>
                </div>
                <div v-for="f in AGENT_FIELDS" :key="f.key" class="flow-row agent-row">
                  <span class="flow-name">{{ f.label }}</span>
                  <span class="flow-sys">{{ flowText(systemAgent[f.key]) }}</span>
                  <el-input v-model="tenantAgent[f.key]" size="small" :placeholder="t('platform.myTenants.phFallbackSystemLevel')" />
                  <span class="flow-eff">{{ flowText(effectiveAgent[f.key]) }}</span>
                </div>
              </div>

              <div class="blk-tip">
                {{ t('platform.myTenants.agentTip1') }}<b>{{ t('platform.myTenants.agentTipBold') }}</b>{{ t('platform.myTenants.agentTip2') }}
              </div>
              <div class="blk-foot">
                <span class="tip">{{ t('platform.myTenants.scopeTip')}}</span>
                <el-button size="small" type="primary" :loading="agentSaving" @click="submitAgent">
                  {{ t('platform.myTenants.saveAgentConfigs') }}
                </el-button>
              </div>
            </div>
          </div>

          <!-- 可折叠卡片⑤：租户信息（纯只读，不是输入框） -->
          <div class="blk">
            <button type="button" class="blk-head" @click="open.info = !open.info">
              <span class="blk-title">{{ t('platform.myTenants.infoTitle')}}</span>
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
            {{ t('platform.myTenants.footTip') }}
          </div>
        </template>
      </section>
    </div>
  </div>
</template>

<script setup>
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { ElMessage, ElMessageBox } from 'element-plus'
import { ArrowRight, Refresh, Search } from '@element-plus/icons-vue'
import { api } from '../api'

// count：把租户数上抛给顶栏导航的标签（「我的租户 (N)」，与管理员页同款）
const emit = defineEmits(['count', 'session-expired'])

const { t } = useI18n()

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

const QUOTA_META = computed(() => [
  { key: 'max_agents', usageKey: 'agents', label: t('platform.myTenants.quotaFieldAgents'), short: t('platform.myTenants.quotaShortAgents') },
  { key: 'max_skills', usageKey: 'skills', label: t('platform.myTenants.quotaFieldSkills'), short: t('platform.myTenants.quotaShortSkills') },
  { key: 'max_flows', usageKey: 'flows', label: t('platform.myTenants.quotaFieldFlows'), short: t('platform.myTenants.quotaShortFlows') }
])

// 备注为空时的兜底文案（卡片与右卡「租户信息」共用一份，别写两遍）
const EMPTY_REMARK = computed(() => t('platform.myTenants.emptyRemark'))

// ===== 工作流运行参数（platform.flow_configs 的租户行；系统级只读对照） =====
const FLOW_FIELDS = computed(() => [
  { key: 'max_timeout', label: t('platform.system.flowConfigs.max_timeout') },
  { key: 'max_recursion_depth', label: t('platform.system.flowConfigs.max_recursion_depth') },
  { key: 'max_node_count', label: t('platform.system.flowConfigs.max_node_count') },
  { key: 'max_exec_count', label: t('platform.system.flowConfigs.max_exec_count') },
  { key: 'max_loop_times', label: t('platform.system.flowConfigs.max_loop_times') },
  { key: 'max_run_records', label: t('platform.system.flowConfigs.max_run_records') },
  { key: 'max_session_records', label: t('platform.system.flowConfigs.max_session_records') }
])
const flowLoading = ref(false)
const flowSaving = ref(false)
const systemFlow = ref({})
const tenantFlow = ref({})
const flowText = (v) => (v === null || v === undefined || v === '' ? t('platform.myTenants.unset') : String(v))

const statusText = (s) =>
  ({
    active: t('platform.users.statusNormal'),
    suspended: t('platform.users.statusDisabled'),
    deleted: t('platform.users.statusDeleted'),
    pending: t('platform.users.statusPending')
  })[s] || s
const statusType = (s) => ({ active: 'success', suspended: 'warning', deleted: 'info', pending: 'info' }[s] || 'info')
const limitBrief = (v) => (v === -1 ? '∞' : String(v ?? '-'))
// 列表里的用量摘要：上限 >= 0 且用量超出才标红（-1 = 不限制）
const isOver = (tn, m) => tn.quota[m.key] >= 0 && tn.usage[m.usageKey] > tn.quota[m.key]

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
  const tn = selected.value
  if (!tn) return []
  return [
    { label: 'schema', value: tn.schema, mono: true },
    { label: t('platform.myTenants.labelDisplayName'), value: tn.display_name },
    { label: t('platform.myTenants.labelPlan'), value: tn.plan },
    { label: t('platform.myTenants.labelStatus'), value: statusText(tn.status) },
    { label: t('platform.myTenants.labelRemark'), value: tn.remark || EMPTY_REMARK.value }
  ]
})

const fmtTime = (s) => {
  if (!s) return '—'
  const d = new Date(s)
  if (Number.isNaN(d.getTime())) return String(s).replace('T', ' ').slice(0, 16)
  const p = (n) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`
}

function select(tn) {
  if (!tn || tn.schema === selected.value?.schema) return
  selected.value = tn
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
  ElMessage.error(err?.message || t('platform.shared.requestFailed'))
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
async function toggleStatus(tn) {
  if (!tn) return
  const next = tn.status === 'active' ? 'suspended' : 'active'
  const action = next === 'suspended' ? t('platform.users.actionDisable') : t('platform.users.actionEnable')
  try {
    await ElMessageBox.confirm(
      next === 'suspended'
        ? t('platform.myTenants.statusDisableConfirm', { schema: tn.schema })
        : t('platform.myTenants.statusEnableConfirm', { schema: tn.schema }),
      t('platform.users.actionConfirmTitle', { action }),
      { type: next === 'suspended' ? 'warning' : 'info' }
    )
  } catch {
    return
  }
  try {
    await api.setMyTenantStatus(tn.schema, next)
    ElMessage.success(next === 'suspended' ? t('platform.users.disabledDone') : t('platform.users.enabledDone'))
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
    const ten = data?.tenant || {}
    const obj = {}
    for (const f of FLOW_FIELDS.value) {
      const v = ten[f.key]
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
  const tn = selected.value
  if (!tn) return

  for (const f of FLOW_FIELDS.value) {
    const sysVal = systemFlow.value[f.key]
    if (sysVal === null || sysVal === undefined || sysVal === -1) continue
    const raw = tenantFlow.value[f.key]
    if (raw === '' || raw === null || raw === undefined) continue
    const num = Number(raw)
    if (Number.isNaN(num) || num === -1) continue
    if (num > sysVal) {
      ElMessage.error(t('platform.myTenants.overSystemLimit', { label: f.label, value: sysVal }))
      return
    }
  }

  const body = {}
  for (const f of FLOW_FIELDS.value) {
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
    await api.setMyTenantFlowConfigs(tn.schema, body)
    ElMessage.success(t('platform.myTenants.flowSaved'))
    await loadFlowConfigs(tn.schema)
  } catch (err) {
    handleError(err)
  } finally {
    flowSaving.value = false
  }
}

// ===== 智能体运行参数（platform.agent_configs 的租户行；与工作流运行参数同一套交互） =====
// 唯一差异：本表没有 -1 —— 四项都是安全阀，只接受 >= 1（后端同样会拒）。
const AGENT_FIELDS = computed(() => [
  { key: 'max_iterations', label: t('platform.system.agentConfigs.max_iterations') },
  { key: 'max_call_depth', label: t('platform.system.agentConfigs.max_call_depth') },
  { key: 'max_tools', label: t('platform.system.agentConfigs.max_tools') },
  { key: 'max_output_bytes', label: t('platform.system.agentConfigs.max_output_bytes') }
])
const agentLoading = ref(false)
const agentSaving = ref(false)
const systemAgent = ref({})
const tenantAgent = ref({})
// Effective = what the runtime really applies; it differs from the stored tenant value when the ceiling is lower
const effectiveAgent = ref({})

async function loadAgentConfigs(schema) {
  agentLoading.value = true
  try {
    const data = await api.getMyTenantAgentConfigs(schema)
    systemAgent.value = data?.system || {}
    const ten = data?.tenant || {}
    const obj = {}
    for (const f of AGENT_FIELDS.value) {
      const v = ten[f.key]
      obj[f.key] = v === null || v === undefined ? '' : String(v)
    }
    tenantAgent.value = obj
    effectiveAgent.value = data?.effective || {}
  } catch (err) {
    handleError(err)
  } finally {
    agentLoading.value = false
  }
}

async function submitAgent() {
  const tn = selected.value
  if (!tn) return

  for (const f of AGENT_FIELDS.value) {
    const raw = tenantAgent.value[f.key]
    if (raw === '' || raw === null || raw === undefined) continue
    const num = Number(raw)
    if (Number.isNaN(num)) {
      ElMessage.error(t('platform.myTenants.agentNumRequired', { label: f.label }))
      return
    }
    if (num < 1) {
      ElMessage.error(t('platform.myTenants.agentMin', { label: f.label }))
      return
    }
    const sysVal = systemAgent.value[f.key]
    if (sysVal !== null && sysVal !== undefined && num > sysVal) {
      ElMessage.error(t('platform.myTenants.overSystemLimit', { label: f.label, value: sysVal }))
      return
    }
  }

  const body = {}
  for (const f of AGENT_FIELDS.value) {
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
    await api.setMyTenantAgentConfigs(tn.schema, body)
    ElMessage.success(t('platform.myTenants.agentSaved'))
    await loadAgentConfigs(tn.schema)
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
const QUOTA_FIELDS = computed(() => [
  { key: 'max_agents', label: t('platform.myTenants.quotaFieldAgents'), unit: 'count', usageKey: 'agents', short: t('platform.myTenants.quotaShortAgents') },
  { key: 'max_skills', label: t('platform.myTenants.quotaFieldSkills'), unit: 'count', usageKey: 'skills', short: t('platform.myTenants.quotaShortSkills') },
  { key: 'max_flows', label: t('platform.myTenants.quotaFieldFlows'), unit: 'count', usageKey: 'flows', short: t('platform.myTenants.quotaShortFlows') },
  { key: 'max_session_storage_bytes', label: t('platform.myTenants.quotaFieldStorage'), unit: 'mib', short: t('platform.myTenants.quotaShortStorage') },
  { key: 'max_user_sessions', label: t('platform.myTenants.quotaFieldSessions'), unit: 'count', short: t('platform.myTenants.quotaShortSessions') }
])
const quotaLoading = ref(false)
const quotaSaving = ref(false)
const systemQuota = ref({})
const fallbackQuota = ref({})
const tenantQuota = ref({})
const effectiveQuota = ref({})

// 字节 → 界面上的 MiB 文本（非整 MiB 保留一位小数；-1 当「不限」）
function bytesToMibText(v) {
  if (typeof v !== 'number' || v < 0) return t('platform.myTenants.unlimited')
  return Number.isInteger(v / MIB) ? String(v / MIB) : (v / MIB).toFixed(1)
}

// 「系统限额」列的文本：null = 更上层也没设（实际走代码兜底，这里只表达"跟随"）
function quotaText(f, v) {
  if (v === null || v === undefined) return t('platform.myTenants.followHigher')
  if (v === -1) return t('platform.myTenants.unlimited')
  return f.unit === 'mib' ? `${bytesToMibText(v)} MiB` : t('platform.myTenants.unitCount', { n: v })
}

// 系统限额 = 系统行，未设置时退到代码兜底值（并标注"兜底"，让人知道这不是谁配的）
function systemLimitText(f) {
  const v = systemQuota.value[f.key]
  if (v === null || v === undefined) {
    const fb = fallbackQuota.value[f.key]
    return quotaText(f, fb) + t('platform.myTenants.fallbackSuffix')
  }
  return quotaText(f, v)
}

// 配额表的行：标签 / 当前用量 / 系统顶文本 / 是否超限（用量超过生效值标红）
const quotaEditRows = computed(() => {
  const tn = selected.value
  if (!tn) return []
  return QUOTA_FIELDS.value.map((f) => {
    const used = f.usageKey ? (tn.usage?.[f.usageKey] ?? 0) : null
    const limit = tn.quota?.[f.key]
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
  return QUOTA_FIELDS.value.map((f) => {
    const v = e[f.key]
    const text =
      v === -1
        ? t('platform.myTenants.unlimited')
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
    for (const f of QUOTA_FIELDS.value) {
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
  const tn = selected.value
  if (!tn) return
  const body = {}
  for (const f of QUOTA_FIELDS.value) {
    const raw = String(tenantQuota.value[f.key] ?? '').trim()
    if (raw === '') {
      body[f.key] = null
      continue
    }
    if (!/^-?\d+$/.test(raw)) {
      ElMessage.error(t('platform.myTenants.quotaInteger'))
      return
    }
    const n = Number(raw)
    body[f.key] = f.unit === 'mib' ? (n === -1 ? -1 : n * MIB) : n
  }
  quotaSaving.value = true
  try {
    await api.setMyTenantQuota(tn.schema, body)
    ElMessage.success(t('platform.myTenants.quotaSaved'))
    await loadQuota(tn.schema)
  } catch (err) {
    handleError(err)
  } finally {
    quotaSaving.value = false
  }
}

// 给本租户根账号 primary 重置密码：只改密码（角色/状态不碰），校验与租户侧一致（≥8 位、两次一致）
async function submitPrimaryPassword() {
  const tn = selected.value
  if (!tn) return
  const pwd = pwdForm.password
  if (!pwd || pwd.length < 8) return ElMessage.error(t('password.minLength'))
  if (pwd !== pwdForm.confirm) return ElMessage.error(t('password.mismatch'))

  pwdSaving.value = true
  try {
    await api.resetMyTenantPrimaryPassword(tn.schema, pwd)
    ElMessage.success(t('platform.myTenants.primaryPwdReset'))
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

/* Agent run limits carry a fourth column: what the runtime really applies after merging with the ceiling */
.flow-head.agent-head,
.flow-row.agent-row {
  grid-template-columns: 1.2fr 72px 1fr 72px;
}

.flow-eff {
  font-size: 13px;
  color: #606266;
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
