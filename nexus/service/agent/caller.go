package agent

import (
	"context"
	"fmt"
	"strings"

	"flame/nexus/service/agent/core"
	"flame/nexus/service/agent/tools"
	"flame/nexus/shared/auth"
	"flame/nexus/shared/callchain"
	"flame/nexus/shared/models"
	"flame/nexus/shared/repository/agentrelations"
	agents "flame/nexus/shared/repository/agents"
)

// 级联深度上限不再是常量：来自 platform.agent_configs（见 shared/agentconfig），
// 由 newAgentImpl 快照后经 NewAgentCaller 注入。子 agent 创建时会自己 Resolve 一次
// （同租户同配置），所以整棵调用树的结论一致。

// agentCaller 实现 tools.AgentCaller：同步调用另一个 agent 并返回其最终结果。
// 每次调用按其定义起一个临时实例，跑完即回收（不常驻，无需生命周期管理）。
type agentCaller struct {
	schema     string
	agentsRepo agents.Repository
	rel        agentrelations.Repository
	emit       func(core.Event) // 转发被调 agent 的事件到父级事件流（可为 nil）
	maxDepth   int              // 级联调用最大深度（agentconfig，<=0 时兜底为 3）
}

// NewAgentCaller 创建 AgentCaller（租户 schema 固定）。
// emit 用于把被调 agent 的思考/工具调用/正文事件转发给父级，使其过程在主对话可见（可为 nil）。
// maxDepth 来自 agentconfig（租户/系统两级）；传 <= 0 时兜底为 3 —— 绝不能变成「不限深度」。
func NewAgentCaller(schema string, emit func(core.Event), maxDepth int) tools.AgentCaller {
	if maxDepth <= 0 {
		maxDepth = 3
	}
	return &agentCaller{
		schema:     schema,
		agentsRepo: agents.NewRepository(),
		rel:        agentrelations.NewRepository(),
		emit:       emit,
		maxDepth:   maxDepth,
	}
}

