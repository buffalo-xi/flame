<template>
  <div class="section">
    <!-- 整个页面就是两张卡：左 = 列表，右 = 详情（工具条收在左卡里面） -->
    <div class="split">
      <div class="list-pane">
        <!-- 计数 / 筛选 / 刷新 / 新建：列表卡内的第一行，不是独立卡头。
             控件尺寸与本地市场页保持一致（small + 固定宽度 + 搜索带图标） -->
        <div class="lp-bar">
          <span class="lp-count">{{ t('platform.myTenants.count', { n: total }) }}</span>
          <el-input
            v-model="filters.q"
            class="f-search"
            size="small"
            :placeholder="t('platform.tenants.searchPlaceholder')"
            clearable
            @keyup.enter="reload"
            @clear="reload"
          >
            <template #prefix><el-icon><Search /></el-icon></template>
          </el-input>
          <el-select v-model="filters.status" class="f-select" size="small" :placeholder="t('platform.users.statusAll')" clearable @change="reload">
            <el-option :label="t('platform.users.statusNormal')" value="active" />
            <el-option :label="t('platform.users.statusDisabled')" value="suspended" />
            <el-option :label="t('platform.users.statusDeleted')" value="deleted" />
            <el-option :label="t('platform.users.statusPending')" value="pending" />
          </el-select>
          <el-button size="small" :icon="Refresh" :loading="loading" @click="load">{{ t('common.refresh') }}</el-button>

          <div class="lp-bar-right">
            <el-button size="small" type="primary" :icon="Plus" @click="openCreate">{{ t('platform.tenants.newTenant') }}</el-button>
          </div>
        </div>

        <!-- 卡片而不是表格：左栏被右侧 560px 详情压着，六列表格每列只剩几十像素。
             卡片把「显示名 / schema / 负责人 / 三格用量」压成三行，扫得更顺 -->
        <div v-loading="loading" class="cards">
          <div
            v-for="row in items"
            :key="row.schema"
            class="tenant-card"
            :class="{ 'is-active': selectedTenant && selectedTenant.schema === row.schema }"
            @click="select(row)"
          >
            <div class="tc-top">
              <span class="tc-name" :title="row.display_name || row.schema">
                {{ row.display_name || row.schema }}
              </span>
              <el-tag size="small" :type="statusType(row.status)" effect="light">
                {{ statusText(row.status) }}
              </el-tag>
            </div>
            <div class="tc-sub">
              <span class="mono">{{ row.schema }}</span>
              <span class="tc-dot">·</span>
              <span :class="{ muted: !row.owner_user_id }">
                {{ row.owner_user_id ? ownerLabel(row.owner_user_id) : t('platform.tenants.managedByAdmin') }}
              </span>
            </div>
            <div class="tc-usage">
              <span v-for="m in QUOTA_META" :key="m.key" class="usage" :class="{ over: isOver(row, m) }">
                {{ m.label }} {{ row.usage[m.usageKey] }}/{{ limitBrief(row.quota[m.key]) }}
              </span>
            </div>
          </div>
          <el-empty v-if="!items.length && !loading" :description="t('platform.myTenants.emptyFiltered')" />
        </div>

        <div class="pager">
          <el-pagination
            v-model:current-page="page"
            v-model:page-size="pageSize"
            :total="total"
            :page-sizes="[10, 20, 50, 100]"
            layout="total, sizes, prev, pager, next"
            background
            small
            @current-change="onPageChange"
            @size-change="onSizeChange"
          />
        </div>
      </div>

      <section class="detail-pane">

        <el-empty v-if="!selectedTenant" :description="t('platform.myTenants.selectTenant')" />

        <template v-else>
          <!-- 头部：身份 + 启停（写操作，不混进「保存资料」） -->
          <div class="d-head">
            <div class="d-title">
              <span class="d-name">{{ selectedTenant.display_name || selectedTenant.schema }}</span>
              <span class="mono d-schema">{{ selectedTenant.schema }}</span>
              <el-tag size="small" :type="statusType(selectedTenant.status)">
                {{ statusText(selectedTenant.status) }}
              </el-tag>
              <span v-if="selectedTenant.plan" class="d-plan">{{ t('platform.myTenants.planTag', { plan: selectedTenant.plan }) }}</span>
            </div>
            <el-button
              size="small"
              :type="selectedTenant.status === 'active' ? 'danger' : 'success'"
              @click="toggleStatus(selectedTenant)"
            >
              {{ selectedTenant.status === 'active' ? t('platform.users.disable') : t('platform.users.enable') }}
            </el-button>
            <!-- 删除是不可逆的写操作：正常在用的租户必须先停用（停用那一步已经确认过影响）。
                 tooltip 要包一层 span —— disabled 的按钮不派发事件，裸包在 el-tooltip 里弹不出来 -->
            <el-tooltip
              :disabled="selectedTenant.status !== 'active'"
              :content="t('platform.tenants.deleteActiveTip')"
              placement="top"
            >
              <span>
                <el-button
                  size="small"
                  type="danger"
                  plain
                  :disabled="selectedTenant.status === 'active'"
                  @click="removeTenant(selectedTenant)"
                >
                  {{ t('platform.tenants.deleteTenant') }}
                </el-button>
              </span>
            </el-tooltip>
          </div>

          <div class="meta-grid">
            <div class="meta-cell">
              <div class="meta-label">{{ t('platform.tenants.labelOwner') }}</div>
              <div class="meta-value">{{ ownerLabel(selectedTenant.owner_user_id) }}</div>
            </div>
            <div class="meta-cell">
              <div class="meta-label">{{ t('platform.users.labelCreatedAt') }}</div>
              <div class="meta-value">{{ fmtTime(selectedTenant.created_at) }}</div>
            </div>
          </div>

          <!-- 可折叠卡片①：租户资料 -->
          <div class="blk">
            <button type="button" class="blk-head" @click="open.profile = !open.profile">
              <span class="blk-title">{{ t('platform.tenants.profileTitle') }}</span>
              <el-icon class="blk-arrow" :class="{ open: open.profile }"><ArrowRight /></el-icon>
            </button>
            <div v-show="open.profile" class="blk-body">
              <div class="form-grid">
                <label class="f-label">{{ t('platform.myTenants.labelDisplayName') }}</label>
                <el-input v-model="editForm.display_name" size="small" :placeholder="t('platform.tenants.phDisplayNameFallback')" />

                <label class="f-label">{{ t('platform.tenants.labelOwner') }}</label>
                <el-select
                  v-model="editForm.owner_user_id"
                  size="small"
                  :placeholder="t('platform.myTenants.noTenantsAdmin')"
                  clearable
                  style="width: 100%"
                >
                  <el-option
                    v-for="u in ownerOptions"
                    :key="u.id"
                    :label="`${u.display_name || u.name}${t('platform.tenants.ownerSuffix', { name: u.name })}`"
                    :value="u.id"
                  />
                </el-select>

                <label class="f-label">{{ t('platform.myTenants.labelRemark') }}</label>
                <el-input
                  v-model="editForm.remark"
                  size="small"
                  type="textarea"
                  :rows="2"
                  :placeholder="t('platform.tenants.phRemark')"
                />
              </div>

              <div class="blk-foot">
                <span class="tip">
                  {{ t('platform.tenants.profileTip') }}
                </span>
                <el-button size="small" :loading="savingProfile" @click="submitProfile">{{ t('platform.users.saveProfile') }}</el-button>
              </div>
            </div>
          </div>

          <!-- 可折叠卡片②：配额（三类分别调整，走 PUT /tenants/:schema/quota） -->
          <div class="blk">
            <button type="button" class="blk-head" @click="open.quota = !open.quota">
              <span class="blk-title">{{ t('platform.myTenants.quotaTitle') }}</span>
              <span class="blk-hint">{{ t('platform.tenants.byCategory') }}</span>
              <el-icon class="blk-arrow" :class="{ open: open.quota }"><ArrowRight /></el-icon>
            </button>
            <div v-show="open.quota" class="blk-body">
              <div v-loading="quotaLoading">
                <div v-for="row in quotaRows" :key="row.key" class="quota-row">
                  <span class="q-name">{{ row.label }}</span>
                  <span
                    class="q-text"
                    :class="{ over: row.over }"
                    :title="t('platform.tenants.usageTitle', { used: row.used, effective: row.effectiveText, ceiling: row.ceilingText })"
                  >
                    {{ row.used }} / {{ row.effectiveText }}
                  </span>
                  <div class="q-bar-wrap">
                    <el-progress
                      v-if="row.hasBar"
                      :percentage="row.pct"
                      :color="row.color"
                      :show-text="false"
                      :stroke-width="6"
                    />
                    <span v-else class="q-none">{{ t('platform.tenants.noLimit') }}</span>
                  </div>
                  <el-input v-model="quotaForm[row.key]" size="small" class="q-input" :placeholder="t('platform.tenants.phTenantValue')" />
                </div>
                <!-- 会话维度：没有「当前用量」可显示（要扫存储才知道），只给生效值与输入框 -->
                <div v-for="row in quotaExtraRows" :key="row.key" class="quota-row">
                  <span class="q-name">{{ row.label }}</span>
                  <span class="q-text" :title="row.title">{{ row.effectiveText }}</span>
                  <div class="q-bar-wrap"><span class="q-none">{{ row.hint }}</span></div>
                  <el-input v-model="quotaForm[row.key]" size="small" class="q-input" :placeholder="t('platform.tenants.phTenantValue')" />
                </div>
              </div>

              <div v-if="clampedRows.length" class="tip warn">
                {{ t('platform.tenants.clampedTip', {
                  labels: clampedRows
                    .map((r) => t('platform.tenants.clampedRow', { label: r.label, ceiling: r.ceilingText }))
                    .join(t('platform.tenants.listSep'))
                }) }}
              </div>

              <div class="blk-foot">
                <span class="tip">
                  {{ t('platform.tenants.quotaTip') }}
                </span>
                <el-button size="small" type="primary" :loading="savingQuota" @click="submitQuota">{{ t('platform.myTenants.saveQuota') }}</el-button>
              </div>
            </div>
          </div>

          <!-- 可折叠卡片③：插件（platform.tenant_plugins 的租户行）
               **缺省允许**：没有行 = 允许使用；只有要禁用某个租户时才写 allowed=false。
               租户侧仍要自己启用 + 配实现（{schema}.plugins），两层都过才能调用。 -->
          <div class="blk">
            <button type="button" class="blk-head" @click="open.plugins = !open.plugins">
              <span class="blk-title">{{ t('platform.tenants.pluginsTitle') }}</span>
              <span class="blk-hint">{{ t('platform.tenants.pluginsHint') }}</span>
              <el-icon class="blk-arrow" :class="{ open: open.plugins }"><ArrowRight /></el-icon>
            </button>
            <div v-show="open.plugins" class="blk-body">
              <div v-loading="pluginLoading" class="plug-list">
                <div v-if="!plugins.length" class="tip">{{ t('platform.tenants.noPlugins') }}</div>
                <div v-for="p in plugins" :key="p.name" class="plug-row">
                  <div class="plug-head">
                    <span class="plug-name">{{ p.display_name || p.name }}</span>
                    <span class="plug-key">{{ p.name }}</span>
                    <!-- 开关的语义是「停用」而不是「允许」：缺省本来就允许，
                         这里唯一的动作是把某一家关掉。文字常显，别让人猜开关方向。 -->
                    <span class="plug-switch">
                      <span :class="pluginForm[p.name].allowed ? 'muted' : 'plug-off'">
                        {{ pluginForm[p.name].allowed ? t('platform.tenants.allowed') : t('platform.users.statusDisabled') }}
                      </span>
                      <el-switch
                        :model-value="!pluginForm[p.name].allowed"
                        size="small"
                        :title="pluginForm[p.name].allowed ? t('platform.tenants.switchTitleOn') : t('platform.tenants.switchTitleOff')"
                        @change="(v) => (pluginForm[p.name].allowed = !v)"
                      />
                    </span>
                  </div>
                  <div class="plug-meta">
                    <!-- 两层分开写，别混成一句：平台这层是「允许 / 停用」，租户那层是「启没启用」。
                         全局急停压过两者，所以它单独一条。 -->
                    <span v-if="p.blocked" class="plug-off">{{ t('platform.tenants.globalKilled') }}</span>
                    <span v-else-if="!pluginForm[p.name]?.allowed" class="plug-off">{{ t('platform.tenants.platformDisabled') }}</span>
                    <span v-else-if="p.enabled">{{ p.impl ? t('platform.tenants.tenantEnabledWithImpl', { impl: p.impl }) : t('platform.tenants.tenantEnabled') }}</span>
                    <span v-else class="muted">{{ t('platform.tenants.tenantNotEnabled') }}</span>
                    <span v-if="p.row" class="plug-mark">{{ t('platform.tenants.rowSet') }}</span>
                    <span v-else class="muted">{{ t('platform.tenants.rowUnsetPlugin') }}</span>
                  </div>
                  <div class="plug-limits">
                    <el-button v-if="p.row" link size="small" @click="resetPlugin(p.name)">{{ t('platform.tenants.resetDefault') }}</el-button>
                  </div>
                </div>
              </div>

              <div class="blk-foot">
                <span class="tip">
                  {{ t('platform.tenants.pluginsTip') }}
                </span>
                <el-button size="small" type="primary" :loading="pluginSaving" @click="submitPlugins">{{ t('platform.tenants.savePlugins') }}</el-button>
              </div>
            </div>
          </div>

          <!-- 可折叠卡片④：工具能力（platform.tenant_capabilities 的租户行）
               按进程形态分四档：exec（一次性，含技能脚本）/ shell（常驻会话）/
               script（工作流代码节点与 handler，池化）/ browser（浏览器）。
               另有第五项 net.egress：**跨通道的出口策略**，管命令执行族（exec / shell /
               技能脚本）能否访问外网。它与四档并列排在最后，不是某一档的子项 ——
               依赖「exec 或 shell 任一」（两者都关掉时没有可出网的主体，后端写入时会挡）；
               浏览器自带出网、工作流脚本恒断网，都不受它控制。
               平台决定「能不能用这类形态」，租户侧只能决定「给哪个 agent 授权」。
               除 egress 外各档都能执行代码，这不是权限边界，是资源形态授权。 -->
          <div class="blk">
            <button type="button" class="blk-head" @click="open.capabilities = !open.capabilities">
              <span class="blk-title">{{ t('platform.tenants.capabilitiesTitle') }}</span>
              <span class="blk-hint">{{ t('platform.tenants.capabilitiesHint') }}</span>
              <el-icon class="blk-arrow" :class="{ open: open.capabilities }"><ArrowRight /></el-icon>
            </button>
            <div v-show="open.capabilities" class="blk-body">
              <div v-loading="capLoading" class="plug-list">
                <div v-if="!capabilities.length" class="tip">{{ t('platform.tenants.noCaps') }}</div>
                <div v-for="cap in capabilities" :key="cap.key" class="plug-row">
                  <div class="plug-head">
                    <span class="plug-name">{{ capLabel(cap) }}</span>
                    <span class="plug-key">{{ cap.key }}</span>
                    <!-- 出网是**跨通道的出口策略**（管 exec / shell / 技能脚本），不是某一档
                         的子项 —— 所以它与四档并列、排在最后。两者都关掉时它无主体可依附，
                         直接置灰；后端写入时也会挡（capabilityDeps）。 -->
                    <el-switch v-model="capForm[cap.key]" size="small"
                               :disabled="cap.key === EGRESS_KEY && !capForm[EXEC_KEY] && !capForm[SHELL_KEY]" />
                  </div>
                  <div class="plug-meta">
                    <span v-if="cap.key === EGRESS_KEY && !capForm[EXEC_KEY] && !capForm[SHELL_KEY]" class="plug-off">
                      {{ t('platform.tenants.egressDep', { exec: EXEC_KEY, shell: SHELL_KEY }) }}
                    </span>
                    <template v-else>
                      <!-- 硬闸与租户授权是两层：闸拦下时原始行仍然保留（解闸即恢复），
                           所以这里两个标签可以同时出现，别把「被拦」显示成「租户关了」。 -->
                      <span v-if="cap.blocked" class="plug-off">{{ t('platform.tenants.capBlocked') }}</span>
                      <span v-else-if="!capForm[cap.key]" class="plug-off">{{ t('platform.tenants.capNotOpened') }}</span>
                      <span v-else class="muted">{{ t('platform.tenants.capOpened') }}</span>
                      <span v-if="cap.row !== null && cap.row !== undefined" class="plug-mark">{{ t('platform.tenants.rowSet') }}</span>
                      <span v-else class="muted">{{ t('platform.tenants.capRowUnset') }}</span>
                    </template>
                    <el-button v-if="cap.row !== null && cap.row !== undefined" link size="small"
                               @click="resetCapability(cap.key)">{{ t('platform.plugins.clearSettings') }}</el-button>
                  </div>
                </div>
              </div>

              <div class="blk-foot">
                <span class="tip">
                  {{ t('platform.tenants.capTip') }}
                </span>
                <el-button size="small" type="primary" :loading="capSaving" @click="submitCapabilities">{{ t('platform.tenants.saveCaps') }}</el-button>
              </div>
            </div>
          </div>

          <!-- 可折叠卡片⑤：工作流运行参数（platform.flow_configs 的租户行，系统级只读对照） -->
          <div class="blk">
            <button type="button" class="blk-head" @click="open.flow = !open.flow">
              <span class="blk-title">{{ t('platform.myTenants.flowConfigsTitle') }}</span>
              <span class="blk-hint">{{ t('platform.tenants.flowSystemReadonly') }}</span>
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

              <div class="blk-foot">
                <span class="tip">
                  {{ t('platform.myTenants.flowTip') }}
                </span>
                <el-button size="small" type="primary" :loading="flowSaving" @click="submitFlow">{{ t('platform.myTenants.saveFlowConfigs') }}</el-button>
              </div>
            </div>
          </div>

          <!-- 可折叠卡片⑥：智能体运行参数（platform.agent_configs 的租户行，系统级只读对照）
               与工作流运行参数同一套交互，唯一差异：本表没有 -1（四项都是安全阀） -->
          <div class="blk">
            <button type="button" class="blk-head" @click="open.agent = !open.agent">
              <span class="blk-title">{{ t('platform.myTenants.agentConfigsTitle') }}</span>
              <span class="blk-hint">{{ t('platform.tenants.flowSystemReadonly') }}</span>
              <el-icon class="blk-arrow" :class="{ open: open.agent }"><ArrowRight /></el-icon>
            </button>
            <div v-show="open.agent" class="blk-body">
              <div v-loading="agentLoading" class="flow-table">
                <div class="flow-head">
                  <span>{{ t('platform.myTenants.colItem') }}</span>
                  <span>{{ t('platform.myTenants.colSystemLevel') }}</span>
                  <span>{{ t('platform.myTenants.colTenantValue') }}</span>
                </div>
                <div v-for="f in AGENT_FIELDS" :key="f.key" class="flow-row">
                  <span class="flow-name">{{ f.label }}</span>
                  <span class="flow-sys">{{ flowText(systemAgent[f.key]) }}</span>
                  <el-input v-model="tenantAgent[f.key]" size="small" :placeholder="t('platform.myTenants.phFallbackSystemLevel')" />
                </div>
              </div>

              <div class="blk-foot">
                <span class="tip">
                  {{ t('platform.myTenants.agentTip1') }}<b>{{ t('platform.tenants.agentTipBold') }}</b>{{ t('platform.myTenants.agentTip2') }}
                </span>
                <el-button size="small" type="primary" :loading="agentSaving" @click="submitAgent">
                  {{ t('platform.myTenants.saveAgentConfigs') }}
                </el-button>
              </div>
            </div>
          </div>
        </template>
      </section>
    </div>

    <!-- 开通租户：schema 由后端自动探索（不可编辑）+ 首个租户管理员（登录名固定 primary） -->
    <el-dialog v-model="createVisible" :title="t('platform.tenants.createTitle')" width="520px">
      <el-form :model="createForm" label-width="110px">
        <el-form-item label="schema">
          <el-input v-model="createForm.schema" disabled :placeholder="t('platform.tenants.phSchemaExploring')">
            <template #append>
              <el-button :icon="Refresh" :loading="schemaLoading" @click="fetchNextSchema">{{ t('platform.tenants.reExplore') }}</el-button>
            </template>
          </el-input>
          <div class="tip">
            {{ t('platform.tenants.schemaTip') }}
          </div>
        </el-form-item>
        <el-form-item :label="t('platform.myTenants.labelDisplayName')">
          <el-input v-model="createForm.display_name" :placeholder="t('platform.tenants.phDisplayNameFallback')" />
        </el-form-item>
        <el-form-item :label="t('platform.myTenants.labelPlan')">
          <el-input v-model="createForm.plan" :placeholder="t('platform.tenants.phPlan')" />
        </el-form-item>
        <el-form-item :label="t('platform.tenants.labelOwner')">
          <el-select v-model="createForm.owner_user_id" :placeholder="t('platform.myTenants.noTenantsAdmin')" clearable style="width: 100%">
            <el-option
              v-for="u in ownerOptions"
              :key="u.id"
              :label="`${u.display_name || u.name}${t('platform.tenants.ownerSuffix', { name: u.name })}`"
              :value="u.id"
            />
          </el-select>
        </el-form-item>
        <el-divider content-position="left">{{ t('platform.tenants.firstAdmin') }}</el-divider>
        <el-form-item :label="t('platform.tenants.labelLoginName')">
          <el-input v-model="createForm.admin_name" disabled />
          <div class="tip">{{ t('platform.tenants.fixedAdminTip', { name: FIXED_ADMIN_NAME }) }}</div>
        </el-form-item>
        <el-form-item :label="t('platform.users.labelInitialPassword')">
          <el-input v-model="createForm.admin_password" type="password" show-password :placeholder="t('password.newPlaceholder')" />
          <div class="tip">{{ t('platform.tenants.defaultAdminPwdTip', { password: DEFAULT_ADMIN_PASSWORD }) }}</div>
        </el-form-item>
      </el-form>
      <div class="tip">
        {{ t('platform.tenants.createTip') }}<br />
        {{ t('platform.tenants.verifyPrefix') }}<b>{{ t('platform.tenants.verifyLoginWord') }}</b>{{ t('platform.tenants.verifyMid') }}<b>{{ FIXED_ADMIN_NAME }}</b>{{ t('platform.tenants.verifyTail') }}
      </div>
      <template #footer>
        <el-button @click="createVisible = false">{{ t('common.cancel') }}</el-button>
        <el-button
          type="primary"
          :loading="saving"
          :disabled="schemaLoading || !createForm.schema"
          @click="submitCreate"
        >{{ t('platform.tenants.submitOpen') }}</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup>
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { ElMessage, ElMessageBox } from 'element-plus'
import { ArrowRight, Plus, Refresh, Search } from '@element-plus/icons-vue'
import { api } from '../api'
import { capabilityLabelKey } from '../../locales'

