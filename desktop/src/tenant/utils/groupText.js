import { toolGroupTextKey } from '../../locales'

/**
 * 工具组的展示名 / 描述：按 ref 查语言包，查不到才用后端原文。
 *
 * 后端 groups 接口里这几类组的 name / description 是硬编码中文（见
 * nexus/service/agent/toolgrant/catalog.go）：内置类组 `"内置-" + category`、
 * 预设组 PresetGroups、插件 / 自定义工具的类别容器、local:rpc 组。中文界面下没问题，
 * 切到英文仍然是中文 —— 所以显示时按 ref 覆盖（与 unavailable.js 的
 * 「先编码、后原文」同一套口径，见 locales/index.js 的 toolGroupTextKey）。
 *
 * 为什么在渲染时算、而不是 load 时改写目录数据：切语言不刷新页面
 * （setLocale 只改 locale 与 <html lang>），烘焙进数据的文案不会跟着变。
 *
 * 自定义工具组（group:xxx）与连接（mcp: / rpc:）命中不了映射，原样返回 ——
 * 那些是租户数据，本来就不该翻。
 *
 * @param {(key: string, params?: object) => string} t
 * @param {(key: string) => boolean} te
 * @param {{ref?: string, name?: string, label?: string, description?: string}} g 目录里的组，或已选项草稿（有 label）
 */
export function groupName(t, te, g) {
  const key = toolGroupTextKey(g?.ref)
  const nameKey = key ? `${key}.name` : ''
  if (nameKey && te(nameKey)) return t(nameKey)
  return g?.name || g?.label || ''
}

// groupDesc 同上，取 `<key>.desc`；无译文时回落后端 description。
export function groupDesc(t, te, g) {
  const key = toolGroupTextKey(g?.ref)
  const descKey = key ? `${key}.desc` : ''
  if (descKey && te(descKey)) return t(descKey)
  return g?.description || ''
}
