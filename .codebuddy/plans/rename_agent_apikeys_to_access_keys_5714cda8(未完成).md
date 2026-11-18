---
name: rename_agent_apikeys_to_access_keys
overview: 将数据库表 agent_apikeys 改名为 access_keys，新增 client_id（字符串列）与 protocols（jsonb 字符串数组列，值含 rpc/llm，可扩展），并同步修改后端 Go 代码（models/repository/handlers）与前端 nexus_desk 的 API Key 管理界面（Settings.vue）。原加载的 RPC 错误方案与本任务无关，以本方案为准。
todos:
  - id: verify-no-other-refs
    content: 用 [subagent:code-explorer] 确认无遗漏的 agent_apikeys 引用与前端展示点
    status: pending
  - id: models-fields
    content: models.go 为 AgentAPIKey 增加 ClientID、Protocols xsql.JSONB 字段
    status: pending
  - id: repo-table-rename
    content: postgres.go 表名改 access_keys 并加 client_id、protocols 读写与 Scan
    status: pending
    dependencies:
      - models-fields
  - id: handlers-client-protocols
    content: handlers.go 的 Create/Update 支持 client_id、protocols 并回传
    status: pending
    dependencies:
      - repo-table-rename
  - id: frontend-settings
    content: Settings.vue 展示与编辑 client_id、protocols 多选
    status: pending
    dependencies:
      - models-fields
  - id: build-verify
    content: go build 与前端 lint 验证通过并复核字段一致
    status: pending
    dependencies:
      - handlers-client-protocols
      - frontend-settings
---

## 用户需求
将数据库表 `agent_apikeys` 改名为 `access_keys`，并新增两列：`client_id`（字符串类型）与 `protocols`（jsonb 字符串数组，存储允许的协议，当前为 `rpc` 和 `llm`，后续可扩展）。据此同步修改前后端代码。

## 产品概述
后端把 API Key 存储表从 `agent_apikeys` 重命名为 `access_keys`，模型与仓储层新增 `client_id`、`protocols` 字段（protocols 用 `xsql.JSONB` 与项目其他 jsonb 字段保持一致）；管理接口支持读写这两个字段（创建时 protocols 默认 `['rpc','llm']`）。前端在「智能体 API 密钥」管理界面中展示并编辑 `client_id` 与 `protocols`，统一前后端数据表达。

## 核心特性
- 表名由 `agent_apikeys` 改为 `access_keys`（SQL 层全部切换，Go 路由路径不变）
- 新增 `client_id`（字符串）与 `protocols`（jsonb 字符串数组）两列，protocols 默认 `['rpc','llm']`
- 后端 `AgentAPIKey` 模型新增 `ClientID`、`Protocols` 字段，仓储增删改查覆盖新列
- 前端密钥卡片展示协议标签与 client_id，对话框支持编辑 client_id 与协议多选


## 技术栈
- 后端：Go + Gin，`flame/tools/xsql`（含 `JSONB` 类型），`flame/tools/xhttp`，PostgreSQL
- 前端：Vue 3 + Element Plus（`nexus_desk`），组件内 `fetch` 调用 admin API

## 实现方案
### 策略
后端在 `models`/`repository`/`handlers` 三层同步扩展：表名常量化拼接改为 `access_keys`，模型增加 `ClientID`、`Protocols xsql.JSONB`；handlers 的 Create/Update 增加 `client_id`、`protocols []string` 入参，以 `xsql.JSONB{Data: protocols}` 写入，响应回传数组。前端 `Settings.vue` 增加展示与编辑。复用既有 `xsql.JSONB`（专门支持 PG jsonb 列，与 `SvcEndpoint`/`PipeInstance`/`McpConnection` 一致），不引入新类型。