const emit = defineEmits(['count', 'session-expired'])

const { t, te } = useI18n()

const items = ref([])
const users = ref([])
const loading = ref(false)
const saving = ref(false)
const savingProfile = ref(false)
const savingQuota = ref(false)
const filters = reactive({ q: '', status: '' })

// 后端真分页：page 从 1 起，page_size 由分页器给，total 用接口回的总数（不是 items.length）
const page = ref(1)
const pageSize = ref(20)
const total = ref(0)

// 选中项存整行快照：翻页后它可能不在当前页，不能再从 items 里 find
const selectedTenant = ref(null)

const createVisible = ref(false)
// 首个租户管理员登录名固定为 primary（与后端 provision.DefaultAdminName 一致）
const FIXED_ADMIN_NAME = 'primary'
// 初始密码缺省值：仅前端预填，操作者仍可改；租户首次登录后建议立即改密
const DEFAULT_ADMIN_PASSWORD = '12345678'
// schema 自动探索中：探索完成前「开通」按钮禁用
const schemaLoading = ref(false)
// 详情卡里五张折叠卡片的展开状态：只收起「租户资料」（只读信息，不常改），
// 其余（配额 / 插件 / 能力授权 / 工作流运行参数）默认展开 —— 进来就能看到全貌，
// 不用逐个点开才知道这个租户被限成什么样。切换租户时也保持这套状态。
const open = reactive({ profile: false, quota: true, plugins: true, capabilities: true, flow: true, agent: true })
const editForm = reactive({ display_name: '', remark: '', owner_user_id: '' })
const quotaForm = reactive({
  max_agents: '',
  max_skills: '',
  max_flows: '',
  // 空间在界面上以 MiB 输入，提交时换算成字节（后端存 bigint）
  max_session_storage_bytes: '',
  max_user_sessions: ''
})
const createForm = reactive({
  schema: '',
  display_name: '',
  plan: '',
  owner_user_id: '',
  admin_name: '',
  admin_password: ''
})

