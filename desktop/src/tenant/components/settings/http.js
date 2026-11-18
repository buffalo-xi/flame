import { getToken } from '../../utils/auth.js'
import { formatError } from '../../../locales'

/** 带 token 的 JSON 请求头 */
export const authHeaders = (json = false) => ({
  ...(json ? { 'Content-Type': 'application/json' } : {}),
  Authorization: `Bearer ${getToken()}`
})

/**
 * 检查后端响应是否业务成功。
 * 后端 xhttp.ErrJSON 虽然 HTTP 状态码 200，但 body.code 为 400 —— 只看 response.ok 会漏判。
 *
 * 错误文案统一走 formatError：后端带了 error 编码就按当前语言翻译，没带就回退 msg。
 * 这里是租户侧业务错误的收口点之一，改这一处即可覆盖全部调用方，
 * 不必去改上百个 `ElMessage.error(err.message)`。
 */
export const checkBizOk = async (response) => {
  if (!response.ok) {
    throw new Error(
      formatError({
        error: 'common.network',
        params: { reason: `HTTP ${response.status}` },
        msg: `HTTP ${response.status}`
      })
    )
  }
  const data = await response.json()
  if (data && typeof data.code !== 'undefined' && data.code !== 200) {
    throw new Error(formatError(data))
  }
  return data
}
