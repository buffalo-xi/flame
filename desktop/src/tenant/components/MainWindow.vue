<template>
  <div class="main-window">
    <!-- 顶部导航栏-->
    <header class="top-nav">
      <!-- 左面标志 -->
      <div class="logo">
        <img src="../assets/pic/flame.png" alt="flame" class="logo-image" />
        <div class="logo-text">flame</div>
      </div>
      
      <!-- 中间操作项目 -->
      <nav class="nav-menu">
        <ul>
          <!-- 三个布局开关：会话（左）/ 工作台（右）/ 内容（主内容区），各管各的显示。
               只给图标不给文字：一是和右边「图标 + 文字」的页面入口一眼分开（这三个点了不切页），
               二是把顶部这一行的宽度让给账户区。图标语义靠 :title / aria-label 说明，
               开着时只把图标变蓝 —— 不跟页面入口那样加白底胶囊，避免和「当前所在页」混淆。 -->
          <li class="nav-group">
            <ul>
              <li class="nav-state" :class="{ 'is-on': chatVisible }">
                <a href="#" :title="chatVisible ? $t('layout.chatHide') : $t('layout.chatShow')"
                   :aria-label="chatVisible ? $t('layout.chatHide') : $t('layout.chatShow')" @click.prevent="toggleChat">
                  <span class="nav-icon">
                    <el-icon><ChatRound /></el-icon>
                  </span>
                </a>
              </li>
              <li class="nav-state" :class="{ 'is-on': wbOpen }">
                <a href="#" :title="wbOpen ? $t('layout.wbHide') : $t('layout.wbShow')"
                   :aria-label="wbOpen ? $t('layout.wbHideAria') : $t('layout.wbShow')" @click.prevent="toggleWorkbench">
                  <span class="nav-icon">
                    <el-icon><Grid /></el-icon>
                  </span>
                </a>
              </li>
              <!-- 内容区开关：收起后对话 + 工作台占满整行。与分隔条上的 ‹ / › 是同一套状态，
                   这里只是常显入口（收起后不会没有回去的路）。 -->
              <li class="nav-state" :class="{ 'is-on': !contentCollapsed }">
                <a href="#"
                   :title="contentCollapsed ? $t('layout.contentShow') : $t('layout.contentHide')"
                   :aria-label="contentCollapsed ? $t('layout.contentShowAria') : $t('layout.contentHideAria')"
                   @click.prevent="toggleContentPanel">
                  <span class="nav-icon">
                    <el-icon><Document /></el-icon>
                  </span>
                </a>
              </li>
            </ul>
          </li>
          <!-- 组二：定义与能力 -->
          <li class="nav-group">
            <ul>
              <li :class="{ active: activeNav === 'agents' }" @click="setActiveNav('agents')">
                <a href="#">
                  <span class="nav-icon">
                    <el-icon><Cpu /></el-icon>
                  </span>
                  <span class="nav-text">{{ $t('nav.agents') }}</span>
                </a>
              </li>
              <li :class="{ active: activeNav === 'agent-skills' }" @click="setActiveNav('agent-skills')">
                <a href="#">
                  <span class="nav-icon">
                    <el-icon><Collection /></el-icon>
                  </span>
                  <span class="nav-text">{{ $t('nav.skills') }}</span>
                </a>
              </li>
              <li :class="{ active: activeNav === 'capabilities' }" @click="setActiveNav('capabilities')">
                <a href="#">
                  <span class="nav-icon">
                    <el-icon><Tools /></el-icon>
                  </span>
                  <span class="nav-text">{{ $t('nav.tools') }}</span>
                </a>
              </li>
              <!-- 工作流排在工具之后：智能体 / 技能 / 工具是「可被组合的零件」，
                   工作流是把它们串起来编排，按「零件 → 编排」的顺序读 -->
              <li :class="{ active: activeNav === 'wf-instances' }" @click="setActiveNav('wf-instances')">
                <a href="#">
                  <span class="nav-icon">
                    <el-icon><Share /></el-icon>
                  </span>
                  <span class="nav-text">{{ $t('nav.workflows') }}</span>
                </a>
              </li>
            </ul>
          </li>
          <!-- 组三：运行与对外（设置 / 资产市场是低频管理项，已挪到右上账户区） -->
          <li class="nav-group">
            <ul>
              <!-- 运行中：租户级的「正在跑什么」（agent + flow，跨实例台账），只读。
                   定义域（智能体 / 工作流）在上一组，这一组只放「跑起来之后」的事 -->
              <li :class="{ active: activeNav === 'running' }" @click="setActiveNav('running')">
                <a href="#">
                  <span class="nav-icon">
                    <el-icon><Odometer /></el-icon>
                  </span>
                  <span class="nav-text">{{ $t('nav.running') }}</span>
                </a>
              </li>
              <li :class="{ active: activeNav === 'endpoints' }" @click="setActiveNav('endpoints')">
                <a href="#">
                  <span class="nav-icon">
                    <el-icon><Promotion /></el-icon>
                  </span>
                  <span class="nav-text">{{ $t('nav.endpoints') }}</span>
                </a>
              </li>
            </ul>
          </li>
        </ul>
      </nav>
      
      <!-- 右面账户设置 -->
      <div class="account-settings">
        <!-- 会话 / 内容区的布局开关已并入左侧导航组（三个开关各管各的显示） -->
        <!-- 头像：蓝底圆形 + 大写首字母（显示名与账号名在悬停 tip 里；点击复制账号） -->
        <div class="user-avatar" :title="avatarTip" @click="copyAccount">{{ avatarText }}</div>

        <!-- 设置 / 资产市场从左侧导航挪到这里：两者都是低频的管理项（改配置、跨租户上架安装），
             与「运行 / 端点」那种日常入口不是一个心智模型，占着主路径反而干扰。
             仍保留文字（「资产市场」纯图标不直观），当前在这一页时同导航一样变蓝。 -->
        <button class="logout-button" :class="{ 'is-active': activeNav === 'assets' }"
                :title="$t('nav.assetsTip')" @click="setActiveNav('assets')">
          <el-icon><Shop /></el-icon>
          <span>{{ $t('nav.assets') }}</span>
        </button>
        <button class="logout-button" :class="{ 'is-active': activeNav === 'settings' }"
                :title="$t('nav.settingsTip')" @click="setActiveNav('settings')">
          <el-icon><Setting /></el-icon>
          <span>{{ $t('nav.settings') }}</span>
        </button>

        <button class="logout-button" :title="$t('common.language')" @click="toggleLocale">
          <span>{{ nextLocaleLabel }}</span>
        </button>
        <button class="logout-button" @click="openPwdDialog">
          <el-icon><Lock /></el-icon>
          <span>{{ $t('nav.password') }}</span>
        </button>
        <button class="logout-button" @click="handleLogout">
          <el-icon><SwitchButton /></el-icon>
          <span>{{ $t('nav.logout') }}</span>
        </button>
      </div>
    </header>
    
    <!-- 主内容区域-->
    <main ref="mainContentRef" class="main-content">
      <!-- 左侧：agent 对话窗口（内容区折叠时占满整行） -->
      <Chat
        ref="chatPanelRef"
        v-show="chatPanelShown"
        class="chat-panel"
        :class="{ 'chat-panel--full': chatPanelFull }"
        :style="chatPanelShown && !chatPanelFull ? { width: chatPanelWidth + 'px' } : null"
        :chat-visible="chatVisible"
        :wb-open="wbOpen"
        :wb-width="wbWidth"
        :session-panel-open="sessionPanelOpen"
        @close-chat="chatVisible = false"
        @session-panel-change="sessionPanelOpen = $event"
        @wb-resize="startWbResize"
        @open-workbench="wbOpen = true"
        @open-entity="handleOpenEntity"
      />

      <!-- 拖拽分隔条（带折叠按钮） -->
      <div v-show="chatVisible && !contentHidden" class="chat-resizer" @mousedown="startResize">
        <button class="resizer-toggle" title="折叠内容区（对话全宽）" @click.stop="hideContent">‹</button>
      </div>

      <!-- 折叠后的展开入口（贴右边缘） -->
      <button v-show="chatVisible && contentHidden" class="resizer-toggle resizer-toggle--edge"
              title="展开内容区" @click="restoreContent">›</button>

      <!-- 右侧：现有内容（Chat 关闭或内容区折叠时隐藏） -->
      <div ref="contentPanelRef" class="content-panel" v-show="!(chatVisible && contentHidden)">
        <!-- 根据选中的导航项显示不同的内容-->
        <div v-if="activeNav === 'agents' || activeNav === 'wf-instances' || activeNav === 'agent-skills' || activeNav === 'capabilities' || activeNav === 'endpoints' || activeNav === 'settings' || activeNav === 'assets' || activeNav === 'running'">
          <keep-alive>
            <Agents v-if="activeNav === 'agents'" :focus="focus" />
            <Running v-else-if="activeNav === 'running'" />
            <Skills v-else-if="activeNav === 'agent-skills'" :focus="focus" />
            <Capabilities v-else-if="activeNav === 'capabilities'" :focus="focus" @nav="handleNav" />
            <Endpoints v-else-if="activeNav === 'endpoints'" @open="handleNav" />
            <AssetMarket v-else-if="activeNav === 'assets'" />
            <Flows v-else-if="activeNav === 'wf-instances'" :focus="focus" />
            <Settings v-else-if="activeNav === 'settings'" :tab="settingsTab" />
          </keep-alive>
        </div>
        <div v-else class="content-placeholder">
          <h2>{{ activeNav === 'models' ? 'Models' : 'Settings' }}</h2>
          <p>内容将在这里显示</p>
        </div>
      </div>
    </main>

    <!-- 修改密码（当前登录用户自助） -->
    <el-dialog v-model="pwdDialogVisible" title="修改密码" width="440px">
      <el-form label-position="top">
        <el-form-item label="当前密码">
          <el-input v-model="pwdForm.old_password" type="password" show-password placeholder="请输入当前密码" />
        </el-form-item>
        <el-form-item label="新密码">
          <el-input v-model="pwdForm.new_password" type="password" show-password placeholder="至少 8 位" />
        </el-form-item>
        <el-form-item label="确认新密码">
          <el-input v-model="pwdForm.confirm" type="password" show-password @keyup.enter="submitChangePwd" />
        </el-form-item>
        <div style="font-size:12px;color:#909399">
          修改成功后，其它端已登录的会话仍保持有效（token 不会失效）；如需立即踢下线，请让管理员停用该账号。
        </div>
      </el-form>
      <template #footer>
        <el-button @click="pwdDialogVisible = false">取消</el-button>
        <el-button type="primary" :loading="pwdSaving" @click="submitChangePwd">确定</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup>
