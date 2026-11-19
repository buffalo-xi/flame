package executors

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"flame/nexus/service/flow/core"
	"flame/nexus/service/llm/gateway"
	"flame/tools/xsql"
)

var _ Executor = (*IntentExecutor)(nil)

// IntentExecutor 意图节点：用大模型对输入做意图分类，命中后执行对应分支链。
// 与分支节点（branch）的区别：branch 用脚本条件判断，intent 用一次 LLM 分类。
// 语义为单选（天然排他）：命中一个分支执行一个分支，未命中走默认分支。
type IntentExecutor struct{}

// intentBranchConfig 单个意图分支的配置
type intentBranchConfig struct {
	id          string // 唯一标识，对应边 source_handle 中的 childId
	label       string // 显示名称
	description string // 意图描述（用于拼进 prompt 供模型理解）
}

func (e *IntentExecutor) Execute(node *flow.Node, ctx *flow.ExecContext,
	processID string, nr *flow.NodeResult) error {

	nr.BranchResult = &flow.BranchResult{}

	// ── 1. 解析配置 ──
	config := node.Data.Config
	if config == nil {
		return failf(nr, flow.ErrCodeConfig, "intent node is missing its configuration")
	}

	children, ok := config["children"].([]interface{})
	if !ok || len(children) == 0 {
		return failf(nr, flow.ErrCodeConfig, "intent node configures no branch")
	}

	branches := parseIntentBranches(children)
	if len(branches) == 0 {
		return failf(nr, flow.ErrCodeConfig, "failed to parse the intent branch config")
	}

	// 初始化输出变量（固定 intent / index，可被 config.outputs 覆盖类型）
	outputs := mergeIntentOutputs(config)
	nr.InitOutputVars(outputs)
	nr.InitOutputBindings(outputs)

	// ── 2. 获取输入变量 ──
	inputArgs, buildErr := resolveInputVars(node, ctx, processID, nr)
	if buildErr != nil {
		return failf(nr, flow.ErrCodeConfig, buildErr.Error())
	}

	// ── 3. 构建 LLM 请求 ──
	llmCfg, _ := config["llm"].(map[string]interface{})
	if llmCfg == nil {
		llmCfg = map[string]interface{}{}
	}

	messages, msgErr := buildIntentMessages(nr, config, llmCfg, branches, inputArgs)
	if msgErr != nil {
		return failf(nr, flow.ErrCodeConfig, msgErr.Error())
	}

	model, herr := resolveLLMModel(ctx, llmCfg)
	if herr != nil {
		return failf(nr, herr.ErrCode, herr.ErrMsg)
	}
	model.GatewayModel = xsql.String(ctx.Schema) + ":" + model.GatewayModel
	modelInput := buildModelInput(llmCfg, model)

	if gateway.Current == nil {
		return failf(nr, flow.ErrCodeConnection, "the LLM gateway is not initialised")
	}

	// ── 4. 调用大模型分类 ──
	nr.AddLogf("classifying the intent with the LLM, %d branch(es)", len(branches))
	resp, genErr := gateway.Current.Generate(ctx.StdCtx, *modelInput, messages, nil)
	if genErr != nil {
		return failf(nr, flow.ErrCodeConnection, fmt.Sprintf("the intent classification LLM call failed: %v", genErr))
	}
	nr.AddLogf("LLM returned: %s", strings.TrimSpace(resp.Content))

	// ── 5. 解析命中分支并执行 ──
	idx := parseIntentIndex(resp.Content, len(branches))
	branchExec := &BranchExecutor{}

	if idx >= 0 && idx < len(branches) {
		b := branches[idx]
		nr.AddLogf("matched branch [%d] %s(%s)", idx, b.label, b.id)
		if werr := nr.SetOutputVarWithFire("intent", b.label); werr != nil {
			nr.AddLogf("failed to write intent: %v", werr)
		}
		if werr := nr.SetOutputVarWithFire("index", idx); werr != nil {
			nr.AddLogf("failed to write index: %v", werr)
		}
		result := branchExec.executeBranch(node, ctx, processID, nr, b.id, b.label)
		if result != nil {
			return result
		}
	} else {
		// 未命中：走默认分支
		nr.AddLogf("the LLM matched no intent (index=%d), taking the default branch", idx)

		if werr := nr.SetOutputVarWithFire("intent", ""); werr != nil {
			nr.AddLogf("failed to write intent: %v", werr)
		}
		if werr := nr.SetOutputVarWithFire("index", -1); werr != nil {
			nr.AddLogf("failed to write index: %v", werr)
		}

		defaultEdge := findBranchEdge(node, node.ID)
		if defaultEdge != nil {
			result := branchExec.executeBranch(node, ctx, processID, nr, node.ID, "default")
			if result != nil {
				return result
			}
		} else {
			nr.AddLog("no outgoing edge from the default branch")
		}
	}

	nr.Finish(flow.StatusSuccess, "", "")
	return nil
}

