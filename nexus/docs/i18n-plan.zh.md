# 中英双语实施方案

中文 | [English](i18n-plan.md)

本文是施工依据，不是提案。分四层，成本差一个数量级，**不要混在一起做**。

## 现状（2026-11 盘点，2026-11-19 更新）

| 层 | 现状 | 规模 |
|---|---|---|
| 文档 | **英文为主**：`README.md` 英文主版，中文对照见 `README.zh.md`；`nexus/docs/` 下按 `<name>.md` + `<name>.zh.md` 增量迁移 | 小 |
| 前端 UI | **已落地**：`vue-i18n` + `desktop/src/locales/{zh-CN,en}/` 各 14 个模块包（`error` 在内），组件统一 `t()` + `formatError()`；注释之外已无中文文案（见下） | 剩余中文注释（改到哪个文件处理哪个，第 7b 节） |
| 后端文案 | **已错误码化**：`ErrCode` / `ErrCodeData` 铺满 `nexus`，`nexus` 内「`ErrJSON` + 中文」零残留，见 `error-codes.md` | 新代码按规范加 |
| 生成内容（落盘） | **语言是生成参数**：`shared/skeleton` 注册表 + 创建请求的 `lang`，默认英文；定义域模板由 CI 兜底 | 加一门语言 = 注册表加一条 |
| 构建与运维脚本 | **诊断英文**：`scripts/check-locale.ps1`、`scripts/verify-sql.ps1`、`desktop/scripts/check-locales.mjs` 的提示都是英文；审计会跳过被当数据引用的中文，所以英文注释里引用一句中文样本不算命中 | — |
| 代码注释 | **AI 辅助代码里的注释是英文**：助手新写或重写的注释一律英文，不论用户用什么语言；历史注释按「改到哪行翻哪行」 | Go 已归零并被闸门管住；前端存量按触碰递减（第 7b 节） |

还剩多少文案，用「去掉注释后统计中文」量（`scripts/check-locale.ps1` 第 7 节）。按「含中文的行数」量不出这件事：那个口径给
`Workbench.vue` 报了 403 行，而该文件的文案早就在语言包里了 —— 其余是设计说明，本方案不翻（L0）。2026-11-20 清理后是
**2 行，且都是刻意的**：

- 语言切换器的自名（`desktop/src/locales/index.js` 里的 `中文`）—— 一门语言用它自己的名字列出，翻了就失去了选择器的意义；
- `MainWindow.vue` 里一处老的 `msg` 兜底，用于 `error` 字段还不存在的后端。

口径有一点要说明：它找的是「没进语言包的中文」，找不出「本该走 `t()` 却写死英文」的情况。新页面仍然必须走 `t()`，
而 `desktop/scripts/check-locales.mjs`（`prebuild` 跑）只管反方向 —— 一种语言有的键另一种没有。

同一脚本第 1 节（Go 里的中文注释）也归零了。它最后三处命中都是英文注释里引用的数据 ——
能力探测的 `系统:` 行、`"中"` 的字节偏移、快照片段 `"百度一下"` —— 所以计数现在会先剔除引号内的内容。
**描述**中文数据的注释，不是待翻译的注释。

## 分层与成本

### L1 文档双语（1–1.5 天）— 已落地（2026-11 起改为英文主版）

- `README.md`（英文主版）与 `README.zh.md`（中文对照）顶部互相链接
- **英文是主版本**：新内容先写英文，中文版随后对齐（原独立的英文文件已并入 `README.md`）
- `nexus/docs/` 下的技术文档同样按 `<name>.md`（英文）+ `<name>.zh.md`（中文镜像）增量迁移，暂不批量翻
- 这个形状不只限 `nexus/docs/`：`desktop/README.md`、`nexus/platform/README.md`、`desktop/src/platform/README.md` 也已按它改好，
  `desktop/DEPLOYMENT.md` 同样。最后这份是**重写而非翻译**：它原本是一份通用 Vite 部署模板，里面还带着另一个项目的构建数字，
  忠实翻译只会把虚构一起留下来；英文版与镜像现在描述的都是这个管理台。
- 已没有「只有中文」的主文档：最后两份是 `desktop/NGINX_SUB_FILTER_SOLUTION.md` 与 `docs/screenshots/README.md`，
  现在都是英文主版 + 镜像。主文档里剩下的中文都是刻意的：语言切换行、术语对照（`上架` / `下架`）、以及引用的样本
  （`系统:`、`"张三"`）

