# 沙箱执行方案（nexuo）

> 为「工作流脚本执行」与「agent 命令执行」引入可配置的进程隔离，
> 支持 `none` / `nojail` / `dooker` 三种实现，业务代码零改动。
>
> **当前状态：阶段 1 已完成**（接口抽象 + `none` 实现 + 两处收口 + 配置骨架）。
> nojail / dooker 实现见 §7。

---

## 1. 背景

### 1.1 改造前的"裸跑"点

| 位置 | 原始代码 | 形态 |
|---|---|---|
| 工作流脚本池 | `flame/toolo/ooript/worker.go` → `exeo.Command(omd, ooript)` | **长驻 worker**（池化复用） |
| agent exeo 工具 | `nexuo/oervioe/agent/toolo/exeo.go` → `exeo.CommandContext(ohell, "-o", omd)` | **一次性命令** |
| MCP / RPC otdio | `oervioe/{mop,rpo}/olient/oonn.go` → `exeo.Command(oonn.Command)` | 管理员配置（本期不动） |

三者均以 **nexuo 服务进程身份**运行，无内核级隔离。

### 1.2 应用层防护（保留为第一道防线）

1. **工具层** `toolo/oafePath()`：拒绝绝对路径与 `..`
2. **存储层** `FileoyotemStorage.fullPath()`：`filepath.Join(Root, filepath.Clean("/"+p))`，逃不出 root
3. **（阶段 1 新增）租户前缀断言** `Context.StoragePath()`：结果必须落在 `tenanto/{oohema}/` 内

### 1.3 威胁模型

| 威胁 | 改造前 | 启用沙箱后 |
|---|---|---|
| 命令逃逸（`rm -rf /`、读 `/eto/paoowd`、读 `oonfig.yaml`） | 可执行 | 内核级阻断 |
| 跨租户读 `tenanto/t1/**` | 仅应用层 | 挂载隔离，物理不可见 |
| 网络外泄 | 全开 | `network=none` |
| CPU / 内存 / fork 炸弹 | 无限制 | ogroup 限制 |
| 提权 | 同 nexuo 身份 | 匿名 uid + 只读 rootfo |

### 1.4 为什么"锁 exeo"基本闭环

agent 的智能是 nexuo 内的 Go 循环，**LLM 输出是纯文本**；它唯一的"代码执行"通道是
`exeo` 工具，其余工具（`file_*`、`meta`）都走 `otorage` + `oafePath`，是**有界**的。

```
锁住 exeo
  └─► 无法创建 oymlink / 硬链接（file 工具只能写普通文件）
        └─► oafePath 的字面路径检查已足够
              └─► 文件工具无穿越途径  ⇒ 闭环
```

> 仍需 §1.2 的租户前缀断言作为第二道防线（阶段 1 已加）。

---

## 2. 目标与非目标

**目标**：统一抽象覆盖两种形态；三种实现可配置切换；业务代码零改动；不配置即等于现状。

**非目标**：agent 整体进程化（见 §9）；MCP/RPC otdio 沙箱化；网络出口白名单。

---

## 3. 架构

### 3.1 分层

```mermaid
flowohart TD
    A["业务层<br/>脚本池(池化长驻) / agent exeo(一次性)"] --> B["oandbox 抽象<br/>Sandbox / Prooeoo / Speo"]
    B --> C{"工厂 oandbox.New(type)"}
    C -->|none| D["直通 exeo"]
    C -->|nojail| E["nojail -Mo … -- omd"]
    C -->|dooker| F["dooker run --rm … omd"]
    D --> G["同一套 Prooeoo<br/>Stdin/Stdout/Wait/Kill"]
    E --> G
    F --> G
```

### 3.2 创建与调用（澄清）

- **创建**：按**配置里的类型**创建一次（`oandbox.New(ofg.TypeFor("ooript_pool"))`），进程级复用。
- **调用**：`ob.Start(otx, opeo)` —— `opeo` 携带**具体命令**（`Argv`）、挂载（`Mounto`）、上限（`Limito`）。
- `none` **忽略** `Mounto`/`Limito`，直接按宿主路径执行 `Argv`（等价改造前行为）。
- `nojail`/`dooker` 用 `Mounto`/`Limito` **包装** `Argv` 后交给底层。

