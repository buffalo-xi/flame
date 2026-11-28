<template>
  <div v-if="visible" class="node-panel" :style="{ ...panelStyle, left: left }" @click.stop>
    <div class="panel-header">
      <span
        class="panel-tab"
        :class="{ active: activeTab === 'node' }"
        @click="activeTab = 'node'"
      >
        {{ t('flow.nodePanel.tabNode') }}
      </span>
      <span
        class="panel-tab"
        :class="{ active: activeTab === 'tool' }"
        @click="activeTab = 'tool'"
      >
        {{ t('flow.nodePanel.tabTool') }}
      </span>
    </div>
    <div class="panel-content">
      <div v-show="activeTab === 'node'" class="panel-section">
        <div class="node-group">
          <div class="group-title">{{ t('flow.nodePanel.groupCommon') }}</div>
          <div class="group-items">
            <div class="node-card" @click="handleNodeClick('llm')">
              <span class="icon-wrapper icon-llm"><el-icon><ChatLineSquare /></el-icon></span>
              <span class="node-label">{{ nodeLabel('llm') }}</span>
            </div>
            <div class="node-card" @click="handleNodeClick('memory')">
              <span class="icon-wrapper icon-memory"><el-icon><Clock /></el-icon></span>
              <span class="node-label">{{ nodeLabel('memory') }}</span>
            </div>
            <div class="node-card" @click="handleNodeClick('code')">
              <span class="icon-wrapper icon-code"><el-icon><MagicStick /></el-icon></span>
              <span class="node-label">{{ nodeLabel('code') }}</span>
            </div>
            <div class="node-card" @click="handleNodeClick('assign')">
              <span class="icon-wrapper icon-assign"><el-icon><Coin /></el-icon></span>
              <span class="node-label">{{ nodeLabel('assign') }}</span>
            </div>            
            <div class="node-card" @click="handleNodeClick('http')">
              <span class="icon-wrapper icon-http"><el-icon><Lightning /></el-icon></span>
              <span class="node-label">{{ nodeLabel('http') }}</span>
            </div>
            <div class="node-card" @click="handleNodeClick('mcp')">
              <span class="icon-wrapper icon-mcp"><el-icon><Tools /></el-icon></span>
              <span class="node-label">{{ nodeLabel('mcp') }}</span>
            </div>
            <div class="node-card" @click="handleNodeClick('rpc')">
              <span class="icon-wrapper icon-rpc"><el-icon><Mouse /></el-icon></span>
              <span class="node-label">{{ nodeLabel('rpc') }}</span>
            </div>
            <div class="node-card" @click="handleNodeClick('result')">
              <span class="icon-wrapper icon-result"><el-icon><Promotion /></el-icon></span>    
              <span class="node-label">{{ nodeLabel('result') }}</span>
            </div>
            <div class="node-card" @click="handleNodeClick('progress')">
              <span class="icon-wrapper icon-progress"><el-icon><RefreshRight /></el-icon></span>
              <span class="node-label">{{ nodeLabel('progress') }}</span>
            </div>
            
          </div>
        </div>
        <div class="node-group">
          <div class="group-title">{{ t('flow.nodePanel.groupLogic') }}</div>
          <div class="group-items">
            <div class="node-card" @click="handleNodeClick('branch')">
              <span class="icon-wrapper icon-branch"><el-icon><Connection /></el-icon></span>
              <span class="node-label">{{ nodeLabel('branch') }}</span>
            </div>
            <div class="node-card" @click="handleNodeClick('intent')">
              <span class="icon-wrapper icon-intent"><el-icon><Aim /></el-icon></span>
              <span class="node-label">{{ nodeLabel('intent') }}</span>
            </div>
            <div class="node-card" @click="handleNodeClick('raise')">
              <span class="icon-wrapper icon-raise"><el-icon><WarnTriangleFilled /></el-icon></span>
              <span class="node-label">{{ nodeLabel('raise') }}</span>
            </div>
            <div class="node-card" @click="handleNodeClick('break')">
              <span class="icon-wrapper icon-break"><el-icon><Minus /></el-icon></span>
              <span class="node-label">{{ nodeLabel('break') }}</span>
            </div>
            <div class="node-card" @click="handleNodeClick('continue')">
              <span class="icon-wrapper icon-continue"><el-icon><Upload /></el-icon></span>
              <span class="node-label">{{ nodeLabel('continue') }}</span>
            </div>
            <div class="node-card" @click="handleNodeClick('return')">
              <span class="icon-wrapper icon-return"><el-icon><CircleCheck /></el-icon></span>
              <span class="node-label">{{ nodeLabel('return') }}</span>
            </div>
            <div class="node-card" @click="handleNodeClick('exit')">
              <span class="icon-wrapper icon-exit"><el-icon><SwitchButton /></el-icon></span>
              <span class="node-label">{{ nodeLabel('exit') }}</span>
            </div>
            <div class="node-card" @click="handleNodeClick('process')">
              <span class="icon-wrapper icon-process"><el-icon><Help /></el-icon></span>
              <span class="node-label">{{ nodeLabel('process') }}</span>
            </div>
            <div class="node-card" @click="handleNodeClick('function')">
              <span class="icon-wrapper icon-function"><el-icon><ScaleToOriginal /></el-icon></span>
              <span class="node-label">{{ nodeLabel('function') }}</span>
            </div>
            <div class="node-card" @click="handleNodeClick('call')">
              <span class="icon-wrapper icon-call"><el-icon><Mouse /></el-icon></span>
              <span class="node-label">{{ nodeLabel('call') }}</span>
            </div>
            <div class="node-card" @click="handleNodeClick('loop')">
              <span class="icon-wrapper icon-loop"><el-icon><RefreshLeft /></el-icon></span>
              <span class="node-label">{{ nodeLabel('loop') }}</span>
            </div>
          </div>
        </div>
        <div class="node-group">
          <div class="group-title">{{ t('flow.nodePanel.groupOther') }}</div>
          <div class="group-items">            
            <div class="node-card" @click="handleNodeClick('layout')">
              <span class="icon-wrapper icon-layout"><el-icon><CircleClose /></el-icon></span>
              <span class="node-label">{{ nodeLabel('layout') }}</span>
            </div>
            <div class="node-card" @click="handleNodeClick('comment')">
              <span class="icon-wrapper icon-comment"><el-icon><ChatLineSquare /></el-icon></span>
              <span class="node-label">{{ nodeLabel('comment') }}</span>
            </div>
          </div>
        </div>
      </div>
      <div v-show="activeTab === 'tool'" class="panel-section">
        <el-input v-model="search" size="small" clearable class="tool-search"
                  :placeholder="t('flow.nodePanel.searchTools')" />
        <div v-if="catalogLoading" class="tool-empty">{{ t('flow.nodePanel.loadingTools') }}</div>
        <template v-else>
          <div v-if="visibleEndpoints.length" class="node-group">
            <div class="group-title">{{ t('flow.nodePanel.groupEndpoints') }}</div>
            <div class="tool-list">
              <div v-for="ep in visibleEndpoints" :key="ep.id" class="tool-card" @click="pickEndpoint(ep)">
                <el-icon class="tool-icon"><Link /></el-icon>
                <div class="tool-text">
                  <div class="tool-name">{{ ep.name }}</div>
                  <div class="tool-desc">{{ ep.description || '' }}</div>
                </div>
                <span v-if="endpointKind(ep)" class="tool-kind">{{ endpointKind(ep) }}</span>
              </div>
            </div>
          </div>

          <div v-for="group in connections" :key="group.kind" class="node-group">
            <div class="group-title">{{ t(group.titleKey) }}</div>
            <div v-if="!group.items.length" class="tool-empty">{{ t(group.emptyKey) }}</div>
            <div v-else class="tool-list">
              <div v-for="conn in group.items" :key="conn.id" class="conn-block">
                <div class="conn-head" @click="toggleConnection(group.kind, conn)">
                  <el-icon class="conn-caret" :class="{ open: conn.expanded }"><CaretRight /></el-icon>
                  <span class="conn-name">{{ conn.name }}</span>
                  <span v-if="conn.loaded && conn.tools.length" class="conn-count">{{ conn.tools.length }}</span>
                </div>
                <div v-if="conn.expanded" class="conn-tools">
                  <div v-if="conn.loading" class="tool-empty">{{ t('flow.nodePanel.loadingTools') }}</div>
                  <template v-else>
                    <div v-if="!conn.tools.length" class="tool-empty">{{ t('flow.nodePanel.noTools') }}</div>
                    <div v-for="tool in conn.tools" :key="toolName(tool)" class="tool-card"
                         @click="pickTool(group.kind, conn, tool)">
                      <el-icon class="tool-icon"><Tools /></el-icon>
                      <div class="tool-text">
                        <div class="tool-name">{{ toolName(tool) }}</div>
                        <div class="tool-desc">{{ toolDescription(tool) }}</div>
                      </div>
                    </div>
                  </template>
                </div>
              </div>
            </div>
          </div>

          <div v-if="!hasCatalog" class="tool-empty">{{ t('flow.nodePanel.emptyCatalog') }}</div>
          <div v-else-if="query && !visibleEndpoints.length && !connections.some(g => g.items.length)"
               class="tool-empty">{{ t('flow.nodePanel.searchEmpty') }}</div>
        </template>
      </div>
    </div>
  </div>
