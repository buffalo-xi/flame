package agent

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"flame/nexus/service/agent/tools"
	"flame/nexus/shared/auth"
	"flame/nexus/shared/models"
	"flame/nexus/shared/quota"
	agents "flame/nexus/shared/repository/agents"
	flowruns "flame/nexus/shared/repository/flowruns"
	flows "flame/nexus/shared/repository/flows"
	svcendpoints "flame/nexus/shared/repository/svcendpoints"
	"flame/tools/xsql"

	"github.com/google/uuid"
)

// flowsManager 实现 tools.FlowsManager：包装 flows 仓储与端点仓储。
//
// schema 与当前用户在这里固化，工具层传不进来 —— 与 agentManager / skillManager 同一套惯例，
// 「能读写哪个租户」不取决于模型给了什么参数。
type flowsManager struct {
	schema string
	userID string
	repo   flows.Repository
	eps    svcendpoints.SvcEndpointRepository
	// agents 只用于「工作流名不能与 agent 重名」的前置校验：两个实体共享命名空间，
	// 端点名又恒等于实体名，创建前不拦就会在 svc_endpoints 里撞成同一个名字。
	agents agents.Repository
}

// NewFlowsManager 创建 tools.FlowsManager（租户 schema 与当前用户固定）。
func NewFlowsManager(schema, userID string) tools.FlowsManager {
	return &flowsManager{
		schema: schema,
		userID: userID,
		repo:   flows.NewPostgresRepository(),
		eps:    svcendpoints.NewPostgresRepository(),
		agents: agents.NewRepository(),
	}
}

func (m *flowsManager) auth() *auth.AuthInfo {
	// 带上 userID：仓储用 authInfo.ID 写 created_by / updated_by（空串会被转成 NULL）
	return &auth.AuthInfo{Schema: m.schema, ID: m.userID}
}

// Exists 工作流名是否已被占用。
//
// 同时看 agents：两边共享命名空间（CreateFlow 内部也会查一次 agents，这里提前查是为了
// 给出「撞的是智能体」这种能直接纠正的错误，而不是让仓储返回一句笼统的已存在）。
func (m *flowsManager) Exists(ctx context.Context, name string) (bool, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return false, nil
	}
	exists, err := m.repo.ExistsFlowName(m.auth(), name)
	if err != nil || exists {
		return exists, err
	}
	return m.agents.ExistsName(m.auth(), name, "")
}

// 新建工作流的限额沿用编辑器「新增工作流」的默认值（前端 Flows.vue 的初始表单）。
//
// 为什么不能落零值：max_run_records 的 0 在运行时的语义是「一条运行记录都不保留」，
// max_session_records 的 0 等同「不做会话记忆」。直接留零会让工具建的流程在运行记录页
// 里永远是空白，而排查的人只会觉得「这个流程从来没跑过」。
const (
	defaultFlowTimeout           = 300 // 秒
	defaultFlowMaxRunRecords     = 50
	defaultFlowMaxSessionRecords = 30
)

// Create 创建工作流；Endpoints 非空时一并把这些协议对外开放成端点。
func (m *flowsManager) Create(ctx context.Context, in tools.FlowInput) (string, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return "", errors.New("工作流名不能为空")
	}
	// 名字会变成端点名 / 模型侧工具名 local__<name>，只能用 ASCII 字母数字横线。
	// 工具层（create_flow）已经挡过一次，这里是给其它调用方兜底 —— 少一处就等于
	// 又留了一条「前端建不出来、工具建得出来」的口子。
	if !tools.ValidName(name) {
		return "", fmt.Errorf("工作流名 %q 不合法：只能用字母、数字与横线（不能以横线开头，≤64），中文说明请写进描述", name)
	}
	description := strings.TrimSpace(in.Description)
	if description == "" {
		return "", errors.New("工作流描述不能为空")
	}

	// 配额：与 agent 创建同一套口径（CountFlows 直查库，不为一次计数往仓储接口上加方法）
	cnt, err := quota.CountFlows(m.schema)
	if err != nil {
		return "", err
	}
	if err := models.CheckQuota("flow", cnt, quota.Resolve(m.schema).MaxFlows); err != nil {
		return "", err
	}

	inst := &models.Flow{
		Name:              name,
		Diagram:           xsql.JSONB{Data: in.Diagram},
		Description:       xsql.String(description),
		Timeout:           defaultFlowTimeout,
		MaxRecursionDepth: -1,
		MaxNodeCount:      -1,
		MaxExecCount:      -1,
		MaxRunRecords:     defaultFlowMaxRunRecords,
		MaxSessionRecords: defaultFlowMaxSessionRecords,
	}
	if len(in.Arguments) > 0 {
		// 入口参数声明：对外开放后端点对外的 input_schema 就取自这一列
		// （仓储 CreateFlow 里的 CreateManagedRPC 会强制覆盖过去）。
		inst.Arguments = xsql.JSONB{Data: in.Arguments}
	}
	id, err := m.repo.CreateFlow(m.auth(), inst)
	if err != nil {
		return "", err
	}

	if err := m.expose(id, in.Endpoints); err != nil {
		// 端点没发出去就把刚建的定义回滚掉：留下一个「用户以为带端点、实际没有」的半成品，
		// 比直接报错难排查得多。先删端点：刚建的可能已经对外，DeleteFlow 会拒绝。
		_ = m.eps.DeleteByTarget(m.auth(), "flow", id)
		_ = m.repo.DeleteFlow(m.auth(), id)
		return "", err
	}
	return id, nil
}