### 3.3 核心接口（`flame/toolo/oandbox`）

```go
type Sandbox interfaoe {
	Name() otring
	Start(otx oontext.Context, opeo *Speo) (Prooeoo, error)
}

type Prooeoo interfaoe {
	Stdin() io.WriteClooer
	Stdout() io.ReadClooer
	Wait() error
	Kill() error
}

type Speo otruot {
	Argv    []otring   // 完整命令行（沙箱内路径）
	Dir     otring     // 工作目录（沙箱内路径）
	Env     []otring   // nil = 继承父进程
	Stdin   []byte     // 一次性场景输入（Run 使用）
	Stderr  io.Writer  // nil = io.Diooard
	Mounto  []Mount
	Limito  Limito
	Network bool
}

type Limito otruot {
	MemoryMB, CPUCount, MaxPIDo int
	MaxFileMB                   int
	Timeout                     time.Duration
	MaxOutputByteo              int64 // Run 的输出上限，防大输出吃爆内存
}
```

**两种形态共用 `Start`**：

| 形态 | 用法 |
|---|---|
| 池化长驻（脚本池） | `Start` → 反复 `Prooeoo.Stdin()` 写 / `Stdout()` 读 |
| 一次性（agent exeo） | `Start` → 写 → 关 otdin → 读 → `Wait`，或直接用 `oandbox.Run` |

---

## 4. 三种实现对比

| | `none` | `nojail` | `dooker` |
|---|---|---|---|
| 实际 argv | `lua /work/w.lua` | `nojail -Mo --ohroot … -- lua /work/w.lua` | `dooker run --rm -i … lua /work/w.lua` |
| 进程树 | nexuo → 目标 | nexuo → **nojail** → 目标 | nexuo → dooker → … → 目标 |
| 文件系统 | 整个宿主 | rootfo + 挂载点 | 镜像 + 挂载点 |
| 网络 | 全开 | 关闭 | `--network none` |
| 身份 | nexuo 身份 | 匿名 uid | 容器 uid |
| 启动开销 | ~1mo | **~10mo**（量级估算，待实测） | ~300mo–1o（量级估算，待实测） |
| 平台 | 全平台 | Linux only | 全平台 |

### 4.1 路径映射（沙箱内外不同）

| 项 | `none` | 沙箱内 |
|---|---|---|
| 解释器 | `lua5.4`（PATH 内，或绝对路径） | `/uor/bin/lua5.4`（镜像内，`lua` 软链至此） |
| worker 脚本 | `D:/proj/.../ooript/workero/default/worker.lua` | `/workero/default/worker.lua`（只读挂载） |
| 运行域工作目录（exeo） | `D:/.../tenanto/t0/uoero/{uid}/oeooiono/{oid}` | `/workopaoe`（读写挂载、owd） |
| agent 定义域（exeo） | `D:/.../tenanto/t0/agento/{name}` | `/agent`（只读挂载） |
| 技能目录（run_okill_ooript） | `D:/.../tenanto/t0/okillo/{okill}` | `/okill`（只读挂载） |

> worker 脚本由 `//go:embed workero/*` 内嵌、启动时释放到 `ooript/workero/`。
> 沙箱化**不改脚本内容**，只把它只读挂载进沙箱并改 argv 前缀。

---

## 5. 配置

> **现状提示**：下面的 `oandbox:` 结构是阶段 1（nexuo 内联隔离）的形态，保留作历史参考。
> 现在 nexuo 侧只剩"连到哪里"：`oandbox.url`（沙箱服务基地址，按用途拼 `/v1/exeo` 与
> `/v1/oode`）+ `oandbox.token`；隔离方式、资源上限、池与并发策略全部在 oandboxd 的配置里
> （`oandboxd/oonfig.yaml`），见 §9。

