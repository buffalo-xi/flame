# 中英双语实施方案

本文是施工依据，不是提案。分四层，成本差一个数量级，**不要混在一起做**。

## 现状（2026-11 盘点，2026-11-19 更新）

| 层 | 现状 | 规模 |
|---|---|---|
| 文档 | **英文为主**：`README.md` 英文主版，中文对照见 `README.zh.md`；`nexus/docs/en/` 按需增量 | 小 |
| 前端 UI | **已落地**：`vue-i18n` + `desktop/src/locales/{zh-CN,en}/` 各 14 个模块包（`error` 在内），组件统一 `t()` + `formatError()`；长段设计说明仍中文（见坑 5） | 剩余零散硬编码 |
| 后端文案 | **已错误码化**：`ErrCode` / `ErrCodeData` 铺满 `nexus`，`nexus` 内「`ErrJSON` + 中文」零残留，见 `error-codes.md` | 新代码按规范加 |
| 代码注释 | 全中文 | 万行量级（不翻） |

热点文件：`Workbench.vue`（403 行含中文）、`Flows.vue`（348）、`AdminAssetsView.vue`（340）、`AdminTenantsView.vue`（315）、`MainWindow.vue`（242）。

## 分层与成本

### L1 文档双语（1–1.5 天）— 已落地（2026-11 起改为英文主版）

- `README.md`（英文主版）与 `README.zh.md`（中文对照）顶部互相链接
- **英文是主版本**：新内容先写英文，中文版随后对齐（原独立的英文文件已并入 `README.md`）
- `nexus/docs/en/` 按需增量，暂不批量翻

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

### L0 代码注释翻译 — 不做

万行注释，翻译后必然与代码漂移（改代码的人不会同步改英文注释）。维护成本高于收益，对推广没有帮助。

## 六个坑（比工作量更值得先看）

### 1. 布局会被英文挤爆

中文 2–4 字（「保存」「新建租户」）对应英文 `Save` / `Create tenant`，长度差 2–3 倍。
所有固定 `width`、`white-space: nowrap`、定宽的 `el-button` 都会溢出或截断。
**L3 必须单独安排时间做视觉回归**，不能当作顺手改。

### 2. 中文被当作业务值使用

```17:17:desktop/src/tenant/utils/labels.js
  const wanted = (base || '').trim() || '节点'
```

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

### 5. 面向开发者的长段说明暂不翻译

`.editor-tip` / `el-alert` 里的整段解释（例如「授予时会自动补齐起步套件…」）**先保留中文**。

理由：这类文字是设计说明，直译出来是不可读的英文；它们也不影响操作路径 —— 用户要的是标签和按钮。

处置规则：

- **短标签必须翻**：字段名、按钮、页签、空态、状态值、确认框；
- **长段落按需翻**：有人在英文界面下真的需要时，逐段人工翻译，不要先塞进语言包；
- 半成品译文比中文原文更糟：读英文的人会以为这是能力缺失，读中文的人会以为改坏了。

`AgentSettingsPanel.vue` 的「调用授权」页签是这条规则的首个样本：标签与空态已翻，四段解释仍是中文。

### 6. 标点与排版

`，。：（）` 在英文语境下要换成 `, . : ()`。英文语言包按 `en-US` 单独撰写，不要机器翻译后直接提交。

## 执行顺序

```
已完成   L1 文档双语
已完成   L2 错误码规范 + 全库落地（error-codes.md）
已完成   L3 骨架 + 14 个模块语言包 + 语言切换
进行中   L4 剩余零散硬编码、长段设计说明按需翻
不做     L0 注释翻译
```

## 廉价版备选

如果只是想「有个英文界面给外国人看一眼」：只翻外壳（导航、按钮、登录），内容区保留中文，README 注明 `UI partially translated`。
约 2 天，可先验证需求再决定是否做 L4。

## 验收

- 切换语言后刷新页面，选择保持（`localStorage`）
- `<html lang>` 与实际语言一致
- Element Plus 内置文案（分页、日期选择、空状态）跟随切换
- 主要页面在 `1280×720` 下无溢出、无截断
- 后端业务错误在英文界面下显示英文
