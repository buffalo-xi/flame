package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"flame/nexus/shared/callchain"
	"flame/nexus/shared/sandboxinfo"

	"github.com/google/uuid"
)

// ===== 工作流（{schema}.flows）=====
//
// 与 agent 元工具同构：create / delete / list / read / run（试跑）/ read_flow_run（查运行记录），
// 分类 flow-admin（仅超级智能体）。
// 差别在于工作流**没有定义域目录** —— 本体是 flows 表里一行 JSONB（diagram + 变量 + 限额），
// 所以 create_flow 必须自己把「步骤列表」组装成前端能渲染、执行器能跑的 diagram。
//
// 为什么不让模型直接传 diagram：26 种节点各有自己的 config 形状，还要算坐标、连边、
// source_handle / target_handle。让模型手写这段 JSON，错一个字段就是「能渲染、一运行就崩」。
// 工具只暴露「线性步骤」这一层，diagram 由 buildLinearDiagram 生成。

// FlowStep 创建工作流时的一步。
type FlowStep struct {
	Type   string         // 节点类型，见 flowStepTypes
	Label  string         // 节点标题（画布上显示的短名）
	Config map[string]any // 按 type 不同的节点配置，原样写进 data.config
	// Position 可选：模型自己排版的画布坐标。
	//
	// 规则是「顶层节点全给或全不给」：全给就完全按你给的摆，工具只做重叠检测；
	// 少给一个就整体退回自动排版 —— 半套坐标和自动排版混着用只会互相压住。
	// 容器子节点不用给（坐标相对父容器，工具按内容纵向排好）。
	Position map[string]int
	// Branches 只对 branch / intent 有意义：为该节点的每个子分支声明一条自己的步骤链。
	//
	// 执行器靠 handle 找路（executors.findBranchEdge 前缀匹配 "source"+branchID），
	// 所以「分支能分流」= 每个 config.children[].id 都有一条挂着 source<该 id> 的出边。
	// 这里声明后，每条子链会从分支节点拉一条带 handle 的边过去。
	//
	// 声明了 Branches 的步骤必须是所在链的最后一步：branch 执行完只会走 default 边，
	// 「分支之后的主链」没有语义，后续步骤应该写进各子链里。
	Branches []FlowBranch

	// Children 只对 loop / process / function 有意义：容器内部的步骤链。
	//
	// 执行器从 parent_node==容器 id 的 entry 节点起步，所以工具会顺手建 entry 并把
	// Children 的首节点接上去 —— 少这一步，容器会「成功但什么都没跑」。
	// 与 Branches 不同，声明了 Children 的步骤不必是链尾：容器有正常的出入口。
	Children []FlowStep
}

// FlowBranch 分支节点上某条子分支的步骤链。
//
// Key 对应 config.children[].id；"default" 表示默认分支（没有条件命中时走的那条边，
// 执行器按 "source"+节点自身 id 找它，所以不需要在 children 里再写一个）。
//
// 用切片而不是 map：节点 id 按声明顺序分配，map 的遍历顺序不固定会让同一份入参
// 建出不同的 diagram。
type FlowBranch struct {
	Key   string
	Steps []FlowStep
}

// FlowInfo list_flows 返回的一行。
type FlowInfo struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Status      int      `json:"status"`
	Endpoints   []string `json:"endpoints,omitempty"` // 已对外开放的协议（mcp / rpc）
}

