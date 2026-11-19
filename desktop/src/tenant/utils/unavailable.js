import { capabilityLabelKey } from '../../locales'

/**
 * unavailableText 把后端给的「工具/组不可用原因」翻成本地文案。
 *
 * 后端同时给两样东西（见 nexus/service/agent/toolgrant/catalog.go）：
 *   unavailable_code + unavailable_params  给前端查语言包（toolUnavailable.<code>）
 *   unavailable_reason                     中文兜底：日志、旧后端、以及前端还没翻的编码
 *
 * 所以这里的顺序是「先编码、后原文」，两者都取不到才返回调用方给的 fallback。
 * 不要反过来用 reason 做匹配 —— 那等于把中文文案当接口用，改一个字就静默失效。
 *
 * @param {(key: string, params?: object) => string} t
 * @param {(key: string) => boolean} te
 * @param {{unavailable_code?: string, unavailable_params?: object, unavailable_reason?: string}} item
 * @param {string} fallback 连中文原文都没有时的兜底（如「不可用」）
 */
export function unavailableText(t, te, item, fallback = '') {
  const reason = item?.unavailable_reason || fallback
  const code = item?.unavailable_code
  if (!code) return reason

  const path = `toolUnavailable.${code}`
  if (!te(path)) return reason

  const params = { ...(item.unavailable_params || {}) }
  if (code === 'capability') {
    // 能力名不能直接用后端的 label（中文硬编码）：按 key 查语言包。
    // 查不到就不翻了 —— 与其拼出「（undefined）」不如显示后端那句完整中文。
    const labelPath = capabilityLabelKey(params.key)
    if (!params.key || !labelPath || !te(labelPath)) return reason
    params.label = t(labelPath)
  }
  return t(path, params)
}
