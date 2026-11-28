package executors

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"flame/nexus/service/flow/core"
)

// The loop node's config contract: a missing loop block, an unknown type and a non-numeric count are all
// configuration errors. The count case matters most: silently reading 0 would run the system ceiling instead.
func TestLoopExecutorConfigErrors(t *testing.T) {
	cases := []struct {
		name    string
		config  map[string]any
		wantMsg string
	}{
		{"no loop block", map[string]any{}, "loop config not found"},
		{"unknown type", map[string]any{"loop": map[string]any{"type": "bogus"}}, "unknown loop type: bogus"},
		{"count without total_times", map[string]any{"loop": map[string]any{"type": "count", "total_times": "3"}}, "must be a number"},
		{"while without max_times", map[string]any{"loop": map[string]any{"type": "while", "max_times": "2"}}, "must be a number"},
		{"do_while without max_times", map[string]any{"loop": map[string]any{"type": "do_while", "max_times": true}}, "must be a number"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			node := newExecNode("2", "loop", tc.config)
			ctx := newLoopScaffold(t)
			nr := newExecResult(node, ctx)
			if err := (&LoopExecutor{}).Execute(node, ctx, "1", nr); err == nil {
				t.Fatal("Execute must fail for a malformed loop config")
			}
			if nr.ErrCode != flow.ErrCodeConfig {
				t.Errorf("ErrCode = %q, want config", nr.ErrCode)
			}
			if !strings.Contains(nr.ErrMsg, tc.wantMsg) {
				t.Errorf("ErrMsg = %q, want it to contain %q", nr.ErrMsg, tc.wantMsg)
			}
		})
	}
}

func TestLoopExecutorMissingData(t *testing.T) {
	node := newExecNode("2", "loop", nil)
	node.Data = nil
	ctx := newLoopScaffold(t)
	nr := newExecResult(node, ctx)
	if err := (&LoopExecutor{}).Execute(node, ctx, "1", nr); err == nil {
		t.Fatal("Execute must fail without node data")
	}
	if nr.ErrCode != flow.ErrCodeConfig {
		t.Errorf("ErrCode = %q, want config", nr.ErrCode)
	}
}

// count runs exactly total_times rounds and exposes the zero-based index to the body.
func TestLoopExecutorCount(t *testing.T) {
	body := newExecNode("c1", "assign", map[string]any{})
	body.ParentNode = "2"
	ctx := newLoopScaffold(t, body)
	node := newExecNode("2", "loop", map[string]any{
		"loop": map[string]any{"type": "count", "total_times": 3},
	})
	nr := newExecResult(node, ctx)
	if err := (&LoopExecutor{}).Execute(node, ctx, "1", nr); err != nil {
		t.Fatalf("Execute = %v (code %q)", err, nr.ErrCode)
	}
	if nr.Status != flow.StatusSuccess {
		t.Errorf("status = %v, want success", nr.Status)
	}
	if nr.LoopResult == nil || nr.LoopResult.Iterations != 3 {
		t.Fatalf("iterations = %v, want 3", nr.LoopResult)
	}
	if got := fmt.Sprint(nr.LoopResult.LoopVars["index"]); got != "2" {
		t.Errorf("index after the loop = %v, want the last round 2", nr.LoopResult.LoopVars["index"])
	}
}

// An explicit total_times of 0 (or a negative value) runs no round at all: it must not fall back to the system ceiling,
// which would turn "do not run" into ten thousand executions.
func TestLoopExecutorCountZeroRunsNothing(t *testing.T) {
	for _, totalTimes := range []int{0, -3} {
		body := newExecNode("c1", "assign", map[string]any{})
		body.ParentNode = "2"
		ctx := newLoopScaffold(t, body)
		node := newExecNode("2", "loop", map[string]any{
			"loop": map[string]any{"type": "count", "total_times": totalTimes},
		})
		nr := newExecResult(node, ctx)
		if err := (&LoopExecutor{}).Execute(node, ctx, "1", nr); err != nil {
			t.Fatalf("total_times=%d: Execute = %v (code %q)", totalTimes, err, nr.ErrCode)
		}
		if nr.Status != flow.StatusSuccess || nr.LoopResult.Iterations != 0 {
			t.Errorf("total_times=%d: status = %v, iterations = %d, want success with no round",
				totalTimes, nr.Status, nr.LoopResult.Iterations)
		}
	}
}

