package plugin

import (
	"errors"
	"fmt"
)

// Call error codes, aligned with the flow error codes (see service/flow/core). The single semantic
// rule: **only CodeConfig skips the fallback chain** — a missing key or a missing implementation
// will not be fixed by trying another implementation.
const (
	CodeAuth    = "auth"    // authentication failed (wrong or expired key, insufficient permission)
	CodeQuota   = "quota"   // rate limited / quota exhausted
	CodeNetwork = "network" // cannot connect, timeout, 5xx
	CodeConfig  = "config"  // wrong configuration (missing field, invalid URL, invalid argument)
	CodeOther   = "other"
)

// CallError is one failed plugin call, carrying a decidable error code.
type CallError struct {
	Code    string
	Message string
}

func (e *CallError) Error() string {
	return fmt.Sprintf("plugin call failed (%s): %s", e.Code, e.Message)
}

// Code returns the error's category; anything that is not a CallError counts as other.
func Code(err error) string {
	var ce *CallError
	if errors.As(err, &ce) {
		return ce.Code
	}
	return CodeOther
}

// normalizeCode narrows a plugin-reported error code onto the known set (anything unknown becomes
// other).
func normalizeCode(code string) string {
	switch code {
	case CodeAuth, CodeQuota, CodeNetwork, CodeConfig:
		return code
	default:
		return CodeOther
	}
}
