// Package mcpspec supports exactly one MCP protocol version.
//
// Since 2026-07-28 the protocol is stateless: there is no initialize/initialized handshake and no
// protocol-level session; every request carries protocolVersion / clientInfo / clientCapabilities in
// its _meta (SEP-2575). We are a stateless multi-instance deployment ourselves, with no Mcp-Session-Id
// stickiness and no session storage, so the old protocol is bound to fail here — therefore no legacy
// version negotiation is kept at all: the server allowlist admits this one version only, and client
// sessions request this one version only.
//
// To talk to an MCP server that only speaks the old protocol, let an external proxy do the adaptation
// (new version outside, old version inside) rather than bringing the old protocol back into nexus.
package mcpspec

// ProtocolVersion is the only MCP protocol version we support.
// The server (ServerOptions.SupportedProtocolVersions) and the client
// (ClientSessionOptions.ProtocolVersion) share it.
//
// Client side, mind this: the SDK's ProtocolVersion is only a "preferred version", and when the peer
// does not know the new protocol it falls back to initialize and accepts the version the peer offers.
// So the negotiated result must be checked again after connecting (connectSession in
// service/mcp/client/conn.go).
const ProtocolVersion = "2026-07-28"