### 关键技术决策
1. **沿用 `xsql.JSONB`**：项目大量表使用 `xsql.JSONB`（`tools/xsql/sql.go` 第 398 行 `type JSONB struct{ Data interface{} }`），其 `Value()` 写库、`Scan()` 读库、`MarshalJSON()` 输出均适配 jsonb 数组；`Protocols.Data` 直接存 `[]string`，序列化即 `["rpc","llm"]`。
2. **保留原列与路由**：需求为"增加"列，故 `key/note/status/type` 不动；后端 API 路径 `/agentapikeys` 不变，前端 `admin.js` 的 `agentAPIKeys` 配置键无需改动，零破坏兼容。
3. **protocols 默认值**：Create 未传时默认 `['rpc','llm']`，与用户给定的初始集合一致，便于后续扩展新协议。

### 性能与可靠性
- 纯结构体字面量与 `[]string` 赋值，无额外分配；jsonb 序列化由 `xsql.JSONB` 完成，复用成熟实现。
- 表名仅在 SQL 字符串拼接处出现，集中于 `postgres.go` 一处；字段扩展对现有查询影响最小化。

## 实现注意事项
- `xsql.JSONB` 的 `.Data` 读取在 Update 回传时需做类型分支（`[]string` 直接用，否则 `json.Marshal(.Data)` 再 `Unmarshal`），避免 `len()`/`Unmarshal` 在 `JSONB` 上误用。
- `GetAgentAPIKey`/`ListAgentAPIKeys` 直接返回模型，依赖 `JSONB.MarshalJSON` 输出数组，前端按数组处理，无需额外转换。
- 必须单独执行 DB 迁移（非代码）：`ALTER TABLE <schema>.agent_apikeys RENAME TO access_keys; ADD COLUMN client_id text NOT NULL DEFAULT ''; ADD COLUMN protocols jsonb NOT NULL DEFAULT '["rpc","llm"]'::jsonb;`

## 架构设计
```mermaid
flowchart TD
    A[前端 Settings.vue] -->|POST /agentapikeys| B[CreateAgentAPIKey]
    A -->|PUT /agentapikeys/:id| C[UpdateAgentAPIKey]
    B --> D[models.AgentAPIKey{ClientID,Protocols xsql.JSONB}]
    C --> D
    D --> E[PostgresRepository 访问 access_keys 表]
    E --> F[(PostgreSQL: access_keys<br>client_id text, protocols jsonb)]
```

## 目录结构
```
d:/proj/flame/
├── nexus/shared/models/models.go                  # [MODIFY] AgentAPIKey 增加 ClientID string、Protocols xsql.JSONB
├── nexus/shared/repository/agentapikeys/postgres.go # [MODIFY] 5 处 SQL 表名 agent_apikeys→access_keys，加 client_id、protocols 读写与 Scan
├── nexus/admin/agentapikeys/handlers.go           # [MODIFY] Create/Update 增加 client_id、protocols 入参与回传；import encoding/json
└── nexus_desk/src/components/Settings.vue         # [MODIFY] 卡片展示与对话框编辑 client_id、protocols
```

## 关键代码结构
```go
// nexus/shared/models/models.go
type AgentAPIKey struct {
    ID        string      `json:"id"`
    Key       string      `json:"key"`
    Note      xsql.String `json:"note"`
    Status    string      `json:"status"`
    Type      string      `json:"type"`
    ClientID  string      `json:"client_id"`
    Protocols xsql.JSONB  `json:"protocols"`
    CreatedAt xsql.Time   `json:"created_at"`
    UpdatedAt xsql.Time   `json:"updated_at"`
}
```


## Agent Extensions
### SubAgent
- **code-explorer**
  - Purpose: 二次核对前后端是否还有遗漏的 `agent_apikeys` 表名引用、以及前端是否还有其他读取/展示 API Key 协议或 client_id 的组件（如 AgentInstances.vue 等），确保改名与加列无遗漏点
  - Expected outcome: 产出确认清单，证明除已修改文件外无其他引用需要改动，或列出必须补改的文件与行号
