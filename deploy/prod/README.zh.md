# Flame 正式部署产物

中文 | [English](README.md)

这个目录是 [`nexus/docs/deployment.zh.md`](../../nexus/docs/deployment.zh.md)（设计与"为什么"）和
[`nexus/docs/deployment-runbook.zh.md`](../../nexus/docs/deployment-runbook.zh.md)（逐条命令与验收）的可部署形态。
理由与验收标准看那两份；这里只说清"有什么、按什么顺序用"。

刻意**不用 K8s、不用 Ansible、不用 operator**：一台普通 Linux 主机跑整套产品，一份 compose 作为启动器，systemd
负责开机顺序与失败重启，两个持有宿主特权的组件（`sandboxd`、`browserd`）裸跑。

## 先在本机试一下（约一分钟）

不需要 root、不装 systemd、不在这个目录之外写任何东西：

```bash
./scripts/images.sh build --tag dev --no-sandbox   # 一次性：base / nexus / gateway / web
./scripts/dev-up.sh                                # 数据面、SQL、种子、执行器、应用面
```

`dev-up.sh` 不停在"容器都起来了"：它会等到 `/health` 变成 `ok`，所以打印凭据的那一刻控制台就能用。种子会给租户一条
`DeepSeek` 供应商（API key 留空）——把 key 填进去、再把它的 `deepseek-flash` 模型配置到网关即可。然后打开
<http://127.0.0.1:27001>：

| | |
|---|---|
| 平台管理员 | `plat` / `12345678` |
| 租户管理员（租户 `t0`） | `primary` / `12345678` |

`./scripts/dev-up.sh down` 停掉（保留数据），`purge` 连数据一起删。

试跑有两条诚实的边界：种子里的 `DeepSeek` 供应商没有 API key（也没有本地回显模型），所以在你填上真实 key 并配置
模型之前，没有任何模型能应答；`sandboxd` 以普通进程跑在本机（必须如此——它就是"起容器"的那一环），所以 shell 工具
能跑但看不到租户的文件，`browserd` 则完全不启动，浏览器工具报不可用。其余都是真的——控制台、登录、租户、智能体、
endpoint、API key、经 LiteLLM 的模型配置。

`./scripts/dev-up.sh --smoke` 会额外把这条路径端到端跑一遍。它需要 `FLAME_LLM_BASE_URL`、`FLAME_LLM_API_KEY` 和
`FLAME_LLM_MODEL`，并用固定名字创建供应商 `smoke` 和一个模型，所以只在你不介意控制台里多出这两行时再用。

## 这里有什么

```
deploy/prod/
├─ compose.yaml                     整套：consul、postgres、redis、litellm、nexus、两个网关、web
├─ compose.multi-host.yaml          第 3 档叠加：只写 ports，由 COMPOSE_FILE 决定加不加载
├─ compose.dev.yaml                 本机试跑叠加：.devrun 路径、命名卷；永不用于部署
├─ config/
│  ├─ nexus.yaml                    全是 ${VAR}，由 nexus 从自己的环境展开
│  ├─ litellm.yaml                  store_model_in_db + master key：没有它，配模型会静默不生效
│  ├─ sandboxd.yaml                 渲染一次进 /etc/flame，之后升级不覆盖（宿主策略）
│  ├─ browserd.yaml
│  ├─ sandboxg.yaml
│  ├─ browserg.yaml
│  ├─ nginx/                        公共 map、共用 location、27001、可选的 27443 TLS
│  ├─ postgres/10-litellm-db.sql    initdb.d：只在空数据卷上建第二个库
│  └─ redis/redis.conf.tmpl         渲染进口令；noeviction + AOF
├─ images/                          base / nexus / gateway / web 四个 Dockerfile（sandbox 用 sandboxd/ 那份）
├─ systemd/                         flame（容器）、sandboxd、browserd、migrate、可选的专用 dockerd
├─ scripts/
│  ├─ lib.sh                        共用：log/die、密钥生成、配置渲染器
│  ├─ flame                          安装到 /usr/local/bin 的包装脚本
│  ├─ build.sh  images.sh           产物与镜像（images.sh 另有 save/load 做离线交付）
│  ├─ install.sh                    前置检查、落位、渲染、装 unit、导入镜像
│  ├─ migrate.sh  migrate-apply.sh  平台 SQL（固定顺序，--check 校验文件集合）
│  ├─ bootstrap.sh  smoke.sh        一次性种子（含 v1 回填）/ 端到端调用并断言拿到回答
│  └─ dev-up.sh                     本机试跑：拉起、种子、执行器；--smoke 另外跑端到端自检
├─ env/flame.env.example            每个变量的用途与改坏的后果
├─ env/SECRETS.zh.md                影响面与轮换风险表（英文见 SECRETS.md）
├─ .devrun/                         试跑的渲染配置与密钥（gitignore）
└─ out/                             构建产物（gitignore）
```

