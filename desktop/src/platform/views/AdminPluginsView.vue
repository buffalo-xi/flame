<template>
  <!-- 插件与能力：左 = 两组导航（磁盘插件 / 平台可授权的工具能力），右 = 选中项的详情。
       两边的缺省**不一样**（2026-11-02 起）：
         插件  缺省开放 —— 租户自己启用即可用，平台只在需要时按租户**停用**（写 allowed=false）
         能力  缺省拒绝 —— 能力等于「租户能自己跑代码 / 出网」，必须逐个开
       两边都有全局闸（缺省放行，急停）：闸不是缺省值，它压过租户的显式设置，所以不能混成一层。
       2026-10-21 两边都移除了「系统默认行」：它只改缺省值，和急停挤在一张表里
       会让管理员分不清「我开的」和「本来就没有」。
       能力键名仍是 sandbox.*（exec / shell / script / browser，按进程形态分档：
       一次性 / 常驻 / 池化 / 浏览器），界面统一叫「能力」——
       出网（net.egress）这类同样需要平台授权的工具都归这一组。 -->
  <div class="section">
    <div class="split">
      <aside class="plug-nav">
        <div class="nav-caption">{{ t('platform.plugins.navPluginsCaption') }}</div>
        <div
          v-for="p in plugins"
          :key="p.name"
          class="plug-item"
          :class="{ active: mode === 'plugin' && selected === p.name }"
          @click="pickPlugin(p.name)"
        >
          <div class="plug-item-name">{{ p.display_name || p.name }}</div>
          <div class="plug-item-desc">
            <span class="mono">{{ p.name }}</span>
            <span>{{ t('platform.plugins.implCount', { count: (p.implementations || []).length }) }}</span>
          </div>
          <div class="plug-item-stat">{{ t('platform.plugins.revokedCount', { count: revokedCount(p.name), total: tenants.length }) }}</div>
        </div>
        <div v-if="!plugins.length" class="empty">{{ t('platform.plugins.emptyPlugins') }}</div>

        <div class="nav-caption nav-gap">{{ t('platform.plugins.navCapsCaption') }}</div>
        <div
          v-for="cap in capabilities"
          :key="cap.key"
          class="plug-item"
          :class="{ active: mode === 'cap' && capSelected === cap.key }"
          @click="pickCapability(cap.key)"
        >
          <div class="plug-item-name">{{ capLabel(cap, true) }}</div>
          <div class="plug-item-desc">
            <span class="mono">{{ cap.key }}</span>
          </div>
          <div class="plug-item-stat">
            <template v-if="capLoaded">{{ t('platform.plugins.grantedCount', { count: capGrantedCount(cap.key), total: tenants.length }) }}</template>
            <template v-else>{{ t('platform.plugins.counting') }}</template>
          </div>
        </div>
        <div v-if="!capabilities.length" class="empty">{{ t('platform.plugins.emptyCaps') }}</div>
      </aside>

      <section class="plug-body">
        <div v-if="loading" v-loading="true" class="blk placeholder" />
        <div v-else-if="!current && !capCurrent" class="blk placeholder">{{ t('platform.plugins.selectItem') }}</div>

        <template v-if="current">
          <div class="blk">
            <div class="blk-head">
              <div class="hd-main">
                <span class="blk-title">{{ current.display_name || current.name }}</span>
                <span class="mono">{{ current.name }}</span>
                <span v-if="current.version" class="ver">v{{ current.version }}</span>
              </div>
              <el-button link size="small" @click="load">{{ t('common.refresh') }}</el-button>
            </div>

            <div class="blk-body">
              <div class="desc">{{ current.description || t('platform.plugins.noDescription') }}</div>
              <div v-if="(current.implementations || []).length" class="impls">
                <span v-for="im in current.implementations" :key="im.name" class="impl-tag">
                  {{ im.display_name || im.name }}
                </span>
              </div>

              <!-- 全局停用：唯一带全局语义的东西。没有「系统默认行」了 ——
                   它只改缺省值，与急停是两种东西，混在一起会让人以为是总闸。 -->
              <div class="sub-title">
                {{ t('platform.plugins.gateTitle') }}
                <span class="sub-hint">{{ t('platform.plugins.gateHint') }}</span>
              </div>
              <div class="row">
                <el-switch v-model="gateForm.blocked" :active-text="t('platform.plugins.switchBlocked')" :inactive-text="t('platform.plugins.switchPass')" />
                <span class="hint">
                  {{ gateForm.blocked ? t('platform.plugins.gateOnHint') : t('platform.plugins.gateOffHint') }}
                </span>
                <span class="spacer" />
                <el-button
                  size="small"
                  :disabled="!gateDirty"
                  :loading="saving"
                  :title="t('platform.plugins.saveGateTip')"
                  @click="saveGate"
                >{{ t('common.save') }}</el-button>
                <el-button size="small" link :disabled="!current.blocked" @click="resetGate">{{ t('platform.plugins.resetGate') }}</el-button>
              </div>

              <div class="sub-title">
                {{ t('platform.plugins.tenantTitle') }}
                <span class="sub-hint">{{ t('platform.plugins.tenantHint') }}</span>
              </div>
              <div v-if="current.blocked" class="hint" style="margin-bottom: 8px">
                {{ t('platform.plugins.blockedNote') }}
              </div>
              <div class="hint" style="margin-bottom: 8px">
                {{ t('platform.plugins.draftNote') }}
              </div>
              <el-table :data="rows" size="small" border class="tbl">
                <el-table-column :label="t('platform.plugins.colTenant')" min-width="160">
                  <template #default="{ row }">
                    <span class="t-name">{{ row.display_name }}</span>
                    <span class="mono">{{ row.schema }}</span>
                  </template>
                </el-table-column>
                <el-table-column :label="t('platform.plugins.colSource')" width="110">
                  <template #default="{ row }">
                    <el-tag v-if="row.row && !row.row.allowed" size="small" type="danger">{{ t('platform.plugins.tagDisabled') }}</el-tag>
                    <el-tag
                      v-else-if="row.row"
                      size="small"
                      type="info"
                      :title="t('platform.plugins.explicitAllowTip')"
                    >{{ t('platform.plugins.tagExplicitAllow') }}</el-tag>
                    <el-tag v-else size="small" type="success">{{ t('platform.plugins.tagDefaultOpen') }}</el-tag>
                  </template>
                </el-table-column>
                <el-table-column :label="t('platform.plugins.colTenantSide')" width="90">
                  <template #default="{ row }">
                    <span :class="row.enabled ? 'ok' : 'muted'">{{ row.enabled ? t('platform.plugins.enabled') : t('platform.plugins.notEnabled') }}</span>
                  </template>
                </el-table-column>
                <!-- 开关语义是「停用」：缺省所有租户都可用，打开才写一条 allowed=false -->
                <el-table-column :label="t('platform.plugins.colBlocked')" width="70">
                  <template #default="{ row }">
                    <el-switch
                      :model-value="!row.form.allowed"
                      size="small"
                      @change="(v) => (row.form.allowed = !v)"
                    />
                  </template>
                </el-table-column>
                <el-table-column :label="t('platform.plugins.colActions')" width="150">
                  <template #default="{ row }">
                    <el-button link size="small" :disabled="!rowDirty(row)" @click="saveTenant(row)">{{ t('common.save') }}</el-button>
                    <el-button link size="small" :disabled="!row.row" @click="resetTenant(row)">{{ t('platform.plugins.clearSettings') }}</el-button>
                  </template>
                </el-table-column>
              </el-table>
              <div v-if="!rows.length" class="hint">{{ t('platform.plugins.noTenants') }}</div>
            </div>
          </div>
        </template>

        <!-- 能力：两层，缺一不可 —— 租户授权（缺省拒绝）+ 全局硬闸（缺省放行，急停）。
             与插件的上一版差别：没有「系统默认行」了。它只改缺省值、与急停是两种东西，
             放在一起会让管理员分不清「我关的」和「本来就没有」。区别还有一点：能力没有
             额度字段，只有开/不开。文案刻意不叫「沙箱」：键名（sandbox.exec / sandbox.browser）
             只是按「会不会在沙箱里跑东西」划档。 -->
        <template v-else-if="capCurrent">
          <div class="blk">
            <div class="blk-head">
              <div class="hd-main">
                <span class="blk-title">{{ capLabel(capCurrent) }}</span>
                <span class="mono">{{ capCurrent.key }}</span>
                <el-tag v-if="capBlockedOf(capCurrent.key)" size="small" type="danger">{{ t('platform.plugins.capIntercepted') }}</el-tag>
              </div>
              <el-button link size="small" @click="load">{{ t('common.refresh') }}</el-button>
            </div>

            <div class="blk-body">
              <div class="desc">
                {{ t('platform.plugins.capIntro') }}
              </div>

              <div class="sub-title">
                {{ t('platform.plugins.capGateTitle') }}
                <span class="sub-hint">{{ t('platform.plugins.capGateHint') }}</span>
              </div>
              <div class="row">
                <el-switch v-model="capGateForm.blocked" :active-text="t('platform.plugins.capSwitchBlocked')" :inactive-text="t('platform.plugins.switchPass')" />
                <span class="hint">
                  {{ t('platform.plugins.capGateOnHint') }}
                </span>
                <span class="spacer" />
                <el-button
                  size="small"
                  :disabled="!capGateDirty"
                  :loading="saving"
                  :title="t('platform.plugins.capSaveGateTip')"
                  @click="saveCapGate"
                >{{ t('common.save') }}</el-button>
                <el-button size="small" link :disabled="!capBlockedOf(capCurrent.key)" @click="resetCapGate">
                  {{ t('platform.plugins.capResetGate') }}
                </el-button>
              </div>

              <div class="sub-title">
                {{ t('platform.plugins.capTenantTitle') }}
                <span class="sub-hint">{{ t('platform.plugins.capTenantHint') }}</span>
              </div>
              <div class="hint" style="margin-bottom: 8px">
                {{ t('platform.plugins.capDraftNote') }}
              </div>
              <el-table :data="capRows" size="small" border class="tbl">
                <el-table-column :label="t('platform.plugins.colTenant')" min-width="160">
                  <template #default="{ row }">
                    <span class="t-name">{{ row.display_name }}</span>
                    <span class="mono">{{ row.schema }}</span>
                  </template>
                </el-table-column>
                <el-table-column :label="t('platform.plugins.colSource')" width="110">
                  <template #default="{ row }">
                    <el-tag v-if="row.row !== null" size="small" type="warning">{{ t('platform.plugins.tagSingleSet') }}</el-tag>
                    <el-tag v-else size="small" type="danger">{{ t('platform.plugins.tagNotOpened') }}</el-tag>
                  </template>
                </el-table-column>
                <el-table-column :label="t('platform.plugins.colEffective')" width="90">
                  <template #default="{ row }">
                    <span :class="row.effective ? 'ok' : 'muted'">
                      {{ row.effective ? t('platform.plugins.effectiveOpened') : capBlockedOf(capCurrent.key) && row.row ? t('platform.plugins.effectiveBlocked') : t('platform.plugins.tagNotOpened') }}
                    </span>
                  </template>
                </el-table-column>
                <el-table-column :label="t('platform.plugins.colOpen')" width="70">
                  <template #default="{ row }">
                    <!-- 被闸拦下时仍允许改租户行：原始配置与急停是两件事，
                         拦着不让点会让人以为要先解闸才能配置 -->
                    <el-switch v-model="row.form.allowed" size="small" />
                  </template>
                </el-table-column>
                <el-table-column :label="t('platform.plugins.colActions')" width="150">
                  <template #default="{ row }">
                    <el-button link size="small" :disabled="!capRowDirty(row)" @click="saveCapTenant(row)">{{ t('common.save') }}</el-button>
                    <el-button link size="small" :disabled="row.row === null" @click="resetCapTenant(row)">{{ t('platform.plugins.clearSettings') }}</el-button>
                  </template>
                </el-table-column>
              </el-table>
              <div v-if="!capRows.length" class="hint">{{ t('platform.plugins.noCapTenants') }}</div>
            </div>
          </div>
        </template>
      </section>
    </div>
  </div>
