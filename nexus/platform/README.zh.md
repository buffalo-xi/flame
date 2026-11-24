# nexus/platform —— 平台管理台（多租户管理）

中文 | [English](README.md)

平台身份（管理员 / 用户）与租户控制台完全独立：前者在 `platform.users`，后者在各租户
`<schema>.users`——**同名但完全独立的两张表**，语义也不同（平台侧 `admin`/`user`，
租户侧 `manage`/`service`）。平台登录不填租户，登录后按 token 里的 `role` 分流：

| role | 界面 | 数据面 |
|---|---|---|
| `admin` 管理员 | 管理员页（租户管理 + 平台用户管理） | `/platform/tenants*`、`/platform/users*` 全部 |
| `user` 用户 | 用户页（我的租户） | `/platform/me/tenants`（读，`owner_user_id = 自己`）+ `/platform/me/tenants/:schema/status`（启停自己名下的租户） |

## 目录

```
nexus/platform/
├─ README.md                      本文件（边界规则）
├─ router.go                      挂载 /platform 路由组（由 nexus/cmd/router.go 调用）
├─ auth/                          平台身份：角色词表、token 签发/解析、两把中间件、受保护管理员名常量
├─ handler/                       HTTP 层（薄）：login / me / tenants / users
├─ repository/{tenants,pmusers}/  platform schema 的数据访问
├─ provision/                     开通编排 + template/ 模板包（//go:embed，必须在本包目录下）
│                                 template/schema.sql  表结构
│                                 template/seed.sql    main + 受保护技能 + 授权绑定 + DeepSeek 供应商与模型
│                                 template/files/      定义域文件（AGENT.md / SKILL.md）
└─ sql/                           platform.sql（平台两张表，DBA 执行）
```

## 挂载方式

`nexus/cmd/router.go` 两行：

```go
platformGroup := router.Group("/platform")
platform.SetupRoutes(platformGroup)
```

本包**没有启动期引导**：首个平台管理员（登录名固定 `plat`）由环境初始数据预置，
`nexus/cmd/main.go` 不再需要调用任何 platform 初始化函数。

## 边界规则（改动本目录时必须遵守）

**允许依赖**

- `flame/nexus/shared/*`：`config`、`db`、`account`、`tenants`、`quota`、`flowconfig`、`storage`、`workspace`、`repository/flowconfigs`
- `flame/tools/*`：`xhttp`、`xjwt`、`xlog`

**禁止依赖**

| 禁止 | 原因 / 替代 |
|---|---|
| `nexus/admin/**` | 那是租户控制台的 handler；开通租户时需要的"建首个租户管理员"由本目录自己写 INSERT |
| `nexus/service/**` | 租户运行时逻辑；目录约定请用 `nexus/shared/paths` |
| `nexus/shared/auth`（整个包） | 里面是租户登录 handler（依赖 gin）。密码/状态请用 `nexus/shared/account` |

反向也成立：**`nexus/**` 不得 import `nexus/platform/**`**，唯一例外是 `nexus/cmd` 的那两行挂载。

> 为什么放在 `nexus` module 内、而不是仓库根目录的独立 module：
> `nexus/` 自身是 module `flame/nexus`；若平台做成根目录独立 module，它要 `require flame/nexus`
> （用 shared 包），而 `nexus/cmd` 又要 `require` 平台 module 才能挂载 ⇒ **module 互相依赖成环**。
> 想要"仓库根目录的独立 module"，前提是**平台独立进程**（届时依赖变成单向：平台 → nexus，
> 但"停用租户立即生效"要退化为 TTL 生效，或改用 LISTEN/NOTIFY）。

## 身份互斥（两把锁）

| 打过来的 token | 结果 |
|---|---|
| 平台 token → `/admin/*` | 无 `schema` → 租户侧 `parseClaims` 直接拒 |
| 租户 token → `/platform/*` | 无 `scope` → `PlatformAuthMiddleware` 拒 |
| `user` token → `/platform/tenants*`、`/platform/users*` | `RequirePlatformAdmin` → 403 |

## 自助改密

