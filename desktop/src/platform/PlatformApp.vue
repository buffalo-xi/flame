<template>
  <!-- 管理台外壳：与控制台主界面（components/MainWindow.vue）同款 ——
       logo 图 + 居中图标药丸导航 + 右侧头像/退出，内容区为灰底白卡。
       这里是复制样式而非 import：platform 子树必须能整棵搬走（见 README.md）。 -->
  <div class="platform-app">
    <header class="top-nav">
      <div class="logo">
        <img src="./assets/pic/flame.png" alt="flame" class="logo-image" />
        <div class="logo-text">flame</div>
      </div>

      <nav class="nav-menu">
        <!-- 分组托盘：治理动作一组、两个市场一组（与租户控制台的 nav-group 同款） -->
        <div v-for="(group, gi) in navGroups" :key="gi" class="nav-group">
          <ul>
            <li
              v-for="item in group"
              :key="item.key"
              :class="{ active: activeNav === item.key }"
              @click="activeNav = item.key"
            >
              <a href="#" @click.prevent>
                <span class="nav-icon">
                  <el-icon><component :is="item.icon" /></el-icon>
                </span>
                <span class="nav-text">{{ item.label }}</span>
              </a>
            </li>
          </ul>
        </div>
      </nav>

      <div class="account-settings">
        <!-- 头像：蓝底圆形 + 大写首字母；角色/显示名/账号名在悬停 tip 里，点击复制账号 -->
        <div class="user-avatar" :title="identityTip" @click="copyAccount">{{ avatarText }}</div>
        <button
          v-if="hasDesk"
          class="top-btn"
          title="浏览器里还留着租户控制台的会话"
          @click="emit('switch-console')"
        >
          <el-icon><Back /></el-icon>
          <span>返回控制台</span>
        </button>
        <button class="top-btn" @click="openPwdDialog">
          <el-icon><Lock /></el-icon>
          <span>密码</span>
        </button>
        <button class="top-btn" @click="handleLogout">
          <el-icon><SwitchButton /></el-icon>
          <span>退出</span>
        </button>
      </div>
    </header>

    <main class="main-content">
      <div class="content-panel">
        <!-- 导航上不再挂数量，各视图 emit('count') 留空不接（用于日志/调试时可再挂回来） -->
        <AdminConsole v-if="isAdminRole" :tab="activeNav" @session-expired="handleLogout" />
        <!-- 用户身份：我的租户（只读） -->
        <MyTenantsView v-else @session-expired="handleLogout" />
      </div>
    </main>

    <!-- 修改密码（当前登录的平台账号自助；租户账号在控制台改自己的） -->
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
        <div class="pwd-tip">
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
import { computed, onMounted, ref } from 'vue'
import { ElIcon, ElMessage } from 'element-plus'
import { Back, Box, Connection, Lock, OfficeBuilding, Setting, Shop, SwitchButton, UserFilled } from '@element-plus/icons-vue'
import { api } from './api'
import { clearAuth, getToken, getUser, saveAuth } from './auth'
import { hasDeskSession } from '../mode'
import AdminConsole from './views/AdminConsole.vue'
import MyTenantsView from './views/MyTenantsView.vue'

const emit = defineEmits(['logout', 'switch-console'])

const user = ref(getUser() || {})
const tenantCount = ref(0)
const hasDesk = ref(hasDeskSession())

const isAdminRole = computed(() => user.value?.role === 'admin')

// 顶栏导航：一个内层数组 = 一块托盘。第一组是治理动作，第二组是两个市场
// （本地 = 本机资产池含入库审核与上架策略，远端 = 别的站点的资产池拉取导入）。
// 「用户」身份只有我的租户（与控制台 MainWindow 的导航同形）
const navGroups = computed(() =>
  isAdminRole.value
    ? [
        [
          { key: 'tenants', label: '租户管理', icon: OfficeBuilding },
          { key: 'users', label: '平台用户', icon: UserFilled },
          { key: 'plugins', label: '插件与能力', icon: Box },
          { key: 'system', label: '系统设置', icon: Setting }
        ],
        [
          { key: 'assets', label: '本地市场', icon: Shop },
          { key: 'market', label: '远端市场', icon: Connection }
        ]
      ]
    : [[{ key: 'my-tenants', label: '我的租户', icon: OfficeBuilding }]]
)
const activeNav = ref('tenants')

