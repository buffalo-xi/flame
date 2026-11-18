<template>
  <div class="settings-container">
    <!-- 顶部：设置项跳转（页面 + 具体配置项，选中直接切 tab 并定位） -->
    <div class="settings-topbar">
      <div ref="searchBoxRef" class="settings-search">
        <el-input v-model="navKeyword" placeholder="搜索设置项，如「超时」「模型」「用户」" prefix-icon="Search"
                  clearable
                  @keydown.enter.prevent="pickActiveResult"
                  @keydown.down.prevent="moveActiveResult(1)"
                  @keydown.up.prevent="moveActiveResult(-1)"
                  @keydown.esc="navKeyword = ''" />
        <div v-if="navKeyword.trim()" class="search-results">
          <div v-if="!navResults.length" class="search-empty">
            没有匹配「{{ navKeyword.trim() }}」的设置项
          </div>
          <div v-for="(r, i) in navResults" :key="r.tab + (r.anchor || '')"
               class="search-item" :class="{ active: i === activeResultIndex }"
               @mouseenter="activeResultIndex = i"
               @click="gotoSetting(r)">
            <span class="search-item-main">
              <span v-if="r.group" class="search-item-group">{{ r.group }} ›</span>{{ r.title }}
            </span>
            <span v-if="r.hint" class="search-item-hint">{{ r.hint }}</span>
          </div>
        </div>
      </div>
    </div>

    <div class="settings-body">
      <!-- 左侧：图标 + 名称（选中＝浅蓝底 + 蓝边 + 蓝字） -->
      <div class="settings-nav">
        <div class="nav-card" :class="{ active: activeTab === 'llm' }" @click="activeTab = 'llm'">
          <div class="nav-card-icon"><el-icon><Cpu /></el-icon></div>
          <div class="nav-card-body">
            <div class="nav-card-title">LLM 配置</div>
            <div class="nav-card-count">{{ llmProviderCount ?? '—' }} 服务商 · {{ llmModelCount ?? '—' }} 模型</div>
          </div>
        </div>
        <div class="nav-card" :class="{ active: activeTab === 'mcp' }" @click="activeTab = 'mcp'">
          <div class="nav-card-icon"><el-icon><Connection /></el-icon></div>
          <div class="nav-card-body">
            <div class="nav-card-title">MCP 连接（出站）</div>
            <div class="nav-card-count">{{ mcpConnectionCount ?? '—' }}</div>
          </div>
        </div>
        <div class="nav-card" :class="{ active: activeTab === 'rpc' }" @click="activeTab = 'rpc'">
          <div class="nav-card-icon"><el-icon><Connection /></el-icon></div>
          <div class="nav-card-body">
            <div class="nav-card-title">RPC 连接（出站）</div>
            <div class="nav-card-count">{{ rpcConnectionCount ?? '—' }}</div>
          </div>
        </div>
        <div class="nav-card" :class="{ active: activeTab === 'mcpserverconfig' }" @click="activeTab = 'mcpserverconfig'">
          <div class="nav-card-icon"><el-icon><Lock /></el-icon></div>
          <div class="nav-card-body">
            <div class="nav-card-title">MCP 鉴权（入站）</div>
            <div class="nav-card-count">{{ mcpServerConfigCount ?? '—' }}</div>
          </div>
        </div>
        <div class="nav-card" :class="{ active: activeTab === 'rpcserverconfig' }" @click="activeTab = 'rpcserverconfig'">
          <div class="nav-card-icon"><el-icon><Lock /></el-icon></div>
          <div class="nav-card-body">
            <div class="nav-card-title">RPC 鉴权（入站）</div>
            <div class="nav-card-count">{{ rpcServerConfigCount ?? '—' }}</div>
          </div>
        </div>
        <div class="nav-card" :class="{ active: activeTab === 'svcauthbindings' }" @click="switchToBindings">
          <div class="nav-card-icon"><el-icon><Link /></el-icon></div>
          <div class="nav-card-body">
            <div class="nav-card-title">服务凭证</div>
            <div class="nav-card-count">{{ apikeyBindings.length + oauthBindings.length }}</div>
          </div>
        </div>
        <div class="nav-card" :class="{ active: activeTab === 'users' }" @click="switchToUsers">
          <div class="nav-card-icon"><el-icon><User /></el-icon></div>
          <div class="nav-card-body">
            <div class="nav-card-title">用户管理</div>
            <div class="nav-card-count">{{ users.length }} 用户</div>
          </div>
        </div>
        <div class="nav-card" :class="{ active: activeTab === 'globalVars' }" @click="activeTab = 'globalVars'">
          <div class="nav-card-icon"><el-icon><Key /></el-icon></div>
          <div class="nav-card-body">
            <div class="nav-card-title">全局变量</div>
            <div class="nav-card-count">本租户内全局</div>
          </div>
        </div>
        <div class="nav-card" :class="{ active: activeTab === 'tenantQuota' }" @click="activeTab = 'tenantQuota'">
          <div class="nav-card-icon"><el-icon><Odometer /></el-icon></div>
          <div class="nav-card-body">
            <div class="nav-card-title">租户配额</div>
            <div class="nav-card-count">系统顶 + 本租户</div>
          </div>
        </div>
        <div class="nav-card" :class="{ active: activeTab === 'flowConfig' }" @click="activeTab = 'flowConfig'">
          <div class="nav-card-icon"><el-icon><Tools /></el-icon></div>
          <div class="nav-card-body">
            <div class="nav-card-title">工作流运行参数</div>
            <div class="nav-card-count">系统缺省 + 租户</div>
          </div>
        </div>
        <div class="nav-card" :class="{ active: activeTab === 'agentConfig' }" @click="activeTab = 'agentConfig'">
          <div class="nav-card-icon"><el-icon><Cpu /></el-icon></div>
          <div class="nav-card-body">
            <div class="nav-card-title">智能体运行参数</div>
            <div class="nav-card-count">系统缺省 + 租户</div>
          </div>
        </div>
      </div>

      <!-- 右侧内容（表单侧边面板的定位基准） -->
      <div ref="settingsMainRef" class="settings-main">
        <!-- 内容区窄于 360px 时的替代显示：只留分区名 + 一句解释。
             硬塞进来的表格/表单会被压成一条，读不了也点不准，不如直说。 -->
        <div class="main-narrow-tip">
          <div class="main-narrow-title">{{ activeTabMeta.title }}</div>
          <div class="main-narrow-desc">{{ activeTabMeta.desc }}</div>
          <div class="main-narrow-hint">当前宽度不足，设置项已收起。把窗口拉宽即可正常显示。</div>
        </div>

        <!-- LLM：服务商内嵌模型 -->
        <LlmTab v-show="activeTab === 'llm'" :active="activeTab === 'llm'"
                @count="onLlmCount" />

        <!-- MCP 连接 -->
        <!-- MCP / RPC 连接（出站）：同一个组件的两个实例 -->
        <ConnectionTab v-show="activeTab === 'mcp'" :active="activeTab === 'mcp'"
                       kind="mcp" title="MCP 连接（出站）" :api="apiConfig.admin.mcpConnections"
                       url-placeholder="输入 MCP 服务 URL，如 http://localhost:8080/service/mcp/{schema}"
                       @count="(n) => (mcpConnectionCount = n)" />
        <ConnectionTab v-show="activeTab === 'rpc'" :active="activeTab === 'rpc'"
                       kind="rpc" title="RPC 连接（出站）" :api="apiConfig.admin.rpcConnections"
                       url-placeholder="输入 RPC 服务 URL，如 http://localhost:8080/service/rpc/{schema}"
                       with-token-url
                       @count="(n) => (rpcConnectionCount = n)" />

        <!-- MCP 鉴权（入站）配置 -->
        <!-- MCP / RPC 鉴权（入站）：两块界面一致，同一个组件两处复用 -->
        <ServerAuthTab v-show="activeTab === 'mcpserverconfig'" :active="activeTab === 'mcpserverconfig'"
                       kind="mcp" title="MCP 鉴权（入站）" :api="apiConfig.admin.mcpServerConfig"
                       scope-placeholder="可选，如 read:mcp" @refresh-count="refreshMcpConfigCount" />
        <ServerAuthTab v-show="activeTab === 'rpcserverconfig'" :active="activeTab === 'rpcserverconfig'"
                       kind="rpc" title="RPC 鉴权（入站）" :api="apiConfig.admin.rpcServerConfig"
                       scope-placeholder="可选，如 read:rpc" @refresh-count="refreshRpcConfigCount" />
        <!-- 服务凭证（独立配置项，MCP/RPC 共用） -->
        <BindingsTab v-show="activeTab === 'svcauthbindings'" :active="activeTab === 'svcauthbindings'" />

        <!-- 用户管理（role：manage 可登录控制台 / service 仅作为 API 身份） -->
        <UsersTab v-show="activeTab === 'users'" :active="activeTab === 'users'" />

        <!-- 全局变量（{schema}.global_vars）：本租户内全局，资产包只带引用名 -->
        <GlobalVarsTab v-show="activeTab === 'globalVars'" :active="activeTab === 'globalVars'" />

        <!-- 租户配额（独立配置项：本租户资源上限，受平台系统顶约束） -->
        <QuotaTab v-show="activeTab === 'tenantQuota'" :active="activeTab === 'tenantQuota'" />

        <!-- 工作流运行参数 -->
        <ConfigTableTab v-show="activeTab === 'flowConfig'" :active="activeTab === 'flowConfig'"
                        title="工作流运行参数" :fields="flowConfigFields"
                        :api="apiConfig.admin.flowconfigs" allow-unlimited>
          <template #hint>租户配置：留空 = 回退系统级，-1 = 不限制；租户 max_* 不能超过系统级</template>
        </ConfigTableTab>

        <!-- 智能体运行参数 -->
        <ConfigTableTab v-show="activeTab === 'agentConfig'" :active="activeTab === 'agentConfig'"
                        title="智能体运行参数" :fields="agentConfigFields"
                        :api="apiConfig.admin.agentconfigs">
          <template #hint>
            租户配置：留空 = 回退系统级；<b>本组没有 -1</b>（四项都是安全阀，调成「不限制」等于拆掉防线）；
            租户值不能超过系统级
          </template>
        </ConfigTableTab>
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref, computed, watch, nextTick, onMounted, onUnmounted } from 'vue'
import { Cpu, Connection, Lock, Link, User, Tools, Search, Odometer, Key } from '@element-plus/icons-vue'
import apiConfig from '../config/api.js'
import { getToken } from '../utils/auth.js'
import { provideSidePanel } from './settings/useSidePanel.js'
import LlmTab from './settings/tabs/LlmTab.vue'
import BindingsTab from './settings/tabs/BindingsTab.vue'
import UsersTab from './settings/tabs/UsersTab.vue'
import QuotaTab from './settings/tabs/QuotaTab.vue'
import ConfigTableTab from './settings/tabs/ConfigTableTab.vue'
import ServerAuthTab from './settings/tabs/ServerAuthTab.vue'
import ConnectionTab from './settings/tabs/ConnectionTab.vue'
import GlobalVarsTab from './settings/tabs/GlobalVarsTab.vue'
import { useBindings } from './settings/useBindings.js'
import { useUsers } from './settings/useUsers.js'