## 操作顺序

```bash
# 构建机（有 go 与 node）
./scripts/build.sh
./scripts/images.sh build --tag 2026.12.01
./scripts/images.sh save  --tag 2026.12.01 --dir /srv/flame/release-images   # 离线交付

# 目标机，root
./scripts/install.sh --tag 2026.12.01 --check      # 只做前置检查，什么都不改
./scripts/install.sh --tag 2026.12.01 --images /srv/flame/release-images

flame up -d consul postgres redis litellm          # 先起数据面
./scripts/migrate.sh                               # 平台 SQL，按依赖序
flame --profile jobs run --rm bootstrap             # plat + user01 + t0 + 能力 + API key
systemctl start flame-sandboxd flame-browserd      # 两个裸跑执行器
flame up -d                                        # 再起应用面
./scripts/health.sh
```

## 三个容易踩的地方

- **`/srv/flame/data` 必须处处同一份**，挂载点绝对路径一致，并且以 uid 1000 访问。路径不同或 uid 不同都不会报错，
  只会把租户文件悄悄分成两棵看起来都正常的树。
- **执行器不是容器。** `sandboxd` 要宿主 docker daemon，`browserd` 要宿主 Chromium；它们的端口
  （27081 / 27091）必须被网关容器与注册中心拨到。这条跨边界链路要在两处同时配对（`compose.yaml` 的
  extra_hosts、`sandboxd.yaml` 的 `registry.*`），漏一处就表现为"sandboxd 不可达"或"注册了却始终不健康"。
- **`FLAME_TAG` 就是版本。** 升级与回退都是"改这一行 + `flame up -d`"，再对两个裸跑单元
  `systemctl restart`。不要在运行中的容器里改文件：容器一重建就丢。

## 试跑与正式部署的差别

| | 试跑（`dev-up.sh`） | 正式（`install.sh`） |
|---|---|---|
| root、systemd | 不需要 | 需要（unit、`flame` 包装脚本、`/etc/flame`） |
| 存储 | Docker 命名卷 | 绑定 `/srv/flame/data`，uid 1000 |
| 模型 | 种子给了 `DeepSeek`（key 留空）与 `deepseek-flash` | 在控制台加自己的供应商 |
| 执行器 | `sandboxd` 本机进程，无 `browserd` | 两个都是 systemd unit |
| TLS | 无 | 27443 上可选证书 |
| `/health` | 沙箱注册后即 `ok` | 必须 `ok`，其余由 `health.sh` 检查 |

## 这一版的已知缺口

- **provision 不创建发布版本**，而生产入口拒绝没有版本的智能体（"no released version yet"）。所以 bootstrap
  作业在种子之后跑一次 `backfillversions`（`scripts/bootstrap.sh` 里标了 `TODO(provision)`）：以后从控制台新建的
  租户仍需要补这一次。
- `bootstrap` 仍由 `nexus/cmd/seedpoc` 编译、以正式名字安装（`images/nexus.Dockerfile` 里有说明）。行为已符合
  文档契约，待做的是重命名。
- 没有 `schema_migrations` 表，也没有租户 schema 升级工具：平台 SQL 靠幂等与固定顺序，租户 schema 完全不迁移。
  见 `deployment.zh.md` §11。
- 没有 drain 端点：停 `sandboxd` 会关掉它的复用容器，停 `browserd` 会丢该节点的浏览器会话。维护窗口只能是
  "低峰 + 等空闲超时"。
- 第 3 档（容器分机）有产物但未端到端验证，边界见运行手册的"已知缺口"。
