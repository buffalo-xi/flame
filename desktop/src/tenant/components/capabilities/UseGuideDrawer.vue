<template>
  <!-- Endpoint "how to use" panel: content only.
       The shell (teleport, docked to the container's right edge, drag-to-widen, ESC / click-outside
       close) is src/help/HelpDrawer.vue, shared with the help topics — one copy of that geometry
       instead of two that drift apart.
       Two ways in:
         open()         generic explanation (toolbar entry), examples use <endpoint> placeholders
         open(row)      per-endpoint explanation, examples generated from the row's protocol / schema -->
  <HelpDrawer :visible="visible" :title="title" :anchor-el="anchorEl" @close="visible = false">
    <div class="ug">
          <!-- ===== 接入信息 ===== -->
          <section class="ug-sec">
            <div class="ug-title">{{ t('endpointGuide.accessTitle') }}</div>
            <p class="ug-p">
              {{ t('endpointGuide.accessBody', {
                mcp: `POST ${baseUrl}/service/mcp/<tenant>`,
                rpc: `POST ${baseUrl}/service/rpc/<tenant>`,
                auth: 'Authorization: Bearer <credential>'
              }) }}
            </p>
            <div v-if="row" class="ug-kv">
              <div class="ug-kv-row"><span>{{ t('endpointGuide.fieldName') }}</span><code>{{ row.name }}</code></div>
              <div class="ug-kv-row"><span>{{ t('endpointsTab.colProtocol') }}</span><span>{{ (row.protocol || '').toUpperCase() }}</span></div>
              <div class="ug-kv-row"><span>{{ t('endpointsTab.dTarget') }}</span><span>{{ targetKind }} · {{ row.target_name || row.target_id }}</span></div>
              <div class="ug-kv-row">
                <span>{{ t('endpointGuide.fieldStatus') }}</span>
                <span :class="row.exposed ? 'ug-ok' : 'ug-warn'">
                  {{ row.exposed ? t('endpointGuide.statusExposed') : t('endpointGuide.statusInternal') }}
                </span>
              </div>
              <div class="ug-kv-row">
                <span>{{ t('endpointGuide.fieldUrl') }}</span>
                <code>{{ callUrl }}</code>
                <el-button link type="primary" size="small" @click="copy(callUrl)">{{ t('common.copy') }}</el-button>
              </div>
            </div>
          </section>

          <!-- ===== MCP ===== -->
          <section v-if="showMCP" class="ug-sec">
            <div class="ug-title">{{ t('endpointGuide.mcpTitle') }}</div>
            <p class="ug-p">
              {{ t('endpointGuide.mcpBody', { schema: 'input_schema', list: 'tools/list' }) }}
            </p>
            <div class="ug-code">
              <el-button class="ug-copy" link type="primary" size="small" @click="copy(mcpConfig)">{{ t('common.copy') }}</el-button>
              <pre class="ug-pre">{{ mcpConfig }}</pre>
            </div>
            <el-alert type="warning" :closable="false" show-icon class="ug-alert">
              <template #title>{{ t('endpointGuide.mcpAlertTitle') }}</template>
              {{ t('endpointGuide.mcpAlertBody', { init: 'initialize', meta: '_meta.protocolVersion' }) }}
            </el-alert>
          </section>

          <!-- ===== RPC ===== -->
          <section v-if="showRPC" class="ug-sec">
            <div class="ug-title">{{ t('endpointGuide.rpcTitle') }}</div>
            <p class="ug-p">
              {{ t('endpointGuide.rpcBody', {
                name: 'params.name',
                args: 'params.arguments',
                call: 'tools/call',
                version: 'version',
                content: 'result.content[]',
                isError: 'isError',
                method: 'method=tools/list'
              }) }}
            </p>
            <div class="ug-code">
              <el-button class="ug-copy" link type="primary" size="small" @click="copy(rpcCurl)">{{ t('common.copy') }}</el-button>
              <pre class="ug-pre">{{ rpcCurl }}</pre>
            </div>
          </section>

          <!-- ===== 作为工具 ===== -->
          <section class="ug-sec">
            <div class="ug-title">{{ t('endpointGuide.toolTitle') }}</div>
            <p v-if="row && toolRef" class="ug-p">
              {{ t('endpointGuide.toolBodyRef', { ref: toolRef }) }}
            </p>
            <p v-else-if="row" class="ug-p ug-dim">
              {{ t('endpointGuide.toolBodyUnavailable', { reason: toolRefHint }) }}
            </p>
            <p v-else class="ug-p">
              {{ t('endpointGuide.toolBodyGeneric', { ref: 'local:rpc:<endpoint>' }) }}
            </p>
            <div v-if="row && toolRef" class="ug-actions">
              <el-button size="small" type="primary" plain @click="copy(toolRef)">{{ t('endpointGuide.copyRef') }}</el-button>
              <el-button v-if="row.target_type === 'flow'" size="small" @click="goFlow">{{ t('endpointsTab.goFlows') }}</el-button>
            </div>
          </section>

          <!-- ===== 注意 ===== -->
          <section class="ug-sec">
            <div class="ug-title">{{ t('endpointGuide.notesTitle') }}</div>
            <ul class="ug-list">
              <li>{{ t('endpointGuide.noteInternal') }}</li>
              <li>{{ t('endpointGuide.noteOnePerTarget') }}</li>
              <li>{{ t('endpointGuide.noteDecoupled') }}</li>
              <li>{{ t('endpointGuide.noteCredScope') }}</li>
            </ul>
          </section>
    </div>
  </HelpDrawer>
