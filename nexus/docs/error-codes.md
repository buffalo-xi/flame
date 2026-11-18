# 业务错误码规范

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

- `error` 缺省时前端回退到 `msg`（灰度期必须能跑）；
- `msg` **继续返回中文**，供日志、curl、旧前端使用 —— 不要删；
- `params` 只放可展示的标量（名字、数量、上限），不放内网地址、SQL、堆栈。

## 命名规则

`<域>.<动作或状态>`，全小写 + 点分隔，用词与 `nexus/docs/terminology.md` 对齐：

| 域 | 示例 | 说明 |
|---|---|---|
| `auth` | `auth.invalid_credentials` | 登录失败（不与「用户不存在」区分） |
| `tenant` | `tenant.not_found`、`tenant.schema_taken` | `schema_taken` 对应 409 |
| `endpoint` | `endpoint.not_found`、`endpoint.not_exposed` | 端点域用 expose/retract 用词 |
| `asset` | `asset.not_found`、`asset.not_available` | `not_available` = 存在但未对本租户开放 |
| `flow` | `flow.not_released`、`flow.archived` | |
| `agent` | `agent.call_depth_exceeded` | |
| `quota` | `quota.session_storage_exceeded` | 对应 507 |
| `sandbox` | `sandbox.not_ready` | |
| `common` | `common.invalid_params`、`common.internal` | |

禁止：

- 用 `publish` / `hide` 表示资产或端点（见术语文档）；
- 只写 `error.1`、`error.2` 这类无语义编号；
- 把变量拼进 `error`（`endpoint.not_exposed.poet` 是错的，名字进 `params`）。

## 迁移方式

分三步，可分批推进，每步都能独立上线：

1. **加字段**（已完成）：
   - `tools/xhttp`：`ErrCode(c, errCode, params, msg, trace)`，业务码固定 400，旧接口不动；
   - `nexus/service/rpc/server`：`writeRPCErrorCode(...)`，业务编码落在 `error.data`。
2. **高频路径先改**：登录、租户、端点、配额、flow 运行 —— 这几条覆盖绝大多数用户可见报错。
   已完成试点：`endpoint.not_found` / `endpoint.not_exposed`（RPC 服务端）。
3. **前端兜底**：语言包里维护 `error.*` 映射，查不到时回退 `msg`。

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
  "error": { "code": -32602, "message": "服务端点不存在: poet", "data": { "error": "endpoint.not_found", "params": { "name": "poet" } } } }
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
