# Flame PoC —— 双镜像演示环境

中文 | [English](README.md)

一个容器跑完整套产品栈——数据库、缓存、API、沙箱、浏览器池和 Web 控制台——模型网关则由官方 LiteLLM 镜像作为第二个容器承担。`docker compose up` 就是全部安装步骤。这是一个**概念验证**，不是部署方案：放到任何可被访问的环境之前，请先读[不适合生产](#不适合生产)。

逐项操作说明（账号、第一次接模型、两套控制台、从外部调用）：[`MANUAL.zh.md`](MANUAL.zh.md)。

## 跑着什么

两个镜像，一个 compose 项目：

| 容器 | 镜像 | 作用 |
|---|---|---|
| `flame` | 由 `deploy/poc/Dockerfile` 构建 | 产品栈；唯一的对外端口（**27001**，仅回环） |
| `flame-litellm` | `docker.litellm.ai/berriai/litellm-database:lts` | 模型网关；模型都是通过它 provision 的 |

`flame` 内部：

| 进程 | 端口（仅回环） | 作用 |
|---|---|---|
| `nginx` | 27001 | 唯一入口：`/` 出桌面端，其余全部反代给 nexus |
| `nexus` | 27006 | 平台 + 租户 API、智能体运行时 |
| `sandboxd` | 27081 | 代码执行（`mode: none`——容器本身就是沙箱） |
| `browserd` | 27091 | 浏览器池，一会话一 Chromium |
| `postgres` | 5432 | PostgreSQL **18**（来自 PGDG 仓库）；`flame`（产品数据）+ `litellm`（网关自己的数据） |
| `redis` | 6379 | nexus 的硬依赖（运行账本、停止信号、配额计数） |

网关为什么单独一个镜像：它原来是 flame 镜像里的一次 pip 安装，等于让产品镜像背着 ~400 MB Python 依赖，而且每个依赖都得有与基础镜像 Python 版本匹配的 wheel（升 3.13 会被 `uvloop` / `pydantic-core` / `orjson` 卡住）。一个 tag 比一个 venv 好推理，网关也能独立升级、不碰产品镜像。必须是 `litellm-database` 而不是普通 `litellm`：后者没有 Prisma，而「控制台加个模型能落库」靠的就是 `store_model_in_db`。

这里**没有** `sandboxg` / `browserg`，也**没有** consul：那两个网关存在的意义是「多实例下的粘性路由 + 一份全局配额」。单实例时 nexus 直连 `sandboxd`、`browserd` 和网关，路由与协议完全相同，只是少一个需要解释的部件。

## 智能体脚本跑在什么上面

| | 版本 | 说明 |
|---|---|---|
| Python | **3.13** | Debian 13 基础镜像；预装 `openpyxl` / `xlrd` / `pandas` / `numpy` / `requests` / `python-docx` / `beautifulsoup4` |
| Lua | 5.4 | `lua` 是 `/usr/bin/lua5.4` 的符号链接 |

这些 Python 包在构建期就装好，**故意不让智能体运行期自己装**：docker 模式的沙箱是 `--network none` + 只读 rootfs，「我在脚本里 pip install 一下」在这个箱子里能成、到真沙箱里必失败。`docker exec <容器> cat /etc/flame-versions` 可以查到镜像里实际是什么版本。镜像里没有 node，所以 `sandboxd.yaml` 里 `js` 语言是关掉的。

## 跑起来

要求：Docker Desktop（或 Docker Engine）、约 3 GB 镜像（flame ≈ 1.2 GB + litellm 1.73 GB）、4 GB 内存、x86-64 宿主——镜像固定 `GOARCH=amd64`，`docker-compose.yml` 里声明了 `platform: linux/amd64`，所以 ARM 机器走模拟而不是报 `exec format error`。

先拿到文件：下面所有命令都相对于仓库根目录，而且 compose 文件会从自己所在目录挂载 `./litellm.yaml`（构建时又把仓库根当上下文），所以这个目录必须是完整的：

```bash
git clone https://github.com/buffalo-xi/flame.git
cd flame
```

然后二选一：**拉预构建镜像**（不需要 Go / Node，也不需要构建，只想试功能就走这条）：

```bash
docker compose -f deploy/poc/docker-compose.yml pull
docker compose -f deploy/poc/docker-compose.yml up -d
```

或者**自己构建**（编译发生在镜像的构建阶段，宿主不需要工具链，但要能访问 Go 模块代理与 npm 源；5–10 分钟，首次最慢）：

```bash
docker compose -f deploy/poc/docker-compose.yml up -d --build
```

两种方式之后都一样：打开 <http://localhost:27001>。一个端口后面是**两套控制台、两套账号**，这里最容易搞混：

- **`primary` / `12345678`**：`t0` **租户内部**的管理员，进的是租户端控制台（智能体、技能、工作流、对话，以及这个租户自己的供应商 / 模型 / 连接）。它是租户账号，看不到别的租户，也没有平台侧的租户/用户管理。
- **`plat` / `12345678`**：**平台管理员**，管租户、加 LLM 供应商与模型。**从这里进**——平台控制台的租户列表里就有演示租户 `t0`，从那个列表切进去。
- **`user01` / `12345678`**：平台侧 `user` 角色的账号，是 `t0` 的负责人，用它能看到非管理员视角（只有自己名下的租户，没有管理页）。

`docker-compose.yml` 里有三处「看着可选其实必需」的配置：

- `shm_size: "1gb"` —— Chromium 要用 `/dev/shm`，Docker 默认 64 MB 会让页面随机崩，现象像是「浏览器工具坏了」。
- `flame-data:/data` —— 想要租户数据在 `compose down` 之后还在就保留它；只想试一把可以去掉。
- `LITELLM_SALT_KEY` —— 必须和卷同寿。它加密网关数据库里的供应商 key：换掉之后库里已存的 key 全部解不开（模型还在列表里，但每次聊天都报认证错误）。

首次启动 2–3 分钟：网关要跑自己的 Prisma 迁移、nexus 等它就绪、沙箱要跑一次语言探测。跟着看日志：

```bash
docker compose -f deploy/poc/docker-compose.yml logs -f
```

`docker compose ... ps` 里 `flame` 只有「nginx 起来了 **且** nexus 就绪」才会报 `healthy`；而 nexus 在 postgres / redis / 网关任一不可达时对所有请求返回 `503`——所以这个 `healthy` 是真信号，不是「进程启动了」。

## 三步开聊

`flame` 容器每次启动都会幂等地预置一套可用的环境（`nexus/cmd/seedpoc`），所以手工什么都不用建：

| 账号 | 密码 | 是什么 |
|---|---|---|
| `plat` | `12345678` | 平台管理员——进控制台用的账号 |
| `user01` | `12345678` | 平台 `user` 角色，演示租户的负责人 |
| `primary` | `12345678` | `t0` 租户**内部**的管理员（开通时交接的那个账号） |

租户 `t0`（“Demo tenant”）已经存在，带预装的 `main` 智能体、四个内置技能、预置的 LLM 供应商 `DeepSeek` 与模型
`deepseek-flash`（还没有密钥，供应商是停用状态），能力也全部打开。apikey
`sk-poc-t0-primary` 绑定在 `primary` 上，所以 `/service/mcp/t0`、`/service/rpc/t0` 认
`Authorization: Bearer sk-poc-t0-primary`。注意这句话的边界：凭证是就绪的，但租户还没有**已 expose** 的端点，
所以 `tools/list` 现在是空表——端点列表只包含 `exposed = true` 的那些。

1. **登录。** 用 `plat` / `12345678` 进入平台管理台，里面能看到 `t0`；进这个租户用 `primary` / `12345678`。
2. **把密钥填进预置模型。** 设置 → LLM 里已经有供应商 `DeepSeek`（停用、没有密钥）和模型 `deepseek-flash`：打开供应商填上自己的 key、保存、启用，然后在模型那一行点「启用」（这一步才是真正把模型注册进 LiteLLM 的动作），最后点「设为默认」。换厂商同理：就着预置的改，或者删掉自己加。没做完之前，控制台一切正常但每次回答都失败。
3. **开聊。** 打开 `main` 智能体发一条消息。它已经能用文件、shell 和浏览器。

镜像里**不带任何供应商密钥**：没有任何敏感信息被烤进镜像，第 2 步就是你填自己 key 的地方。从控制台继续新建租户照旧可用——`t0` 只是一个已经摆在那里的演示。

## 改代码，不重建镜像

镜像自带一份可运行的构建（在构建阶段编译），所以这一步是可选的——它就是「改完重启看效果」的循环：

```powershell
powershell -File deploy/poc/build.ps1     # 交叉编译 linux/amd64 二进制 + 构建前端，产物落在 out/
docker restart flame                      # 重启即生效
```

`build.ps1` 会把产物写到 `deploy/poc/out/{bin,dist}`，同时生成 `deploy/poc/out/compose-mounts.yml`（都已 gitignore）——一个把这两个目录挂进 `flame` 容器的 compose override——并打印命令：

```bash
docker compose -f deploy/poc/docker-compose.yml -f deploy/poc/out/compose-mounts.yml up -d
```

这些二进制是 **Linux** 二进制，在宿主机上跑不了——它们本来就是要给容器用的。这个循环完全不碰网关容器，它跑的是发布镜像。

想下断点调试单个服务，继续用各模块自己的方式（`go run`、Vite dev server）；这个容器是用来验证「整套装配在一起」的。

## 数据、卷与重置

| 容器内路径 | 内容 |
|---|---|
| `/data/pg` | postgres 数据目录 |
| `/data/tenants/<schema>/` | 各租户的定义域文件与智能体工作区 |
| `/data/platform-assets/` | 市场快照（不属于任何租户） |
| `/data/plugins/` | Python 插件 |
| `/var/lib/flame/profiles` | Chromium profile（每会话一个，会话结束即删） |

`/data` 是 nexus、sandboxd、browserd、nginx 共同挂载的**同一个绝对路径**——正是这个共享路径让「沙箱里写一个文件、租户能下载它」成立，也让浏览器截图落在模型读得回来的地方。

想在资源管理器里直接看这些文件？在 `docker-compose.yml` 旁边写一个 compose override 加绑定目录，但数据库仍留在卷上（9p/CIFS 文件系统与 postgres 合不来）：

```yaml
# docker-compose.override.yml
services:
  flame:
    volumes:
      - flame-pg:/data/pg
      - d:/flame-data:/data
```

重置：

```bash
docker compose -f deploy/poc/docker-compose.yml down        # 停掉两个容器，卷保留
docker compose -f deploy/poc/docker-compose.yml down -v     # 连卷一起删（数据全没）
```

从旧镜像升级：`nexus/platform/sql` 下的平台脚本每次启动都会重跑，但租户的**表结构与定义域文件**（`agents/main/AGENT.md`、四个受保护技能）只在创建租户时写入一次，保留的卷等于让新代码跑在旧形状上——演示环境请用 `down -v` 重建卷，这才是诚实的升级路径。

## 排障

| 现象 | 多半是 |
|---|---|
| 页面能开，所有接口 503 | 硬依赖还没起全——`docker compose ... logs -f flame \| tail -50` 看 `readiness` |
| 登录被拒 | 管理员种子没跑成功（看日志里的 bootstrap 输出），或密码不是 `12345678` |
| 启动停在 `check the preinstalled assets of t0` | 租户丢了 `main` 或四个受保护技能之一（只有手工改库才会这样）；换个干净的卷重启，或把那一行补回去 |
| 控制台正常，但每次回答都报模型错误 | 还没配供应商/模型（上面的第 2 步） |
| 浏览器工具报「不可用」 | Chromium 起不来，最常见的原因是把 compose 里的 `shm_size` 删了 |
| `flame-litellm` 一直重启 | 它先于 flame 初始化完就开始连库：`docker compose ... logs flame-litellm`，报 `pg_hba` 就是放行 compose 网段的那行 trust 缺失，报连接被拒就是 PG 还没起来 |
| 模型都在，但每次聊天都报认证错误 | `LITELLM_SALT_KEY` 变了：库里存的供应商 key 解不开。改回原值，或重新填一次供应商 key |
| 启动报 `the data volume was created by postgres 17, this image ships 18` | `/data` 卷是 PG 18 基础镜像之前建的。数据目录不能跨大版本打开——换干净卷，或对旧库跑 `pg_upgrade` |
| `port is already allocated` | 27001 被占用，改 `docker-compose.yml` 端口映射左边那个 |
| 构建在 `go build` 阶段报连不上 proxy.golang.org | 构建容器到默认 Go 模块代理没有路由——在 `deploy/poc/.env` 里设 `GOPROXY`（如 `https://goproxy.cn,direct`），或加 `--build-arg GOPROXY=...` |

`docker exec flame supervisorctl status` 能看到产品容器里的六个进程及其重启次数。网关没有 supervisor：`docker compose ... ps` 加它自己的日志就是全部。

## 不适合生产

- **`plat` / `12345678` 是烤进镜像的**，预置的 `user01` / `primary` 共用这个密码，而平台管理员是受保护账号（改不了）。
- `sandboxd` 是 `mode: none`：智能体的 shell 就是这个容器里的普通进程，能读 `/data`，也就是能读所有租户的文件。单人在笔记本上可以，多租户绝对不行。
- 服务间 token 为空，JWT / value / 网关密钥都是 PoC 值。postgres 现在监听范围超出了回环，并对整个 compose 网段 trust（网关要连进来）——没有映射到宿主，但任何加入这个 compose 项目的容器都能读两个库。
- 两个容器共用 `LITELLM_MASTER_KEY` 与 `LITELLM_SALT_KEY`：拿到任一个容器环境变量的人就拿到了网关的控制面。
- 两个容器实际仍是同一个故障域：`flame` 崩了，数据库和 API 一起没，网关离了它们也没用。

请把它当成「可以丢给别人试一下的演示」，而不是「一套安装」。真正部署时，还是这些服务各跑各的容器、打开隔离、前面放上网关。