选择理由：零回归风险，直接改善 GitHub 可发现性。外国访客先看 README，能跑起来才看 UI。

### L2 后端文案错误码化（2–3 天）— UI 双语的前置条件

现状 `code` 只有 `200 / 400 / 507` 三个值，无法承载语义。若只翻前端，用户会看到「英文界面 + 中文报错」。

方案见 `nexus/docs/error-codes.md`。要点：

- `msg` 降级为「编码 + 参数」，后端不关心语言
- **不走** `Accept-Language` 返回文案那条路（同一套文案维护两遍）
- 可分模块推进，先高频路径：登录 / 租户 / 端点 / 配额 / flow 运行

### L3 前端 i18n 骨架（4–6 天）

- 引入 `vue-i18n@9`
- Element Plus locale 走 `<el-config-provider :locale="elLocale">`，`zhCn` / `en` 按需加载
- dayjs locale 同步
- 语言切换器 + `localStorage` 持久化 + `<html lang>` 更新
- **首批只做 5 个页面**：登录（`AuthGate.vue`）、外壳（`MainWindow.vue` / `PlatformApp.vue`）、工作台（`Workbench.vue`）、运行台账（`Running.vue`）、运行历史（`RunHistory.vue`）

语言包按模块拆：

```
desktop/src/locales/
  index.js
  zh-CN/{common,tenant,platform}.json
  en/{common,tenant,platform}.json
```

**key 用语义名，不要用中文当 key**（`t('common.save')` 而不是 `t('保存')`），否则改文案要动全部语言包。

### L4 全量页面迁移（8–12 天）

按模块分批，每批一个 PR。不要一次性大改：`Flows.vue`、`AdminAssetsView.vue` 这种 300+ 行的文件冲突概率极高。

文案层面 2026-11-20 已收口：`scripts/check-locale.ps1` 第 7 节在桌面源码里报不出注释之外的中文。这些文件剩下的中文是注释，
按文件被改动时逐行处理（L0）。

### L0 注释语言 —— 碰到的写成英文，不做整体迁移

注释不进界面，模型也不读，所以它的语言不影响任何验收项 —— 这也是存量不整体翻的原因：整体翻必然与代码漂移
（改代码的人不会同步改英文注释）。

要求更窄也更便宜：**助手新写或重写的注释是英文** —— Go、`.vue` / `.js`、SQL 都一样，不论用户用哪种语言写需求。
同一份文件里两种语言的注释，比任何一种单一语言都更难评审；而这段文字是助手引入的，助手没有理由留下中文。

这不是翻译工程。碰了一个带 200 行中文注释的文件里的三行，那 200 行仍然留着：
Go 已归零并由闸门管住，前端存量靠触碰递减，只在审计里报告（第 7b 节）。

## 内容语言策略（客户可见 / 落盘）

上面四层说的是**活儿在哪**，这一节说的是**一段文案允许是什么语言**，这部分不能按文件逐个决定：
塞错类别，文案要么跟着界面闪、要么被冻结在错的语言，要么悄悄让 agent 行为分叉。

| # | 类别 | 谁读 | 语言 | 机制 |
|---|---|---|---|---|
| 1 | 界面文案（标签、按钮、页签、空态、状态值、确认框） | 用户，只在界面上读 | 界面语言 | `desktop/src/locales/{zh-CN,en}/**`，走 `t()` / `$t()` |
| 2 | 后端返回的业务文案（报错） | 用户 | 界面语言，前端解析 | `ErrCode` / `ErrCodeData` 发 `error` + `params`；`msg` 是英文，只到运维和 `curl`（`error-codes.md`） |
| 3 | 面向模型的产品文案：定义域文件、内置工具的 `Description` / `Label` / 参数 schema | 模型 | **只能英文** | `nexus/platform/provision/template/files/**`、`ToolMeta`；由 `TestSystemEntityFilesAreEnglish` 兜底 |
| 4 | 一次运行交回来的文字（授权原因、解析告警、终止提示） | 模型，再经模型到用户 | **英文**，因为产出那一刻手上没有任何语言 | 纯散文、没有编码 —— 见下 |
| 5 | 生成并落盘的内容（agent / skill 骨架、默认节点 label、版本快照、资产包） | 用户，当作可编辑的文件读 | **生成时**就定死 | 语言是请求参数（`lang`），见下 |