`POST /platform/me/password`（body `{old_password,new_password}`）：本人凭旧密码更换，只改自己，
**不签发新 token、不作废其它端会话**（与租户侧 `/admin/me/password` 同语义）。
代人重置仍走 `POST /platform/users/:id/password`（仅管理员）。

> 初始数据里给 `plat` 的密码只是临时值：第一次登录后立刻用这个接口改掉。

## 用户自助操作自己名下的租户

平台「用户」有两条自助写入口（都在 `/platform/me/...`，**都不进管理员组**）：

| 接口 | 作用 | 权限 |
|---|---|---|
| `PUT /platform/me/tenants/:schema/status` | 启停（body `{status: active\|suspended}`） | `owner_user_id == 自己`，否则 **403**；租户不存在 **404** |
| `GET/PUT /platform/me/tenants/:schema/flowconfigs` | 读写自己名下租户的**工作流运行参数**（`GET` 回 `{system, tenant}`） | 同上 |
| `POST /platform/me/tenants/:schema/primary/password` | 为名下租户的**根账号 primary** 重置密码（body `{password}`） | 同上；`primary` 行不存在 **404** |

- 管理员走 `/platform/tenants/:schema/...` 那组（可操作**任意**租户）；两条路**共用同一份校验核心**
  （`handler/flowconfigs.go` 的 `loadFlowBundle` / `saveFlowConfigs`），口径不会漂。
- owner 校验统一在 `mustOwnTenant`（启停与工作流运行参数共用），越权一律 403 `{error:"forbidden"}`。
- 状态只允许 `active ⇄ suspended`（`pending / deleted` 属内部态，两边都不给写）；工作流字段与租户侧
  `/admin/flowconfigs` 同规则：7 个字段均可空 = 回退系统级、`-1` = 不限制、**不得超过系统级**。
- 写完的缓存失效：状态 → `sharedtenants.Invalidate(schema)`；工作流 → `flowconfig.Invalidate(schema)`。
  **两者都立即生效**（停用后该租户的登录与 MCP/RPC 解析马上被拒）。重置密码无缓存需要失效。
- **根账号密码**那条只 `UPDATE <schema>.users SET password`（`WHERE name = primary`），
  角色 / 状态 / 显示名一律不碰 —— 与租户侧对 primary 的处理一致（`updateUser` 拦修改，**重置密码刻意留着**）。
  它解决的是"平台交付的初始密码被忘了"：租户侧进不去（改密要旧密码），平台管理台又不触碰租户 `users`。
  不签发 token、不作废已有会话（要立即踢下线就停用该租户）。
- 管理员永远不是 owner（`CreateTenant` / `UpdateTenant` 都要求 owner 是 `role=user`），所以两条路不重叠。

## 列表分页（租户）

`GET /platform/tenants?q=&status=&page=&page_size=`：`page` 从 1 起；`page_size` 省略 / `<=0` = **不分页**
（返回全部，兼容旧调用），上限 100。回包 `{items,total,page,page_size}`，其中 **`total` 是筛选后的总数**
（不是本页条数），前端分页器必须用它。

> 每行都要算三次用量 COUNT（`quota.CountAgents/Skills/Flows`），所以放大 `page_size` 等于放大 DB 压力。

## 新建租户：schema 自动探索 + 固定登录名

- `GET /platform/tenant-schemas/next` → `{schema}`：建议值，规则是 **`t` + 从 0 起逐个 +1**
  （`t0`、`t1`、`t2`…）取第一个未被占用的，跳过系统保留名。
  路径用独立静态前缀 `tenant-schemas`：`/tenants/next-schema` 与 `/tenants/:schema` 同级，有冲突风险。
- `POST /platform/tenants`：`schema` 留空 = 后端按同一规则自动探索（管理台不给手填）；
  `admin_name` 留空 = `provision.DefaultAdminName`（`primary`）。显式传值仍按显式值走，兼容脚本调用。