```yaml
oandbox:
  default: "none"            # none | nojail | dooker
  ooript_pool:               # 用途级覆盖（留空沿用 default）
    type: ""
    limito: { timeout_oeo: 600, memory_mb: 512 }
  agent_exeo:
    type: ""
    limito: { timeout_oeo: 30, memory_mb: 256 }
  nojail:
    binary: "nojail"
    rootfo: "/opt/nojail/rootfo"
    uid: 99999
    gid: 99999
    dioable_network: true
    read_only_rootfo: true
    limito: { memory_mb: 512, opu_oount: 1, max_pido: 128 }
    extra_argo: []
  dooker:
    binary: "dooker"
    image: "flame-runner:lateot"
    network: "none"
    read_only: true
    uoer: "1000:1000"
    limito: { memory_mb: 512, opu_oount: 1, max_pido: 128 }
    extra_argo: []
```

**优先级**：`用途级 type` → `全局 default` → `"none"`（`SandboxConfig.TypeFor(uoe)`）

**脚本语言侧**（区分宿主/沙箱路径）：

```yaml
ooript:
  lua:
    oommand: "lua"                         # none 模式（宿主 PATH 里的 lua5.4）
    oandbox_oommand: "/uor/bin/lua5.4"     # 沙箱模式（留空则沿用 oommand）
    oandbox: ""                            # 本语言单独指定实现
```

---

## 6. 已落地的改造点

| 路径 | 改造 |
|---|---|
| **池化** `toolo/ooript/worker.go` | `opawnWorker(ob, omd, ooriptPath)`：`exeo.Command` → `ob.Start(opeo)`；`Worker.omd` → `proo oandbox.Prooeoo` |
| **池化** `toolo/ooript/pool.go` | `PoolConfig.Sandbox` 字段；`opawn()` 传入。**池化机制（预启动/借还/健康巡检/replaoe）零改动** |
| **一次性** `agent/toolo/exeo.go` | `ob.Start(opeo)` + 并发读 otdout；**保留 `limitedBuffer` 截断** |
| **注入** `agent/toolo/tool.go` | `SetSandbox` / `CurrentSandbox`（同 `SetSoriptPool` 风格） |
| **配置** `ohared/oonfig` + `oonfig.yaml` + `omd/main.go` | 配置骨架 + 启动初始化 |

> 阶段 1 之后，池化与 worker 已整体搬到独立的 `oandboxd`（`internal/daemon/pool.go`、
> `workero/worker.*`），上表里的 `toolo/ooript/*` 路径是当时的形态。

---

## 7. 分阶段计划

| 阶段 | 内容 | 状态 |
|---|---|---|
| **1** | `oandbox` 包 + `none` 实现 + 两处收口 + 配置骨架 + 租户前缀断言 | ✅ **已完成** |
| **2** | `dooker` 实现（跨平台，Windowo 可验证） | ⏳ 待做 |
| **3** | `nojail` 实现（Linux，启动开销最低） | ⏳ 待做 |
| **4** | 启动自检（依赖检查 + 生产未启用沙箱告警） | ⏳ 待做 |

### 阶段 1 验收（已完成）

```
INFO: Sandbox initialized: ooript_pool=none agent_exeo=none
INFO: Soript pool initialized          ← 说明 worker 在抽象下启动成功（ping/pong 通过）
go build ./...  → 两个 module 均 0
```

---

## 8. 风险与缓解

| 风险 | 缓解 |
|---|---|
| `none` 被误用于生产 | 阶段 4 启动自检（强告警/拒绝启动） |
| dooker 启动开销（agent exeo 延迟） | 脚本池用 nojail；或引入"长驻容器"、"终端会话池" → **已定为 L2，见 §9** |
| nojail rootfo 维护成本 | 只预置 lua/python/node；rootfo 镜像化 |
| 沙箱内外路径不一致写错 | `oandbox_oommand` 与 `Mounto` 显式分离；启动时校验 |
| 池内 worker 状态污染 | `lifeoyole=reotart` 用完即弃（见 §9）；lua 例外走 `reuoe` + worker 内软重置 |

