# 并发执行：每个执行体一个并发作用域

中文 | [English](parallel-scope.md)

**状态：已落地。** 本文替代 `fork` / `wait` 这一对节点。已落地：`data.concurrent`、`config.parallel`（`max_concurrency`、`timeout`、`join` = collect / drain / abort）、`process` 上的 body 作用域（派发 / 汇合 / 快照）、六个校验码（四个 error、两个 warning）、`create_node` / `insert_node` / `update_node` 的 `concurrent` 参数，`read_flow` 的 `blocks`，`loop` 作宿主（每轮一个新作用域，逐轮快照），以及 `fork` / `wait` / mono 的删除。这里**故意不提供迁移路径，也不留兼容层**：两个节点类型整片删除（见 §9），仍带着它们的图不构成本设计的约束条件。取舍记录与被否决的备选方案在 §11 与 §12 —— 重新打开设计前请先读那两节。

## 1. 为什么换掉现有这一对

`fork` 是一个什么也不拥有的标记，`wait` 是一个可选节点却承担了模型需要的全部保证。这个倒置是下面五个缺陷的共同来源。

| # | 缺陷 | 依据 |
|---|---|---|
| 1 | 运行状态只由主链的错误决定，成员失败除非有 `wait` 否则不可见 | `runner.go` —— `persistRun` 用 `runErr`；派发出去的 goroutine 丢弃错误（`_ = executeNode`） |
| 2 | 运行结束时**不**收口 fork 组（只收口 mono），未收口的成员在运行已被记录为成功之后才被取消 | `runner.go` —— `execCtx.MonoWGWait()`；`defer execCtx.Cancel()` |
| 3 | mono 并发节点有同一个洞，且完全没有观察路径 | 同上两处 |
| 4 | 组的结果没有通道：`wait` 不声明 outputs，成员只能写共享的 flow 变量（无同步，后写覆盖） | `wait.go`；`wait.md`「No output variables」 |
| 5 | 合流把全部成员失败压成 `other`，且只支持 `skip` —— 按错误码分流不可能；而 `skip` 恰恰是唯一会让主链与仍在运行的成员并行的动作 | `wait.go` —— `failf(nr, ErrCodeOther, …)`、`Capabilities{ Actions: []ErrorAction{ErrorActionSkip} }` |

结构问题是随之而来的：一个组是挂在节点结果上的一次性 barrier（`ForkState` active → waiting → done），所以第二次合流是 `config` 错误（`nodeResult.go`）；成员资格由「谁调用了 `Add`」发现，因此从未被派发的成员在计数里根本不存在。`flowcheck` 从不追问一个组到底有没有合流点。

## 2. 模型

> **并发作用域 = 一次 body 执行。成员 = 从该 body 的链上甩出去、在 body 结束时被收口的节点。**

术语在此冻结（UI、代码、数据库使用同一批词）：

| 术语 | 含义 |
|---|---|
| **宿主**（host） | body 是一个作用域的容器：`process` 或 `loop`，仅此两种 |
| **执行体**（body） | 宿主执行的那条链，从其 `entry` 子节点开始 |
| **作用域**（scope） | 该 body 的一次执行（一个 `BodyScope` 值，每次执行新建；`loop` 每轮迭代一个） |
| **成员**（member） | 宿主的一个**可执行**直接子节点，带 `data.concurrent: true`，且位于 body 的 entry 链上。入口、`break` / `continue` / `return` / `exit`、注释 / 布局节点、输出节点（`result` / `progress`）与瞬时节点（`assign` / `memory`）不能当成员（控制流待在链上，结果与进度按调用顺序追加到本次运行，assign / memory 不等待任何东西，占位没有收益） |
| **汇合**（join） | body 结束时隐式执行的收口动作；不存在 `wait` 节点 |

