// 平台管理台的会话（自包含）。
//
// 刻意**不** import 控制台的 utils/auth.js：键名与生命周期都独立，
// 这样 src/platform/ 这一整棵树可以随时搬走（见 ./README.md 的边界规则）。
//
// 存储位置与控制台保持一致的分工：
//   token → sessionStorage（关标签页即失效）
//   user  → localStorage（刷新后仍能显示身份；真实身份以 /platform/me 回查为准）

import { SESSION_KEYS } from '../mode'
import { resetTitle, setTitle } from '../title'
import i18n from '../locales'

const TOKEN_KEY = SESSION_KEYS.manage.token
const USER_KEY = SESSION_KEYS.manage.user

let token = null
let user = null

export const saveAuth = (newToken, newUser) => {
  token = newToken
  user = newUser
  try {
    sessionStorage.setItem(TOKEN_KEY, newToken)
    localStorage.setItem(USER_KEY, JSON.stringify(newUser || {}))
  } catch (err) {
    console.error('[platform-auth] failed to save the session:', err)
  }
  // 管理台固定后缀：这个页签是"管理台"，不属于某个具体租户
  setTitle(i18n.global.t('platform.app.consoleTitle'))
}

export const getToken = () => {
  if (!token) {
    try {
      token = sessionStorage.getItem(TOKEN_KEY)
    } catch {
      token = null
    }
  }
  return token
}

export const getUser = () => {
  if (!user) {
    try {
      const raw = localStorage.getItem(USER_KEY)
      user = raw ? JSON.parse(raw) : null
    } catch {
      user = null
    }
  }
  return user
}

export const isLoggedIn = () => !!getToken()

// isAdmin 平台管理员：管所有租户与所有平台用户
export const isAdmin = () => (getUser()?.role || '') === 'admin'

export const clearAuth = () => {
  token = null
  user = null
  try {
    sessionStorage.removeItem(TOKEN_KEY)
    localStorage.removeItem(USER_KEY)
  } catch (err) {
    console.error('[platform-auth] failed to clear the session:', err)
  }
  resetTitle()
}

export default { saveAuth, getToken, getUser, isLoggedIn, isAdmin, clearAuth }
