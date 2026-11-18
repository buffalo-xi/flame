package toolgrant

import (
	"encoding/json"
	"testing"

	"flame/tools/xsql"
)

// 外部工具的入参 schema 必须原样递给模型：内置工具有 Meta() 兜底，
// mcp / rpc 工具没有 —— 缺了它模型看到的是「无参数工具」。
func TestToolSchema(t *testing.T) {
	t.Run("nil 返回 nil", func(t *testing.T) {
		if got := toolSchema(xsql.JSONB{}); got != nil {
			t.Fatalf("got %+v, want nil", got)
		}
	})

	t.Run("管理端写入的 map 形态", func(t *testing.T) {
		j := xsql.JSONB{Data: map[string]any{
			"type":       "object",
			"properties": map[string]any{"path": map[string]any{"type": "string"}},
			"required":   []any{"path"},
		}}
		got := toolSchema(j)
		if got == nil || got["type"] != "object" {
			t.Fatalf("got %+v", got)
		}
		props, _ := got["properties"].(map[string]any)
		if _, ok := props["path"]; !ok {
			t.Fatalf("properties 丢了：%+v", got)
		}
	})

	t.Run("发现逻辑写入的 RawMessage 形态", func(t *testing.T) {
		// rpc/client 的 tools/list 解析写的是 json.RawMessage（见 conn.go）。
		j := xsql.JSONB{Data: json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}}}`)}
		got := toolSchema(j)
		if got == nil {
			t.Fatal("RawMessage 形态没解析出来")
		}
		props, _ := got["properties"].(map[string]any)
		if _, ok := props["id"]; !ok {
			t.Fatalf("properties 丢了：%+v", got)
		}
	})

	t.Run("缺 type 时补 object", func(t *testing.T) {
		// 模型侧要求的形状是 object，缺了部分上游会拒整次请求。
		j := xsql.JSONB{Data: json.RawMessage(`{"properties":{"q":{"type":"string"}}}`)}
		got := toolSchema(j)
		if got == nil || got["type"] != "object" {
			t.Fatalf("got %+v", got)
		}
	})

	t.Run("非 object 的 schema 回退", func(t *testing.T) {
		// 与 rpc/server listTools 的 isObjectSchema 同向：不是 object 就不递给模型。
		j := xsql.JSONB{Data: json.RawMessage(`{"type":"string"}`)}
		if got := toolSchema(j); got != nil {
			t.Fatalf("got %+v, want nil", got)
		}
	})

	t.Run("坏 JSON 回退", func(t *testing.T) {
		j := xsql.JSONB{Data: json.RawMessage(`{not json`)}
		if got := toolSchema(j); got != nil {
			t.Fatalf("got %+v, want nil", got)
		}
	})
}
