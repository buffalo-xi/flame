<template>
  <!-- 编辑器：左侧定义文件树 + 右侧多 tab 编辑区（Markdown 单视图预览） -->
  <!-- 左：定义文件树（AGENT.md 等；「工作区」一词已让给会话运行场地） -->
  <div class="editor-side">
    <div v-if="!agentId" class="editor-tip">保存后自动创建 AGENT.md</div>
    <template v-else>
      <!-- 顶部：面包屑 + 操作，与工作台「工作区」/ 技能编辑同一套 -->
      <div class="side-bar">
        <button class="side-root" :class="{ active: !dirSegs.length }" title="回到根目录"
                @click="enterDir('')">
          <el-icon><HomeFilled /></el-icon><span>根目录</span>
        </button>
        <template v-for="(seg, i) in dirSegs" :key="i">
          <span class="side-sep">/</span>
          <button class="side-crumb" :class="{ active: i === dirSegs.length - 1 }"
                  :title="dirSegs.slice(0, i + 1).join('/')"
                  @click="enterDir(dirSegs.slice(0, i + 1).join('/'))">{{ seg }}</button>
        </template>
        <span class="side-actions">
          <!-- 上传：落点是当前浏览的目录；同名不覆盖，后端另存为 name-2.ext -->
          <input ref="fileInputRef" type="file" multiple class="side-fileinput"
                 @change="onFilesPicked" />
          <el-dropdown v-if="!locked" trigger="click" @command="onCreateCommand">
            <button class="side-btn" :title="dirPath ? `在 /${dirPath} 下新建` : '在根目录新建'">新建</button>
            <template #dropdown>
              <el-dropdown-menu>
                <el-dropdown-item command="dir">新建文件夹</el-dropdown-item>
                <el-dropdown-item command="file">新建文件</el-dropdown-item>
              </el-dropdown-menu>
            </template>
          </el-dropdown>
          <button v-if="!locked" class="side-btn" :disabled="uploading"
                  :title="dirPath ? `上传到 /${dirPath}` : '上传到根目录'"
                  @click="pickFiles">{{ uploading ? '上传中…' : '上传' }}</button>
          <button v-if="!locked && dirEntries.length" class="side-btn"
                  :class="{ active: selectMode }"
                  :title="selectMode ? '退出多选' : '多选删除'"
                  @click="toggleSelect">{{ selectMode ? '取消' : '多选' }}</button>
          <el-icon v-if="dirSegs.length" class="side-icon" title="上一级" @click="dirUp"><Back /></el-icon>
          <el-icon class="side-icon" title="刷新" @click="loadDir()"><Refresh /></el-icon>
        </span>
      </div>
      <div v-if="dragActive" class="side-dropbar">
        释放以上传到 {{ dirPath ? '/' + dirPath : '根目录' }}
      </div>
      <div v-if="selectMode" class="side-selbar">
        <el-checkbox :model-value="allSelected" :indeterminate="someSelected"
                     @change="toggleSelectAll">全选</el-checkbox>
        <span class="side-selcount">已选 {{ selected.length }} 项</span>
        <el-button size="small" type="danger" plain :disabled="!selected.length"
                   @click="removeSelected">删除</el-button>
      </div>
      <!-- 落点只挂在列表容器上：挂整张卡片的话，从桌面拖个文件划过就会误传 -->
      <div class="side-list" :class="{ 'drag-over': dragActive }"
           @dragover.prevent="onDragOver" @dragenter.prevent="onDragEnter"
           @dragleave.prevent="onDragLeave" @drop.prevent="onDrop">
        <div v-for="e in dirEntries" :key="e.path" class="side-item"
             :draggable="!e.is_dir && !selectMode"
             @dragstart="onItemDragStart($event, e)"
             @dragend="onItemDragEnd"
             @pointerdown="onItemPointerDown(e, $event)"
             :class="{ dir: e.is_dir, selecting: selectMode, checked: isSelected(e.path) }"
             @click="selectMode ? toggleSelectOne(e.path) : (e.is_dir ? enterDir(e.path) : openFile(e.path))">
          <el-checkbox v-if="selectMode" class="side-check" :model-value="isSelected(e.path)" />
          <el-icon :style="{ color: e.is_dir ? '#f0a020' : '#909399' }">
            <Folder v-if="e.is_dir" /><Document v-else />
          </el-icon>
          <span class="side-name" :title="e.path">{{ e.name }}</span>
          <span class="side-size">{{ e.is_dir ? '' : fmtSize(e.size) }}</span>
          <el-icon v-if="e.readonly" class="side-lock" title="技能目录，只读"><Lock /></el-icon>
          <!-- 入口文件 AGENT.md：与技能侧 SKILL.md 同口径 —— 可编辑，但不给删除入口 -->
          <el-icon v-else-if="isEntryFile(e)" class="side-lock"
                   title="入口文件，不可删除（可编辑）"><Lock /></el-icon>
          <el-icon v-else-if="!locked && !selectMode" class="side-del"
                   @click.stop="removeEntry(e)"><Delete /></el-icon>
        </div>
        <div v-if="!dirEntries.length" class="side-empty">这个目录还没有文件</div>
      </div>
    </template>
  </div>

  <!-- 右：多 tab 编辑器 -->
  <div class="editor-main">
    <div v-if="tabs.length === 0" class="editor-empty">从左侧选择一个文件开始编辑</div>
    <template v-else>
      <div class="editor-tabs">
        <div v-for="t in tabs" :key="t.path" class="editor-tab"
             :class="{ active: t.path === activeTab }" @click="activeTab = t.path">
          <span>{{ t.name }}</span>
          <i v-if="t.dirty" class="tab-dot"></i>
          <el-icon class="tab-close" @click.stop="closeTab(t.path)"><Close /></el-icon>
        </div>
        <!-- 仅 md 文件：编辑（源码）⇄ 渲染预览，同一时间只显示一个 -->
        <span
          v-if="isMarkdownTab"
          class="md-preview-toggle"
          :class="{ active: mdPreviewVisible }"
          :title="mdPreviewVisible ? '回到编辑（源码）' : '按 Markdown 格式预览'"
          @click="mdPreviewVisible = !mdPreviewVisible"
        >
          <el-icon><View /></el-icon>
          <span>{{ mdPreviewVisible ? '返回编辑' : 'Markdown 预览' }}</span>
        </span>
      </div>
      <div class="editor-split">
        <el-input v-if="!showMdPreview" v-model="currentTab.content" type="textarea" class="content-editor"
                  resize="none" :readonly="!!currentTab.readonly || locked"
                  @input="markDirty" />
        <!-- Markdown 渲染预览（只读，独占编辑区，不再与源码分栏）。
             内容必须经 renderMarkdown 消毒后再注入，不要直接插原始文本。 -->
        <div v-if="showMdPreview" class="md-preview md-preview--solo"
             v-html="renderMarkdown(currentTab.content)"></div>
      </div>
    </template>
  </div>
