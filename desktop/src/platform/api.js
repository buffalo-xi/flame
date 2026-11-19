// /platform/* 的统一请求层。
//
// 自包含：apiBase 的解析逻辑与控制台 config/base.js 一致（运行时真源都是
// window.APP_CONFIG.apiBase / VITE_API_BASE），但本文件不 import 控制台代码，
// 以便 src/platform/ 整棵树搬走。
//
// 响应约定：
//   - 成功：{code:200, msg, data}          → 返回 data
//   - 业务失败：{code:400, msg}            → 抛 ApiError(msg)
//   - 传输层未授权/无权限：401/403 {error, message} → 抛 ApiError(message, status)
//     401 时会顺手清掉本地会话（令牌失效/账号被停用）

import { getToken, clearAuth } from './auth'
import i18n, { formatError } from '../locales'

const t = (key, params) => i18n.global.t(key, params)

export class ApiError extends Error {
  constructor(message, status = 0, code = 0, data = null) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.code = code
    // 结构化附加信息（如导入撞名时的 { existing: {id, build, status} }）：
    // 界面要靠它决定「弹覆盖确认」还是「直接报错」，不能只凭文案。
    this.data = data
  }
}

const resolveApiBase = () => {
  if (typeof window !== 'undefined' && window.APP_CONFIG?.apiBase) {
    return window.APP_CONFIG.apiBase
  }
  return import.meta.env.VITE_API_BASE || '/nexus'
}

const apiBase = resolveApiBase()

// bizMessage 把后端响应渲染成本地化文案：有 error 编码就按当前语言翻译，否则用原文。
//
// 不直接在 throw 处调 formatError 的原因：这三条分支的兜底文案各不相同，
// 而且 payload 可能是 null（网关返回 HTML），需要统一挑出可用的那句话。
const bizMessage = (payload, fallback) =>
  formatError({
    error: payload?.error,
    params: payload?.params,
    msg: payload?.msg || payload?.message || fallback
  })

// request 发一个平台请求，返回 data；失败抛 ApiError。
// auth=false 用于登录接口（还没有令牌）。
export const request = async (path, { method = 'GET', body, auth = true } = {}) => {
  const headers = { 'Content-Type': 'application/json' }
  if (auth) {
    const t = getToken()
    if (t) headers.Authorization = `Bearer ${t}`
  }

  let resp
  try {
    resp = await fetch(`${apiBase}/platform${path}`, {
      method,
      headers,
      body: body === undefined ? undefined : JSON.stringify(body)
    })
  } catch (err) {
    throw new ApiError(
      formatError({
        error: 'common.network',
        params: { reason: err.message },
        msg: t('apiError.network', { reason: err.message })
      })
    )
  }

  let payload = null
  try {
    payload = await resp.json()
  } catch {
    /* 非 JSON 响应（如网关 502 HTML），下面按状态码报错 */
  }

  if (resp.status === 401) {
    clearAuth()
    // 401 一律用本地的「会话失效」文案：后端的 message 在部分网关下是英文短语，
    // 直接透出会出现「英文界面里混一句英文网关话」或反过来。
    throw new ApiError(
      formatError({ error: 'auth.session_expired', msg: payload?.message || t('apiError.sessionExpired') }),
      401
    )
  }
  if (!resp.ok) {
    throw new ApiError(bizMessage(payload, t('apiError.requestFailedHttp', { status: resp.status })), resp.status, 0, payload?.data || null)
  }
  if (payload && payload.code !== undefined && payload.code !== 200) {
    throw new ApiError(bizMessage(payload, t('apiError.requestFailed')), resp.status, payload.code, payload.data || null)
  }
  return payload ? payload.data : null
}

const qs = (params) => {
  const p = new URLSearchParams()
  Object.entries(params || {}).forEach(([k, v]) => {
    if (v !== undefined && v !== null && v !== '') p.set(k, v)
  })
  const s = p.toString()
  return s ? `?${s}` : ''
}

