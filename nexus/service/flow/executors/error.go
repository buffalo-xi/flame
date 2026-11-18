package executors

import "flame/nexus/service/flow/core"

// ExecError 携带错误码的执行错误，供容器节点透传子链错误码。
//
// executeNode 在失败出口包装此错误，loop/function/process 等容器节点
// 通过 errors.As(err, &ee) 提取 ee.Code，避免硬编码 ErrCodeOther/Internal，
// 从而把子链真实错误类型逐层传给祖先节点。
//
// 仅承载 Code 和 Msg：Code 来自失败节点的 nr.ErrCode，Msg 来自 nr.ErrMsg
// （容器节点会逐层叠加上下文前缀，如 "iteration X failed: ..."）。
type ExecError struct {
	Code string
	Msg  string
}

func (e *ExecError) Error() string { return e.Msg }

// newExecError 构造携带错误码的执行错误，code 为空时降级为 other。
func newExecError(code, msg string) error {
	if code == "" {
		code = flow.ErrCodeOther
	}
	return &ExecError{Code: code, Msg: msg}
}

// failf 收尾为失败并返回对应的 *ExecError。
// 把「nr.Finish(...) + return newExecError(...)」这对固定搭配收成一处：
// 手工重复时容易漏写 return 或返回错误的变量，导致节点状态与函数返回值不一致。
func failf(nr *flow.NodeResult, errCode, msg string) error {
	nr.Finish(flow.StatusFailed, errCode, msg)
	// 用 nr 上的实际值而不是入参：脚本可以通过 wf_set_error 先设过错误，
	// Finish 是「先到先得」，此时节点状态与入参已经不同，返回入参会造成两者不一致。
	return newExecError(nr.ErrCode, nr.ErrMsg)
}