</template>

<script setup>
import { ref, computed, watch } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { HomeFilled, Back, Folder, Document, Lock, Delete, Close, Refresh, View } from '@element-plus/icons-vue'
import adminApi from '../../config/admin.js'
import { getToken } from '../../utils/auth.js'
import { renderMarkdown } from '../../utils/markdown.js'

const props = defineProps({
  agentId: { type: String, default: '' },
  // 状态只读（废弃）：整个编辑器只读
  locked: { type: Boolean, default: false },
  // 保存前的一次确认（有端点对外开放时提示「改动即时生效」），由父组件提供
  confirmSave: { type: Function, default: async () => true }
})
// state：让父组件的头部按钮知道「能不能保存 / 有没有未保存 / 是否正在保存」
const emit = defineEmits(['state'])

const authHeaders = () => ({
  'Content-Type': 'application/json',
  'Authorization': `Bearer ${getToken()}`
})

const savingFile = ref(false)

const dirPath = ref('')
const dirEntries = ref([])
const dirSegs = computed(() => (dirPath.value ? dirPath.value.split('/').filter(Boolean) : []))

const tabs = ref([])          // [{ path, name, content, readonly, dirty }]
const activeTab = ref('')
const currentTab = computed(() => tabs.value.find((t) => t.path === activeTab.value) || null)

// Markdown 预览：当前 tab 是 .md（AGENT.md / SKILL.md / 任意 *.md）时，可在「源码编辑」与
// 「渲染预览」之间切换 —— 同一时间只显示一个（两栏并排在窄面板下都读不下去）。
// 预览只读；renderMarkdown 内部有 LRU 缓存，反复切换不会重复解析整篇。
const isMarkdownTab = computed(() => /\.md$/i.test(currentTab.value?.name || ''))
const mdPreviewVisible = ref(false) // 默认停在编辑（源码）；非 md 文件不受影响
const showMdPreview = computed(() => isMarkdownTab.value && mdPreviewVisible.value)

const reloadOpenTabs = async () => {
  let skipped = 0
  for (const t of tabs.value) {
    if (t.dirty) { skipped++; continue }
    try {
      const res = await fetch(`${adminApi.agents.file(props.agentId)}?path=${encodeURIComponent(t.path)}`,
        { headers: authHeaders() })
      if (!res.ok) continue
      const data = await res.json()
      const content = data?.data?.content
      if (typeof content === 'string' && content !== t.content) t.content = content
    } catch { /* 单个文件失败不影响其余刷新 */ }
  }
  return skipped
}

// ============ 定义文件：当前目录列表 ============
const fmtSize = (n) => {
  if (n === undefined || n === null) return ''
  if (n < 1024) return `${n}B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)}K`
  return `${(n / 1024 / 1024).toFixed(1)}M`
}

