# 目录分层与 超级智能体权限方案（nexus）

> 把「定义域」和「运行域」拆开，引入用户 / 会话目录；在此基础上给超级智能体
> 开放跨 agent / skill 的超级权限，闸门口径为「目标处于编辑态（`status == 1`）」。
>
> **当前状态：阶段 1、阶段 2、阶段 3 均已落地并生效** —— 目录已迁移（`workspace/` → `agents/`、
> `shared-skills/` → `skills/`），迁移脚本执行完毕并已从代码中摘除；`gate.go`（`/` 租户根寻址 +
> 状态闸）、`IsMainAgent`、元工具的 `skill_id` 定向定位都已实现。管理类工具进一步收口为
> `CategorySuper`（天然组 `builtin:super`），只对 超级智能体产出（见 D18）；其后按 D19 把 skill 的
> 「建 / 改 / 删」拆成两套工具：私有 `*_own_skill`（人人可用，但**不进缺省集**，见 D20）与共享
> `*_shared_skill`（仅超级智能体）；随后按 D20 把「技能自建」三件套移出缺省工具集，改由管理员按 agent 授权。
> 阶段 3 原计划里的几项延伸未做，清单见 §7；待定项见 §6。
>
> **D30（后续变更）**：身份判据已从 `IsMainAgent(name)` 改为实体属性 `agents.is_supervisor`
> —— `main` 只是建租户时预置的那个 supervisor，租户可以再配出同等权限的 agent。
> 迁移脚本见 `sql/agent_supervisor.sql`；下文凡写「超级智能体专属」的段落，判据一律按 `agents.is_supervisor` 属性读。

---

## 1. 背景

两条主线，**必须按顺序做**（反过来 `Context.Workspace` 会被改两次，白干一轮）：

1. **目录分层**（地基）：拆开「定义（AGENT.md / 私有技能）」与「运行（干活时的产物）」，并引入用户 / 会话目录
2. **超级智能体权限**：让 超级智能体能改任何 agent / skill，闸门 = 目标处于编辑态

### 1.1 改造前的痛点

一个目录扛了两个职责：

```
tenants/{schema}/workspace/{agent}/
├── AGENT.md                ← 定义：属于 agent，跨用户、跨会话共用
├── skills/{skill}/SKILL.md ← 定义：同上
└── 各种产物 / 临时文件       ← 运行：属于「某一次会话」，跟一次执行同生命周期
```

后果（都已实际发生或可复现）：

- 用户 A 与用户 B 用同一个 agent → **互相看到对方的文件**（串台）
- 上一次会话的产物还在原地 → **跨会话污染**
- 无法「清掉某一次会话的东西」
- 超级智能体的权限（改别人的**定义**）与「在自己的场地干活」共用同一个路径空间，闸门口径混在一起
- 超级智能体改别的 agent 只能「删了重建」，重建后 **ID 会变**

### 1.2 改造前的路径事实

| 项 | 位置 / 值 |
|---|---|
| 存储根 | `agent.root_dir`（local / nfs / s3 三种 Storage 实现） |
| 定义目录 | `tenants/{schema}/workspace/{agent}/`、`tenants/{schema}/shared-skills/{skill}/` |
| 文件工具根 | `Context.StoragePath(rel)` = `tenants/{schema}/workspace/{me}/{rel}` |
| exec 沙箱 | `Mounts: [{Host: Context.Workspace, Guest: "/data"}]`，cwd = `/data` |
| `Context.Workspace` | `ResolveWorkspace(rootDir, schema, name)` 的**宿主绝对路径** |
| session | **只有 DB 行，没有目录**；且 `session_id` **没有传进运行时** |

---

## 2. 已定稿的决策

| # | 决策 | 备注 |
|---|---|---|
| D1 | 超级智能体可修改**任何**其他 agent / skill 的文件与属性 | 原始诉求 |
| D2 | 闸门：目标必须 `status == 1`（正常），废弃(3) 拒绝 | 判据统一走 `models.IsModifiable`，勿再写字面量 `status == 1` |
| D3 | 超级智能体 **不能改 status** | 否则能自我解锁，D2 形同虚设；状态切换只走管理端 |
| D4 | 读不受限，只有**写**被闸 | 废弃的也能读 |
| D5 | ~~超级智能体不能改自己的工具授权~~ **已修订**：超级智能体 **可以改自己的工具授权与开关**（它就是管理员）。边界改由 `status` 承担 —— 废弃(3) 的 agent（含 超级智能体自己）一律拒写，而**改 status 仍只有管理端能做**（D3），故不可能自我解锁。仅保留一条自改禁令：**不能给自己改名**（本轮后续文件操作会失效，技术约束而非权限）。自改后置脏 `Context.MarkToolsDirty()`，本 run 下一轮即重算工具集，不必等下次会话 | 旧口径的「防自我扩权」挡不住真正的威胁（提示注入的目标正是让它改自己），却把「给超级智能体配个自定义工具验证」变成必须找管理员。放开后边际风险很小：超级智能体本就带 `builtin:exec`（沙箱内任意命令），工具面的增量远小于数据域/沙箱边界 |
| D6 | 文件工具按**实体目录**判定（阶段 3 修订）：实体目录**内部**的任意文件/子目录由 超级智能体可写（过状态闸）；实体的**创建/删除/改名/元数据**走元工具 | 取代原「整棵 `skills/` 只读」；防孤儿的边界从"文件类型"移到"实体存在性"——写不存在的实体 → 拒绝 |
| D7 | 改名：`workspace/` → `agents/`，`shared-skills/` → `skills/` | |
| D8 | 新增 `users/{uid}/files/` | 用户上传件 / 跨会话保留产物 |
| D9 | 新增 `users/{uid}/sessions/{sid}/`，**不带 agent 维度** | `call_agent` 子 agent 共用同一场地；flow session 可复用 |
| D10 | agent **不能写自己的定义域** | 定义只走元工具。**技能子目录已由 D23 收窄**；`AGENT.md` 仍归元工具（**读**已由 D32 放开） |
| D11 | exec 挂载：`/workspace` ← 运行域工作目录（rw、cwd）；`/agent` ← 自己的定义域（ro） | 沙箱内逻辑路径，映射源为已算好的宿主路径 |
| D12 | 超级智能体的 exec **不挂 `/tenant`** | 会绕过 D2 闸门，且黑盒 shell 有越权读整租户风险 |
| D13 | 删 session 即删目录（`DeletePrefix`）；`Create` 后建目录（S3 写 `.keep`） | 配额 / 保留期以后再说 |
| D14 | 老数据**不迁移** | 存量产物只有 `workspace/poet/sfds`（空文件），删掉即可 → 无需定 `uid` |
| D15 | 路径：仅 超级智能体支持 `/` 前缀 = 从**租户根**算（三域命名空间） | 普通路径语义不变，向后兼容 |
| D16 | `main`、`skill-authoring`、`agent-authoring` 属**删除/改名保护名单**：不可删除、不可改名（其它字段可改） | 与 D2 的 status 闸门**正交叠加**；实现为 `models.IsProtectedAgent` / `models.IsProtectedSkill` |
| D17 | 身份判据 `IsMainAgent` 与删除/改名保护名单 `IsProtected*` **解耦** | 前者唯一、驱动超权与缺省工具；后者可多元、只管删/改名。现取值同为 `{main}`，代码拆为 `mainAgents` / `protectedAgents` |
| D18 | 管理类工具（`create/update/delete_agent` + 技能绑定类 `bind_skill` / `unbind_skill`）收口为**超级智能体专属**：新建分类 `CategorySuper`（天然组 `builtin:super`），并从 `builtin:agent` 里移出 | 两级收口：授权目录按身份不产出（非超级智能体连选都选不到，API 也拒收）+ `ExecuteTool` 执行层硬闸。删除冗余的 `preset:agent-manage` / `preset:skill-manage`（成员与 `builtin:super` 完全重合） |
| D19 | skill 的「建 / 改 / 删」**按归属拆成两套工具**：私有 `create/update/delete_own_skill`（分类 `skill`，人人可用，只能动自己的私有）+ 共享 `create/update/delete_shared_skill`（分类 `skill-admin`，仅超级智能体）；工具名即归属，**下线** `scope` 参数与 `owner` 定向 | 分类回答"谁能用哪个工具"，空间归属另由 `skillManager.checkSkillScope` 收口，二者正交、缺一不可。`*_own_skill` 属普通工具（分类 `skill`，人人可用），但**不进**缺省集（见 D20）。旧 ref 改名由 `sql/skill_own_tool_rename.sql` 迁移 |
| D20 | 「技能自建」三件套（`create/update/delete_own_skill`）**移出缺省工具集**：新 agent 默认不带，改由管理员按 agent 显式授权（授权时物化为 `required`） | 自建私有 skill 会把 agent 自己的提示词变成可写面 —— 写进去的 `name` / `description` 下次调用即常驻 system prompt，被输入诱导即形成**持久化注入**。`required` 恒注入、不被 skill 依赖收窄；代价是要收回就整条删（`revoke_agent_tool`，或 D29 的 `deny` 覆盖），不能只收窄其中一部分 |
| D21 | 超级智能体可给**任意** agent（含自己）授权工具：`grant_agent_tool` / `revoke_agent_tool`（分类 `tool-grant`）+ `update_agent(…, unrestricted)`；**写入方式（`required` / `allow`）由系统自动分配，不让模型选**；`deny` 与显式 `kind` 由 D29 增补；不做全量替换 | 目标是让超级智能体接管「工具配置」，前提是目标 `status == 1`（含自己 —— 改 status 只有管理端能做，故不会自我解锁，见 D3 / D5 修订）。自动分配而非模型选：模型看不到各 skill 的 `tool_deps`（在 `skill_tool_grants` 表里，不在 SKILL.md 正文），猜错即「写了 allow 却永不生效」或「本该按需注入却常驻上下文」。口径与 `Resolver` 同源（两边都展开成**叶子工具 ref** 求交）：有 skill 声明 → `allow`（按需注入、省上下文）；没有 → `required`（`allow` 只是候选过滤器，无候选即空转）。代价：`allow` 的生效挂在 skill 绑定关系上，解绑后会**静默失效** —— 故 `read_agent` 回显每条授权的生效状态与来源 skill，把不确定性变可见 |
| D22 | 超级智能体也可管**调用授权**与**租户级 skill 可见性**：`allow_agent_call` / `revoke_agent_call`（分类 `tool-grant`）+ `update_shared_skill(…, restricted)`；调用授权**允许超级智能体给自己加出边**；不做全量替换形态 | 调用授权就是 `agent_relations` 里的一行 —— `caller.go` 对所有 agent 一视同仁（**无行即拒绝，超级智能体无豁免**），所以「超级智能体能调用子 agent」必须真的写行，否则造完 agent 也调不通。允许超级智能体给自己加出边：它扩的是「超级智能体能调谁」，与 D5 说的工具面是两件事；反向（target = 超级智能体）在工具层拒（`caller.go` 硬拒超级智能体被调用）。skill 可见性只加**开关**：白名单就是 `agent_skill_bindings`，由 `bind_skill` / `unbind_skill` 维护，故**不能**用 `SetVisibility` 代劳（它会清空重建白名单）—— 新增只改一列的 `SetRestricted`；共享 skill 建出来默认就是收窄的（`restricted=true` + 白名单含创建者），所以 `create_shared_skill` 不需要该参数 |

