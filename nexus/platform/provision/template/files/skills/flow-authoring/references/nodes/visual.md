# 结构节点与纯视觉节点

这几类**没有执行逻辑**，但画错了会让整条链跑不起来。

## `start`（开始）

- 新流程约定 **id = `"1"`**，`type: "start"`，`data: {"label": "开始"}`，**没有 `config`**。
- 运行时按 **`type == "start"`** 识别起点（`id == "1"` 仅作为优先命中，缺失时按类型兜底），
  并直接跳过该节点（不进执行器），从它的第一条出边开始跑；跳过同样按 type 判断。
- 流程的**入参 schema 与限额**（`arguments` / `timeout` / `max_*`）不在 diagram 里，在前端「开始」节点的
  属性面板里编辑，落到 flow 实体上。`create_flow` **不写 `arguments`** —— 这就是工具建的流程
  端点「没有入参说明」的原因。

## `entry`（容器入口）

- 容器（`process` / `loop` / `function`）内的第一个节点，`type: "entry"`、`parentNode: <容器 id>`、
  `data.label` 统一为 `入口`，无 `config`。
- 前端建容器时自动生成；运行时同样被跳过，只作为容器内链的起点。
- **每个容器有且只有一个**；没有 entry → 容器直接成功跳过。

## `comment`（注释）与 `layout`（布局）

- `comment`：纯视觉，没有 Handle，**不要连线**（一旦被导入的边连上，会报
  `no executor registered for node type: comment`）。
- `layout`：占位/分组容器，**可以连线**；runner 跳过它本身，直接沿第一条出边继续，
  适合把一串节点框起来而不影响执行。
- `layout` 可以在 `create_flow` 的 `steps` 里直接建（`type: "layout"`，32px 圆点，宽按 60 算），
  常见用法是**折行拐点**：主链一行排到 1500px 时插一个 layout，把后续节点摆到下一行，
  让整张图方一点、别拉成 3000px 长条。
- 两者后端都**没有执行器**。

## `knowledge`（知识）

- 前端有组件，但**后端没有注册执行器**（`RegisterExecutor` 里没有 `knowledge`）。
  连进链路的唯一结果就是 `no executor registered for node type: knowledge`。
- **`create_node` / `insert_node` 也不允许建它**（前端 palette 早已撤掉入口，只为渲染存量流程图保留映射）。
- 需要知识检索请用 `mcp` / `rpc` 节点调对应服务，或让 `llm` 节点自己带上下文。

源码：`service/flow/executors/runner.go`（按 `type` 跳过 `start` / `entry` / `layout`）、`registry` 的注册表
