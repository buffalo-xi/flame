// 认证管理工具
//
// 这里不做任何 token / user 的日志输出：之前 saveAuth / getToken / isLoggedIn / clearAuth
// 里有一批 console.log 把 Bearer token 明文打到控制台，生产环境等于把租户权限交给任何能
// 打开 devtools 的人，也会被前端错误采集一并带走。出错只留 console.error，且不带凭证内容。

import { resetTitle, setTitle } from '../../title'

// 内存存储
let authToken = null
let authUser = null

// 保存登录信息
export const saveAuth = (token, user) => {
  authToken = token
  authUser = user
  // token保存到会话存储，用户信息保存到本地存储
  try {
    sessionStorage.setItem('app_flame_auth_token', token)
    localStorage.setItem('app_flame_auth_user', JSON.stringify(user))
  } catch (error) {
    console.error('Failed to save auth:', error)
  }
  // 标题跟着会话走：登录成功即可见，不必各页面自己设
  setTitle(user && user.schema)
}

// 获取token
export const getToken = () => {
  if (!authToken) {
    // 从会话存储获取
    try {
      authToken = sessionStorage.getItem('app_flame_auth_token')
    } catch (error) {
      console.error('Failed to get token from sessionStorage:', error)
      authToken = null
    }
  }
  return authToken
}

// 获取用户信息
export const getUser = () => {
  if (!authUser) {
    // 从本地存储获取
    try {
      const userStr = localStorage.getItem('app_flame_auth_user')
      if (userStr) {
        try {
          authUser = JSON.parse(userStr)
        } catch (error) {
          console.error('Failed to parse user info:', error)
          authUser = null
        }
      }
    } catch (error) {
      console.error('Failed to get user from localStorage:', error)
      authUser = null
    }
  }
  return authUser
}

// 检查是否已登录（getToken 内部已兜住 storage 异常，这里不再套一层 try）
export const isLoggedIn = () => !!getToken()

// 清除登录信息
export const clearAuth = () => {
  authToken = null
  authUser = null
  // 从会话存储清除token，从本地存储清除用户信息
  try {
    sessionStorage.removeItem('app_flame_auth_token')
    localStorage.removeItem('app_flame_auth_user')
  } catch (error) {
    console.error('Failed to clear auth:', error)
  }
  // 退出后标题回到默认值，别把上一个租户的标识留在标签页上
  resetTitle()
}

// 获取认证头
export const getAuthHeader = () => {
  const token = getToken()
  return token ? { Authorization: `Bearer ${token}` } : {}
}

// 获取完整的认证信息
export const getAuthInfo = () => ({
  token: getToken(),
  user: getUser(),
  isLoggedIn: isLoggedIn()
})

export default {
  saveAuth,
  getToken,
  getUser,
  isLoggedIn,
  clearAuth,
  getAuthHeader,
  getAuthInfo
}
