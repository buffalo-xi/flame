package executors

import (
	"errors"
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

// ========== Break ==========

var _ Executor = (*BreakExecutor)(nil)

type BreakExecutor struct{}

func (e *BreakExecutor) Execute(node *flow.Node, ctx *flow.ExecContext,
	processID string, nr *flow.NodeResult) error {
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