</template>

<script setup>
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { ChatLineSquare, Clock, Promotion, RefreshRight, Connection, Help, MagicStick, Lightning, SwitchButton, CircleClose, Minus, Upload, CircleCheck, ScaleToOriginal, Mouse, Coin, RefreshLeft, Tools, Aim, CaretRight, Link, WarnTriangleFilled } from '@element-plus/icons-vue'
import { useEscClose } from '../../../composables/useEscClose'
import apiConfig from '../../../config/api.js'
import { getAuthHeader } from '../../../utils/auth.js'
import { sampleArguments } from './toolSample.js'

const { t } = useI18n()

// 节点类型的显示名统一走语言包：键与 nodeIcons.js 的类型表一致
const nodeLabel = (type) => t(`flow.nodeType.${type}`)

const props = defineProps({
  visible: {
    type: Boolean,
    default: false
  },
  left: {
    type: String,
    default: '60px'
  },
  // 当前流程 id：本地端点目录必须排除「指向自己」的那一条，否则建出来的节点一定跑不通
  flowId: {
    type: String,
    default: ''
  }
})

const emit = defineEmits(['node-select', 'close'])

// 选择节点窗口没有关闭按钮（靠点外部收起），ESC 补一个同义出口
useEscClose(() => props.visible, () => emit('close'))