const props = defineProps({
  // 外部指定页签：登录后未配缺省大模型时，直接从控制台落到「LLM 配置」
  tab: { type: String, default: '' }
})

// 当前激活模块
const activeTab = ref(props.tab || 'llm')

// 被 keep-alive 复用时实例不会重建，外部再次点名要哪个页签就跟着走
watch(() => props.tab, (v) => {
  if (v) activeTab.value = v
})

// 内容区窄到放不下时（<360px）只显示这两行：分区名 + 这里能配什么
const tabMeta = {
  llm: { title: 'LLM 配置', desc: '管理大模型服务商与模型：接入地址、API Key、上下文长度与生成参数。' },
  mcp: { title: 'MCP 连接（出站）', desc: '以客户端身份接入外部 MCP 服务，供智能体调用它的工具。' },
  rpc: { title: 'RPC 连接（出站）', desc: '以客户端身份接入外部 RPC 服务，供智能体调用它的接口。' },
  mcpserverconfig: { title: 'MCP 鉴权（入站）', desc: '设置本租户 MCP 服务接受调用时的鉴权方式（API Key / OAuth）。' },
  rpcserverconfig: { title: 'RPC 鉴权（入站）', desc: '设置本租户 RPC 服务接受调用时的鉴权方式（API Key / OAuth）。' },
  svcauthbindings: { title: '服务凭证', desc: '把 API Key / OAuth 身份绑定到租户用户，MCP 与 RPC 共用一份凭证。' },
  users: { title: '用户管理', desc: '维护租户内账号、角色、密码，以及单用户的会话 / 空间配额。' },
  globalVars: { title: '全局变量', desc: '本租户内全局的键值配置，供工作流 / 智能体 / 技能引用；导出资产时只带引用名不带值。' },
  tenantQuota: { title: '租户配额', desc: '设置本租户的资源上限（智能体 / 技能 / 工作流 / 会话空间 / 会话数），受平台系统顶约束。' },
  flowConfig: { title: '工作流运行参数', desc: '限制工作流的运行时长、递归深度、节点数与记录数（留空回退系统级）。' },
  agentConfig: { title: '智能体运行参数', desc: '限制智能体的运行轮次、调用深度、工具数与输出大小（留空回退系统级）。' }
}
const activeTabMeta = computed(() => tabMeta[activeTab.value] || { title: '', desc: '' })

