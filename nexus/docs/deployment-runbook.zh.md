# Flame 部署操作手册

中文 | [English](deployment-runbook.md)

> 状态：这是 [`deployment.md`](deployment.md) 的**操作篇**。那里解释"为什么"，这里只列步骤、文件与检查项。
> `deploy/prod/` 已经存在：下面的 compose 片段、unit 与脚本都摘自其中的真实文件，命令按它们的接口写。片段省略了
> 注释与不常用的段，以文件为准。
>
> 用词遵循 [`terminology.md`](terminology.md)：资产是 `list` / `delist`，端点是 `expose` / `retract`，
> 版本是 `release` / `deploy` / `rollback`。部署回退说的是"切回上一个 release"，不是版本域的 `rollback`。

---

## 1. 选档位

| 档位 | 形态 | 要开放的端口 | 适用 |
|---|---|---|---|
| 1 | 全部容器 + 本机裸跑执行器 | 对外 `27001`（+`27443`）；本机 `127.0.0.1:8500` | 开发、小规模 |
| 2 | 首台跑全部容器，其它机器只加 `sandboxd` / `browserd` | `8500` 供注册；`27081` / `27091` 指向执行器 | 生产默认 |
| 3 | 容器也分机（nexus / 网关 / 数据面） | §4 全部 | 高可用、大流量 |

第 2 档**不开放任何数据面端口**：执行器不访问 PostgreSQL / Redis。只有第 3 档才需要。

## 2. 主机前置

- Linux x86_64，内核 ≥ 5.10；`nsjail` 模式另需 cgroup v2 与独立 rootfs（按镜像代际构建，见 §5.3）
- Docker Engine ≥ 24，Compose v2
- 每个节点 NTP 同步（JWT 过期、日志排序、粘性 TTL 都依赖时钟）
- 每个节点挂载同一份共享目录到 `/srv/flame/data`（NFS / CephFS / 多云盘）
- 一个专用系统用户 `flame`，裸跑执行器以它运行
- 经验配置：4C/8G 可跑单机；生产从 8C/16G 起，每个并发浏览器实例预留 1–2 GB

## 3. 目录布局

```
/opt/flame/
├─ current -> releases/<ver>
├─ releases/<ver>/
│  ├─ bin/{sandboxd,codehost,browserd,browserhost}   # 只给两个原生单元用
│  ├─ compose.yaml
│  └─ compose.multi-host.yaml
└─ rootfs/<ver>/          # nsjail 的 rootfs，从 flame/sandbox:<ver> 导出（§5.3）

/etc/flame/
├─ flame.env             # 密钥、地址、开关（chmod 600）
├─ nexus.yaml
├─ sandboxd.yaml
├─ sandboxd.env          # 开了 ACL 时放 CONSUL_HTTP_TOKEN
├─ browserd.yaml
├─ sandboxg.yaml
├─ browserg.yaml
└─ tls/{fullchain,privkey}.pem   # 可选；存在即启用 27443

/srv/flame/data/{tenants,platform-assets,plugins}
/var/lib/flame/sandboxd/tmp/       # sandboxd 的 tmp_dir，机器本地、独占
/var/lib/flame/browserd/profiles/  # Chromium profile，机器本地
/var/log/flame/<service>/
```

容器来自镜像，原生进程来自 release 目录：`flame/nexus:<tag>`、`flame/gateway:<tag>`、`flame/web:<tag>` 只构建或
拉取一次，而 `releases/<ver>/bin` 里只剩两个跑 systemd 单元的执行器。两边共用同一个 `FLAME_TAG`——所以一次发布只有
一个版本号，不是两个。

`nexus` **没有 `-config` 参数**：它读工作目录下的 `./config.yaml`，所以容器设 `working_dir: /etc/flame`，并把
`/etc/flame/nexus.yaml` 挂成 `/etc/flame/config.yaml`。`server.static_root` 仍是 `/opt/flame/static`——它在镜像
里，不是 bind mount。其余四个服务都接受 `-config`，直接读 `/etc/flame/<name>.yaml`。

## 4. 端口与依赖

| 客户端 | 依赖 | 端口 | 第 1 档 | 第 2 档 | 第 3 档放行对象 |
|---|---|---|---|---|---|
| 用户 / 桌面端 | nginx | 27001（+27443） | 对外 | 对外 | 对外 |
| nginx | nexus | 27006 | `flame-net` | `flame-net` | nginx 节点 |
| nexus | sandboxg / browserg | 27080 / 27090 | `flame-net` | `flame-net` | nexus 节点 |
| nexus | PostgreSQL / Redis / LiteLLM | 5432 / 6379 / 4000 | `flame-net` | `flame-net` | 每台 nexus |
| LiteLLM | PostgreSQL | 5432 | `flame-net` | `flame-net` | LiteLLM 节点 |
| sandboxg / browserg | Redis / Consul | 6379 / 8500 | `flame-net` | `flame-net` | 每个网关节点 |
| sandboxd / browserd | Consul 注册 | 8500 | 回环 | 首台内网 | 每台执行器节点 |
| Consul | 执行器健康检查 | 27081 / 27091 | 回环 / 内网 | 内网 | Consul 节点 → 每台执行器 |
| 执行器 | PostgreSQL / Redis | — | 不需要 | 不需要 | 不需要 |

Consul 超过一个 server 时，server 之间还要 `8300`（server RPC）与 `8301`（LAN serf，TCP+UDP），并且每个 server
要有唯一的 `-node` 名；单 server 不需要发布这两个端口。

## 5. 配置

### 5.1 `/etc/flame/flame.env`（0600）