// break ends the loop on the round it fires, and the loop itself still succeeds.
func TestLoopExecutorBreakStopsTheLoop(t *testing.T) {
	body := &flow.Node{ID: "b1", Type: "break", ParentNode: "2", Data: &flow.NodeData{}}
	ctx := newLoopScaffold(t, body)
	node := newExecNode("2", "loop", map[string]any{
		"loop": map[string]any{"type": "count", "total_times": 5},
	})
	nr := newExecResult(node, ctx)
	if err := (&LoopExecutor{}).Execute(node, ctx, "1", nr); err != nil {
		t.Fatalf("Execute = %v (code %q)", err, nr.ErrCode)
	}
	if nr.LoopResult.Iterations != 1 {
		t.Errorf("iterations = %d, want 1: break must end the loop", nr.LoopResult.Iterations)
	}
	if nr.Status != flow.StatusSuccess {
		t.Errorf("status = %v, want success", nr.Status)
	}
}

// continue skips the rest of the round but keeps the loop going: every round is still counted.
func TestLoopExecutorContinueKeepsGoing(t *testing.T) {
	body := &flow.Node{ID: "k1", Type: "continue", ParentNode: "2", Data: &flow.NodeData{}}
	ctx := newLoopScaffold(t, body)
	node := newExecNode("2", "loop", map[string]any{
		"loop": map[string]any{"type": "count", "total_times": 3},
	})
	nr := newExecResult(node, ctx)
	if err := (&LoopExecutor{}).Execute(node, ctx, "1", nr); err != nil {
		t.Fatalf("Execute = %v (code %q)", err, nr.ErrCode)
	}
	if nr.LoopResult.Iterations != 3 {
		t.Errorf("iterations = %d, want 3: continue must not end the loop", nr.LoopResult.Iterations)
	}
}

// A member that carries an `exit` out of its own body ends the run from inside a loop round: the loop succeeds, the signal
// goes upwards, and the remaining rounds never start.
func TestLoopMemberExitEndsRun(t *testing.T) {
	registerProbe(t)
	inner := newExecNode("p2", "process", map[string]any{})
	inner.ParentNode = "2"
	inner.Data.Concurrent = true
	innerExit := newExecNode("x1", "exit", map[string]any{})
	innerEntry := entryTo("p2", innerExit)
	slow := memberNode("m2", "2", map[string]any{"delay_ms": 300})
	ctx := newLoopScaffold(t, inner, slow)
	nestBody(ctx, innerEntry, innerExit)
	node := newExecNode("2", "loop", map[string]any{
		"loop": map[string]any{"type": "count", "total_times": 3},
	})
	nr := newExecResult(node, ctx)

	err := (&LoopExecutor{}).Execute(node, ctx, "1", nr)
	if !errors.Is(err, SignalExit) {
		t.Fatalf("Execute = %v, want the exit signal", err)
	}
	if nr.Status != flow.StatusSuccess {
		t.Errorf("status = %q, want success", nr.Status)
	}
	if nr.LoopResult == nil || len(nr.LoopResult.Parallel) == 0 || nr.LoopResult.Parallel[0].ExitedBy != "p2" {
		t.Errorf("parallel snapshots = %+v, want the round marked exited_by p2", nr.LoopResult)
	}
}