有两个词会进入标识符，**不可互换**：`concurrent` 是节点自身的成员标记（`data.concurrent`、工具的 `concurrent` 参数、画布上的勾选框），`parallel` 是宿主的设置块与记录（`config.parallel`、`ParallelConfig`、`ParallelResult`、`ProcessResult.Parallel`）。校验码同样分组：`concurrent_*` 校验标记本身，`parallel_*` 校验作用域的运行时形态。散文层——面板标题、帮助主题、本文——一律说「并发」，它只是概念，永远不是字段名。

`process` 与 `loop` 既是宿主**也**可以当成员。宿主可以放在任何能放节点的位置——`branch` 子链里、顶层、另一个宿主里——所以并发可以出现在任何地方，但永远通过宿主出现。

## 3. 数据模型

成员标记是**节点结构属性**，不是 `config` 键。`update_node` 会整体替换 `config`；放在那里的标记会丢，这正是今天需要 `keepConcurrency` 的原因。

```go
// flow.NodeData
Concurrent bool `json:"concurrent,omitempty"` // 从所属 body 的链上甩出去；仅对 process/loop 的子节点合法
```

为什么放在 `data` 里、而不是 `data` 的同级：`position` / `parent_node` / `extent` 在顶层，是因为画布库本身就有这几个字段；`concurrent` 是我们自己的概念，而 `data` 是画布原样透传的那一格。它也不能进 `config` —— `config` 是行为，行为会被 `update_node` 整体替换。

**`data` 必须处处「读出—改一处—写回」。** `buildNodeForTool` 现在会重新构造 `map[string]any{"label": …, "config": …}`；这条路径必须把已有的标记带过去，否则同一类「静默丢失」只是往上挪了一层。用一条单测盯住它。

宿主设置（`process` / `loop` 上的可选配置块）：

```go
type ParallelConfig struct {
    MaxConcurrency int         `json:"max_concurrency"` // <= 0 表示作者不设上限，按租户天花板
    Timeout        TimeoutConf `json:"timeout"`         // 复用现有 {enabled, seconds}
    Join           string      `json:"join"`            // "collect"（默认）| "drain" | "abort"
    Ceiling        int         `json:"system_ceiling,omitempty"` // 执行器填，恒为正
}
```

`max_concurrency` 是**请求**，租户天花板（`flow_configs.max_concurrency`，代码兜底 16、系统种子行 32）才是答案：执行器在建立作用域之前就把它压到天花板以内 —— `0` 解析成天花板而不是「无界」—— 压低一个显式数字时打一行日志，并把两个数都留在快照里。无界并发放大正是把一张畸形图变成沙箱 `busy` 失败和供应商 429 的原因，而有天花板时它只是一个队列：超出的成员等空位。这也是新建 `process` 节点带 `max_concurrency: 8` 的原因 —— 一个看得见、可改的数字，胜过看不见也就懒得改的「不限」。

快照：**在任何退出路径上**写入宿主的 `NodeResult`。

```go
type ParallelResult struct {
    MaxConcurrency int         `json:"max_concurrency"`          // 实际生效的值
    SystemCeiling  int         `json:"system_ceiling,omitempty"` // 限制它的天花板
    Join           string      `json:"join"`
    Members        []MemberRun `json:"members"`
    Failed         []string    `json:"failed"`
    Abandoned      bool        `json:"abandoned"`
    ExitedBy       string      `json:"exited_by,omitempty"` // 结束本次运行的 exit 节点，否则为空
}

type MemberRun struct {
    NodeID     string     `json:"node_id"`
    ProcessID  string     `json:"process_id"`
    Status     NodeStatus `json:"status"`
    ErrCode    string     `json:"err_code,omitempty"`
    ErrMsg     string     `json:"err_msg,omitempty"`
    StartAt    time.Time  `json:"start_at"`
    EndAt      time.Time  `json:"end_at"`
    DurationMs int64      `json:"duration_ms"`
}
```

这份快照是硬性要求而非点缀：没有它，「到底是不是真并发」只能靠作者自己写计时流程去算，而这正是本方案要替掉的那类测量错误的来源。

## 4. 执行语义

