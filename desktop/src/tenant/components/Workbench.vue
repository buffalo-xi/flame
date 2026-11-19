<template>
  <div class="workbench" :class="'workbench--divider-' + dividerSide">
    <!-- 标签条 -->
    <div class="wb-tabbar">
      <div ref="tabsRef" class="wb-tabs" @wheel="onTabsWheel">
        <div
          v-for="t in tabs"
          :key="t.id"
          :data-tab-id="t.id"
          class="wb-tab"
          :class="{ active: t.id === activeId }"
          :title="t.title"
          @click="activeId = t.id"
          @auxclick.middle.prevent="t.closable && close(t.id)"
        >
          <el-icon class="wb-tab-icon"><component :is="iconOf(t)" /></el-icon>
          <span class="wb-tab-title">{{ t.title }}</span>
          <el-icon v-if="t.closable" class="wb-tab-close" @click.stop="close(t.id)"><Close /></el-icon>
        </div>
        <button class="wb-tab-add" :title="t('workbench.tabNew')" @click="newTab">
          <el-icon><Plus /></el-icon>
        </button>
      </div>
      <!-- 关闭整个工作台（对应会话工具的 ×）；标签保留，下次点顶栏「工作台」还能回来 -->
      <button class="wb-close" :title="t('workbench.closePanel')" @click="closePanel">
        <el-icon><Close /></el-icon>
      </button>
    </div>

    <!-- 内容区 -->
    <div class="wb-body">
      <!-- 启动页：新标签的若干选项 -->
      <div v-if="active && active.type === 'home'" class="wb-home">
        <div class="wb-home-title">{{ t('workbench.tabNew') }}</div>
        <div class="wb-home-grid">
          <button class="wb-opt" @click="openWorkspace">
            <el-icon class="wb-opt-icon"><FolderOpened /></el-icon>
            <span>{{ t('workbench.openWorkspace') }}</span>
          </button>
          <button class="wb-opt" @click="openRecentFiles">
            <el-icon class="wb-opt-icon"><Document /></el-icon>
            <span>{{ t('workbench.recentFiles') }}</span>
          </button>
          <button class="wb-opt" @click="openBrowser">
            <el-icon class="wb-opt-icon"><Monitor /></el-icon>
            <span>{{ t('workbench.openBrowser') }}</span>
          </button>
        </div>
      </div>

      <!-- 浏览器：直接 iframe，不带内置地址栏 -->
      <div v-else-if="active && active.type === 'browser'" class="wb-browser">
        <iframe class="wb-frame" :src="active.payload && active.payload.url" @load="onFrameLoad"></iframe>
      </div>

      <!-- 工作区：当前会话的运行场地（浏览 / 预览 / 新建 / 上传 / 删除；已有文件不在界面里编辑内容） -->
      <div v-else-if="active && active.type === 'workspace'" class="wb-ws">
        <template v-if="sessionContext.sessionId">
          <div class="wb-ws-bar">
            <!-- 根目录单独做成胶囊按钮：原来是一个 "/" 字符，点击目标只有几像素宽 -->
            <button class="wb-ws-root" :class="{ active: !wsSegs.length }"
                    :title="t('workbench.root')" @click="wsOpen('')">
              <el-icon><HomeFilled /></el-icon><span>{{ t('workbench.root') }}</span>
            </button>
            <template v-for="(seg, i) in wsSegs" :key="i">
              <span class="wb-ws-sep">/</span>
              <button class="wb-ws-crumb" :class="{ active: i === wsSegs.length - 1 }"
                      :title="wsSegs.slice(0, i + 1).join('/')"
                      @click="wsOpen(wsSegs.slice(0, i + 1).join('/'))">{{ seg }}</button>
            </template>
            <span class="wb-ws-actions">
              <!-- 会话空间用量：只读计数（不扫目录），接近上限变红 -->
              <span v-if="wsUsageText" class="wb-ws-quota" :class="{ warn: wsUsagePct >= 90 }"
                    :title="t('workbench.usageTitle', { text: wsUsageText })">{{ wsUsageText }}</span>
              <!-- 新建：落点同样是当前浏览的目录；名字里带 / 就是多级子目录（后端逐级建） -->
              <el-dropdown class="wb-ws-new" trigger="click" :disabled="wsSelectMode"
                           @command="onWsNewCommand">
                <button class="wb-ws-selbtn" :disabled="wsSelectMode"
                        :title="wsPath ? t('workbench.newIn', { path: wsPath }) : t('workbench.newInRoot')">{{ t('workbench.newBtn') }}</button>
                <template #dropdown>
                  <el-dropdown-menu>
                    <el-dropdown-item command="dir">{{ t('workbench.newDir') }}</el-dropdown-item>
                    <el-dropdown-item command="file">{{ t('workbench.newFile') }}</el-dropdown-item>
                  </el-dropdown-menu>
                </template>
              </el-dropdown>
              <!-- 上传：落点是当前浏览的目录（不是固定的 uploads/）；同名不覆盖，后端另存为 name-2.ext -->
              <input ref="wsFileInputRef" type="file" multiple class="wb-ws-fileinput"
                     @change="onWsFilesPicked" />
              <button class="wb-ws-selbtn" :disabled="wsUploading"
                      :title="wsPath ? t('workbench.uploadTo', { path: wsPath }) : t('workbench.uploadToRoot')"
                      @click="pickWsFiles">{{ wsUploading ? t('workbench.uploading') : t('workbench.upload') }}</button>
              <!-- 多选：产物清理常是一次删好几个，单条删太慢 -->
              <button class="wb-ws-selbtn" :class="{ active: wsSelectMode }"
                      :title="wsSelectMode ? t('workbench.exitSelect') : t('workbench.selectDelete')"
                      @click="toggleWsSelect">{{ wsSelectMode ? t('workbench.cancelSelect') : t('workbench.multiSelect') }}</button>
              <!-- 一键清空：清的是当前浏览的目录（在根目录就是整个工作区）；空目录 / 多选态下不显示 -->
              <button v-if="wsEntries.length" class="wb-ws-selbtn wb-ws-clear"
                      :disabled="wsClearing || wsSelectMode"
                      :title="wsPath ? t('workbench.clearUnder', { path: wsPath }) : t('workbench.clearAllWorkspace')"
                      @click="wsClearAll">{{ wsClearing ? t('workbench.clearing') : t('workbench.clear') }}</button>
              <el-icon v-if="wsSegs.length" class="wb-ws-up" :title="t('workbench.up')" @click="wsUp()"><Back /></el-icon>
              <el-icon class="wb-ws-refresh" :title="t('workbench.refresh')" @click="wsList()"><Refresh /></el-icon>
            </span>
          </div>
          <!-- 拖拽中：把落点写出来，别让人靠猜 -->
          <div v-if="wsDragActive" class="wb-ws-dropbar">
            {{ t('workbench.dropHint', { where: wsPath ? '/' + wsPath : t('workbench.root') }) }}
          </div>
          <!-- 多选操作条：只在多选态出现，全选 / 计数 / 删除一行说完 -->
          <div v-if="wsSelectMode" class="wb-ws-selbar">
            <el-checkbox class="wb-ws-selall" :model-value="wsAllSelected"
                         :indeterminate="wsSomeSelected" @change="toggleWsSelectAll">{{ t('workbench.selectAll') }}</el-checkbox>
            <span class="wb-ws-selcount">{{ t('workbench.selectedCount', { n: wsSelected.length }) }}</span>
            <el-button size="small" type="danger" plain
                       :disabled="!wsSelected.length" @click="wsRemoveSelected">{{ t('workbench.delete') }}</el-button>
          </div>
          <!-- 拖拽落点只挂在列表容器上：挂整页的话，从桌面拖个文件划过工作区就会误传 -->
          <div ref="wsListRef" class="wb-ws-list" :class="{ 'drag-over': wsDragActive }"
               @dragover.prevent="onWsDragOver"
               @dragenter.prevent="onWsDragEnter"
               @dragleave.prevent="onWsDragLeave"
               @drop.prevent="onWsDrop">
            <div
              v-for="e in wsEntries"
              :key="e.path"
              :data-path="e.path"
              class="wb-ws-item"
              :class="{ dir: e.is_dir, selecting: wsSelectMode, checked: isWsSelected(e.path), focused: wsFocusPath === e.path }"
              :draggable="!e.is_dir && !wsSelectMode"
              @dragstart="onWsItemDragStart($event, e)"
              @dragend="onWsItemDragEnd"
              @pointerenter="prewarmWsTicket(e, 120)"
              @pointerdown="onWsItemPointerDown(e, $event)"
              @click="wsSelectMode ? toggleWsSelectOne(e.path) : (e.is_dir ? wsOpen(e.path) : openWsFile(e))"
            >
              <!-- 多选态：勾选框只做展示，点击统一交给整行，避免双触发 -->
              <el-checkbox v-if="wsSelectMode" class="wb-ws-check" :model-value="isWsSelected(e.path)" />
              <el-icon :style="{ color: e.color }"><component :is="e.icon" /></el-icon>
              <span class="wb-ws-name">{{ e.name }}</span>
              <span class="wb-ws-size">{{ e.is_dir ? t('workbench.folder') : fmtSize(e.size) }}</span>
              <span class="wb-ws-time" :title="fmtWsTimeFull(e.mod_time)">{{ fmtWsTime(e.mod_time) }}</span>
              <!-- 重命名与删除：.stop 是必须的 —— 父层的点击是「进目录 / 开预览」 -->
              <el-icon v-if="!wsSelectMode" class="wb-ws-edit"
                       :title="e.is_dir ? t('workbench.renameDir') : t('workbench.renameFile')"
                       @click.stop="wsRename(e)"><EditPen /></el-icon>
              <el-icon v-if="!wsSelectMode" class="wb-ws-del"
                       :title="e.is_dir ? t('workbench.deleteDir') : t('workbench.deleteFile')"
                       @click.stop="wsRemove(e)"><Delete /></el-icon>
            </div>
            <div v-if="wsEntries.length === 0" class="wb-ws-empty">{{ t('workbench.emptyDir') }}</div>
          </div>
        </template>
        <div v-else class="wb-tip">
          <p>{{ t('workbench.noSession') }}</p>
          <p class="wb-tip-sub">{{ t('workbench.noSessionWsSub') }}</p>
        </div>
        <!-- 新建文件：名字 + 初始内容。内容必须在这里写 —— 文件标签是只读的（不提供就地编辑），
             不带内容的话新建出来的就只是个空壳。对话框放在 v-else 之后：v-else 必须紧邻上面的模板 -->
        <el-dialog v-model="wsNewOpen" :title="t('workbench.newFile')" width="520px" class="wb-newdlg"
                   :close-on-click-modal="false" @closed="wsNewContent = ''">
          <div class="wb-newrow">
            <span class="wb-newlabel">{{ t('workbench.pathLabel') }}</span>
            <el-input v-model="wsNewName" :placeholder="t('workbench.pathPlaceholder')"
                      @keyup.enter="wsCreateFile" />
          </div>
          <div class="wb-newhint">
            {{ t('workbench.newFileHint', { where: wsPath ? '/' + wsPath : t('workbench.root') }) }}
          </div>
          <div class="wb-newrow">
            <span class="wb-newlabel">{{ t('workbench.contentLabel') }}</span>
            <el-input v-model="wsNewContent" type="textarea" :rows="10"
                      :placeholder="t('workbench.contentPlaceholder')" />
          </div>
          <template #footer>
            <el-button @click="wsNewOpen = false">{{ t('workbench.cancel') }}</el-button>
            <el-button type="primary" :loading="wsNewSaving" @click="wsCreateFile">{{ t('workbench.create') }}</el-button>
          </template>
        </el-dialog>
      </div>

      <!-- 工作区里的文件：点列表项开一个与「工作区」平级的只读标签 -->
      <div v-else-if="active && active.type === 'wsfile'" class="wb-wsfile">
        <div class="wb-wsfile-head">
          <span class="wb-wsfile-path" :title="active.payload.path">/{{ active.payload.path }}</span>
          <span class="wb-wsfile-tools">
            <!-- 仅 Markdown：渲染预览 ⇄ 源码 -->
            <el-icon v-if="active.payload.kind === 'markdown'" class="wb-wsfile-toggle"
                     :class="{ active: active.payload.preview }"
                     :title="active.payload.preview ? t('workbench.showSource') : t('workbench.showPreview')"
                     @click="active.payload.preview = !active.payload.preview"><View /></el-icon>
            <el-icon class="wb-wsfile-refresh" :title="t('workbench.reload')" @click="loadWsFileTab(active)"><Refresh /></el-icon>
            <el-icon class="wb-wsfile-download" :title="t('workbench.download')" @click="downloadWsFile(active)"><Download /></el-icon>
          </span>
        </div>
        <div v-if="active.payload.loading" class="wb-tip wb-wsfile-tip"><p>{{ t('workbench.loading') }}</p></div>
        <div v-else-if="active.payload.error" class="wb-tip wb-wsfile-tip">
          <p>{{ active.payload.error }}</p>
          <p class="wb-tip-sub">{{ t('workbench.downloadToOpen') }}</p>
        </div>
        <!-- 图片 / PDF：用 blob URL 内联预览（token 走 fetch 头，不拼进 URL） -->
        <img v-else-if="active.payload.kind === 'image'" class="wb-wsfile-image"
             :src="active.payload.blobUrl" :alt="active.payload.name" />
        <iframe v-else-if="active.payload.kind === 'pdf'" class="wb-wsfile-frame"
                :src="active.payload.blobUrl"></iframe>
        <!-- 其它二进制：兜底。正常入口（openWsFile）不会给 binary 开标签 —— 点了直接下载；
             只有历史标签、或将来新增的入口才会落到这里 -->
        <div v-else-if="active.payload.kind === 'binary'" class="wb-tip wb-wsfile-tip">
          <p>{{ active.payload.name }}</p>
          <p class="wb-tip-sub">{{ t('workbench.binaryUnsupported') }}</p>
          <el-button size="small" type="primary" plain @click="downloadWsFile(active)">{{ t('workbench.downloadFile') }}</el-button>
        </div>
        <!-- Markdown 渲染：内容经 renderMarkdown 消毒后注入（与智能体编辑器的 md 预览同一套） -->
        <div v-else-if="active.payload.kind === 'markdown' && active.payload.preview"
             class="wb-wsfile-md" v-html="renderMarkdown(active.payload.content)"></div>
        <pre v-else class="wb-wsfile-body">{{ active.payload.content }}</pre>
      </div>

      <!-- 文件：待接入 FileViewer（第一步先占位） -->
      <div v-else-if="active && active.type === 'file'" class="wb-tip">
        <p>{{ t('workbench.fileTab', { path: active.payload && active.payload.path }) }}</p>
        <p class="wb-tip-sub">{{ t('workbench.fileViewerNext') }}</p>
      </div>

      <!-- 最近改动/生成的文件：不分目录，按修改时间倒序摊平，点一下开文件标签 -->
      <div v-else-if="active && active.type === 'custom' && active.payload && active.payload.kind === 'recent'"
           class="wb-ws">
        <template v-if="sessionContext.sessionId">
          <div class="wb-ws-bar">
            <span class="wb-recent-title">{{ t('workbench.recentTitle') }}</span>
            <span class="wb-ws-actions">
              <!-- 一键清空：删的是「当前列表里的这些文件」（后端只回最近 30 条，更深目录的文件不在这里） -->
              <button v-if="recentFiles.length" class="wb-ws-selbtn wb-ws-clear"
                      :disabled="wsClearing" :title="t('workbench.deleteAllInList')"
                      @click="wsClearRecent">{{ wsClearing ? t('workbench.clearing') : t('workbench.clear') }}</button>
              <el-icon class="wb-ws-refresh" :title="t('workbench.refresh')" @click="loadRecent()"><Refresh /></el-icon>
            </span>
          </div>
          <div class="wb-ws-list">
            <!-- 与工作区列表同一套：按住预读内容，拖出桌面即落一个文件；行尾可删。
                 最近文件是扁平列表、没有目录项，所以不给多选，删除单条走行尾图标 -->
            <div v-for="f in recentFiles" :key="f.path" class="wb-ws-item"
                 draggable="true"
                 @dragstart="onWsItemDragStart($event, f)"
                 @dragend="onWsItemDragEnd"
                 @pointerdown="onWsRecentPointerDown(f, $event)"
                 :title="f.path" @click="openWsFile(f)">
              <el-icon :style="{ color: fileMeta(f.name).color }">
                <component :is="fileMeta(f.name).icon" />
              </el-icon>
              <span class="wb-ws-name">
                {{ f.name }}
                <span v-if="dirOf(f.path)" class="wb-recent-dir">{{ dirOf(f.path) }}</span>
              </span>
              <span class="wb-ws-size">{{ fmtSize(f.size) }}</span>
              <span class="wb-ws-time" :title="fmtWsTimeFull(f.mod_time)">{{ fmtWsTime(f.mod_time) }}</span>
              <el-icon class="wb-ws-edit" :title="t('workbench.renameFile')" @click.stop="wsRename(f, true)"><EditPen /></el-icon>
              <el-icon class="wb-ws-del" :title="t('workbench.deleteFile')" @click.stop="wsRemoveRecent(f)"><Delete /></el-icon>
            </div>
            <div v-if="recentLoading" class="wb-ws-empty">{{ t('workbench.recentLoading') }}</div>
            <div v-else-if="!recentFiles.length" class="wb-ws-empty">{{ t('workbench.recentEmpty') }}</div>
          </div>
        </template>
        <div v-else class="wb-tip">
          <p>{{ t('workbench.noSession') }}</p>
          <p class="wb-tip-sub">{{ t('workbench.noSessionRecentSub') }}</p>
        </div>
      </div>

      <!-- 其它类型的占位（workspace / custom） -->
      <div v-else-if="active" class="wb-tip">
        <p>{{ active.title }}</p>
        <p class="wb-tip-sub">{{ t('workbench.notImplemented', { type: active.type }) }}</p>
      </div>

      <div v-else class="wb-tip">
        <p>{{ t('workbench.noTabs') }}</p>
        <p class="wb-tip-sub">{{ t('workbench.noTabsHint') }}</p>
      </div>
    </div>
  </div>