// expose 为这些协议打开对外（落到 svc_endpoints）。
//
// 建流程时仓储已经自动补了一条未对外的 RPC 端点（CreateManagedRPC），所以这里是
// 「确保存在 + 打开开关」：直接 Create 会撞 (target, protocol) 唯一约束。
//
// 不写 flows.endpoints：那一列是 svc_endpoints 的派生镜像，由仓储的 syncEndpoints
// 在每次增删后整列重写 —— 直接写它会被覆盖，看起来对外开放成功、实际没有入口。
func (m *flowsManager) expose(flowID string, protocols []string) error {
	if len(protocols) == 0 {
		return nil
	}
	existing, err := m.eps.ListAll(m.auth(), &svcendpoints.ListSvcEndpointsRequest{
		TargetType: "flow",
		TargetID:   flowID,
	})
	if err != nil {
		return fmt.Errorf("查询流程端点失败: %w", err)
	}
	endpointIDs := make(map[string]string, len(existing))
	for i := range existing {
		endpointIDs[existing[i].Protocol] = existing[i].ID
	}

	for _, p := range protocols {
		if id, ok := endpointIDs[p]; ok {
			// 自动补的 RPC 端点就在这一类：只切开关，不重建
			if err := m.eps.UpdateExposed(m.auth(), id, true); err != nil {
				return fmt.Errorf("开放 %s 端点失败: %w", p, err)
			}
			continue
		}
		ep := &models.SvcEndpoint{
			TargetType: "flow",
			TargetID:   flowID,
			Protocol:   p,
			Exposed:    xsql.Bool(true),
		}
		if _, err := m.eps.Create(m.auth(), ep); err != nil {
			return fmt.Errorf("创建 %s 端点失败: %w", p, err)
		}
	}
	return nil
}

// List 列出本租户全部工作流。Limit=-1 是仓储约定的「取全部」，工具层再按条数截断展示。
func (m *flowsManager) List(ctx context.Context) ([]tools.FlowInfo, error) {
	list, _, err := m.repo.ListFlows(m.auth(), &flows.ListFlowsRequest{Page: 1, Limit: -1})
	if err != nil {
		return nil, err
	}
	out := make([]tools.FlowInfo, 0, len(list))
	for i := range list {
		out = append(out, flowInfo(&list[i]))
	}
	return out, nil
}

// Get 按 id 优先、名称兜底取详情；不存在返回 (nil, nil)（与 AgentManager.Get 一致，由调用方报错）。
func (m *flowsManager) Get(ctx context.Context, id, name string) (*tools.FlowDetail, error) {
	f, err := m.find(ctx, id, name)
	if err != nil || f == nil {
		return nil, err
	}
	diagram, err := f.Diagram.MarshalJSON()
	if err != nil {
		return nil, err
	}
	detail := &tools.FlowDetail{FlowInfo: flowInfo(f), Diagram: json.RawMessage(diagram)}
	// 没声明过就留空（omitempty）：让工具层能区分「没有入参」与「有入参但读不到」。
	if !f.Arguments.IsNil() {
		args, err := f.Arguments.MarshalJSON()
		if err != nil {
			return nil, err
		}
		detail.Arguments = json.RawMessage(args)
	}
	return detail, nil
}

// UpdateDiagram 只替换 diagram：名称 / 描述 / 限额 / 断点都不动。
//
// 为什么不用 UpdateFlow：那是整行重写，而 GetFlow 不加载 breakpoints，
// 拿它改图会把断点清空。边工具只该动图，所以走仓储的窄更新。
func (m *flowsManager) UpdateDiagram(ctx context.Context, id, name string, diagram json.RawMessage) (*tools.FlowInfo, error) {
	f, err := m.find(ctx, id, name)
	if err != nil || f == nil {
		return nil, err
	}
	if err := m.repo.UpdateFlowDiagram(m.auth(), f.ID, xsql.JSONB{Data: diagram}); err != nil {
		return nil, err
	}
	info := flowInfo(f)
	return &info, nil
}