判定办法：新文案默认进类别 1；只有「必须跨语言切换存活」「要由模型转述」「本来就要给模型读」才往外走。
「在服务端拼更省事」不是进类别 2 的理由。

### 落盘内容：语言是入参，不是查表

骨架、默认节点 label、发布出来的版本，写一次之后就是用户的东西：会被编辑、被版本化、被导出成资产包、被装进别的租户。
若改成读取时翻译，版本快照、资产包和页面渲染会对同一个文件给出不同内容，同一份实体在两台机器上也不再是同一件事。

所以语言在创建时定一次，之后不再解析：

- 创建请求带 `lang`（`admin/agents`、`admin/skills` 绑定它，界面传 `currentLocale()`），
  `shared/skeleton.Normalize` 把任意 tag 归一化到注册表（`en`、`zh-CN`），**默认英文** ——
  一门不认识的语言得到一份可读的英文骨架，比空文件或 500 更接近用户预期；
- 加一门语言 = 在 `agentSkeletons` / `skillSkeletons` 里加一条，handler 不动；
- 默认节点 label 同理：`desktop/src/tenant/utils/labels.js` 从语言包取兜底 label，
  于是图里落盘的就是创建时界面所在的语言；
- 历史不重写。已存在的 `AGENT.md`、版本快照、已安装的资产都保持创建时的语言 ——
  只有模板（`template/files/**`，类别 3）被门禁要求英文，因为它会被每个新租户物化、并直接进模型上下文。

### 手上没有语言时，默认英文

终止提示、授权原因、解析告警、回填的版本说明，产出那一刻都拿不到语言：终止是从 Redis 信号落到持有该 run 的实例上，
授权解析发生在一次工具调用内部，`cmd/backfillversions` 是运维在命令行里按 schema 跑的。没有可解析的对象，
于是它们走和 `skeleton.Default` 同一条默认：**英文**。

这不是「把后端文案再翻一遍」，而是类别 5 的同一个规则 —— 语言由产出者知道什么决定。用户也不会直接读到英文散文：
`responseLanguageRule`（系统提示词的最后一行）要求模型把工具输出用用户的语言复述。界面若直接渲染这类值，它是**数据**
而不是文案 —— 发布历史里的一条版本说明保持它写入时的语言，和骨架完全一致。

只剩一个例外刻意保持双语，且属于内容而非文案：

- `shared/skeleton` 里的 `zh-CN` 正文是类别 5 的内容（给中文租户生成的），不是产品文案。

`responseLanguageRule` **只用英文**，和其它给模型看的字符串一样（工具说明、工具 Label、参数 schema）：
它本身就是一条语言约束，必须是一条无歧义的单语指令 —— 双语版本反而会诱使模型照着它描述的混排去混。

若某个界面真要把这些字符串**当文案**渲染，它必须改成 `code` + `params` 再由语言包解析 ——
和 `error` 信封同一形状。目前一个都没有；`AgentGrantView.Note` 与 `ResolveResult.Warnings` 的字段注释里写明了这条约束，
下一个人改到这里就能看见。

### 为一次调用指定输出语言（MCP / RPC）

程序化调用本身不带语言：输入是一个 JSON 参数表，而「跟随用户所写的语言」这条规则没有可读的东西。于是允许调用方指定：

```json
{"name": "writer", "arguments": { "...": "..." }, "_meta": {"language": "zh-CN"}}
```

- `params._meta.language`，BCP-47，可选 —— 与 `progressToken` / `session_id` / `call_chain` 同一个槽位，因为它描述的是
  「这次调用该怎么回」，不是「输入是什么」（`core.MetaLanguageKey`）；
- 它会变成一句英文，追加在回复语言规则之后。优先级：**`AGENT.md` 指定语言 > `_meta.language` >
  `agents.default_language` > 产品缺省（英文）**。只会一种语言的 agent（比如某语言的写作教练）不该被调用方覆盖；
- 什么都没指定时，提示词会**明说**：“未指定输出语言：按输入所用的语言回答；输入没有语言信号（纯 JSON、id、数字）时用英文”。
  原先是什么都不说 —— 而中文在前的规则叠在英文在前的提示词上，模型只能猜，同一次调用可能中文也可能英文；
- `agents.default_language` 是中间那级：这是一列定义（发布时随版本冻进 `agent_versions`，正式运行读版本里的那份，
  见 `versioning.AgentCurrentAttributes`），走正常的 agent 更新接口即可设置，也可以用超级 agent 的
  `update_agent(default_language=...)` 写（单列更新，绝不整行覆盖；`read_agent` 会回显），
  并且和其他 tag 一样校验（非法值直接落空，不会被透传）；