import { ref, computed, nextTick, watch, onMounted, onUnmounted, provide } from 'vue'
import { ElIcon, ElMessage, ElMessageBox } from 'element-plus'
import { Document, Cpu, ChatRound, Setting, SwitchButton, Share, Collection, Tools, Promotion, Lock, Grid, Shop, Odometer } from '@element-plus/icons-vue'
import apiConfig from '../config/api.js'
import { getToken, getUser } from '../utils/auth.js'
import { currentLocale, setLocale } from '../../locales'
import Agents from './Agents.vue'
import AssetMarket from './AssetMarket.vue'
import Capabilities from './Capabilities.vue'
import Endpoints from './Endpoints.vue'
import Flows from './Flows.vue'
import Running from './Running.vue'
import Settings from './Settings.vue'
import Skills from './Skills.vue'
import Chat from './Chat.vue'

const emit = defineEmits(['logout'])
const activeNav = ref('agents')

// 语言切换常驻入口：登录页那个按钮只在未登录时可见，登录后得有地方能切回来。
// 按钮显示「将要切到」的语言，避免用户误以为当前就是英文界面。
const nextLocaleLabel = computed(() => (currentLocale() === 'zh-CN' ? 'EN' : '中'))
const toggleLocale = () => setLocale(currentLocale() === 'zh-CN' ? 'en' : 'zh-CN')
// 跳进设置页时希望落在哪个页签（'' = 由 Settings 自己决定，默认 llm）
const settingsTab = ref('')

