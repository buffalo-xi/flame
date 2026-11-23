# Flame PoC 操作手册

中文 | [English](MANUAL.md)

这份手册面向「在本机用 Docker 把 PoC 跑起来、再把功能逐项验证一遍」的人。安装细节与设计说明在
[README](README.zh.md)；这里只讲**怎么操作**。它不覆盖生产部署、集群、TLS 与 SSO——见文末「已知限制」。

## 目录

1. [五分钟跑起来](#1-五分钟跑起来)
2. [两套控制台、三个账号](#2-两套控制台三个账号)
3. [第一次必须做：接一个模型](#3-第一次必须做接一个模型)
4. [验证智能体](#4-验证智能体)
5. [租户侧的六件事](#5-租户侧的六件事)
6. [平台侧的四件事](#6-平台侧的四件事)
7. [从外部调进来](#7-从外部调进来)
8. [数据、备份、重置、升级](#8-数据备份重置升级)
9. [排障](#9-排障)
10. [已知限制](#10-已知限制)

## 1. 五分钟跑起来

```bash
git clone https://github.com/buffalo-xi/flame.git
cd flame
docker compose -f deploy/poc/docker-compose.yml pull     # 或 up -d --build 从源码构建
docker compose -f deploy/poc/docker-compose.yml up -d
```

判断它真的起来了，而不是「进程还在」：

```bash
docker compose -f deploy/poc/docker-compose.yml ps       # flame 显示 healthy
docker compose -f deploy/poc/docker-compose.yml logs -f  # 首次 2–3 分钟：网关迁移 → SQL → seedpoc → supervisord
curl -fsS http://localhost:27001/healthz                 # 200
```

浏览器打开 <http://localhost:27001>。首次启动慢是正常的：网关要跑 Prisma 迁移，nexus 会一直返回 503 直到
postgres、redis、网关都可连——所以 `healthy` 的意思是「真的能服务」，不是「进程起来了」。

| 想做什么 | 命令 |
|---|---|
| 看日志 | `docker compose -f deploy/poc/docker-compose.yml logs -f flame` |
| 看六个进程 | `docker exec flame supervisorctl status` |
| 重启（改代码后） | `docker restart flame` |
| 停止（保留数据） | `docker compose -f deploy/poc/docker-compose.yml down` |
| 清空重来 | `docker compose -f deploy/poc/docker-compose.yml down -v` |

## 2. 两套控制台、三个账号

登录卡片右上角可以在**租户登录**与**平台登录**之间切换。它们对应两套独立会话，可以同时存在；平台顶栏的
「返回控制台」在租户会话还在时直接切回去。

| 账号 | 密码 | 从哪个入口登 | 它是什么 |
|---|---|---|---|
| `plat` | `12345678` | 平台登录 | 平台管理员：租户管理、平台用户、插件与能力、系统设置、本地/远端市场 |
| `primary` | `12345678` | 租户登录，**租户栏填 `t0`** | `t0` 内部的管理员：智能体、技能、工具、工作流、运行、端点、资产、设置 |
| `user01` | `12345678` | 平台登录 | 平台 `user` 角色，只有「我的租户」，看不到管理页 |

最容易卡住的一点：**租户登录除了账号密码还要填「租户」**（也就是 schema 名，演示租户是 `t0`）。只填
`primary` / `12345678` 而不填 `t0` 不会成功。

改密码：两个控制台的顶栏都有「密码」，各自改自己那一侧账号的密码。`plat` 是受保护账号，密码改不了。

## 3. 第一次必须做：接一个模型

镜像里**没有**任何供应商密钥。没接模型时每个页面都能点，但智能体一问就失败——网关里没有可调的模型。

在**租户控制台**：设置 → **LLM 配置**。下面以 **DeepSeek 走 OpenAI 兼容协议**为例，换成别的厂商只改地址与模型名。
DeepSeek 没有内置预设，所以用通用预设 `openai_compatible`——它就是所有 OpenAI 协议厂商的入口。

**第一步：添加 LLM 服务商**

1. 左侧「选择预设服务商」里点 **openai_compatible**：协议会被填成 `openai`（该字段只读），地址先给一个占位值
2. 服务商名称：`DeepSeek`
3. 接入地址：`https://api.deepseek.com`
4. API Key：`sk-...`（在 DeepSeek 开放平台创建）
5. 额外配置 (JSON)：**留空**。这个字段进的是 LiteLLM 的 `litellm_params`（**注册期**参数，如 Azure 的
   `api_version`、Vertex 的 `vertex_project`），不是每次请求的请求体——别把模型参数写在这里

**第二步：添加 LLM 模型**（同一页 → 添加 LLM 模型，服务商选刚建的 DeepSeek）

| 字段 | 填什么 | 说明 |
|---|---|---|
| 模型名称 | `deepseek-flash` | 网关按 `openai/<模型名称>` 调上游，所以**必须与上游模型标识一致** |
| 上下文窗口 | `128000` | 按供应商文档填，用于前端估算与截断 |
| 网关模型名 | `deepseek-flash` | 客户端看到的别名，实际请求是 `<租户>:<这个名字>`；**启用后不可改**，先确认再启用 |
| 基础生成参数 | 按需开 `max_tokens` / `temperature` / `top_p` | 开关式：只有打开的项才写进 `base_params`（不存在＝不传，用供应商默认） |
| 额外参数 (JSON) | `{"thinking": {"type": "disabled"}}` | DeepSeek 的写法，见下一小节；换厂商要换成该厂商的开关 |
| 能力配置 (JSON) | 一般留空 | 例如 `{"vision": true}` 声明多模态 |

保存后同步进网关（LiteLLM 容器），智能体这时才有模型可用。

### `{"thinking": {"type": "disabled"}}` 是干什么的

这一行是 **DeepSeek 的扩展字段，不是本系统的通用要求**。换别家模型时按该厂商自己的规则写：OpenAI 推理模型用
`reasoning_effort`，Anthropic 是「不传 `thinking` 就是关」，Qwen / GLM 等各有各的开关；不确定就**留空**——
本系统只透传、不校验，不填也照样能正常调用。

它写在**模型的「额外参数」**里，请求时被塞进 `extra_body`，LiteLLM 不校验、原样转给上游（代码里的注释就是
`extra_params all go into extra_body`）。所以它是一句**给上游的指令：关掉这个模型的思考（reasoning）输出，只返回
正文**。同一份 JSON 也会写进 `model_info`（注册期元数据），但每个请求真正生效的是 `extra_body` 那一份。

演示环境默认关掉思考，有四个理由：

1. **延迟与成本**：思考内容按输出 token 计费，也拉高首字延迟；
2. **工具调用更稳**：智能体大量依赖 function calling（读文件、跑 Shell、开浏览器）。推理模式下模型常常先输出
   一大段 reasoning 再决定要不要调工具，部分端点在思考模式下对工具调用支持更弱；
3. **界面与工作流只要正文**：控制台把思考渲染成可折叠的「思考过程」块；工作流里的 LLM 节点通常只取正文，
   思考是纯负载；
4. **结果可复现**：同一模型开关思考，输出风格差别很大，演示时希望行为稳定。

两个边界：

- 本系统**只透传、不校验**这个字段。上游认它（DeepSeek 的 OpenAI 兼容端点用 `thinking.type` 切换思考模式）就
  生效；不认就按未知字段忽略，**不会因此报错**，差别只是回复里有没有思考内容。如果你的端点明确报「不支持该
  参数」，把这个 JSON 清空即可，功能不受影响。
- 想看推理过程就改成 `{"thinking": {"type": "enabled"}}`（或者直接用供应商的 reasoner 模型），控制台会把它显示成
  可折叠的「思考过程」。

两个坑：

- `LITELLM_SALT_KEY` 决定网关数据库里供应商密钥的加密，必须与卷同生命周期——换掉之后已存的 key 全部无法解密，
  现象是「模型还在列表里，每次调用都报鉴权失败」。
- 租户侧的 LLM 配的是**本租户自己的**供应商与模型；平台侧还有一层系统级配置，两者不要混。

## 4. 验证智能体

租户控制台 → 智能体 → `main`（预装、受保护）→ 顶栏的对话开关 → 发一句消息。

- 它在这个容器里已经能用文件、Shell 和浏览器（Chromium 已装，browserd 起了浏览器池）。sandboxd 用的是
  `mode: none`，所以「沙箱」的边界就是容器本身。
- 每次运行都会落一行记录：**运行 → 智能体历史**，能看到状态、耗时、问题与答案。
- 智能体设置面板两页：**调用授权**（它能用哪些技能）、**工具配置**（工具授权与最终生效的工具）。

自建一个：智能体 → 新建 → 编辑 `AGENT.md`（源码 / Markdown 预览可切换）→ 保存。

## 5. 租户侧的六件事

**5.1 技能**：`SKILL.md` + `references/` + 可选脚本。设置面板三页——**可见性**（租户内共享，还是某个智能体
私有）、**工具依赖**（技能只声明「需要什么工具」，授权在智能体侧给）、**属性**。

**5.2 工具**：工具目录 / 插件 / 自定义工具。这里只决定「存在」；某个智能体能不能用，在智能体设置面板里授权。

**5.3 工作流**：新建 → 从节点面板拖节点 → 试运行（本次运行 / 历史运行）→ 版本发布与部署。版本操作只用
`release` / `deploy` / `rollback` 这套词，不要说成 publish。

**5.4 运行**：运行中 / 工作流历史 / 智能体历史。

**5.5 资产**：资产市场 → 上架（list）/ 下架（delist）；也可以从别的租户安装。

**5.6 设置**（左栏十一项）：LLM 配置、MCP 连接（出站）、RPC 连接（出站）、MCP 鉴权（入站）、RPC 鉴权（入站）、
服务凭证、用户管理、全局变量、租户配额、工作流运行参数、智能体运行参数。

导航栏「端点」里默认是**不对外**。要让外部调到某个智能体/工作流，需要**对外开放**（expose）；收回时是
**收回对外**（retract）——revoke 只用于撤销授权/凭证。

## 6. 平台侧的四件事

**6.1 租户管理**：新建租户时 schema 由后端取下一个可用值（如 `t1`），只需要填显示名、首个管理员的账号
（默认 `primary`）与密码，负责人可选。租户行上还有能力开关（执行 / Shell / 出网 / 浏览器等），关掉即对该租户
生效。

**6.2 平台用户**：账号、角色（`admin` / `user`）、启用状态。`user` 角色登录后只有「我的租户」。

**6.3 插件与能力**：平台级的插件与能力目录；对单个租户的开放程度在 6.1 的租户行上控制。

**6.4 系统设置 / 本地市场 / 远端市场**：系统级限额；资产的上架策略与远端市场接入。

## 7. 从外部调进来

预置了一把 API Key：`sk-poc-t0-primary`，绑定 `t0` 的 `primary`，放在 `Authorization` 头里。三个入口都在同一个
端口上：

| 端点 | 用途 |
|---|---|
| `POST /service/mcp/t0` | MCP 协议：`initialize`、`tools/list`、`tools/call` |
| `POST /service/rpc/t0` | RPC 方式调用本租户接口 |
| `GET /healthz` | 存活与就绪（nginx → nexus `/health`） |

MCP 的响应是 server-sent events，所以 `Accept` 要带 `text/event-stream`；先握手，再列工具：

```bash
curl -s -X POST http://localhost:27001/service/mcp/t0 \
  -H 'Authorization: Bearer sk-poc-t0-primary' \
  -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"curl","version":"1"}}}'
# 200，返回 event: message data: {"jsonrpc":"2.0","id":1,"result":{"serverInfo":{"name":"flame-mcp-server","version":"2.0"},…}}

curl -s -X POST http://localhost:27001/service/mcp/t0 \
  -H 'Authorization: Bearer sk-poc-t0-primary' \
  -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}'
# 200，返回 {"jsonrpc":"2.0","id":2,"result":{"tools":[]}}
```

`tools: []` 是正常的：`tools/list` **只列已对外开放的端点**，而刚跑起来的 `t0` 还没有任何 `exposed = true` 的
端点。先在控制台把某个智能体或工作流「对外开放」，它才会出现在这里。不带 `Authorization` 时是 401。

> Windows PowerShell 里 `curl` 是 `Invoke-WebRequest` 的别名，请写 `curl.exe`。

## 8. 数据、备份、重置、升级

| 容器内路径 | 内容 |
|---|---|
| `/data/pg` | postgres 数据目录 |
| `/data/tenants/<schema>/` | 每个租户的定义域文件与智能体工作区 |
| `/data/platform-assets/` | 市场快照（不属于任何租户） |
| `/data/plugins/` | Python 插件 |
| `/var/lib/flame/profiles` | Chromium 配置（按会话，会话结束即删） |

**备份**（卷名以 `docker volume ls` 为准，默认是 `flame-poc_flame-data`）：

```bash
docker volume ls | grep flame
docker run --rm -v flame-poc_flame-data:/data -v "$PWD":/backup alpine \
  tar czf /backup/flame-data.tgz -C /data .
```

**重置**：`down -v`，下次 `up -d` 会重新建库、建 `t0`、重新种演示数据。

**升级**：换掉镜像后 `docker compose ... up -d`。启动时 `nexus/platform/sql` 下的脚本会重跑一遍，能带过平台侧的
结构变化；但**租户的表结构与定义域文件只在创建租户时写入**，所以跨版本升级请直接 `down -v` 重建卷，否则会出现
「新代码 + 旧表」——最典型的表现就是智能体历史一直是空的。

**改代码不重建镜像**：`powershell -File deploy/poc/build.ps1` 交叉编译并生成 override，然后 `docker restart flame`；
细节见 README。

## 9. 排障

| 现象 | 处理 |
|---|---|
| 页面打不开，`ps` 显示 `127.0.0.1:27001->27001/tcp` | 端口只绑回环；本机用 `http://localhost:27001`，要给别的机器用就改映射 |
| `port is already allocated` | 27001 被占用，改 compose 端口映射左边那个 |
| `healthy` 迟迟不出现 | 首次要 2–3 分钟；`logs -f` 看卡在哪一步 |
| 登录成功但一问就报错 | 没接模型，见 §3 |
| 模型在列表里，调用报鉴权/解密错误 | `LITELLM_SALT_KEY` 变过，已存 key 不可解密，重新填 key |
| `/service/...` 返回 405 | 镜像比本文旧（nginx 没有 `/service/` 转发）；换新镜像，或自己加 location |
| 智能体历史一直是空的 | 旧数据卷缺表/缺列，`down -v` 重建（§8） |
| 工具调用失败、浏览器崩溃 | 多为浏览器进程被杀；`shm_size` 已给到 1 GB，看 `docker logs flame` 里 browserd 的段 |
| 构建卡在 `go build`，报连不上 proxy.golang.org | 构建容器到默认 Go 代理没有路由；在 `deploy/poc/.env` 设 `GOPROXY`（如 `https://goproxy.cn,direct`） |
| 想直接查库 | `docker exec flame psql -h 127.0.0.1 -U postgres -d flame`（必须带 `-h`：cluster 的 socket 在 `/tmp`） |

## 10. 已知限制

- 演示密码是**烤进镜像**的占位值（`plat` 还是受保护账号，改不了密码）；没有 TLS。
- `mode: none` 下智能体的 Shell 以容器 root 身份运行，能读到容器内的一切——包括 `/data/pg` 里的数据库和
  `/etc/flame` 下的密钥。因此它绝不能放在别人能访问的网络里。
- 镜像只有 amd64；ARM 机器走模拟（compose 里已声明 `platform`）。