| # | 规则 |
|---|---|
| S1 | 宿主创建 body 进程，从 `entry` 开始走链 |
| S2 | 普通节点就地在链上执行，随后沿其出边继续 |
| S3 | 成员被派发（自己的子进程 + goroutine，登记进作用域），走链**继续沿它的出边前进、不等待**。成员只执行自己，绝不走自己的出边 |
| S4 | 链走完时，body **汇合**本作用域的全部成员 |
| S5 | `max_concurrency` 只限制成员；信号量在成员的 goroutine 内获取，因此限额永不阻塞走链 |
| S6 | 宿主的执行在「链已走完」**且**「全部成员到达终态」时结束 |
| S7 | 每个作用域都是新实例：`loop` 的 body 跑三次就是三个互相隔离的作用域 |

成员不必连续：`A(并发) → C(普通) → D(并发)` 合法，`C` 在 `A` 仍在运行时执行。

`branch` / `intent` 可以当成员。因为它们终结链条，branch 成员必然是走链最后派发的那个节点，其子链在那条车道里跑完——汇合等的是整条子链，不只是条件求值。想让一条车道就是一个条件子流程，或想让同一个宿主里有两条条件车道，各自包一个 `process`。

汇合时的失败聚合：

| 情形 | 块的错误 |
|---|---|
| 失败成员的错误码一致，记为 `X` | `X`，原样透传 |
| 失败成员错误码不同 | `other`，消息里逐成员一行 |
| 有成员在作用域超时前未完成 | `timeout`，附每个成员当时的状态 |
| body 自己的链失败 | 该节点的错误码（链优先于成员） |

## 5. 取消与提前离开 body

| 情形 | 行为 |
|---|---|
| 成员失败且 `join: "collect"`（默认） | 让其余成员跑完，由汇合判定（§4） |
| 成员失败且 `join: "drain"` | 不再启动新的工作：队列里的成员与链上后续节点都不启动（记 `skipped`，原因 `scope draining`）；已在飞的成员跑完，因此它们的终态是真实的 |
| 成员失败且 `join: "abort"` | 在 `drain` 基础上，取消仍在飞的成员（记 `cancelled`，原因 `aborted by scope`）。**正在执行的链上节点不打断** —— 走链在下一个节点边界停下，把「结果未知」限制在成员这一侧 |
| 普通节点失败 | 取消本作用域，返回错误 |
| `break` / `continue` / `return` | **先汇合本作用域**，再向上传递信号 —— 离开 body 意味着这一层的并发已收口 |
| `exit`（在 body 自己的链上，或在成员自己的 body 里） | 停掉本作用域已启动的工作，而不是等它：队列里的成员不再启动（记 `skipped`，原因 `scope terminated by exit at node X`），在飞的成员被取消（记 `cancelled`，原因 `stopped by exit at node X`），随后汇合把信号上抛、宿主记 `success`。`exit` 是全局且立即的 —— 已经结束的运行读不到这些结果，等它们只会推迟结束。`exit` 节点自己永远不是成员（`concurrent_member_type`）：控制流待在链上，由嵌套宿主把信号带出成员 |
| `branch` / `intent` 终结 body | 子链在 branch 车道内跑完，随后执行 body 的汇合 |
| 作用域超时 | 取消成员、打断走链，`timeout` 并附逐成员状态 |
| 运行被取消 | 沿 `StdCtx` 向下传播 |

`abort` 依赖 `ExecContext.WithContext(ctx)`：成员跑在运行上下文的一份浅拷贝上，变量存储和它的锁按指针共享，但取消树是各自的，所以取消一个成员既不会牵连兄弟成员，也不会牵连 body 自己那条链。作用域在 body 开始时派生一次（`BindMembers`），首次成员失败时取消它；作用域超时把触发条件换成 deadline，用的是同一个原语，并且报 `timeout` 而不是它取消掉的那些失败，快照标记 `abandoned`。所有成员都已完成之后才到的 deadline 会被忽略 —— 已经跑完的 body 不算超时。