// ===== 右上角：当前登录用户 + 自助修改密码 =====
const currentUser = getUser() || {}
const currentUserName = computed(() => currentUser.display_name || currentUser.name || '未登录')
// 头像里的首字母（大写）
const avatarText = computed(() => String(currentUserName.value || 'U').slice(0, 1).toUpperCase())
// 悬浮 tip：显示名 + 账号名（登录名）。显示名可以重复，账号名才是唯一身份；
// 显示名为空时上面已回退成账号名，此时不重复打印一遍。
const avatarTip = computed(() => {
  const account = String(currentUser.name || '')
  const name = account && account !== currentUserName.value
    ? `${currentUserName.value}（账号 ${account}）`
    : currentUserName.value
  return account ? `${name}，点击复制账号` : name
})

// 复制文本：优先 navigator.clipboard（需要 https 或 localhost）；
// 局域网用 http 直连时该 API 不可用，退回 execCommand('copy')。
const copyText = async (text) => {
  try {
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(text)
      return true
    }
  } catch {
    /* 落到下面的兜底 */
  }
  try {
    const ta = document.createElement('textarea')
    ta.value = text
    ta.setAttribute('readonly', '')
    ta.style.position = 'fixed'
    ta.style.left = '-9999px'
    document.body.appendChild(ta)
    ta.select()
    const ok = document.execCommand('copy')
    document.body.removeChild(ta)
    return ok
  } catch {
    return false
  }
}

// 点击头像复制账号名（登录名）
const copyAccount = async () => {
  const account = String(currentUser.name || '')
  if (!account) return
  const ok = await copyText(account)
  if (ok) {
    ElMessage.success({ message: `账号已复制：${account}`, duration: 1200 })
  } else {
    ElMessage.error('复制失败，请手动选择账号名')
  }
}

const pwdDialogVisible = ref(false)
const pwdSaving = ref(false)
const pwdForm = ref({ old_password: '', new_password: '', confirm: '' })

// 会话侧栏宽度（与 Chat.vue 的 .session-panel 保持一致）
const SESSION_PANEL_WIDTH = 200
// 分隔条宽度 / 对话内容区最小宽度 / 内容区最小宽度（拖拽上下限用）
const RESIZER_WIDTH = 5
const CHAT_MIN_WIDTH = 300   // 与工作台最小宽（WB_MIN_WIDTH）一致；Chat.vue 里 .chat-main 的 min-width 同值
const CONTENT_MIN_WIDTH = 380
// 左侧对话区宽度（不含会话侧栏；可拖拽调整）
const chatWidth = ref(300)
// 左侧对话面板是否可见
const chatVisible = ref(true)
// Chat 内部会话侧栏是否展开（由 Chat 通知）：展开时整体加宽，对话区宽度不变
const sessionPanelOpen = ref(true)

// ===== 右侧工作台（嵌在会话卡片内、对话区右侧） =====
const WB_MIN_WIDTH = 300   // 工作台最小宽度（与对话内容区一致）
const WB_DEF_WIDTH = 480   // 工作台默认宽度
const wbOpen = ref(false)  // 默认收起；下次进入按上次关闭时的状态恢复
const wbWidth = ref(WB_DEF_WIDTH)
const wbTabs = ref([])     // { id, type, title, closable, payload }
const wbActiveId = ref('')
let wbSeq = 0              // 标签 id 自增序号

// 两侧栏（左会话栏 + 右工作台）占用的总宽度
const sideTotal = computed(() =>
  (sessionPanelOpen.value ? SESSION_PANEL_WIDTH : 0) + (wbOpen.value ? wbWidth.value : 0)
)
// 会话关掉后、只要工作台还开着，Chat 面板就仍然显示
const chatPanelShown = computed(() => chatVisible.value || wbOpen.value)
// 内容区折叠（对话占满整行）只在会话可见时成立
const chatPanelFull = computed(() => chatVisible.value && contentHidden.value)
// 面板总宽 = 可见的会话部分 + 工作台（工作台拖拽条也占宽）
const chatPanelWidth = computed(() => {
  const chatPart = chatVisible.value
    ? chatWidth.value + (sessionPanelOpen.value ? SESSION_PANEL_WIDTH : 0)
    : 0
  const wbPart = wbOpen.value ? wbWidth.value + RESIZER_WIDTH : 0
  return chatPart + wbPart
})

const setActiveNav = (nav) => {
  activeNav.value = nav
  // 内容区被折叠时，点导航把它显示出来（按上次宽度 / 各占一半恢复分栏）
  restoreContent()
}

const openPwdDialog = () => {
  pwdForm.value = { old_password: '', new_password: '', confirm: '' }
  pwdDialogVisible.value = true
}

const submitChangePwd = async () => {
  const f = pwdForm.value
  if (!f.old_password) return ElMessage.warning('请输入当前密码')
  if (!f.new_password || f.new_password.length < 8) return ElMessage.warning('新密码至少 8 位')
  if (f.new_password !== f.confirm) return ElMessage.warning('两次输入的新密码不一致')
  if (f.new_password === f.old_password) return ElMessage.warning('新密码不能与当前密码相同')

  pwdSaving.value = true
  try {
    const resp = await fetch(apiConfig.admin.me.password, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', 'Authorization': `Bearer ${getToken()}` },
      body: JSON.stringify({ old_password: f.old_password, new_password: f.new_password })
    })
    const data = await resp.json().catch(() => ({}))
    if (data.code !== 200) throw new Error(data.msg || '修改失败')
    ElMessage.success({ message: '密码已修改', duration: 1200 })
    pwdDialogVisible.value = false
  } catch (e) {
    ElMessage.error(e.message || '修改失败')
  } finally {
    pwdSaving.value = false
  }
}

// 跨页跳转请求：字符串 = 只切页；对象 { nav, id, name, node_id } = 切页并定位到该实体
//（id / name 两个标识子页面都认：create_* 回 id，update_* 只回名字；node_id 有值时再定位到具体节点）。
// 每次都是新对象（含 ts），保证子页面 watch 能拿到新引用、同一实体重复点也生效。
const focus = ref(null)
const handleNav = (payload) => {
  if (!payload) return
  if (typeof payload === 'string') {
    activeNav.value = payload
    return
  }
  const next = {
    nav: payload.nav,
    id: payload.id || '',
    name: payload.name || '',
    node_id: payload.node_id || '',
    ts: Date.now()
  }
  activeNav.value = payload.nav
  // 子页面是 v-if 挂载的：等它挂上再推 focus，否则首次进入时 watch 收不到这个值
  nextTick(() => { focus.value = next })
}