// FlowDetail read_flow 返回：元信息 + diagram + 入口参数声明。
//
// Arguments 是 flows.arguments 原文（JSON Schema 形态）；没声明过就省略，
// 让模型能区分「没有入参」和「有入参但读不到」。
type FlowDetail struct {
	FlowInfo
	Diagram   json.RawMessage `json:"diagram"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

// FlowInput 创建工作流的入参（diagram 已由工具层组装好）。
type FlowInput struct {
	Name        string
	Description string
	Diagram     json.RawMessage
	// Arguments 入口参数声明（flows.arguments）；空 = 不声明。
	// 对外开放后它就是端点的 input_schema，也是 run_flow 的 inputs 键。
	Arguments json.RawMessage
	Endpoints []string // 要对外开放到哪些协议；空 = 仅内部
}

// flowRefHint flow / 工作流定位参数的统一描述（read / delete / run 三处共用）。
//
// 写法上的两个刻意之处：
//   - 示例只给名字，不给 UUID。模型有强模仿倾向，示例里出现 uuid 会让它学着编 id，
//     而编出来的 id 形态合法、只能靠「查不到」兜底，是最难自愈的一类错误；
//   - 明确说「不要自己编 id」，把「id 必须来自 list_flows」这条压成一句可执行的话。
const flowRefHint = "Workflow name, e.g. daily-sales-report. " +
	"The id returned by list_flows is accepted too (the two are equivalent: a UUID-shaped value is looked up by id, anything else by name); " +
	"never invent an id — copy it from list_flows' output"

// FlowRunResult 一次试运行的结果（run_flow 的返回体）。
//
// 只回「跑成了没、卡在哪个节点、为什么」，**不回节点输出变量**：HTTP body / LLM 正文
// 动辄几十 KB，塞进模型上下文既费 token 又几乎读不完；要看数据有运行记录页与 read_flow。
type FlowRunResult struct {
	FlowID   string `json:"flow_id"`
	FlowName string `json:"flow_name"`
	RunID    string `json:"run_id"`
	// Status success / failed / cancelled（与 flow_run_records.status 同口径）。
	Status     string `json:"status"`
	ErrCode    string `json:"err_code,omitempty"` // 取最早失败节点的错误码
	ErrMsg     string `json:"err_msg,omitempty"`
	Result     string `json:"result,omitempty"` // 最终输出（result 节点 / return 收集到的内容）
	DurationMs int64  `json:"duration_ms"`
	// Nodes 执行过的节点，按开始时间升序。成功的也在里面 —— 失败时靠它看「跑到哪一步了」。
	Nodes     []FlowNodeTrace `json:"nodes"`
	Truncated bool            `json:"truncated,omitempty"` // Result / 节点日志被截断过
}

// FlowNodeTrace 单个节点的执行结果。
type FlowNodeTrace struct {
	NodeID     string `json:"node_id"`
	Type       string `json:"type,omitempty"`
	Status     string `json:"status"` // success / failed / skipped / cancelled
	ErrCode    string `json:"err_code,omitempty"`
	ErrMsg     string `json:"err_msg,omitempty"`
	DurationMs int64  `json:"duration_ms"`
	// Logs / ErrStack / PostError 只在非成功节点上给（脚本 stderr、traceback、变量取值报错
	// 这些排查信息）；成功节点的日志多数是「执行成功」这类噪音，回传只是浪费上下文。
	Logs string `json:"logs,omitempty"`
	// ErrStack 比 ErrMsg 长得多但对定位最有用：lua / python / js 的 traceback 只在这里。
	ErrStack string `json:"err_stack,omitempty"`
	// PostError 后处理脚本（post）的错误：不影响节点主状态，但同样是失败原因之一。
	PostError  string `json:"post_error,omitempty"`
	RetryCount int    `json:"retry_count,omitempty"`
}

// FlowRunDetail read_flow_run 的返回体：一次运行记录 + 节点记录（含节点级变量）。
//
// 与 FlowRunResult 的分工：那是「刚跑完的结论」，这是「事后的取证」—— 变量、错误栈、
// 日志都在库里，运行结束后照样可查，而且不限于 run_flow 跑的那次（前端 debug 跑、
// 别的 agent 调起来的，都有 run_id）。
type FlowRunDetail struct {
	FlowID     string `json:"flow_id"`
	RunID      string `json:"run_id"`
	Status     string `json:"status"`
	RunType    string `json:"run_type,omitempty"` // call / debug / task
	ErrMsg     string `json:"err_msg,omitempty"`
	Result     string `json:"result,omitempty"`
	DurationMs int64  `json:"duration_ms"`
	StartedAt  string `json:"started_at,omitempty"`
	FinishedAt string `json:"finished_at,omitempty"`
	// RunArgs / FlowVars 是运行时入参与流程变量快照（解码成对象，直接可读）
	RunArgs  any `json:"run_args,omitempty"`
	FlowVars any `json:"flow_vars,omitempty"`
	// NodeTotal 记录里的节点总数；Nodes 可能因 only_failed / nodes 过滤而更少
	NodeTotal int           `json:"node_total"`
	Nodes     []FlowRunNode `json:"nodes"`
	Truncated bool          `json:"truncated,omitempty"`
}

// FlowRunNode 一条节点运行记录。
type FlowRunNode struct {
	NodeID      string `json:"node_id"`
	Type        string `json:"type,omitempty"`
	Status      string `json:"status"`
	ErrCode     string `json:"err_code,omitempty"`
	ErrMsg      string `json:"err_msg,omitempty"`
	ErrStack    string `json:"err_stack,omitempty"`
	RetryCount  int    `json:"retry_count,omitempty"`
	ErrorAction string `json:"error_action,omitempty"`
	DurationMs  int64  `json:"duration_ms"`
	// 变量三兄弟取自落库的 jsonb，解码成对象；超限时退化成 {_truncated, _bytes, keys} 摘要。
	InputVars  any    `json:"input_vars,omitempty"`
	OutputVars any    `json:"output_vars,omitempty"`
	LocalVars  any    `json:"local_vars,omitempty"`
	Extra      any    `json:"extra,omitempty"` // 节点类型特有的结果（HTTP 状态、LLM token 用量等）
	Logs       string `json:"logs,omitempty"`
	// Truncated 落库时就被截断过 —— 再看也看不到全量，别据此断言「值就只有这些」。
	Truncated bool `json:"truncated,omitempty"`
}

// FlowsManager 工作流的注册与查询（由 service/agent 注入，内部自带租户 schema 与仓储）。
// 与 AgentManager 同一套惯例：身份与 schema 固化在实现里，工具层传不进来。
type FlowsManager interface {
	Exists(ctx context.Context, name string) (bool, error)
	Create(ctx context.Context, in FlowInput) (string, error)
	List(ctx context.Context) ([]FlowInfo, error)
	// Get 按 id 或 name 取；id 优先，两者都为空返回错误。
	Get(ctx context.Context, id, name string) (*FlowDetail, error)
	// UpdateDiagram 只替换 diagram（create_edge / update_edge / delete_edge 用）：
	// 名称、描述、限额、断点都不动。id 优先、name 兜底；不存在返回 (nil, nil)。
	UpdateDiagram(ctx context.Context, id, name string, diagram json.RawMessage) (*FlowInfo, error)
	// UpdateArguments 只替换入口参数声明（update_flow_arguments 用）：
	// 名称、描述、diagram、限额都不动。id 优先、name 兜底；不存在返回 (nil, nil)。
	UpdateArguments(ctx context.Context, id, name string, arguments json.RawMessage) (*FlowInfo, error)
	// Delete 按 id 或 name 删除，返回被删掉的那一行（供调用方回显）；不存在返回 (nil, nil)。
	Delete(ctx context.Context, id, name string) (*FlowInfo, error)
	// RunTest 试运行一个已保存的工作流（真实执行）。
	//
	// callDepth 是发起方 agent 的当前栈深度，执行时以 +1 下传 —— 流程里若有 agent
	// 节点，agent → flow → agent 这条环靠它收敛。sessionID 非空时流程落在调用方同一会话场地。
	// followDrafts 决定这次运行调起的子流程读草稿还是正式版本（与编辑页调试器的同名开关同一语义）。
	// 不存在返回 (nil, nil)，由调用方报「不存在」。
	RunTest(ctx context.Context, ref string, args map[string]any, sessionID string, callDepth int, followDrafts bool) (*FlowRunResult, error)
	// GetRun 读一次运行记录（run + 全部节点记录，含变量与错误栈）。
	// 不存在返回 (nil, nil)，由调用方报「不存在」。
	GetRun(ctx context.Context, runID string) (*FlowRunDetail, error)
}

// flowStepTypes 允许出现在 steps 里的节点类型，与 executors.RegisterExecutor 的注册表一一对应。
//
// 排除的是前端结构/视觉节点：start（工具自动生成 id=1）、entry（容器入口，前端生成）、
// comment / knowledge（没有执行器，连进链路会报 no executor registered）、
// layout（占位节点，runner 会跳过并直通，但工具本身不创建它）。
//
// 「允许创建」不等于「线性串起来就能跑」：branch / intent 要多条带 handle 的出边，
// loop / process / function 要子节点，wait 要同一条链上的 fork，call 的 target 要指向
// 已存在的 function 节点 id。这些结构缺口不阻断创建，但会以 warnings 回给调用方，
// 让人知道去哪补（前端画布）。
var flowStepTypes = map[string]bool{
	// 执行型：单入单出，线性直连即可跑
	"llm": true, "code": true, "http": true, "mcp": true, "rpc": true,
	"assign": true, "memory": true, "result": true, "progress": true,
	// 控制流 / 容器：能建，但要额外结构（见 flowStepHint）
	"branch": true, "intent": true, "loop": true, "process": true, "function": true,
	"call": true, "wait": true, "fork": true,
	// 控制信号：会提前结束流程，通常不该出现在主链上
	"break": true, "continue": true, "return": true, "exit": true,
	// 视觉节点：layout 可以被 runner 跳过并直通，适合当「折行拐点 / 分组锚点」，
	// 把主链的高度用起来；comment / knowledge 没有执行器且 knowledge 会报错，不开放。
	"layout": true,
}

// flowStepHint 指出「线性图里这个节点为什么跑不出效果」，空串 = 无需提示。
// 只做提示不做拦截：节点本身是合法的，只是缺结构，用户可以在画布上补齐。
func flowStepHint(typ, id string, idx int) string {
	switch typ {
	case "branch", "intent":
		return fmt.Sprintf("step %d (%s, id=%s) has no branches: it only has the main-chain outgoing edge, and the executor cannot route by source<sub-branch id>, "+
			"so every condition is skipped (the default branch is not taken either). For conditional routing, write branches on this step: each key maps to "+
			"config.children[].id and its value is that branch's own step chain; use key=\"default\" for the unconditional fallback", idx+1, typ, id)
	case "loop", "process", "function":
		return fmt.Sprintf("step %d (%s, id=%s) is a container node and a linear graph has no children — it spins empty at run time; "+
			"nested nodes can only be dragged into the container (entry included) on the front-end canvas", idx+1, typ, id)
	case "wait":
		return fmt.Sprintf("step %d (wait, id=%s) waits on a fork group, but a linear graph has no group members; "+
			"wait's config.fork_node_id and each member's config.concurrency have to be set explicitly", idx+1, id)
	case "break", "continue", "return", "exit":
		return fmt.Sprintf("step %d (%s, id=%s) is a control signal: on the main chain it ends or jumps the rest of the flow right at this step — "+
			"confirm this is the effect you want", idx+1, typ, id)
	}
	return ""
}

type diagramNode struct {
	ID       string         `json:"id"`
	Type     string         `json:"type"`
	Position map[string]int `json:"position"`
	// ParentNode 只出现在容器子节点上（loop / process / function 的内层）
	ParentNode string `json:"parent_node,omitempty"`
	// Extent 让子节点在画布上被限制在父节点范围内，取值与前端 nodeFactory 一致
	Extent map[string]any `json:"extent,omitempty"`
	Data   map[string]any `json:"data"`
}

type diagramEdge struct {
	ID           string  `json:"id"`
	Source       string  `json:"source"`
	Target       string  `json:"target"`
	SourceHandle *string `json:"source_handle"`
	TargetHandle *string `json:"target_handle"`
	// keepSourceHandle 分支子链的首条边：source_handle 是语义 key
	// （source<config.children[].id>right，执行器靠前缀匹配找出边），排版后回填 handle 时不能改。
	// 非导出字段，不进 JSON。
	keepSourceHandle bool
}

// diagramBuild 建图结果：diagram 原文 + 结构缺口提示 + 节点清单。
//
// 节点清单单独回给 create_flow：模型拿到 id 才能核对 source_ref / call target / 分支
// handle 落在预期节点上（目前没有 update_flow，建错了只能删掉重建）。
type diagramBuild struct {
	Diagram  json.RawMessage
	Warnings []string
	Nodes    []diagramNode
}

// buildFlowDiagram 把步骤列表组装成 diagram：顶层一条主链，branch / intent 按 Branches
// 从自己身上拉出带 handle 的子链。坐标由 layoutDiagram 统一排版（主链横向、分支各占一行），
// 前端打开后可自由拖动，位置不影响执行。
//
// 节点 id 按建图顺序分配：start 恒为 1，之后每建一个节点 +1（先建父节点，再按 branches
// 声明顺序建它的子链）。同一份入参建出的 id 与边顺序都是确定的。
//
// 返回值里的 Warnings 是结构缺口提示（例如容器没有子节点），不阻断创建。
func buildFlowDiagram(steps []FlowStep, concurrency string) (*diagramBuild, error) {
	if len(steps) == 0 {
		return nil, errors.New("steps must not be empty: a workflow needs at least one step")
	}
	b := &diagramBuilder{concurrency: concurrency}
	b.addStart()
	if _, err := b.emitChain(steps, "1", "", 0, ""); err != nil {
		return nil, err
	}
	// 排版：模型给了全套坐标就按它的来（它比任何规则都清楚这张图想表达什么），
	// 没给或只给了一半才走代码排版（主链横向、分支纵向、容器按内容撑开）。
	if !b.applyModelPositions() {
		b.layoutDiagram()
	}
	// 坐标定稿后再落 handle：主链同一行 → right→left，分支子链在右下方 → right→top。
	// 分支子链的 source_handle 是语义 key，回填时保持原样。
	b.fillEdgeHandles()
	b.resolveSourceRefs()
	b.resolveNodeLabels()
	b.warnCalls()

	raw, err := json.Marshal(map[string]any{"nodes": b.nodes, "edges": b.edges})
	if err != nil {
		return nil, err
	}
	return &diagramBuild{Diagram: raw, Warnings: b.warnings, Nodes: b.nodes}, nil
}

// ===== 并发策略（默认关闭）=====
//
// 并发是「用户明确要求才开」的能力：默认生成的定义里一律不写 config.concurrency，
// 因为并发节点只执行自己、不沿出边继续（要配 wait 汇合），语义与串行完全不同，
// 模型照文档示例顺手抄一行 concurrency 就会把整条流程改坏。
//
// 两种形态由调用方（模型按用户诉求判断后）显式声明：
//   - mono  单体并发：单个节点内部并发跑多份数据，不组队（{"mode":"mono"}）
//   - group 并发组并发：多个节点同时跑、由 wait 汇合（{"mode":"fork","fork_node_id":...}）
const (
	concurrencyOff   = "off"
	concurrencyMono  = "mono"
	concurrencyGroup = "group"
)

// concurrencyTypes 画布上真的能配并发的类型（CodeNode / HttpNode / CallNode 带并发下拉）。
// 其它节点执行器虽然也认 config.concurrency，但前端改不了，所以不主动提示。
var concurrencyTypes = map[string]bool{"code": true, "http": true, "call": true}

// concurrencyParamSchema 节点工具（create_node / insert_node）共用的并发参数。
var concurrencyParamSchema = map[string]any{
	"type": "string",
	"enum": []any{"off", "mono", "group"},
	"description": "Optional: concurrency shape, **omit for off**, pass it only when the user explicitly asks for concurrency. " +
		"mono = single-node concurrency (this node processes several items concurrently; config becomes {\"mode\":\"mono\"}); " +
		"group = concurrency group (this node joins a fork group; config becomes " +
		"{\"mode\":\"fork\",\"fork_node_id\":\"<fork node id>\"}). " +
		"When omitted, config.concurrency is stripped and a warning is returned.",
}

// parseConcurrencyPolicy 解析并发参数：off（默认）/ mono / group。
func parseConcurrencyPolicy(args map[string]any) (string, error) {
	raw := strings.ToLower(strings.TrimSpace(argString(args, "concurrency")))
	switch raw {
	case "", "off", "none", "serial":
		return concurrencyOff, nil
	case "mono", "single":
		return concurrencyMono, nil
	case "group", "fork", "fork-group", "fork_group":
		return concurrencyGroup, nil
	}
	return "", fmt.Errorf("concurrency supports off / mono / group only, got %q"+
		" (off = off by default, mono = single-node concurrency, group = concurrency group)", raw)
}

// concurrencyShape 并发形态在 config 里的写法（回显用）。
func concurrencyShape(policy string) string {
	switch policy {
	case concurrencyMono:
		return "{\"mode\":\"mono\"}"
	case concurrencyGroup:
		return "{\"mode\":\"fork\",\"fork_node_id\":\"<fork node id>\"}"
	}
	return ""
}

// applyConcurrencyPolicy 按策略处理一个节点的 config.concurrency，返回给调用方的回显（空串 = 没动）。
//
// where 用来定位节点（create_flow 传「步骤 3」、节点工具传「新节点」）。
// 容器（loop / process / function）永远不带这一项：concurrency 是「组内成员」的属性。
func applyConcurrencyPolicy(cfg map[string]any, typ, policy, where string) string {
	if cfg == nil {
		return ""
	}
	raw, ok := cfg["concurrency"]
	if !ok || raw == nil {
		// 声明了并发形态却没写字段：不提示的话就是「以为开了、其实串行」。
		if policy != concurrencyOff && concurrencyTypes[typ] {
			return fmt.Sprintf("%s (%s) has no config.concurrency: concurrency shape %s was declared, "+
				"so write %s into the node config", where, typ, policy, concurrencyShape(policy))
		}
		return ""
	}
	cc, _ := raw.(map[string]any)
	if len(cc) == 0 {
		delete(cfg, "concurrency")
		return ""
	}
	mode, _ := cc["mode"].(string)
	forkID, _ := cc["fork_node_id"].(string)
	forkID = strings.TrimSpace(forkID)

	if containerTypes[typ] {
		delete(cfg, "concurrency")
		return fmt.Sprintf("%s (%s): config.concurrency ignored — loop / process containers are serial by default, "+
			"concurrency belongs on the member nodes inside the group", where, typ)
	}
	switch policy {
	case concurrencyMono:
		if mode == "mono" && forkID == "" {
			return ""
		}
		delete(cfg, "concurrency")
		return fmt.Sprintf("%s (%s): config.concurrency ignored — only single-node concurrency is on this time "+
			"({\"mode\":\"mono\"}); declare concurrency=group explicitly for a concurrency group", where, typ)
	case concurrencyGroup:
		if mode == "fork" && forkID != "" {
			return ""
		}
		delete(cfg, "concurrency")
		return fmt.Sprintf("%s (%s): config.concurrency ignored — a concurrency group is on this time "+
			"({\"mode\":\"fork\",\"fork_node_id\":\"<fork node>\"}); declare concurrency=mono explicitly for single-node concurrency",
			where, typ)
	default:
		delete(cfg, "concurrency")
		return fmt.Sprintf("%s (%s): config.concurrency ignored — concurrency is off by default and is only generated "+
			"when the user explicitly asks for it (single-node concurrency=mono, concurrency group=group)", where, typ)
	}
}

// diagramBuilder 建图累加器：管 id 分配、边编号与告警。
type diagramBuilder struct {
	nodes    []diagramNode
	edges    []diagramEdge
	seq      int
	warnings []string
	// concurrency 并发策略：off（默认）/ mono / group，建图时统一按它处理每个节点的
	// config.concurrency（见 applyConcurrencyPolicy）。
	concurrency string
	// hasBranches 是否建过分支子链：决定 call 的 id 提示口径（k+1 只在纯线性图里成立）
	hasBranches bool
	// explicit 模型显式给了坐标的顶层节点（step.position）。有值时坐标由模型决定，
	// 要求「全给」，缺一个就整体退回自动排版。
	explicit map[string]map[string]int
}

func (b *diagramBuilder) addStart() {
	b.seq = 1
	b.nodes = append(b.nodes, diagramNode{
		ID:       "1",
		Type:     "start",
		Position: map[string]int{"x": 0, "y": 0},
		Data:     map[string]any{"label": "Start"},
	})
}

// addNode 建一个顶层节点并返回 id。depth 只用于占位坐标的初值（子链先右移一列），
// 最终坐标由 layoutDiagram 决定。
func (b *diagramBuilder) addNode(typ, label string, cfg map[string]any, depth int) string {
	return b.addNodeIn(typ, label, cfg, depth, "")
}

// addNodeIn 建节点：parentID 非空时作为容器子节点，坐标相对父节点并带 extent。
//
// 这里给的坐标只是占位（保证 JSON 里 position 字段存在），真正的排版在
// layoutDiagram 里统一算 —— 建图阶段拿不到整张图的结构，算不出好看的坐标。
func (b *diagramBuilder) addNodeIn(typ, label string, cfg map[string]any, depth int, parentID string) string {
	b.seq++
	id := strconv.Itoa(b.seq)
	data := map[string]any{"label": label}
	if len(cfg) > 0 {
		data["config"] = cfg
	}
	n := diagramNode{ID: id, Type: typ, Data: data}
	if parentID == "" {
		n.Position = map[string]int{"x": depth * 280, "y": (b.seq - 1) * 140}
	} else {
		n.Position = map[string]int{"x": 60, "y": 40 + b.childCount(parentID)*140}
		n.ParentNode = parentID
		n.Extent = childExtent()
	}
	b.nodes = append(b.nodes, n)
	return id
}

// childCount 容器里已有多少子节点（决定新子节点的纵向落点）。
func (b *diagramBuilder) childCount(parentID string) int {
	n := 0
	for i := range b.nodes {
		if b.nodes[i].ParentNode == parentID {
			n++
		}
	}
	return n
}

// addEdge 连一条边，先给一个占位 handle。
//
// 真正的 handle 在排版完成后由 fillEdgeHandles 按两个节点的最终坐标统一回填 ——
// 建图阶段坐标还是占位值，按它算端点只会算出与画布走向不符的值。
// handle 非空表示这是分支子链的首条边：它的 source_handle 是语义 key，回填时保持不动。
func (b *diagramBuilder) addEdge(from, to, handle string) {
	if from == "" || to == "" {
		return
	}
	e := diagramEdge{ID: fmt.Sprintf("e%d", len(b.edges)+1), Source: from, Target: to}
	h := "source" + from + "right"
	if handle != "" {
		h = handle
		e.keepSourceHandle = true
	}
	e.SourceHandle = &h
	th := "target" + to + "left"
	e.TargetHandle = &th
	b.edges = append(b.edges, e)
}

// fillEdgeHandles 排版完成后按最终坐标把所有边的 handle 补齐 / 校正。
//
// 之前只给分支子链写 source_handle，主链边落库就是 null —— 运行期不影响（主链只走
// OutEdges[0]），但 insert_node 会拒绝、update_edge 得先补、模型看到「一半有 handle
// 一半没有」也会糊涂。这里统一成：除了分支子链的语义 key，其余一律按位置算。
func (b *diagramBuilder) fillEdgeHandles() {
	for i := range b.edges {
		e := &b.edges[i]
		si, di := b.nodeIndex(e.Source), b.nodeIndex(e.Target)
		if si < 0 || di < 0 {
			continue
		}
		s, d := b.nodes[si], b.nodes[di]
		srcPos, dstPos, _ := suggestHandlePositions(
			s.Type, s.Position["x"], s.Position["y"],
			d.Type, d.Position["x"], d.Position["y"])
		if !e.keepSourceHandle {
			h := "source" + e.Source + srcPos
			e.SourceHandle = &h
		}
		th := "target" + e.Target + dstPos
		e.TargetHandle = &th
	}
}

// nodeIndex 按 id 找节点下标（建边回填、类型判断共用）。
func (b *diagramBuilder) nodeIndex(id string) int {
	for i := range b.nodes {
		if b.nodes[i].ID == id {
			return i
		}
	}
	return -1
}

// nodeType 按 id 找已建节点的类型。
func (b *diagramBuilder) nodeType(id string) string {
	if i := b.nodeIndex(id); i >= 0 {
		return b.nodes[i].Type
	}
	return ""
}

// emitChain 把一条步骤链串成节点与边，返回链尾节点 id（空链返回 from）。
//
// handle 非空表示这条链接在分支节点的某个出边上（source<childID>right），
// 此时首节点只经由该边可达。
// parentID 非空表示这条链在容器内部（loop / process / function），建出来的节点都带 parent_node。
func (b *diagramBuilder) emitChain(steps []FlowStep, from, handle string, depth int, parentID string) (string, error) {
	prev, prevHandle := from, handle
	for i, st := range steps {
		typ := strings.ToLower(strings.TrimSpace(st.Type))
		if !flowStepTypes[typ] {
			return "", fmt.Errorf("unsupported step type %q. "+
				"Available: llm / code / http / mcp / rpc / branch / intent / loop / process / function / call / "+
				"assign / memory / wait / fork / result / progress / break / continue / return / exit / layout; "+
				"start is generated by the tool, entry is generated by the front end, knowledge has no executor and cannot be created, "+
				"comment can only be created with create_node (pure annotation, no wiring), layout can be created and the runner skips it (pass-through)", st.Type)
		}
		if len(st.Branches) > 0 {
			switch typ {
			case "branch", "intent":
			default:
				return "", fmt.Errorf("step %d (%s) does not support branches: only branch / intent have sub-branches", i+1, typ)
			}
			if i != len(steps)-1 {
				return "", fmt.Errorf("step %d (%s) declares branches and must be the last step of its chain: "+
					"put the steps after the branch into branches[].steps (a branch only takes its default edge once it runs)", i+1, typ)
			}
		}
		// config 形状校验：类型写错直接拒绝，缺必填键只回 warning
		// （工具允许先给最小配置、到画布补齐，与 warnings 的定位一致）。
		cfgWarnings, cfgErr := validateStepConfig(typ, st.Config)
		if cfgErr != nil {
			return "", cfgErr
		}
		b.warnings = append(b.warnings, cfgWarnings...)

		// 并发默认关闭：按调用方声明的策略处理这一节点的 concurrency，
		// 没声明（off）就一律摘掉并回显；容器无论什么策略都不带这一项。
		if w := applyConcurrencyPolicy(st.Config, typ, b.concurrency, fmt.Sprintf("step %d", i+1)); w != "" {
			b.warnings = append(b.warnings, w)
		}

		label := strings.TrimSpace(st.Label)
		if label == "" {
			label = b.uniqueLabel(typ)
		} else if owner := b.labelOwner(label); owner != "" {
			return "", fmt.Errorf(
				"step %d's label %q collides with node %s: node labels must be unique across the flow "+
					"(source_ref / call.target / fork_node_id can all be written as labels, and a duplicate points at the wrong node)",
				i+1, label, owner)
		}
		id := b.addNodeIn(typ, label, st.Config, depth, parentID)
		if st.Position != nil && parentID == "" {
			if b.explicit == nil {
				b.explicit = make(map[string]map[string]int)
			}
			b.explicit[id] = st.Position
		}
		b.addEdge(prev, id, prevHandle)
		prev, prevHandle = id, ""

		if len(st.Children) > 0 {
			switch typ {
			case "loop", "process", "function":
			default:
				return "", fmt.Errorf("step %d (%s) does not support children: only loop / process / function can hold child nodes", i+1, typ)
			}
			if err := b.emitContainerChildren(id, st.Children, depth); err != nil {
				return "", err
			}
		}
		if len(st.Branches) > 0 {
			b.hasBranches = true
			if err := b.emitBranchChains(typ, id, st, depth, parentID); err != nil {
				return "", err
			}
			return id, nil
		}
		// 容器已经带了子链，就别再报「没有子节点 —— 运行时空转」
		if len(st.Children) == 0 {
			if hint := flowStepHint(typ, id, i); hint != "" {
				b.warnings = append(b.warnings, hint)
			}
		}
	}
	return prev, nil
}

// emitContainerChildren 在容器里建 entry + 子链。
//
// 执行器从 parent_node==容器 id 的 entry 节点起步（executors.findEntryNode），没有它就
// 「成功但什么都没跑」—— 所以 entry 必须由工具补，不能指望调用方记得。
func (b *diagramBuilder) emitContainerChildren(containerID string, children []FlowStep, depth int) error {
	entryID := b.addNodeIn("entry", b.uniqueLabel("Entry"), nil, depth+1, containerID)
	if _, err := b.emitChain(children, entryID, "", depth+1, containerID); err != nil {
		return err
	}
	// 容器子链也让 id 不再线性（第 k 步 ≠ k+1），call 的提示口径要跟着改。
	b.hasBranches = true
	return nil
}

// emitBranchChains 按 Branches 从分支节点拉出各条子链。
//
// handle 约定与执行器一致（executors.findBranchEdge 是前缀匹配）：
//
//	普通子分支：source<childID>right —— childID = config.children[].id
//	默认分支  ：source<节点id>right  —— 执行器用节点自身 id 找 default 边
//
// 默认分支可以不给：执行器找不到 default 边时只记一条日志，条件全不命中也算正常结束。
func (b *diagramBuilder) emitBranchChains(typ, nodeID string, st FlowStep, depth int, parentID string) error {
	valid := make(map[string]bool, len(st.Branches)+1)
	valid["default"] = true
	childIDs := make([]string, 0)
	if children, ok := st.Config["children"].([]any); ok {
		for _, c := range children {
			id, _ := objectOf(c)["id"].(string)
			if id = strings.TrimSpace(id); id != "" {
				valid[id] = true
				childIDs = append(childIDs, id)
			}
		}
	}

	declared := make(map[string]bool, len(st.Branches))
	for _, br := range st.Branches {
		key := strings.TrimSpace(br.Key)
		if !valid[key] {
			return fmt.Errorf("branches on %s (id=%s): key=%q is neither an id from config.children nor default; available: %s",
				typ, nodeID, br.Key, strings.Join(append(childIDs, "default"), " / "))
		}
		declared[key] = true
		handle := "source" + key + "right"
		if key == "default" {
			handle = "source" + nodeID + "right"
		}
		if _, err := b.emitChain(br.Steps, nodeID, handle, depth+1, parentID); err != nil {
			return err
		}
	}

	// 声明了 branches 却漏掉某个子分支：条件命中后无路可走（执行器只记「没有出边，跳过」）。
	// 只提示不拦 —— 允许先建结构、后补子链。
	for _, id := range childIDs {
		if !declared[id] {
			b.warnings = append(b.warnings, fmt.Sprintf(
				"branch (id=%s): sub-branch %q has no outgoing edge, so it is skipped once its condition is met; "+
					"either add steps under branches or remove it from config.children", nodeID, id))
		}
	}
	return nil
}

// ---------- 排版 ----------
//
// 建图只负责结构与 id，坐标统一在 layoutDiagram 里算：
//
//	主链从左往右（同排节点 y 相同），一行放不下就在行尾折行 —— 不用一条竖线拉到几百像素；
//	branch / intent 的每条分支单独占一行，排在分支节点下方（right → top 进入子链）；
//	折行处与分支首节点的入边改走 top，让线条顺着高度走而不是横穿节点；
//	容器（loop / process / function）的子节点纵向排列，容器尺寸按内容撑开并写进 config.size。
//
// 尺寸只能估算（后端没有渲染信息），按前端实测值取：叶子高 32、branch/容器宽 300、
// 标签按每字 13px 估宽。宁可留一点余量，也不要重叠。
const (
	layoutRowMaxWidth = 1500 // 一行最多排多宽，超过就折行
	layoutColGap      = 80   // 同排相邻节点的水平间距
	layoutRowGap      = 120  // 折行后的垂直间距（本行分支占用的高度另算）
	layoutBranchGap   = 60   // 分支首节点距分支节点底部的距离
	layoutBranchStep  = 40   // 同一分支节点下，两条分支之间的额外间隔
	layoutOrphanGap   = 120  // 游离节点（没有入边也不会从 start 走到）接在图底部的间距
	// 起笔留白：画布视口固定在 (0,0)，节点贴着左上角会显得局促，
	// 与前端「新建流程」默认把 start 放在 (100,50) 对齐
	layoutMarginX = 100
	layoutMarginY = 50

	// 容器内布局：坐标相对父容器，与前端 nodeFactory 的 37px 上边距对齐
	childBaseX = 20
	childBaseY = 37
	childGap   = 24
)

// nodeSize 估算节点在画布上的尺寸（宽, 高）。
func nodeSize(n diagramNode) (int, int) {
	label, _ := n.Data["label"].(string)
	cfg, _ := n.Data["config"].(map[string]any)
	switch n.Type {
	case "start":
		return 120, 40
	case "branch", "intent":
		rows := 1
		if children, ok := cfg["children"].([]any); ok && len(children) > 0 {
			rows = len(children)
		}
		return 300, 44 + 28*rows
	case "loop", "process", "function":
		return 300, 100
	case "layout":
		// 32px 的小圆点，名字 hover 才显示，本身几乎不占地方
		return 60, 32
	}
	w := 56 + 13*len([]rune(label))
	if w < 120 {
		w = 120
	}
	if w > 280 {
		w = 280
	}
	h := 32
	// 开了并发的节点（config.concurrency = {mode, fork_node_id}）会多出一行
	// 「并发控制 + 下拉 + 取消」：宽度顶到 280、高度约翻倍。
	// 漏算这一块，排版会和邻居重叠、重叠检测也会误判。
	if hasConcurrency(cfg) {
		if w < 280 {
			w = 280
		}
		h = 64
	}
	return w, h
}

// hasConcurrency 节点是否开了并发（config.concurrency 非空）。
func hasConcurrency(cfg map[string]any) bool {
	c, ok := cfg["concurrency"].(map[string]any)
	return ok && len(c) > 0
}

// diagramLayout 排版上下文：图的邻接、已落位集合与最终尺寸。
type diagramLayout struct {
	b     *diagramBuilder
	byID  map[string]int
	out   map[string][]int // 节点 id 的出边下标
	topTo map[string]bool  // 入边要走 top 的节点（折行首节点 / 分支首节点）
	sizes map[string][2]int
	taken map[string]bool
}

// newDiagramLayout 建排版上下文（邻接表按出边索引，便于直接改写 target_handle）。
func newDiagramLayout(b *diagramBuilder) *diagramLayout {
	l := &diagramLayout{
		b:     b,
		byID:  make(map[string]int, len(b.nodes)),
		out:   make(map[string][]int, len(b.edges)),
		topTo: make(map[string]bool, len(b.nodes)),
		sizes: make(map[string][2]int, len(b.nodes)),
		taken: make(map[string]bool, len(b.nodes)),
	}
	for i := range b.nodes {
		l.byID[b.nodes[i].ID] = i
	}
	for i := range b.edges {
		l.out[b.edges[i].Source] = append(l.out[b.edges[i].Source], i)
	}
	return l
}

// applyModelPositions 用模型给的坐标排版；返回 true 表示已按其坐标落位。
//
// 只接受「顶层节点全给」：混着代码坐标会互相压住，而且模型也说不清哪块是自己排的。
// 缺一个就整体退回自动排版，并在 warnings 里点名缺了谁 —— 让模型下轮补齐，而不是猜。
func (b *diagramBuilder) applyModelPositions() bool {
	if len(b.explicit) == 0 {
		return false
	}
	missing := make([]string, 0, 2)
	for i := range b.nodes {
		n := &b.nodes[i]
		if n.ParentNode != "" || n.ID == "1" {
			continue
		}
		if _, ok := b.explicit[n.ID]; !ok {
			missing = append(missing, n.ID)
		}
	}
	if len(missing) > 0 {
		b.warnings = append(b.warnings, fmt.Sprintf(
			"%d top-level nodes have no position (%s): coordinates must be given for all of them or none — "+
				"a partial set mixed with auto layout overlaps itself, so auto layout is used this time", len(missing), strings.Join(missing, ", ")))
		return false
	}

	for i := range b.nodes {
		if pos, ok := b.explicit[b.nodes[i].ID]; ok {
			b.nodes[i].Position = pos
		}
	}
	// start 由工具生成，模型给不了它坐标：摆在首个节点左侧 200px 同行
	if first := b.firstPositionedNode(); first != "" {
		p := b.explicit[first]
		x := p["x"] - 200
		if x < 0 {
			x = 0
		}
		b.setNodePosition("1", map[string]int{"x": x, "y": p["y"]})
	}
	// 容器内部仍由工具排（相对坐标）+ 写 config.size
	b.layoutContainersOnly()
	b.warnOverlaps()
	return true
}

// firstPositionedNode 按建图顺序找第一个给了坐标的顶层节点（start 除外）。
func (b *diagramBuilder) firstPositionedNode() string {
	for i := range b.nodes {
		n := &b.nodes[i]
		if n.ParentNode != "" || n.ID == "1" {
			continue
		}
		if _, ok := b.explicit[n.ID]; ok {
			return n.ID
		}
	}
	return ""
}

// setNodePosition 改一个已存在节点的坐标（找不到就忽略）。
func (b *diagramBuilder) setNodePosition(id string, pos map[string]int) {
	for i := range b.nodes {
		if b.nodes[i].ID == id {
			b.nodes[i].Position = pos
			return
		}
	}
}

// warnOverlaps 检查顶层节点是否互相压住：只提示、不挪动 ——
// 坐标是模型给的，挪了会破坏它自己的构图意图，让它决定怎么让步。
func (b *diagramBuilder) warnOverlaps() {
	l := newDiagramLayout(b)
	type box struct {
		id   string
		x, y int
		w, h int
	}
	boxes := make([]box, 0, len(b.nodes))
	for i := range l.b.nodes {
		n := &l.b.nodes[i]
		if n.ParentNode != "" {
			continue
		}
		w, h := l.sizeOf(n.ID)
		boxes = append(boxes, box{n.ID, n.Position["x"], n.Position["y"], w, h})
	}
	for i := 0; i < len(boxes); i++ {
		for j := i + 1; j < len(boxes); j++ {
			a, c := boxes[i], boxes[j]
			if a.x < c.x+c.w && c.x < a.x+a.w && a.y < c.y+c.h && c.y < a.y+a.h {
				b.warnings = append(b.warnings, fmt.Sprintf(
					"nodes %s and %s overlap: move one with update_node's position (x gap ≥ 80, y gap ≥ 40 suggested)",
					a.id, c.id))
			}
		}
	}
}

// layoutContainersOnly 只排容器内部（模型自己排顶层坐标时用）：
// 子节点按内容纵向排列，容器尺寸按包围盒写进 config.size。
func (b *diagramBuilder) layoutContainersOnly() {
	l := newDiagramLayout(b)
	for i := range b.nodes {
		n := &b.nodes[i]
		if n.ParentNode == "" && containerTypes[n.Type] {
			l.containerChildren(n.ID)
		}
	}
}

// layoutDiagram 统一排版整张图，并据此改写需要走 top 的入边。
func (b *diagramBuilder) layoutDiagram() {
	l := newDiagramLayout(b)
	start := "1"
	if _, ok := l.byID[start]; !ok {
		start = l.firstRoot()
	}
	if start != "" {
		l.chain(start, layoutMarginX, layoutMarginY, layoutRowMaxWidth)
	}
	l.placeOrphans()
	for i := range b.edges {
		if !l.topTo[b.edges[i].Target] {
			continue
		}
		h := "target" + b.edges[i].Target + "top"
		b.edges[i].TargetHandle = &h
	}
}

// firstRoot 找没有入边的顶层节点（start 不在时的兜底）。
func (l *diagramLayout) firstRoot() string {
	in := make(map[string]bool, len(l.b.edges))
	for _, e := range l.b.edges {
		in[e.Target] = true
	}
	for i := range l.b.nodes {
		n := &l.b.nodes[i]
		if n.ParentNode == "" && !in[n.ID] {
			return n.ID
		}
	}
	return ""
}

// sizeOf 取节点的最终尺寸；容器在 containerChildren 里被撑开后会覆盖这里的估算值。
func (l *diagramLayout) sizeOf(id string) (int, int) {
	if s, ok := l.sizes[id]; ok {
		return s[0], s[1]
	}
	idx, ok := l.byID[id]
	if !ok {
		return 120, 32
	}
	w, h := nodeSize(l.b.nodes[idx])
	l.sizes[id] = [2]int{w, h}
	return w, h
}

// next 返回唯一的后继（0 条或多条出边都算「没有线性后继」）。
func (l *diagramLayout) next(id string) string {
	out := l.out[id]
	if len(out) != 1 {
		return ""
	}
	return l.b.edges[out[0]].Target
}

// chain 把从 startID 起的一条链摆到画布上，返回该链的右边界与底边界。
// x/y 是链首落点，limit 是本行宽度上限（折行后回到 x 起列）。
func (l *diagramLayout) chain(startID string, x, y, limit int) (int, int) {
	cur := startID
	baseX := x
	rowY := y
	branchBottom := y // 本行分支占用的最低点，折行时要让开
	right, bottom := x, y

	for cur != "" {
		idx, ok := l.byID[cur]
		if !ok || l.taken[cur] {
			break
		}
		w, h := l.sizeOf(cur)
		// 折行：一行排不下就换行，并让开本行分支占用的高度
		if cur != startID && x > baseX && x+w > limit {
			rowY = branchBottom + layoutRowGap
			branchBottom = rowY
			x = baseX
			l.topTo[cur] = true
		}

		node := &l.b.nodes[idx]
		node.Position = map[string]int{"x": x, "y": rowY}
		l.taken[cur] = true

		if containerTypes[node.Type] {
			l.containerChildren(cur)
			w, h = l.sizeOf(cur)
		}
		if x+w > right {
			right = x + w
		}
		if rowY+h > bottom {
			bottom = rowY + h
		}

		// 分支节点：每条分支单独一行排在下方，主链在这里结束（与 emitChain 的建图口径一致）
		if node.Type == "branch" || node.Type == "intent" {
			cursor := rowY + h + layoutBranchGap
			for _, ei := range l.out[cur] {
				target := l.b.edges[ei].Target
				if l.taken[target] {
					continue
				}
				// 首节点在分支节点右下方：right 出边接到它的 top
				l.topTo[target] = true
				_, bBottom := l.chain(target, x+layoutBranchStep, cursor, limit)
				if bBottom > bottom {
					bottom = bBottom
				}
				cursor = bBottom + layoutBranchStep
			}
			if cursor-layoutBranchStep > branchBottom {
				branchBottom = cursor - layoutBranchStep
			}
			return right, bottom
		}

		next := l.next(cur)
		if next == "" {
			break
		}
		x += w + layoutColGap
		cur = next
	}
	return right, bottom
}

// containerChildren 排版容器内的子节点（相对坐标）并按包围盒撑开容器尺寸。
func (l *diagramLayout) containerChildren(containerID string) {
	children := make([]string, 0, 4)
	for i := range l.b.nodes {
		if l.b.nodes[i].ParentNode == containerID {
			children = append(children, l.b.nodes[i].ID)
		}
	}
	if len(children) == 0 {
		return
	}

	x := childBaseX
	y := childBaseY
	maxW, maxH := 300, 100
	for _, cid := range children {
		idx := l.byID[cid]
		child := &l.b.nodes[idx]
		if containerTypes[child.Type] {
			l.containerChildren(cid)
		}
		w, h := l.sizeOf(cid)
		child.Position = map[string]int{"x": x, "y": y}
		l.taken[cid] = true
		if right := x + w + childBaseX; right > maxW {
			maxW = right
		}
		y += h + childGap
		if bottom := y - childGap + childBaseX; bottom > maxH {
			maxH = bottom
		}
	}

	// 写回 config.size：前端 LoopNode/ProcessNode/FunctionNode 会直接读它设宽高，
	// 不写的话打开画布后会先按默认 300×100 渲染、下一帧才撑开，看起来像抖动。
	node := &l.b.nodes[l.byID[containerID]]
	cfg, _ := node.Data["config"].(map[string]any)
	if cfg == nil {
		cfg = map[string]any{}
		node.Data["config"] = cfg
	}
	cfg["size"] = map[string]any{
		"width":  fmt.Sprintf("%dpx", maxW),
		"height": fmt.Sprintf("%dpx", maxH),
	}
	l.sizes[containerID] = [2]int{maxW, maxH}
}

// placeOrphans 把没从 start 走到的顶层节点接在图底部，避免全叠在 (0,0)。
func (l *diagramLayout) placeOrphans() {
	maxBottom := 0
	for i := range l.b.nodes {
		n := &l.b.nodes[i]
		if n.ParentNode != "" || !l.taken[n.ID] {
			continue
		}
		if _, h := l.sizeOf(n.ID); n.Position["y"]+h > maxBottom {
			maxBottom = n.Position["y"] + h
		}
	}
	y := maxBottom + layoutOrphanGap
	for i := range l.b.nodes {
		n := &l.b.nodes[i]
		if n.ParentNode != "" || l.taken[n.ID] {
			continue
		}
		_, h := l.sizeOf(n.ID)
		n.Position = map[string]int{"x": 0, "y": y}
		l.taken[n.ID] = true
		y += h + layoutRowGap
	}
}

// labelOwner 返回正在使用该 label 的节点 id（"" = 没被占用）。
//
// label 是 source_ref / call.target / fork_node_id 的可写形式，重名会让这些引用指错节点，
// 所以建图过程中每个节点的 label 都必须唯一。
func (b *diagramBuilder) labelOwner(label string) string {
	label = strings.TrimSpace(label)
	if label == "" {
		return ""
	}
	for _, n := range b.nodes {
		if l, _ := n.Data["label"].(string); strings.TrimSpace(l) == label {
			return n.ID
		}
	}
	return ""
}

// uniqueLabel 给**自动生成**的 label 加 -2 / -3 … 直到全图唯一；
// 用户显式写的 label 撞名由 emitChain 直接报错，不静默改名。
func (b *diagramBuilder) uniqueLabel(base string) string {
	base = strings.TrimSpace(base)
	if base == "" {
		base = "Node"
	}
	if b.labelOwner(base) == "" {
		return base
	}
	for i := 2; i <= 999; i++ {
		if candidate := fmt.Sprintf("%s-%d", base, i); b.labelOwner(candidate) == "" {
			return candidate
		}
	}
	return fmt.Sprintf("%s-%d", base, len(b.nodes)+1)
}

// resolveSourceRefs 把 config.inputs 里 source_ref 写成「节点 label」的值换成节点 id。
//
// 模型很难心算 id（分支子链会插在中间，id 不再是「第 k 步 = k+1」），而 label 是它自己
// 刚写的、一定记得住。只在值不匹配任何节点 id 时才按 label 替换：id 优先，
// 避免 label 恰好是数字串时把正确的 id 改坏。
func (b *diagramBuilder) resolveSourceRefs() {
	byLabel := make(map[string]string, len(b.nodes))
	ids := make(map[string]bool, len(b.nodes))
	for _, n := range b.nodes {
		ids[n.ID] = true
		label, _ := n.Data["label"].(string)
		if _, dup := byLabel[label]; label == "" || dup {
			continue // 重名 label 不参与改写：多义时宁可保留原样，也不猜
		}
		byLabel[label] = n.ID
	}
	for _, n := range b.nodes {
		inputs, _ := objectOf(n.Data["config"])["inputs"].(map[string]any)
		for _, v := range inputs {
			in, ok := v.(map[string]any)
			if !ok {
				continue
			}
			ref, _ := in["source_ref"].(string)
			if ref == "" || ids[ref] {
				continue
			}
			if id, ok := byLabel[ref]; ok {
				in["source_ref"] = id
			}
		}
	}
}

// warnCalls call 的 target 必须是同流程内某个 function 节点的 id。
// 纯线性图里第 k 步的 id 就是 k+1，可以直接说；含分支 / 容器子链后 id 不再线性，
// 只能让调用方回读真实 id —— 猜错是运行期 config 错误，代价比多一次 read_flow 高。
func (b *diagramBuilder) warnCalls() {
	for _, n := range b.nodes {
		if n.Type != "call" {
			continue
		}
		if b.hasBranches {
			b.warnings = append(b.warnings, fmt.Sprintf(
				"call (id=%s): target must point at a function node in this flow; this diagram contains branches / container sub-chains, so node ids cannot be derived from the step index — "+
					"call read_flow first to get the real ids", n.ID))
			continue
		}
		b.warnings = append(b.warnings, fmt.Sprintf(
			"call (id=%s): target must be the id of a function node in the same flow (the node id of step k is k+1)", n.ID))
	}
}

// validateStepConfig 校验步骤 config 的形状。
//
// 分工：形状写错（块不是对象、children 不是数组、loop.type 非法）画布也救不回来，直接拒绝；
// 缺必填键只回 warning —— 允许先建最小配置，结构缺口到画布补（与 flowStepHint 的定位一致）。
// 键名沿用各执行器的读取位置，改执行器时这里要同步。
func validateStepConfig(typ string, cfg map[string]any) ([]string, error) {
	if len(cfg) == 0 {
		return nil, nil
	}

	// 各节点的行为块必须是对象
	for _, key := range []string{"code", "http", "llm", "loop", "mcp", "rpc", "branch"} {
		v, exists := cfg[key]
		if !exists {
			continue
		}
		if _, ok := v.(map[string]any); !ok {
			return nil, fmt.Errorf("step %s: config.%s must be an object", typ, key)
		}
	}
	// children 只出现在 branch / intent
	if v, exists := cfg["children"]; exists {
		if _, ok := v.([]any); !ok {
			return nil, fmt.Errorf("step %s: config.children must be an array", typ)
		}
	}
	// loop.type 决定后续读取哪个字段，写错等于必然运行时报错
	if lc, ok := cfg["loop"].(map[string]any); ok {
		if raw, exists := lc["type"]; exists {
			s, _ := raw.(string)
			switch s {
			case "count", "each", "while", "do_while":
			default:
				return nil, fmt.Errorf("step %s: config.loop.type must be count / each / while / do_while, got %v", typ, raw)
			}
		}
	}

	var warnings []string
	switch typ {
	case "code":
		code, _ := cfg["code"].(map[string]any)
		if script, ok := code["script"].(string); !ok || strings.TrimSpace(script) == "" {
			warnings = append(warnings, "code step has no code.script and fails with script not found at run time")
		}
		if _, ok := cfg["outputs"].(map[string]any); !ok {
			warnings = append(warnings, "code step has no config.outputs: variables returned by the script are rejected as undeclared")
		}
	case "http":
		httpCfg, _ := cfg["http"].(map[string]any)
		if url, _ := httpCfg["url"].(string); strings.TrimSpace(url) == "" {
			warnings = append(warnings, "http step has no http.url and cannot send the request at run time")
		}
	case "llm":
		if _, ok := cfg["llm"].(map[string]any); !ok {
			warnings = append(warnings, "llm step has no config.llm (model and messages) and fails with a config error at run time")
		}
	case "loop":
		if _, ok := cfg["loop"].(map[string]any); !ok {
			warnings = append(warnings, "loop step has no config.loop and fails with loop config not found at run time")
		}
	case "branch", "intent":
		if children, ok := cfg["children"].([]any); !ok || len(children) == 0 {
			warnings = append(warnings, typ+" step has empty config.children: there is no branch to take at run time")
		}
	case "call":
		if target, _ := cfg["target"].(string); strings.TrimSpace(target) == "" {
			warnings = append(warnings, "call step has no config.target (must be the id of a function node in the same flow)")
		}
	case "wait":
		if forkID, _ := cfg["fork_node_id"].(string); strings.TrimSpace(forkID) == "" {
			warnings = append(warnings, "wait step has no config.fork_node_id (the concurrency group to wait on)")
		}
	}

	// 语言字段对照沙箱「工作流脚本语言」清单：写错在保存和画布上都看不出来，
	// 要到运行期才以 unsupported_lang 失败（节点级，没配 on_error.retry 就整条流程挂）。
	// 典型来源是模型凭印象写 js —— 系统提示里的「可执行语言」含 js，但那是 exec 通道的。
	// 取不到清单（沙箱没起来 / 旧版 sandboxd 没报该字段）时不猜，直接不提。
	if langs, ok := sandboxinfo.CodeLangs(); ok {
		for _, lf := range scriptLangFields(typ, cfg) {
			// template 是纯 Go 渲染，不占沙箱的 code 池，但对 result / progress 合法。
			if lf.lang == "template" || containsLang(langs, lf.lang) {
				continue
			}
			warnings = append(warnings, fmt.Sprintf(
				"step %s has %s=%s, which is not among the sandbox's current workflow script languages (%s); it will fail with unsupported_lang at run time",
				typ, lf.path, lf.lang, strings.Join(langs, ", ")))
		}
	}
	return warnings, nil
}

// langField 一步 config 里的某个语言字段；path 是给人看的取值路径（如 code.lang）。
type langField struct {
	path string
	lang string
}

// scriptLangFields 收集这一步 config 里所有「脚本语言」字段。
//
// 覆盖 create_flow 能生成的节点：code / result / progress 的 code.lang、branch / loop 的
// 条件脚本语言、http / rpc / mcp / llm 的 handler，以及 on_error 的两个兜底 handler。
// 一次扫全而不是各 case 里各写一遍：漏一处就等于漏一个「建成了、跑挂了」的入口。
func scriptLangFields(typ string, cfg map[string]any) []langField {
	var out []langField
	add := func(path string, obj map[string]any) {
		if s, _ := obj["lang"].(string); s != "" {
			out = append(out, langField{path: path, lang: s})
		}
	}
	switch typ {
	case "code", "result", "progress":
		add("code.lang", objectOf(cfg["code"]))
	case "branch":
		add("branch.lang", objectOf(cfg["branch"]))
	case "loop":
		add("loop.lang", objectOf(cfg["loop"]))
	case "http", "rpc", "mcp", "llm":
		add(typ+".handler.lang", objectOf(objectOf(cfg[typ])["handler"]))
	}
	if oe := objectOf(cfg["on_error"]); oe != nil {
		add("on_error.handler.lang", objectOf(oe["handler"]))
		add("on_error.post.handler.lang", objectOf(objectOf(oe["post"])["handler"]))
	}
	return out
}

// objectOf 取 map[string]any；非对象一律当成「没有」，形状错误由 validateStepConfig 前面拦。
func objectOf(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func containsLang(langs []string, lang string) bool {
	for _, l := range langs {
		if l == lang {
			return true
		}
	}
	return false
}

// parseFlowSteps 解析 steps 参数：每一项是 {type, label?, config?, branches?}。
func parseFlowSteps(raw any) ([]FlowStep, error) { return parseStepList(raw, "steps") }

// parseStepList 递归解析一条步骤链；path 只用于报错定位（steps / steps[1].branches[0].steps）。
func parseStepList(raw any, path string) ([]FlowStep, error) {
	list, ok := raw.([]any)
	if !ok || len(list) == 0 {
		if path == "steps" {
			return nil, errors.New("steps must be a non-empty array")
		}
		return nil, fmt.Errorf("%s must be a non-empty array", path)
	}
	steps := make([]FlowStep, 0, len(list))
	for i, it := range list {
		m, ok := it.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s[%d] is not an object", path, i)
		}
		st := FlowStep{
			Type:  argString(m, "type"),
			Label: argString(m, "label"),
		}
		if cfg, ok := m["config"].(map[string]any); ok {
			st.Config = cfg
		} else if m["config"] != nil {
			return nil, fmt.Errorf("%s[%d].config must be an object", path, i)
		}
		if strings.TrimSpace(st.Type) == "" {
			return nil, fmt.Errorf("%s[%d].type must not be empty", path, i)
		}
		if rawPos, exists := m["position"]; exists && rawPos != nil {
			pos, err := parseStepPosition(rawPos, fmt.Sprintf("%s[%d].position", path, i))
			if err != nil {
				return nil, err
			}
			st.Position = pos
		}
		if rawBranches, exists := m["branches"]; exists {
			branches, err := parseFlowBranches(rawBranches, fmt.Sprintf("%s[%d].branches", path, i))
			if err != nil {
				return nil, err
			}
			st.Branches = branches
		}
		if rawChildren, exists := m["children"]; exists && rawChildren != nil {
			children, err := parseStepList(rawChildren, fmt.Sprintf("%s[%d].children", path, i))
			if err != nil {
				return nil, err
			}
			st.Children = children
		}
		steps = append(steps, st)
	}
	return steps, nil
}

// parseStepPosition 解析步骤上的 position：{x, y}（单位 px，画布坐标）。
func parseStepPosition(raw any, path string) (map[string]int, error) {
	m, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s must be an {x, y} object", path)
	}
	x, okX := positionAxis(m["x"])
	y, okY := positionAxis(m["y"])
	if !okX || !okY {
		return nil, fmt.Errorf("%s: x / y must be numbers (px, canvas coordinates)", path)
	}
	return map[string]int{"x": x, "y": y}, nil
}

// positionAxis 把 JSON 数字转成 int；兼容 float64 / int / json.Number。
func positionAxis(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	case int64:
		return int(n), true
	case json.Number:
		i, err := n.Int64()
		if err != nil {
			return 0, false
		}
		return int(i), true
	}
	return 0, false
}

// parseFlowBranches 解析 branches：[{key, steps}]。
//
// 用数组而不是对象：节点 id 按声明顺序分配，对象（JSON map）在 Go 里解出来顺序不定，
// 同一份入参会建出不同的 id 与边，read_flow 回读和后续引用都会漂。
func parseFlowBranches(raw any, path string) ([]FlowBranch, error) {
	list, ok := raw.([]any)
	if !ok || len(list) == 0 {
		return nil, fmt.Errorf("%s must be a non-empty array of {key, steps}", path)
	}
	branches := make([]FlowBranch, 0, len(list))
	seen := make(map[string]bool, len(list))
	for i, it := range list {
		m, ok := it.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s[%d] is not an object", path, i)
		}
		key := strings.TrimSpace(argString(m, "key"))
		if key == "" {
			return nil, fmt.Errorf("%s[%d].key must not be empty: use a config.children[].id or default", path, i)
		}
		if seen[key] {
			return nil, fmt.Errorf("%s declares branch %q twice (each sub-branch takes exactly one chain)", path, key)
		}
		seen[key] = true
		steps, err := parseStepList(m["steps"], fmt.Sprintf("%s[%d].steps", path, i))
		if err != nil {
			return nil, err
		}
		branches = append(branches, FlowBranch{Key: key, Steps: steps})
	}
	return branches, nil
}

// parseFlowEndpoints 解析 external_protocols：
// 只接受 mcp / rpc，去重并保持给定顺序。
//
// 端点表 svc_endpoints 还允许 llm，但那是给「模型直连」用的，不是工作流对外的服务入口；
// 在这里放行一个没有消费方的协议，只会让对外开放的协议看起来比实际能用的多。
// 形态无法识别（不是字符串数组）时按「没传」处理 —— 与 argStringSlice 的约定一致。
func parseFlowEndpoints(args map[string]any) ([]string, error) {
	raw, ok := argStringSlice(args, "external_protocols")
	if !ok {
		return nil, nil
	}
	seen := make(map[string]bool, len(raw))
	out := make([]string, 0, len(raw))
	for _, p := range raw {
		p = strings.ToLower(p)
		switch p {
		case "mcp", "rpc":
		default:
			return nil, fmt.Errorf("external_protocols supports mcp / rpc only, got %q", p)
		}
		if seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out, nil
}

// ===== 入口参数（flows.arguments）=====
//
// arguments 是「声明」，不是运行时入参：它同时是端点对外的 input_schema 与前端
// 「入口参数」面板的读数，run_flow 的 inputs 键也按它对齐。所以写库前必须校验 ——
// 写坏的 schema 不会在创建期报错，只会在调用方那里变成「没有说明 / 必填不生效」。

// flowArgTypes 入口参数允许的 JSON Schema 类型（与前端编辑器、执行器 InitArguments 同一套）。
var flowArgTypes = map[string]bool{
	"string": true, "number": true, "boolean": true, "object": true, "array": true,
}

// flowArgNameRe 参数名会变成模板块里的变量名与 input_schema 的 key。
// 带点号的名字会被 GetVarByPath 当成嵌套路径切碎（a.b 读成 a 的 b），
// 前端也是按点号拼嵌套路径，所以点号 / 方括号一律不收。
var flowArgNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// parseFlowArguments 规整 create_flow 的 arguments，返回落库 JSON 与参数名清单。
// 两种写法都收：完整 schema（properties / required）与省掉外壳的简写。
func parseFlowArguments(raw any) (json.RawMessage, []string, error) {
	if raw == nil {
		return nil, nil, nil
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return nil, nil, errors.New(`arguments must be an object, e.g. {"properties":{"city":{"type":"string"}},"required":["city"]}`)
	}

	propsRaw := m
	if v, has := m["properties"]; has {
		pm, ok := v.(map[string]any)
		if !ok {
			return nil, nil, errors.New("arguments.properties must be an object (keys are parameter names)")
		}
		propsRaw = pm
	} else {
		// 简写：{"city":{"type":"string"}}；required 允许一起写在外面
		propsRaw = make(map[string]any, len(m))
		for k, v := range m {
			switch k {
			case "required":
			case "type", "description":
				return nil, nil, errors.New(`arguments has type / description but no properties: write {"type":"object","properties":{...}}`)
			default:
				propsRaw[k] = v
			}
		}
	}

	props, err := validateFlowArgProps(propsRaw, "")
	if err != nil {
		return nil, nil, err
	}
	required, err := flowArgRequired(m["required"], props, "")
	if err != nil {
		return nil, nil, err
	}
	if len(props) == 0 && len(required) == 0 {
		return nil, nil, nil // 空声明等于不声明，别往库里写一份空 schema
	}

	names := make([]string, 0, len(props))
	for name := range props {
		names = append(names, name)
	}
	sort.Strings(names)

	doc := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		doc["required"] = flowArgAnySlice(required)
	}
	buf, err := json.Marshal(doc)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to serialize arguments: %w", err)
	}
	return json.RawMessage(buf), names, nil
}

// validateFlowArgProps 递归校验 properties，并只保留已知字段（拼错的键会被拦下而不是写进库）。
func validateFlowArgProps(props map[string]any, prefix string) (map[string]any, error) {
	out := make(map[string]any, len(props))
	for name, raw := range props {
		if !flowArgNameRe.MatchString(name) {
			return nil, fmt.Errorf("invalid parameter name %q: letters, digits and underscores only, and it must not start with a digit (it becomes a variable name in template blocks)", prefix+name)
		}
		spec, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf(`parameter %q must be an object, e.g. {"type":"string","description":"..."}`, prefix+name)
		}
		typ := strings.TrimSpace(argString(spec, "type"))
		if typ == "" {
			return nil, fmt.Errorf("parameter %q has no type (string / number / boolean / object / array)", prefix+name)
		}
		if !flowArgTypes[typ] {
			return nil, fmt.Errorf("parameter %q has unsupported type=%q: only string / number / boolean / object / array", prefix+name, typ)
		}
		clean := map[string]any{"type": typ}
		for _, k := range []string{"description", "default"} {
			if v, ok := spec[k]; ok {
				clean[k] = v
			}
		}
		switch typ {
		case "object":
			subRaw := map[string]any{}
			if v, ok := spec["properties"]; ok {
				pm, ok := v.(map[string]any)
				if !ok {
					return nil, fmt.Errorf("parameter %q: properties must be an object", prefix+name)
				}
				subRaw = pm
			}
			sub, err := validateFlowArgProps(subRaw, prefix+name+".")
			if err != nil {
				return nil, err
			}
			clean["properties"] = sub
			req, err := flowArgRequired(spec["required"], sub, prefix+name+".")
			if err != nil {
				return nil, err
			}
			if len(req) > 0 {
				clean["required"] = flowArgAnySlice(req)
			}
		case "array":
			items, ok := spec["items"].(map[string]any)
			if !ok {
				return nil, fmt.Errorf(`parameter %q is an array and needs items, e.g. {"type":"array","items":{"type":"string"}}`, prefix+name)
			}
			it := strings.TrimSpace(argString(items, "type"))
			if !flowArgTypes[it] || it == "array" {
				return nil, fmt.Errorf("parameter %q: items.type must be string / number / boolean / object (wrap nested arrays in an object)", prefix+name)
			}
			cleanItems := map[string]any{"type": it}
			if it == "object" {
				subRaw := map[string]any{}
				if v, ok := items["properties"]; ok {
					pm, ok := v.(map[string]any)
					if !ok {
						return nil, fmt.Errorf("parameter %q: items.properties must be an object", prefix+name)
					}
					subRaw = pm
				}
				sub, err := validateFlowArgProps(subRaw, prefix+name+"[].")
				if err != nil {
					return nil, err
				}
				cleanItems["properties"] = sub
				req, err := flowArgRequired(items["required"], sub, prefix+name+"[].")
				if err != nil {
					return nil, err
				}
				if len(req) > 0 {
					cleanItems["required"] = flowArgAnySlice(req)
				}
			}
			clean["items"] = cleanItems
		}
		out[name] = clean
	}
	return out, nil
}

// flowArgRequired 校验 required 只能是 properties 里已有的名字（写错名字等于「永不生效的必填」）。
func flowArgRequired(raw any, props map[string]any, prefix string) ([]string, error) {
	if raw == nil {
		return nil, nil
	}
	where := "arguments"
	if prefix != "" {
		where = "parameter " + strings.TrimSuffix(prefix, ".")
	}
	list, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("%s: required must be an array of strings", where)
	}
	seen := make(map[string]bool, len(list))
	out := make([]string, 0, len(list))
	for _, item := range list {
		name, ok := item.(string)
		if !ok || name == "" {
			return nil, fmt.Errorf("%s: one required entry is not a parameter name: %v", where, item)
		}
		if _, exists := props[name]; !exists {
			return nil, fmt.Errorf("%s: required lists %q but properties has no such parameter", where, name)
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out, nil
}

// flowArgAnySlice 把 []string 转成 []any：保证编码成 JSON 数组，而不是 Go 的类型串。
func flowArgAnySlice(in []string) []any {
	out := make([]any, len(in))
	for i, s := range in {
		out[i] = s
	}
	return out
}

// ===== create_flow =====

type createFlowTool struct{}

func (t *createFlowTool) Meta() ToolMeta {
	return ToolMeta{
		Name:     "create_flow",
		Category: CategoryFlowAdmin,
		Description: "Create a workflow: assemble a list of steps into \"start → main chain\", then branch / intent pull out their own sub-chains with branches. " +
			"steps may use every node type in the executor registry (llm / code / http / mcp / rpc / branch / intent / loop / process / " +
			"function / call / assign / memory / wait / fork / result / progress / break / continue / return / exit / layout). " +
			"layout is a purely visual node (a 32px dot): the runner skips it and continues along its first outgoing edge, which makes it a good wrap point or grouping anchor. " +
			"**Conditional routing**: write branches on the branch / intent step — each key maps to a config.children[].id (use key=\"default\" for the unconditional fallback), " +
			"its value is that branch's own step chain, and the tool generates a source<sub-branch id> outgoing edge; a step that declares branches must be the last step of its chain. " +
			"**Inside containers**: write children on the loop / process / function step (same shape as steps, may nest further) — " +
			"the tool creates the entry automatically and links the sub-chain to it; without children it is an empty container (which spins empty at run time). " +
			"**Concurrency is off by default**: do not write config.concurrency on any node (the tool strips it and returns a warning). " +
			"Only when the user explicitly asks for concurrency, declare the shape with the concurrency parameter: mono = single-node concurrency (one node processes several items concurrently), " +
			"group = concurrency group (several nodes run at once and a wait joins them; pair with fork + wait); if the user asks for concurrency without naming a shape, judge by the task — " +
			"it is group only when several nodes must run at the same time. " +
			"wait's concurrency-group membership (config.concurrency) and config.fork_node_id still have to be supplied; any gap is reported in the returned warnings. " +
			"Each step's config is written verbatim into the node's data.config (the shape matches the front-end node form; when unsure, give the minimal config and calibrate in the UI afterwards); " +
			"source_ref in config.inputs, config.target on call and config.fork_node_id on wait may all be node labels — the tool replaces them with real ids. " +
			"nodes in the result is the list of created nodes (id / type / label), for checking source_ref and branch routing. " +
			"**Layout**: you may give each step a position to lay it out yourself (top-level nodes must all be given; the tool only detects overlaps and reports warnings); " +
			"otherwise it lays out automatically (main chain horizontal, branches vertical). After creating, update_node's position can fine-tune a single node. " +
			"**Entry arguments**: arguments declares the workflow's input parameters (JSON Schema); once exposed it is the endpoint's input_schema, " +
			"and it is also the run_flow inputs keys. Do not invent parameters the user never mentioned. " +
			"Endpoints are the workflow's external entry points (mcp / rpc), but they are **not exposed by default**: " +
			"without external_protocols it stays an internal flow; exposing it has side effects, so do it only when the user explicitly asks.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":        map[string]any{"type": "string", "description": "Workflow name: letters, digits and hyphens (must not start with a hyphen, ≤64). It becomes the external tool name local__<name>, so no non-ASCII; put the human-readable purpose in description"},
				"description": map[string]any{"type": "string", "description": "One sentence on what this flow does (required: the list tells flows apart by it, and the editor refuses an empty one)"},
				"arguments": map[string]any{
					"type": "object",
					"description": "Entry parameter declaration (optional; written to flows.arguments): once exposed it is the endpoint's input_schema, " +
						"and also the run_flow inputs keys. The shape matches the front-end \"Entry parameters\" editor: " +
						`{"type":"object","properties":{"city":{"type":"string","description":"city name"}},"required":["city"]}. ` +
						"type supports string / number / boolean / object / array only (array must carry items.type; object may also carry properties / required). " +
						`The shell-less shorthand is accepted too: {"city":{"type":"string"}}. ` +
						"Parameter names may only use letters, digits and underscores (they become template variables) — no dots.",
				},
				"steps": map[string]any{
					"type": "array",
					"description": "Steps in execution order, at least one. Structures beyond the main chain: branch / intent declare sub-chains with branches, " +
						"while loop / process / function children still have to be completed on the canvas — the returned warnings say which steps need it. " +
						"Each step may carry a position to lay it out yourself (give all top-level positions); otherwise the tool lays out automatically. " +
						"When you need to control wrapping or grouping, insert a type=layout step in the chain as the wrap point (it passes through at run time and does not change execution order)",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"type": map[string]any{"type": "string", "enum": []any{
								"llm", "code", "http", "mcp", "rpc",
								"branch", "intent", "loop", "process", "function", "call",
								"assign", "memory", "wait", "fork", "result", "progress",
								"break", "continue", "return", "exit", "layout",
							}},
							"label": map[string]any{"type": "string", "description": "Node title (the short name shown on the canvas); it can be referenced directly by source_ref in config.inputs"},
							"position": map[string]any{
								"type": "object",
								"description": "Optional: canvas coordinates {x, y} (px) you lay out for this step. **Top-level nodes must be given for all or none**: " +
									"give all and the tool follows your coordinates exactly (it only detects overlaps and reports warnings); miss one and everything falls back to auto layout. " +
									"Nodes inside a container need none (they are relative to the parent container and the tool lays them out itself). " +
									"Layout aesthetics: use multiples of 20 for x / y; keep the main chain on one row (same y) running left to right; " +
									"a leaf node is only 100–160 wide, so **use an x step of 200 between neighbours** (branch and containers are 300 wide, " +
									"so use 380 after them; **a node with concurrency on is about 280×64**) — do not stretch the step past 300 just to be safe, " +
									"the canvas ends up looking empty; place branches to the lower right of the branch node, one row per branch, with ≥ 72 between rows; " +
									"when a row would exceed 1500 wide, wrap at the branch / container — to control where it wraps, insert a " +
									"type=layout step there as the wrap point (a 32px dot that passes through at run time) and put the following nodes on the next row; " +
									"avoid crossing edges",
								"properties": map[string]any{
									"x": map[string]any{"type": "number", "description": "Canvas x (px)"},
									"y": map[string]any{"type": "number", "description": "Canvas y (px)"},
								},
								"required": []any{"x", "y"},
							},
							"config": map[string]any{
								"type":        "object",
								"description": "Node config, written verbatim into data.config (the shape matches the front-end form for that node type)",
							},
							"branches": map[string]any{
								"type": "array",
								"description": "branch / intent only, and the step must be the last one in its chain: declare one step chain per sub-branch. " +
									"Generates a source<key> outgoing edge, which is what the executor branches on",
								"items": map[string]any{
									"type": "object",
									"properties": map[string]any{
										"key": map[string]any{
											"type":        "string",
											"description": "Maps to config.children[].id; use \"default\" for the unconditional fallback branch",
										},
										"steps": map[string]any{
											"type":        "array",
											"description": "That sub-branch's step chain (same shape as the outer steps; may nest further branch steps)",
											"items":       map[string]any{"type": "object"},
										},
									},
									"required": []any{"key", "steps"},
								},
							},
							"children": map[string]any{
								"type": "array",
								"description": "loop / process / function only: the step chain inside the container (same shape as the outer steps; may nest). " +
									"The tool creates the entry automatically and connects the first node; without children it is an empty container (spins empty at run time)",
								"items": map[string]any{"type": "object"},
							},
						},
						"required": []any{"type", "config"},
					},
				},
				"external_protocols": map[string]any{
					"type":  "array",
					"items": map[string]any{"type": "string", "enum": []any{"mcp", "rpc"}},
					"description": "**Omit by default = not exposed** (the flow stays internal to the tenant; do not add it just to look complete). " +
						"Pass it only when the user explicitly asks to expose it / let others call it / mount it as an MCP or RPC endpoint, and list only the protocols they named; " +
						"a user merely describing a capability (e.g. \"can look up weather\", \"can output HTML\", \"needs to reach baidu\") is not an exposure request — " +
						"if unsure, leave it out and ask: which flow, mcp or rpc, endpoint name, who may call it.",
				},
				"concurrency": map[string]any{
					"type": "string",
					"enum": []any{"off", "mono", "group"},
					"description": "Concurrency scope, **default off, do not pass it proactively**: turn it on only when the user explicitly asks for concurrent execution, " +
						"and pick one of the two shapes according to what they asked — mono (single-node concurrency: one node processes several items concurrently, no teaming), " +
						"group (concurrency group: several nodes run at once and a wait joins them; needs fork + wait). " +
						"If the user just says \"concurrent\" without a shape, judge by the task: several inputs of the same job → mono; " +
						"several independent jobs running at the same time → group. " +
						"With off, config.concurrency on every node is stripped and a warning is returned; " +
						"mono keeps only {\"mode\":\"mono\"}; group keeps only {\"mode\":\"fork\",\"fork_node_id\":\"<id>\"}.",
				},
			},
			"required": []any{"name", "description", "steps"},
		},
	}
}

func (t *createFlowTool) Execute(ctx context.Context, c *Context, args map[string]any) (any, error) {
	name := strings.TrimSpace(argString(args, "name"))
	if err := validateFlowName(name); err != nil {
		return nil, err
	}
	// 描述必填：管理端「更新工作流」会拒绝空描述，建一个改不动的流程等于埋雷。
	description := strings.TrimSpace(argString(args, "description"))
	if description == "" {
		return nil, errors.New("description must not be empty: the list uses it to tell flows apart, and the front-end editor requires it")
	}
	arguments, argNames, err := parseFlowArguments(args["arguments"])
	if err != nil {
		return nil, err
	}
	if c.Flows == nil {
		return nil, errors.New("flow manager not available")
	}
	exists, err := c.Flows.Exists(ctx, name)
	if err != nil {
		return nil, err
	}
	if exists {
		return nil, fmt.Errorf("workflow %q already exists: pick another name or inspect it with read_flow", name)
	}

	steps, err := parseFlowSteps(args["steps"])
	if err != nil {
		return nil, err
	}
	concurrency, err := parseConcurrencyPolicy(args)
	if err != nil {
		return nil, err
	}
	build, err := buildFlowDiagram(steps, concurrency)
	if err != nil {
		return nil, err
	}
	endpoints, err := parseFlowEndpoints(args)
	if err != nil {
		return nil, err
	}

	id, err := c.Flows.Create(ctx, FlowInput{
		Name:        name,
		Description: description,
		Diagram:     build.Diagram,
		Arguments:   arguments,
		Endpoints:   endpoints,
	})
	if err != nil {
		return nil, err
	}
	// 字段名与其它 flow 工具保持一致（flow_id / flow_name）：create_flow 早先用 id / name，
	// 调用方要记两套；节点、边、run 的返回体都叫 flow_id，这里跟上。
	out := map[string]any{
		"flow_id":   id,
		"flow_name": name,
		"steps":     len(steps),
		"nodes":     nodeOutlines(build.Nodes),
		"endpoints": endpoints,
	}
	if len(argNames) > 0 {
		// 回参数名而不是整份 schema：要核对的是「声明了哪几个」，原文用 read_flow 看。
		out["arguments"] = argNames
	}
	// 结构缺口（容器没有子节点、子分支没有出边等）不阻断创建，但必须让调用方看见，
	// 否则「建好了」和「能跑」会被混为一谈。
	if len(build.Warnings) > 0 {
		out["warnings"] = build.Warnings
	}
	return out, nil
}

// nodeOutlines 压出 {id, type, label, position} 清单。
//
// 回给 create_flow 是为了让模型核对 id 相关的东西：source_ref 指向谁、分支子链的首节点
// 是哪一个、call 的 target 该填什么。没有 update_flow，建错了只能删掉重建。
// 带上 position：模型能据此判断自动排版是否可接受，需要时用 update_node 微调。
func nodeOutlines(nodes []diagramNode) []map[string]any {
	out := make([]map[string]any, 0, len(nodes))
	for _, n := range nodes {
		label, _ := n.Data["label"].(string)
		out = append(out, map[string]any{
			"id": n.ID, "type": n.Type, "label": label, "position": n.Position,
		})
	}
	return out
}

// ===== list_flows =====

type listFlowsTool struct{}

func (t *listFlowsTool) Meta() ToolMeta {
	return ToolMeta{
		Name:        "list_flows",
		Category:    CategoryFlowAdmin,
		Description: "List this tenant's workflows (id / name / description / status / exposed endpoints).",
		Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
	}
}

// maxListFlows 单次 list_flows 返回的条数上限（与 list_skills 同一考虑：
// 一个租户攒到几百个工作流时，全量铺进上下文不值当）。total 仍是真实总数。
const maxListFlows = 200

func (t *listFlowsTool) Execute(ctx context.Context, c *Context, args map[string]any) (any, error) {
	if c.Flows == nil {
		return nil, errors.New("flow manager not available")
	}
	list, err := c.Flows.List(ctx)
	if err != nil {
		return nil, err
	}
	total := len(list)
	if total > maxListFlows {
		return map[string]any{
			"flows":     list[:maxListFlows],
			"total":     total,
			"truncated": true,
			"note":      fmt.Sprintf("%d workflows in total and only the first %d are listed here; if the one you need is missing, fetch it directly by name with read_flow", total, maxListFlows),
		}, nil
	}
	return map[string]any{"flows": list, "total": total}, nil
}

// ===== read_flow =====

type readFlowTool struct{}

func (t *readFlowTool) Meta() ToolMeta {
	return ToolMeta{
		Name:     "read_flow",
		Category: CategoryFlowAdmin,
		Description: "Read one workflow: name, description, status, exposed endpoints, the entry parameter declaration (arguments) and the full diagram (each node's type and config). " +
			"Call it before changing a flow so you work from the current state instead of rewriting from memory.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"flow": map[string]any{"type": "string", "description": flowRefHint},
			},
			"required": []any{"flow"},
		},
	}
}

func (t *readFlowTool) Execute(ctx context.Context, c *Context, args map[string]any) (any, error) {
	ref := strings.TrimSpace(argString(args, "flow"))
	if ref == "" {
		return nil, errors.New("flow must not be empty (workflow id or name)")
	}
	if c.Flows == nil {
		return nil, errors.New("flow manager not available")
	}
	detail, err := c.Flows.Get(ctx, ref, ref)
	if err != nil {
		return nil, err
	}
	// nil = 不存在：给个明确的错，别让模型把 null 当成「这个流程是空的」。
	if detail == nil {
		return nil, fmt.Errorf("workflow %q does not exist: confirm the name with list_flows first", ref)
	}
	return detail, nil
}

// ===== delete_flow =====

type deleteFlowTool struct{}

func (t *deleteFlowTool) Meta() ToolMeta {
	return ToolMeta{
		Name:     "delete_flow",
		Category: CategoryFlowAdmin,
		Description: "Delete a workflow (definition only; its exposed endpoints stop working with it. Run records are not cascade-deleted). " +
			"The result returns the flow_id and flow_name of the one that was deleted — a name may point at a different definition after a rename or a delete-then-recreate, " +
			"so verify destructive operations against the returned id + name.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"flow": map[string]any{"type": "string", "description": flowRefHint},
			},
			"required": []any{"flow"},
		},
	}
}

func (t *deleteFlowTool) Execute(ctx context.Context, c *Context, args map[string]any) (any, error) {
	ref := strings.TrimSpace(argString(args, "flow"))
	if ref == "" {
		return nil, errors.New("flow must not be empty (workflow id or name)")
	}
	if c.Flows == nil {
		return nil, errors.New("flow manager not available")
	}
	deleted, err := c.Flows.Delete(ctx, ref, ref)
	if err != nil {
		return nil, err
	}
	if deleted == nil {
		return nil, fmt.Errorf("workflow %q does not exist: confirm the name with list_flows first", ref)
	}
	// 回显真实的 id 与名称：名称可能因「改名 / 删后同名重建」指向另一份定义，
	// 破坏性操作要让调用方看得到「删掉的到底是哪一个」。
	return map[string]any{
		"deleted":   true,
		"flow_id":   deleted.ID,
		"flow_name": deleted.Name,
	}, nil
}

// validateFlowName 与 assetpack.ValidateWorkflowName 同口径：工作流名就是对外工具名
// （svc_endpoints.name → MCP tool.name / local__<name> / 授权 ref local:rpc:<name>），
// 只能用字母数字横线。中文用途写 description —— 授权与模型侧工具名都只认 ASCII，
// 中文名建得出来、却对外开放不出可用的端点。
func validateFlowName(name string) error {
	if name == "" {
		return errors.New("workflow name cannot be empty")
	}
	if !ValidName(name) {
		return fmt.Errorf("workflow name %q is invalid: letters, digits and hyphens only (must not start with a hyphen, ≤64); "+
			"it becomes the external tool name local__<name>, so put the human-readable purpose in description", name)
	}
	return nil
}

// runFlowTool 试运行工作流：建完先跑一次，别等到对外开放端点、被别的 agent 调起来才发现跑不通。
type runFlowTool struct{}

func (t *runFlowTool) Meta() ToolMeta {
	return ToolMeta{
		Name:     "run_flow",
		Category: CategoryFlowAdmin,
		Description: "Test-run a **saved** workflow and return the overall status (success / failed / cancelled), the final output, " +
			"and each executed node's outcome, error code and duration — when it fails, use the node list to see where it stopped and why. " +
			"This is a **real execution**: http / rpc / mcp really send requests, llm really bills, code / handler really run scripts, " +
			"so use it only when you need to verify the flow actually works. " +
			"The whole call is bounded by the tool timeout (default 60s): a long flow is cancelled at that point and returns cancelled plus the nodes reached so far. " +
			"Node output variables are not returned (they can be large): read them from the run records page or read_flow_run.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"flow": map[string]any{"type": "string", "description": flowRefHint},
				"inputs": map[string]any{
					"type": "object",
					"description": "Workflow inputs: the keys match the source_type=argument variable names in config.inputs, " +
						"i.e. the names declared in create_flow's arguments. Passing extras is allowed, but they never show up in any source_type=argument variable",
				},
				"follow_drafts": map[string]any{
					"type": "boolean",
					"description": "Which version the **sub-flows** invoked by this test run read. Defaults to the current session's \"sub-calls follow drafts\" setting " +
						"(on by default in the chat window); pass false explicitly to make them use the released version (a sub-flow that was never released errors out). " +
						"In a formal run (MCP / RPC / endpoint) this parameter never opens up drafts. " +
						"It only affects invoked sub-flows — the main flow always runs the current draft.",
				},
			},
			"required": []any{"flow"},
		},
	}
}

func (t *runFlowTool) Execute(ctx context.Context, c *Context, args map[string]any) (any, error) {
	if c.Flows == nil {
		return nil, errors.New("flow manager not available")
	}
	ref := strings.TrimSpace(argString(args, "flow"))
	if ref == "" {
		return nil, errors.New("a workflow id or name is required")
	}
	inputs, err := argObject(args, "inputs")
	if err != nil {
		return nil, err
	}
	// 会话与栈深度一并下传：流程里若有 agent 节点，与本轮 run 同一场地、同一条深度链。
	// 只传合法 UUID：NewFlow 对坏 session_id 直接报错，那句「session_id is not a valid UUID」
	// 会被读成「流程跑不起来」，与真实原因（调用方会话 id 形态不对）差得远 —— 退成临时场地即可。
	sessionID := c.SessionID
	if _, perr := uuid.Parse(sessionID); perr != nil {
		sessionID = ""
	}
	// 子流程版本：缺省跟随本次运行的「子调用跟随草稿」开关（聊天缺省开，与编辑页调试器同义）。
	// 显式传 false 可以收紧成正式版本；传 true 在正式运行里也不生效 —— 草稿不从正式链扩散。
	followDrafts := c.ChildMode() == ModeDraft
	if raw, ok := args["follow_drafts"]; ok && raw != nil {
		followDrafts = followDrafts && argBool(args, "follow_drafts")
	}
	// 祖先链经 ctx 交给执行层（run_flow 试跑与 rpc 节点同口径，不为签名再加参数）
	ctx = callchain.With(ctx, c.CallChain)
	res, err := c.Flows.RunTest(ctx, ref, inputs, sessionID, c.CallDepth, followDrafts)
	if err != nil {
		return nil, err
	}
	if res == nil {
		return nil, fmt.Errorf("workflow %q does not exist", ref)
	}
	clipFlowRun(res, c.MaxOutputLimit())
	return res, nil
}

// flowRunNodeLogLimit 单节点日志 / 错误栈的回传上限（字节）。脚本 stderr 可能很长，但定位
// 问题只需要末尾：traceback 与真正的报错都在最后。
const flowRunNodeLogLimit = 2000

// clipFlowRun 按调用方的输出上限裁剪：最终输出用满额，失败节点的日志与栈各自封顶。
// 只裁剪回传给模型的这份副本 —— 运行记录里存的是完整数据。
func clipFlowRun(res *FlowRunResult, limit int) {
	if limit <= 0 {
		limit = DefaultMaxOutput
	}
	if clipped := cutHeadBytes(res.Result, limit); clipped != res.Result {
		res.Result = clipped
		res.Truncated = true
	}
	for i := range res.Nodes {
		if clipped := cutTailBytes(res.Nodes[i].Logs, flowRunNodeLogLimit); clipped != res.Nodes[i].Logs {
			res.Nodes[i].Logs = clipped
			res.Truncated = true
		}
		if clipped := cutTailBytes(res.Nodes[i].ErrStack, flowRunNodeLogLimit); clipped != res.Nodes[i].ErrStack {
			res.Nodes[i].ErrStack = clipped
			res.Truncated = true
		}
	}
}

// readFlowRunTool 读一次运行记录的完整取证数据（run_flow 负责「跑」，它负责「为什么」）。
type readFlowRunTool struct{}

func (t *readFlowRunTool) Meta() ToolMeta {
	return ToolMeta{
		Name:     "read_flow_run",
		Category: CategoryFlowAdmin,
		Description: "Read the full detail of one workflow run: overall status, inputs / flow variables, " +
			"and each node's input / output / local variables, error code, error stack, logs, retry count and error-handling action. " +
			"run_flow only answers \"did it succeed, where did it stop\"; this answers \"why\" — fetched by run_id. " +
			"run_id comes from run_flow's result or from the run-records list in the UI (records produced by UI debug runs are queryable too). " +
			"Only failed nodes are returned by default; the variables are a persisted snapshot, already truncated per top-level key at 64KB when stored.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"run_id":      map[string]any{"type": "string", "description": "Run id (the run_id returned by run_flow, or an id from the run-records list in the UI)"},
				"only_failed": map[string]any{"type": "boolean", "description": "Only return failed nodes (default true); pass false for the whole chain"},
				"nodes": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "Only look at these node ids (optional); combined with only_failed, for inspecting just a few steps",
				},
			},
			"required": []any{"run_id"},
		},
	}
}

func (t *readFlowRunTool) Execute(ctx context.Context, c *Context, args map[string]any) (any, error) {
	if c.Flows == nil {
		return nil, errors.New("flow manager not available")
	}
	runID := strings.TrimSpace(argString(args, "run_id"))
	if runID == "" {
		return nil, errors.New("run_id is required")
	}
	res, err := c.Flows.GetRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	if res == nil {
		return nil, fmt.Errorf("run record %q does not exist (run_id comes from run_flow output or the run-record list in the UI)", runID)
	}

	// only_failed 缺省 true：这个工具的用途是定位失败，成功节点只会白占上下文。
	// 显式传 false 才回全链路。
	onlyFailed := true
	if _, ok := args["only_failed"]; ok {
		onlyFailed = argBool(args, "only_failed")
	}
	if wanted, _ := argStringSlice(args, "nodes"); onlyFailed || len(wanted) > 0 {
		filterFlowRunNodes(res, onlyFailed, wanted)
	}
	clipFlowRunDetail(res, c.MaxOutputLimit())
	return res, nil
}

// filterFlowRunNodes 按 only_failed / nodes 过滤节点；NodeTotal 保留过滤前的总数，
// 免得调用方以为「整条流程就跑了这么几个节点」。
func filterFlowRunNodes(detail *FlowRunDetail, onlyFailed bool, wanted []string) {
	set := make(map[string]struct{}, len(wanted))
	for _, id := range wanted {
		if id = strings.TrimSpace(id); id != "" {
			set[id] = struct{}{}
		}
	}
	kept := make([]FlowRunNode, 0, len(detail.Nodes))
	for _, n := range detail.Nodes {
		if len(set) > 0 {
			if _, ok := set[n.NodeID]; !ok {
				continue
			}
		}
		if onlyFailed && n.Status != "failed" {
			continue
		}
		kept = append(kept, n)
	}
	detail.Nodes = kept
}

// flowRunVarLimit 单个变量字段的回传上限（字节）。落库本身按 64KB 截断，但一次运行可能有
// 几十个节点 × 4 个字段，全量给出去就是 MB 级。
const flowRunVarLimit = 8 * 1024

// clipFlowRunDetail 裁剪 read_flow_run 的返回：输出/日志/栈按文本截断，变量按 JSON 摘要降级。
func clipFlowRunDetail(detail *FlowRunDetail, limit int) {
	if limit <= 0 {
		limit = DefaultMaxOutput
	}
	if clipped := cutHeadBytes(detail.Result, limit); clipped != detail.Result {
		detail.Result = clipped
		detail.Truncated = true
	}
	var clipped bool
	detail.RunArgs, clipped = clipVar(detail.RunArgs, flowRunVarLimit)
	detail.Truncated = detail.Truncated || clipped
	detail.FlowVars, clipped = clipVar(detail.FlowVars, flowRunVarLimit)
	detail.Truncated = detail.Truncated || clipped
	for i := range detail.Nodes {
		n := &detail.Nodes[i]
		if clippedText := cutTailBytes(n.ErrStack, flowRunNodeLogLimit); clippedText != n.ErrStack {
			n.ErrStack = clippedText
			detail.Truncated = true
		}
		if clippedText := cutTailBytes(n.Logs, flowRunNodeLogLimit); clippedText != n.Logs {
			n.Logs = clippedText
			detail.Truncated = true
		}
		for _, field := range []struct {
			dst *any
			src any
		}{
			{&n.InputVars, n.InputVars},
			{&n.OutputVars, n.OutputVars},
			{&n.LocalVars, n.LocalVars},
			{&n.Extra, n.Extra},
		} {
			clippedField, changed := clipVar(field.src, flowRunVarLimit)
			*field.dst = clippedField
			detail.Truncated = detail.Truncated || changed
		}
	}
}

// clipVar 把超限的变量值换成「大小 + 顶层 key 列表」摘要。
//
// 不能像文本那样切前 N 字节：这些值是 JSON 对象，切一半就不是合法 JSON，外层序列化
// 会直接失败（整个工具结果变成 error）。摘要既合法，又能让人知道「键都在、值太大」。
func clipVar(v any, limit int) (any, bool) {
	if v == nil || limit <= 0 {
		return v, false
	}
	b, err := json.Marshal(v)
	if err != nil || len(b) <= limit {
		return v, false
	}
	summary := map[string]any{"_truncated": true, "_bytes": len(b)}
	if m, ok := v.(map[string]any); ok {
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		summary["keys"] = keys
	}
	return summary, true
}

// cutHeadBytes 按字节上限保留开头，并回退到 rune 边界：
// 直接切 []byte 会把汉字劈成半个，JSON 序列化后变成 U+FFFD。
func cutHeadBytes(s string, limit int) string {
	if limit <= 0 || len(s) <= limit {
		return s
	}
	i := limit
	for i > 0 && !utf8.RuneStart(s[i]) {
		i--
	}
	return s[:i] + "\n… (truncated)"
}

// cutTailBytes 同上，但保留结尾。
func cutTailBytes(s string, limit int) string {
	if limit <= 0 || len(s) <= limit {
		return s
	}
	i := len(s) - limit
	for i < len(s) && !utf8.RuneStart(s[i]) {
		i++
	}
	return "… (truncated, tail kept)\n" + s[i:]
}

func init() {
	Register(&createFlowTool{})
	Register(&listFlowsTool{})
	Register(&readFlowTool{})
	Register(&deleteFlowTool{})
	Register(&runFlowTool{})
	Register(&readFlowRunTool{})
	Register(&createEdgeTool{})
	Register(&updateEdgeTool{})
	Register(&deleteEdgeTool{})
	Register(&createNodeTool{})
	Register(&insertNodeTool{})
	Register(&updateNodeTool{})
	Register(&deleteNodeTool{})
	Register(&updateFlowArgumentsTool{})
}