```bash
FLAME_TAG=v2026.12.01               # 唯一的版本号：镜像 tag 和 releases/<ver> 都用它
FLAME_BIND_IP=127.0.0.1            # 第 2/3 档改成这台的内网 IP
COMPOSE_FILE=compose.yaml          # 第 3 档：compose.yaml:compose.multi-host.yaml
FLAME_PUBLISH_IP=0.0.0.0           # 27001 绑到哪；前置 LB 时改成 127.0.0.1 或内网 IP
FLAME_TLS_IP=0.0.0.0               # 27443 绑到哪；没有证书时这行不产生监听

# 一律 hex：POSTGRES_PASSWORD 会被拼进 LiteLLM 的 DATABASE_URL，base64 的 + / = 会让它失效
POSTGRES_PASSWORD=<openssl rand -hex 24>
REDIS_PASSWORD=<openssl rand -hex 24>
JWT_SECRET=<openssl rand -hex 24>
FLAME_VALUE_KEY=<openssl rand -hex 24>
LITELLM_MASTER_KEY=sk-<openssl rand -hex 24>
LITELLM_SALT_KEY=<openssl rand -hex 24>     # 永不轮换：它解 LiteLLM 库里的供应商 key
SANDBOX_TOKEN=<openssl rand -hex 24>
BROWSER_TOKEN=<openssl rand -hex 24>

# 种子凭据：与 PoC 同一套（口令 12345678、固定 API key），首次登录后立刻改口令、轮换该 key，见 §12
FLAME_ADMIN_PASSWORD=12345678
FLAME_API_KEY=sk-poc-t0-primary
FLAME_TENANT_SCHEMA=t0
FLAME_TENANT_TITLE=Default tenant

FLAME_DB_HOST=postgres
FLAME_REDIS_ADDR=redis:6379
FLAME_LITELLM_HOST=litellm
# 完整 URL：nexus 直接拨它们。留空 = 没有该类执行器（nexus 连沙箱就绪闸门一起跳过），只适合试跑
FLAME_SANDBOX_URL=ws://sandboxg:27080
FLAME_BROWSER_URL=ws://browserg:27090

# consul 的两个视角：宿主上的执行器、容器里的网关（第 3 档网关分机时改成首台内网 IP）
FLAME_CONSUL_ADDR_HOST=127.0.0.1:8500
FLAME_CONSUL_ADDR_NET=consul:8500
```

### 5.2 `/etc/flame/nexus.yaml`（全量 `${VAR}` 展开，挂成 `config.yaml`）

```yaml
server:
  host: 0.0.0.0
  port: 27006
  static_root: /opt/flame/static
database:
  type: postgres
  host: "${FLAME_DB_HOST}"
  port: 5432
  username: flame
  password: "${POSTGRES_PASSWORD}"
  database: flame
  platform_schema: platform
redis:
  mode: single
  addrs: ["${FLAME_REDIS_ADDR}"]
  password: "${REDIS_PASSWORD}"
  db: 0
jwt:
  secret_key: "${JWT_SECRET}"
  token_expiry: 600
  issuer: "nexus@flame"
security:
  value_key: "${FLAME_VALUE_KEY}"
registry:
  enabled: false
litellm:
  enabled: true
  url: "http://${FLAME_LITELLM_HOST}:4000"
  master_key: "${LITELLM_MASTER_KEY}"
  database: litellm
sandbox:
  url: "ws://${FLAME_SANDBOXG_HOST}:27080"
  token: "${SANDBOX_TOKEN}"
browser:
  url: "ws://${FLAME_BROWSERG_HOST}:27090"
  token: "${BROWSER_TOKEN}"
storage:
  root_dir: /srv/flame/data
  driver: local
download:
  x_accel_prefix: /__files
```

未设置的 `${VAR}` 会展开成空串并让服务拒绝启动——这是有意的。上面每个变量都必须在 `flame.env` 里，而
`flame.env` 也是通过 `env_file` 进容器的：展开发生在 nexus 自己的进程里，不是在 compose 里。

它在容器里挂成 `/var/lib/flame/nexus/config.yaml`，工作目录同为 `/var/lib/flame/nexus`（nexus 读 `./config.yaml`，
并在工作目录下建 `logs/`，那份 `logs` 从宿主挂进来）。

### 5.3 `sandboxd` / `browserd`（渲染，不支持 `${VAR}`）

```yaml
# /etc/flame/sandboxd.yaml
listen: "0.0.0.0:27081"
token: "${SANDBOX_TOKEN}"
mode: docker
allowed_mount_roots:
  - "/srv/flame/data"
tmp_dir: "/var/lib/flame/sandboxd/tmp"
docker:
  image: flame/sandbox:<ver>
  network: bridge
  read_only: true
registry:
  enabled: true
  type: consul
  address: "<首台内网 IP>:8500"
  service_name: sandboxd
  health_check_path: /healthz
  # service_addr 可不写：留空即从 listen 推导。
  # 注册中心在远端时不要写 127.0.0.1：启动会直接拒绝。
```

```yaml
# /etc/flame/browserd.yaml
listen: "0.0.0.0:27091"
token: "${BROWSER_TOKEN}"
workspace_root: "/srv/flame/data"
browser:
  # 要嵌在 browser 下：扁平的 `profile_root` 不是已知键，会被直接忽略，
  # 于是用默认值（`run/browserd/profiles`，相对当前工作目录）去填一块没人看的盘。
  # 每个会话独立，且故意不放租户目录——里面只有该会话的 cookie。
  profile_root: "/var/lib/flame/browserd/profiles"
registry:
  enabled: true
  type: consul
  address: "<首台内网 IP>:8500"
  service_name: browserd
```

`install.sh` 会把 `deploy/prod/config/<name>.yaml` 渲染一次到 `/etc/flame/<name>.yaml`（`0640 root:flame`），
**只在文件不存在时**；`--force-config` 才会覆盖。升级因此不会冲掉宿主策略与口令。

渲染用的是按已知变量表的 `sed`，不是 `envsubst`：`envsubst` 会把文件里任何它能解析的 `$NAME` 都替换掉，nginx
风格的 `$host` / `$http_upgrade` 一旦出现在同一份模板里就会被吃掉。渲染后若还剩 `${...}`，`install.sh` 会告警。

这四个服务是照字面读 yaml 的——留在文件里的 `${VAR}` 运行时不会展开，所以没渲染的模板会**把占位符本身当成
token** 启动，之后每次调用都被拒。

