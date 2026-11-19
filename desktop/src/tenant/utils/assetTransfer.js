// 资产包导出 / 导入的前端公共动作（智能体 / 技能 / 工作流 / 自定义工具四类共用）。
//
// 为什么不能用 <a href> 直接下载：令牌在 Authorization 头里，导航式请求带不上。
// 所以导出走 fetch + blob，导入走 FormData 上传。
//
// 后端的错误响应是 HTTP 200 + { code: 400, msg, data }（见 tools/xhttp），
// 导出因为可能是二进制，先看 content-type 再决定按 JSON 解析还是当文件保存。

import { ElMessage, ElMessageBox } from 'element-plus'
import i18n, { formatError } from '../../locales'
import adminApi from '../config/admin.js'
import { getToken } from './auth.js'

// 这里产生的都是给用户看的句子（网络失败 / HTTP 码 / 导出提示），必须跟随当前界面语言；
// 不是组件，拿不到 useI18n()，所以取全局实例（与 locales/index.js 的 formatError 同一套）。
const t = (key, params) => i18n.global.t(key, params)

const authHeaders = () => {
  const t = getToken()
  return t ? { Authorization: `Bearer ${t}` } : {}
}

// X-Export-Warnings：每条 URL 编码，用 "|" 分隔（header 只能放 ASCII）
const decodeWarnings = (raw) => {
  if (!raw) return []
  return raw
    .split('|')
    .map((s) => {
      try {
        return decodeURIComponent(s)
      } catch {
        return s
      }
    })
    .filter(Boolean)
}

// 从 Content-Disposition 取文件名：优先 RFC 5987 的 filename*（中文工作流名走这个）
const filenameFrom = (disposition, fallback) => {
  if (!disposition) return fallback
  const star = /filename\*=UTF-8''([^;]+)/i.exec(disposition)
  if (star) {
    try {
      return decodeURIComponent(star[1])
    } catch {
      /* 落回纯 ASCII 名 */
    }
  }
  const plain = /filename="?([^";]+)"?/i.exec(disposition)
  return plain ? plain[1] : fallback
}

const saveBlob = (blob, filename) => {
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  document.body.appendChild(a)
  a.click()
  a.remove()
  // 立刻 revoke 在部分浏览器会打断下载，挪出一帧
  setTimeout(() => URL.revokeObjectURL(url), 0)
}

// exportAsset 下载资产包，返回需要提示的 warnings（脱敏 / 未带走的私有技能等）。
// kind: skill | agent | workflow | custom_tool；ref: 前三者用 id，工具用名字。
export const exportAsset = async (kind, ref) => {
  let res
  try {
    res = await fetch(adminApi.share.exportAsset(kind, ref), { headers: authHeaders() })
  } catch (err) {
    throw new Error(t('assetTransfer.networkFailed', { msg: err.message }))
  }

  const contentType = res.headers.get('content-type') || ''
  if (!res.ok || contentType.includes('application/json')) {
    const body = await res.json().catch(() => null)
    throw new Error(formatError(body) || t('assetTransfer.exportFailedHttp', { status: res.status }))
  }

  const blob = await res.blob()
  const fallback = `${kind}-${ref}.zip`
  saveBlob(blob, filenameFrom(res.headers.get('content-disposition'), fallback))
  return decodeWarnings(res.headers.get('x-export-warnings'))
}

const escapeHtml = (s) =>
  String(s).replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c])

// exportEntity 导出并反馈到界面（四个列表共用一行调用）。
// 有 warnings 时用弹框逐条列出：脱敏了哪些字段、哪些私有技能没带走，一闪而过的 toast 装不下。
export const exportEntity = async (kind, ref, label) => {
  const name = label || ref
  try {
    const warnings = await exportAsset(kind, ref)
    if (!warnings.length) {
      ElMessage.success(t('assetTransfer.exported', { name }))
      return
    }
    await ElMessageBox.alert(
      warnings.map((w) => `· ${escapeHtml(w)}`).join('<br>'),
      t('assetTransfer.exportedWithWarnings', { name: escapeHtml(name) }),
      { dangerouslyUseHTMLString: true, confirmButtonText: t('assetTransfer.gotIt') }
    )
  } catch (err) {
    ElMessage.error(err?.message || t('assetTransfer.exportFailed'))
  }
}