// 负责人只能是「用户」身份且未停用的平台账号（后端同样校验）
const ownerOptions = computed(() => users.value.filter((u) => u.role === 'user' && u.status === 'active'))

const statusText = (s) =>
  ({
    active: t('platform.users.statusNormal'),
    suspended: t('platform.users.statusDisabled'),
    deleted: t('platform.users.statusDeleted'),
    pending: t('platform.users.statusPending')
  })[s] || s
const statusType = (s) => ({ active: 'success', suspended: 'warning', deleted: 'info', pending: 'info' }[s] || 'info')

// 三类配额：列表里是三列「用量 / 上限」，卡片里每类一行
const QUOTA_META = computed(() => [
  { key: 'max_agents', usageKey: 'agents', label: t('platform.tenants.metricAgents') },
  { key: 'max_skills', usageKey: 'skills', label: t('platform.tenants.metricSkills') },
  { key: 'max_flows', usageKey: 'flows', label: t('platform.tenants.metricFlows') }
])

const limitBrief = (v) => (v === -1 ? '∞' : String(v ?? '-'))
const limitText = (v) => (v === -1 ? t('platform.myTenants.unlimited') : String(v ?? t('platform.myTenants.unset')))
const isOver = (row, m) => row.quota[m.key] >= 0 && row.usage[m.usageKey] > row.quota[m.key]



