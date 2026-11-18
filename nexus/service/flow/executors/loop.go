package executors

import (
	"context"
	"errors"
	"flame/nexus/service/flow/core"
	"fmt"
)

var _ Executor = (*LoopExecutor)(nil)

type LoopExecutor struct{}

func (e *LoopExecutor) Execute(node *flow.Node, ctx *flow.ExecContext, processID string, nr *flow.NodeResult) error {
	nr.LoopResult = &flow.LoopResult{}

	if node.Data == nil || node.Data.Config == nil {
		return failf(nr, flow.ErrCodeConfig, "node data or config is nil")
	}

	// 初始化 locals
	if locals, ok := node.Data.Config["locals"].(map[string]any); ok {
		nr.InitLocalVars(locals)
	}

	// 初始化 outputs
	if outputs, ok := node.Data.Config["outputs"].(map[string]any); ok {
		nr.InitOutputVars(outputs)
		nr.InitOutputBindings(outputs)
	}

	// 初始化循环变量
	if loopVars, ok := node.Data.Config["loop_vars"].(map[string]any); ok {
		nr.LoopResult.LoopVars = flow.InitLoopVars(loopVars)
	}

	// 统一解析 inputs（config 顶层）到 NodeResult.InputVars
	if inputsCfg, ok := node.Data.Config["inputs"].(map[string]interface{}); ok {
		inputVars, err := ctx.GetVariables(inputsCfg, processID)
		if err != nil {
			return failf(nr, flow.ErrCodeConfig, err.Error())
		}
		nr.SetInputVars(inputVars)
	}

	lc, _ := node.Data.Config["loop"].(map[string]interface{})
	if lc == nil {
		return failf(nr, flow.ErrCodeConfig, "loop config not found")
	}

	loopType, _ := lc["type"].(string)
	nr.LoopResult.LoopConfig = lc

	switch loopType {
	case "count":
		totalTimes, terr := parseCountInt(lc, "total_times")
		if terr != nil {
			return failf(nr, flow.ErrCodeConfig, terr.Error())
		}
		return e.executeLoopCount(node, ctx, processID, nr, totalTimes)

	case "each":
		breakOnEmpty, _ := lc["break_on_empty"].(bool)
		sourceKey, _ := lc["source_key"].(string)
		return e.executeLoopEach(node, ctx, processID, nr, sourceKey, breakOnEmpty)

	case "while":
		return e.executeLoopWhile(node, ctx, processID, nr, lc)

	case "do_while":
		return e.executeLoopDoWhile(node, ctx, processID, nr, lc)

	default:
		return failf(nr, flow.ErrCodeConfig, "unknown loop type: "+loopType)
	}
}

func (e *LoopExecutor) Capabilities() NodeCapabilities {
	return NodeCapabilities{
		ErrorCodes: []string{},
		Actions:    []flow.ErrorAction{},
	}
}

// ========== 四种循环执行 ==========

func (e *LoopExecutor) executeLoopCount(node *flow.Node, ctx *flow.ExecContext,
	processID string, nr *flow.NodeResult, totalTimes int) error {

	totalTimes = resolveMaxTimes(ctx, totalTimes)
	for i := 0; i < totalTimes; i++ {
		if err := ctx.StdCtx.Err(); err != nil {
			nr.Finish(flow.StatusCancelled, getCancelErrCode(ctx), err.Error())
			return err
		}

		nr.LoopResult.IncIteration()
		nr.LoopResult.SetLoopVar("index", i)

		err := runIteration(node, ctx, processID)
		if err != nil {
			stop, retErr := handleIterationError(ctx, nr, i, err)
			if retErr != nil {
				return retErr
			}
			if stop {
				break
			}
			continue
		}
	}

	nr.Finish(flow.StatusSuccess, "", "")
	return nil
}

func (e *LoopExecutor) executeLoopEach(node *flow.Node, ctx *flow.ExecContext,
	processID string, nr *flow.NodeResult, sourceKey string, breakOnEmpty bool) error {

	// 从统一 inputs 中按 source_key 取遍历数组
	inputVars := nr.GetInputVars()
	arrayData, exists := inputVars[sourceKey]
	if !exists {
		return failf(nr, flow.ErrCodeConfig, "each 数据源在 inputs 中不存在: "+sourceKey)
	}

	items := toSlice(arrayData)
	if items == nil && arrayData != nil {
		return failf(nr, flow.ErrCodeConfig, "each 数据源不是数组类型: "+sourceKey)
	}
	if sysMax := ctx.ResolveMaxLoopTimes(); sysMax > 0 && len(items) > sysMax {
		nr.AddLogf("数组长度 %d 超过系统上限 %d，已截断", len(items), sysMax)
		items = items[:sysMax]
	}
	if breakOnEmpty && len(items) == 0 {
		nr.AddLog("数据源为空，跳过循环")
		nr.Finish(flow.StatusSuccess, "", "")
		return nil
	}

	for i, item := range items {
		if err := ctx.StdCtx.Err(); err != nil {
			nr.Finish(flow.StatusCancelled, getCancelErrCode(ctx), err.Error())
			return err
		}

		if breakOnEmpty && item == nil {
			nr.AddLogf("第 %d 项为空，中断循环", i)
			break
		}

		nr.LoopResult.IncIteration()
		nr.LoopResult.SetLoopVar("index", i)
		nr.LoopResult.SetLoopVar("item", item)

		err := runIteration(node, ctx, processID)
		if err != nil {
			stop, retErr := handleIterationError(ctx, nr, i, err)
			if retErr != nil {
				return retErr
			}
			if stop {
				break
			}
			continue
		}
	}

	nr.Finish(flow.StatusSuccess, "", "")
	return nil
}

