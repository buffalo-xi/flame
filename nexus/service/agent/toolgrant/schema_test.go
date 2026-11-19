package toolgrant

import (
	"encoding/json"
	"testing"

	"flame/tools/xsql"
)

// An external tool's argument schema must reach the model verbatim: built-in tools have Meta() as a fallback, mcp / rpc tools have none
// - without it the model sees a "tool with no arguments".
func TestToolSchema(t *testing.T) {
	t.Run("nil returns nil", func(t *testing.T) {
		if got := toolSchema(xsql.JSONB{}); got != nil {
			t.Fatalf("got %+v, want nil", got)
		}
	})

	t.Run("the map shape written by the admin side", func(t *testing.T) {
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
			t.Fatalf("properties were lost: %+v", got)
		}
	})

	t.Run("the RawMessage shape written by the discovery path", func(t *testing.T) {
		// rpc/client's tools/list parsing stores json.RawMessage (see conn.go).
		j := xsql.JSONB{Data: json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}}}`)}
		got := toolSchema(j)
		if got == nil {
			t.Fatal("the RawMessage shape was not parsed")
		}
		props, _ := got["properties"].(map[string]any)
		if _, ok := props["id"]; !ok {
			t.Fatalf("properties were lost: %+v", got)
		}
	})

	t.Run("object is filled in when type is missing", func(t *testing.T) {
		// The shape the model side requires is object, and some upstreams reject the whole request without it.
		j := xsql.JSONB{Data: json.RawMessage(`{"properties":{"q":{"type":"string"}}}`)}
		got := toolSchema(j)
		if got == nil || got["type"] != "object" {
			t.Fatalf("got %+v", got)
		}
	})

	t.Run("a non-object schema falls back", func(t *testing.T) {
		// The same direction as rpc/server listTools' isObjectSchema: something that is not an object is not handed to the model.
		j := xsql.JSONB{Data: json.RawMessage(`{"type":"string"}`)}
		if got := toolSchema(j); got != nil {
			t.Fatalf("got %+v, want nil", got)
		}
	})

	t.Run("broken JSON falls back", func(t *testing.T) {
		j := xsql.JSONB{Data: json.RawMessage(`{not json`)}
		if got := toolSchema(j); got != nil {
			t.Fatalf("got %+v, want nil", got)
		}
	})
}
