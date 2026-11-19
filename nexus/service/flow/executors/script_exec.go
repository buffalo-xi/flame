package executors

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"flame/nexus/service/flow/core"
	"flame/nexus/shared/capability"
	"flame/nexus/shared/capabilitypolicy"
	"flame/tools/code"
)

// 全局脚本客户端，通过 SetCodeClient 初始化。
//
// 它只是到 sandboxd 的连接配置（无池）：池与隔离方式都在 sandboxd 侧。
var globalCodeClient *code.Client

// SetCodeClient 设置全局脚本执行客户端
func SetCodeClient(client *code.Client) {
	globalCodeClient = client
}

// capabilitySnapshot 平台能力快照（单测可替换，避免依赖 DB）。
// 与 agent 工具侧同源：capabilitypolicy.Snapshot 是 fail-closed 的。
var capabilitySnapshot = capabilitypolicy.Snapshot

// capabilityError 平台能力未开通。
//
// 单独成一个类型而不是复用 code.Error：它必须归到 flow.ErrCodeCapability，
// 混进「脚本报错」会让 on_error 规则把它当成脚本问题（可重试、看 traceback），
// 而真正该做的是找平台管理员开通。
type capabilityError struct{ capability string }

func (e *capabilityError) Error() string {
	// 流程节点报错属于执行层，用 ShortLabel：不把「工作流 code 节点 / 分支 / binding」
	// 这类场景枚举塞进报错（见 capability.go 的 Label 注释）。
	return "平台未给本租户开通能力 " + e.capability +
		"（" + capability.ShortLabel(e.capability) + "），需平台管理员在「插件与能力」中开通"
}

// ensureCodeCapability 工作流 code 节点的平台能力准入。
//
// 为什么必须在这里：工作流走 /v1/code 通道（服务端托管解释器），与 agent 的 exec
// 工具不是同一条通道，之前**完全没有能力闸** —— 平台关掉执行能力后，agent 跑不了
// 命令，但流程里的 code 节点照样能跑任意 Python。这道闸就是补那一半。
//
// schema 为空（单测 / 无租户语义的调用）时放行：没有租户就没有平台策略可言，
// 与 toolgrant 的 caps == nil 口径一致。
func ensureCodeCapability(schema string) error {
	if schema == "" {
		return nil
	}
	if capabilitySnapshot(schema)[capability.Code] {
		return nil
	}
	return &capabilityError{capability: capability.Code}
}

// schemaOf 从节点结果取租户 schema（ExecContext 为空时返回空串 = 不做能力判定）。
func schemaOf(nr *flow.NodeResult) string {
	if nr == nil || nr.Ctx == nil {
		return ""
	}
	return nr.Ctx.Schema
}

// scriptNodeConfig 脚本类节点（code / result / progress）的公共配置。
type scriptNodeConfig struct {
	Lang    string
	Script  string
	Timeout int
	Inputs  map[string]interface{}
	// Category 仅 progress 节点使用：外发类别 text | thinking，缺省 text。
	Category string
}

// parseScriptNode 解析脚本类节点的公共配置：config.code（lang/script/timeout）+ config.inputs，
// 以及 progress 专属的节点级 config.category。
// 三个节点同构，差异只在「结果怎么落地」，解析部分没有理由各写一份。
// 失败时已通过 nr 收尾，调用方直接 return 即可。
func parseScriptNode(node *flow.Node, ctx *flow.ExecContext, processID string, nr *flow.NodeResult) (*scriptNodeConfig, error) {
	if node.Data == nil || node.Data.Config == nil {
		return nil, failf(nr, flow.ErrCodeConfig, "node data or config is nil")
	}

	codeData, ok := node.Data.Config["code"].(map[string]interface{})
	if !ok {
		return nil, failf(nr, flow.ErrCodeConfig, "code config not found")
	}

	inputs := make(map[string]interface{})
	if raw, exists := node.Data.Config["inputs"].(map[string]interface{}); exists {
		resolved, err := ctx.GetVariables(raw, processID)
		if err != nil {
			return nil, failf(nr, flow.ErrCodeConfig, err.Error())
		}
		inputs = resolved
	}
	nr.SetInputVars(inputs)

	script, ok := codeData["script"].(string)
	if !ok {
		return nil, failf(nr, flow.ErrCodeConfig, "script not found")
	}

	lang, _ := codeData["lang"].(string)
	if lang == "" {
		lang = "lua"
	}

	timeout := ctx.ResolveScriptTimeout(lang, parseTimeoutConfig(codeData["timeout"]))

	// category 是 progress 节点自己的输出行为，不再塞进 code 脚本块
	category, _ := node.Data.Config["category"].(string)
	if category == "" {
		category = "text"
	}

	return &scriptNodeConfig{
		Lang:     lang,
		Script:   script,
		Timeout:  timeout,
		Inputs:   inputs,
		Category: category,
	}, nil
}

