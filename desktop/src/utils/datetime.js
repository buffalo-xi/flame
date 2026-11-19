import { currentLocale } from '../locales'

// 时间的显示统一走这里，不要在组件里直接 new Date(x).toLocaleString()。
//
// 为什么必须显式传 locale：不传时跟随「系统」语言，界面切成英文后日期仍是系统格式
// （中文系统上显示 2026/01/05 10:30:00，英文界面里就格外割裂）。这里取当前界面语言，
// 保证日期格式与界面语言一致，加第三门语言时也不用再逐个调用点改。
//
// 时区仍是用户本机时区：后端存 UTC，界面按本地时间显示是既定口径。

const DATE_TIME = {
  year: 'numeric',
  month: '2-digit',
  day: '2-digit',
  hour: '2-digit',
  minute: '2-digit',
  second: '2-digit'
}

function toDate(value) {
  if (value === null || value === undefined || value === '') return null
  const d = value instanceof Date ? value : new Date(value)
  return Number.isNaN(d.getTime()) ? null : d
}

/**
 * 日期 + 时间（到秒）。
 *
 * 解析不出来返回空串，占位符由调用方决定（'—' / '-' / 原串），
 * 这样各处的既有语义不变，也不会把 "Invalid Date" 印到界面上。
 */
export function formatDateTime(value) {
  const d = toDate(value)
  return d ? d.toLocaleString(currentLocale(), DATE_TIME) : ''
}