// return leaves the whole function: the loop succeeds and hands the signal upwards.
func TestLoopExecutorReturnPropagates(t *testing.T) {
	body := &flow.Node{ID: "r1", Type: "return", ParentNode: "2", Data: &flow.NodeData{}}
	ctx := newLoopScaffold(t, body)
	node := newExecNode("2", "loop", map[string]any{
		"loop": map[string]any{"type": "count", "total_times": 4},
	})
	nr := newExecResult(node, ctx)
	err := (&LoopExecutor{}).Execute(node, ctx, "1", nr)
	if !errors.Is(err, SignalReturn) {
		t.Fatalf("Execute = %v, want the return signal", err)
	}
	if nr.Status != flow.StatusSuccess {
		t.Errorf("status = %v, want success", nr.Status)
	}
	if nr.LoopResult.Iterations != 1 {
		t.Errorf("iterations = %d, want 1", nr.LoopResult.Iterations)
	}
}

// A loop body with no entry node cannot run at all; the failure names the round so the log points at the loop.
func TestLoopExecutorMissingBodyEntry(t *testing.T) {
	ctx := newLoopScaffold(t)
	node := newExecNode("2", "loop", map[string]any{
		"loop": map[string]any{"type": "count", "total_times": 1},
	})
	nr := newExecResult(node, ctx)
	if err := (&LoopExecutor{}).Execute(node, ctx, "1", nr); err == nil {
		t.Fatal("Execute must fail when the body has no entry node")
	}
	if nr.ErrCode != flow.ErrCodeConfig {
		t.Errorf("ErrCode = %q, want config", nr.ErrCode)
	}
	if !strings.Contains(nr.ErrMsg, "body entry node not found") || !strings.Contains(nr.ErrMsg, "iteration 0") {
		t.Errorf("ErrMsg = %q, want the missing entry and the round", nr.ErrMsg)
	}
}

// each iterates the array taken from the node's inputs and exposes index / item to the body.
func TestLoopExecutorEach(t *testing.T) {
	body := newExecNode("c1", "assign", map[string]any{})
	body.ParentNode = "2"
	ctx := newLoopScaffold(t, body)
	ctx.RunFlowVars = map[string]any{"items": []any{"a", "b", "c"}}

	node := newExecNode("2", "loop", map[string]any{
		"inputs": map[string]any{
			"items": map[string]any{"source_type": "flow", "source_key": "items"},
		},
		"loop": map[string]any{"type": "each", "source_key": "items"},
	})
	nr := newExecResult(node, ctx)
	if err := (&LoopExecutor{}).Execute(node, ctx, "1", nr); err != nil {
		t.Fatalf("Execute = %v (code %q)", err, nr.ErrCode)
	}
	if nr.LoopResult.Iterations != 3 {
		t.Fatalf("iterations = %d, want 3", nr.LoopResult.Iterations)
	}
	if got, _ := nr.LoopResult.LoopVars["item"].(string); got != "c" {
		t.Errorf("item after the loop = %v, want the last element c", nr.LoopResult.LoopVars["item"])
	}
	if got := fmt.Sprint(nr.LoopResult.LoopVars["index"]); got != "2" {
		t.Errorf("index = %v, want 2", nr.LoopResult.LoopVars["index"])
	}
}

func TestLoopExecutorEachSourceErrors(t *testing.T) {
	cases := []struct {
		name    string
		inputs  map[string]any
		loop    map[string]any
		wantMsg string
	}{
		{
			name:    "not in inputs",
			inputs:  map[string]any{},
			loop:    map[string]any{"type": "each", "source_key": "items"},
			wantMsg: "the each source is not in inputs: items",
		},
		{
			name: "not an array",
			inputs: map[string]any{
				"items": map[string]any{"source_type": "flow", "source_key": "items"},
			},
			loop:    map[string]any{"type": "each", "source_key": "items"},
			wantMsg: "the each source is not an array: items",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := newExecNode("c1", "assign", map[string]any{})
			body.ParentNode = "2"
			ctx := newLoopScaffold(t, body)
			ctx.RunFlowVars = map[string]any{"items": "not-an-array"}
			node := newExecNode("2", "loop", map[string]any{"inputs": tc.inputs, "loop": tc.loop})
			nr := newExecResult(node, ctx)
			if err := (&LoopExecutor{}).Execute(node, ctx, "1", nr); err == nil {
				t.Fatal("Execute must fail for a bad each source")
			}
			if nr.ErrCode != flow.ErrCodeConfig {
				t.Errorf("ErrCode = %q, want config", nr.ErrCode)
			}
			if !strings.Contains(nr.ErrMsg, tc.wantMsg) {
				t.Errorf("ErrMsg = %q, want it to contain %q", nr.ErrMsg, tc.wantMsg)
			}
		})
	}
}

