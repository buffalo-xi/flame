// Package mcpspec 只认一版 MCP 协议。
//
// 2026-07-28 起协议是无状态的：没有 initialize/initialized 握手、没有协议级会话，
// 每个请求自带 _meta 里的 protocolVersion / clientInfo / clientCapabilities（SEP-2575）。
// 我们本身又是无状态多实例部署，没有 Mcp-Session-Id 粘滞与会话存储，旧协议在这里注定失败，
// 所以不再保留任何旧版本协商：服务端白名单只放这一版，客户端会话也只请求这一版。
//
// 需要接老协议的 MCP server 时，由外部代理（对外新版 / 对内老版）承担适配，
// 不把老协议重新引回 nexus。
package mcpspec

// ProtocolVersion 我们唯一支持的 MCP 协议版本。
// 服务端（ServerOptions.SupportedProtocolVersions）与客户端（ClientSessionOptions.ProtocolVersion）共用。
//
// 客户端侧要留意：SDK 的 ProtocolVersion 只是"首选版本"，对端不认新协议时会退回
// initialize 并接受对端给的旧版本，所以建连后必须再校验一次实际协商结果
// （service/mcp/client/conn.go 的 connectSession）。
const ProtocolVersion = "2026-07-28"
