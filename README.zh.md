# flame

中文 | [English](README.md)

Go 写的多租户 AI Agent 平台。把「智能体 / 技能 / 工具 / 工作流」这些构建物管起来，把它们暴露成对外可调的端点（MCP / RPC），并在一个统一的运行台账里看到所有正在跑的东西。

配套一个 Vue 3 管理台（`desktop/`），租户端与平台端共用一套前端。

## 功能

**构建物**

- **智能体**：提示词 + 模型 + 工具 + 技能的组合，可挂知识、可配工具权限
- **技能**（Skill）：`SKILL.md` + 脚本的能力包，脚本在沙箱里跑
- **工具**：工具目录（MCP / RPC 等入向能力）、插件、自定义工具
- **工作流**：可视化编排（节点 / 连线 / 变量），带调试器、版本与发布

**运行**

- **运行台账**：跨实例看「现在在跑什么」（智能体 + 工作流），按类型 / 耗时过滤，可终止
- **运行历史**：跑完落库的记录，按工作流 / 状态 / 时间段查，可导出 CSV、单条或按条件清理
- **端点**：把智能体 / 工作流暴露成 MCP 或 RPC 入口，按租户开关「对外开放 / 收回对外」
- **沙箱**：脚本与命令在独立单元里执行（docker / nsjail / none 三档），带资源上限与断网开关

**平台**

- **多租户**：每租户一个 PostgreSQL schema，平台端统一开租户、发账号
- **资产市场**：跨租户上架 / 安装资产（上架 `listed` / 下架 `delisted`）
- **设置**：模型与连接、服务端认证（apikey / oauth）、全局变量（secret 静态加密）

## 界面一览

| 运行台账 · 跨实例看「谁在跑」、跑了多久，可按类型 / 耗时过滤并直接终止 | 运行历史 · 跑完落库的记录，按工作流 / 状态 / 时间段查，可导出 CSV |
|---|---|
| ![运行台账](docs/screenshots/01-runtime.png) | ![运行历史](docs/screenshots/02-runtime-history.png) |
| 工作流编排 · 节点 / 连线 / 变量，带调试器、版本与发布 | 智能体 · 提示词 + 模型 + 工具 + 技能的装配面 |
| ![工作流编排](docs/screenshots/03-flow-editor.png) | ![智能体](docs/screenshots/04-agent.png) |
| 端点 · 把智能体 / 工作流暴露成 MCP / RPC，按租户开关对外 | 工具 · 工具目录 / 插件 / 自定义工具，三种来源统一管理 |
| ![端点](docs/screenshots/05-endpoints.png) | ![工具](docs/screenshots/06-capabilities.png) |

## 组成

| 目录 | 语言 | 作用 |
|---|---|---|
| `nexus/` | Go | 主服务：平台 API、租户 API、管理 API（默认 `:27006`） |
| `sandboxd/` | Go | 执行沙箱守护进程：脚本 / 命令 / 浏览器的隔离执行（默认 `:27081`） |
| `sandboxg/` | Go | 沙箱网关：对 `nexus` 暴露统一 `/v1/*`，后端做负载均衡与粘性路由（默认 `:27080`） |
| `tools/` | Go | 三个服务共用的本地库（`xhttp` / `xsql` / `xlog` / `sandbox` / `code` …） |
| `desktop/` | Vue 3 + Vite | 管理台（开发端口 `:5173`） |

依赖方向是单向的：三个服务用 `replace flame/tools => ../tools` 引用本地库，`tools` 不反向依赖任何服务。

```
浏览器 ── desktop ──> nexus ──┬──> PostgreSQL    业务数据（每租户一个 schema）
                              ├──> Redis         运行台账 / 停止信号 / 配额
                              ├──> LiteLLM       模型调用
                              └──> sandboxg ──> sandboxd ──> 隔离单元
                                                   （docker / nsjail / none）
```

## 设计要点

几条贯穿全仓的取舍，决定了这套东西在速度和边界上的表现。细节见 [`nexus/docs/design-highlights.md`](nexus/docs/design-highlights.md)。

