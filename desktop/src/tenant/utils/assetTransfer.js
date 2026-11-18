// 资产包导出 / 导入的前端公共动作（智能体 / 技能 / 工作流 / 自定义工具四类共用）。
//
// 为什么不能用 <a href> 直接下载：令牌在 Authorization 头里，导航式请求带不上。
// 所以导出走 fetch + blob，导入走 FormData 上传。
//
// 后端的错误响应是 HTTP 200 + { code: 400, msg, data }（见 tools/xhttp），
// 导出因为可能是二进制，先看 content-type 再决定按 JSON 解析还是当文件保存。

import { ElMessage, ElMessageBox } from 'element-plus'
import adminApi from '../config/admin.js'
import { getToken } from './auth.js'

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
    throw new Error(`网络请求失败：${err.message}`)
  }

  const contentType = res.headers.get('content-type') || ''
  if (!res.ok || contentType.includes('application/json')) {
    const body = await res.json().catch(() => null)
    throw new Error(body?.msg || `导出失败（HTTP ${res.status}）`)
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
      ElMessage.success(`已导出 ${name}`)
      return
    }
    await ElMessageBox.alert(
      warnings.map((w) => `· ${escapeHtml(w)}`).join('<br>'),
      `已导出 ${escapeHtml(name)}，注意以下事项`,
      { dangerouslyUseHTMLString: true, confirmButtonText: '知道了' }
    )
  } catch (err) {
    ElMessage.error(err?.message || '导出失败')
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
    throw new Error(`网络请求失败：${err.message}`)
  }

  const body = await res.json().catch(() => null)
  if (!res.ok || !body || body.code !== 200) {
    throw new ImportError(body?.msg || `导入失败（HTTP ${res.status}）`, body?.data || null)
  }
  return body.data || {}
}

// OVERWRITE_COPY 覆盖确认文案按类型给：替换范围不一样，笼统说一句「会覆盖」等于没说。
// 上传导入与市场安装共用这一份，避免以后改一处漏一处。
export const OVERWRITE_COPY = {
  agent: {
    title: '覆盖已有智能体',
    detail: '覆盖会按包替换它的文件、配置、私有技能、技能绑定，以及三个权限开关' +
      '（超级智能体 / 不限制工具 / 可发现未绑定技能）。'
  },
  skill: {
    title: '覆盖已有技能',
    detail: '覆盖会按包替换它的正文与附件、描述 / 元数据、命令配置和工具依赖；' +
      '该技能未发布的草稿改动会丢失。可见性授权（哪些智能体可用）保留原样。'
  },
  workflow: {
    title: '覆盖已有工作流',
    detail: '覆盖只替换草稿（图 / 变量 / 限额）。已发布的版本、对外端点、断点保留原样 ——' +
      '要让新内容对外生效，需在编辑器里发布新版本。'
  },
  custom_tool: {
    title: '覆盖已有自定义工具',
    detail: '覆盖会按包替换脚本与 TOOL.json 契约。已填写的配置值、启用状态保留原样' +
      '（配置就是凭据，包里的配置是空的）；装完请复核脚本再继续使用。'
  }
}

// publishAssetVersion 导入成功后立刻发布一个版本（复用现有版本接口，不另写一套发布逻辑）。
// kind: skill / agent / workflow（ref 用 id）、custom_tool（ref 用 name）。
// 返回版本号；失败抛错，由调用方降级成提示（导入本身已经成功，不能因此回滚）。
export const publishAssetVersion = async (kind, ref, note = '通过资产包导入发布') => {
  const urlOf = {
    skill: adminApi.skills.versions,
    agent: adminApi.agents.versions,
    workflow: adminApi.flows.versions,
    custom_tool: adminApi.customTools.versions
  }[kind]
  if (!urlOf || !ref) throw new Error('该类型不支持在导入后自动发布版本')

  let res
  try {
    res = await fetch(urlOf(ref), {
      method: 'POST',
      headers: { ...authHeaders(), 'Content-Type': 'application/json' },
      body: JSON.stringify({ note })
    })
  } catch (err) {
    throw new Error(`网络请求失败：${err.message}`)
  }

  const body = await res.json().catch(() => null)
  if (!res.ok || !body || body.code !== 200) {
    throw new Error(body?.msg || `发布版本失败（HTTP ${res.status}）`)
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
    throw new Error(`网络请求失败：${err.message}`)
  }

  const body = await res.json().catch(() => null)
  if (!res.ok || !body || body.code !== 200) {
    // 预检失败可能是「包本身不合法」，把后端原话带出去，别吞成一句「检查失败」
    throw new ImportError(body?.msg || `依赖检查失败（HTTP ${res.status}）`, body?.data || null)
  }
  return body.data || {}
}
