package executors

import (
	"context"
	"errors"
	"flame/nexus/service/flow/core"
	"fmt"
	"strings"
)

var _ Executor = (*BranchExecutor)(nil)

type BranchExecutor struct{}

// branchConfig 单个分支的配置
type branchConfig struct {
	id        string // 唯一标识，对应边 source_handle 中的 childId
	label     string // 显示名称（用于日志，不保证唯一）
	condition string
	exclusive bool // 排他：命中并执行完成后，停止尝试后续分支与默认分支
}

func (e *BranchExecutor) Execute(node *flow.Node, ctx *flow.ExecContext,
	processID string, nr *flow.NodeResult) error {

	nr.BranchResult = &flow.BranchResult{}

	// ── 1. 解析配置 ──
	config := node.Data.Config
	if config == nil {
		return failf(nr, flow.ErrCodeConfig, "分支节点缺少配置")
	}

	// 脚本相关配置统一放在 config.branch 块：{lang, timeout}
	lang := "lua"
	timeout := -1
	if branchCfg, ok := config["branch"].(map[string]interface{}); ok {
		if s, _ := branchCfg["lang"].(string); s != "" {
			lang = s
		}
		timeout = parseTimeoutConfig(branchCfg["timeout"])
	}
	timeout = ctx.ResolveScriptTimeout(lang, timeout)

	children, ok := config["children"].([]interface{})
	if !ok || len(children) == 0 {
		return failf(nr, flow.ErrCodeConfig, "分支节点没有配置任何分支")
	}

	branches, err := parseBranchChildren(children)
	if err != nil {
		return failf(nr, flow.ErrCodeConfig, "解析分支配置失败: "+err.Error())
	}

	// 获取输入变量（config 顶层，所有分支共用）
	inputVars := make(map[string]interface{})
	if inputsConfig, ok := config["inputs"].(map[string]interface{}); ok {
		vars, err := ctx.GetVariables(inputsConfig, processID)
		if err != nil {
			nr.AddLogf("获取输入变量失败: %v", err)
		} else {
			inputVars = vars
		}
	}
	nr.SetInputVars(inputVars)

	nr.AddLogf("共 %d 个普通分支", len(branches))

	// ── 2. 遍历普通分支 ──
	for _, branch := range branches {
		if err := ctx.StdCtx.Err(); err != nil {
			nr.Finish(flow.StatusCancelled, getCancelErrCode(ctx), err.Error())
			return err
		}

		if branch.condition == "" {
			nr.AddLogf("分支 %s(%s) 没有条件脚本，跳过", branch.label, branch.id)
			continue
		}

		matched, evalErr := evaluateBranchCondition(ctx, nr, branch, lang, timeout, inputVars)
		if evalErr != nil {
			nr.AddLogf("分支 %s(%s) 条件执行失败: %v", branch.label, branch.id, evalErr)
			return failf(nr, flow.ErrCodeConfig, fmt.Sprintf("分支 %s(%s) 条件执行失败: %v", branch.label, branch.id, evalErr))
		}
		if !matched {
			nr.AddLogf("分支 %s(%s) 条件不满足", branch.label, branch.id)
			continue
		}

		// 条件满足，执行分支链
		result := e.executeBranch(node, ctx, processID, nr, branch.id, branch.label)
		if result != nil {
			return result
		}

		// 排他分支：命中并执行完成后，停止尝试后续分支与默认分支，结束 branch 节点
		if branch.exclusive {
			nr.AddLogf("分支 %s(%s) 命中并执行完成（排他），停止后续分支尝试", branch.label, branch.id)
			nr.Finish(flow.StatusSuccess, "", "")
			return nil
		}

		nr.AddLogf("分支 %s(%s) 正常完成，继续检查下一个分支", branch.label, branch.id)
	}

	// ── 3. 默认分支 ──
	if err := ctx.StdCtx.Err(); err != nil {
		nr.Finish(flow.StatusCancelled, getCancelErrCode(ctx), err.Error())
		return err
	}

	defaultEdge := findBranchEdge(node, node.ID)
	if defaultEdge != nil {
		nr.AddLog("执行默认分支")
		result := e.executeBranch(node, ctx, processID, nr, node.ID, "default")
		if result != nil {
			return result
		}
		nr.AddLog("默认分支正常完成")
	} else {
		nr.AddLog("没有默认分支出边")
	}

	nr.Finish(flow.StatusSuccess, "", "")
	return nil
}

