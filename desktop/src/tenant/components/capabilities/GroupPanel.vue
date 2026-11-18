<template>
  <!-- 自定义工具组编辑器：左侧挑组/工具（草稿），右侧填名字与描述，底部保存 / 删除。
       成员只能是「天然组」或「单个工具」，不允许嵌套工具组（后端同步校验）。 -->
  <el-dialog v-model="visible" :title="title" width="820px" append-to-body
             :close-on-click-modal="false" @closed="onClosed">
    <div class="gp-body">
      <!-- 左：可选目录 -->
      <div class="gp-left">
        <div class="gp-toolbar">
          <el-input v-model="search" size="small" placeholder="搜索名称或 ref" clearable style="width:100%" />
          <el-radio-group v-model="kindFilter" size="small" class="gp-kinds">
            <el-radio-button value="">全部</el-radio-button>
            <el-radio-button value="builtin">内置</el-radio-button>
            <el-radio-button value="preset">预设</el-radio-button>
            <el-radio-button value="ext">插件/脚本</el-radio-button>
            <el-radio-button value="conn">连接/端点</el-radio-button>
          </el-radio-group>
        </div>

        <div class="gp-cards">
          <div v-for="c in pickerCards" :key="c.ref" class="gp-card"
               :class="{ active: !c.synthetic && inDraft(c.ref) }">
            <div class="gp-card-head" @click="c.synthetic ? toggleCollapse(c.ref) : toggleGroup(c)">
              <el-icon class="gp-caret" :class="{ open: !isCollapsed(c.ref) }"
                       @click.stop="toggleCollapse(c.ref)"><CaretRight /></el-icon>
              <div class="gp-main">
                <div class="gp-name">
                  {{ c.name }}
                  <span class="ref-tag">{{ c.synthetic ? '类别' : '组' }}</span>
                  <span class="ref-tag">{{ (c.items || []).length }} 个</span>
                  <!-- 类别卡的名字本身就是类别名（插件 / 自定义工具），不再重复标 kind -->
                  <span v-if="!c.synthetic" class="ref-tag">{{ kindLabel(c.kind) }}</span>
                  <span v-if="c.available === false" class="ref-tag warn">不可用</span>
                </div>
                <div class="gp-desc" :title="c.ref">{{ c.synthetic ? '勾选下面的具体工具' : c.ref }}</div>
              </div>
              <template v-if="!c.synthetic">
                <el-icon v-if="inDraft(c.ref)" class="gp-check"><Check /></el-icon>
                <el-icon v-else class="gp-plus"><Plus /></el-icon>
              </template>
            </div>

            <div v-if="!isCollapsed(c.ref) && (c.items || []).length" class="gp-card-body">
              <div v-for="t in c.items" :key="t.ref" class="gp-tool"
                   :class="{ on: inDraft(t.ref), covered: !c.synthetic && inDraft(c.ref) }" @click="toggleTool(c, t)">
                <span class="gp-tool-name" :title="t.description || t.ref">{{ t.label || t.name }}</span>
                <span class="gp-tool-ref">{{ t.ref }}</span>
                <span class="gp-tool-mark">{{ inDraft(t.ref) || inDraft(c.ref) ? '✓' : '+' }}</span>
              </div>
            </div>
          </div>
          <div v-if="!pickerCards.length" class="editor-tip">没有匹配的项</div>
        </div>
      </div>

      <!-- 右：组信息 + 已选成员 -->
      <div class="gp-right">
        <div class="gp-field">
          <div class="gp-label">组名</div>
          <el-input v-model="form.name" size="small" maxlength="64" placeholder="如 fs-basic（名称即 group:组名）" />
          <div class="editor-tip">字母或数字开头，可用 <code>_ . -</code>；名称等同引用里的 <code>group:&lt;名&gt;</code>。</div>
        </div>
        <div class="gp-field">
          <div class="gp-label">描述</div>
          <el-input v-model="form.description" type="textarea" :rows="2" placeholder="可选" />
        </div>

        <div class="gp-field">
          <div class="gp-label">已选成员（{{ items.length }}）</div>
          <div v-if="!items.length" class="editor-tip">（空）—— 左侧点组或工具加入</div>
          <div v-else class="gp-selected">
            <div v-for="d in sortedItems" :key="d.ref" class="gp-sel-row">
              <el-icon v-if="d.item_type === 'group'" class="gp-group-icon"><Folder /></el-icon>
              <el-icon v-else class="gp-tool-icon"><Tools /></el-icon>
              <div class="gp-main">
                <div class="gp-name">{{ d.label || d.ref }}</div>
                <div class="gp-desc" :title="d.ref">{{ d.ref }}</div>
              </div>
              <el-icon class="gp-del" @click="removeItem(d.ref)"><Delete /></el-icon>
            </div>
          </div>
        </div>
      </div>
    </div>

    <template #footer>
      <el-button v-if="editing" type="danger" plain :loading="deleting" @click="removeGroup">删除组</el-button>
      <span style="flex:1"></span>
      <el-button @click="visible = false">取消</el-button>
      <el-button type="primary" :loading="saving" @click="save">保存</el-button>
    </template>
  </el-dialog>
