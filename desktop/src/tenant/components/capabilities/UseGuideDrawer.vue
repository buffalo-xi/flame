<template>
  <!-- 端点「怎么用」说明面板。
       外壳与「智能体编辑 → 设置」面板同款：teleport 到 body + fixed 贴容器右侧、
       无遮罩、左侧拖拽条可调宽、点击面板外关闭（细节见 AgentSettingsPanel.vue）。
       两种打开方式：
         open()        通用说明（工具条入口），示例里用 <端点名> 占位
         open(row)     某个端点的专属说明，示例按该行的协议 / 入参 schema 生成 -->
  <teleport to="body">
    <div v-if="visible" ref="panelRef" class="ug-panel" :style="panelStyle">
      <div class="panel-resizer" @mousedown="startResize"></div>
      <div class="panel-header">
        <span class="panel-title">{{ title }}</span>
        <span class="close-icon" @click="visible = false">&#xd7;</span>
      </div>

      <div class="panel-body">
        <div class="ug">
          <!-- ===== 接入信息 ===== -->
          <section class="ug-sec">
            <div class="ug-title">接入信息</div>
            <p class="ug-p">
              一个端点对外只有一个地址，按协议落在两段路径上：<code>POST {{ baseUrl }}/service/mcp/&lt;租户&gt;</code>
              与 <code>POST {{ baseUrl }}/service/rpc/&lt;租户&gt;</code>。
              请求都要带 <code>Authorization: Bearer &lt;服务凭证&gt;</code>；凭证能调哪些端点由「设置 → 服务凭证」里的绑定决定，
              未被授权的端点对外表现为「不存在」。
            </p>
            <div v-if="row" class="ug-kv">
              <div class="ug-kv-row"><span>端点名</span><code>{{ row.name }}</code></div>
              <div class="ug-kv-row"><span>协议</span><span>{{ (row.protocol || '').toUpperCase() }}</span></div>
              <div class="ug-kv-row"><span>目标</span><span>{{ targetKind }} · {{ row.target_name || row.target_id }}</span></div>
              <div class="ug-kv-row">
                <span>状态</span>
                <span :class="row.exposed ? 'ug-ok' : 'ug-warn'">
                  {{ row.exposed ? '对外开放（可被外部调用）' : '仅内部（外部调用会被拒绝，先在列表里打开开关）' }}
                </span>
              </div>
              <div class="ug-kv-row">
                <span>调用地址</span>
                <code>{{ callUrl }}</code>
                <el-button link type="primary" size="small" @click="copy(callUrl)">复制</el-button>
              </div>
            </div>
          </section>

          <!-- ===== MCP ===== -->
          <section v-if="showMCP" class="ug-sec">
            <div class="ug-title">MCP 客户端接入</div>
            <p class="ug-p">
              每个对外开放的 MCP 端点就是一个标准 MCP 工具：<strong>工具名 = 端点名</strong>，
              工具入参 = 端点的 <code>input_schema</code>。客户端连上后 <code>tools/list</code> 只会列出当前凭证有权访问的端点。
            </p>
            <div class="ug-code">
              <el-button class="ug-copy" link type="primary" size="small" @click="copy(mcpConfig)">复制</el-button>
              <pre class="ug-pre">{{ mcpConfig }}</pre>
            </div>
            <el-alert type="warning" :closable="false" show-icon class="ug-alert">
              <template #title>只支持 MCP 协议 2026-07-28</template>
              该版本无 <code>initialize</code> 握手，每个请求自带 <code>_meta.protocolVersion</code>。
              默认使用旧协议（如 2025-11-25）的客户端会在传输层被直接拒绝 —— 需要新版客户端或前置协议适配代理。
            </el-alert>
          </section>

          <!-- ===== RPC ===== -->
          <section v-if="showRPC" class="ug-sec">
            <div class="ug-title">RPC 直连调用</div>
            <p class="ug-p">
              JSON-RPC 2.0，端点名放在 <code>params.name</code>、业务入参放在 <code>params.arguments</code>
              （信封形状与 MCP 的 <code>tools/call</code> 一致，<code>version</code> 缺省按 1 处理）。
              返回 <code>result.content[]</code> 文本段与 <code>isError</code>；<code>method=tools/list</code> 可取端点清单。
            </p>
            <div class="ug-code">
              <el-button class="ug-copy" link type="primary" size="small" @click="copy(rpcCurl)">复制</el-button>
              <pre class="ug-pre">{{ rpcCurl }}</pre>
            </div>
          </section>

          <!-- ===== 作为工具 ===== -->
          <section class="ug-sec">
            <div class="ug-title">给智能体当工具用</div>
            <p v-if="row && toolRef" class="ug-p">
              工作流的 RPC 端点会以 <code>{{ toolRef }}</code> 出现在工具授权目录里，授权给智能体后即可被模型直接调用。
              入口：智能体编辑页 → 工具授权（技能则在「工具依赖」里勾选）。
            </p>
            <p v-else-if="row" class="ug-p ug-dim">
              只有「工作流的 RPC 端点」会进入工具授权体系（{{ toolRefHint }}）。
              当前端点不参与授权，只能按上面的地址被外部系统调用。
            </p>
            <p v-else class="ug-p">
              只有「工作流的 RPC 端点」会以 <code>local:rpc:&lt;端点名&gt;</code> 进入工具授权体系，可在智能体编辑页 → 工具授权里勾选。
              MCP 端点与智能体端点只供外部系统调用，不出现在工具目录里。
            </p>
            <div v-if="row && toolRef" class="ug-actions">
              <el-button size="small" type="primary" plain @click="copy(toolRef)">复制 ref</el-button>
              <el-button v-if="row.target_type === 'flow'" size="small" @click="goFlow">去工作流</el-button>
            </div>
          </section>

          <!-- ===== 注意 ===== -->
          <section class="ug-sec">
            <div class="ug-title">注意</div>
            <ul class="ug-list">
              <li>仅内部的端点对外<strong>不可调用</strong>：MCP 不会注册这个工具，RPC 直接返回「服务端点未对外开放」。列表里的开关就是对外开放 / 收回。</li>
              <li>同一个目标、同一协议只能有一个端点；改端点入参 schema 会立刻影响调用方（工具契约变了）。</li>
              <li>端点名与目标实体名解耦：工作流改名不会改端点名，反之亦然。</li>
              <li>凭证范围按「目标实体」判定：同一个智能体下所有协议的端点共用一份授权。</li>
            </ul>
          </section>
        </div>
      </div>
    </div>
  </teleport>