</template>

<script setup>
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { ElMessage, ElMessageBox } from 'element-plus'
import { api } from '../api'
import { capabilityLabelKey } from '../../locales'

// 一次取全：插件清单 + 租户列表 + 各租户原始行 + 租户启用状态。
// 矩阵要同时看「平台给没给」与「租户开没开」，逐个租户请求会让页面一闪一闪。
const { t, te } = useI18n()

const loading = ref(false)
const saving = ref(false)
const plugins = ref([])
const tenants = ref([])
const policies = ref({})
const enabled = ref({})
const selected = ref('')

// 左导航两组共用一个右侧面板，mode 决定渲染哪一份详情
const mode = ref('plugin')

const current = computed(() => (mode.value === 'plugin' ? plugins.value.find((p) => p.name === selected.value) || null : null))

// 全局停用的编辑态：闸只有两态（停用/放行），没有「未设置」——
// blocked=false 在后端等价于删行，所以两者在界面上无需区分。
const gateForm = ref({ blocked: false })
const gateBase = ref(false)

const rows = ref([])

// ===== 能力（platform.tenant_capabilities + platform.capability_gates） =====
// 键名仍是 sandbox.*，但界面口径是「这类工具能不能用」。两层：
//   租户行   允不允许（缺省拒绝：没有行 = 未开通）
//   全局闸   还开不开（缺省放行：没有行 = 不拦），压过租户的显式开启
const capabilities = ref([]) // [{key,label,blocked}]，blocked 来自全局闸
const capSelected = ref('')
const capLoaded = ref(false)
const capCells = ref({}) // {schema: {key: {row, allowed}}} —— row = 本租户原始行（null = 没设过）