</template>

<script setup>
import { ref, computed } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { CaretRight, Check, Plus, Delete, Folder, Tools } from '@element-plus/icons-vue'
import adminApi from '../../config/admin.js'
import { getToken } from '../../utils/auth.js'

const props = defineProps({
  // 可引用目录（/admin/toolcatalog/ 的 groups：内置类 / 预设 / 连接 / 本地端点 / 自定义组）
  groups: { type: Array, default: () => [] }
})
const emit = defineEmits(['saved'])

const visible = ref(false)
const saving = ref(false)
const deleting = ref(false)
const search = ref('')
const kindFilter = ref('')
const collapsed = ref({})
const form = ref({ name: '', description: '' })
const items = ref([])          // 草稿：[{ ref, item_type, label, kind, count, description }]
const editing = computed(() => !!originalName.value)
const originalName = ref('')
const title = computed(() => (editing.value ? `编辑工具组 · ${originalName.value}` : '新建工具组'))

const authHeaders = () => ({
  'Content-Type': 'application/json',
  'Authorization': `Bearer ${getToken()}`
})

const kindLabel = (k) => ({
  builtin: '内置', mcp: 'MCP', rpc: 'RPC', local: '本地端点', group: '工具组', preset: '预设',
  plugin: '插件', custom: '自定义工具'
}[k] || k)

// ===== 目录索引：把 ref 翻成可展示的 label / 描述 =====
const groupByRef = computed(() => {
  const m = new Map()
  for (const g of props.groups || []) m.set(g.ref, g)
  return m
})
const toolIndex = computed(() => {
  const m = new Map()
  for (const g of props.groups || []) {
    for (const t of (g.items || [])) m.set(t.ref, t)
  }
  return m
})
const describe = (ref, itemType) => {
  if (itemType === 'group') {
    const g = groupByRef.value.get(ref)
    return {
      ref, item_type: 'group', label: g?.name || ref, kind: g?.kind || '',
      count: (g?.items || []).length, description: g?.description || ''
    }
  }
  const t = toolIndex.value.get(ref)
  return {
    ref, item_type: 'tool', label: t?.label || t?.name || ref, kind: '',
    count: 0, description: t?.description || ''
  }
}

// 展示顺序：组在前、工具在后，同类按名称（与技能页工具依赖一致）
const sortedItems = computed(() => [...items.value].sort((a, b) => {
  if (a.item_type !== b.item_type) return a.item_type === 'group' ? -1 : 1
  return String(a.label || a.ref || '').localeCompare(String(b.label || b.ref || ''), 'zh')
}))

// ===== 左侧可选目录（排除工具组：禁止嵌套） =====
const pickerCards = computed(() => {
  const kw = search.value.trim().toLowerCase()
  const hit = (text) => (text || '').toLowerCase().includes(kw)
  const out = []
  for (const g of props.groups || []) {
    if (g.kind === 'group') continue
    // 插件与自定义工具单独成类：混进「连接/端点」会让人以为要去设置页配连接
    const bucket = ['builtin', 'preset'].includes(g.kind)
      ? g.kind
      : (['plugin', 'script'].includes(g.kind) ? 'ext' : 'conn')
    if (kindFilter.value && kindFilter.value !== bucket) continue
    const groupHit = !kw || hit(g.name) || hit(g.ref)
    const tools = (g.items || []).filter((t) => groupHit || hit(t.label) || hit(t.name) || hit(t.ref))
    if (!groupHit && !tools.length) continue
    out.push({ ...g, items: groupHit ? (g.items || []) : tools })
  }
  return out
})