// UpdateArguments 只替换入口参数声明：名字 / 描述 / diagram / 限额 / 断点都不动。
//
// 为什么不用 UpdateFlow：那是整行重写，而 GetFlow 不加载 breakpoints，
// 拿它改参数会把断点清空（与 UpdateDiagram 同一个坑）。
// 端点 input_schema 由仓储在同一事务里同步，工具层不用再手动刷一遍。
func (m *flowsManager) UpdateArguments(ctx context.Context, id, name string, arguments json.RawMessage) (*tools.FlowInfo, error) {
	f, err := m.find(ctx, id, name)
	if err != nil || f == nil {
		return nil, err
	}
	if err := m.repo.UpdateFlowArguments(m.auth(), f.ID, xsql.JSONB{Data: arguments}); err != nil {
		return nil, err
	}
	info := flowInfo(f)
	return &info, nil
}

// Delete 删除工作流，并返回被删掉的那一行；不存在返回 (nil, nil)。
//
// 返回实体是为了让工具层回显「真实 id + 名称」：名称在「改名 / 删后同名重建」的场景下
// 会指向另一份定义，破坏性操作必须留下可核对的痕迹。缺失时的报错文案也收到工具层，
// 口径与其他工具一致。
func (m *flowsManager) Delete(ctx context.Context, id, name string) (*tools.FlowInfo, error) {
	f, err := m.find(ctx, id, name)
	if err != nil {
		return nil, err
	}
	if f == nil {
		return nil, nil
	}
	// 端点交给 DeleteFlow 统一处理：未对外的（含自动补的 RPC）随流程级联删除；
	// 仍有对外开放的端点时仓储会拒绝 —— 那是「外部还在用」的信号，不该由一次
	// 模型发起的删除悄悄掐断，让用户先收回对外。
	if err := m.repo.DeleteFlow(m.auth(), f.ID); err != nil {
		return nil, err
	}
	deleted := flowInfo(f)
	return &deleted, nil
}

// RunTest 试运行：定位到流程后交给 executors 注册的执行通道真实跑一遍。
//
// 不校验 exposed（试跑的对象通常还没端点），但状态按运行时同口径拦废弃 ——
// 「测试通过、上线跑不了」比直接拒绝更难查。
func (m *flowsManager) RunTest(ctx context.Context, ref string, args map[string]any, sessionID string, callDepth int, followDrafts bool) (*tools.FlowRunResult, error) {
	f, err := m.find(ctx, ref, ref)
	if err != nil {
		return nil, err
	}
	if f == nil {
		return nil, nil // 由工具层报「不存在」
	}
	if !models.IsRunnable(int(f.Status)) {
		return nil, fmt.Errorf("工作流 %q 已废弃，不可运行", f.Name)
	}
	if runFlowTest == nil {
		return nil, errFlowTestUnavailable
	}
	return runFlowTest(ctx, m.auth(), f.ID, args, sessionID, callDepth, followDrafts)
}

// GetRun 读一次运行记录（run + 节点记录）。
//
// 数据与前端「运行记录」页同源（executors.persistRun 落的 flow_run_records /
// flow_node_records），所以 agent 不只能查自己 run_flow 跑的那次，前端 debug 跑、
// 别的 agent 调起来的运行也都能按 run_id 查 —— 这才是排障时的真实入口。
func (m *flowsManager) GetRun(ctx context.Context, runID string) (*tools.FlowRunDetail, error) {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return nil, errors.New("需要运行 id")
	}
	// flow_run_records.id 也是 uuid 列：非 UUID 一定查不到，但错误是 22P02 而不是 no rows，
	// 会把一句 SQL 报错糊到模型脸上。直接当「不存在」，让工具层给「run_id 不存在」的人话。
	if !isUUID(runID) {
		return nil, nil
	}
	run, nodes, err := flowruns.NewRepository().GetRunDetail(m.schema, runID)
	if err != nil {
		// 找不到不是错误：由工具层给一句「run_id 不存在」的提示，别把 sql 错误抛给模型
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if run == nil {
		return nil, nil
	}

	out := &tools.FlowRunDetail{
		FlowID:     run.FlowID,
		RunID:      run.ID,
		Status:     run.Status,
		RunType:    run.RunType,
		ErrMsg:     run.ErrMsg,
		Result:     run.Result,
		DurationMs: run.DurationMs,
		StartedAt:  formatRunTime(run.StartedAt),
		RunArgs:    decodeRunVar(run.RunArgs),
		FlowVars:   decodeRunVar(run.RunFlowVars),
		NodeTotal:  len(nodes),
		Nodes:      make([]tools.FlowRunNode, 0, len(nodes)),
	}
	if run.FinishedAt != nil {
		out.FinishedAt = formatRunTime(*run.FinishedAt)
	}
	for _, n := range nodes {
		out.Nodes = append(out.Nodes, toFlowRunNode(n))
	}
	return out, nil
}

