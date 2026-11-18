package agent

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"flame/nexus/service/agent/toolgrant"
	"flame/nexus/service/agent/tools"
	"flame/nexus/shared/auth"
	"flame/nexus/shared/capabilitypolicy"
	agents "flame/nexus/shared/repository/agents"
	"flame/nexus/shared/repository/toolgrants"
	"flame/tools/xlog"
)

// toolSearchLimit 每类（组 / 工具）每次回多少条上限。
//
// 这是**上下文预算**而不是性能取舍：结果整段进入模型上下文，一次倒出上百个工具
// 既挤爆窗口又用不上。超限时置 Truncated 并提示收窄 query。
const toolSearchLimit = 20

// groupMemberPreview 组卡片里回显的成员 ref 上限。
//
// 与 toolSearchLimit 同源考虑：成员摘要也要占上下文。给组概览的目的只是让模型
// 「授整组之前先看一眼里面是什么」，12 条足够判断类型（文件类 / 连接类 / 混合），
// 真实总数由 Count 承担，要看全量再用 query 展开。
const groupMemberPreview = 12

// toolCatalogManager 实现 tools.ToolCatalogManager：把「可授权目录」摊成可检索的列表。
//
// 数据源就是管理端授权面板用的那份 Catalog，所以「agent 查得到什么」与「面板上看到什么」
// 永远一致 —— 不会再出现「面板里有、agent 摸不到」的错位。
type toolCatalogManager struct {
	schema       string
	agentName    string
	isSupervisor bool // 当前（调用者）agent 的身份，目标未指定时用它
}

var _ tools.ToolCatalogManager = (*toolCatalogManager)(nil)

func newToolCatalogManager(schema, agentName string, isSupervisor bool) *toolCatalogManager {
	return &toolCatalogManager{schema: schema, agentName: agentName, isSupervisor: isSupervisor}
}

// Search 检索可授权的组与工具。
//
// 视角取**目标 agent**（可能与调用者不同）：非超级智能体的候选里剔掉 super 类工具，
// 因为那些授了也执行不了（ExecuteTool 的执行层硬闸会拒），露出来只会诱导一次
// 必然失败的授权动作。
func (m *toolCatalogManager) Search(_ context.Context, query, agentName, agentID string) (*tools.ToolSearchResult, error) {
	if agentName == "" {
		agentName = m.agentName
	}
	// 视角按**目标**身份（可能与调用者不同）：非 supervisor 的候选里剔掉 super 类工具，
	// 因为那些授了也执行不了。agentID 为空（未指定目标）时回落调用者身份。
	isSupervisor := m.isSupervisor
	if agentID != "" {
		if inst, err := agents.NewRepository().GetAgent(&auth.AuthInfo{Schema: m.schema}, agentID); err == nil && inst != nil {
			isSupervisor = inst.IsSupervisor
		}
	}
	repo := toolgrants.NewRepository()
	cat := toolgrant.NewCatalog(m.schema, repo, isSupervisor).
		WithCapabilities(capabilitypolicy.Snapshot(m.schema))

	all, err := cat.AllTools()
	if err != nil {
		return nil, err
	}
	groups, err := cat.Groups()
	if err != nil {
		return nil, err
	}

	// 目标 agent 当前已授权的 ref：只用于展示（避免重复授权、让「授过什么」可见）。
	// 读失败不致命 —— 宁可少一列状态，也别让「查目录」整体失败。
	granted := make(map[string]string)
	if agentID != "" {
		list, gerr := repo.ListAgentGrants(m.schema, agentID)
		if gerr != nil {
			xlog.Info("search_tools 读取授权状态失败，按未授权展示 schema=%s agent=%s err=%v",
				m.schema, agentName, gerr)
		} else {
			for _, g := range list {
				granted[g.Ref] = g.GrantKind
			}
		}
	}

	return searchCatalog(query, all, groups, granted), nil
}

