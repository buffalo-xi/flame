<template>
  <!-- 工具详情面板：与智能体配置 / 插件 / 技能设置面板同款 ——
       右侧滑出、无遮罩（背后列表仍可见）、左缘可拖宽、点外面或 Esc 关闭。
       数据全部来自 catalog 的 ToolInfo（不额外请求接口）：
         ref / name（模型侧工具名）/ label / description / available / unavailable_reason / parameters
       本地工作流端点的 parameters 就是工作流入参（后端 toolSchema(ep.InputSchema)），
       所以这里把 schema 摊成字段表 —— 「详情」的意义就是让人看清要传什么。 -->
  <div v-if="visible" ref="panelRef" class="settings-panel" :style="{ width: panelWidth + 'px' }">
    <div class="panel-resizer" @mousedown="startResize"></div>
    <div class="panel-header">
      <div class="panel-title">
        {{ group ? t('toolDetail.titleConn') : t('toolDetail.title') }}
        <span class="ref-tag">{{ group ? group.name : (tool?.label || tool?.name || '') }}</span>
      </div>
      <span class="close-icon" @click="close">&#xd7;</span>
    </div>

    <div class="panel-body" :class="{ 'is-scrolling': scrolling }" @scroll.passive="onScroll">
      <div v-if="tool" class="td">
        <div class="td-head">
          <div class="td-name">{{ tool.label || tool.name }}</div>
          <span class="td-tag" :class="tool.available === false ? 'warn' : 'ok'">
            {{ availableText }}
          </span>
        </div>
        <div class="td-desc">{{ tool.description || t('customTools.noDescription') }}</div>

        <div class="td-section">
          <div class="td-title">{{ t('toolDetail.identity') }}</div>
          <div class="td-kv">
            <span class="td-k">{{ t('toolDetail.ref') }}</span>
            <code class="td-v">{{ tool.ref }}</code>
          </div>
          <div class="td-kv">
            <span class="td-k">{{ t('toolDetail.modelName') }}</span>
            <code class="td-v">{{ tool.name }}</code>
          </div>
          <div v-if="groupLabel" class="td-kv">
            <span class="td-k">{{ t('toolDetail.owner') }}</span>
            <span class="td-v td-plain">{{ groupLabel }}</span>
          </div>
        </div>

        <div v-if="reasonText" class="td-section">
          <div class="td-title">{{ t('toolDetail.whyUnavailable') }}</div>
          <div class="td-reason">{{ reasonText }}</div>
        </div>

        <div class="td-section">
          <div class="td-title">
            {{ params.length ? t('toolDetail.paramsCount', { n: params.length }) : t('toolDetail.params') }}
          </div>
          <template v-if="params.length">
            <div v-for="p in params" :key="p.name" class="td-param">
              <div class="td-param-head">
                <code class="td-param-name">{{ p.name }}</code>
                <span class="td-type">{{ p.type }}</span>
                <span v-if="p.required" class="td-req">{{ t('customTools.required') }}</span>
              </div>
              <div v-if="p.desc" class="td-param-desc">{{ p.desc }}</div>
            </div>
          </template>
          <div v-else class="td-empty">{{ t('toolDetail.noParams') }}</div>
        </div>

        <div v-if="hasRaw" class="td-section">
          <div class="td-title">{{ t('toolDetail.rawSchema') }}</div>
          <pre class="td-pre">{{ rawSchema }}</pre>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup>
