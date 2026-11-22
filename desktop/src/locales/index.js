// 前端 i18n 入口：语言包装配、语言切换、以及「后端错误码 → 本地文案」的兜底。
//
// 分两层，不要混：
//   - common.*  界面自身文案（按钮、标题、空状态）
//   - error.*   后端业务错误码（见 nexus/docs/error-codes.md）
//
// 后端只返回编码与参数（{ code, error, params, msg }），翻译只发生在这里。
// 这样后端不必知道语言，同一个响应既能给中文界面也能给英文界面。
import { createI18n } from 'vue-i18n'
import zhCommon from './zh-CN/common.json'
import zhError from './zh-CN/error.json'
import zhHistory from './zh-CN/history.json'
import zhAgents from './zh-CN/agents.json'
import zhEndpoints from './zh-CN/endpoints.json'
import zhSkills from './zh-CN/skills.json'
import zhVersion from './zh-CN/version.json'
import zhAsset from './zh-CN/asset.json'
import zhTools from './zh-CN/tools.json'
import zhSettings from './zh-CN/settings.json'
import zhWorkbench from './zh-CN/workbench.json'
import zhChat from './zh-CN/chat.json'
import zhFlow from './zh-CN/flow.json'
import zhPlatform from './zh-CN/platform.json'
import zhHelp from './zh-CN/help.json'
import enCommon from './en/common.json'
import enError from './en/error.json'
import enHistory from './en/history.json'
import enAgents from './en/agents.json'
import enEndpoints from './en/endpoints.json'
import enSkills from './en/skills.json'
import enVersion from './en/version.json'
import enAsset from './en/asset.json'
import enTools from './en/tools.json'
import enSettings from './en/settings.json'
import enWorkbench from './en/workbench.json'
import enChat from './en/chat.json'
import enFlow from './en/flow.json'
import enPlatform from './en/platform.json'
import enHelp from './en/help.json'

// 可选语言。label 用各自语言写，切换菜单里不出现「英文」这种译文；
// short 是顶栏按钮上的短码（中 / EN / 日本語 → 日），加语言时一起补。
export const LOCALES = [
  { value: 'zh-CN', label: '中文', short: '中' },
  { value: 'en', label: 'English', short: 'EN' }
]

const STORAGE_KEY = 'flame.locale'

// readLocale 决定初始语言：用户上次的选择优先，其次跟随浏览器，非中文浏览器一律英文。
// 英文是基准语言（骨架语言），中文是第二语言。
// 不读 localStorage 的话，每次刷新都会跳回浏览器语言，用户会觉得设置没生效。
export function readLocale() {
  const saved = localStorage.getItem(STORAGE_KEY)
  if (saved && LOCALES.some((l) => l.value === saved)) return saved
  const nav = (navigator.language || '').toLowerCase()
  return nav.startsWith('zh') ? 'zh-CN' : 'en'
}

const i18n = createI18n({
  // legacy: false —— 组合式 API，模板里 $t 仍可用（globalInjection）
  legacy: false,
  globalInjection: true,
  locale: readLocale(),
  // 回退到英文而不是中文：英文是基准语言，任一语言包缺条目时至少显示可读内容，
  // 也避免「新增第三语言时缺的键莫名其妙变成中文」。
  fallbackLocale: 'en',
  messages: {
    'zh-CN': {
      ...zhCommon, ...zhError, ...zhHistory, ...zhAgents,
      ...zhEndpoints, ...zhSkills, ...zhVersion, ...zhAsset, ...zhTools, ...zhSettings,
      ...zhWorkbench, ...zhChat, ...zhFlow, ...zhPlatform, ...zhHelp
    },
    en: {
      ...enCommon, ...enError, ...enHistory, ...enAgents,
      ...enEndpoints, ...enSkills, ...enVersion, ...enAsset, ...enTools, ...enSettings,
      ...enWorkbench, ...enChat, ...enFlow, ...enPlatform, ...enHelp
    }
  }
})

// setLocale 切换语言并持久化。
// <html lang> 必须同步：浏览器据此决定断词、朗读与表单校验提示的语言。
export function setLocale(locale) {
  if (!LOCALES.some((l) => l.value === locale)) return
  i18n.global.locale.value = locale
  localStorage.setItem(STORAGE_KEY, locale)
  document.documentElement.setAttribute('lang', locale)
}

// currentLocale 供组件做响应式读取（切换语言后立即重渲染的那一类需求）。
export const currentLocale = () => i18n.global.locale.value

