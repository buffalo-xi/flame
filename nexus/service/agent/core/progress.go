package core

import (
	"encoding/json"
	"math"
	"regexp"
	"strings"
)

// Streaming output categories (the _meta.category of a notifications/progress frame).
//
// They are all "process": the protocol only recognises the result / CallToolResult of the response as the final result, and no
// notifications/progress frame constitutes one, so there is no final-like tier here.
const (
	CategoryThinking   = "thinking"    // model reasoning delta
	CategoryText       = "text"        // text delta (agent body / the progress and result channels of a flow)
	CategoryToolCall   = "tool_call"   // a tool is starting
	CategoryToolResult = "tool_result" // a tool finished
)

// ProgressItem is one piece of streaming output ready to be sent out.
type ProgressItem struct {
	Category string
	Message  string
	// Meta holds structured extra fields (tool events only): call_id / tool / args / ok / duration_ms / agent
	Meta map[string]any
}

var progressMarkerRe = regexp.MustCompile(`\[\[(?:action|think|tools)\]\].*?\[\[/(?:action|think|tools)\]\]`)

// CleanProgressMarkers cleans the internal markers out of a stream:
// - [[action]]/[[think]]/[[tools]] blocks are removed
// - the [[content]] prefix is replaced by the content separator "."
//
// mcp/server, rpc/server and flow/executors each carried a copy of this; they are unified here.
func CleanProgressMarkers(s string) string {
	s = progressMarkerRe.ReplaceAllString(s, "")
	return strings.ReplaceAll(s, "[[content]]", ".")
}

// ToProgress turns one agent event into streaming output.
// ok=false means there is nothing to send (such as a text delta that is empty after cleaning).
func (ev Event) ToProgress() (ProgressItem, bool) {
	switch ev.Type {
	case EventThinking, EventText:
		msg := CleanProgressMarkers(ev.Delta)
		if msg == "" {
			return ProgressItem{}, false
		}
		category := CategoryThinking
		if ev.Type == EventText {
			category = CategoryText
		}
		return ProgressItem{Category: category, Message: msg}, true
	case EventToolCall:
		meta := map[string]any{"call_id": ev.CallID}
		if ev.Tool != "" {
			meta["tool"] = ev.Tool
		}
		if ev.Args != "" {
			meta["args"] = ev.Args
		}
		if ev.Agent != "" {
			meta["agent"] = ev.Agent
		}
		return ProgressItem{Category: CategoryToolCall, Message: ev.Tool, Meta: meta}, true
	case EventToolResult:
		meta := map[string]any{
			"call_id":     ev.CallID,
			"ok":          ev.OK,
			"duration_ms": ev.DurationMS,
		}
		if ev.Tool != "" {
			meta["tool"] = ev.Tool
		}
		if ev.Agent != "" {
			meta["agent"] = ev.Agent
		}
		return ProgressItem{Category: CategoryToolResult, Message: ev.Result, Meta: meta}, true
	}
	return ProgressItem{}, false
}

// ===== Progress subscription: the sending side of notifications/progress =====
//
// MCP 2026-07-28's rules on progress state two hard constraints, and missing either one makes a notification non-compliant:
//  1. without a progressToken none may be sent (a notification MUST only reference a token "provided in an active request");
//  2. progress MUST increase on every frame (even without a total).
//
// Binding both into one object leaves the caller no room to remember only one of them.
// The earlier shape was "a stream boolean in the request arguments" plus "a server-side fallback UUID token":
// the former was in the wrong place (a control parameter mixed into business arguments, which also polluted a flow's business
// variables), and the latter directly violated 1.

// ProgressPump is one call's progress sender: it holds the client's progressToken and the monotonically increasing sequence.
//
// Without a token Enabled() is false, and the caller should take the "run synchronously, push nothing" path - that is not a degraded mode
// but the default the spec requires (a server MAY send progress, provided the client subscribed).
type ProgressPump struct {
	Token any
	seq   float64
}

// NewProgressPump creates a sender. A nil token means the client did not subscribe to progress.
func NewProgressPump(token any) *ProgressPump { return &ProgressPump{Token: token} }

// Enabled reports whether the client subscribed to this call's progress.
func (p *ProgressPump) Enabled() bool { return p != nil && p.Token != nil }

// Next returns the next progress sequence number (starting at 1).
// Only call it when Enabled() is true; a pump is consumed by a single goroutine, so no lock is needed.
func (p *ProgressPump) Next() float64 {
	p.seq++
	return p.seq
}

// LegacyStreamKey is the key of the deprecated "should this stream" switch inside the business arguments.
//
// It used to be what both the mcp and rpc servers decided on, but it sat in the wrong place: a control parameter mixed into business
// arguments is treated as a flow / agent business variable, and it duplicated params._meta.progressToken.
const LegacyStreamKey = "stream"

// StripLegacyStream strips the deprecated arguments.stream and reports whether anything was actually stripped (the caller logs on it).
//
// The strip is kept rather than simply ignored so that it is neither interpreted as a control parameter nor flows into a flow / agent as a
// business variable - otherwise a flow that happens to have an argument of the same name would be silently overwritten by the caller's
// old configuration.
func StripLegacyStream(input map[string]any) bool {
	if _, existed := input[LegacyStreamKey]; !existed {
		return false
	}
	delete(input, LegacyStreamKey)
	return true
}

// ValidProgressToken applies the spec's constraint: progressToken must be a string or an integer, and anything else counts as not
// provided.
// JSON-decoded integers are float64, so the test is "is it an integer value" (1.5 and the like are refused).
func ValidProgressToken(v any) bool {
	switch t := v.(type) {
	case string:
		return t != ""
	case float64:
		return !math.IsNaN(t) && !math.IsInf(t, 0) && t == math.Trunc(t)
	case int, int32, int64, json.Number:
		return true
	}
	return false
}
