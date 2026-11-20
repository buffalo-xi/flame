# 跨租户资源分发（平台审核 + 复制式安装）

中文 | [English](asset-sharing.md)

> 状态：**技能 / 自定义工具 / 智能体三条链路均已实现**（上架 / 审核 / 安装 / 撤回）。范围：智能体 / 技能 / 自定义工具 从 A 租户流向 B 租户。
> 定案：**平台审核 + 复制式分发**；不做跨租户引用、不做双向同步、不做自动升级。
>
> 已落地（本轮）：
>
> | 层 | 位置 |
> |---|---|
> | 文件打包 / 还原 / 脱敏 / 命名 | `shared/assetpack`（含单测） |
> | 表读写 | `shared/repository/sharedassets` |
> | 租户侧接口 | `admin/share`（`/admin/share/*`） |
> | 平台侧接口 | `platform/handler/assets.go`（`/platform/assets/*`） |
> | 建表 | `platform/sql/shared_assets.sql`（**需先执行**，否则上述接口报缺表） |
>
> `kind` 支持 `skill` / `custom_tool` / `agent`。智能体的装法见 §7 第 4 步：
> 技能按**名字**重绑、工具授权按 `ref` 重建、三个开关（`is_supervisor` 等）按包设置、
> `agent_relations` 不复制（装完必须在目标租户重新配「谁能调用我」）。

## 1. 为什么必须是「复制」

| 事实 | 后果 |
|---|---|
| 每租户 = 一个 PG schema，`agents` / `skills` / `custom_tools` / 授权表都在该 schema 内，**没有 tenant 列** | 按 id 跨租户引用等于绕开隔离 |
| 文件在 `tenants/<schema>/{agents,skills,tools}/…`，`Context.StoragePath` 有「必须落在本租户目录内」的断言 | 跨租户读文件在存储层就被拦 |
| 运行期按 schema 解析实体（agent → skills → tool grants → capability） | 引用式共享要把权限判定、缓存失效、删除级联全部重做一遍 |
| `platform` 禁止 import `nexus/admin`；`nexus/**` 禁止 import `platform`（仅 `cmd` 两行挂载） | 打包/安装逻辑只能落在 `shared/*`，两侧共用 |

装完就是目标租户自己的实体，此后独立演化；`forked_from` 只做溯源与「上游有新版」提示，不构成依赖。

## 2. 三种单元与依赖闭包

| 单元 | DB 行 | 文件树 | 依赖 |
|---|---|---|---|
| `skill` | `skills`（租户级：`agent_id IS NULL`） | `skills/<name>/` | `skill_tool_grants`（`ref` 是**字符串**：`custom:<name>` / 组名 / 工具名） |
| `custom_tool` | `custom_tools` | `tools/<name>/`（`entry` + `lib/` + `templates/`…） | 无；但要 `sandbox.*` 与 `net.egress` 能力 |
| `agent` | `agents` | `agents/<agent>/`（`AGENT.md` + 私有 `skills/`） | `agent_skill_bindings`（uuid）、`agent_tool_grants`（字符串）、`agent_relations`（uuid）、`config` |

P0 规则：

- 一次只上架**一个**单元。
- agent 的租户级技能依赖**不内联**，安装时按名字匹配目标租户已有技能，缺失进 `warnings`。内联打包（bundle）留 P1。
- **几乎任何实体都能上架 / 导出**：受保护名（`main` / `skill-authoring` / `agent-authoring` /
  `custom-tool-authoring`）、超权智能体、废弃态一概不阻断，只以 `warnings` 提示。
  内容与身份分开处理 —— `agent_relations` 不随包走（跨租户无法重映射）；而
  `is_supervisor` / `tools_unrestricted` / `skill_discovery` 三个开关与默认回复语言 `default_language` **随包走**
  （见 §7、§12.1）：它们描述「这个智能体怎么跑」，装完按包生效并在 `warnings` 里点名，不静默降权。
- **私有技能**（`tenants/<schema>/agents/<agent>/skills/<name>/`）**只能随所属智能体上架 / 导出**：
  - 随智能体走时保留归属 —— `skills/` 整棵目录进包，元数据进 `payload.private_skills`，
    安装时为每个技能重建一行（新 id、`agent_id` = 新 agent），连同它自己的 `skill_tool_grants`；
  - **不能单独上架 / 导出**：正文住在 agent 目录里，单独打包的落点只剩「提级成租户级」——
    归属永久丢失、可见性白名单不随包走（导入结果是「`restricted=true` 且零绑定 → 谁都发现不了」）。
    接口直接拒绝并提示「导出它所属的智能体」；确要共享，先在技能设置里把它提级为租户级技能。
- 安装侧同样不按保护名拒绝：占位靠重名冲突（`suggested_name`）兜住，否则自己导出的
  `main` / `skill-authoring` 会装不回来。

## 3. 文件落点

| 阶段 | 路径 | 归属 |
|---|---|---|
| 上架（读源） | `{root_dir}/tenants/<source_schema>/skills/<name>/` | 源租户 |
| 快照（上架后存） | `{root_dir}/platform-assets/<asset_id>/<build>/` | 平台 |
| 安装（写目标） | `{root_dir}/tenants/<target_schema>/skills/<installed_name>/` | 目标租户 |

