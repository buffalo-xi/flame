# 设计要点

这份文档只讲**为什么这么设计**，不讲怎么用。每条结论后面标了实现位置，便于核对。

四条主线：**调用要快**、**递归要收敛**、**租户要有边界**、**端点要能复用**。

---

## 1. 速度：能不付的远程成本就不付

### 端点直调是进程内函数调用

`rpc` 节点调用另一个 flow 或 agent，走的是 `runEndpoint`，没有 HTTP / RPC wire。
agent 的 `local__<端点名>` 工具同理，靠一个注册点把「跑 flow」的能力倒置出去，
避免 `executors` 与 `service/agent` 互相 import 成环。

实现：`nexus/service/flow/executors/rpc_target.go`、`nexus/service/flow/executors/localendpoint.go`。

**代价**：这条链假定目标在同进程，跨节点部署需要换实现。这是明确的取舍，不是遗漏。

### stdio 子进程池

MCP / RPC 的 stdio 传输要 fork 子进程 + 握手，是最贵的一环。
`SessionPool` 一个 `conn.ID` 复用一个会话，用 `command + args + work_dir + env` 的
sha256 指纹判断是否需要重建；子进程退出只打 `closed` 标记、不删 map，避免并发 `Get`
误判后重复拉起。`streamable_http` 是无状态短连接，**不进池**。

实现：`nexus/service/rpc/client/pool.go`（MCP 侧同构）。

### 配额检查走 O(1) 快路径

写文件时先读计数，只有「超限」或「计数过期」才扫目录；扫描完把真实值回写，
后续继续走快路径。过期判定除了定期校准，还有一条「接近上限时缩短校准间隔」，
防止「以为 92% 其实已 99%」。

实现：`nexus/shared/quota/admit.go`。

### 存储层避免 N 次 LIST

递归列目录用可选接口 `Walker.WalkPrefix`（S3 上一次翻页拿整棵子树），
取文件大小用 `Stat`（S3 上是 HEAD，不是 LIST 父目录）。
配额淘汰正需要整棵树的 `(路径, 大小, mtime)`，所以这个能力是为它存在的。

实现：`nexus/shared/storage/storage.go`。

### 配置与快照

租户级配置走 Redis 读穿缓存，命中直接返回；未命中或 Redis 不可用则回源 DB 并回填。
两个细节：**负缓存**（把「该行不存在」也缓存成 `"null"`，否则租户没配就是每次打空库）、
**Redis 挂了直接跳过**而不是等超时。

运行期不在循环里 `Resolve`，而是在 run 开始时快照一次（`ExecContext.Limits`）。
端点目录同理，按一次运行做快照 —— 一轮 agent 循环里可能连着调好几次 `local__`。

实现：`nexus/shared/agentconfig/cache.go`、`nexus/shared/quota/cache.go`。

---

## 2. 防环：深度上限 + 祖先链，不用计数器

### 为什么不能用计数器

`ExecContext` 里的执行计数只能管「一次 run 内的节点次数」。而端点链每跨一层就会
新建一个 `ExecContext`，计数各自归零。所以防环必须靠**跨 run 传递的状态**。

### 两道闸，各管各的语义

| 闸 | 位置 | 默认上限 | 管什么 |
|---|---|---|---|
| flow 链深度 | `runEndpoint` | `max_recursion_depth` = 10 | 纯 flow 编排（A→B→C→D 流水线） |
| agent 链深度 | `service/agent/localendpoint.go` | `max_call_depth` = 3 | 每回到一次 agent 就多跑一整轮 LLM |

agent 那道闸放在「查端点之前」—— 超限时连库都不查。

### 「不限制」这个配置在这里不认

别的配置项 `-1` 表示不限（配额、超时），但深度上限刻意收窄成正数：
纯 flow 的环只有这一道闸，允许它被一次误配置关掉等于敞开。
`effectiveEndpointDepthLimit` 的兜底顺序是「配置 → fallback → 10」，绝不放行无上限。

### 祖先链：提前失败，且不误伤扇出

深度上限只能「跑满 10 层再报」，`f → a → f` 这种立刻能看出来的环不该等。
链里只放**祖先**，所以同一 flow 的多个 `rpc` 节点并行调同一个目标（平级扇出）不会被误判。
检查放在深度检查之后 —— 「超过 10 层」比「检测到循环」更好理解。

### 链怎么传：保留键 + 剥离

跨 run 只能靠参数：`_call_depth` / `_call_chain` 作为保留键注入，`NewFlow` 入口剥离，
模型看到的业务入参是干净的。注入前**复制 map**，不改调用方持有的那份。
`extractCallDepth` 同时兼容 JSON 反序列化的 `float64` 与进程内直调的 `int`，
非法值按 0 处理 —— 深度只用于限流，不因脏数据拒正常调用。