const activeTab = ref('node')

const panelStyle = {
  top: '2%',
  bottom: '2%',
  width: '300px'
}

const handleNodeClick = (nodeType) => {
  emit('node-select', nodeType)
}

// ── 工具目录 ──
// 三组的数据来源与预填字段各不相同：本地端点写 rpc.endpoint（存端点名，改名同步），RPC / MCP 连接写
// connection_id + tool_name。连接里的工具清单在展开时才拉，避免一打开面板就是 N+1 次请求。
const catalogLoaded = ref(false)
const catalogLoading = ref(false)
const search = ref('')
const endpoints = ref([])
const mcpConnections = ref([])
const rpcConnections = ref([])

const request = async (url, { method = 'GET', body } = {}) => {
  const headers = { ...getAuthHeader() }
  if (body) headers['Content-Type'] = 'application/json'
  const res = await fetch(url, { method, headers, body: body ? JSON.stringify(body) : undefined })
  const payload = await res.json().catch(() => ({}))
  if (!res.ok || (typeof payload.code === 'number' && payload.code !== 200)) {
    throw new Error(payload?.msg || payload?.message || `HTTP ${res.status}`)
  }
  // 列表接口的空结果回的是 data: null，非空时 data 可能是数组、也可能包成 {items: [...]}。这里只接受数组：
  // 直接 `data ?? payload` 在 data 为 null 时会把整个信封对象当成列表，v-for 于是按值迭代出带图标没内容的行。
  const data = payload?.data
  if (Array.isArray(data)) return data
  if (Array.isArray(data?.items)) return data.items
  return []
}