// break_on_empty is the "no data, nothing to do" switch: an empty source is a successful no-op rather than an
// error.
func TestLoopExecutorEachBreakOnEmpty(t *testing.T) {
	body := newExecNode("c1", "assign", map[string]any{})
	body.ParentNode = "2"
	ctx := newLoopScaffold(t, body)
	ctx.RunFlowVars = map[string]any{"items": []any{}}
	node := newExecNode("2", "loop", map[string]any{
		"inputs": map[string]any{
			"items": map[string]any{"source_type": "flow", "source_key": "items"},
		},
		"loop": map[string]any{"type": "each", "source_key": "items", "break_on_empty": true},
	})
	nr := newExecResult(node, ctx)
	if err := (&LoopExecutor{}).Execute(node, ctx, "1", nr); err != nil {
		t.Fatalf("Execute = %v (code %q)", err, nr.ErrCode)
	}
	if nr.Status != flow.StatusSuccess || nr.LoopResult.Iterations != 0 {
		t.Errorf("status = %v, iterations = %d, want success with no round", nr.Status, nr.LoopResult.Iterations)
	}
}

// while checks first: a false condition runs no round at all, and the ceiling comes from max_times.
func TestLoopExecutorWhile(t *testing.T) {
	cases := []struct {
		name       string
		condition  string
		maxTimes   int
		iterations int
	}{
		{"never true", "false", 3, 0},
		{"always true is capped by max_times", "true", 2, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := newExecNode("c1", "assign", map[string]any{})
			body.ParentNode = "2"
			ctx := newLoopScaffold(t, body)
			node := newExecNode("2", "loop", map[string]any{
				"loop": map[string]any{
					"type":      "while",
					"lang":      "template",
					"condition": tc.condition,
					"max_times": tc.maxTimes,
				},
			})
			nr := newExecResult(node, ctx)
			if err := (&LoopExecutor{}).Execute(node, ctx, "1", nr); err != nil {
				t.Fatalf("Execute = %v (code %q)", err, nr.ErrCode)
			}
			if nr.LoopResult.Iterations != tc.iterations {
				t.Errorf("iterations = %d, want %d", nr.LoopResult.Iterations, tc.iterations)
			}
		})
	}
}

// The "condition not met" log must name the rounds that actually ran: a while whose very first check is false has run
// none, so "after 1 round(s)" would contradict the iteration count right next to it.
func TestLoopExecutorWhileLogsZeroRounds(t *testing.T) {
	body := newExecNode("c1", "assign", map[string]any{})
	body.ParentNode = "2"
	ctx := newLoopScaffold(t, body)
	node := newExecNode("2", "loop", map[string]any{
		"loop": map[string]any{
			"type":      "while",
			"lang":      "template",
			"condition": "false",
			"max_times": 3,
		},
	})
	nr := newExecResult(node, ctx)
	if err := (&LoopExecutor{}).Execute(node, ctx, "1", nr); err != nil {
		t.Fatalf("Execute = %v (code %q)", err, nr.ErrCode)
	}
	if !strings.Contains(strings.Join(nr.RunLogs, "\n"), "after 0 round(s)") {
		t.Errorf("logs = %v, want them to report 0 rounds", nr.RunLogs)
	}
}

