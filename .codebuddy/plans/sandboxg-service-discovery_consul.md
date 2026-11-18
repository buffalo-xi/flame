# sandboxg 服务发现方案（consul 实现 + nacos 可插拔）

状态：**P1–P5 全部完成（含真机验证）。**

## 0. 目标

- 把 sandboxg 的后端列表来源从静态 yaml 换成**服务发现**，sandboxd 增删无需改 gateway 配置。
- 发现层做成**接口**：现在实现 consul，将来加 nacos 时使用方零改动。
- **负载均衡计算、粘性路由、探活兜底全部保持不变**（发现与负载是正交的两层）。

## 1. 三层职责（互不干扰）

| 层 | 提供者 | 频率 | 说明 |
|---|---|---|---|
| 发现（谁活着、地址） | consul/nacos | 低频（秒级） | `ListInstances` 拿健康实例列表 → 对账增删池 |
| 负载（谁多忙） | 直连 `/v1/metrics` | 高频（2s） | `score = exec + script + active`，`leastLoaded` 选最闲 |
| 粘性（session→后端） | redis / 内存 | 请求热路径 | 不变 |

请求热路径只读内存里的 score，不查 consul、不逐个广播。

## 2. xregistry 重构（已完成）

拆成两个接口，删掉旧的混合大接口：

```go
type Discovery interface {
	ListInstances(ctx context.Context, serviceName string) ([]Instance, error)
}

type Registrar interface {
	Register() error
	Deregister() error
	KeepRegistered(ctx context.Context, interval time.Duration) // 保活自愈
}
```

- `service_registry.go`：`Type` / `Instance` / `Discovery` / `Registrar` / `DiscoveryConfig` / `RegistrarConfig` / `NewDiscovery` / `NewRegistrar` + nacos 占位。
- `consul_registry.go`：`ConsulDiscovery`（`Health().Service(name, "", true, nil)`）+ `ConsulRegistrar`（挂 HTTP check）。

删掉：`GetService` / `GetServiceWithTag` / `GetServiceByID` / `GetAllServices` / `StartHeartbeat` / `RegisterHealthCheck` / `DeregisterHealthCheck` / `ServiceInfo` / `ServiceRegistryType` / `ServiceRegistry` / `ServiceRegistryConfig` / `NewServiceRegistry`。

## 3. 各使用方

| 组件 | 角色 | 接口 | 状态 |
|---|---|---|---|
| sandboxd | 提供者 | `Registrar`（启动注册挂 `/healthz`、退出注销） | P4 ✅ |
| sandboxg | 消费者 | `Discovery.ListInstances("sandboxd")` → `Pool.Reconcile` | P3 ✅ |
| nexus_gw | 提供者+消费者 | `Registrar` + `Discovery`（随机挑一个上游） | P2 ✅（从简） |

## 4. sandboxg 侧新增

- `Pool.Reconcile(instances, token)`：差集对账，新增进池（URL=`http://addr:port`，healthy 默认 true）、消失移出。
- `DiscoveryLoop`：低频对账；失败保留旧列表不摘除；移除的后端逐个 `sticky.RemoveBackend`。
- 健康判定两层：consul passing 列表为主，`checkHealth` 探 `/healthz` 兜底。
- token 约定：共用 `sandbox_token`，consul 只给地址。

## 5. sandboxd 侧新增

- config `registry` 段（`enabled` 默认 false，保住零外部依赖现状）。
- `registryConfig()`：ServiceName 默认 `sandboxd`、HealthCheckPath 默认 `/healthz`、ServicePort 缺省从 listen 解析；ServiceAddr 需显式给 gateway 可达地址。

## 6. 分阶段

| 阶段 | 内容 | 状态 |
|---|---|---|
| P1 | xregistry 重构 | ✅ build+vet 通过 |
| P2 | nexus_gw 从简适配 | ✅ build 通过 |
| P3 | sandboxg 接 Discovery | ✅ build+test 通过 |
| P4 | sandboxd 接 Registrar | ✅ build+vet 通过 |
| P5 | 单测 + 真机验证 | 单测 ✅（TestPoolReconcile）；真机待做 |

## 7. 待定项

1. 轮询 vs watch：P3 用简单轮询，后续可换 consul blocking query/watch。
2. discovery 失败语义：定为「保留旧列表不摘除」。
3. address 可达性：`service_addr` 必须是 gateway 能连到的地址（跨机用内网 IP）。
4. nacos 未实现：接口先就位，实现待定。

## 8. 真机验证（已完成 ✅）

在本机 consul + 2 sandboxd + 1 sandboxg 实测通过：

1. 2 个 sandboxd（`mode:none`，端口 27080 / 27082）注册到 consul，`/v1/health/service/sandboxd` 两个都 `passing`。
2. sandboxg（`discovery.type: consul`，`backends: []`）`/v1/metrics` 显示 2 个后端、`backends_total: 2`。
3. 杀一个 sandboxd，consul check 变 critical 后，sandboxg 池自动缩到 1 个后端（`backends_total: 1`）。
4. 结论：注册 → 发现 → 进池 → 动态移除 全链路正确。

**过程中发现并修掉的 bug**：consul HTTP check 的 URL 缺 `http://` scheme（报 `parse "127.0.0.1:27080/healthz": first path segment in URL cannot contain colon`），已在 `ConsulRegistrar.Register` 补上 `http://` 前缀。

**多实例部署注意事项（配置层）**：每个 sandboxd 实例需要独立的 `tmp_dir`（和 `log_dir`）——sandboxd 启动时会整棵清空 `tmp_dir`，两个实例共用会因文件占用而启动失败（Windows 上 `unlinkat ... being used by another process`）。

**注册自愈（KeepRegistered，后补）**：`Registrar` 接口新增 `KeepRegistered(ctx, interval)` 保活自愈——定期确认自己还在注册中心，被摘除（如注册中心断连）则重新注册；覆盖「初始注册失败」与「运行中失联被摘除」两类自愈。真机验证：手动 `deregister` sandboxd 后，33s 内自动重注册并恢复 `passing`。
