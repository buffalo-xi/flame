package executors

import (
	"strings"
	"testing"

	"flame/nexus/service/rpc/client"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The arguments config is a JSON object string whose string values carry the {{.input.x}} template. Rendering it
// is the last step before the call, so a bad template or a non-object result has to fail there with a clear message.
func TestRenderMcpArguments(t *testing.T) {
	tmplCtx := map[string]any{"input": map[string]any{"topic": "billing", "limit": 5}}

	t.Run("absent and null mean no arguments", func(t *testing.T) {
		for _, cfg := range []map[string]any{{}, {"arguments": nil}} {
			got, err := renderMcpArguments(cfg, tmplCtx)
			if err != nil || len(got) != 0 {
				t.Errorf("renderMcpArguments(%v) = %v, %v, want an empty object", cfg, got, err)
			}
		}
	})

	t.Run("blank JSON string means no arguments", func(t *testing.T) {
		got, err := renderMcpArguments(map[string]any{"arguments": "   "}, tmplCtx)
		if err != nil || len(got) != 0 {
			t.Errorf("renderMcpArguments(blank) = %v, %v, want an empty object", got, err)
		}
	})

	t.Run("JSON string is rendered then decoded", func(t *testing.T) {
		got, err := renderMcpArguments(map[string]any{
			"arguments": `{"query": "{{ .input.topic }}", "limit": 5}`,
		}, tmplCtx)
		if err != nil {
			t.Fatalf("renderMcpArguments = %v", err)
		}
		if got["query"] != "billing" {
			t.Errorf("query = %v, want the rendered input", got["query"])
		}
		if got["limit"] != float64(5) {
			t.Errorf("limit = %#v, want 5 as decoded by encoding/json", got["limit"])
		}
	})

	t.Run("object is rendered value by value", func(t *testing.T) {
		got, err := renderMcpArguments(map[string]any{
			"arguments": map[string]any{
				"query": "{{ .input.topic }}",
				"nested": map[string]any{
					"text": "q={{ .input.topic }}",
				},
				"list":  []any{"{{ .input.topic }}", 7, true},
				"limit": 5,
				"flag":  true,
				"none":  nil,
			},
		}, tmplCtx)
		if err != nil {
			t.Fatalf("renderMcpArguments = %v", err)
		}
		if got["query"] != "billing" {
			t.Errorf("query = %v, want the rendered input", got["query"])
		}
		if nested, _ := got["nested"].(map[string]any); nested["text"] != "q=billing" {
			t.Errorf("nested = %#v, want the nested value rendered", got["nested"])
		}
		list, _ := got["list"].([]any)
		if len(list) != 3 || list[0] != "billing" || list[1] != 7 || list[2] != true {
			t.Errorf("list = %#v, want the string rendered and the rest untouched", got["list"])
		}
		if got["limit"] != 5 || got["flag"] != true || got["none"] != nil {
			t.Errorf("scalars = %#v, want non-string values passed through", got)
		}
	})

	t.Run("broken template", func(t *testing.T) {
		_, err := renderMcpArguments(map[string]any{"arguments": "{{"}, tmplCtx)
		if err == nil || !strings.Contains(err.Error(), "failed to render the arguments template") {
			t.Errorf("error = %v, want the render error", err)
		}
	})

	t.Run("rendered text is not JSON", func(t *testing.T) {
		_, err := renderMcpArguments(map[string]any{"arguments": "not json"}, tmplCtx)
		if err == nil || !strings.Contains(err.Error(), "failed to decode the arguments JSON") {
			t.Errorf("error = %v, want the decode error", err)
		}
	})

	t.Run("wrong type", func(t *testing.T) {
		_, err := renderMcpArguments(map[string]any{"arguments": 42}, tmplCtx)
		if err == nil || !strings.Contains(err.Error(), "is malformed") {
			t.Errorf("error = %v, want the malformed error", err)
		}
	})
}

// Both progress maps feed a handler's eventData: category lives in meta, so it must survive, while an empty optional
// field should not appear at all (a handler testing `eventData.message` must not see "").
func TestProgressToMap(t *testing.T) {
	got := progressToMap(&mcp.ProgressNotificationParams{
		Progress:      3,
		Total:         10,
		Message:       "halfway",
		Meta:          mcp.Meta{"category": "thinking"},
		ProgressToken: "tok-1",
	})
	if got["progress"] != 3.0 || got["total"] != 10.0 || got["message"] != "halfway" {
		t.Errorf("map = %#v, want the numbers and the message", got)
	}
	if meta, _ := got["meta"].(mcp.Meta); meta["category"] != "thinking" {
		t.Errorf("meta = %#v, want the category kept", got["meta"])
	}
	if got["progress_token"] != "tok-1" {
		t.Errorf("progress_token = %v, want the token", got["progress_token"])
	}

	sparse := progressToMap(&mcp.ProgressNotificationParams{Progress: 1})
	if _, ok := sparse["message"]; ok {
		t.Error("an empty message must not be emitted")
	}
	if _, ok := sparse["meta"]; ok {
		t.Error("a nil meta must not be emitted")
	}
	if _, ok := sparse["progress_token"]; ok {
		t.Error("a nil token must not be emitted")
	}
}

func TestRpcProgressToMap(t *testing.T) {
	got := rpcProgressToMap(&client.ProgressNotificationParams{
		Progress: 1,
		Total:    2,
		Message:  "step",
		Meta:     map[string]any{"category": "text"},
	})
	if got["message"] != "step" {
		t.Errorf("message = %v, want step", got["message"])
	}
	if meta, _ := got["meta"].(map[string]any); meta["category"] != "text" {
		t.Errorf("meta = %#v, want the category kept", got["meta"])
	}

	// The rpc shape has no progress / total fields; the message is always present, even when empty.
	bare := rpcProgressToMap(&client.ProgressNotificationParams{})
	if msg, ok := bare["message"]; !ok || msg != "" {
		t.Errorf("bare = %#v, want an empty message present", bare)
	}
	if _, ok := bare["meta"]; ok {
		t.Error("a nil meta must not be emitted")
	}
}

// The collectors are written from the transport goroutine and read by the executor: snapshot must be a copy, or the
// append that happens next would invalidate what the caller is iterating.
func TestProgressCollectorsSnapshotIsACopy(t *testing.T) {
	var mcpCollector progressCollector
	mcpCollector.append("a")
	snap := mcpCollector.snapshot()
	if len(snap) != 1 || snap[0] != "a" {
		t.Fatalf("snapshot = %v, want a", snap)
	}
	mcpCollector.append("b")
	if len(snap) != 1 {
		t.Error("the snapshot grew after a later append: it shares the backing array")
	}
	if got := mcpCollector.snapshot(); len(got) != 2 {
		t.Errorf("snapshot = %v, want both entries", got)
	}

	var rpcCollector rpcProgressCollector
	rpcCollector.append("x")
	rpcSnap := rpcCollector.snapshot()
	rpcCollector.append("y")
	if len(rpcSnap) != 1 {
		t.Error("the rpc snapshot grew after a later append")
	}
	if got := rpcCollector.snapshot(); len(got) != 2 {
		t.Errorf("snapshot = %v, want both entries", got)
	}

	var empty progressCollector
	if got := empty.snapshot(); len(got) != 0 {
		t.Errorf("empty snapshot = %v, want none", got)
	}
}