import { computed, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { unavailableText } from '../../utils/unavailable'

const { t, te } = useI18n()

const visible = ref(false)
const tool = ref(null)
const groupLabel = ref('')

// 面板：宽度（可拖，320–760），元素引用用于「点在外面关闭」
const panelRef = ref(null)
const panelWidth = ref(520)

// 可用性文案按来源区分：本地端点用「对外开放 / 仅内部」，其余用「可用 / 不可用」
// 不可用原因：后端给了 unavailable_code 就本地化，没有（旧后端 / 未翻译的编码）才显示中文原文
const reasonText = computed(() => unavailableText(t, te, tool.value, ''))

const availableText = computed(() => {
  if (!tool.value) return ''
  const isLocal = String(tool.value.ref || '').startsWith('local:')
  if (isLocal) {
    return tool.value.available === false ? t('toolsTab.localInternal') : t('toolsTab.localExposed')
  }
  return tool.value.available === false ? t('toolDetail.unavailable') : t('toolDetail.available')
})

// 类型展示：enum 直接列可选值，数组带出元素类型
const typeOf = (def) => {
  if (!def || typeof def !== 'object') return 'any'
  if (Array.isArray(def.enum) && def.enum.length) return def.enum.map((v) => JSON.stringify(v)).join(' / ')
  if (Array.isArray(def.anyOf)) return def.anyOf.map(typeOf).join(' | ')
  let t = def.type || 'any'
  if (t === 'array') t = `array<${typeOf(def.items)}>`
  return t
}

// 参数列表：JSON Schema 的 properties 顺序即展示顺序（后端保留了工作流声明的顺序）
const params = computed(() => {
  const schema = tool.value?.parameters
  const props = schema && typeof schema === 'object' ? schema.properties : null
  if (!props || typeof props !== 'object') return []
  const required = Array.isArray(schema.required) ? schema.required : []
  return Object.entries(props).map(([name, def]) => ({
    name,
    type: typeOf(def),
    desc: (def && (def.description || def.title)) || '',
    required: required.includes(name)
  }))
})

const hasRaw = computed(() => {
  const schema = tool.value?.parameters
  return !!schema && typeof schema === 'object' && Object.keys(schema).length > 0
})
const rawSchema = computed(() => JSON.stringify(tool.value?.parameters || {}, null, 2))

// 左缘拖拽调宽：与其它设置面板同款手感（越往左拖越宽）
const startResize = (e) => {
  const startX = e.clientX
  const startW = panelWidth.value
  const onMove = (ev) => {
    panelWidth.value = Math.max(320, Math.min(startW + (startX - ev.clientX), 760))
  }
  const onUp = () => {
    document.removeEventListener('mousemove', onMove)
    document.removeEventListener('mouseup', onUp)
  }
  document.addEventListener('mousemove', onMove)
  document.addEventListener('mouseup', onUp)
}

const close = () => { visible.value = false }

// 滚动条「不悬浮不显示」：默认全透明，只有鼠标压在滚动条上（thumb:hover）
// 或正在滚动（.is-scrolling，700ms 后收回）才显形。
// 不用 :hover 整块面板来控制 —— 那样只要鼠标在面板里就一直亮着，等于没藏。
const scrolling = ref(false)
let scrollTimer = null
const onScroll = () => {
  scrolling.value = true
  clearTimeout(scrollTimer)
  scrollTimer = setTimeout(() => { scrolling.value = false }, 700)
}
onUnmounted(() => clearTimeout(scrollTimer))

// 点面板外**任何地方**都关。只放过两种：
//  - 面板自己；
//  - 页面上另一行的「详情」按钮（那是切换工具，不该先关再开，会闪一下）。
// Element 的 popper / overlay 也放过 —— 它们挂在 body 上，不算「外面」。
const onDocMouseDown = (e) => {
  if (!visible.value) return
  const t = e.target
  if (panelRef.value && panelRef.value.contains(t)) return
  if (t.closest && t.closest('.tt-detail-btn')) return
  if (t.closest && (t.closest('.el-popper') || t.closest('.el-overlay'))) return
  close()
}

const onKeyDown = (e) => {
  if (e.key === 'Escape') close()
}

const stopListeners = () => {
  document.removeEventListener('mousedown', onDocMouseDown)
  document.removeEventListener('keydown', onKeyDown)
}

// 监听跟着显隐走：开着才挂，关掉立刻摘 —— 避免面板没了还在后台吞点击，
// 也避免重复 open 时叠加多份监听。
watch(visible, (v) => {
  stopListeners()
  if (!v) return
  document.addEventListener('mousedown', onDocMouseDown)
  document.addEventListener('keydown', onKeyDown)
})
onUnmounted(stopListeners)

// 父组件调用：open(工具项, 归属说明)
const open = (item, label = '') => {
  tool.value = item || null
  groupLabel.value = label
  visible.value = true
}

defineExpose({ open })
</script>

<style scoped>
/* ============ 面板：对齐智能体 / 插件 / 技能设置面板 ============
   容器（ToolsTab 的 .tt-wrap）是 position: relative，面板贴它的右缘、占满高度。 */
.settings-panel {
  position: absolute;
  z-index: 100;
  right: 0;
  top: 0;
  bottom: 0;
  background: #fff;
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
  gap: 12px;
  border-bottom: 1px solid #f1f1f1;
}

.panel-title {
  display: flex;
  align-items: center;
  gap: 6px;
  flex: 1;
  min-width: 0;
  font-size: 14px;
  font-weight: 600;
  color: #333;
}

.close-icon {
  cursor: pointer;
  padding: 4px;
  border-radius: 4px;
  font-size: 18px;
  line-height: 1;
}

.close-icon:hover {
  background: rgba(0, 0, 0, 0.1);
}

.panel-body {
  flex: 1;
  overflow-y: auto;
  padding: 16px;
  min-height: 0;
  /* 槽位常驻（条宽固定 6px）：滚动条的显隐不会把内容挤动 */
  scrollbar-gutter: stable;
  /* 平时透明 = 看不见，鼠标进面板才上色（与插件 / 技能 / 智能体设置面板同一套） */
  scrollbar-width: thin;
  scrollbar-color: transparent transparent;
}

.panel-body::-webkit-scrollbar {
  width: 6px;
  height: 6px;
}

.panel-body::-webkit-scrollbar-track {
  background: transparent;
}

.panel-body::-webkit-scrollbar-thumb {
  background: transparent;
  border-radius: 3px;
}

/* 鼠标移进内容区就显形（移出即透明）；压到滚动条上再深一档 */
.panel-body:hover {
  scrollbar-color: rgba(144, 147, 153, 0.4) transparent;
}

.panel-body:hover::-webkit-scrollbar-thumb {
  background: rgba(144, 147, 153, 0.4);
}

.panel-body::-webkit-scrollbar-thumb:hover {
  background: rgba(144, 147, 153, 0.6);
}

.panel-body.is-scrolling {
  scrollbar-color: rgba(144, 147, 153, 0.4) transparent;
}

.panel-body.is-scrolling::-webkit-scrollbar-thumb {
  background: rgba(144, 147, 153, 0.4);
}

/* ============ 内容 ============ */
.td {
  font-size: 13px;
  color: #303133;
}

.td-head {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 6px;
}

.td-name {
  font-size: 15px;
  font-weight: 600;
}

.td-tag {
  font-size: 11px;
  padding: 1px 6px;
  border-radius: 3px;
  background: #f4f4f5;
  color: #909399;
}

.td-tag.ok {
  background: #f0f9eb;
  color: #67c23a;
}

.td-tag.warn {
  background: #fef0f0;
  color: #f56c6c;
}

.td-desc {
  color: #606266;
  line-height: 1.6;
  margin-bottom: 14px;
  white-space: pre-wrap;
}

.td-section {
  margin-bottom: 16px;
}

.td-title {
  font-weight: 600;
  color: #303133;
  margin-bottom: 6px;
}

.td-kv {
  display: flex;
  gap: 10px;
  padding: 3px 0;
}

.td-k {
  width: 96px;
  flex-shrink: 0;
  color: #909399;
}

.td-v {
  font-family: ui-monospace, Menlo, Consolas, monospace;
  font-size: 12px;
  color: #303133;
  word-break: break-all;
}

.td-plain {
  font-family: inherit;
  font-size: 13px;
}

.td-reason {
  color: #e6a23c;
  background: #fdf6ec;
  border-radius: 4px;
  padding: 8px 10px;
  line-height: 1.6;
}

.td-param {
  border: 1px solid #ebeef5;
  border-radius: 4px;
  padding: 8px 10px;
  margin-bottom: 8px;
}

.td-param-head {
  display: flex;
  align-items: center;
  gap: 8px;
}

.td-param-name {
  font-family: ui-monospace, Menlo, Consolas, monospace;
  font-size: 12px;
  color: #303133;
}

.td-type {
  font-size: 11px;
  color: #409eff;
  background: #ecf5ff;
  padding: 1px 6px;
  border-radius: 3px;
}

.td-req {
  font-size: 11px;
  color: #f56c6c;
  background: #fef0f0;
  padding: 1px 6px;
  border-radius: 3px;
}

.td-param-desc {
  color: #606266;
  margin-top: 5px;
  line-height: 1.55;
}

.td-empty {
  color: #909399;
  padding: 6px 0;
}

.td-pre {
  margin: 0;
  padding: 10px;
  background: #f7f8fa;
  border-radius: 4px;
  font-family: ui-monospace, Menlo, Consolas, monospace;
  font-size: 12px;
  line-height: 1.5;
  color: #303133;
  /* 按内容实际高度全部展开：不加 max-height、不出内嵌滚动条 ——
     要看的就是整段 schema，截断后再滚一遍纯属多余。太长时由面板主体滚动。 */
  white-space: pre-wrap;
  word-break: break-word;
  overflow: visible;
}
</style>