| D23 | **非超级智能体的根寻址（自有技能目录）**：编辑态下可用 `/` 前缀访问**自己的私有 skill 目录** `agents/{self}/skills/{skill}/**` —— 读任意文件，写 / 改 / 删任意文件与子目录；白名单外一切拒绝。`AGENT.md` 与其它实体仍不可达（D10 不变）。读与写**共用同一条白名单**，但读不查库、不设状态闸（D4），写要过「自身 `status == 1`」+「skill 已登记且 `status == 1`」双重闸 | 动机：旧口径把私有 skill 当「单文件知识文档」，非超级智能体改自己的技能只能改 `SKILL.md` 正文（`update_own_skill`）；而 超级智能体与管理端（`admin/skills/files.go`）早就能管理整个技能目录。开这一格是为了把三者口径对齐。实现：`gate.go` 的 `checkReadOwn` / `checkWriteOwn`，解析与判权经 `parseRootAddr` 拆开（写侧不复用读侧，避免报错文案串口径）。**刻意不是 opt-in**：`write_file` / `edit_file` / `delete_file` 本就在缺省工具集里，唯一开关是 agent 的 `status`。顺带把 `SKILL.md` 的**删除**保护补齐到文件工具（与管理端 `isSkillEntry` 同口径）—— 删掉入口文件会留下「登记在库、没有正文」的空技能，而运行时读不到正文会静默降级为空内容。**归属 agent 状态闸**：写 agent 私有 skill 时，除 skill 自身状态外还要判**归属 agent** 是否正常态 —— 废弃 agent 名下的私有 skill 整片冻结（读不受影响，D4）。四处同口径：`checkWriteOwn`（判自身）、`checkWrite`（超级智能体档判归属者）、`skillManager.checkOwnerAgent`（元工具三件套）、管理端（`admin/skills` 的建 / 改 / 删 / 可见性 / 状态切换 + `files.go` 三个写接口） |

| ~~D24~~ | **已撤销**：原决策新增 `agents.locked_skill_edit`（锁定态是否仍允许编辑该 agent 名下的私有技能）。agent / skill 已取消「锁定(2)」态（三态 → 两态：1 正常 / 3 废弃），该开关随之失去语义并整体下线：列、`models.SkillWritableByAgentStatus` / `CanToggleLockedSkillEdit`、`PUT /admin/agents/:id/locked-skill-edit` 全部删除。现在「归属 agent 能否写它名下的私有 skill」只剩一条判据 —— `models.IsModifiable(status)`（只有正常态），四处判据点（`checkWriteOwn` / `checkWrite` / `checkOwnerAgent` / `ensureAgentModifiable`）仍必须同口径 | 撤销理由与工作流一致：线上冻结已由版本模型表达（改草稿不影响线上，发布才生效），锁定原本要防的「改坏线上」不会发生；剩下的效果只有「禁止改定义」，于是出问题必须先解锁才能改，解锁那一刻定义又可改 —— 反而把风险让了回来。迁移见 `nexus/sql/agents_skills_drop_locked_status.sql` |

| D25 | **超级智能体的工具集与其它 agent 完全同源**：常驻 = `agent_tool_grants` 里的 `required` 行。core 集由 `models.SupervisorCoreGrantRefs`（6 个**组**，展开 19 个工具）定义，`CreateAgent`（超级智能体分支）物化、`sql/main_core_tool_grants.sql` 回填存量。原「代码里全开 + 授权表只回答额外给了什么」删除 | 动机：超级智能体手里有什么只有代码知道，库里查不到、也没法按租户调整。用**组**而非单工具：组是原子的，误删一整类看得见；单工具行删错一个（如 `read_file`）系统不报错，只是静默少个工具 —— 这个坑实际踩过。**保险丝**：超级智能体**零授权行**、或**还没物化 core 集**（`MainCoreMigrationPending`：不含 `builtin:agent-admin` 组行）时回落全开并记 Error（**2026-11：迁移完成后该判据已删除，只剩零授权行一条**）。运行时与管理端预览共用同一条判据 |
| D26 | `browser` **不常驻**，需要浏览器的租户显式授 `required`（不是 `allow` —— 它不跟随任何技能，allow 会空转） | 四条理由见 §2.4。它不进基线，谁要用谁自己加（超级智能体也能给自己加，D5 修订）；但加进去就是 `required` 常驻，14 action 的大 schema 会一直占预算，所以建议按租户需求临时加、用完 `revoke_agent_tool` 收回，而不是默认留在 core 里 |
| D27 | 管理面从单个 `CategorySuper` 拆成四组：`agent-admin` / `skill-admin` / `tool-grant` / `custom-tool`；`supervisorOnlyCategories` 是「仅超级智能体」的唯一判据；`CategorySuper` 保留为 `builtin:super` 的兼容展开（四组并集），待存量清空后删 | 单组没法做「按需授权」—— 要么 19 个全给，要么全不给。拆开后 `agent-admin` 常驻（自举：能建 agent 才能拉起别的工作面），其余三组跟随技能按需激活 |
| D28 | **「用过即注入」**：`use_skill` 成功后，该技能声明的工具依赖在本 run 的后续轮次生效（`Context.LoadedSkills` + `refreshToolDefs`）；run 结束随 Context 消失。**绑定只决定清单可见性，不产生工具候选**；`read_skill` 退化为纯查看。三个 `*-authoring` 技能**不常驻绑定**给超级智能体 | 技能依赖原本只认「已绑定」（跨 run 持久），`read_skill` 只拉正文不带工具 —— 于是要么永久挂着 17 个工具（省不了上下文），要么让模型 `bind_skill`/`unbind_skill` 两步（同一 run 内还断链，因为工具集只在 run 开始算一次）。若只把「已绑定」换成「读过」，又会把「只想看文档」变成激活（大 schema 占预算、撞 50 上限、扩大误选面）。拆成 read / use 两个工具后：查看与激活解耦，正文与工具都只在本轮存在，跨 run 清零。不构成提权：候选恒被 `allow` 收窄 |

| D29 | `grant_agent_tool` 增加可选 `kind`（`required` / `allow` / `deny`），省略时仍由系统自动判定；`revoke_agent_tool` 回显被删行的 `kind`；`read_agent` 的 `deny` 行不再被误标成 pending | D21 的「不让模型选」解决的是默认路径，但留了两个洞：① 没有 `deny` 入口 ——「技能声明了但我不要」只能走管理端，而按需口径下 `allow` 要 `use_skill` 才亮，`deny` 是唯一能连这条路一起堵死的刹车；② 自动判定在「有 skill 声明」时会把本该常驻的基础工具降级成 `allow`（如 `read_file`），agent 连读文件都要先 `use_skill`。故显式 `required` 用于钉住常驻、显式 `deny` 用作刹车，`allow` 仍不许硬写（没有声明者直接拒绝，避免一行永不生效的死授权）。每个 ref 只留一行（`UpsertAgentGrant` 按 ref 覆盖），改 `kind` 即覆盖旧行，返回值回显「旧行是什么」—— 否则调用方会误以为 `deny` 是叠加。不构成提权：`tool-grant` 组仍是 supervisor-only，且 `deny` 是收权 |