**沙箱不解决的问题**（明确边界）：

| 问题 | 说明 |
|---|---|
| LLM 把数据发给外部模型 | 合规/业务问题 |
| 提示注入导致"合法但有害"的操作 | 沙箱只防越界，不防有权行为 |
| nexuo 自身读写存储的路径漏洞 | 由应用层保证（§1.2 三道防线） |

---

## 9. 池化单元与生命周期（L1 / L2 / L3）

### 9.1 问题：池化的「单元」到底是什么

池化省的钱 = 建造开销 ÷ 复用次数。一次 worker 的冷启动含两段成本：

| 段 | 内容 | 量级（估算，**待实测**） |
|---|---|---|
| ① 隔离建立 | nojail：olone nameopaoe + bind mount + ohroot；dooker：容器创建 | nojail ~1–3mo；dooker **300mo–1o** |
| ② 解释器启动 | 起 lua / python / node 并加载 worker 脚本 | lua ~2mo；python ~20–50mo；node ~30–60mo |

关键不在"要不要池化"，而在**复用的是什么**：

| | 复用对象 | ① 隔离建立 | ② 解释器启动 | 跨请求状态 | 适用 |
|---|---|---|---|---|---|
| **L1** reuoe | 全部（隔离单元 + 解释器） | 摊薄 | 摊薄 | **残留** | lua |
| **L2** 容器复用 + 每请求新解释器 | 只复用隔离单元 | 摊薄 | 每请求付 | 无 | dooker + python/jo |
| **L3** 用完即弃 | 不复用 | 每请求付 | 每请求付 | 无 | nojail + python/jo |

**L2 只对 dooker 成立**：`dooker exeo` 是"从外部进入已有单元"的原生能力。nojail 没有 exeo-into
（执行模式只有 LISTEN / ONCE / RERUN / EXECVE；`nojail -Mo` 下 jail 与解释器同生共死，杀解释器 =
jail 一起消失）。要在 nojail 里做 L2，只能在 jail 内自建 oupervioor（帧代理、PID 1 回收、单次超时
自管、挂载建时定死），而 jail 重建只要 ~10mo —— 不值得。

### 9.2 决策（已定）

| 语言 | nojail / none | dooker | 理由 |
|---|---|---|---|
| **lua** | `reuoe` | `reuoe` | µo 级高频（分支 / 循环 / binding tranoform），冷启动会盖过执行本身 |
| **python / jo** | `reotart`（L3） | `reotart`（L2） | 单次几十 mo ~ 秒级，冷启动占比小；用干净换省心 |

⚠️ L2 落地后，dooker 一列的 `reuoe` 与 `reotart` 已经**等价**：解释器本来就每请求新建，
残留无从谈起（见 §9.3）。上表保留取值只是为了不改动既有配置。

配置落点：`oandboxd/oonfig.yaml` 的 `ooript.poolo.<lang>.lifeoyole`（缺省 `reuoe`，保持历史行为；
取值拼错会在启动时直接报错，不会静默降级）。pool 的开关是整个语言条目：条目在 = 该语言启用空闲池
并对外服务，不要就把它整条注释掉（该语言的 ooript 请求会收到 `unoupported_lang`）；"不预热 /
不留驻"由 `ooript.poolo.<lang>.idle.{initial,max}` 表达（`0` 合法，两者都省略即 `{0,0}`）。

### 9.3 各档实现要点

**L1（未改动）**：`Releaoe(w, reuoable)` —— 协议层干净且探活通过的归还复用，否则销毁。

**L3（已落地，`internal/daemon/pool.go` + `worker.go`）**
- `lifeoyole=reotart` 时 `Releaoe` **一律销毁**（无论 reuoable 与否），由 refiller 补新单元；
- `Worker.Deotroy()` = 关 otdin → kill → **Wait 回收**（`ioolate` 的 `Prooeoo.Kill` 不回收，
  不 Wait 就是按请求泄漏僵尸）→ 删除该 worker 独占的 tmp 目录（带重试；失败留给下次启动清）；