const seg = (v) => encodeURIComponent(v)

// blobRequest 取二进制响应（资产包下载）。JSON 的 request() 把它当 JSON 解析会直接失败，
// 所以单独一条：错误分支仍然是后端统一的 {code,msg}，只是成功分支回 Blob。
const blobRequest = async (path) => {
  const t = getToken()
  let resp
  try {
    resp = await fetch(`${apiBase}/platform${path}`, {
      headers: t ? { Authorization: `Bearer ${t}` } : {}
    })
  } catch (err) {
    throw new ApiError(t('apiError.network', { reason: err.message }))
  }
  if (resp.status === 401) {
    clearAuth()
    throw new ApiError(t('apiError.sessionExpired'), 401)
  }
  if (!resp.ok) {
    let msg = t('apiError.downloadFailed', { status: resp.status })
    try {
      const p = await resp.json()
      msg = bizMessage(p, msg)
    } catch {
      /* 非 JSON 错误体，保留状态码文案 */
    }
    throw new ApiError(msg, resp.status)
  }
  const blob = await resp.blob()
  const cd = resp.headers.get('Content-Disposition') || ''
  const m = /filename="?([^";]+)"?/.exec(cd)
  return { blob, filename: m ? m[1] : 'asset.zip' }
}

// formRequest 发 multipart（文件上传）。Content-Type 必须留给浏览器自动加 boundary。
const formRequest = async (path, formData) => {
  const t = getToken()
  let resp
  try {
    resp = await fetch(`${apiBase}/platform${path}`, {
      method: 'POST',
      headers: t ? { Authorization: `Bearer ${t}` } : {},
      body: formData
    })
  } catch (err) {
    throw new ApiError(t('apiError.network', { reason: err.message }))
  }
  let payload = null
  try {
    payload = await resp.json()
  } catch {
    /* 网关 502 之类 */
  }
  if (resp.status === 401) {
    clearAuth()
    throw new ApiError(payload?.message || t('apiError.sessionExpired'), 401)
  }
  if (!resp.ok) {
    throw new ApiError(
      bizMessage(payload, t('apiError.requestFailedHttp', { status: resp.status })),
      resp.status,
      payload?.code || 0,
      payload?.data || null
    )
  }
  if (payload && payload.code !== undefined && payload.code !== 200) {
    throw new ApiError(formatError(payload) || t('apiError.requestFailed'), resp.status, payload.code, payload.data || null)
  }
  return payload ? payload.data : null
}