// toFlowRunNode 把落库的节点记录转成工具返回体。
func toFlowRunNode(n *models.FlowNodeRecord) tools.FlowRunNode {
	return tools.FlowRunNode{
		NodeID:      n.NodeID,
		Type:        n.NodeType,
		Status:      n.Status,
		ErrCode:     n.ErrCode,
		ErrMsg:      n.ErrMsg,
		ErrStack:    n.ErrStack,
		RetryCount:  n.RetryCount,
		ErrorAction: n.ErrorAction,
		DurationMs:  n.DurationMs,
		InputVars:   decodeRunVar(n.InputVars),
		OutputVars:  decodeRunVar(n.OutputVars),
		LocalVars:   decodeRunVar(n.LocalVars),
		Extra:       decodeRunVar(n.Extra),
		Logs:        decodeRunLogs(n.RunLogs),
		// 落库时就截断过：告诉调用方「值不全」，别据此断言数据只有这些
		Truncated: n.InputVarsTruncated || n.OutputVarsTruncated || n.LocalVarsTruncated || n.RunLogsTruncated,
	}
}

// decodeRunVar 把 jsonb 解成可直接读的对象；坏数据原样给字符串，至少不丢信息。
func decodeRunVar(raw []byte) any {
	if len(raw) == 0 {
		return nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return string(raw)
	}
	return v
}

// decodeRunLogs run_logs 存的是 JSON 字符串数组，转成多行文本给模型读。
func decodeRunLogs(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	var lines []string
	if err := json.Unmarshal(raw, &lines); err != nil {
		return string(raw)
	}
	return strings.Join(lines, "\n")
}

func formatRunTime(t xsql.Time) string {
	if time.Time(t).IsZero() {
		return ""
	}
	return time.Time(t).Format(time.RFC3339)
}

// find 定位一行工作流（含 diagram）。id 优先，name 兜底。
//
// 仓储没有「按名字取」的方法：列表查询刻意不取 diagram（避免拖着一张大 JSON 扫全表），
// 所以按名字命中后还要再取一次整行。为了一个只读工具往共享接口上加方法不划算。
func (m *flowsManager) find(ctx context.Context, id, name string) (*models.Flow, error) {
	id = strings.TrimSpace(id)
	name = strings.TrimSpace(name)
	if id == "" && name == "" {
		return nil, errors.New("需要工作流 id 或名称")
	}
	// 只有 UUID 形态才当 id 查。flows.id 是 uuid 列：把名称塞进 `WHERE id = $1` 不会得到
	// 「查无此行」，而是让 Postgres 报 22P02（invalid input syntax for type uuid），
	// 于是 name 兜底永远走不到 —— 工具文档里写的「id 或名称」就成了假的。
	// 工具层统一按 (ref, ref) 传参，所以这里是唯一的形态判定点。
	if id != "" && isUUID(id) {
		f, err := m.repo.GetFlow(m.auth(), id)
		switch {
		case err == nil:
			return f, nil
		case !errors.Is(err, sql.ErrNoRows):
			return nil, err
		}
	}
	if name == "" {
		return nil, nil
	}

	list, _, err := m.repo.ListFlows(m.auth(), &flows.ListFlowsRequest{Page: 1, Limit: -1, Name: name})
	if err != nil {
		return nil, err
	}
	for i := range list {
		if strings.EqualFold(list[i].Name, name) {
			return m.repo.GetFlow(m.auth(), list[i].ID)
		}
	}
	return nil, nil
}

// isUUID 判断 ref 是不是 id 形态。
//
// 存在的理由见 find 里那段：flows.id / flow_run_records.id 都是 uuid 列，非 UUID 值不会
// 得到「没查到」，而是整条语句报错。在应用层先分流，才能让「名称兜底」真正可执行。
func isUUID(s string) bool {
	_, err := uuid.Parse(s)
	return err == nil
}

// flowInfo 把模型行压成工具层视图：端点只回已对外开放的协议名（协议 + 开关是存储形态，
// 模型要的是「对外有什么入口」）。
func flowInfo(f *models.Flow) tools.FlowInfo {
	eps := make([]string, 0, len(f.Endpoints))
	for _, ep := range f.Endpoints {
		if ep.Exposed {
			eps = append(eps, ep.Protocol)
		}
	}
	return tools.FlowInfo{
		ID:          f.ID,
		Name:        f.Name,
		Description: string(f.Description),
		Status:      int(f.Status),
		Endpoints:   eps,
	}
}
