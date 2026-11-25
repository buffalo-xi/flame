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
| 内核 | docker 模式：沙箱里要跑容器，daemon 必须允许嵌套 Docker；nsjail 模式：改用 cgroup v2（§4） |

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

`sandboxd` 会在这台机器上起 `flame/sandbox:<tag>` 容器，但它自己不会去拉镜像：要么让这台机器能访问 GHCR
（镜像名来自 `sandboxd.yaml`，即 `FLAME_IMAGE_NS` 加 `FLAME_TAG`），要么用
`./scripts/images.sh load --dir /srv/flame/dist` 从离线包导入。

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

## 4. 可选：nsjail 隔离模式

docker 模式把每个沙箱跑成宿主 daemon 上的一个容器。nsjail 换成「被关起来的进程」：不要 daemon、运行时不需要镜像，
也不存在嵌套 Docker 的问题——但它换了一种代价：一个 **setuid** 的二进制、cgroup v2，以及一份要从同一个沙箱镜像里
导出的 rootfs。模式写在某一台机器的 `/etc/flame/sandboxd.yaml` 里，所以同一个池子里可以混用；按机器选，不按部署选。

### 4.1 nsjail 节点需要什么

| | |
|---|---|
| `nsjail` | 从源码编译（Ubuntu 26.04 不打包它），并以 **setuid root** 安装——它要在 `sandboxd` 仍以 `flame` 身份运行时创建命名空间 |
| cgroup v2 | `stat -fc %T /sys/fs/cgroup` 输出 `cgroup2fs`；内存限制走 `--cgroup_mem_max` |
| rootfs | `/opt/flame/rootfs/<tag>`，从 `flame/sandbox:<tag>` 导出——一个镜像世代一份，约 600 MB |
| `codehost` | 在上面那份 rootfs 里的 `/usr/local/bin/codehost`，来自同一镜像，不要手工拷贝 |
| `/dev` | rootfs 里要有 `null`、`zero`、`random`、`urandom`、`tty`——`docker export` 一个都不带（`codehost` 每 fork 一个子进程都要打开 `/dev/null`） |

```bash
sudo apt install -y bison flex libprotobuf-dev protobuf-compiler libnl-3-dev libnl-route-3-dev libcap-dev pkg-config
git clone https://github.com/google/nsjail && cd nsjail && make
sudo install -m 4755 -o root -g root nsjail /usr/local/bin/nsjail   # 4755 = setuid root：没有这一位，非 root 调用方创建不了命名空间
stat -fc %T /sys/fs/cgroup                                          # cgroup2fs，否则这台机器不适合这个模式
```

`install.sh --check` 对缺 `nsjail` 只 **warn**（默认是 docker 模式），所以那一行要看清，别扫过去。

### 4.2 单机怎么做

tag 与镜像命名空间都在 `/etc/flame/flame.env` 里，rootfs 路径必须带 tag——一个镜像世代一份，否则 nsjail 跑的
解释器会和镜像里真正有的那些慢慢脱节：

```bash
FLAME_TAG=$(sed -n 's/^FLAME_TAG=//p' /etc/flame/flame.env)
NS=$(sed -n 's/^FLAME_IMAGE_NS=//p' /etc/flame/flame.env)
R=/opt/flame/rootfs/$FLAME_TAG

# 导出，而不是手搭：镜像就是契约（解释器、包、codehost）
sudo docker create --name flame-rootfs "$NS/sandbox:$FLAME_TAG" true
sudo install -d "$R"
sudo docker export flame-rootfs | sudo tar -x -C "$R"
sudo docker rm flame-rootfs

# docker export 不会带过来的设备节点
sudo install -d "$R/dev"
sudo mknod -m 666 "$R/dev/null"    c 1 3
sudo mknod -m 666 "$R/dev/zero"    c 1 5
sudo mknod -m 666 "$R/dev/random"  c 1 8
sudo mknod -m 666 "$R/dev/urandom" c 1 9
sudo mknod -m 666 "$R/dev/tty"     c 5 0

# 先自证，再让 sandboxd 用它：解释器路径错了只会在每次请求里表现为 "unavailable"
sudo nsjail -Mo --chroot "$R" -- /usr/bin/python3 -V
sudo nsjail -Mo --chroot "$R" -- /usr/local/bin/lua -v
```

然后改主机策略文件——安装器只渲染过一次，之后不再碰它：