#### nsjail 的 rootfs 是造出来的，一份对应一代镜像

只有 `mode: nsjail` 需要它——docker 模式直接用镜像。镜像本身就是契约（解释器、库、`codehost`），所以从镜像
export，不要手搓 rootfs。`scripts/rootfs.sh` 就是这个 export，第一台与每台执行器节点用的都是同一条命令：

```bash
./scripts/rootfs.sh --tag <ver>          # docker export -> /opt/flame/rootfs/<ver>，建设备节点，再自证
./scripts/rootfs.sh --tag <ver> --check  # 校验已有的一份；不改任何东西
```

它做的事，顺序是有理由的：把 `flame/sandbox:<ver>` 用 `docker create` / `docker export` 落到按 tag 分目录的位置
（回退要用旧的，所以不原地覆盖）；补上 `docker export` 不带的五个设备节点——`codehost` 给每个子进程都要开
`/dev/null`，少了它们 jail 能起来但第一次 exec 就失败，报
`codehost: start python3: open /dev/null: no such file or directory`；最后用
`nsjail -Mo --chroot "$R" -- /usr/bin/python3 -V` 自证，因为解释器路径坏了只会在运行期表现为"不可用"。
无法 `mknod` 时（开发机没有 root、Docker Desktop 的 overlay）它会明确报错，并留下文档里的退路：
`--no-dev-nodes` 加 `extra_args: ["-R", "/dev"]`，把宿主 `/dev` 只读绑进监狱。

```yaml
# /etc/flame/sandboxd.yaml —— 模板里这块已经填好，切换只改 mode 这一行
mode: nsjail
nsjail:
  binary: /usr/local/bin/nsjail   # 源码编译
  rootfs: /opt/flame/rootfs/<ver>
  uid: 99999
  gid: 99999
  disable_network: true
  read_only_rootfs: true          # 整棵树只读挂载，运行期不会有人去"修"它
  cgroup_version: auto
```

- **一份 rootfs 对应一代镜像。** 只换 `docker.image` 不重建 rootfs，nsjail 就还在跑旧解释器，而 docker 模式跑的是
  新镜像——而且没有任何地方会报这个差异。
- **`/usr/local/bin/lua` 是指向 `/usr/bin/lua5.4` 的绝对路径符号链接。** 两边都要留：只留目标或只留链接，该语言会
  在启动时被上报为不可用，nsjail 只给一个 `exit 255`，看不出原因。
- **Ubuntu 26.04 没有 nsjail 包。** 源码编译（`bison flex libprotobuf-dev protobuf-compiler libnl-3-dev
  libnl-route-3-dev libcap-dev pkg-config`），装到 `/usr/local/bin/nsjail`。
- 每份 rootfs 预留约 600 MB，并且上一代要留到新一代确认接流量之后再删。

注册相关的三条规则，值得重复：

- 注册端口恒随 `listen`；`service_port` 已移除（旧配置会打一条警告并忽略）；
- 服务 ID 默认 `<名称>-<地址>-<端口>`，每台天然唯一，不需要配置；
- `service_addr` 可以留空：`listen` 写具体地址就直接用；通配地址则依次取 `advertise_iface`、主机名、首个物理网卡。
  推导失败、或推成回环而注册中心在远端，启动直接失败。

### 5.4 `sandboxg` / `browserg`

```yaml
# /etc/flame/sandboxg.yaml
listen: ":27080"
sandbox_token: "${SANDBOX_TOKEN}"
redis: { addr: "redis:6379", password: "${REDIS_PASSWORD}", db: 0 }
sticky_ttl_sec: 2100
discovery:
  type: consul
  address: "consul:8500"          # 第 3 档改成首台内网 IP
  service_name: sandboxd
  interval_sec: 5
backends: []                      # 千万不要在这里留 127.0.0.1 种子
```

`browserg` 同构：端口 `27090`、`browser_token`、`max_per_tenant: 1`、`service_name: browserd`。

## 6. compose 布局

`compose.yaml` 是服务定义的唯一来源；`compose.multi-host.yaml` 只写 `ports`，只在第 3 档加载
（Compose 的 `ports` 是**合并**而非替换，所以每个端口只能有一处归属）。