// 会话里建 / 改了实体（工作流、智能体、技能、自定义工具）：
// 内容区直接切到对应页并打开被改的那个，工作流再按 node_id 定位到节点。
const handleOpenEntity = (p) => {
  if (!p || !p.nav) return
  handleNav(p)
  // 内容区被折叠时先展开，否则切了页用户也看不见
  restoreContent()
}

const handleLogout = () => {
  console.log('用户点击退出登录')
  emit('logout')
}

// ===== 左侧对话区宽度 & 内容区折叠 =====
const mainContentRef = ref(null)   // .main-content：量可用宽度（拖拽上限、各占一半）
const chatPanelRef = ref(null)     // Chat 面板：量面板实际宽度（折叠时由 flex 撑满整行）
const contentPanelRef = ref(null)  // .content-panel：量内容区实际宽度（能吸收多少拖拽位移）
// 用户手动调过的分栏宽度（Chat 总宽，含会话侧栏）；null = 没拖过，恢复时左右各占一半
const lastSplitTotal = ref(null)
// 内容区是否被折叠（true = 对话占满整行）
const contentHidden = ref(false)

// ===== 文档级拖拽：统一的 mousemove / mouseup 绑定与收尾 =====
// 1) 开始前先收掉上一次可能残留的监听：鼠标若在 iframe 上松开，父文档收不到 mouseup，
//    残留回调会和新的拖拽回调一起写宽度，表现为「宽度突然跳变 + 拖不动」；
// 2) 拖拽期间给 body 加 class，配合全局样式禁用 iframe 的指针事件，否则鼠标一旦
//    滑进工作台里的 iframe，mousemove / mouseup 会被 iframe 吞掉，拖拽直接中断。
let endActiveDrag = null
const beginDrag = (onMove, onUp) => {
  endActiveDrag?.()
  document.body.classList.add('flame-dragging')
  const move = (ev) => onMove(ev)
  const up = (ev) => { cleanup(); onUp?.(ev) }
  const cleanup = () => {
    document.removeEventListener('mousemove', move)
    document.removeEventListener('mouseup', up)
    document.body.classList.remove('flame-dragging')
    if (endActiveDrag === cleanup) endActiveDrag = null
  }
  document.addEventListener('mousemove', move)
  document.addEventListener('mouseup', up)
  endActiveDrag = cleanup
}

// 内容区当前可让出的宽度（内容区实际宽 - 最小宽）：把左侧面板拖宽时由它吸收
const contentSpareWidth = () => {
  const el = contentPanelRef.value
  if (el && el.clientWidth > 0) return Math.max(0, el.clientWidth - CONTENT_MIN_WIDTH)
  const mainEl = mainContentRef.value
  if (!mainEl) return 0
  return Math.max(0, mainEl.clientWidth - RESIZER_WIDTH - chatPanelWidth.value - CONTENT_MIN_WIDTH)
}

// 对话内容区 + 工作台 在当前面板里可分配的总宽（两者之间重分配时恒定）。
// 内容区折叠时面板被 flex 撑满整行，此时它比 chatWidth + wbWidth 记录值更宽，
// 用记录值算会让工作台（或对话区）提前到顶、拖不动，所以现场量面板实际宽度。
const chatWbTotalWidth = () => {
  const panelEl = chatPanelRef.value?.$el
  const fixedW = (sessionPanelOpen.value ? SESSION_PANEL_WIDTH : 0) + RESIZER_WIDTH
  if (panelEl && panelEl.clientWidth > 0) return panelEl.clientWidth - fixedW
  return chatWidth.value + wbWidth.value
}

// 拖拽面板与内容区之间的分割条（工作台展开时它贴着工作台右侧）。
// 一条分割条只改一个区域的宽度，多出来的宽度由右侧内容区吸收：
//   工作台展开 → 只改工作台宽度（工作台 ↔ 内容区），对话内容区完全不动
//   工作台收起 → 只改对话内容区宽度（对话面板 ↔ 内容区）
const startResize = (e) => {
  const startX = e.clientX
  const wbOn = wbOpen.value
  const startWb = wbWidth.value
  const startChat = chatWidth.value
  const spare = contentSpareWidth()

  const onMouseMove = (ev) => {
    const delta = ev.clientX - startX
    if (wbOn) {
      // 往左拖工作台变窄、往右拖变宽；上限到内容区最小宽为止
      const maxWb = Math.max(WB_MIN_WIDTH, startWb + spare)
      wbWidth.value = Math.round(Math.min(Math.max(startWb + delta, WB_MIN_WIDTH), maxWb))
    } else {
      const maxChat = Math.max(CHAT_MIN_WIDTH, startChat + spare)
      chatWidth.value = Math.round(Math.min(Math.max(startChat + delta, CHAT_MIN_WIDTH), maxChat))
    }
  }
  const onMouseUp = () => {
    // 记下用户手动调过的分栏宽度，供折叠后恢复（折叠态面板占满整行，不算数）
    if (!chatPanelFull.value) lastSplitTotal.value = chatWidth.value + sideTotal.value
  }
  beginDrag(onMouseMove, onMouseUp)
}

// 左右各占一半
const applyHalfSplit = () => {
  const el = mainContentRef.value
  if (!el) return
  const total = el.clientWidth - RESIZER_WIDTH
  const half = Math.max(CHAT_MIN_WIDTH, Math.round(total / 2))
  chatWidth.value = Math.max(CHAT_MIN_WIDTH, half - sideTotal.value)
}

// 折叠内容区（对话占满整行）。
// 折叠后面板被 flex 撑满整行，若工作台过宽会直接把对话内容区压到最小宽以下，
// 所以要等 DOM 更新后收敛一次（此时对话区/工作台的总宽由实测得出）。
const hideContent = () => {
  contentHidden.value = true
  nextTick(clampLayout)
}

