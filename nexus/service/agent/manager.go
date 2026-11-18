package agent

import (
	"context"
	"fmt"
	"strings"

	"flame/nexus/service/agent/tools"
	"flame/nexus/shared/auth"
	"flame/nexus/shared/models"
	"flame/nexus/shared/paths"
	"flame/nexus/shared/quota"
	agents "flame/nexus/shared/repository/agents"
	flows "flame/nexus/shared/repository/flows"
	"flame/nexus/shared/storage"
	"flame/tools/xlog"
	"flame/tools/xsql"
)

// agentManager 实现 tools.AgentManager，包装 agents repository。
type agentManager struct {
	schema       string
	agentID      string // 当前 agent uuid（创建新 agent 时作为创建者写入授权）
	agentName    string // 当前 agent 名
	isSupervisor bool   // 身份判据（agents.is_supervisor）：仅 supervisor 可做管理类动作
	userID       string // 当前用户 uuid（写 created_by）
	repo         agents.Repository
	flowRepo     flows.Repository // agent 名不能与工作流重名
	store        storage.Storage  // 定义域目录重命名/删除走 storage（local / nfs / s3 通用）
}

// NewAgentManager 创建 tools.AgentManager（租户 schema 与当前 agent 身份固定）。
func NewAgentManager(schema, agentID, agentName, userID string, store storage.Storage, isSupervisor bool) tools.AgentManager {
	return &agentManager{schema: schema, agentID: agentID, agentName: agentName, isSupervisor: isSupervisor, userID: userID,
		repo: agents.NewRepository(), flowRepo: flows.NewPostgresRepository(), store: store}
}

func (m *agentManager) auth() *auth.AuthInfo {
	// 带上 userID：仓储用 authInfo.ID 写 created_by/updated_by（空串会被转成 NULL）
	return &auth.AuthInfo{Schema: m.schema, ID: m.userID}
}

// Create 创建 agent 实例（写库）。文件（AGENT.md）由元工具经 storage 写入。
// config 列不在此写入：由仓储落默认 {}，留作将来扩展。
//
// isSupervisor=true 表示「直接建成 supervisor」：仓储会按起步套件物化
// core 工具组、两个开关与四份 authoring 技能（与创建后 SetSupervisor(true) 同结果）。
// 非 supervisor 创建不了 supervisor —— 执行层已按分类拦过一次，这里再拦一次纵深防御。
func (m *agentManager) Create(ctx context.Context, name, description string, isSupervisor bool) (string, error) {
	// 名称规则兜底：只允许字母数字横线，长度≤64
	if !tools.ValidName(name) {
		return "", fmt.Errorf("invalid agent name %q (只允许字母数字横线，长度≤64)", name)
	}
	if isSupervisor && !m.isSupervisor {
		return "", fmt.Errorf("仅 supervisor 可创建 supervisor")
	}
	// 先校验重名：不能与其它 agent、工作流重名（任何写库之前）
	if err := m.ensureNameFree(ctx, name, ""); err != nil {
		return "", err
	}
	// 配额检查：租户内 agent 数量上限
	cnt, err := quota.CountAgents(m.schema)
	if err != nil {
		return "", err
	}
	if err := models.CheckQuota("agent", cnt, quota.Resolve(m.schema).MaxAgents); err != nil {
		return "", err
	}

	inst := &models.Agent{
		Name:         name,
		Description:  xsql.String(description),
		Status:       xsql.Int(models.StatusNormal),
		IsSupervisor: isSupervisor,
	}
	// 缺省授权：把新 agent 授权给创建者（谁建的谁能调）
	return m.repo.CreateAgent(m.auth(), inst, m.agentID)
}

// List 列出所有 agent 实例的简要信息。
func (m *agentManager) List(_ context.Context) ([]tools.AgentInfo, error) {
	// Limit: -1 = 取全部（不再受 1000 条隐含上限约束）
	instances, _, err := m.repo.ListAgents(m.auth(), &agents.ListAgentsRequest{Page: 1, Limit: -1})
	if err != nil {
		return nil, err
	}
	out := make([]tools.AgentInfo, 0, len(instances))
	for _, inst := range instances {
		out = append(out, tools.AgentInfo{
			ID:                inst.ID,
			Name:              inst.Name,
			Description:       string(inst.Description),
			Status:            int(inst.Status),
			IsSupervisor:      inst.IsSupervisor,
			SkillDiscovery:    inst.SkillDiscovery,
			ToolsUnrestricted: inst.ToolsUnrestricted,
		})
	}
	return out, nil
}