// 顶部搜索：跳转到某个设置项（页面，或页面里的具体配置项）
const navKeyword = ref('')
// 搜索面板的定位基准 + 点击面板外关闭
const searchBoxRef = ref(null)

// 切换 tab 的小助手（用户 / 绑定两页的数据由子组件在 active 变 true 时懒加载）
const switchToBindings = () => { activeTab.value = 'svcauthbindings' }
const switchToUsers = () => { activeTab.value = 'users' }

// LLM 服务商 / 模型列表在 LlmTab.vue 里，这里只留左侧导航上的计数（子组件加载后回报）。
// 初值用 null 而不是 0：这几个 tab 是懒加载的，没加载过时「不知道」不等于「没有」，
// 0 会让人以为真的一个都没有，模板里用 ?? '—' 区分这两种状态。
const llmProviderCount = ref(null)
const llmModelCount = ref(null)
const onLlmCount = ({ providers, models }) => {
  llmProviderCount.value = providers
  llmModelCount.value = models
}


// MCP / RPC 连接列表在 ConnectionTab.vue 里，这里只留左侧导航上的连接数（子组件加载后回报）
const mcpConnectionCount = ref(null)
const rpcConnectionCount = ref(null)

// MCP / RPC 鉴权（入站）：配置表单在 ServerAuthTab.vue，这里只留左侧导航上的
// 「已启用的鉴权方式数」计数（apikey / oauth 两条）
const mcpServerConfigCount = ref(null)
const rpcServerConfigCount = ref(null)