| D30 | 身份判据从**名字**改成**属性**：`agents.is_supervisor`（`sql/agent_supervisor.sql`）。**删除** `models.IsMainAgent(name)`，改为实体字段判定（`tools.Context.IsSupervisor`、各 manager 构造时固化的 `isSupervisor`）；`main` 只是建租户时预置的那个 supervisor，租户可再配多个。对外字段同步改名：`ToolInfo.MainOnly` → `SupervisorOnly`（JSON `main_only` → `supervisor_only`），`toolgrant.NewCatalog(..., isSupervisor)` | 名字判据把「谁是超权 agent」钉死成唯一的 `main`，既不能改名也不能多实例。属性化之后：① 授予走 `update_agent(is_supervisor=…)`（分类 `agent-admin`，仅 supervisor 可用）与管理端 `PUT /admin/agents/:id` 的同一字段；② **降级本租户最后一个 supervisor 被仓储拒绝** —— `SetSupervisor` 在同一事务里 `SELECT … FOR UPDATE` 锁住全部 supervisor 行再计数，防并发两个降级同时通过，否则租户永久失去管理面；此外 `main` 作为**预置资产永不降级**（`models.CanDemoteSupervisor`）—— 它不可改名、不可删除，降级会留下一个占着固定入口却没有权限的死角，无法恢复；③ 删除保护仍然只看名字名单（`IsProtectedAgent`，只含 `main`）—— 它保护的是「预置资产删了没有恢复路径」，与超权正交，新建的 supervisor 可删可改名 |
| D31 | supervisor 属性**不写进** `UpdateAgent` 的全字段覆盖，而是单列 `SetSupervisor`（仓储）+ 管理端 `PUT /admin/agents/:id` 的显式分支 + 元工具 `update_agent(is_supervisor=…)` | 全字段覆盖式 UPDATE 一旦带上这一列，任何「只改描述」的调用方忘了回填就会把超权静默清零。属性变更必须是显式单列操作（与 `SetUnrestricted` 同一做法）。目标须处于正常态（废弃不接受属性变更），与 D2 同口径；改的是自己时置脏 `Context.MarkToolsDirty()`，本轮下一轮即重算工具集 |
| D32 | 非超级智能体的**读**闸从「仅私有 skill 目录」放宽到「自己的定义目录 `agents/{self}/**`」（含 `AGENT.md` 与附属文件）；**写闸不变**（仍只有 `agents/{self}/skills/{skill}/**`） | 读写共用一条白名单的后果是自相矛盾：能改 `skills/x/SKILL.md`（那也是提示词）却读不到自己的 `AGENT.md`。读写不对称的正确理由不是"防提示词自改"，而是**归因** —— 写 `AGENT.md` 是身份变更，必须由第三方（人或 supervisor）做出，否则「这份草稿是谁产生的」无法回答，版本模型里「禁止自发布」的前提就没了；读没有这个问题，还能让 agent 自述 / 诊断时说清自己用的是哪套规则。实现只在 `checkReadOwn` 加一条 `kind == "agent" && name == c.AgentName`，`checkWriteOwn` 与 `resolveWrite` 一行未动（`TestResolveWriteOwnAgentMDStillDenied` 守住） |

### 2.1 D12 的理由（为什么不给 超级智能体的 exec 挂 `/tenant`）

1. **重复能力**：超级智能体走文件工具（Storage 层）已能读写三个域，且**带闸门**
2. **可写 `/tenant` 会废掉 D2**：一句 `echo ... > /tenant/agents/poet/AGENT.md` 就绕过 `IsModifiable()`
3. **只读 `/tenant` 仍有越权读风险**：超级智能体由 LLM 驱动，用户 A 的提示词可诱导它 `cat` 用户 B 的会话文件；exec 是黑盒 shell，一条 `find` 就能扫全租户
4. **真实需求是「在自己的场地跑命令」**：`/data` 已覆盖

将来若确有需求，按次最小化开放：`exec(command="npm run build", agent="poet")` → 只额外挂 `agents/poet`（超级智能体专享 + `status == 1` 校验）。

### 2.2 D18 的理由（管理类工具只给超级智能体）

「创建 / 修改 / 删除 agent」与「把技能绑定给谁用」是系统基石，只应归 超级智能体（skill 本体见 §2.3）。
实现为**三层**，缺一不可：

1. **归属收口（分类）**：`create/update/delete_agent`、共享 skill 的 `create/update/delete_shared_skill`
   与技能绑定类（`bind_skill` / `unbind_skill`）标 `Category = CategorySuper`，天然组成 `builtin:super` 组，
   并从 `builtin:agent` / `builtin:skill` 里移出 —— 它们只在自己的专属组里出现。
2. **授权目录按身份生成**：`toolgrant.Catalog` 带 `isSupervisor` 视角，非超级智能体时 `builtin:super` 整组不产出
   （成员被过滤光的自定义 / 预设组也一并消失）。授权页里**根本选不到**。`SetAgentToolGrants` 用同口径
   目录做存在性校验 → 非超级智能体提交这些 ref 直接 `ref not found or has no tools` **拒收**。
3. **执行层硬闸（纵深防御）**：`ExecuteTool` 对 `IsSupervisorOnlyTool(name) && !IsMainAgent(c.AgentName)`
   一律拒绝 —— 即便授权被误配或历史数据残留，非超级智能体也执行不了。

**skill 侧是例外**：`agent-authoring` / `skill-authoring` 需要**声明**这些工具作为依赖，而 skill 依赖
只是「声明需要」不是授权。故 `Skills.vue` 的依赖挑选器保持全量可见，仅给这些工具打「仅超级智能体」徽标提示
「绑给非超级智能体也不会生效」。

### 2.3 D19 的理由（skill 的建改删按归属拆成两套工具）

「让某个 agent 用上一份技能」有两条路：替它建**私有** skill，或建**共享** skill 再 `bind_skill` 绑定。
只有后者是对的 —— 绑定关系是"谁在用"的唯一真源，可随时解绑、可在 `read_agent` 里看到现状；
替别人建私有 skill 会让归属与使用权脱钩，绑定体系也就废了。由此：

1. **共享 skill 只归超级智能体**（`*_shared_skill` 标 `CategorySuper`）→ 直接复用 D18 的三层收口，零新增闸门；
2. **私有 skill 开放给每个 agent 自建**（`*_own_skill` 标 `CategorySkill`）。分类只回答"谁能用哪个工具"，
   "只能动哪个空间"另由 `skillManager.checkSkillScope` 收口：own 只能动**自己的私有**、shared 只能动**租户级**。
   二者正交 —— own 系列人人可用，而开启「可发现」的 agent 能"看见"共享 skill，若没有这道校验，
   非超级智能体就能借可见口径改到共享 skill；
3. **拆成两个工具而不是加 `scope` 参数**：工具名即口径，拿错工具会被**明确拒绝**并提示该用哪个；
   `scope` 是隐式默认，模型漏传就静默建到另一个空间（改动前的 `create_skill` 正是这个形状）。
   近似同名工具间的误选风险用四层压制：工具名（`own` / `shared` 对称）、工具描述、
   `skill-authoring` 里的判定表、旧参数字段的显式报错；
4. **`*_own_skill` 不进 `DefaultAgentTools`**（D20）：自建私有 skill 等于把自己的提示词变成可写面，
   默认不给；确实需要自建的 agent 由管理员在「工具配置」里显式授权，授权时物化为 `required`
   —— 选 `required` 是为了避开 `allow` 被 skill 依赖收窄的坑（否则 agent 一绑上声明了工具依赖的技能，
   自建能力就莫名消失）。代价：`deny` 对 `required` 无效，要收回得在管理端做**全量替换**。

改名代价：工具名是授权 ref 的组成部分，存量的 `builtin:{create,update,delete}_skill`
（`agent_tool_grants` / `skill_tool_grants` / `tool_group_items` 三张表）由
`sql/skill_own_tool_rename.sql` 一并改写，否则会变成悬空引用、工具静默消失。

---

### 2.4 D25 / D26 的理由（超级智能体的工具从「代码特权」搬进「授权表」）

**改造前**：`buildToolDefs` 在无授权时把超级智能体兜到 `toolgrant.BuiltinTools()`（37 个全量），
`resolveGrantedTools` 里还有一段「超级智能体**始终**拥有全部内置工具，授权表只回答额外还给了什么」。
后果是三条：

1. **不可审计**：`select * from agent_tool_grants where agent_id = <超级智能体的 id>` 只看到 11 条旧缺省种子，
   与它真实的 37 个工具毫无关系；
2. **不可调整**：想给某个租户的超级智能体去掉 `shell`，只能改代码发版；
3. **语义分裂**：同一个概念（能用的工具）对超级智能体与对其它 agent 是两套机制。

**改造后**：超级智能体的常驻集 = `models.SupervisorCoreGrantRefs` 物化出的 `required` **组**行（6 组 / 20 个工具）：