- 新增常量 `paths.PlatformAssetsSubdir = "platform-assets"` 与 `paths.PlatformAssetKey(assetID, build)`；`{root_dir}` 即 `agent.root_dir`。
- 快照是**分发的唯一真源**：安装从这里拷，不回源租户读（跨租户读被存储层断言拦住，且源租户停用/删除后别人就装不了）。
- 快照目录 = 源目录的镜像（相对路径原样保留），安装整棵拷过去。
- 该前缀**不在任何沙箱挂载点内**（`/workspace`、`/agent`、`tools/` 都不覆盖），`safePath` 拒绝 `..`，租户侧路径工具天然到不了。
- 撤回/删除资产 = `storage.DeletePrefix("platform-assets/<asset_id>")`；已安装的副本不受影响（复制式语义）。
- 读写一律走 `shared/storage` 的 `List / ReadFile / WriteFile / DeletePrefix`（local / nfs / s3 通用），键用 `/` 分隔的逻辑路径，不要直接 `os.Open`。

## 4. 数据模型

建表脚本：`nexus/platform/sql/shared_assets.sql`（幂等，风格对齐 `platform/sql/platform.sql`）。三张表：

| 表 | 作用 |
|---|---|
| `platform.shared_assets` | 一行 = 一个已上架快照（`payload` 实体行快照 + `manifest` 依赖与文件清单 + 上架人） |
| `platform.shared_asset_tenants` | 可见范围 allowlist（仅 `visibility='allowlist'` 时读；有行 = 允许） |
| `platform.shared_asset_installs` | 谁从哪个版本装过（P1「上游有新的快照」的依据） |

关键口径：

- **文件内容不进 DB**，只在 `manifest.files` 里放清单（path / size / sha256）。
- 同名重复上架 = 覆盖同一行并 `build + 1`，新快照写新目录 `platform-assets/<id>/<newver>/`，旧版本目录保留（回滚与审计用）。
- `status`：`draft`（保存未提交）/ `pending`（待审）/ `listed` / `rejected` / `delisted`。租户侧只能 `pending ⇄ draft`（撤回），其余只能平台改。
- 上架人是**租户侧账号**（`{schema}.users`），不是平台用户；平台审核人是 `reviewed_by`（`platform.users`）。两者不可混用。

### 上架人字段

| 列 | 用途 |
|---|---|
| `source_schema` | 上架租户（= schema） |
| `listed_by` | 上架人 id（`users.id`），审计追溯 |
| `listed_by_name` | 上架时**展示名快照** |

只存名字快照，不跨 schema 回查 `users`：用户改名/停用后历史资产仍要能说清是谁发的，回查会让历史漂移。

对外 JSON（**JSON 用 `schema`，SQL 列才是 `schema_name`**，与 `tenantView` / `createTenantRequest` 的口径一致）：

```json
"listed_by": {
  "tenant": { "schema": "t3", "display_name": "研发一队" },
  "user":   { "id": "0f3c…", "display_name": "张三" }
}
```

- `tenant.display_name` 现查 `platform.tenants`（租户改名跟着变，这是租户自身属性）。
- `user.display_name` 取快照。
- 故意不给 `user.name`（登录名）：表里没存，硬给就得回查。

安装记录同理：`installer: { tenant: {schema, display_name}, user: {id, display_name} }`。

## 5. 上架（租户侧）

`POST /admin/share/listings`，body `{kind, ref}`（`ref`：skill / agent 用 id，custom_tool 用 name）。

1. 操作者需 `manage`；实体态不设闸门（受保护名 / 超权 / 私有归属 / 废弃只进 `warnings`，见 §2）。
2. 只取白名单列读实体行，不整行照搬（`created_by` / `updated_by` / `endpoints` / 快照号不带走）。
3. **剥敏感值**：
   - `custom_tools.configs` 一律清空，只保留 `config_fields` 声明；
   - `agents.config` 保留，但键名命中 `(?i)(api[_-]?key|token|secret|password|credential|authorization)` 的值置空并记 warning。
4. **打包文件**：递归列目录，上限 **2000 个文件 / 单文件 64 MiB / 合计 128 MiB**；`SKILL.md`、`AGENT.md`、`custom_tools.entry` 必须存在，否则拒绝上架。`agent` 打**整棵**目录（含 `skills/` 私有技能），不再跳过任何子目录。依赖里属于**本租户自定义工具**的那部分，以 `_deps/custom_tools/<name>/**` 挂进同一个包（`PackOptions.Trees`）—— 否则装到目标租户只有一条指向不存在工具的授权行；`builtin:<x>` 与组是平台自带的，不带。
5. **依赖声明**写进 `manifest.deps`（只声明，不解析成实体）：
   - `skill` → `[{type:"tool", ref}]`（来自 `skill_tool_grants`）
   - `agent` → `{skills:[name], private_skills:[name], tools:[{type,ref}]}`
   - `custom_tool` → `{requires:["sandbox.exec"], egress:bool}`
6. 落库 `status='pending'` 等平台审核——**但缺省是免审，直接 `listed`**；只有被显式设为
   「必须审核」的租户才进 `pending`（见下）。

### 上架审核策略（平台可配）

两级，缺省从宽（上架免审）：