const capCurrent = computed(() => (mode.value === 'cap' ? capabilities.value.find((c) => c.key === capSelected.value) || null : null))

// 全局闸编辑态：闸只有两态（拦/不拦），没有「未设置」——
// blocked=false 在后端等价于删行，所以「未设置」与「放行」在界面上无需区分。
const capGateForm = ref({ blocked: false })
const capGateBase = ref(false)
const capRows = ref([])

// 缺省开放：只有「有行且 allowed=false」才算被平台停用
// （policies 里是各租户的**原始行**，不能拿生效值算 —— 那样全局闸也会被算成「已停用」）
function revokedCount(pluginName) {
  return tenants.value.filter((t) => {
    const raw = policies.value?.[t.schema]?.[pluginName]
    return raw && !raw.allowed
  }).length
}

function rowDirty(row) {
  return !!row.form.allowed !== !!row.base.allowed
}

const gateDirty = computed(() => !!gateForm.value.blocked !== !!gateBase.value)

async function load() {
  loading.value = true
  try {
    await loadPlugins()
  } catch (err) {
    ElMessage.error(err.message || t('platform.plugins.loadPluginsFailed'))
  } finally {
    loading.value = false
  }
  await refreshCaps()
}

// 插件清单一次取全：清单（含实现）+ 租户列表 + 各租户原始行 + 租户启用状态
async function loadPlugins() {
  const data = await api.getPluginOverview()
  plugins.value = data?.plugins || []
  tenants.value = data?.tenants || []
  policies.value = data?.policies || {}
  enabled.value = data?.enabled || {}
  if (!plugins.value.some((p) => p.name === selected.value)) {
    selected.value = plugins.value[0]?.name || ''
  }
  syncForms()
}

