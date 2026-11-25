# 生产环境安装 Flame

[English](INSTALL.md) | 中文

面向一台没有工具链的机器，一条命令一条命令地装。设计推理在
[`nexus/docs/deployment.zh.md`](../../nexus/docs/deployment.zh.md)，验收检查在
[`nexus/docs/deployment-runbook.zh.md`](../../nexus/docs/deployment-runbook.zh.md)；本文只回答「按什么顺序敲哪条命令」：
**先装一台（tier 1），再让同一套安装长到多台，不需要重装。**

下文 `<owner>` 是发布镜像的 GitHub 账号；本仓库是 `buffalo-xi`。

## 0. 机器要求

| | |
|---|---|
| 架构 | `linux/amd64`（发布的镜像只有 amd64） |
| 权限 | root，systemd |
| 运行时 | Docker，带 `compose` 插件 |
| 网络 | 能访问 `github.com` 与 `ghcr.io`；否则走离线包（§1b） |
| 端口 | `27001` 空闲（`27443` 会发布端口，但只有证书就位才监听）；执行器监听的是 `0.0.0.0:27081` / `0.0.0.0:27091`，不要暴露到公网 |
| 存储 | `/srv/flame/data` 在本机文件系统上，属主 uid 1000（从 tier 2 起改为共享挂载） |
| 内核 | 沙箱里要跑容器，daemon 必须允许嵌套 Docker |

## 1. 拿到产物

两条路选一条，最后都落在同一个 `/srv/flame/dist`——下面所有命令都在那里执行。

### 1a. 从 Release 取（需要 `ghcr.io`）

```bash
mkdir -p /srv/flame/dist && cd /srv/flame/dist
curl -fLO https://github.com/<owner>/flame/releases/download/v2026.11.25/flame-prod-v2026.11.25.tar.gz
curl -fLO https://github.com/<owner>/flame/releases/download/v2026.11.25/SHA256SUMS
sha256sum -c SHA256SUMS
tar -xzf flame-prod-v2026.11.25.tar.gz
```

这个 tar 包是部署的**宿主侧那一半**：两个做不成容器的执行器二进制（`sandboxd`、`browserd`）加上
`browserhost` / `codehost`、systemd units、配置模板和这些脚本；四个运行时镜像（nexus、gateway、web、sandbox）
来自 GHCR，`base` 只在构建期用。

### 1b. 离线（目标机既不需要 registry 也不需要外网）

在一台装了 Go、Node、Docker 的构建机上：

```bash
cd deploy/prod
./scripts/release.sh --tag 2026.11.25        # 产物 + 镜像 + 两个 tar 包 + SHA256SUMS
```

把 `release/` 拷到目标机、在目标机解开，然后在第 2.2 步用 `--images` 代替 `--registry`。离线包连四个上游镜像
（consul、postgres、redis、litellm）一起带上。

## 2. 单机（tier 1）

数据面、应用面、两个执行器，全部在一台机器上。

### 2.1 预检（不改任何东西）

```bash
sudo ./scripts/install.sh --tag v2026.11.25 --check --registry ghcr.io/<owner>/flame
```

它会检查 Docker、compose 插件、openssl、uid 1000 是否可用、存储挂载与端口冲突，并逐个到 registry 解析镜像
manifest（不拉取）。先把每一条 `FAIL` 修掉再往下走；`warn` 只是提示（PATH 里没有 Chromium、端口被上一次安装
占着……）。

### 2.2 放置 release

```bash
sudo ./scripts/install.sh --tag v2026.11.25 --registry ghcr.io/<owner>/flame
# 走离线包时改为：
# sudo ./scripts/install.sh --tag v2026.11.25 --images /srv/flame/dist
```

这一步拉四个运行时镜像，放置 `/opt/flame/releases/<tag>` 并把 `/opt/flame/current` 指向它，写
`/etc/flame/flame.env`（0600，含新生成的密钥），渲染四份服务配置和 `redis.conf`，安装 `/usr/local/bin/flame`
与 units，**不启动任何东西**。