// 绑定列表 / 用户列表都是 module 级单例：这里只用来渲染左侧导航的计数
const { apikeyBindings, oauthBindings, loadBindings } = useBindings()
const { users, loadUsers } = useUsers()

// 两张「系统级 + 租户覆盖」配置表的字段定义；表格与保存逻辑在 ConfigTableTab.vue
// desc 只讲「这个数是什么」，怎么填（留空回退 / -1 不限制 / 不得超系统级）由模板里的 #hint 负责，别重复
const flowConfigFields = [
  { key: 'max_timeout', label: '最大超时（秒）', desc: '单次运行的总时长上限，到点强制终止' },
  { key: 'max_recursion_depth', label: '最大递归深度', desc: '工作流之间互相调用时可嵌套的层数' },
  { key: 'max_node_count', label: '最大节点数', desc: '一张流程图里允许的节点数量' },
  { key: 'max_exec_count', label: '最大执行次数', desc: '单次运行中节点被执行的累计次数' },
  { key: 'max_loop_times', label: '最大循环次数', desc: '循环节点的迭代次数上限' },
  { key: 'max_run_records', label: '最大运行记录数', desc: '每个工作流保留的历史运行记录条数' },
  { key: 'max_session_records', label: '最大会话记忆', desc: '会话中保留的上下文记录条数' }
]