// 选中插件或数据刷新后，重建「全局停用编辑态」与租户矩阵的编辑态。
// 矩阵基线必须是**原始行**（null = 没设过）：拿生效值回填会把「被闸停用」
// 固化成「租户关了」，解闸后能力就回不来了。
function syncForms() {
  const p = plugins.value.find((x) => x.name === selected.value) || null
  if (!p) {
    gateBase.value = false
    gateForm.value = { blocked: false }
    rows.value = []
    return
  }
  gateBase.value = !!p.blocked
  gateForm.value = { blocked: !!p.blocked }

  rows.value = tenants.value.map((t) => {
    const raw = policies.value?.[t.schema]?.[p.name] || null
    return {
      schema: t.schema,
      display_name: t.display_name || t.schema,
      status: t.status,
      enabled: !!enabled.value?.[t.schema]?.[p.name],
      row: raw,
      // 基线取原始行；没有行 = 缺省开放（不能填 false，否则一进页面就显示成「全部停用」）
      base: raw || { allowed: true },
      form: { allowed: raw ? !!raw.allowed : true }
    }
  })
}

watch(selected, syncForms)

// ===== 能力：全局硬闸 + 租户矩阵 =====
// 与插件不同：没有系统默认行，改成本租户授权（缺省拒绝）叠一层全局闸（缺省放行）。
function pickPlugin(name) {
  mode.value = 'plugin'
  if (selected.value === name) syncForms()
  else selected.value = name
}

