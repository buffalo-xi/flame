package models

import "sort"

// This file carries two **semantically orthogonal** judgements. Do not mix them up:
//
//  1. The identity criterion IsSupervisor - who holds the super privilege. The criterion is an **entity attribute**
//     (agents.is_supervisor), no longer a name allowlist: main is merely one preinstalled supervisor, a tenant may configure agents with
//     the same privilege, and several of them are allowed.
//  2. The delete / rename protection IsProtectedAgent / IsProtectedSkill - which resources may not be deleted and may not be renamed
//     (their content and description stay editable). The list holds **preinstalled assets only**.
//
// Historically the identity criterion was IsMainAgent(name), with the same values as the protection list, so "adding a second name to the
// protection list" also misjudged it as privileged. The two are now completely separate:
//
//	main               -> is_supervisor = true (set by the migration script) + in the protection list (preinstalled)
//	a new supervisor   -> is_supervisor = true, but **not** in the protection list: deletable and renamable
//
// Why not protect every supervisor: the protection list is "what has no recovery path once deleted", whereas the supervisor attribute itself
// can be granted again. The real risk (a tenant losing all management capability) is caught by the business layer's "the last supervisor
// cannot be demoted", not by a name list.

// protectedAgents is the preinstalled agent list: no delete, no rename.
// It holds only the assets materialised when a tenant is created (data/tenants/*/agents/main is a ready-made knowledge base, and deleting it
// has no rebuild path). Adding a name here deserves care - once added, it can never be deleted.
var protectedAgents = map[string]struct{}{
	"main": {},
}

// protectedSkills is the skill list that may not be deleted or renamed.
// All four are the knowledge source of "meta capabilities" and are system cornerstones:
//   - skill-authoring        -> how to create / maintain a skill
//   - agent-authoring        -> how to create / maintain an agent
//   - custom-tool-authoring  -> how to turn business logic into a custom tool (custom__*)
//   - flow-authoring         -> how to create / maintain a workflow and expose it as an mcp / rpc endpoint
var protectedSkills = map[string]struct{}{
	"skill-authoring":       {},
	"agent-authoring":       {},
	"custom-tool-authoring": {},
	"flow-authoring":        {},
}

// IsSupervisor is the identity criterion: does this agent hold the super privilege (nil safe, returns false).
//
// The single source of truth is the entity field - do not reintroduce a judgement by name, or the two criteria will necessarily diverge.
func IsSupervisor(a *Agent) bool {
	return a != nil && a.IsSupervisor
}

// IsProtectedAgent reports whether an agent is protected from deletion / renaming (the preinstalled asset list).
func IsProtectedAgent(name string) bool {
	_, ok := protectedAgents[name]
	return ok
}

// CanDemoteSupervisor reports whether the agent's super privilege (agents.is_supervisor) may be revoked.
//
// A protected agent (protectedAgents, currently only main) may never be demoted. The reason differs from "the last supervisor cannot be
// demoted":
//   - "the last one" is a quantity constraint, guarding against the tenant as a whole losing its management surface (a second supervisor
//     lifts it);
//   - this one is an asset constraint, guarding against the name main becoming a dead corner that "holds the entrance but has no
//     privilege" - it can neither be renamed nor deleted, so once demoted it can never be restored into the preinstalled management entry.
//
// So main's supervisor attribute is constant: its description, its definition-domain files and its tool grants are all editable, but it can
// never lose the super privilege.
func CanDemoteSupervisor(name string) bool {
	return !IsProtectedAgent(name)
}

// IsProtectedSkill reports whether a skill is protected from deletion / renaming.
func IsProtectedSkill(name string) bool {
	_, ok := protectedSkills[name]
	return ok
}

// ProtectedAgentNames returns the protected agent names (lexicographically sorted, a read-only copy).
//
// Same contract as ProtectedSkillNames: validation code (the template pack's files/ tree and seed rows) must derive from
// this, not from literals - otherwise adding a protected agent creates a gap of "protected but not packaged".
func ProtectedAgentNames() []string {
	names := make([]string, 0, len(protectedAgents))
	for n := range protectedAgents {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// ProtectedSkillNames returns the protected skill names (lexicographically sorted, a read-only copy).
//
// Usage: in a tenant's resource plan, the template pack's seed.sql / files must stay in sync with this list - use it to validate "the skill
// set in the pack == the protection list", and do not copy the literals at the call site, or adding a protected skill creates a gap of
// "protected but not packaged".
func ProtectedSkillNames() []string {
	names := make([]string, 0, len(protectedSkills))
	for n := range protectedSkills {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
