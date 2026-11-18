# sandboxd 配置：`interact.langs.<lang>.pool` → `interact.pools.<lang>.idle`（并删除 `enabled`）

**状态**：已实现并验证（见 §9；另含 §11 的 `acquire_timeout_sec` 归位）
**日期**：2026-10-14
**范围**：`sandboxd` 的配置 / 装配 / 槽位池与单元池 + 测试 + `nexus/docs/sandbox-design.md`；**不动** wire 协议，不改 nexus 代码

---

## 1. 定位

> **段名说清「这是双工的池策略」，子段名说清「这管的是空闲池」，而池的开关只有一个层级——整个语言条目。**

1. 现名两处含糊：`interact.langs` 与顶层 `langs` 同名不同义（顶层是「解释器入口名」，语言的事实，单工也用；这里是「池与并发策略」，双工的策略，真实关系是交叉约束「本段 key 必须在顶层有同名定义」）。`pool` 覆盖范围过大——它管的是**空闲池**（借出中的 worker 不占这个额度），真正的总量闸是 `max_concurrent`。
2. **池的开关 = 整个 `<lang>` 条目在不在**：条目在 = 该语言启用并被服务；不要就**把整条注释掉**，该语言的 interact 请求立刻收到 `unsupported_lang`。刻意让"关"的结果是响亮的——另一条路（只关空闲池、语言照跑）的后果是**静默的性能悬崖**（每次冷启动 300ms~1s），本项目一贯不接受静默降级。
3. **布尔 `enabled` 删除**：`initial: 0` 已经能表达"不预热 / 不保留"；"写了 `initial: 2` 又说 `enabled: false`"这种自相矛盾状态不该存在。

## 2. 命名映射

| 旧 | 新 | 含义（不变） |
|---|---|---|
| `interact.langs.<lang>` | `interact.pools.<lang>` | 要池化并服务双工的语言；key 必须在顶层 `langs` 有同名定义 |
| `<lang>.pool` | `<lang>.idle` | 空闲池参数段 |
| `<lang>.pool.enabled` | **删除** | 开关上移到「整个语言条目」 |
| `<lang>.pool.initial` | `<lang>.idle.initial` | 保底空闲数（0 = 不预热） |
| `<lang>.pool.max` | `<lang>.idle.max` | 空闲池容量上限（0 = 不留驻） |
| `<lang>.{max_concurrent, acquire_timeout_sec, lifecycle}` | 不动 | 并发闸 / 取槽等待 / 生命周期 |
| `interact.limits` / `exec` / 顶层 `langs` | 不动 | 用途级默认值与解释器入口 |

Go 侧：`InteractConfig.Langs`→`Pools`(`yaml:"pools"`)、`InteractLang.Pool`→`Idle`(`yaml:"idle"`)、`LangConfig.Pool`→`Idle`；类型名 `InteractLang` / `PoolConfig` 保留（只改注释，见 §6）。

## 3. 目标形态

```yaml
interact:
  limits:
    memory_mb: 512
  pools:
    lua:
      idle:
        initial: 2      # 保底空闲数（启动预热 + refiller 维持）；0 = 不预热
        max: 16         # 空闲池容量上限（借出中的不占额度）；缺省 = initial
      max_concurrent: 32
      acquire_timeout_sec: 5
      lifecycle: reuse
    # 关掉一个语言：整条注释掉（只注释 idle 会退化成「语言照跑但每次现场冷启动」的静默性能悬崖，所以没有那一档）
    #   ruby:
    #     idle: {initial: 1, max: 4}
    #     max_concurrent: 8
    #     acquire_timeout_sec: 5
    #     lifecycle: restart
```

## 4. 取值规则与真值表

### 4.1 缺省（无指针、无静默改写）

| 键 | 缺省 | 显式值 |
|---|---|---|
| `idle.initial` | `0` | 原样保留，`0` 合法（不预热） |
| `idle.max` | `= initial`（跟随，不是固定 4） | 原样保留，`0` 合法（不留驻） |

- `initial` 若沿用旧的隐式缺省 `1`，必须用 `*int` 区分「没写」与「写 0」；为了一个历史缺省把零值语义搞脏不值得。
- `max` 若取固定 `4`，无法区分「没写」与「写 `max: 0`」→ 必然静默改写用户的 `0`。取「跟随 `initial`」后无此格子。

### 4.2 状态空间（L2/L3 口径一致）