func (e *LoopExecutor) executeLoopWhile(node *flow.Node, ctx *flow.ExecContext,
	processID string, nr *flow.NodeResult, lc map[string]interface{}) error {

	maxTimes, terr := parseCountInt(lc, "max_times")
	if terr != nil {
		return failf(nr, flow.ErrCodeConfig, terr.Error())
	}
	maxTimes = resolveMaxTimes(ctx, maxTimes)

	for i := 0; i < maxTimes; i++ {
		if err := ctx.StdCtx.Err(); err != nil {
			nr.Finish(flow.StatusCancelled, getCancelErrCode(ctx), err.Error())
			return err
		}

		// 先判断条件

		ok, errScript := evaluateCondition(ctx, nr, lc)
		if errScript != nil {
			nr.Finish(flow.StatusFailed, flow.ErrCodeScript, "执行条件脚本失败: "+errScript.Error())
			return errScript
		}
		if !ok {
			nr.AddLogf("条件不满足，循环结束（共 %d 轮）", i+1)
			break
		}

		nr.LoopResult.IncIteration()
		err := runIteration(node, ctx, processID)
		if err != nil {
			stop, retErr := handleIterationError(ctx, nr, i, err)
			if retErr != nil {
				return retErr
			}
			if stop {
				break
			}
			continue
		}
	}

	nr.Finish(flow.StatusSuccess, "", "")
	return nil
}

func (e *LoopExecutor) executeLoopDoWhile(node *flow.Node, ctx *flow.ExecContext,
	processID string, nr *flow.NodeResult, lc map[string]interface{}) error {

	maxTimes, terr := parseCountInt(lc, "max_times")
	if terr != nil {
		return failf(nr, flow.ErrCodeConfig, terr.Error())
	}
	maxTimes = resolveMaxTimes(ctx, maxTimes)

	for i := 0; i < maxTimes; i++ {
		if err := ctx.StdCtx.Err(); err != nil {
			nr.Finish(flow.StatusCancelled, getCancelErrCode(ctx), err.Error())
			return err
		}

		// 先执行（至少一轮）
		nr.LoopResult.IncIteration()
		err := runIteration(node, ctx, processID)
		if err != nil {
			stop, retErr := handleIterationError(ctx, nr, i, err)
			if retErr != nil {
				return retErr
			}
			if stop {
				break
			}
			continue
		}

		// 后判断条件
		ok, errScript := evaluateCondition(ctx, nr, lc)
		if errScript != nil {
			nr.Finish(flow.StatusFailed, flow.ErrCodeScript, "执行条件脚本失败: "+errScript.Error())
			return errScript
		}
		if !ok {
			nr.AddLogf("条件不满足，循环结束（共 %d 轮）", i+1)
			break
		}
	}

	nr.Finish(flow.StatusSuccess, "", "")
	return nil
}

// ========== 子函数 ==========

// handleIterationError 处理一轮迭代的错误，把控制信号翻译成循环语义。
//
// 四种循环（count / each / while / do-while）共用：控制信号的处理规则必须一致，
// 否则 break / return 在不同循环里表现不同。
//   - stop=true  循环应当结束（break / return / exit / 取消 / 失败）
//   - retErr!=nil 调用方直接返回该错误
func handleIterationError(ctx *flow.ExecContext, nr *flow.NodeResult, i int, err error) (stop bool, retErr error) {
	if errors.Is(err, SignalBreak) {
		nr.AddLogf("第 %d 轮 break", i)
		return true, nil
	}
	if errors.Is(err, SignalContinue) {
		nr.AddLogf("第 %d 轮 continue", i)
		return false, nil
	}
	if errors.Is(err, SignalReturn) {
		nr.Finish(flow.StatusSuccess, "", "")
		return true, err
	}
	if errors.Is(err, SignalExit) {
		nr.AddLogf("第 %d 轮 exit，终止循环", i)
		nr.Finish(flow.StatusSuccess, "", "")
		return true, err
	}
	if ctx.StdCtx.Err() != nil {
		nr.Finish(flow.StatusCancelled, getCancelErrCode(ctx), ctx.StdCtx.Err().Error())
		return true, ctx.StdCtx.Err()
	}
	return true, failIteration(nr, i, err)
}