```yaml
services:
  consul:
    image: hashicorp/consul:1.20
    command: ["agent", "-server", "-bootstrap-expect=1", "-client=0.0.0.0", "-data-dir=/consul/data"]
    volumes: ["flame-consul:/consul/data"]
    ports: ["${FLAME_BIND_IP:-127.0.0.1}:8500:8500"]
    extra_hosts: ["host.docker.internal:host-gateway"]   # 健康检查是它发起的，它也要能解析 service_addr

  postgres:
    image: postgres:18
    environment:
      POSTGRES_USER: flame                 # initdb 直接建出这个角色，不需要"建角色 + 移交所有权"
      POSTGRES_PASSWORD: "${POSTGRES_PASSWORD}"
      POSTGRES_DB: flame
    shm_size: 256mb                        # 默认 64 MB 的 /dev/shm 会让并行查询失败
    volumes:
      # 挂父目录：18 的镜像把 PGDATA 挪到了 /var/lib/postgresql/18/docker，
      # 挂在旧的 /var/lib/postgresql/data 上会静默地不再持久化
      - flame-pg:/var/lib/postgresql
      - /srv/flame/pg-init:/docker-entrypoint-initdb.d:ro

  redis:
    image: redis:7-alpine
    command: ["redis-server", "/etc/flame/redis.conf"]   # 口令在文件里，不在命令行
    volumes:
      - flame-redis:/data
      - /etc/flame/redis.conf:/etc/flame/redis.conf:ro

  litellm:
    image: ghcr.io/berriai/litellm-database:main-latest
    environment:
      DATABASE_URL: "postgresql://flame:${POSTGRES_PASSWORD}@postgres:5432/litellm?sslmode=disable"
      LITELLM_MASTER_KEY: "${LITELLM_MASTER_KEY}"
      LITELLM_SALT_KEY: "${LITELLM_SALT_KEY}"          # 解库里的供应商 key，永不轮换

  nexus:
    image: flame/nexus:${FLAME_TAG}
    user: "1000:1000"
    working_dir: /var/lib/flame/nexus      # nexus 从这里读 ./config.yaml，并在同目录建 logs/
    env_file: ["/etc/flame/flame.env"]     # 配置里的 ${VAR} 由 nexus 自己展开
    environment: { XLOG_CONSOLE: "1" }     # 否则日志只进文件，docker logs 是空的
    volumes:
      - /etc/flame/nexus.yaml:/var/lib/flame/nexus/config.yaml:ro
      - /var/log/flame/nexus:/var/lib/flame/nexus/logs
      - /srv/flame/data:/srv/flame/data

  sandboxg:
    image: flame/gateway:${FLAME_TAG}
    user: "1000:1000"
    command: ["/opt/flame/bin/sandboxg", "-config", "/etc/flame/config.yaml"]
    environment: { XLOG_CONSOLE: "1" }
    volumes:
      - /etc/flame/sandboxg.yaml:/etc/flame/config.yaml:ro
      - /var/log/flame/sandboxg:/var/log/flame/sandboxg
    extra_hosts: ["host.docker.internal:host-gateway"]

  browserg:
    image: flame/gateway:${FLAME_TAG}
    user: "1000:1000"
    command: ["/opt/flame/bin/browserg", "-config", "/etc/flame/config.yaml"]
    environment: { XLOG_CONSOLE: "1" }
    volumes:
      - /etc/flame/browserg.yaml:/etc/flame/config.yaml:ro
      - /var/log/flame/browserg:/var/log/flame/browserg
    extra_hosts: ["host.docker.internal:host-gateway"]

  web:
    image: flame/web:${FLAME_TAG}
    # 容器内端口与宿主一致（nginx 就在 27001 / 27443 上监听），暴露范围由变量决定
    ports:
      - "${FLAME_PUBLISH_IP:-0.0.0.0}:27001:27001"
      - "${FLAME_TLS_IP:-0.0.0.0}:27443:27443"   # 没有证书时 nginx 不监听它，发布出去也无害
    volumes:
      - /srv/flame/data:/srv/flame/data:ro   # 下载走 X-Accel-Redirect，nginx 自己发字节
      - /etc/flame/tls:/etc/nginx/tls:ro     # 可选；入口脚本按它是否存在接入 TLS 配置块
```

同一份文件里还有两个一次性任务，`up -d` 不会启动它们——它们挂在 `jobs` profile 后面，只能用
`flame --profile jobs run --rm <name>` 调用：`migrate`——用 `postgres` 镜像跑 `releases/<ver>/sql` 里的 psql，
顺序由 `scripts/migrate-apply.sh` 里的清单决定；以及 `bootstrap`——`flame/nexus` 镜像加上覆盖的 entrypoint 跑种子
脚本。两者都必须带 nexus 的 `user: "1000:1000"` 和 `/srv/flame/data` 挂载，否则它们写出的文件属主既不是 nexus
也不是沙箱能读的 uid。

**是镜像，不是 release 目录**：应用面已经没有任何东西从 `/opt/flame` bind mount 进来了。改 `FLAME_TAG` 就改了每个
容器引用的镜像，这正是 Compose 判定重建的依据——不需要 `--force-recreate`，`releases/` 只对两个 systemd 单元有意义。

`/srv/flame/pg-init/10-litellm-db.sql` 只在空数据卷上跑一次。角色与 `flame` 库由 `POSTGRES_USER` / `POSTGRES_DB`
建好（口令就是 `POSTGRES_PASSWORD`），所以这个文件只负责第二个库；写成幂等形式，手工重复执行也不会报错：

```sql
SELECT 'CREATE DATABASE litellm OWNER flame'
WHERE NOT EXISTS (SELECT 1 FROM pg_database WHERE datname = 'litellm')\gexec
```

`compose.multi-host.yaml`，仅第 3 档：

```yaml
services:
  postgres: { ports: ["${FLAME_BIND_IP:?FLAME_BIND_IP must be the intranet IP}:5432:5432"] }
  redis:    { ports: ["${FLAME_BIND_IP:?FLAME_BIND_IP must be the intranet IP}:6379:6379"] }
  litellm:  { ports: ["${FLAME_BIND_IP:?FLAME_BIND_IP must be the intranet IP}:4000:4000"] }
  # 仅当 nginx / nexus 不在同一台时
  # nexus:  { ports: ["${FLAME_BIND_IP:?set FLAME_BIND_IP}:27006:27006"] }
  # 仅当 nexus 与两个网关不在同一台时
  # sandboxg: { ports: ["${FLAME_BIND_IP:?set FLAME_BIND_IP}:27080:27080"] }
  # browserg: { ports: ["${FLAME_BIND_IP:?set FLAME_BIND_IP}:27090:27090"] }
```

包装脚本，让第 1 档与第 3 档命令一致（`/usr/local/bin/flame`）：

```bash
#!/usr/bin/env bash
set -euo pipefail
set -a; . /etc/flame/flame.env; set +a
cd /opt/flame/current
IFS=: read -r -a files <<< "${COMPOSE_FILE:-compose.yaml}"
args=(); for f in "${files[@]}"; do args+=(-f "$f"); done
exec docker compose "${args[@]}" "$@"
```

nginx（在 `web` 容器内）：

```nginx
location = /health { proxy_pass http://nexus:27006/health; }
location /__files/ { internal; alias /srv/flame/data/; }
location /        { proxy_pass http://nexus:27006; }
```

## 7. 首次启动

### 7.0 想先在自己机器上看到它跑起来（不需要 root）

```bash
./scripts/images.sh build --tag dev --no-sandbox
./scripts/dev-up.sh            # 数据面 → SQL → 种子 → 本机 sandboxd → 应用面
```