const agentConfigFields = [
  { key: 'max_iterations', label: '最大运行轮次（轮）', desc: '一次对话里「思考 → 调工具 → 再思考」的最大轮数' },
  { key: 'max_call_depth', label: '最大调用深度（层）', desc: '智能体之间互相调用的嵌套层数' },
  { key: 'max_tools', label: '工具数量上限（个）', desc: '单个智能体最多能绑定的工具数量' },
  { key: 'max_output_bytes', label: '命令输出上限（字节）', desc: '单条命令或工具输出的截断长度' }
]

// ============ 顶部搜索：设置项跳转 ============
// 索引里全是前端写死的名字，不含任何拉取到的数据，所以搜索是零请求的
// （不像搜用户名 / 连接名那样必须先加载列表 —— 那些是懒加载的，没点开就没有）。
const settingsPages = [
  { tab: 'llm', title: 'LLM 配置', hint: '服务商 · 模型 · API Key', kw: 'llm 模型 服务商 apikey api key 密钥 provider model' },
  { tab: 'mcp', title: 'MCP 连接（出站）', hint: '远程 MCP 服务', kw: 'mcp 连接 出站 客户端 远程 工具 client' },
  { tab: 'rpc', title: 'RPC 连接（出站）', hint: '远程 RPC 服务', kw: 'rpc 连接 出站 客户端 远程 工具 client' },
  { tab: 'mcpserverconfig', title: 'MCP 鉴权（入站）', hint: '入站鉴权方式', kw: 'mcp 鉴权 入站 服务器 apikey oauth 密钥' },
  { tab: 'rpcserverconfig', title: 'RPC 鉴权（入站）', hint: '入站鉴权方式', kw: 'rpc 鉴权 入站 服务器 apikey oauth 密钥' },
  { tab: 'svcauthbindings', title: '服务凭证', hint: 'API Key / OAuth 绑定关系', kw: '凭证 绑定 服务鉴权 用户 apikey oauth 密钥' },
  { tab: 'users', title: '用户管理', hint: '账号 · 角色 · 密码 · 配额', kw: '用户 账号 角色 密码 配额 重置' },
  { tab: 'tenantQuota', title: '租户配额', hint: '本租户资源上限 · 受系统顶约束', kw: '租户 配额 上限 智能体 技能 工作流 会话 空间 存储 quota' }
]

const settingsIndex = [
  ...settingsPages,
  ...flowConfigFields.map((f) => ({
    tab: 'flowConfig', group: '工作流运行参数', title: f.label, hint: f.desc, anchor: f.key,
    kw: '工作流 运行参数 限制 配置 ' + f.key
  })),
  ...agentConfigFields.map((f) => ({
    tab: 'agentConfig', group: '智能体运行参数', title: f.label, hint: f.desc, anchor: f.key,
    kw: '智能体 运行参数 限制 配置 ' + f.key
  })),
  // 租户配额的五项上限（字段定义在 QuotaTab.vue，这里只留搜索用的名字）
  ...[
    { key: 'max_agents', label: '智能体数量上限', desc: '本租户最多可创建多少个智能体' },
    { key: 'max_skills', label: '技能数量上限', desc: '本租户最多可创建多少个技能' },
    { key: 'max_flows', label: '工作流数量上限', desc: '本租户最多可创建多少个工作流' },
    { key: 'max_session_storage_bytes', label: '单会话空间上限（租户默认）', desc: '单个会话工作区的空间上限（MiB）' },
    { key: 'max_user_sessions', label: '单用户会话数上限（租户默认）', desc: '每个用户默认最多拥有多少个会话' }
  ].map((f) => ({
    tab: 'tenantQuota', group: '租户配额', title: f.label, hint: f.desc, anchor: f.key,
    kw: '租户 配额 上限 限制 配置 ' + f.key
  })),
  // 会话相关的两个上限属于配额，不在「运行参数」两组里；搜索时指到「用户管理」的按用户覆盖。
  {
    tab: 'users',
    group: '用户管理',
    title: '单用户会话数上限（用户覆盖）',
    hint: '为某个用户单独设的会话数上限（留空跟随租户，-1 不限）',
    kw: '会话 数量 上限 配额 max_user_sessions'
  },
  {
    tab: 'users',
    group: '用户管理',
    title: '单会话空间上限（用户覆盖）',
    hint: '为某个用户单独设的空间上限（留空跟随租户，-1 不限）',
    kw: '会话 空间 上限 存储 配额 max_session_storage_bytes'
  }
]

