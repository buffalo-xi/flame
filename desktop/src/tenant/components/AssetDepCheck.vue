<template>
  <!-- 安装前预检的唯一渲染入口：市场安装与上传导入共用，保证两边看到的结论、措辞、分组一致。
       只展示 inspect 接口的回包，不做请求（两个入口各自的预检接口不同，但回包同形）。 -->
  <div class="dep-check">
    <div v-if="inspect.error" class="dep-error">{{ inspect.error }}</div>

    <template v-else>
      <div v-if="inspect.existing" class="dep-existing">
        本租户已有同名{{ kindLabel }}「{{ inspect.existing.name }}」<template
          v-if="inspect.existing.can_overwrite">，{{ verb }}时会问你一句，确认即覆盖</template><template
          v-else>，{{ verb }}时请改个名字</template>
      </div>

      <!-- 内容指纹：判断「这份包和上次装的是不是同一份」，老包没有则整行不显示 -->
      <div v-if="inspect.digest" class="dep-digest">
        内容指纹 {{ inspect.digest.slice(0, 8) }}<template v-if="inspect.spec">（清单 spec {{ inspect.spec }}）</template>
      </div>

      <template v-if="groups.length">
        <div class="dep-title">
          依赖检查 {{ inspect.deps.length }} 项<template v-if="inspect.missing">（{{ inspect.missing }} 项缺失）</template>
        </div>
        <div v-for="g in groups" :key="g.key" class="dep-group">
          <div class="dep-group-title">{{ g.label }}（{{ g.items.length }}）</div>
          <div v-for="d in g.items" :key="d.kind + ':' + d.ref + ':' + (d.via || '')"
               class="dep-item" :class="'dep-' + d.state">
            <div class="dep-line">
              <span class="dep-ref">{{ d.ref }}</span>
              <span class="dep-state">{{ DEP_STATE[d.state] || d.state }}</span>
              <span v-if="d.via" class="dep-via">来自 {{ d.via.replace('skill:', '技能 ') }}</span>
            </div>
            <div v-if="d.items && d.items.length" class="dep-members">
              {{ d.items.join('、') }}<template v-if="d.total > d.items.length">…（共 {{ d.total }} 个）</template>
            </div>
            <div v-if="d.detail" class="dep-detail">{{ d.detail }}</div>
          </div>
        </div>
      </template>
      <div v-else class="dep-none">包内没有声明外部依赖</div>

      <div v-if="bundledText" class="dep-bundled">随包自带：{{ bundledText }}</div>
      <div v-for="n in inspect.notes || []" :key="n" class="dep-note">{{ n }}</div>
    </template>
  </div>
</template>

<script setup>
import { computed } from 'vue'

const props = defineProps({
  // inspect 接口的回包（与 /import/inspect、/install/inspect 同形）；错误形态是 { error }
  inspect: { type: Object, required: true },
  kindLabel: { type: String, default: '资产' },
  // 动词：导入 / 安装 —— 同一句话在两处读起来才不别扭
  verb: { type: String, default: '安装' }
})

// 依赖条目的中文标签：kind 区分大类，scope 细分工具来源（mcp / rpc / local …）
// global_var：包里只带变量名，值要目标租户自己在「设置 → 全局变量」填
const DEP_KIND = { skill: '技能', tool: '工具', agent: '智能体', capability: '能力', global_var: '全局变量' }
const DEP_SCOPE = {
  custom: '自定义工具',
  mcp: 'MCP 工具',
  rpc: 'RPC 工具',
  local: '本地端点工具',
  builtin: '内置工具',
  plugin: '插件工具',
  group: '工具组',
  preset: '预设组'
}
const DEP_STATE = { ok: '已存在', bundled: '随包自带', disabled: '不可用', deprecated: '已废弃', missing: '缺失' }

// 按类型归并（技能 / 自定义工具 / MCP / RPC / 本地端点 / 内置 / 插件 / 组 / 智能体 / 能力），
// 顺序固定 —— 依赖清单得能一眼扫出「哪一类缺了几项」
const groups = computed(() => {
  const list = props.inspect?.deps || []
  const order = ['skill', 'custom', 'mcp', 'rpc', 'local', 'builtin', 'plugin', 'group', 'preset', 'agent', 'global_var', 'capability']
  const byKey = new Map()
  for (const d of list) {
    const key = d.kind === 'tool' ? d.scope || 'tool' : d.kind
    if (!byKey.has(key)) byKey.set(key, { key, label: DEP_SCOPE[key] || DEP_KIND[key] || key, items: [] })
    byKey.get(key).items.push(d)
  }
  return [...byKey.values()].sort((a, b) => order.indexOf(a.key) - order.indexOf(b.key))
})

const bundledText = computed(() => {
  const b = props.inspect?.bundled
  if (!b) return ''
  const parts = []
  if (b.custom_tools?.length) parts.push(`自定义工具 ${b.custom_tools.join('、')}`)
  if (b.private_skills?.length) parts.push(`私有技能 ${b.private_skills.join('、')}`)
  return parts.join('；')
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
