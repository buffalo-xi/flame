package toolgrant

import (
	"fmt"
	"sort"
	"strings"

	"flame/nexus/shared/models"
)

// DefaultMaxTools is the cap on how many tools end up bound (beyond it the list is truncated with a warning).
//
// It mirrors shared/agentconfig's code fallback: the supervisor's 20 resident tools plus every shipped on-demand group
// (36) must fit in one run, so a cap below 56 truncates a group the skill was activated for.
const DefaultMaxTools = 60

// VisibleSkill is a skill **activated in this run** (the model read its body with use_skill; supplied by the caller to avoid an import
// cycle with the skills package).
// A mere binding does not count: a binding only decides whether the skill gets an entry in the system prompt, and produces no tool
// candidates.
type VisibleSkill struct {
	ID   string
	Name string
	// Deps is the snapshot of the tool dependencies the skill declared at activation time (in a production run it comes from the
	// current version, see skillManager.Get).
	//
	// nil = the caller supplied no snapshot -> fall back to querying skill_tool_grants (admin preview, the legacy criterion without a
	// version system);
	// non-nil (an empty slice included) = authoritative snapshot: the live table is not read once the version declares no
	// dependencies.
	// The distinction matters - freezing only the display (read_skill's tool_deps) without freezing what takes effect would let a
	// dependency added in the draft light up in production, so the freeze would not be deep enough.
	Deps []string
}

// ResolveResult is the resolution result (usable for runtime binding, UI preview and auditing at once).
type ResolveResult struct {
	AgentID       string     `json:"agent_id"`
	Unrestricted  bool       `json:"unrestricted"`  // tools are unrestricted
	Degraded      bool       `json:"degraded"`      // candidates empty -> every allow grant is ineffective (spinning)
	RequiredRefs  []string   `json:"required_refs"` // must be imported (expanded tool refs)
	AllowRefs     []string   `json:"allow_refs"`
	DenyRefs      []string   `json:"deny_refs"`
	CandidateRefs []string   `json:"candidate_refs"` // tools declared by visible skills (expanded)
	FinalRefs     []string   `json:"final_refs"`     // the tool refs that take effect
	FinalNames    []string   `json:"final_names"`    // the corresponding LLM tool names
	FinalTools    []ToolInfo `json:"final_tools"`
	// Warnings are shown to an operator (admin preview) and relayed by the model; English for the same reason as
	// AgentGrantView.Note: resolution happens inside a run, with no caller language in hand.
	Warnings []string `json:"warnings"`

	// The next two are filled in by the admin preview only: with no grants configured (and unrestricted off) the runtime does not use
	// this result at all - the built-in super agent holds all built-in tools by default, and other agents bind none. The preview has to
	// show that faithfully.
	NoGrants       bool `json:"no_grants"`       // true = no grant item configured
	BuiltinDefault bool `json:"builtin_default"` // true = the built-in super agent's default all-on (every built-in tool)
}

// Resolver resolves an agent's final tool set.
type Resolver struct {
	Catalog       *Catalog
	Schema        string
	VisibleSkills []VisibleSkill
	// Unrestricted is the "tools are unrestricted" switch (agents.tools_unrestricted).
	// It is an attribute of the agent itself, passed in by the caller after reading the instance - the Resolver does not go to the
	// repository itself, so that "which repository to read" (irrelevant to grant resolution) is not dragged in.
	Unrestricted bool
	MaxTools     int
}

// NewResolver creates a resolver (a MaxTools of 0 means DefaultMaxTools).
func NewResolver(catalog *Catalog, schema string, visible []VisibleSkill, unrestricted bool) *Resolver {
	return &Resolver{
		Catalog:       catalog,
		Schema:        schema,
		VisibleSkills: visible,
		Unrestricted:  unrestricted,
		MaxTools:      DefaultMaxTools,
	}
}