| 组 ref | 成员 | 常驻 | 说明 |
|---|---|---|---|
| `builtin:file` | 6 | ✅ | 读写 / 列 / 删 / 存在性 |
| `builtin:exec` | `exec`、`run_skill_script` | ✅ | 一次性进程 |
| `builtin:shell` | `shell` | ✅ | 常驻会话，schema 仅 3 参数 |
| `builtin:agent` | `list_agents`、`read_agent`、`call_agent` | ✅ | 只读 + 协作 |
| `builtin:skill` | `list_skills`、`read_skill`、`use_skill` | ✅ | 看 / 用技能：`read_skill` 给正文 + `tool_deps`（不激活），`use_skill` 才激活依赖 |
| `builtin:agent-admin` | 建 / 改 / 删 agent + `bind_skill` / `unbind_skill`（5） | ✅ | **超级智能体的自举能力** |
| `builtin:skill-own` | `*_own_skill`（3） | ❌ | 跟随 `skill-authoring` |
| `builtin:skill-admin` | `*_shared_skill` + `*_agent_skill`（6） | ❌ | 跟随 `skill-authoring` / `agent-authoring` |
| `builtin:tool-grant` | `grant/revoke_agent_tool`、`allow/revoke_agent_call`、`search_tools`（5） | ❌ | 跟随 `agent-authoring` |
| `builtin:custom-tool` | `*_custom_tool`（4） | ❌ | 跟随 `custom-tool-authoring` |
| `builtin:flow-admin` | `create/list/read/delete_flow`（4） | ❌ | 跟随 `flow-authoring` |
| `builtin:browser` | `browser`（1） | ❌ | 不跟随任何技能（见下） |

**为什么是组而不是逐个工具**：组是原子的 —— 误删一个 = 少一整类，面板上一眼能看出来；
而 33 条单工具行里删掉 `read_file`，系统**不报任何错**，只是静默少一个工具（这是实际踩过的坑）。
代价是组内新增工具自动进 core，这正是「core = 系统能力基线」该有的语义。

**不在 core 的两类**：

- `browser`（D26）：① 它自己写着「能用 exec 拿到内容就不要开」，而工具列表里的存在是**最强**的使用暗示；② schema 全场最大（14 action / 18 参数）；③ 唯一「出网 + 读任意页面 + 带登录态（cookie 跨会话保留）」的组合；④ 与系统自身口径不一致 —— `DefaultAgentTools` 已认定浏览器不是标配。
  **注意它不能用 `allow` 按需**：`allow` 只是候选的过滤器，而候选来自「本轮已激活技能声明的依赖」，
  browser 不跟随任何技能 ⇒ 候选恒空 ⇒ allow 空转（`resolve.go` 的 `Degraded`）。
  要就加一条 `required`（超级智能体自己就能加，D5 修订），用完删掉。
- `*_own_skill`：超级智能体用 `*_shared_skill` / `*_agent_skill`，私有 skill 对全局管理者没有意义（与 D20 同源）。

**按需组怎么生效（「用过即注入」）**：候选来源 = **本轮用 `use_skill` 激活的技能**（run 内有效）。
绑定只决定技能出现在 system prompt 的清单里（那一行 name + 描述），不产生任何候选。

```
use_skill("custom-tool-authoring")     → 正文进上下文 + 记进 Context.LoadedSkills
                                       → 下一次 LLM 调用时重算工具集
                                       → custom-tool 的 4 个工具出现在 schema 里
  ...干活...
（run 结束，工具与正文一起消失）

read_skill("custom-tool-authoring")    → 正文 + tool_deps（只展示），不登记、工具一律不出现
```

关键实现：

| 位置 | 作用 |
|---|---|
| `tools.Context.MarkSkillLoaded` | `use_skill` 成功后登记 + 置脏，**同时带上依赖声明快照**（`read_skill` 是纯查看，不登记） |
| `agentImpl.refreshToolDefs` | 重算 `toolDefs` **与** `AllowedTools`（两者必须同源） |
| `toolgrant.Resolver.Resolve` | 候选优先用 `VisibleSkill.Deps`（激活时登记的声明快照）；nil 才回落查 `skill_tool_grants` |
| `Run` 循环顶部 | `ConsumeSkillsDirty()` 为真才重算 —— 没激活过技能就零额外开销 |

三条边界：

1. **只在 run 内有效**：跨 run 重置（`core.RecordsToMessages` 只回填 user/assistant 文本，工具往返不进历史），
   所以不会「以后所有轮次都带着」。这比 A 方案（`bind_skill` 写库、永久生效）省得多。
2. **上限不变**：候选恒被 `allow` 收窄，激活技能只能点亮管理员已授权的组，点不亮授权外的东西。
3. **查看 ≠ 激活**：`read_skill` 返回正文 + `tool_deps`（依赖清单，**只展示**），任何工具都不会因此出现；
   要工具必须 `use_skill`（返回同样的正文与 `tool_deps` + 登记激活）。展示依赖不构成提权：
   激活结果恒被 `allow` 收窄，看到 `builtin:exec` 也点不亮未授权的东西。
4. **绑定 ≠ 候选**：绑定只决定技能出现在 system prompt 的清单里（name + 描述这条「注释」），
   不产生工具候选 —— 否则绑几个 authoring 技能就等于把它们那 17 个工具常驻进上下文。

生效链路缺一环就静默不生效：`required` 无此组 → `allow` 有此组 → 某个**本轮已激活**的技能声明了它
→ 候选 ∩ allow。`read_agent` 的 `effective` / `pending` / `via_skill` 可查每条授权到底有没有点亮、
以及要 `use_skill` 哪个技能才会亮。

**正式运行（`DefinitionMode=current`）下这条链路里的「技能声明了哪些依赖」取当前版本的冻结快照**
（`skillMetaReader`，与常驻清单 / `read_skill` 同一实现）：候选只认快照，`grant_agent_tool` 的自动
kind 与 `read_agent` 的 `via_skill` 也按它判 —— 草稿里加/删一条依赖，不会让线上多亮或少亮工具。
展示（`tool_deps`）与生效同源，正是「冻 metadata 也冻 deps」这条改造的全部意义。

> **2026-11 更新**：存量租户迁移已完成，`MainCoreMigrationMarker` / `MainCoreMigrationPending`
> 已从运行时（`agent_impl.resolveGrantedTools`）与管理端预览中删除，`onlyLegacyDefaultGrants`
> 以及预览里「请执行 sql/main_core_tool_grants.sql」的告警一并移除。
> 现在只剩「**零授权行** ⇒ 回落全开」这一条保险丝（自举安全，长期保留）。
> 下面关于单组标志的论证保留为历史记录 —— 它记录了当初为什么那样选。

**保险丝（I1）**：超级智能体零授权行 ⇒ 回落全开。这不是遗留妥协，而是**自举安全** ——
租户模板 `tenant_template.sql` 里没有 agent 行，超级智能体是事后由管理端创建的；漏跑回填脚本时，
让超级智能体失能等于整租户失去创建 / 授权能力，比短暂越权更难恢复（它本来就是全权身份）。
判据只有一条：`models.MainCoreMigrationPending(refs)` 为真，即超级智能体**没有 `builtin:agent-admin` 这一组行**。

为什么用单组标志而不是「有没有任意 core 组」或「数够 6 条」：

- 数条数：管理员可以合法删掉某一组（例如不要 shell），一删就「不达标」，判据失效；
- 任意 core 组：`builtin:file` / `builtin:skill` 在旧口径下可能被单独授过，会误判成已迁移，
  于是按表解析、超级智能体丢掉全部管理能力；
- `agent-admin`：旧口径下**任何路径都不会**给超级智能体单授这一组（那时管理类是 `CategorySuper` 一个整体，
  管理端对超级智能体隐藏内置类，`preset:default` 与 `DefaultAgentTools` 都不含管理类）。

实测存量 t0 的超级智能体正是「只有一条 `plugin:web_search`」—— 恰好是前两种判据都会漏掉的形状。

**取舍：`deny` 对超级智能体的 core 不再生效。** core 行是 `required`，而 `required` 的通用语义就是
「恒注入、deny 不动」（见 `toolgrant/resolve.go`）。要收回整类就**删掉那一组行** ——
收回动作也留在数据里，而不是藏在一个反方向的授权行里。
`sql/main_core_tool_grants.sql` 因此用单组标志判「是否已迁移」，**不会**把你删掉的组加回来。

**上线顺序（必须遵守）**：
1. `sql/main_core_tool_grants.sql` —— 清掉旧逐工具 core 行、落 6 个组行
2. `sql/supervisor_on_demand_groups.sql` —— 超级智能体的 5 条 `allow`、四个技能的 `skill_tool_grants`
3. 发代码（分类拆分：`CategorySuper` → 五个 `*-admin` 组 + `supervisorOnlyCategories`）
4. 重启

**验收**：

| 项 | 判据 |
|---|---|
| 库内 | 超级智能体的 `required` = 6 个 core 组；`allow` = 5 个按需组 |
| 技能 | 四个 `*-authoring` 各有 `skill_tool_grants`；超级智能体由起步套件绑定它们 |
| 运行时 | 日志无「回落缺省全开」；超级智能体的 schema 里没有 `browser` 与五个按需组的工具 |
| 按需 | `bind_skill` 后对应工具出现，`unbind_skill` 后消失 |
| 保险丝 | 清空超级智能体的授权行 → 仍全开 + 一条 Error |
| 可逆 | 删掉回填的行 + revert 代码 ⇒ 回到改造前 |

**`CategorySuper` 的去向**：拆成 `agent-admin` / `skill-admin` / `tool-grant` / `custom-tool` / `flow-admin` 五组后，
没有任何工具归属 `super`；它只作为 `builtin:super` 的**兼容展开入口**保留（等价五组并集），
让存量授权行继续解析。等存量清干净后连同 `tools.CategorySuper` 一起删。

---

## 3. 目标目录结构