// runScript 执行节点脚本。
// templateAsString 为 true 时 template 语言直接返回渲染后的字符串（result / progress
// 只需要文本，跳过 JSON decode）；false 时走 ExecuteScript 保留结构化返回值（code 节点）。
// 错误已按统一口径收尾，调用方直接 return 即可。
func runScript(ctx *flow.ExecContext, nr *flow.NodeResult, cfg *scriptNodeConfig, templateAsString bool) (any, error) {
	if templateAsString && cfg.Lang == "template" {
		tmplCtx := BuildScriptTemplateContext(nr, cfg.Inputs)
		rendered, err := RenderTemplate(cfg.Script, tmplCtx)
		if err != nil {
			return nil, failf(nr, flow.ErrCodeScript, err.Error())
		}
		return rendered, nil
	}

	result, err := ExecuteScript(ctx.StdCtx, cfg.Lang, cfg.Script, cfg.Inputs, nr, cfg.Timeout)
	if err != nil {
		return nil, finishScriptError(ctx, nr, err)
	}
	return result, nil
}

// finishScriptError 把脚本执行错误映射成节点错误码并收尾。
//
// code / result / progress 三个脚本类节点共用这一份映射：同一类错误在哪个节点里
// 都必须归到同一个码，否则 on_error 里按码配置的重试/跳过会因节点类型而失效。
// 返回值一律是 *ExecError，保证 runner 的 shouldBypassOnError 能读到 code。
func finishScriptError(ctx *flow.ExecContext, nr *flow.NodeResult, err error) error {
	var capErr *capabilityError
	if errors.As(err, &capErr) {
		// 平台能力未开通：与脚本报错分开归码（重试没用，该找管理员开通）
		return failf(nr, flow.ErrCodeCapability, err.Error())
	}

	var scriptErr *code.Error
	if !errors.As(err, &scriptErr) {
		return failf(nr, flow.ErrCodeInternal, err.Error())
	}

	switch scriptErr.Code {
	case code.ErrCodeTimeout:
		return failf(nr, flow.ErrCodeTimeout, err.Error())
	case code.ErrCodeBusy:
		// 池满被拒：脚本没有执行过，重试是安全的 —— 单独成码供 on_error.retry 匹配
		return failf(nr, flow.ErrCodeBusy, err.Error())
	case code.ErrCodeScriptError:
		return failf(nr, flow.ErrCodeScript, err.Error())
	case code.ErrCodeCancel:
		// 取消类错误要区分「用户主动停止」与「工作流级超时」：
		// 用 ctx 判断而不是匹配错误文本，两者在 on_error 里可配的动作不同。
		errCode := flow.ErrCodeCancel
		if ctx != nil && ctx.StdCtx.Err() == context.DeadlineExceeded {
			errCode = flow.ErrCodeTimeout
		}
		nr.Finish(flow.StatusCancelled, errCode, err.Error())
		return newExecError(nr.ErrCode, nr.ErrMsg)
	case code.ErrCodeConnectFail,
		code.ErrCodeWorkerComm,
		code.ErrCodeSendFail:
		// 与沙箱 / worker 的连接问题：换一次通常就好，归 connection 才能被 on_error.retry 匹配。
		// 归 internal 会被 shouldBypassOnError 直接返回，等于把这个最该重试的错误锁死。
		return failf(nr, flow.ErrCodeConnection, err.Error())
	case code.ErrCodeUnsupportedLang:
		// 用户选的语言平台不支持：配置问题，改配置就好（config 同样 bypass on_error）
		return failf(nr, flow.ErrCodeConfig, err.Error())
	case code.ErrCodeUnknownAction:
		// 沙箱返回了预期之外的动作：平台侧协议问题，归 other 至少还能进 on_error
		return failf(nr, flow.ErrCodeOther, err.Error())
	default:
		return failf(nr, flow.ErrCodeOther, err.Error())
	}
}