const decorate = (items) => (Array.isArray(items) ? items : []).map(c => ({
  id: c.id,
  name: c.name || c.id,
  tools: [],
  loaded: false,
  loading: false,
  expanded: false
}))

const loadCatalog = async () => {
  if (catalogLoading.value) return
  catalogLoading.value = true
  try {
    const [eps, mcps, rpcs] = await Promise.all([
      request(apiConfig.admin.svcEndpoints.base, {
        method: 'POST',
        body: { page: 1, limit: 200, protocol: 'rpc', sort_by: ['name'], sort_order: ['asc'] }
      }),
      request(apiConfig.admin.mcpConnections.base, {
        method: 'POST',
        body: { page: 1, limit: -1, sort_by: ['name'], sort_order: ['asc'] }
      }),
      request(apiConfig.admin.rpcConnections.base, {
        method: 'POST',
        body: { page: 1, limit: -1, sort_by: ['name'], sort_order: ['asc'] }
      })
    ])
    endpoints.value = (Array.isArray(eps) ? eps : []).filter(ep =>
      !(props.flowId && ep.target_type === 'flow' && String(ep.target_id) === String(props.flowId)))
    mcpConnections.value = decorate(mcps)
    rpcConnections.value = decorate(rpcs)
    catalogLoaded.value = true
  } catch (e) {
    console.error('load tool catalog failed:', e)
  } finally {
    catalogLoading.value = false
  }
}

// 目录只在真正切到「工具」tab 时加载，只逛节点 tab 的人不会为它付请求；面板关掉后清标记，
// 下次打开重新拉一次，免得刚在「设置 → 工具」里加完连接回来还是旧清单
watch([() => props.visible, activeTab], ([visible, tab]) => {
  if (!visible) {
    catalogLoaded.value = false
    return
  }
  if (tab === 'tool' && !catalogLoaded.value) loadCatalog()
})

const toolName = (tool) => tool?.name || tool?.tool_name || tool?.id || ''
const toolDescription = (tool) => tool?.description || ''

const toggleConnection = async (kind, conn) => {
  conn.expanded = !conn.expanded
  if (!conn.expanded || conn.loaded || conn.loading) return
  conn.loading = true
  try {
    const api = kind === 'mcp' ? apiConfig.admin.mcpConnections : apiConfig.admin.rpcConnections
    conn.tools = (await request(api.tools(conn.id))) || []
    conn.loaded = true
  } catch (e) {
    console.error('load connection tools failed:', e)
    conn.tools = []
  } finally {
    conn.loading = false
  }
}

const query = computed(() => search.value.trim().toLowerCase())
const hit = (...values) => !query.value || values.some(v => String(v || '').toLowerCase().includes(query.value))

const visibleEndpoints = computed(() => endpoints.value.filter(ep => hit(ep.name, ep.description)))

const visibleByKind = (kind) => {
  const list = kind === 'mcp' ? mcpConnections.value : rpcConnections.value
  return list.filter(c => hit(c.name) || c.tools.some(tool => hit(toolName(tool), toolDescription(tool))))
}

const connections = computed(() => [
  { kind: 'mcp', titleKey: 'flow.nodePanel.groupMcp', emptyKey: 'flow.nodePanel.emptyMcp', items: visibleByKind('mcp') },
  { kind: 'rpc', titleKey: 'flow.nodePanel.groupRpc', emptyKey: 'flow.nodePanel.emptyRpc', items: visibleByKind('rpc') }
])

