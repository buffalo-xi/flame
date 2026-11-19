package core

import "strings"

// MetaSessionIDKey is the key carrying the session id inside params._meta.
//
// The session id is only recognised there: a key of the same name in arguments no longer acts as a session switch (it is merely stripped
// by StripSessionID at the entry point).
const MetaSessionIDKey = "session_id"

// MetaSessionID takes the session id from params' _meta (the same location for MCP and JSON-RPC).
//
// When params is not an object, or _meta is not an object, it returns an empty string = "not given", and the caller falls back to the
// legacy location.
func MetaSessionID(params any) string {
	mp, ok := params.(map[string]any)
	if !ok {
		return ""
	}
	meta, ok := mp["_meta"].(map[string]any)
	if !ok {
		return ""
	}
	s, _ := meta[MetaSessionIDKey].(string)
	return strings.TrimSpace(s)
}

// WithSessionID returns a copy of the arguments carrying a session_id key (the original map is not modified).
//
// It is for the case where "the session id comes from _meta but downstream only reads args" (flow's NewFlow).
// Downstream strips it out of the business arguments again, so this is a pass-through, not "stuffing a control field into business
// parameters".
func WithSessionID(args map[string]any, sid string) map[string]any {
	if sid == "" {
		return args
	}
	merged := make(map[string]any, len(args)+1)
	for k, v := range args {
		merged[k] = v
	}
	merged["session_id"] = sid
	return merged
}

// StripSessionID returns a copy of the arguments without the session_id key (the original map is not modified).
//
// The session id is a control intent and must not stay in the business arguments - otherwise it becomes a model-visible
// {{args.session_id}} and can collide with a user field of the same name. flow and the MCP / RPC entry points share this one copy, so
// none of them drifts.
func StripSessionID(args map[string]any) map[string]any {
	if args == nil {
		return nil
	}
	if _, ok := args["session_id"]; !ok {
		return args // nothing to strip: return as is rather than copying the map for the vast majority of requests
	}
	clean := make(map[string]any, len(args))
	for k, v := range args {
		if k == "session_id" {
			continue
		}
		clean[k] = v
	}
	return clean
}