// 恢复内容区：优先回到上次的分栏宽度，没有记录则左右各占一半
const restoreContent = () => {
  if (!contentHidden.value) return
  contentHidden.value = false
  nextTick(() => {
    if (lastSplitTotal.value != null) {
      chatWidth.value = Math.max(CHAT_MIN_WIDTH, lastSplitTotal.value - sideTotal.value)
    } else {
      applyHalfSplit()
    }
    // 折叠期间工作台可能被拖得很宽（占满整行时面板宽度不再由 chatWidth 决定），
    // 直接恢复会把右侧内容区挤成 0，这里再收敛一次：优先压缩工作台、保住内容区最小宽。
    clampLayout()
  })
}

// 内容区是否已关闭（= 对话 + 工作台占满整行）。只在会话可见时成立，与 chatPanelFull 同口径。
const contentCollapsed = computed(() => chatVisible.value && contentHidden.value)

// 顶栏内容区开关：关闭内容区 / 打开内容区。
// 与分隔条上的 ‹ / › 共用 contentHidden，这里只是常显入口（收起后不会没有回去的路）。
// 会话关着时左侧没有面板可占满，先把会话拉回来再折叠。
const toggleContentPanel = () => {
  if (contentHidden.value) {
    restoreContent()
    return
  }
  if (!chatVisible.value) chatVisible.value = true
  hideContent()
}

// 顶栏「会话窗口」入口：常显，开着时只变蓝字。点了就收起/打开，只动会话、不碰工作台。
const toggleChat = () => {
  chatVisible.value = !chatVisible.value
  // 会话关掉后左侧没有面板，折叠状态失去意义（否则再开会话会直接是全宽布局）
  if (!chatVisible.value) contentHidden.value = false
}

// ===== 右侧工作台：开关 / 拖拽 / 标签协议 =====
// 新建标签（启动页）：点「+」或首次打开工作台时调用
const newWorkbenchTab = () => {
  const t = { id: `wb${++wbSeq}`, type: 'home', title: '新标签', closable: true }
  wbTabs.value.push(t)
  wbActiveId.value = t.id
  wbOpen.value = true
  return t.id
}

// 程序推标签：同 id 已存在则激活，否则新建；返回标签 id
const openWorkbenchTab = (tab) => {
  wbOpen.value = true
  if (tab.id) {
    const exist = wbTabs.value.find((t) => t.id === tab.id)
    if (exist) {
      wbActiveId.value = exist.id
      return exist.id
    }
  }
  const t = { id: tab.id || `wb${++wbSeq}`, closable: true, ...tab }
  wbTabs.value.push(t)
  wbActiveId.value = t.id
  return t.id
}

const closeWorkbenchTab = (id) => {
  const i = wbTabs.value.findIndex((t) => t.id === id)
  if (i < 0) return
  wbTabs.value.splice(i, 1)
  if (wbActiveId.value === id) {
    const next = wbTabs.value[i] || wbTabs.value[i - 1]
    wbActiveId.value = next ? next.id : ''
  }
  if (!wbTabs.value.length) wbOpen.value = false // 标签关光即收起
}

const openWorkbench = () => {
  if (!wbTabs.value.length) {
    newWorkbenchTab()
    return
  }
  wbOpen.value = true
}

// 顶栏「工作台」入口：常显，开着时只变蓝字。点了就收起/打开，标签保留
const toggleWorkbench = () => {
  if (wbOpen.value) {
    wbOpen.value = false
    return
  }
  openWorkbench()
}

// 拖工作台分割线。side：'left' = 对话内容区↔工作台（会话可见）；'right' = 工作台↔内容区（会话已关）
const startWbResize = (e, side = 'left') => {
  const startX = e.clientX
  const startWb = wbWidth.value
  const total = chatWbTotalWidth()
  // 起始的对话宽度必须取「当前实际渲染值」，不能直接读 chatWidth：
  // 内容区折叠时面板被 flex 撑满整行，chatWidth 只是上次的记录值，会比实际小一大截，
  // 用它算就会在第一帧把差额从对话区抢给工作台（表现：一碰分割条对话区突然变得很小）。
  // 常态下面板宽度由 chatWidth 决定，两者本来就相等。
  const startChat = chatPanelFull.value ? total - startWb : chatWidth.value
  // 会话可见时这条分割条在对话内容区右侧：它只负责改对话内容区的宽度
  const chatSide = side === 'left' && chatVisible.value
  const spare = contentSpareWidth()
  const onMouseMove = (ev) => {
    const delta = ev.clientX - startX
    if (chatSide) {
      // 往左拖对话内容区变窄。多出来的宽度：常态由右侧内容区吸收；
      // 内容区折叠时没有可吸收的区域，只能由工作台同时让位（面板总宽恒定）。
      const maxChat = chatPanelFull.value
        ? Math.max(CHAT_MIN_WIDTH, total - WB_MIN_WIDTH)
        : Math.max(CHAT_MIN_WIDTH, startChat + spare)
      chatWidth.value = Math.round(Math.min(Math.max(startChat + delta, CHAT_MIN_WIDTH), maxChat))
      if (chatPanelFull.value) {
        wbWidth.value = Math.round(Math.max(WB_MIN_WIDTH, total - chatWidth.value))
      }
    } else {
      // 工作台右侧（会话已关）：只改工作台宽度，上限到内容区最小宽为止
      const maxWb = Math.max(WB_MIN_WIDTH, startWb + spare)
      wbWidth.value = Math.round(Math.min(Math.max(startWb + delta, WB_MIN_WIDTH), maxWb))
    }
  }
  const onMouseUp = () => {
    // 记录 Chat 面板总宽（含侧栏），供内容区折叠后恢复。
    // 内容区折叠（面板占满整行）时「总宽」等于整行宽，那不是用户选的分栏：
    // 记下来会让下次展开内容区时对话面板独占整行、内容区被挤成 0。所以跳过。
    if (chatVisible.value && !chatPanelFull.value) {
      lastSplitTotal.value = chatWidth.value + sideTotal.value
    }
  }
  beginDrag(onMouseMove, onMouseUp)
}

