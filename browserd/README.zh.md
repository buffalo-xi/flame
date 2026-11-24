# browserd

中文 | [English](README.md)

独立浏览器池：一个活会话一个 Chromium，自带容量上限与空闲回收。它既是独立服务、**也是独立 Go module** —— 浏览器
200-400 MB 且带登录态，不能和执行沙箱（`../sandboxd`）共享生命周期，也不该共享发布节奏。

## nexus 怎么调它

nexus 访问 `/v1/browser`，用的是与 sandboxd `/v1/interactive` **完全相同的一套帧**（open 帧 + `ChanStdin` + kill），
因此直接复用 `flame/tools/sandbox.InteractiveClient`：换的是 URL，不是客户端。

多实例部署时那个 URL 是 **browserg**（`../browserg`，默认 `:27090`），不是本服务：会话粘性与租户配额由网关负责。
单实例部署可以把 nexus 直接指到本服务（`:27091`）——两种接法的协议完全一致。

- open 帧里的 `argv` 与 `mounts` 被忽略 —— 启动哪个驱动、哪个 Chromium 由本服务决定。
- `Spec.Dir` 会被采用，但语义是**相对 `workspace_root` 的工作区路径**，不是沙箱内路径。
- `GET /v1/capabilities` 是 nexus 周期性拉的纯 HTTP 快照（可用性、引擎版本、workers、队列、headed 支持）。
- 通道上的 `?tenant=` 是租户配额键，不是隔离边界。

## 构建

```bash
# 本目录，宿主开发用
go build ./...

# 供镜像使用的交叉编译二进制（与 sandboxd 的做法一致）
GOOS=linux GOARCH=amd64 go build -o bin/browserd-linux ./cmd/browserd
GOOS=linux GOARCH=amd64 go build -o bin/browserhost-linux ./cmd/browserhost
docker build -t flame/browserd:latest .
```

`browserhost` 是容器内驱动：stdin/stdout 一行一个 JSON，第一次操作时才拉起 Chromium。`browserclient` 是手工验证工具。

## 启动

```bash
./browserd                                       # 默认读 ./config.yaml
./browserd -config /etc/browserd/config.yaml     # 指定其它路径
```

`-config` 默认是相对工作目录的 `config.yaml`；镜像里显式传绝对路径，因为它不在 `/etc/browserd` 下工作。

下面三项必须与部署对齐，否则浏览器工具会悄无声息地不可用：

| 配置 | 必须对上 |
|---|---|
| `workspace_root` | nexus 的 `storage.root_dir` —— **同一个目录**，而不只是同一个字符串（本机开发：同一个绝对路径；容器：同一个卷挂载）。截图与下载写在这里，租户再通过 nexus 读到它们。文件系统层面写错不会报错，所以启动时若该根下没有 `tenants/` 子目录会打一条 error 日志。 |
| `profile_root` | 只属于本服务的可写卷。profile 是私有状态，会话结束即删除。 |
| `token` | nexus 的 `browser.token`。留空表示不校验（仅限内网）。 |

nexus 侧：`browser.url`（留空 = 该部署没有浏览器）与 `browser.token`。

**服务注册**：`registry.enabled: true` 会让本实例在 HTTP 起来之后注册到 consul/nacos 并持续保活，browserg 下一次拉取
就能发现它 —— 起第二个实例不需要重启网关。`service_addr` 可以留空，由 `listen` 推导；注册端口恒随 `listen`。服务 ID
默认是 `<名称>-<地址>-<端口>`，多台机器注册到**同一个** agent 也不会互相覆盖，不需要手配。注册中心不可达不会影响本服务
提供能力，只是让它对发现不可见；如果这一点不能接受，就同时把它留在网关的静态 `backends` 里。

## 需要知道的行为

- **一会话一 Chromium**，跨会话不复用 —— 复用等于把一个租户的 cookie 交给下一个。池化的是容量：`workers`、
  `max_per_tenant`、排队，以及 `idle_timeout_sec` + `janitor_sec` 的空闲回收。
- **不带 spec 的 open 帧绝不创建浏览器。** 探测与 kill 都走这条路径，所以停止一次运行不会先拉起一个浏览器再杀掉。
- 断连不杀浏览器（cookie 与标签页保留）；kill 帧才杀，并删除 profile。

## 验证

```bash
go test ./...                  # 单测：不需要安装浏览器
go vet ./...
```

端到端测试需要一个正在运行的池：

```bash
BROWSERD_TEST_URL=ws://127.0.0.1:27091/v1/browser BROWSERD_TEST_WORKSPACE=e2e go test ./internal/browserpool -run E2E -v
```

或者用真实协议手工跑：

```bash
go run ./cmd/browserclient -session s1 -tenant t0 -workspace e2e \
  -op '{"id":1,"op":"open","url":"https://example.com"}'
```