// scriptErrCode 把脚本执行错误归一为 flow 错误码。
//
// 目前只把「沙箱繁忙」单独拎出来：它是一次可重试的瞬时拒绝（脚本压根没执行），
// 与「脚本自己报错」完全不同 —— 混进 script 之后，按码重试就永远配不上了
// （码值来自 sandboxd，见 tools/code 的 ErrCodeBusy）。
func scriptErrCode(err error) string {
	var ce *capabilityError
	if errors.As(err, &ce) {
		return flow.ErrCodeCapability
	}
	var se *code.Error
	if errors.As(err, &se) && se.Code == code.ErrCodeBusy {
		return flow.ErrCodeBusy
	}
	return flow.ErrCodeScript
}

// ExecuteScript 执行脚本：纯「输入 → 返回」，无回调副作用。
//
// template 不走 sandboxd，纯 Go 渲染；其余语言委托给全局脚本客户端。
func ExecuteScript(ctx context.Context, lang, scriptTxt string,
	inputs map[string]any, nr *flow.NodeResult, timeout int) (any, error) {

	if lang == "template" {
		ctxData := BuildScriptTemplateContext(nr, inputs)
		result, err := ExecuteTemplateScript(scriptTxt, ctxData)
		if err != nil {
			return nil, code.NewError(code.ErrCodeScriptError, err.Error(), err)
		}
		return result, nil
	}

	// 平台能力闸：template 是纯 Go 渲染（不碰沙箱，上面已返回），其余语言一律要过。
	// 放在这里是因为所有脚本入口（code / result / progress / branch / loop / binding /
	// 各节点的 handler 与 post）都收敛到本函数 —— 漏一处就等于没闸。
	if err := ensureCodeCapability(schemaOf(nr)); err != nil {
		return nil, err
	}

	if globalCodeClient == nil {
		return nil, errors.New("code client not initialized")
	}
	r, err := globalCodeClient.Execute(ctx, lang, scriptTxt, inputs, timeout)
	if err != nil {
		// 错误时的完整 stderr（含 traceback）也进节点日志，不进 agent 上下文。
		var se *code.Error
		if errors.As(err, &se) && se.Logs != "" {
			nr.AddLogf("脚本错误输出:\n%s", se.Logs)
		}
		return nil, err
	}
	// print 输出进节点日志；机器要的数据仍走 return（r.Value）。
	if r != nil && r.Logs != "" {
		nr.AddLogf("脚本输出:\n%s", r.Logs)
	}
	return r.Value, nil
}

// ---------- return 结果按声明分发（outputs / targets / locals） ----------

// topLevelName 取变量路径的顶层名："a.b" / "a[0]" → "a"
func topLevelName(path string) string {
	name := strings.Split(path, ".")[0]
	return strings.Split(name, "[")[0]
}

// outputDecls 已声明的 output 变量名集合（OutputVars 由 InitOutputVars 预填）。
func outputDecls(nr *flow.NodeResult) map[string]bool {
	set := make(map[string]bool, len(nr.OutputVars))
	for k := range nr.OutputVars {
		set[k] = true
	}
	return set
}

// localDecls 已声明的 local 变量名集合（LocalVars 由 InitLocalVars 预填）。
func localDecls(nr *flow.NodeResult) map[string]bool {
	set := make(map[string]bool, len(nr.LocalVars))
	for k := range nr.LocalVars {
		set[k] = true
	}
	return set
}

// targetDecls 从节点配置读取 targets 声明的变量名集合。
func targetDecls(nr *flow.NodeResult) map[string]bool {
	set := make(map[string]bool)
	if nr.Ctx == nil || nr.Ctx.Flow == nil || nr.Ctx.Flow.NodeMap == nil {
		return set
	}
	node := nr.Ctx.Flow.NodeMap[nr.NodeID]
	if node == nil || node.Data == nil || node.Data.Config == nil {
		return set
	}
	targets, ok := node.Data.Config["targets"].(map[string]interface{})
	if !ok {
		return set
	}
	for k := range targets {
		set[k] = true
	}
	return set
}