### 诚实的前提

链是**调用方自报**的，不传就检测不到。所以深度上限必须一直留着兜底，
外部入口（RPC / MCP）只把链当「精确拦截」而非唯一防线。

---

## 3. 多租户：显式隔离 + 三层收紧

### 一租户一 schema，且 SQL 显式限定

不靠 `search_path` 全局设置（连接池复用下不可靠），每个查询都拼 `authInfo.Schema`。
代码啰嗦一点，但永远不会「连接被换掉后查错库」。

`search_path` 只在开通事务内用一次，且是**事务级 + 参数化**。

### 开通 = 一个事务 + 一条回滚路径

顺序即设计：先做全部校验（不碰库）→ 单事务内 `CREATE SCHEMA` + 建表 + 预置资产 +
首个管理员 + 注册表行 → 提交后失效缓存。文件写失败就 `DeletePrefix` 回收，
保证「要么是一个完整租户，要么什么都没留下」。

并发抢占由 `42P06` 识别成「schema 已占用」，接口回 409 让操作者换候选，而不是 500。

实现：`nexus/platform/provision/provision.go`。

### 配额与运行参数：系统硬顶 → 租户 → 用户

合并规则统一：`NULL` = 未设置，回退下一级；结果再按**系统行**截断。
所以租户只能在平台给的天花板内收紧，用户只能在租户天花板内再分配。
合并层还吃掉脏值（`-1` / `0` 一律回落 fallback），让消费点永远拿到正数。

用户配额单独一张 `user_quotas` 表，而不是往 `users` 加列 ——
认证表不承载资源治理，将来加维度只动这张表。

实现：`nexus/shared/quota/quota.go`、`nexus/shared/agentconfig/agentconfig.go`。

### 授权绑目标，不绑端点

同一个 agent 通常有 rpc / mcp 两条端点。按端点授权会出现「只勾了 rpc，MCP 连接就全空」
以及「端点重发后授权失效」。所以白名单里的 key 是 `target_type:target_id`。

三档语义：不限制 / 空集合（一个都不允许）/ 白名单。另有「不限制但排除超级智能体」的中间档，
避免「要么全开、要么全关」。

### 越权不泄漏

外部入口的检查顺序是**授权 → 开放状态 → 环路**：
无权调用方连「这个端点存在但没开放」都探不到，未授权与不存在用完全相同的措辞。

---

## 4. 端点：一行「目标 × 协议」

### 端点不是新实体

`svc_endpoints` 的一行 = `target_type`（agent / flow）+ `protocol`（rpc / mcp）+ `exposed`。
新增协议只要加一行，不用复制业务逻辑。`input_schema` 对 flow 端点恒等于 `flows.arguments`，
避免「端点入参和 flow 入参不一致」。

### `exposed` 只管对外

- 对外（MCP / RPC 客户端）：必须 `exposed = true`；
- 对内（本租户 `rpc` 节点 / `local__` 工具）：**不校验** `exposed`。

收益：「内部编排用的子流程」不必为了能被调用而被迫对外开放。
代价是必须保证内部调用不能从外部触发 —— 所以外部入口的检查一点都不能省。

### 按名字定位，不按 id

端点名恒等于实体名，创建时保证唯一，实体改名时 `syncEndpoints` 同步；
而 id 在「导出 → 导入到另一个租户」后会重新生成，diagram 里的 id 就废了。

查找必须**精确匹配**：`ListAll` 的 name 过滤是 `ILIKE '%name%'`，
`poet` 会命中 `poet-2`，模型按名字可能调到另一个端点。

### 收回 ≠ 删除

`retract` 只置 `exposed = false`，端点记录与配置保留；`delete` 才是删行。
这样「收回对外」不会连带让按目标的凭证授权失效。

术语红线见 `nexus/docs/terminology.md`：端点域用 `expose` / `retract`，
不用 `hide`，也不用 `publish`。

---

## 5. 已知取舍

写在这里，避免后来者以为是遗漏：

- **进程内直调是速度红利，也是扩展性天花板。** 跨节点部署时这条链要换实现（停止信号已经走 Redis 广播）。
- **深度上限依赖配置。** `-1` 在这里被刻意收窄，是权宜但正确的做法；长期应把「不允许配成不限」写进管理界面校验。
- **祖先链依赖调用方自报。** 伪造 / 省略只能靠深度上限兜底。
- **`schema` 拼接分散在各 repository。** 安全，但把正确性责任摊薄了；后续可考虑强制经过租户包装的 query builder。
- **`List` 默认 `limit=10` 会静默截断。** 目前靠 `ListAll` 约定 + 注释规避。