> `primary` 在**租户侧是受保护账号**：`nexus/admin/users` 的 `updateUser` 拒绝修改它（资料 / 角色 / 状态），
> 租户侧也**不提供删除用户接口** —— 保证"平台交付的那个账号"始终是有效的 `manage` 且可登录。
> 常量同源在 `shared/tenants.PrimaryUserName`（platform 与 admin 互不 import，只能共享这一份）。
- **占用判定**（`provision.SchemaAvailable`）两套事实都可用才算可用：
  库里没有同名 schema（`information_schema.schemata`）**且**注册表没有该行（含已停用/软删）。
  只看一处都会在 `CREATE SCHEMA` 撞墙（孤儿 schema / 已 DROP 的注册行）。
- **落库前会再查一次**；检查与 `CREATE SCHEMA` 之间被抢占（PG `42P06` 也算）时，
  `Provision` 返回 `ErrSchemaTaken` → 接口回 **HTTP 409 + `{error:"conflict", message}`**，
  前端提示"已被占用"、重新探索出新值，**弹窗不关**，等操作者确认后再次开通（**不静默改 schema**）。

## 工作流运行参数（租户行）

`GET /platform/tenants/:schema/flowconfigs` → `{system, tenant}`（系统级只读，供对照）；
`PUT` 同路径只写 `platform.flow_configs` 里 `tenant_id = :schema` 那一行，校验口径与租户侧
`/admin/flowconfigs` 完全一致（租户值不得超过系统级）。实现在 `handler/flowconfigs.go`，
仓储直接复用 `shared/repository/flowconfigs`（不引 `nexus/admin/**`）。

> 租户运行时读的是 `shared/flowconfig` 的 TTL 缓存；平台侧写完会调 `flowconfig.Invalidate(schema)`
> 主动失效，**本进程下一次读取即生效**（TTL 只作为"绕过接口直接改库 / 多实例部署"的兜底）。

## 系统设置

前两个域是 `tenant_id = ''` 的**系统行**（全局默认 / 硬顶），第三个域是全局共享的预设表。都仅管理员：

| 域 | 接口 | 语义 |
|---|---|---|
| 配额上限 | `GET/PUT /platform/system/quota` | 同样是**硬顶**：运行期取 `min(租户值, 系统值)`，租户行配得比它高也无效 |
| 工作流运行参数 | `GET/PUT /platform/system/flowconfigs` | 既是租户未设置时的**兜底**，也是租户值的**硬顶**（运行期 `clampMax` 取 min） |
| LLM 服务商预设 | `GET/POST /platform/system/llm-provider-presets`、`PUT/DELETE /platform/system/llm-provider-presets/:id` | `platform.llm_provider_presets` 的完整 CRUD（租户侧只读；点预设是**复制填充**，无外键，删除不影响租户） |

前两个域的 GET 都额外回带 `fallback`（代码内置兜底值），供界面提示"留空会退到哪"：
`flowconfig.Fallback()` / `quota.Fallback()`，避免前端硬编码而与共享包漂移。

前两个域的写接口只校验"值只能是 `-1`（不限制）或非负数"；**没有"不超过上级"的校验**（它就是最上级）。

预设域：`id` 由**程序生成**（`uuid.NewString()`，表上无 DB 默认值），长度按建表列宽校验
（name 100 / icon 255 / protocol 50 / url 1024），`protocol` 无 CHECK（前端给
vertexai / anthropic / googleai / openai 四项 + 允许自定义）；`extra_params` 不参与读写
（新增走表默认 `{}`，编辑不动该列）。

实现在 `handler/system.go` 与 `handler/llmpresets.go`，前端在管理台顶栏「系统设置」页（左域导航 + 右域卡片）。

## 租户配额：原始值 vs 生效值

`GET /platform/tenants/:schema/quota` 回四样：

| 字段 | 含义 |
|---|---|
| `quota` | 该租户的**原始行**（`null` = 未设置）—— 编辑界面绑它 |
| `system` | 系统行原始值（`null` = 未设置）—— 展示"系统顶" |
| `fallback` | 代码兜底值（智能体 100 / 技能 300 / 工作流 300 / 单会话空间 `-1` / 单用户会话数 `-1`）—— 系统行未设置时它就是顶 |
| `effective` | 生效值 = `min(租户值, 系统顶)`，直接取 `quota.Resolve(schema)`，**前端不重算**截断规则 |

`PUT` 同路径写的是原始行，写完 `quota.Invalidate(schema)`。

