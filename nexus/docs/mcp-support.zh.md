# MCP 支持：只有一个协议版本，而且是无状态的

中文 | [English](mcp-support.md)

Flame 只支持一个 MCP 协议版本：`2026-07-28`。两个方向都不做版本协商，也不回落到旧版的握手协议。

`2026-07-28` 起 MCP 是无状态的：没有 `initialize` / `initialized` 握手，没有协议层会话（没有 `Mcp-Session-Id`，
负载均衡不需要做粘性），每个请求自带 `protocolVersion`、`clientInfo`、`clientCapabilities`——它们在
`params._meta` 里（SEP-2575）。Flame 本身就是无状态、多实例、没有会话存储的部署，旧的有状态协议在这里没有东西
可以绑定；要兼容旧协议，适配应当放在代理里，而不是塞回 nexus。

## 作为服务端：对外开放的端点

一个对外开放的端点就是一个 MCP 工具——工具名 = 端点名，入参 = 端点的 `{schema}`，`tools/list` 只列出当前凭证
有权访问的端点。服务端只放行一个版本：

| 客户端 | 结果 |
|---|---|
| `_meta.protocolVersion = 2026-07-28` | 正常服务 |
| 新协议客户端在 `_meta` 里报了别的版本 | `-32022 UnsupportedProtocolVersion` |
| 旧协议客户端（`2025-11-25` 及更早），即以 `initialize` 开场的 | 被协商到 `2025-11-25`，随后那个请求的 `MCP-Protocol-Version` 头被传输层以 `400 Unsupported protocol version` 拒绝 |

后两种是最容易踩的：能「连上」的客户端，不一定是能调用的客户端。

## 作为客户端：出站 MCP 连接

出站连接（`streamable_http` 或 `stdio`）以 `2026-07-28` 为首选版本。SDK 把它当**偏好**：对端不认识新协议时会退回
`initialize` 并接受对端给出的版本，所以 Flame 在连接后复核协商结果，不匹配就让这次连接失败，而不是静默降级——
旧服务端表现为连接错误，而不是连上了却没有工具。

## 对接旧的对端

在前面放一个协议适配代理：Flame 这一侧用新协议，对端那一侧用旧协议。这是唯一支持的形态；产品内不带第二套协议实现。
