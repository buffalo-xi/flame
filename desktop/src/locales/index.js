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
import enCommon from './en/common.json'
import enError from './en/error.json'

// 可选语言。label 用各自语言写，切换菜单里不出现「英文」这种译文。
export const LOCALES = [
  { value: 'zh-CN', label: '中文' },
  { value: 'en', label: 'English' }
]

const STORAGE_KEY = 'flame.locale'

// readLocale 决定初始语言：用户上次的选择优先，其次跟随浏览器，默认中文。
// 不读 localStorage 的话，每次刷新都会跳回中文，用户会觉得设置没生效。
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
  // 回退到中文而不是英文：中文是唯一真源，英文包缺条目时至少显示可读内容
  fallbackLocale: 'zh-CN',
  messages: {
    'zh-CN': { ...zhCommon, ...zhError },
    en: { ...enCommon, ...enError }
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

export default i18n
