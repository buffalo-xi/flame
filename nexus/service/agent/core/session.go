package core

import "strings"

// MetaSessionIDKey params._meta 里承载会话 id 的键。
//
// 会话 id 只认这一处：arguments 里的同名键不再被当作会话开关（只会在入口被 StripSessionID 剥掉）。
const MetaSessionIDKey = "session_id"

// MetaSessionID 从 params 的 _meta 里取会话 id（MCP 与 JSON-RPC 同一位置）。
//
// params 不是对象、或 _meta 不是对象时一律返回空串 = 「没给」，由调用方回落旧位置。
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

// WithSessionID 返回带 session_id 键的参数副本（不修改原 map）。
//
// 给「会话 id 从 _meta 来、但下游只认 args」的场景用（flow 的 NewFlow）。
// 下游会再把它剥离出业务入参，所以这只是一次中转，不是"把控制字段塞进业务参数"。
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

// StripSessionID 返回去掉 session_id 键的参数副本（不修改原 map）。
//
// 会话 id 是控制意图，不该留在业务入参里 —— 否则它会变成模型可见的 {{args.session_id}}，
// 还能和用户自己的同名字段撞车。flow 与 MCP / RPC 三个入口共用这一份，免得各自实现走样。
func StripSessionID(args map[string]any) map[string]any {
	if args == nil {
		return nil
	}
	if _, ok := args["session_id"]; !ok {
		return args // 没有就原样返回，不为绝大多数请求复制一遍 map
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