const accountName = computed(() => user.value?.name || '')
const userName = computed(() => user.value?.display_name || user.value?.name || '未知账号')
const avatarText = computed(() => String(userName.value || 'U').slice(0, 1).toUpperCase())
// tip 里带上账号名（登录名）：显示名可以重复，账号名才是唯一身份
// （显示名为空时纯展示账号名，此时不重复打印一遍）
const identityTip = computed(() => {
  const role = isAdminRole.value ? '平台管理员' : '用户'
  const account =
    accountName.value && accountName.value !== userName.value ? `（账号 ${accountName.value}）` : ''
  const sub = isAdminRole.value ? '管所有租户' : `名下 ${tenantCount.value} 个租户`
  return `${role} · ${userName.value}${account} · ${sub}${accountName.value ? '，点击复制账号' : ''}`
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
  if (!accountName.value) return
  const ok = await copyText(accountName.value)
  if (ok) {
    ElMessage.success({ message: `账号已复制：${accountName.value}`, duration: 1200 })
  } else {
    ElMessage.error('复制失败，请手动选择账号名')
  }
}

// 角色可能与本地缓存不一致（/me 回来才知道），保证 activeNav 始终落在有效项上
const ensureNav = () => {
  const flat = navGroups.value.flat()
  if (!flat.some((i) => i.key === activeNav.value)) {
    activeNav.value = flat[0].key
  }
}

// 进入管理台先回查身份：账号被停用/删除时，即便 token 未过期也立即踢出
onMounted(async () => {
  ensureNav()
  try {
    const data = await api.me()
    if (data?.user) {
      user.value = data.user
      saveAuth(getToken(), data.user) // 同步本地缓存，保证刷新后一致
    }
    tenantCount.value = data?.tenant_count ?? 0
    ensureNav()
  } catch (err) {
    ElMessage.error(err.message || '平台会话已失效，请重新登录')
    clearAuth()
    emit('logout')
  }
})

// ===== 右上角：当前登录的平台账号自助改密（与主界面同款弹窗） =====
const pwdDialogVisible = ref(false)
const pwdSaving = ref(false)
const pwdForm = ref({ old_password: '', new_password: '', confirm: '' })

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
    await api.changeMyPassword(f.old_password, f.new_password)
    ElMessage.success({ message: '密码已修改', duration: 1200 })
    pwdDialogVisible.value = false
  } catch (err) {
    // api.js 遇到 401 已清掉本地会话：账号被停用/删除 → 直接回登录页
    if (err?.status === 401) {
      ElMessage.error(err.message || '平台会话已失效，请重新登录')
      emit('logout')
      return
    }
    ElMessage.error(err.message || '修改失败')
  } finally {
    pwdSaving.value = false
  }
}

const handleLogout = () => {
  clearAuth()
  emit('logout')
}
</script>

<style scoped>
.platform-app {
  width: 100%;
  height: 100vh;
  display: flex;
  flex-direction: column;
  background-color: #f0f2f5; /* 与控制台主界面同底色 */
  overflow: hidden;
}

/* ===== 顶栏（以下至 .top-btn 为 MainWindow.vue 的复制，保持观感一致） ===== */
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

/* 导航：与租户控制台（components/MainWindow.vue）同款 —— 每组一块浅灰托盘，药丸浮在上面。
   组间 12px、组内 1px、内缩 2px（描边余量） */
.nav-menu {
  display: flex;
  align-items: center;
  gap: 12px;
}

.nav-menu .nav-group > ul {
  display: flex;
  align-items: center;
  gap: 1px;
  list-style: none;
  margin: 0;
  padding: 2px;
  /* 比 .top-nav 的 #f0f2f5 深一档才看得见；再叠一圈内描边，读起来是「凹槽」 */
  background: #e7ebf0;
  box-shadow: inset 0 0 0 1px #dfe4ea;
  border-radius: 18px;
}

.nav-menu li {
  margin: 0;
  position: relative;
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

.nav-icon {
  font-size: 16px;
  display: flex;
  align-items: center;
  justify-content: center;
}

.nav-text {
  display: flex;
  align-items: center;
  justify-content: center;
}

.nav-menu a:hover {
  color: #1890ff;
  background-color: #e6f0f8;
}

/* 选中态与租户导航一致：半透明白胶囊（透出一点托盘灰），不叠加粗体与投影 */
.nav-menu li.active a {
  color: #1890ff;
  background-color: rgba(255, 255, 255, 0.72);
  box-shadow: none;
  border-radius: 16px;
}

/* 右侧这一组（头像 / 改密 / 退出）间距收紧，读起来是一个整体（与租户控制台同款） */
.account-settings {
  display: flex;
  align-items: center;
  gap: 4px;
}

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

.top-btn {
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

.top-btn:hover {
  color: #1890ff;
  background-color: #e6f0f8;
}

/* 改密弹窗底部说明（与主界面同款文案） */
.pwd-tip {
  font-size: 12px;
  color: #909399;
  line-height: 1.6;
}

/* ===== 内容区：灰底 + 单张白色圆角卡片 ===== */
.main-content {
  flex: 1;
  padding: 0;
  overflow: hidden;
  display: flex;
  flex-direction: row;
}

.content-panel {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  display: flex;
  flex-direction: column;
  /* 只负责灰底上的上下内缩：白卡由各页自己给。
     左右不留：10px 叠加页面内部的卡间距后边距成 16~20px，两张大卡显得又空又散 */
  margin: 4px 0;
}
</style>