function pickCapability(key) {
  mode.value = 'cap'
  // 选中的还是同一个键时 watch 不会触发（两个面板共用右侧），得手动重建一次
  if (capSelected.value === key) syncCapForms()
  else capSelected.value = key
}

// 能力名以键为准查语言包：后端返回的 label 是中文硬编码，直接显示会在英文界面里露中文。
// 导航里只放短的档位名，完整说明（含实现的工具名）放在右侧详情标题上。
// 未知键（平台新增能力但前端还没翻译）才回落后端 label，不至于只剩一个裸键名。
function capLabel(cap, short = false) {
  const path = capabilityLabelKey(cap?.key, short)
  return path && te(path) ? t(path) : (cap?.label || cap?.key || '')
}

function capBlockedOf(key) {
  return !!capabilities.value.find((c) => c.key === key)?.blocked
}

// 生效值直接取后端算好的 allowed（已经乘过全局闸）——
// 前端不重算一遍，否则口径分叉时会出现「界面说开通、执行被拒」。
function capAllowedOf(schema, key) {
  return !!capCells.value?.[schema]?.[key]?.allowed
}

function capGrantedCount(key) {
  return tenants.value.filter((t) => capAllowedOf(t.schema, key)).length
}

// 全局闸清单 + 每个租户的原始行。闸没有平台级总览接口（就一张表），租户行只能逐个取；
// 租户数量是个位数，并发拉比串行快，单个失败按「未取到」处理，不拖垮整页。
async function refreshCaps() {
  try {
    const gateList = await api.getCapabilityGates()
    capabilities.value = gateList?.capabilities || []
  } catch (err) {
    capabilities.value = []
    capCells.value = {}
    capLoaded.value = false
    ElMessage.error(err.message || t('platform.plugins.loadCapsFailed'))
    return
  }
  if (!capabilities.value.some((c) => c.key === capSelected.value)) {
    capSelected.value = capabilities.value[0]?.key || ''
  }
  await loadCapCells()
  syncCapForms()
}

async function loadCapCells() {
  const schemas = tenants.value.map((t) => t.schema)
  if (!capabilities.value.length || !schemas.length) {
    capCells.value = {}
    capLoaded.value = true
    return
  }
  const results = await Promise.all(schemas.map((s) => api.getTenantCapabilities(s).catch(() => null)))
  const cells = {}
  schemas.forEach((s, i) => {
    const m = {}
    for (const c of results[i]?.capabilities || []) {
      m[c.key] = { row: c.row ?? null, allowed: !!c.allowed }
    }
    cells[s] = m
  })
  capCells.value = cells
  capLoaded.value = true
}

const capGateDirty = computed(() => !!capGateForm.value.blocked !== !!capGateBase.value)

function capRowDirty(row) {
  return !!row.form.allowed !== !!row.base
}

// 选中能力或数据刷新后重建编辑态。租户行的基线必须是**原始行**（null = 没设过），
// 不能拿生效值：生效值已经乘过全局闸，拿它回填会在一次保存后把「被拦」固化成「租户关了」。
function syncCapForms() {
  const c = capCurrent.value
  const blocked = c ? capBlockedOf(c.key) : false
  capGateBase.value = blocked
  capGateForm.value = { blocked }
  if (!c) {
    capRows.value = []
    return
  }
  capRows.value = tenants.value.map((t) => {
    const cell = capCells.value?.[t.schema]?.[c.key] || null
    const row = cell ? cell.row : null
    return {
      schema: t.schema,
      display_name: t.display_name || t.schema,
      row,
      base: !!row,
      effective: cell ? !!cell.allowed : false,
      form: { allowed: !!row }
    }
  })
}