// executeBranch 执行一条分支链，返回 nil 表示正常完成，非 nil 表示需要向上传播
func (e *BranchExecutor) executeBranch(node *flow.Node, ctx *flow.ExecContext,
	processID string, nr *flow.NodeResult, edgeKey string, label string) error {

	targetEdge := findBranchEdge(node, edgeKey)
	if targetEdge == nil {
		nr.AddLogf("分支 %s 没有出边，跳过", label)
		return nil
	}

	targetNode := GetNodeByID(ctx.Flow, targetEdge.Target)
	if targetNode == nil {
		nr.AddLogf("分支 %s 目标节点 %s 不存在，错误返回", label, targetEdge.Target)
		return failf(nr, flow.ErrCodeConfig, fmt.Sprintf("分支 %s 目标节点 %s 不存在，错误返回", label, targetEdge.Target))
	}

	// 记录执行的分支 id
	nr.BranchResult.AddExecutedBranch(edgeKey)

	childPID, err := ctx.ProcessManager.NewProcess(processID, nil)
	if err != nil {
		return failf(nr, flow.ErrCodeInternal, fmt.Sprintf("创建分支 %s 子进程失败: %v", label, err))
	}

	nr.AddLogf("执行分支 %s → 节点 %s(%s)", label, targetNode.Data.Label, targetNode.ID)
	execErr := execute(targetNode, ctx, childPID, 0)

	// 正常完成
	if execErr == nil {
		return nil
	}

	// 控制信号（break/continue/return/exit）→ 节点成功，向上传播
	if isControlSignal(execErr) {
		nr.AddLogf("分支 %s 返回控制信号 %s，节点成功", label, execErr.Error())
		nr.Finish(flow.StatusSuccess, "", "")
		return execErr
	}

	// 上下文取消
	if ctx.StdCtx.Err() != nil {
		errCode := flow.ErrCodeTimeout
		if ctx.StdCtx.Err() == context.Canceled {
			errCode = flow.ErrCodeCancel
		}
		nr.Finish(flow.StatusCancelled, errCode, ctx.StdCtx.Err().Error())
		return ctx.StdCtx.Err()
	}

	// 子链错误 → 容器节点自身不算失败（条件判断/意图分类已完成），
	// 记 Success 后向上透传：runner 对非 Failed 状态自动绕过容器节点的
	// on_error，错误码经 ExecError 逐层传给祖先/顶层。
	// （目标节点不存在、子进程创建失败等 execute 之前的错误，
	//  已在上方 Finish(StatusFailed)，仍归属容器节点并走其 on_error。）
	code := flow.ErrCodeOther
	var ee *ExecError
	if errors.As(execErr, &ee) {
		code = ee.Code
	}
	nr.AddLogf("分支 %s 子链执行失败，错误向上透传: %v", label, execErr)
	nr.Finish(flow.StatusSuccess, "", "")
	return newExecError(code, fmt.Sprintf("分支 %s 执行失败: %v", label, execErr))
}

// parseBranchChildren 解析 children 配置为 branchConfig 切片（只解析普通分支）
func parseBranchChildren(children []interface{}) ([]branchConfig, error) {
	branches := make([]branchConfig, 0, len(children))
	for _, child := range children {
		childMap, ok := child.(map[string]interface{})
		if !ok {
			continue
		}

		id, _ := childMap["id"].(string)
		if id == "" {
			continue
		}

		label, _ := childMap["label"].(string)
		if label == "" {
			label = id
		}

		condition, _ := childMap["condition"].(string)

		// 解析 exclusive：缺省 true（排他：首个命中即结束本节点）。
		// 只有显式 false / "false" / "0" / "no" 才是非排他，其余（缺键、非法值）按 true。
		exclusive := true
		switch v := childMap["exclusive"].(type) {
		case bool:
			exclusive = v
		case string:
			switch strings.ToLower(strings.TrimSpace(v)) {
			case "false", "0", "no":
				exclusive = false
			}
		}

		branches = append(branches, branchConfig{
			id:        id,
			label:     label,
			condition: condition,
			exclusive: exclusive,
		})
	}

	if len(branches) == 0 {
		return nil, fmt.Errorf("没有有效的分支配置")
	}

	return branches, nil
}

// evaluateBranchCondition 评估分支条件脚本，返回是否匹配
func evaluateBranchCondition(ctx *flow.ExecContext, nr *flow.NodeResult, branch branchConfig,
	lang string, timeout int, inputVars map[string]interface{}) (bool, error) {

	result, err := ExecuteScript(ctx.StdCtx, lang, branch.condition, inputVars, nr, timeout)
	if err != nil {
		return false, fmt.Errorf("执行条件脚本失败: %v", err)
	}

	return isConditionTrue(result), nil
}

// findBranchEdge 前缀匹配 source_handle，兼容 right/left/bottom 方向
func findBranchEdge(node *flow.Node, branchID string) *flow.Edge {
	prefix := "source" + branchID
	for _, edge := range node.OutEdges {
		if strings.HasPrefix(edge.SourceHandle, prefix) {
			return edge
		}
	}
	return nil
}

// isConditionTrue 判断条件脚本的返回值是否为 true
func isConditionTrue(result interface{}) bool {
	if result == nil {
		return false
	}

	switch v := result.(type) {
	case bool:
		return v
	case string:
		return v == "true" || v == "True" || v == "TRUE"
	case int, int8, int16, int32, int64:
		return v != 0
	case float64:
		return v != 0
	case float32:
		return v != 0
	default:
		return false
	}
}

func (e *BranchExecutor) Capabilities() NodeCapabilities {
	return NodeCapabilities{
		ErrorCodes: []string{flow.ErrCodeConfig, flow.ErrCodeScript},
		Actions:    []flow.ErrorAction{},
	}
}

func NewBranchExecutor() *BranchExecutor {
	return &BranchExecutor{}
}

func init() {
	RegisterExecutor("branch", NewBranchExecutor())
}