| `idle` 写法 | 生效值 | L3（none / nsjail） | L2（docker） |
|---|---|---|---|
| 整段省略 | `0 / 0` | 不预热；归还即销毁 → 等价不池化 | 不预热；容器用完即关 → 等价不池化 |
| `initial: 0, max: 0` | `0 / 0` | 同省略 | 同省略 |
| `initial: 2` | `2 / 2` | 预热并维持 2，容量 2 | 预热 2 个单元，容量 2 |
| `initial: 2, max: 16` | `2 / 16` | 预热 2，容量 16 | 同 |
| `initial: 0, max: 8` | `0 / 8` | 不预热，归还入池（≤8） | 不预热，容器复用（≤8） |
| `initial: 8, max: 4` | — | **启动失败**（§4.3） | — |

L2 的容器复用第一次变成配置可控（旧代码 `newUnitPool` 的 `size <= 0 → 4` 硬兜底让 `max` 在 L2 下形同虚设）。

### 4.3 校验：`initial > max` 必须报错

`refill()` 的循环是 `for len(p.idle) < target` + `select { case p.idle <- w: ...; default: w.Destroy(); return }`——容量 0 而 target > 0 时，每次推送都走 `default` 销毁并返回，而 refiller 每秒 tick → **每秒起一个再杀一个**；`unitPool.refill` 同理（每秒建一个容器再销毁）。故 `validate` 增一条并给可粘贴修法，同时删掉旧的「静默 `initial = max` 钳制」。

## 5. 兼容：旧键 → 新键

### 5.1 为什么必须收

yaml.v3 默认不校验未知字段。`interact.langs` 被整段丢弃的后果：`Pools` 空 → `langScripts` 遍历空 map（`missing` 也为空，**不报错**）→ `Languages()` 空 → `s.pools` 空 → **启动成功、单工完好、双工每一次调用回 `unsupported_lang`**。沿用 `applyLegacyKeys` 机制：**回填 + 逐级告警**。

### 5.2 回填链与顺序

```
session.langs ──段级回填+告警──> interact.langs ──段内回填+告警──> interact.pools
pools.lua.pool ──语言级回填+告警──> pools.lua.idle
pool.enabled ──按旧语义翻译+告警──> pools.lua.idle
```

顺序：**段级（`session`→`interact`）→ 段内（`langs`→`pools`）→ 语言级（`pool`→`idle`）**。三个静默失效入口：

1. **顺序**：反过来跑，`session.langs` 在搬到 `interact` 之后才被搬走，中间那步判定的是空 map。
2. **`isZero` 必须算上 `LegacyLangs`**：`session: {langs: ...}` 的内容落在这个字段上，只看 `Pools` 会把「有内容的旧段」判成「一个字都没写」→ 段级回填被跳过 → 静默失效。`LegacySession` 的类型就是 `InteractConfig`，修好这一处，三级更旧键自动连通。
3. **新旧同写以新键为准 + 明确告警旧键被忽略**（段内、语言级各一条）。

### 5.3 `enabled` 的忠实翻译

旧语义里 `enabled` 是**总开关**：不是 `true`（含没写）时 `initial/max` 一律被忽略。翻译必须忠实，否则会出现"配了数值却没生效"或"旧配置直接起不来"：

| 旧写法 | 新写法（回填结果） | 出声 |
|---|---|---|
| `pool: {enabled: true, initial: 2, max: 16}` | `idle: {initial: 2, max: 16}` | 更名 + 键已移除 |
| `pool: {enabled: true}`（无数值） | `idle: {initial: 1, max: 4}` | 更名 + 键已移除 |
| `pool: {enabled: true, initial: 5}`（旧钳制） | `idle: {initial: 4, max: 4}` | 更名 + 键已移除 |
| `pool: {enabled: false, initial: 2}` | `idle: {initial: 0, max: 0}` | 更名 + 键已移除 + **数值被忽略的说明** |
| `pool: {initial: 2, max: 16}`（没写 enabled） | `idle: {initial: 0, max: 0}` | 更名 + **数值被忽略的说明** |

规则：**`enabled` 不是 `true` → 落成 `{0, 0}`（旧语义的"不启用空闲池"）；`enabled: true` → 按旧缺省补全（`initial<=0→1`、`max<=0→4`）并复现旧的 `initial = max` 钳制。** 落成 `{0,0}` 时额外出声，说明用户写的 `initial/max` 在旧语义下本来就被忽略 —— 否则那些数字看起来"配了却没生效"。

注意这一步也顺带堵掉一个坑：若不按旧语义钳制，`pool: {enabled: true, initial: 5}`（没有 max）会翻成 `{5,4}`，直接撞上 §4.3 的启动校验 —— 旧配置被新校验"判死刑"是不该发生的。