// 收敛分栏宽度：保证「左会话栏 + 工作台 + 对话区 + 内容区最小宽」装得下容器
// （换屏 / 改窗口大小 / 开合侧栏后都可能超出）。优先压缩工作台，其次压缩对话区。
const clampLayout = () => {
  // 内容区折叠时对话占满整行，不需要为内容区预留宽度；
  // 但工作台依然不能把对话内容区挤到 CHAT_MIN_WIDTH 以下（面板被 flex 撑满整行，
  // 超出部分会全部压到对话内容区上），所以这里只收工作台。
  if (contentHidden.value) {
    const total = chatWbTotalWidth()
    if (wbOpen.value) {
      wbWidth.value = Math.round(Math.min(wbWidth.value, Math.max(WB_MIN_WIDTH, total - CHAT_MIN_WIDTH)))
      if (chatVisible.value) {
        // 折叠时对话区宽度不由 chatWidth 决定，顺手同步成实际渲染值，
        // 免得这个「旧值」被后续的拖拽 / 恢复逻辑当成真值用
        chatWidth.value = Math.round(Math.max(CHAT_MIN_WIDTH, total - wbWidth.value))
      }
    }
    return
  }
  const el = mainContentRef.value
  if (!el) return
  const avail = el.clientWidth - RESIZER_WIDTH - CONTENT_MIN_WIDTH
  const chatOn = chatVisible.value
  const left = chatOn && sessionPanelOpen.value ? SESSION_PANEL_WIDTH : 0
  let wb = wbOpen.value ? wbWidth.value : 0
  let chat = chatOn ? chatWidth.value : 0
  const over = left + wb + chat - avail
  if (over > 0) {
    const shrinkWb = Math.min(over, Math.max(0, wb - WB_MIN_WIDTH))
    wb -= shrinkWb
    if (wbOpen.value) wbWidth.value = wb
    const rest = over - shrinkWb
    if (rest > 0 && chatOn) chat = Math.max(CHAT_MIN_WIDTH, chat - rest)
  }
  if (chatOn && chat !== chatWidth.value) chatWidth.value = chat
}

// 侧栏 / 会话开关后 Chat 总宽变化，需重新收敛，否则会把右侧内容区挤破
watch([sessionPanelOpen, wbOpen, chatVisible], () => nextTick(clampLayout))

// 供任意后代组件主动开标签（inject('workbench')）
provide('workbench', {
  tabs: wbTabs,
  activeId: wbActiveId,
  isOpen: wbOpen,
  open: openWorkbenchTab,
  close: closeWorkbenchTab,
  newTab: newWorkbenchTab,
  closePanel: () => { wbOpen.value = false },  // 收起整个工作台（标签保留）
})

// ===== 界面布置持久化：下次进入时按上次的状态恢复 =====
const LAYOUT_KEY = 'app_flame_main_window_layout'
// 可持久化的导航项（与模板里的 setActiveNav 取值一致，顺序同导航）。
// running 必须在内：漏了它，停在「运行」页刷新会回落成默认的 agents
const NAV_KEYS = ['agents', 'agent-skills', 'capabilities', 'wf-instances', 'running', 'endpoints', 'assets', 'settings']

const saveLayout = () => {
  try {
    localStorage.setItem(LAYOUT_KEY, JSON.stringify({
      activeNav: activeNav.value,
      chatWidth: chatWidth.value,
      chatVisible: chatVisible.value,
      sessionPanelOpen: sessionPanelOpen.value,
      contentHidden: contentHidden.value,
      lastSplitTotal: lastSplitTotal.value,
      wbOpen: wbOpen.value,
      wbWidth: wbWidth.value
    }))
  } catch {
    /* 隐私模式等场景下忽略 */
  }
}

// 拖拽 / 会话侧栏开关会连续触发，写入做 200ms 去抖
let saveLayoutTimer = null
const scheduleSaveLayout = () => {
  if (saveLayoutTimer) clearTimeout(saveLayoutTimer)
  saveLayoutTimer = setTimeout(saveLayout, 200)
}

const restoreLayout = () => {
  let saved = null
  try {
    const raw = localStorage.getItem(LAYOUT_KEY)
    saved = raw ? JSON.parse(raw) : null
  } catch {
    saved = null
  }
  if (!saved || typeof saved !== 'object') return

  if (NAV_KEYS.includes(saved.activeNav)) activeNav.value = saved.activeNav
  if (typeof saved.chatVisible === 'boolean') chatVisible.value = saved.chatVisible
  // 会话侧栏显隐：恢复后通过 :session-panel-open 单向驱动 Chat（见 Chat.vue）。
  // 两边必须保持一致，否则面板总宽会算错，差额全被 flex:1 的对话内容区吸收。
  if (typeof saved.sessionPanelOpen === 'boolean') sessionPanelOpen.value = saved.sessionPanelOpen
  if (typeof saved.lastSplitTotal === 'number' && Number.isFinite(saved.lastSplitTotal)) {
    lastSplitTotal.value = saved.lastSplitTotal
  }
  if (typeof saved.chatWidth === 'number' && Number.isFinite(saved.chatWidth)) {
    chatWidth.value = Math.max(CHAT_MIN_WIDTH, Math.round(saved.chatWidth))
  }
  // 对话本身是关闭状态时不能保持折叠，否则整屏空白
  contentHidden.value = saved.contentHidden === true && chatVisible.value
  // 工作台：恢复上次的开关与宽度；恢复为打开时至少要有一个标签
  if (typeof saved.wbOpen === 'boolean') wbOpen.value = saved.wbOpen
  if (typeof saved.wbWidth === 'number' && Number.isFinite(saved.wbWidth)) {
    wbWidth.value = Math.max(WB_MIN_WIDTH, Math.round(saved.wbWidth))
  }
  if (wbOpen.value && wbTabs.value.length === 0) newWorkbenchTab()

  // 首帧量到 .main-content 后收敛一次：上次的宽度可能超出当前窗口（换屏 / 改窗口大小）
  nextTick(clampLayout)
}

// 在 setup 阶段就恢复：这样首帧渲染拿到的就是上次的导航 / 侧栏 / 分栏宽度，
// 不会先按默认布局画一帧再跳过去（restoreLayout 内部用 nextTick(clampLayout) 等 DOM 就绪）。
restoreLayout()

watch([activeNav, chatWidth, chatVisible, sessionPanelOpen, contentHidden, lastSplitTotal, wbOpen, wbWidth], scheduleSaveLayout)

