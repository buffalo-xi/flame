package executors

import (
	"fmt"
	"strings"
	"testing"

	"flame/nexus/service/flow/core"
)

// applyHandlerAppend is the shared "script result -> node variables" step of the streaming handlers (llm / rpc / mcp /
// http): a string accumulates across frames, anything else overwrites, and the node's own fixed variables are
// off-limits.
func TestApplyHandlerAppend(t *testing.T) {
	deny := map[string]struct{}{"content": {}}

	t.Run("non-map result", func(t *testing.T) {
		nr, _ := newAppendResult(t, map[string]any{"custom": map[string]any{"type": "string"}})
		err := applyHandlerAppend(nr, "just text", deny)
		if err == nil || !strings.Contains(err.Error(), "must return a map") {
			t.Errorf("error = %v, want the non-map rejection", err)
		}
	})

	t.Run("an empty map changes nothing", func(t *testing.T) {
		nr, _ := newAppendResult(t, map[string]any{"custom": map[string]any{"type": "string"}})
		if err := applyHandlerAppend(nr, map[string]any{}, deny); err != nil {
			t.Errorf("error = %v, want nil", err)
		}
		if got, _ := nr.OutputVars["custom"].(string); got != "" {
			t.Errorf("custom = %q, want untouched", got)
		}
	})

	t.Run("strings accumulate across frames", func(t *testing.T) {
		nr, _ := newAppendResult(t, map[string]any{"custom": map[string]any{"type": "string"}})
		for _, chunk := range []string{"a", "b", "c"} {
			if err := applyHandlerAppend(nr, map[string]any{"custom": chunk}, deny); err != nil {
				t.Fatalf("applyHandlerAppend(%q) = %v", chunk, err)
			}
		}
		if got, _ := nr.OutputVars["custom"].(string); got != "abc" {
			t.Errorf("custom = %q, want the frames concatenated", got)
		}
	})

	t.Run("non-strings overwrite", func(t *testing.T) {
		nr, _ := newAppendResult(t, map[string]any{"snapshot": map[string]any{"type": "object"}})
		if err := applyHandlerAppend(nr, map[string]any{"snapshot": map[string]any{"step": 1}}, deny); err != nil {
			t.Fatalf("applyHandlerAppend = %v", err)
		}
		if err := applyHandlerAppend(nr, map[string]any{"snapshot": map[string]any{"step": 2}}, deny); err != nil {
			t.Fatalf("applyHandlerAppend = %v", err)
		}
		snapshot, _ := nr.OutputVars["snapshot"].(map[string]any)
		// The write normalises numbers to json.Number, so compare the textual value.
		if got := fmt.Sprint(snapshot["step"]); got != "2" {
			t.Errorf("snapshot = %#v, want the last frame's value", nr.OutputVars["snapshot"])
		}
	})

	t.Run("a fixed variable is ignored and warned once", func(t *testing.T) {
		nr, _ := newAppendResult(t, map[string]any{
			"content": map[string]any{"type": "string"},
			"custom":  map[string]any{"type": "string"},
		})
		nr.OutputVars["content"] = "written by the node"

		// Two frames, the same offending key: the log has to fold them into one line.
		for i := 0; i < 2; i++ {
			if err := applyHandlerAppend(nr, map[string]any{
				"content": "from the script",
				"custom":  "ok",
			}, deny); err != nil {
				t.Fatalf("applyHandlerAppend = %v", err)
			}
		}
		if got, _ := nr.OutputVars["content"].(string); got != "written by the node" {
			t.Errorf("content = %q, want the node's own value kept", got)
		}
		if got, _ := nr.OutputVars["custom"].(string); got != "okok" {
			t.Errorf("custom = %q, want the custom variable still written", got)
		}
		logged := strings.Join(nr.RunLogs, "\n")
		if count := strings.Count(logged, "fixed output variable content"); count != 1 {
			t.Errorf("the warning appears %d times, want once:\n%s", count, logged)
		}
	})

	t.Run("an undeclared variable fails the write", func(t *testing.T) {
		nr, _ := newAppendResult(t, map[string]any{"custom": map[string]any{"type": "string"}})
		err := applyHandlerAppend(nr, map[string]any{"nope": "x"}, deny)
		if err == nil || !strings.Contains(err.Error(), "output var nope not defined") {
			t.Errorf("error = %v, want the undeclared-variable error", err)
		}
	})
}

func newAppendResult(t *testing.T, outputs map[string]any) (*flow.NodeResult, *flow.ExecContext) {
	t.Helper()
	ctx := newExecContext()
	nr := newExecResult(newExecNode("2", "llm", map[string]any{}), ctx)
	nr.InitOutputVars(outputs)
	return nr, ctx
}