// checkDeclConflict 三份声明的变量名必须两两不相交。
func checkDeclConflict(outs, targets, locals map[string]bool) error {
	if c := intersect(outs, targets); len(c) > 0 {
		return fmt.Errorf("变量同时声明在 outputs 和 targets: %v", c)
	}
	if c := intersect(outs, locals); len(c) > 0 {
		return fmt.Errorf("变量同时声明在 outputs 和 locals: %v", c)
	}
	if c := intersect(targets, locals); len(c) > 0 {
		return fmt.Errorf("变量同时声明在 targets 和 locals: %v", c)
	}
	return nil
}

func intersect(a, b map[string]bool) []string {
	var out []string
	for k := range a {
		if b[k] {
			out = append(out, k)
		}
	}
	return out
}

// applyScriptResult 把脚本返回的 map 按节点声明分发到 output / target / local。
//
// 脚本是无状态的纯函数：返回的每个 key 都是「已声明变量」的名字，引擎查声明
// 把它路由到对应落地位置。未声明的 key 报错（fail-fast，防手滑写错变量名）。
func applyScriptResult(nr *flow.NodeResult, result any) error {
	m, ok := result.(map[string]any)
	if !ok {
		return fmt.Errorf("脚本须返回 map，得到 %T", result)
	}
	outs, targets, locals := outputDecls(nr), targetDecls(nr), localDecls(nr)
	if err := checkDeclConflict(outs, targets, locals); err != nil {
		return err
	}
	for key, value := range m {
		root := topLevelName(key)
		switch {
		case outs[root]:
			if err := nr.SetOutputVarWithFire(key, value); err != nil {
				return err
			}
		case targets[root]:
			if err := applyTargetVar(nr, key, value); err != nil {
				return err
			}
		case locals[root]:
			if err := nr.SetLocalVar(key, value); err != nil {
				return err
			}
		default:
			return fmt.Errorf("脚本返回了未声明的变量: %s", key)
		}
	}
	return nil
}

// applyTargetVar 把返回值落到 targets 声明的目标。
func applyTargetVar(nr *flow.NodeResult, path string, value any) error {
	def, err := nr.GetTargetDef(path)
	if err != nil {
		return err
	}
	target, ok := def.(map[string]interface{})
	if !ok {
		return errors.New("invalid target definition")
	}
	// 拷贝 target 再改 source_key：GetTargetDef 返回的是 node config 里的 map 引用，
	// 直接改会污染节点配置（老实现就踩过这一点）。
	tgt := make(map[string]interface{}, len(target))
	for k, v := range target {
		tgt[k] = v
	}
	sourceKey, _ := tgt["source_key"].(string)
	if firstDot := strings.IndexAny(path, ".["); firstDot != -1 {
		tgt["source_key"] = sourceKey + path[firstDot:]
	}
	return nr.Ctx.SetVariableWithFire(tgt, value, nr.ProcessID)
}

// applyHandlerAppend 把逐 chunk 调用的 handler 脚本返回结果用「追加」语义写回自定义输出变量。
//
// 流式节点（LLM / HTTP / RPC / MCP）的 handler 每来一个 chunk 调一次，脚本是无状态的
// 纯转换：return 的 string 字段拼接到当前值末尾，非 string 字段直接覆盖
// —— 与 AppendOutputVarWithFire 的语义对齐。
//
// deny 里的名字是节点固定输出变量（RPC 的 result/progress/error 等）：这些变量由执行器
// 自己写，脚本不允许写。原因是 string 是追加语义，执行器写过之后脚本再写只会拼成两份，
// "这个变量到底是最终结果还是片段拼接"就没有答案了。命中时记一条日志再跳过，
// 免得脚本作者以为自己的写入生效了。传 nil 表示不限制（尚未收口的节点）。
func applyHandlerAppend(nr *flow.NodeResult, result any, deny map[string]struct{}) error {
	m, ok := result.(map[string]any)
	if !ok {
		return fmt.Errorf("handler 脚本须返回 map，得到 %T", result)
	}
	for k, v := range m {
		if _, fixed := deny[k]; fixed {
			// 逐帧调用会反复命中同一条，用 WarnOnce 收成一条，避免刷屏。
			nr.WarnOnce("handler-fixed-output:"+k,
				"handler 返回了固定输出变量 %s，已忽略（该变量由节点自动写入，脚本只能写自定义变量）", k)
			continue
		}
		if err := nr.AppendOutputVarWithFire(k, v); err != nil {
			return err
		}
	}
	return nil
}