onMounted(() => {
  window.addEventListener('resize', clampLayout)
  // 关闭标签页 / 窗口时把待写的那次立即落盘，避免卡在 200ms 去抖窗口里丢掉
  window.addEventListener('beforeunload', saveLayout)
  checkDefaultLLM()
})

// ===== 缺省大模型检查 =====
// 没配模型的租户，第一句对话会以一条看不懂的后端错误收场。与其让人猜，
// 不如进控制台就提示并把人送到「设置 → LLM 配置」。
//
// 去重用页面级变量而不是 sessionStorage：刷新一次就能重新检查，
// 「到底有没有提示」这件事不该被上一个标签页的历史标记挡住（排查时尤其致命）。
let llmHintShown = false

const checkDefaultLLM = async () => {
  if (llmHintShown) return

  let body
  try {
    const res = await fetch(apiConfig.admin.llmModels.getDefault, {
      headers: { Authorization: `Bearer ${getToken()}` }
    })
    body = await res.json()
  } catch (e) {
    // 网络/网关异常不打扰：那不是"没配模型"，提示了反而误导
    console.info('[llm-check] 请求失败，跳过提示:', e)
    return
  }

  // 后端有缺省模型 → code=200 直接返回；
  // 没有时是 ErrJSON("Default LLM model not found")，只认这一种情况
  if (body?.code === 200) {
    console.info('[llm-check] 已有缺省模型:', body?.data?.name)
    return
  }
  if (!/not found|不存在/i.test(body?.msg || '')) {
    console.info('[llm-check] 非「缺少默认模型」的响应，跳过提示:', body?.code, body?.msg)
    return
  }

  llmHintShown = true
  console.info('[llm-check] 未检测到缺省模型，提示用户去配置')

  try {
    await ElMessageBox.confirm(
      '当前租户还没有配置缺省大模型，智能体无法开始对话。现在去「设置 → LLM 配置」添加服务商与模型吗？',
      '未检测到大模型',
      { type: 'warning', confirmButtonText: '去配置', cancelButtonText: '稍后' }
    )
    // 明确点名 llm 页签：用户之前的设置可能停在别的分区
    settingsTab.value = 'llm'
    setActiveNav('settings')
  } catch {
    // 点「稍后」：留在当前页
  }
}

onUnmounted(() => {
  window.removeEventListener('resize', clampLayout)
  window.removeEventListener('beforeunload', saveLayout)
  if (saveLayoutTimer) clearTimeout(saveLayoutTimer)
})
</script>

<style scoped>
.main-window {
  width: 100%;
  height: 100vh;
  display: flex;
  flex-direction: column;
  background-color: #f0f2f5;
  overflow: hidden;
}

/* 顶部导航栏*/
.top-nav {
  display: flex;
  align-items: center;
  justify-content: space-between;
  height: 60px;
  padding: 0 20px;
  background-color: #f0f2f5;
  box-shadow: 0 1px 3px rgba(0, 0, 0, 0.02);
  z-index: 100;
}

/* 标志 */
.logo {
  display: flex;
  align-items: flex-end;
  gap: 0;
}

.logo-image {
  height: 32px;
  width: auto;
}

.logo-text {
  font-size: 14px;
  font-weight: 600;
  font-style: italic;
  color: #1890ff;
  margin: 0;
  line-height: 1;
  align-self: flex-end;
  padding-bottom: 0;
}

/* 导航菜单 */
/* 导航容器：间距由外层 gap 统一控制，不再靠每个 li 的 margin 拼 */
.nav-menu > ul {
  display: flex;
  align-items: center;
  gap: 12px;
  list-style: none;
  margin: 0;
  padding: 0;
}

.nav-menu li {
  margin: 0;
  position: relative;
}

/* 导航分组：一组共用一块连续底座的浅灰托盘，药丸浮在上面。组间 12px，组内 1px。
   组一 = 布局开关（开合面板，不切页，变蓝但不给胶囊）；
   组二 = 构建（智能体 / 技能 / 工具 / 工作流，按「零件 → 编排」排）；
   组三 = 运行与对外（运行 / 端点）。
   设置与资产市场不在这三组里：低频管理项，放右上账户区（见 .account-settings）。 */
.nav-menu .nav-group > ul {
  display: flex;
  align-items: center;
  gap: 1px;
  list-style: none;
  margin: 0;
  padding: 2px;
  /* 托盘要比 .top-nav 的 #f0f2f5 深一档才看得见（最初用 #eef1f4 差 2 个色值，等于没画）；
     但也不能太深，否则半透明的选中胶囊会被底色压住。再叠一圈内描边，读起来是「凹槽」 */
  background: #e7ebf0;
  box-shadow: inset 0 0 0 1px #dfe4ea;
  border-radius: 18px;
}

.nav-menu a {
  text-decoration: none;
  color: #666;
  font-size: 14px;
  font-weight: 500;
  transition: all 0.2s;
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 8px 12px;
  border-radius: 16px;
  height: 36px;
  box-sizing: border-box;
  box-shadow: 0 0 0 rgba(0, 0, 0, 0);
}

/* 导航图标 */
.nav-icon {
  font-size: 16px;
  display: flex;
  align-items: center;
  justify-content: center;
}

/* 三个布局开关只给图标：图标 + 文字是「页面入口」的规格，开关套用同一规格
   会让人以为点了要切页。做成正方形，视觉重量也正好和旁边那组托盘对齐。 */
.nav-menu li.nav-state > a {
  width: 36px;
  padding: 8px;
  justify-content: center;
}

/* 导航文字（页面入口仍在用；开关那组已去掉文字） */
.nav-text {
  padding-bottom: 0;
  display: flex;
  align-items: center;
  justify-content: center;
}

.nav-menu a:hover {
  color: #1890ff;
  background-color: #e6f0f8;
}

/* 选中状态：白胶囊本身就够醒目，不再叠加粗与明显阴影 ——
   灰导航条 + 浅灰托盘里，纯白 + 加粗 + 投影三件一起上会显得"压"在托盘上。 */
