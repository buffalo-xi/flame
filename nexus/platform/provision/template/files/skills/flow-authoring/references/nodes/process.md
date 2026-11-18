# `process` 节点

内联子流程容器：把一组节点收进一个框里顺序执行，跑完回到主链。

## config 形状

```json
{
  "outputs": {"summary": {"type": "string", "description": "子流程产出"}},
  "locals": {}
}
```

## 字段

| 键 | 必填 | 缺省 | 说明 |
|---|---|---|---|
| `outputs` | — | `{}` | 子流程对外暴露的变量；由**内层节点**用 `targets`（`source_ref: <process id>`）写入 |
| `locals` | — | `{}` | 容器内局部变量 |
| `concurrency` | — | 不写（串行） | **容器永远不带这一项：工具会忽略这里写的 `concurrency` 并回 warning**；并发（默认关闭，只有用户明确要求才开）配在组内的**成员节点**上（`{"mode":"mono"}` 或 `{"mode":"fork","fork_node_id":"<id>"}`，见 `README.md`） |

**不读** `inputs`，也**没有**「调用另一个流程」的引用键 —— 子节点就是同一个 diagram 里
`parentNode = process.id` 的节点。

## 结构要求

- 容器内必须有 `entry` 子节点（`type: "entry"`、`parentNode: <process id>`），前端建容器时自动生成。
- 没有 entry → 运行期报 `config` 错误（`process node X: body entry node not found`）。
  以前是静默跳过，等于整个子流程没执行却看不出来。
- 体內链从 entry 沿第一条出边跑到底；每次执行新建子进程。
- 容器自己的出边只能有一条；连第二条会报 `config` 错误（不是"只走第一条"）。

## 控制信号

`break` / `continue` / `return` / `exit` 都以成功收尾并**向上传播**（不会在容器里被吞掉）。

## 出边

容器跑完后，沿 `process` 节点自己的**第一条出边**继续。

源码：`service/flow/executors/process.go`