| 层 | 存储 | 语义 |
|---|---|---|
| 全局默认 | `platform.listing_settings`（单行 `id=1`） | 没有行 = 免审 |
| 按租户覆盖 | `platform.listing_policies`（`schema_name` 主键） | 有行以行为准；删行 = 跟回默认 |

接口：`GET /platform/listing-policy`、`PUT /platform/listing-policy`（全局）、
`PUT /platform/listing-policy/:schema`（`{require_review: true|false|null}`，`null` = 删覆盖）。
界面：平台「本地市场 → 上架策略」。

- 免审（**缺省**）上架**直接 `listed`**，`listed_by` 照记、`reviewed_by` 留空（没有人工审过，不伪造）；
- 被设为「必须审核」的租户上架落后进 `pending`，等平台裁决；
- 重新上架同样按策略走：免审直接 `listed` 且版本 +1；
- 只影响「上架」，**不影响「从远端拉取」**——后者看 `peer_servers.install_policy`，两码事；
- 读不到策略时按「必须审核」处理（fail closed），改回必须审核不追溯已上架内容。

## 6. 审核（平台侧）

`POST /platform/assets/:id/review`，body `{action: approve|reject|delist, note}`。

删除（`DELETE /platform/assets/:id`）**不在状态机上**：下架是「撤出市场但留档」，删除是「彻底清掉」，
两者并列、不构成前后步骤，所以在架的资产也能直接删；不碰租户已装出去的副本。
`pending` / `rejected` / `draft` 仍拒绝 —— 那是租户侧还没走完的流程（要处置就是驳回）。

入口：平台管理台（`desktop/src/platform`）→ 顶栏「本地市场」（`views/AdminAssetsView.vue`），用平台管理员
（`plat`，`platform.users.role=admin`）登录。状态机单向：`approve`/`reject` 只对 `pending` 生效、`delist`
只对 `listed` 生效，界面按当前状态置灰按钮而不是点下去再报错；驳回必须填原因，它就是上架方在
「我的资产」里唯一能看到的那句话。同页还能改可见范围（`all` ↔ `allowlist`，全量替换）。
需要脚本化时直接打接口：`/platform/login` 拿 token → `GET /platform/assets?status=pending` →
`POST /platform/assets/:id/review`。

审核页按五项平铺：

1. 依赖是否齐全（`custom:<name>` 是否也上架了那个工具；声明的技能是否同名存在）
2. `manifest.deps.requires` 与描述是否相符（尤其 `net.egress`）
3. 文件数量 / 大小是否触顶；对文件内容再扫一遍凭据正则
4. 是否冒用受保护名（仅提示，不阻断 —— 保护只约束删除 / 改名）
5. 名称是否与已有资产冲突

## 7. 安装（租户侧）

`POST /admin/share/install`，body `{asset_id, name?, status?}`。

**顺序：先文件、后 DB**（文件删除幂等，DB 回滚更贵）。

1. 可见性：`status='listed'` 且（`visibility='all'` 或 `shared_asset_tenants` 有本租户）。
2. 预检（任一项不过直接 400，不做半截安装）：
   - 目标租户 `status='active'`，调用方 `manage`
   - 配额：`quota.Resolve(schema)` 的 `max_agents` / `max_skills` 还有余量
   - 能力：`manifest.deps.requires` 逐项查 `platform.tenant_capabilities`；`egress` 查 `net.egress`
   - 名字：默认沿用原名，`ValidName` 校验、租户内唯一；冲突回 **409 + 建议名**（`name-2`），由前端确认后重试（不静默改名）
   - 不按受保护名单独拒绝（保护只约束删除 / 改名）；同名会走上面的「建议名」分支
3. 拷文件：`platform-assets/<id>/<ver>/` → `tenants/<target>/…/<name>/`。清单里的
   `_deps/custom_tools/<name>/**` 是**依赖的自定义工具本体**，不落进资产自己的目录，而是剥掉
   前缀装到 `custom_tools/<tool>/`（`assetpack.BodyFiles` 拆本体、`assetpack.Attachments` 拆附件、
   `RestoreFilesUnder` 剥前缀落盘）。
4. 单事务写 DB（schema 限定 + `pq.QuoteIdentifier`）：
   - 新 uuid（`uuid.NewString()`）；`created_by` / `updated_by` = 调用者
   - `skill`：`agent_id = NULL`（恒为租户级），`restricted` **不随包走**（恒落 `false`：白名单无法跨租户重映射，落 `true` 只会得到「谁都没被授权 → 谁都发现不了」的技能），`status` 默认 1
   - `agent` 自带的**私有技能**：按 `payload.private_skills` 逐个插行（新 id、`agent_id` = 新 agent、`status` 默认 1），`skill_tool_grants` 一并重建；废弃态的私有技能不打包也不重建
   - `custom_tool`：`configs = {}`（等租户自己填），`status` 默认 **1（启用）** —— 装上即可调用，
     不再默认停用（要复核的装完自己停用）；
     覆盖安装时 `configs` / `created_by` / `status` 保留目标既有值（配置就是凭据，包里的值是空的）
   - **随包附带的工具**（`_deps/custom_tools/**`）：智能体 / 技能装完再装它们，同样是启用态；
     目标已有同名工具则**不覆盖**（保留本地脚本与配置），缺 `sandbox.exec` / `net.egress` 则跳过，
     全部只进 `warnings`，不让整次安装失败
   - `agent`：`is_supervisor` / `tools_unrestricted` / `skill_discovery` **按包设置**（老资产没这些字段 → 取 `false`；覆盖导入则沿用目标既有值），`default_language` 同样按包设置（BCP-47 字符串，空串表示「未配置」，老资产没有该字段时覆盖导入沿用目标既有值），`status` 默认 1（只接受 1 / 3）
   - 授权重写：`skill_tool_grants` / `agent_tool_grants` 逐条重建；`agent_skill_bindings` 只绑「目标租户里同名且未废弃的租户级技能」，其余丢弃（私有技能天然属于它的 agent，不需要绑定）
   - `agent_relations` **P0 不复制**（它表达「谁能调谁」，跨租户无法重映射）
   - 智能体与工作流共用一个名字空间：目标租户已有同名 workflow 时同样按重名处理（409 + 建议名）
   - 智能体的技能按**名字**匹配（`skills.agent_id IS NULL` 的租户级技能）：缺同名技能、或同名技能已废弃 → 跳过并进 `warnings`；`agent_tool_grants` 按 `{item_type, ref, grant_kind}` 全量替换重建，`grant_kind` 缺失时按 `allow` 补（老资产可能没记这个字段）