// do_while checks last, so the body runs at least once even when the condition is false from the start.
func TestLoopExecutorDoWhileRunsAtLeastOnce(t *testing.T) {
	body := newExecNode("c1", "assign", map[string]any{})
	body.ParentNode = "2"
	ctx := newLoopScaffold(t, body)
	node := newExecNode("2", "loop", map[string]any{
		"loop": map[string]any{
			"type":      "do_while",
			"lang":      "template",
			"condition": "false",
			"max_times": 5,
		},
	})
	nr := newExecResult(node, ctx)
	if err := (&LoopExecutor{}).Execute(node, ctx, "1", nr); err != nil {
		t.Fatalf("Execute = %v (code %q)", err, nr.ErrCode)
	}
	if nr.LoopResult.Iterations != 1 {
		t.Errorf("iterations = %d, want 1", nr.LoopResult.Iterations)
	}
}

// A continue ends the round, not the exit test: the condition must still be evaluated on a round the body cut short.
// Skipping it made a do_while run to max_times whenever the body always continued, and the run still reported success.
func TestLoopExecutorDoWhileContinueStillChecksCondition(t *testing.T) {
	body := newExecNode("k1", "continue", map[string]any{})
	body.ParentNode = "2"
	ctx := newLoopScaffold(t, body)
	node := newExecNode("2", "loop", map[string]any{
		"loop": map[string]any{
			"type":      "do_while",
			"lang":      "template",
			"condition": "false",
			"max_times": 5,
		},
	})
	nr := newExecResult(node, ctx)
	if err := (&LoopExecutor{}).Execute(node, ctx, "1", nr); err != nil {
		t.Fatalf("Execute = %v (code %q)", err, nr.ErrCode)
	}
	if nr.LoopResult.Iterations != 1 {
		t.Errorf("iterations = %d, want 1: continue must not skip the condition check", nr.LoopResult.Iterations)
	}
	if nr.Status != flow.StatusSuccess {
		t.Errorf("status = %v, want success", nr.Status)
	}
}

// The other half of the same rule: a condition that stays true still runs to the ceiling, so catching the bug above did
// not turn continue into break.
func TestLoopExecutorDoWhileContinueRunsToCeiling(t *testing.T) {
	body := newExecNode("k1", "continue", map[string]any{})
	body.ParentNode = "2"
	ctx := newLoopScaffold(t, body)
	node := newExecNode("2", "loop", map[string]any{
		"loop": map[string]any{
			"type":      "do_while",
			"lang":      "template",
			"condition": "true",
			"max_times": 3,
		},
	})
	nr := newExecResult(node, ctx)
	if err := (&LoopExecutor{}).Execute(node, ctx, "1", nr); err != nil {
		t.Fatalf("Execute = %v (code %q)", err, nr.ErrCode)
	}
	if nr.LoopResult.Iterations != 3 {
		t.Errorf("iterations = %d, want 3: a true condition keeps the loop going", nr.LoopResult.Iterations)
	}
}

// A condition script that does not render is a script error: the loop cannot decide, and silently treating it as
// false would end the loop with a wrong result.
func TestLoopExecutorBrokenCondition(t *testing.T) {
	body := newExecNode("c1", "assign", map[string]any{})
	body.ParentNode = "2"
	ctx := newLoopScaffold(t, body)
	node := newExecNode("2", "loop", map[string]any{
		"loop": map[string]any{
			"type":      "while",
			"lang":      "template",
			"condition": "{{",
			"max_times": 1,
		},
	})
	nr := newExecResult(node, ctx)
	if err := (&LoopExecutor{}).Execute(node, ctx, "1", nr); err == nil {
		t.Fatal("Execute must fail for a condition that does not render")
	}
	if nr.ErrCode != flow.ErrCodeScript {
		t.Errorf("ErrCode = %q, want script", nr.ErrCode)
	}
	if !strings.Contains(nr.ErrMsg, "failed to run the condition script") {
		t.Errorf("ErrMsg = %q, want it to name the condition", nr.ErrMsg)
	}
}