三个易错细节，必须钉死：

- **谁是「第一个」失败。** 触发策略的那次失败在作用域锁下裁决：单次运行内确定，跨次不保证复现（两个成员在同一瞬间失败时可能互换）。下一次运行里「首要失败成员」换了人，不是 bug。
- **被策略停掉的成员不算失败。** 被取消或从未启动的成员记为 `cancelled` / `skipped` 并写明原因，排除在失败清单之外。一个成员失败、五个被停掉，若报成「6 个成员全部失败」，就是把一个故障说成六个。
- **被 skip 的宿主会发布部分结果。** 宿主配了 `on_error: {skip}` 时，已经写进它 `outputs` 的值仍然可读，下游会看到缺口。这是「变量直接写入」而非「结束时统一收集」的必然结果；只有当下游能容忍缺失时才用 skip。
- **宿主的 `on_error` 是唯一的作用域级处理点。** `process` / `loop` 只声明 `skip` 与 `handler`：`skip` 让主链越过这段可选并发，`handler` 写宿主的兜底 outputs。**不给 `retry`** —— 重跑整个 body 会把每个成员的副作用重复一遍，「重试那个失败的成员」在宿主这一层没有意义。`config` / `internal` 仍然绕过 `on_error`，所以这层兜底盖不住平台故障。

## 6. 可见性

一个节点只能读「生产者已经完成」的变量。可读集合（`done(R)`）为：

```
done(R) =
    { n ∈ chain(body(R)) | index(n) < index(R) ∧ n ∉ members(body(R)) }   // 同链更前、且不是成员
  ∪ { n ∈ chain(ancestorBody) | n 位于「包含 R 的那个节点」之前 }          // 祖先已执行
  ∪ { 已汇合的内层宿主（本身不是成员）的全部成员 }                          // 内层作用域已收口
```

其余一律 `parallel_visibility`（静态错误）：成员永不读兄弟成员或更后的链上节点，链上节点永不读成员。结果只能通过宿主的 `outputs` 跨出 body 边界——成员用 `targets` 写入，汇合之后可读。**不引入映射语法**：`targets` 本来就能写到祖先容器的 outputs（`setVariableNoLock` → `SetLocalVar`，失败再 `SetOutputVar`，经 `FindNodeResultByProcessChain` 解析），现有机制就是完整通道。

这些推论写在这里，是因为它们日后一定会被当成 bug 提出来：

- 两个成员不能互相读；
- 成员之后的链上节点不能读该成员；
- 宿主之后的节点可以读宿主产出的一切。

那该怎么做（免得这条规则只像一堵墙）：

| 你想要 | 应该写成 |
|---|---|
| 成员 B 用成员 A 的结果 | 两个宿主串联：`process { A } → process { B }`——每段内部并行，段与段之间是显式的先后 |
| 对所有成员做一次汇总 | 收集进宿主的 `outputs`，**在宿主之后**汇总 |
| 一个与成员同时进行的串行步骤 | 留在链上（它可以和成员同时跑），但它不能读那些成员 |

这条规则**故意不提供开关**。给了开关就会重新长出「看起来没问题、测过是绿的，直到某次时序变了」——那正是本文要消灭的失败模式。

## 7. 校验

码按"校验什么"分两组：`concurrent_*` 管成员标记本身（站在哪、它标出的节点能碰什么），`parallel_*` 管作用域的运行时形态。

