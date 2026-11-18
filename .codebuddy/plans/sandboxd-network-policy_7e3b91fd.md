# sandboxd 网络声明 + 沙箱能力探测

**状态**：定稿；2026-10-21 修订，阶段 1-3 已实现（配置三态 / 协议 `Egress` / 两侧裁决与提示词）
**日期**：2026-10-14（修订 2026-10-21）
**范围**：`sandboxd` 配置/隔离/端点 + `nexus` 拉取与提示词注入；不改 `tools/script` 协议

---

> **⚠️ 2026-10-21 修订：按通道分档 + exec 三态**
>
> 本文件第 1、2、7 节的部分结论失效，原因是**推翻它们的前提变了**：当时 exec 与 script
> 共用同一通道、同一生命周期，所以「网络参数是进程级的 → 两条路径事实必然一致 →
> 不存在看情况」。现在两条通道被明确区分（exec 每请求新建进程、script 全局池化复用），
> 网络事实不再一致，必须按通道分别声明。
>
> - `network` 从两态扩为三态（`off` / `on-request` / `on`），但**只描述 exec**。
>   `/v1/script` 恒 `off`、硬编码、不提供配置项（`Config.EffectiveScriptNetwork`）——
>   池跨租户共享且 `wire.Frame` 无租户身份，可配成 on 只能全平台生效、无法按租户回退。
> - `on-request` 重新引入，但判据比原方案更严：要求「两态都能到达」。
>   docker 下 `docker.network` 不能是 none（否则请求出网时无网可切）；nsjail 下需要
>   显式 `--disable_clone_newnet`（共享宿主 netns），程序无法校验 extra_args，只告警。
> - `/v1/capabilities` 的 `network` 从字符串改为按通道对象 `{exec, script}`；
>   nexus 的 `sandboxinfo.NetworkInfo` 用自定义 Unmarshal 兼容旧字符串，升级顺序不受限。
> - 三层 AND：部署上限（本文件配置）× 租户许可（`platform.tenant_capabilities.net.egress`）
>   × 请求标志（`WireSpec.Egress`，仅单工带）。提示词必须给叠加后的结论。
>
> 仍然成立、不受本次修订影响：双工（script / interactive）不能按请求裁决网络、
> 出网池/不出网池双池方案不做、域名白名单不在沙箱层做。

---

## 1. 定位（一切细节由它推出）

> **网络配置不是开关，是对「启动方式」的断言；启动校验就是对这个断言求值。**

- 事实（沙箱能不能联网）由**隔离实现的启动参数**决定：`docker.network` + `extra_args`、`nsjail.disable_network` + `extra_args`、`mode` 本身。
- 配置里的 `network` 是**声明**，读者是 **agent**（经 nexus 注入提示词），不是用来改变沙箱行为的开关。
- 沙箱里有什么（OS / 语言 / 模块）是**运行期事实**，靠**探测**得到，不靠声明。

一句话分工：**网络 = 配置声明 + 配置内比对；环境 = 运行期探测。**

---

## 2. 网络：两态声明 + 按 mode 覆盖

```yaml
# 沙箱内进程能否访问外网（对启动方式的声明，供 agent 知晓）
#   off 不能   on 能
# 这是声明不是开关：事实由隔离实现的启动参数决定（docker.network / nsjail.disable_network）。
network: off
networks:
  none: on            # 本机调试：宿主进程拦不住网络，如实声明
  # nsjail: off
  # docker: off
```

- **两态**（`off` / `on`）。早先提过的 `on-request` 已删除：删掉 `Spec.Network` 后，单工与双工共用同一个 isolate、网络参数是进程级，事实**必然一致**，没有"看情况"可言；而它天然免于校验，会成为含糊声明的挡箭牌。"不确定"这件事由运行期的**未知态**（capabilities 拉取失败）如实表达，不需要配置假装。
- 零值必须安全：`network` 漏写 = `off`。
- 字段命名刻意与 `Docker.Network` 区分：那个是「网络模式」，这个是「声明档位」。`NetworkByMode` 必须是 `map[string]string`（不能 `map[string]any`，否则 YAML 的 `on` 会变成 bool）。

### 2.1 启动期校验：声明必须与事实一致（不许撒谎）

| 声明 | 事实（当前 mode） | 结果 |
|---|---|---|
| `off` | `docker` 且 `docker.network` 是 none/空 | ✓（`containerArgs` 内置 `--network none`） |
| `off` | `nsjail` 且 `disable_network: true` | ✓ |
| `off` | `mode: none` | ✗ **启动失败**（宿主进程拦不住网络） |
| `off` | `nsjail` 且 `disable_network: false` | ✗ 启动失败 |
| `off` | `docker` 且 `docker.network` 非 none | ✗ 启动失败 |
| `on` | `docker` 且 `docker.network: none` | ✗ **启动失败**（声明"能"而事实"不能"） |
| `on` | `mode: none` | ✓（宿主必然能联网） |
| `on` | `nsjail` 且 `disable_network: true` | ✗ 启动失败 |