```
{root_dir}/tenants/{schema}/
├── agents/{agent}/                  定义域（原 workspace/）
│   ├── AGENT.md
│   └── skills/{skill}/SKILL.md        私有技能
├── skills/{skill}/SKILL.md          定义域（原 shared-skills/）
├── users/{user_id}/                 运行域
│   ├── files/                         用户上传件 / 跨会话产物
│   └── sessions/{session_id}/         ← 文件工具默认根 = exec cwd
└── runs/{run_id}/                   无 session 的临时场地（见 §6 T1；有 user 时优先 users/{uid}/runs/{run_id}/）
```

### 3.1 迁移对照表

| 现在 | 改后 |
|---|---|
| `tenants/t0/workspace/main/` | `tenants/t0/agents/main/` |
| `tenants/t0/workspace/joker/` | `tenants/t0/agents/joker/` |
| `tenants/t0/workspace/poet/` | `tenants/t0/agents/poet/` |
| `tenants/t0/workspace/poet/sfds` | **删除**（空文件） |
| `tenants/t0/shared-skills/agent-authoring/` | `tenants/t0/skills/agent-authoring/` |
| `tenants/t0/shared-skills/english/` | `tenants/t0/skills/english/` |
| `tenants/t0/shared-skills/skill-authoring/` | `tenants/t0/skills/skill-authoring/` |

迁移**不做手工搬文件**，写成启动时执行的**幂等**函数（`agents/` 已有内容 → 跳过；否则 `Rename`），local / nfs / s3 三种 Storage 都能走通。

> **已于 2026-10-11 执行完毕**：`tenants/t0/` 下 `workspace/` → `agents/`、`shared-skills/` → `skills/`，三个 agent 的 `AGENT.md` 与私有 skills 完好。迁移实现（跨租户遍历 + 幂等改名）执行后已从代码中摘除，重启后端不再有任何迁移动作；如需复用见 git 历史。

---

## 4. 工具语义与权限矩阵

### 4.1 三域

| 域 | 路径 | 读 | 写 | 闸门 |
|---|---|---|---|---|
| **运行域** | `users/{uid}/sessions/{sid}/`（默认根，无需前缀） | 本会话 | 随便写 | 无 |
| **用户域** | `/users/{uid}/files/` | 本人 / 超级智能体 | 本人 / 超级智能体 | 无 |
| **定义域** | `/agents/{name}/`、`/skills/{name}/` | 所有 agent | **仅 超级智能体**（例外见下一行） | `status == 1` |
| **定义域 · 自有技能**（D23） | `/agents/{self}/skills/{skill}/**` | 超级智能体 + 本人 | 超级智能体 + 本人 | 写：自身 `status == 1` + skill `status == 1`；读：无闸 |

### 4.2 工具映射

| 工具 | 改前 | 改后 |
|---|---|---|
| `read_file` / `write_file` / `edit_file` / `list_files` / `delete_file` / `file_exists` | 根 = `workspace/{me}` | 根 = 运行域工作目录（session / runs）；`/` 前缀 = 租户根（仅 超级智能体） |
| `exec` | 挂 `workspace/{me}` → `/data` | 挂运行域 → `/workspace`(rw, cwd)；定义域 → `/agent`(ro) |
| `read_agent` / `read_skill` / `list_agents` / `list_skills` | `WorkspaceKey` | 指 `agents/` / `skills/`；`list_skills` 加 `all`（超级智能体专享） |
| `create_agent` / `update_agent` / `delete_agent` | 写 `workspace/{name}/` | 写 `agents/{name}/` |

> 「工作区」一词专指**运行域工作目录**（映射进沙箱的 `/workspace`），不再指 agent 定义域。
> 前端 `Agents.vue` 左侧那棵树改名为「定义」；「工作区」面板改读会话目录。

`call_agent` 另受两重约束（与写闸门分开）：① 废弃的 agent 不可被调用（`IsRunnable`）；② `agentrelations` 授权表无记录即拒绝。supervisor 不在这两类排除里 —— 它可以被其它 agent / flow 调起，被调起时仍按 `resolveDefinitionMode` 恒读草稿（管理面能力不因调用者打折）。

### 4.3 超级智能体的跨域写法示例

```
write_file("/agents/poet/AGENT.md", ...)            → 要求 poet.status == 1
edit_file("/skills/report-format/SKILL.md", ...)    → 要求该 skill.status == 1
read_file("/users/B/files/a.txt")                    → 读不受限
exec("npm run build")                                → 在自己 session 的 /workspace 里跑
```

### 4.4 非超级智能体的根寻址（D23）

```
write_file("/agents/joker/skills/report/docs/spec.md", ...)  → ✅ 自己的私有 skill 附属文件
                                                                （skill 须 status == 1；自己也须 status == 1）
read_file("/agents/joker/skills/report/SKILL.md")             → ✅ 读不设状态闸
list_files("/agents/joker/skills/report")                     → ✅ 列自己技能的目录
write_file("/agents/poet/AGENT.md", ...)                      → ❌ 别人的定义域
write_file("/agents/joker/AGENT.md", ...)                     → ❌ 本次范围不含 AGENT.md（归元工具）
write_file("/agents/joker/skills/unknown/a.md", ...)          → ❌ 未登记的 skill，提示用 create_own_skill
write_file("/skills/report-format/SKILL.md", ...)             → ❌ 租户级共享 skill（仅超级智能体）
delete_file("/agents/joker/skills/report/SKILL.md")           → ❌ 入口文件不许单独删除（D23）
```

> 不带 `/` 前缀写 `agents/joker/skills/...` 会被 `entityLikePath` 拦下，并把上面这行写法
> 作为指引回给模型 —— 否则它会以为「已创建」，在工作区留下假实体目录。

---

## 5. exec / 沙箱挂载

```go
Mounts: []sandbox.Mount{
    {Host: workDir,   Guest: "/workspace", ReadOnly: false}, // 干活场地（session 或 runs），cwd
    {Host: agentRoot, Guest: "/agent",     ReadOnly: true },  // 自己的定义域，只读
}
```

`workDir` 有 session 时是 `users/{uid}/sessions/{sid}/`，无 session 时是 `users/{uid}/runs/{run_id}/`（见 T1）。
`exec.go` 不需要区分两者 —— 它只认 `Context.Workspace` 这个已算好的宿主绝对路径。

注意事项：

- `Mount.ReadOnly` 字段已存在（`tools/sandbox/sandbox.go`），但 **`none` 隔离是把沙箱路径反向翻译回宿主路径**，只读靠工具层自律；`nsjail` / `docker` 才是真只读
- `Mount.Host` 是**宿主路径**，sandboxd 可能跑在别的机器 / 容器 → **临时目录不能用 `os.MkdirTemp`**，必须落在 `{root_dir}` 树下
- `Context.StoragePath` 里「必须落在本租户目录内」的 panic 断言**保留** → 跨租户依然不可达
- `nsjail` 的 rootfs 需**预先存在 `/workspace` 目录**（bind mount 目标必须已存在；`/tmp` 之所以到处能用就是因为它总在）
- exec 的 `cwd` 仅经 `safePath`，目前**不支持 `/` 租户根前缀** → 超级智能体也没有越权 shell 通道

---

## 6. 待定项

| # | 状态 | 结论 |
|---|---|---|
| **T1** | ✅ 已定 | 无 session 时用 `users/{uid}/runs/{run_id}/`（每次调用一个，**不写 sessions / session_records**）；调用结束即删，启动时兜底清扫。连 user 都没有才退租户级 `runs/{run_id}/` |
| **T2** | ✅ 已定 | 接。flow 的 `ExecContext` 加 `UserID`，agent 节点共用 flow 的 `session_id` 与 user |
| **T3** | 待定 | 特权工具是否写审计日志（谁在何时改了谁的什么）—— 建议加 |
| **T4** | 待定 | 超级智能体的 exec 按次最小化挂载（如 `exec(agent="poet")` 只挂该定义域）—— 将来有需求再说 |

### 6.1 各入口的 `session_id` / `user_id` 可得性

**结论：所有入口都能拿到 user**（RPC / MCP / flow 的 `AuthInfo` 由 apikey / oauth 绑定解析而来，`ID` 即绑定用户）。

| 入口 | `session_id` | `user_id` |
|---|---|---|
| `admin/agents/chat.go`（对话） | 有（会新建 / 校验） | 有（`ai.ID`） |
| `service/rpc/server`（RPC 端点调用） | 无（调用方传 `params._meta[session_id]`，**只认这一处**） | 有（`authInfo.ID`） |
| `service/mcp/server`（MCP 端点调用） | 无（同上） | 有（`authInfo.ID`） |
| `service/flow/executors/rpc_target.go`（工作流里的 agent 节点） | 有（flow 的 `ExecContext.SessionID`） | 有（`authInfo.ID`） |
| `service/agent/caller.go`（`call_agent` 子 agent） | 透传父 | 透传父（**可能空**） |
| `service/websocket/handler.go`（后台调试） | 有（`ExecContext.SessionID`） | 有（`s.authInfo.ID`） |

`session_id` 是**开关**：传了 = 有会话（可续、有记录、产物留在 `sessions/{sid}/`）；不传 = 无状态（`runs/{run_id}/`，不留痕）。
协议端点只认 `params._meta[session_id]`：`arguments.session_id` 不再被当作会话开关（旧客户端这样传会变成
无状态调用），该键仍会被 `StripSessionID` 剥掉，不会漏进提示词。
`agent_impl.go` 对 `session_id` 强制 UUID 校验（它会拼进存储路径），**非法直接报错**，不静默降级到
`runs/`：入口层已校验过，走到这里还不合法就是内部 bug，悄悄换路径的代价是产物落进一个跑完就删的目录。