| 码 | 级别 | 条件 |
|---|---|---|
| `concurrent_member_parent` | error | 节点的父节点不是 `process` / `loop`（顶层、`function` 内、`branch` 子链）却标了 `concurrent: true` |
| `concurrent_member_not_on_chain` | error | 父节点是宿主，但节点不在 body 的 entry 链上（branch 子链、悬浮子节点） |
| `parallel_visibility` | error | 违反 §6 |
| `concurrent_member_write` | error | 成员的 `targets` 指向 flow 变量或别的节点的变量；成员只写自己的 outputs / locals 与宿主的 outputs。没有这条，通道就不唯一：`done(R)` 只解析 `source_type: node`，链上节点读一个由成员写过的 flow 变量会直接漏过去 |
| `parallel_output_conflict` | error | 两个节点写宿主的同一个 output 键，且至少一个是成员（两个普通链上节点保持今天的有序覆盖） |
| `parallel_degenerate` | warning | 作用域只有一个成员，或 `max_concurrency: 1` |
| `parallel_fanout` | warning | 嵌套深度 ≥ 3，或同层并发度乘积 > 8 |
| `container_entry` | error | 复用现有规则 |

删除：`wait_fork`、`concurrency_shape`、`concurrency_fork`、`concurrency_group`。

`parallel_fanout` 把宿主自身算作深度 1，并沿外层链把 `max_concurrency` 相乘。只有当每一层都设了正数限额时才乘：无限的一层没有上限可乘，在那里告警等于在每个嵌套宿主的默认配置上告警。两半都是 warning 而不是 error —— 深嵌套可能正是作者想要的，只是不该等跑完看账单才发现。

## 8. 工具、画布、文档

- `create_flow` / `create_node` / `insert_node`：step 支持 `concurrent`；宿主支持 `config.parallel`。`concurrency=mono|group` 参数、`applyConcurrencyPolicy`、`parseConcurrencyPolicy`、`keepConcurrency` 全部删除 —— 已经不存在需要剥离或保留的节点级并发块。
- 节点跨容器移动时必须清掉 `concurrent` 并在 `warnings` 里说明（移动绝不允许悄悄改变并发语义）。
- `read_flow` 返回 `blocks: [{host, members, max_concurrency, join}]`，让「我在哪个作用域里」由工具返回回答，而不是靠推断。
- `read_flow_run` 展示宿主的 `ParallelResult`（成员状态、错误码、耗时）。
- 画布：成员开关、宿主设置面板、作用域的视觉区域，以及成员标题旁的并发徽标（悬停显示勾选项的文案）。`process` / `loop` 的 `?` 帮助主题补充宿主一节。
- 节点参考文档：`process.md` 与 `loop.md` 增加并发章节；删除 `fork.md` 与 `wait.md`；更新 `nodes/README.md` 索引；`terminology.md` 增加 host / body / scope / member 一行。
- 工具提示与帮助主题写明：`branch` / `intent` 终结链条，作为成员时只能是最后派发的节点；想让一条车道成为条件子流程（或需要两条），各自包一个 `process`。

## 9. 删除清单

`fork.go`、`wait.go`（执行器与节点类型）、`ForkResult` / `WaitResult` / `ForkState` / `ForkMember`、`dispatchConcurrent` / `getConcurrency` / `concurrencyConfig`、mono 派发分支与 `MonoWGWait` / `ExecContext.MonoWGAdd|Done|Wait`、`config.concurrency`（`mode`、`fork_node_id`、`max_concurrency`）、「并发默认关闭」的工具策略，以及四个 `concurrency_*` / `wait_*` 校验码。

`mono` **直接删除，不提供替代**：它的实现只是把一个节点丢到后台 goroutine、在运行结束时 `MonoWGWait` 等它，并丢弃其错误，没有任何分批机制；节点参考文档里那句「one node processes several records concurrently」与代码不符。动态批量并行（N 轮迭代同时在飞）是另一个独立设计：loop 的循环变量、outputs、迭代计数与 `break` / `continue` 语义目前都是单槽的，不能靠加一个配置字段实现。

删除是无条件的，**不**沿用 `knowledge` 那套「摘掉调色板入口、为老图保留渲染」的先例。给仍带它们的图不留任何东西：没有 deprecated 别名、没有渲染器、没有迁移脚本、也没有专门诊断 —— 它和任何未知类型一样，落到现有的 `no executor registered`（归为 config 错误）。

删除分两步落地，顺序本身也是设计的一部分：