const hasCatalog = computed(() => !!(endpoints.value.length || mcpConnections.value.length || rpcConnections.value.length))

const endpointKind = (ep) => {
  if (ep.target_type === 'flow') return t('flow.nodePanel.targetFlow')
  if (ep.target_type === 'agent') return t('flow.nodePanel.targetAgent')
  return ''
}

const emitPick = (nodeType, preset) => emit('node-select', nodeType, preset)

// 预填的是属性面板写的那几个字段本身，所以建出来就是「已选好连接和工具、参数有样本」的节点
const pickEndpoint = (ep) => emitPick('rpc', {
  rpc: { target_type: 'endpoint', endpoint: ep.name, arguments: sampleArguments(ep) }
})

const pickTool = (kind, conn, tool) => {
  const args = sampleArguments(tool)
  if (kind === 'mcp') {
    emitPick('mcp', { mcp: { connection_id: conn.id, tool_name: toolName(tool), arguments: args } })
  } else {
    emitPick('rpc', {
      rpc: { target_type: 'connection', connection_id: conn.id, tool_name: toolName(tool), arguments: args }
    })
  }
}
</script>

<style scoped>
.node-panel {
  position: absolute;
  left: 60px;
  background-color: #ffffff;
  border-radius: 8px;
  box-shadow: 0 2px 12px rgba(0, 0, 0, 0.15);
  display: flex;
  flex-direction: column;
  overflow: hidden;
  z-index: 1000;
}

.panel-header {
  display: flex;
  gap: 8px;
  padding: 1px 12px 0 12px;
  background-color: #f5f5f5;
}

.panel-tab {
  padding: 8px 16px;
  text-align: center;
  font-size: 14px;
  color: #666;
  cursor: pointer;
  transition: all 0.2s;
  border-radius: 12px 12px 0 0;
}

.panel-tab:hover {
  background-color: rgba(255, 255, 255, 0.5);
}

.panel-tab.active {
  background-color: #ffffff;
  color: #1890ff;
  font-weight: 600;
  box-shadow: 0 -2px 8px rgba(0, 0, 0, 0.08);
}

.panel-content {
  flex: 1;
  overflow-y: auto;
  overflow-x: hidden;
  padding: 12px;
}