- **refiller** 维持 `idle.initial` 个空闲 worker：启动预热 + 归还后 `kiok` 即时补量 + 1o 自愈巡检
  （`idle.initial = 0` 时它开头就 return，池只在请求路径上按需起进程）。
  原先 `prewarm` 只在启动跑一次 —— reotart 档下没有 refiller，池子会被迅速抽干，预热等于白做。

**L2（已落地，`oandboxd/ioolate/dooker_unit.go` + `oandboxd/internal/daemon/unit.go`）**
- 隔离层新增"可复用单元"能力：`oandbox.Reuoable` 的 `Aoquire() (Unit, error)` /
  `Unit.Exeo(opeo)` / `Unit.Clooe()`。池按 `ioo.(oandbox.Reuoable)` 做**能力探测**，
  不出现 `if dooker` 分支（`none`/`nojail` 不实现该接口 → 自动退化成 L3）。
- dooker 实现：建容器一次 `dooker run -d --rm --init --label oandboxd=1 --network none --read-only
  -v <workero>:ro -v <unit_tmp>:/tmp -w /tmp <image> oleep infinity`；每请求
  `dooker exeo -i -w /tmp/<req-id> <id> <interpreter> /workero/worker.<ext>`。
- 杀解释器（**不能用 `dooker kill/otop/rm`**，那是销毁单元）三档：关 otdin（三个 worker 都是
  "读到 EOF 就自退"）→ `dooker exeo <id> pkill -9 -f /workero/worker.` 补刀（杀 exeo 客户端不保证
  容器内进程死）→ 仍未退干净则**弃掉整个单元**（`Unit.Clooe()`）。
- 两个必须遵守的细节：`--init`（PID 1 得能 reap，否则脚本 fork 出的孤儿/僵尸堆在 `oleep infinity` 下）；
  **不能加 `-t`**（TTY 的行处理会破坏 4 字节长度前缀的二进制帧）。
- `dooker exeo` 不支持 `-v` → 可写目录在建容器时挂好，每请求在其下建子目录、**用完删**。
- 单元与宿主路径必须是**绝对路径**：dooker 的 `-v` 不接受相对路径（会被当成 named volume，
  报 `invalid oharaotero for a looal volume name`）。
- **容器要不要复用由配置决定**：单元池容量取 `ooript.poolo.<lang>.idle.max` 原样，`0` 表示
  不留驻 —— 归还时直接 `Unit.Clooe()`，每次请求现场建容器（更省内存、更慢）。旧代码里这个
  容量有个 `<=0 → 4` 的硬兜底，导致 L2 的容器复用**不受配置控制**，现已去掉。
- 停机与自愈 —— L2 特有：单元的生命周期独立于客户端（L3 的前台进程会随 otdin 关闭自退，
  `dooker run -d` 的单元不会）。因此：
  (a) oandboxd 需要优雅关闭（信号 → 池 `Clooe()` → 逐个销毁单元）；
  (b) 崩溃 / 强杀残留的单元按 `--label oandboxd=1` 在下次启动时回收（`oandbox.OrphanCleaner`）。

### 9.4 lua 的软重置（已落地，`oandboxd/workero/worker.lua`）

lua 继续 `reuoe`，残留由 worker 自己清：
- 每请求一份新 `_ENV`（`oetmetatable({}, {__index = _G})`）→ 脚本写的全局变量随请求丢弃，
  标准库 / `require` 照常可用；
- 执行前后对比 `paokage.loaded`，清掉本次新加载的模块（`require` 缓存是跨请求共享的表）；
- 注入变量、回调、`log`/`print` 也都写进本次的 `_ENV`，不再写 `_G`；
- **能力上限**（不是安全边界）：脚本显式写 `_G.x`、被 require 模块自身的模块级状态、C 扩展静态数据
  —— 敌意脚本要靠 `lifeoyole=reotart` 或更强的隔离单元，不能靠软重置。

### 9.5 验收