// 归一化后按空格分词，「全部命中」才算匹配 —— 这样「mcp 鉴权」能把 MCP 连接那条筛掉
const normalizeNav = (s) => String(s).toLowerCase().replace(/[\s（）()【】[\]]/g, '')
const navResults = computed(() => {
  const kws = String(navKeyword.value).trim().toLowerCase().split(/\s+/).filter(Boolean).map(normalizeNav)
  if (!kws.length) return []
  return settingsIndex
    .filter((item) => {
      const hay = normalizeNav(`${item.group || ''}${item.title}${item.hint || ''}${item.kw || ''}`)
      return kws.every((k) => hay.includes(k))
    })
    .slice(0, 8)
})

// 键盘上下移动选中项；回车取当前项（没有高亮时取第一条）
const activeResultIndex = ref(0)
const moveActiveResult = (step) => {
  if (!navResults.value.length) return
  const n = navResults.value.length
  activeResultIndex.value = (activeResultIndex.value + step + n) % n
}
const pickActiveResult = () => {
  const r = navResults.value[activeResultIndex.value] || navResults.value[0]
  if (r) gotoSetting(r)
}

// 关键词一变就回到第一条：否则上一次下移到 3 的高亮会落到新结果集的第 3 条上，
// 回车跳的和眼前看到的第一条不是同一个。
watch(navKeyword, () => { activeResultIndex.value = 0 })

// 跳到目标：切 tab；如果是具体配置项，再滚到那一行并短暂高亮
const gotoSetting = (item) => {
  activeTab.value = item.tab
  navKeyword.value = ''
  activeResultIndex.value = 0
  if (!item.anchor) return
  nextTick(() => {
    const el = settingsMainRef.value && settingsMainRef.value.querySelector(`[data-field="${item.anchor}"]`)
    if (!el) return
    el.scrollIntoView({ block: 'center', behavior: 'smooth' })
    el.classList.add('field-flash')
    setTimeout(() => el.classList.remove('field-flash'), 1400)
  })
}

// 点搜索框以外的地方收起结果面板
const onSearchOutside = (e) => {
  if (searchBoxRef.value && searchBoxRef.value.contains(e.target)) return
  navKeyword.value = ''
}
onMounted(() => document.addEventListener('mousedown', onSearchOutside, true))
onUnmounted(() => document.removeEventListener('mousedown', onSearchOutside, true))
// MCP 鉴权（入站）数据在 onMounted 预加载，切换 auth_type 时由 @change 触发加载，无需 watch activeTab

// 统计已启用的鉴权方式数量（仅用于概览卡片）
const refreshMcpConfigCount = async () => {
  try {
    const token = getToken()
    const types = ['apikey', 'oauth']
    let enabledCount = 0
    await Promise.all(types.map(async (t) => {
      try {
        const res = await fetch(apiConfig.admin.mcpServerConfig.get(t), {
          method: 'GET',
          headers: { 'Authorization': `Bearer ${token}` }
        })
        if (res.ok) {
          const d = await res.json()
          if (d?.data?.is_active) enabledCount++
        }
      } catch (e) { /* 忽略单类型错误 */ }
    }))
    mcpServerConfigCount.value = enabledCount
  } catch (e) {
    mcpServerConfigCount.value = 0
  }
}

// 统计 RPC 已启用的鉴权方式数量（仅用于概览卡片）
const refreshRpcConfigCount = async () => {
  try {
    const token = getToken()
    const types = ['apikey', 'oauth']
    let enabledCount = 0
    await Promise.all(types.map(async (t) => {
      try {
        const res = await fetch(apiConfig.admin.rpcServerConfig.get(t), {
          method: 'GET',
          headers: { 'Authorization': `Bearer ${token}` }
        })
        if (res.ok) {
          const d = await res.json()
          if (d?.data?.is_active) enabledCount++
        }
      } catch (e) { /* 忽略单类型错误 */ }
    }))
    rpcServerConfigCount.value = enabledCount
  } catch (e) {
    rpcServerConfigCount.value = 0
  }
}