// Resolve performs the resolution:
//
//	required is always injected (the only grant that can take effect on its own);
//	when the candidates (tool dependencies declared by **skills activated in this run**) are non-empty: required ∪ (candidates ∩ allow);
//	with the "unrestricted" switch on: required ∪ candidates (allow does not participate);
//	when the candidates are empty: required only - allow is never injected (it filters candidates, and there is nothing to filter);
//	finally subtract deny (deny never touches required), then apply the availability filter and the budget cap.
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

	// Candidates: the tool dependencies of visible skills.
	// The snapshot taken at activation is preferred (in a production run = the declaration of the current version); the live table is
	// only queried when there is no snapshot.
	// A dependency is a **declaration**, part of the definition, and must follow the version; whether it can actually be called is
	// still narrowed by allow / deny above.
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
	// Declaration order: on truncation "declared first is kept first", so alphabetical order no longer decides who gets cut
	candOrder := make(map[string]int, len(candOrdered))
	for i, ref := range candOrdered {
		candOrder[ref] = i
	}

	finalSet := map[string]bool{}
	for ref := range required {
		finalSet[ref] = true
	}

	// allow spinning: with no candidates allow takes effect in no branch - it only filters "what skills declare", and with nothing
	// declared there is nothing to filter (the unrestricted branch does not look at allow either, so it spins too).
	// Checked and warned separately, otherwise the administrator only sees "I ticked the tool but it did not take effect" with no way
	// to know why.
	if len(candidates) == 0 && len(allow) > 0 {
		res.Degraded = true
		res.Warnings = append(res.Warnings, "every allow grant of this agent has no effect in this run: no activated (use_skill) skill declares a tool dependency")
	}

	// Candidates -> effective: all of them when unrestricted, otherwise filtered by allow.
	// With no candidates nothing is added and only required remains - there are exactly two ways for a tool to take effect:
	// materialise it as required, or declare it as a tool dependency of an injected skill.
	for ref := range candidates {
		if r.Unrestricted || allow[ref] {
			finalSet[ref] = true
		}
	}

	// deny wins, but never touches required
	for ref := range deny {
		if !required[ref] {
			delete(finalSet, ref)
		}
	}

	// Availability filter + LLM tool names
	all, err := r.Catalog.AllTools()
	if err != nil {
		return nil, err
	}
	finalTools := make([]ToolInfo, 0, len(finalSet))
	for ref := range finalSet {
		info, ok := all[ref]
		if !ok {
			res.Warnings = append(res.Warnings, "referenced tool no longer exists: "+ref)
			continue
		}
		if !info.Available {
			// The reason is split by attribution (the platform has not enabled the capability / the plugin is disabled / the connection
			// is disabled / the endpoint is not exposed): one blanket "connection / endpoint not enabled" would also misreport a
			// platform gate as the tenant's own configuration problem.
			why := info.UnavailableReason
			if why == "" {
				why = "the connection or endpoint that owns this tool is not enabled"
			}
			res.Warnings = append(res.Warnings, "not bound in this run: "+ref+" ("+why+")")
			continue
		}
		finalTools = append(finalTools, info)
	}
	// The sort order is the truncation priority: required always comes first (never cut just because a skill declares many tools),
	// the rest follow the caller's VisibleSkills order (see toolctx: most recently activated skill first, so the skill the model
	// just read keeps its tools), and only then the name as a tiebreaker for determinism.
	// The old implementation truncated by **alphabetical name** - declaring a few extra tools could silently cut the ones actually in
	// use.
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
		// Name what was dropped, not just how many: "dropped 6" leaves an operator to guess which six, and the list is the only
		// place that answers "why is run_flow missing" without replaying the resolution by hand.
		droppedRefs := make([]string, 0, len(finalTools)-max)
		for _, t := range finalTools[max:] {
			droppedRefs = append(droppedRefs, t.Ref)
		}
		res.Warnings = append(res.Warnings, fmt.Sprintf(
			"tool count exceeds the limit of %d; truncated by \"required first, then the most recently activated skill\" (dropped %d: %s)",
			max, len(droppedRefs), joinLimited(droppedRefs, 20)))
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

// expandToolRefs expands a list of refs into a de-duplicated set of "tool refs" and collects warnings (missing / illegal references).
// The second return value is the **declaration order** list (de-duplicated), used to rank truncation priority.
func (r *Resolver) expandToolRefs(refs []string) (map[string]bool, []string, []string) {
	out := map[string]bool{}
	ordered := make([]string, 0, len(refs))
	warnings := make([]string, 0)
	for _, ref := range refs {
		if err := ValidateRef(ref, true); err != nil {
			warnings = append(warnings, "ignoring an invalid ref "+ref+": "+err.Error())
			continue
		}
		list, err := r.Catalog.ToolsOfRef(ref)
		if err != nil {
			warnings = append(warnings, "failed to expand ref "+ref+": "+err.Error())
			continue
		}
		if len(list) == 0 {
			warnings = append(warnings, "ref matches no tools (it may have been deleted): "+ref)
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

// joinLimited joins at most limit entries and summarises the rest, so a truncation warning stays one readable log line even
// when a large group is cut.
func joinLimited(items []string, limit int) string {
	if len(items) <= limit {
		return strings.Join(items, ", ")
	}
	return strings.Join(items[:limit], ", ") + fmt.Sprintf(", ... (%d more)", len(items)-limit)
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
