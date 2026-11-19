package executors

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"flame/nexus/service/agent"
	"flame/nexus/service/agent/tools"
	"flame/nexus/service/flow/core"
	"flame/nexus/shared/auth"
	"flame/nexus/shared/callchain"
	flows "flame/nexus/shared/repository/flows"

	"github.com/google/uuid"
)

// init 把「按 id 试运行一个流程」交给 agent 包，供 run_flow 工具使用。
// 与 localendpoint.go 的注册点同理：打破 executors → agent 的单向依赖。
func init() { agent.SetFlowTestRunner(runFlowForTest) }

// runFlowForTest 供 run_flow 工具试运行一个工作流。
//
// 与 runFlowEndpoint 的差别只有两点：不要求端点存在（试跑的对象通常还没对外开放），
// 以及返回节点级成败而不只是一段文本。状态口径仍与运行时一致（废弃不可运行，
// 由调用方 flowsManager.RunTest 拦截）。
//
// 只回节点成败与失败节点日志，**不回节点输出变量**：HTTP body / LLM 正文动辄几十 KB，
// 进模型上下文既费 token 又几乎读不完；需要看数据走运行记录。
func runFlowForTest(
	ctx context.Context,
	authInfo *auth.AuthInfo,
	flowID string,
	args map[string]any,
	sessionID string,
	callDepth int,
	followDrafts bool,
) (*tools.FlowRunResult, error) {
	instance, err := flows.NewRepository().GetFlow(authInfo, flowID)
	if err != nil {
		return nil, fmt.Errorf("failed to get the flow: %w", err)
	}
	if instance == nil {
		return nil, fmt.Errorf("flow not found: %s", flowID)
	}

	// 入参复制后注入两个保留键（NewFlow 会剥离，不污染业务参数）：
	//   _call_depth 深度 +1，流程里的 agent 节点再调回 agent 时靠它收敛（与 runEndpoint 同口径）
	//   session_id  与调用方同一会话场地，产物落在同一个 users/{uid}/sessions/{sid}/
	input := make(map[string]any, len(args)+2)
	for k, v := range args {
		input[k] = v
	}
	input["_call_depth"] = callDepth + 1
	// 链经 ctx 传来（run_flow 工具里 callchain.With）：试跑的 flow 也是「即将执行的实体」，
	// 先把自己追加进链再下传 —— 否则它内部的 rpc / agent 节点绕回自己时，
	// 入站第一层看不到 F，要白跑一整轮才被拦住。
	input[callchain.ArgumentKey] = callchain.Append(
		callchain.From(ctx),
		callchain.Key(callchain.KindFlow, instance.ID),
	)
	if sessionID != "" {
		input["session_id"] = sessionID
	}

	runID := uuid.Must(uuid.NewV7()).String()
	start := time.Now()
	execCtx, err := NewFlow(instance, input, "", runID, authInfo.Schema, authInfo.ID, ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to initialise the flow run: %w", err)
	}
	// 试跑属于调试：本次运行读的是当前工作副本。它调起的子流程默认也跟草稿 ——
	// 否则「改 A、改 B、一起看效果」就得先把 B 发布一遍（发布=上线，见 releaseFlowVersionTx 的注释）。
	// 调用方可以改成 false，让子流程按正式版本跑，用来验证线上那套链路。
	execCtx.DebugScope = flow.NewDebugScope(followDrafts)

	// Start 同步跑完并落运行记录（run_type 落在 task / call，与其他直调一致）
	runErr := Start(execCtx)

	res := &tools.FlowRunResult{
		FlowID:     execCtx.GetflowId(),
		FlowName:   execCtx.GetFlowName(),
		RunID:      runID,
		Status:     "success",
		Result:     execCtx.GetResult(),
		DurationMs: time.Since(start).Milliseconds(),
		Nodes:      collectNodeTraces(execCtx),
	}
	switch {
	case ctx.Err() != nil:
		// 工具调用超时（缺省 60s）或上游取消：流程被中断，节点列表停在中断点。
		// 这一条要排在最前：被取消时 GetError 往往也被写成 context 错误，
		// 报成 failed 会让人以为是流程自身逻辑坏了。
		res.Status = "cancelled"
		res.ErrMsg = ctx.Err().Error()
	case execCtx.GetError() != "":
		res.Status = "failed"
		res.ErrMsg = execCtx.GetError()
	case runErr != nil:
		res.Status = "failed"
		res.ErrMsg = runErr.Error()
	}
	res.ErrCode = firstFailureCode(res.Nodes)
	return res, nil
}

// collectNodeTraces 取执行过的节点，按开始时间升序（执行顺序）。
//
// 成功节点也在列表里：失败时最有用的信息是「前几步跑过了、停在哪一步」。
// 日志只给非成功节点 —— 成功节点的日志基本都是「执行成功」这类噪音。
func collectNodeTraces(execCtx *flow.ExecContext) []tools.FlowNodeTrace {
	if execCtx == nil || execCtx.ExecutedNodeMap == nil {
		// 空数组而不是 nil：JSON 里 null 会被模型读成「不知道跑了什么」
		return []tools.FlowNodeTrace{}
	}
	// Range 持锁回调，排序必须放到锁外
	results := make([]*flow.NodeResult, 0, execCtx.ExecutedNodeMap.Len())
	execCtx.ExecutedNodeMap.Range(func(_ string, nr *flow.NodeResult) bool {
		if nr != nil {
			results = append(results, nr)
		}
		return true
	})
	sort.SliceStable(results, func(i, j int) bool {
		return results[i].StartTime.Before(results[j].StartTime)
	})

	out := make([]tools.FlowNodeTrace, 0, len(results))
	for _, nr := range results {
		tr := tools.FlowNodeTrace{
			NodeID:     nr.NodeID,
			Type:       string(nr.NodeType),
			Status:     string(nr.Status),
			ErrCode:    nr.ErrCode,
			ErrMsg:     nr.ErrMsg,
			DurationMs: nr.DurationMs,
			// post 脚本报错不影响节点主状态，但同样是失败原因，所以不看 status 都带上
			PostError:  nr.PostError,
			RetryCount: nr.RetryCount,
		}
		// 只有非成功节点才带日志与栈：成功节点的日志基本都是「执行成功」这类噪音
		if nr.Status != flow.StatusSuccess {
			if len(nr.RunLogs) > 0 {
				tr.Logs = strings.Join(nr.RunLogs, "\n")
			}
			tr.ErrStack = nr.ErrStack
		}
		out = append(out, tr)
	}
	return out
}

// firstFailureCode 取最早失败节点的错误码：流程级没有独立的错误码字段，
// 而 on_error 的匹配、排查的起点都是这个码。
func firstFailureCode(nodes []tools.FlowNodeTrace) string {
	for _, n := range nodes {
		if n.Status == string(flow.StatusFailed) && n.ErrCode != "" {
			return n.ErrCode
		}
	}
	return ""
}