// 连接的新建/编辑面板、状态与保存逻辑都在 ConnectionTab.vue

// 组件挂载时：只拉导航上的计数（LLM / 连接 / 配置表仍在各自 tab 里按需加载）
onMounted(() => {
  // 服务鉴权方式数：两个轻请求，本来就在这里拉
  refreshMcpConfigCount()
  refreshRpcConfigCount()
  // 用户 / 绑定数也显示在左侧导航上，各预载一次（module 级单例，切到对应 tab 时会再刷）。
  // 这两个请求很轻，不像连接列表还要按连接扇出工具数，所以可以在这里无条件拉。
  loadUsers()
  loadBindings()
  // LLM 服务商/模型、MCP/RPC 连接数：由各自 tab 加载后回报，未加载时导航显示「—」
  // 工作流/智能体运行参数由各自的 ConfigTableTab 在切到该 tab 时加载
})

// 连接（列表 / 面板 / 工具预览 / 保存）全部在 ConnectionTab.vue
// ============ 表单侧边面板（外壳见 settings/SidePanel.vue，行为见 useSidePanel） ============
// 定位基准：右侧内容区 .settings-main；面板与它顶部对齐、贴窗口右边缘。
// 各 tab 的面板（LlmTab / ConnectionTab / ServerAuthTab / BindingsTab / UsersTab）
// 通过 useSidePanelContext() 拿同一套坐标，并各自 register 自己的可见性开关。
const settingsMainRef = ref(null)
provideSidePanel({
  anchorRef: settingsMainRef,
  // 端点范围面板在子组件里，关闭时由它自己复位附庸的候选列表
  onCloseAll: () => {}
})
</script>

<style scoped>
.settings-container {
  padding: 20px 24px 24px;
  height: 100%;
  overflow-y: auto;
  background-color: #fafafa;
  box-sizing: border-box;
  /* 滚动条：位置常驻（一直 6px），拇指默认透明 → 鼠标悬停在页面上才上色。
     常驻占位是为了避免"条突然出现"时内容横向跳动；透明则是默认看不见。 */
  scrollbar-width: thin;                    /* Firefox */
  scrollbar-color: transparent transparent;
  transition: scrollbar-color 0.2s;
}

.settings-container::-webkit-scrollbar {
  width: 6px;
  height: 6px;
}

.settings-container::-webkit-scrollbar-track {
  background: transparent;
}

.settings-container::-webkit-scrollbar-thumb {
  background: transparent;                  /* 不悬浮：看不见，但占位 */
  border-radius: 3px;
}

.settings-container:hover {
  scrollbar-color: rgba(144, 147, 153, 0.4) transparent; /* Firefox */
}

.settings-container:hover::-webkit-scrollbar-thumb {
  background: rgba(144, 147, 153, 0.4);
}

.settings-container:hover::-webkit-scrollbar-thumb:hover {
  background: rgba(144, 147, 153, 0.6);
}

/* ===== 顶部：搜索（按名称过滤左侧导航） ===== */
.settings-topbar {
  display: flex;
  align-items: center;
  margin-bottom: 20px;
}

.settings-search {
  flex: 1;
  max-width: 720px;
  position: relative; /* 结果面板的定位基准 */
}

/* 搜索结果面板：挂在输入框下方，最多 8 条，超出内部滚动 */
.search-results {
  position: absolute;
  top: calc(100% + 6px);
  left: 0;
  right: 0;
  z-index: 30;
  background: #ffffff;
  border: 1px solid #e6e8eb;
  border-radius: 10px;
  box-shadow: 0 6px 20px rgba(0, 0, 0, 0.08);
  padding: 4px;
  max-height: 320px;
  overflow-y: auto;
}

.search-item {
  display: flex;
  flex-direction: column;
  gap: 2px;
  padding: 8px 10px;
  border-radius: 8px;
  cursor: pointer;
}

.search-item.active {
  background: #eef4ff;
}

.search-item-main {
  font-size: 13px;
  color: #1f2329;
}

.search-item-group {
  color: #909399;
}

.search-item-hint {
  font-size: 12px;
  color: #a0a4ab;
  line-height: 1.5;
}

