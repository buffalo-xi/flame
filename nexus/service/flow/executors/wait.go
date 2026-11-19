package executors

import (
	"flame/nexus/service/flow/core"
	"fmt"
)

var _ Executor = (*WaitExecutor)(nil)

type WaitExecutor struct{}

func (e *WaitExecutor) Execute(node *flow.Node, ctx *flow.ExecContext, processID string, nr *flow.NodeResult) error {
	nr.WaitResult = &flow.WaitResult{}

	// 获取关联的 fork 节点 ID
	forkNodeID := ""
	if node.Data != nil && node.Data.Config != nil {
		if val, ok := node.Data.Config["fork_node_id"].(string); ok {
			forkNodeID = val
		}
	}

	if forkNodeID == "" {
		return failf(nr, flow.ErrCodeConfig, "fork_node_id not configured")
	}

	// 查找 fork 节点对应的 NodeResult
	forkNR := ctx.FindNodeResultByProcessChain(forkNodeID, processID)
	if forkNR == nil || forkNR.ForkResult == nil {
		// fork_node_id 指错了（指向不存在的 fork、或那个 fork 不在本链上）：
		// 图配置错误，与上一行「没配置」同属一类，报 internal 会把人引去查平台
		return failf(nr, flow.ErrCodeConfig, fmt.Sprintf("fork node result not found: %s", forkNodeID))
	}

	// 从当前节点的 config 中获取超时配置（与其它节点同口径：关闭即不设节点级超时）
	timeout := -1
	if node.Data != nil && node.Data.Config != nil {
		timeout = parseTimeoutConfig(node.Data.Config["timeout"])
	}
	timeout = ctx.ResolveTimeout(timeout)

	nr.WaitResult.ForkNodeID = forkNodeID
	nr.AddLogf("waiting for concurrent group %s, timeout %ds", forkNodeID, timeout)
	completed, err := forkNR.ForkResult.WaitTimeoutCtx(ctx.StdCtx, timeout)
	if err != nil {
		// ctx 取消/超时：交由上层按取消处理，不当作 wait 节点自身失败
		if ctxErr := ctx.StdCtx.Err(); ctxErr != nil {
			nr.Finish(flow.StatusCancelled, getCancelErrCode(ctx), ctxErr.Error())
			return ctxErr
		}
		// 状态异常：fork 已被 wait 过（重复 wait）
		nr.AddLog(fmt.Sprintf("wait failed: %v", err))
		// 同一个 fork 被 wait 两次是图结构错误（不是平台故障）
		return failf(nr, flow.ErrCodeConfig, err.Error())
	}
	if completed {
		nr.AddLog("wait finished: every concurrent task completed")
		// 扫描同 fork 组成员状态：任一失败则 wait 失败（fail-fast）
		// 注意：扫描的是节点状态而非输出变量，不违反"wait 后不引用并发分支结果"约束
		failed := scanForkMembersFailed(ctx, forkNR.ForkResult)
		if failed != "" {
			nr.AddLog("concurrent branch failed: " + failed)
			return failf(nr, flow.ErrCodeOther, "concurrent branch failed: "+failed)
		}
	} else {
		// 真正的超时：部分并发任务未完成，交由 onError 规则决定 skip/retry/terminate
		nr.AddLog("wait timed out: some concurrent tasks did not finish")
		return failf(nr, flow.ErrCodeTimeout, "wait timed out: some concurrent tasks did not finish")
	}

	nr.Finish(flow.StatusSuccess, "", "")
	return nil
}

// scanForkMembersFailed 扫描 fork 组成员的节点状态，返回第一个失败成员的描述（空串表示无失败）
func scanForkMembersFailed(ctx *flow.ExecContext, fr *flow.ForkResult) string {
	errMsg := ""
	for _, m := range fr.Members() {
		key := m.ProcessID + "_" + m.NodeID
		memberNR, ok := ctx.ExecutedNodeMap.Get(key)
		if !ok || memberNR == nil {
			continue
		}
		if memberNR.Status == flow.StatusFailed {
			errMsg = errMsg + "\n" + fmt.Sprintf("node failed %s (process %s): %s", m.NodeID, m.ProcessID, memberNR.ErrMsg)
		}
		if memberNR.Status == flow.StatusCancelled {
			errMsg = errMsg + "\n" + fmt.Sprintf("node canceled %s (process %s): %s", m.NodeID, m.ProcessID, memberNR.ErrMsg)
		}
	}
	return errMsg
}

func (e *WaitExecutor) Capabilities() NodeCapabilities {
	return NodeCapabilities{
		// config：fork_node_id 没填 / 指错、同一个 fork 被 wait 两次，都是图配置错误
		ErrorCodes: []string{flow.ErrCodeConfig, flow.ErrCodeTimeout, flow.ErrCodeOther},
		Actions:    []flow.ErrorAction{flow.ErrorActionSkip},
		HasPost:    false,
	}
}

func NewWaitExecutor() *WaitExecutor {
	return &WaitExecutor{}
}

func init() {
	RegisterExecutor("wait", NewWaitExecutor())
}