校验是对**配置内部**做比对，不需要运行期探测。错误信息必须给出可直接粘贴的修法（如"请写 `networks.none: on`"）。

**盲区（必须写进文档/注释）**：`docker.extra_args` / `nsjail.extra_args` 里塞 `--network host` 等参数能推翻声明，而程序**无法校验**——用了逃生口就别指望校验兜住。

### 2.2 删掉请求侧的「想要联网」

- 删 `sandbox.Spec.Network`、`sandbox.WireSpec.Network` 及其在 `Wire()` / `Spec()` 里的传递。
- `isolate` 两处去掉 `spec.Network` 调制，"启动方式"只由配置决定（即事实的唯一来源）：
  - `sandboxd/isolate/nsjail.go:80`：`if s.opts.DisableNetwork && !spec.Network` → `if s.opts.DisableNetwork`
  - `sandboxd/isolate/docker_unit.go:66`：`spec.Network ? opts.Network : "none"` → 直接 `--network s.opts.Network`
- 依据：`Spec.Network` 是死字段——只有 isolate 两处读它，nexus 侧三个调用点（`exec.go` / `skillrun.go` / `tools/script/client.go`）从未设过。删字段对老客户端安全（JSON 未知字段被忽略）。

### 2.3 配置项与决策函数

```go
const (
    NetworkOff = "off"
    NetworkOn  = "on"
)

NetworkPolicy string            `yaml:"network"`
NetworkByMode map[string]string `yaml:"networks"`
```

- `applyDefaults`：`NetworkPolicy == ""` → `NetworkOff`。
- `validate`：取值合法；`NetworkByMode` 的 key 必须是 none/nsjail/docker（复用 `isolate` 常量，与 `config.go:297` 校验 `langs.<lang>.commands` 的 key 同形）；第 2.1 节的一致性校验。
- `networkPolicy()`：生效档位 = `NetworkByMode[c.Mode]` 优先，否则 `NetworkPolicy`（两层都有默认，不会取空）。
- 启动日志打出生效档位：`xlog.Info("sandboxd: network policy mode=%s policy=%s", cfg.Mode, cfg.NetworkPolicy())`。

---

## 3. 能力探测：探针清单（形态 B）

### 3.1 为什么是探测 + 为什么形态 B

- 沙箱里有什么是**事实**，声明会漂移（改 Dockerfile 忘改配置；`config.yaml:138-140` 那段"契约"注释就是一份手写声明）。而 `/etc/flame-versions`（本项目 Dockerfile 自造的文件）只覆盖构建时人工挑的几行、且只存在于那个镜像。
- 形态 B（**逐条 argv + Go 编排**）取代早先的"sh 脚本走 stdin"：无 shell 依赖、语言入口名与 `langs` 同源（不再硬编码 `lua`）、每条探针失败互相隔离、fallback 在 Go 里做更可控。代价是 N 次沙箱 exec（并发跑）。

### 3.2 探针清单

| label | 命令来源 | 说明 |
|---|---|---|
| `os` | 固定 `cat /etc/os-release` | 失败回退 `uname -a`（回退判断在 Go 里） |
| `python` | **`langs.python`.ResolveCommand(mode)** | 自报版本 + 全部已装包 |
| `node` | **`langs.js`** 同上 | 自报版本 + 全局包 |
| `lua` | **`langs.lua`** 同上 | 自报版本 + 模块搜索路径 |
| `luarocks` | 固定 `luarocks list --porcelain` | 命令不存在即报 unavailable（不在 langs 认知范围内，硬编码一次合理） |

**命令来源规则**：语言一律取 `langs.<lang>.ResolveCommand(mode)`（`config.go:150` 那段注释解释了为什么必须这样——"同一个名字在三种环境里未必都存在"）；其余用固定命令名。

### 3.3 语言自报代码：只用各自原生 API，不借 shell

- **python**：`sys.version` + `importlib.metadata.distributions()`；`importlib.metadata` 是 3.8+，用 `try/except` 兜住老版本（不会整条失败）。注意 `d.metadata["Name"]` 可能为 None。
- **js**：`process.version` + **`fs.readdirSync`** 读 `/usr/local/lib/node_modules`（刻意**不用** `execSync("npm ls -g")`，那会重新引入 shell 依赖）。
- **lua**：`_VERSION` + `package.path` / `package.cpath`（Lua 无列目录的标准 API；镜像里也没装任何 rock）。