.search-empty {
  padding: 12px 10px;
  font-size: 13px;
  color: #a0a4ab;
}

.settings-search :deep(.el-input__wrapper) {
  background: #f1f3f4;
  border-radius: 10px;
  box-shadow: none;
}

.settings-search :deep(.el-input__wrapper:hover) {
  background: #f1f3f4;
}

.settings-search :deep(.el-input__wrapper.is-focus) {
  background: #eef1f4;
  box-shadow: none;
}

/* 左侧导航项：Chrome 式行（无边框、无卡片底；选中＝浅蓝底 + 蓝边 + 蓝字） */
.nav-card {
  display: flex;
  align-items: center;
  gap: 12px;
  height: 40px;
  padding: 0 12px;
  background: transparent;
  border: 1px solid transparent;
  border-radius: 20px;
  cursor: pointer;
  transition: background-color 0.15s ease, border-color 0.15s ease;
}

.nav-card:hover {
  background: #f1f3f4;
}

.nav-card.active {
  background: #d3e3fd;
  border-color: #0b57d0;
}

.nav-card-icon {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 20px;
  height: 20px;
  flex-shrink: 0;
  color: #444746;
  font-size: 18px;
}

.nav-card.active .nav-card-icon {
  color: #0b57d0;
}

.nav-card-body {
  flex: 1;
  min-width: 0;
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}

.nav-card-title {
  font-size: 14px;
  color: #1f1f1f;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.nav-card.active .nav-card-title {
  color: #0b57d0;
  font-weight: 500;
}

.nav-card-count {
  font-size: 12px;
  font-weight: 400;
  color: #80868b;
  flex-shrink: 0;
}

.nav-card.active .nav-card-count {
  color: #0b57d0;
}

/* 主体布局 */
.settings-body {
  display: flex;
  gap: 24px;
  align-items: flex-start;
}

.settings-nav {
  width: 260px;
  flex-shrink: 0;
  display: flex;
  flex-direction: column;
  gap: 2px;
}

/* 搜索无结果时的左侧空态 */
.settings-nav-empty {
  padding: 16px 12px;
  font-size: 13px;
  color: #a0a4ab;
  line-height: 1.6;
}

.settings-main {
  flex: 1;
  min-width: 0;
  /* 容器查询基准：取内容区真实分到的宽度（已被左侧导航 260px 挤过），
     按视口宽度算会差一个导航的宽度 */
  container-type: inline-size;
}

/* 内容区窄时的替代显示；正常宽度下不占位 */
.main-narrow-tip {
  display: none;
  padding: 16px 18px;
  border: 1px dashed #e0e2e6;
  border-radius: 10px;
  background: #fff;
}

.main-narrow-title {
  font-size: 14px;
  font-weight: 600;
  color: #1f2329;
  margin-bottom: 6px;
}

.main-narrow-desc {
  font-size: 13px;
  line-height: 1.7;
  color: #80868b;
}

/* 说明这块是「怎么才能看到内容」，跟上面的分区介绍分开一层。
   用浅黄底 + 深橙字压住，别做成灰字小字 —— 那句话是唯一的操作指引，看不见就白搭。 */
.main-narrow-hint {
  margin-top: 12px;
  padding: 8px 10px;
  border: 1px solid #ffe0b2;
  border-radius: 6px;
  background: #fff7e6;
  font-size: 13px;
  font-weight: 500;
  line-height: 1.6;
  color: #b2560d;
}

/* 内容区不足 360px：整块内容收起来，只留分区名 + 解释 */
@container (max-width: 360px) {
  .settings-main > *:not(.main-narrow-tip) {
    display: none;
  }
  .main-narrow-tip {
    display: block;
  }
}

/* 再窄到 50px：说明本身也排不下，整块内容区都不显示。
   必须写在上面那块之后 —— 同为 max-width 条件的规则，靠后者生效。 */
@container (max-width: 50px) {
  .main-narrow-tip {
    display: none;
  }
}

@media (max-width: 768px) {
  .stat-row {
    grid-template-columns: repeat(2, 1fr);
  }
  .settings-body {
    flex-direction: column;
  }
  .settings-nav {
    width: 100%;
  }
}
</style>