// Call 深度校验 → 查定义 → 授权校验 → 起临时实例 → 同步运行 → 返回结果。
func (c *agentCaller) Call(ctx context.Context, req tools.CallRequest) (string, error) {
	if req.Depth >= c.maxDepth {
		return "", fmt.Errorf("agent call depth exceeded (limit %d)", c.maxDepth)
	}

	inst, err := c.getByName(req.CalleeName)
	if err != nil {
		return "", err
	}
	if inst == nil {
		return "", fmt.Errorf("agent %q not found", req.CalleeName)
	}
	// 废弃的 agent 不可被调用
	if !models.IsRunnable(int(inst.Status)) {
		return "", fmt.Errorf("agent %q is deprecated", req.CalleeName)
	}
	// supervisor 可以被其它 agent 调用：它的能力不该因调用者而打折，
	// 且它恒读草稿（resolveDefinitionMode），被调起时仍是完整的管理面。
	// 风险由授权兜住 —— 下面这条校验对 supervisor 同样生效，无授权行一律拒绝。

	// 授权校验：所有 agent 一视同仁（无行 = 拒绝；无特权账号）
	allowed, aerr := c.rel.Allowed(c.schema, req.CallerID, inst.ID)
	if aerr != nil {
		return "", aerr
	}
	if !allowed {
		return "", fmt.Errorf("agent %q is not allowed to call %q", req.CallerName, req.CalleeName)
	}

	// 循环检测：目标已在自己的祖先链里（f → a → f）就断，不必等深度上限跑满。
	// 与 RPC 入站同一套判定（见 shared/callchain）：平级重复调用不在链上，不会被误伤。
	self := callchain.Key(callchain.KindAgent, inst.ID)
	if callchain.Contains(req.Chain, self) {
		return "", fmt.Errorf("检测到循环调用：%s 已在本调用链中（%s）", self, callchain.Format(req.Chain))
	}

	// 被调 agent 的入参：arguments 作为其输入，并透传调用深度、userID 与会话
	// （会话透传让子 agent 与父共用同一场地）
	sub := map[string]any{
		"args":        req.Args,
		"_call_depth": req.Depth + 1,
		"_run_type":   "agent",
	}
	// 链：追加被调方自己后下传（子 agent 再从它继续往下走时，本层就成了祖先）
	if chain := callchain.Append(req.Chain, self); len(chain) > 0 {
		sub[callchain.ArgumentKey] = chain
	}
	if req.UserID != "" {
		sub["user_id"] = req.UserID
	}
	if req.SessionID != "" {
		sub["session_id"] = req.SessionID
	}
	// 读定义模式跟着父走：父正式 → 子读已发布版本；父调试 → 子读草稿。
	// 不透传的话，调试时子 agent 跑的是线上版本，排查时极易被这一点骗。
	if req.DefinitionMode.IsCurrent() {
		SetReleaseMode(sub)
	} else {
		SetDraftMode(sub)
	}

	a, err := NewAgent(inst, c.schema, sub)
	if err != nil {
		return "", err
	}
	// 无 session 的子 agent 落在 runs/{run_id}/ 的一次性场地上，靠 Close() 回收
	// （cleanupEphemeralWorkspace：先 KillInteractiveRun 再 DeletePrefix；有 session 时直接跳过，
	// 不会动会话目录）。rpc / mcp / flow 三个入口都有 defer Close，只有这里漏了 ——
	// 每次 call_agent 都留下一个目录，要等下次进程重启才被启动清扫带走。
	defer a.Close()

	// 边跑边把被调 agent 的事件转发给父级（标注来源 agent 与深度）
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range a.GetEventChan() {
			if c.emit == nil {
				continue
			}
			ev.Agent = inst.Name
			ev.Depth = req.Depth + 1
			c.emit(ev)
		}
	}()

	// 父 ctx 透传：父被终止（用户停止 / 断连）时子实例立即退出，
	// 而不是继续跑完再丢弃结果。
	streamErr := a.Stream(ctx)
	a.CloseChans() // 关闭 eventCh → 上面的 goroutine 退出
	<-done
	if streamErr != nil {
		return "", streamErr
	}
	return a.GetResult(), nil
}

// Callable 列出 caller 可调用的 agent（出边有记录 ∩ 可运行 ∩ 非超级智能体），口径与 Call 同源。
// 一次 JOIN 查询拿全（name / description / status），不逐目标二次查询。
func (c *agentCaller) Callable(ctx context.Context, callerID string) ([]tools.AgentInfo, error) {
	if callerID == "" {
		return nil, nil
	}
	list, err := c.rel.ListCallableByCaller(c.schema, callerID)
	if err != nil || len(list) == 0 {
		return nil, err
	}
	out := make([]tools.AgentInfo, 0, len(list))
	for _, m := range list {
		// 只有废弃的不可被调用（见 IsRunnable）；supervisor 照常列出 ——
		// 它能不能被调由授权关系决定，不靠身份硬拒。
		if !models.IsRunnable(m.Status) {
			continue
		}
		out = append(out, tools.AgentInfo{
			ID: m.ID, Name: m.Name, Description: m.Description, Status: m.Status,
		})
	}
	return out, nil
}

// getByName 按 name 精确查定义（ListAgents 的 Name 为模糊匹配，需再精确比对）。
//
// Limit 必须取 -1（全部）：仓储按 name 升序返回，带 LIMIT 时精确匹配可能被排在窗外。
// 例：writer / writer-1 … writer-9 时查 "writer-5"，前 5 条是 writer、writer-1…4，
// 精确匹配被截掉 → 一个存在的被调方被报成「not found」。
func (c *agentCaller) getByName(name string) (*models.Agent, error) {
	instances, _, err := c.agentsRepo.ListAgents(&auth.AuthInfo{Schema: c.schema},
		&agents.ListAgentsRequest{Name: name, Page: 1, Limit: -1})
	if err != nil {
		return nil, err
	}
	for i := range instances {
		if strings.EqualFold(instances[i].Name, name) {
			return &instances[i], nil
		}
	}
	return nil, nil
}