5. 所有被丢弃 / 改写的项进 `warnings`（与 `contract.warnings`、目录 `issues` 同一口径）。
6. 写 `shared_asset_installs` 一行。
7. DB 失败 → 删掉第 3 步写入的文件；两步都失败只记日志并报错，不留半残。

响应：`{ok, installed:{name,id}, warnings:[...], skipped_files:[...]}`。

### 6.1 自定义工具的两点不同

1. **契约在文件里**（`TOOL.json`，见 `models.ToolFile`）。所以安装是「先还原文件 → 从
   目标目录把契约读回来 → 用它生成表行」，而不是从 payload 抄字段。改名安装时会把
   契约里的 `name` 改写成新名字再写回，否则 `Validate` 会一直报「name 与目录名不一致」。
2. **默认启用**（`status=1`）：装上即可调用；觉得脚本需要先复核的，装完自己停用即可。
   `configs` 一律为空，配置值必须由使用方自己填（没填时调用会失败，安装时在 `warnings` 里点名）。

能力预检在安装前做，缺能力直接 400（不是警告）：没开 `sandbox.exec` 的工具装上根本跑不起来，
没开 `net.egress` 的带出网工具同理。

## 8. 接口草案

租户侧（`nexus/admin/assets/`，前缀 `/admin/share`）：

| 方法 | 路径 | 作用 |
|---|---|---|
| GET | `/admin/share/assets?kind=&q=` | 可安装列表（listed + 可见） |
| GET | `/admin/share/assets/:id` | 详情（依赖、warnings、文件清单、是否装过） |
| POST | `/admin/share/listings` | 上架 `{kind, ref}` |
| GET | `/admin/share/mine` | 我上架的（含状态、驳回原因） |
| POST | `/admin/share/mine/:id/withdraw` | 撤回（pending→draft）或删除 draft |
| POST | `/admin/share/install` | 安装 |
| GET | `/admin/share/installs` | 已安装记录（含「有新版本」标记） |

平台侧（`nexus/platform/handler/assets.go`，前缀 `/platform/assets`）：

| 方法 | 路径 | 作用 |
|---|---|---|
| GET | `/platform/assets?status=&kind=&q=&page=&page_size=` | 审核列表（分页口径同 `/platform/tenants`：`page` 从 1 起，`page_size<=0` 不分页，上限 100，`total` 是筛选后总数） |
| GET | `/platform/assets/:id` | 详情（payload 预览、文件清单、依赖、warnings、上架人） |
| POST | `/platform/assets/:id/review` | `approve` / `reject` / `delist` |
| DELETE | `/platform/assets/:id` | 彻底删除：`listed` / `delisted` 可直接删，**不要求先下架**；`pending` / `rejected` / `draft` 拒绝 |
| GET/PUT | `/platform/assets/:id/visibility` | `all` / `allowlist` + 白名单增删 |

列表里每行展示 **上架人 张三 · 研发一队**，悬停给 `user_id` 与上架时间；前端由 `listed_by.user.display_name` + `listed_by.tenant.display_name` 拼接，服务端不给拼好的展示串。

控制台入口：顶栏「资产」（`desktop/src/tenant/components/AssetMarket.vue`，接口配置在 `config/admin.js` 的 `share`）。三个 tab 正好对应上面的接口：资产市场（`/assets` 列表 + 详情 + 安装）、我的上架（`/mine` + 撤回 + 上架弹窗）、安装记录（`/installs`）。

## 9. 代码落点（受边界规则约束）

- `nexus/shared/assetpack/`：打包 / 解包 / 依赖解析 / ID 重映射 / schema 限定读写。只依赖 `shared/{db,storage,paths,models,capability,quota}`。
- `nexus/admin/assets/`：租户侧 7 个接口。
- `nexus/platform/handler/assets.go`：审核与可见范围。
- 两侧都调 `assetpack`，互不 import；`platform/README.md` 的「允许依赖」清单要加上 `shared/assetpack`。

## 10. 分期与验收

**P0**：三张表 + `assetpack` + 上述接口 + 两个页面（租户「上架 / 安装」、平台「审核」）。

