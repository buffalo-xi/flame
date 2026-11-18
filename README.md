# flame

Go 语言设计的企业级工作流与智能体运行环境，参照 openclaw + dify 的特性，一起创建吧。

多租户 AI Agent 平台。把「智能体 / 技能 / 工具 / 工作流」这些构建物管起来，把它们暴露成对外可调的端点（MCP / RPC），并在一个统一的运行台账里看到所有正在跑的东西。

配套一个 Vue 3 管理台（`desktop/`），租户端与平台端共用一套前端。

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
- `AGENTS.md` — 模块地图与跨领域红线

## 安全提示

- `nexus/config.yaml`、`desktop/.env` 都是本机配置，不入库。
- `nexus/data/`（租户工作区）被忽略，不会被提交。
- 生产部署前请改掉默认口令，并给 `sandbox` / `sandboxg` 配 token。

## 许可

[Apache License 2.0](LICENSE)
