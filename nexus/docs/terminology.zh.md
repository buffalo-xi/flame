# 术语表：界面、代码、数据库三处必须同词

中文 | [English](terminology.md)

改代码前先看这张表。三个领域各有一套动作词，**不要跨领域借词**，也不要为了「更顺口」在某一层换说法 ——
之前资产市场就吃过一次亏：界面叫「发布」，端点也叫「发布」，代码里还有第三套 `publish`，谁也说不清指的是哪个。

## 1. 资产市场（跨租户分发）

| 概念 | 界面 | 代码 | 数据库 |
|---|---|---|---|
| 上架（动作） | 上架 | `share.CreateListing`、`listingPlan` | — |
| 上架记录 | 我的资产 | `listing*`、`POST /admin/share/listings` | `platform.shared_assets` 一行 |
| 已上架（状态） | 已上架 | `sharedassets.StatusListed` | `status = 'listed'` |
| 下架（动作） | 下架 | `action: 'delist'` | — |
| 已下架（状态） | 已下架 | `sharedassets.StatusDelisted` | `status = 'delisted'` |
| 上架人 | 提供方 | `Asset.ListedBy/ListedByName` | `listed_by` / `listed_by_name` |
| 上架审核策略 | 上架策略 | `shared/listingpolicy`、`/platform/listing-policy` | `platform.listing_settings` / `listing_policies` |

完整状态机：`draft → pending → listed | rejected → delisted`（撤回是 `pending → draft`）。

### 快照序号 `build`

| 概念 | 界面 | 代码 | 数据库 |
|---|---|---|---|
| 第几次打包上架 | 快照 v2 | `Asset.Build`、`PackageOrigin.Build` | `shared_assets.build` |
| 装的是哪一份快照 | 已安装 v2 | `Install.Build` | `shared_asset_installs.build` |

`build` 是**打包序号**：每次上架产出一份快照目录 `platform-assets/<id>/<build>/`，序号 +1，
与实体内容改过几次无关。它不是实体的版本号（实体修订方案尚未落地，届时另立词，别复用 `build`）。

**别用**：`publish` / `published` / `offline` / `publisher`；资产侧的 `version`
（旧的 `shared_assets.version` 已改名 `build`，见 `nexus/platform/sql/asset_build_rename.sql`）。

## 2. 端点（对外接口）

| 概念 | 界面 | 代码 | 数据库 |
|---|---|---|---|
| 对外开放 | 对外开放 | `Expose`、`SvcEndpoint.Exposed` | `svc_endpoints.exposed` |
| 单个端点切换 | 开关 | `UpdateExposed(bool)` | 同上 |
| 收回对外 | 收回对外 | `Retract`、`RetractByTarget` / `RetractSvcEndpoints` | 同上置 `false` |
| 镜像 JSON 键 | — | `EndpointProtocol.Exposed` | `agents.endpoints` / `flows.endpoints` 里的 `{"exposed":true}` |

镜像列是 `svc_endpoints` 的派生副本，改键名必须两边一起改，否则界面显示「未对外开放」。

**别用**：`publish` / `published` / `unpublish`（端点域已全部改掉）；
`revoke`（留给第 4 节的授权撤销）；`hide`（那是可见性，端点是可调用性）。

## 3. 版本 / 部署（部分落地）

| 概念 | 用词 |
|---|---|
| 把某个版本推到线上 | **发布**（`release` / `deploy`） |
| 线上切到某个历史版本（不动草稿） | **回滚**（`rollback`） |
| 把某个历史版本写回草稿（不动线上） | **载入**（`apply`） |
| 把当前版本从线上摘下来（不动草稿 / 历史 / 端点） | **取消发布**（`unrelease`） |
| 灰度 | 灰度 |

工作流版本已按这套用词落地：`ReleaseFlowVersion` / `ErrNoReleasedVersion`，库里的标记是
`is_current`（唯一指向线上那一版）。删除历史版本（`DeleteFlowVersion`）只清历史、
不动线上；当前版本拒绝删除 —— 停服是另一个动作，不要塞进删除里。

