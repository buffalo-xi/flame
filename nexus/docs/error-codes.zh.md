# 业务错误码规范

中文 | [English](error-codes.md)

前端要支持中英双语，后端的用户可见文案就不能再是「一句话」。本文定义 `code + params` 的形状，
以及迁移方式。

## 现状

`xhttp` 信封（`tools/xhttp/http.go`）：

```json
{ "code": 200, "msg": "服务端点不存在: poet", "data": null, "trace": "host/path" }
```

问题：

- `code` 只有 `200 / 400 / 507` 三个值，是**传输层分类**，不是业务语义；
- `msg` 是拼好的中文，前端只能原样展示，无法翻译；
- 拼进去的参数（`poet`）与文案粘连，前端即使用正则会拆，也没法处理单复数与语序。

截至 2026-11，第一个问题已经消除：`nexus/` 里 `xhttp.ErrJSON` 调用点**归零**（函数本身也已从 `tools/xhttp` 删除），
每个 handler 的响应都带 `error` 码（纯运维失败也带，统一是 `common.internal`）；前端中英两份文案包
（`desktop/src/locales/{en,zh-CN}/error.json`）对后端会发出的每个码都有条目，用户能看到的错误不再回落到 `msg`。

## 目标形状

`code` 保留数字语义不变（兼容前端既有判断），新增 `error` 字段承载业务编码：

```json
{
  "code": 400,
  "error": "endpoint.not_exposed",
  "params": { "name": "poet" },
  "msg": "服务端点未对外开放: poet",
  "data": null,
  "trace": "host/path"
}
```

约束：

- `error` 缺省时前端回退到 `msg` —— 这只是**灰度期**的路径，见下方的顺序约束；
- `msg` 是**英文诊断文本**（日志、curl、旧前端），不是用户可见文案。用户读到的任何一句话都由 `error` 码 + 语言包
  渲染，所以 `msg` 绝不能成为用户可见错误的唯一通道；
- **顺序约束**：把 `msg` 改成英文，必须排在 `error` 覆盖率到 100% **之后**（flow 执行层、`SendErrorResponse` 的
  409/403/404、以及前端 24 个域）。提前改会让中文用户看到英文句子；
- `params` 只放可展示的标量（名字、数量、上限），不放内网地址、SQL、堆栈。

## 命名规则

`<域>.<动作或状态>`，全小写 + 点分隔，用词与 `nexus/docs/terminology.md` 对齐：