// searchCatalog 纯函数：目录 + 授权现状 → 检索结果（不碰库，便于单测）。
func searchCatalog(query string, all map[string]toolgrant.ToolInfo, groups []toolgrant.GroupInfo, granted map[string]string) *tools.ToolSearchResult {
	q := strings.ToLower(strings.TrimSpace(query))
	res := &tools.ToolSearchResult{
		Query:  q,
		Groups: []tools.ToolSearchItem{},
		Tools:  []tools.ToolSearchItem{},
	}

	var truncatedGroups []string
	for _, g := range groups {
		// 合成容器（plugin:* / custom:*）不是可授权对象：ref 会被 ValidateRef 拒，
		// 它们的成员各自以工具 ref 出现在下面的列表里。
		if g.Synthetic {
			continue
		}
		if !matchQuery(q, g.Ref, g.Name, g.Description) {
			continue
		}
		members, cut := memberRefs(g.Items, groupMemberPreview)
		if cut {
			truncatedGroups = append(truncatedGroups, g.Ref)
		}
		res.Groups = append(res.Groups, tools.ToolSearchItem{
			Ref:               g.Ref,
			Label:             g.Name,
			Kind:              g.Kind,
			Description:       g.Description,
			Count:             len(g.Items),
			Members:           members,
			Available:         g.Available,
			UnavailableKind:   g.UnavailableKind,
			UnavailableReason: g.UnavailableReason,
			Granted:           granted[g.Ref],
		})
	}

	// 空 query 只给组概览：工具可能有上百个，全倒进上下文既没用又挤占窗口。
	if q != "" {
		for _, t := range all {
			if !matchQuery(q, t.Ref, t.Name, t.Label, t.Description) {
				continue
			}
			res.Tools = append(res.Tools, tools.ToolSearchItem{
				Ref:               t.Ref,
				Name:              t.Name,
				Label:             t.Label,
				Kind:              kindOfRef(t.Ref),
				Description:       t.Description,
				Available:         t.Available,
				UnavailableKind:   t.UnavailableKind,
				UnavailableReason: t.UnavailableReason,
				SupervisorOnly:    t.SupervisorOnly,
				Granted:           granted[t.Ref],
			})
		}
	}

	// map 迭代无序 → 必须自己排。否则同样一次查询两次结果顺序不同，
	// 模型会以为目录变了。
	sortSearchItems(res.Groups)
	sortSearchItems(res.Tools)
	if len(res.Groups) > toolSearchLimit {
		res.Groups = res.Groups[:toolSearchLimit]
		res.Truncated = true
	}
	if len(res.Tools) > toolSearchLimit {
		res.Tools = res.Tools[:toolSearchLimit]
		res.Truncated = true
	}
	if res.Truncated {
		res.Notes = append(res.Notes, "命中超过上限，只回了前 20 条；请给更精确的 query（连接名 / 工具名的一部分 / ref 片段）")
	}
	if len(truncatedGroups) > 0 {
		res.Notes = append(res.Notes, fmt.Sprintf(
			"%s 的成员超过 %d 个，只回了前 %d 个（count 仍是真实总数）；要全部成员请用 query 匹配该组名展开",
			strings.Join(truncatedGroups, "、"), groupMemberPreview, groupMemberPreview))
	}
	if q == "" && len(res.Groups) > 0 {
		res.Notes = append(res.Notes,
			"未给 query 时只列组概览（组 ref + 成员数 + 前几个成员 ref）；要看某个组的全部工具，用 query 匹配（如 fs、read_file）")
	}
	return res
}

// memberRefs 取组成员前 limit 个授权 ref，第二个返回值表示是否被截断。
// 只回 ref 不回描述：组摘要的用途是「看一眼里面是什么类型」，描述留给 query 展开时再给。
func memberRefs(items []toolgrant.ToolInfo, limit int) ([]string, bool) {
	if len(items) == 0 {
		return nil, false
	}
	n := len(items)
	cut := false
	if n > limit {
		n, cut = limit, true
	}
	out := make([]string, 0, n)
	for _, t := range items[:n] {
		out = append(out, t.Ref)
	}
	return out, cut
}

// matchQuery 大小写不敏感的「任一字段包含」。
func matchQuery(q string, fields ...string) bool {
	if q == "" {
		return true
	}
	for _, f := range fields {
		if f != "" && strings.Contains(strings.ToLower(f), q) {
			return true
		}
	}
	return false
}

// sortSearchItems 可用优先，其次按 ref 字典序（保证同样输入输出稳定）。
func sortSearchItems(items []tools.ToolSearchItem) {
	sort.Slice(items, func(i, j int) bool {
		if items[i].Available != items[j].Available {
			return items[i].Available
		}
		return items[i].Ref < items[j].Ref
	})
}

// kindOfRef 取 ref 的 scope 段（builtin / mcp / rpc / local / plugin / script / custom / preset）。
func kindOfRef(ref string) string {
	if p, err := toolgrant.ParseRef(ref); err == nil {
		return p.Scope
	}
	return ""
}