watch(capSelected, syncCapForms)

async function saveGate() {
  const p = current.value
  if (!p) return
  saving.value = true
  try {
    // 后端把 blocked=false 翻译成删行：闸表只保留「确实被拦」的记录
    await api.setPluginGate(p.name, { blocked: !!gateForm.value.blocked })
    ElMessage.success(t('platform.plugins.gateSaved'))
    await loadPlugins()
  } catch (err) {
    ElMessage.error(err.message || t('platform.plugins.saveFailed'))
  } finally {
    saving.value = false
  }
}

async function resetGate() {
  const p = current.value
  if (!p) return
  try {
    await ElMessageBox.confirm(
      t('platform.plugins.resetGateConfirm', { name: p.name }),
      t('platform.plugins.resetGateTitle'),
      { type: 'warning' }
    )
  } catch {
    return
  }
  try {
    await api.deletePluginGate(p.name)
    ElMessage.success(t('platform.plugins.resetGateDone'))
    await loadPlugins()
  } catch (err) {
    ElMessage.error(err.message || t('platform.plugins.opFailed'))
  }
}

async function saveTenant(row) {
  const p = current.value
  if (!p) return
  try {
    await api.setTenantPlugin(row.schema, p.name, row.form)
    ElMessage.success(t('platform.plugins.tenantSaved', { name: row.display_name }))
    await loadPlugins()
  } catch (err) {
    ElMessage.error(err.message || t('platform.plugins.saveFailed'))
  }
}

async function resetTenant(row) {
  const p = current.value
  if (!p) return
  try {
    await ElMessageBox.confirm(
      t('platform.plugins.resetTenantConfirm', { name: row.display_name, schema: row.schema }),
      t('platform.plugins.clearSettings'),
      { type: 'info' }
    )
  } catch {
    return
  }
  try {
    await api.deleteTenantPlugin(row.schema, p.name)
    ElMessage.success(t('platform.plugins.tenantResetDone'))
    await loadPlugins()
  } catch (err) {
    ElMessage.error(err.message || t('platform.plugins.opFailed'))
  }
}

async function saveCapGate() {
  const c = capCurrent.value
  if (!c) return
  saving.value = true
  try {
    // 后端把 blocked=false 翻译成删行：闸表只保留「确实被拦」的记录，
    // 否则排查时看到一行 false 会以为「有人动过这个闸」。
    await api.setCapabilityGate(c.key, { blocked: !!capGateForm.value.blocked })
    ElMessage.success(t('platform.plugins.capGateSaved'))
    await refreshCaps()
  } catch (err) {
    ElMessage.error(err.message || t('platform.plugins.saveFailed'))
  } finally {
    saving.value = false
  }
}

async function resetCapGate() {
  const c = capCurrent.value
  if (!c) return
  try {
    await ElMessageBox.confirm(
      t('platform.plugins.capResetGateConfirm', { key: c.key }),
      t('platform.plugins.capResetGateTitle'),
      { type: 'warning' }
    )
  } catch {
    return
  }
  try {
    await api.deleteCapabilityGate(c.key)
    ElMessage.success(t('platform.plugins.capResetDone'))
    await refreshCaps()
  } catch (err) {
    ElMessage.error(err.message || t('platform.plugins.opFailed'))
  }
}

async function saveCapTenant(row) {
  const c = capCurrent.value
  if (!c) return
  try {
    await api.setTenantCapability(row.schema, c.key, { allowed: !!row.form.allowed })
    ElMessage.success(t('platform.plugins.capTenantSaved', { name: row.display_name }))
    await refreshCaps()
  } catch (err) {
    ElMessage.error(err.message || t('platform.plugins.saveFailed'))
  }
}

async function resetCapTenant(row) {
  const c = capCurrent.value
  if (!c) return
  try {
    await ElMessageBox.confirm(
      t('platform.plugins.capResetTenantConfirm', { name: row.display_name, schema: row.schema, key: c.key }),
      t('platform.plugins.clearSettings'),
      { type: 'info' }
    )
  } catch {
    return
  }
  try {
    await api.deleteTenantCapability(row.schema, c.key)
    ElMessage.success(t('platform.plugins.capTenantResetDone'))
    await refreshCaps()
  } catch (err) {
    ElMessage.error(err.message || t('platform.plugins.opFailed'))
  }
}