验收：

1. 发一个技能 → 审核 → B 租户装上：`SKILL.md` 与授权都在，`configs` 为空。
2. 发一个脚本工具 → 装完是**启用态**、`config_fields` 齐全、值需自填。
3. 发一个 agent → 装上后三个开关按包设置、技能绑定按名字匹配、缺依赖有 `warnings`。
4. 重名装第二次 → 409 且给出建议名。
5. 未审核（`pending` / `rejected` / `delisted`）资产不可安装。
6. 快照与安装副本互不影响：删资产后目标租户技能仍可用。

**P1**：快照号 + 「上游有新的快照」（只提示、重新导入，不做 diff 合并）、allowlist 细化、bundle（agent 内联技能/工具）。

**P2**：市场形态（搜索 / 分类 / 评分）、依赖锁文件。

## 11. 明确不做

跨租户引用、双向同步、自动升级覆盖租户改动、跨租户 `agent_relations`、把 `custom_tools.configs` 的**值**带走。

## 12. 跨服务器搬运（手工包，P0）

目标是「测试机上审过的资产，生产机也能装」。做法是**消费方主动拉/收**，不是让远端用户往这边上架：

- 上架永远在**上架方本机**发生（本机租户 + 本机平台审核），跨服务器的动作只有「导出 → 导入」；
- 不允许远端用户直接调 `/admin/share/listings`：`listed_by` 指向本机 `{schema}.users.id`、
  `source_schema` 指向本机 `platform.tenants`，远端账号两者都没有，写进去就是一条查不到上架人的行；
- 不做运行期远程引用：对端停机不能影响本机已导入的资产。

### 12.1 包格式

`platform-assets/<id>/<build>` 快照打成 zip（`archive/zip`，标准库，跨平台）：

```
package.json   { format: "flame-asset/1", exported_at, origin:{server,asset_id,source_schema,build,listed_by},
                 manifest:{kind,name,entry,deps,files[]}, payload:{…} }
files/<rel>    快照里的每个文件（相对路径原样）
```

包里**不含**授权行与 `agent_relations` —— 跨环境无法重映射。但 `manifest.deps.agents` 会记下
「这个智能体可调用谁」的**名字清单**（导出时按 `agent_relations` 取）：授权行不迁移，依赖却要能在
装之前核对（见 §12.5 的 `/import/inspect`）。

智能体的三个开关（`is_supervisor` / `tools_unrestricted` / `skill_discovery`）**在包内**
（`payload` 同名字段，一律写明确布尔值）：它们描述「这个智能体在租户里怎么跑」，不带出去就
会在目标租户装出一个行为不同的副本。导入时按包设置，`warnings` 里会明说「已按包设为超级
智能体」；老版本的包没有这些字段时，覆盖导入沿用目标既有值、新建取 `false`（不能断言成
`false`，否则一次覆盖就静默降权）。

`payload.default_language` 同理：它是智能体定义的一部分（发布时随版本冻结），不带出去就会在
目标租户装出一个「作者配了中文、装上却回英文」的副本。它是 BCP-47 字符串而不是布尔值 ——
空串表示「未配置」；老版本的包整段没有这个字段时，覆盖导入沿用目标既有值。

### 12.2 导入端（不信上游）

`POST /platform/asset-imports`（multipart：`file` / `origin?` / `force?`，仅平台管理员）依次做：

1. 上传上限 128 MiB，解压后按 `assetpack.DefaultLimits`（2000 个 / 单文件 64 MiB / 合计 128 MiB）再卡一次；
2. 路径安全：拒绝绝对路径、`..`、反斜杠、未规范化的 `a/../b`（解压穿越的常见手法）；
3. 清单与内容一一对应：数量、字节数、`sha256` 三项全对，不一致**整包拒绝**（不做部分导入）；
4. `assetpack.ValidateName`：名字必须能落成目录；不再按受保护名拒绝（见 §2 的说明）；
5. **重跑 `StripSecrets`**：不信任上游做过的那一遍（对端版本更老 / 被改过 / 根本没做）；
6. 落 `platform-assets/<本地新 uuid>/<ver>/`，失败即 `DeletePrefix` 回收；
7. 写 `shared_assets` 行：`status = pending`（**对面审过不等于本机放行**）、`visibility = all`、
   `origin_server` / `origin_asset_id` / `imported_at` / `imported_by` 记录溯源。

去重与覆盖：唯一索引升级为 `(origin_server, kind, source_schema, lower(name))`，
本机上架行的 `origin_server = ''` 仍然唯一。导入时按同一键查既有行：

- 没有 → 新 uuid，版本取包里的 `origin.build`（`< 1` 视为 1）；
- 有且未带 `force` → 回 400，`msg` 说明「来源 + 名字 + 当前快照号」，`data.existing` 带
  `{id, build, status}`：由界面弹一次确认，**不要求事先勾复选框**（忘了勾只会白跑一趟）；
- 带 `force` → **复用行 id**（安装记录仍指向同一资产）、版本取 `max(本地版本+1, 包里版本)`
  （本地版本只增不减）、内容替换后回到 `pending`，并提示「已覆盖 / 已升到 vN」。

