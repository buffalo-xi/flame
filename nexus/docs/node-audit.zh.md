# 逐节点审计

中文 | [English](node-audit.md)

每个节点类型按六个维度检查。目的不是给每个类型写六份文档，而是让描述同一个节点的六处地方——执行器、静态规则、
创建工具、画布、文档、测试——说同一句话。

✅ 表示"有测试或门禁保证"，具体是哪一个列在文末，所以 ✅ 不会悄悄过期。
⬜ 表示"尚未审计"，**不代表**没有测试。
— 表示"不适用"：该类型没有可配置项，或工具不能直接创建它。

## 六个维度

| # | 维度 | 问什么 | 看哪里 |
|---|---|---|---|
| 1 | 运行时 | 能否跑通、按文档失败、产出文档承诺的变量；`on_error` / post / 流式 / 超时同样要看 | `nexus/service/flow/executors/<type>.go` |
| 2 | 静态规则 | `Step` / `Diagram` 的判定是否与第 1 项一致：必然失败都报，合法写法不报 | `nexus/shared/flowcheck/` |
| 3 | 创建 | `create_node` 能否建出完整节点，返回的 warnings 是否等于第 2 项的 findings | `nexus/service/agent/tools/flow_nodes.go` |
| 4 | 画布 | props 面板字段、执行器读取的键、文档里的键名是否一致；变量选择器是否列出该节点真实输出 | `desktop/src/tenant/components/flows/flow/<Type>NodeProps.vue` |
| 5 | 文档/词条 | 节点页、`SKILL.md`、README 判定表、画布标签、它发出的每个 finding code 的文案 | `skills/flow-authoring/**`、`desktop/src/locales/*/flow.json` |
| 6 | 端到端 | 每个类型一条最小 `run_flow`，或写明为什么它不能脱离外部数据运行 | `nexus/service/flow/executors/*_test.go` |

## 状态

| 类型 | 批次 | 文档/词条 | 静态规则 | 创建 | 运行时 | 画布 | 端到端 |
|---|---|---|---|---|---|---|---|
| `code` | 1 | ✅ | ✅ | ✅ | ⬜ | ⬜ | ⬜ |
| `http` | 1 | ✅ | ✅ | ✅ | ⬜ | ⬜ | ⬜ |
| `llm` | 1 | ✅ | ✅ | ✅ | ⬜ | ⬜ | ⬜ |
| `branch` | 2 | ✅ | ✅ | ✅ | ⬜ | ⬜ | ⬜ |
| `loop` | 2 | ✅ | ✅ | ✅ | ⬜ | ⬜ | ⬜ |
| `call` | 2 | ✅ | ✅ | ✅ | ⬜ | ⬜ | ⬜ |
| `result` | 2 | ✅ | ✅ | ✅ | ⬜ | ⬜ | ⬜ |
| `progress` | 2 | ✅ | ✅ | ✅ | ⬜ | ⬜ | ⬜ |
| `mcp` | 3 | ✅ | ✅ | ✅ | ⬜ | ⬜ | ⬜ |
| `rpc` | 3 | ✅ | ✅ | ✅ | ⬜ | ⬜ | ⬜ |
| `intent` | 3 | ✅ | ✅ | ✅ | ⬜ | ⬜ | ⬜ |
| `assign` | 3 | ✅ | ✅ | ✅ | ⬜ | ⬜ | ⬜ |
| `memory` | 3 | ✅ | ✅ | ✅ | ⬜ | ⬜ | ⬜ |
| `process` | 4 | ✅ | ✅ | ✅ | ⬜ | ⬜ | ⬜ |
| `function` | 4 | ✅ | ✅ | ✅ | ⬜ | ⬜ | ⬜ |
| `fork` | 4 | ✅ | ✅ | ✅ | ⬜ | ⬜ | ⬜ |
| `wait` | 4 | ✅ | ✅ | ✅ | ⬜ | ⬜ | ⬜ |
| `break` | 4 | ✅ | ✅ | ✅ | ⬜ | ⬜ | ⬜ |
| `continue` | 4 | ✅ | ✅ | ✅ | ⬜ | ⬜ | ⬜ |
| `return` | 4 | ✅ | ✅ | ✅ | ⬜ | ⬜ | ⬜ |
| `exit` | 4 | ✅ | ✅ | ✅ | ⬜ | ⬜ | ⬜ |
| `layout` | 5 | ✅ | ✅ | ✅ | — | ⬜ | — |
| `comment` | 5 | ✅ | ✅ | ✅ | — | ⬜ | — |
| `start` | 5 | ✅ | ✅ | — | — | ⬜ | — |
| `entry` | 5 | ✅ | ✅ | — | — | ⬜ | — |
| `knowledge` | 5 | ✅ | ✅ | — | — | ⬜ | — |

## 目前由什么保证

- `flowcheck/conformance_test.go` —— 每类型：完整配置零 findings、空配置恰好给出文末那张表的码；表格必须双向覆盖
  `knownTypes`。
- `tools/flow_node_matrix_test.go` —— 每个可创建类型：裸创建的 warnings 必须等于工具填入默认配置后 `Step` 的结论，另加
  `return` 位于 function 内这一条。
- `tools/flow_nodes_test.go` —— `TestCreatableNodeTypesFixed` 钉住调色板类型清单。
- `executors/capabilities_drift_test.go` —— 错误码、actions、post、固定输出与 `flowcheck` 的副本一致。
- `scripts/check-locale.ps1` —— 第 8 段：finding code ↔ 文案；第 9 段：节点类型 ↔ 文档页 ↔ 画布标签。

## 审计中已发现

- `create_node` 曾拒绝在 function 下直接创建 `return`：守卫只查了祖先。已在 `9aeb4e2` 修复（`isOrInside`）。
- `sandboxd/config.yaml` 里 `langs.js` 与它自己的注释矛盾（注释说已禁用，键却开着），导致没有 Node 的主机启动
  sandboxd 失败。已在 `e911cbb` 修复。

## 改动一个类型

新增或修改节点类型按此顺序动：执行器及其 `Capabilities()`（含 `FixedOutputs`）、`Step` / `Diagram` 规则与
`conformance_test.go` 的对应行、`flow_node_matrix_test.go` 的对应行、文档页与画布标签（两者都被门禁管）、最后是 props
面板。跳过其中一步的类型，就会变成"画布上正常、运行时失败"——这正是这份审计要防的事。