const inDraft = (ref) => items.value.some((d) => d.ref === ref)
// 缺省收起；类别容器（插件 / 自定义工具，synthetic）例外缺省展开 —— 成员就是全部内容，
// 收起后卡片看着像空的。用户手动收过之后以用户操作为准。
const isSyntheticRef = (ref) => (props.groups || []).some((g) => g.synthetic && g.ref === ref)
const isCollapsed = (ref) => collapsed.value[ref] ?? !isSyntheticRef(ref)
const toggleCollapse = (ref) => {
  collapsed.value = { ...collapsed.value, [ref]: !isCollapsed(ref) }
}

// 整组加入：移除此前单独加入的同组成员，避免语义重复
const toggleGroup = (c) => {
  if (inDraft(c.ref)) {
    items.value = items.value.filter((d) => d.ref !== c.ref)
    return
  }
  const members = new Set((c.items || []).map((t) => t.ref))
  items.value = items.value.filter((d) => !(d.item_type === 'tool' && members.has(d.ref)))
  items.value.push({
    ref: c.ref, item_type: 'group', label: c.name, kind: c.kind,
    count: (c.items || []).length, description: c.description || ''
  })
}

// 单个工具：整组已选时忽略（已在组内）
const toggleTool = (c, t) => {
  if (inDraft(c.ref)) return
  if (inDraft(t.ref)) items.value = items.value.filter((d) => d.ref !== t.ref)
  else items.value.push({
    ref: t.ref, item_type: 'tool', label: t.label || t.name, kind: '',
    count: 0, description: t.description || ''
  })
}

const removeItem = (ref) => {
  items.value = items.value.filter((d) => d.ref !== ref)
}

// ===== 打开 / 保存 / 删除 =====
// group 为 null 表示新建；否则按现有组回填（含成员）
const open = (group = null) => {
  originalName.value = group?.name || ''
  form.value = { name: group?.name || '', description: group?.description || '' }
  items.value = (group?.items || []).map((it) => describe(it.ref, it.item_type))
  search.value = ''
  kindFilter.value = ''
  collapsed.value = {}
  visible.value = true
}

const onClosed = () => {
  saving.value = false
  deleting.value = false
}

const save = async () => {
  const name = (form.value.name || '').trim()
  if (!name) { ElMessage.warning('请填写组名'); return }
  if (!/^[A-Za-z0-9][A-Za-z0-9_.-]*$/.test(name)) {
    ElMessage.warning('组名需以字母或数字开头，仅含字母数字与 _ . -'); return
  }
  saving.value = true
  try {
    const res = await fetch(adminApi.toolGroups.upsert, {
      method: 'POST',
      headers: authHeaders(),
      body: JSON.stringify({
        name,
        description: (form.value.description || '').trim(),
        // 只提交 ref：item_type 由服务端按 ref 形态推导
        items: items.value.map((d) => ({ ref: d.ref }))
      })
    })
    const body = await res.json().catch(() => ({}))
    if (body.code !== 200) throw new Error(body.msg || '保存失败')
    ElMessage.success('工具组已保存')
    visible.value = false
    emit('saved')
  } catch (e) {
    ElMessage.error(e.message || '保存失败')
  } finally {
    saving.value = false
  }
}

const removeGroup = async () => {
  if (!originalName.value) return
  try {
    await ElMessageBox.confirm(
      `确定删除工具组 ${originalName.value} ？被 agent / 技能 / 其它组引用时会拒绝删除。`,
      '删除确认', { type: 'warning' }
    )
  } catch {
    return
  }
  deleting.value = true
  try {
    const res = await fetch(adminApi.toolGroups.remove(originalName.value), {
      method: 'DELETE', headers: authHeaders()
    })
    const body = await res.json().catch(() => ({}))
    if (body.code !== 200) throw new Error(body.msg || '删除失败')
    ElMessage.success('工具组已删除')
    visible.value = false
    emit('saved')
  } catch (e) {
    ElMessage.error(e.message || '删除失败')
  } finally {
    deleting.value = false
  }
}

defineExpose({ open })
</script>