导入行的 `source_schema` 是「对方那台机器上的租户」，本机同名 schema 纯属巧合，
所以展示时统一用 `origin.server` 代替租户名（`origin` 字段，本机上架的行回 `null`）。

### 12.3 对端拉取（P1，已实现）

把「包的来源」从人工上传换成 HTTP 拉取，校验与落库是**同一份代码**（`importAssetPackageBytes`）。

**生产方出口**（消费方来读，令牌鉴权，与平台登录身份无关）

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/platform/market/catalog?kind=&since=` | 只列 `listed`；`since`（RFC3339）用于增量 |
| GET | `/platform/market/assets/:id/package` | 只对 `listed` 发包，与目录口径一致 |

出口口径两条：只出 `listed`（本机审核通过是对端可见的唯一前提）；只出**本机原创**
（`origin_server = ''`）—— 导入行不再转发，否则 A→B→A 会成环，同一份资产被反复复制，
来源标记也会被改名冒充。包接口对导入行同样 404，否则猜到一个 id 就绕过这层限制。

因为第二条，把对端配成本机（`localhost` 自测）是**允许**的：从自己拉回来的那条
`origin_server` 非空，不会再出现在出口目录里，一轮就收敛，不会自增殖。
若将来放开「转发导入行」，必须同时恢复「对端不能指向本机」的拦截。

令牌：`platform.market_tokens`（`name` + `token_hash` + `enabled` + `last_used_at`），
明文 `flmt_<64hex>` **只在创建时返回一次**，库里只有 `sha256`。吊销即删除或停用；
`/platform/market/*` 全部走 `MarketTokenMiddleware`，拿不到平台管理权限。

**公开市场（可选：不要令牌）**

对端可以不校验令牌 —— 适合内网，或资产本来就打算公开的部署。这个开关在管理端：
「远端市场 → 市场出口令牌」顶部，**不进 config.yaml**（理由见下）：

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/platform/market-settings` | `{open, updated_at?, updated_by?}` |
| PUT | `/platform/market-settings` | `{open}`：改出口策略 |

存 `platform.market_settings`（单行表，`id` 恒为 1）。**没有行 = 关**：忘配置绝不等于
全开，fail-closed 不靠种子数据物化。注意路径是 `market-settings` 而不是
`market/settings` —— `/platform/market/*` 整段由市场令牌鉴权，管理员进去会被 401。

为什么不放配置文件：这是治理决定，改它的人应当是有平台账号的管理员，而不是
「能登上服务器改文件再重启」的人。同一类决定的先例是 `capability_gates` /
`plugin_gates` —— 它们也都在库里，不在 config.yaml。

开了之后 `/platform/market/*` 接受不带 `Authorization` 的请求，目录响应里的 `token`
回 `anonymous`。两条口径要分清：

- **带了令牌但无效**仍然 401 —— 开放不等于不校验。写错的令牌应当在接入时就暴露，
  否则「配了令牌却一直没生效」永远查不到；
- `market_tokens` 不再是唯一入口，`last_used_at` 只覆盖带令牌来的那一部分；
- 中间件每请求点查一次（不做进程内缓存）：多实例部署下缓存会让「刚关掉的开关」还生效一会儿，
  而这个开关是安全相关的一票；`/platform/market/*` 本来就是低频接口。

消费方（`platform.peer_servers.token`）为空串即「对端公开」：出站请求不带 `Authorization`。
接口上用 `auth_mode`（`token` | `none`）表达这个状态，它从 token 推导，不单独落库
（多一列就会出现「方式是 token 但令牌是空的」这种自相矛盾的行）：

- `POST/PUT /platform/peers` 传 `auth_mode: "none"` → 清空令牌。必须显式传，因为 PUT 里
  `token` 留空的既有语义是「不改」，否则切不回公开市场；
- 响应同时回 `auth_mode` 与 `has_token`（后者给老前端）。

**消费方入口**（平台管理员）

| 方法 | 路径 | 说明 |
|---|---|---|
| GET/POST | `/platform/peers` | 对端列表 / 新建（`name`、`base_url`、`auth_mode`、`token`、`install_policy`） |
| PUT/DELETE | `/platform/peers/:id` | 改配置（`token` 留空 = 不改；`auth_mode: none` = 清空）/ 删除 |
| GET | `/platform/peers/:id/catalog` | 代理对端目录，并标注本机是否已导入、本地版本 |
| POST | `/platform/peers/:id/import` | `{asset_id, force}`：拉包 → 走 12.2 的导入管线 |

- 对端表 `platform.peer_servers` 存**出站**令牌明文（出站调用要用），只在管理员接口里以
  `has_token` 布尔回显，不重复回明文；
- `install_policy` 默认 `review`：导入一律 `pending`。设 `auto` 后直接 `listed`，但
  `origin_*` 与 `imported_by` 照记，且 warnings 里会说明「本次为自动通过」；
- 拉取刻意**分两步**（先目录、再逐条导入），不做批量：一次导入一条，warnings 逐条看得见，
  覆盖与否由人决定（`force`）；
- `origin_server` 用对端站点名（`peers.name`），因此同一份资产从两个站点导入会并存两行，
  互不覆盖（唯一键含 `origin_server`）。

**租户侧入口（pull-through，平台不代导入）**

平台只决定「哪个对端对哪些租户可见」（`visible_to` / `tenant_schemas`），不替租户逐条导入：

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/admin/share/remote-assets?peer=&kind=` | 对本租户可见的对端目录；每条带 `local{asset_id,build,status}` / `installed` / `stale` |
| POST | `/admin/share/pull-asset` | `{peer, asset_id, force}`：按需拉包 → 走同一套导入管线 |

- 请求体带的是 peer **名**，不是 URL：地址只来自平台配置，令牌只在服务端，浏览器既不直连对端也拿不到 `flmt_…`；
- 拉取按**资产**去重（`sharedassets.FindByOriginAsset`）：多个租户拉同一份只下载一次，之后各自 `install`；
  `force: true` 跳过快路径（「拉取新版本」用），覆盖判定交给 Import；
- 快路径只认对方 asset id，**同名查重**还要过 `assetimport.FindByOriginName`（来源 + 类型 + 源 schema + 名字）。
  对方「撤回后重新上架」会派生新 id，于是命中同名冲突：这时接口回
  `400 + data.conflict{name,build,status,asset_id}`，前端确认后带 `force: true` 重试
  —— `ConflictError` 的文案只陈述事实（来源 / 名字 / 当前快照号），不写「勾选某复选框」这类
  某个界面才成立的操作指引；租户侧据此自己拼「覆盖」提示；
- 落库状态由对端的 `install_policy` 决定：`auto` → 直接 `listed`，前端拉完直接进安装流程；
  `review` → `pending`，等平台审核通过才出现在「资产市场」；
- 目录 60s 缓存 + 失败也缓存（负缓存），单站不可达只让该站回 error，不影响本机资产与其它对端；
- 前端：租户后台「资产市场 → 远端市场」页签，按对端分组，按钮文案随本地状态变化
  （拉取并安装 / 等平台审核 / 安装 / 拉取新版本）。

### 12.4 租户侧看「这份东西来自哪个市场」

租户的市场永远只有一个（本机 `platform.shared_assets`），远端是货源。接入多个对端后，
区分靠 `origin_server`：

- 列表接口 `GET /admin/share/assets?kind=&q=&origin=`：`origin` 留空 = 全部来源，
  `local` = 只看本机上架，其它值 = 只看该对端站点导入的；
- 响应额外带 `origins: [{server, label, count}]` 汇总（**忽略 origin 条件**，其余条件照旧），
  前端直接渲染「来源」下拉 —— 否则选中 `test` 之后其它来源会从选项里消失；
- 前端文案：有 `listed_by.origin.server` 时显示 **「来源：test 站」**（详情附导入时间），
  不再把远端站点名说成「上架方」；
- 平台审核列表同理（`GET /platform/assets?...&origin=`），资产列表里能一眼分开
  「本机上架」与各站点导入的行。

站点名就是身份：换机器请换名字，或只停用（`enabled=false`）不要删除后复用同名。

### 12.5 租户自建包：卡片导出 / 列表导入（不落平台）

前面几节都需要「先上架、再审核、最后由平台或市场分发」。但很多时候用户只是想**把一个
自己的智能体 / 技能 / 工作流 / 工具拷到另一台机器**，不涉及平台审核、也不该在市场上多出一行。

两个接口专做这件事，四类资产共用：

| 接口 | 说明 |
|---|---|
| `GET /admin/share/export?kind=&ref=` | 现场打包下载 zip（`ref`：skill / agent / workflow 用 id，custom_tool 用名字） |
| `POST /admin/share/import`（multipart：`file` / `name?` / `status?` / `kind?` / `overwrite?`） | 上传 zip，装成本租户实体 |
| `POST /admin/share/import/inspect`（multipart：`file` / `name?` / `kind?`） | **装之前预检（只读）**：解析包并回依赖清单，绝不落地 |

与 12.2 的差别：

- **不落 `platform-assets/`，也不写 `shared_assets`**：导出把内容打到本租户的
  `.export-tmp/<uuid>` 再写成 zip，随打随删；导入把包铺到 `.import-tmp/<uuid>`，装完删掉。
  两边都没有平台资产行，因此不进市场、不进资产列表、也不写安装流水。
- **打包内容与上架同源**：`planFor(kind, info, ref)` 是上架与导出共用的组装逻辑
  （payload 白名单化、依赖声明、`StripSecrets` 脱敏），所以「导出后再导入」与
  「上架后安装」得到同一份东西，不会随时间走偏。
- **校验与安装也同源**：包用 `assetpack.ReadPackage` 完整校验（体积 / 路径 / 清单 /
  sha256），再用 `assetimport.ValidatePackage` 判类型 / 名字 / 入口文件；安装走
  `installFrom`（与市场安装同一个函数），重名冲突、配额、能力预检、默认状态、warnings
  全部一致。
- **几乎「没有不可导出 / 不可上架」的实体**：受保护名（`main` 等）、超权智能体、废弃态都能走
  这两条路，只以 `warnings` 提示；三个开关随包走，`agent_relations` 与 `endpoints` 不随包走。
  唯一的例外是**私有技能**：正文住在 `agents/<agent>/skills/<name>`，离开所属智能体只剩
  「提级成租户级」一种落点（归属永久丢失、可见性白名单不随包走，导回来要么谁都发现不了，
  要么是个与私有技能同名的孤立副本），所以 `export` 与上架都直接拒绝并提示「导出它所属的
  智能体」—— 私有技能随智能体走时是完整的（`payload.private_skills` + 文件树）。
- `kind` 是「用户以为自己在导什么」的断言：在智能体页导入一个技能包会被直接拒绝，
  免得装完了找不到。
- **依赖预检**（`/import/inspect`）：与导入同一套读包校验（同一个 `readUploadedPackage`），
  但只读。回 `deps[]`，每条带 `kind`（skill / tool / agent / capability）、`scope`
  （工具细分：custom / mcp / rpc / local / builtin / plugin / group / preset）、
  `state`（ok / bundled / disabled / deprecated / missing）、`via`（`skill:<名字>` = 技能声明的传递依赖）、
  `items` / `total`（组引用展开出的成员）与 `detail`；另回 `existing`（同名与能否覆盖）、
  `bundled`（随包自带的工具 / 私有技能）、`notes`。
  口径：组引用查 `toolgrant.Catalog.Groups()` 的**组级**状态（能分清「连接不存在」与
  「连接存在但未启用」），单工具查 `ToolsOfRef` 的叶子；随包自带的 `custom:` 记为 `bundled` 不报缺失；
  智能体的被调用目标来自 `manifest.deps.agents`（上架时按 `agent_relations` 记名字，授权行仍不随包走）。
  运行期动态选择（模型临时决定用哪个技能 / 工具）无法静态判定，`notes` 里明确说了。
- **市场安装与上传导入同一套规矩**：
  - 装前预检：`POST /admin/share/install/inspect`（只读，`{asset_id, name?}`）与 `/import/inspect`
    同源同判据 —— 共用 `resolveInstallTarget`，返回 `{kind,name,deps,summary,missing,existing,bundled,notices,notes}`；
  - 同名：默认回 `suggested_name` + `can_overwrite=true`，前端弹一次确认后带 `overwrite=true` 重试；
    覆盖口径见下一条（四类通用）；
  - 发版：勾了「安装成功后立刻发布一个版本」才发（**默认不发**），失败只进安装提示、不回滚安装。
  - 界面同构：两个入口的表单顺序（装成 → 预检 → 安装后状态 → 立刻发版）、预检渲染与结果页
    共用同一组前端组件（`AssetDepCheck` / `AssetInstallResult`），只有「内容来源」不同
    （选文件 vs 选资产）；失败一律内联提示、可原地重试，结果页常驻到点「完成」。

- **同名覆盖（四类都支持）**：`POST /admin/share/import` 另有可选 `overwrite=1`。默认撞名仍回
  `suggested_name`（改名，不动既有实体）；带 `overwrite=1` 时覆盖既有实体。共同口径：
  「身份 / 目标租户的授权与对外暴露配置 / 版本历史」一律保留，只替换包里带来的内容。
  - 智能体：文件整棵重铺（旧版独有文件不残留）、配置 / 私有技能 / 技能绑定 / 工具授权 /
    三个开关按包**全量替换**；唯一保留旧值的是 `endpoints`（对外暴露配置属于目标租户，包里没有）。
    降级「本租户最后一个超级智能体」会被仓储拒绝 —— 那是保护，此时保留原值并给 warning。
  - 技能：文件整棵重铺、`description` / `metadata` / `user_invocable` / `command_*` 按包替换、
    `skill_tool_grants` 按包**全量替换**；`restricted` + `agent_skill_bindings`（可见性授权）与
    `status`（生命周期）**保留目标既有值**，`versions/skills/<name>/` 历史不清。
    目标是「`restricted=true` 且零绑定」时会顺手自愈成全员可见（否则覆盖完谁都发现不了）。
  - 自定义工具：整棵 `custom_tools/<name>/` 先删再铺（旧脚本 / 数据文件不残留）、`TOOL.json`
    契约字段按包替换；`configs`（配置就是凭据）、`created_by`、`status` **保留目标既有值**，
    `versions/tools/<name>/` 历史不清。随包附带的工具仍然「有同名就不装」，永不覆盖。
  - 工作流：只改草稿行（diagram / arguments / outputs / 变量 / 限额 / 描述），`status`、
    `endpoints`、断点与 `flow_versions` **一律保留**；**线上仍跑已发布版本**，warning 里明说
    「要生效需发布新版本」；目标处于废弃态时直接拒绝（`UpdateFlow` 不允许改废弃工作流）。
  - 前端先弹一次确认（按类型给替换范围）再带这个字段重试。
  - **导入后可顺手发一版**：导入对话框有「导入成功后立刻发布一个版本」勾选项，**默认不发**。
    发的是当前草稿（技能 / 智能体 / 工作流 / 自定义工具各自的版本接口），失败不影响导入结果、
    只进提示；停用或废弃的实体发不出去（接口会拒绝并说明原因）。
  - 覆盖时**不清**该实体的版本域（历史属于这个被覆盖的实体自己），只在「新建同名」时清残留。
  - 已知取舍：目标租户里同名的若是**私有技能**，不算冲突（`FindByName` 只查租户级），会新建一个
    租户级技能；而那个 agent 取名时仍优先命中自己的私有那份。
- 导出提示（哪些凭据被清空、随包带走了哪些私有技能）放在响应头 `X-Export-Warnings`
  （每条 URL 编码、`|` 分隔），前端解码后弹框逐条列出。
