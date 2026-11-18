package toolgrant

import (
	"fmt"
	"sort"

	"flame/nexus/shared/models"
)

// DefaultMaxTools 最终绑定的工具数量上限（超出截断并告警）。
const DefaultMaxTools = 50

// VisibleSkill **本轮已激活**的 skill（模型 use_skill 读过正文；由调用方提供，避免与 skills 包循环依赖）。
// 只「绑定」不算：绑定只决定 system prompt 里有没有它这一条注释，不产生工具候选。
type VisibleSkill struct {
	ID   string
	Name string
	// Deps 激活那一刻该技能声明的工具依赖快照（正式运行下取自当前版本，见 skillManager.Get）。
	//
	// nil = 调用方没给快照 → 回落查 skill_tool_grants（管理端预览、无版本体系的老口径）；
	// 非 nil（含空切片）= 权威快照：版本里没声明依赖就不再读实时表。
	// 这条区分很关键 —— 只冻展示（read_skill 的 tool_deps）不冻生效，草稿里加一条依赖
	// 就会在正式运行里被点亮，等于冻结没冻到底。
	Deps []string
}

// ResolveResult 解析结果（可同时用于运行时绑定、UI 预览与审计）。
type ResolveResult struct {
	AgentID       string     `json:"agent_id"`
	Unrestricted  bool       `json:"unrestricted"`  // 不限制工具
	Degraded      bool       `json:"degraded"`      // 候选为空 → allow 授权全部未生效（空转）
	RequiredRefs  []string   `json:"required_refs"` // 必须导入（展开后的工具 ref）
	AllowRefs     []string   `json:"allow_refs"`
	DenyRefs      []string   `json:"deny_refs"`
	CandidateRefs []string   `json:"candidate_refs"` // 可见 skill 声明的工具（展开后）
	FinalRefs     []string   `json:"final_refs"`     // 最终生效的工具 ref
	FinalNames    []string   `json:"final_names"`    // 对应的 LLM 工具名
	FinalTools    []ToolInfo `json:"final_tools"`
	Warnings      []string   `json:"warnings"`

	// 以下两项仅由管理端预览填充：未配置授权（且未开启不限制）时运行时并不使用本解析结果，
	// 而是 —— 内置超级智能体缺省拥有全部内置工具；其他 agent 不绑定任何工具。预览需如实反映。
	NoGrants       bool `json:"no_grants"`       // true = 未配置任何授权项
	BuiltinDefault bool `json:"builtin_default"` // true = 内置超级智能体的缺省全开（全部内置工具）
}

// Resolver 解析某 agent 的最终工具集。
type Resolver struct {
	Catalog       *Catalog
	Schema        string
	VisibleSkills []VisibleSkill
	// Unrestricted 「不限制工具」开关（agents.tools_unrestricted）。
	// 它是 agent 自身的属性，由调用方读实例后传入 —— Resolver 不自己去仓储取，
	// 免得把「读哪个仓储」这件与授权解析无关的事带进来。
	Unrestricted bool
	MaxTools     int
}

// NewResolver 创建解析器（MaxTools 为 0 时使用 DefaultMaxTools）。
func NewResolver(catalog *Catalog, schema string, visible []VisibleSkill, unrestricted bool) *Resolver {
	return &Resolver{
		Catalog:       catalog,
		Schema:        schema,
		VisibleSkills: visible,
		Unrestricted:  unrestricted,
		MaxTools:      DefaultMaxTools,
	}
}