// A cancelled run stops before the next round and reports the right code for "user stopped" vs "deadline".
func TestLoopExecutorCancelled(t *testing.T) {
	body := newExecNode("c1", "assign", map[string]any{})
	body.ParentNode = "2"
	ctx := newLoopScaffold(t, body)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	ctx.StdCtx = cancelled
	node := newExecNode("2", "loop", map[string]any{
		"loop": map[string]any{"type": "count", "total_times": 3},
	})
	nr := newExecResult(node, ctx)
	if err := (&LoopExecutor{}).Execute(node, ctx, "1", nr); err == nil {
		t.Fatal("Execute must return the cancellation")
	}
	if nr.Status != flow.StatusCancelled || nr.ErrCode != flow.ErrCodeCancel {
		t.Errorf("status = %v code = %q, want cancelled / cancel", nr.Status, nr.ErrCode)
	}
	if nr.LoopResult.Iterations != 0 {
		t.Errorf("iterations = %d, want none", nr.LoopResult.Iterations)
	}
}

func TestParseCountInt(t *testing.T) {
	cases := []struct {
		name    string
		raw     any
		present bool
		want    int
		wantErr bool
	}{
		{"absent", nil, false, 0, false},
		{"null", nil, true, 0, false},
		{"float64", float64(5), true, 5, false},
		{"int", 7, true, 7, false},
		{"int64", int64(9), true, 9, false},
		{"fractional is truncated", float64(2.9), true, 2, false},
		{"string", "5", true, 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lc := map[string]any{}
			if tc.present {
				lc["n"] = tc.raw
			}
			got, set, err := parseCountInt(lc, "n")
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseCountInt(%#v) = %d, want an error", tc.raw, got)
				}
				var ee *ExecError
				if !errors.As(err, &ee) || ee.Code != flow.ErrCodeConfig {
					t.Errorf("error = %v, want a config ExecError", err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Errorf("parseCountInt(%#v) = %d, %v, want %d", tc.raw, got, err, tc.want)
			}
			// set distinguishes "unset, take the system ceiling" from an explicit 0, which is a real value.
			if wantSet := tc.present && tc.raw != nil; set != wantSet {
				t.Errorf("parseCountInt(%#v) set = %v, want %v", tc.raw, set, wantSet)
			}
		})
	}
}

func TestResolveMaxTimes(t *testing.T) {
	ctx := newExecContext() // Limits = the built-in fallback (max_loop_times 3000)
	cases := []struct{ in, want int }{
		{0, 3000},
		{-5, 3000},
		{5, 5},
		{5000, 3000},
	}
	for _, tc := range cases {
		if got := resolveMaxTimes(ctx, tc.in); got != tc.want {
			t.Errorf("resolveMaxTimes(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestToSlice(t *testing.T) {
	if got := toSlice([]any{1, 2}); len(got) != 2 {
		t.Errorf("toSlice([]any) = %v", got)
	}
	if got := toSlice([]string{"a"}); len(got) != 1 || got[0] != "a" {
		t.Errorf("toSlice([]string) = %v", got)
	}
	if got := toSlice([]float64{1}); len(got) != 1 {
		t.Errorf("toSlice([]float64) = %v", got)
	}
	if got := toSlice([]bool{true}); len(got) != 1 {
		t.Errorf("toSlice([]bool) = %v", got)
	}
	if got := toSlice([]map[string]any{{}}); len(got) != 1 {
		t.Errorf("toSlice([]map) = %v", got)
	}
	if got := toSlice(nil); got != nil {
		t.Errorf("toSlice(nil) = %v, want nil", got)
	}
	if got := toSlice("nope"); got != nil {
		t.Errorf("toSlice(string) = %v, want nil", got)
	}
}

// newLoopScaffold wires a loop node's body: the children get parent_node "2" (the loop's id) and an entry node,
// which is how runIteration finds the body start.
func newLoopScaffold(t *testing.T, body ...*flow.Node) *flow.ExecContext {
	t.Helper()
	children := make([]*flow.Node, 0, len(body)+1)
	children = append(children, body...)
	if len(body) > 0 {
		children = append(children, entryTo("2", body[0]))
	}
	ctx := newContainerContext(t, children)
	return ctx
}
