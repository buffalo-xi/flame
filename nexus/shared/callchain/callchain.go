// Package callchain 维护一次调用跨 agent / flow / RPC 的**祖先链**，用于检测循环调用。
//
// 元素格式：
//
//	<id>|<kind>     例如 3f2a1c40-…-1c0d5b6e9f31|agent
//
// 竖线之前是实体 id，**唯一参与判重**；竖线之后是类型（agent / flow），仅为日志可读，
// 可缺省、可写错 —— 身份只有 id，类型变了、端点改指到别的实体，都不该判错。
//
// 说明段刻意**不带租户 schema**：schema 只是本实例的路由信息，跨部署后同一个 "t0"
// 可能指向完全不同的租户，写进链只会误导排障（判重又只看 id）。租户信息在请求 URL、
// 日志字段、端点行里本来就有，链路不必重复一遍。
//
// 语义（与 depth 那套刻意区分）：
//   - 只记**祖先**：子调用拿到的是「父链 + 父自己」，兄弟分支之间互不传递。
//     因此「同一父级并列调两次同一个子级」（f a a）永远放行，
//     而「自己出现在自己的祖先链里」（f a f）必然被拒；
//   - 入站 / 内部直调时把自己追加到链尾再下传，出站时**原样带出**，不重复追加。
//
// 代价：判重不区分租户与类型，靠「id 全局唯一」这个前提（各租户表的主键是 uuid，
// 碰撞概率约 2⁻¹²²）。换来的是说明段可以随意演化而不影响判定。
//
// 与 max_call_depth / max_recursion_depth 的关系：
//   - 那两个是兜底闸（深度上限），本包是精确检测（一发现重入就拒），两者互补；
//   - 链由调用方自报，客户端可以不传或伪造 —— 伪造的后果只是「本次调用被拒」，
//     但「不传」意味着检测不到，所以**绝不能拿本包替代深度上限**。
package callchain

import (
	"context"
	"strings"
)

const (
	// MetaKey JSON-RPC params._meta 里的键名（跨进程）。
	MetaKey = "call_chain"
	// ArgumentKey 进程内直调的保留参数键（与 _call_depth 同一套机制）。
	ArgumentKey = "_call_chain"

	// MaxLen 链长硬上限：正常栈深远小于此值，超出即视为构造的恶意请求。
	// 超限直接拒，不做截断 —— 截断等于帮调用方绕过。
	MaxLen = 64

	KindAgent = "agent"
	KindFlow  = "flow"

	// noteSep 分隔「参与判重的 id」与「说明」。
	noteSep   = "|"
	formatSep = " → "

	// maxIDLen 单个字段（id 或说明）的长度上限。
	maxIDLen = 64
)

// Key 拼一个实体在链里的元素：<id>|<kind>。
//
// id 是实体（agent / flow）的主键，不是端点 id：执行层天然知道自己是谁，
// 端点 id 还会在导出导入后重新生成。
// kind 缺省时只返回裸 id —— 仍可判重，只是日志里少一段上下文。
func Key(kind, id string) string {
	id = strings.TrimSpace(strings.ToLower(id))
	if id == "" {
		return ""
	}
	if kind == "" {
		return id
	}
	return id + noteSep + strings.ToLower(strings.TrimSpace(kind))
}

// ID 取元素里参与判重的部分（竖线之前）；没有说明时就是整串。
func ID(key string) string {
	if i := strings.Index(key, noteSep); i >= 0 {
		return key[:i]
	}
	return key
}

// Valid 判断元素是否合法。非法元素在解析时被丢弃 —— 不因脏数据拒掉正常调用。
//
// 校验只看「id 是标识符」+「说明段形态」；说明里的 schema / kind 取值不做枚举，
// 因为新类型出现时旧版本应当放行而不是把整条链丢掉。
func Valid(key string) bool {
	if key == "" {
		return false
	}
	i := strings.Index(key, noteSep)
	if i < 0 {
		return identifier(key) && len(key) <= maxIDLen
	}
	id := key[:i]
	if !identifier(id) || len(id) > maxIDLen {
		return false
	}
	return noteIdent(key[i+1:])
}

// identifier 标识符校验：字母数字、下划线、横线（uuid 需要横线）。
func identifier(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '_' || c == '-' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') {
			continue
		}
		return false
	}
	return true
}

