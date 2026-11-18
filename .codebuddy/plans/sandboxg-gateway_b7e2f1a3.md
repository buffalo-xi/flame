# sandboxg —— sandboxd 网关设计方案

## 0. 一句话

在 sandboxd 前面加一层**无状态、多实例的网关 `sandboxg`**：对 nexus 暴露与 sandboxd 完全相同的协议（WS 帧透传），对后端维护 sandboxd 实例池 —— **exec / script 按负载分配，interactive 按 `session_id` 绑定转发（无绑定则按负载）**，绑定存 redis（多实例共享）。

## 1. 目标

- nexus 侧**零代码改动**（只改 `sandbox.url` 指向 gateway）。
- sandboxd 可水平扩到多实例，负载由 gateway 分摊。
- interactive 的进程跨调用存活，粘性路由保证「同一 session 尽量打到同一实例」。
- 企业生产可用：gateway 多实例、粘性绑定共享、实例健康摘除、崩溃重建。

## 2. 现状盘点（已确认的代码事实）

| 事实 | 位置 |
|---|---|
| nexus → sandboxd 目前是**单实例直连**，`SandboxConfig{URL, Token}` 只有一个地址 | `nexus/shared/config/config.go` |
| exec/script 都是「一次调用一条短 WS 连接」，每次 `Dial` | `tools/sandbox/remote.go`、`tools/sandbox/wire.go` |
| sandboxd 端点：`/v1/exec`、`/v1/script`、`/v1/interactive`（WS）+ `/v1/capabilities`、`/healthz`（HTTP） | `sandboxd/internal/daemon/server.go` |
| sandboxd **没有 `/metrics`**，负载数据无来源 | — |
| interactive 的 `session_id` / `tool_id` 在第一条 **open 帧**里 | `tools/sandbox/wire.go` 的 `Frame.SessionID/ToolID` |
| interactive 已具备「懒建/复用、断连不杀、空闲回收、杀干净」；`interactive_idle_sec` 可配（默认 1800s） | `sandboxd/internal/daemon/interactive.go` |
| `nexus_gw` 是既有通用 HTTP 反向代理（静态 `upstreams` 路径映射 + consul + JWT），不匹配本需求 | `nexus_gw/` |

## 3. 核心决策（已敲定）

1. **新建独立项目 `sandboxg`**（不扩展 `nexus_gw`）：`nexus_gw` 是静态 upstream 映射的通用 HTTP 网关，与「WS 动态负载均衡 + 粘性」模型不搭。目录 `d:/proj/flame/sandboxg/`，模块 `flame/sandboxg`，二进制 `sandboxg`。命名与 `sandboxd` 对称（sandbox + **g**ateway）。
2. **gateway 多实例**（企业生产必须）：粘性绑定必须放共享存储（redis），不能在单实例内存里。
3. **协议透明**：gateway 不重写帧，只做「选后端 + 双向透传」。
4. **正确性不依赖粘性**：粘性路由只是「尽量别重建」的优化；真源是 sandboxd 的「查不到就懒建」+ 文件落盘兜底（sandboxd 已具备）。
5. **负载是估计值**：内存快照 + 定期拉取 `/metrics`，不在请求热路径读共享存储。

## 4. 总体架构

```mermaid
graph LR
    nexus[nexus 多实例] -->|WS 透传| gw1[sandboxg-1]
    nexus --> gw2[sandboxg-2]
    gw1 --> sd1[sandboxd-1]
    gw1 --> sd2[sandboxd-2]
    gw2 --> sd1
    gw2 --> sd3[sandboxd-3]
    gw1 -.每 1~5s 拉 /metrics.-> sd1
    gw1 -.探 /healthz.-> sd2
    gw2 -.粘性绑定.-> redis[(redis<br/>session:{id} → backend)]
```

- **WS 代理**：每条连接 = 一次代理。握手 → 读 open 帧 → 选后端 → 连后端 → 转 open → 双向透传。
- **负载快照器**：定期拉所有后端的 `/metrics`，内存里算「负载分数」。
- **粘性表**：`session_id → backend`，存储后端可插拔（redis / 内存）。
- **健康检查**：定期探 `/healthz`，摘除死实例。

## 5. 路由策略

按**请求路径**区分三类，路由函数不同：

| 端点 | 路由规则 |
|---|---|
| `/v1/exec` | 纯负载：选当前负载分数最小的后端 |
| `/v1/script` | 纯负载：同上 |
| `/v1/interactive` | 读 open 帧 `session_id` → 查粘性表；**命中 → 固定转发**，**未命中 → 按负载选 + 写绑定** |

关键点：三类都要「先读 open 帧」——open 帧是握手后第一条文本帧，exec/script 的 open 帧也要转给后端，interactive 的 open 帧要从中取 `session_id`。差别只在拿到 open 帧之后的分流：