.nav-menu li.active a {
  color: #1890ff;
  /* 半透明白：透出一点托盘灰，比纯白轻，但仍是三层里最亮的一层 */
  background-color: rgba(255, 255, 255, 0.72);
  box-shadow: none;
  border-radius: 16px;
}

/* 「会话」「工作台」「内容」是开关不是页面：开着时和普通导航项一样变蓝（图标同步），
   但不给白胶囊 —— 白底是「当前所在页」的语义，开关这一层只留颜色这一个信号。 */
.nav-menu li.nav-state.is-on a {
  color: #1890ff;
  background-color: transparent;
  box-shadow: none;
}

/* hover 只补底色（颜色与通用 hover 一致，开关项与页面项手感统一） */
.nav-menu li.nav-state.is-on a:hover {
  color: #1890ff;
  background-color: #e6f0f8;
}

/* 账户设置：右侧这一组（开关 / 头像 / 密码 / 退出）间距收紧，读起来是一个整体 */
.account-settings {
  display: flex;
  align-items: center;
  gap: 4px;
}

/* 头像：蓝底圆形 + 大写首字母（居中）。完整名字由 :title 的浏览器 tip 显示 */
.user-avatar {
  width: 32px;
  height: 32px;
  border-radius: 50%;
  background-color: #1890ff;
  color: #fff;
  flex-shrink: 0;
  cursor: pointer; /* 点击复制账号 */
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 14px;
  font-weight: 500;
}

.logout-button {
  background: none;
  border: none;
  border-radius: 4px;
  /* 左右内边距压到 6px：外部 gap 只有 4px，按钮自带的内边距才是主要视觉间隔 */
  padding: 6px 6px;
  font-size: 14px;
  cursor: pointer;
  color: #666;
  display: flex;
  align-items: center;
  gap: 6px;
  transition: all 0.3s ease;
}


.logout-button:hover {
  color: #1890ff;
  background-color: #e6f0f8;
}

/* 设置 / 资产市场挪到账户区后仍要能看出「当前在这一页」。
   这一排没有导航那种浅灰托盘，套白胶囊会突兀，所以只给颜色这一个信号。 */
.logout-button.is-active {
  color: #1890ff;
}

/* 主内容区域：左右两栏 */
.main-content {
  flex: 1;
  padding: 0;
  overflow: hidden;
  display: flex;
  flex-direction: row;
  position: relative; /* 折叠后浮动展开按钮的定位基准 */
}

/* 左侧对话面板 */
.chat-panel {
  min-width: 280px;
  flex-shrink: 0;
  margin: 10px 0;
  background-color: #ffffff;
  border-radius: 12px;
  overflow: hidden;
}

/* 内容区折叠：对话占满整行（宽度交给 flex，此时不设内联 px 宽度） */
.chat-panel--full {
  flex: 1 1 auto;
  width: auto;
  min-width: 0;
  margin: 10px;
}

/* 拖拽分隔条 */
.chat-resizer {
  width: 5px;
  flex-shrink: 0;
  cursor: col-resize;
  background: transparent;
  transition: background 0.2s;
  position: relative; /* 折叠按钮的定位基准 */
}

.chat-resizer:hover {
  background: #e0e0e0;
}

/* 分隔条上的折叠按钮：鼠标移到分隔条上才出现 */
.resizer-toggle {
  position: absolute;
  top: 50%;
  left: 50%;
  transform: translate(-50%, -50%);
  width: 18px;
  height: 34px;
  padding: 0;
  border: 1px solid #dcdfe6;
  border-radius: 4px;
  background: #ffffff;
  color: #909399;
  font-size: 12px;
  line-height: 1;
  cursor: pointer;
  opacity: 0;
  pointer-events: none; /* 平时不拦截鼠标，避免挡住分隔条拖拽 */
  transition: opacity 0.2s, color 0.2s, border-color 0.2s;
  z-index: 5;
}

.chat-resizer:hover .resizer-toggle {
  opacity: 1;
  pointer-events: auto;
}

.resizer-toggle:hover {
  color: #409eff;
  border-color: #409eff;
}

/* 折叠后的展开按钮：贴右边缘，半透明常显 */
.resizer-toggle--edge {
  left: auto;
  right: 0;
  transform: translateY(-50%);
  opacity: 0.35;
}

.resizer-toggle--edge:hover {
  opacity: 1;
}

/* 右侧内容面板 */
.content-panel {
  flex: 1;
  min-width: 0; /* flex 子项默认 min-width:auto，会被内容撑破导致横向溢出 */
  /* 不要 height: 100%：它会与 margin: 10px 0 叠加导致总高溢出 20px（底部被裁）。
     flex 默认 align-items: stretch 会自动拉伸并扣掉 margin，与 .chat-panel 一致。 */
  overflow: hidden;
  display: flex;
  flex-direction: column;
  margin: 10px 10px 10px 0;
  background-color: #ffffff;
  border-radius: 12px;
}

/* 确保右侧内容面板内的div占满空间 */
.content-panel > div {
  flex: 1;
  display: flex;
  flex-direction: column;
  width: 100%;
  max-height: 100%;
}

/* 确保keep-alive包裹的内容占满空间*/
.content-panel > div > keep-alive {
  flex: 1;
  width: 100%;
  max-height: 100%;
}

/* 内容占位符*/
.content-placeholder {
  text-align: center;
  padding: 40px;
  background-color: white;
  border-radius: 12px;
  box-shadow: 0 2px 8px rgba(0, 0, 0, 0.1);
  flex: 1;
  display: flex;
  flex-direction: column;
  justify-content: center;
  align-items: center;
  max-height: 100%;
}

.content-placeholder h2 {
  font-size: 24px;
  font-weight: 600;
  color: #333;
  margin: 0 0 16px 0;
}

.content-placeholder p {
  font-size: 16px;
  color: #666;
  margin: 0;
}
</style>

<!-- 全局（非 scoped）：拖拽分割条期间禁用 iframe 的指针事件。
     iframe 是独立的浏览上下文，鼠标移到它上面时父文档收不到 mousemove / mouseup，
     会导致拖拽中途「卡住 + 宽度跳变」（工作台里的浏览器标签就是 iframe）。 -->
<style>
body.flame-dragging iframe {
  pointer-events: none !important;
}

body.flame-dragging {
  user-select: none;
}
</style>
