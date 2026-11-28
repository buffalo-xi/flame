package executors

import (
	"errors"
	"fmt"

	"flame/nexus/service/flow/core"
)

// Control signals: they travel through the error interface but are normal control flow, not errors
var (
	SignalBreak    = errors.New("loop_break")
	SignalContinue = errors.New("loop_continue")
	SignalReturn   = errors.New("function_return")
	SignalExit     = errors.New("flow_exit")
)

// isControlSignal reports whether this is a control signal (as opposed to a real error)
func isControlSignal(err error) bool {
	return errors.Is(err, SignalBreak) ||
		errors.Is(err, SignalContinue) ||
		errors.Is(err, SignalReturn) ||
		errors.Is(err, SignalExit)
}

// signalScope reports where a break / continue would be consumed, together with the container that decided it: "loop" when a
// loop is reachable (the boundary is that loop), "function" when the walk up the parent chain stops at a function first (a
// function is an isolation scope, so the signal cannot leave it), "top" at the top level, and "unknown" when the chain cannot
// be resolved at all.
//
// The rule is the run-time twin of flowcheck's control_scope: process / branch / fork are transparent, a function is not - a
// signal inside a function body must not behave differently depending on where the function is called from.
//
// "unknown" exists for a broken context (no flow, or a parent id missing from the project's node map): the signal is then
// allowed through and the container / root turns it into a classified error. Failing the node here would blame it for a
// broken map, which is a tooling bug rather than an authoring mistake.
func signalScope(node *flow.Node, ctx *flow.ExecContext) (scope string, boundary *flow.Node) {
	if node == nil || ctx == nil || ctx.Flow == nil {
		return "unknown", nil
	}
	for cur := node.ParentNode; cur != ""; {
		parent, ok := ctx.Flow.NodeMap[cur]
		if !ok {
			return "unknown", nil
		}
		switch parent.Type {
		case "loop":
			return "loop", parent
		case "function":
			return "function", parent
		}
		cur = parent.ParentNode
	}
	return "top", nil
}

// checkSignalScope fails the break / continue node itself when no loop can consume it.
//
// Checking here rather than letting the signal leak to the root is what makes the failure land on the offending node, with
// its own label and with err_code=config (so a caller can classify it instead of matching a string). It used to be reported
// after the enclosing containers had already finished as success, which said the wrong thing about where the mistake was.
func checkSignalScope(typ string, node *flow.Node, ctx *flow.ExecContext, nr *flow.NodeResult) error {
	scope, boundary := signalScope(node, ctx)
	switch scope {
	case "loop", "unknown":
		return nil
	case "function":
		return failf(nr, flow.ErrCodeConfig, fmt.Sprintf(
			"%s node %s (%s) sits inside function %s (%s), and a function is an isolation scope: this %s cannot reach a loop "+
				"outside it — declare the loop inside that function", typ, node.ID, node.Label(), boundary.ID, boundary.Label(), typ))
	default:
		return failf(nr, flow.ErrCodeConfig, fmt.Sprintf(
			"%s node %s (%s) has no loop above it, so there is nothing for it to act on: put a loop around this node, or delete it",
			typ, node.ID, node.Label()))
	}
}

// ========== Break ==========

var _ Executor = (*BreakExecutor)(nil)

type BreakExecutor struct{}

func (e *BreakExecutor) Execute(node *flow.Node, ctx *flow.ExecContext,
	processID string, nr *flow.NodeResult) error {
	if err := checkSignalScope("break", node, ctx, nr); err != nil {
		return err
	}
	nr.Finish(flow.StatusSuccess, "", "")
	return SignalBreak
}

func (e *BreakExecutor) Capabilities() NodeCapabilities {
	return NodeCapabilities{ErrorCodes: []string{}, Actions: []flow.ErrorAction{}}
}

func NewBreakExecutor() *BreakExecutor { return &BreakExecutor{} }

// ========== Continue ==========

var _ Executor = (*ContinueExecutor)(nil)

type ContinueExecutor struct{}

func (e *ContinueExecutor) Execute(node *flow.Node, ctx *flow.ExecContext,
	processID string, nr *flow.NodeResult) error {
	if err := checkSignalScope("continue", node, ctx, nr); err != nil {
		return err
	}
	nr.Finish(flow.StatusSuccess, "", "")
	return SignalContinue
}

func (e *ContinueExecutor) Capabilities() NodeCapabilities {
	return NodeCapabilities{ErrorCodes: []string{}, Actions: []flow.ErrorAction{}}
}

func NewContinueExecutor() *ContinueExecutor { return &ContinueExecutor{} }

// ========== Return ==========

var _ Executor = (*ReturnExecutor)(nil)

type ReturnExecutor struct{}

func (e *ReturnExecutor) Execute(node *flow.Node, ctx *flow.ExecContext,
	processID string, nr *flow.NodeResult) error {
	nr.Finish(flow.StatusSuccess, "", "")
	return SignalReturn
}

func (e *ReturnExecutor) Capabilities() NodeCapabilities {
	return NodeCapabilities{ErrorCodes: []string{}, Actions: []flow.ErrorAction{}}
}

func NewReturnExecutor() *ReturnExecutor { return &ReturnExecutor{} }

func init() {
	RegisterExecutor("break", NewBreakExecutor())
	RegisterExecutor("continue", NewContinueExecutor())
	RegisterExecutor("return", NewReturnExecutor())
	RegisterExecutor("exit", NewExitExecutor())
}

// ========== Exit ==========

var _ Executor = (*ExitExecutor)(nil)

type ExitExecutor struct{}

func (e *ExitExecutor) Execute(node *flow.Node, ctx *flow.ExecContext,
	processID string, nr *flow.NodeResult) error {
	nr.Finish(flow.StatusSuccess, "", "")
	return SignalExit
}

func (e *ExitExecutor) Capabilities() NodeCapabilities {
	return NodeCapabilities{ErrorCodes: []string{}, Actions: []flow.ErrorAction{}}
}

func NewExitExecutor() *ExitExecutor { return &ExitExecutor{} }