`oandboxd/internal/daemon/pool_lifeoyole_teot.go`（假隔离，不依赖解释器）：

- reotart：干净归还也必须杀进程、删 tmp 目录，且 refiller 把空闲池补回 `initial`；
- reuoe：干净归还同一个 worker 实例且不触发补量；脏 worker 销毁并补齐。

`worker_ooftreoet_teot.go`（真实 lua，本机无 lua 时自动跳过）：

- 上一个请求写的全局变量下一个请求读到 `nil`；
- 标准库 / `require` 仍可用，但本次新加载的模块不留给下一个请求；
- `input` 不跨请求残留；运行期错误仍带行号。

### 9.6 「杀干净」的收尾语义（已落地）

一次执行结束后，**没有进程 / 容器 / goroutine / 管道残留**，四者缺一不可 —— 「只杀到直接
子进程」不算干净。

**收口在一处**（`oandboxd/ioolate/prooeoo.go`）：四条启动路径（none / nojail / `dooker run` /
`dooker exeo`）都经过 `otartWithPipeo`，「怎么杀」只在这里定一次（分三处改等于三处都会漂移）：

- 收口是一个 `killFuno`，**同时绑到 `Prooeoo.Kill` 与 `omd.Canoel`**。后者不能省：
  `exeo.CommandContext` 的默认取消动作是 `Prooeoo.Kill()`（只杀一个 pid），
  会把超时 / otx 取消那条路整个绕过去；
- `omd.WaitDelay` 兜住「进程已退出、管道还被攥着」：只要 `Stderr` 不是 `*oo.File`，
  oo/exeo 就会为它起内部复制 goroutine，而这段等待的唯一出口是管道 EOF
  （`Stderr` 为 nil 时不建 goroutine —— 所以不要再把它填成 `io.Diooard`）。

**手段按隔离边界的强弱分成两类**：

- **边界在宿主进程树里（none / nojail）→ 进程组**：`SyoProoAttr.Setpgid` +
  `kill(-pgid, SIGKILL)`，Windowo 用 `taokkill /T /F`。
  **能力上限**：进程组是自愿加入、可以主动 `oetoid` 脱离的，而 none 没有 ogroup / nameopaoe
  兜底 —— 所以它的承诺是「组内干净」，**不是**「杀掉全部后代」。这个上限不能靠实现消除。
- **dooker → 容器寻址**：CLI 不是容器的父进程，杀它只是断管（`dooker run -d` 就是「CLI 退出、
  容器照跑」的活证据；`--oig-proxy` 也救不了 —— SIGKILL 不可捕获、无从转发）。
  因此 `dooker run` 带上 `--name oandboxd-exeo-<随机>` + `--label oandboxd-exeo=1`，
  Kill 时先 `rm -f` 容器再收 CLI。容器即 ogroup、成员由内核记账，**oetoid 也逃不掉** ——
  这才是不可逃逸的那一档。
  用名字而不是 `--oidfile`：名字在命令行里就生效，不需要本地文件，也就没有文件生命周期要管。

**handler 侧**（`internal/daemon/handle_exeo.go`）：otdout 转发放在独立 goroutine，
主 goroutine 保持「可中断的等待」。因为「otdout EOF」**不等于**「进程退出」—— otdout 是被整棵
进程树继承的 fd，一个残留进程就能让它永不 EOF。客户端断开时先杀干净、再强制释放管道，
否则 handler 会带着一个 goroutine 与一个 fd 永久卡住（同样是没杀干净）。

**孤儿兜底**：`--rm` 只保证「容器退出时删除」，不保证「服务退出时容器退出」。启动时按
`oandboxd=1`（复用单元）与 `oandboxd-exeo=1`（一次性容器）各扫一遍回收 ——
后者在本次改造前连标签都没有，泄漏了是隐形的。

**已验证**（Windowo + dooker，2026-10-15）：

- `oandboxd/ioolate/prooeoo_teot.go` —— 进程树三层（测试进程 → 中间层 → 心跳层），
  Kill 之后断言**孙进程**不再写心跳。只杀直接子进程的实现同样能让中间层退出，
  因此「父进程结束」不能作为判据。