</template>

<script setup>
import { computed, inject, nextTick, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { ElIcon, ElMessage, ElMessageBox } from 'element-plus'
import {
  Plus, Close, Document, DocumentCopy, Folder, FolderOpened, Monitor, Refresh,
  HomeFilled, Back, Picture, VideoCamera, Headset, Box, Tickets, DataLine, Download, View, Delete, EditPen
} from '@element-plus/icons-vue'
import adminApi from '../config/admin.js'
import { getToken } from '../utils/auth.js'
import { renderMarkdown } from '../utils/markdown.js'
import { sessionContext } from '../store/sessionContext.js'
import { formatError } from '../../locales'
import { formatDateTime } from '@/utils/datetime.js'

// 分割线侧：会话可见时贴左侧（对话区↔工作台）；工作台单独存在时贴右侧（工作台↔内容区）
defineProps({
  dividerSide: { type: String, default: 'left' }, // 'left' | 'right'
})

const { t, locale } = useI18n()

// 由 MainWindow provide；见 MainWindow.vue 的 provide('workbench', ...)
const wb = inject('workbench')
const tabs = wb.tabs
const activeId = wb.activeId
const { newTab, close, open, closePanel } = wb

const active = computed(() => tabs.value.find((t) => t.id === activeId.value) || null)

const tabsRef = ref(null)

// 标签条横向滚动：滚轮纵向动作转成横向，不必去拖那条细滚动条
const onTabsWheel = (e) => {
  const el = tabsRef.value
  if (!el || el.scrollWidth <= el.clientWidth) return
  if (Math.abs(e.deltaY) <= Math.abs(e.deltaX)) return
  e.preventDefault()
  el.scrollLeft += e.deltaY
}

// 切到某个标签后把它滚进可见区 —— 窄宽度下新开的标签会被推到右边，
// 连 X 都被裁掉，看起来就像「没有关闭按钮」
watch(activeId, async (id) => {
  if (!id) return
  await nextTick()
  const el = tabsRef.value && tabsRef.value.querySelector(`[data-tab-id="${id}"]`)
  if (el && el.scrollIntoView) el.scrollIntoView({ inline: 'nearest', block: 'nearest' })
})

// 标签类型 → 图标
const iconOf = (t) => ({
  home: Plus,
  file: Document,
  wsfile: Document,
  browser: Monitor,
  workspace: FolderOpened,
  custom: Document,
}[t.type] || Document)

const openWorkspace = () => open({ type: 'workspace', title: t('workbench.workspaceTabTitle') })

// ===== 工作区面板：浏览当前会话的运行场地（users/{uid}/sessions/{sid}/） =====
// 可读 / 可下载 / 可删；刻意不提供新建与就地编辑 —— 这个目录归 agent 所有，
// 多一个写者会和正在运行的 agent 抢同一个目录（无锁），见 nexus/admin/agents/files.go 的分节注释。
const wsPath = ref('')
const wsEntries = ref([])
// 上次成功加载列表的时间：判断「切回标签要不要重拉」，见文件末尾的工作区刷新策略
const wsLoadedAt = ref(0)
// 批量删除进行中（工作区「清空」与最近文件「清空」共用）：期间按钮禁用，防重复点击
const wsClearing = ref(false)

// ===== 多选删除 =====
// 进多选后「点行」不再进目录/开预览，改为勾选；换目录或退出即清空
const wsSelectMode = ref(false)
const wsSelected = ref([]) // 选中的 path
const isWsSelected = (p) => wsSelected.value.includes(p)
const wsAllSelected = computed(() => wsEntries.value.length > 0
  && wsSelected.value.length === wsEntries.value.length)
const wsSomeSelected = computed(() => wsSelected.value.length > 0
  && wsSelected.value.length < wsEntries.value.length)
const toggleWsSelect = () => {
  wsSelectMode.value = !wsSelectMode.value
  wsSelected.value = []
}
const toggleWsSelectOne = (p) => {
  const i = wsSelected.value.indexOf(p)
  if (i < 0) wsSelected.value.push(p)
  else wsSelected.value.splice(i, 1)
}
const toggleWsSelectAll = () => {
  wsSelected.value = wsAllSelected.value ? [] : wsEntries.value.map((e) => e.path)
}

// 拖出下载的票据缓存：path -> { url, exp }。
// 票据必须 POST 才能拿到，而 dragstart 里不能 await（drag 一旦开始，setData 就无效了），
// 所以只能在 hover / 按下时预热，dragstart 里同步取缓存。见 prewarmWsTicket。
const wsTicketCache = new Map()
// 小文件的 data: 载荷缓存：path -> "mime:文件名:data:..."（见 ensureDragData）
const wsDataCache = new Map()
const wsDataPending = new Set()

// 「新生成的文件」标签：扁平列表（不分目录），按修改时间倒序
const recentFiles = ref([])
const recentLoading = ref(false)

const wsSegs = computed(() => (wsPath.value ? wsPath.value.split('/').filter(Boolean) : []))

const fmtSize = (n) => {
  if (n === undefined || n === null) return ''
  if (n < 1024) return `${n}B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)}K`
  return `${(n / 1024 / 1024).toFixed(1)}M`
}

// 修改时间：同年只显示「MM-DD HH:mm」，跨年显示「YYYY-MM-DD」；零值（S3 的目录）留空
const pad2 = (n) => String(n).padStart(2, '0')
const wsModTime = (v) => {
  if (!v) return null
  const d = new Date(v)
  if (Number.isNaN(d.getTime()) || d.getFullYear() < 1970) return null
  return d
}
const fmtWsTime = (v) => {
  const d = wsModTime(v)
  if (!d) return ''
  const md = `${pad2(d.getMonth() + 1)}-${pad2(d.getDate())}`
  return d.getFullYear() === new Date().getFullYear()
    ? `${md} ${pad2(d.getHours())}:${pad2(d.getMinutes())}`
    : `${d.getFullYear()}-${md}`
}
const fmtWsTimeFull = (v) => (wsModTime(v) ? formatDateTime(v) : '')

// 按后缀决定图标与颜色：工作区里一眼分辨；认不出的一律中性灰的 Document
const FILE_TYPES = [
  { test: /\.(png|jpe?g|gif|webp|svg|bmp|ico|avif)$/i, icon: Picture, color: '#67c23a' },
  { test: /\.(mp4|mov|avi|mkv|webm|flv|m4v)$/i, icon: VideoCamera, color: '#9254de' },
  { test: /\.(mp3|wav|flac|m4a|aac|ogg)$/i, icon: Headset, color: '#eb2f96' },
  { test: /\.(zip|rar|7z|tar|gz|bz2|xz)$/i, icon: Box, color: '#8c6d1f' },
  { test: /\.(docx?|xlsx?|pptx?)$/i, icon: Tickets, color: '#2f9e44' },
  { test: /\.pdf$/i, icon: Document, color: '#f5222d' },
  { test: /\.(json|ya?ml|toml|ini|csv|tsv|xml)$/i, icon: DataLine, color: '#13c2c2' },
  { test: /\.(js|mjs|cjs|ts|tsx|jsx|vue|go|py|rb|php|java|kt|c|cc|cpp|h|hpp|cs|rs|sh|bash|ps1|sql|html?|css|scss|less|lua)$/i, icon: DocumentCopy, color: '#409eff' },
]
const fileMeta = (name) => FILE_TYPES.find((t) => t.test.test(name || '')) || { icon: Document, color: '#a8abb2' }

// 列表：目录排在一起、文件排在一起（目录在前，组内按名称数字感知排序），并给每项带上图标与颜色
const decorateWsEntries = (list) =>
  list
    .map((e) => {
      const meta = e.is_dir ? { icon: Folder, color: '#f0a020' } : fileMeta(e.name)
      return { ...e, icon: meta.icon, color: meta.color }
    })
    .sort((a, b) => {
      if (!!a.is_dir !== !!b.is_dir) return a.is_dir ? -1 : 1
      return String(a.name).localeCompare(String(b.name), locale.value, { numeric: true, sensitivity: 'base' })
    })

// ===== 会话空间用量 =====
// 后端只读计数（不扫目录），所以可以随列表刷新顺手拉一次；失败不打扰用户。
const wsUsage = ref({ used: 0, limit: -1 })
const wsUsageLoadedAt = ref(0)
const fmtWsBytes = (n) => {
  if (!n || n < 0) return '0 B'
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB']
  let v = n
  let i = 0
  while (v >= 1024 && i < units.length - 1) { v /= 1024; i++ }
  return `${v >= 10 || i === 0 ? Math.round(v) : v.toFixed(1)} ${units[i]}`
}
const wsUsageText = computed(() => {
  const { used, limit } = wsUsage.value
  if (!limit || limit === -1) return '' // 未配置上限：不显示，免得给一个没有意义的数字
  return `${fmtWsBytes(used)} / ${fmtWsBytes(limit)}`
})
const wsUsagePct = computed(() => {
  const { used, limit } = wsUsage.value
  if (!limit || limit <= 0) return 0
  return Math.min(100, Math.round((used / limit) * 100))
})
const loadWsUsage = async () => {
  const { sessionId } = sessionContext
  if (!sessionId) return
  const token = getToken()
  if (!token) return
  try {
    const res = await fetch(adminApi.sessions.usage(sessionId), {
      headers: { Authorization: `Bearer ${token}` }
    })
    const data = await res.json().catch(() => ({}))
    if (data.code === 200 && data.data) {
      wsUsage.value = { used: data.data.used_bytes || 0, limit: data.data.limit_bytes ?? -1 }
      wsUsageLoadedAt.value = Date.now()
    }
  } catch { /* 用量条是辅助信息，不因它弹错 */ }
}

// 预检已经删掉的旧文件：无论最终写入成功还是 507，都要说出来 ——
// 否则文件像是凭空消失的，用户下一步会去找它们
const warnEvicted = (data) => {
  const n = Array.isArray(data && data.evicted) ? data.evicted.length : 0
  if (n) ElMessage.warning(t('workbench.evicted', { n }))
}

// 507：空间满时给一条可操作的引导，而不是只丢一句红字
const showQuotaHint = async (msg) => {
  try {
    await ElMessageBox.confirm(
      t('workbench.quotaMsg', { msg }),
      t('workbench.quotaTitle'),
      {
        type: 'warning',
        confirmButtonText: t('workbench.quotaClear'),
        cancelButtonText: t('workbench.quotaLater')
      }
    )
  } catch { return }
  await wsClearAll()
}

const wsList = async (p = wsPath.value) => {
  const { agentId, sessionId } = sessionContext
  if (!agentId || !sessionId) return
  const token = getToken()
  if (!token) return
  const url = adminApi.agents.sessionFiles(agentId, sessionId, p || '')
  try {
    const res = await fetch(url, { headers: { Authorization: `Bearer ${token}` } })
    const data = await res.json()
    if (data.code === 200 && data.data) {
      wsPath.value = data.data.path || ''
      wsEntries.value = decorateWsEntries(data.data.entries || [])
      wsLoadedAt.value = Date.now()
      // 刷新后把已经不存在的选择剔掉，别对着"幽灵条目"发删除
      const alive = new Set(wsEntries.value.map((e) => e.path))
      wsSelected.value = wsSelected.value.filter((p) => alive.has(p))
      // 票据 / data: 载荷都绑定的是具体路径与内容，换目录后缓存必然失效
      wsTicketCache.clear()
      wsDataCache.clear()
      wsDataPending.clear()
      // 顺手刷新用量条：5 秒内已经拉过就跳过，避免连续切目录打出成串请求
      if (Date.now() - wsUsageLoadedAt.value > 5000) loadWsUsage()
    }
  } catch (e) {
    console.error('列出工作区文件失败:', e)
  }
}

const wsOpen = (p) => {
  // 换目录就把勾选清空 —— 上一层的选择对下一层没有意义
  wsSelected.value = []
  wsList(p)
}

// ===== 来自会话窗口的「打开这个文件」请求 =====
// Chat 里的「本轮产出」chip 只负责写 sessionContext（openPath + openNonce 递增），
// 真正的打开动作在这里：文件 → 开一个文件标签；路径以 / 结尾表示目录 → 进那个目录。
// 用 nonce 而不是 watch(path)：同一个文件连点两次也要有反应。
// 从会话窗口点「不可预览」产物时的定位高亮：进到所在目录后把那一行选出来，几秒后自动淡出
const wsFocusPath = ref('')
const wsListRef = ref(null)
let wsFocusTimer = null

const focusWsEntry = (fullPath) => {
  wsFocusPath.value = fullPath
  if (wsFocusTimer) clearTimeout(wsFocusTimer)
  wsFocusTimer = setTimeout(() => { wsFocusPath.value = '' }, 4000)
}

// wsList 是异步的：等条目渲染出来再滚动，否则找不到元素
watch(wsEntries, async () => {
  if (!wsFocusPath.value) return
  await nextTick()
  const el = wsListRef.value && wsListRef.value.querySelector(`[data-path="${wsFocusPath.value}"]`)
  if (el && el.scrollIntoView) el.scrollIntoView({ block: 'nearest' })
})

const ensureWorkspaceTab = () => {
  const exist = tabs.value.find((t) => t.type === 'workspace')
  if (exist) {
    activeId.value = exist.id
    return exist.id
  }
  // 固定 id：复用已有工作区标签，点产物不该每次都多开一个工作区页
  return open({ id: 'workspace:main', type: 'workspace', title: t('workbench.workspaceTabTitle') })
}

watch(() => sessionContext.openNonce, () => {
  const raw = String(sessionContext.openPath || '').trim()
  if (!raw) return
  // 归一化成工作区相对路径：容忍 /workspace/xxx、/xxx、./xxx 这几种写法
  const clean = raw.replace(/^\/+/, '').replace(/^(\.\/)+/, '')
  if (!clean) return

  // 目录：进目录即可
  if (clean.endsWith('/')) {
    ensureWorkspaceTab()
    wsOpen(clean.replace(/\/+$/, ''))
    return
  }

  const name = clean.split('/').pop()
  // 可预览（markdown / 文本 / 图片 / PDF）：直接开预览标签
  if (wsFileKind(name) !== 'binary') {
    openWsFile({ path: clean, name })
    return
  }

  // 不可预览（docx / xlsx / zip / 音视频…）：不下载，只把工作区带到所在目录并选中该行
  const dir = clean.includes('/') ? clean.slice(0, clean.lastIndexOf('/')) : ''
  ensureWorkspaceTab()
  wsOpen(dir)
  focusWsEntry(clean)
})

// 删掉之后把受影响的预览标签一并关掉：删文件关它自己，删目录关该前缀下所有，
// 否则标签条上会留一个点开是空的页
const closeWsTabsUnder = (paths) => {
  const hit = (p) => paths.some((d) => p === d || p.startsWith(`${d}/`))
  tabs.value
    .filter((t) => t.type === 'wsfile' && t.payload && hit(t.payload.path || ''))
    .forEach((t) => close(t.id))
}

// 删除工作区条目（产物清理）。目录是递归删除且不可恢复，
// 确认框里必须把「类型 + 完整路径」写全，别让人凭文件名猜。
const wsRemove = async (e) => {
  const { agentId, sessionId } = sessionContext
  if (!agentId || !sessionId) return
  const kind = e.is_dir ? t('workbench.deleteKindDir') : t('workbench.deleteKindFile')
  try {
    await ElMessageBox.confirm(
      t('workbench.deleteConfirm', { kind, path: e.path }),
      t('workbench.confirmDeleteTitle'),
      {
        type: 'warning',
        confirmButtonText: t('workbench.delete'),
        cancelButtonText: t('workbench.cancel')
      }
    )
  } catch { return }
  const token = getToken()
  if (!token) return
  try {
    const url = adminApi.agents.sessionFile(agentId, sessionId, e.path)
    const res = await fetch(url, { method: 'DELETE', headers: { Authorization: `Bearer ${token}` } })
    const data = await res.json().catch(() => ({}))
    if (data.code !== 200) throw new Error(formatError(data) || t('workbench.deleteFailed'))
    ElMessage.success(t('workbench.deleted', { path: e.path }))
    closeWsTabsUnder([e.path])
    await wsList()
  } catch (err) {
    ElMessage.error(err.message || t('workbench.deleteFailed'))
  }
}

// 重命名 / 移动：只改名字、不换目录 —— 换目录属于「移动」，留给将来的专门入口，
// 否则在改名框里多敲一个 / 就会把文件静默挪走。
// 必须走专门的接口：新建接口的 content 是字符串，二进制文件（xlsx / zip / 图片）
// 根本没法用「下载再新建」改名。
const wsRename = async (e, fromRecent = false) => {
  const { agentId, sessionId } = sessionContext
  if (!agentId || !sessionId) return
  let name = ''
  try {
    const { value } = await ElMessageBox.prompt(
      t('workbench.renameTitle', { path: e.path }),
      t('workbench.rename'),
      {
        inputValue: e.name,
        inputPlaceholder: t('workbench.renamePlaceholder'),
        confirmButtonText: t('workbench.rename'),
        cancelButtonText: t('workbench.cancel'),
        inputValidator: (v) => {
          const s = String(v || '').trim()
          if (!s) return t('workbench.nameRequired')
          if (s.includes('/')) return t('workbench.invalidSlash')
          if (s.startsWith('.')) return t('workbench.invalidDot')
          if ([...s].length > 120) return t('workbench.invalidLong')
          return true
        }
      }
    )
    name = String(value || '').trim()
  } catch { return }
  if (name === e.name) return
  const dir = dirOf(e.path)
  const newPath = dir ? `${dir}/${name}` : name
  const token = getToken()
  if (!token) return
  try {
    const res = await fetch(adminApi.agents.sessionRename(agentId, sessionId), {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${token}` },
      body: JSON.stringify({ path: e.path, new_path: newPath })
    })
    const data = await res.json().catch(() => ({}))
    if (data.code !== 200) throw new Error(formatError(data) || t('workbench.renameFailed'))
    ElMessage.success(t('workbench.renamed', { path: newPath }))
    // 旧路径上的一切都作废：预览标签是按路径绑定的，拖出缓存也按路径存
    closeWsTabsUnder([e.path])
    wsDataCache.delete(e.path)
    wsTicketCache.delete(e.path)
    if (fromRecent) await loadRecent()
    await wsList()
  } catch (err) {
    ElMessage.error(err.message || t('workbench.renameFailed'))
  }
}

// 最近文件没有多选态，直接预读；点删除图标不必把文件内容读一遍
const onWsRecentPointerDown = (f, ev) => {
  if (ev && ev.target && ev.target.closest && ev.target.closest('.wb-ws-del, .wb-ws-edit')) return
  if (typeof f.size === 'number' && f.size >= 0 && f.size <= DATA_DRAG_MAX) ensureDragData(f)
  prewarmWsTicket(f)
}

// 最近文件删一条：与工作区同一个删除接口（path 已是完整相对路径），
// 删完刷新最近列表，并清掉该路径的拖出缓存 —— 否则下次拖动还会拿到已删文件的内容
const wsRemoveRecent = async (f) => {
  const { agentId, sessionId } = sessionContext
  if (!agentId || !sessionId) return
  try {
    await ElMessageBox.confirm(
      t('workbench.deleteFileConfirm', { path: f.path }),
      t('workbench.confirmDeleteTitle'),
      {
        type: 'warning',
        confirmButtonText: t('workbench.delete'),
        cancelButtonText: t('workbench.cancel')
      }
    )
  } catch { return }
  const token = getToken()
  if (!token) return
  try {
    const url = adminApi.agents.sessionFile(agentId, sessionId, f.path)
    const res = await fetch(url, { method: 'DELETE', headers: { Authorization: `Bearer ${token}` } })
    const data = await res.json().catch(() => ({}))
    if (data.code !== 200) throw new Error(formatError(data) || t('workbench.deleteFailed'))
    ElMessage.success(t('workbench.deleted', { path: f.path }))
    // 已打开的预览标签也要关掉，否则留一个点开是空的页
    closeWsTabsUnder([f.path])
    wsDataCache.delete(f.path)
    wsTicketCache.delete(f.path)
    await loadRecent()
    // 工作区面板可能正停在同一个目录：列表非空才刷新，空目录本来就没有残留
    if (wsEntries.value.length) await wsList()
  } catch (err) {
    ElMessage.error(err.message || t('workbench.deleteFailed'))
  }
}

// 一键清空最近文件：目标就是「列表里现在这些条目」（后端只回最近 30 条，更深目录的不会出现在这里）。
// 与单条删除同一个接口，逐条发；失败项不静默，提示里带出来让人手动重试。
const wsClearRecent = async () => {
  const { agentId, sessionId } = sessionContext
  if (!agentId || !sessionId || wsClearing.value) return
  const paths = recentFiles.value.map((f) => f.path)
  if (!paths.length) return
  try {
    await ElMessageBox.confirm(
      t('workbench.clearRecentConfirm', { n: paths.length }),
      t('workbench.clearConfirmTitle'),
      {
        type: 'warning',
        confirmButtonText: t('workbench.clear'),
        cancelButtonText: t('workbench.cancel')
      }
    )
  } catch { return }
  const token = getToken()
  if (!token) return
  wsClearing.value = true
  const failed = []
  try {
    for (const p of paths) {
      try {
        const url = adminApi.agents.sessionFile(agentId, sessionId, p)
        const res = await fetch(url, { method: 'DELETE', headers: { Authorization: `Bearer ${token}` } })
        const data = await res.json().catch(() => ({}))
        if (data.code !== 200) failed.push(p)
      } catch {
        failed.push(p)
      }
    }
  } finally {
    wsClearing.value = false
  }
  const oks = paths.filter((p) => !failed.includes(p))
  closeWsTabsUnder(oks)
  oks.forEach((p) => {
    wsDataCache.delete(p)
    wsTicketCache.delete(p)
  })
  await loadRecent()
  // 工作区面板可能正停在同一个目录：列表非空才刷新，空目录本来就没有残留
  if (wsEntries.value.length) await wsList()
  if (failed.length) {
    ElMessage.error(t('workbench.clearPartial', {
      ok: oks.length, failed: failed.length, list: failed.join(t('common.listJoiner'))
    }))
  } else {
    ElMessage.success(t('workbench.clearedFiles', { n: oks.length }))
  }
}

// 批量删除：后端没有批量接口，逐条发 DELETE；失败的不计入成功数，最后一个提示汇总
const wsRemoveSelected = async () => {
  const { agentId, sessionId } = sessionContext
  if (!agentId || !sessionId || !wsSelected.value.length) return
  const paths = [...wsSelected.value]
  try {
    await ElMessageBox.confirm(
      t('workbench.batchDeleteConfirm', { n: paths.length }),
      t('workbench.batchDeleteTitle'),
      {
        type: 'warning',
        confirmButtonText: t('workbench.delete'),
        cancelButtonText: t('workbench.cancel')
      }
    )
  } catch { return }
  const token = getToken()
  if (!token) return
  const failed = []
  for (const p of paths) {
    try {
      const url = adminApi.agents.sessionFile(agentId, sessionId, p)
      const res = await fetch(url, { method: 'DELETE', headers: { Authorization: `Bearer ${token}` } })
      const data = await res.json().catch(() => ({}))
      if (data.code !== 200) failed.push(p)
    } catch {
      failed.push(p)
    }
  }
  const oks = paths.filter((p) => !failed.includes(p))
  closeWsTabsUnder(oks)
  wsSelected.value = failed // 只留下失败项，方便直接重试
  await wsList()
  if (failed.length) {
    ElMessage.error(t('workbench.batchPartial', {
      ok: oks.length, failed: failed.length, list: failed.join(t('common.listJoiner'))
    }))
  } else {
    ElMessage.success(t('workbench.batchDeleted', { n: oks.length }))
    // 目录删空了就顺手退出多选，省得停在一个没有内容的勾选态
    if (!wsEntries.value.length) wsSelectMode.value = false
  }
}

// 一键清空：清的是「当前浏览的目录」——在根目录点，就是整个工作区。
// 与多选删除同一个删除接口，只是目标改成该目录下的全部条目，省去逐条勾选。
const wsClearAll = async () => {
  const { agentId, sessionId } = sessionContext
  if (!agentId || !sessionId || wsClearing.value) return
  const paths = wsEntries.value.map((e) => e.path)
  if (!paths.length) return
  const where = wsPath.value ? `/${wsPath.value}` : t('workbench.clearAllWhereRoot')
  try {
    await ElMessageBox.confirm(
      t('workbench.clearAllConfirm', { where, n: paths.length }),
      t('workbench.clearConfirmTitle'),
      {
        type: 'warning',
        confirmButtonText: t('workbench.clear'),
        cancelButtonText: t('workbench.cancel')
      }
    )
  } catch { return }
  const token = getToken()
  if (!token) return
  wsClearing.value = true
  const failed = []
  try {
    // 后端没有批量接口，逐条发 DELETE；失败的不计入成功数
    for (const p of paths) {
      try {
        const url = adminApi.agents.sessionFile(agentId, sessionId, p)
        const res = await fetch(url, { method: 'DELETE', headers: { Authorization: `Bearer ${token}` } })
        const data = await res.json().catch(() => ({}))
        if (data.code !== 200) failed.push(p)
      } catch {
        failed.push(p)
      }
    }
  } finally {
    wsClearing.value = false
  }
  const oks = paths.filter((p) => !failed.includes(p))
  // 被清掉的条目如果正开着预览标签，一并关掉
  closeWsTabsUnder(oks)
  // 失败的留成选中项并进多选态：可以直接点「删除」重试
  wsSelected.value = failed
  wsSelectMode.value = failed.length > 0
  await wsList()
  if (failed.length) {
    ElMessage.error(t('workbench.clearAllPartial', { ok: oks.length, failed: failed.length }))
  } else {
    ElMessage.success(t('workbench.clearAllDone', { where, n: oks.length }))
  }
}

// ===== 新建文件 / 目录 =====
// 落点 = 当前浏览的目录（与上传一致，所见即所得）；名字里带 / 表示多级子目录，
// 中间层由存储层自然建出（本地 WriteFile 会 MkdirAll 父链，S3 的前缀本身就是目录）。
// 冲突一律报错、不静默改名：名字是用户显式给的，悄悄改名比报错更难用 ——
// 要覆盖已有文件请先删（有二次确认）再建。
const wsNewOpen = ref(false)
const wsNewName = ref('untitled.md')
const wsNewContent = ref('')
const wsNewSaving = ref(false)

// 名字校验：去掉首尾空白与首尾 /；不允许空段、. / ..、. 开头（后端同样会挡）
const validateWsNewName = (raw) => {
  const v = String(raw || '').trim().replace(/^\/+|\/+$/g, '')
  if (!v) return t('workbench.nameRequired')
  const segs = v.split('/')
  if (segs.some((s) => !s || s === '.' || s === '..')) return t('workbench.invalidPath')
  if (segs.some((s) => s.startsWith('.'))) return t('workbench.invalidDot')
  if (segs.some((s) => [...s].length > 120)) return t('workbench.segLong')
  if (segs.length > 16) return t('workbench.tooDeep')
  return true
}

// 真正发请求的一层：成功返回完整的相对路径（含当前目录前缀），失败抛错
const wsCreateEntry = async (rawName, isDir, content = '') => {
  const { agentId, sessionId } = sessionContext
  if (!agentId || !sessionId) return ''
  const rel = String(rawName || '').trim().replace(/^\/+|\/+$/g, '')
  const full = wsPath.value ? `${wsPath.value}/${rel}` : rel
  const token = getToken()
  if (!token) return ''
  const res = await fetch(
    adminApi.agents.sessionFile(agentId, sessionId),
    {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${token}` },
      body: JSON.stringify({ path: full, is_dir: isDir, content: isDir ? '' : content })
    }
  )
  const data = await res.json().catch(() => ({}))
  if (data.code !== 200) {
    // 判「同名已存在」按 error 编码，不比对中文 / 英文文案（改文案不该改行为）
    const err = new Error(data.error === 'agent.file_exists'
      ? t('workbench.exists', { path: full })
      : (formatError(data) || t('workbench.createFailed')))
    err.code = data.code // 507 = 会话空间满，调用方据此给「去清理」引导
    err.data = data.data // 507 时带 evicted：预检已经删掉的文件，必须一并告诉用户
    throw err
  }
  return full
}

// 共用收尾：刷新列表 + 提示；只有直接落在当前目录的文件才顺手开标签，
// 多层路径（docs/notes.md）把完整路径写在提示里就够了，别把列表切走
const afterWsCreated = async (full, isDir) => {
  ElMessage.success(t('workbench.created', { path: full }))
  await wsList()
  const name = full.split('/').pop()
  const inCurrentDir = full === (wsPath.value ? `${wsPath.value}/${name}` : name)
  // binary 类（zip/docx…）不预览：openWsFile 会直接触发下载，刚建的空壳没必要下一遍
  if (!isDir && inCurrentDir && wsFileKind(name) !== 'binary') openWsFile({ path: full, name })
}

const wsCreateDir = async () => {
  let name = ''
  try {
    const { value } = await ElMessageBox.prompt(
      wsPath.value
        ? t('workbench.newDirPrompt', { path: wsPath.value })
        : t('workbench.newDirPromptRoot'),
      t('workbench.newDirTitle'),
      {
        confirmButtonText: t('workbench.create'),
        cancelButtonText: t('workbench.cancel'),
        inputValue: t('workbench.newDirDefault'),
        inputPlaceholder: t('workbench.newDirPlaceholder'),
        inputValidator: validateWsNewName
      }
    )
    name = value
  } catch {
    return // 取消
  }
  try {
    const full = await wsCreateEntry(name, true)
    if (full) await afterWsCreated(full, true)
  } catch (err) {
    ElMessage.error(err.message || t('workbench.createFailed'))
  }
}

const wsCreateFile = async () => {
  const bad = validateWsNewName(wsNewName.value)
  if (bad !== true) {
    ElMessage.warning(bad)
    return
  }
  wsNewSaving.value = true
  try {
    const full = await wsCreateEntry(wsNewName.value, false, wsNewContent.value)
    if (!full) return
    wsNewOpen.value = false
    await afterWsCreated(full, false)
  } catch (err) {
    if (err.code === 507) {
      warnEvicted(err.data)
      await showQuotaHint(err.message)
    } else ElMessage.error(err.message || t('workbench.createFailed'))
  } finally {
    wsNewSaving.value = false
  }
}

const onWsNewCommand = (cmd) => {
  if (cmd === 'dir') {
    wsCreateDir()
    return
  }
  wsNewName.value = 'untitled.md'
  wsNewContent.value = ''
  wsNewOpen.value = true
}

// ===== 上传到当前目录 =====
// 落点 = 当前浏览的目录（所见即所得），不再固定堆进 uploads/。
// 不覆盖同名文件：后端 uniqueUploadRel 会另存为 name-2.ext —— 覆盖等于删掉 agent 的产物，
// 那种事必须走删除（有二次确认），不能藏在上传里悄悄发生。
const wsFileInputRef = ref(null)
const wsUploading = ref(false)

const pickWsFiles = () => {
  if (wsUploading.value) return
  wsFileInputRef.value && wsFileInputRef.value.click()
}
const onWsFilesPicked = async (ev) => {
  const files = Array.from(ev.target.files || [])
  ev.target.value = '' // 清掉，否则连续选同一个文件不会触发 change
  await uploadToWsDir(files)
}
const uploadToWsDir = async (files) => {
  const { agentId, sessionId } = sessionContext
  if (!agentId || !sessionId || !files || !files.length) return
  const token = getToken()
  if (!token) return
  const dir = wsPath.value
  const where = dir ? `/${dir}` : t('workbench.root')
  wsUploading.value = true
  let ok = 0
  let quotaMsg = ''
  let quotaData = null
  const failed = []
  try {
    for (const f of files) {
      try {
        const fd = new FormData()
        fd.append('file', f)
        fd.append('session_id', sessionId)
        fd.append('dir', dir) // 空串 = 工作区根，后端会兜底成 uploads/ 的只有 chat 那条不传 dir 的路径
        const res = await fetch(adminApi.agents.uploadSessionFile(agentId), {
          method: 'POST',
          headers: { Authorization: `Bearer ${token}` },
          body: fd
        })
        const data = await res.json().catch(() => ({}))
        if (data.code !== 200) {
          if (data.code === 507) {
            quotaMsg = formatError(data) || t('workbench.quotaFull')
            quotaData = data.data
          }
          throw new Error(formatError(data) || t('workbench.uploadFailed'))
        }
        warnEvicted(data.data)
        ok++
      } catch {
        failed.push(f.name)
      }
    }
  } finally {
    wsUploading.value = false
  }
  if (failed.length) {
    ElMessage.error(t('workbench.uploadPartial', {
      ok, where, failed: failed.length, list: failed.join(t('common.listJoiner'))
    }))
  } else {
    ElMessage.success(t('workbench.uploaded', { n: ok, where }))
  }
  await wsList()
  if (quotaMsg) {
    warnEvicted(quotaData) // 已删除但仍放不下：先说删了什么，再说「去清理」
    await showQuotaHint(quotaMsg)
  }
}

// 拖拽上传：dragleave 在子元素之间移动时会连续触发，必须用计数器防抖，
// 否则高亮会一闪一闪（直接布尔值是最常见的写法错误）。
const wsDragDepth = ref(0)
const wsDragActive = computed(() => wsDragDepth.value > 0)
const wsCarriesFiles = (ev) => {
  const types = ev.dataTransfer && ev.dataTransfer.types
  return !!types && Array.from(types).includes('Files')
}
const onWsDragOver = (ev) => {
  if (!wsCarriesFiles(ev)) return
  if (ev.dataTransfer) ev.dataTransfer.dropEffect = 'copy'
}
const onWsDragEnter = (ev) => {
  if (wsCarriesFiles(ev)) wsDragDepth.value++
}
const onWsDragLeave = () => {
  wsDragDepth.value = Math.max(0, wsDragDepth.value - 1)
}
const onWsDrop = async (ev) => {
  wsDragDepth.value = 0
  const dt = ev.dataTransfer
  if (!dt) return
  // 拖进来的是文件夹：明确拒绝，不做递归展开 ——
  // 上传是「投喂文件」，递归展开就变成「批量建目录树」了，跟这个面板的边界冲突
  const items = Array.from(dt.items || [])
  const hasDir = items.some((it) => {
    const entry = it.webkitGetAsEntry && it.webkitGetAsEntry()
    return !!(entry && entry.isDirectory)
  })
  if (hasDir) {
    ElMessage.warning(t('workbench.dropFolder'))
    return
  }
  const files = Array.from(dt.files || [])
  if (files.length) await uploadToWsDir(files)
}

// ===== 拖出到桌面 / 资源管理器 =====
// 只有 Chrome / Edge 支持 DownloadURL；Firefox 会退化成拖出一段路径文本，
// 所以这是「下载按钮」的补充，不是替代。
//
// 载荷格式是 "mime:文件名:URL" 三段式（URL 段允许自带 http:// 的冒号，文件名段不允许）。
// setData 只能在 dragstart 的同步执行里调用 —— 一旦有 await，拖拽已经开始，写了也是空操作。
// 所以两种载荷都得提前备好，dragstart 里只做同步取用：
//   1) 小文件：把字节读成 data: URL。拖出时浏览器不再访问服务器，
//      不受票据过期 / 403 / 反向代理影响，是最稳的一条；
//   2) 大文件：短时票据 URL（60s），浏览器拖出时自己流式下载。
let wsPrewarmTimer = null

// 小文件走 data: URL 的上限：再大就别整段塞进拖拽载荷了（占内存，跨进程传输也慢）
const DATA_DRAG_MAX = 4 << 20

// 扩展名 → MIME。不能一律写 application/octet-stream：
// MIME 对不上内容时，Windows 落盘可能退化成一个 .url 快捷方式而不是文件本身
const MIME_BY_EXT = {
  txt: 'text/plain', log: 'text/plain', ini: 'text/plain', conf: 'text/plain', toml: 'text/plain',
  md: 'text/markdown', markdown: 'text/markdown', csv: 'text/csv', tsv: 'text/tab-separated-values',
  json: 'application/json', yml: 'text/yaml', yaml: 'text/yaml', xml: 'application/xml',
  html: 'text/html', htm: 'text/html', css: 'text/css', js: 'text/javascript', mjs: 'text/javascript',
  png: 'image/png', jpg: 'image/jpeg', jpeg: 'image/jpeg', gif: 'image/gif', webp: 'image/webp',
  svg: 'image/svg+xml', bmp: 'image/bmp', ico: 'image/x-icon', avif: 'image/avif',
  pdf: 'application/pdf', zip: 'application/zip', gz: 'application/gzip', tar: 'application/x-tar',
  '7z': 'application/x-7z-compressed', rar: 'application/vnd.rar',
  mp4: 'video/mp4', webm: 'video/webm', mov: 'video/quicktime',
  mp3: 'audio/mpeg', wav: 'audio/wav', flac: 'audio/flac', m4a: 'audio/mp4', ogg: 'audio/ogg',
  doc: 'application/msword', xls: 'application/vnd.ms-excel', ppt: 'application/vnd.ms-powerpoint',
  docx: 'application/vnd.openxmlformats-officedocument.wordprocessingml.document',
  xlsx: 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet',
  pptx: 'application/vnd.openxmlformats-officedocument.presentationml.presentation'
}
const mimeOf = (name) => {
  const s = String(name || '')
  const i = s.lastIndexOf('.')
  return (i > 0 && MIME_BY_EXT[s.slice(i + 1).toLowerCase()]) || 'application/octet-stream'
}

// 文件名既要当 Windows 文件名，又夹在 DownloadURL 的三段式中间：
// 冒号会拆坏格式，其余非法字符 Windows 会自己剔除（这里一并做掉，免得落盘名和预期不一致）
const dragFileName = (name) => {
  const s = String(name || 'file')
    .replace(/[\\/:*?"<>|\u0000-\u001f]/g, '_')
    .replace(/[. ]+$/, '')
  return s || 'file'
}

// 鼠标划过整列会逐个触发 pointerenter，所以悬停预热要带静置延迟（否则一个目录扫过去就是几十次签票）。
// pointerdown 则立刻预热 —— 它比 dragstart 早得多，够跑完一次签票。
const prewarmWsTicket = (e, delay = 0) => {
  if (!e || e.is_dir) return
  const { agentId, sessionId } = sessionContext
  if (!agentId || !sessionId) return
  const hit = wsTicketCache.get(e.path)
  // 留 10s 余量：拖起来之后浏览器还要走一趟请求，票不能刚到就过期
  if (hit && hit.exp * 1000 > Date.now() + 10000) return
  const run = async () => {
    try {
      wsTicketCache.set(e.path, await fetchTicketUrl({ agentId, sessionId, path: e.path }))
    } catch {
      // 预热失败不打扰用户：真拖的时候会提示一次
    }
  }
  clearTimeout(wsPrewarmTimer)
  if (delay > 0) wsPrewarmTimer = setTimeout(run, delay)
  else run()
}

// 小文件：把字节读成 data: URL 载荷（按下时预备，dragstart 里同步取）。
// 用登录态 XHR 拿字节 —— 走的还是下载接口，不额外签票。
const ensureDragData = async (e) => {
  const { agentId, sessionId } = sessionContext
  if (!agentId || !sessionId) return
  if (typeof e.size !== 'number' || e.size < 0 || e.size > DATA_DRAG_MAX) return
  if (wsDataCache.has(e.path) || wsDataPending.has(e.path)) return
  const token = getToken()
  if (!token) return
  wsDataPending.add(e.path)
  try {
    const url = adminApi.agents.sessionDownload(agentId, sessionId, e.path)
    const res = await fetch(url, { headers: { Authorization: `Bearer ${token}` } })
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
    wsDataCache.set(e.path, `${mimeOf(name)}:${name}:${dataUrl}`)
  } catch {
    // 备不成不打扰：dragstart 还有票据这条路
  } finally {
    wsDataPending.delete(e.path)
  }
}

// 按下即预备：小文件备 data: 载荷，大文件签票（两条路谁先成用谁）
const onWsItemPointerDown = (e, ev) => {
  if (!e || e.is_dir || wsSelectMode.value) return
  // 删除按钮的 pointerdown 也会冒泡到这里：点删除没必要去把文件内容读一遍
  if (ev && ev.target && ev.target.closest && ev.target.closest('.wb-ws-del, .wb-ws-edit')) return
  // 两条路都备上：data: 备失败（接口 4xx / 读盘失败）时还能退回票据
  if (typeof e.size === 'number' && e.size >= 0 && e.size <= DATA_DRAG_MAX) ensureDragData(e)
  prewarmWsTicket(e)
}

const onWsItemDragStart = (ev, e) => {
  if (!ev.dataTransfer || e.is_dir) return
  const name = dragFileName(e.name)
  // 先清空：拖拽元素自带的默认载荷（行内文本）会干扰落点的类型判断
  ev.dataTransfer.clearData()
  ev.dataTransfer.effectAllowed = 'copy'
  // 1) data: 载荷：拖出时浏览器不再访问服务器
  const prepared = wsDataCache.get(e.path)
  if (prepared) {
    ev.dataTransfer.setData('DownloadURL', prepared)
    console.debug('[drag-out] data-url', e.path)
    return
  }
  // 2) 票据 URL：必须是**绝对 URL** —— apiBase 是 /nexus 这种相对前缀（见 config/base.js），
  // 而 Chrome 碰到相对地址会静默忽略 DownloadURL（表现就是"拖到桌面什么也没发生"）
  const hit = wsTicketCache.get(e.path)
  if (hit && hit.exp * 1000 > Date.now() + 5000) {
    const abs = new URL(hit.url, window.location.origin).href
    // 只设 DownloadURL：再塞一个 text/plain，部分 Chrome 版本会优先按文本处理，
    // 结果就是拖出来的是链接而不是文件
    ev.dataTransfer.setData('DownloadURL', `${mimeOf(name)}:${name}:${abs}`)
    console.debug('[drag-out] ticket-url', abs)
    return
  }
  ElMessage.warning(t('workbench.dragNotReady'))
  ev.dataTransfer.setData('text/plain', `/${e.path}`)
}

// 诊断用：dropEffect 为 none 说明落点根本没接受这份数据（浏览器层面就拒了）；
// 为 copy 说明浏览器已发起下载 —— 那问题在服务端（票据 / 403 / 文件已删）
const onWsItemDragEnd = (ev) => {
  if (ev.dataTransfer) console.debug('[drag-out] dropEffect:', ev.dataTransfer.dropEffect)
}

// 回到上一级：路径末段去掉一段。已在根目录时无动作（按钮本身也不渲染）
const wsUp = () => {
  const segs = wsSegs.value
  if (!segs.length) return
  wsOpen(segs.slice(0, -1).join('/'))
}

// 父目录（用于最近文件列表里的次要提示）：根目录下的文件返回空串
const dirOf = (p) => {
  const s = String(p || '')
  const i = s.lastIndexOf('/')
  return i > 0 ? s.slice(0, i) : ''
}

// 「新生成的文件」：后端扫根目录 + 一层子目录，按 mtime 倒序给前 30 条
const loadRecent = async () => {
  const { agentId, sessionId } = sessionContext
  if (!agentId || !sessionId) return
  const token = getToken()
  if (!token) return
  recentLoading.value = true
  try {
    const url = adminApi.agents.sessionRecent(agentId, sessionId, 30)
    const res = await fetch(url, { headers: { Authorization: `Bearer ${token}` } })
    const data = await res.json()
    if (data.code === 200 && data.data) recentFiles.value = data.data.files || []
  } catch (err) {
    console.error('列出最近文件失败:', err)
  } finally {
    recentLoading.value = false
  }
}

// 点文件：能预览的开一个与「工作区」平级的标签（同一文件只开一个，靠稳定 id 复用）；
// 不能预览的（doc/xlsx/ppt/zip/音视频…）直接下载 —— 不为一个下载按钮再开个标签，
// 那等于让用户白点两次。
// payload 里带 agentId / sessionId 快照：会话切走之后，旧标签读的仍是它当初那个会话的文件。
const openWsFile = (e) => {
  const { agentId, sessionId } = sessionContext
  if (!agentId || !sessionId) return
  if (wsFileKind(e.name) === 'binary') {
    downloadFile({ agentId, sessionId, path: e.path })
    return
  }
  open({
    id: `wsfile:${sessionId}:${e.path}`,
    type: 'wsfile',
    title: e.name,
    payload: { agentId, sessionId, path: e.path, name: e.name, content: '', error: '', loading: true }
  })
}

// 文件按「能不能当文本读」分成四类：text 走读取接口；image / pdf 走下载接口拿 blob 内联预览；
// 其它（doc/xlsx/zip/音视频…）不解析，只给下载 —— 浏览器本来也渲染不了。
const TEXT_EXT = /\.(txt|md|markdown|csv|tsv|json|ya?ml|toml|ini|conf|cfg|env|log|xml|sql|srt|vtt|js|mjs|cjs|ts|tsx|jsx|vue|go|py|rb|php|java|kt|c|cc|cpp|h|hpp|cs|rs|swift|sh|bash|zsh|ps1|bat|lua|html?|css|scss|less)$/i
const IMAGE_EXT = /\.(png|jpe?g|gif|webp|svg|bmp|ico|avif)$/i
const PDF_EXT = /\.pdf$/i
const MD_EXT = /\.(md|markdown|mdx)$/i
const wsFileKind = (name) => {
  const n = String(name || '')
  if (IMAGE_EXT.test(n)) return 'image'
  if (PDF_EXT.test(n)) return 'pdf'
  if (MD_EXT.test(n)) return 'markdown'
  if (TEXT_EXT.test(n)) return 'text'
  // 没有扩展名（Dockerfile / LICENSE / Makefile）按文本处理，读失败再退回下载
  return n.includes('.') ? 'binary' : 'text'
}

// 每个文件标签的 objectURL 登记在这，重读与关标签时释放，避免 blob 泄漏
const wsBlobUrls = new Map()
const revokeWsBlob = (tabId) => {
  const u = wsBlobUrls.get(tabId)
  if (u) {
    URL.revokeObjectURL(u)
    wsBlobUrls.delete(tabId)
  }
}

// 读取标签内容（首次打开与「重新读取」共用）。失败把原因留在标签里，不弹全局提示
const loadWsFileTab = async (t) => {
  if (!t || t.type !== 'wsfile' || !t.payload) return
  const token = getToken()
  if (!token) return
  const { agentId, sessionId, path: p } = t.payload
  const kind = wsFileKind(t.payload.name)
  t.payload.kind = kind
  t.payload.loading = true
  t.payload.error = ''
  revokeWsBlob(t.id)
  t.payload.blobUrl = ''

  // Markdown 默认按渲染后的样子看（与智能体编辑器的 md 预览一致），可切回源码
  if (kind === 'markdown' && t.payload.preview === undefined) t.payload.preview = true

  try {
    if (kind === 'text' || kind === 'markdown') {
      const res = await fetch(adminApi.agents.sessionFile(agentId, sessionId, p), { headers: { Authorization: `Bearer ${token}` } })
      const data = await res.json()
      if (data.code === 200 && data.data) {
        const c = data.data.content || ''
        // 无扩展名的文件一律按文本读（Dockerfile / LICENSE / Makefile）：真塞的是二进制（含 NUL）
        // 就退回「不支持预览」，免得用户对着满屏乱码还要自己去找下载按钮。
        if (kind === 'text' && c.includes('\u0000')) {
          t.payload.kind = 'binary'
          t.payload.content = ''
          return
        }
        t.payload.content = c
      } else {
        t.payload.content = ''
        // 常见原因：超过 2 MiB 的查看上限、文件已被删除
        t.payload.error = formatError(data) || t('workbench.readFailed')
      }
      return
    }

    if (kind === 'binary') {
      // 不预取内容：可能几十 MB，等用户点下载再说
      t.payload.content = ''
      return
    }

    // image / pdf：拿 blob 生成 objectURL 给 <img> / <iframe>
    const res = await fetch(adminApi.agents.sessionDownload(agentId, sessionId, p), { headers: { Authorization: `Bearer ${token}` } })
    if (!res.ok) {
      const body = await res.json().catch(() => ({}))
      throw new Error(formatError(body) || t('workbench.readFailed'))
    }
    const blob = await res.blob()
    const url = URL.createObjectURL(blob)
    wsBlobUrls.set(t.id, url)
    t.payload.blobUrl = url
    t.payload.size = blob.size
  } catch (err) {
    t.payload.content = ''
    t.payload.error = err.message || t('workbench.readFailed')
    revokeWsBlob(t.id)
    t.payload.blobUrl = ''
  } finally {
    t.payload.loading = false
  }
}

// 下载：先签一张短时票，再 window.location 导航 —— 交给浏览器下载管理器。
// 好处是流式写盘、有进度条、支持断点续传、内存零拷贝（不再 fetch 整个文件成 blob）。
const downloadWsFile = (t) => {
  if (!t || t.type !== 'wsfile' || !t.payload) return
  downloadFile(t.payload)
}

// 签一张短时下载票并拼成导航 URL（票 60s 有效，见 admin/agents/download_ticket.go）。
// 下载按钮与「拖出到桌面」共用这一条路径，避免两处各写一遍 URL 拼接。
const fetchTicketUrl = async ({ agentId, sessionId, path: p }) => {
  const token = getToken()
  if (!token) throw new Error(t('workbench.notLoggedIn'))
  const res = await fetch(adminApi.agents.downloadTicket(agentId), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${token}` },
    body: JSON.stringify({ session_id: sessionId, path: p })
  })
  const body = await res.json().catch(() => ({}))
  if (body.code !== 200 || !body.data) throw new Error(formatError(body) || t('workbench.downloadFailed'))
  const d = body.data
  const qs = new URLSearchParams({
    session_id: sessionId,
    path: d.path || p,
    schema: d.schema,
    exp: String(d.exp),
    sig: d.sig
  })
  return { url: `${adminApi.agents.ticketDownload(agentId)}?${qs.toString()}`, exp: Number(d.exp) || 0 }
}

// downloadFile 是下载的唯一实现：标签里的下载按钮、以及「点不可预览的文件」共用这一条路径。
const downloadFile = async ({ agentId, sessionId, path: p }) => {
  if (!agentId || !sessionId || !p) return
  try {
    const { url } = await fetchTicketUrl({ agentId, sessionId, path: p })
    // 用隐藏 iframe 触发下载：attachment 响应会直接落盘，
    // 而万一失败（票据过期/文件已删），错误页也只出现在那个 iframe 里，不会把整个应用顶掉
    const iframe = document.createElement('iframe')
    iframe.style.display = 'none'
    iframe.src = url
    document.body.appendChild(iframe)
    setTimeout(() => iframe.remove(), 60000)
  } catch (err) {
    ElMessage.error(err.message || t('workbench.downloadFailed'))
  }
}

// 标签被关掉时释放它占用的 blob
watch(
  () => tabs.value.map((t) => t.id),
  (ids) => {
    const alive = new Set(ids)
    for (const id of Array.from(wsBlobUrls.keys())) {
      if (!alive.has(id)) revokeWsBlob(id)
    }
  }
)

onUnmounted(() => {
  for (const id of Array.from(wsBlobUrls.keys())) revokeWsBlob(id)
})

// ===== 工作区刷新策略 =====
// 以前是「切回标签就 wsList('')」，等于每次都把用户踢回根目录 —— 打开的子目录「又关上了」。
// 现在的口径：
//   1. 会话变了 → 目录作废，回根目录重载（唯一会重置路径的情况）；
//   2. 面板还没加载过（wsLoadedAt = 0）→ 按当前目录加载一次；
//   3. 数据超过 WS_STALE_MS → 静默重拉「当前目录」（保留现场，只更新列表）；
//   4. 切回标签本身不刷新 —— 想看最新就点工具条上的刷新按钮。
const WS_STALE_MS = 30 * 1000

watch(
  () => [active.value && active.value.type, sessionContext.sessionId],
  (next, prev) => {
    // immediate 首次执行时 prev 是 undefined，不能直接解构（会 TypeError）
    const [type, sid] = next || []
    const prevSid = prev ? prev[1] : undefined
    if (!active.value) return
    if (type === 'workspace') {
      if (prevSid !== sid) {
        wsOpen('') // 换会话：目录作废
        return
      }
      if (!wsLoadedAt.value) {
        wsList() // 首次进入：按当前目录加载（wsPath 默认 ''）
        return
      }
      if (Date.now() - wsLoadedAt.value > WS_STALE_MS) wsList()
      return
    }
    if (type === 'custom' && active.value.payload && active.value.payload.kind === 'recent') loadRecent()
  },
  { immediate: true }
)

// 切到文件标签时重读一次：会话里可能又改过这个文件（同一个文件重复点击不会再新建标签）
watch(
  () => active.value && active.value.id,
  () => {
    if (!active.value) return
    if (active.value.type === 'wsfile') loadWsFileTab(active.value)
    else if (active.value.type === 'custom' && active.value.payload && active.value.payload.kind === 'recent') loadRecent()
  }
)

const openRecentFiles = () => open({
  id: 'recent',
  type: 'custom',
  title: t('workbench.recentFiles'),
  payload: { kind: 'recent' }
})

// 规范化地址：没写协议就补 http://
const normalizeUrl = (raw) => {
  const s = String(raw || '').trim()
  if (!s) return ''
  return /^https?:\/\//i.test(s) ? s : `http://${s}`
}

// 开一个新的「浏览器」工作台标签。地址栏回车 / 「新标签打开」/ 页内链接拦截都走这里。
const openBrowserTab = (raw) => {
  const url = normalizeUrl(raw)
  if (!url) return
  open({ type: 'browser', title: url, payload: { url } })
}

// 启动页「打开浏览器」：先问地址，再开工作台标签
const openBrowser = async () => {
  try {
    const { value } = await ElMessageBox.prompt(
      t('workbench.browserPrompt'),
      t('workbench.browserTitle'),
      {
        inputPlaceholder: 'https://example.com',
        inputValue: 'http://localhost:3000',
        confirmButtonText: t('workbench.browserOpen'),
        cancelButtonText: t('workbench.cancel')
      }
    )
    openBrowserTab(value)
  } catch {
    /* 用户取消 */
  }
}

// iframe 只有「同源」（协议 / 主机 / 端口都一致）时才能被父页面读到并拦截：
// 把页内 target=_blank 和 window.open 改道到工作台新标签。
// 跨域时浏览器禁止读取，拦不住 —— 那种情况只能用上面的地址栏 / 「新标签打开」。
const onFrameLoad = (ev) => {
  const win = ev.target && ev.target.contentWindow
  if (!win) return
  let doc
  try {
    doc = win.document
  } catch {
    return // 跨域，放弃
  }
  win.open = (url) => {
    openBrowserTab(url)
    return win
  }
  doc.addEventListener('click', (e) => {
    const a = e.target && e.target.closest && e.target.closest('a[target="_blank"]')
    if (!a) return
    e.preventDefault()
    openBrowserTab(a.href)
  }, true)
}
</script>

<style scoped>
.workbench {
  flex-shrink: 0;
  min-width: 0;
  background-color: #fafafa;
  display: flex;
  flex-direction: column;
  overflow: hidden;
  /* 工作台永远贴在外面板（.chat-panel，12px）的右边缘，右上/右下角跟着面板走圆弧，
     不能靠外面板的裁剪兜底（会话关掉后右边还有一条 5px 拖拽条会把它顶离边缘）。 */
  border-radius: 0 12px 12px 0;
}

/* 分割线：会话可见时在左侧（对话区↔工作台）；工作台单独存在时在右侧（工作台↔内容区） */
.workbench--divider-left {
  border-left: 1px solid #e8e8e8;
}

.workbench--divider-right {
  border-right: 1px solid #e8e8e8;
  /* 会话关掉后右边那条 5px 拖拽条是浮在边缘上的：让它不再把工作台顶开，
     工作台吃掉这 5px（弹性撑满 + 负边距），右上角的圆弧才能正好落在面板边缘上。 */
  flex: 1 1 auto;
  margin-right: -5px;
}

/* ===== 标签条 ===== */
/* 整条：高度 + 白底 + 底边（与左侧 .chat-toolbar 对齐成一条） */
.wb-tabbar {
  display: flex;
  align-items: stretch;
  height: 64px;                  /* 与左侧会话头部（.session-new 的 12px 外边距 + 按钮）同高 */
  box-sizing: border-box;
  flex-shrink: 0;
  /* 顶部留 8px：标签行整体下移，与左侧 .chat-toolbar 内容对齐。
     必须放在整条上而不是 .wb-tabs 里 —— 关闭按钮（.wb-close）在 .wb-tabs 之外，
     留白放在里面时它按整条 64px 居中，会比标签行 / 「+」高 4px。 */
  padding-top: 8px;
  border-bottom: 1px solid #f0f0f0;
  background-color: #ffffff;
}

/* 标签滚动区（右侧给关闭按钮留出固定位置，标签再多也不会把它挤走） */
.wb-tabs {
  flex: 1;
  min-width: 0;
  display: flex;
  align-items: center;
  gap: 2px;
  padding: 0 8px;                /* 顶部的 8px 已交给 .wb-tabbar，这里只管左右 */
  overflow-x: auto;
  overflow-y: hidden;
  scrollbar-width: none;
}

.wb-tabs::-webkit-scrollbar {
  display: none;
}

.wb-tab {
  display: flex;
  align-items: center;                     /* 文字垂直居中 */
  align-self: stretch;                     /* 撑满标签条高度，下划线贴到底边 */
  gap: 6px;
  max-width: 160px;
  padding: 0 10px;                         /* 高度交给标签条 */
  border-radius: 0;                        /* 下划线式不用圆角 */
  font-size: 14px;                         /* 与左侧 .session-new（新对话）一致 */
  color: #333;                             /* 与左侧 .session-new 一致 */
  cursor: pointer;
  white-space: nowrap;
  /* 可压缩：宽度不够时先缩短标题，而不是把整个标签推到可视区外（那会把 X 一起裁掉）。
     88px 是「图标 + 省略号标题 + X」的最小可用宽度。 */
  flex: 0 1 auto;
  min-width: 88px;
  border-bottom: 2px solid transparent;    /* 占位，避免激活时高度跳动 */
  transition: color 0.15s, border-color 0.15s;
}

.wb-tab:hover {
  color: #1890ff;
}

/* 激活态：蓝字加粗 */
.wb-tab.active {
  color: #1890ff;
  font-weight: 600;
}

.wb-tab-icon {
  font-size: 14px;                         /* 图标随文字一起对齐 .session-new */
  flex-shrink: 0;
}

.wb-tab-title {
  flex: 1 1 auto;
  min-width: 0;                            /* flex 里允许缩到 0，ellipsis 才真正生效 */
  overflow: hidden;
  text-overflow: ellipsis;
}

/* 关闭按钮：命中区 14px → 20px（padding 撑大，负 margin 抵掉视觉尺寸变化），
   窄宽度下更好点，也更不容易点错到标签本身 */
.wb-tab-close {
  flex-shrink: 0;
  font-size: 13px;
  color: #bbb;
  border-radius: 50%;
  padding: 3px;
  margin: -3px -2px -3px 0;
}

.wb-tab-close:hover {
  color: #f56c6c;
  background-color: #fdecec;
}

.wb-tab-add {
  flex-shrink: 0;
  width: 26px;
  height: 26px;
  margin-left: 2px;
  border: none;
  border-radius: 6px;
  background: transparent;
  color: #888;
  cursor: pointer;
  display: flex;
  align-items: center;
  justify-content: center;
}

.wb-tab-add:hover {
  background-color: #f2f3f5;
  color: #1890ff;
}

/* 关闭整个工作台（对应会话工具的 ×） */
.wb-close {
  flex-shrink: 0;
  align-self: center;
  width: 26px;
  height: 26px;
  margin-right: 8px;
  border: none;
  border-radius: 6px;
  background: transparent;
  color: #888;
  cursor: pointer;
  display: flex;
  align-items: center;
  justify-content: center;
}

.wb-close:hover {
  background-color: #f2f3f5;
  color: #f56c6c;
}

/* ===== 内容区 ===== */
.wb-body {
  flex: 1;
  min-height: 0;
  overflow: auto;
  display: flex;
  flex-direction: column;
}

/* 启动页 */
.wb-home {
  padding: 24px 18px;
}

.wb-home-title {
  font-size: 13px;
  color: #999;
  margin-bottom: 14px;
}

.wb-home-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(120px, 1fr));
  gap: 12px;
}

.wb-opt {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 10px;
  padding: 22px 12px;
  border: 1px solid #ebeef5;
  border-radius: 10px;
  background: #fff;
  color: #555;
  font-size: 13px;
  cursor: pointer;
  transition: all 0.2s;
}

.wb-opt:hover {
  border-color: #1890ff;
  color: #1890ff;
  box-shadow: 0 2px 8px rgba(24, 144, 255, 0.12);
}

.wb-opt-icon {
  font-size: 22px;
}

/* 浏览器 */
.wb-browser {
  flex: 1;
  display: flex;
  flex-direction: column;
  min-height: 0;
}

.wb-frame {
  flex: 1;
  width: 100%;
  border: none;
  background: #fff;
}

/* 工作区（会话运行场地，只读） */
.wb-ws {
  flex: 1;
  min-height: 0;
  display: flex;
  flex-direction: column;
  overflow: hidden;
}

.wb-ws-bar {
  display: flex;
  align-items: center;
  gap: 4px;
  padding: 8px 12px;
  border-bottom: 1px solid #f0f0f0;
  /* 与技能编辑左侧（Skills.vue 的 .side-bar）同字号：两处都是「面包屑 + 操作」的一条 bar，
     一边 14px 一边 12.5px 会显得不是同一套控件 */
  font-size: 12.5px;
  color: #666;
  flex-wrap: wrap;
  flex-shrink: 0;
}

/* 根目录：胶囊按钮，点击目标 ≥ 24px 高（原来是一个 "/" 字符，宽度只有几像素）。
   字号与技能编辑左侧（.side-root）一致 */
.wb-ws-root {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 4px 10px;
  border: 1px solid #e8e8e8;
  border-radius: 999px;
  background: #fafafa;
  color: #666;
  font: inherit;
  font-size: 12px;
  line-height: 1;
  cursor: pointer;
}
.wb-ws-root:hover {
  color: #1890ff;
  border-color: #bedaff;
  background: #f0f7ff;
}
/* 当前就在根目录：高亮，但仍然可点 */
.wb-ws-root.active {
  color: #1890ff;
  background: #f0f7ff;
  border-color: #d6e9ff;
}

.wb-ws-sep {
  color: #ccc;
}

/* 各层级：加内边距 + hover 底色，单字母目录也好点；超长名省略，完整路径在 title 里 */
.wb-ws-crumb {
  max-width: 180px;
  padding: 4px 6px;
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
.wb-ws-crumb:hover {
  color: #1890ff;
  background: #f0f7ff;
}
.wb-ws-crumb.active {
  color: #333;
  font-weight: 500;
}

/* 右侧操作区（上一级 / 刷新）：margin-left:auto 从单个图标挪到容器上 */
.wb-ws-actions {
  margin-left: auto;
  display: inline-flex;
  align-items: center;
  gap: 10px;
}

/* 会话空间用量：只读提示，不占按钮位 */
.wb-ws-quota {
  font-size: 12px;
  color: #909399;
  white-space: nowrap;
  font-variant-numeric: tabular-nums;
}
.wb-ws-quota.warn {
  color: #f56c6c;
  font-weight: 500;
}
.wb-ws-up,
.wb-ws-refresh {
  cursor: pointer;
  color: #999;
  font-size: 14px;
}
.wb-ws-up:hover,
.wb-ws-refresh:hover {
  color: #1890ff;
}

.wb-ws-list {
  flex: 1;
  min-height: 0;
  overflow: auto;
  padding: 6px 0;
  /* 滚动条只在鼠标进入列表时出现：默认透明（窄条仍占位，hover 时不产生布局抖动） */
  scrollbar-width: thin;
  scrollbar-color: transparent transparent;
  transition: scrollbar-color 0.2s;
}
.wb-ws-list:hover {
  scrollbar-color: #c8c9cc transparent;
}
/* WebKit：Chrome / Edge / Safari */
.wb-ws-list::-webkit-scrollbar {
  width: 6px;
  height: 6px;
}
.wb-ws-list::-webkit-scrollbar-thumb {
  border-radius: 3px;
  background: transparent;
}
.wb-ws-list:hover::-webkit-scrollbar-thumb {
  background: #c8c9cc;
}
.wb-ws-list::-webkit-scrollbar-thumb:hover {
  background: #b1b3b8;
}
.wb-ws-list::-webkit-scrollbar-track {
  background: transparent;
}

.wb-ws-item {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 7px 14px;
  font-size: 14px;
  color: #444;
  cursor: pointer;
}

.wb-ws-item:hover {
  background-color: #f5f7fa;
}

/* 图标：形状与颜色由 decorateWsEntries 按类型给出（内联 style 覆盖此处颜色） */
.wb-ws-item .el-icon {
  flex-shrink: 0;
  font-size: 16px;
  color: #a8abb2;
}

.wb-ws-name {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.wb-ws-size {
  flex-shrink: 0;
  color: #bbb;
  font-size: 12.5px;
  white-space: nowrap;
}

/* 修改时间：等宽数字，右对齐，和大小分两列（面板窄时也不会把文件名挤没） */
.wb-ws-time {
  flex-shrink: 0;
  min-width: 88px;
  text-align: right;
  color: #c0c4cc;
  font-size: 12px;
  font-variant-numeric: tabular-nums;
}

/* 删除按钮：常态半透明（触屏上也能看见、够得着），行悬停才显形。
   刻意不做成"悬停才出现"——删除是破坏性操作，藏起来反而更容易误触旁边的行 */
.wb-ws-item .wb-ws-del {
  flex-shrink: 0;
  font-size: 14px;
  color: #c0c4cc;
  opacity: 0.35;
  transition: opacity 0.15s, color 0.15s;
}
.wb-ws-item:hover .wb-ws-del {
  opacity: 1;
}
.wb-ws-item .wb-ws-del:hover {
  color: #f56c6c;
}

/* 重命名按钮：与删除同排同尺寸，但只是中性操作 —— 悬停给蓝色而不是红色 */
.wb-ws-item .wb-ws-edit {
  flex-shrink: 0;
  font-size: 14px;
  color: #c0c4cc;
  opacity: 0.35;
  transition: opacity 0.15s, color 0.15s;
}
.wb-ws-item:hover .wb-ws-edit {
  opacity: 1;
}
.wb-ws-item .wb-ws-edit:hover {
  color: #409eff;
}

/* 多选入口：轻量文字按钮，与「上一级 / 刷新」图标同一排 */
.wb-ws-selbtn {
  border: none;
  background: none;
  padding: 2px 6px;
  font-size: 12px;
  color: #909399;
  border-radius: 4px;
  cursor: pointer;
  white-space: nowrap;
}
.wb-ws-selbtn:hover {
  background: #f0f2f5;
  color: #409eff;
}
.wb-ws-selbtn.active {
  color: #409eff;
  background: #ecf5ff;
}
/* 清空是破坏性操作：hover 用危险色，和「多选」区分开 */
.wb-ws-clear:not(:disabled):hover {
  color: #f56c6c;
  background: #fef0f0;
}

/* 「新建」下拉：去掉 el-dropdown 触发器默认的行高，和旁边的文字按钮对齐 */
.wb-ws-new {
  line-height: 1;
}
.wb-ws-new .el-dropdown-link {
  outline: none;
}

/* 新建文件对话框：左侧标签 + 右侧输入，两行之间留一点呼吸 */
.wb-newrow {
  display: flex;
  align-items: flex-start;
  gap: 10px;
  margin-bottom: 8px;
}
.wb-newrow .wb-newlabel {
  flex-shrink: 0;
  width: 40px;
  padding-top: 6px;
  font-size: 13px;
  color: #606266;
}
.wb-newhint {
  margin: 0 0 14px 50px;
  font-size: 12px;
  color: #909399;
}

/* 多选操作条 */
.wb-ws-selbar {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 6px 14px;
  border-bottom: 1px solid #f0f0f0;
  background: #fafafa;
  font-size: 12.5px;
  color: #666;
}
.wb-ws-selbar .wb-ws-selcount {
  flex: 1;
  min-width: 0;
  color: #909399;
}

/* 行内勾选框：只做展示，点击交给整行（关掉 pointer-events 避免与行点击双触发） */
.wb-ws-item .wb-ws-check {
  pointer-events: none;
  height: auto;
}
/* 选中行给底色：一眼看出哪些会被删 */
.wb-ws-item.selecting.checked {
  background-color: #ecf5ff;
}

/* 从会话窗口点产物定位过来的那一行：左侧色条 + 淡蓝底，4 秒后由 wsFocusPath 清空自动淡出 */
.wb-ws-item.focused {
  background-color: #ecf5ff;
  box-shadow: inset 3px 0 0 #409eff;
}

/* 上传：原生 input 藏起来，由按钮触发 */
.wb-ws-fileinput {
  display: none;
}
.wb-ws-selbtn:disabled {
  opacity: 0.6;
  cursor: default;
}

/* 拖拽落点提示：与多选操作条同一位置、同一套底纹，视觉上属于同一层 */
.wb-ws-dropbar {
  padding: 6px 14px;
  border-bottom: 1px solid #d9ecff;
  background: #f0f7ff;
  font-size: 12.5px;
  color: #409eff;
}

/* 拖拽经过列表：整块虚线高亮，明确"松手就落在这个目录" */
.wb-ws-list.drag-over {
  outline: 2px dashed #409eff;
  outline-offset: -6px;
  background-color: #f7fbff;
}

/* 最近文件列表：标题 + 每行末尾的父目录提示 */
.wb-recent-title {
  color: #666;
  font-size: 13px;
}

.wb-recent-dir {
  margin-left: 6px;
  color: #c0c4cc;
  font-size: 12px;
}

.wb-ws-empty {
  padding: 18px;
  color: #bbb;
  font-size: 13px;
  text-align: center;
}

/* 工作区文件标签（与「工作区」平级）：整块占满标签内容区，头部显示路径 + 重新读取 */
.wb-wsfile {
  flex: 1;
  min-height: 0;
  display: flex;
  flex-direction: column;
  background: #fff;
}

.wb-wsfile-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  padding: 7px 14px;
  font-size: 14px;
  color: #888;
  border-bottom: 1px solid #f5f5f5;
  flex-shrink: 0;
}

.wb-wsfile-path {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.wb-wsfile-tools {
  display: inline-flex;
  align-items: center;
  gap: 10px;
  flex-shrink: 0;
}

.wb-wsfile-refresh,
.wb-wsfile-download,
.wb-wsfile-toggle {
  cursor: pointer;
}

.wb-wsfile-refresh:hover,
.wb-wsfile-download:hover,
.wb-wsfile-toggle:hover,
.wb-wsfile-toggle.active {
  color: #1890ff;
}

.wb-wsfile-tip {
  padding: 24px 12px;
  gap: 6px;
}

/* 图片：居中留白，过大时等比缩到容器内（不撑破面板） */
.wb-wsfile-image {
  flex: 1;
  min-height: 0;
  max-width: 100%;
  object-fit: contain;
  padding: 12px;
  background:
    linear-gradient(45deg, #fafafa 25%, transparent 25%),
    linear-gradient(-45deg, #fafafa 25%, transparent 25%),
    linear-gradient(45deg, transparent 75%, #fafafa 75%),
    linear-gradient(-45deg, transparent 75%, #fafafa 75%);
  background-size: 16px 16px;
  background-position: 0 0, 0 8px, 8px -8px, -8px 0;
}

/* PDF：直接交给浏览器内置阅读器 */
.wb-wsfile-frame {
  flex: 1;
  min-height: 0;
  width: 100%;
  border: none;
  background: #fff;
}

.wb-wsfile-body {
  flex: 1;
  min-height: 0;
  margin: 0;
  padding: 12px 14px;
  overflow: auto;
  font-size: 14px;
  line-height: 1.8;
  color: #333;
  white-space: pre-wrap;
  word-break: break-word;
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  /* 滚动条：默认透明（窄槽占位，hover 不抖），鼠标进入正文才上色 */
  scrollbar-width: thin;
  scrollbar-color: transparent transparent;
  transition: scrollbar-color 0.2s;
}
.wb-wsfile-body:hover {
  scrollbar-color: rgba(0, 0, 0, 0.2) transparent;
}
.wb-wsfile-body::-webkit-scrollbar {
  width: 8px;
  height: 8px;   /* 长行横向滚动也用同一条细条 */
}
.wb-wsfile-body::-webkit-scrollbar-thumb {
  background: transparent;
  border-radius: 4px;
}
.wb-wsfile-body:hover::-webkit-scrollbar-thumb {
  background: rgba(0, 0, 0, 0.2);
}
.wb-wsfile-body::-webkit-scrollbar-thumb:hover {
  background: rgba(0, 0, 0, 0.35);
}
.wb-wsfile-body::-webkit-scrollbar-track {
  background: transparent;
}

/* Markdown 渲染预览（只读）：与智能体编辑器的 .md-preview 同一套排版口径。
   内容由 v-html 注入，没有 scoped 属性，元素样式必须走 :deep()。 */
.wb-wsfile-md {
  flex: 1;
  min-height: 0;
  overflow: auto;
  padding: 12px 14px;
  font-size: 14px;
  line-height: 1.75;
  color: #333;
  word-break: break-word;
  scrollbar-width: thin;
  scrollbar-color: transparent transparent;
}
.wb-wsfile-md:hover {
  scrollbar-color: rgba(0, 0, 0, 0.2) transparent;
}
.wb-wsfile-md::-webkit-scrollbar { width: 8px; }
.wb-wsfile-md::-webkit-scrollbar-thumb { background: transparent; border-radius: 4px; }
.wb-wsfile-md:hover::-webkit-scrollbar-thumb { background: rgba(0, 0, 0, 0.2); }

.wb-wsfile-md :deep(p) { margin: 0 0 8px; }
.wb-wsfile-md :deep(p:last-child) { margin-bottom: 0; }
.wb-wsfile-md :deep(h1),
.wb-wsfile-md :deep(h2),
.wb-wsfile-md :deep(h3),
.wb-wsfile-md :deep(h4),
.wb-wsfile-md :deep(h5),
.wb-wsfile-md :deep(h6) { margin: 14px 0 8px; line-height: 1.35; font-weight: 600; }
.wb-wsfile-md :deep(h1) { font-size: 20px; }
.wb-wsfile-md :deep(h2) { font-size: 17px; }
.wb-wsfile-md :deep(h3) { font-size: 15.5px; }
.wb-wsfile-md :deep(h4),
.wb-wsfile-md :deep(h5),
.wb-wsfile-md :deep(h6) { font-size: 14.5px; }
.wb-wsfile-md :deep(ul),
.wb-wsfile-md :deep(ol) { margin: 0 0 8px; padding-left: 22px; }
.wb-wsfile-md :deep(li) { margin: 2px 0; }
.wb-wsfile-md :deep(a) { color: #409eff; text-decoration: none; }
.wb-wsfile-md :deep(a:hover) { text-decoration: underline; }
.wb-wsfile-md :deep(code) {
  font-family: ui-monospace, Menlo, Consolas, monospace;
  font-size: 13px;
  background: #f2f3f5;
  border-radius: 4px;
  padding: 1px 5px;
}
.wb-wsfile-md :deep(pre) {
  margin: 0 0 8px;
  padding: 10px 12px;
  background: #f6f7f9;
  border: 1px solid #ebeef5;
  border-radius: 6px;
  overflow-x: auto;
}
.wb-wsfile-md :deep(pre code) { background: transparent; padding: 0; line-height: 1.55; }
.wb-wsfile-md :deep(blockquote) {
  margin: 0 0 8px;
  padding: 2px 0 2px 10px;
  border-left: 3px solid #dcdfe6;
  color: #606266;
}
.wb-wsfile-md :deep(table) { border-collapse: collapse; margin: 0 0 8px; font-size: 13px; }
.wb-wsfile-md :deep(th),
.wb-wsfile-md :deep(td) { border: 1px solid #e4e7ed; padding: 4px 8px; }
.wb-wsfile-md :deep(th) { background: #fafafa; font-weight: 600; }
.wb-wsfile-md :deep(hr) { border: none; border-top: 1px solid #e8e8e8; margin: 12px 0; }
.wb-wsfile-md :deep(img) { max-width: 100%; border-radius: 4px; }

/* 占位 */
.wb-tip {
  flex: 1;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 6px;
  padding: 24px;
  color: #999;
  font-size: 14px;
  text-align: center;
}

.wb-tip p {
  margin: 0;
}

.wb-tip-sub {
  font-size: 12px;
  color: #bbb;
}
</style>