- 非法 tag 在入口就被拒（`update_agent` 报错而不是照样写入），在读出时则被忽略（`NormalizeOutputLanguage` 让它落到下一级），
  因为这段文字最终会进系统提示词；
- 它跟着链路走，**判定出来的值才往下传**：调用方指定的，或 agent 自己配的 `default_language`。产品兜底值不传 ——
  只因为没人配置就把 `en` 当强制值发下去，会盖掉下一跳自己的缺省，中文会话会因此把 `en` 传给每一层子 agent；
- 它只约束散文。错误文案仍走 `error` 编码 + 语言包，生成并落盘的内容仍取自己的 `lang` 参数（类别 5）——
  一次调用的提示不得悄悄改掉骨架的语言。

RPC 与 MCP 都从同一个 `params._meta` 读这个字段。这个值现在也进到工作流，位置与 `session_id` 完全对称：

- flow 端点（RPC / MCP）像取 `_meta.session_id` 一样取 `_meta.language`，每次 flow 运行都带一个内置变量
  `language`（`flow.ExecContext.Language`，节点里是 `builtin.language`）。空串表示"没人指定过"，节点可以直接判断或
  拼进提示词，不需要第二个开关；
- 它**双向**跟着链路走：agent 把自己判定出的语言 —— 调用方指定的，或它自己配的 `default_language` —— 通过 ctx
  （`core.WithPinnedLanguage`）记下，flow 据此继承为内置变量 `language`，出站 RPC 与 MCP 客户端再把它写回
  `params._meta.language`：flow → agent、flow → 子 flow、agent → agent（`call_agent`）、agent → `mcp__` / `rpc__` 工具
  都保持同一种语言。产品兜底值**不往下传**，否则 flow 读到 `language: en`、远端工具收到它当成强制值，都会误解成意图
  （这条界线由 `core.LanguagePinnedFrom` 划）；
- 因此有两个载体、各一个含义：`params._meta.language`（线上）是"强制值"，ctx 里的值是"事实 + 是否被判定过"。
  `core.WithLanguageHint` 留给进程内没有强制含义的值；flow 只把收到的"强制值"再发出去。

按设计如此：flow 自身的 LLM 节点**不会**自动追加语言规则。提示词里要不要说"用某语言回答"，由 flow 的作者决定；
节点只负责把值给出去 —— 声明一个 `source_type: "builtin"`、`source_key: "language"` 的输入变量，然后在提示词里引用它。
flow 里的 agent 节点是被覆盖的：flow 的强制值随之调用传下去，再由该 agent 按自己的优先级处理。

## 六个坑（比工作量更值得先看）

### 1. 布局会被英文挤爆

中文 2–4 字（「保存」「新建租户」）对应英文 `Save` / `Create tenant`，长度差 2–3 倍。
所有固定 `width`、`white-space: nowrap`、定宽的 `el-button` 都会溢出或截断。
**L3 必须单独安排时间做视觉回归**，不能当作顺手改。

### 2. 中文被当作业务值使用

```19:19:desktop/src/tenant/utils/labels.js
  const wanted = (base || '').trim() || i18n.global.t('flow.node.fallbackLabel.node')
```

（这行原本是 `'节点'`，已改为走 i18n。）

同类还有按 label 判重、按中文状态字符串做分支的地方。**这些不是文案，是业务常量**，塞进语言包会改变行为。
迁移前先搜一遍「中文字面量参与比较 / 拼接 / 持久化」的位置，改成常量或枚举。

已处置的样本：`Workbench.vue` / `SkillFileEditor.vue` / `AgentFileEditor.vue` 原来比对
`body.msg === 'already exists'`，后端改成 `agent.file_exists` / `skill.file_exists` 后，前端改判
`body.error === '...'`；`MainWindow.vue` 的「没配默认模型」也从双语正则改成优先认
`llm.model_not_found`（无 `error` 字段时才退回文案匹配，兼容老后端）。
**新写的判断一律认 `error` 编码，不要认 `msg`。**

### 3. 字符串拼接 vs 复数

`${n} 个节点` 这类拼接，中文无复数变化，英文有（`1 node` / `2 nodes`）。
必须走 vue-i18n 的 pluralization：`t('flow.nodes', { n }, n)`，否则英文永远差一个 s。

### 4. 定义域模板不要翻

