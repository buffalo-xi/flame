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

// toolSearchLimit caps how many results each category (group / tool) returns per call.
//
// This is a **context budget**, not a performance trade-off: the result enters the model context as a block, and dumping a hundred tools at
// once both blows up the window and is useless. Over the limit, Truncated is set and the model is asked to narrow the query.
const toolSearchLimit = 20

// groupMemberPreview caps the member refs echoed in a group card.
//
// The same reasoning as toolSearchLimit: a member digest also costs context. The point of a group overview is only "look inside before
// granting the whole group", and 12 entries are enough to tell the kind (files / connections / mixed); Count carries the real total, and the
// full list is reached by expanding with a query.
const groupMemberPreview = 12

// toolCatalogManager implements tools.ToolCatalogManager: it flattens the "grantable catalog" into a searchable list.
//
// The data source is the very Catalog the admin grant panel uses, so "what an agent can find" and "what the panel shows" always agree -
// the mismatch of "in the panel, unreachable for the agent" is gone.
type toolCatalogManager struct {
	schema       string
	agentName    string
	isSupervisor bool // identity of the current (calling) agent, used when no target is given
}

var _ tools.ToolCatalogManager = (*toolCatalogManager)(nil)

func newToolCatalogManager(schema, agentName string, isSupervisor bool) *toolCatalogManager {
	return &toolCatalogManager{schema: schema, agentName: agentName, isSupervisor: isSupervisor}
}

// Search searches grantable groups and tools.
//
// The viewpoint is the **target agent** (possibly different from the caller): super-only tools are stripped from a non-super agent's
// candidates, because granting them cannot be executed anyway (the hard gate in ExecuteTool refuses), and showing them only lures the model
// into a grant action that is bound to fail.
func (m *toolCatalogManager) Search(_ context.Context, query, agentName, agentID string) (*tools.ToolSearchResult, error) {
	if agentName == "" {
		agentName = m.agentName
	}
	// The viewpoint follows the **target** identity (possibly different from the caller): super-only tools are stripped from a non-super
	// candidate, because granting them cannot be executed. An empty agentID (no target given) falls back to the caller's identity.
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

	// The refs currently granted to the target agent: for display only (avoid duplicate grants, make "what was granted" visible).
	// A read failure is not fatal - one missing status column beats failing the whole catalog lookup.
	granted := make(map[string]string)
	if agentID != "" {
		list, gerr := repo.ListAgentGrants(m.schema, agentID)
		if gerr != nil {
			xlog.Info("search_tools failed to read the grant state, showing items as ungranted schema=%s agent=%s err=%v",
				m.schema, agentName, gerr)
		} else {
			for _, g := range list {
				granted[g.Ref] = g.GrantKind
			}
		}
	}

	return searchCatalog(query, all, groups, granted), nil
}

// searchCatalog is a pure function: catalog + current grants -> search result (no database access, so it is easy to unit test).
func searchCatalog(query string, all map[string]toolgrant.ToolInfo, groups []toolgrant.GroupInfo, granted map[string]string) *tools.ToolSearchResult {
	q := strings.ToLower(strings.TrimSpace(query))
	res := &tools.ToolSearchResult{
		Query:  q,
		Groups: []tools.ToolSearchItem{},
		Tools:  []tools.ToolSearchItem{},
	}

	var truncatedGroups []string
	for _, g := range groups {
		// Synthetic containers (plugin:* / custom:*) are not grantable: ValidateRef refuses their ref, and their members appear in the list
		// below as individual tool refs.
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

	// An empty query gives the group overview only: tools may number in the hundreds, and dumping all of them into the context is both
	// useless and wasteful.
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

	// map iteration is unordered -> sort explicitly. Otherwise two runs of the same query return different orders and the model thinks the
	// catalog changed.
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
		res.Notes = append(res.Notes, "Too many hits: only the first 20 were returned. Narrow the query (a connection name, part of a tool name, a ref fragment).")
	}
	if len(truncatedGroups) > 0 {
		res.Notes = append(res.Notes, fmt.Sprintf(
			"%s has more than %d members; only the first %d were returned (count is still the real total). To see every member, match that group name with query to expand it.",
			strings.Join(truncatedGroups, ", "), groupMemberPreview, groupMemberPreview))
	}
	if q == "" && len(res.Groups) > 0 {
		res.Notes = append(res.Notes,
			"Without a query only group digests are listed (group ref + member count + the first few member refs); to see every tool of a group, match it with query (e.g. fs, read_file).")
	}
	// Zero hits must be said outright, with the cross-language way out right there: matchQuery is a **substring** match (see below), and the
	// catalog's names / descriptions may be English while the model queries with a Chinese phrase copied from the user (or the reverse).
	// Returning empty silently would make the model think "this tenant has no such tool", which misleads more than a miss.
	if q != "" && len(res.Groups) == 0 && len(res.Tools) == 0 {
		res.Notes = append(res.Notes,
			"No match — the query is a substring match (it does not map across languages automatically). "+
				"Retry with a tool name, a ref fragment or an English keyword (e.g. exec, read_file, mcp:fs).")
	}
	return res
}

// memberRefs takes the first limit grantable refs of a group's members; the second return value says whether it was truncated.
// Refs only, no descriptions: a group digest exists to show "what kind of things are inside", and descriptions come when a query expands it.
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

// matchQuery is a case-insensitive "any field contains".
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

// sortSearchItems puts available items first, then orders by ref lexicographically (so the same input yields stable output).
func sortSearchItems(items []tools.ToolSearchItem) {
	sort.Slice(items, func(i, j int) bool {
		if items[i].Available != items[j].Available {
			return items[i].Available
		}
		return items[i].Ref < items[j].Ref
	})
}

// kindOfRef returns the scope segment of a ref (builtin / mcp / rpc / local / plugin / script / custom / preset).
func kindOfRef(ref string) string {
	if p, err := toolgrant.ParseRef(ref); err == nil {
		return p.Scope
	}
	return ""
}