接着确认第五份配置在位——安装器会从带这份文件的每个 release 里写它，已经在位时这一步是空操作：

```bash
sudo test -f /etc/flame/nexus.yaml || \
  sudo install -m 0640 -o root -g flame config/nexus.yaml /etc/flame/nexus.yaml
```

`nexus.yaml` 是唯一**原样拷贝**的配置（其余都渲染），因为 nexus 自己从环境变量展开里面的 `${VAR}` 占位符。

### 2.3 按顺序启动

启动顺序即安装顺序：数据面 → schema → seed → 执行器 → 应用面。

```bash
flame up -d consul postgres redis litellm     # 数据面
./scripts/migrate.sh --check                  # SQL 文件集必须与执行清单一致
./scripts/migrate.sh
flame --profile jobs run --rm bootstrap       # 写出 /etc/flame/initial-credentials.txt（0600）
systemctl start flame-sandboxd flame-browserd # 两个裸机执行器
flame up -d                                   # nexus、两个网关、web
flame ps
./scripts/health.sh                           # 验收：失败项会被点名
```

`install.sh` 已经把 units 设成开机启用，留给你的只有 `systemctl start`，而且要等 schema 与 seed 就位之后再敲。

### 2.4 首次登录

打开 `http://<host>:27001`。

| | |
|---|---|
| 平台管理员 | `plat` / `12345678` |
| 租户管理员（`t0`） | `primary` / `12345678` |

这是 seed 的 PoC 账号。**立刻改密码并轮换 `FLAME_API_KEY`**——两者都在 `/etc/flame/flame.env` 里。各供应商的
API key 在控制台里添加，所有模型调用都经过 LiteLLM。

## 3. 长到多台

任何地方都不需要重装：同一份 release、同一批镜像，改 `/etc/flame/flame.env` 里的几行。加第一台执行器节点
的前提是共享存储已经挂好，因为沙箱写下的文件要由应用面去读。

| | tier 1（本文） | tier 2（加执行器） | tier 3（容器拆分） |
|---|---|---|---|
| 数据面、nexus、网关 | 一台 | 同一台 | 分散到多台 |
| `sandboxd` / `browserd` | 同一台 | 额外机器，裸机 | 同 tier 2 |
| `FLAME_BIND_IP` | `127.0.0.1` | 本机内网 IP | 同左 |
| `FLAME_CONSUL_ADDR_HOST` | `127.0.0.1:8500` | `<第一台>:8500` | 同左 |
| `COMPOSE_FILE` | `compose.yaml` | 不变 | `compose.yaml:compose.multi-host.yaml` |
| `/srv/flame/data` | 本地目录 | **共享挂载，同一路径，uid 1000** | 同左 |

### 3.1 加一台执行器节点（tier 1 → 2）

在第一台上，别再把 Consul 留在回环地址：

```bash
# /etc/flame/flame.env：FLAME_BIND_IP=<本机内网 IP>、FLAME_CONSUL_ADDR_HOST=<本机内网 IP>:8500
flame up -d consul                                   # 重建容器让新监听生效；卷里的目录数据不丢
curl -s "http://<第一台>:8500/v1/status/leader"      # 先证明能连通，再动新节点
```

网关不用改：服务发现是从 Consul pull 的，新执行器在下一次 pull（≤ 5 秒）就加入。

在新机器上：

```bash
# 装 docker、同样的 /srv/flame/data 挂载、flame 用户（uid 1000），以及 /opt/flame/current/bin
# （只要二进制——容器、compose 文件、SQL 都留在第一台）
install -d -o flame -g flame /var/lib/flame/sandboxd/tmp
# 从第一台拷 /etc/flame/sandboxd.yaml，把 registry.address 改成 <第一台>:8500；
# registry.service_addr 留空——它由 listen 推导，注册 ID 跟着地址走
systemctl enable --now flame-sandboxd
```

防火墙：新节点 → 第一台 `8500`（注册）；第一台以及各网关节点 → 新节点 `27081`（健康检查与流量）。

确认目录是**变多**而不是把旧条目替换掉：

```bash
curl -s "http://<第一台>:8500/v1/health/service/sandboxd?passing=1"
```