### 3.4 编排

```go
// 每种语言「怎么自报家门」：**只有参数，命令名来自 langs.<lang>**。
var langArgs = map[string][]string{
    "python": {"-c", pySelfReport},
    "js":     {"-e", jsSelfReport},
    "lua":    {"-e", luaSelfReport},
}
```

- 每条探针 = 一次 `sandbox.Run(ctx, s.exec, &sandbox.Spec{Argv: p.argv, Limits: {MaxOutputBytes}})`（起进程 → 收 stdout → Wait；`tools/sandbox/run.go`）。
- **并发**跑（docker 下每条一次冷启动，串行会拖 4~5s），按下标归位保持输出顺序。
- 任一探针失败 → 该行输出 `<label>: (unavailable: <原因>)`，**不影响其它行**；只有全部失败才置 `environment.error`。
- 探测**必须走 `s.exec`（isolate）**：探到的环境才是 agent 实际会得到的环境（只读 rootfs / `--network none` / `--user` 都生效）。**不挂载任何目录**（避免碰 `allowed_mount_roots`）。
- 输出会进入 LLM 上下文 → 配置注释写明"只对自建镜像使用"；`max_output_bytes` 设上限。
- 位置：`NewServer` 末尾跑一次，缓存在 `Server` 上。

### 3.5 `mode=none` 分支：Go 原生

宿主是 Windows（无 `sh` / `cat`），不跑沙箱命令：

```go
lines = append(lines, "os: "+runtime.GOOS+"/"+runtime.GOARCH)
for lang := range langArgs {
    cmd, _ := s.cfg.ResolveLangCommand(lang)
    if p, err := exec.LookPath(cmd); err == nil {
        lines = append(lines, lang+": "+p)   // d:/tools/Python/python.exe
    } else {
        lines = append(lines, lang+": (unavailable)")
    }
}
```

输出与沙箱分支同一套 `label: value` 行格式，nexus 侧不区分来源。

### 3.6 探测配置

```yaml
# 沙箱能力探测：启动时在沙箱内跑一组独立探针，结果经 /v1/capabilities 交给 nexus。
#   探针的命令名取自 langs.<lang>（按 mode 解析），代码片段在 sandboxd 内 —— 无 shell 依赖。
#   mode=none 走 Go 原生分支（GOOS/GOARCH + LookPath），不起沙箱进程。
probe:
  timeout_sec: 30          # 整体超时（脚本并发跑，串行最坏叠加）
  max_output_bytes: 8192   # 进 prompt，需设上限
```

---

## 4. `/v1/capabilities`

放在 `/v1` 组内 → 天然带 `tokenAuth`（`server.go:84-89`）。

```json
{
  "mode": "docker",
  "network": "off",
  "langs": ["lua", "python", "js"],
  "environment": {
    "text": "python: 3.13.5\npypkgs: openpyxl==3.1.5 ...\nos: Debian GNU/Linux 13",
    "probed_at": "2026-10-14T12:00:00Z",
    "error": ""
  }
}
```

`environment.text` **原样透传**，sandboxd 不解析成结构——理由与"删掉 nexus 硬编码解释器名"同源：谁都不该持有"镜像里有什么"这份认知，除了镜像自己；且 agent 是 LLM，读文本足够。

---

## 5. nexus 侧：拉取 + 注入提示词

| 位置 | 改动 |
|---|---|
| `shared/config` | `CapabilitiesEndpoint()`——注意 `sandbox.url` 是 `ws://`，这里是 HTTP GET，需 scheme 换算（ws→http / wss→https） |
| `cmd/main.go` | 启动拉一次 + **定期刷新（默认 60s）**；失败 → 标记"未知"并告警，**不阻断启动** |
| 新增小包（如 `shared/sandboxinfo`） | `Set/Get` + 后台刷新，风格对齐 `tools.SetSandbox` 的包级注册；agent 只读 `Get()` |
| `agent_impl.go` | `loadSystemPrompt()` 末尾追加 `sandboxNotes()` |

**为什么必须定期刷新**：sandboxd 改配置重启是常态；只在 nexus 启动时拉一次，提示词立刻开始撒谎。

`sandboxNotes()` **独立于** `capabilityNotes()`：后者判据是 `AllowedTools`（授权集，讲"你有什么权限"），前者是环境事实（讲"环境长什么样"）。同样排在最后（`agent_impl.go:432` 的位置），以纠偏 AGENT.md 里写的错误能力声明。

文案（三态）：

