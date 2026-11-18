# sandboxd exec 契约正名：`handle_exec` / `remote` 注释

**状态**：已实现并验证（4.1 + 4.2 + 4.3；build / gofmt / 相关测试全绿）
**日期**：2026-10-15
**范围**：`sandboxd/internal/daemon/handle_exec.go`、`tools/sandbox/remote.go`、`tools/sandbox/wire.go` 三处注释。**不改任何代码逻辑、配置、协议。**

---

## 1. 定位

> **「单工」= 不池化（每次新建、跑完即弃、无回调通道），不等于「单向」，也不等于「短命」。exec 的线上协议本就是完整双向流。**

一句话：长驻的交互式执行**直接复用 exec 路径即可，不新增端点**；要动的是把这个语义**写进注释**，不是改 exec 的代码。

---

## 2. 为什么需要（问题陈述）

背景问题：需要一个「长期运行的执行会话」，应复用 `/v1/exec` 还是新开端点？
**结论：复用。** 因为 exec 已经是「一个进程 + 一条双向流」，且**没有 interact 那种内建硬超时**。

但现有注释把这个能力的描述引向了反方向 —— 读者容易误判「exec 撑不住长驻会话」而重复造端点。

### 2.1 现状（不正之处）

- `handle_exec.go:16`「一次提交 → 执行 → 返回输出与退出码」——读起来像请求/响应，掩盖了它是流式双向的。
- `handle_exec.go:18`「单工永不入池：进程跑完即弃」——「跑完即弃」容易被读成「短命」。
- `remote.go:21`「一次性进程执行」、`remote.go:23`「单工（one-shot）语义」——把单工直接等价于 one-shot。
- `wire.go:32`「单工带 Spec，双工带 Lang」——语言路径其实也是单工在走。

### 2.2 事实（代码依据）

| 事实 | 依据 |
|---|---|
| 双向：stdin 续写 / 半关闭 / kill 帧 | `handle_exec.go:89-113`（`ChanStdin`、`FrameStdinEOF`、`FrameKill`） |
| stdout、stderr 分流回传 | `handle_exec.go:74`、`:116` |
| **无内建硬超时** | `handle_exec.go:76` 只有 `context.WithCancel`，无 deadline；超时只来自 `limits.timeout_sec`（`ExecLimits`，0 = 不限时） |
| 对照：双工才有硬闸 | `handle_interact.go:36-50`（`interactDeadline` / `interact_max_sec`） |
| 「双工」的「双」= 回调 + 池 | `handle_interact.go:52`「脚本 + 回调」；`server.go:23`「按语言的槽位池 + 并发闸」 |

即：exec 与 interact 的差别是**池 / 回调 / 硬闸**，不是「单向 vs 双向」。

---

## 3. 术语决策：保留「单工」，只正语义

`单工/双工` 在仓库出现 80+ 处（`config.go` / `server.go` / `wire.go` / `sandbox.go` / `handle_interact.go` …），是「exec 路径 vs interact 路径」的既定简称。**不改名**（改名牵动全仓、且与既有注释冲突），只在三个锚点注释里把含义写准。

---

## 4. 变更清单（全部已实现）

### 4.1 `sandboxd/internal/daemon/handle_exec.go`（第 16–24 行）

旧：

```go
// handleExec 单工端点：一次提交 → 执行 → 返回输出与退出码。
//
// 单工永不入池：进程跑完即弃，池化它需要「重置进程状态」，
// 成本比直接启动还高，且没有复用收益。
```

新：

```go
// handleExec 单工端点：一条连接 = 一个进程，跑到退出为止。
//
// ⚠️ 「单工」在这里指**不池化**（每次新建、跑完即弃、无回调通道），
// 不是「单向」也不是「短命」：线上协议是完整双向流 —— stdin 可续写、可半关闭，
// stdout / stderr 分流回传，还支持 kill 帧。同时它**没有**双工那样的内建硬超时
// （对比 interactDeadline）：超时只来自 limits.timeout_sec（用途默认 → 请求覆盖 →
// 全局 clamp，0 = 不限时）。长驻的交互式命令直接走这条路径即可，不必新增端点。
//
// 为什么不入池：池化它需要「重置进程状态」，成本比直接启动还高，且没有复用收益。
```