1. **创建路径（已完成）。** `creatableNodeTypes`、`flowStepTypes`、节点 / 连线 / 流程工具的 `concurrency` 参数、节点参考都不再提供 `fork` / `wait`。写节点时剥掉 `config.concurrency` 的那条逻辑一并删除：已经没有图会带上这个块，普通写入不再被改写。
2. **运行期（已完成）。** `fork.go` / `wait.go`、执行器注册表、`knownTypes`、调色板入口、`ForkNode` / `ForkNodeProps` / `WaitNode` / `WaitNodeProps` / `concurrencyConfig.js`、`ForkResult` / `WaitResult` / `ForkState` / `ForkMember`、`dispatchConcurrent` / `getConcurrency`、mono 分支、画布节点上的节点级并发 UI 与四条校验码一起删，并同步节点参考页（`fork.md` / `wait.md`）与 `terminology.md`。

先删运行期会让调色板继续提供一类「插进去就报 no executor registered」的节点 —— 那正是本节要清理的半留状态。

## 10. 测试矩阵

**执行**

1. 两个成员成功 → 宿主 `success`；`ParallelResult.Members` 两条且带时间戳。
2. 一个成员超时 → 宿主 `failed` 且 `err_code: timeout`（透传，不是 `other`）。
3. 两个成员不同错误码 → `other` 且逐成员一行。
4. `abort`：首次失败取消正在运行的成员，排队成员记 `skipped`，走链在下一个节点边界停下；已执行到一半的链上节点自行跑完。
5. `collect`：首次失败让其余跑完。
6. `drain`：首次失败让在飞成员跑完，但队列里的成员与后续链节点不再启动（记 `skipped`，原因 `scope draining`）。
7. `max_concurrency: 1`、三个成员 → 宿主之后的节点在第一个成员结束前执行（走链未被阻塞）。
8. `loop` 两个成员、三轮迭代 → 三次独立汇合，快照互不串（重入）。
9. 非连续成员 `A → C → D` → `C` 在 `A` 运行时执行；汇合等 `A` 与 `D`。
10. `branch` 终结 body → 含 branch 车道的全部成员都在宿主返回前收口。
11. body 内 `exit`（链上，或成员自己的 body 里）→ 在飞成员被取消、排队成员不再启动；不等待即终止流程，宿主记 `success`。`exit` 节点本身带并发标记会被拒绝（`concurrent_member_type`）。
12. 宿主套宿主 → 内层先汇合。
13. 顶层放一个宿主 → 可用（用一个 `process` 包住整个流程）。
14. 成员失败 + 链失败 → 取链的错误码。

**校验**

15. 顶层节点标 `concurrent` → `concurrent_member_parent`。
16. process 内 branch 子链上的节点标 `concurrent` → `concurrent_member_not_on_chain`。
17. 链上节点读成员输出、以及成员互相读 → `parallel_visibility`。
18. 两个成员写同一宿主 output → `parallel_output_conflict`。
19. 单成员 / `max_concurrency: 1` → warning。
20. 成员的 `targets` 指向 flow 变量 → `concurrent_member_write`。

**回归**：成员失败时，宿主的 outputs 不得留下半截值。

## 11. 分期

**一期 —— 可用且止住假绿。** 结构属性与 `config.parallel`；`BodyScope`（派发 / 汇合 / 快照）；`runBody`；`execute` 签名；`process` 与 `loop` 作宿主（loop 每轮一个新作用域，逐轮留快照）；校验码 `concurrent_member_parent`、`concurrent_member_not_on_chain`、`parallel_visibility`、`concurrent_member_write`、`parallel_output_conflict` 与 `parallel_degenerate` 告警；测试 1、2、5、6、7、8、9、13、15、17、18、19。验收：成员失败在没有 `wait` 的情况下也让运行失败；宿主记录带成员耗时；限额为 1 时走链仍能走到汇合。

