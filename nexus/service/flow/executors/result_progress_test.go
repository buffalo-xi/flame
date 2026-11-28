package executors

import (
	"errors"
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

// Text a result node produced before an `exit` still reaches the caller: `exit` ends the walk, it does not roll back the
// result the run has already accumulated. This is the answer to "I printed before exiting - does the caller still get
// it?", and it has to stay true for the buffer (GetResult / ResultMsgs), not just for the stream.
func TestExitKeepsResultProducedBeforeIt(t *testing.T) {
	res := newExecNode("r1", "result", map[string]any{
		"code": map[string]any{"lang": "template", "script": "the answer is 42"},
	})
	ex := newExecNode("x1", "exit", map[string]any{})
	ctx := newContainerContext(t, []*flow.Node{res, ex})
	res.OutEdges = []*flow.Edge{{ID: "e1", Source: "r1", Target: "x1"}}

	err := execute(res, ctx, "1", 0)
	if !errors.Is(err, SignalExit) {
		t.Fatalf("execute = %v, want the exit signal", err)
	}
	if got := ctx.GetResult(); got != "the answer is 42" {
		t.Errorf("Result = %q, want the text written before the exit", got)
	}
	if len(ctx.ResultMsgs) != 1 || ctx.ResultMsgs[0] != "the answer is 42" {
		t.Errorf("ResultMsgs = %v, want the text kept for storage", ctx.ResultMsgs)
	}
}

// The same rule for a streamed result: the exit stops the walk after the frame is on the channel, and the accumulated
// result is what a non-streaming caller reads.
func TestExitKeepsStreamedResultProducedBeforeIt(t *testing.T) {
	res := newExecNode("r1", "result", map[string]any{
		"code":   map[string]any{"lang": "template", "script": "chunk"},
		"stream": true,
	})
	ex := newExecNode("x1", "exit", map[string]any{})
	ctx := newContainerContext(t, []*flow.Node{res, ex})
	ctx.ResultChan = make(chan string, 1)
	res.OutEdges = []*flow.Edge{{ID: "e1", Source: "r1", Target: "x1"}}

	if err := execute(res, ctx, "1", 0); !errors.Is(err, SignalExit) {
		t.Fatalf("execute = %v, want the exit signal", err)
	}
	select {
	case got := <-ctx.ResultChan:
		if got != "chunk" {
			t.Errorf("streamed %q, want chunk", got)
		}
	default:
		t.Error("the frame written before the exit never reached the result channel")
	}
	if got := ctx.GetResult(); got != "chunk" {
		t.Errorf("Result = %q, want the streamed text kept too", got)
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