它用 `compose.dev.yaml`：配置文件渲染到 `.devrun/`、数据放命名卷，并且**不装 systemd、不写 `/etc/flame`**。
结束时会打印控制台地址与两组账号（平台 `plat`、租户 `primary`，口令都是 `12345678`）。种子会给租户一条 `DeepSeek`
供应商（key 留空）：进控制台把 key 填上，再把它配套的 `deepseek-flash` 模型配置到网关。`dev-up.sh --smoke` 会额外跑
一次端到端调用（需要 `FLAME_LLM_BASE_URL` / `FLAME_LLM_API_KEY` / `FLAME_LLM_MODEL`），
`dev-up.sh down|purge|status|logs` 是配套命令。

试跑的边界：种子里的供应商没有 key（填上之前没有任何模型能应答）、没有 `browserd`；`sandboxd` 跑在本机进程里，因此
shell 工具能执行但看不到租户文件。正式部署走下面的 7.1。

### 7.1 生产主机

```bash
# 0) 在构建机（有 go、node、docker）：产物、镜像、两个 tar 与 SHA256SUMS 一次落进同一目录。
#    把两个包拷过来，就在它们所在处解开目录树（这里是 /srv/flame/dist）——install.sh 从同一个目录读两者，
#    主机上不需要再整理路径。
./scripts/release.sh --tag "$FLAME_TAG"

# 1) 前置检查：要么通过，要么点名缺什么（它不改任何东西）
sudo ./scripts/install.sh --tag "$FLAME_TAG" --check

# 2) 落位：release、unit、包装脚本、渲染好的四份配置、nexus.yaml 与 redis.conf；首次运行会生成 /etc/flame/flame.env
#    --images 指向镜像 tar 所在目录，它会先 docker load
sudo ./scripts/install.sh --tag "$FLAME_TAG" --images /srv/flame/dist
#    ……或者镜像已发布到 registry（`./scripts/images.sh push --tag "$FLAME_TAG" --registry <ns>`）：
#    它会拉四个运行镜像，并把 <ns> 记为 flame.env 里的 FLAME_IMAGE_NS
# sudo ./scripts/install.sh --tag "$FLAME_TAG" --registry ghcr.io/acme/flame

# 3) 先起数据面
flame up -d consul postgres redis litellm

# 4) 平台 SQL，然后 t0 种子——两个都是一次性 compose 服务，都可重复执行
./scripts/migrate.sh --check                # 文件集合与执行清单必须相等
./scripts/migrate.sh
flame --profile jobs run --rm bootstrap     # 写出 /etc/flame/initial-credentials.txt，0600，属主 flame

# 5) 原生执行器，然后应用面（install.sh 已经 enable，这里只 start）
systemctl start flame-sandboxd flame-browserd
flame up -d
flame ps

# 6) 验收：§13 的检查项，失败即点名
./scripts/health.sh
```

执行器是宿主上的 systemd unit（`flame-sandboxd.service`）：

```ini
[Unit]
Description=flame sandboxd
After=network-online.target docker.service
Requires=docker.service

[Service]
User=flame
EnvironmentFile=-/etc/flame/flame.env
EnvironmentFile=-/etc/flame/sandboxd.env       # 开了 ACL 时放 CONSUL_HTTP_TOKEN
ExecStart=/opt/flame/current/bin/sandboxd -config /etc/flame/sandboxd.yaml
Restart=always
RestartSec=3
LimitNOFILE=65535

[Install]
WantedBy=multi-user.target
```

`flame-browserd.service` 同构，换成 browserd 的二进制与配置。

## 8. 演进路径：单机 → 多机

第 1 档是起点；下面每一步都是**增量**的——什么都不重装。真正要搬数据的只有共享存储，而它正是 8.2 / 8.3 的前提。

| 你想要 | 去哪 | 代价 |
|---|---|---|
| 更多沙箱或浏览器容量 | 8.1 | 一台机器、两个端口；网关不用改 |
| 更高的应用吞吐，或 nexus 成了单点 | 8.2（+ 存储） | 内网放开五个端口、共享存储、JWT / value_key 一致 |
| 网关成了单点 | 8.3（+ 存储） | 内网放开 `6379` / `8500`，nexus 前加负载均衡 |

每一步都是同一个三段式：**改首台 → 准备新机 → 验证**。只要动了 `ports`，对应容器就一定会重建（`restart`
不够）；定义没变的服务保持不动。

### 8.1 增加执行器节点（第 1 档 → 第 2 档）

首台——把 Consul 从回环放到内网：

```bash
# 1) flame.env：FLAME_BIND_IP=<本机内网 IP>
# 2) 重建 consul，让新的端口绑定生效（卷里的 catalog 保留）
flame up -d consul
# 3) 先去内网验证可达，再动新节点
curl -s "http://<首台内网 IP>:8500/v1/status/leader"
```

两个网关不用改：它们的 discovery 从 Consul 拉取，新实例在下一次拉取（≤ 5s）自动入池。

新机器：

```bash
# docker、共享挂载、/opt/flame/current（只要 bin）、flame 用户
install -d -o flame -g flame /var/lib/flame/sandboxd/tmp
# 把首台的 /etc/flame/sandboxd.yaml 拷过来，再把 registry.address 改成首台内网 IP
# service_addr 留空：它从 listen 推导，ID 跟着地址走
systemctl enable --now flame-sandboxd
```

防火墙：

- 新节点 → 首台 `8500`（注册）
- 首台 → 新节点 `27081`（健康检查），各网关节点 → 新节点 `27081`（业务）

确认是"多了一条"而不是"顶掉旧的"：

```bash
curl -s 'http://<首台>:8500/v1/health/service/sandboxd?passing=1'
```

回退：在新节点上 `systemctl disable --now flame-sandboxd`；条目会被摘除，或 30s 后自然过期。

### 8.2 增加一个 nexus 副本（第 3 档）

**前提**：`/srv/flame/data` 在两台上已经是同一份目录（§9 第 5 步）。否则副本只服务一半文件，而且要等到某次下载
404 才有人发现。

首台——副本**不在** `flame-net` 里，所以它要用的东西都得发布出来：