// 列当前目录：目录在前、文件在后，组内按名称数字感知排序（与工作区同口径）
const loadDir = async (p = dirPath.value) => {
  if (!props.agentId) return
  const res = await fetch(`${adminApi.agents.files(props.agentId)}?path=${encodeURIComponent(p || '')}`,
    { headers: authHeaders() })
  const body = await res.json().catch(() => ({}))
  if (body.code !== 200) { ElMessage.error(body.msg || '读取目录失败'); return }
  dirPath.value = body.data?.path || ''
  dirEntries.value = (body.data?.entries || [])
    .map((e) => ({ ...e, readonly: !!e.readonly }))
    .sort((a, b) => {
      if (!!a.is_dir !== !!b.is_dir) return a.is_dir ? -1 : 1
      return String(a.name).localeCompare(String(b.name), 'zh-Hans-CN', { numeric: true, sensitivity: 'base' })
    })
  // 换目录后上一层勾选没有意义，只留仍然存在的
  selected.value = selected.value.filter((x) => dirEntries.value.some((e) => e.path === x))
  // 拖出载荷按路径缓存，换目录后必然失效
  dragPayload.clear()
  dragPending.clear()
}
const reloadDir = () => loadDir()
const enterDir = (p) => {
  selectMode.value = false
  selected.value = []
  loadDir(p)
}
const dirUp = () => enterDir(dirSegs.value.slice(0, -1).join('/'))

// ===== 多选删除：入口文件与 skills/ 都不参与（后端同样拒绝） =====
const selectMode = ref(false)
const selected = ref([])
const isSelected = (p) => selected.value.includes(p)
const selectableEntries = computed(() => dirEntries.value.filter((e) => !e.readonly && !isEntryFile(e)))
const allSelected = computed(() =>
  selectableEntries.value.length > 0 && selected.value.length === selectableEntries.value.length)
const someSelected = computed(() => selected.value.length > 0 && !allSelected.value)
const toggleSelect = () => {
  selectMode.value = !selectMode.value
  selected.value = []
}
const toggleSelectAll = () => {
  selected.value = allSelected.value ? [] : selectableEntries.value.map((e) => e.path)
}
const toggleSelectOne = (p) => {
  if (!isSelected(p)) { selected.value = [...selected.value, p]; return }
  selected.value = selected.value.filter((x) => x !== p)
}

// ===== 上传（按钮 + 从桌面拖进来）=====
// 落点 = 当前目录；同名不覆盖，后端另存为 name-2.ext；skills/ 由后端拒绝
const fileInputRef = ref(null)
const uploading = ref(false)
const pickFiles = () => {
  if (props.locked || uploading.value) return
  if (fileInputRef.value) fileInputRef.value.click()
}
const onFilesPicked = async (ev) => {
  const files = Array.from(ev.target.files || [])
  ev.target.value = '' // 清掉，否则连续选同一个文件不会再触发 change
  await uploadFiles(files)
}
const uploadFiles = async (files) => {
  if (!files || !files.length || !props.agentId || props.locked) return
  const where = dirPath.value ? `/${dirPath.value}` : '定义根目录'
  uploading.value = true
  let ok = 0
  const failed = []
  try {
    for (const f of files) {
      try {
        const fd = new FormData()
        fd.append('file', f)
        fd.append('dir', dirPath.value)
        // 不能带 Content-Type：multipart 的 boundary 必须由浏览器生成
        const res = await fetch(adminApi.agents.upload(props.agentId), {
          method: 'POST',
          headers: { Authorization: `Bearer ${getToken()}` },
          body: fd
        })
        const body = await res.json().catch(() => ({}))
        if (body.code !== 200) throw new Error(body.msg || '上传失败')
        ok++
      } catch {
        failed.push(f.name)
      }
    }
  } finally {
    uploading.value = false
  }
  if (failed.length) ElMessage.error(`${ok} 个已上传到 ${where}，${failed.length} 个失败：${failed.join('、')}`)
  else ElMessage.success(`已上传 ${ok} 个文件到 ${where}`)
  await loadDir()
}

// 拖拽上传：dragleave 在子元素之间移动会连续触发，必须用计数器防抖
const dragDepth = ref(0)
const dragActive = computed(() => dragDepth.value > 0)
const carriesFiles = (ev) => {
  const types = ev.dataTransfer && ev.dataTransfer.types
  return !!types && Array.from(types).includes('Files')
}
const onDragOver = (ev) => {
  if (carriesFiles(ev) && ev.dataTransfer) ev.dataTransfer.dropEffect = 'copy'
}
const onDragEnter = (ev) => { if (carriesFiles(ev)) dragDepth.value++ }
const onDragLeave = () => { dragDepth.value = Math.max(0, dragDepth.value - 1) }
const onDrop = async (ev) => {
  dragDepth.value = 0
  const dt = ev.dataTransfer
  if (!dt) return
  // 拖进来的是文件夹：明确拒绝，不做递归展开（与工作区同口径）
  const items = Array.from(dt.items || [])
  const hasDir = items.some((it) => {
    const entry = it.webkitGetAsEntry && it.webkitGetAsEntry()
    return !!(entry && entry.isDirectory)
  })
  if (hasDir) { ElMessage.warning('不支持拖入文件夹，请逐个上传或先压缩'); return }
  await uploadFiles(Array.from(dt.files || []))
}

