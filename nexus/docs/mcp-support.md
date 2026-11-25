# MCP support: one protocol version, and it is stateless

[中文](mcp-support.zh.md) | English

Flame speaks one MCP protocol revision and only one: `2026-07-28`. There is no version negotiation and no fallback to
the older handshake protocol, in either direction.

Since `2026-07-28` MCP is stateless: there is no `initialize` / `initialized` handshake, there is no protocol-level
session (no `Mcp-Session-Id`, nothing a load balancer has to stick), and every request is self-describing —
`protocolVersion`, `clientInfo` and `clientCapabilities` travel in `params._meta` (SEP-2575). Flame is itself a
stateless multi-instance deployment with no session store, so the earlier stateful protocol has nothing to bind to
here; adapting it belongs in a proxy, not in nexus.

## As a server: exposed endpoints

An exposed endpoint is one MCP tool — tool name = endpoint name, input = the endpoint's `{schema}` — and `tools/list`
returns only what the caller's credential may reach. The server admits a single version:

| Client | What happens |
|---|---|
| `_meta.protocolVersion = 2026-07-28` | served |
| a new-protocol client announcing another version in `_meta` | `-32022 UnsupportedProtocolVersion` |
| an old client (`2025-11-25` and earlier), i.e. one that opens with `initialize` | negotiated down to `2025-11-25`, then the next request's `MCP-Protocol-Version` header is refused by the transport with `400 Unsupported protocol version` |

The last two rows are what people trip over: a client that appears to connect is not necessarily a client that can
call anything.

## As a client: outbound MCP connections

Outbound connections (`streamable_http` or `stdio`) request `2026-07-28` as their preferred version. The SDK treats
that as a *preference*: against a peer that does not know the new protocol it falls back to `initialize` and accepts
what the peer offers, so Flame re-checks the negotiated version after connecting and fails the connection instead of
silently degrading — an old server surfaces as a connection error, not as a connection with missing tools.

## Connecting an old peer

Put a protocol-adapting proxy in front: new protocol on the Flame side, old protocol on the peer side. That is the only
supported shape; the product carries no second protocol implementation.
