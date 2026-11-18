# sandboxd 脚本执行重构：scripthost 常驻宿主程序

**状态**：定稿，待实现（分 5 阶段）
**日期**：2026-10-16
**范围**：`sandboxd` worker 脚本执行路径（`worker.lua/py/js` → `scripthost`）+ `nexus` 脚本回调协议裁剪；不改 docker 隔离路径

---

## 1. 定位（一切细节由它推出）

> **用语言无关的常驻宿主程序 `scripthost` 替代 `worker.lua/py/js`。**

一句话分工：**隔离单元复用摊掉启动成本，进程级 fork 保证干净，协议单向化砍掉交互。**

三个核心决策：

1. **复用隔离单元**：scripthost 本身常驻在已建好的隔离单元里（nsjail/none），把 nsjail 启动成本摊掉。
2. **每次 fork 全新解释器进程**：彻底干净、保留真 lua/python/node、能扩包，代价是放弃解释器进程复用。
3. **砍掉交互**：`FrameCallback` / `FrameCbResp`、`wf_*` 回调全删，协议退化成单向 `execute/done/error`，脚本 `print` 走 stderr。

---

## 2. 为什么需要（问题陈述）

现有 worker 路径的问题（推动重构的原因）：

- 语言 worker（`worker.lua/py/js`）各自维护一份生命周期/协议逻辑，语言相关与协议相关耦合。
- 交互式回调（`FrameCallback` / `FrameCbResp`、`wf_*`）引入双向协议复杂度，而实际脚本执行并不需要。
- 解释器进程复用（reuse）带来的「软重置」状态不彻底，残留状态是隐患。

结论：换成一个语言无关的常驻宿主，fork 语言解释器来跑脚本。

---

## 3. scripthost 设计

### 3.1 一份代码，fork 三种解释器

- scripthost 是**语言无关**的常驻宿主程序，一份代码 fork 三种解释器（lua / python / node）。
- 内嵌三份**包装模板**，把用户脚本包进「能拿到返回值、能收 stdout/stderr」的壳：
  - **lua**：`load(...)` 加载脚本
  - **python**：`exec(...)` + 约定 `__return__` 变量回传结果
  - **node**：IIFE 包裹

### 3.2 隔离模式：只上 nsjail / none

| mode | 处理 |
|---|---|
| `none` | scripthost 本机 fork |
| `nsjail` | scripthost 放 rootfs 内，在 jail 里 fork |
| `docker` | **保持现状**——已有 L2 等价能力，零改动 |

---

## 4. 协议退化：单向 execute / done / error

- 删 `FrameCallback` / `FrameCbResp` 及全部 `wf_*` 回调。
- 协议退化成单向：`execute` → `done` / `error`。
- 脚本 `print` 走 **stderr**（区分「结果」与「人看的输出」）。

---

## 5. 性能与权衡

| 项 | 变化 | 结论 |
|---|---|---|
| lua | reuse（µs 级）→ fork（2–5ms） | **已接受** |
| 默认策略 | 按需 fork | 实测瓶颈后再叠「预 fork 预热」 |

即：先按需 fork 走通，若实测成为瓶颈，再考虑「预 fork 预热」优化，不预设。

---

## 6. 分阶段落地（5 阶段）

| 阶段 | 内容 | 状态 |
|---|---|---|
| ① scripthost 本体 | 协议 + 三份包装模板 + fork + 超时 + 僵尸回收 | ✅ 完成 |
| ② sandboxd 接入（none） | `spawnWorker` → `spawnScripthost`；删 L3 池 / 软重置 / lifecycle 分档 | ✅ 完成 |
| ③ nsjail | rootfs 放 scripthost | 待做 |
| ④ nexus 砍回调 | `client.go` / `script.go` / `executors` 相关回调链路裁剪 | 待做 |
| ⑤ 清理测试 | 删 worker 脚本、更新 `sandbox-design.md` §9 | 待做 |

---

## 7. 决策留痕（已评估并推翻，勿重走）

| 曾经的方案 | 为何推翻 |
|---|---|
| 继续用 `worker.lua/py/js` 三份 worker | 语言与协议耦合、交互回调复杂；统一为 scripthost 一份代码 |
| 保留解释器进程复用（reuse + 软重置） | 状态不彻底、有残留隐患；改为每次 fork 全新解释器 |
| 保留 `FrameCallback` / `FrameCbResp` / `wf_*` 交互协议 | 脚本执行本不需要双向交互，砍掉退化为单向 |
| docker 也上 scripthost | docker 已有 L2 等价能力，零改动保持现状 |

---

## 8. 附：待办验证项

- ✅ 阶段 ②：`mode=none` 接入与 L3 池删除已完成，`scripthost_integration_test.go` 端到端回归通过（python / node / lua 结果、超时、常驻复用）。
- ✅ Lua 模板已在 lua 5.5（`d:/tools/lua5.5/lua55.exe`，none 模式入口）下验证：结果回传、print→stderr、error 三路径全绿。
- 阶段 ④ 完成 nexus 侧回调裁剪后，确认 `script.go` / `client.go` 无残留回调引用。
- 阶段 ⑤ 更新 `sandbox-design.md` §9，同步 scripthost 定位与协议口径。