</template>

<script setup>
import { ref, computed, watch, nextTick, onUnmounted } from 'vue'
import { ElMessage } from 'element-plus'
import { getUser } from '../../utils/auth.js'
import { apiBase } from '../../config/base.js'
import { useEscClose } from '../../composables/useEscClose'

const props = defineProps({
  // 面板贴哪个容器的右侧（通常是端点页根元素）；null 时退化为整个视口
  anchorEl: { type: Object, default: null }
})

const visible = ref(false)
const row = ref(null)

const emit = defineEmits(['open'])

useEscClose(visible, () => { visible.value = false })

const schema = computed(() => getUser()?.schema || '<租户>')

// 对外地址 = 前端访问后端的同一前缀（部署走反向代理时就是 /nexus）
const baseUrl = computed(() => {
  const b = String(apiBase || '').replace(/\/+$/, '')
  return /^https?:\/\//i.test(b) ? b : `${window.location.origin}${b}`
})

const title = computed(() => (row.value ? `端点用法 · ${row.value.name}` : '端点用法'))

const protocol = computed(() => (row.value ? row.value.protocol : ''))
// 通用模式（无具体行）两种协议都讲；有行时只讲它自己的
const showMCP = computed(() => !row.value || protocol.value === 'mcp')
const showRPC = computed(() => !row.value || protocol.value === 'rpc')

const callUrl = computed(() => `${baseUrl.value}/service/${protocol.value || 'mcp'}/${schema.value}`)

const targetKind = computed(() => {
  const t = row.value?.target_type
  return t === 'agent' ? '智能体' : t === 'flow' ? '工作流' : t
})

// 进工具授权体系的只有「工作流的 rpc 端点」，与列表页 usableAsTool 同口径
const toolRef = computed(() => {
  if (!row.value) return ''
  return row.value.protocol === 'rpc' && row.value.target_type === 'flow' ? `local:rpc:${row.value.name}` : ''
})
const toolRefHint = computed(() =>
  row.value?.protocol !== 'rpc' ? 'MCP 端点只供外部系统调用' : '智能体端点不进工具目录，agent 之间请走 call_agent'
)

// ===== 面板几何（对齐 AgentSettingsPanel 的 settingsRect / startSettingsResize） =====
const panelRef = ref(null)
const panelWidth = ref(460)
const panelRect = ref({ top: 0, left: 0, width: 460, height: 600 })
const panelStyle = computed(() => ({
  top: panelRect.value.top + 'px',
  left: panelRect.value.left + 'px',
  width: panelRect.value.width + 'px',
  height: panelRect.value.height + 'px'
}))