> **列表接口（`/platform/tenants`）回的是生效值**（`tenantView` 里走 `quota.Resolve`），
> 所以别拿列表数据当编辑框：某租户行存 500、生效值 100 时，用列表数据回填再保存会把 500 压成 100。

## 插件停用

插件**缺省对所有租户开放**（2026-11-02 起）：租户只要在自己的「插件」页启用并配好实现就能用。
平台侧不再逐租户开通，`platform.tenant_plugins`（建表脚本 `sql/tenant_plugins.sql`，幂等）退化为
**停用记录** —— 只有显式 `is_allowed = false` 的行才表示「对这个租户停用」，没有行 = 开放。
租户侧 `{schema}.plugins` 仍负责「用哪个实现、key、是否启用」，两者都过才放行。

| 接口 | 语义 |
|---|---|
| `GET /platform/plugins` | 插件管理页的聚合视图：磁盘清单（含实现）+ 全部租户 + 各租户原始行 + 租户侧启用状态 |
| `GET /platform/tenants/:schema/plugins` | 磁盘插件清单 × 本租户策略（生效值 + 原始行）× 租户侧启用状态 |
| `PUT /platform/tenants/:schema/plugins/:name` | 写本租户行（`allowed=false` 即停用）；字段省略 = 保留当前值 |
| `DELETE /platform/tenants/:schema/plugins/:name` | 删本租户行 = 回到缺省（开放） |

解析规则在 `shared/pluginpolicy`：**没有行 = 开放**，有行按 `is_allowed` 取。
执行期 `Runner.Invoke` 读不到策略时**不放行**（fail-closed），插件目录降级为「全部不可用」并记日志 ——
缺省虽已开放，但「读不出」不等于「没人停用过」。
全局急停仍在 `platform.plugin_gates`：缺省放行，一旦拦下压过本表、影响所有租户。

为什么翻转：插件本体是平台放进磁盘的可信代码，凭证与成本都在租户侧
（`{schema}.plugins.configs` 里是租户自己的 key），逐个开通只是运营负担。
要收紧时仍能按租户停用，或用全局急停一键止血。

升级注意：老库执行 `sql/tenant_plugins.sql` 的 2026-11-02 段，会删掉 `is_allowed = true`
的冗余行；原先 `is_allowed = false` 的行继续表示停用，行为不变（仍然不可用）。

> 历史：本表曾带 `daily_limit` / `concurrency_limit`，但计数只在进程内（重启清零、
> 多副本各算各的），「每天最多 N 次」名不副实，已移除。将来要做真正的配额，
> 应基于共享存储（Redis）重新设计。
>
> 平台管理台不能 import `service/plugin`（见「边界规则」），磁盘插件清单经
> `shared/plugininfo` 这一层最小视图转发（`service/plugin.SetDefault` 时注入）。

## 平台用户

`POST /platform/users`：账号名 `^[A-Za-z0-9_-]{1,64}$`、密码 ≥ `account.MinPasswordLength`（8）。
**显示名可空**（写入前两侧都 trim；为空时界面回退显示账号名）—— `POST` 与 `PUT` 口径一致，都不强制。

**受保护管理员（固定 `plat`）受保护**：`PUT /platform/users/:id` 对它一律拒绝（资料 / 角色 / 状态都不可改，
放在"不能对自己动手"之前，所以 `plat` 本人也改不了自己）；**没有删除用户接口**，所以"不得删除"天然成立。
登录名写死在 `auth.BootstrapAdminName`（与租户侧 `shared/tenants.PrimaryUserName` 同性质：都是"系统预置的
那个账号"），配置里不再有 `name` 字段，也不再由 nexus 在启动时创建（见「建表与引导」）。`userView` 回带 `protected: true/false`，前端据此置灰「编辑 / 停用」，
**不写死名字**。重置密码不拦（与前几条同理：不改账号身份，是恢复访问的手段）。

`GET /platform/users?role=&status=&q=&page=&page_size=`：分页口径与 `/platform/tenants` **完全一致**
（`page` 从 1 起；`page_size` 省略 / `<=0` = 不分页，上限 100；回 `{items,total,page,page_size}`，
`total` 是筛选后的总数）。「用户」行附带 `tenant_count`（名下租户数）—— 这是**逐行一次 `CountByOwner`**，
所以 `page_size` 别放大。

