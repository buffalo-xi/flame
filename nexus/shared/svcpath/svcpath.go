// Package svcpath 收敛「服务端点路径 ↔ 租户 schema」的约定（MCP/RPC 共用）。
//
// 端点形态被锁定为「端点段后紧跟一段 schema、且为末段」：
//
//	POST /service/mcp/{schema}
//	POST /service/rpc/{schema}
//
// 该形态由 gin 路由 /mcp/:schema 天然保证（:schema 不跨 "/"，多余段直接 404）。
// 本包提供两件事：
//  1. 路由参数 → 请求上下文 → 服务端 handler 的传递（WithSchema / FromContext）；
//  2. 出站连接 URL → 目标租户 schema 的解析（SchemaFromURL），供「自连检测」使用。
package svcpath

import (
	"context"
	"net/url"
	"strings"
)

// tenantSchemaKey 是上下文键的私有类型，避免与其它包的键冲突。
type tenantSchemaKey struct{}

// WithSchema 把路径中解析出的租户 schema 放进请求上下文。
func WithSchema(ctx context.Context, schema string) context.Context {
	return context.WithValue(ctx, tenantSchemaKey{}, schema)
}

// FromContext 取出路径携带的租户 schema；未设置返回空串。
func FromContext(ctx context.Context) string {
	s, _ := ctx.Value(tenantSchemaKey{}).(string)
	return s
}

// SchemaFromURL 从「…/{endpoint}/{schema}」形态的 URL 中解析租户 schema。
// 锁死形态：endpoint 段后必须正好紧跟一段且为末段，否则返回空串。
// endpoint 为端点名（"mcp" / "rpc"），大小写不敏感。
func SchemaFromURL(rawURL, endpoint string) string {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return ""
	}
	segs := strings.Split(strings.Trim(u.Path, "/"), "/")
	for i, s := range segs {
		if !strings.EqualFold(s, endpoint) {
			continue
		}
		// 锁死形态：endpoint 后正好一段，且该段为末段
		if i+1 == len(segs)-1 {
			return segs[i+1]
		}
		return ""
	}
	return ""
}