回滚：在新节点上 `systemctl disable --now flame-sandboxd`。

### 3.2 拆分容器（tier 2 → 3）

前提：`/srv/flame/data` 已经在所有机器上是同一个目录。否则某个副本只服务一半文件，直到某次下载 404 才会被
发现。

tier 3 也不是重写——第一台把其它机器需要的东西对外开放，每台新机器只需要一份 env、一份配置和镜像（不需要
release 目录）：

```bash
# 第一台 —— /etc/flame/flame.env：COMPOSE_FILE=compose.yaml:compose.multi-host.yaml
flame up -d
```

这个 overlay 只加 `ports`，而且只加真正拆开的那些：PostgreSQL `5432`、Redis `6379`、LiteLLM `4000` 是默认
打开的，因为 nexus 通常不在数据库那台机器上；`nexus` `27006`、`sandboxg` `27080`、`browserg` `27090` 是注释
掉的——只为真正分开的那台机器取消对应注释，因为对外发布端口是防火墙决策，不是默认值。

新的 nexus 主机要把 `config/nexus.yaml` 里的 `db.host`、`redis.addr`、`litellm.host`、`sandbox.url`、
`browser.url` 指向第一台的内网地址。三个值必须在所有机器上一致：`JWT_SECRET`、`FLAME_VALUE_KEY`、
`LITELLM_SALT_KEY`。

已知边界：tier 3 有产物但**没有做过端到端验证**；经过验证的是 tier 1 与 tier 2。tier 3 也没有编排产物——
每台机器起哪几个服务目前仍是手工选择。

## 4. 升级与回滚

`FLAME_TAG` **就是**版本：它决定镜像 tag、release 目录和 `flame.env` 里的那一行。

```bash
# 升级：先放新 tag，再改那一行
sudo ./scripts/install.sh --tag v2026.11.26 --registry ghcr.io/<owner>/flame
sudo sed -i 's/^FLAME_TAG=.*/FLAME_TAG=v2026.11.26/' /etc/flame/flame.env
flame up -d && systemctl restart flame-sandboxd flame-browserd

# 回滚：把旧 tag 写回去，重复同样的两条命令
```

`install.sh` 从不覆盖 `/etc/flame/flame.env`、各服务配置和 `/srv/flame/data`——它们装的是主机策略、密钥和
租户数据。`--force-config` 只用新 release 的模板重渲染配置（别的都不动）。

## 5. 出问题了

| 现象 | 原因 |
|---|---|
| nexus 反复重启，日志说读不到配置 | `/etc/flame/nexus.yaml` 不存在——bind mount 在源文件缺失时会创建同名目录（见 2.2） |
| `pull access denied` / `unauthorized` | 拉的不是 `ghcr.io/<owner>/flame/*`，或网络挡住了 GHCR |
| 容器起来就退出 | 缺上游镜像（consul / postgres / redis / litellm）；`install.sh` 结尾会点名是哪个 |
| 写租户文件 `permission denied` | `/srv/flame/data` 不是 uid 1000，或两台机器指向了不同路径 |
| `health.sh` 报沙箱不可用 | `flame-sandboxd` 没启动，或网关到不了 `27081` |
| `exec format error` | 主机是 arm64 而镜像是 amd64；在目标架构上重跑 `install.sh --check` |

## 6. 本文不覆盖

- **模型配置。** seed 给租户 `t0` 一个 `DeepSeek` 供应商、key 为空；在控制台填入真实 key 并通过 LiteLLM 上线
  对应模型之前，什么都不会回答。
- **租户 schema 升级。** 平台 SQL 幂等且按固定顺序执行；租户 schema 完全不做迁移（`deployment.zh.md` §11）。
- **TLS 证书。** 放进 `/etc/flame/tls/`；只有当 `fullchain.pem` 与 `privkey.pem` 同时存在，nginx 才会挂上 TLS
  的 server 块。
- **备份。** `/srv/flame/data` 加 PostgreSQL 卷，再加 `/etc/flame/flame.env`（没有它，加密过的值读不回来）。