// Exists 判断 name 是否已存在（精确匹配）。
func (m *agentManager) Exists(ctx context.Context, name string) (bool, error) {
	inst, err := m.getByName(ctx, name)
	if err != nil {
		return false, err
	}
	return inst != nil, nil
}

// Get 按 name 精确取 agent 简要信息（含 id/status）；不存在返回 (nil, nil)。
func (m *agentManager) Get(ctx context.Context, name string) (*tools.AgentInfo, error) {
	inst, err := m.getByName(ctx, name)
	if err != nil {
		return nil, err
	}
	if inst == nil {
		return nil, nil
	}
	return &tools.AgentInfo{
		ID:                inst.ID,
		Name:              inst.Name,
		Description:       string(inst.Description),
		Status:            int(inst.Status),
		IsSupervisor:      inst.IsSupervisor,
		SkillDiscovery:    inst.SkillDiscovery,
		ToolsUnrestricted: inst.ToolsUnrestricted,
	}, nil
}

// ensureNameFree 校验 name 未被其它 agent / 工作流占用（excludeAgentID 非空时忽略自身）。
// 调用方必须在任何写库 / 目录迁移之前调用，保证校验失败时数据库与工作区都不动。
func (m *agentManager) ensureNameFree(ctx context.Context, name, excludeAgentID string) error {
	inst, err := m.getByName(ctx, name)
	if err != nil {
		return err
	}
	if inst != nil && inst.ID != excludeAgentID {
		return fmt.Errorf("agent %q already exists", name)
	}
	exist, err := m.flowRepo.ExistsFlowName(m.auth(), name)
	if err != nil {
		return err
	}
	if exist {
		return fmt.Errorf("workflow %q already exists; agent name must be unique against workflows", name)
	}
	return nil
}