// Resolve 执行解析：
//
//	required 恒注入（唯一能独立生效的授权）；
//	候选（**本轮已激活**技能声明的工具依赖）非空时：required ∪ (候选 ∩ allow)；
//	开关「不限制工具」时：required ∪ 候选（allow 不参与判定）；
//	候选为空时：仅 required —— allow 一律不注入（它是候选的过滤器，没有候选就无从过滤）；
//	最后减去 deny（deny 不动 required），再做可用性过滤与预算截断。
func (r *Resolver) Resolve(agentID string) (*ResolveResult, error) {
	grants, err := r.Catalog.grants.ListAgentGrants(r.Schema, agentID)
	if err != nil {
		return nil, err
	}

	res := &ResolveResult{AgentID: agentID, Unrestricted: r.Unrestricted}

	refsByKind := map[string][]string{}
	for _, g := range grants {
		refsByKind[g.GrantKind] = append(refsByKind[g.GrantKind], g.Ref)
	}
	required, _, w := r.expandToolRefs(refsByKind[models.GrantKindRequired])
	res.Warnings = append(res.Warnings, w...)
	allow, _, w := r.expandToolRefs(refsByKind[models.GrantKindAllow])
	res.Warnings = append(res.Warnings, w...)
	deny, _, w := r.expandToolRefs(refsByKind[models.GrantKindDeny])
	res.Warnings = append(res.Warnings, w...)

	// 候选：可见 skill 的工具依赖。
	// 优先用激活时带来的快照（正式运行 = 当前版本的声明）；没有快照才查实时表。
	// 依赖是**声明**，属定义，必须跟着版本走；能不能真调到仍由上面的 allow / deny 收窄。
	candidateRaw := make([]string, 0)
	for _, s := range r.VisibleSkills {
		if s.Deps != nil {
			candidateRaw = append(candidateRaw, s.Deps...)
			continue
		}
		list, err := r.Catalog.grants.ListSkillGrants(r.Schema, s.ID)
		if err != nil {
			return nil, err
		}
		for _, g := range list {
			candidateRaw = append(candidateRaw, g.Ref)
		}
	}
	candidates, candOrdered, w := r.expandToolRefs(candidateRaw)
	res.Warnings = append(res.Warnings, w...)
	// 声明顺序：截断时「先声明的先保留」，不再让字母序决定谁被切掉
	candOrder := make(map[string]int, len(candOrdered))
	for i, ref := range candOrdered {
		candOrder[ref] = i
	}

	finalSet := map[string]bool{}
	for ref := range required {
		finalSet[ref] = true
	}

	// allow 空转：候选为空时 allow 在任何分支下都不生效 —— 它只是「skill 声明的需求」的
	// 过滤器，没有需求就无从过滤（unrestricted 分支根本不看 allow，同样空转）。
	// 单独判并告警，否则管理员只会看到「勾了工具却没生效」而无从知道原因。
	if len(candidates) == 0 && len(allow) > 0 {
		res.Degraded = true
		res.Warnings = append(res.Warnings, "该 agent 的 allow 授权本轮全部未生效：没有任何已激活（use_skill）的技能声明工具依赖")
	}

	// 候选 → 生效：不限制工具时全给；否则由 allow 过滤。
	// 候选为空则一个都不加，只剩 required —— 要让工具生效只有两条路：
	// required 物化，或写进某个「已注入 skill」的工具依赖。
	for ref := range candidates {
		if r.Unrestricted || allow[ref] {
			finalSet[ref] = true
		}
	}

	// deny 优先，但不动 required
	for ref := range deny {
		if !required[ref] {
			delete(finalSet, ref)
		}
	}

	// 可用性过滤 + 生成 LLM 工具名
	all, err := r.Catalog.AllTools()
	if err != nil {
		return nil, err
	}
	finalTools := make([]ToolInfo, 0, len(finalSet))
	for ref := range finalSet {
		info, ok := all[ref]
		if !ok {
			res.Warnings = append(res.Warnings, "引用的工具已不存在："+ref)
			continue
		}
		if !info.Available {
			// 原因按归因分开（平台未开通能力 / 插件被停用 / 连接停用 / 端点未对外开放）：
			// 一句笼统的「连接/端点未启用」会把平台闸门也误报成租户自己的配置问题。
			why := info.UnavailableReason
			if why == "" {
				why = "工具所属连接/端点未启用"
			}
			res.Warnings = append(res.Warnings, "本次不绑定："+ref+"（"+why+"）")
			continue
		}
		finalTools = append(finalTools, info)
	}
	// 排序即截断优先级：required 恒在最前（绝不因技能声明的工具多而被切），
	// 其余按技能声明顺序（先声明的先保留），最后才用名字兜底保证确定性。
	// 旧实现按**名字字母序**截断 —— 多声明几个工具就可能把真正要用的那批静默切掉。
	rank := func(ref string) int {
		if required[ref] {
			return 0
		}
		if i, ok := candOrder[ref]; ok {
			return 1 + i
		}
		return 1 << 20
	}
	sort.Slice(finalTools, func(i, j int) bool {
		ri, rj := rank(finalTools[i].Ref), rank(finalTools[j].Ref)
		if ri != rj {
			return ri < rj
		}
		return finalTools[i].Name < finalTools[j].Name
	})

	max := r.MaxTools
	if max == 0 {
		max = DefaultMaxTools
	}
	if max > 0 && len(finalTools) > max {
		dropped := len(finalTools) - max
		res.Warnings = append(res.Warnings, fmt.Sprintf(
			"工具数量超过上限 %d，已按「required 优先、技能声明顺序」截断（丢弃 %d 个）", max, dropped))
		finalTools = finalTools[:max]
	}

	res.RequiredRefs = sortedKeys(required)
	res.AllowRefs = sortedKeys(allow)
	res.DenyRefs = sortedKeys(deny)
	res.CandidateRefs = sortedKeys(candidates)
	res.FinalTools = finalTools
	for _, t := range finalTools {
		res.FinalRefs = append(res.FinalRefs, t.Ref)
		res.FinalNames = append(res.FinalNames, t.Name)
	}
	return res, nil
}

// expandToolRefs 把 ref 列表展开为去重的「工具 ref」集合，并收集告警（引用不存在/非法）。
// 第二个返回值是**声明顺序**（去重）列表，供截断时排优先级用。
func (r *Resolver) expandToolRefs(refs []string) (map[string]bool, []string, []string) {
	out := map[string]bool{}
	ordered := make([]string, 0, len(refs))
	warnings := make([]string, 0)
	for _, ref := range refs {
		if err := ValidateRef(ref, true); err != nil {
			warnings = append(warnings, "忽略非法引用 "+ref+"："+err.Error())
			continue
		}
		list, err := r.Catalog.ToolsOfRef(ref)
		if err != nil {
			warnings = append(warnings, "展开引用失败 "+ref+"："+err.Error())
			continue
		}
		if len(list) == 0 {
			warnings = append(warnings, "引用无匹配工具（可能已被删除）："+ref)
			continue
		}
		for _, t := range list {
			if out[t.Ref] {
				continue
			}
			out[t.Ref] = true
			ordered = append(ordered, t.Ref)
		}
	}
	return out, ordered, warnings
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
