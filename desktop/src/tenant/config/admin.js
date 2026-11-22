// 导入API基础路径
import { apiBase } from './base.js';

// 会话工作区类接口的公共查询串：session_id 必带（后端据此定位 user + 会话目录），
// path 可选（列根目录、新建走 body 时都不带），limit 只给「最近文件」用。
// 空值一律不落参数 —— 别下发 ?path= 这种语义为空的键。
const wsQuery = ({ session_id, path, limit } = {}) => {
  const p = new URLSearchParams()
  if (session_id) p.set('session_id', session_id)
  if (path) p.set('path', path)
  if (limit) p.set('limit', String(limit))
  const qs = p.toString()
  return qs ? `?${qs}` : ''
}

// 管理模块API配置
const adminApi = {
  // 运行台账：本租户当前在跑的 agent + flow，跨实例（Redis 台账）
  // kind 可选 'agent' | 'flow'，留空为全部
  running: (kind) => `${apiBase}/admin/running${kind ? `?kind=${encodeURIComponent(kind)}` : ''}`,
  // 终止一条运行：POST {kind, run_id, session_id}。本实例没命中会广播给其它实例，
  // 返回 reason = stopped / broadcast / unreachable
  runningStop: `${apiBase}/admin/running/stop`,
  // 运行历史：跨流程查 flow_run_records（跑完才落库、长期保留），flow_id 可空。
  // 列表 / 导出 / 清理共用一个查询串（flow_id / status / run_type / start_from / start_to / limit / offset），
  // 三方必须同参，否则「看到的」「导出的」「删掉的」不是一批
  runningHistory: (qs = '') => `${apiBase}/admin/running/history${qs ? `?${qs}` : ''}`,
  runningHistoryExport: (qs = '') => `${apiBase}/admin/running/history/export${qs ? `?${qs}` : ''}`,

  // Agent run history: reads agent_run_records (one row per agent run), a different table from the workflow
  // history above and shown on its own page. It also covers stateless MCP / RPC calls that carry no session.
  // List / export / cleanup share one query string (agent_id / status / run_type / start_from / start_to /
  // limit / offset): three different conditions would mean "seen", "exported" and "deleted" disagree.
  agentHistory: (qs = '') => `${apiBase}/admin/running/agent-history${qs ? `?${qs}` : ''}`,
  agentHistoryExport: (qs = '') => `${apiBase}/admin/running/agent-history/export${qs ? `?${qs}` : ''}`,
  agentHistoryDetail: (id) => `${apiBase}/admin/running/agent-history/${id}`,
  agentHistoryDelete: (id) => `${apiBase}/admin/running/agent-history/${id}`,

  // 智能体API（agents 表）
  agents: {
    base: `${apiBase}/admin/agents/`,
    create: `${apiBase}/admin/agents/create`,
    get: `${apiBase}/admin/agents/`,
    update: `${apiBase}/admin/agents/`,
    delete: `${apiBase}/admin/agents/`,
    chat: (id) => `${apiBase}/admin/agents/${id}/chat`,
    // 终止运行：POST {session_id}（按会话停掉所有在跑的运行），幂等
    stop: (id) => `${apiBase}/admin/agents/${id}/stop`,
    sessions: (id) => `${apiBase}/admin/agents/${id}/sessions`,
    status: (id) => `${apiBase}/admin/agents/${id}/status`,
    // 调用授权：GET 查看（can_call/called_by）| PUT 双向设置（{can_call_ids, called_by_ids}）
    relations: (id) => `${apiBase}/admin/agents/${id}/relations`,
    // 技能绑定（agent_skill_bindings 的 agent 侧）：GET 读取 | PUT 全量替换（{skill_ids}）
    skillBindings: (id) => `${apiBase}/admin/agents/${id}/skillbindings`,
    // 对话窗口的斜杠命令（user_invocable 技能）：GET {items:[{name,description,dispatch,tool}]}
    commands: (id) => `${apiBase}/admin/agents/${id}/commands`,
    // 定义域文件（agent 即文件夹）：files 列目录 | file 读/写/建/删
    files: (id) => `${apiBase}/admin/agents/${id}/files`,
    file: (id) => `${apiBase}/admin/agents/${id}/file`,
    // 上传到定义域（multipart：file + dir；dir 缺省即定义域根目录，skills/ 拒绝）
    upload: (id) => `${apiBase}/admin/agents/${id}/upload`,
    // 定义域文件流式下载（只读；拖出到桌面取字节，需带 ?path=）
    download: (id) => `${apiBase}/admin/agents/${id}/download`,
    // 会话工作区文件（运行产物：浏览 / 下载 / 删除）。session_id 必传，path 缺省即目录根
    sessionFiles: (id, sessionId, path = '') =>
      `${apiBase}/admin/agents/${id}/session/files${wsQuery({ session_id: sessionId, path })}`,
    // GET 读内容（带 path）| POST 新建（path 在 body，查询串只带 session_id）| DELETE 删除（带 path）
    sessionFile: (id, sessionId, path = '') =>
      `${apiBase}/admin/agents/${id}/session/file${wsQuery({ session_id: sessionId, path })}`,
    // 会话工作区文件下载（登录态 XHR，二进制/大文件走这里）
    sessionDownload: (id, sessionId, path = '') =>
      `${apiBase}/admin/agents/${id}/session/download${wsQuery({ session_id: sessionId, path })}`,
    // 重命名 / 移动（POST {path, new_path}，目标必须不存在）。
    // 改名只能走这条：新建接口的 content 是字符串，二进制文件没法「下载再新建」。
    // session_id 与其他会话文件接口一样走查询串 —— 后端用它定位 user + 会话目录
    sessionRename: (id, sessionId) =>
      `${apiBase}/admin/agents/${id}/session/rename${wsQuery({ session_id: sessionId })}`,
    // 下载票（POST，登录态）：拿到 schema/path/exp/sig 后拼成 URL 导航下载
    downloadTicket: (id) => `${apiBase}/admin/agents/${id}/session/download-ticket`,
    // 导航式下载入口（无 Authorization 头，靠 URL 里的短时签名）
    ticketDownload: (id) => `${apiBase}/admin/filedl/${id}`,
    // 最近改动/生成的文件（工作台「新生成的文件」标签）；limit 缺省 30，后端自己也会封顶
    sessionRecent: (id, sessionId, limit = 30) =>
      `${apiBase}/admin/agents/${id}/session/recent${wsQuery({ session_id: sessionId, limit })}`,
    // 会话工作区上传：multipart，file + session_id + dir。
    // dir 是工作区内的相对目录（工作区面板传当前浏览的目录）；不传 → 落 uploads/（chat 输入框「+」的老行为）
    uploadSessionFile: (id) => `${apiBase}/admin/agents/${id}/session/upload`,
    // 工具授权（工具授权体系）：GET 读取 | PUT 全量替换（{unrestricted, grants}）
    toolGrants: (id) => `${apiBase}/admin/agents/${id}/toolgrants`,
    // 实际生效工具预览（授权解析结果 + 降级标记 + 告警）
    effectiveTools: (id) => `${apiBase}/admin/agents/${id}/effective-tools`,
    // 版本：正式运行读 is_current 那一份；草稿只在编辑页调试时生效。
    // 发布 = 冻结当前草稿；载入 = 该版本写回草稿（不动线上）；删除 = 只清历史
    versions: (id) => `${apiBase}/admin/agents/${id}/versions`,
    // 回滚 = 线上切到该版本（不动草稿）；载入 = 该版本写回草稿（不动线上）
    rollbackVersion: (id, version) => `${apiBase}/admin/agents/${id}/versions/${version}/rollback`,
    applyVersion: (id, version) => `${apiBase}/admin/agents/${id}/versions/${version}/apply`,
    deleteVersion: (id, version) => `${apiBase}/admin/agents/${id}/versions/${version}`
  },

  // 内置工具清单（智能体设置 → 工具配置）
  agentTools: {
    list: `${apiBase}/admin/agenttools/`
  },

  // 工具授权：可引用目录（天然组 / 预设组 / 自定义组 / 连接 + 展开成员）
  // 传 agentId 时按该 agent 身份生成：非超级智能体会隐藏「仅超级智能体可用」的管理类工具；
  // 不传时返回全量（skill 工具依赖挑选器等场景需要）
  toolCatalog: {
    list: (agentId = '') =>
      `${apiBase}/admin/toolcatalog/${agentId ? `?agent_id=${encodeURIComponent(agentId)}` : ''}`
  },

  // 自定义工具组：GET 列表 | POST 创建/更新（按 name upsert）| DELETE 删除
  toolGroups: {
    list: `${apiBase}/admin/toolgroups/`,
    upsert: `${apiBase}/admin/toolgroups/`,
    remove: (name) => `${apiBase}/admin/toolgroups/${encodeURIComponent(name)}`
  },

  // 工具授权：某 ref 的引用明细（agent 授权 / skill 依赖 / 自定义组成员）
  // prefix=true 时连带统计子项（如 ref=mcp:fs 也匹配 mcp:fs:read_file）
  toolGrants: {
    usages: (ref, prefix = false) =>
      `${apiBase}/admin/toolgrants/usages?ref=${encodeURIComponent(ref)}${prefix ? '&prefix=1' : ''}`
  },

  // 技能API
  skills: {
    base: `${apiBase}/admin/skills/`,
    create: `${apiBase}/admin/skills/create`,
    get: (id) => `${apiBase}/admin/skills/${id}`,
    update: (id) => `${apiBase}/admin/skills/${id}`,
    delete: (id) => `${apiBase}/admin/skills/${id}`,
    status: (id) => `${apiBase}/admin/skills/${id}/status`,
    // 可见性：GET 查看 / PUT 设置（{restricted, agent_ids}）
    visibility: (id) => `${apiBase}/admin/skills/${id}/visibility`,
    // 内容目录文件管理（与 agent 工作区文件接口同构；入口 SKILL.md 不可删除）
    files: (id) => `${apiBase}/admin/skills/${id}/files`,
    file: (id) => `${apiBase}/admin/skills/${id}/file`,
    // 上传到内容目录（multipart：file + dir；dir 缺省即技能根目录）
    upload: (id) => `${apiBase}/admin/skills/${id}/upload`,
    // 技能内容文件流式下载（只读；拖出到桌面取字节，需带 ?path=）
    download: (id) => `${apiBase}/admin/skills/${id}/download`,
    // 工具依赖（skill 声明「需要哪些工具」）
    toolGrants: (id) => `${apiBase}/admin/skills/${id}/toolgrants`,
    // 版本：只覆盖**租户级**技能。私有技能的版本就是所属 agent 的版本
    // （见 /agents/:id/versions），这里对私有技能会返回空列表
    versions: (id) => `${apiBase}/admin/skills/${id}/versions`,
    rollbackVersion: (id, version) => `${apiBase}/admin/skills/${id}/versions/${version}/rollback`,
    applyVersion: (id, version) => `${apiBase}/admin/skills/${id}/versions/${version}/apply`,
    deleteVersion: (id, version) => `${apiBase}/admin/skills/${id}/versions/${version}`
  },

  // 会话管理API
  sessions: {
    latest: `${apiBase}/admin/sessions/latest`,
    messages: (sid) => `${apiBase}/admin/sessions/${sid}/messages`,
    delete: (sid) => `${apiBase}/admin/sessions/${sid}`,
    // 工作区用量（只读计数，不扫目录）：{used_bytes, limit_bytes, sessions_used, sessions_limit}
    usage: (sid) => `${apiBase}/admin/sessions/${sid}/usage`
  },

  // LLM模型API
  llmModels: {
    base: `${apiBase}/admin/llmmodels/`,
    // 缺省模型（无则后端回 "Default LLM model not found"）：登录后据此引导去配置
    getDefault: `${apiBase}/admin/llmmodels/default`,
    provision: (id) => `${apiBase}/admin/llmmodels/${id}/provision`,
    deprovision: (id) => `${apiBase}/admin/llmmodels/${id}/deprovision`,
    deprovisionAll: `${apiBase}/admin/llmmodels/deprovision-all`,
    // 逐个检查本地配置同步到 LiteLLM（配置对齐 + 清理多余）
    sync: `${apiBase}/admin/llmmodels/sync`
  },

  // LLM服务商API
  llmProviders: {
    base: `${apiBase}/admin/llmproviders/`
  },

  // LLM服务商预设API（系统级，全局共享）
  llmProviderPresets: {
    list: `${apiBase}/admin/llmproviderpresets/`
  },

  // 服务端点API
  svcEndpoints: {
    base: `${apiBase}/admin/svcendpoints/`,
    create: `${apiBase}/admin/svcendpoints/create`,
    update: `${apiBase}/admin/svcendpoints/`,
    delete: `${apiBase}/admin/svcendpoints/`,
    retract: `${apiBase}/admin/svcendpoints/retract`,
    deleteByTarget: `${apiBase}/admin/svcendpoints/delete-by-target`
  },

  // 工作流实例API
  flows: {
    base: `${apiBase}/admin/flows/`,
    create: `${apiBase}/admin/flows/create`,
    get: `${apiBase}/admin/flows/`,
    update: `${apiBase}/admin/flows/`,
    delete: `${apiBase}/admin/flows/`,
    status: (id) => `${apiBase}/admin/flows/${id}/status`,
    // 运行记录：GET 列表（qs 带 limit / offset / run_type）| GET 详情 /:runId | DELETE 单条 / 全部
    runs: (id, qs = '') => `${apiBase}/admin/flows/${id}/runs${qs ? `?${qs}` : ''}`,
    run: (id, runId) => `${apiBase}/admin/flows/${id}/runs/${runId}`,
    // 版本：正式入口（MCP/RPC/agent 直调）只运行 is_current 那一版，草稿只在试运行里生效
    versions: (id) => `${apiBase}/admin/flows/${id}/versions`,
    rollback: (id, version) => `${apiBase}/admin/flows/${id}/versions/${version}/rollback`,
    applyVersion: (id, version) => `${apiBase}/admin/flows/${id}/versions/${version}/apply`,
    // 删除历史版本（DELETE）：只清历史。当前版本不可删 —— 正式调用读的就是 is_current 那一行
    deleteVersion: (id, version) => `${apiBase}/admin/flows/${id}/versions/${version}`
  },

  // 工作流运行参数API（platform.Flow_configs）
  flowconfigs: {
    get: `${apiBase}/admin/flowconfigs/`,
    update: `${apiBase}/admin/flowconfigs/`
  },

  // 智能体运行参数API（platform.agent_configs）—— 与 flowconfigs 对称，
  // 区别是本表不接受 -1（四项都是安全阀，调成「不限制」等于拆掉防线）
  agentconfigs: {
    get: `${apiBase}/admin/agentconfigs/`,
    update: `${apiBase}/admin/agentconfigs/`
  },

  // 全局变量API（{schema}.global_vars）
  //
  // 本租户内全局：工作流里作为节点输入变量的来源（source_type: "global"）、
  // 智能体/技能文档写 ${global_vars.名字}。资产包只带引用名不带值 ——
  // 这是「导出导入不把值写死在资产里」的落点，装到别的租户填一次即可。
  //
  // GET 列表（secret 的值一律掩码，只回 has_value）
  // POST 保存（按 name upsert；secret 且 value 为空 = 不改原值）
  // GET  引用面（删除确认框用它列出「谁会坏」）
  // DELETE 删除（默认拦引用：仍被引用时返回错误，带 force=1 才真删）
  globalVars: {
    list: `${apiBase}/admin/globalvars/`,
    save: `${apiBase}/admin/globalvars/`,
    usage: (name) => `${apiBase}/admin/globalvars/${encodeURIComponent(name)}/usage`,
    remove: (name, force) =>
      `${apiBase}/admin/globalvars/${encodeURIComponent(name)}${force ? '?force=1' : ''}`
  },

  // MCP 连接API
  mcpConnections: {
    base: `${apiBase}/admin/mcpconnections/`,
    create: `${apiBase}/admin/mcpconnections/create`,
    get: `${apiBase}/admin/mcpconnections/`,
    update: `${apiBase}/admin/mcpconnections/`,
    delete: `${apiBase}/admin/mcpconnections/`,
    tools: (id) => `${apiBase}/admin/mcpconnections/${id}/tools`,
    refreshTools: (id) => `${apiBase}/admin/mcpconnections/${id}/refresh-tools`
  },

  // RPC 连接API
  rpcConnections: {
    base: `${apiBase}/admin/rpcconnections/`,
    create: `${apiBase}/admin/rpcconnections/create`,
    get: `${apiBase}/admin/rpcconnections/`,
    update: `${apiBase}/admin/rpcconnections/`,
    delete: `${apiBase}/admin/rpcconnections/`,
    tools: (id) => `${apiBase}/admin/rpcconnections/${id}/tools`,
    refreshTools: (id) => `${apiBase}/admin/rpcconnections/${id}/refresh-tools`
  },

  // 插件API（磁盘 Python 插件：发现 + 租户级启用/配置/试跑）
  // 没有 create：插件靠把代码放进 {root_dir}/plugins/tools/<name>/ 自注册
  plugins: {
    list: `${apiBase}/admin/plugins/`,
    refresh: `${apiBase}/admin/plugins/refresh`,
    update: (name) => `${apiBase}/admin/plugins/${encodeURIComponent(name)}`,
    test: (name) => `${apiBase}/admin/plugins/${encodeURIComponent(name)}/test`
  },

  // 自定义工具API（{schema}.custom_tools：租户自定义的 custom__* 工具）
  // 与插件相反：这里是我们自己创建的实体（元数据在库、代码在
  // {root_dir}/tenants/<schema>/tools/<name>/）；删除会一并删盘上目录
  customTools: {
    list: `${apiBase}/admin/customtools/`,
    create: `${apiBase}/admin/customtools/create`,
    get: (name) => `${apiBase}/admin/customtools/${encodeURIComponent(name)}`,
    update: (name) => `${apiBase}/admin/customtools/${encodeURIComponent(name)}`,
    remove: (name) => `${apiBase}/admin/customtools/${encodeURIComponent(name)}`,
    // 试跑：与线上调用同一条执行内核，回 stdout / stderr / 退出码 / 耗时
    run: (name) => `${apiBase}/admin/customtools/${encodeURIComponent(name)}/run`,
    // 内容目录文件管理（tools/<name>/，与技能内容目录同构；TOOL.json 只读、入口文件不可删）
    files: (name) => `${apiBase}/admin/customtools/${encodeURIComponent(name)}/files`,
    file: (name) => `${apiBase}/admin/customtools/${encodeURIComponent(name)}/file`,
    upload: (name) => `${apiBase}/admin/customtools/${encodeURIComponent(name)}/upload`,
    download: (name) => `${apiBase}/admin/customtools/${encodeURIComponent(name)}/download`,
    // 版本：name 创建后不可改，版本目录只按 name 键（tools/<name> → versions/tools/<name>/v<n>）
    versions: (name) => `${apiBase}/admin/customtools/${encodeURIComponent(name)}/versions`,
    rollbackVersion: (name, version) => `${apiBase}/admin/customtools/${encodeURIComponent(name)}/versions/${version}/rollback`,
    applyVersion: (name, version) => `${apiBase}/admin/customtools/${encodeURIComponent(name)}/versions/${version}/apply`,
    deleteVersion: (name, version) => `${apiBase}/admin/customtools/${encodeURIComponent(name)}/versions/${version}`
  },

  // MCP 鉴权（入站）配置API（mcp_server_configs，按 auth_type 分行）
  mcpServerConfig: {
    get: (authType) => `${apiBase}/admin/mcpserverconfig?auth_type=${authType}`,
    update: `${apiBase}/admin/mcpserverconfig`
  },

  // RPC 鉴权（入站）配置API（rpc_server_configs，按 auth_type 分行）
  rpcServerConfig: {
    get: (authType) => `${apiBase}/admin/rpcserverconfig?auth_type=${authType}`,
    update: `${apiBase}/admin/rpcserverconfig`
  },

  // 服务端认证身份绑定API（svc_auth_bindings，MCP/RPC 共用）
  svcAuthBindings: {
    list: (authType) => `${apiBase}/admin/svcauthbindings${authType ? `?auth_type=${authType}` : ''}`,
    create: `${apiBase}/admin/svcauthbindings/create`,
    update: (id) => `${apiBase}/admin/svcauthbindings/${id}`,
    delete: (id) => `${apiBase}/admin/svcauthbindings/${id}`,
    // 端点访问范围：按 agent / flow 目标授权，覆盖该目标在 rpc 与 mcp 两条入口上的端点
    targets: (id) => `${apiBase}/admin/svcauthbindings/${id}/targets`,
    targetCandidates: `${apiBase}/admin/svcauthbindings/target-candidates`
  },

  // 租户用户 API（用户管理 + 绑定选择用户）
  // role: manage 可登录控制台 / service 不可登录（仅作为 API 调用身份）
  // status: active / disabled / locked
  users: {
    list: `${apiBase}/admin/users`,
    create: `${apiBase}/admin/users/create`,
    update: (id) => `${apiBase}/admin/users/${id}`,
    password: (id) => `${apiBase}/admin/users/${id}/password`,
    // 单用户配额覆盖：PUT {max_sessions, max_session_storage_bytes}，null = 跟随租户
    quota: (id) => `${apiBase}/admin/users/${id}/quota`
  },

  // 租户级配额自助（platform.quota_configs 的租户行；GET 回原始行+系统顶+生效值+用量，PUT 五项）
  tenantQuota: `${apiBase}/admin/tenantquota`,

  // 跨租户资源分发（上架 → 平台审核 → 安装；复制式，装完与快照无关）
  // kind: skill / custom_tool / agent；装完的重名冲突是 HTTP 200 + code 400 + data.suggested_name
  share: {
    // 可安装列表（已通过审核且对本租户可见）：?kind=&q=&origin=
    // origin：'' = 全部来源；'local' = 本机上架；其它 = 该对端站点导入的（回包里带 origins 汇总）
    // 分页：「加载更多」按 offset 追加；回 {items,total,limit,offset,has_more,origins}
    assets: (kind = '', q = '', origin = '', limit = 0, offset = 0) => {
      const p = new URLSearchParams()
      if (kind) p.set('kind', kind)
      if (q) p.set('q', q)
      if (origin) p.set('origin', origin)
      if (limit) p.set('limit', String(limit))
      if (offset) p.set('offset', String(offset))
      const qs = p.toString()
      return `${apiBase}/admin/share/assets${qs ? `?${qs}` : ''}`
    },
    asset: (id) => `${apiBase}/admin/share/assets/${id}`,
    // 上架：{kind, ref}（skill / agent 传 id，custom_tool 传名字）→ 进 pending 等平台审核
    listings: `${apiBase}/admin/share/listings`,
    mine: `${apiBase}/admin/share/mine`,
    // 撤回：pending → draft；draft → 直接删除（含快照目录）；已通过的只能平台下架
    withdraw: (id) => `${apiBase}/admin/share/mine/${id}/withdraw`,
    // 直接删除一条资产（草稿/待审/被驳回/已下架都行，已上架的不行）
    deleteMine: (id) => `${apiBase}/admin/share/mine/${id}`,
    // 一键清空未上架的资产（已上架的保留）
    clearMine: `${apiBase}/admin/share/mine`,
    // 安装：{asset_id, name?, status?, overwrite?}；回 {installed:{name,id}, warnings:[], skipped_files:[]}
    // 同名回 code 400 + data{suggested_name, can_overwrite}；带 overwrite:true 重试即覆盖
    install: `${apiBase}/admin/share/install`,
    // 安装前预检（只读）：POST {asset_id, name?} → 与导入预检同形
    // {kind,name,deps,summary,missing,existing,bundled,notices,notes}
    inspectInstall: `${apiBase}/admin/share/install/inspect`,
    installs: `${apiBase}/admin/share/installs`,
    // 删一条安装流水（不卸载实体）
    deleteInstall: (assetId, installedId) =>
      `${apiBase}/admin/share/installs/${assetId}/${installedId}`,
    // 清空本租户的全部安装流水（同样不卸载实体）
    clearInstalls: `${apiBase}/admin/share/installs`,
    // 远端市场（pull-through）：平台只配「本租户能看哪个站」，不替租户导入。
    // ?peer=&kind=&q= → {peers:[{peer, install_policy, server?, items[], count, truncated, error?}]}
    // items 每条带 local{asset_id,version,status} / installed / stale（本机状态标注）
    // 不分页：每站最多回 200 条（先按 q/kind 过滤再截断），用 search 收敛
    remoteAssets: (peer = '', kind = '', q = '') => {
      const p = new URLSearchParams()
      if (peer) p.set('peer', peer)
      if (kind) p.set('kind', kind)
      if (q) p.set('q', q)
      const qs = p.toString()
      return `${apiBase}/admin/share/remote-assets${qs ? `?${qs}` : ''}`
    },
    // 拉取：{peer, asset_id, force?} → 本机平台资产行（review 策略下落 pending，不进市场）
    pullAsset: `${apiBase}/admin/share/pull-asset`,
    // 导出本租户实体为资产包：?kind=&ref=（skill / agent / workflow 用 id，custom_tool 用名字）
    // 回 zip 流（需带 Authorization，走 fetch + blob）；提示在 X-Export-Warnings 头里
    exportAsset: (kind, ref) =>
      `${apiBase}/admin/share/export?kind=${encodeURIComponent(kind)}&ref=${encodeURIComponent(ref)}`,
    // 导入资产包（multipart：file 必填；name / status / kind / overwrite 可选）。
    // 不落平台资产、不进审核，直接装成本租户实体；重名回 data.suggested_name
    // （overwrite=1 时覆盖同名实体，四类都支持）
    importAsset: `${apiBase}/admin/share/import`,
    // 导入前预检（只读）：列出包内依赖（技能 / 工具 / 被调用智能体 / 能力）在本租户是否存在
    inspectImport: `${apiBase}/admin/share/import/inspect`
  },

  // 当前登录用户自助接口
  me: {
    password: `${apiBase}/admin/me/password`
  }
};

export default adminApi;