const updateRect = () => {
  const el = props.anchorEl
  const r = el
    ? el.getBoundingClientRect()
    : { top: 0, right: window.innerWidth, height: window.innerHeight }
  // 容器比面板窄时贴窗口左边缘，保证整块可见（已脱离容器裁剪）
  const width = Math.min(panelWidth.value, window.innerWidth)
  panelRect.value = {
    top: r.top,
    left: Math.max(0, r.right - width),
    width,
    height: r.height || window.innerHeight
  }
}

// 拖拽改宽：向左拖变宽（380~760）
const startResize = (e) => {
  const startX = e.clientX
  const startWidth = panelWidth.value
  const onMouseMove = (ev) => {
    const deltaX = startX - ev.clientX
    panelWidth.value = Math.max(380, Math.min(startWidth + deltaX, 760))
    updateRect()
  }
  const onMouseUp = () => {
    document.removeEventListener('mousemove', onMouseMove)
    document.removeEventListener('mouseup', onMouseUp)
  }
  document.addEventListener('mousemove', onMouseMove)
  document.addEventListener('mouseup', onMouseUp)
}

// 点击面板外部关闭（下拉/提示等 teleport 到 body 的浮层不算外部）
const onOutsideClick = (e) => {
  const target = e.target
  if (target.closest && (target.closest('.el-overlay') || target.closest('.el-popper'))) return
  if (panelRef.value && !panelRef.value.contains(target)) visible.value = false
}

watch(visible, (v) => {
  if (v) {
    // nextTick 延迟绑定，避免打开按钮的 mousedown 立即触发关闭
    nextTick(() => {
      document.addEventListener('mousedown', onOutsideClick)
      updateRect()
    })
    window.addEventListener('resize', updateRect)
  } else {
    document.removeEventListener('mousedown', onOutsideClick)
    window.removeEventListener('resize', updateRect)
  }
})

onUnmounted(() => {
  document.removeEventListener('mousedown', onOutsideClick)
  window.removeEventListener('resize', updateRect)
})

// ===== 示例内容生成 =====

// input_schema 可能是对象、可能是已序列化字符串
const parseSchema = (v) => {
  if (!v) return {}
  if (typeof v === 'object') return v
  try {
    return JSON.parse(v) || {}
  } catch {
    return {}
  }
}

// 按 JSON Schema 造一份「填好类型」的入参骨架，比空对象更能提示要传什么
const sampleValue = (p) => {
  if (!p || typeof p !== 'object') return null
  if (Array.isArray(p.enum) && p.enum.length) return p.enum[0]
  switch (p.type) {
    case 'string':
      return ''
    case 'integer':
    case 'number':
      return 0
    case 'boolean':
      return false
    case 'array':
      return []
    case 'object':
      return {}
    default:
      return null
  }
}

const sampleArgs = computed(() => {
  const s = parseSchema(row.value?.input_schema)
  const props2 = s.properties && typeof s.properties === 'object' ? s.properties : {}
  const out = {}
  for (const [k, v] of Object.entries(props2)) out[k] = sampleValue(v)
  return out
})

const endpointName = computed(() => row.value?.name || '<端点名>')

const mcpConfig = computed(() =>
  JSON.stringify(
    {
      mcpServers: {
        [`flame-${schema.value}`]: {
          type: 'http',
          url: `${baseUrl.value}/service/mcp/${schema.value}`,
          headers: { Authorization: 'Bearer <服务凭证>' }
        }
      }
    },
    null,
    2
  )
)

const rpcBody = computed(() =>
  JSON.stringify(
    {
      jsonrpc: '2.0',
      version: 1,
      id: 1,
      method: 'tools/call',
      params: { name: endpointName.value, arguments: sampleArgs.value }
    },
    null,
    2
  )
)

const rpcCurl = computed(() => {
  // bash 换行续行；JSON 里没有单引号，单引号包裹是安全的
  return [
    `curl -X POST '${baseUrl.value}/service/rpc/${schema.value}' \\`,
    `  -H 'Content-Type: application/json' \\`,
    `  -H 'Authorization: Bearer <服务凭证>' \\`,
    `  -d '${rpcBody.value}'`
  ].join('\n')
})

// ===== 行为 =====