`nexus/platform/provision/template/files/` 下的 `AGENT.md`、`SKILL.md` 是**给模型看的提示词**，属于数据不是 UI。
翻成英文会改变 agent 行为，且会与存量租户（`nexus/data/tenants/t0/`）不一致。

### 5. 面向开发者的长段说明：整段翻，但不要留在源码里

`.editor-tip` / `el-alert` 里的整段解释（例如「授予时会自动补齐起步套件…」）原本以中文留在模板里。现在它们是普通语言包
键（`agentSettings.supervisorGrantHint`），按整段翻译 —— 本文原先当作「保留中文」样本的 `AgentSettingsPanel.vue`
「调用授权」页签，如今已整段译完。

分批规则现在约束的是**什么时候花这份工**，而不是段落能否留在源码里：这类文字是设计说明，不在操作路径上
（用户要的是标签和按钮），直译出来也是不可读的英文。

处置规则：

- **短标签优先翻**：字段名、按钮、页签、空态、状态值、确认框；
- **长段落整段人工翻，跟着页面改动走** —— 不机器翻译，也不翻半段；
- 半成品译文比中文原文更糟：读英文的人会以为这是能力缺失，读中文的人会以为改坏了；
- 不能留的只有「模板里的中文散文」：`scripts/check-locale.ps1` 第 7 节会报出来，目前是 0。

### 6. 标点与排版

`，。：（）` 在英文语境下要换成 `, . : ()`。英文语言包按 `en-US` 单独撰写，不要机器翻译后直接提交。

## 执行顺序

```
已完成   L1 文档双语
已完成   L2 错误码规范 + 全库落地（error-codes.md）
已完成   L3 骨架 + 14 个模块语言包 + 语言切换
已完成   生成内容的语言 = 生成参数（shared/skeleton、创建时传 lang、CI 门禁）
已完成   L4 文案：前端源码注释之外已无中文（check-locale 第 7 节）
递减     L0 注释语言：助手碰到的那些行写成英文（存量见第 7b 节）
```

## 廉价版备选

如果只是想「有个英文界面给外国人看一眼」：只翻外壳（导航、按钮、登录），内容区保留中文，README 注明 `UI partially translated`。
约 2 天，可先验证需求再决定是否做 L4。

## 强制手段

`scripts/check-locale.ps1` 打印上面那些审计项。`-Gate` 把其中能靠正则判定的部分变成一个退出码，而
`scripts/run-tests.ps1` 在跑完测试后会以这个模式调用它，于是「跑一遍全部」这一条命令同时给出两个结论
（`-SkipCopyCheck` 可跳过该步）。闸门始终扫四个模块，即使 `-Module` 缩小了测试范围。

- 注释里的中文 —— 引号内的样本除外，因为 `系统:`、`"中"`、`"百度一下"` 是被描述的数据，不是待翻译的散文；
- 非 `_test.go` 的 Go 文件里注释之外的中文（fixture 是数据，见 AGENTS.md）；
- 桌面源码里没进语言包的中文文案；
- `ErrJSON`（2026-11 已移除，不许回来），以及 `ErrCode` 那行 `msg` 里含中文；
- `shared/flowcheck` 发出的 finding code 在任一桌面语言包里没有 `flow.check.code.*` 文案，或反过来——语言包里留着一条
  已经没有规则会发出的文案（第 8 段；否则画布只能退回到那句通用提示）。

唯一的例外是脚本末尾两张 allowlist：`shared/skeleton` 的 `zh-CN` 正文、语言切换器的自名、一处老 `msg` 兜底。
往里加一条是需要评审的决定，不是修构建的手法。

闸门刻意不管的（重写它的代价大于容忍）：测试 fixture；SQL 注释与 `COMMENT ON` 文本（迁移就是历史 ——
`nexus/sql/system_entity_desc_en.sql` 才是把预置文案改成英文的正规做法，它写明了为什么实体行 **和** `is_current`
版本行必须一起改）；以及 `.vue` / `.js` 里的中文注释 —— 由第 7b 节报告而不进闸门，因为那里的规则是「你碰到的那几行是英文」（L0）。

## 验收

- 切换语言后刷新页面，选择保持（`localStorage`）
- `<html lang>` 与实际语言一致
- Element Plus 内置文案（分页、日期选择、空状态）跟随切换
- 主要页面在 `1280×720` 下无溢出、无截断
- 后端业务错误在英文界面下显示英文
