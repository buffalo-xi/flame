# browserg

中文 | [English](README.md)

浏览器池前面的网关。它对 nexus 暴露 `/v1/browser` 与聚合后的 `/v1/capabilities`，把会话按粘性路由到持有其 Chromium 的
实例，并在整个池的范围执行租户上限。

它与 `../sandboxg` **刻意分成两个服务、两个 module**。两个网关长得像，但浏览器通道与沙箱通道在三处结构性问题上不同：

1. 这里的 `/v1/capabilities` 是**池**的答案（`available` = 任一实例、`headed_available` = 全部、计数求和），而 sandboxg
   是透传某一实例的文档 —— 而 nexus 是从 `browser.url` 派生这个地址的。
2. shell 通道与浏览器通道用同一个 `session_id`，却必须落在不同的池，粘性命名空间无法共用。
3. 容量不可比：sandboxg 按活进程数打分，浏览器的单位是一个活着的 Chromium。

合在一起还意味着一次重启会同时断掉所有浏览器连接和所有 exec 路由。

## 接线

```
nexus ──browser.url──> browserg :27090 ──backends──> browserd :27091
                                                     browserd :27091 ...
```

nexus 侧不需要改代码：`browser.url` 指向网关，它用到的两条路径（`/v1/browser`、`/v1/capabilities`）语义不变。
单实例部署可以把 `browser.url` 直接指向某个 browserd —— 协议完全一致。

## 行为

| | |
|---|---|
| 路由 | 凡带会话键的 open 帧都粘（无会话运行把 run id 放在同一个字段）；`?tenant=` 原样转发 |
| 容量 | `queue × 1_000_000 + sessions × 1_000 / workers` —— 排队优先，占用率打破平局 |
| 配额 | `max_per_tenant` 只在这里执行（各实例 `/v1/sessions` 的 live 计数 + 短命的 pending 计数）；browserd 自己的上限保持 0 |
| 绑定丢失 | open 以 `session lost` 失败 —— 浏览器搬不了家，在别处悄悄重建等于把丢失藏起来 |
| `session_empty` | 干净信号：解绑，不报错 |

## 配置

- `sticky_ttl_sec` 必须大于每个实例的 `idle_sec + 2×janitor_sec`（运行时校验，不足会打 error）—— 否则一个会话可能
  会拥有两个 Chromium。
- 只要网关不止一个副本，`redis` 就是必需的；内存绑定只存在于单个进程内。redis 连不上是**启动失败**，所以本机没有
  redis 时把 `addr` 留空。
- `backends` 是静态池，或者在配了 `discovery.type` 时只作为种子（`service_name: browserd`）。
- **默认拓扑就是服务发现**：每个 browserd 都注册（`registry.enabled: true`，两边 `service_name` 一致），因此扩缩容
  池子不用改这个文件 —— 新实例在下一次拉取时加入，注销的实例离开且其绑定被清除。`backends` 只是「首次拉取之前」与
  「注册中心故障期间」的种子。

## 部署

同一网关后面的每个 browserd 必须有**同一个** `workspace_root`，且该目录必须是 nexus 的 `storage.root_dir`。否则截图
会落在恰好服务该会话的那台机器上，nexus 读不到 —— 而且任何地方都不会报错。

## 验证

```bash
go test ./...     # 单测，不需要浏览器
go build ./...
```

用真实协议手工验证 —— 把 `browserclient` 指向网关而不是某个实例：

```bash
cd ../browserd && go run ./cmd/browserclient -url ws://127.0.0.1:27090/v1/browser -session s1 -tenant t0 \
  -op '{"id":1,"op":"open","url":"https://example.com"}'
```