</template>

<script setup>
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { ElMessage } from 'element-plus'
import { getUser } from '../../utils/auth.js'
import { apiBase } from '../../config/base.js'
import HelpDrawer from '../../../help/HelpDrawer.vue'

defineProps({
  // Container the drawer docks to (usually the endpoints root); null falls back to the viewport.
  // Read in the template only — the shell owns the geometry.
  anchorEl: { type: Object, default: null }
})

const { t } = useI18n()

const visible = ref(false)
const row = ref(null)

const emit = defineEmits(['open'])

const schema = computed(() => getUser()?.schema || '<tenant>')

// 对外地址 = 前端访问后端的同一前缀（部署走反向代理时就是 /nexus）
const baseUrl = computed(() => {
  const b = String(apiBase || '').replace(/\/+$/, '')
  return /^https?:\/\//i.test(b) ? b : `${window.location.origin}${b}`
})

const title = computed(() => (row.value
  ? t('endpointGuide.titleNamed', { name: row.value.name })
  : t('endpointGuide.title')))

const protocol = computed(() => (row.value ? row.value.protocol : ''))
// 通用模式（无具体行）两种协议都讲；有行时只讲它自己的
const showMCP = computed(() => !row.value || protocol.value === 'mcp')
const showRPC = computed(() => !row.value || protocol.value === 'rpc')

const callUrl = computed(() => `${baseUrl.value}/service/${protocol.value || 'mcp'}/${schema.value}`)

const targetKind = computed(() => {
  const kind = row.value?.target_type
  return kind === 'agent' ? t('assetImport.kindAgent')
    : kind === 'flow' ? t('assetImport.kindWorkflow') : kind
})

// 进工具授权体系的只有「工作流的 rpc 端点」，与列表页 usableAsTool 同口径
const toolRef = computed(() => {
  if (!row.value) return ''
  return row.value.protocol === 'rpc' && row.value.target_type === 'flow' ? `local:rpc:${row.value.name}` : ''
})
const toolRefHint = computed(() =>
  row.value?.protocol !== 'rpc' ? t('endpointGuide.reasonMcp') : t('endpointGuide.reasonAgent')
)

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

const endpointName = computed(() => row.value?.name || '<endpoint>')

const mcpConfig = computed(() =>
  JSON.stringify(
    {
      mcpServers: {
        [`flame-${schema.value}`]: {
          type: 'http',
          url: `${baseUrl.value}/service/mcp/${schema.value}`,
          headers: { Authorization: 'Bearer <credential>' }
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
    `  -H 'Authorization: Bearer <credential>' \\`,
    `  -d '${rpcBody.value}'`
  ].join('\n')
})

// ===== 行为 =====

const copy = async (text, label = t('common.copied')) => {
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
      ElMessage.error(t('common.copyFailed'))
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
/* ===== Content =====
   The panel shell lives in src/help/HelpDrawer.vue; only what this panel renders itself is styled
   here, and scoped styles still reach it because these elements are ours, not v-html output. */
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
