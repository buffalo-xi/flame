package core

import (
	"encoding/json"
	"math"
	"regexp"
	"strings"
)

// 流式输出分类（notifications/progress 帧的 _meta.category）。
//
// 全部都是「过程」：协议里的最终结果只认响应中的 result / CallToolResult，
// notifications/progress 里的任何一帧都不构成结果，所以这里没有 final 之类的档位。
const (
	CategoryThinking   = "thinking"    // 模型推理增量
	CategoryText       = "text"        // 文本增量（agent 正文 / flow 的 progress 与 result 通道）
	CategoryToolCall   = "tool_call"   // 开始执行工具
	CategoryToolResult = "tool_result" // 工具执行完成
)

// ProgressItem 一条可外发的流式输出。
type ProgressItem struct {
	Category string
	Message  string
	// Meta 结构化附加字段（仅工具类事件）：call_id / tool / args / ok / duration_ms / agent
	Meta map[string]any
}

var progressMarkerRe = regexp.MustCompile(`\[\[(?:action|think|tools)\]\].*?\[\[/(?:action|think|tools)\]\]`)

// CleanProgressMarkers 清理流里的内部标记：
// - 删除 [[action]]/[[think]]/[[tools]] 区块
// - 将 [[content]] 前缀替换为内容分隔符 "."
//
// 原 mcp/server、rpc/server、flow/executors 三处各有一份同名实现，统一到这里。
func CleanProgressMarkers(s string) string {
	s = progressMarkerRe.ReplaceAllString(s, "")
	return strings.ReplaceAll(s, "[[content]]", ".")
}

// ToProgress 把一条 agent 事件转成流式输出。
// ok=false 表示无需外发（如清理后为空的文本增量）。
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

// ===== 进度订阅：notifications/progress 的发送侧 =====
//
// MCP 2026-07-28 对进度的规定里只有两条硬约束，缺一条通知就不合规：
//  ① 没有 progressToken 就不得发（通知 MUST 只引用「活跃请求里提供过的 token」）；
//  ② progress 每次 MUST 递增（即使没有 total）。
//
// 把这两条绑进同一个对象，调用方就没有「只记起一条」的余地。
// 早先的写法是「请求参数里的 stream 布尔值」+「服务端兜底生成一个 UUID token」：
// 前者位置错（控制参数混进业务入参，还会污染 flow 的业务变量），后者直接违反 ①。

// ProgressPump 一次调用的进度发送器：持有客户端给的 progressToken 与单调递增序号。
//
// 没有 token 时 Enabled() 为 false，调用方应走「同步执行、一帧不推」的路径 ——
// 这不是降级，而是规范要求的默认行为（server MAY 发进度，前提是客户端订阅了）。
type ProgressPump struct {
	Token any
	seq   float64
}

// NewProgressPump 创建发送器。token 为 nil 表示客户端没订阅进度。
func NewProgressPump(token any) *ProgressPump { return &ProgressPump{Token: token} }

// Enabled 客户端是否订阅了本次调用的进度。
func (p *ProgressPump) Enabled() bool { return p != nil && p.Token != nil }

// Next 取下一个进度序号（从 1 开始）。
// 只在 Enabled() 为真时调用；同一 pump 由单协程消费，无需加锁。
func (p *ProgressPump) Next() float64 {
	p.seq++
	return p.seq
}

// LegacyStreamKey 已废弃的「要不要流式」开关在业务入参里的键名。
//
// 它曾经是 mcp / rpc 两个 server 的判定依据，但位置错了：控制参数混在业务入参里，
// 会被当成 flow / agent 的业务变量，也与 params._meta.progressToken 重复。
const LegacyStreamKey = "stream"

// StripLegacyStream 剥掉已废弃的 arguments.stream，返回是否真的剥掉了（调用方据此记日志）。
//
// 保留剥除动作（而不是直接忽略）的理由：让它既不被解释成控制参数，也不作为业务变量
// 流进 flow / agent —— 否则某个 flow 恰好有同名入参时会被调用方的旧配置意外覆盖。
func StripLegacyStream(input map[string]any) bool {
	if _, existed := input[LegacyStreamKey]; !existed {
		return false
	}
	delete(input, LegacyStreamKey)
	return true
}

// ValidProgressToken 规范约束：progressToken 必须是 string 或 integer，其余一律视为未提供。
// JSON 解码后整数是 float64，所以按「是否为整数值」判定（1.5 这类要拒绝）。
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