`unrelease` 就是那个独立动作，也是这句话缺的另一半：它只清 `is_current`（版本行、版本目录、
草稿、端点、授权都不动），正式调用随即得到 `ErrNoReleasedVersion` —— 明确的「线上没有版本」
状态。四个域共用：`versions.Unrelease`，上层是 `UnreleaseFlowVersion` /
`UnreleaseAgentVersion` / `UnreleaseSkillVersion` / `UnreleaseToolVersion`。由此有两条结论：

- **删除实体要求线上没有发布版本**：有 `is_current` 时删除被拒（`versions.ErrHasReleasedVersion`，
  带着版本号）—— 与「删版本」同一条规则，只是上升了一层，因为删除不该顺带停服；
- **废弃态可以删**（智能体 / 技能以前要求「先切回正常再删」，那等于为了删除先把实体放回线上）；
  废弃本身已经是明确的停服步骤，所以拦住删除的是发布版本，而不是状态。

### 实体版本（智能体 / 技能 / 自定义工具）

三条与流程不同的规则，改代码前先对齐：

1. **版本号跟着「实体根目录」走，不跟着子目录走。** 私有技能没有独立版本号：它在
   `agents/<a>/skills/**` 里，版本号就是所属 agent 的版本号。技能域里只有**租户级**
   技能有独立版本（`skill_versions`）。
2. **草稿域与版本域平级，不嵌套。** 草稿是 `agents|skills|tools/<name>/`，版本是
   `versions/{agents|skills|tools}/<name>/v<n>/`（租户根下一级，不加点开头 —— 它是正式
   数据，备份要能带走）。放实体目录里会让「草稿目录只装草稿」这句话不成立，快照还会套娃。
3. **授权不冻结，声明冻结。** 版本里冻结的是定义（正文 + 描述 / 可见性 / 工具依赖声明 /
   智能体的 `tools_unrestricted`、`skill_discovery` 两个开关，以及默认回复语言 `default_language`
   —— 它们决定运行时口径，属定义）；
   `agent_tool_grants` / `agent_skill_bindings` / `agent_relations`（调用授权）这类授权永远取当前
   —— `revoke` 必须立即生效，所以 `rollback` 不动它们，`apply` 也不回写它们。
   推论：正式运行读的是**当前版本**里的这些属性（`versioning.AgentCurrentAttributes`），
   不是 `agents` 表里的当前值；载入旧版本会按版本回写这些列。

   自定义工具同理：正式运行的工具清单与契约（`TOOL.json`：参数 / 入口 / 语言 / 超时 / 出网）
   取当前版本（`customtoolstore.ListReleased / Released`），只有 `status` 与 `configs` 取当前表行；
   没有当前版本的工具**不进入正式清单**，也不退回草稿定义。草稿视图只给管理端与编辑页调试用。

   技能同理：正式运行的常驻清单只列当前版本里有的技能（`skillMetaReader.runtimeMeta`），
   与 `read_skill` / `use_skill` 的读取路径、`run_skill_script` 的 `/skill` 挂载同源；
   版本里没有的技能不出现（正文与脚本同样不在版本目录里）。
4. **回滚是纯指针操作，与载入分开。** `rollback` 只把 `is_current` 切到目标版本（线上立刻生效，
   不新增版本、**不动草稿**）；`apply` 把该版本写回草稿（**不动线上**）。四个域同一套语义
   （`RollbackFlowVersion` / `RollbackAgentVersion` / `RollbackSkillVersion` / `RollbackToolVersion`）。
   少了 rollback，回退就只能「载入草稿 → 再发布」，既覆盖正在改的草稿，又凭空多一个版本号。

