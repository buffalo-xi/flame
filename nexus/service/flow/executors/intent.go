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

// IntentExecutor is the intent node: it classifies the input with an LLM and runs the matching branch
// chain. It differs from the branch node in that branch decides with a script condition while intent uses
// one LLM classification. The semantics are single-choice (naturally exclusive): one branch runs when it
// matches, otherwise the default branch runs.
type IntentExecutor struct{}

// intentBranchConfig is the config of one intent branch
type intentBranchConfig struct {
	id          string // unique id, matching childId in an edge's source_handle
	label       string // display name
	description string // intent description (folded into the prompt so the model understands it)
}

func (e *IntentExecutor) Execute(node *flow.Node, ctx *flow.ExecContext,
	processID string, nr *flow.NodeResult) error {

	nr.BranchResult = &flow.BranchResult{}

	// ── 1. Parse the config ──
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

	// Initialise the output variables (the fixed intent / index, whose types config.outputs may override)
	outputs := mergeIntentOutputs(config)
	nr.InitOutputVars(outputs)
	nr.InitOutputBindings(outputs)

	// ── 2. Read the input variables ──
	inputArgs, buildErr := resolveInputVars(node, ctx, processID, nr)
	if buildErr != nil {
		return failf(nr, flow.ErrCodeConfig, buildErr.Error())
	}

	// ── 3. Build the LLM request ──
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

	// ── 4. Ask the LLM to classify ──
	nr.AddLogf("classifying the intent with the LLM, %d branch(es)", len(branches))
	resp, genErr := gateway.Current.Generate(ctx.StdCtx, *modelInput, messages, nil)
	if genErr != nil {
		return failf(nr, flow.ErrCodeConnection, fmt.Sprintf("the intent classification LLM call failed: %v", genErr))
	}
	nr.AddLogf("LLM returned: %s", strings.TrimSpace(resp.Content))

	// ── 5. Parse the matched branch and run it ──
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
		// No match: take the default branch
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

// mergeIntentOutputs merges config.outputs with the fixed intent / index output variables, guaranteeing the
// backend can always write them.
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

// parseIntentBranches parses the children config into intent branches (plain branches that carry an id)
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

// buildIntentMessages builds the intent classification messages: system = the user's system_prompt plus the
// generated intent list plus the output-format instruction; user = the input variables as JSON.
//
// Global variables arrive through the node inputs ({{ .input.xxx }}), so the template context has no
// .global_vars.
func buildIntentMessages(nr *flow.NodeResult, nodeCfg map[string]interface{},
	llmCfg map[string]interface{}, branches []intentBranchConfig,
	inputVars map[string]interface{}) ([]gateway.Message, error) {

	systemPrompt, _ := llmCfg["system_prompt"].(string)

	// Before rendering, check "does the prompt reference a global variable of secret kind" — after
	// rendering it is one assembled string and the secret part cannot be told apart. This only warns (into
	// the node log), it does not block.
	WarnSecretPromptRefs(nr, nodeCfg, systemPrompt)

	// Render the system_prompt template (supporting {{.input.xxx}})
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
	// 	return nil, fmt.Errorf("failed to serialise the input variables: %w", err)
	// }

	return []gateway.Message{
		{Role: "system", Content: sb.String()},
		//	{Role: "user", Content: string(userContent)},
	}, nil
}

// parseIntentIndex parses the branch index returned by the LLM, tolerating a markdown code fence, a bare
// number, or a JSON object. -1 means no match (take the default branch).
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
	// Fallback: pull the first { ... } fragment out of the text
	if start := strings.Index(s, "{"); start >= 0 {
		if end := strings.LastIndex(s, "}"); end > start {
			if err := json.Unmarshal([]byte(s[start:end+1]), &obj); err == nil && obj.Index != nil {
				return *obj.Index
			}
		}
	}
	return -1
}

// stripCodeFence removes a markdown code fence such as ```json ... ```
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
		// on_error only applies to the intent node's own errors (mainly a failed LLM call:
		// connection/timeout); child-chain errors propagate as Success under the container rule and do not
		// trigger this node's on_error (see executeBranch in branch.go)
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