| 域 | 示例 | 说明 |
|---|---|---|
| `auth` | `auth.invalid_credentials` | 登录失败（不与「用户不存在」区分） |
| `tenant` | `tenant.not_found`、`tenant.schema_taken` | `schema_taken` 对应 409 |
| `endpoint` | `endpoint.not_found`、`endpoint.not_exposed` | 端点域用 expose/retract 用词 |
| `asset` | `asset.not_found`、`asset.not_available`、`asset.storage_unavailable`、`asset.build_conflict`（`params.build`、`data.existing`） | `not_available` = 存在但未对本租户开放 |
| `flow` | `flow.not_released`、`flow.deprecated`、`flow.node_limit_exceeded`（`params.count`、`params.limit`） | 与 `models.StatusDeprecated`「废弃」同词，界面也叫废弃。`node_limit_exceeded` = 图里的节点数超过租户配额（`flow_configs.max_node_count`），在保存与资产导入时校验 |
| `agent` | `agent.call_depth_exceeded`、`agent.not_found` | |
| `skill` | `skill.not_found`、`skill.not_invocable`、`skill.invalid_command` | `not_invocable` = 技能存在但没开放为用户命令（`user_invocable` 未开）；`invalid_command` = `command_dispatch` 与 `command_tool` 不配套（只接受空值与 `tool`，选 `tool` 必须填目标工具） |
| `tool` | `tool.not_found`、`tool.contract_readonly` | 自定义工具（`customtools`）；`contract_readonly` 覆盖 TOOL.json 的建 / 改 / 删 / 上传 |
| `version` | `version.not_found` | 版本域，与 release / rollback / apply 同域 |
| `llm` | `llm.model_not_found`、`llm.provider_not_found`、`llm.no_default_model`、`llm.default_model_not_enabled` | 后两个表示「这一轮跑不起来，因为租户没有可用的默认模型」；属于 `ConfigError`，见「高频路径先改」里的说明 |
| `user` | `user.not_found`、`user.invalid_role`、`user.invalid_status`、`user.password_too_long` | 平台用户 / 租户用户 |
| `session` | `session.not_found` | 会话本身；令牌失效是 `auth.session_expired` |
| `run` | `run.not_found` | 运行台账的记录 |
| `mcp` / `rpc` | `mcp.connection_not_found`、`rpc.connection_not_found`、`mcp.auth_type_required` / `rpc.auth_type_required`、`mcp.invalid_auth_type` / `rpc.invalid_auth_type` | 连接域，两个协议各用各的；`auth_type` 两个来自服务端配置接口 |
| `binding` | `binding.not_found`、`binding.invalid_auth_type`、`binding.external_subject_required`、`binding.invalid_target_type`、`binding.target_id_required` | 端点 / 工具的授权绑定 |
| `toolgrant` | `toolgrant.ref_not_found`、`toolgrant.item_not_found`、`toolgrant.invalid_ref`、`toolgrant.invalid_item`、`toolgrant.invalid_grant_kind`、`toolgrant.invalid_group_name`、`toolgrant.ref_required`、`toolgrant.name_required`、`toolgrant.group_in_use`（`params.count`） | ref / item 下没有任何可用工具；`invalid_*` 表示 ref 自身格式非法 |
| `plugin` | `plugin.not_found`、`plugin.not_enabled_for_tenant`、`plugin.invalid_selection` | 插件本体在磁盘上被发现，不是租户实体 |
| `globalvar` | `globalvar.not_found`、`globalvar.in_use` | `in_use` = 引用面扫不出来；`referenced` = 扫出来了且确实有引用（带 `params.count`） |
| `peer` | `peer.not_found`、`peer.unreachable` | 对端市场（`platform/handler/peers.go`）；`unreachable` = 连不上或令牌不对 |
| `capability` | `capability.dependency_missing` | 能力开闭的依赖准入（`params.deps` 是「 / 」连接的候选清单） |
| `quota` | `quota.session_storage_exceeded` | 对应 507 |
| `sandbox` | `sandbox.not_ready` | |
| `agentconfig` | `agentconfig.invalid_value`、`agentconfig.exceeds_system_limit` | 智能体运行参数（`platform/handler/agentconfigs.go` 与 `admin/agentconfigs`），形状同 `quota.*`：`params.field` / `params.min` / `params.limit`；本表没有 `-1`，所以 `min` 恒为 1 |
| `common` | `common.invalid_params`、`common.internal` | |

文件类统一用 `<域>.file_not_found`（`agent.file_not_found` / `skill.file_not_found` / `tool.file_not_found`）：
同一句「文件不存在」在不同域下的处置引导不同，别合并成 `common.not_found`。

「可操作性」类编码（以 agent 为例，其它域同理）：`agent.deprecated`（废弃后不可改 / 删 / 发布 /
载入 / 切版本）、`agent.invalid_name`、`agent.invalid_status`、`agent.file_too_large`（`params.limit_mb`）、
`agent.skills_dir_readonly`、`agent.last_version_undeletable`（`params.version`）、`agent.rename_failed`。
工作区文件接口每类被拒操作一个编码，调用方不必读 `msg`：`agent.path_required`、`agent.invalid_path`
（`tools.SafePath` / `checkEntryPath` 的任一规则）、`agent.invalid_rename`、`agent.file_exists`（`params.path`）、
`agent.file_required`、`agent.invalid_file_name`、`agent.delete_failed`、`agent.upload_failed`、
`agent.upload_save_failed`、`agent.session_no_owner`、`agent.invalid_doc_refs`（`.md` 里引用了密钥型全局变量）。
技能文件接口在 `skill.*` 下有一组同名后缀：`skill.path_required`、`skill.invalid_path`、`skill.file_too_large`、
`skill.file_exists`、`skill.entry_undeletable`、`skill.delete_failed`、`skill.upload_failed`、
`skill.upload_save_failed`、`skill.invalid_doc_refs`，技能 CRUD 另加 `skill.protected`、`skill.name_taken`、
`skill.owner_agent_not_modifiable`、`skill.private_no_visibility`。
自定义工具的文件接口在 `tool.*` 下用同一组后缀：`tool.path_required`、`tool.invalid_path`、`tool.file_too_large`、
`tool.file_required`、`tool.invalid_file_name`、`tool.delete_failed`、`tool.upload_failed`、`tool.upload_save_failed`。
纯运维失败（`xx 失败: <err>`）不进新编码，统一 `common.internal`，原始错误留在 `msg` 里给日志和 curl。

禁止：