| 在首台发布 | 端口 | 用途 |
|---|---|---|
| PostgreSQL | 5432 | 平台库 + `litellm` 库 |
| Redis | 6379 | 硬依赖，共用同一实例 |
| LiteLLM | 4000 | 模型出口 |
| sandboxg / browserg | 27080 / 27090 | `sandbox.url` / `browser.url` |

```bash
# flame.env：COMPOSE_FILE=compose.yaml:compose.multi-host.yaml
# overlay：上面五个端口（+ nexus:27006，如果 nginx 还不在前两台）
flame up -d
```

新机器——没有 release 目录，只有一份 env、一份配置和镜像：

```bash
# 1) flame 包装脚本、/opt/flame/current（只有 compose 文件）、/etc/flame/flame.env：
#    FLAME_BIND_IP 写本机内网 IP，COMPOSE_FILE 与 FLAME_TAG 与密钥照抄首台
# 2) overlay：只发布本机自己的 27006——nginx 拨的就是它
#    nexus: { ports: ["${FLAME_BIND_IP:?set FLAME_BIND_IP}:27006:27006"] }
# 3) 只起这一个服务，不要起整套：裸 `flame up -d` 会在这里再建一套
#    PostgreSQL / Redis / LiteLLM / Consul，而它们属于首台
flame up -d nexus
```

配置是首台 `nexus.yaml` 的副本，只改地址：

```yaml
server:  { host: 0.0.0.0, port: 27006, static_root: /opt/flame/static }
database: { host: "<首台内网 IP>", ... }
redis:   { addrs: ["<首台内网 IP>:6379"], ... }
litellm: { url: "http://<首台内网 IP>:4000", ... }
sandbox: { url: "ws://<首台内网 IP>:27080", ... }
browser: { url: "ws://<首台内网 IP>:27090", ... }
```

其余全部**照抄**——数据库与 Redis 口令、`litellm.master_key`、`sandbox.token` / `browser.token`（网关用它们认证
nexus，换成新生成的 token 之后每次沙箱调用都是 401，而不是启动报错）。下面这三个是"看着像本地配置、其实必须一致"的：

| 值 | 不一致的后果 |
|---|---|
| `jwt.secret_key` | 两个副本互不认 token，用户被随机登出 |
| `security.value_key` | 已加密的 `global_vars` 解不开；留空则从 `jwt.secret_key` 派生 |
| `storage.root_dir` + 挂载 | 文件时有时无，下载 404 |

**不要**在这台上跑 `migrate` 或 `bootstrap`：两者都属于首台的一次性任务（§7），两台同时执行 DDL 正是拆分要避免的
锁等待。PostgreSQL / Redis / LiteLLM 也不属于这台机器。

副本若要开 `registry`，每台的 `registry.service_addr` 必须是**自己的裸 IP**。服务 ID 由该地址派生
（`<名称>-<地址>-<端口>`），默认值是 `http://localhost`，健康检查地址又是 `http://` 直接拼上它——所以两台共用一份
yaml 会派生出同一个 ID、在 Consul 里互相顶掉，探活还会变成畸形 URL。注册不开也没关系：nginx 直接指副本。

nginx（或外部 LB）的 upstream 必须同时列出两个地址；在那之前副本收不到任何流量。§6 里的 `proxy_pass
http://nexus:27006` 指的是 Compose 服务名，永远到不了第二台——要换成显式 upstream：

```nginx
upstream nexus_app {
    server <首台内网 IP>:27006 max_fails=2 fail_timeout=10s;
    server <副本内网 IP>:27006  max_fails=2 fail_timeout=10s;
}
location / { proxy_pass http://nexus_app; }
```

等 `curl -s http://<副本内网 IP>:27006/health` 返回 200 之后再加这一条。nginx 自己不会去探 `/health`，而就绪检查
把"沙箱能力快照还没取到"也算未就绪——所以 `sandbox.url` 或 token 配错的副本在那边就是 503，先加进去等于带走一半流量。

防火墙：nginx 节点（或 LB）→ 副本的 `27006`；这是副本唯一需要的入向规则，因为首台这边不会有任何东西主动拨它。

容量：每个 nexus 固定开 25 条数据库连接，PostgreSQL 默认总共 100 条且 LiteLLM 共用同一实例——要上第三、第四个副本，
得先把 `max_connections` 提上去，而不是等 503 出现之后再补。

回退：把副本从 upstream 去掉并停掉它——首台照常跑。要重新关掉端口，就是改 overlay 再 `flame up -d`（重建五个
容器）；副本自己那个 `27006` 则在它那台上改 overlay 后 `flame up -d nexus` 撤掉。

### 8.3 增加一个网关（第 3 档）

首台：发布 `6379`（远端网关的粘性绑定存在 Redis 里）与 `8500`（它读 Consul）。新网关自己的 `27080` / `27090`
必须能被 nexus 连到。

新机器：

```yaml
listen: ":27080"
redis:  { addr: "<首台内网 IP>:6379", password: "...", db: 0 }
discovery: { type: consul, address: "<首台内网 IP>:8500", service_name: sandboxd, interval_sec: 5 }
backends: []
```

与 §8.2 的副本同一份 release 与 `flame.env`，然后 `flame up -d sandboxg`——裸 `flame up -d` 会把这台也起成整套。
在它的 overlay 里发布本机的 `27080`，并放开 nexus 节点 → 这台；这里的 token 是 nexus 发出去的那个
（`sandbox.token`），不是新生成的。

然后把 nexus 指过去（`sandbox.url` / `browser.url`）。网关多于一个时，nexus 每个通道仍只有一个 URL——配置里是
一个字符串而不是列表——前面加 LB，或每个通道只留一个网关。

回退：把 nexus 指回去并重启它（URL 只在启动时读），再停掉新网关。

### 8.4 每一步共同的部分

- **只有定义变化的服务会重建。** `flame up -d` 按服务比较配置哈希；放开五个端口就恰好重建那五个容器。
  `restart` 永远不会应用新的 `ports` 绑定。
- **卷与网络保留。** `flame-pg` / `flame-redis` / `flame-consul` 与 `flame-net` 不受影响；容器 IP 可能变化，
  服务名不变。
