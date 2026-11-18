import { getToken } from '../../utils/auth.js'

/** 带 token 的 JSON 请求头 */
export const authHeaders = (json = false) => ({
  ...(json ? { 'Content-Type': 'application/json' } : {}),
  Authorization: `Bearer ${getToken()}`
})

/**
 * 检查后端响应是否业务成功。
 * 后端 xhttp.ErrJSON 虽然 HTTP 状态码 200，但 body.code 为 400 —— 只看 response.ok 会漏判。
 */
export const checkBizOk = async (response) => {
  if (!response.ok) {
    throw new Error(`HTTP ${response.status}`)
  }
  const data = await response.json()
  if (data && typeof data.code !== 'undefined' && data.code !== 200) {
    throw new Error(data.msg || '操作失败')
  }
  return data
}
