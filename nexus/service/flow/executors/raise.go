package executors

import (
	"fmt"
	"strings"

	"flame/nexus/service/flow/core"
)

var _ Executor = (*RaiseExecutor)(nil)

// RaiseExecutor fails the node on purpose, with an error code the author picked in config.raise.code.
//
// It closes a gap that has no working answer today: a code node can only fail as `script`, and wf_set_error - still
// referenced in comments - wrote a run-level ctx.Error that no on_error rule can match. A raise carries a real
// node-level code, so an enclosing host or a call node can classify the failure.
type RaiseExecutor struct{}

// raiseExcludedCodes are the global codes a raise node may not carry:
//   - config / internal: shouldBypassOnError returns straight away for them, so an on_error rule written against a
//     raised config error would silently never fire;
//   - cancel: it means "the user stopped this run", while the stored status is derived from the run context -
//     raising it would record a failure whose code claims a cancellation that never happened.
var raiseExcludedCodes = map[string]bool{
	flow.ErrCodeConfig:   true,
	flow.ErrCodeInternal: true,
	flow.ErrCodeCancel:   true,
}

// IsRaisableCode reports whether a raise node may carry code. It is the one definition of the set: flowcheck keeps a
// copy (it cannot import this package) guarded by capabilities_drift_test.go, and the admin endpoint hands the same set
// to the editor picker through RaisableCodeList.
func IsRaisableCode(code string) bool {
	if code == "" || raiseExcludedCodes[code] {
		return false
	}
	for _, c := range flow.AllErrorCodes() {
		if c == code {
			return true
		}
	}
	return false
}

// RaisableCodeList returns the raisable codes in the runtime's own order, for the editor picker.
func RaisableCodeList() []string {
	out := make([]string, 0, len(flow.AllErrorCodes()))
	for _, c := range flow.AllErrorCodes() {
		if IsRaisableCode(c) {
			out = append(out, c)
		}
	}
	return out
}

func (e *RaiseExecutor) Execute(node *flow.Node, ctx *flow.ExecContext, processID string, nr *flow.NodeResult) error {
	if node.Data == nil || node.Data.Config == nil {
		return failf(nr, flow.ErrCodeConfig, "node data or config is nil")
	}

	// 1. config.inputs first: the message is a template over them, so an unresolvable row has to fail here rather
	// than render a half-empty sentence into the text an operator ends up reading.
	var inputArgs map[string]interface{}
	if inputs, ok := node.Data.Config["inputs"].(map[string]interface{}); ok {
		resolved, err := ctx.GetVariables(inputs, processID)
		if err != nil {
			return failf(nr, flow.ErrCodeConfig, "failed to read the input variables: "+err.Error())
		}
		inputArgs = resolved
	} else {
		inputArgs = make(map[string]interface{})
	}
	nr.SetInputVars(inputArgs)

	// 2. The behaviour block. The code is checked again at run time, not only at save time: a hand-written diagram, or
	// one written by an older tool, never passed that check.
	raiseCfg, _ := node.Data.Config["raise"].(map[string]interface{})
	if raiseCfg == nil {
		return failf(nr, flow.ErrCodeConfig, "raise node missing config.raise")
	}
	code, _ := raiseCfg["code"].(string)
	if !IsRaisableCode(code) {
		return failf(nr, flow.ErrCodeConfig, fmt.Sprintf(
			"raise.code %q is not raisable: use one of the global codes except config / internal / cancel", code))
	}
	message, _ := raiseCfg["message"].(string)
	if strings.TrimSpace(message) == "" {
		return failf(nr, flow.ErrCodeConfig, "raise node missing config.raise.message")
	}
	rendered, err := RenderTemplate(message, BuildScriptTemplateContext(nr, inputArgs))
	if err != nil {
		return failf(nr, flow.ErrCodeConfig, "failed to render the raise message: "+err.Error())
	}

	// 3. The failure itself. failf rather than Finish + nil: the node must end as failed carrying this code, which is
	// what lets an enclosing host classify it and what lets a call node propagate it.
	return failf(nr, code, rendered)
}

// Capabilities: the code is the author's choice, so nothing is declared and nothing is validated (an undeclared list
// means "pass every code through"; a declared one would rewrite the author's code to "other").
//
// No on_error action and no post, for the same reason branch / assign declare none: this node's failure is authored, so
// "is it tolerable here" was answered before the node existed. The catch point is the caller - a process / loop host, or
// the call node that invoked the function - and that is where skip / handler / post live.
func (e *RaiseExecutor) Capabilities() NodeCapabilities {
	return NodeCapabilities{ErrorCodes: nil}
}

func NewRaiseExecutor() *RaiseExecutor { return &RaiseExecutor{} }

func init() {
	RegisterExecutor("raise", NewRaiseExecutor())
}
