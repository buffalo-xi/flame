# `function` 节点

可被 `call` 调用的内联函数容器（**不是**子流程调用，子节点仍是本 flow 的图形节点）。

## config 形状

```json
{
  "params": {"a": {"type": "number", "description": "第一个加数"}},
  "outputs": {"sum": {"type": "number", "description": "和"}},
  "locals": {}
}
```

## 字段

| 键 | 必填 | 缺省 | 说明 |
|---|---|---|---|
| `params` | — | `{}` | 形参声明 `{type, required, description}`；`required` 缺省 `true`，`call` 的 `args` 必须与其键集合一致 |
| `outputs` | — | `{}` | 返回值声明；`call` 会把目标函数的 `outputs` 同步成自己的输出 |
| `locals` | — | `{}` | 局部变量 |

**不读** `inputs`。直接进入 `function` 节点（不是由 `call` 进来）时参数为空。

## 结构要求

- 必须有 `entry` 子节点（`parentNode = function.id`，`label: 开始`）。
- 体内链从 entry 沿第一条出边跑到底；每次调用新建子进程。

## 控制信号（与 process 不同）

| 节点 | 效果 |
|---|---|
| `return` | 函数**正常返回**（信号被吞掉），`call` 拿到输出 |
| `exit` | 一路向上传播，终止外层 |
| `break` / `continue` | 函数体内报 `config` 错误（只能在 loop 里用） |

## 出边

作为普通节点也有第一条出边；但通常只通过 `call` 进入，外部不必连。

注意：后端对 `function` 也读 `on_error`，但**前端没有 on_error 面板**，需要在别处（如 API）写。

源码：`service/flow/executors/function.go`