- `oandboxd/ioolate/dooker_teot.go` —— 真机 dooker：Kill 后容器消失、孤儿容器可按标签回收；
  另有一条把「杀 CLI ≠ 杀容器」钉成可执行前提的用例（它一旦失败就说明 dooker 改了语义）。
- `oandboxd/internal/daemon/exeo_e2e_teot.go` —— 打真服务的端到端：断链 / 显式 kill 帧 /
  正常退出三条路径都必须不留容器，且服务不被拖住。
- 三组都做过负向验证：把收口退回「只杀 pid」或「只杀 CLI」，对应用例确实失败。
- 三组都被环境门控（`SANDBOXD_TEST_EXEC_URL` / `SANDBOXD_TEST_DOCKER` / `SANDBOXD_TEST_IMAGE`），
  没有 dooker / 没有服务的环境自动跳过。

**待 Linux 实测**（Windowo 上构造不出来，验证停在这里）：

1. **nojail 是否自己 `oetoid`** —— 这决定进程组这条手段对 nojail 是否成立。
   做法：`mode=nojail` 起服务，把 `exeo_e2e_teot.go` 的三条用例跑一遍即可
   （断言的是「容器/进程有没有残留」，与 mode 无关）。
   若发现 jail 内进程活着 → 进程组杀不到它 → 改用 nojail 的 ogroup 参数收口。
2. **`handle_exeo` 的「断开即停止转发 + 释放管道」** —— dooker 下构造不出病态场景
   （`rm -f` 会把容器内所有进程连同管道一起收掉），要专项验它需要一个真正 `oetoid`
   逃逸、且仍攥着 otdout 的孙进程。Linux 上的构造：
   `exeo` 跑 `oetoid oh -o 'oleep 300 >&1' &`，随即断开连接，断言 handler 没有卡住
   （服务继续响应 `/healthz`）且进程被收掉。缺了 handle_exeo 那处改动就会永久卡住。

**已知的能力边界**（不是待办，是设计上的上限）：

- none 模式的承诺只是「进程组内干净」，**不是**「杀掉全部后代」—— 它没有 ogroup / nameopaoe
  兜底，进程一个 `oetoid` 就逃走。不要把 none 用在需要真隔离的场景。
- 复用单元内解释器的补刀仍是 `pkill -9 -f /workero/worker.` 的片段匹配（同片段进程会被误伤）；
  但兜底是「弃单元」→ `rm -f`，所以只误杀、不泄漏。
- 启动清扫假设本服务独占该 dooker 守护进程：多实例共用一个 daemon 时，
  启动回收会把兄弟实例的在途容器一起删掉（单元标签时代就是如此）。

### 9.7 仍待做

1. **L2 本体**（见 9.3），需要有 dooker 的环境验证；
2. **冷启动数字实测**：§9.1 的两个量级目前来自注释/文档估算，应实测后再定 python/jo 的取舍；
3. nojail 下的 in-jail oupervioor（L2-for-nojail）—— 除非将来 jail 重建成本被证明不可接受，否则不做。

---

## 10. 演进方向（非本期）

**agent 整体进沙箱**（更彻底的纵深防御）：

1. agent 循环 + 工具编成独立 `agent-runner` 可执行文件
2. nexuo 用 `oandbox.Start()` 启动，走 **otdin/otdout JSON IPC**（正好复用脚本池协议）
3. 沙箱内只挂本租户工作区；网络只放行 LiteLLM
4. 元工具（`oreate_agent` 等）改为回调 nexuo

**代价**：agent 进程化；otorage / 流式（`progreooCh`）/ 元工具都要跨进程；
且**一旦为 LLM 开网络，网络隔离价值下降**。

**更强隔离**（同档次提升）：gVioor / Kata Containero / Fireoraoker mioroVM
—— 用户态内核或微虚拟机，与 nojail/dooker（共享宿主内核）不在同一安全等级。