差异需在告警文案里说清：旧 `enabled: false` 在 L3 下是"彻底不复用"，在 L2 下容器**仍然复用**（旧兜底不受 `enabled` 控制）；新写法 `{0,0}` 在两个隔离下都"不留驻"。→ docker 部署若想保留容器复用，请写 `idle: {initial: 0, max: 4}`。

### 5.4 实现骨架

```go
type InteractConfig struct {
	Limits LimitsConfig `yaml:"limits"`
	Pools  map[string]InteractLang `yaml:"pools"`  // key 必须在顶层 langs 有同名定义
	LegacyLangs map[string]InteractLang `yaml:"langs"` // 旧键 interact.langs
}

// ⚠️ 必须算上 LegacyLangs（见 §5.2 第 2 条）
func (s InteractConfig) isZero() bool {
	return s.Limits == (LimitsConfig{}) && len(s.Pools) == 0 && len(s.LegacyLangs) == 0
}

type PoolConfig struct {
	Initial int `yaml:"initial"`
	Max     int `yaml:"max"`
	// 只为检测已移除的 enabled（翻译 + 告警），不参与运行期判断。
	LegacyEnabled *bool `yaml:"enabled"`
}

// 必须在 Config.applyLegacyKeys 的段级回填之后调用
func (c *Config) applyInteractLegacyKeys() {
	switch {
	case len(c.Interact.Pools) == 0 && len(c.Interact.LegacyLangs) > 0:
		c.Interact.Pools = c.Interact.LegacyLangs
		c.warnLegacy("interact.langs", "interact.pools")
	case len(c.Interact.LegacyLangs) > 0:
		c.warnLegacyIgnored("interact.langs", "interact.pools")
	}
	for name, lang := range c.Interact.Pools {
		switch {
		case lang.Idle == (PoolConfig{}) && lang.LegacyPool != (PoolConfig{}):
			lang.Idle = legacyIdle(lang.LegacyPool)
			c.warnLegacy("interact.pools."+name+".pool", "interact.pools."+name+".idle")
		case lang.LegacyPool != (PoolConfig{}):
			c.warnLegacyIgnored("interact.pools."+name+".pool", "interact.pools."+name+".idle")
		}
		if lang.Idle.LegacyEnabled != nil {
			c.warnRemoved("interact.pools."+name+".idle.enabled",
				"开关是整个语言条目：要关掉就把该语言整条注释掉（不要只删 idle 段）")
		}
		c.Interact.Pools[name] = lang
	}
}

func legacyIdle(old PoolConfig) PoolConfig {
	if old.LegacyEnabled != nil && !*old.LegacyEnabled {
		return PoolConfig{Initial: 0, Max: 0}
	}
	in, mx := old.Initial, old.Max
	if in <= 0 { in = 1 } // 旧缺省
	if mx <= 0 { mx = 4 } // 旧缺省
	return PoolConfig{Initial: in, Max: mx}
}
```

## 6. 决策留痕（已评估并推翻，勿重走）

| 曾经的方案 | 为何推翻 |
|---|---|
| 硬改名、不收旧键 | 失败模式是静默降级（§5.1），而 `config.yaml` 在编译期外；仓库已有 `applyLegacyKeys` 先例 |
| legacy 段只按数值照搬（不看 `enabled`） | 旧语义里 `enabled` 是总开关，"没写 enabled 但有 initial/max"在旧代码里等于**不启用池**；照搬等于凭空改变行为，且 `{5,4}` 这类组合会直接撞上新校验。改为按旧语义翻译（§5.3） |
| `yaml.Decoder.KnownFields(true)` | 会让 `session*` 那批旧键启动即失败，与「旧键仍收 + 告警」互斥 |
| 保留 `enabled`（默认改 `true`，随包配置里注释掉） | 默认变 `true` 后 `pools.<lang>` 段的存在失去意义；自相矛盾状态依然可表达 |
| 池开关放在 `idle` 子段（本文档早期版本） | 唯一后果是**静默性能悬崖**；开关上移到语言条目后"关"的结果是响亮的 `unsupported_lang` |
| "保留语言但不池化"不可表达（早期结论） | **撤回**：`{initial: 0, max: 0}` 就是它，且 L2/L3 口径一致 |
| `initial` 缺省沿用旧的隐式 `1` | 必须 `Initial *int` 区分「没写」与「写 0」；而 `0`（不预热）本身就是合法语义 |
| `max` 缺省固定 `4` | 无法区分「没写」与「写 `max: 0`」→ 必然静默改写用户的 `0` |
| `initial > max` 静默钳制 | 既"偷偷改我写的值"，又会在 `max: 0` 时引发 refiller 每秒起杀抖动 |
| 把 `limits` / `max_concurrent` 塞进 `idle` | 它们管「总量 / 借出中」，正好是 `idle`（借出中不占额度）的反面 |
| Go 类型名跟着改 | 字段名必须跟 YAML 走；类型名内部可见，改它只增 churn |