const copy = async (text, label = '已复制') => {
  try {
    await navigator.clipboard.writeText(text)
    ElMessage.success({ message: label, duration: 800 })
  } catch {
    // 非安全上下文（http 访问 IP）下 clipboard 不可用，退回 execCommand
    try {
      const ta = document.createElement('textarea')
      ta.value = text
      ta.style.position = 'fixed'
      ta.style.opacity = '0'
      document.body.appendChild(ta)
      ta.select()
      document.execCommand('copy')
      document.body.removeChild(ta)
      ElMessage.success({ message: label, duration: 800 })
    } catch {
      ElMessage.error('复制失败，请手动选中文本')
    }
  }
}

const goFlow = () => {
  if (!row.value?.target_id) return
  visible.value = false
  emit('open', { nav: 'wf-instances', id: row.value.target_id })
}

// open() 通用说明；open(row) 针对某个端点
const open = (r = null) => {
  row.value = r
  visible.value = true
}

defineExpose({ open })
</script>

<style scoped>
/* ===== 外壳：与智能体设置面板同一套 ===== */
.ug-panel {
  /* teleport 到 body + fixed：脱离 .endpoints 的 overflow:hidden，位置按端点页容器 rect 计算 */
  position: fixed;
  z-index: 3200;
  background: #ffffff;
  border-radius: 6px 0 0 6px;
  box-shadow: -4px 0 20px rgba(0, 0, 0, 0.2);
  display: flex;
  flex-direction: column;
  overflow: hidden;
}

.panel-resizer {
  position: absolute;
  left: 0;
  top: 0;
  bottom: 0;
  width: 6px;
  cursor: col-resize;
  background: transparent;
  z-index: 10;
}

.panel-resizer:hover {
  background: #e0e0e0;
}

.panel-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 6px 16px;
  color: black;
  gap: 12px;
}

.panel-title {
  font-weight: 600;
  font-size: 14px;
}

.close-icon {
  cursor: pointer;
  padding: 4px;
  border-radius: 4px;
  font-size: 18px;
}

.close-icon:hover {
  background: rgba(0, 0, 0, 0.1);
}

.panel-body {
  flex: 1;
  overflow-y: auto;
  padding: 16px;
  min-height: 0;
  /* 预留滚动条位置：内容高度变化时不会左右晃动 */
  scrollbar-gutter: stable;
}

/* ===== 内容 ===== */
.ug {
  display: flex;
  flex-direction: column;
  gap: 18px;
}

.ug-sec {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.ug-title {
  font-size: 13px;
  font-weight: 600;
  color: #303133;
  padding-left: 8px;
  border-left: 3px solid #409eff;
  line-height: 1.2;
}

.ug-p {
  margin: 0;
  font-size: 13px;
  line-height: 1.7;
  color: #606266;
}

.ug-p code,
.ug-kv code,
.ug-list code,
.ug-alert code {
  font-family: ui-monospace, Menlo, Consolas, monospace;
  font-size: 12px;
  padding: 1px 5px;
  border-radius: 3px;
  background: #f4f4f5;
  color: #c7254e;
  word-break: break-all;
}

.ug-dim {
  color: #909399;
}

.ug-kv {
  display: flex;
  flex-direction: column;
  gap: 6px;
  padding: 10px 12px;
  background: #fafafa;
  border: 1px solid #ebeef5;
  border-radius: 4px;
}

.ug-kv-row {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 13px;
  color: #303133;
}

.ug-kv-row > span:first-child {
  flex: 0 0 68px;
  color: #909399;
}

.ug-ok {
  color: #67c23a;
}

.ug-warn {
  color: #e6a23c;
}

.ug-code {
  position: relative;
}

.ug-copy {
  position: absolute;
  top: 4px;
  right: 8px;
  z-index: 1;
}

.ug-pre {
  margin: 0;
  padding: 10px 12px;
  background: #f6f8fa;
  border: 1px solid #ebeef5;
  border-radius: 4px;
  font-family: ui-monospace, Menlo, Consolas, monospace;
  font-size: 12px;
  line-height: 1.6;
  color: #303133;
  white-space: pre-wrap;
  word-break: break-all;
  max-height: 260px;
  overflow: auto;
}

.ug-alert {
  margin-top: 4px;
}

.ug-list {
  margin: 0;
  padding-left: 18px;
  font-size: 13px;
  line-height: 1.8;
  color: #606266;
}

.ug-actions {
  display: flex;
  gap: 8px;
  margin-top: 2px;
}
</style>
