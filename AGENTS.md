# 术语约定（改代码前先读）

四个领域的动作词各有一套，**不要跨领域借词**，也不要为了“更顺口”在某一层换说法。

本文件只是**入口摘要与红线**；完整对照、状态机、已落地范围以 `nexus/docs/terminology.md` 为准。
两处若不一致，改本文件去对齐主文档，不要反过来。

| 领域 | 动作 | 状态 / 字段 | 库 |
|---|---|---|---|
| 资产市场 | `list` / `delist` | `listed` / `delisted` | `platform.shared_assets.status`、`listed_by` |
| 端点 | `expose` / `retract` | `exposed`（bool） | `svc_endpoints.exposed`、镜像 JSON 键 `exposed` |
| 授权 / 凭证 | `revoke` | — | `revoke_agent_tool` 等 |
| 版本 / 部署 | `release` / `deploy`；回滚 `rollback`；载入 `apply` | `is_current` | 版本目录 `versions/` + `is_current` |

版本域已落地：`rollback` 只切 `is_current`（不动草稿），`apply` 只写草稿（不动线上）；
`revoke` 取的是当前授权，回滚与载入都不回写它。

**禁止混用**：
- 不要用 `publish` / `published` 表示资产或端点（资产域已改 `listing`，端点域已改 `expose`）；
- 不要用 `revoke` 表示“收回端点对外”，那是 `retract`；
- 不要用 `hide` 表示端点收回，那是可调用性而非可见性；
- 界面文案与代码/库必须同词：资产「上架 / 下架」，端点「对外开放 / 收回对外」。

**看到混用怎么办**：报告，不要擅自改名。改名需要同时改：
符号、接口字段、数据库列/状态值与镜像 JSON、前端、文档、迁移脚本。

## 模块地图

四个 Go module，靠 `replace` 串联，没有 go.work：

| 目录 | module | 说明 |
|---|---|---|
| `nexus/` | `flame/nexus` | 主服务：平台 API、租户 API、admin API |
| `sandboxd/` | `flame/sandboxd` | 执行沙箱守护进程（隔离、worker 池、浏览器） |
| `sandboxg/` | `flame/sandboxg` | 沙箱网关（对外代理、按租户选节点） |
| `tools/` | `flame/tools` | 三方共用的本地库（`xhttp` / `xsql` / `xlog` / `sandbox` / `code` …） |

依赖方向单向：三个服务用 `replace flame/tools => ../tools` 引用本地库，**`tools` 不反向依赖任何服务**。

验证：

- 只改某个服务：在该目录跑 `go build ./...`
- 改 `tools/`：必须把 `nexus`、`sandboxd`、`sandboxg` 三个都编一遍，否则下游编译错误不会暴露