// ImportError 带上后端 data（重名时是 { suggested_name }），供对话框给出可操作提示。
export class ImportError extends Error {
  constructor(message, data = null) {
    super(message)
    this.name = 'ImportError'
    this.data = data
  }
}

// importAsset 上传资产包并直接装成本租户实体。
// kind 用于「在哪个列表导入就只能装哪一类」：包内类型不符时后端会报错。
// overwrite=true 表示「同名就覆盖既有实体」（四类资产都支持；不支持的类别后端会明确拒绝）。
export const importAsset = async (kind, file, { name = '', status, overwrite = false } = {}) => {
  const fd = new FormData()
  fd.append('file', file)
  if (kind) fd.append('kind', kind)
  if (name) fd.append('name', name)
  if (status) fd.append('status', String(status))
  if (overwrite) fd.append('overwrite', '1')

  let res
  try {
    res = await fetch(adminApi.share.importAsset, {
      method: 'POST',
      headers: authHeaders(),
      body: fd
    })
  } catch (err) {
    throw new Error(t('assetTransfer.networkFailed', { msg: err.message }))
  }

  const body = await res.json().catch(() => null)
  if (!res.ok || !body || body.code !== 200) {
    throw new ImportError(formatError(body) || t('assetTransfer.importFailedHttp', { status: res.status }), body?.data || null)
  }
  return body.data || {}
}

// 四类资产在文案 key 里的后缀（assetImport.kindAgent / overwriteDetailSkill / …）。
// 覆盖确认的两处入口（上传导入、市场安装）都按这张表取词，避免一份文案两处写。
export const ASSET_KIND_SUFFIX = {
  agent: 'Agent',
  skill: 'Skill',
  workflow: 'Workflow',
  custom_tool: 'CustomTool'
}

// publishAssetVersion 导入成功后立刻发布一个版本（复用现有版本接口，不另写一套发布逻辑）。
// kind: skill / agent / workflow（ref 用 id）、custom_tool（ref 用 name）。
// 返回版本号；失败抛错，由调用方降级成提示（导入本身已经成功，不能因此回滚）。
export const publishAssetVersion = async (kind, ref, note = '') => {
  const urlOf = {
    skill: adminApi.skills.versions,
    agent: adminApi.agents.versions,
    workflow: adminApi.flows.versions,
    custom_tool: adminApi.customTools.versions
  }[kind]
  if (!urlOf || !ref) throw new Error(t('assetTransfer.publishUnsupported'))

  let res
  try {
    res = await fetch(urlOf(ref), {
      method: 'POST',
      headers: { ...authHeaders(), 'Content-Type': 'application/json' },
      // 版本说明缺省就是「从资产包装进来的」，让版本面板上看得出这一版的来历
      body: JSON.stringify({ note: note || t('assetImport.publishNote') })
    })
  } catch (err) {
    throw new Error(t('assetTransfer.networkFailed', { msg: err.message }))
  }

  const body = await res.json().catch(() => null)
  if (!res.ok || !body || body.code !== 200) {
    throw new Error(formatError(body) || t('assetTransfer.publishFailedHttp', { status: res.status }))
  }
  // 回包形状不同：技能 / 智能体是 {version:{version:n}}，工作流 / 自定义工具是版本对象本身
  const v = body.data?.version
  return (v && typeof v === 'object' ? v.version : v) ?? null
}

// inspectImport 装之前把包内依赖对照本租户检查一遍（只读：不落地、不改租户）。
// 回 { kind, name, deps:[{kind,scope,ref,via,state,total,items,detail}], summary, missing,
//      existing, bundled, notices, notes }。
export const inspectImport = async (kind, file, { name = '' } = {}) => {
  const fd = new FormData()
  fd.append('file', file)
  if (kind) fd.append('kind', kind)
  if (name) fd.append('name', name)

  let res
  try {
    res = await fetch(adminApi.share.inspectImport, { method: 'POST', headers: authHeaders(), body: fd })
  } catch (err) {
    throw new Error(t('assetTransfer.networkFailed', { msg: err.message }))
  }

  const body = await res.json().catch(() => null)
  if (!res.ok || !body || body.code !== 200) {
    // 预检失败可能是「包本身不合法」，把后端原话带出去，别吞成一句「检查失败」
    throw new ImportError(formatError(body) || t('assetTransfer.inspectFailedHttp', { status: res.status }), body?.data || null)
  }
  return body.data || {}
}