// ===== 拖出到桌面（与工作台「工作区」同一套：data: 载荷）=====
// 只走 data: URL：定义域文件都是提示词/脚本（编辑器上限 2 MiB），而票据那条路在拖出失败时
// 是全静默的（拖拽期间 GET 到 403 / 超时，桌面上就是什么都不落），排障成本太高。
// 注意：拖出的是磁盘上的内容，编辑器里未保存的修改不会带出去。
const DATA_DRAG_MAX = 4 << 20
const dragPayload = new Map() // path -> "mime:文件名:data:..."
const dragPending = new Set()

// 扩展名 → MIME。不能一律 application/octet-stream：MIME 对不上内容时，
// Windows 落盘可能退化成一个 .url 快捷方式而不是文件本身
const MIME_BY_EXT = {
  txt: 'text/plain', log: 'text/plain', ini: 'text/plain', conf: 'text/plain', toml: 'text/plain',
  md: 'text/markdown', markdown: 'text/markdown', csv: 'text/csv', tsv: 'text/tab-separated-values',
  json: 'application/json', yml: 'text/yaml', yaml: 'text/yaml', xml: 'application/xml',
  html: 'text/html', htm: 'text/html', css: 'text/css', js: 'text/javascript', mjs: 'text/javascript',
  sh: 'text/x-sh', py: 'text/x-python', go: 'text/plain', lua: 'text/plain', sql: 'text/plain',
  png: 'image/png', jpg: 'image/jpeg', jpeg: 'image/jpeg', gif: 'image/gif', webp: 'image/webp',
  svg: 'image/svg+xml', bmp: 'image/bmp', ico: 'image/x-icon', avif: 'image/avif',
  pdf: 'application/pdf', zip: 'application/zip', gz: 'application/gzip', tar: 'application/x-tar'
}
const mimeOf = (name) => {
  const s = String(name || '')
  const i = s.lastIndexOf('.')
  return (i > 0 && MIME_BY_EXT[s.slice(i + 1).toLowerCase()]) || 'application/octet-stream'
}
// 文件名夹在 DownloadURL 的三段式里：冒号会拆坏格式；其余非法字符 Windows 会自己剔除，这里一并做掉
const dragFileName = (name) => {
  const s = String(name || 'file')
    .replace(/[\\/:*?"<>|\u0000-\u001f]/g, '_')
    .replace(/[. ]+$/, '')
  return s || 'file'
}

// 按下即预读：dragstart 里不能 await（拖拽一旦开始，setData 就是空操作）
const ensureDragPayload = async (e) => {
  if (!e || e.is_dir || dragPayload.has(e.path) || dragPending.has(e.path)) return
  if (typeof e.size !== 'number' || e.size < 0 || e.size > DATA_DRAG_MAX) return
  const token = getToken()
  if (!token || !props.agentId) return
  dragPending.add(e.path)
  try {
    const res = await fetch(
      `${adminApi.agents.download(props.agentId)}?path=${encodeURIComponent(e.path)}`,
      { headers: { Authorization: `Bearer ${token}` } }
    )
    if (!res.ok) return
    const blob = await res.blob()
    if (blob.size > DATA_DRAG_MAX) return
    const dataUrl = await new Promise((resolve, reject) => {
      const fr = new FileReader()
      fr.onload = () => resolve(String(fr.result || ''))
      fr.onerror = () => reject(fr.error)
      fr.readAsDataURL(blob)
    })
    if (!dataUrl.startsWith('data:')) return
    const name = dragFileName(e.name)
    dragPayload.set(e.path, `${mimeOf(name)}:${name}:${dataUrl}`)
  } catch {
    // 备不成不打扰：真拖的时候提示一次
  } finally {
    dragPending.delete(e.path)
  }
}

// 行上的 pointerdown：顺手预读；点删除按钮不必把文件内容读一遍
const onItemPointerDown = (e, ev) => {
  if (!e || e.is_dir || selectMode.value) return
  if (ev && ev.target && ev.target.closest && ev.target.closest('.side-del')) return
  ensureDragPayload(e)
}

const onItemDragStart = (ev, e) => {
  if (!ev.dataTransfer || e.is_dir) return
  // 先清空：拖拽元素自带的默认载荷（行内文本）会干扰落点的类型判断
  ev.dataTransfer.clearData()
  const payload = dragPayload.get(e.path)
  if (!payload) {
    ElMessage.warning('拖出前请按住该文件稍等片刻（正在准备内容）')
    ev.dataTransfer.setData('text/plain', e.path)
    return
  }
  ev.dataTransfer.effectAllowed = 'copy'
  ev.dataTransfer.setData('DownloadURL', payload)
}
// 诊断用：dropEffect=none 说明落点没接受（浏览器层面就拒了）；copy 说明已发起写入
const onItemDragEnd = (ev) => {
  if (ev.dataTransfer) console.debug('[drag-out] dropEffect:', ev.dataTransfer.dropEffect)
}

// 打开 / 保存 / 关闭文件
const openFile = async (path) => {
  const exist = tabs.value.find((t) => t.path === path)
  if (exist) { activeTab.value = path; return }
  const res = await fetch(`${adminApi.agents.file(props.agentId)}?path=${encodeURIComponent(path)}`,
    { headers: authHeaders() })
  if (!res.ok) { ElMessage.error('读取文件失败'); return }
  const data = await res.json()
  tabs.value.push({
    path,
    name: path.split('/').pop(),
    content: data?.data?.content || '',
    readonly: !!data?.data?.readonly,
    dirty: false
  })
  activeTab.value = path
}
const markDirty = () => { if (currentTab.value) currentTab.value.dirty = true }
const saveCurrentFile = async () => {
  const t = currentTab.value
  if (!t?.path) return
  if (t.readonly) { ElMessage.warning('技能目录为只读'); return }
  // 有端点对外开放时二次确认：保存即生效，线上调用方立刻看到新行为的差异
  if (!(await props.confirmSave('保存该文件'))) return
  savingFile.value = true
  try {
    const res = await fetch(adminApi.agents.file(props.agentId), {
      method: 'PUT',
      headers: authHeaders(),
      body: JSON.stringify({ path: t.path, content: t.content })
    })
    if (!res.ok) throw new Error('保存失败')
    t.dirty = false
    ElMessage.success('已保存')
  } catch (e) {
    ElMessage.error(e.message || '保存失败')
  } finally {
    savingFile.value = false
  }
}
const closeTab = (path) => {
  const i = tabs.value.findIndex((t) => t.path === path)
  if (i < 0) return
  if (tabs.value[i].dirty) {
    ElMessage.warning({ message: '该文件有未保存修改', duration: 2000, offset: 20 })
  }
  tabs.value.splice(i, 1)
  if (activeTab.value === path) {
    activeTab.value = tabs.value[Math.min(i, tabs.value.length - 1)]?.path || ''
  }
}

// 新建 / 删除条目
// 名字校验：去掉首尾空白与首尾 /；不允许空段、. / ..、. 开头（后端同样会挡）
const validateNewName = (raw) => {
  const v = String(raw || '').trim().replace(/^\/+|\/+$/g, '')
  if (!v) return '请输入名字'
  const segs = v.split('/')
  if (segs.some((s) => !s || s === '.' || s === '..')) return '路径不合法（不能有空段、. 或 ..）'
  if (segs.some((s) => s.startsWith('.'))) return '名字不能以 . 开头（会被列表隐藏）'
  if (segs.some((s) => [...s].length > 120)) return '每段名字最多 120 个字符'
  if (segs.length > 16) return '层级太深（最多 16 层）'
  return true
}
// 「新建」下拉：与工作区一致，一个入口两个动作
const onCreateCommand = (cmd) => promptCreate(cmd === 'dir')

// 新建：落点 = 当前目录；名字里带 / 表示多级子目录（中间层由存储层自然建出）
const promptCreate = async (isDir) => {
  if (props.locked) return
  if (!props.agentId) { ElMessage.warning('请先保存智能体'); return }
  // skills/ 是只读前缀（由技能页维护），当前目录在它下面就直接拦下，不等服务端报错
  if (dirSegs.value[0] === 'skills') {
    ElMessage.warning('该目录只读（skills/ 由技能页维护）')
    return
  }
  const where = dirPath.value ? `/${dirPath.value}` : '智能体根目录'
  let raw = ''
  try {
    const { value } = await ElMessageBox.prompt(
      `创建在 ${where}；名字里带 / 表示多级子目录`,
      isDir ? '新建文件夹' : '新建文件',
      {
        confirmButtonText: '创建',
        cancelButtonText: '取消',
        inputPlaceholder: isDir ? '如 scripts' : '如 scripts/run.sh',
        inputValidator: validateNewName
      }
    )
    raw = value
  } catch { return }
  const rel = raw.trim().replace(/^\/+|\/+$/g, '')
  const fullPath = dirPath.value ? `${dirPath.value}/${rel}` : rel
  try {
    const res = await fetch(adminApi.agents.file(props.agentId), {
      method: 'POST',
      headers: authHeaders(),
      body: JSON.stringify({ path: fullPath, is_dir: isDir })
    })
    const body = await res.json().catch(() => ({}))
    if (body.code !== 200) {
      throw new Error(body.msg === 'already exists' ? `${fullPath} 已存在` : (body.msg || '创建失败'))
    }
    // 多层路径（scripts/run.sh）在当前层看不到，提示里必须写完整路径
    ElMessage.success(`已创建${isDir ? '目录' : '文件'} ${fullPath}`)
    await loadDir()
    // 文件建完直接进编辑器 —— 这里本来就是编辑器，不需要「先写内容再建」
    if (!isDir) await openFile(fullPath)
  } catch (e) {
    ElMessage.error(e.message || '创建失败')
  }
}
// 工作区根的入口文件 AGENT.md：可编辑但不可删除（与技能侧 SKILL.md 同口径）。
// 注意只认「根目录」这一层：实体内部的同名文件（如 docs/AGENT.md）是普通文件，照旧可删。
const isEntryFile = (d) => (d?.path || '').toLowerCase() === 'agent.md'

// 删掉条目后把受影响的 tab 一并关掉：删文件关它自己，删目录关该前缀下所有
const closeTabsUnder = (paths) => {
  const hit = (p) => paths.some((d) => p === d || p.startsWith(`${d}/`))
  tabs.value = tabs.value.filter((t) => !hit(t.path))
  if (!tabs.value.some((t) => t.path === activeTab.value)) {
    activeTab.value = tabs.value[tabs.value.length - 1]?.path || ''
  }
}

const removeEntry = async (e) => {
  if (props.locked) return
  if (isEntryFile(e)) { ElMessage.warning('入口文件 AGENT.md 不可删除（可编辑内容）'); return }
  if (e.readonly) { ElMessage.warning('技能目录为只读'); return }
  const kind = e.is_dir ? '目录（含其中全部文件）' : '文件'
  try {
    await ElMessageBox.confirm(`确定删除${kind}「${e.path}」？此操作不可恢复。`, '删除确认',
      { type: 'warning', confirmButtonText: '删除', cancelButtonText: '取消' })
  } catch { return }
  try {
    const res = await fetch(`${adminApi.agents.file(props.agentId)}?path=${encodeURIComponent(e.path)}`, {
      method: 'DELETE',
      headers: authHeaders()
    })
    const body = await res.json().catch(() => ({}))
    if (body.code !== 200) throw new Error(body.msg || '删除失败')
    ElMessage.success(`已删除 ${e.path}`)
    closeTabsUnder([e.path])
    await loadDir()
  } catch (err) {
    ElMessage.error(err.message || '删除失败')
  }
}

// 批量删除：后端没有批量接口，逐条 DELETE；失败项留在勾选里方便重试
const removeSelected = async () => {
  if (props.locked || !selected.value.length) return
  const paths = [...selected.value]
  try {
    await ElMessageBox.confirm(
      `确定删除选中的 ${paths.length} 项？目录会连同其中全部文件一起删掉，此操作不可恢复。`,
      '批量删除确认',
      { type: 'warning', confirmButtonText: '删除', cancelButtonText: '取消' }
    )
  } catch { return }
  const failed = []
  for (const p of paths) {
    try {
      const res = await fetch(`${adminApi.agents.file(props.agentId)}?path=${encodeURIComponent(p)}`, {
        method: 'DELETE',
        headers: authHeaders()
      })
      const body = await res.json().catch(() => ({}))
      if (body.code !== 200) failed.push(p)
    } catch {
      failed.push(p)
    }
  }
  const oks = paths.filter((p) => !failed.includes(p))
  closeTabsUnder(oks)
  selected.value = failed // 只留失败项，方便直接重试
  await loadDir()
  if (failed.length) {
    ElMessage.error(`${oks.length} 项已删除，${failed.length} 项失败：${failed.join('、')}`)
  } else {
    ElMessage.success(`已删除 ${oks.length} 项`)
    if (!dirEntries.value.length) selectMode.value = false
  }
}

// 头部按钮状态：能否保存（当前 tab 存在且可写）/ 是否有未保存修改 / 是否正在保存
const canSave = computed(() => !!currentTab.value && !currentTab.value.readonly && !props.locked)
const hasDirty = computed(() => tabs.value.some((t) => t.dirty))
watch([canSave, hasDirty, savingFile], () => {
  emit('state', { canSave: canSave.value, hasDirty: hasDirty.value, saving: savingFile.value })
}, { immediate: true })

// 换 agent：清空已开文件，重读根目录并打开入口文件。
// 新建保存后父组件把 editingId 从 '' 改成新 id，也走这里（不再由父组件手动 loadDir/openFile）。
watch(() => props.agentId, async (id) => {
  tabs.value = []
  activeTab.value = ''
  mdPreviewVisible.value = false
  selectMode.value = false
  selected.value = []
  if (!id) {
    dirPath.value = ''
    dirEntries.value = []
    return
  }
  await loadDir('')
  await openFile('AGENT.md')
}, { immediate: true })

defineExpose({ saveCurrentFile, reloadOpenTabs, reloadDir, hasDirty, canSave })

</script>

<style scoped>
/* 新建态提示（还没有 agentId 时左侧只有这一行） */
.editor-tip {
  font-size: 12px;
  color: #909399;
  margin-top: 6px;
}

.editor-side {
  width: 300px;
  flex-shrink: 0;
  display: flex;
  flex-direction: column;
  background: #fff;
  border-radius: 6px;
  padding: 10px 12px;
  /* 滚动交给 .side-list：面包屑和操作条要固定在上面，不能被列表一起滚走 */
  overflow: hidden;
}

/* ===== 定义文件列表（与技能编辑页 Skills.vue 的 .side-* 完全同一套） ===== */
.side-bar {
  display: flex;
  align-items: center;
  gap: 2px;
  padding: 0 0 8px;
  border-bottom: 1px solid #f0f0f0;
  font-size: 12.5px;
  color: #666;
  flex-wrap: wrap;
  flex-shrink: 0;
}
.side-root {
  display: inline-flex;
  align-items: center;
  gap: 3px;
  padding: 3px 8px;
  border: 1px solid #e8e8e8;
  border-radius: 999px;
  background: #fafafa;
  color: #666;
  font: inherit;
  font-size: 12px;
  line-height: 1;
  cursor: pointer;
}
.side-root:hover,
.side-root.active {
  color: #1890ff;
  border-color: #bedaff;
  background: #f0f7ff;
}
.side-crumb {
  max-width: 120px;
  padding: 3px 5px;
  border: none;
  border-radius: 6px;
  background: transparent;
  font: inherit;
  color: inherit;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  cursor: pointer;
}
.side-crumb:hover { color: #1890ff; background: #f0f7ff; }
.side-crumb.active { color: #333; font-weight: 500; }
.side-sep { color: #ccc; flex-shrink: 0; }
.side-actions {
  margin-left: auto;
  display: inline-flex;
  align-items: center;
  gap: 8px;
  flex-shrink: 0;
}
.side-btn {
  border: none;
  background: none;
  padding: 2px 6px;
  font-size: 12px;
  color: #909399;
  border-radius: 4px;
  cursor: pointer;
  white-space: nowrap;
}
.side-btn:hover { background: #f0f2f5; color: #409eff; }
.side-btn.active { color: #409eff; background: #ecf5ff; }
.side-btn:disabled { opacity: 0.6; cursor: default; }
.side-icon { cursor: pointer; color: #999; font-size: 14px; }
.side-icon:hover { color: #1890ff; }
.side-dropbar {
  padding: 5px 4px;
  border-bottom: 1px solid #d9ecff;
  background: #f0f7ff;
  font-size: 12px;
  color: #409eff;
}
.side-selbar {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 4px 6px;
  margin-bottom: 4px;
  border-radius: 4px;
  background: #fafafa;
  font-size: 12px;
  color: #666;
}
.side-selbar .side-selcount { flex: 1; min-width: 0; color: #909399; }
.side-list {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  border-radius: 4px;
}
.side-list.drag-over {
  outline: 2px dashed #409eff;
  outline-offset: -4px;
  background: #f7fbff;
}
.side-item {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 6px 4px;
  border-radius: 4px;
  font-size: 13px;
  color: #444;
  cursor: pointer;
}
.side-item:hover { background: #f5f7fa; }
.side-item .el-icon { flex-shrink: 0; font-size: 15px; }
/* 行内勾选框：只做展示，点击交给整行 */
.side-item .side-check { pointer-events: none; height: auto; margin-right: 2px; }
.side-item.selecting.checked { background: #ecf5ff; }
.side-name { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.side-size {
  flex-shrink: 0;
  font-size: 11px;
  color: #c0c4cc;
  font-variant-numeric: tabular-nums;
}
.side-lock { color: #c0c4cc; font-size: 12px; }
/* 删除按钮：常态半透明（触屏上也够得着），行悬停才显形 */
.side-del {
  color: #c0c4cc;
  font-size: 12px;
  opacity: 0.35;
  transition: opacity 0.15s, color 0.15s;
}
.side-item:hover .side-del { opacity: 1; }
.side-del:hover { color: #f56c6c; }
.side-empty { padding: 14px 4px; font-size: 12px; color: #a8abb2; }
/* 上传：原生 input 藏起来，由按钮触发 */
.side-fileinput { display: none; }

.editor-main {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
  background: #fff;
  border-radius: 6px;
  padding: 10px 12px;
  overflow: clip;
}
.editor-empty { color: #909399; font-size: 13px; padding: 20px; }
.editor-tabs {
  display: flex;
  gap: 2px;
  flex-shrink: 0; /* 关键：防止被下方 flex:1 的编辑器挤压塌陷 */
  border-bottom: 1px solid #e4e7ed;
  margin-bottom: 8px;
  overflow-x: auto;
}
.editor-tab {
  display: flex;
  align-items: center;
  gap: 4px;
  height: 34px; /* 原 padding:4px 10px 约 27px，太矮 */
  padding: 0 12px;
  font-size: 13px;
  color: #606266;
  border-radius: 4px 4px 0 0;
  cursor: pointer;
  white-space: nowrap;
}
.editor-tab.active { background: #ecf5ff; color: #409eff; }
.tab-dot { width: 6px; height: 6px; border-radius: 50%; background: #e6a23c; display: inline-block; }
.tab-close { font-size: 12px; color: #c0c4cc; }
.tab-close:hover { color: #f56c6c; }
.content-editor { flex: 1; min-height: 0; }
.content-editor :deep(textarea) {
  height: 100% !important;
  /* 不能用 min-height: calc(100vh - 200px)：编辑区比它矮时 textarea 会被顶破下边缘 */
  min-height: 0;
  font-family: ui-monospace, Menlo, Consolas, monospace;
  font-size: 13px;
  line-height: 1.6;
}
/* 聚焦时不显示蓝色高亮边框（Element Plus 默认会给 focus 加 primary 色边框 + inset 阴影） */
.content-editor :deep(textarea:focus) {
  outline: none !important;
  border-color: #dcdfe6 !important;
  box-shadow: none !important;
}

/* 文件内容编辑框：滚动条默认隐藏，鼠标悬停在编辑框上时才显示
   （thumb 透明而不是隐藏滚动条，避免出现/消失时文本重排） */
.content-editor :deep(textarea) {
  scrollbar-width: thin;                    /* Firefox */
  scrollbar-color: transparent transparent; /* Firefox：默认透明 */
}
.content-editor :deep(textarea)::-webkit-scrollbar {
  width: 8px;
}
.content-editor :deep(textarea)::-webkit-scrollbar-track {
  background: transparent;
}
.content-editor :deep(textarea)::-webkit-scrollbar-thumb {
  background: transparent;
  border-radius: 4px;
}
.content-editor:hover :deep(textarea)::-webkit-scrollbar-thumb {
  background: rgba(0, 0, 0, 0.2);
}
.content-editor:hover :deep(textarea)::-webkit-scrollbar-thumb:hover {
  background: rgba(0, 0, 0, 0.35);
}
.content-editor:hover :deep(textarea) {
  scrollbar-color: rgba(0, 0, 0, 0.2) transparent;
}

/* ---- 编辑器 + Markdown 预览 双栏 ---- */
.editor-split {
  flex: 1;
  min-height: 0;   /* 关键：滚动交给子项自己，别把 .editor-main 撑破 */
  display: flex;
  gap: 12px;
}
/* 横向排列时给编辑器一个可压缩的下限，避免长行把预览挤没 */
.editor-split .content-editor { min-width: 0; }

.md-preview {
  flex: 1;
  min-width: 0;
  overflow: auto;
  padding: 0 2px 0 12px;
  border-left: 1px solid #e4e7ed;
  font-size: 13px;
  line-height: 1.7;
  color: #333;
  word-break: break-word;
}

/* 单视图：预览独占编辑区，去掉分栏用的左边框与左侧内边距 */
.md-preview--solo {
  border-left: none;
  padding: 0;
  font-size: 13.5px;
}

/* 预览滚动条：与编辑框同口径（默认透明，悬浮才上色；约定见 platform/README.md） */
.md-preview { scrollbar-width: thin; scrollbar-color: transparent transparent; }
.md-preview:hover { scrollbar-color: rgba(0, 0, 0, 0.2) transparent; }
.md-preview::-webkit-scrollbar { width: 8px; }
.md-preview::-webkit-scrollbar-thumb { background: transparent; border-radius: 4px; }
.md-preview:hover::-webkit-scrollbar-thumb { background: rgba(0, 0, 0, 0.2); }

/* 预览开关（tab 栏右侧） */
.md-preview-toggle {
  margin-left: auto;   /* 顶到 tab 栏最右 */
  display: inline-flex;
  align-items: center;
  gap: 4px;
  height: 34px;
  padding: 0 6px;
  font-size: 12px;
  color: #909399;
  cursor: pointer;
  white-space: nowrap;
}
.md-preview-toggle:hover,
.md-preview-toggle.active { color: #409eff; }

/* 预览里的 Markdown 元素样式：内容由 v-html 注入，没有 scoped 属性，必须走 :deep()。
   与 Chat.vue 的 .md-body 同一套口径（标题 / 列表 / 表格 / 代码块 / 引用 / 链接 / 分割线）。 */
.md-preview :deep(p) { margin: 0 0 8px; }
.md-preview :deep(p:last-child) { margin-bottom: 0; }
.md-preview :deep(h1),
.md-preview :deep(h2),
.md-preview :deep(h3),
.md-preview :deep(h4),
.md-preview :deep(h5),
.md-preview :deep(h6) { margin: 12px 0 8px; line-height: 1.35; font-weight: 600; }
.md-preview :deep(h1) { font-size: 19px; }
.md-preview :deep(h2) { font-size: 16px; }
.md-preview :deep(h3) { font-size: 14.5px; }
.md-preview :deep(h4),
.md-preview :deep(h5),
.md-preview :deep(h6) { font-size: 13.5px; }
.md-preview :deep(ul),
.md-preview :deep(ol) { margin: 0 0 8px; padding-left: 22px; }
.md-preview :deep(li) { margin: 2px 0; }
.md-preview :deep(a) { color: #409eff; text-decoration: none; }
.md-preview :deep(a:hover) { text-decoration: underline; }
.md-preview :deep(code) {
  font-family: ui-monospace, Menlo, Consolas, monospace;
  font-size: 12.5px;
  background: #f2f3f5;
  border-radius: 4px;
  padding: 1px 5px;
}
.md-preview :deep(pre) {
  margin: 0 0 8px;
  padding: 10px 12px;
  background: #f6f7f9;
  border: 1px solid #ebeef5;
  border-radius: 6px;
  overflow-x: auto;
}
.md-preview :deep(pre code) { background: transparent; padding: 0; line-height: 1.55; }
.md-preview :deep(blockquote) {
  margin: 0 0 8px;
  padding: 2px 0 2px 10px;
  border-left: 3px solid #dcdfe6;
  color: #606266;
}
.md-preview :deep(table) { border-collapse: collapse; margin: 0 0 8px; font-size: 12.5px; }
.md-preview :deep(th),
.md-preview :deep(td) { border: 1px solid #e4e7ed; padding: 4px 8px; }
.md-preview :deep(th) { background: #fafafa; font-weight: 600; }
.md-preview :deep(hr) { border: none; border-top: 1px solid #e8e8e8; margin: 12px 0; }
.md-preview :deep(img) { max-width: 100%; border-radius: 4px; }

</style>
