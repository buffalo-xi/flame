package agent

import (
	"flame/nexus/service/agent/core"
	"flame/nexus/service/agent/tools"
	"flame/nexus/shared/models"
)

// NewAgent 创建 core.Agent 实现。
// 目前只有一种实现（agentImpl：prompt 取自工作区的 AGENT.md）。
// 将来若新增实现方式（如容器化），在此按类型分派。
func NewAgent(agent *models.Agent, schema string, arguments map[string]interface{}) (core.Agent, error) {
	return newAgentImpl(agent, schema, arguments)
}

// DefinitionModeKey arguments 里的保留键：本次运行读草稿还是读当前版本。
//
// 与 user_id / session_id / _call_depth 同一类东西：入口层注入、运行时消费，
// 不属于模型可见的业务入参（core.StripSessionID 这类清洗只针对业务键，别把它混进去）。
const DefinitionModeKey = "_definition_mode"

// SetReleaseMode 标记本次运行读**已发布的当前版本**（正式入口用）。
//
// 只能在正式入口调用：RPC / MCP / 端点 / flow 的 agent 节点（非调试）/ call_agent 透传。
// 编辑页调试、试聊、单测保持缺省（草稿）—— 它们要看到「正在改的那份」。
func SetReleaseMode(arguments map[string]interface{}) {
	if arguments == nil {
		return
	}
	arguments[DefinitionModeKey] = string(tools.ModeCurrent)
}

// SetDraftMode 显式标记读草稿（调试器用；等价于缺省，写出来是为了可读与可审计）。
func SetDraftMode(arguments map[string]interface{}) {
	if arguments == nil {
		return
	}
	arguments[DefinitionModeKey] = string(tools.ModeDraft)
}

// FollowDraftsKey arguments 里的保留键：本次运行**调起的子实体**（agent / flow）
// 读草稿还是已发布版本。只管「子」，不管「自己」—— 自己读哪份由 DefinitionModeKey 决定。
//
// 缺省 true（与 flow 调试器的「跟随依赖草稿」同一缺省）；只有自身为草稿时才有意义：
// 正式入口即使传 true 也不会让子实体读草稿，判据见 tools.Context.ChildMode。
const FollowDraftsKey = "_follow_drafts"

// SetFollowDrafts 写入「子调用是否跟随草稿」。正式入口不必调用（ChildMode 会强制 false），
// 聊天入口按用户当轮的选择注入。
func SetFollowDrafts(arguments map[string]interface{}, follow bool) {
	if arguments == nil {
		return
	}
	arguments[FollowDraftsKey] = follow
}

// resolveDefinitionMode 本次运行「自己」读哪份定义。
//
// 超级智能体恒读草稿：它的全部职责就是改草稿（建 agent / skill / flow），
// 读发布版会让它看不见自己刚建的东西 —— 与入口无关，MCP / RPC 进来也一样。
func resolveDefinitionMode(raw string, isSupervisor bool) tools.DefinitionMode {
	if isSupervisor {
		return tools.ModeDraft
	}
	if tools.DefinitionMode(raw) == tools.ModeCurrent {
		return tools.ModeCurrent
	}
	return tools.ModeDraft
}

// resolveFollowDrafts 本次运行「调起的子实体」读哪份定义。三条规则，顺序不能换：
//  1. supervisor 恒 true —— 它建的东西还没发布，必须能立刻调起来验证；
//  2. 自身是正式运行时 false —— 草稿不允许从正式链扩散；
//  3. 其余看开关，缺省 true（与 flow 调试器的 FollowDrafts 同一缺省）。
func resolveFollowDrafts(selfMode tools.DefinitionMode, raw any, isSupervisor bool) bool {
	if isSupervisor {
		return true
	}
	if selfMode.IsCurrent() {
		return false
	}
	if v, ok := raw.(bool); ok {
		return v
	}
	return true
}
