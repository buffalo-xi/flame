package agent

import (
	"flame/nexus/service/agent/core"
	"flame/nexus/service/agent/tools"
	"flame/nexus/shared/models"
)

// NewAgent creates a core.Agent implementation.
// There is only one implementation today (agentImpl: the prompt comes from AGENT.md in the workspace).
// A new implementation (containerised, say) would be dispatched by type here.
func NewAgent(agent *models.Agent, schema string, arguments map[string]interface{}) (core.Agent, error) {
	return newAgentImpl(agent, schema, arguments)
}

// DefinitionModeKey is a reserved key in arguments: whether this run reads the draft or the current version.
//
// It belongs with user_id / session_id / _call_depth: injected by the entry layer, consumed at runtime, and not a model-visible business
// argument (sanitising helpers such as core.StripSessionID only cover business keys - do not mix it in there).
const DefinitionModeKey = "_definition_mode"

// SetReleaseMode marks this run as reading the **released current version** (used by production entries).
//
// Only production entries may call it: RPC / MCP / endpoints / a non-debug agent node of a flow / call_agent pass-through.
// The editor's debug chat, trial chat and unit tests keep the default (draft) - they have to see "the copy being edited".
func SetReleaseMode(arguments map[string]interface{}) {
	if arguments == nil {
		return
	}
	arguments[DefinitionModeKey] = string(tools.ModeCurrent)
}

// SetDraftMode marks draft reading explicitly (used by the debugger; equivalent to the default, written out for readability and audit).
func SetDraftMode(arguments map[string]interface{}) {
	if arguments == nil {
		return
	}
	arguments[DefinitionModeKey] = string(tools.ModeDraft)
}

// FollowDraftsKey is a reserved key in arguments: whether the **child entities this run calls** (agent / flow) read the draft or the
// released version. It only governs "the child", never "self" - which copy self reads is decided by DefinitionModeKey.
//
// Defaults to true (the same default as the flow debugger's "follow dependency drafts"); it only matters when self is a draft:
// a production entry passing true does not make children read the draft either, see tools.Context.ChildMode for the criterion.
const FollowDraftsKey = "_follow_drafts"

// SetFollowDrafts writes "whether child calls follow drafts". Production entries do not need to call it (ChildMode forces false); the chat
// entry injects it from the user's choice for the current round.
func SetFollowDrafts(arguments map[string]interface{}, follow bool) {
	if arguments == nil {
		return
	}
	arguments[FollowDraftsKey] = follow
}

// resolveDefinitionMode decides which definition "self" reads in this run.
//
// The super agent always reads the draft: its whole job is editing drafts (creating agents / skills / flows), and reading the released
// version would hide what it just built - independent of the entry, MCP / RPC included.
func resolveDefinitionMode(raw string, isSupervisor bool) tools.DefinitionMode {
	if isSupervisor {
		return tools.ModeDraft
	}
	if tools.DefinitionMode(raw) == tools.ModeCurrent {
		return tools.ModeCurrent
	}
	return tools.ModeDraft
}

// resolveFollowDrafts decides which definition "the child entities this run calls" read. Three rules, and the order must not change:
//  1. supervisor is always true - what it builds is not released yet and must be callable immediately for verification;
//  2. false when self is a production run - a draft must not spread from a production chain;
//  3. otherwise the switch decides, defaulting to true (the same default as the flow debugger's FollowDrafts).
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