代码里避免再出现 `Publish`：资产域已改用 `listing`，端点域已改用 `expose`，版本域用 `release` / `deploy`。
例外：工作流试运行（草稿试跑）的 `published` 是调试范围（`draft` / `published`）的对外取值，属既有契约，不改。

## 4. 授权 / 凭证（保持不动）

| 概念 | 用词 |
|---|---|
| 撤销某条工具或调用授权 | **revoke**（`revoke_agent_tool` / `revoke_agent_call`） |
| 吊销市场令牌 | **revoke** |

`revoke` 只用于授权与凭证，不要拿来表示「收回端点对外」。

## 5. 变量（三个作用域，别混）

同一个词根、不同 scope：`<scope>_vars`。三个都叫「…变量」，作用域从左到右收窄。

| 概念 | 界面 | 代码 / 表 | 作用域 | 运行期可变 |
|---|---|---|---|---|
| 全局变量 | 全局变量 | `global_vars` / `shared/globalvars` / 节点输入 `source_type: "global"` / 文档 `${global_vars.X}` | 整个租户 | 否（运行快照） |
| 环境变量 | 环境变量 | `flows.env_vars` / `EnvVars` / 节点输入 `source_type: "env"`；type 只有 string / number / secret | 单个流程 | 否（启动快照） |
| 流程变量 | 流程变量 | `flows.flow_vars` / `FlowVars` | 单个流程 | 是（节点写） |

- **全局 = 本租户内全局，不跨租户**。每个租户一个 schema，值永远不出租户。
- 资产（工作流 / 智能体 / 技能）导出时只带**引用名**，不带值：工作流在节点
  `config.inputs` 里写 `{source_type: "global", source_key: "变量名"}`、模板里写
  `{{ .input.变量名 }}`；定义域文档（`AGENT.md` / `SKILL.md`）写 `${global_vars.变量名}`。
- **两处已下线**，别再写：模板级的 `{{.global_vars.X}}`（流程载入即报错，见
  `executors.rejectLegacyGlobalRefs`）、环境变量指向全局变量的 `{type: "global", ...}`
  （运行期报「已废弃的类型 global」，见 `core.GetEnvVar`）。全局变量与环境变量现在**平级**，
  都是节点输入变量的来源，谁也不借道谁。
- 引用取不到时**硬失败**，错误带变量名；不要退化成空串 —— 空串会让「没配置也能跑」
  变成线上现实，排查成本远高于直接报错。
- `secret` 类型的值只在运行期取，接口一律掩码；不要写进 `AGENT.md` / `SKILL.md`
  （会进模型上下文与运行记录）。
- 存量资产包里若还有 `{type: "global"}`，`assetpack.StripSecrets` 会**保留**它的值并记一条
  迁移提示（清空就看不出原本指向谁了）。这是兼容存量，不是支持该写法。

**别用**：`tenant_variables`（作用域已由 schema 表达）、`shared_vars`（`share` 专指跨租户分发，
会撞词）、`vars` 作为表名（与 `env_vars` / `flow_vars` 的词根对不齐）。

## 6. 生效范围

- 新增文案、接口、表：按上表取词；
- 存量标识符若与上表冲突，以本表为准，改的时候连带迁移脚本一起提；
- 状态值（`'listed'` 等）是 DB CHECK 约束的一部分，改它必须同时改
  `nexus/platform/sql/shared_assets.sql`、迁移脚本与前后端。

## 7. 注释与诊断文案的用词

代码注释、内部错误串、`msg` 诊断文本与日志一律**英文**（2026-11 决定）。**用户可见文案不在此列**：它由 `error`
码 + 前端语言包渲染，后端的那一句永远不决定中文用户还是英文用户读到什么。

注释里仍要守住上面的动作词：`list` / `delist`、`expose` / `retract`、`release` / `deploy` / `rollback` / `apply`、
`revoke`。不要译成 `publish` / `hide`；注释中出现的代码片段、SQL、路径、标识符也不要翻译 —— 那些是引用，不是叙述。