- **预期一段 503。** PostgreSQL / Redis 重建期间会这样——放在低峰做，并把防火墙规则安排在同一窗口。
- **有两个任务每个副本各跑一遍。** 启动清理（`runs/` 超过 24h、孤儿快照超过 1h）与插件目录刷新都没有分布式锁；
  它们靠这些时间阈值才安全，所以不要调小——否则一台会删掉另一台还在用的东西。
- **`storage.driver: s3` 不能顶替共享挂载。** 它只覆盖走 storage 接口的那些消费者；自定义工具目录与文件守卫仍直接
  读 `storage.root_dir`，执行器还挂着宿主机路径（`allowed_mount_roots`、`workspace_root`）。
- **只起本机该起的服务。** `flame up -d` 会把整个 compose 应用一遍；只加一个副本或一个网关的机器要指名服务
  （`flame up -d nexus`）——裸调一次就等于多建一套 PostgreSQL / Redis / Consul。
- **每台新机器都要有共享挂载和自己的二进制/配置副本**；运行时不会去别处拉取。
- **回退代价随步骤上升**：执行器 ≈ 零，副本 = 改 upstream，网关 = 重启一次 nexus。

## 9. 容器分机（第 3 档）

这仍然不是重装：容器、数据、账号都留着。真正必须搬的只有**存储**——第 1 档通常把 `/srv/flame/data` 放在本地盘，
而每台跑 nexus、browserd 或 nginx 的机器都要看到同一份目录（下载是 nginx 自己经 `X-Accel-Redirect` 发的）。为这次
拷贝预留一个停机窗口，其余都是改配置 + 重启。

1. `flame.env`：`FLAME_BIND_IP=<本机内网 IP>`、`COMPOSE_FILE=compose.yaml:compose.multi-host.yaml`。
2. `compose.multi-host.yaml`：只保留真正跨机的端口。
3. `flame.env`：`FLAME_DB_HOST` / `FLAME_REDIS_ADDR` / `FLAME_LITELLM_HOST` 指向首台。
4. 按 §4 放开端口；两个网关的 `discovery.address` 改成首台。
5. 共享目录必须是同一份——本地同名目录会表现为"文件时有时无"。

应用这次变更：`flame up -d` 只会重建**定义发生变化的服务**（Compose 按服务比较配置哈希），所以放开
5432/6379/4000/27080/27090 时重建的恰好是这五个，其余容器不动；端口一变就是换容器，`restart` 顶不上。卷、网络与
503 的表现见 §8.4。

再加一个 nexus 是另一件更重的事，有自己的清单：§8.2。

## 10. 升级与回退

```bash
# 升级：先构建，然后只动一个版本号——容器跟镜像 tag，原生进程跟软链
./scripts/build.sh
./scripts/images.sh build --tag "$FLAME_TAG"
sudo ./scripts/install.sh --tag "$FLAME_TAG"        # 放置 releases/<ver>、重指 current、重载单元
./scripts/migrate.sh                                # 先跑 SQL，再让新代码上线
flame up -d sandboxg browserg nexus web             # 镜像引用变了，Compose 会逐个重建
systemctl restart flame-sandboxd flame-browserd     # 通过重指后的软链重新 exec

# 回退到上一个 release（不是版本域的 rollback）
sudo ./scripts/install.sh --tag "<old>"
flame up -d sandboxg browserg nexus web
systemctl restart flame-sandboxd flame-browserd
```

容器侧不需要 `--force-recreate`：服务定义自己变了（`image: flame/nexus:<new>`），这正是 Compose 逐个服务比较的配置
哈希；而且应用面已经没有任何东西从 `/opt/flame` bind mount 进来。两个原生进程相反——单元里的路径没变，只有
`install.sh` 重指 `current` 加一次 `restart` 才会跑新二进制。

`bootstrap` 不属于日常升级：它的作用是在空库上建 t0，虽然幂等，但每次发布都跑只会重新打印一堆没人要的凭据。

`mode: nsjail` 多一步，而且没人替你把关：rootfs 是绑在镜像代际上的（§5.3），所以先
`./scripts/rootfs.sh --tag <新 tag>`，再把配置里的 `rootfs:` 指过去，最后 `systemctl restart flame-sandboxd`。
省掉这一步，jail 还在跑上一代的解释器——而如果同时混着 docker 模式，那边已经是新镜像了。旧 rootfs 留到新一代确认
接流量再删，删早了回退就得重新导出。

约束：SQL 只做增量（新表、新列）。破坏性变更会让旧代码跑在新 schema 上，回退就不再安全。每次迁移前先 dump。

有副本时（§8.2），`migrate` 只在首台跑一次，但每台都要各自执行 `install.sh --tag` 和 `flame up -d nexus`：每台有自己
的 `FLAME_TAG`、自己的 `current` 软链、自己的本地镜像。一次升一台，并让 nginx 指向已经升好的那台，而不是还停在旧 tag
的那台。

## 11. 备份

| 对象 | 方式 | 说明 |
|---|---|---|
| PostgreSQL | `pg_dump -Fc flame` 与 `litellm`，每日 | 迁移前必须手工一份 |
| `/srv/flame/data` | 存储快照或 rsync | 租户文件、资产、插件 |
| `/etc/flame` | 加密归档 | 含全部密钥；丢失等于重签所有 JWT |
| `flame-consul` 卷 | 不备份 | catalog 由周期重注册重建 |
| `flame-redis` 卷 | 不备份 | 丢失只掉粘性绑定（`session lost`） |

## 12. 安全基线

- 只发布 `27001`（+`27443`）；其余留在回环或内网
- 挂数据目录，不挂 docker socket；只有宿主上的 `sandboxd` 可以访问 daemon
- 桌面端与 API 同源，不需要放宽 CORS
- 无 ACL 的 Consul 是可写控制面：回环，或内网 + 防火墙白名单，或 ACL + TLS（token 只能通过
  `CONSUL_HTTP_TOKEN` 注入，四个服务的配置都没有 token 字段）
