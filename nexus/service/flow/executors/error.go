package executors

import "flame/nexus/service/flow/core"

// ExecError is an execution error carrying an error code, so container nodes can pass a child chain's
// error code through.
//
// executeNode wraps it at the failure exit, and container nodes such as loop/function/process extract
// ee.Code with errors.As(err, &ee) instead of hard-coding ErrCodeOther/Internal, which lets the child
// chain's real error type travel up through every ancestor.
//
// It only carries Code and Msg: Code comes from the failing node's nr.ErrCode, Msg from nr.ErrMsg (a
// container node prefixes context layer by layer, e.g. "iteration X failed: ...").
type ExecError struct {
	Code string
	Msg  string
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