`GET /platform/users/:id/tenants`：某平台用户名下的租户（管理台详情卡的「名下租户」块）。
与 `/platform/me/tenants` 同源（`tenantsrepo.ListByOwner`，已排除 `status=deleted`），回 `{items,total}`。

`userView` 回带 `created_at` / `updated_at`（详情卡的信息行用）。

## 自锁守卫（不可省略）

- 不能停用 / 降级**当前登录账号**
- 不能停用 / 降级**最后一个 active 的管理员**（否则平台自锁，只能改库救场）
- 停用名字下有租户的「用户」：允许，但返回 `warning` 提示先转移负责人（**不级联、不自动转移**）

## 写操作后必须失效缓存

| 写什么 | 调用 |
|---|---|
| 租户开 / 停 / 删 / 改状态 | `sharedtenants.Invalidate(schema)`（否则停用最多延迟一个 TTL 周期才生效） |
| 改租户配额 | `quota.Invalidate(schema)` |
| 改租户的 flow 配置 | `flowconfig.Invalidate(schema)` |
| 改**系统行**（flow / 配额） | `flowconfig.InvalidateAll()` / `quota.InvalidateAll()` |

> `Resolve` 缓存的是"已合并（tenant -> sys -> fallback）"的结果，所以**改系统行必须全量清**：
> 只删 `""` 那一格不够，每个租户缓存的那一格都过时了。
>
> 失效只覆盖**本进程内存**：单进程部署下等于全局生效；同服务多副本 / 拆成多进程后，别的实例
> 只能等 TTL（2 分钟）兜底。要跨进程立即生效再上 PG `LISTEN/NOTIFY`（写方在同一事务里
> `pg_notify`，各实例常驻 LISTEN 连接收到就清缓存），或"配置版本号 + 每 3~5 秒轮询"。

## 建表与引导

1. `psql ... -f nexus/platform/sql/platform.sql`（两张表 + 索引）
   - 跨租户资源分发另有一份 `sql/shared_assets.sql`（资产快照 / 可见范围 / 安装记录），
     **必须在 `platform.sql` 之后执行**（外键引用 `platform.tenants`）。方案见
     [docs/asset-sharing.zh.md](../docs/asset-sharing.zh.md)。
2. 装载**环境初始数据**：其中必须有一行 active、角色 `admin`、登录名固定 `plat` 的 `users`。
   平台管理员不再由 nexus 自举 —— `nexus/cmd/main.go` 没有任何 platform 启动引导，配置里也没有
   `platform.bootstrap_admin` / `platform.ddl_dsn`。缺这一行，管理台就没有可登录的账号
   （登录名 `plat` 是 `auth.BootstrapAdminName`，管理台不可修改 / 不可删除）。
3. 开通租户（管理台「新建租户」或 `POST /platform/tenants`）已可用，实现在 `provision/`：

   - 单个事务：`CREATE SCHEMA` → 事务级 `set_config('search_path', …)` → `template/schema.sql` → `template/seed.sql`
     → 首个 `manage` 用户 → 提交前物化 `template/files/` 到 `tenants/<schema>/` → `platform.tenants` 行
     → `platform.tenant_capabilities` 缺省全开（`capability.All()` 每项 `is_allowed=true`；平台可逐项关闭、全局闸可急停）
   - 失败整体回滚（PG 支持事务性 DDL），已写入的文件目录用 `DeletePrefix` 回收，不留半残租户
   - 提交后 `sharedtenants.Invalidate` + `quota.Invalidate`
   - schema 由 `provision.NextSchema` 自动探索（管理台不可编辑），首个管理员登录名固定 `primary`

   模板包内容与重新生成方式见 [docs/tenant-template-pack.zh.md](../docs/tenant-template-pack.zh.md)（租户创建资源方案）。
   一句话：`schema.sql` 从 t0 dump 生成、`seed.sql` 与 `files/` 由预置资产维护，**运行时完全不读 t0**。
