package executors

import (
	"strings"
	"testing"

	"flame/nexus/service/flow/core"
)

// result / progress with `lang: template` render in pure Go - that is why the doc says template is only for these two
// nodes - so their whole Execute path is testable without a sandbox: the text lands in `output`, and the stream flag only
// decides whether it also goes onto the channel.
func TestResultExecutorTemplateOutput(t *testing.T) {
	node := newExecNode("2", "result", map[string]any{
		"code": map[string]any{"lang": "template", "script": "all done"},
	})
	ctx := newExecContext()
	nr := newExecResult(node, ctx)
	if err := (&ResultExecutor{}).Execute(node, ctx, "1", nr); err != nil {
		t.Fatalf("Execute = %v (code %q)", err, nr.ErrCode)
	}
	if got, _ := nr.OutputVars["output"].(string); got != "all done" {
		t.Errorf("output = %v, want the rendered text", nr.OutputVars["output"])
	}
	if nr.Status != flow.StatusSuccess {
		t.Errorf("status = %v, want success", nr.Status)
	}
	// stream defaults to false: the text is accumulated, not pushed onto the channel.
	if len(ctx.ResultMsgs) != 1 || ctx.ResultMsgs[0] != "all done" {
		t.Errorf("ResultMsgs = %v, want one entry", ctx.ResultMsgs)
	}
	if ctx.ResultChan != nil {
		t.Error("the channel must stay untouched without stream: true")
	}
}

func TestResultExecutorStreamsWhenAsked(t *testing.T) {
	node := newExecNode("2", "result", map[string]any{
		"code":   map[string]any{"lang": "template", "script": "chunk"},
		"stream": true,
	})
	ctx := newExecContext()
	ctx.ResultChan = make(chan string, 1)
	nr := newExecResult(node, ctx)
	if err := (&ResultExecutor{}).Execute(node, ctx, "1", nr); err != nil {
		t.Fatalf("Execute = %v (code %q)", err, nr.ErrCode)
	}
	select {
	case got := <-ctx.ResultChan:
		if got != "chunk" {
			t.Errorf("streamed %q, want chunk", got)
		}
	default:
		t.Fatal("stream: true must push the text onto the result channel")
	}
	if got, _ := nr.OutputVars["output"].(string); got != "chunk" {
		t.Errorf("output = %v, want the text regardless of streaming", nr.OutputVars["output"])
	}
}

// The static rule says a result node without config.code cannot run; this is the same statement at run time.
func TestResultExecutorWithoutCodeFails(t *testing.T) {
	node := newExecNode("2", "result", map[string]any{})
	ctx := newExecContext()
	nr := newExecResult(node, ctx)
	if err := (&ResultExecutor{}).Execute(node, ctx, "1", nr); err == nil {
		t.Fatal("Execute must fail without config.code")
	}
	if nr.ErrCode != flow.ErrCodeConfig {
		t.Errorf("ErrCode = %q, want config", nr.ErrCode)
	}
	if !strings.Contains(nr.ErrMsg, "code config not found") {
		t.Errorf("ErrMsg = %q, want it to name the missing block", nr.ErrMsg)
	}
}

// progress writes the same `output` variable but lands on the progress channel instead of the result one.
func TestProgressExecutorEmitsProgress(t *testing.T) {
	node := newExecNode("2", "progress", map[string]any{
		"code": map[string]any{"lang": "template", "script": "working"},
	})
	ctx := newExecContext()
	nr := newExecResult(node, ctx)
	if err := (&ProgressExecutor{}).Execute(node, ctx, "1", nr); err != nil {
		t.Fatalf("Execute = %v (code %q)", err, nr.ErrCode)
	}
	if len(ctx.ProgressMsgs) != 1 || ctx.ProgressMsgs[0] != "working" {
		t.Errorf("ProgressMsgs = %v, want one entry", ctx.ProgressMsgs)
	}
	if len(ctx.ResultMsgs) != 0 {
		t.Errorf("ResultMsgs = %v, want none: progress is not result text", ctx.ResultMsgs)
	}
	if got, _ := nr.OutputVars["output"].(string); got != "working" {
		t.Errorf("output = %v, want the rendered text", nr.OutputVars["output"])
	}
}