```yaml
# /etc/flame/sandboxd.yaml
mode: nsjail

nsjail:
  binary: /usr/local/bin/nsjail     # 写绝对路径：unit 里的 PATH 是 systemd 的，不是你的
  rootfs: /opt/flame/rootfs/v2026.11.25
  uid: 99999
  gid: 99999
  disable_network: true
  read_only_rootfs: true            # 只读挂载；运行期什么都不修
  cgroup_version: auto
  # extra_args: ["-R", "/dev"]      # 只在无法 mknod 时用——见 4.3
```

```bash
sudo systemctl restart flame-sandboxd && ./scripts/health.sh
```

这里最容易错的三件事：

- **`--force-config` 会把 `mode: docker` 写回来。** 它会用 release 里的模板重渲染 `/etc/flame/sandboxd.yaml`，所以
  用过之后要把上面那段重新加回去。
- **这个模式下 `docker.image` 不再起作用。** 它的位置由 rootfs 顶替，所以节点运行时不需要沙箱镜像——但**导出那一刻**
  需要，这也正是路径里带 tag 的原因。
- **`network: on-request` 自己发不出网。** nsjail 断网靠 `clone_newnet` 加 `--iface_no_lo`；要放行 egress 必须在
  `extra_args` 里加 `--disable_clone_newnet`。`sandboxd` 只会记一条日志然后照常启动，所以你看到的现象是「egress
  请求拿不到网」，而不是启动失败。

### 4.3 无法 `mknod` 时

开发机上没有 root，或 Docker Desktop 的 overlay 拒绝该系统调用，rootfs 里就没有设备节点。这时在那一块里写
`extra_args: ["-R", "/dev"]`：它把宿主的 `/dev` 以只读方式绑进监狱，不需要 root 就能提供
`null` / `zero` / `random` / `urandom` / `tty`。生产机上能 `mknod` 就优先 `mknod`——绑定会把比它需要的更多的
设备交给监狱。

### 4.4 多机（tier 2 与 tier 3）

模式是每台机器的事，别处不记录：Consul 只带地址，网关既不知道也不关心某个节点用哪种隔离。所以池子里可以混用
docker 与 nsjail 节点——但那样 **镜像世代必须一致**，否则同一个请求落到不同节点会用不同的解释器作答。

每个 nsjail 执行器节点都要自己那份三件套：setuid 二进制、**同一个 tag** 导出的 rootfs、以及设备节点。release
包里既没有 `nsjail` 也没有 rootfs（§1），`install.sh` 只是把 `/opt/flame/rootfs` 建成空目录——这一步它留给你。

因此 §3.1 在新节点上多一步，位置在 `systemctl enable --now flame-sandboxd` 之前：

```bash
FLAME_TAG=$(sed -n 's/^FLAME_TAG=//p' /etc/flame/flame.env)   # 从第一台拷过来，或由 install.sh 写入
NS=$(sed -n 's/^FLAME_IMAGE_NS=//p' /etc/flame/flame.env)
R=/opt/flame/rootfs/$FLAME_TAG
sudo docker create --name flame-rootfs "$NS/sandbox:$FLAME_TAG" true   # 这个镜像在本节点上要先存在一次
sudo install -d "$R" && sudo docker export flame-rootfs | sudo tar -x -C "$R" && sudo docker rm flame-rootfs
sudo install -d "$R/dev" && sudo mknod -m 666 "$R/dev/null" c 1 3   # ……以及 zero / random / urandom / tty
sudo nsjail -Mo --chroot "$R" -- /usr/bin/python3 -V
```

其余都不变：拷过去的 `/etc/flame/sandboxd.yaml` 已经写着那个 tag，注册还是 `8500`，防火墙还是 `27081`。tier 3 对
这个模式没有任何改变——两个 tier 里 `sandboxd` 都裸跑，因为它就是持有特权的那一环；tier 3 拆开的是 nexus、
两个网关和数据面。

`/opt/flame/rootfs` 保持**机器本地**、一个世代一个目录。共享挂载并不被禁止——tag 目录本来就是同一个世代——但那样
每次执行都要从网络上读解释器；必须共享的只有 `/srv/flame/data`。

### 4.5 nsjail 节点怎么升级

`FLAME_TAG` 仍然是版本，但 rootfs **不跟着走**：`install.sh --tag …` 不碰 `/etc/flame/sandboxd.yaml`，于是节点会
继续跑上一世代的解释器，而且没有任何东西会报告这个差异。顺序是：先导出，再改指向。