- 用 `publish` / `hide` 表示资产或端点（见术语文档）；
- 只写 `error.1`、`error.2` 这类无语义编号；
- 把变量拼进 `error`（`endpoint.not_exposed.poet` 是错的，名字进 `params`）；
- 把点号编码塞进 `SendErrorResponse` 的 `error` —— 那是**传输层 reason 枚举**
  （`not_found` / `unauthorized` / `forbidden` / `conflict` …），两套信封不要混；
  这类响应（如租户开通抢占的 409、`mustOwnTenant` 的 403/404）暂时仍只有 `message`，前端按 `msg`/`message` 兜底。

## 迁移方式

分三步，可分批推进，每步都能独立上线：

1. **加字段**（已完成）：
   - `tools/xhttp`：`ErrCode(c, errCode, params, msg, trace)`，业务码固定 400，旧接口不动；
     另有 `ErrCodeData(...)` —— 同名冲突这类还要带 `data`（`suggested_name` / `can_overwrite`）的
     错误走它，别为了加编码把 `data` 丢掉；
   - `nexus/service/rpc/server`：`writeRPCErrorCode(...)`，业务编码落在 `error.data`。
2. **高频路径先改**：登录、租户、端点、配额、flow 运行 —— 这几条覆盖绝大多数用户可见报错。
   已完成试点：`endpoint.not_found` / `endpoint.not_exposed`（RPC 服务端）；
   登录路径已铺：`auth.invalid_credentials` / `auth.account_not_console` / `auth.account_locked` /
   `auth.account_disabled`（`shared/auth/login.go`、`platform/handler/login.go`），
   参数非法与内部故障分别用 `common.invalid_params` / `common.internal`；
   租户：`tenant.not_found`（`platform/handler` 下 9 处，`params.name` 带 schema）；
   配额：`quota.session_storage_exceeded` / `quota.user_sessions_exceeded`，
   507 统一从 `respondQuotaErr` 发出，用 `xhttp.ErrQuotaCode`（配额按 `metric` 分编码；
  `data` 带 `scope` / `metric` / `used` / `limit`，另有 `evicted` —— 预检已经删掉的文件，界面据此说明「什么消失了」）；
   flow 运行入口（RPC 服务端）：`flow.not_released`、`flow.deprecated`，
   走 `writeRPCErrorCode` 落在 `error.data.error`。MCP 服务端按本文末节不改造；
   flow 执行层（executors 的 `rpc_target`）与被调方内部错误仍只有文案（没有编码通道）；
  **因配置缺失而跑不起来的一轮**只在 agent 层归类一次（`agent.ConfigError`：
  `llm.no_default_model` / `llm.default_model_not_enabled`），各入口按自己的协议渲染 —— SSE 对话用带 `code` 的
  `RUN_ERROR` 帧；JSON-RPC 用 `-32002` 加 `error.data.error`；MCP 用 `isError` 结果带那句话（那边没有编码通道）；
  flow 节点记 `config`；`call_agent` 把带被调方名字的句子当工具结果返回。**一律不报** `common.internal` /
  `-32603` / `ErrCodeOther`：这是操作者要改配置，报「服务端故障」会把人支到错误的方向；
   端点 / 资产的 HTTP 侧（`admin/svcendpoints`、`platform/handler`）：`endpoint.not_found`、
   `asset.not_found`，参数非法与缺上下文归 `common.invalid_params` / `common.internal`；
   资源类 not found 已扫全库（`ErrJSON(c, "... not found")` 已零残留）：`agent.*`、`skill.*`、
   `version.*`、`flow.*`、`llm.*`、`user.*`、`session.*`、`run.*`、`mcp.*`、`rpc.*`、`binding.*`、
   `toolgrant.*`、`*.file_not_found`，以及 `Authentication info(mation) not found` 统一归
   `common.internal`（这一条占全部报错点的大头，属路由/装配疏漏，`msg` 保留原文便于排障）；
   `admin/agents` 的失败类文案（废弃 / 非法入参 / 过大 / 只读 / 唯一版本）已铺 31 处；
   `admin/llmmodels`、`admin/flows`、`admin/runs`、`admin/mcpconnections`、`admin/rpcconnections`、
   `admin/svcauthbindings` 已铺 18 处（`llm.model_*` / `flow.invalid_status` / `run.*` /
   `mcp|rpc.discovery_failed` / `binding.target_not_exposed`）；
   `admin/skills`(26)、`admin/plugins`(15)、`admin/globalvars`(11) 也已铺完；
   `admin/users`(32) 已铺：`user.invalid_name` / `user.name_taken` / `user.primary_readonly` /
   `user.cannot_modify_self` / `user.last_admin_undeletable` / `user.password_*`（必填 / 过短 /
   未变 / 不正确 / 服务用户不可设），配额用 `quota.invalid_value` / `quota.self_lockout` /
   `quota.exceeds_tenant_limit` / `quota.exceeds_system_limit`（`params.field` 带字段名）；
   新建撞到租户数量上限时用 `quota.agent_limit_reached` / `quota.skill_limit_reached` / `quota.flow_limit_reached`
   （`params.used` / `params.limit`）—— 以前这里报的是 `common.internal`，等于告诉管理员"你自己的配额是服务器故障"；
   智能体运行参数（`platform/handler/agentconfigs.go` 与 `admin/agentconfigs`）在用户填的值小于 1 或超过系统级时
   改答 `agentconfig.invalid_value` / `agentconfig.exceeds_system_limit`，字段名走 `params.field`，不再是
   `common.internal`；
   `admin/customtools`(54) 已铺：`tool.invalid_name` / `tool.name_taken` / `tool.not_found` /
   `tool.invalid_contract` / `tool.deprecated` / `tool.contract_readonly`（TOOL.json 四个写口同码）/
   `tool.entry_exists` / `tool.entry_readonly` / `tool.invalid_path` / `tool.file_too_large` /
   `tool.contract_write_failed` / `tool.entry_write_failed`（部分成功语义）/
   `tool.capability_denied` / `tool.last_version_undeletable`，沙箱门用 `sandbox.not_ready`；
  `admin/share`(52) 已铺：`asset.*`（`invalid_kind` / `ref_required` / `invalid_status` / `not_owner` /
  `name_conflict` / `invalid_package` / `package_too_large` / `package_too_new` / `kind_mismatch` /
  `record_write_failed` / `install_not_found` / `stale_build` / `peer_not_available`），
  同名冲突用 `xhttp.ErrCodeData` 保住 `data.suggested_name` / `can_overwrite`；
  `platform/handler`(101) 已铺：`tenant.*`（`owner_not_found` / `owner_invalid_role` / `invalid_status` /
  `must_suspend_first`）、`user.*`（平台账号，与租户侧同码）、`peer.*`、`plugin.*`、
  `capability.dependency_missing`、`llm.preset_*`、`asset.*`，配额沿用 `quota.*`。
  至此 `ErrJSON + 中文` 在 `nexus` 已零残留（`admin/agents/session_guard.go` 那处是英文常量透出，
  中文只出现在注释里），MCP 服务端按末节不改造。