// mergeIntentOutputs 合并 config.outputs 与固定输出变量 intent / index，保证后端始终可写。
func mergeIntentOutputs(config map[string]interface{}) map[string]interface{} {
	outputs := make(map[string]interface{})
	if o, ok := config["outputs"].(map[string]interface{}); ok {
		for k, v := range o {
			outputs[k] = v
		}
	}
	if _, ok := outputs["intent"]; !ok {
		outputs["intent"] = map[string]interface{}{"type": "string"}
	}
	if _, ok := outputs["index"]; !ok {
		outputs["index"] = map[string]interface{}{"type": "number"}
	}
	return outputs
}

// parseIntentBranches 解析 children 配置为意图分支切片（只解析含 id 的普通分支）
func parseIntentBranches(children []interface{}) []intentBranchConfig {
	branches := make([]intentBranchConfig, 0, len(children))
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
		description, _ := childMap["description"].(string)
		branches = append(branches, intentBranchConfig{id: id, label: label, description: description})
	}
	return branches
}

// buildIntentMessages 构造意图分类 messages：
// system = 用户 system_prompt + 自动拼接的意图清单 + 输出格式指令；user = 输入变量 JSON。
//
// 全局变量经节点输入进入（{{ .input.xxx }}），模板上下文里没有 .global_vars。
func buildIntentMessages(nr *flow.NodeResult, nodeCfg map[string]interface{},
	llmCfg map[string]interface{}, branches []intentBranchConfig,
	inputVars map[string]interface{}) ([]gateway.Message, error) {

	systemPrompt, _ := llmCfg["system_prompt"].(string)

	// 渲染前先看「提示词有没有引用密钥类型的全局变量」—— 渲染后就是拼好的字符串，
	// 分不出哪一段来自 secret。这里只告警（记节点日志），不阻断。
	WarnSecretPromptRefs(nr, nodeCfg, systemPrompt)

	// 渲染 system_prompt 模板（支持 {{.input.xxx}}）
	renderedPrompt := ""
	if strings.TrimSpace(systemPrompt) != "" {
		tmplCtx := map[string]interface{}{"input": inputVars}
		rendered, err := RenderTemplate(systemPrompt, tmplCtx)
		if err != nil {
			return nil, fmt.Errorf("failed to render the system_prompt template: %w", err)
		}
		renderedPrompt = strings.TrimSpace(rendered)
	}

	var sb strings.Builder
	if renderedPrompt != "" {
		sb.WriteString(renderedPrompt)
		sb.WriteString("\n\n")
	}
	sb.WriteString("Available intents (0-based index):\n")
	for i, b := range branches {
		line := fmt.Sprintf("%d. %s", i, b.label)
		if strings.TrimSpace(b.description) != "" {
			line += " - " + strings.TrimSpace(b.description)
		}
		sb.WriteString(line + "\n")
	}
	sb.WriteString("\nDecide which intent best matches the user input. Output JSON only, nothing else: {\"index\": <number>}\n")
	sb.WriteString("If none matches, output {\"index\": -1}")

	// userContent, err := json.Marshal(inputVars)
	// if err != nil {
	// 	return nil, fmt.Errorf("输入变量序列化失败: %w", err)
	// }

	return []gateway.Message{
		{Role: "system", Content: sb.String()},
		//	{Role: "user", Content: string(userContent)},
	}, nil
}

// parseIntentIndex 解析 LLM 返回的分支序号，容错处理 markdown 代码块 / 纯数字 / JSON 对象。
// 返回 -1 表示未命中（走默认分支）。
func parseIntentIndex(content string, n int) int {
	s := strings.TrimSpace(content)
	s = stripCodeFence(s)

	var obj struct {
		Index *int `json:"index"`
	}
	if err := json.Unmarshal([]byte(s), &obj); err == nil && obj.Index != nil {
		return *obj.Index
	}
	if i, err := strconv.Atoi(s); err == nil {
		return i
	}
	// 兜底：从文本中提取第一个 { ... } 片段
	if start := strings.Index(s, "{"); start >= 0 {
		if end := strings.LastIndex(s, "}"); end > start {
			if err := json.Unmarshal([]byte(s[start:end+1]), &obj); err == nil && obj.Index != nil {
				return *obj.Index
			}
		}
	}
	return -1
}

// stripCodeFence 去掉 ```json ... ``` 之类的 markdown 代码块包裹
func stripCodeFence(s string) string {
	if !strings.HasPrefix(s, "```") {
		return s
	}
	if idx := strings.Index(s, "\n"); idx >= 0 {
		s = s[idx+1:]
	}
	s = strings.TrimSuffix(s, "```")
	return strings.TrimSpace(s)
}

func (e *IntentExecutor) Capabilities() NodeCapabilities {
	return NodeCapabilities{
		ErrorCodes: []string{
			flow.ErrCodeConfig,
			flow.ErrCodeConnection,
			flow.ErrCodeTimeout,
			flow.ErrCodeNotFound,
			flow.ErrCodeVar,
			flow.ErrCodeOther,
		},
		// on_error 只对意图节点自身错误生效（主要是 LLM 调用失败 connection/timeout）；
		// 子链错误已按容器语义 Success 透传，不触发本节点 on_error（见 branch.go executeBranch）
		Actions: []flow.ErrorAction{flow.ErrorActionRetry},
		HasPost: false,
	}
}

func NewIntentExecutor() *IntentExecutor {
	return &IntentExecutor{}
}

func init() {
	RegisterExecutor("intent", NewIntentExecutor())
}