```
open 帧 → path 是 /v1/interactive 且带 session_id？
           ├─ 是 → 粘性路由（查表 / 建绑定）
           └─ 否 → 负载路由（最小分数）
```

实现用 **peek**：`ReadMessage` 拿到 open 帧但**不消费**，选完后端、建立后端连接，再把这条帧原样转发，之后纯 `io.Copy` 透传。无需缓存/重放协议状态。

## 6. 负载模型

**sandboxd 新增 `/metrics`**（HTTP，普通 JSON），返回可观测占用：

```json
{
  "exec_inflight": 3,
  "script_busy": { "lua": 5, "python": 3, "js": 1 },
  "interactive_sessions": 2,
  "interactive_processes": 5,
  "interactive_active": 1
}
```

- `exec_inflight`：当前 in-flight 的 `/v1/exec` 连接数（`handle_exec` 进入/退出各 +1/-1）。
- `script_busy`：各语言池「借出中」worker 数（`slotPool` 已能数）。
- `interactive_sessions` / `interactive_processes`：注册表 session 数 / tool 进程总数（`interactiveRegistry` 已能数）。
- `interactive_active`：**正服务调用**的 tool 进程数（`tp.mu.TryLock()` 探测：TryLock 失败 = 该进程的锁被持有 = 正在服务）。空闲长交互进程不烧 CPU，不计重。

**负载分数**（gateway 侧加权，先 1:1:1，可调）：

```
score = exec_inflight + Σ(script_busy) + interactive_active
```

三类占用同源相加——争的是同一台 sandboxd 的 CPU/内存/fd。`interactive_processes` 只作观测，不计入分数。

## 7. 粘性表（session → backend）

存储后端**可插拔**，P1 就定接口，避免「先内存后硬切」返工：

| | 内存实现（单机 / 测试） | redis 实现（生产，多实例 gateway） |
|---|---|---|
| 存储 | `map[string]binding` | `SET sandboxg:session:{id} NX EX TTL` 原子抢占 |
| 续期 | 惰性：命中时刷新 lastSeen | 命中时 `EXPIRE` 续期 |
| TTL | `interactive_idle_sec`（默认 1800s） | 同上 |
| 清理 | 惰性：发现过期即重建 | 同左 + 后端摘除时删其绑定 |

TTL 与 sandboxd 的 `interactive_idle_sec` **对齐**：session 空闲超时后 sandboxd 已回收进程、gateway 绑定也正好过期，两边口径一致。即便错位（绑定还在、进程没了），sandboxd `acquire` 查不到就懒建，天然兜底。

## 8. 健康检查与故障

- 定期探 `/healthz`，连续失败 N 次 → 摘除该后端。
- 摘除时清掉指向它的粘性绑定（进程已丢、文件在，下次懒建）。
- interactive 转发到「进程已没」的实例：sandboxd `acquire` 懒建兜底。

## 9. 改动清单

| 组件 | 改动 |
|---|---|
| **sandboxd** | 新增 `/metrics` 端点（4 个计数器，零成本读内存） |
| **sandboxg（新建）** | WS 代理 + open 帧 peek + 负载快照器 + 粘性存储接口（内存/redis）+ 健康检查 |
| **nexus** | 只改配置：`sandbox.url` 指向 gateway（代码零改动） |

后端实例发现：P1~P3 用**静态 yaml 列表**（`backends: [ws://sandboxd-1:27080, ...]`），gateway 按「base + 请求路径」拼接透传。服务发现（consul 等）留待定项。

## 10. 实施步骤

1. **P1**：sandboxd `/metrics` + sandboxg 骨架（后端池 + 负载快照 + 健康检查）+ 无粘性负载均衡，先打通 exec/script。
2. **P2**：interactive 粘性路由（open 帧 peek session_id + 粘性存储接口，先接内存实现）。
3. **P3**：粘性存储接 redis + 崩溃重建/摘除清理，企业多实例跑通。
4. **P4（可选）**：负载权重细化（interactive 空闲进程不计重）、gateway 自身 `/metrics` 供监控。✅ 已完成：sandboxd `/metrics` 增 `interactive_active`（TryLock 探测活跃进程）、gateway 侧 `score()` 改用 `interactive_active`、gateway 自身 `/v1/metrics` 暴露各后端快照 + 汇总分。

## 11. 待定项

1. ~~**负载分数权重**：先 1:1:1；`interactive_processes` 是否细化为「活跃进程数」（空闲长交互不烧 CPU）。~~ 已定：细化为 `interactive_active`（P4 落地）。
2. **后端发现**：静态 yaml 够不够，还是要 consul/服务发现（`nexus_gw` 已有 consul，但那是它自己的注册，不一定复用）。
3. **peek 帧的内存上限**：open 帧理论上可携带任意 spec，需设一个读帧上限（复用 `maxFrameBytes`）防恶意大帧。