// 负责人显示：显示名（账号名）；两者相同时不重复
const ownerLabel = (id) => {
  if (!id) return t('platform.myTenants.noTenantsAdmin')
  const u = users.value.find((x) => x.id === id)
  if (!u) return id.slice(0, 8)
  return u.display_name && u.display_name !== u.name
    ? `${u.display_name}${t('platform.tenants.ownerSuffix', { name: u.name })}`
    : u.name
}

const fmtTime = (s) => {
  if (!s) return '—'
  const d = new Date(s)
  if (Number.isNaN(d.getTime())) return String(s).replace('T', ' ').slice(0, 16)
  const p = (n) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`
}

// ===== 配额：原始行（编辑框绑它）+ 系统顶 + 兜底 + 生效值 =====
// 生效值由后端 quota.Resolve 算好回带，前端**不重算**截断规则，避免两套实现漂移。
const quotaLoading = ref(false)
const quotaCfg = ref({ quota: {}, system: {}, fallback: {}, effective: {} })

// 卡片里的三行配额：用量 / 生效值 + 进度（不限时不画进度条）。
// 生效值用后端算好的 effective；原始值高于系统顶时提示"已按顶截断"（原始值照存，只是不生效）。
const quotaRows = computed(() => {
  const tn = selectedTenant.value
  if (!tn) return []
  const cfg = quotaCfg.value
  return QUOTA_META.value.map((m) => {
    const used = tn.usage?.[m.usageKey] ?? 0
    const effective = cfg.effective?.[m.key]
    const hasBar = typeof effective === 'number' && effective >= 0
    const pct = hasBar ? Math.min(100, effective === 0 ? (used > 0 ? 100 : 0) : Math.round((used / effective) * 100)) : 0
    const raw = cfg.quota?.[m.key]
    const sys = cfg.system?.[m.key]
    const ceiling = sys === null || sys === undefined ? cfg.fallback?.[m.key] : sys
    return {
      key: m.key,
      label: m.label,
      used,
      effectiveText: limitText(effective),
      ceilingText:
        sys === null || sys === undefined
          ? t('platform.tenants.fallbackPrefix', { text: limitText(ceiling) })
          : limitText(sys),
      hasBar,
      pct,
      over: hasBar && used > effective,
      clamped:
        typeof raw === 'number' &&
        raw !== -1 &&
        typeof ceiling === 'number' &&
        ceiling !== -1 &&
        raw > ceiling,
      color: pct >= 100 ? '#f56c6c' : pct >= 80 ? '#e6a23c' : '#1890ff'
    }
  })
})

// 原始值被系统顶截断的那几项（卡片底部提示用）
const clampedRows = computed(() => quotaRows.value.filter((r) => r.clamped))

// ===== 会话维度配额 =====
// 空间在界面按 MiB 展示/输入（后端存字节），会话数按个。
// 这两项没有「当前用量」可显示 —— 用量要扫存储才知道 —— 所以只给生效值与输入框。
const MIB = 1024 * 1024
const QUOTA_EXTRA = computed(() => [
  { key: 'max_session_storage_bytes', label: t('platform.tenants.quotaExtraStorage'), unit: 'mib' },
  { key: 'max_user_sessions', label: t('platform.tenants.quotaExtraSessions'), unit: 'int' }
])

// 字节 → 展示文本：-1 = 不限，null = 未设置，其余按整 MiB（不做小数）
const mibText = (v) => {
  if (v === null || v === undefined) return t('platform.myTenants.unset')
  if (v === -1) return t('platform.myTenants.unlimited')
  return `${Math.round(v / MIB)} MiB`
}
// 字节 → 输入框：'' = 未设置，'-1' = 不限
const mibToInput = (v) => {
  if (v === null || v === undefined) return ''
  if (v === -1) return '-1'
  return String(Math.round(v / MIB))
}

const quotaExtraRows = computed(() => {
  const cfg = quotaCfg.value
  return QUOTA_EXTRA.value.map((m) => {
    const eff = cfg.effective?.[m.key]
    const sys = cfg.system?.[m.key]
    const ceiling = sys === null || sys === undefined ? cfg.fallback?.[m.key] : sys
    const text = m.unit === 'mib' ? mibText(eff) : limitText(eff)
    const ceilText = m.unit === 'mib' ? mibText(ceiling) : limitText(ceiling)
    return {
      key: m.key,
      label: m.label,
      effectiveText: text,
      hint:
        sys === null || sys === undefined
          ? t('platform.tenants.fallbackPrefix', { text: ceilText })
          : t('platform.tenants.systemCeiling', { text: ceilText }),
      title: t('platform.tenants.effectiveTitle', { text, ceiling: ceilText })
    }
  })
})

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

// 选中租户一变就重新拉它的配额原始行、插件策略、工作流运行参数与智能体运行参数
// （select / load / refreshSelected 都会经过这里）
watch(
  () => selectedTenant.value?.schema,
  (schema) => {
    if (!schema) return
    loadFlowConfigs(schema)
    loadAgentConfigs(schema)
    loadQuotaConfig(schema)
    loadPlugins(schema)
    loadCapabilities(schema)
  }
)

// ===== 智能体运行参数（platform.agent_configs 的租户行；系统级只读对照） =====
// 与工作流运行参数同一套交互；唯一差异是本表不接受 -1（四项都是安全阀，只接受 >= 1）。
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

// 拉某租户的运行参数：system 一并回带（只作对照），tenant 值统一转成字符串给输入框
async function loadAgentConfigs(schema) {
  agentLoading.value = true
  try {
    const data = await api.getTenantAgentConfigs(schema)
    systemAgent.value = data?.system || {}
    const row = data?.tenant || {}
    const obj = {}
    for (const f of AGENT_FIELDS.value) {
      const v = row[f.key]
      obj[f.key] = v === null || v === undefined ? '' : String(v)
    }
    tenantAgent.value = obj
  } catch (err) {
    handleError(err)
  } finally {
    agentLoading.value = false
  }
}

// 保存租户行：空串 → null（未设置）；前端先按「>= 1」和「不超过系统级」各拦一道
async function submitAgent() {
  const tn = selectedTenant.value
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
    await api.setTenantAgentConfigs(tn.schema, body)
    ElMessage.success(t('platform.myTenants.agentSaved'))
    await loadAgentConfigs(tn.schema)
  } catch (err) {
    handleError(err)
  } finally {
    agentSaving.value = false
  }
}

// ===== 插件（platform.tenant_plugins 的租户行） =====
// 编辑态与「原始行」一一对应：row 为 null 表示没设过（= 缺省开放），
// 保存时只提交真正改过的行（没动过的行不会因为一次保存被物化成租户行）。
const pluginLoading = ref(false)
const pluginSaving = ref(false)
const plugins = ref([])
const pluginForm = ref({})

// 基线是**原始行**；没有行 = 缺省允许（不能填 false，否则一进页面开关就显示成「已停用」）
function pluginBaseOf(p) {
  return p.row || { allowed: true }
}

function pluginDirty(p) {
  const f = pluginForm.value[p.name]
  if (!f) return false
  return !!f.allowed !== !!pluginBaseOf(p).allowed
}

async function loadPlugins(schema) {
  pluginLoading.value = true
  try {
    const data = await api.getTenantPlugins(schema)
    const list = data?.plugins || []
    // 先建表单再挂列表：模板里 v-model 直接绑 pluginForm[name].xxx，不能有一帧是 undefined
    const form = {}
    for (const p of list) {
      // 用原始行回填：allowed 已经乘过全局闸，闸停着时会把「租户开着」显示成「租户关了」。
      // 没有行 → 缺省允许（true），否则开关会以「已停用」的样子出现，与实际策略相反。
      form[p.name] = { allowed: p.row ? !!p.row.allowed : true }
    }
    pluginForm.value = form
    plugins.value = list
  } catch (err) {
    handleError(err)
  } finally {
    pluginLoading.value = false
  }
}

async function submitPlugins() {
  const tn = selectedTenant.value
  if (!tn) return
  const changed = plugins.value.filter(pluginDirty)
  if (!changed.length) {
    ElMessage.info(t('platform.tenants.msgNoPluginChange'))
    return
  }
  pluginSaving.value = true
  try {
    for (const p of changed) {
      await api.setTenantPlugin(tn.schema, p.name, pluginForm.value[p.name])
    }
    ElMessage.success(t('platform.tenants.msgPluginsSaved'))
    await loadPlugins(tn.schema)
  } catch (err) {
    handleError(err)
  } finally {
    pluginSaving.value = false
  }
}

// 删掉本租户行 = 回到缺省（开放）。系统默认行已移除，所以「清除」就是彻底放开这一家
async function resetPlugin(name) {
  const tn = selectedTenant.value
  if (!tn) return
  try {
    await ElMessageBox.confirm(t('platform.tenants.resetPluginConfirm', { name }), t('platform.tenants.resetPluginTitle'), {
      type: 'info'
    })
  } catch {
    return
  }
  try {
    await api.deleteTenantPlugin(tn.schema, name)
    ElMessage.success(t('platform.tenants.msgPluginReset'))
    await loadPlugins(tn.schema)
  } catch (err) {
    handleError(err)
  }
}

// ===== 工具能力（platform.tenant_capabilities 的租户行）=====
// 只有一行原始值：row === null 表示没设过（= 未开通）。保存只提交真正改过的行，
// 没动过的行不会因为一次保存被物化成租户行。
// 注意不能用 allowed（生效值）回填：它已经乘过全局闸，闸拦着时会把「租户开着」
// 显示成「租户关着」，一次保存就把原始配置改掉了。
const capLoading = ref(false)
const capSaving = ref(false)
const capabilities = ref([])
const capForm = ref({})

// 能力键：出网是跨通道的出口策略，依赖「exec 或 shell 任一」，模板里按这三个常量判。
const EXEC_KEY = 'sandbox.exec'
const SHELL_KEY = 'sandbox.shell'
const EGRESS_KEY = 'net.egress'

// 能力名以键为准查语言包：后端返回的 label 是中文硬编码，直接显示会在英文界面里露中文。
// 未知键（平台新增能力但前端还没翻译）才回落后端 label，不至于只剩一个裸键名。
function capLabel(cap) {
  const path = capabilityLabelKey(cap?.key)
  return path && te(path) ? t(path) : (cap?.label || cap?.key || '')
}

// 依赖断开时立刻把出网回落到关闭。否则开关只是变灰、value 仍是 true：
// 那一行还写着「已开放」，保存时又被静默改成 false —— 界面和实际提交的结果对不上。
watch(
  [() => capForm.value[EXEC_KEY], () => capForm.value[SHELL_KEY]],
  ([exec, shell]) => {
    if (!exec && !shell && capForm.value[EGRESS_KEY]) capForm.value[EGRESS_KEY] = false
  },
  { immediate: true }
)

// 改动基线：原始行；没有行就是 false（缺省未开通）。
function capDirty(cap) {
  const f = capForm.value[cap.key]
  if (f === undefined) return false
  const base = cap.row !== null && cap.row !== undefined ? !!cap.row : false
  return f !== base
}

async function loadCapabilities(schema) {
  capLoading.value = true
  try {
    const data = await api.getTenantCapabilities(schema)
    const list = data?.capabilities || []
    const form = {}
    for (const cap of list) form[cap.key] = cap.row !== null && cap.row !== undefined ? !!cap.row : false
    capForm.value = form
    capabilities.value = list
  } catch (err) {
    handleError(err)
  } finally {
    capLoading.value = false
  }
}

async function submitCapabilities() {
  const tn = selectedTenant.value
  if (!tn) return
  // 出网依赖「exec 或 shell 任一」，后端会拒（capabilityDeps）。
  // 但这里不再二次改写：上面的 watcher 已经保证依赖断开时 egress 被一起关掉，
  // 而原先在这里静默置 false 时，界面上那一行还写着「已开放」。
  const changed = capabilities.value.filter(capDirty)
  if (!changed.length) {
    ElMessage.info(t('platform.tenants.msgNoCapChange'))
    return
  }
  capSaving.value = true
  try {
    for (const cap of changed) {
      await api.setTenantCapability(tn.schema, cap.key, { allowed: !!capForm.value[cap.key] })
    }
    ElMessage.success(t('platform.tenants.msgCapsSaved'))
    await loadCapabilities(tn.schema)
  } catch (err) {
    handleError(err)
  } finally {
    capSaving.value = false
  }
}

// 删掉本租户行 = 回到缺省（未开通）。系统默认行已移除，所以「清除」就是彻底关掉
async function resetCapability(key) {
  const tn = selectedTenant.value
  if (!tn) return
  try {
    await ElMessageBox.confirm(t('platform.tenants.resetCapConfirm', { key }), t('platform.plugins.clearSettings'), {
      type: 'info'
    })
  } catch {
    return
  }
  try {
    await api.deleteTenantCapability(tn.schema, key)
    ElMessage.success(t('platform.tenants.msgCapReset'))
    await loadCapabilities(tn.schema)
  } catch (err) {
    handleError(err)
  }
}

function handleError(err) {
  if (err?.status === 401) {
    emit('session-expired')
    return
  }
  ElMessage.error(err?.message || t('platform.shared.requestFailed'))
}

// 把资料字段灌进卡片编辑态。
// 配额不在这里：items 里的 quota 是 Resolve 之后的**生效值**，拿它当编辑框会把原始行静默压平，
// 所以配额走 loadQuotaConfig 读原始行。
function syncForm(t) {
  if (!t) return
  editForm.display_name = t.display_name || ''
  editForm.remark = t.remark || ''
  editForm.owner_user_id = t.owner_user_id || ''
}

// 点卡片：选中（存快照，翻页后卡片不会变空）+ 填充编辑态
function select(row) {
  if (!row || row.schema === selectedTenant.value?.schema) return
  selectedTenant.value = row
  syncForm(row)
}

// 筛选条件变了要回到第 1 页
function reload() {
  page.value = 1
  load()
}

// 翻页 / 改每页条数：都回到第 1 页再拉（显式赋值，不依赖 v-model 与事件的先后顺序）
function onPageChange(p) {
  page.value = p
  load()
}

function onSizeChange() {
  page.value = 1
  load()
}

// 只回读当前选中的那一个租户：编辑/改配额后让卡片与表格都拿到新值
async function refreshSelected() {
  const schema = selectedTenant.value?.schema
  if (!schema) return
  try {
    const fresh = await api.getTenant(schema)
    if (fresh) {
      selectedTenant.value = fresh
      syncForm(fresh)
    }
  } catch (err) {
    handleError(err)
  }
}

async function load() {
  loading.value = true
  try {
    const [tenantData, userData] = await Promise.all([
      api.listTenants({ q: filters.q, status: filters.status, page: page.value, page_size: pageSize.value }),
      api.listUsers({})
    ])
    items.value = tenantData?.items || []
    total.value = tenantData?.total ?? items.value.length
    users.value = userData?.items || []

    // 选中项还在本页就同步成新数据；本页没有它则保留快照（卡片不闪空）
    const hit = items.value.find((t) => t.schema === selectedTenant.value?.schema)
    if (hit) {
      selectedTenant.value = hit
      syncForm(hit)
    } else if (!selectedTenant.value) {
      select(items.value[0])
    }

    // 顶栏徽标用总数，不是本页条数
    emit('count', total.value)
  } catch (err) {
    handleError(err)
  } finally {
    loading.value = false
  }
}

// 空串 → null（未设置），其它 → 数字；非法输入直接拦下
function toQuotaValue(raw) {
  const s = String(raw ?? '').trim()
  if (s === '') return null
  if (!/^-?\d+$/.test(s)) return NaN
  return Number(s)
}

async function submitProfile() {
  const tn = selectedTenant.value
  if (!tn) return
  // 负责人 = 归属：它一变，等于把该租户转给另一个平台账号。后端不会拒绝也不会回滚，
  // 所以确认只能放在这里，并且要说清「原负责人失去入口」这件事。
  const prevOwner = tn.owner_user_id || ''
  const nextOwner = editForm.owner_user_id || ''
  if (prevOwner !== nextOwner) {
    try {
      await ElMessageBox.confirm(
        t('platform.tenants.ownerChangeConfirm', { from: ownerLabel(prevOwner), to: ownerLabel(nextOwner) }),
        t('platform.tenants.ownerChangeTitle'),
        { type: 'warning', confirmButtonText: t('platform.tenants.ownerChangeBtn'), cancelButtonText: t('common.cancel') }
      )
    } catch {
      return // 取消：留在原地，让操作者自己把负责人改回去
    }
  }
  savingProfile.value = true
  try {
    await api.updateTenant(tn.schema, {
      display_name: editForm.display_name,
      remark: editForm.remark,
      owner_user_id: editForm.owner_user_id
    })
    ElMessage.success(t('platform.users.profileSaved'))
    await load()
    await refreshSelected()
  } catch (err) {
    handleError(err)
  } finally {
    savingProfile.value = false
  }
}

async function submitQuota() {
  const tn = selectedTenant.value
  if (!tn) return
  const maxAgents = toQuotaValue(quotaForm.max_agents)
  const maxSkills = toQuotaValue(quotaForm.max_skills)
  const maxFlows = toQuotaValue(quotaForm.max_flows)
  const sessMiB = toQuotaValue(quotaForm.max_session_storage_bytes)
  const maxUserSessions = toQuotaValue(quotaForm.max_user_sessions)
  if ([maxAgents, maxSkills, maxFlows, sessMiB, maxUserSessions].some((v) => Number.isNaN(v))) {
    ElMessage.error(t('platform.tenants.quotaIntegerAdmin'))
    return
  }
  // 界面按 MiB 输入，后端存字节；-1（不限）原样传，不参与换算
  const sessBytes = sessMiB === null ? null : sessMiB === -1 ? -1 : sessMiB * MIB
  savingQuota.value = true
  try {
    await api.setTenantQuota(tn.schema, {
      max_agents: maxAgents,
      max_skills: maxSkills,
      max_flows: maxFlows,
      max_session_storage_bytes: sessBytes,
      max_user_sessions: maxUserSessions
    })
    ElMessage.success(t('platform.tenants.msgQuotaUpdated'))
    await load()
    await refreshSelected()
    await loadQuotaConfig(tn.schema)
  } catch (err) {
    handleError(err)
  } finally {
    savingQuota.value = false
  }
}

// 拉某租户的工作流运行参数：system 一并回带（只作对照），tenant 值统一转成字符串给输入框
async function loadFlowConfigs(schema) {
  flowLoading.value = true
  try {
    const data = await api.getTenantFlowConfigs(schema)
    systemFlow.value = data?.system || {}
    const row = data?.tenant || {}
    const obj = {}
    for (const f of FLOW_FIELDS.value) {
      const v = row[f.key]
      obj[f.key] = v === null || v === undefined ? '' : String(v)
    }
    tenantFlow.value = obj
  } catch (err) {
    handleError(err)
  } finally {
    flowLoading.value = false
  }
}

// 拉某租户的配额：原始行（灌进编辑框）+ 系统顶 + 兜底 + 生效值（展示用）
async function loadQuotaConfig(schema) {
  quotaLoading.value = true
  try {
    const data = await api.getTenantQuota(schema)
    quotaCfg.value = {
      quota: data?.quota || {},
      system: data?.system || {},
      fallback: data?.fallback || {},
      effective: data?.effective || {}
    }
    for (const m of QUOTA_META.value) {
      const v = data?.quota?.[m.key]
      quotaForm[m.key] = v === null || v === undefined ? '' : String(v)
    }
    for (const m of QUOTA_EXTRA.value) {
      const v = data?.quota?.[m.key]
      quotaForm[m.key] = m.unit === 'mib' ? mibToInput(v) : v === null || v === undefined ? '' : String(v)
    }
  } catch (err) {
    handleError(err)
  } finally {
    quotaLoading.value = false
  }
}

// 保存租户行：空串 → null（未设置）；前端先按"不超过系统级"拦一道（文案与后端同口径）
async function submitFlow() {
  const tn = selectedTenant.value
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
    await api.setTenantFlowConfigs(tn.schema, body)
    ElMessage.success(t('platform.myTenants.flowSaved'))
    await loadFlowConfigs(tn.schema)
  } catch (err) {
    handleError(err)
  } finally {
    flowSaving.value = false
  }
}

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
    await api.setTenantStatus(tn.schema, next)
    ElMessage.success(next === 'suspended' ? t('platform.users.disabledDone') : t('platform.users.enabledDone'))
    await load()
    await refreshSelected()
  } catch (err) {
    handleError(err)
  }
}

// 删除租户：不可逆，但不再要求打名字 —— 前置条件是「先停用」，
// 停用那一步已经让操作者确认过「会影响调用方」，这里只确认一次不可恢复。
// 删完清空选中：列表里已经没这一行了，留着卡片只会让人以为还在。
async function removeTenant(tn) {
  if (!tn) return
  if (tn.status === 'active') {
    ElMessage.warning(t('platform.tenants.msgDeactivateFirst'))
    return
  }
  const label = tn.display_name || tn.schema
  try {
    await ElMessageBox.confirm(
      t('platform.tenants.deleteConfirm', { label, schema: tn.schema }),
      t('platform.tenants.deleteTitle'),
      { type: 'error', confirmButtonText: t('platform.tenants.deleteBtn'), cancelButtonText: t('common.cancel') }
    )
  } catch {
    return
  }
  try {
    await api.deleteTenant(tn.schema)
    ElMessage.success(t('platform.tenants.msgDeleted', { schema: tn.schema }))
    selectedTenant.value = null
    await load()
  } catch (err) {
    handleError(err)
  }
}

onMounted(load)

function openCreate() {
  createForm.schema = ''
  createForm.display_name = ''
  createForm.plan = ''
  createForm.owner_user_id = ''
  createForm.admin_name = FIXED_ADMIN_NAME
  createForm.admin_password = DEFAULT_ADMIN_PASSWORD
  createVisible.value = true
  fetchNextSchema()
}

// 探索一个可用 schema（后端从 t0 起逐个 +1）。开通时若被占用，会再调一次拿新值。
async function fetchNextSchema() {
  schemaLoading.value = true
  try {
    const data = await api.nextTenantSchema()
    createForm.schema = data?.schema || ''
  } catch (err) {
    createForm.schema = ''
    handleError(err)
  } finally {
    schemaLoading.value = false
  }
}

async function submitCreate() {
  if (!createForm.schema) {
    ElMessage.error(t('platform.tenants.msgSchemaNotReady'))
    return
  }
  if (!createForm.admin_password) {
    ElMessage.error(t('platform.tenants.msgPwdRequired'))
    return
  }
  saving.value = true
  try {
    const data = await api.createTenant({
      schema: createForm.schema, // 不可手填：值是后端探索出来的
      display_name: createForm.display_name,
      plan: createForm.plan,
      owner_user_id: createForm.owner_user_id,
      admin_name: FIXED_ADMIN_NAME,
      admin_password: createForm.admin_password
    })
    ElMessage({
      message: t('platform.tenants.msgCreated', {
        schema: data?.schema || createForm.schema,
        admin: data?.created_admin || FIXED_ADMIN_NAME
      }),
      type: 'success',
      duration: 1000
    })
    createVisible.value = false
    await load()
  } catch (err) {
    // 409 = 探索到开通之间被抢占：提示 + 重新探索，弹窗不关，等操作者确认后再点一次「开通」
    if (err?.status === 409) {
      ElMessage.warning(err.message || t('platform.tenants.msgSchemaTaken', { schema: createForm.schema }))
      await fetchNextSchema()
      return
    }
    handleError(err)
  } finally {
    saving.value = false
  }
}
</script>

<style scoped>
/* ===== 三层灰阶（整块浅灰 → 两张灰卡 → 白色项目卡）=====
   大背景：#f0f2f5（与平台外壳同色，整页一块浅灰，不留白卡边框感）
   两张灰卡：.list-pane / .detail-pane（#fafafa）
   项目卡：.tenant-card / .blk 白底 + 边框 + 投影，浮在灰卡上
   外壳（PlatformApp 的 .content-panel）已经给 10px 内缩。 */
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

/* 列表卡内的控制行：计数 / 筛选 / 刷新 / 新建（不是独立卡头，跟表格同处一张卡） */
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

/* 筛选控件宽固定：选项文案长短不会把这行挤变形（与本地市场页同款） */
.f-select {
  width: 128px;
  flex-shrink: 0;
}

.f-search {
  width: 200px;
  flex-shrink: 0;
}

.lp-bar-right {
  margin-left: auto;
  display: flex;
  align-items: center;
  gap: 10px;
}

/* ===== 主从布局：左右各一张灰卡 =====
   只留上下 6px，左右贴边：外壳的 margin 已经收掉，再给左右内缩两张大卡就显空。
   卡与卡之间留 6px：两卡同色，这条缝是它们唯一的分界。 */
.split {
  flex: 1;
  min-height: 0;
  display: flex;
  gap: 6px;
  padding: 6px 0;
}

/* 左：列表灰卡（自由宽，含分页器）。比大背景提亮一档，白卡再浮在它上面。
   内边距收到 8px：灰卡本身已经是一层留白，再给 12px 白卡四周就空得明显了 */
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

/* 卡片队列：撑满剩余高度自己滚；间距对齐智能体卡片列表（14px）。
   底色交给灰卡，滚动条位置常驻、拇指透明 → 悬浮才上色 */
.cards {
  flex: 1;
  min-height: 0;
  overflow: auto;
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(200px, 1fr));
  align-content: start;
  gap: 14px;
  padding: 0;
  scrollbar-gutter: stable;
  scrollbar-width: thin;
  scrollbar-color: transparent transparent;
}

.cards::-webkit-scrollbar {
  width: 6px;
  height: 6px;
}

.cards::-webkit-scrollbar-track {
  background: transparent;
}

.cards::-webkit-scrollbar-thumb {
  background: transparent;
  border-radius: 3px;
}

.cards:hover {
  scrollbar-color: #c1c1c1 transparent;
}

.cards:hover::-webkit-scrollbar-thumb {
  background: #c1c1c1;
}

.cards :deep(.el-empty) {
  grid-column: 1 / -1;
}

/* 一张卡三行：显示名 + 状态 / schema + 负责人 / 三格用量。
   盒模型对齐智能体卡片（AgentsList.vue 的 .agent-card）：白底、灰边、12px 圆角、常驻投影 */
.tenant-card {
  padding: 16px;
  background: #fff;
  border: 1px solid #e8e8e8;
  border-radius: 12px;
  box-shadow: 0 2px 8px rgba(0, 0, 0, 0.1);
  cursor: pointer;
  width: 100%;
  min-width: 200px;
  max-width: 250px;
  transition: all 0.3s ease;
}

.tenant-card:hover {
  box-shadow: 0 4px 16px rgba(0, 0, 0, 0.15);
}

/* 智能体卡片没有选中态；这里保留蓝边 + 浅蓝底，否则在灰底上看不出选的是哪张 */
.tenant-card.is-active {
  border-color: #409eff;
  background: #ecf5ff;
  box-shadow: 0 2px 8px rgba(64, 158, 255, 0.18);
}

.tc-top {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}

.tc-name {
  flex: 1;
  min-width: 0;
  font-size: 13px;
  font-weight: 600;
  color: #303133;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.tc-sub {
  display: flex;
  align-items: center;
  gap: 4px;
  margin-top: 4px;
  font-size: 12px;
  color: #909399;
  min-width: 0;
}

.tc-sub > span:not(.tc-dot) {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.tc-dot {
  flex-shrink: 0;
  color: #dcdfe6;
}

.tc-usage {
  display: flex;
  flex-wrap: wrap;
  gap: 2px 10px;
  margin-top: 6px;
}

.pager {
  flex-shrink: 0;
  display: flex;
  justify-content: flex-end;
  padding-top: 10px;
}

/* 右：详情灰卡（固定宽）。灰卡上的 .blk 是白块，层次与租户平台的设置页一致。
   680px：卡片里有配额表（四列：名称/用量/系统顶/输入框）、插件矩阵、能力开关与工具概览，
   560 时代码类输入框和配额输入框会被挤得只剩几个字符。左侧列表仍是 flex:1，
   只要窗口 ≥1280 就还能放下两列 200px 的租户卡；再窄由列表自己滚动。 */
.detail-pane {
  width: 680px;
  flex-shrink: 0;
  overflow: auto;
  padding: 10px 12px;
  box-sizing: border-box;
  background: #fafafa;
  border-radius: 12px;
}

/* 卡片滚动条：位置常驻（一直是 6px），只是拇指透明 → 悬浮才上色。
   这样悬浮时不会因为"条突然出现"而让内容横向跳动。 */
.detail-pane {
  scrollbar-width: thin; /* 常驻细条：占位固定 */
  scrollbar-color: transparent transparent; /* 不悬浮：看不见，但占位 */
}

.detail-pane::-webkit-scrollbar {
  width: 6px;
  height: 6px;
}

.detail-pane::-webkit-scrollbar-track {
  background: transparent;
}

.detail-pane::-webkit-scrollbar-thumb {
  background: transparent; /* 不悬浮：看不见，但占位 */
  border-radius: 3px;
}

.detail-pane:hover {
  scrollbar-color: rgba(144, 147, 153, 0.4) transparent;
}

.detail-pane:hover::-webkit-scrollbar-thumb {
  background: rgba(144, 147, 153, 0.4);
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
}

.d-name {
  font-size: 16px;
  font-weight: 600;
  color: #303133;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.d-schema,
.d-plan {
  font-size: 12px;
  color: #909399;
}

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

/* 卡片底部：tip 靠左、按钮靠右（只有一个按钮时也靠右） */
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

/* 资料：左标签 + 右控件 */
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

/* 配额：两类各一行 —— 名称 / 用量 / 进度 / 输入框
   名称列 132px：会话那两行的标签是「单会话空间（MiB）」「单用户会话数（个）」，
   56px 会折成两行；前三行的短名称留白多一点也无妨。 */
.quota-row {
  display: grid;
  grid-template-columns: 132px 84px 1fr 96px;
  align-items: center;
  gap: 10px;
  padding: 6px 0;
}

.q-name {
  font-size: 13px;
  color: #606266;
  white-space: nowrap;
}

.q-text {
  font-size: 13px;
  color: #303133;
}

.q-text.over {
  color: #f56c6c;
}

.q-bar-wrap {
  min-width: 60px;
}

.q-none {
  font-size: 12px;
  color: #a8abb2;
}

.q-input {
  width: 100%;
}

/* 工作流运行参数：配置项 / 系统级 / 租户配置 三列 */
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
}

.flow-sys {
  font-size: 13px;
  color: #606266;
}

.mono {
  font-family: Consolas, Monaco, monospace;
  color: #303133;
}

.muted {
  color: #a8abb2;
}

.usage {
  font-size: 12px;
  color: #606266;
}

/* 列表里超限的用量标红 */
.usage.over {
  color: #f56c6c;
}

.tip {
  font-size: 12px;
  color: #909399;
}

/* 原始值被系统顶截断的提示 */
.tip.warn {
  margin-top: 10px;
  color: #e6a23c;
}

/* ===== 插件：一行一个插件（开不开通 + 每日/并发额度） ===== */
.plug-list {
  max-height: 320px;
  overflow: auto;
}

.plug-row {
  padding: 8px 0;
  border-bottom: 1px dashed #ebeef5;
}

.plug-row:last-child {
  border-bottom: none;
}

.plug-head {
  display: flex;
  align-items: center;
  gap: 6px;
}

.plug-name {
  font-size: 13px;
  color: #303133;
}

.plug-key {
  font-family: Consolas, Monaco, monospace;
  font-size: 12px;
  color: #909399;
}

.plug-head .el-switch {
  margin-left: auto;
}

.plug-meta {
  display: flex;
  gap: 8px;
  margin-top: 4px;
  font-size: 12px;
  color: #909399;
}

.plug-off {
  color: #f56c6c;
}

.plug-mark {
  color: #e6a23c;
}

.plug-limits {
  display: flex;
  align-items: center;
  gap: 6px;
  margin-top: 6px;
}

.plug-limit-label {
  font-size: 12px;
  color: #606266;
}

.plug-num {
  width: 110px;
}

.plug-hint {
  font-size: 12px;
  color: #a8abb2;
}
</style>