// currentLocaleShort 是顶栏切换按钮上显示的短码：写「当前」语言，菜单里再选目标语言。
// 语言多于两种时按钮不能再显示「将要切到」的语言（不知道该切哪一个）。
export const currentLocaleShort = () => {
  const hit = LOCALES.find((l) => l.value === i18n.global.locale.value)
  return hit ? hit.short : i18n.global.locale.value
}

/**
 * formatError 把后端响应渲染成本地化文案。
 *
 * 优先级：error 编码可翻译 → 用它；否则回落 msg。
 * 回落是必须的 —— 错误码是逐个接口铺开的，没铺到的接口只有 msg，
 * 而且新编码可能比语言包先上线。任何情况下都不该把 `error.xxx` 这种原文显示给用户。
 *
 * @param {{error?: string, params?: object, msg?: string, message?: string}} res
 */
export function formatError(res) {
  if (!res) return ''
  const { t, te } = i18n.global
  const key = res.error ? `error.${res.error}` : ''
  if (key && te(key)) return t(key, res.params || {})
  return res.msg || res.message || ''
}

// 能力键 → 语言包路径。键名是接口口径（sandbox.exec 等），界面文案必须走本地化，
// 不能直接显示后端返回的 label（那是中文硬编码，切到英文仍然显示中文）。
//
// 旧键 sandbox.script 归一为 sandbox.code：库里可能还有过渡期旧行，只换文案不换键。
const CAPABILITY_SLUGS = {
  'sandbox.exec': 'exec',
  'sandbox.shell': 'shell',
  'sandbox.code': 'code',
  'sandbox.script': 'code',
  'sandbox.browser': 'browser',
  'net.egress': 'egress'
}

/**
 * capabilityLabelKey 给出能力名的语言包路径；未知键返回空串（调用方回落后端 label）。
 *
 * short = true 取档位短名（导航/列表），否则取带括注的完整说明（详情标题）。
 */
export function capabilityLabelKey(key, short = false) {
  const slug = CAPABILITY_SLUGS[key]
  if (!slug) return ''
  return `platform.capabilities.${slug}.${short ? 'short' : 'full'}`
}

// 内置类组的 ref 后缀是稳定口径（tools.Category*），同时兼当语言包键名。
// 清单必须与 nexus/service/agent/toolgrant/catalog.go 的 builtinGroupCategories +
// builtinCategoryLabel 对齐：多列无害（只是允许翻译），漏列则那一组回落后端中文。
const BUILTIN_GROUP_CATEGORIES = {
  file: true, exec: true, shell: true, script: true, browser: true,
  agent: true, skill: true, 'skill-own': true,
  'agent-admin': true, 'skill-admin': true, 'tool-grant': true,
  'custom-tool': true, 'flow-admin': true, super: true
}

/**
 * toolGroupTextKey 给出工具组名字/描述的语言包前缀（`<key>.name` / `<key>.desc`）。
 *
 * 为什么需要它：groups 接口里这几类组的 name / description 是**后端硬编码中文**
 * （见 catalog.go：`"内置-" + category`、builtinCategoryLabel、PresetGroups、
 * categoryGroup 的「插件 / 自定义工具」、local:rpc 组），切到英文仍然是中文。
 * 判据只能落在 ref 上（接口口径），不能匹配中文文案 —— 那等于把译文当接口用。
 *
 * 返回空串 = 不是这几类（租户自定义工具组 group:xxx、连接 mcp:/rpc:、
 * 或将来新增的未知预设），调用方回落后端原文。
 */
export function toolGroupTextKey(ref) {
  const s = String(ref || '')
  const i = s.indexOf(':')
  if (i < 0) return ''
  const scope = s.slice(0, i)
  const rest = s.slice(i + 1)
  if (scope === 'builtin') {
    return BUILTIN_GROUP_CATEGORIES[rest] ? `toolGroups.builtin.${rest}` : ''
  }
  // 预设组由后端代码定义（当前只有 preset:default），新增时补语言包即可
  if (scope === 'preset') return rest ? `toolGroups.preset.${rest}` : ''
  // 类别容器：ref 形如 plugin:* / custom:*（容器不可授权，只用于展示）
  if (scope === 'plugin' || scope === 'custom') return `toolGroups.category.${scope}`
  // 本地端点组只有一个 local:rpc
  if (scope === 'local') return rest === 'rpc' ? 'toolGroups.local.rpc' : ''
  return ''
}

export default i18n