// noteIdent 说明段校验：比 id 宽松（额外允许 "/" 与 "@"，给未来的
// "agent@instance" 之类后缀留余地），但拒绝空白与控制字符。
// 说明不参与判重，所以这里只求「日志里不会出现奇怪的字符」；
// 放得宽一点，才能让对端的新后缀在旧版本里被接受而不是整条链丢掉。
func noteIdent(s string) bool {
	if s == "" || len(s) > maxIDLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '_' || c == '-' || c == '/' || c == '@' ||
			(c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') {
			continue
		}
		return false
	}
	return true
}

// Contains 判断 key 是否已在链中（判定规则唯一入口：只比 ID，不比说明）。
func Contains(chain []string, key string) bool {
	want := ID(strings.ToLower(strings.TrimSpace(key)))
	if want == "" {
		return false
	}
	for _, v := range chain {
		if ID(strings.ToLower(v)) == want {
			return true
		}
	}
	return false
}

// Append 返回「链 + key」的新切片（不修改入参，避免兄弟分支互相污染）。
// 已存在或 key 为空时返回原链：重复追加对判定无影响，但会让日志难读。
func Append(chain []string, key string) []string {
	if key == "" || Contains(chain, key) {
		return chain
	}
	out := make([]string, 0, len(chain)+1)
	out = append(out, chain...)
	out = append(out, key)
	return out
}

// Format 人读的链路（用于错误信息与日志）。
func Format(chain []string) string {
	if len(chain) == 0 {
		return "（空链）"
	}
	return strings.Join(chain, formatSep)
}

// Normalize 把任意来源的链值归一成 []string，并丢弃非法元素 / 超长部分。
//
// 认的类型：[]string、[]any（JSON 反序列化）、单个 string。
// 统一转小写：uuid 在 DB 里是小写，而 URL 参数 / 前端可能传大写，
// 不归一就会出现「同一个实体两种写法」导致判重失效。
// 超长直接截断到 MaxLen：调用方显式传入超长链时已在 ParseParams 处被拒，
// 这里只是防御性兜底。
func Normalize(raw any) []string {
	var src []string
	switch v := raw.(type) {
	case []string:
		src = v
	case []any:
		src = make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				src = append(src, s)
			}
		}
	case string:
		if v != "" {
			src = []string{v}
		}
	default:
		return nil
	}
	out := make([]string, 0, len(src))
	for _, s := range src {
		s = strings.ToLower(strings.TrimSpace(s))
		if !Valid(s) {
			continue
		}
		out = append(out, s)
		if len(out) >= MaxLen {
			break
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// RawLen 原始链长度（用于在 Normalize 之前判断是否超限）。
func RawLen(raw any) int {
	switch v := raw.(type) {
	case []string:
		return len(v)
	case []any:
		return len(v)
	}
	return 0
}

// ParseParams 从 JSON-RPC 的 params 里取链（只认 params._meta.call_chain 一处，
// 与 session_id / progressToken 同口径）。
//
// tooLong 表示调用方给的链超过 MaxLen：此时应**直接拒绝**，
// 不能截断后继续 —— 被截掉的那几段里可能正好藏着要检测的重入。
func ParseParams(params any) (chain []string, tooLong bool) {
	mp, ok := params.(map[string]any)
	if !ok {
		return nil, false
	}
	meta, ok := mp["_meta"].(map[string]any)
	if !ok {
		return nil, false
	}
	raw := meta[MetaKey]
	if RawLen(raw) > MaxLen {
		return nil, true
	}
	return Normalize(raw), false
}

// FromArguments 从进程内直调的参数里取链（_call_chain 保留键）。
func FromArguments(args map[string]any) []string {
	if args == nil {
		return nil
	}
	return Normalize(args[ArgumentKey])
}

// WithArguments 返回一个带链的**新参数表**（不修改入参）。
// 链为空时原样返回，避免给正常调用多加一个空键。
func WithArguments(args map[string]any, chain []string) map[string]any {
	if len(chain) == 0 {
		return args
	}
	out := make(map[string]any, len(args)+1)
	for k, v := range args {
		out[k] = v
	}
	out[ArgumentKey] = chain
	return out
}

// ctxKey 私有类型：避免与其它包的 context key 冲突。
type ctxKey struct{}

// With 把链放进 context（供出站调用方读取；不改变业务签名）。
func With(ctx context.Context, chain []string) context.Context {
	if len(chain) == 0 || ctx == nil {
		return ctx
	}
	return context.WithValue(ctx, ctxKey{}, chain)
}

// From 从 context 取链；没有则返回 nil。
func From(ctx context.Context) []string {
	if ctx == nil {
		return nil
	}
	chain, _ := ctx.Value(ctxKey{}).([]string)
	return chain
}
