// 应用外壳的中立开关：当前显示哪一套界面 + 两套会话的存在性判断。
//
// 不 import 任何一侧的代码 —— App.vue / AuthGate.vue / src/platform/** 都可读它。
//
// 两套会话各自独立、可并存（切模式不丢另一个的会话）：
//   desk   租户控制台：app_flame_auth_token(session) / app_flame_auth_user(local)   —— 由 utils/auth.js 维护
//   manage 平台管理台：app_flame_mgmt_token(session) / app_flame_mgmt_user(local)   —— 由 platform/auth.js 维护

const MODE_KEY = 'app_flame_mode'

export const MODE_DESK = 'desk'
export const MODE_MANAGE = 'manage'

const DESK_TOKEN_KEY = 'app_flame_auth_token'
const MGMT_TOKEN_KEY = 'app_flame_mgmt_token'

// 会话键名常量（供两侧各自实现读写，避免互相 import）
export const SESSION_KEYS = {
  desk: { token: DESK_TOKEN_KEY, user: 'app_flame_auth_user' },
  manage: { token: MGMT_TOKEN_KEY, user: 'app_flame_mgmt_user' }
}

function readSession(key) {
  try {
    return sessionStorage.getItem(key)
  } catch {
    return null
  }
}

// getMode 当前模式；未设置时按租户控制台处理（老用户的默认体感不变）。
export function getMode() {
  try {
    const m = localStorage.getItem(MODE_KEY)
    return m === MODE_MANAGE ? MODE_MANAGE : MODE_DESK
  } catch {
    return MODE_DESK
  }
}

// setMode 记录当前模式。
export function setMode(mode) {
  try {
    localStorage.setItem(MODE_KEY, mode === MODE_MANAGE ? MODE_MANAGE : MODE_DESK)
  } catch {
    /* 隐私模式等场景下忽略 */
  }
}

export function clearMode() {
  try {
    localStorage.removeItem(MODE_KEY)
  } catch {
    /* 忽略 */
  }
}

export function hasDeskSession() {
  return !!readSession(DESK_TOKEN_KEY)
}

export function hasMgmtSession() {
  return !!readSession(MGMT_TOKEN_KEY)
}