// getByName 按 name 精确取实例（不存在返回 nil）。
// 注意 ListAgents 的 Name 是 ILIKE 模糊匹配，需再精确比对；比对大小写不敏感。
//
// Limit 必须取 -1（全部）：仓储按 name 升序返回，带 LIMIT 时精确匹配可能被排在
// 窗外 —— 租户里有 writer / writer-1 … writer-9 时，查 "writer-5" 只拿到前 N 条，
// 于是一个存在的 agent 被判定为「不存在」（同 flowsManager.find 的做法）。
func (m *agentManager) getByName(_ context.Context, name string) (*models.Agent, error) {
	instances, _, err := m.repo.ListAgents(m.auth(), &agents.ListAgentsRequest{Name: name, Page: 1, Limit: -1})
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

// Update 按 name 更新描述或改名（改名同步迁移定义域目录），返回最终名。
func (m *agentManager) Update(ctx context.Context, name, newName, description string) (string, error) {
	inst, err := m.getByName(ctx, name)
	if err != nil {
		return "", err
	}
	if inst == nil {
		return "", fmt.Errorf("agent %q not found", name)
	}
	// 状态闸 + 保护闸（判定真源见包内 ensureAgent* 函数）
	if err := ensureAgentMutable(inst); err != nil {
		return "", err
	}
	if err := ensureAgentRenamable(inst, newName); err != nil {
		return "", err
	}

	finalName := inst.Name
	if newName != "" && newName != inst.Name {
		if !tools.ValidName(newName) {
			return "", fmt.Errorf("invalid agent name %q (只允许字母数字横线)", newName)
		}
		// 先校验重名（其它 agent + 工作流），再写库 / 迁移工作区
		if err := m.ensureNameFree(ctx, newName, inst.ID); err != nil {
			return "", err
		}
		inst.Name = newName
		finalName = newName
	}
	if description != "" {
		inst.Description = xsql.String(description)
	}
	// 改名 → 迁移「草稿目录 + 版本域」，**先搬盘、再写库**（盘是内容，库是索引）。
	// 版本目录按名字键：先写库后搬盘，一旦搬盘失败，正式调用会按新名拼出一个空版本目录。
	renamed := finalName != name
	if renamed {
		if rerr := paths.MoveEntityStorage(m.store, m.schema, paths.VersionAgents, name, finalName); rerr != nil {
			return "", fmt.Errorf("agent dir rename failed: %w", rerr)
		}
	}
	if err := m.repo.UpdateAgent(m.auth(), inst.ID, inst); err != nil {
		if renamed {
			// 库没改成 → 把盘搬回去，别留「盘新名、库旧名」的半套
			if rerr := paths.MoveEntityStorage(m.store, m.schema, paths.VersionAgents, finalName, name); rerr != nil {
				xlog.Error("回滚定义域/版本目录失败 schema=%s %s→%s: %v", m.schema, finalName, name, rerr)
			}
		}
		return "", err
	}
	return finalName, nil
}

// SetUnrestricted 设置目标 agent（按 uuid）的「不限制工具」开关（agents.tools_unrestricted）。
//
// 语义（见 toolgrant/resolve.go）：候选（已注入 skill 声明的工具依赖）非空时不再被 allow 收窄 ——
// 它放宽的是 allow 的过滤作用，不是「无脑开全部工具」。
// D5（修订）：超级智能体可以给自己设 —— 它即管理员；边界由 status 承担：废弃的 agent
// （含超级智能体自己）不接受任何写入，而改 status 只有管理端能做，故不会自我解锁。
func (m *agentManager) SetUnrestricted(_ context.Context, agentID string, v bool) error {
	if !m.isSupervisor {
		return fmt.Errorf("仅超级智能体可修改 agent 的工具授权")
	}
	if agentID == "" {
		return fmt.Errorf("target agent id is empty")
	}
	return m.repo.SetUnrestricted(m.auth(), agentID, v)
}

// SetSupervisor 设置目标 agent（按 uuid）的超级权限属性（agents.is_supervisor）。
//
// 仅 supervisor 可改：非 supervisor 连管理类工具都调不到，这里是纵深防御。
// 可以是自己 —— 降级自己时由仓储拦「最后一个 supervisor」，否则租户会永久失去管理面。
// 目标须处于正常态（与 D5 一致：废弃不接受属性变更）。
func (m *agentManager) SetSupervisor(_ context.Context, agentID string, v bool) error {
	if !m.isSupervisor {
		return fmt.Errorf("仅 supervisor 可修改 agent 的超级权限属性")
	}
	if agentID == "" {
		return fmt.Errorf("target agent id is empty")
	}
	inst, err := m.repo.GetAgent(m.auth(), agentID)
	if err != nil {
		return err
	}
	if inst == nil {
		return fmt.Errorf("agent %q not found", agentID)
	}
	if err := ensureAgentMutable(inst); err != nil {
		return err
	}
	return m.repo.SetSupervisor(m.auth(), agentID, v)
}

// Delete 按 name 删除 agent（同时清理其定义域目录）。
func (m *agentManager) Delete(ctx context.Context, name string) error {
	inst, err := m.getByName(ctx, name)
	if err != nil {
		return err
	}
	if inst == nil {
		return fmt.Errorf("agent %q not found", name)
	}
	// 保护闸 + 状态闸
	if err := ensureAgentDeletable(inst); err != nil {
		return err
	}
	// 删除数据：私有 skill 记录、授权关联与 agents 行由仓储在同一事务内完成
	// （skills.agent_id 没有外键，级联只能由应用层保证）。
	if err := m.repo.DeleteAgent(m.auth(), inst.ID); err != nil {
		return err
	}
	// 定义域目录 + 版本域一起清（DB cascade 删不掉盘）。
	// 删不掉只记日志：库是索引，盘上的残留由启动清扫兜底 —— 不能把一次已经
	// 生效的删除报成失败（与 admin/agents 的删除口径一致）。
	if err := paths.DeleteEntityStorage(m.store, m.schema, paths.VersionAgents, name); err != nil {
		xlog.Error("清理定义域/版本目录失败 schema=%s name=%s: %v", m.schema, name, err)
	}
	return nil
}

// ensureAgentMutable 状态闸：只有正常态允许改定义（唯一真源：models.IsModifiable）。
func ensureAgentMutable(inst *models.Agent) error {
	if !inst.IsModifiable() {
		return fmt.Errorf("agent %q is deprecated; switch it back to normal first", inst.Name)
	}
	return nil
}

// ensureAgentRenamable 保护闸：受保护 agent 不允许改名。
func ensureAgentRenamable(inst *models.Agent, newName string) error {
	if models.IsProtectedAgent(inst.Name) && newName != "" && newName != inst.Name {
		return fmt.Errorf("protected agent %q cannot be renamed", inst.Name)
	}
	return nil
}

// ensureAgentDeletable 保护闸 + 状态闸。
func ensureAgentDeletable(inst *models.Agent) error {
	if models.IsProtectedAgent(inst.Name) {
		return fmt.Errorf("protected agent %q cannot be deleted", inst.Name)
	}
	return ensureAgentMutable(inst)
}
