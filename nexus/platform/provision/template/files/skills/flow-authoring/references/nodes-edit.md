# 改结构：`create_node` / `insert_node` / `update_node` / `delete_node`

`create_flow` 一次给全 `steps` 最省事。漏了节点、要插到链中间、要改 `config`、要挪进容器，用这四个。
它们只动 `nodes`（以及必要的 `edges`），不碰流程图以外的任何东西。

## 什么时候用哪个

| 想干什么 | 用哪个 |
|---|---|
| 加一个节点（顶层或容器内） | `create_node` |
| 在已有链的某处插一个节点 | `insert_node`（原子：删旧边 + 连 A→新→B） |
| 改标题 / 改 config / 挪进容器 / 挪位置 | `update_node` |
| 删节点（连带边与子树） | `delete_node` |

建完节点还没有边，**下一步用 `create_edge`**（返回体里的 `handles` 直接给出可用位置）。

## `create_node`

```json
{"flow": "daily-sales-report", "type": "code", "label": "清洗"}
{"flow": "daily-sales-report", "type": "http", "parent_node": "7"}
```

参数：`flow`、`type`、`label?`、`config?`、`parent_node?`。

- `config` **不给就用该类型的默认模板**（与前端「新建节点」写进去的一致：`http` 带 outputs、
  `assign` 带空 `inputs/outputs/targets/assignments`、`loop` 带 `loop` + `loop_vars`、
  `branch`/`intent` 自动带一条 `children`）。给了就按你的来，缺的键不补。
- `parent_node` 只能是 `loop` / `process` / `function` 的 id。传了就建在容器里（坐标是相对父节点的，
  并带 `extent` 限制在父节点范围内）。
- **容器自动带 `entry`**；父容器原来没有 `entry` 也会顺手补一个（返回 `parent_entry` 并给 warning）——
  没有 entry 的容器会「成功但什么都没跑」。
- `function` 只能建在顶层；`return` 只能建在 `function` 容器内部；`start` / `entry` 不接受手动创建。
- 返回：`node` `entry`（新建容器的）`parent_entry`（补的）`handles` `warnings` `node_count` `edge_count`。

## `insert_node`

```json
{"flow": "daily-sales-report", "edge_id": "04958", "type": "code", "label": "审计"}
```

- `edge_id` 来自 `read_flow` 的 `diagram.edges[].id`。
- 新节点落在与这条边相同的 `parent` 下；原来那条边被删，改成 `A→新`、`新→B` 两条。
- 只能插**既有入边又有出边**的节点：`function`（没有边）、`exit`/`break`/`continue`/`return`（没有出边）
  都会被拒绝。
- 返回：`node` `edges`（两条）`handles` `warnings`。

## `update_node`

```json
{"flow": "daily-sales-report", "node_id": "3", "label": "改成新标题"}
{"flow": "daily-sales-report", "node_id": "3", "config": {...}}          // 整体替换
{"flow": "daily-sales-report", "node_id": "3", "config_patch": {"limit": 5, "window": null}}  // 顶层合并，null = 删键
{"flow": "daily-sales-report", "node_id": "4", "parent_node": "7"}       // 移进容器
{"flow": "daily-sales-report", "node_id": "4", "parent_node": ""}        // 移回顶层
{"flow": "daily-sales-report", "node_id": "3", "position": {"x": 1240, "y": 320}}  // 挪位置
```

- `config` 与 `config_patch` **二选一**。返回体里带最终 `config`，改完扫一眼。
- `position` 是画布坐标：顶层节点写绝对值；容器内节点写**相对父容器**的坐标。
  自动排版看着别扭时用它微调，规则同 `SKILL.md` 的「布局」：`x`/`y` 取 20 的倍数，
  主链同 `y`、相邻叶子 `x` 步长 200（`branch` / 容器之后 380），分支放分支节点右下方（行间 ≥ 72），别和已有节点重叠。
  返回的 `node.position` 就是落库后的坐标。
- 改 `parent_node` 时：**横跨新旧容器的边会被删掉**（跨 `parent_node` 的边本来就不合法），
  返回 `removed_edges`，需要在新容器里重新 `create_edge`。
- 不能把节点移进它自己的子树；`function` 不能移进容器；`start` / `entry` 不能移动。

## `delete_node`

```json
{"flow": "daily-sales-report", "node_id": "4"}
```

- 连带删掉与该节点相连的所有边。
- 删容器 = 删整棵子树（含它自己的 `entry`），`deleted_nodes` 会列全。
- **被引用时拒绝**，并列出引用方：
  - `config.inputs.<key>.source_ref`
  - `config.target`（`call` 调用目标）
  - `config.fork_node_id`（`wait` 等的并发组）

  先 `update_node` 把这些引用改掉再删，否则删完运行期直接报取值失败。
- `start` 不能删（删了整张图跑不起来）；`entry` 也不能单独删（随容器走）。

## label 必须全图唯一

`label` 是 `config.inputs.*.source_ref`、`call.config.target`、`wait.config.fork_node_id` 的**可写形式**
（工具会按 label 换真实 id），所以重名会让这些引用指向错误节点。全流程图内 label 必须唯一：

| 场景 | 撞名怎么办 |
|---|---|
| `create_flow` 的 `steps[].label`（显式写的） | **报错**，说明是第几步、被哪个节点 id 占用 |
| `create_node` / `insert_node` 的 `label` | **报错** |
| `update_node` 改名 | **报错**（改成自己原来的名字不算撞） |
| 不写 `label`（用默认名）、容器自动生成的 `entry` | **自动加 `-2` / `-3` 后缀**，不会失败 |
| 画布拖入节点 / 面板改名 | **自动加 3 位字符数字后缀**（如 `代码-x7k`），并提示已改名 |
| 保存接口（画布「保存」） | **拒绝保存**：`节点名称不能重复：「代码」（节点 2 与 3）…`，改名后才能存 |

所以：

- 想稳稳引用某一步时，**给它起个明确的名字**，重名会被拦下来，不会默默指错；
- 同类型的第二个「代码」节点会自动叫 `代码-2`，不要以为两个都叫「代码」；
- 第二个容器的入口叫 `入口-2`（`entry` 也参与唯一性）。

## 容器的结构约定

容器（`loop` / `process` / `function`）的执行入口是 `parent_node == 容器id && type == "entry"` 的节点。
找不到入口会在运行期报 `config` 错误（`... body entry node not found`），不再静默跳过。所以：

- 往容器里加节点后，确认容器有 `entry`，并且 `entry` 有一条出边指向子链首节点（`create_edge`）。
- 容器内部节点之间可以自由连线，但**不能跨容器**：`parent_node` 不同的两个节点连不上。
- 容器可以嵌套（`loop` 里放 `process`），`return` 必须在 `function` 的任意层祖先里。

## 与其他工具的分工

| 工具 | 边界 |
|---|---|
| `create_flow` | 一次建出主干（含 `branches` / `children`）；步骤内的结构它最省事 |
| `create_node` / `insert_node` | 只建节点，不替你连边（`insert_node` 除外，它自带两条边） |
| `create_edge` / `update_edge` / `delete_edge` | 只动边，不建节点、不改 `config` |
| `update_node` | 只改 `label` / `config` / `parent_node` / `position`，不改边（除了清理跨容器边） |
| `delete_node` | 删节点 + 相关边，不重建 |