```bash
# 1. 新世代，放进自己的目录（旧的留着，直到新的接过流量）
FLAME_TAG=v2026.11.26; NS=$(sed -n 's/^FLAME_IMAGE_NS=//p' /etc/flame/flame.env); R=/opt/flame/rootfs/$FLAME_TAG
docker pull "$NS/sandbox:$FLAME_TAG"          # 或 ./scripts/images.sh load --dir <bundle>
docker create --name flame-rootfs "$NS/sandbox:$FLAME_TAG" true
sudo install -d "$R" && docker export flame-rootfs | sudo tar -x -C "$R" && docker rm flame-rootfs
sudo install -d "$R/dev" && sudo mknod …      # 与 4.2 同样的五个节点
sudo nsjail -Mo --chroot "$R" -- /usr/bin/python3 -V

# 2. 改指向，然后走 §5 的正常升级流程
sudo sed -i "s#^  rootfs: .*#  rootfs: $R#" /etc/flame/sandboxd.yaml
sudo ./scripts/install.sh --tag v2026.11.26 --registry ghcr.io/<owner>/flame
sudo sed -i 's/^FLAME_TAG=.*/FLAME_TAG=v2026.11.26/' /etc/flame/flame.env
flame up -d && sudo systemctl restart flame-sandboxd
```

回滚就是把 `rootfs:` 指回上一个目录再重启——这也是旧目录要留到新世代接过流量之后的原因。删掉它，回滚就变成重建。

### 4.6 nsjail 节点出问题时

| 现象 | 原因 |
|---|---|
| 启动时报 `isolate: nsjail binary not found ("nsjail")` | 不在 unit 的 `PATH` 里；`nsjail.binary` 写绝对路径 |
| `isolate: nsjail requires rootfs` | `nsjail.rootfs` 为空——rootfs 目录本身从不做存在性检查，写错路径要到更晚才失败 |
| `codehost: start python3: open /dev/null: no such file or directory` | 缺设备节点——见 4.2，或用 `-R /dev`——见 4.3 |
| 语言被报成 unavailable，nsjail 退出码 `255` | rootfs 里的解释器路径不对，或 `/usr/local/bin/lua` 在而它的 `/usr/bin/lua5.4` 目标没了 |
| 节点注册成功，但每次执行都 `EPERM` / `ENOSYS` | cgroup v1，或内核没有 user namespace：先看 `stat -fc %T /sys/fs/cgroup` 与 `nsjail --version` |
| `network: on-request` 下 egress 请求拿不到网 | `extra_args` 里缺 `--disable_clone_newnet`（4.2） |

## 5. 升级与回滚

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

`mode: nsjail` 的升级多一步，而且没人替你把关：rootfs 不在 release 里，所以重启之前要先导出新世代并把
`rootfs:` 改过去（§4.5）。

## 6. 出问题了

| 现象 | 原因 |
|---|---|
| nexus 反复重启，日志说读不到配置 | `/etc/flame/nexus.yaml` 不存在——bind mount 在源文件缺失时会创建同名目录（见 2.2） |
| `pull access denied` / `unauthorized` | 拉的不是 `ghcr.io/<owner>/flame/*`，或网络挡住了 GHCR |
| 容器起来就退出 | 缺上游镜像（consul / postgres / redis / litellm）；`install.sh` 结尾会点名是哪个 |
| 写租户文件 `permission denied` | `/srv/flame/data` 不是 uid 1000，或两台机器指向了不同路径 |
| `health.sh` 报沙箱不可用 | `flame-sandboxd` 没启动，或网关到不了 `27081` |
| `exec format error` | 主机是 arm64 而镜像是 amd64；在目标架构上重跑 `install.sh --check` |
| nsjail 节点注册成功但什么都跑不了 | §4.6——通常是三件：二进制不在 `PATH`、rootfs 缺 `/dev` 节点、rootfs 是上一世代 |

## 7. 本文不覆盖

- **模型配置。** seed 给租户 `t0` 一个 `DeepSeek` 供应商、key 为空；在控制台填入真实 key 并通过 LiteLLM 上线
  对应模型之前，什么都不会回答。
- **租户 schema 升级。** 平台 SQL 幂等且按固定顺序执行；租户 schema 完全不做迁移（`deployment.zh.md` §11）。
- **TLS 证书。** 放进 `/etc/flame/tls/`；只有当 `fullchain.pem` 与 `privkey.pem` 同时存在，nginx 才会挂上 TLS
  的 server 块。
- **备份。** `/srv/flame/data` 加 PostgreSQL 卷，再加 `/etc/flame/flame.env`（没有它，加密过的值读不回来）。