// 静态资源路径：icon 形如 /static/llm/openai.png（后端 static 挂载），完整 URL 原样返回。
// 与控制台的那份逻辑同源，但平台子树不 import 控制台代码，所以这里自带一份。
export const resolveAsset = (p) => {
  if (!p) return ''
  if (/^(https?:)?\/\//.test(p)) return p
  return `${apiBase}${p.startsWith('/') ? p : `/${p}`}`
}

export const api = {
  // 登录：没有租户字段，身份完全由平台用户表决定
  login: (name, password) => request('/login', { method: 'POST', auth: false, body: { name, password } }),

  // 身份与"我的租户"
  me: () => request('/me'),
  myTenants: () => request('/me/tenants'),
  // 「用户」启停自己名下的租户（管理员走 setTenantStatus）
  setMyTenantStatus: (schema, status) =>
    request(`/me/tenants/${seg(schema)}/status`, { method: 'PUT', body: { status } }),

  // 「用户」读写自己名下租户的工作流运行参数（系统级只读对照；管理员走 getTenantFlowConfigs / setTenantFlowConfigs）
  getMyTenantFlowConfigs: (schema) => request(`/me/tenants/${seg(schema)}/flowconfigs`),
  setMyTenantFlowConfigs: (schema, body) =>
    request(`/me/tenants/${seg(schema)}/flowconfigs`, { method: 'PUT', body }),

  // 「用户」读写自己名下租户的智能体运行参数（与 flowconfigs 对称；本表不接受 -1）
  getMyTenantAgentConfigs: (schema) => request(`/me/tenants/${seg(schema)}/agentconfigs`),
  setMyTenantAgentConfigs: (schema, body) =>
    request(`/me/tenants/${seg(schema)}/agentconfigs`, { method: 'PUT', body }),

  // 「用户」读写自己名下租户的会话配额（只开放两个会话字段；管理员走 getTenantQuota / setTenantQuota）
  getMyTenantQuota: (schema) => request(`/me/tenants/${seg(schema)}/quota`),
  setMyTenantQuota: (schema, body) =>
    request(`/me/tenants/${seg(schema)}/quota`, { method: 'PUT', body }),

  // 「用户」为名下租户的根账号 primary 重置密码（忘记初始密码时的自助恢复口）
  resetMyTenantPrimaryPassword: (schema, password) =>
    request(`/me/tenants/${seg(schema)}/primary/password`, { method: 'POST', body: { password } }),

  // 自助改密：本人凭旧密码更换（管理员代人重置走 resetUserPassword）
  changeMyPassword: (oldPassword, newPassword) =>
    request('/me/password', { method: 'POST', body: { old_password: oldPassword, new_password: newPassword } }),

  // 跨租户资源分发（platform.shared_assets）：审核 / 下架 / 可见范围。
  // 平台侧只做治理不搬运：快照由租户侧上架产生（/admin/share/listings），安装也在租户侧。
  // 状态机：draft → pending → listed | rejected；listed → delisted（越级一律 400）。
  listAssets: (params) => request(`/assets${qs(params)}`),
  // 详情比列表多带 manifest（完整文件清单）、install_count（装过几家）、allowlist
  getAsset: (id) => request(`/assets/${seg(id)}`),
  // body: { action: 'approve' | 'reject' | 'delist', note }
  reviewAsset: (id, body) => request(`/assets/${seg(id)}/review`, { method: 'POST', body }),
  // 彻底删除：listed / delisted 都能直接删（不要求先下架）；pending / rejected / draft 后端会拒
  deleteAsset: (id) => request(`/assets/${seg(id)}`, { method: 'DELETE' }),
  getAssetVisibility: (id) => request(`/assets/${seg(id)}/visibility`),
  // body: { visibility: 'all' | 'allowlist', schemas: [] }（白名单全量替换，不提供单条增删）
  setAssetVisibility: (id, body) => request(`/assets/${seg(id)}/visibility`, { method: 'PUT', body }),

  // 跨服务器搬运（手工包）：导出成 zip / 上传 zip 导入。
  // 导出回 Blob（不能走 JSON request）；导入是 multipart，成功回 {asset, overwritten, warnings}。
  // 导入后一律 pending —— 对面审过不等于本机放行，要在审核页重新过一眼。
  exportAssetPackage: (id) => blobRequest(`/assets/${seg(id)}/package`),
  importAssetPackage: (file, { origin = '', force = false } = {}) => {
    const fd = new FormData()
    fd.append('file', file)
    if (origin) fd.append('origin', origin)
    if (force) fd.append('force', '1')
    return formRequest('/asset-imports', fd)
  },

  // 跨服务器市场（两个方向）：
  //   peers        —— 我去拉的那一端（对方地址 + 对方给我的市场令牌）
  //   marketTokens —— 允许谁来拉我（明文只在创建时回一次，库里只有 sha256）
  listPeers: () => request('/peers'),
  createPeer: (body) => request('/peers', { method: 'POST', body }),
  updatePeer: (id, body) => request(`/peers/${seg(id)}`, { method: 'PUT', body }),
  deletePeer: (id) => request(`/peers/${seg(id)}`, { method: 'DELETE' }),
  // 代理对端目录，并标注本机是否已导入（imported / local.build）
  peerCatalog: (id, kind = '') => request(`/peers/${seg(id)}/catalog${kind ? `?kind=${encodeURIComponent(kind)}` : ''}`),
  // body: {asset_id, force}；force=true 覆盖同来源同名资产（本地版本 +1）
  importFromPeer: (id, body) => request(`/peers/${seg(id)}/import`, { method: 'POST', body }),
  listMarketTokens: () => request('/market-tokens'),
  createMarketToken: (name) => request('/market-tokens', { method: 'POST', body: { name } }),
  setMarketToken: (id, enabled) =>
    request(`/market-tokens/${seg(id)}`, { method: 'PUT', body: { enabled } }),
  deleteMarketToken: (id) => request(`/market-tokens/${seg(id)}`, { method: 'DELETE' }),
  // 市场出口策略：open=true 表示允许不带令牌读本机市场（默认 false）
  getMarketSettings: () => request('/market-settings'),
  setMarketSettings: (open) => request('/market-settings', { method: 'PUT', body: { open } }),

  // 上架审核策略：租户上架资产要不要过平台审核（全局默认 + 按租户覆盖）
  // 只管「上架」；「从远端拉取」的审核在 peer 的 install_policy 上，两码事。
  getListingPolicy: () => request('/listing-policy'),
  setListingDefault: (requireReview) =>
    request('/listing-policy', { method: 'PUT', body: { require_review: requireReview } }),
  // requireReview 传 null = 删掉该租户的覆盖，回到全局默认
  setListingOverride: (schema, requireReview) =>
    request(`/listing-policy/${seg(schema)}`, { method: 'PUT', body: { require_review: requireReview } }),

  // 租户管理（管理员）
  listTenants: (params) => request(`/tenants${qs(params)}`),
  // 新建租户用：自动探索一个未被占用的 schema（t0、t1、t2… 取第一个可用的）
  nextTenantSchema: () => request('/tenant-schemas/next'),
  createTenant: (body) => request('/tenants', { method: 'POST', body }),
  getTenant: (schema) => request(`/tenants/${seg(schema)}`),
  updateTenant: (schema, body) => request(`/tenants/${seg(schema)}`, { method: 'PUT', body }),
  // 彻底删除租户（不可逆）：正常状态的租户后端会拒，必须先停用
  deleteTenant: (schema) => request(`/tenants/${seg(schema)}`, { method: 'DELETE' }),
  setTenantStatus: (schema, status) => request(`/tenants/${seg(schema)}/status`, { method: 'PUT', body: { status } }),
  // 原始配额行 + 系统顶 + 兜底 + 生效值（编辑框绑原始值，生效值只做展示）
  getTenantQuota: (schema) => request(`/tenants/${seg(schema)}/quota`),
  setTenantQuota: (schema, quota) => request(`/tenants/${seg(schema)}/quota`, { method: 'PUT', body: quota }),

  // 插件授权（platform.tenant_plugins 的租户行；row = 本租户原始行，null = 未开通）
  // 只有租户行：系统默认行已移除，「上架即对所有租户开放」改为逐个授权。
  getTenantPlugins: (schema) => request(`/tenants/${seg(schema)}/plugins`),
  setTenantPlugin: (schema, name, body) =>
    request(`/tenants/${seg(schema)}/plugins/${seg(name)}`, { method: 'PUT', body }),
  deleteTenantPlugin: (schema, name) =>
    request(`/tenants/${seg(schema)}/plugins/${seg(name)}`, { method: 'DELETE' }),
  // 插件管理页一次取全：插件清单（含实现 + blocked）+ 租户列表 + 各租户原始行 + 启用状态
  getPluginOverview: () => request('/plugins'),
  // 全局停用（platform.plugin_gates）：急停口，缺省放行、压过租户的显式开启。
  // 没有列表接口 —— /plugins 上每个插件已经带 blocked。
  setPluginGate: (name, body) => request(`/system/plugin-gates/${seg(name)}`, { method: 'PUT', body }),
  deletePluginGate: (name) => request(`/system/plugin-gates/${seg(name)}`, { method: 'DELETE' }),

  // 工具能力的平台授权（platform.tenant_capabilities；键名仍是 sandbox.*）
  // row = 本租户原始行（null = 没设过 = 未开通），blocked = 全局闸是否拦下，allowed = 生效值。
  // 关闭后即使租户给 agent 授了对应工具，目录置灰且执行层直接拒。
  getTenantCapabilities: (schema) => request(`/tenants/${seg(schema)}/capabilities`),
  setTenantCapability: (schema, key, body) =>
    request(`/tenants/${seg(schema)}/capabilities/${seg(key)}`, { method: 'PUT', body }),
  deleteTenantCapability: (schema, key) =>
    request(`/tenants/${seg(schema)}/capabilities/${seg(key)}`, { method: 'DELETE' }),
  // 全局硬闸（platform.capability_gates）：急停口，缺省放行、压过租户的显式开启。
  // blocked=false 走删行，所以「解除拦截」与 DELETE 等价，界面上只需一个开关 + 恢复默认。
  getCapabilityGates: () => request('/system/capability-gates'),
  setCapabilityGate: (key, body) =>
    request(`/system/capability-gates/${seg(key)}`, { method: 'PUT', body }),
  deleteCapabilityGate: (key) => request(`/system/capability-gates/${seg(key)}`, { method: 'DELETE' }),

  // 工作流运行参数（platform.flow_configs 的租户行；系统行只读，接口里回 system 供对照）
  getTenantFlowConfigs: (schema) => request(`/tenants/${seg(schema)}/flowconfigs`),
  setTenantFlowConfigs: (schema, body) => request(`/tenants/${seg(schema)}/flowconfigs`, { method: 'PUT', body }),

  // 智能体运行参数（platform.agent_configs 的租户行；本表不接受 -1，只接受 >= 1）
  getTenantAgentConfigs: (schema) => request(`/tenants/${seg(schema)}/agentconfigs`),
  setTenantAgentConfigs: (schema, body) =>
    request(`/tenants/${seg(schema)}/agentconfigs`, { method: 'PUT', body }),

  // 系统设置（platform.* 的系统行 tenant_id = ''）：全局默认 / 硬顶
  getSystemFlowConfigs: () => request('/system/flowconfigs'),
  setSystemFlowConfigs: (body) => request('/system/flowconfigs', { method: 'PUT', body }),
  getSystemQuota: () => request('/system/quota'),
  setSystemQuota: (body) => request('/system/quota', { method: 'PUT', body }),
  // 智能体运行参数的系统行（platform.agent_configs，tenant_id = ''）
  getSystemAgentConfigs: () => request('/system/agentconfigs'),
  setSystemAgentConfigs: (body) => request('/system/agentconfigs', { method: 'PUT', body }),

  // 系统设置 - LLM 服务商预设（platform.llm_provider_presets，全局共享；租户侧只读）
  listLlmPresets: () => request('/system/llm-provider-presets'),
  createLlmPreset: (body) => request('/system/llm-provider-presets', { method: 'POST', body }),
  updateLlmPreset: (id, body) => request(`/system/llm-provider-presets/${seg(id)}`, { method: 'PUT', body }),
  deleteLlmPreset: (id) => request(`/system/llm-provider-presets/${seg(id)}`, { method: 'DELETE' }),

  // 平台用户管理（管理员）
  listUsers: (params) => request(`/users${qs(params)}`),
  // 某用户名下的租户（详情卡「名下租户」块）
  getUserTenants: (id) => request(`/users/${seg(id)}/tenants`),
  createUser: (body) => request('/users', { method: 'POST', body }),
  updateUser: (id, body) => request(`/users/${seg(id)}`, { method: 'PUT', body }),
  resetUserPassword: (id, password) => request(`/users/${seg(id)}/password`, { method: 'POST', body: { password } })
}

export default api