3. **前端兜底**：语言包里维护 `error.*` 映射，查不到时回退 `msg`。
   消费面已统一：组件里不再直出 `body.msg` / `data.msg`，一律 `formatError(res)`（内部就是
   `te(error.xxx) ? t(...) : msg`），所以后端铺编码时前端不用跟着改。

### 前端消费

```js
const text = te(`error.${res.error}`)
  ? t(`error.${res.error}`, res.params || {})
  : res.msg
```

`te()` 判断 key 是否存在，避免漏翻时显示 `error.xxx` 原文。

## 与 MCP / RPC 的关系

MCP / RPC 走 JSON-RPC，错误对象里要用**标准错误码**（`-32602` 等），业务编码放在 `data.error`：

```json
{ "jsonrpc": "2.0", "id": 1,
  "error": { "code": -32602, "message": "service endpoint does not exist: poet", "data": { "error": "endpoint.not_found", "params": { "name": "poet" } } } }
```

原因：外部客户端（Claude Desktop、第三方 MCP 宿主）只认 JSON-RPC 的错误码语义，
把业务编码塞进 `code` 会让它们误判为协议错误。

补充：**MCP 服务端不需要 `endpoint.*` 这两个编码**。它按 `protocol=mcp AND exposed=true`
加载端点，无权目标在注册阶段就被跳过，调用根本到不了 handler，因此不存在
「不存在 / 未开放」的运行期分支（见 `nexus/service/mcp/server/server.go`）。
MCP 的消费方是 LLM 宿主而非本项目的界面，`data.error` 对它价值很低，不必为它改造。

## 安全约束

对外入口的错误文案**不得泄漏存在性**：未授权与不存在必须用同一个 `error`（`endpoint.not_found`），
且 `params` 里只带调用方自己给的名字。这条与 `service/rpc/server/server.go` 现有的处理一致，迁移时不要破坏。