## 7. 改动清单

**A. 配置层**

| 文件 | 改动 |
|---|---|
| `internal/daemon/config.go` | 字段改名；删 `PoolConfig.Enabled`、加 `LegacyEnabled`；加 `LegacyLangs` / `LegacyPool`；`isZero` 纳入 `LegacyLangs`；新增 `applyInteractLegacyKeys` / `legacyIdle` / `warnRemoved`；`applyDefaults` 改为 `Max==0 → Max = Initial`；`validate` 加 `initial > max` 报错 + 路径文案同步 |
| `config.yaml` | `interact.langs`→`pools`、`pool:`→`idle:`、删 `enabled: true`；段头注释改写（开关 = 整条条目 + "整条注释"示范） |
| `main.go` | 注释里的 `interact.langs` → `interact.pools` |
| `Dockerfile` | 注释里的 `interact.langs.*.command` → `langs.<lang>.command`（那一直是顶层 `langs` 的事） |
| `internal/daemon/scripts.go` / `server.go` | 字段跟随；启动日志 `initial=%d max=%d idle=%d` |
| `internal/daemon/pool.go` | `cfg.Pool.*`→`cfg.Idle.*`；删 `<= 0 → 4` 兜底两处；池随语言条目无条件启动 |
| `internal/daemon/unit.go` | `newUnitPool` 删 `if max <= 0 { max = 4 }` |

**B. 测试**：`config_test.go`（fixture 与字段改名 + 新增回填/缺省/校验用例）、`pool_lifecycle_test.go`（字面量改名 + `{0,0}` 档）。

**C. 文档**：`nexus/docs/sandbox-design.md`（`:293` lifecycle 路径、`:304` `pool.initial`、§9 补一句 L2 单元复用现由 `idle.max` 控制）。

## 8. 测试面

| 判定 | 用例 |
|---|---|
| 新键生效 | shipped `config.yaml` 可加载；`Pools["lua"].Idle == {2,16}`；lua 仍 `reuse` |
| 段内旧键收 | `interact.langs.lua.max_concurrent: 3` → 生效 + 1 告警 |
| 语言级旧键收 | `pools.lua.pool.{initial,max}` → 生效 + 1 告警 |
| 三级链 | `session: {langs: {lua: {pool: {enabled: true, initial: 1}}}}` → 全通 + 3 告警 |
| `enabled` 五档 | `true`+数值 → 原样；`true` 无数值 → `{1,4}`；`true`+`initial>max` → 复现旧钳制 `{4,4}`；`false` → `{0,0}` + 说明；**没写 `enabled` → `{0,0}` + 说明** |
| 0 不被改写 | `idle: {initial: 0, max: 8}` → 生效值仍 `0 / 8` |
| max 缺省跟随 | `idle: {initial: 2}` → `{2,2}`；整段省略 → `{0,0}` |
| 矛盾拒绝面 | `idle: {initial: 8, max: 4}` → 启动失败 + 可粘贴修法 |
| `{0,0}` 行为 | 归还即销毁（不复用） |
| 交叉约束 | `pools.ruby` 无顶层定义 → 启动失败，文案含 `interact.pools.ruby` |
| 无旧键无告警 | 只写新键 → `Warnings` 为空 |

## 9. 验证（已执行）

```
cd sandboxd && go build ./... && go test ./...   # 全绿（internal/daemon）
cd tools   && go build ./...                     # 全绿
cd nexus   && go build ./...                     # 全绿（只改了 docs）
```

真机冒烟（Windows，`mode: docker`；本机 docker 守护进程未起，故池预热报错属预期）：

| 场景 | 结果 |
|---|---|
| 旧键配置（顶层 `acquire_timeout_sec` + `interact.langs` + 每个语言的 `pool.enabled/initial/max`）启动 | 起得来；8 条告警 = `acquire_timeout_sec`→`interact.acquire_timeout_sec` 1 + 段级 1 + 每语言 2（`pool`→`idle`、`enabled` 已移除）；`lang lua ready ... idle_initial=2, idle_max=16` |
| 新键配置（随包 `config.yaml`，仅改端口）启动 | 起得来；**零告警**；`idle_initial=2, idle_max=16` 与旧配置逐字一致 |