onMounted(load)
</script>

<style scoped>
/* ===== 三层灰阶（与租户管理 / 平台用户同款）=====
   大背景：#f0f2f5（与平台外壳同色，整页一块浅灰）
   两张灰卡：.plug-nav / .plug-body（#fafafa）
   内容块：.blk 白底 + 边框 */
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

.split {
  flex: 1;
  min-height: 0;
  display: flex;
  gap: 6px;
  padding: 6px 0;
}

/* ===== 左：插件列表灰卡 ===== */
.plug-nav {
  width: 260px;
  flex-shrink: 0;
  padding: 8px;
  box-sizing: border-box;
  overflow: auto;
  background: #fafafa;
  border-radius: 12px;
}

.nav-caption {
  font-size: 12px;
  color: #909399;
  padding: 6px 8px;
}

/* 第二组导航（能力）与插件清单之间留一条视觉分隔 */
.nav-gap {
  margin-top: 10px;
  border-top: 1px solid #e8eaed;
  padding-top: 10px;
}

.plug-item {
  padding: 8px 10px;
  border-radius: 8px;
  cursor: pointer;
  transition: background-color 0.15s;
}

/* 灰卡上的悬停底色要压深一档，否则和卡底色 (#fafafa) 几乎看不出来 */
.plug-item:hover {
  background: #f0f2f5;
}

.plug-item.active {
  background: #e6f0f8;
}

.plug-item-name {
  font-size: 13px;
  color: #303133;
  font-weight: 500;
}

.plug-item-desc {
  font-size: 12px;
  color: #909399;
  margin-top: 2px;
  display: flex;
  gap: 4px;
  align-items: center;
}

.plug-item-stat {
  font-size: 12px;
  color: #a8abb2;
  margin-top: 2px;
}

.mono {
  font-family: Consolas, Monaco, monospace;
  font-size: 12px;
  color: #909399;
}

/* ===== 右：详情灰卡（白块浮在它上面） ===== */
.plug-body {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 10px;
  padding: 10px 12px;
  box-sizing: border-box;
  overflow: auto;
  background: #fafafa;
  border-radius: 12px;
}

.blk {
  background: #fff;
  border: 1px solid #ebeef5;
  border-radius: 12px;
  overflow: hidden;
}

.blk.placeholder {
  flex: 1;
  display: flex;
  align-items: center;
  justify-content: center;
  color: #909399;
  font-size: 13px;
}

.blk-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 10px;
  padding: 12px 16px;
  border-bottom: 1px solid #f2f3f5;
}

.hd-main {
  display: flex;
  align-items: baseline;
  gap: 8px;
}

.blk-title {
  font-size: 14px;
  font-weight: 600;
  color: #303133;
}

.ver {
  font-size: 12px;
  color: #a8abb2;
}

.blk-body {
  padding: 12px 16px 16px;
}

.desc {
  font-size: 13px;
  color: #606266;
  line-height: 1.6;
}

.impls {
  margin-top: 8px;
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}

.impl-tag {
  font-size: 12px;
  color: #409eff;
  background: #ecf5ff;
  border-radius: 4px;
  padding: 1px 8px;
}

.sub-title {
  margin: 18px 0 8px;
  font-size: 13px;
  font-weight: 600;
  color: #303133;
}

.sub-hint {
  margin-left: 8px;
  font-size: 12px;
  font-weight: 400;
  color: #909399;
}

.row {
  display: flex;
  align-items: center;
  gap: 8px;
}

.label {
  font-size: 12px;
  color: #606266;
}

.num {
  width: 110px;
}

.hint {
  font-size: 12px;
  color: #a8abb2;
}

.spacer {
  flex: 1;
}

.tbl {
  width: 100%;
}

.t-name {
  margin-right: 6px;
  color: #303133;
}

.ok {
  color: #67c23a;
}

.muted {
  color: #c0c4cc;
}

.empty {
  padding: 20px;
  text-align: center;
  font-size: 12px;
  color: #909399;
}
</style>