.panel-section {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.node-group {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.group-title {
  font-size: 12px;
  font-weight: 600;
  color: #aaa;
  padding-left: 3px;
}

/* ── 工具 tab：条目是「列表行」，不是节点 tab 那张图标网格 ── */
.tool-search {
  margin-bottom: 2px;
}

.tool-list {
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.tool-card {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 6px 8px;
  border-radius: 8px;
  cursor: pointer;
  transition: background-color 0.2s;
}

.tool-card:hover {
  background-color: #e6f0f8;
}

.tool-icon {
  flex: none;
  font-size: 14px;
  color: #909399;
}

.tool-card:hover .tool-icon {
  color: #1890ff;
}

.tool-text {
  flex: 1;
  min-width: 0;
}

.tool-name {
  font-size: 12px;
  color: #333;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.tool-card:hover .tool-name {
  color: #1890ff;
}

.tool-desc {
  font-size: 11px;
  color: #aaa;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.tool-kind {
  flex: none;
  padding: 0 4px;
  font-size: 10px;
  color: #909399;
  background-color: #f2f3f5;
  border-radius: 4px;
}

.tool-empty {
  padding: 4px 3px;
  font-size: 11px;
  color: #bbb;
}

.conn-block {
  display: flex;
  flex-direction: column;
}

.conn-head {
  display: flex;
  align-items: center;
  gap: 4px;
  padding: 5px 6px;
  border-radius: 6px;
  cursor: pointer;
  transition: background-color 0.2s;
}

.conn-head:hover {
  background-color: #f5f7fa;
}

.conn-caret {
  flex: none;
  font-size: 12px;
  color: #909399;
  transition: transform 0.2s;
}

.conn-caret.open {
  transform: rotate(90deg);
}

.conn-name {
  flex: 1;
  min-width: 0;
  font-size: 12px;
  color: #333;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.conn-count {
  flex: none;
  font-size: 10px;
  color: #909399;
}

.conn-tools {
  display: flex;
  flex-direction: column;
  gap: 2px;
  padding-left: 12px;
}

.group-items {
  display: grid !important;
  grid-template-columns: repeat(5, 1fr) !important;
  grid-auto-flow: row !important;
  gap: 6px !important;
  width: 100% !important;
}

.node-card {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 0px;
  padding: 6px 2px;
  background-color: #fafafa;
  border-radius: 10px;
  cursor: pointer;
  transition: all 0.2s;
  border: 1px solid transparent;
}

.node-card:hover {
  background-color: #e6f0f8;
  border-color: transparent;
  transform: translateY(-2px);
}

.icon-wrapper {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 22px;
  height: 22px;
  border-radius: 8px;
}

.icon-wrapper .el-icon {
  font-size: 16px;
}

.node-label {
  font-size: 11px;
  color: #333;
  text-align: center;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  max-width: 100%;
  margin-top: 3px;
}

.node-card:hover .node-label {
  color: #1890ff;
  font-weight: 500;
}

.icon-llm { color: #ffffff; background-color: #409eff; }
.icon-memory { color: #ffffff; background-color: #673ab7; }
.icon-result { color: #ffffff; background-color: #67c23a; }
.icon-progress { color: #ffffff; background-color: #eea236; }
.icon-agent { color: #ffffff; background-color: #f56c6c; }
.icon-branch { color: #ffffff; background-color: #00bcd4; }
.icon-intent { color: #ffffff; background-color: #9c27b0; }
.icon-break { color: #ffffff; background-color: #ff9800; }
.icon-continue { color: #ffffff; background-color: #4caf50; }
.icon-return { color: #ffffff; background-color: #9c27b0; }
.icon-exit { color: #ffffff; background-color: #c62828; }
.icon-process { color: #ffffff; background-color: #00bcd4; }
.icon-function { color: #ffffff; background-color: #667eea; }
.icon-call { color: #ffffff; background-color: #ffc107; }
.icon-assign { color: #ffffff; background-color: #ff9800; }
.icon-raise { color: #ffffff; background-color: #f56c6c; }
.icon-loop { color: #ffffff; background-color: #7b1fa2; }
.icon-subflow { color: #ffffff; background-color: #667eea; }
.icon-code { color: #ffffff; background-color: #a0522d; }
.icon-http { color: #ffffff; background-color: #409eff; }
.icon-mcp { color: #ffffff; background-color: #e6a23c; }
.icon-rpc { color: #ffffff; background-color: #1890ff; }
.icon-layout { color: #ffffff; background-color: #909399; }
.icon-comment { color: #ffffff; background-color: #409eff; }
</style>

<!-- 与 PropsPanel 同一套口径：滚动条常驻占位但透明，鼠标进面板才显形（非 scoped，
     因为滚动容器就是本组件的 .panel-content，这里保持一致以免两处规则漂移） -->
<style>
.node-panel * {
  scrollbar-width: thin;
  scrollbar-color: transparent transparent;
}
.node-panel *:hover {
  scrollbar-color: #c1c1c1 transparent;
}
.node-panel *::-webkit-scrollbar {
  width: 8px;
  height: 8px;
}
.node-panel *::-webkit-scrollbar-track {
  background: transparent;
}
.node-panel *::-webkit-scrollbar-thumb {
  background: transparent;
  border-radius: 4px;
}
.node-panel *:hover::-webkit-scrollbar-thumb {
  background: #c1c1c1;
}
</style>