package executors

import (
	"errors"
	"fmt"

	"flame/nexus/service/flow/core"
)

// ExecError is an execution error carrying an error code, so container nodes can pass a child chain's
// error code through.
//
// executeNode wraps it at the failure exit, and container nodes such as loop/function/process extract
// ee.Code with errors.As(err, &ee) instead of hard-coding ErrCodeOther/Internal, which lets the child
// chain's real error type travel up through every ancestor.
//
// Code comes from the failing node's nr.ErrCode, Msg from nr.ErrMsg, and NodeID from the walker (see
// execute): the id of the node that produced the failure. A container node prefixes context layer by layer,
// e.g. "iteration X failed: node <id>: ...", so a message names the innermost cause it can, while the
// outermost one says which of its children failed.
type ExecError struct {
	Code   string
	Msg    string
	NodeID string
}

func (e *ExecError) Error() string { return e.Msg }

// newExecError builds an execution error carrying an error code; an empty code degrades to other.
func newExecError(code, msg string) error {
	if code == "" {
		code = flow.ErrCodeOther
	}
	return &ExecError{Code: code, Msg: msg}
}

// failf finishes as a failure and returns the matching *ExecError.
// It folds the fixed pair of "nr.Finish(...) + return newExecError(...)" into one place: repeating it by
// hand easily drops the return or returns the wrong variable, leaving the node status and the function
// result disagreeing.
func failf(nr *flow.NodeResult, errCode, msg string) error {
	nr.Finish(flow.StatusFailed, errCode, msg)
	// The values on nr are used rather than the arguments: a script may have set the error first via
	// wf_set_error, Finish is first-come-first-served, and at that point the node status no longer
	// matches the arguments — returning the arguments would leave the two disagreeing.
	return newExecError(nr.ErrCode, nr.ErrMsg)
}

// nodeMessage prefixes a child's failure with the id of the node that produced it.
//
// A container reports a child's error as its own, and without this the author reads "connection refused" in the
// container's ErrMsg and has to work out which node inside the body said it. The id comes from ExecError, stamped by the
// walker at the failure exit; errors no node produced (a control signal, a ctx cancellation, a scope aggregate that
// already names its members as "member <id>: ...") pass through untouched.
func nodeMessage(err error) string {
	var ee *ExecError
	if errors.As(err, &ee) && ee.NodeID != "" {
		return fmt.Sprintf("node %s: %s", ee.NodeID, err.Error())
	}
	return err.Error()
}