RPC / MCP 的 `user_id` 是**恒有**的：`verifyAPIKey` / `verifyOAuth` 只放行绑定了用户的凭证，
`authorizeBinding` 里对空 `UserID` 直接拒绝（`svc_auth_bindings.user_id` 本身是 `uuid NOT NULL`，
这一条是防御性硬校验）。所以这两个入口不存在「租户级 / 无主」调用，入口侧也不再写
`if authInfo.ID != ""` 的兜底分支。

### 6.2 会话记忆的分线模型（排查必读）

**读写只有一份实现，在 agent 层**：`agent_impl.go` 的 `loadSessionHistory`（读）与
`recordSessionRound`（写，`defer` 在 `Stream` 里，成功/失败/被终止都写）。入口只负责三件事：
传 `session_id`、传 `user_id`、注入 `_run_type`（`call` / `rpc` / `mcp` / `agent` / `flow`）。
控制台聊天不再自己读历史，`admin/agents/chat.go` 只传 `_user_message`（喂模型，含附件清单）
与 `_record_input`（落库，用户原话）。

**写入条件**：`session_id` 与 `user_id` 都非空才写；两者缺一即视为一次性 run，不留记录。
保留口径：读最近 50 条且合计不超过 256 KiB（`trimHistoryByBudget`），写保留最近 100 条。

**一个 `session_id` 下可以有多条线**。`session_records` 的归属键是
`(owner_type, owner_id, session_id)`，而 `sessions` 那一行只记**创建者**（`owner_*`），
两者不是一回事：

| 写入方 | `owner_type` | `owner_id` | 触发条件 |
|---|---|---|---|
| 当前 agent | `agent` | 该 agent id | 正常一轮结束 |
| `call_agent` 子 agent | `agent` | 子 agent id | 父会话透传（`caller.go`） |
| flow 里的 agent 节点 | `agent` | 节点配置的 agent id | `rpc_target.go` 透传同一 sid |
| flow 自身 | `flow` | 该 flow id | 该 flow 的 `max_session_records > 0` |

因此「控制台历史少了几轮」通常不是丢数据，而是**读的是哪条线**的问题：

- 模型上下文（`loadSessionHistory`）读 `owner = 当前 agent + 自己 id` 那条线 —— 子 agent 的
  中间轮次、flow 自己的记录都不进上下文；
- 控制台 `GET /admin/sessions/:id/messages` 读的是**会话行 owner 那条线**（`admin/agents/session.go`），
  同样看不到其它线；
- **flow 的会话记忆默认关闭**：`flows.max_session_records = -1` → `ResolveMaxSessionRecords()` 返回 0，
  `persistSessionMemory` 直接 return。所以多数租户里 `owner_type='flow'` 的记录根本不存在，
  排查时先确认这个开关，再怀疑数据丢失。

**归属与访问控制是两件事**：`sessions.owner_*` 只回答「谁建的」（展示/审计），访问控制是
`VisibleToUser(user_id)`。所以 RPC / MCP / flow 可以复用同一用户下由别的入口建的会话行
（`ResolveOrCreateForUser` 命中已存在的行时原样返回，不改 owner），这是「同一场地接力」的设计，
不是越权。控制台的会话列表仍按 `owner_type='agent' + owner_id` 查（`ListSessions`），
所以 `owner_type='flow'` 的会话不会出现在那里 —— 这是列表口径，不是权限问题。

排查 SQL（某租户 schema 内）：

```sql
-- 这个会话有几条线、各写了几轮
SELECT owner_type, owner_id, count(*) AS rounds, min(created_at), max(created_at)
  FROM <schema>.session_records
 WHERE session_id = '<sid>'
 GROUP BY owner_type, owner_id
 ORDER BY min(created_at);

-- 全租户范围内「一个会话被多条线写过」的情况（默认应为空或极少）
SELECT session_id, array_agg(DISTINCT owner_type) AS lines, count(*) AS records
  FROM <schema>.session_records
 GROUP BY session_id
HAVING count(DISTINCT owner_type) > 1
 ORDER BY records DESC
 LIMIT 20;
```

**落库正文的形态**：控制台是用户原话（`_record_input`，喂模型的那条含附件清单走 `_user_message`）；
协议端点（RPC / MCP / flow）没有「一句话」这个概念，正文是业务参数的 JSON。历史回放与模型上下文
都经 `core.RecordUserContent` 读 `input.content`，所以两种形态在历史里都会原样出现，这是符合预期的，
不要按「形态不同」去修。

**会话数配额**：上限按 `(owner_type, user_id)` 分桶（用户覆盖值 → 租户值 → 系统行，
**fallback `-1`（不限）**，要限制就在系统行 / 租户行 / `user_quotas` 显式配正数），
两条路径处理不同：

- **控制台**（`sessions.Create`，即 `admin/agents` 的建会话 / 首条消息 / 上传自动建）：超限直接返回 507，
  由人手工删除旧会话 —— 聊天记录不替用户做决定；
- **协议端点**（`sessions.CreateWithID`，RPC / MCP 自带新 `session_id` 时）：超限自动回收该桶内
  `updated_at` 最旧的会话（连带 `session_records`、工作区目录、用量计数），腾出位置再建，并记日志
  `会话数达到上限，自动回收最旧会话 ... evicted=N`。端点凭证通常绑定独立账号，不会挤掉控制台会话；
  回收后仍超限（并发抢位、上限为 0）才返回 `-32000` / 507。

---

## 7. 分期实施清单

### 阶段 1 — 地基（改名 + 常量 + 迁移）✅ 已完成

| 文件 | 改动 |
|---|---|
| `shared/paths/paths.go` | 常量改名（`AgentsSubdir` / `TenantSkillsSubdir` / `AgentSkillsSubdir`）+ 新增运行域常量（`UsersSubdir` / `FilesSubdir` / `SessionsSubdir` / `RunsSubdir`）；新增 `AgentsKey` / `UserFilesKey` / `UserSessionKey` / `RunsKey`；新增 `SafePathEx(p, allowTenantRoot)` |
| `shared/tenants/tenants.go` | 新增 `ListSchemas()`（枚举租户；本包原本只有 `Get` / `IsActive` / `Invalidate`） |
| 启动迁移 | 曾以 `agent.MigrateLayouts()` 挂在 `cmd/main.go`，**执行完毕后已摘除** |
| `admin/agents/files.go`、`admin/skills/files.go`、`service/agent/tools/{meta,tool}.go`、`service/agent/agent_impl.go` | **无需改代码**：路径全部经常量计算，改常量值即全局生效 |
| `service/agent/skills.go`、`service/agent/tools/paths.go`（当时叫 `workspace.go`） | 仅路径示例注释同步 |

### 阶段 2 — session 管道 ✅ 已完成

| 文件 | 改动 |
|---|---|
| `shared/paths/paths.go` | 新增 `UserRunsKey` + `ResolveUserSessionDir` / `ResolveUserRunsDir` / `ResolveRunsDir` |
| `service/agent/tools/tool.go` | `Context` 加 `SessionID` / `RunID` / `WorkKey` / `AgentRoot`；`StoragePath` 换根到运行域；新增 `AgentPath`（定义域读取，供 `AGENT.md`）；`CallRequest` 加 `SessionID` |
| `service/agent/agent_impl.go` | 取 `session_id`（强制 UUID 校验）/ `run_id`；算 `Workspace` / `AgentRoot`；`loadSystemPrompt` 改走 `AgentPath`；`Close` 清理临时 runs 目录 |
| `admin/agents/chat.go` | `NewAgent(..., {"user_id", "session_id", "run_id"})` |
| `service/rpc/server`、`service/mcp/server` | 传 `authInfo.ID` 作 `user_id`；`input.session_id` 显式透传 |
| `flow/core/execContext.go`、`flow/executors/runner.go` | `ExecContext.UserID` + `NewFlow(..., userID, ...)`；`ensureSession` 传 `execCtx.UserID` |
| `service/flow/executors/{rpc_target,rpc}.go`、`service/websocket/handler.go` | `NewFlow` 调用点补 `authInfo.ID`；`runEndpoint` 透传 `AuthInfo` + `sessionID` |
| `service/agent/caller.go`、`tools/meta.go` | `call_agent` 透传父 `session_id` |
| `service/agent/tools/exec.go` | 挂载表 `{workDir → /workspace(rw,cwd)}` + `{agentRoot → /agent(ro)}` |
| `shared/repository/sessions/postgres.go` | `Create` / `EnsureExists` 建目录、`Delete` 删目录 |
| `service/agent/janitor.go` | **新文件**：启动兜底清扫 `runs/` |
| `admin/agents/files.go` + `admin.go` | 新增会话工作区浏览接口 `/admin/agents/:id/session/{files,file}`（只读） |
| `desktop` | `Workbench.vue` 实现「工作区」面板；`Agents.vue` 标题改「定义」；`store/sessionContext.js` 共享会话上下文 |
| 工作流脚本池 | **无需改**：sandboxd 的 `/v1/code`（旧名 `/v1/script`、更早 `/v1/session`）只挂它自己的 `workers_dir` / `tmp_dir`，与 agent 数据目录无关 |