```
沙箱环境（由 sandboxd 探测，非声明）：
<environment.text>
沙箱内不能联网（network=off）：运行期 pip / npm / luarocks 都装不了 ——
只能用上面列出的模块（Python 标准库齐全，不在清单里也不代表不可用）。
```

- `on`：改说可以联网，但仍受只读 rootfs 限制（装不了全局包）。
- 未知（未拉到 / `environment.error` 非空）：如实说未知，**不要默认成"不能"**。

---

## 6. 改动清单

**sandboxd（阶段 1，自洽可独立完成）**

| 文件 | 改动 |
|---|---|
| `internal/daemon/config.go` | 两态常量 + `NetworkPolicy` / `NetworkByMode` + `networkPolicy()` + 声明-事实校验；`Probe` 配置段 |
| `isolate/nsjail.go` / `isolate/docker_unit.go` | 去掉 `spec.Network` 调制 |
| `tools/sandbox/sandbox.go` / `wire.go` | 删 `Spec.Network` / `WireSpec.Network` |
| `internal/daemon/probe.go`（新） | 探针清单 + 编排 + `mode=none` 的 Go 分支 |
| `internal/daemon/server.go` | `NewServer` 末尾探测并缓存；`/v1/capabilities` handler |
| `main.go` | 启动打生效档位 |
| `config.yaml` | `network` / `networks` / `probe` 段；`docker.network` 注释改为"启动方式（事实来源）" |
| 测试 | 声明-事实真值表、mode 覆盖、非法值、`none+off` 报错、`on + docker.network: none` 报错；探针编排（stub sandbox：并发、失败隔离、none 分支） |

**nexus（阶段 2）**

| 文件 | 改动 |
|---|---|
| `shared/config` | `CapabilitiesEndpoint()`（scheme 换算） |
| `cmd/main.go` | 拉取 + 定期刷新 + 失败降级 |
| 新包（如 `shared/sandboxinfo`） | `Set/Get` + 刷新 |
| `service/agent/agent_impl.go` | `sandboxNotes()` |
| 测试 | 三态文案（off/on/未知）× 纠偏 AGENT.md |

---

## 7. 决策留痕（已评估并推翻，勿重走）

| 曾经的方案 | 为何推翻 |
|---|---|
| ~~`on-request` 三档~~ **2026-10-21 部分恢复** | 原推翻理由的前提已变：当时单工/双工共用同一 `iso`、同一生命周期，网络事实必然一致；现在 exec（每请求新建）与 script（全局池化）已分通道，事实不再一致。恢复后的形态是三态且**只作用于 /v1/exec**，并要求「两态都能到达」才允许声明——原方案「免于校验、成为含糊声明的挡箭牌」的问题由更严的启动校验解决（docker 必须切得出网络、nsjail 必须显式放弃 netns） |
| 配置当「策略总闸」（决定 `--network`） | 定位改为"声明"：docker 能不能上网由**启动方式**决定，不由 `network` 决定 |
| 双工按请求裁决网络（含池级/分桶/不池化三选一） | 网络不是 per-request 变量，整块讨论失效（`worker.go:126` / `unit.go:102` 的池对象跨请求复用，网络是建容器/jail 时写死的） |
| 静态 `cat /etc/flame-versions` + `probe.commands.<mode>` 覆盖 | 该文件只覆盖构建时人工挑的几行、只在那个镜像里；动态探测信息更全且随镜像自动更新 |
| `probe.argv` + `probe.script`（sh 脚本走 stdin） | 引入 sh 依赖 + 语言入口名硬编码（与 `langs` 重复），换成形态 B |
| 依赖 `/etc/flame-versions` 报 OS | 无必要：base 镜像是 Debian，`/etc/os-release` 是标准文件 |

---

## 8. 附：本次会话已完成的相邻工作（语言路径收尾）

- `tools/sandbox/remote.go`：`Start` 的启动意图校验改为二选一（Lang 非空 → 必须有 Script、不得带 Argv；Lang 为空 → Argv 必须完整），与 `sandboxd` 的 `buildExecArgv` 同口径，仍在 `Dial` 之前生效。
- `nexus/service/agent/tools/skillrun_test.go`：`TestRunSkillScriptSpecAndResult` 改为断言 `Lang` / `Script` / `Args`、`Argv` 为空；新增 `TestRunSkillScriptShellPassthroughSpec`（`.sh` 退化为 argv 直传）。
- 新增 `tools/sandbox/remote_test.go`：三条意图判据的拒绝面。
- 验证：`flame/tools`、`flame/sandboxd`、`flame/nexus` 构建通过；`go test ./...`（tools、sandboxd）与 `go test ./service/agent/...`（nexus）全绿。