只重写 doc comment；函数体、`buildExecArgv` 的注释不动。

### 4.2 `tools/sandbox/remote.go`（第 21–30 行）

旧：

```go
// RemoteSandbox 通过 WebSocket 把「一次性进程执行」委托给远端沙箱服务（sandboxd）。
//
// 只承载单工（one-shot）语义：Start → 写 stdin → 读 stdout → Wait。
// 双工（脚本 + 回调）请用 script.Client。
```

新：

```go
// RemoteSandbox 通过 WebSocket 把「每次新建进程」的执行委托给远端沙箱服务（sandboxd）。
//
// 只承载单工（exec）语义：一条连接对应一个进程，Start → 写 stdin → 读 stdout → Wait。
// 这里的「单工」指不池化、不带回调，**不是**单向或短命 —— 协议同样是完整双向流
// （stdin / stdout / stderr + 半关闭 + kill），且服务端没有内建硬超时，
// 是否限时完全取决于 Spec.Limits / 服务端 limits.timeout_sec。
// 双工（脚本 + 回调、走池并受 interact_max_sec 约束）请用 script.Client。
```

第 29–31 行（`注意：Spec.Mounts …`）原样保留。第 13 行 `Endpoint`（「单工执行端点」）、第 32 行 `NewRemote`（「远端单工沙箱」）与本节口径一致，不动。

### 4.3 `tools/sandbox/wire.go:32`

旧：

```go
FrameOpen     = "open"      // 客户端 → 服务端：开始（单工带 Spec，双工带 Lang）
```

新：

```go
FrameOpen     = "open"      // 客户端 → 服务端：开始（单工可带 Spec，含可选 Lang；双工只带 Lang）
```

理由：语言路径其实也是 exec 在走（`handle_exec.go:43` `buildExecArgv(f.Lang, ...)`），原句把 Lang 独占给了双工。

---

## 5. 明确不改的东西

- 任何函数体、协议帧、配置键、校验逻辑。
- `单工/双工` 术语本身（不做全仓改名）。
- `handle_interact.go` / `config.go` 的既有注释（它们对单工/双工的表述是准的）。
- `server.go:22` 的 `exec` 字段注释（「单工隔离实现…永不入池」）——与本次口径一致，默认不动。

---

## 6. 验证（实测）

纯注释改动，无行为变化。实跑结果：

| 命令 | 结果 |
|---|---|
| `gofmt -l` 三个改动文件 | 无输出 ✓ |
| `go -C tools build ./...` | exit 0 ✓ |
| `go -C sandboxd build ./...` | exit 0 ✓ |
| `go -C tools test ./sandbox/...` | `ok flame/tools/sandbox 0.624s` ✓ |
| `go -C sandboxd test ./internal/daemon/...` | `ok flame/sandboxd/internal/daemon 1.377s` ✓ |

注：仓库根 `go build ./...` 不适用 —— 根 `go.mod` 无包，实际模块是 `nexus/`、`sandboxd/`、`tools/`、`nexus_gw/`、`test/`、`test_kb/`。本次只涉及 `sandboxd` 与 `tools` 两个模块。

---

## 7. 风险

- 极低：仅注释。唯一风险是措辞失准再次误导 —— 故本方案把「不池化 ≠ 单向/短命」写成显式断言。
- 若日后 exec 真加了内建硬闸，须回改 `handle_exec.go` 注释（注释里已点名 `interactDeadline` 作对照，便于检索）。

---

## 8. 决策记录

1. 「单工」保留不改名（只正语义）—— 已按此执行。
2. `wire.go:32`（§4.3）—— 已一并修。