<style scoped>
.gp-body {
  display: flex;
  gap: 14px;
  height: 460px;
}

.gp-left {
  flex: 1.15;
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 8px;
  border-right: 1px solid #f0f0f0;
  padding-right: 14px;
}

.gp-toolbar {
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.gp-kinds :deep(.el-radio-button__inner) {
  padding: 5px 8px;
  font-size: 12px;
}

.gp-cards {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  scrollbar-gutter: stable;
  /* 滚动条「位置常驻、悬浮才上色」（同 .tt / Chat.vue） */
  scrollbar-width: thin;
  scrollbar-color: transparent transparent;
}

.gp-cards::-webkit-scrollbar {
  width: 6px;
  height: 6px;
}

.gp-cards::-webkit-scrollbar-track {
  background: transparent;
}

.gp-cards::-webkit-scrollbar-thumb {
  background: transparent;
  border-radius: 3px;
}

.gp-cards:hover {
  scrollbar-color: #c1c1c1 transparent;
}

.gp-cards:hover::-webkit-scrollbar-thumb {
  background: #c1c1c1;
}

.gp-card {
  border: 1px solid #ebeef5;
  border-radius: 6px;
  margin-bottom: 8px;
  overflow: hidden;
}

.gp-card.active {
  border-color: #a0cfff;
  background: #f5faff;
}

.gp-card-head {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 8px 10px;
  cursor: pointer;
}

.gp-card-head:hover {
  background: #f7f9fc;
}

.gp-caret {
  font-size: 12px;
  color: #909399;
  transition: transform 0.2s;
}

.gp-caret.open {
  transform: rotate(90deg);
}

.gp-main {
  flex: 1;
  min-width: 0;
}

.gp-name {
  font-size: 13px;
  color: #303133;
  display: flex;
  align-items: center;
  gap: 6px;
  flex-wrap: wrap;
}

.gp-desc {
  font-size: 12px;
  color: #909399;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.gp-check {
  color: #409eff;
}

.gp-plus {
  color: #c0c4cc;
}

.gp-card-body {
  border-top: 1px solid #f5f5f5;
  padding: 4px 0;
}

.gp-tool {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 5px 10px 5px 26px;
  cursor: pointer;
  font-size: 12px;
  color: #606266;
}

.gp-tool:hover {
  background: #f7f9fc;
}

.gp-tool.on {
  color: #409eff;
}

.gp-tool.covered {
  color: #c0c4cc;
  cursor: default;
}

.gp-tool-name {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.gp-tool-ref {
  font-family: ui-monospace, Menlo, Consolas, monospace;
  color: #c0c4cc;
}

.gp-tool-mark {
  width: 12px;
  text-align: center;
}

.gp-right {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.gp-field {
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.gp-label {
  font-size: 13px;
  color: #606266;
}

.gp-selected {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  scrollbar-gutter: stable;
  border: 1px solid #f0f0f0;
  border-radius: 6px;
  padding: 4px;
  /* 滚动条「位置常驻、悬浮才上色」（同 .tt / Chat.vue） */
  scrollbar-width: thin;
  scrollbar-color: transparent transparent;
}

.gp-selected::-webkit-scrollbar {
  width: 6px;
  height: 6px;
}

.gp-selected::-webkit-scrollbar-track {
  background: transparent;
}

.gp-selected::-webkit-scrollbar-thumb {
  background: transparent;
  border-radius: 3px;
}

.gp-selected:hover {
  scrollbar-color: #c1c1c1 transparent;
}

.gp-selected:hover::-webkit-scrollbar-thumb {
  background: #c1c1c1;
}

.gp-sel-row {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 5px 6px;
  border-radius: 4px;
}

.gp-sel-row:hover {
  background: #f7f9fc;
}

.gp-group-icon {
  color: #e6a23c;
}

.gp-tool-icon {
  color: #909399;
}

.gp-del {
  color: #c0c4cc;
  cursor: pointer;
}

.gp-del:hover {
  color: #f56c6c;
}

.ref-tag {
  font-size: 11px;
  padding: 1px 5px;
  border-radius: 3px;
  color: #909399;
  background: #f4f4f5;
}

.ref-tag.warn {
  color: #e6a23c;
  background: #fdf6ec;
}

.editor-tip {
  font-size: 12px;
  color: #909399;
}
</style>
