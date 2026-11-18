# `loop` 节点

容器节点：重复执行自己的子节点（`parentNode = loop.id`），从自动生成的 `entry` 子节点开始。

## config 形状

```json
{
  "loop": {"type": "count", "total_times": 10},
  "loop_vars": {"index": {"type": "number", "description": "循环下标"}},
  "inputs": {},
  "outputs": {},
  "locals": {}
}
```

## `loop` 四种形态

| type | 键 | 说明 |
|---|---|---|
| `count` | `total_times`（number） | 固定次数；`<=0` 时取系统上限 `ResolveMaxLoopTimes()` |
| `each` | `source_key`（string）、`break_on_empty`（bool） | `source_key` 必须是 **`config.inputs` 里的同名键**，取其数组逐项遍历；空数组 + `break_on_empty=true` 直接结束；元素为 `null` 且该开关为真时中断 |
| `while` | `lang`（缺省 lua）、`condition`（脚本）、`max_times`（number，`<=0` = 系统上限） | **先判断后执行** |
| `do_while` | 同上 | **先执行至少一轮**再判断 |

`total_times` / `max_times` **必须是数字**。类型不对（例如导出的资产包里变成字符串 `"5"`）直接报
`config` 错误，不再静默按 `<=0` 取系统上限 —— 那种静默会把「跑 5 次」放大成「跑满上限」。

`while` / `do_while` 的条件脚本支持 `loop.timeout = {enabled, seconds}`；关闭（或未配置）时不设节点级超时，由运行上限兜底。

`loop_vars` 声明循环变量（`index` / `item` 或自定义），可在子节点里引用。

`loop_vars` 与 `outputs` 是两条通道，别混：循环变量（下标、当前元素）写 `loop_vars`，循环体产生的普通返回值写 `outputs`（子节点用 `targets` 回写，`source_ref` 指向 loop 节点）。写到错的通道里，取值方会拿不到。

## 循环体与「循环之后」

- 循环体 = 子节点链，入口是 `loop` 的 `entry` 子节点（`label: 入口`），沿第一条出边跑到底；每轮新建子进程。
  找不到入口时运行期报 `config` 错误，**不会**静默空转 N 轮。
- **循环之后** = `loop` 节点自己的**唯一一条出边**（没有独立的 body / next handle）；连第二条会报 `config` 错误。
- 子节点用 `targets`（`source_ref: <loop id>`）把值写回 `loop` 的 `outputs`；`iterations` 记录实际轮数。

## 循环里的控制信号

| 节点 | 效果 |
|---|---|
| `break` | 结束本层循环，循环节点成功 |
| `continue` | 跳过本轮剩余节点，进入下一轮 |
| `return` / `exit` | 循环记成功并**向上传播** |
| 其它错误 | 循环失败，带上轮次 |

## 常见错误

- `loop config not found` / `unknown loop type`。
- `each 数据源在 inputs 中不存在` —— `source_key` 没在 `config.inputs` 里声明。
- `数据源不是数组类型` —— 上游给的不是数组。
- 子链里的 `break` 报错 —— 它不在任何 loop 内（多半是子节点被挪到容器外了）。

源码：`service/flow/executors/loop.go`