- PostgreSQL 的 DSN 写死 `sslmode=disable`，Redis 客户端没有 TLS 字段：跨机只能在受控网络里，直到补齐
- `bootstrap` 的种子凭据（`12345678`、固定 API key `sk-poc-t0-primary`）与 PoC 一致，`install.sh` 就按这个值写入
  `flame.env`：首次登录后立刻改口令、轮换该 key，然后删掉 `initial-credentials.txt`

## 13. 验收检查

| 检查项 | 命令 | 期望 |
|---|---|---|
| 一次跑完上面的检查 | `./scripts/health.sh` | 全部 ok；FAIL 会点名是哪一项 |
| 真的能答一次 | `./scripts/smoke.sh`（需真实的 `FLAME_LLM_*`） | 回答里含脚本发出去的那个随机串——这是唯一能证明"能用"的检查 |
| 容器 | `flame ps` | 全部 running / healthy |
| 当前 tag 的镜像 | `docker image inspect flame/nexus:$FLAME_TAG flame/gateway:$FLAME_TAG flame/web:$FLAME_TAG flame/sandbox:$FLAME_TAG` | 四个都在 |
| 平台 SQL | `./scripts/migrate.sh --check` | 文件集合与执行清单完全一致 |
| 入口 | `curl -sI http://localhost:27001/` | 200 |
| 接口健康 | `curl -s http://localhost:27001/health` | `db` / `redis` / `litellm` / `sandbox` 全部就绪时才是 200；503 的响应体会点名缺哪个 |
| 服务注册 | `curl -s 'localhost:8500/v1/health/service/sandboxd?passing=1'` | 每台执行器一条，全部 passing |
| 网关池 | `flame logs sandboxg \| grep -i backend` | 实例数与预期一致 |
| 浏览器池 | `flame logs browserg \| grep -i browserd` | 实例数与预期一致 |
| 共享存储 | 通过沙箱写文件，从前端下载 | 内容正确、不 404 |
| 粘性 | 同一 session 连续两次交互调用 | 落在同一后端 |
| 副本健康 | `curl -s http://<副本内网 IP>:27006/health` | 200，响应里没有失败的依赖 |
| 副本在轮转里（§8.2） | 从入口登录，再刷新几次让请求落到两个副本上 | 仍在登录态，没有静默 401 |
| 每台 nexus 的静态资源 | `flame logs nexus \| grep -i "static file server"` | 每台都打印出 `/static` 挂在哪里 |
| nsjail rootfs（可选，见 `deployment.zh.md` §5.3） | `./scripts/rootfs.sh --tag $FLAME_TAG --check` | 每一项都 ok，含一次真实的 `nsjail --chroot … python3 -V` |

两处最容易读错的：

- **注册返回是「一个实例一个元素」的数组。** 每个元素里有 `Node`、`Service` 和属于该实例的 `Checks`，所以「每台执行器
  一条」指的是数组长度。一个健康的实例本身就已经包含两次字符串 `"Service"`（条目键和服务名段），再加上每个检查的
  `ServiceID` / `ServiceName` 各一次——按这个字符串计数永远量不出注册了几个实例。`health.sh` 数的是条目键
  `"Service":{`，这是最快的读法。
- **`health.sh` 只证明进程活着，不证明隔离生效。** `mode: nsjail` 下 rootfs 是上一世代也照样应答，所以上面那行
  `rootfs.sh --check` 要一起跑。

## 14. 故障排查

| 现象 | 大概率原因 | 检查 / 处理 |
|---|---|---|
| 注册了但一直 critical | `service_addr` 从 Consul 所在主机不可达 | `flame exec consul wget -qO- http://<addr>:27081/healthz`；修地址或防火墙 |
| 完全没有注册 | 注册关闭、`registry.address` 错，或推导不出地址 | 看执行器日志：它会打印最终 ID/地址，或直接拒绝启动的原因 |
| 网关没有后端 | catalog 为空、`service_name` 不一致，或留了 `127.0.0.1` 种子 | `curl -s 'localhost:8500/v1/catalog/service/sandboxd'`；删掉回环种子 |
| nexus 返回 503 | PostgreSQL / Redis / LiteLLM 不可达，或沙箱能力快照从未取到 | `curl -s localhost:27001/health`，响应里点名失败的键（`db` / `redis` / `litellm` / `sandbox`） |
| 文件时有时无 | 存储其实没共享 | 两个节点 `stat` 同一路径对比 |
| 副本健康但收不到流量 | nginx 还指着 Compose 服务名（`http://nexus:27006`） | 按 §8.2 加显式 `upstream` |
| 第二台机器起了自己的 PostgreSQL / Redis | 那边用了裸 `flame up -d`，起了整套 | 那台的 `flame ps` 应该只有 `nexus`（或一个网关） |
| nginx 502 | nexus 还没起来 | `flame ps`、`flame logs nexus` |
| `session lost` | 持有会话的实例重启或被摘除 | 预期行为，浏览器会话不可迁移 |

## 15. 已知缺口

1. `bootstrap` 仍由 `nexus/cmd/seedpoc` 编译、以正式名字装进镜像：行为与 §7 的契约一致，待做的只是重命名。
2. 平台 SQL 没有版本表（每次按清单全量执行，靠幂等），租户 schema 完全没有升级机制；见 `deployment.md` §11 第
   7、8 项。
3. 第 3 档有产物但没有端到端验证——哪台起哪些服务仍靠人工挑选；v1 只验证第 1、2 档。
4. Consul ACL 不由任何脚本生成；PostgreSQL / Redis 无 TLS；跨机 nexus 未验证。
5. `nexus` 自己的注册默认关闭，且把 `registry.service_addr` 原样透传，不像执行器那样过一遍 `ResolveAdvertise`
   归一化。默认值是 `http://localhost`，所以多机照现状开启会在 Consul 里互相顶掉，探活还会拼成 `http://http://…`；
   在代码归一化之前，逐台写裸 IP。
6. 第 1 档的 `127.0.0.1:8500` 是必需端口（裸跑执行器要注册），不是多余暴露。