### 阶段 3 — 超级智能体权限 ✅ 已完成（延伸项见下）

| 文件 | 改动 |
|---|---|
| `shared/models/protected.go` | `IsMainAgent` / `IsProtectedAgent` / `IsProtectedSkill`（身份判据与保护名单解耦，见 D17） |
| `service/agent/tools/gate.go` | **新文件**：`resolveTarget`（路径 → 实体目录的纯字符串解析）+ `checkWrite`（身份为超级智能体 + 非实体根 + 目标存在且 `IsModifiable`；写 agent 私有 skill 时另判**归属 agent** 是否编辑态）+ `resolveRead` / `resolveWrite`（`SafePathEx` + 跨域写过闸） |
| `service/agent/tools/file.go` | 6 个文件工具接 `resolveRead` / `resolveWrite`；跨出自己工作区的写走闸门；`delete_file` 一律递归删目录（不加参数，边界仍由 `resolveWrite` 的实体根 / 状态闸决定） |
| `service/agent/tools/tool.go` | `SkillRef{ID, Name, Scope}`（D19 后：`Scope` = 空间定向，agent=自己的私有 / tenant=租户级 / 空=可见口径）；`Context.AgentPath` / `TenantPath` |
| `service/agent/tools/meta.go` | 私有 / 共享两套 skill 工具（`*_own_skill` / `*_shared_skill`，工具名即归属，见 D19）；`read_skill` 支持 `skill_id` 定向（仅超级智能体）；技能绑定类 `bind_skill` / `unbind_skill`（追加 / 移除一个，幂等）—— 目标 agent 须 `status == 1`，只接受租户级 skill；`update_agent` 增加 `discovery` 开关；`read_agent` 顺带返回该 agent 绑定的技能名 |
| `service/agent/skills.go` | `findRef`（ID → `Scope` 空间定向 → 可见口径）+ `checkSkillScope`（own 只能动自己的私有 / shared 只能动租户级）+ `IsMainAgent` 判据 + `IsModifiable()` 闸 + `IsProtectedSkill`（禁删 / 禁改名）；租户级 / 私有 skill 双落点；`BindAgentSkill` / `UnbindAgentSkill` / `ListAgentSkills` / `SetAgentSkillDiscovery`（skill 绑定白名单读写） |
| `service/agent/tools/meta.go`（D21 追加） | `grant_agent_tool` / `revoke_agent_tool`（分类 super，目标须 `status == 1`，**可以是自己** —— 改自己时置脏 `Context.MarkToolsDirty()`，D5 修订）；`read_agent` 顺带返回 `tool_grants`（每条带 `effective` / `via_skill` / `note`）、`unrestricted` 与 `discovery`（两者直接取 agent 实例字段）；`update_agent` 增加 `unrestricted` 开关（走 `Agents.SetUnrestricted`） |
| `service/agent/toolgrants.go` + `shared/repository/toolgrants`（D21 新增） | `toolGrantManager`：ref 校验（`toolgrant.Catalog`，非超级智能体目标自动拒收 `builtin:super`）+ **allow / required 自动分配**（按目标已注入 skill 是否声明该依赖，展开成叶子工具 ref 求交）+ 生效状态回显（复用 `Resolver`）；仓储增 `UpsertAgentGrant`（按 ref 先删后插，保证一个 ref 一行）/ `DeleteAgentGrant`（精确删，返回行数）。「不限制工具」开关（`agents.tools_unrestricted`）**是 agent 自身属性**，读写归 `shared/repository/agents`（`Get/SetUnrestricted`）、写入口走 `tools.AgentManager.SetUnrestricted`；`toolgrant.Resolver` 由调用方传入该值，不再自行查库 |
| `service/agent/tools/meta.go`（D22 追加） | `allow_agent_call` / `revoke_agent_call`（分类 super：调用方须 `status == 1`、被调用方非超级智能体 / 非废弃 / 非自身）；`read_agent` 顺带返回 `can_call` / `called_by`；`update_shared_skill` 增 `restricted` 开关；`read_skill` 回填 `restricted` + `bound_agents` |
| `service/agent/tools/tool.go` + `skills.go`（D28 追加） | `SkillDetail.ToolDeps` + `skillManager.fillToolDeps`：`read_skill` / `use_skill` 回显该技能声明的工具依赖（原样 ref，不解组、不剔悬空项；恒为数组）。只展示不激活，非超级智能体也照常给（激活恒被 `allow` 收窄）；读仓库失败降级为空数组，不影响正文 |
| `service/agent/relations.go` + `shared/repository/agentrelations`（D22 新增） | `agentRelationsManager`：`AllowCall` / `RevokeCall` / `ListRelations`；仓储增 `AddRelation`（`ON CONFLICT DO NOTHING`）/ `RemoveRelation`（返回行数）—— 单条操作**不能**用 `ReplaceByCaller` / `ReplaceByCallee` 代劳（全量替换会连带清掉别人的边） |
| `shared/repository/skills`（D22 新增） | `SetRestricted`：只改 `skills.restricted` 一列。**不用 `SetVisibility`** —— 它会在事务里清空并重建 `agent_skill_bindings`，只切开关会把 `bind_skill` 建的白名单全删掉 |
| `service/agent/toolgrant/catalog.go` | **管理类工具收口（D18 / D19）**：新建 `CategorySuper`（天然组 `builtin:super`），从 `builtin:agent` / `builtin:skill` 移出；`Catalog` 带 `isMain` 视角按身份过滤；删除冗余的 `preset:agent-manage` / `preset:skill-manage` |
| `service/agent/tools/tool.go` + `registry.go` | `IsSupervisorOnlyTool(name)` + `ExecuteTool` 执行层硬闸（分类 super 且非超级智能体 → 拒绝） |
| `shared/models/models.go` + `sql/agent_default_tool_grants.sql` | 缺省工具集**不含** `*_own_skill`（12 条，物化为 `required`，见 D20；D28 修订后含 `use_skill` —— 否则普通 agent 无法激活绑定技能的工具）；回填脚本同口径 |
| `sql/skill_own_tool_rename.sql` | **新文件**：把三张表里的 `builtin:{create,update,delete}_skill` 改名为 `*_own_skill`（工具名是 ref 的一部分，不改写会悬空） |
| `data/tenants/t0/...` | 提示词真源同步：`skill-authoring`（两套工具表 + 判定表）、`agent-authoring`（缺省工具与权限边界）、`main/AGENT.md`（工具清单与操作规程） |
| `admin/toolgrants/handlers.go` | `ListToolCatalog?agent_id=` 按身份生成目录；`SetAgentToolGrants` / `GetAgentEffectiveTools` 同口径 |
| `desktop` | `Agents.vue` 目录按 agent 拉取（非超级智能体不见 `builtin:super`）；`Skills.vue` 依赖挑选器保留全量并打「仅超级智能体」徽标 |
| 文档 | `agent-authoring` / `skill-authoring` SKILL.md + `main` 的 `AGENT.md`，改写「只能删了重建」「文件工具只在自己的工作区」 |

**延伸项（原计划里未做，按需再开）**：

- `update_agent` 支持 `agent_md`（改 AGENT.md 正文）—— 目前改正文走文件工具 `write_file("/agents/<name>/AGENT.md", ...)`
- `list_skills` 的 `all`（超级智能体专享全量列举）
- `set_agent_relations`（**全量替换双向**形态）：已按 D22 落为成对单条的 `allow_agent_call` / `revoke_agent_call`
- `set_agent_toolgrants`（**全量替换**形态）：已按 D21 落为**成对单条**的 `grant_agent_tool` / `revoke_agent_tool`，不再做全量替换（LLM 全量替换少写一项就等于静默撤销一条授权）

### 阶段 4 — 非超级智能体的自有技能目录（D23）✅ 已完成

| 文件 | 改动 |
|---|---|
| `shared/paths/paths.go` | `SafePathEx` 第二参改名 `allowRootAddr`、重写语义注释（只管语法，权限下沉到闸门）；新增 `SkillEntryFile` 常量，`SkillPath` / `AgentSkillPath` 改用它 |
| `service/agent/tools/gate.go` | `checkReadOwn`（读闸：超级智能体不受限（D4），非超级智能体仅自己的私有 skill 目录）+ `checkWriteOwn`（写闸：形态 / 归属 → 非实体根 → 自身状态闸 → `AgentID` 非空 → skill 存在且属于自己 → skill 状态闸）；`parseRootAddr` 把「解析」与「判权」拆开（写侧不再复用读侧，避免把读口径的报错发给写调用）；`entityLikePath` 增 `selfName` 参数，对自己的定义域给出可执行的根寻址写法 |
| `service/agent/tools/file.go` | `delete_file` 增加 `SKILL.md` 入口文件保护（根寻址下拒绝单独删除），与管理端 `isSkillEntry` 对齐；6 个工具的路径描述改为「超级智能体可及全租户 / 非超级智能体仅自己的私有 skill 目录」 |
| `service/agent/skills.go` | ① `checkOwnerAgent` 去掉「自身免校验」：锁定态 agent 仍可运行（`IsRunnable` 只拦废弃），跳过自身等于让被锁定的 agent 继续改自己的私有 skill，且会与 `checkWriteOwn` 得出相反结论。② **改名改为整目录搬迁**（`storage.Rename`，与 `paths.RenameAgentDir` 同源）：原先是「写新 `SKILL.md` + 删旧 `SKILL.md`」，技能目录一旦有附属文件就会在旧目录留下孤儿 —— 这条在本阶段之前不可达（写不出附属文件），放开后就成了真 bug |
| `admin/skills/admin.go` | 同上：管理端的改名也是「写新入口文件 + 删旧入口文件」，同步改为整目录搬迁（同口径，避免两条入口行为分歧） |
| `service/agent/tools/tool.go`、`shared/models/models.go` | `TenantPath` / `DefaultAgentTools` 的过期注释同步（原写「非超级智能体无法用 `/` 越界」，已不成立） |
| 测试 | `gate_test.go`：读 / 写白名单矩阵、自身与 skill 双重状态闸、`AgentID` 缺失拒（防共享 skill 被 `FindForWrite("")` 误判为私有）、自己 / 他人指引分流；`file_test.go`：`SKILL.md` 保护（非超级智能体与超级智能体各一例）+ 附属文件仍可删 |