// runIteration 执行一轮迭代：创建子进程，执行子节点链
func runIteration(node *flow.Node, ctx *flow.ExecContext, processID string) error {
	childPID, err := ctx.ProcessManager.NewProcess(processID, nil)
	if err != nil {
		return newExecError(flow.ErrCodeInternal, "create iteration process: "+err.Error())
	}

	entryNode := findEntryNode(ctx.Flow, node.ID)
	if entryNode == nil {
		// 静默跳过会让循环空转 N 轮、整体还判成功，日志里一条线索都没有。
		// 找不到循环体入口是流程图结构错误，运行期一发现就得报出来。
		return newExecError(flow.ErrCodeConfig,
			fmt.Sprintf("loop node %s: body entry node not found", node.ID))
	}

	return execute(entryNode, ctx, childPID, 0)
}

// evaluateCondition 评估 while/do_while 条件脚本
func evaluateCondition(ctx *flow.ExecContext,
	nr *flow.NodeResult, lc map[string]interface{}) (bool, error) {

	lang, _ := lc["lang"].(string)
	if lang == "" {
		lang = "lua"
	}
	// 条件脚本超时与 code/branch 同口径：config.loop.timeout，关闭时不设节点级超时。
	timeout := ctx.ResolveScriptTimeout(lang, parseTimeoutConfig(lc["timeout"]))
	condition, _ := lc["condition"].(string)

	// inputs 已在 Execute 时统一解析到 NodeResult.InputVars（拷贝避免污染原始值）
	inputs := make(map[string]interface{})
	if src := nr.GetInputVars(); src != nil {
		for k, v := range src {
			inputs[k] = v
		}
	}

	// 注入 loop vars 到 input（条件脚本可通过 input.index / input.item 访问）
	if nr.LoopResult != nil && nr.LoopResult.LoopVars != nil {
		for k, v := range nr.LoopResult.LoopVars {
			inputs[k] = v
		}
	}

	result, err := ExecuteScript(ctx.StdCtx, lang, condition, inputs, nr, timeout)
	if err != nil {
		nr.AddLogf("条件评估失败: %v", err)
		// 归 script 而不是 internal：条件脚本写错属于用户可修复的问题，
		// 归 internal 会被 shouldBypassOnError 直接终止，on_error 永远配不上。
		return false, newExecError(flow.ErrCodeScript, "evaluate condition: "+err.Error())
	}

	b, ok := result.(bool)
	return ok && b, nil
}

// parseCountInt 解析循环配置里的整数字段（count 的 total_times、while / do_while 的 max_times）。
//
// 键缺失返回 0，由 resolveMaxTimes 按「<=0 取系统上限」处理（这是文档写明的语义）。
// 但**类型不是数字**必须报错：静默当 0 的后果是「用户以为设了 5 次，实际跑满系统上限
// 10000 次」—— 一次配错类型放大成万次执行，比直接失败贵得多。
func parseCountInt(lc map[string]interface{}, key string) (int, error) {
	raw, ok := lc[key]
	if !ok || raw == nil {
		return 0, nil
	}
	switch v := raw.(type) {
	case float64:
		return int(v), nil
	case int:
		return v, nil
	case int64:
		return int(v), nil
	default:
		return 0, newExecError(flow.ErrCodeConfig,
			fmt.Sprintf("循环配置 %s 必须是数字，实际是 %T", key, raw))
	}
}

func resolveMaxTimes(ctx *flow.ExecContext, maxTimes int) int {
	sysMax := ctx.ResolveMaxLoopTimes()
	if maxTimes <= 0 {
		return sysMax
	}
	if maxTimes > sysMax {
		return sysMax
	}
	return maxTimes
}

func failIteration(nr *flow.NodeResult, iteration int, err error) error {
	code := flow.ErrCodeOther
	var ee *ExecError
	if errors.As(err, &ee) {
		code = ee.Code
	}
	return failf(nr, code, fmt.Sprintf("iteration %d failed: %v", iteration, err))
}

func toSlice(v interface{}) []interface{} {
	if v == nil {
		return nil
	}
	switch arr := v.(type) {
	case []interface{}:
		return arr
	case []string:
		result := make([]interface{}, len(arr))
		for i, v := range arr {
			result[i] = v
		}
		return result
	case []float64:
		result := make([]interface{}, len(arr))
		for i, v := range arr {
			result[i] = v
		}
		return result
	case []bool:
		result := make([]interface{}, len(arr))
		for i, v := range arr {
			result[i] = v
		}
		return result
	case []map[string]interface{}:
		result := make([]interface{}, len(arr))
		for i, v := range arr {
			result[i] = v
		}
		return result
	default:
		return nil
	}
}

// getCancelErrCode 根据 ctx.StdCtx.Err() 返回对应的错误码
func getCancelErrCode(ctx *flow.ExecContext) string {
	if ctx.StdCtx.Err() == context.Canceled {
		return flow.ErrCodeCancel
	}
	return flow.ErrCodeTimeout
}

func NewLoopExecutor() *LoopExecutor {
	return &LoopExecutor{}
}

func init() {
	RegisterExecutor("loop", NewLoopExecutor())
}
