<template>
  <div class="app">
    <!-- 外壳：未登录 → 登录页（租户/平台两个入口）；已登录 → 按 mode 渲染对应控制台 -->
    <AuthGate v-if="!loggedIn" :initial-tab="authTab" @login-success="handleLoginSuccess" />
    <MainWindow v-else-if="isDesk" @logout="handleLogout" />
    <PlatformApp v-else @logout="handleLogout" @switch-console="switchToConsole" />
  </div>
</template>

<script>
import { computed, onMounted, ref } from 'vue'
import AuthGate from './AuthGate.vue'
import MainWindow from './tenant/components/MainWindow.vue'
import PlatformApp from './platform/PlatformApp.vue'
import { clearAuth, getUser as getDeskUser } from './tenant/utils/auth'
import { MODE_DESK, MODE_MANAGE, getMode, hasDeskSession, hasMgmtSession, setMode } from './mode'
import { setTitle } from './title'

export default {
  name: 'App',
  components: {
    AuthGate,
    MainWindow,
    PlatformApp
  },
  setup() {
    // 两套会话各自独立、可并存（见 mode.js）：mode 只决定"现在显示哪套壳"
    const mode = ref(MODE_DESK)
    const loggedIn = ref(false)

    const isDesk = computed(() => mode.value === MODE_DESK)

    // 登录页默认停在哪个 tab：跟上次用的模式一致
    const authTab = computed(() => (mode.value === MODE_MANAGE ? 'manage' : 'desk'))

    const syncLoginState = () => {
      loggedIn.value = mode.value === MODE_MANAGE ? hasMgmtSession() : hasDeskSession()
    }

    const handleLoginSuccess = (loginMode) => {
      mode.value = loginMode === MODE_MANAGE ? MODE_MANAGE : MODE_DESK
      setMode(mode.value)
      syncLoginState()
    }

    const handleLogout = () => {
      // 租户控制台的会话在这里清；平台管理台的会话由 PlatformApp 用自己的 auth 模块清
      if (mode.value === MODE_DESK) {
        clearAuth()
      }
      loggedIn.value = false
    }

    // 管理台 →「返回控制台」：控制台会话还在就直接切，不在就落回登录页
    const switchToConsole = () => {
      mode.value = MODE_DESK
      setMode(MODE_DESK)
      syncLoginState()
      // 标题跟着"当前显示的壳"走：切回控制台就把租户标识换回来
      if (loggedIn.value) setTitle(getDeskUser()?.schema)
    }

    // 初始化时按上次的模式决定展示哪套界面
    onMounted(() => {
      mode.value = getMode()
      syncLoginState()
      // 刷新时 index.html 的静态标题会盖回登录时设的值，按当前会话补一次
      if (loggedIn.value) {
        setTitle(mode.value === MODE_MANAGE ? '平台管理' : getDeskUser()?.schema)
      }
    })

    return {
      mode,
      loggedIn,
      isDesk,
      authTab,
      handleLoginSuccess,
      handleLogout,
      switchToConsole
    }
  }
}
</script>

<style>
* {
  margin: 0;
  padding: 0;
  box-sizing: border-box;
}

body {
  font-family: Arial, sans-serif;
  background-color: #f5f5f5;
}

.app {
  min-height: 100vh;
  display: flex;
  justify-content: center;
  align-items: center;
}

/* ============ 自绘浮层 / Element Plus 下拉的层级约定（全局，三页共用） ============
   EP 浮层（nextZIndex 动态分配，2000 起） < .settings-panel（编辑器设置面板）
                                          < .picker-panel（工具选择侧栏，4000）
                                          < 带 agent-select-popper 的下拉（5000）
   两类自绘面板都是 body 级浮层，会盖住 EP 默认层级的下拉浮层；
   因此设置面板里「技能的归属 agent」等下拉显式抬到侧栏之上。 */
.agent-select-popper {
  z-index: 5000 !important;
}

/* 设置侧栏（.settings-panel，3200）里的普通下拉：同样要抬到面板之上。
   不然 EP 默认 2000 档的下拉会被不透明的面板整块盖住，表现就是「点开什么都没发生」。 */
.settings-select-popper {
  z-index: 5000 !important;
}

/* ============ 弹窗打开时不许整壳横移（全局） ============
   EP 的 lock-scroll 会给 body 加 el-popup-parent--hidden，并在「body 有溢出」时把
   body 宽度压成 calc(100% - 滚动条宽度)（use-lockscreen/index.mjs:47-49）。
   本应用是整屏固定壳 + 内部滚动，body 本来就不该滚，这层补偿只会让导航栏与内容
   整块横移（典型表现：一开弹窗，顶栏和下面的页面一起跳一下）。 */
body.el-popup-parent--hidden {
  width: auto !important;
  padding-right: 0 !important;
}
</style>