- **端点在本租户进程内直调**：`rpc` 节点调 flow / agent 不走网络；stdio MCP 会话按连接复用子进程，配置变更才重建。
- **防环靠深度 + 祖先链，不靠计数器**：每次跨 run 都会新建 `ExecContext`、执行计数归零，所以只能让「层数」收敛。配置成 `-1` / `0`（不限）在这里**刻意不认** —— 纯 flow 的环只有这一道闸。
- **多租户 = 一租户一 schema + SQL 显式限定**：不依赖 `search_path`（连接池复用下不可靠）。配额与运行参数都是「系统硬顶 → 租户收紧 → 用户再收紧」的三层 clamp。
- **端点是一行「目标 × 协议」**：`exposed` 只表达「对外可见」，租户内部直调不受它限制；授权绑的是**目标**（agent / flow），不是端点 —— 端点可以重建，目标才是稳定身份。
- **热路径上少查一次库**：端点目录按运行快照、配置与配额走 Redis 读穿缓存（含负缓存）、运行参数在 run 开始时冻结一次。

## 环境要求

- Go **1.25+**
- Node **18+**（前端构建）
- PostgreSQL **14+**
- Redis **7+**
- LiteLLM Proxy（模型调用；不用模型可以关掉 `litellm.enabled`）
- 可选：Docker（`sandboxd` 的 `mode: docker`，生产推荐）、Consul（服务发现）

## 快速开始

### 1. 起依赖

本地起 PostgreSQL 与 Redis（默认端口 `5432` / `6379`），按需起 LiteLLM（`:4000`）。

### 2. 配 nexus

```bash
cp nexus/config.example.yaml nexus/config.yaml
```

然后编辑 `nexus/config.yaml`，至少改这几项：数据库连接、`jwt.secret_key`、`litellm.master_key`。

也可以全部走环境变量（`${VAR}` 会在 YAML 解析前展开）：

```bash
# Linux / macOS
export JWT_SECRET="$(openssl rand -base64 48)"
export LITELLM_MASTER_KEY="sk-..."
# Windows PowerShell
$env:JWT_SECRET = [Convert]::ToBase64String([byte[]](1..48 | ForEach-Object { Get-Random -Maximum 256 }))
```

> `jwt.secret_key` 泄露等于任何人都能自签 token 操作任意租户，务必用随机值，不要提交到仓库。
> 真正的 `nexus/config.yaml` 已被 `.gitignore` 排除。

### 3. 起服务

```bash
# nexus
cd nexus && go run ./cmd

# sandboxd（另开一个终端）
cd sandboxd && go run ./cmd/sandboxd

# 网关（可选，多后端负载均衡时才需要）
cd sandboxg && go run .
```

`sandboxd` 默认 `mode: none`（宿主直通、只用于本机开发）。生产请切到 `docker` 或 `nsjail`，
配置见 `sandboxd/config.yaml`。

### 4. 起前端

```bash
cd desktop
npm install
npm run dev      # http://localhost:5173
```

开发服务器把 `/nexus` 代理到 `http://localhost:27006`（见 `desktop/vite.config.js`）。

## 目录结构

```
nexus/
  admin/          管理端接口（平台 + 租户）
  service/        业务服务（agent / flow / capability / market …）
  shared/         配置、仓储、鉴权、日志
  platform/       平台初始化与租户模板（含 agent 定义域模板）
  docs/           术语、沙箱设计、工作区布局等文档
sandboxd/         隔离执行（docker / nsjail / none）+ codehost + browserhost
sandboxg/         沙箱网关
tools/            公共库
desktop/          Vue 3 管理台
```

## 文档

- `nexus/docs/terminology.md` — 术语与动作词约定（改代码前建议先读）
- `nexus/docs/sandbox-design.md` — 沙箱隔离设计与三条执行通道
- `nexus/docs/workspace-layout-and-supervisor-power.md` — 租户工作区布局
- `nexus/docs/design-highlights.md` — 设计要点：调用链防环、多租户配额、端点模型（[中文版](nexus/docs/design-highlights.zh.md)）
- `nexus/docs/i18n-plan.md` — 中英双语的分层方案、成本与坑位
- `nexus/docs/error-codes.md` — 业务错误码规范（UI 双语的前置条件）
- `AGENTS.md` — 模块地图与跨领域红线

## 安全提示

- `nexus/config.yaml`、`desktop/.env` 都是本机配置，不入库。
- `nexus/data/`（租户工作区）被忽略，不会被提交。
- 生产部署前请改掉默认口令，并给 `sandbox` / `sandboxg` 配 token。

## 许可

[Apache License 2.0](LICENSE)
