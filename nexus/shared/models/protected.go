package models

import "sort"

// 本文件承载两类**语义正交**的判定，勿混用：
//
//  1. 身份判据 IsSupervisor —— 谁拥有超级权限。判据是**实体属性**
//     （agents.is_supervisor），不再按名字查白名单：main 只是预置的一个
//     supervisor，租户可以再配出同等权限的 agent，且允许多个。
//  2. 删除/改名保护 IsProtectedAgent / IsProtectedSkill —— 哪些资源不允许
//     删除、不允许改名（内容与描述仍可改）。名单只收**预置资产**。
//
// 历史上身份判据叫 IsMainAgent(name)，与保护名单取值相同，导致「给保护名单加
// 第二个名字就把它误判成超权」。现在两者彻底分开：
//
//	main           → is_supervisor = true（迁移脚本置位）+ 在保护名单里（预置）
//	新建的 supervisor → is_supervisor = true，但**不在**保护名单：可删可改名
//
// 为什么不保护所有 supervisor：保护名单是「删了没有恢复路径」的清单，
// 而 supervisor 属性本身可以重新授予。真正的风险（租户失去全部管理能力）
// 由业务层的「最后一个 supervisor 不可降级」兜住，不靠名字名单。

// protectedAgents 预置 agent 名单：不允许删除、不允许改名。
//
// 只收建租户时物化的资产（data/tenants/*/agents/main 是现成知识库，
// 删掉没有重建路径）。往这里加名字要慎重 —— 加了就永远删不掉。
var protectedAgents = map[string]struct{}{
	"main": {},
}

// protectedSkills 不允许删除、不允许改名的 skill 名单。
// 四者都是「元能力」的知识来源，是系统基石：
//   - skill-authoring        → 如何创建 / 维护 skill
//   - agent-authoring        → 如何创建 / 维护 agent
//   - custom-tool-authoring  → 如何把业务逻辑落成自定义工具（custom__*）
//   - flow-authoring         → 如何创建 / 维护工作流，并对外开放为 mcp / rpc 端点
var protectedSkills = map[string]struct{}{
	"skill-authoring":       {},
	"agent-authoring":       {},
	"custom-tool-authoring": {},
	"flow-authoring":        {},
}

// IsSupervisor 身份判据：该 agent 是否拥有超级权限（nil 安全，返回 false）。
//
// 唯一真源是实体字段 —— 不要再引入按名字判定，否则两种口径必然分叉。
func IsSupervisor(a *Agent) bool {
	return a != nil && a.IsSupervisor
}

// IsProtectedAgent 判断智能体是否受删除/改名保护（预置资产名单）。
func IsProtectedAgent(name string) bool {
	_, ok := protectedAgents[name]
	return ok
}

// CanDemoteSupervisor 是否允许撤销该 agent 的超权属性（agents.is_supervisor）。
//
// 受保护 agent（protectedAgents，目前只有 main）永远不允许降级。理由与「最后一个
// supervisor 不可降级」不同：
//   - 「最后一个」是数量约束，防的是租户整体失去管理面（有第二个就能降）；
//   - 这一条是资产约束，防的是 main 这个名字变成一个「占着入口却没有权限」的死角 ——
//     它既不能改名也不能删除，一旦降级就再也无法通过改名 / 重建恢复成预置的管理入口。
//
// 所以 main 的 supervisor 属性是恒定的：可改描述、可改定义域文件、可改工具授权，
// 唯独不能失去超权。
func CanDemoteSupervisor(name string) bool {
	return !IsProtectedAgent(name)
}

// IsProtectedSkill 判断 skill 是否受删除 / 改名保护。
func IsProtectedSkill(name string) bool {
	_, ok := protectedSkills[name]
	return ok
}

// ProtectedSkillNames 返回受保护的 skill 名（按字典序，只读副本）。
//
// 用途：租户创建资源方案里，模板包的 seed.sql / files 必须与这份名单同步 ——
// 校验「包里的技能集合 == 保护名单」时用它，别在调用方另抄一份字面量，
// 否则加一个保护技能就会出现「受保护但没被打包」的窟窿。
func ProtectedSkillNames() []string {
	names := make([]string, 0, len(protectedSkills))
	for n := range protectedSkills {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