**二期 —— 已落地。** 作用域级取消、`abort`（第 4 条测试）与作用域 `timeout`（第 2 条测试）都在 `ExecContext.WithContext` 上落地：变量存储与它的锁按指针共享，成员拿到自己的取消树而不复制整个运行；deadline 是每个 body 一个定时器。`fork` / `wait` / mono 已删除，七个校验码全部落地（含 `parallel_fanout`）。那个「并发段」调色板入口在命名定下来之后又撤掉了：`process` 在 UI 上叫**流程段**，并发设置就在节点自己身上 —— 同一种类型给两个入口，看起来就像多了第二种节点（见 §13）。

## 12. 已定的决策与被否决的备选

已定：

- `join` 默认 `collect`。`abort` 需显式选择：中途取消成员会留下半截副作用，所以它必须是刻意决定，不能是默认值。
- `max_concurrency` 作者侧没有默认值，但运行期永不无界：租户天花板（`flow_configs.max_concurrency`，兜底 16）在运行时生效，嵌套另由 `parallel_fanout` 告警；新建 `process` 节点带 8，让这个数字看得见。`-1` / 缺省的含义是「作者不设上限」，不是「没有上限」。
- `mono` 直接删除、不提供替代；批量并行不在本方案范围（要先有「按轮次归位的结果」和「并行下的 `break` / `continue`」定义）。
- 不为被删除的这一对留兼容层：没有 deprecated 别名、没有为老图保留的渲染器、没有迁移脚本。一个只为渲染而活着的类型，会把 fork/join 模型继续留在代码与文档里 —— 那正是要被替换掉的东西。
- 宿主只有 `process` 与 `loop`，因此**顶层不是宿主**：从头并行到尾的流程要把 body 包进一个 `process`。作用域需要一个所有者来承载 `max_concurrency` / `timeout` / `join`，而「全局并发」正是过去没人负责、却照样报绿的东西。
- `branch` / `intent` 可以当成员。它们本来就是 entry 链上的合法子节点，禁止属于人为设限；因为它们终结链条，所以只能是最后派发的节点，一条条件车道请自己包 `process`。
- 结果只经宿主既有的 `outputs`（由 `targets` 写入）送出，不引入 `outputs: {from: …}` 映射语法。
- 成员标记落在 `data.concurrent`——既不进 `config`（会被整体替换），也不与 `data` 同级（画布只保证透传 `data`）。
- 可见性是硬规则、不给逃生门。替代写法只有「串联宿主」或「把消费方挪到宿主之后」。

被否决，以及各自被否决的理由：

- **保留 `fork` + `wait`、强制成对。** 补上了缺失的汇合，但成员资格仍是逐节点声明，组的结果仍无通道，且留着一个只承担布尔值的节点类型。
- **用链区间 `[fork, wait)` 派生组。** 作为静态模型是对的，但区间内的边会变成「只影响布局」，这个解释要永远背着；而且区间外的成员依然表达得出来。
- **任何节点都能标 `concurrent`，只要父节点是 `parallel` 块。** 评审后否决：想让「loop body 里的两个节点并发」要付结构代价（建容器、搬节点），并且新增了一个除了作用域之外没有任何含义的节点类型。
- **任何容器都能标 `concurrent`。** 同一推理再走一步：body 本来就有开始与结束，作用域不需要除 body 本身之外的所有者。

## 13. 未决问题与明确的范围外事项

- 批量并行（N 轮迭代同时在飞）不在本方案范围。将来要做，需要先设计「按轮次归位的结果通道」，以及「多轮在飞时 `break` / `continue`」的语义。
- 作用域被放弃（超时）时，宿主记录是否需要额外持久化合流结论，还是 `Abandoned` + 成员状态就够？目前是后者。
- `process` 作为并发宿主之后该叫什么？已定：**流程段**（与「并发作用域」这栏设置成对），调色板上只有它一个入口。先前那个「插入并发段」预设已撤掉 —— 两个入口建出同一种节点、其中一个还悄悄预填了 `parallel`，看起来像两种节点，反而把设置藏在哪儿变得更难发现。
