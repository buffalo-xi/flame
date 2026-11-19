<template>
  <!-- 安装前预检的唯一渲染入口：市场安装与上传导入共用，保证两边看到的结论、措辞、分组一致。
       只展示 inspect 接口的回包，不做请求（两个入口各自的预检接口不同，但回包同形）。 -->
  <div class="dep-check">
    <div v-if="inspect.error" class="dep-error">{{ inspect.error }}</div>

    <template v-else>
      <div v-if="inspect.existing" class="dep-existing">
        {{ inspect.existing.can_overwrite
          ? t('assetDep.existingOverwrite', { verb: verbText, kind: kindText, name: inspect.existing.name })
          : t('assetDep.existingRename', { verb: verbText, kind: kindText, name: inspect.existing.name }) }}
      </div>

      <!-- 内容指纹：判断「这份包和上次装的是不是同一份」，老包没有则整行不显示 -->
      <div v-if="inspect.digest" class="dep-digest">
        {{ t('assetDep.digest', { digest: inspect.digest.slice(0, 8) }) }}<template
          v-if="inspect.spec">{{ t('assetDep.spec', { spec: inspect.spec }) }}</template>
      </div>

      <template v-if="groups.length">
        <div class="dep-title">
          {{ t('assetDep.depsCount', { n: inspect.deps.length }) }}<template
            v-if="inspect.missing">{{ t('assetDep.missingCount', { n: inspect.missing }) }}</template>
        </div>
        <div v-for="g in groups" :key="g.key" class="dep-group">
          <div class="dep-group-title">{{ t('assetDep.groupCount', { label: g.label, n: g.items.length }) }}</div>
          <div v-for="d in g.items" :key="d.kind + ':' + d.ref + ':' + (d.via || '')"
               class="dep-item" :class="'dep-' + d.state">
            <div class="dep-line">
              <span class="dep-ref">{{ d.ref }}</span>
              <span class="dep-state">{{ stateLabel(d.state) }}</span>
              <span v-if="d.via" class="dep-via">{{ t('assetDep.via', { name: viaLabel(d.via) }) }}</span>
            </div>
            <div v-if="d.items && d.items.length" class="dep-members">
              {{ d.items.join(t('assetDep.itemJoiner')) }}<template
                v-if="d.total > d.items.length">{{ t('assetDep.itemsMore', { n: d.total }) }}</template>
            </div>
            <div v-if="d.detail" class="dep-detail">{{ d.detail }}</div>
          </div>
        </div>
      </template>
      <div v-else class="dep-none">{{ t('assetDep.noDeps') }}</div>

      <div v-if="bundledText" class="dep-bundled">{{ t('assetDep.bundled', { text: bundledText }) }}</div>
      <div v-for="n in inspect.notes || []" :key="n" class="dep-note">{{ n }}</div>
    </template>
  </div>
</template>

<script setup>
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'

const props = defineProps({
  // inspect 接口的回包（与 /import/inspect、/install/inspect 同形）；错误形态是 { error }
  inspect: { type: Object, required: true },
  // 目标类型的名字（智能体 / 技能 …）：调用方已按领域取好，留空则用兜底词
  kindLabel: { type: String, default: '' },
  // 动作：import / install（同一句话在「导入」与「市场安装」两处才读得通）
  verb: { type: String, default: 'install' }
})

const { t } = useI18n()

// 依赖条目的标签：kind 区分大类，scope 细分工具来源（mcp / rpc / local …）
// global_var：包里只带变量名，值要目标租户自己在「设置 → 全局变量」填
const DEP_KIND_KEYS = {
  skill: 'depKindSkill',
  tool: 'depKindTool',
  agent: 'depKindAgent',
  capability: 'depKindCapability',
  global_var: 'depKindGlobalVar'
}
const DEP_SCOPE_KEYS = {
  custom: 'toolKindCustom',
  mcp: 'toolKindMcp',
  rpc: 'toolKindRpc',
  local: 'toolKindLocal',
  builtin: 'toolKindBuiltin',
  plugin: 'toolKindPlugin',
  group: 'toolKindGroup',
  preset: 'toolKindPreset'
}
const DEP_STATE_KEYS = {
  ok: 'stateOk',
  bundled: 'stateBundled',
  disabled: 'stateDisabled',
  deprecated: 'stateDeprecated',
  missing: 'stateMissing'
}

// 动作词只传令牌：句子里要放在不同位置（中英语序不同），在这里翻成当前语言
const verbText = computed(() =>
  props.verb === 'import' ? t('assetAction.import')
    : props.verb === 'install' ? t('assetAction.install')
      : props.verb
)
const kindText = computed(() => props.kindLabel || t('assetDep.defaultKind'))