**刻意未做**（与本次诉求无关，需要时另开）：

- `update_own_agent`（非超级智能体改自己的 `AGENT.md` / 描述）—— 写侧仍未开放；读侧已由 D32 放开到 `agents/{self}/`
- exec 沙箱把 `/agent/skills` 改挂 rw（嵌套挂载）—— 超级智能体的 shell 也是 ro，「看齐超级智能体」即维持不动（见 D11 / §5）

### 阶段 5 — 超级智能体的工具授权显式化（D25 / D26）✅ 已完成

| 文件 | 改动 |
|---|---|
| `service/agent/tools/tool.go` | 管理面从 `CategorySuper` 拆成 `agent-admin` / `skill-admin` / `tool-grant` / `custom-tool`，另新增 `skill-own`；`supervisorOnlyCategoryList` + `supervisorOnlyCategories` 为「仅超级智能体」唯一判据，`IsSupervisorOnlyTool` 改判据 |
| `meta.go` / `customtool_meta.go` | 19 个管理类工具的 `Category` 按四组重标；`skill-own` 三件套从 `skill` 拆出 |
| `toolgrant/catalog.go` | 类别表补到 11 组 + `super` 兼容项；`builtinGroupCategories` 增 5 组（不含 `super`，避免重复卡片）；`builtinByCategory("super")` 特判为四组并集；标签按组重写 |
| `shared/models/models.go` | `SupervisorCoreGrantRefs`（6 组）+ `SupervisorCoreGrants()` + `MainCoreMigrationPending()`（标志 `builtin:agent-admin`）；`toolGrantsOf` 只服务 `DefaultAgentToolGrants` |
| `shared/repository/agents/postgres.go` | `CreateAgent` 按身份选种子：超级智能体落 `SupervisorCoreGrants()`（`item_type=group`），其余仍 `DefaultAgentToolGrants()` |
| `service/agent/agent_impl.go` | 删除「超级智能体始终拥有全部内置工具」块；零行兜底降级为保险丝并记 Error；新增自清除的迁移闸（`onlyLegacyDefaultGrants`） |
| `admin/toolgrants/handlers.go` | 预览同源：只有「零行 / 未物化 core」才标 `BuiltinDefault` 并合并基线，其余如实按表；未迁移时在 `warnings` 里给出该跑的脚本 |
| `sql/main_core_tool_grants.sql` | 存量回填：清旧逐工具 core 行 → 落 6 个组行；单组标志判是否已迁移，**删掉的组不会被重跑加回来** |
| `sql/supervisor_on_demand_groups.sql` | 超级智能体的 4 条 `allow`；三个 `*-authoring` 的 `skill_tool_grants`；摘掉超级智能体对它们的常驻绑定 |
| `data/tenants/t0/agents/main/AGENT.md` | 新增「先绑定技能 → 读全文 → 干活 → 解绑」四步流程与映射表；常驻/按需边界 |
| `data/tenants/t0/skills/custom-tool-authoring/SKILL.md` | 重写（元工具早已存在，原文说「没有」）：两条落地路径、维护操作表、验证清单 |
| `desktop/.../Agents.vue` | 取消超级智能体的「内置工具不可选」限制；core 组打 `core` 徽标 + 删除二次确认；`importCurrentTools` 增 `skill-own` |
| 测试 | `registry_test.go`：`supervisorOnlyCategories` 双向闭合 / core 组有效 / 排除按需组 / 与 SQL 数组逐项一致；`supervisor_core_test.go`：迁移闸矩阵 + 两条路径同源 |

**未做（本轮刻意留下）**：

- 元能力技能与领域技能的分档（是否给 `skills` 加 `supervisor_only` 语义）—— 前者依赖的全是管理类工具，
  绑给非超级智能体只会得到半残技能。当前靠 `supervisorOnlyCategories` 在目录层剔除，够用
- `shell` 是否也降级：三条判据（低频 / schema 大 / 高风险）只中最后一条，先留
- `CategorySuper` 的最终删除：等存量 `builtin:super` 授权行清空后，连同兼容展开一起删

---

## 8. 风险

| # | 风险 | 应对 |
|---|---|---|
| R1 | 给 LLM 开超权，误改线上 agent | D2 闸门 + D3 不放 status（改 status 只有管理端，D5 修订后这是唯一硬边界）+ T3 审计 |
| R2 | 迁移不可逆 | 幂等改名 + 存量确认（只有一个空文件） |
| R3 | sandboxd 远程 → 宿主路径不可见 | 临时目录必须在 `{root_dir}` 下 |
| R4 | `none` 隔离无法强制 `/agent` 只读 | 工具层自律；生产用 nsjail / docker |
| R5 | 前端「工作区」概念混淆 | `Agents.vue` 树的语义是定义域 → 标题改「定义」，功能不变；「工作区」面板新增读会话目录 |
| R6 | 需重新编译并重启 `nexus.exe`；前端 5173 不受影响 | |
| R7 | 非超级智能体从此可往**定义域**写数据 —— 持久、跨会话、跨用户，与工作区（随 session 生灭）语义不同 | 白名单仅限自己的私有 skill 目录（D23）；`SKILL.md` 之外的附属文件**运行时从不读取**（`skillManager.Get` 只读入口文件），故当前不构成注入面。若将来引入「技能目录整体注入」或 plugin 型技能执行文件，须重新评估此条 |

---

## 9. 验证方式

1. `go build ./...` + `go vet ./...` 通过
2. **三域冒烟**：超级智能体 `write_file("/agents/poet/AGENT.md")` 在 `status=1` 成功、`status=3` 报「已废弃」
3. **会话落盘**：对话产生的文件落在 `users/{uid}/sessions/{sid}/`；删 session 后目录消失
4. **无状态调用**：RPC / MCP 不传 `session_id` → 落在 `users/{uid}/runs/{run_id}/`，且**无** `sessions` / `session_records` 记录
5. **沙箱**：`exec` 里 `pwd` 为 `/workspace`；`ls /agent` 可见 `AGENT.md`（只读）
6. **回归**：前端智能体页的定义文件树、技能页内容加载正常；「工作台 → 打开工作区」能列出会话产物
7. **管理类工具收口**：普通 agent 的授权目录里没有 `builtin:super`；直接 PUT `builtin:create_agent` 被拒（`ref not found or has no tools`）；超级智能体不受影响（core 集已物化为 `required` 行，四组 super 都在它的授权目录里，见 D25）
8. **跨 agent 改 skill**：超级智能体 `write_file("/agents/poet/skills/<s>/SKILL.md")` 在 `status=1` 成功；目标废弃时拒绝；直接写实体根目录被拒（提示用元工具）
9. **为他 agent 绑技能**：超级智能体 `bind_skill(agent="poet", skill="english")` 在 `poet.status=1` 成功且即时写库（重复调用无变化）；`unbind_skill` 移除；`poet` 废弃时拒绝；传私有 skill 名或未知名字报错；`read_agent(agent="poet")` 的返回里带 `skills` 清单
10. **技能可见性开关**：`update_agent(agent="poet", discovery=true)` 生效（仅超级智能体）；不传 `discovery` 则不动
11. **skill 权限模型（D19 / D20）**：普通 agent 的授权目录里 `builtin:skill` 含 `*_own_skill`（可被授权）、**不含** `*_shared_skill`；
    直接 PUT `builtin:create_shared_skill` 被拒（`ref not found or has no tools`）；新 agent 缺省**没有** `*_own_skill`
    （工具不可见、强行调用被拒），管理员授权后才可 `create_own_skill` 并落库；
    用 `*_own_skill` 去动共享 skill 被拒（提示改用 `*_shared_skill`）；超级智能体两套都可用；
    `sql/skill_own_tool_rename.sql` 执行后三张表里不再有 `builtin:{create,update,delete}_skill`

---

## 10. 执行顺序

```
阶段 1 地基（改名 + 常量 + 迁移）              ✅ 已完成
   ↓
阶段 2 session 管道（session_id + 目录生命周期 + 挂载）  ✅ 已完成
   ↓
阶段 3 超级智能体权限（三域寻址 + 闸门 + 元工具 + 管理类工具收口）  ✅ 已完成
   ↓
阶段 4 skill 建改删按归属拆分（own / shared 两套工具 + checkSkillScope + ref 迁移）  ✅ 已完成
   ↓
阶段 5 缺省工具集收紧（技能自建移出缺省，改由管理员按 agent 授权，见 D20）  ✅ 已完成
```
