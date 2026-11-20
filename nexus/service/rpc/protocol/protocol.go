// Package protocol defines the protocol version and envelope structures of our own RPC.
// Both server and client import this package so the version constant and the structures cannot drift.
package protocol

// CurrentProtocolVersion is the current protocol major version.
// It only increments on an incompatible change (dropping a field, changing a response structure or the
// error semantics).
//
// Adding an optional field does not bump the version: the side that does not implement it yet ignores
// the unknown field, behaviour degrades to "no progress subscription", and the call still succeeds —
// that is a compatible change. params._meta.progressToken and progressToken / progress / total in the
// notifications all arrived this way, which is why the version is still 1.
const CurrentProtocolVersion = 1

// MinSupportedVersion is the oldest major version supported for backward compatibility.
// Clients below it are rejected explicitly rather than silently degraded.
const MinSupportedVersion = 1

// ErrCodeInvalidParams is the JSON-RPC 2.0 standard error code (invalid params), reused when the
// protocol version does not match.
const ErrCodeInvalidParams = -32602

// Request is the JSON-RPC request envelope sent by the client.
type Request struct {
	Jsonrpc string      `json:"jsonrpc"`
	Version int         `json:"version,omitempty"` // protocol major version; 0 (absent) is treated as MinSupportedVersion
	Method  string      `json:"method"`
	Params  interface{} `json:"params"`
	ID      interface{} `json:"id"`
}

// VersionError is the error data structure returned when the protocol version is unsupported
// (aligned with the supported feedback of MCP).
type VersionError struct {
	Requested int   `json:"requested"`
	Supported []int `json:"supported"`
}

// NormalizeVersion normalizes a version number: 0 (absent) is treated as MinSupportedVersion.
func NormalizeVersion(v int) int {
	if v == 0 {
		return MinSupportedVersion
	}
	return v
}

// IsVersionSupported reports whether the version is within the range the server supports.
func IsVersionSupported(v int) bool {
	return v >= MinSupportedVersion && v <= CurrentProtocolVersion
}