const stateLabel = (state) => (DEP_STATE_KEYS[state] ? t(`assetDep.${DEP_STATE_KEYS[state]}`) : state)

// via 形如 skill:xxx —— 只翻前缀，名字原样保留
const viaLabel = (via) => (via.startsWith('skill:') ? t('assetDep.skillPrefix', { name: via.slice(6) }) : via)

// 按类型归并（技能 / 自定义工具 / MCP / RPC / 本地端点 / 内置 / 插件 / 组 / 智能体 / 能力），
// 顺序固定 —— 依赖清单得能一眼扫出「哪一类缺了几项」
const groups = computed(() => {
  const list = props.inspect?.deps || []
  const order = ['skill', 'custom', 'mcp', 'rpc', 'local', 'builtin', 'plugin', 'group', 'preset', 'agent', 'global_var', 'capability']
  const labelOf = (key) => {
    if (DEP_SCOPE_KEYS[key]) return t(`assetDep.${DEP_SCOPE_KEYS[key]}`)
    if (DEP_KIND_KEYS[key]) return t(`assetDep.${DEP_KIND_KEYS[key]}`)
    return key
  }
  const byKey = new Map()
  for (const d of list) {
    const key = d.kind === 'tool' ? d.scope || 'tool' : d.kind
    if (!byKey.has(key)) byKey.set(key, { key, label: labelOf(key), items: [] })
    byKey.get(key).items.push(d)
  }
  return [...byKey.values()].sort((a, b) => order.indexOf(a.key) - order.indexOf(b.key))
})

const bundledText = computed(() => {
  const b = props.inspect?.bundled
  if (!b) return ''
  const joiner = t('assetDep.itemJoiner')
  const parts = []
  if (b.custom_tools?.length) parts.push(t('assetDep.bundledCustomTools', { names: b.custom_tools.join(joiner) }))
  if (b.private_skills?.length) parts.push(t('assetDep.bundledPrivateSkills', { names: b.private_skills.join(joiner) }))
  return parts.join(t('assetDep.bundledJoiner'))
})
</script>

<style scoped>
.dep-check {
  display: flex;
  flex-direction: column;
  gap: 8px;
  max-height: 260px;
  overflow-y: auto;
  padding: 8px 10px;
  background: #fafafa;
  border: 1px solid #ebeef5;
  border-radius: 4px;
}
.dep-error {
  font-size: 12px;
  line-height: 1.6;
  color: #f56c6c;
}
.dep-existing {
  font-size: 12px;
  line-height: 1.6;
  color: #b88230;
}
.dep-title {
  font-size: 13px;
  font-weight: 600;
  color: #333;
}
.dep-digest {
  font-size: 11px;
  color: #a8abb2;
  font-family: Menlo, Consolas, monospace;
}
.dep-group-title {
  font-size: 12px;
  color: #606266;
  margin: 4px 0 2px;
}
.dep-item {
  padding: 4px 6px;
  border-left: 3px solid #dcdfe6;
  background: #fff;
  border-radius: 3px;
}
.dep-item.dep-ok {
  border-left-color: #67c23a;
}
.dep-item.dep-bundled {
  border-left-color: #409eff;
}
.dep-item.dep-disabled,
.dep-item.dep-deprecated {
  border-left-color: #e6a23c;
}
.dep-item.dep-missing {
  border-left-color: #f56c6c;
}
.dep-line {
  display: flex;
  align-items: baseline;
  gap: 8px;
  flex-wrap: wrap;
}
.dep-ref {
  font-size: 12px;
  font-family: Menlo, Consolas, monospace;
  color: #303133;
  word-break: break-all;
}
.dep-state {
  font-size: 11px;
  color: #909399;
  flex: none;
}
.dep-item.dep-missing .dep-state {
  color: #f56c6c;
}
.dep-via {
  font-size: 11px;
  color: #909399;
}
.dep-members {
  font-size: 11px;
  color: #606266;
  margin-top: 2px;
  word-break: break-all;
}
.dep-detail {
  font-size: 11px;
  line-height: 1.6;
  color: #909399;
  margin-top: 2px;
}
.dep-none {
  font-size: 12px;
  color: #909399;
}
.dep-bundled {
  font-size: 12px;
  line-height: 1.6;
  color: #409eff;
}
.dep-note {
  font-size: 11px;
  line-height: 1.6;
  color: #a8abb2;
}
</style>