单元测试覆盖了本机跑不了的格子（`{0,0}` 归还即销毁 + refiller 不补量、`initial > max` 启动失败、各级回填与告警条数）。

未验证（本机无条件）：`mode: docker` 下 `idle.max: 0` 是否真的每次现场建容器 —— 需要 docker 守护进程；代码路径是 `newUnitPool(..., max)` → `release` 落 `default: h.close()`，逻辑同 L3 的 `{0,0}`（后者已被测试覆盖）。

nexus 侧零改动（不读 sandboxd 配置，只下发语言名与超时）。

## 10. 已评估未纳入（勿当待办）

1. `max_concurrent: 1` 变校验（注释说「必须 >= 2」，binding transform 嵌套占两个会话）。
2. `acquire_timeout_sec` 的取值校验（负数/荒谬大值）。
3. `idle: {}`（空段）与省略同义，不报错（本就同值）。

---

## 11. 后续修订：`acquire_timeout_sec` 归位（B 方案）

### 11.1 问题

改完名之后随包配置里出现了"两个同名开关"的观感：

| 位置 | 事实 |
|---|---|
| 顶层 `acquire_timeout_sec: 5` | 全局缺省，所有语言共享 |
| `interact.pools.<lang>.acquire_timeout_sec: 5` | 逐语言覆盖（0/缺省 → 继承） |

两层结构本身是既有的（改造前在 `interact.langs.<lang>` 下），但有两个真问题：

1. 随包配置里三个语言都把它写成 `5` —— 与全局逐字相同，**零信息量**，读者看到的是"两个同名开关，值还一样，到底哪个生效？"
2. **顶层那个字段放错了层**：它只服务双工（单工永不池化、没有"取槽"这回事），却和 `limits` / `exec` 这些单工也要用的东西并排；旁边的 `interact_max_sec` 名字里带 `interact`，它却没带。

### 11.2 结论

- 全局缺省挪进 `interact:` 段 → **`interact.acquire_timeout_sec`**，与 `interact.limits` / `interact_max_sec` 同层；"段级缺省 + 语言级覆盖"这层关系从层级上自明。
- 语言级 `pools.<lang>.acquire_timeout_sec` **保留**为覆盖：各语言 `max_concurrent` 差得多（32/16/8），lua 是 µs 级高频调用，等待越久越容易拖住工作流，值得单独配短。
- 随包配置删掉三处重复的 `5`（继承段级缺省），在 lua 下留一行注释示例。

### 11.3 顺序陷阱（本修订最关键的一处）

`isZero()` 现在要算上 `Interact.AcquireTimeoutSec`（否则 `interact: {acquire_timeout_sec: 10}` 会被判成"段是空的"）。但由此引出一个**回填顺序**要求：

> 顶层 `acquire_timeout_sec` 是**旧键**，它的回填必须排在 `session` 段映射**之后**。

否则「`session: {langs: ...}` + 顶层 `acquire_timeout_sec`」的配置会在 session 段映射那一步被 `isZero` 判成"新段已写" → **整个 session 段被丢弃**（症状：该语言的 interact 请求全变 `unsupported_lang`），而这在旧代码里不会发生（那时它是独立的顶层字段，不参与 `isZero`）。故拆出 `applyLegacyAcquireTimeout()` 单独调用，并配一条专门的回归用例 `TestAcquireTimeoutLegacyDoesNotBlockSessionSection`。

### 11.4 改动与验证

| 位置 | 改动 |
|---|---|
| `config.go` | 删顶层 `Config.AcquireTimeoutSec`，加 `InteractConfig.AcquireTimeoutSec`（`yaml:"acquire_timeout_sec"`）与旧键 `Config.LegacyAcquireTimeoutSec`；`isZero` 纳入新字段；新增 `applyLegacyAcquireTimeout()`（排在 session 段映射之后、段内回填之前）；`applyDefaults` 的段级缺省 5 与语言级继承改指向 `c.Interact.AcquireTimeoutSec` |
| `config.yaml` | 顶层那段注释与键挪进 `interact:`；删三处语言级 `5`；lua 下留 `# acquire_timeout_sec: 2` 示例；`:214` 那句歧义改成 `interact.acquire_timeout_sec（或该语言的覆盖值）` |
| 测试 | 新增 4 条：顶层旧键回填 + 语言级继承、新旧同写以新键为准、**顺序回归守卫**、语言级覆盖/继承；随包配置断言段级 `5` 与 lua 继承 |

验证：`go build ./...` + `go test ./...` 全绿；真机冒烟见 §9（旧配置 8 条告警含 `acquire_timeout_sec` 更名、新配置零告警）。
