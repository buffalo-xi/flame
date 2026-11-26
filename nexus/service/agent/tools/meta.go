package tools

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"

	"flame/nexus/service/agent/core"
	"flame/nexus/shared/db"
	"flame/nexus/shared/models"
	"flame/nexus/shared/versioning"
	"flame/nexus/shared/versions"
)

// ===== Meta tools: create_agent / update_agent / delete_agent / create_own_skill / update_own_skill / delete_own_skill / create_shared_skill / update_shared_skill / delete_shared_skill / bind_skill / unbind_skill / list_agents / list_skills / read_agent / read_skill / use_skill =====
//
// The split between read_skill and use_skill: the former only views, the latter "uses" - it returns the body and activates the
// tool dependencies the skill declares.
// A binding only decides whether the skill appears in the system prompt list; it no longer produces tool candidates directly.

// skillRefFrom builds a skill reference from the tool arguments: skill_id first; otherwise space targeting by name + space.
func skillRefFrom(args map[string]any, space string) SkillRef { return skillRefIn(args, space, "") }

// skillRefIn is the same, plus an explicit **owning agent** for the private space (used by the *_agent_skill series; empty =
// the current agent).
//
// space is decided by **the tool itself** (own series = agent / shared series = tenant) and is not accepted from the model -
// the tool name is the criterion, which keeps the model from picking the wrong scope argument, and keeps it from mistaking a
// shared skill for its own by locating it through the visibility criterion.
// ownerID works the same way: only the *_agent_skill series fills it, and the tool layer has already resolved and validated it
// (the target agent must exist and be editable).
func skillRefIn(args map[string]any, space, ownerID string) SkillRef {
	return SkillRef{
		ID:      strings.TrimSpace(argString(args, "skill_id")),
		Name:    argString(args, "name"),
		Scope:   space,
		OwnerID: ownerID,
	}
}

// skillIDDefaultHint is the "do not pass it by default" appended to every skill_id description.
//
// When name and skill_id sit side by side the model tends to fill both, or to have no idea which to fill; and skill_id serves
// exactly one case - "locate across ownership by globally unique id" (which, without a name or space, needs the super agent).
// Saying outright that the default is "do not pass it" is cheaper and less ambiguous than explaining precedence.
const skillIDDefaultHint = "; omit it by default — locate the skill by name; fill it only when you already have the exact id from list_skills"

// rejectScopeArg refuses the scope argument left over from the old interface.
//
// The old shape was "a single create_skill plus a scope argument"; after the split the tool name is the criterion, so a model
// passing scope out of habit must be corrected explicitly - ignoring it silently would leave it believing it "created a shared
// skill" when it actually created a private one.
// counterpart is the dual tool name, used to give actionable correction guidance.
func rejectScopeArg(args map[string]any, tool, counterpart string) error {
	if _, ok := args["scope"]; !ok {
		return nil
	}
	return fmt.Errorf("%s does not take a scope argument (the tool name carries the scope); use %s to create a skill in the other space", tool, counterpart)
}

// skillToolDeps parses the tool_deps argument of a tool.
//   - key absent -> (nil, nil): leave untouched;
//   - key present but unsupported by the tool (own series) -> explicit error: ignoring it silently would leave the model
//     believing it "declared dependencies" when nothing happened - the same consideration as rejectScopeArg;
//   - unrecognisable shape -> error (no guessing, no silent clearing).
func skillToolDeps(args map[string]any, tool string, allowDeps bool) (*[]string, error) {
	if _, present := args["tool_deps"]; !present {
		return nil, nil
	}
	if !allowDeps {
		return nil, fmt.Errorf("%s does not take a tool_deps argument: skill tool deps belong to the grant surface, so only the supervisor can declare them via *_agent_skill / *_shared_skill", tool)
	}
	refs, ok := argStringSlice(args, "tool_deps")
	if !ok {
		return nil, fmt.Errorf("%s: tool_deps must be an array of strings (refs look like builtin:exec)", tool)
	}
	return &refs, nil
}

// withToolDeps adds the tool_deps argument to a skill tool (super agent only, the *_agent_skill / *_shared_skill series).
//
// The own series deliberately does not get it: a skill dependency is the **activation trigger** of an allow grant, so letting a
// non-super agent write it would hand it the decision of "when to enable the tools an administrator granted it" - privilege
// escalation through a self-built skill. The same consideration as not letting the skill side reference custom groups.
func withToolDeps(m ToolMeta) ToolMeta {
	props, ok := m.Parameters["properties"].(map[string]any)
	if !ok {
		props = map[string]any{}
		m.Parameters["properties"] = props
	}
	props["tool_deps"] = map[string]any{
		"type": "array", "items": map[string]any{"type": "string"},
		"description": "Tool refs this skill declares it needs (e.g. builtin:exec, builtin:file); replaced wholesale — pass [] to clear, omit to leave unchanged. Only takes effect when the host agent already has an allow grant for those tools",
	}
	return m
}

// skillRestricted parses the restricted argument (the visibility switch).
//   - key absent -> (nil, nil): leave untouched;
//   - key present but unsupported by the space (a private skill) -> explicit error: ignoring it silently would leave the model
//     believing it changed visibility, the same consideration as rejectScopeArg / skillToolDeps;
//   - wrong shape -> error (no guessing).
func skillRestricted(args map[string]any, tool string, allowRestricted bool) (*bool, error) {
	v, present := args["restricted"]
	if !present {
		return nil, nil
	}
	if !allowRestricted {
		return nil, fmt.Errorf("%s does not take a restricted argument: visibility only applies to tenant-level shared skills, use update_shared_skill instead", tool)
	}
	b, ok := v.(bool)
	if !ok {
		return nil, fmt.Errorf("%s: restricted must be a boolean", tool)
	}
	return &b, nil
}

// withRestricted adds the restricted argument to a skill tool (update_shared_skill only).
//
// A shared skill is narrowed by default when created (restricted=true with an allowlist holding only the creator, see
// skillManager.Create), so this only offers a "switch": false = open it to everyone, true = back to allowlist mode (the
// allowlist data is kept).
// Allowlist membership is maintained by bind_skill / unbind_skill; this argument never touches it.
func withRestricted(m ToolMeta) ToolMeta {
	props, ok := m.Parameters["properties"].(map[string]any)
	if !ok {
		props = map[string]any{}
		m.Parameters["properties"] = props
	}
	props["restricted"] = map[string]any{
		"type": "boolean",
		"description": "Visibility switch (omit to leave unchanged): true = only agents on the whitelist can see it (default), false = visible to everyone. " +
			"Whitelist members are added and removed with bind_skill / unbind_skill — this parameter only flips the switch",
	}
	return m
}

// createAgentTool creates a new agent (writes AGENT.md + registers the instance).
type createAgentTool struct{}

func (t *createAgentTool) Meta() ToolMeta {
	return ToolMeta{
		Name:        "create_agent",
		Category:    CategoryAgentAdmin,
		Description: "Create a new agent instance: write AGENT.md and register it in the system.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":        map[string]any{"type": "string", "description": "Unique name, letters/digits/hyphens only — it is also the definition-domain directory name"},
				"description": map[string]any{"type": "string", "description": "Agent description"},
				"agent_md":    map[string]any{"type": "string", "description": "Full text of AGENT.md"},
				"default_language": map[string]any{"type": "string", "description": "Default reply language as a BCP-47 tag (e.g. zh-CN, en, ja); omit or empty for \"follow the caller\" (optional). " +
					"A caller-pinned _meta.language still overrides it for a single call, and a workflow started by this agent can read it as the builtin `language`. " +
					"Editable later with update_agent(default_language=…)"},
				"is_supervisor": map[string]any{"type": "boolean", "description": "Whether to create it directly as a supervisor (optional, default false). " +
					"When true the starter kit is materialized as well: the core tool group (including builtin:agent-admin), unrestricted tools, discovery of unbound skills, " +
					"and the agent-authoring / skill-authoring / custom-tool-authoring / flow-authoring skills bound"},
			},
			"required": []any{"name", "agent_md"},
		},
	}
}

func (t *createAgentTool) Execute(ctx context.Context, c *Context, args map[string]any) (any, error) {
	name := argString(args, "name")
	if !ValidName(name) {
		return nil, fmt.Errorf("invalid agent name %q (letters, digits and hyphens only)", name)
	}
	if c.Agents == nil {
		return nil, errors.New("agent manager not available")
	}
	exists, err := c.Agents.Exists(ctx, name)
	if err != nil {
		return nil, err
	}
	if exists {
		return nil, fmt.Errorf("agent %q already exists", name)
	}

	agentMd := argString(args, "agent_md")
	description := argString(args, "description")

	// Validate the language before anything is written: a malformed tag must not leave a half-created agent behind
	// (AGENT.md on disk, no row).
	rawLanguage := strings.TrimSpace(argString(args, "default_language"))
	language, _ := core.NormalizeOutputLanguage(rawLanguage)
	if rawLanguage != "" && language == "" {
		return nil, fmt.Errorf("default_language %q is not a valid BCP-47 language tag (e.g. zh-CN, en, ja)", rawLanguage)
	}

	// Write AGENT.md (storage creates the directory automatically)
	if err := c.Storage.WriteFile(ctx, path.Join(AgentsKey(c.Schema, name), agentEntryFile), []byte(agentMd)); err != nil {
		return nil, err
	}
	// Register the instance (tools are decided by the "tool configuration" grant system; config is not written here).
	// The default reply language goes into the same INSERT, so creation is all-or-nothing.
	id, err := c.Agents.Create(ctx, name, description, language, argBool(args, "is_supervisor"))
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"id":               id,
		"name":             name,
		"workspace":        AgentsKey(c.Schema, name),
		"is_supervisor":    argBool(args, "is_supervisor"),
		"default_language": language, // "" = follow the caller (the product default then applies)
	}, nil
}

// updateAgentTool updates an agent (rename / new description).
type updateAgentTool struct{}

func (t *updateAgentTool) Meta() ToolMeta {
	return ToolMeta{
		Name:     "update_agent",
		Category: CategoryAgentAdmin,
		Description: "Update an agent: rename it, change its description, the \"discover unbound skills\" switch, the \"unrestricted tools\" switch or its default reply language (a rename migrates its definition-domain directory too). " +
			"You may update yourself (the supervisor is the admin), but **you cannot rename yourself** (the file operations later in this run would break).",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":          map[string]any{"type": "string", "description": "Current agent name (may be yourself)"},
				"new_name":      map[string]any{"type": "string", "description": "New name (optional; cannot be used on yourself)"},
				"description":   map[string]any{"type": "string", "description": "New description (optional; empty or omitted both mean unchanged — this tool cannot clear a description, ask an admin to do it in the admin UI)"},
				"discovery":     map[string]any{"type": "boolean", "description": "Whether this agent may discover unbound shared skills (optional; omit to leave unchanged)"},
				"unrestricted":  map[string]any{"type": "boolean", "description": "Whether tools are unrestricted: when on, candidate tools are no longer filtered by allow (optional; omit to leave unchanged)"},
				"is_supervisor": map[string]any{"type": "boolean", "description": "Whether to grant supervisor power (same level as the supervisor: admin tools + cross-domain writes). Only a supervisor may do this; demoting the tenant's last supervisor is refused (optional; omit to leave unchanged)"},
				"default_language": map[string]any{"type": "string", "description": "Default reply language as a BCP-47 tag (e.g. zh-CN, en, ja); an empty string clears it (optional; omit to leave unchanged). " +
					"A caller-pinned _meta.language still overrides it for that single call, and a workflow started by this agent can read it as the builtin `language`"},
			},
			"required": []any{"name"},
		},
	}
}

func (t *updateAgentTool) Execute(ctx context.Context, c *Context, args map[string]any) (any, error) {
	name := argString(args, "name")
	if !ValidName(name) {
		return nil, fmt.Errorf("invalid agent name %q", name)
	}
	if c.Agents == nil {
		return nil, errors.New("agent manager not available")
	}
	// On yourself, only renaming is forbidden: a rename would break the later file operations of this round (a technical
	// reason, unrelated to permissions).
	// description / discovery / unrestricted belong to the super agent's own management surface and are allowed (D5 revision).
	if name == c.AgentName && argString(args, "new_name") != "" {
		return nil, errors.New("cannot rename the running agent itself (later file operations in this round would break); ask an administrator to rename it")
	}
	finalName, err := c.Agents.Update(ctx, name, argString(args, "new_name"), argString(args, "description"))
	if err != nil {
		return nil, err
	}
	// Optional switch arguments (not passed = unchanged): skill visibility discovery + tool surface unrestricted.
	// Both need the target agent's uuid, so info is fetched once on demand.
	_, dOK := args["discovery"]
	_, uOK := args["unrestricted"]
	_, sOK := args["is_supervisor"]
	_, lOK := args["default_language"]
	if dOK || uOK || sOK || lOK {
		info, gerr := c.Agents.Get(ctx, finalName)
		if gerr != nil {
			return nil, gerr
		}
		if info == nil {
			return nil, fmt.Errorf("agent %q not found", finalName)
		}
		// Skill visibility: whether discovering "unbound shared skills" is allowed
		if dOK {
			if c.Skills == nil {
				return nil, errors.New("skill manager not available")
			}
			if serr := c.Skills.SetAgentSkillDiscovery(ctx, info.ID, argBool(args, "discovery")); serr != nil {
				return nil, serr
			}
		}
		// Tool surface: whether tools are "unrestricted" (candidates are no longer filtered by allow).
		// It is an attribute of the agent itself, so it goes through Agents; c.Agents was already checked for nil above
		if uOK {
			if uerr := c.Agents.SetUnrestricted(ctx, info.ID, argBool(args, "unrestricted")); uerr != nil {
				return nil, uerr
			}
			// It changed yourself: the tool surface is different, so this run must recompute on its next round (otherwise the
			// switch looks like it had no effect).
			if name == c.AgentName {
				c.MarkToolsDirty()
			}
		}
		// The super privilege: grant / revoke (revoking the tenant's last supervisor is refused by the repository).
		// After revoking it from yourself, the management tools are stripped by the execution-layer hard gate on this run's
		// next round, so the dirty flag is set here too.
		if sOK {
			if serr := c.Agents.SetSupervisor(ctx, info.ID, argBool(args, "is_supervisor")); serr != nil {
				return nil, serr
			}
			if name == c.AgentName {
				c.MarkToolsDirty()
			}
		}
		// Default reply language: agents.default_language, so it is a single-column write rather than the full-row Update
		// (which would wipe the other definition columns). An empty value clears it; a malformed tag is an error instead of
		// a silently ignored value, because the runtime would treat it as "not configured" and look like it had no effect.
		if lOK {
			raw := argString(args, "default_language")
			tag, _ := core.NormalizeOutputLanguage(raw)
			if raw != "" && tag == "" {
				return nil, fmt.Errorf("default_language %q is not a valid BCP-47 language tag (e.g. zh-CN, en, ja)", raw)
			}
			if lerr := c.Agents.SetOutputLanguage(ctx, info.ID, tag); lerr != nil {
				return nil, lerr
			}
		}
	}
	// The id is returned as well: the frontend uses it to locate this agent on the agents page (after a rename the old name from
	// the call arguments no longer finds it).
	// Not finding it is not a failure - the frontend still falls back to name, and a successful update must not fail over a
	// lookup.
	agentID := ""
	if info, gerr := c.Agents.Get(ctx, finalName); gerr == nil && info != nil {
		agentID = info.ID
	}
	out := map[string]any{"id": agentID, "name": finalName, "updated": true}
	if lOK {
		// Echo the normalized value (empty = cleared) rather than what was sent, so a caller that wrote "zh-cn" sees the
		// tag that was actually stored.
		out["default_language"], _ = core.NormalizeOutputLanguage(argString(args, "default_language"))
	}
	return out, nil
}

// deleteAgentTool deletes an agent.
type deleteAgentTool struct{}

func (t *deleteAgentTool) Meta() ToolMeta {
	return ToolMeta{
		Name:        "delete_agent",
		Category:    CategoryAgentAdmin,
		Description: "Delete an agent (its definition-domain directory is cleaned up too).",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{"type": "string", "description": "Agent name"},
			},
			"required": []any{"name"},
		},
	}
}

func (t *deleteAgentTool) Execute(ctx context.Context, c *Context, args map[string]any) (any, error) {
	name := argString(args, "name")
	if !ValidName(name) {
		return nil, fmt.Errorf("invalid agent name %q", name)
	}
	if c.Agents == nil {
		return nil, errors.New("agent manager not available")
	}
	// Deleting yourself is not allowed: an agent that deletes itself while running fails immediately
	if name == c.AgentName {
		return nil, errors.New("cannot delete the running agent itself")
	}
	if err := c.Agents.Delete(ctx, name); err != nil {
		return nil, err
	}
	return map[string]any{"name": name, "deleted": true}, nil
}

// createOwnSkillTool creates a skill in "your own private space" (writes the content file + registers the skills row).
//
// The category is skill (not super): once granted it is usable - every agent can create its own private skills.
// "Only for yourself" does not rest on the category but on the agentID pinned when SkillsManager is constructed: the tool layer
// cannot pass it in.
type createOwnSkillTool struct{}

func (t *createOwnSkillTool) Meta() ToolMeta {
	return ToolMeta{
		Name:     "create_own_skill",
		Category: CategorySkillOwn,
		Description: "Create a skill in your own private space: write SKILL.md and register it in the skill library. " +
			"A private skill is visible to you only; to reuse it across agents, have the supervisor create a shared skill with create_shared_skill and bind it with bind_skill. " +
			"Returns id / name / scope / bound_agents; a private skill is never bound, so bound_agents is always empty - do not try to bind it yourself.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":        map[string]any{"type": "string", "description": "Unique name, letters/digits/hyphens only"},
				"skill_md":    map[string]any{"type": "string", "description": "Full text of SKILL.md"},
				"description": map[string]any{"type": "string", "description": "Skill description (optional)"},
			},
			"required": []any{"name", "skill_md"},
		},
	}
}

func (t *createOwnSkillTool) Execute(ctx context.Context, c *Context, args map[string]any) (any, error) {
	return createSkillInSpace(ctx, c, args, SkillScopeAgent, "", "create_own_skill", "create_shared_skill", false)
}

// createSkillInSpace creates a skill in the given space (the shared implementation of the own / shared / agent_skill tools).
// A non-empty ownerID = the target owning agent (the *_agent_skill series; super agent only, resolved and validated by the tool
// layer first).
// allowDeps decides whether the tool_deps argument is accepted (false for the own series, see the notes on withToolDeps).
func createSkillInSpace(ctx context.Context, c *Context, args map[string]any, space, ownerID, tool, counterpart string, allowDeps bool) (any, error) {
	name := argString(args, "name")
	if !ValidName(name) {
		return nil, fmt.Errorf("invalid skill name %q (letters, digits and hyphens only)", name)
	}
	if c.Skills == nil {
		return nil, errors.New("skill manager not available")
	}
	if err := rejectScopeArg(args, tool, counterpart); err != nil {
		return nil, err
	}
	deps, err := skillToolDeps(args, tool, allowDeps)
	if err != nil {
		return nil, err
	}
	id, err := c.Skills.Create(ctx, SkillInput{
		Name:        name,
		Description: argString(args, "description"),
		Content:     argString(args, "skill_md"),
		Scope:       space,
		OwnerID:     ownerID,
		ToolDeps:    deps,
	})
	if err != nil {
		return nil, err
	}
	// bound_agents is always present, so "not bound to anyone" is explicit information rather than a missing key:
	//   - a shared skill lists its creator: Create grants the creator visibility (a shared skill is restricted by default),
	//     and that row is what makes the skill usable at all - the model must not report it as a binding it made;
	//   - a private skill has no binding dimension (visibility follows the owner), so the list stays empty.
	bound := []string{}
	if space == SkillScopeTenant && c.AgentID != "" && c.AgentName != "" {
		// The same condition as skillManager.Create's automatic grant: no agent context means no creator row either.
		bound = append(bound, c.AgentName)
	}
	return map[string]any{"id": id, "name": name, "scope": space, "bound_agents": bound}, nil
}

// createSharedSkillTool creates a "tenant-level shared" skill (super agent only: category skill-admin, closed at three
// layers).
//
// After creation only the creator is granted (enterprise default-deny); give it to others with bind_skill - so a mistaken
// creation does not spread.
// It supports declaring tool dependencies through tool_deps (super agent only, see withToolDeps).
type createSharedSkillTool struct{}

func (t *createSharedSkillTool) Meta() ToolMeta {
	return withToolDeps(ToolMeta{
		Name:     "create_shared_skill",
		Category: CategorySkillAdmin,
		Description: "Create a tenant-level shared skill (supervisor only): write SKILL.md and register it in the skill library. " +
			"Other agents only see it after bind_skill binds it; by default only you are granted, so it never spreads on its own. " +
			"Returns id / name / scope / bound_agents, where bound_agents lists who can already see it (the creator) - binding anyone else is a separate, explicit bind_skill call.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":        map[string]any{"type": "string", "description": "Unique name, letters/digits/hyphens only"},
				"skill_md":    map[string]any{"type": "string", "description": "Full text of SKILL.md"},
				"description": map[string]any{"type": "string", "description": "Skill description (optional)"},
			},
			"required": []any{"name", "skill_md"},
		},
	})
}

func (t *createSharedSkillTool) Execute(ctx context.Context, c *Context, args map[string]any) (any, error) {
	return createSkillInSpace(ctx, c, args, SkillScopeTenant, "", "create_shared_skill", "create_own_skill", true)
}

// skillUpdateMeta builds the tool definition of update_own_skill / update_shared_skill:
// the two take exactly the same arguments (the space is decided by the tool, the model cannot pass it in) and differ only in
// name / category / description / id hint.
func skillUpdateMeta(name, category, description, idHint string) ToolMeta {
	return ToolMeta{
		Name:        name,
		Category:    category,
		Description: description,
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":        map[string]any{"type": "string", "description": "Current skill name (used to locate it)"},
				"skill_id":    map[string]any{"type": "string", "description": idHint},
				"new_name":    map[string]any{"type": "string", "description": "New name after renaming (optional)"},
				"skill_md":    map[string]any{"type": "string", "description": "New full text of SKILL.md (optional; omit to keep the current content)"},
				"description": map[string]any{"type": "string", "description": "New description (optional)"},
			},
			"required": []any{"name"},
		},
	}
}

// updateSkillInSpace updates a skill in the given space (the shared implementation of the own / shared / agent_skill tools).
// A non-empty ownerID = the target owning agent (the *_agent_skill series; super agent only).
// allowDeps decides whether the tool_deps argument is accepted (false for the own series, see the notes on withToolDeps).
func updateSkillInSpace(ctx context.Context, c *Context, args map[string]any, space, ownerID, tool string, allowDeps bool) (any, error) {
	name := argString(args, "name")
	if !ValidName(name) {
		return nil, fmt.Errorf("invalid skill name %q", name)
	}
	if c.Skills == nil {
		return nil, errors.New("skill manager not available")
	}
	deps, err := skillToolDeps(args, tool, allowDeps)
	if err != nil {
		return nil, err
	}
	// Visibility switch: only the tenant-level space accepts it (the tool's space targeting is the criterion; the model cannot
	// pass it in)
	restricted, err := skillRestricted(args, tool, space == SkillScopeTenant)
	if err != nil {
		return nil, err
	}
	ref := skillRefIn(args, space, ownerID)
	newName := argString(args, "new_name")
	if err := c.Skills.Update(ctx, SkillInput{
		ID:          ref.ID,
		OwnerID:     ownerID,
		Name:        name,
		NewName:     newName,
		Description: argString(args, "description"),
		Content:     argString(args, "skill_md"),
		Scope:       space,
		ToolDeps:    deps,
		Restricted:  restricted,
	}); err != nil {
		return nil, err
	}
	if newName == "" {
		newName = name
	}
	return map[string]any{"name": newName, "updated": true}, nil
}

// updateOwnSkillTool updates a skill in "your own private space" (category skill-own: usable once granted, with the space
// pinned by this tool).
type updateOwnSkillTool struct{}

func (t *updateOwnSkillTool) Meta() ToolMeta {
	return skillUpdateMeta("update_own_skill", CategorySkillOwn,
		"Update your own private skill: change its content, description or name (a rename migrates its content file). Only your own private skills can be updated.",
		"Skill id (optional; when locating by id it must still be inside your own private space)")
}

func (t *updateOwnSkillTool) Execute(ctx context.Context, c *Context, args map[string]any) (any, error) {
	return updateSkillInSpace(ctx, c, args, SkillScopeAgent, "", "update_own_skill", false)
}

// updateSharedSkillTool updates a "tenant-level shared" skill (category skill-admin: super agent only).
type updateSharedSkillTool struct{}

func (t *updateSharedSkillTool) Meta() ToolMeta {
	return withRestricted(withToolDeps(skillUpdateMeta("update_shared_skill", CategorySkillAdmin,
		"Update a tenant-level shared skill (supervisor only): change its content, description, name, tool dependencies or visibility switch (a rename migrates its content file). Note it may currently be bound by several agents.",
		"Skill id (optional; when locating by id it must still be a tenant-level shared skill)")))
}

func (t *updateSharedSkillTool) Execute(ctx context.Context, c *Context, args map[string]any) (any, error) {
	return updateSkillInSpace(ctx, c, args, SkillScopeTenant, "", "update_shared_skill", true)
}

// ===== Incremental maintenance of tool dependencies (grant_skill_tool / revoke_skill_tool) =====
//
// The split with the tool_deps argument of update_*_skill: that one is a **wholesale replacement** (not passed = unchanged,
// []=clear) and suits batch declarations; these two are **incremental** - no need to read the current state first, and a
// forgotten entry never silently deletes another dependency.
//
// The naming deliberately mirrors grant_agent_tool / revoke_agent_tool: `<verb>_<grantee>_tool`, so that at a glance it is clear
// "this is also about tools, but the grantee is a skill rather than an agent". The price is that grant is easily read as
// granting, which is why the description denies it in the first sentence: the skill side only "declares it needs" a tool and
// creates no grant.

func skillDepMeta(name, description string) ToolMeta {
	return ToolMeta{
		Name:     name,
		Category: CategorySkillAdmin, // super agent only: a dependency is the activation trigger of an allow grant
		Description: description +
			"**This is not a grant**: the dependency is only injected when the host agent already has an allow grant for that tool and has activated this skill with use_skill. " +
			"The first argument is the skill to declare it on (name or skill_id), not an agent — to grant an agent, use grant_agent_tool. " +
			"The return value carries the complete tool_deps after the change, so there is no need to look it up again.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":     map[string]any{"type": "string", "description": "Skill name (located within the currently visible scope; for a private skill owned by another agent use skill_id)"},
				"skill_id": map[string]any{"type": "string", "description": "Skill id (optional; alternative to name, takes precedence when non-empty)" + skillIDDefaultHint},
				"ref":      map[string]any{"type": "string", "description": "Tool reference: builtin:<tool> / builtin:<category> / mcp:<connection>[:<tool>] / rpc:<connection>[:<tool>] / local:rpc[:<endpoint>]; group refs are not accepted"},
			},
			"required": []any{"ref"},
		},
	}
}

// skillDepByArgs parses name / skill_id and pre-checks that at least one valid locator was given.
func skillDepByArgs(args map[string]any) (SkillRef, error) {
	ref := skillRefFrom(args, "")
	if ref.ID == "" && !ValidName(ref.Name) {
		return ref, errors.New("a name (a valid skill name) or a skill_id is required to locate the skill")
	}
	return ref, nil
}

type grantSkillToolTool struct{}

func (t *grantSkillToolTool) Meta() ToolMeta {
	return skillDepMeta("grant_skill_tool",
		"**Append** one tool dependency to a skill (written to skill_tool_grants; idempotent, added=false when it already exists).")
}

func (t *grantSkillToolTool) Execute(ctx context.Context, c *Context, args map[string]any) (any, error) {
	if c.Skills == nil {
		return nil, errors.New("skill manager not available")
	}
	ref, err := skillDepByArgs(args)
	if err != nil {
		return nil, err
	}
	return c.Skills.AddToolDep(ctx, ref, argString(args, "ref"))
}

type revokeSkillToolTool struct{}

func (t *revokeSkillToolTool) Meta() ToolMeta {
	return skillDepMeta("revoke_skill_tool",
		"**Remove** one tool dependency from a skill (idempotent; removes only this one and leaves the other dependencies alone).")
}

func (t *revokeSkillToolTool) Execute(ctx context.Context, c *Context, args map[string]any) (any, error) {
	if c.Skills == nil {
		return nil, errors.New("skill manager not available")
	}
	ref, err := skillDepByArgs(args)
	if err != nil {
		return nil, err
	}
	return c.Skills.RemoveToolDep(ctx, ref, argString(args, "ref"))
}

// skillDeleteMeta builds the tool definition of delete_own_skill / delete_shared_skill (same arguments).
func skillDeleteMeta(name, category, description, idHint string) ToolMeta {
	return ToolMeta{
		Name:        name,
		Category:    category,
		Description: description,
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":     map[string]any{"type": "string", "description": "Skill name, e.g. english-joke-writing"},
				"skill_id": map[string]any{"type": "string", "description": idHint + skillIDDefaultHint},
			},
			"required": []any{"name"},
		},
	}
}

// deleteSkillInSpace deletes a skill in the given space (the shared implementation of the own / shared / agent_skill tools).
// A non-empty ownerID = the target owning agent (the *_agent_skill series; super agent only).
func deleteSkillInSpace(ctx context.Context, c *Context, args map[string]any, space, ownerID string) (any, error) {
	name := argString(args, "name")
	if !ValidName(name) {
		return nil, fmt.Errorf("invalid skill name %q", name)
	}
	if c.Skills == nil {
		return nil, errors.New("skill manager not available")
	}
	ref := skillRefIn(args, space, ownerID)
	ref.Name = name
	if err := c.Skills.Delete(ctx, ref); err != nil {
		return nil, err
	}
	return map[string]any{"name": name, "deleted": true}, nil
}

// deleteOwnSkillTool deletes a skill in "your own private space".
type deleteOwnSkillTool struct{}

func (t *deleteOwnSkillTool) Meta() ToolMeta {
	return skillDeleteMeta("delete_own_skill", CategorySkillOwn,
		"Delete your own private skill (its content file is deleted too). Only your own private skills can be deleted.",
		"Skill id (optional; when locating by id it must still be inside your own private space)")
}

func (t *deleteOwnSkillTool) Execute(ctx context.Context, c *Context, args map[string]any) (any, error) {
	return deleteSkillInSpace(ctx, c, args, SkillScopeAgent, "")
}

// deleteSharedSkillTool deletes a "tenant-level shared" skill (super agent only).
type deleteSharedSkillTool struct{}

func (t *deleteSharedSkillTool) Meta() ToolMeta {
	return skillDeleteMeta("delete_shared_skill", CategorySkillAdmin,
		"Delete a tenant-level shared skill (supervisor only; its content file is deleted too). Note it may currently be bound by several agents.",
		"Skill id (optional; when locating by id it must still be a tenant-level shared skill)")
}

func (t *deleteSharedSkillTool) Execute(ctx context.Context, c *Context, args map[string]any) (any, error) {
	return deleteSkillInSpace(ctx, c, args, SkillScopeTenant, "")
}

// skillAgentMeta adds the "target owning agent" argument to a skill tool (the *_agent_skill series only, required).
func skillAgentMeta(m ToolMeta) ToolMeta {
	props, ok := m.Parameters["properties"].(map[string]any)
	if !ok {
		props = map[string]any{}
		m.Parameters["properties"] = props
	}
	props["agent"] = map[string]any{"type": "string", "description": "Target agent name (the skill belongs to it; afterwards only that agent can see it)"}
	if req, ok := m.Parameters["required"].([]any); ok {
		m.Parameters["required"] = append([]any{"agent"}, req...)
	}
	return m
}

// About tool_deps: this group (and *_shared_skill) supports declaring a skill's tool dependencies, but **only the super agent
// may write them** (see withToolDeps / the own series deliberately lacks it). A dependency creates no grant - it only "lights
// up" an allow grant the target agent already has, so the tool descriptions must say so, or it will be read as "declaring it
// is enough".

// createAgentSkillTool creates a **specified agent**'s private skill (super agent only, category skill-admin).
//
// It fills a capability gap the super agent has on "someone else's definition domain": it can rewrite another agent's AGENT.md
// wholesale, but could not create that agent's private skills.
// Alongside create_own_skill (your own) / create_shared_skill (tenant-level) it forms the third space, and "the tool name is
// the criterion": ownership is targeted by the agent argument, and a non-super agent passing agent is caught by the three
// layers.
//
// A skill is a **single-file knowledge document**: the whole body goes into skill_md, and there is no attached-file capability
// (the same shape as create_own_skill).
type createAgentSkillTool struct{}

func (t *createAgentSkillTool) Meta() ToolMeta {
	return skillAgentMeta(withToolDeps(ToolMeta{
		Name:     "create_agent_skill",
		Category: CategorySkillAdmin,
		Description: "Create a **specified agent**'s private skill (supervisor only): write SKILL.md and register it in the skill library. " +
			"The skill belongs to the target agent and only that agent can see it; all content goes into skill_md. " +
			"To share it between several agents, use create_shared_skill + bind_skill instead. " +
			"Returns id / name / scope / bound_agents; a private skill is never bound, so bound_agents is always empty.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":        map[string]any{"type": "string", "description": "Unique name, letters/digits/hyphens only"},
				"skill_md":    map[string]any{"type": "string", "description": "Full text of SKILL.md (the entire content of the skill)"},
				"description": map[string]any{"type": "string", "description": "Skill description (optional)"},
			},
			"required": []any{"name", "skill_md"},
		},
	}))
}

func (t *createAgentSkillTool) Execute(ctx context.Context, c *Context, args map[string]any) (any, error) {
	_, ownerID, err := resolveTargetAgent(ctx, c, args)
	if err != nil {
		return nil, err
	}
	return createSkillInSpace(ctx, c, args, SkillScopeAgent, ownerID, "create_agent_skill", "create_own_skill / create_shared_skill", true)
}

// updateAgentSkillTool updates a "specified agent's private skill" (super agent only).
type updateAgentSkillTool struct{}

func (t *updateAgentSkillTool) Meta() ToolMeta {
	return skillAgentMeta(withToolDeps(skillUpdateMeta("update_agent_skill", CategorySkillAdmin,
		"Update a **specified agent**'s private skill (supervisor only): change its content, description, name or tool dependencies (a rename migrates its content file).",
		"Skill id (optional; when locating by id it must still be inside that agent's private space)")))
}

func (t *updateAgentSkillTool) Execute(ctx context.Context, c *Context, args map[string]any) (any, error) {
	_, ownerID, err := resolveTargetAgent(ctx, c, args)
	if err != nil {
		return nil, err
	}
	return updateSkillInSpace(ctx, c, args, SkillScopeAgent, ownerID, "update_agent_skill", true)
}

// deleteAgentSkillTool deletes a "specified agent's private skill" (super agent only).
type deleteAgentSkillTool struct{}

func (t *deleteAgentSkillTool) Meta() ToolMeta {
	return skillAgentMeta(skillDeleteMeta("delete_agent_skill", CategorySkillAdmin,
		"Delete a **specified agent**'s private skill (supervisor only; its content file is deleted too).",
		"Skill id (optional; when locating by id it must still be inside that agent's private space)"))
}

func (t *deleteAgentSkillTool) Execute(ctx context.Context, c *Context, args map[string]any) (any, error) {
	_, ownerID, err := resolveTargetAgentForDelete(ctx, c, args)
	if err != nil {
		return nil, err
	}
	return deleteSkillInSpace(ctx, c, args, SkillScopeAgent, ownerID)
}

// listAgentsTool lists every agent instance.
type listAgentsTool struct{}

func (t *listAgentsTool) Meta() ToolMeta {
	return ToolMeta{
		Name:        "list_agents",
		Category:    CategoryAgent,
		Description: "List all registered agent instances (name/description/status).",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
	}
}

func (t *listAgentsTool) Execute(ctx context.Context, c *Context, args map[string]any) (any, error) {
	if c.Agents == nil {
		return nil, errors.New("agent manager not available")
	}
	list, err := c.Agents.List(ctx)
	if err != nil {
		return nil, err
	}
	return map[string]any{"agents": list}, nil
}

// maxListSkills caps how many entries one list_skills returns (so that a huge skill library cannot blow up the context).
const maxListSkills = 100

// listSkillsTool lists the skills visible to the current agent.
// The visible range depends on whether that agent has "discover unbound skills" on (agents.skill_discovery): when off it is
// private + bound skills only, when on it additionally includes shared skills "visible to everyone".
type listSkillsTool struct{}

func (t *listSkillsTool) Meta() ToolMeta {
	return ToolMeta{
		Name:        "list_skills",
		Category:    CategorySkill,
		Description: "List the skills visible to the current agent (its own private skills + skills bound to it; shared skills visible to everyone are included when skill discovery is on).",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
	}
}

func (t *listSkillsTool) Execute(ctx context.Context, c *Context, args map[string]any) (any, error) {
	if c.Skills == nil {
		return nil, errors.New("skill manager not available")
	}
	list, err := c.Skills.List(ctx)
	if err != nil {
		return nil, err
	}
	total := len(list)
	if total > maxListSkills {
		return map[string]any{
			"skills":    list[:maxListSkills],
			"total":     total,
			"truncated": true,
			"note": fmt.Sprintf("%d skills are visible and only the first %d are listed here; if the skill you need is missing, "+
				"ask an admin to bind it to this agent (or narrow the visible scope)", total, maxListSkills),
		}, nil
	}
	return map[string]any{"skills": list, "total": total}, nil
}

// readAgentTool reads an agent's AGENT.md.
type readAgentTool struct{}

func (t *readAgentTool) Meta() ToolMeta {
	return ToolMeta{
		Name:     "read_agent",
		Category: CategoryAgent,
		Description: "Read an agent's AGENT.md and return the names of the skills currently bound to it (skills). " +
			"When called by a supervisor it also returns that agent's tool grants (tool_grants: required always applies / allow is effective once its declaring skill is activated with use_skill in this run, and pending for another agent whose run cannot be seen from here / deny is an explicit refusal), the \"unrestricted tools\" switch (unrestricted), " +
			"the \"discover unbound skills\" switch (discovery), its default reply language (default_language, empty when not configured), " +
			"and its call grants (can_call = whom it may call, called_by = who may call it).",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{"type": "string", "description": "Agent name"},
			},
			"required": []any{"name"},
		},
	}
}

func (t *readAgentTool) Execute(ctx context.Context, c *Context, args map[string]any) (any, error) {
	name := argString(args, "name")
	if !ValidName(name) {
		return nil, fmt.Errorf("invalid agent name %q", name)
	}
	data, err := c.Storage.ReadFile(ctx, path.Join(AgentsKey(c.Schema, name), agentEntryFile))
	if err != nil {
		return nil, fmt.Errorf("read agent %q: %w", name, err)
	}
	out := map[string]any{"name": name, "content": string(data)}
	// Also hand over the read-only current state (skill bindings / tool grants), so it can be seen before anything is changed
	if c.Agents != nil {
		if info, gerr := c.Agents.Get(ctx, name); gerr == nil && info != nil {
			if c.Skills != nil {
				if names, serr := c.Skills.ListAgentSkills(ctx, info.ID); serr == nil {
					out["skills"] = names
				}
				// Private skill names are only given when the caller is a supervisor: a non-supervisor should not even see the
				// names of other agents' private skills
				if c.IsSupervisor {
					if names, perr := c.Skills.ListAgentPrivateSkills(ctx, info.ID); perr == nil {
						out["private_skills"] = names
					}
				}
			}
			// The two agent-level switches and the super privilege are the instance's own configuration: given from a supervisor's
			// viewpoint only (the values come straight from the instance fields)
			if c.IsSupervisor {
				out["discovery"] = info.SkillDiscovery
				out["unrestricted"] = info.ToolsUnrestricted
				out["is_supervisor"] = info.IsSupervisor
				// "" means not configured: the agent then follows the caller's language and falls back to the product
				// default, so the empty value is worth showing rather than omitting the key.
				out["default_language"] = info.OutputLanguage
			}
			// Tool grants are given from a supervisor's viewpoint only: they say "who may use which tools", and a non-supervisor
			// has no right to peek at another agent's configuration
			if c.ToolGrants != nil && c.IsSupervisor {
				// Activation belongs to this run, so it is only meaningful when the target is the caller itself: another
				// agent's run cannot be observed from here, and passing ours would report its declare-and-not-activated rows
				// as live.
				var activated map[string]string
				if info.ID == c.AgentID {
					activated = c.LoadedSkills()
				}
				if views, verr := c.ToolGrants.ListAgentGrants(ctx, name, info.ID, activated); verr == nil {
					out["tool_grants"] = views
				}
			}
			// Call grants likewise (who may call whom is administrative configuration): can_call = out-edges, called_by = in-edges
			if c.Relations != nil && c.IsSupervisor {
				if canCall, calledBy, rerr := c.Relations.ListRelations(ctx, info.ID); rerr == nil {
					out["can_call"] = canCall
					out["called_by"] = calledBy
				}
			}
		}
	}
	return out, nil
}

// skillDetailByArgs fetches a skill's detail by name / skill_id (shared by read_skill and use_skill).
func skillDetailByArgs(ctx context.Context, c *Context, args map[string]any) (*SkillDetail, error) {
	name := argString(args, "name")
	if !ValidName(name) {
		return nil, fmt.Errorf("invalid skill name %q", name)
	}
	if c.Skills == nil {
		return nil, errors.New("skill manager not available")
	}
	detail, err := c.Skills.Get(ctx, skillRefFrom(args, ""))
	if err != nil {
		return nil, err
	}
	if detail == nil {
		return nil, fmt.Errorf("skill %q not found", name)
	}
	return detail, nil
}

// readSkillTool reads a skill's SKILL.md - **viewing only**, it does not activate the skill's tool dependencies.
// To work according to a skill use use_skill (it returns the same body and registers the skill as activated for this run).
type readSkillTool struct{}

func (t *readSkillTool) Meta() ToolMeta {
	return ToolMeta{
		Name:     "read_skill",
		Category: CategorySkill,
		Description: "View a skill's SKILL.md — **viewing only**, the tools it declares are NOT activated (use this when you just want to know what it says or look something up). " +
			"tool_deps in the result lists the tool dependencies it declares (raw refs; an empty array means none): shown, not activated — to use those tools call use_skill instead. " +
			"To work according to the skill, call use_skill — that returns the body and makes its tool dependencies available from the next round on. " +
			"Locate it within the currently visible scope; a supervisor editing someone else's skill should use the *_agent_skill tools, or edit the file directly with read_file / edit_file. " +
			"For a tenant-level shared skill the result also carries visibility state: restricted (true = whitelist only) and bound_agents (the whitelist, supervisor only).",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":     map[string]any{"type": "string", "description": "Skill name, e.g. english-joke-writing"},
				"skill_id": map[string]any{"type": "string", "description": "Skill id (optional; only a supervisor can target across ownership by id)" + skillIDDefaultHint},
			},
			"required": []any{"name"},
		},
	}
}

func (t *readSkillTool) Execute(ctx context.Context, c *Context, args map[string]any) (any, error) {
	return skillDetailByArgs(ctx, c, args)
}

// useSkillTool uses a skill: it returns the body and registers it as activated for this run,
// so the tool dependencies it declares enter the tool set from the next LLM call on (and vanish with the Context when the run
// ends).
//
// Why it is separate from read_skill: reading a document is not the same as wanting its tools. A viewing action must not put
// large schemas such as exec / browser / skill-admin into the context (consuming budget, hitting the 50 limit, widening the
// surface of wrong picks).
type useSkillTool struct{}

func (t *useSkillTool) Meta() ToolMeta {
	return ToolMeta{
		Name:     "use_skill",
		Category: CategorySkill,
		Description: "Call this when working according to a skill: returns the SKILL.md body and the tool dependencies it declares (tool_deps), and **activates** them (available from the next call on; expires when this run ends). " +
			"Which tools actually show up still depends on your granted scope (allow) — tool_deps only says what the skill *needs*. " +
			"To merely view the content without acting on it, use read_skill (it activates nothing).",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":     map[string]any{"type": "string", "description": "Skill name, e.g. english-joke-writing"},
				"skill_id": map[string]any{"type": "string", "description": "Skill id (optional; only a supervisor can target across ownership by id)" + skillIDDefaultHint},
			},
			"required": []any{"name"},
		},
	}
}

func (t *useSkillTool) Execute(ctx context.Context, c *Context, args map[string]any) (any, error) {
	detail, err := skillDetailByArgs(ctx, c, args)
	if err != nil {
		return nil, err
	}
	// Activation = injection for this run: the tool dependencies the skill declares take effect from the next LLM call on
	// (see Context.MarkSkillLoaded and agentImpl.refreshToolDefs).
	// The dependency list is registered together with its snapshot: in a production run it is the current version's declaration,
	// and tool resolution no longer goes back to the live table - otherwise "one dependency added in the draft" would light up
	// in production, and the freeze would cover only the display half.
	// Nothing is kept across runs: history refills text only, and a fresh run starts with required-only tools again.
	c.MarkSkillLoaded(detail.ID, detail.Name, detail.ToolDeps)
	return detail, nil
}

// callAgentTool calls another agent synchronously and returns its final result.
type callAgentTool struct{}

func (t *callAgentTool) Meta() ToolMeta {
	return ToolMeta{
		Name:        "call_agent",
		Category:    CategoryAgent,
		Description: "Call another agent to run a task; waits synchronously and returns its final result.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"agent_name": map[string]any{"type": "string", "description": "Target agent name"},
				"arguments":  map[string]any{"type": "object", "description": "Arguments passed to the target agent (optional)"},
			},
			"required": []any{"agent_name"},
		},
	}
}

func (t *callAgentTool) Execute(ctx context.Context, c *Context, args map[string]any) (any, error) {
	name := argString(args, "agent_name")
	if !ValidName(name) {
		return nil, fmt.Errorf("invalid agent name %q (letters, digits and hyphens only)", name)
	}
	// Calling yourself is not allowed: direct self-recursion is meaningless
	if name == c.AgentName {
		return nil, errors.New("cannot call the running agent itself")
	}
	if c.Caller == nil {
		return nil, errors.New("agent caller not available")
	}
	subArgs, _ := args["arguments"].(map[string]any)
	req := CallRequest{
		CallerID:   c.AgentID,
		CallerName: c.AgentName,
		CalleeName: name,
		Args:       subArgs,
		Depth:      c.CallDepth,
		Chain:      c.CallChain,
		UserID:     c.UserID,
		SessionID:  c.SessionID,
		// Child call mode: computed centrally by ChildMode (own draft && the follow-drafts switch).
		// With the switch off the child agent reads the released version, and a missing version is an explicit error - it never
		// falls back to the draft silently.
		DefinitionMode: c.ChildMode(),
	}
	// Any **decided** language travels: a caller pin or this agent's own default_language, so an agent that serves one audience
	// (a Chinese tutor, say) does not hand its children an English default. The bare product fallback stays behind - pinning it
	// would force "en" onto every child of a Chinese conversation, and the callee is otherwise free to resolve its own default.
	if c.OutputLanguageChosen {
		req.OutputLanguage = c.OutputLanguage
	}
	out, err := c.Caller.Call(ctx, req)
	if err != nil {
		return nil, err
	}
	return map[string]any{"agent": name, "result": out}, nil
}

// resolveTargetAgent is the target-agent check shared by the "explicit owner" meta tools: the name is valid, the agent exists
// and is in the normal state.
// It returns (agent name, agent uuid).
//
// The criterion is the same source as the file gate (checkWrite / checkWriteOwn in gate.go) and skillManager.checkOwnerAgent
// (models.IsModifiable): all four must judge together, otherwise "the meta tool can change it, the file tool errors out" splits
// them.
func resolveTargetAgent(ctx context.Context, c *Context, args map[string]any) (string, string, error) {
	name := argString(args, "agent")
	if !ValidName(name) {
		return "", "", fmt.Errorf("invalid agent name %q", name)
	}
	if c.Agents == nil {
		return "", "", errors.New("agent manager not available")
	}
	if c.Skills == nil {
		return "", "", errors.New("skill manager not available")
	}
	info, err := c.Agents.Get(ctx, name)
	if err != nil {
		return "", "", err
	}
	if info == nil {
		return "", "", fmt.Errorf("agent %q not found", name)
	}
	if !models.IsModifiable(info.Status) {
		return "", "", fmt.Errorf("agent %q is deprecated; switch it back to normal first", name)
	}
	return name, info.ID, nil
}

// resolveTargetAgentForDelete is resolveTargetAgent without the status gate: deleting a private skill of a deprecated
// agent is allowed (that agent is deletable in the same state), and requiring "switch it back to normal first" would put
// the agent back online just to remove a file. Every write keeps resolveTargetAgent.
func resolveTargetAgentForDelete(ctx context.Context, c *Context, args map[string]any) (string, string, error) {
	name := argString(args, "agent")
	if !ValidName(name) {
		return "", "", fmt.Errorf("invalid agent name %q", name)
	}
	if c.Agents == nil {
		return "", "", errors.New("agent manager not available")
	}
	if c.Skills == nil {
		return "", "", errors.New("skill manager not available")
	}
	info, err := c.Agents.Get(ctx, name)
	if err != nil {
		return "", "", err
	}
	if info == nil {
		return "", "", fmt.Errorf("agent %q not found", name)
	}
	return name, info.ID, nil
}

// bindSkillTool binds one more skill to an agent (idempotent). Super agent only.
type bindSkillTool struct{}

func (t *bindSkillTool) Meta() ToolMeta {
	return ToolMeta{
		Name:     "bind_skill",
		Category: CategoryAgentAdmin,
		Description: "Bind one more skill to an agent (idempotent; no change when already bound): only bound skills are written into its prompt. " +
			"Only tenant-level skill names are accepted. Supervisor only.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"agent": map[string]any{"type": "string", "description": "Target agent name"},
				"skill": map[string]any{"type": "string", "description": "Tenant-level skill name to bind"},
			},
			"required": []any{"agent", "skill"},
		},
	}
}

func (t *bindSkillTool) Execute(ctx context.Context, c *Context, args map[string]any) (any, error) {
	name, agentID, err := resolveTargetAgent(ctx, c, args)
	if err != nil {
		return nil, err
	}
	skill := argString(args, "skill")
	if err := c.Skills.BindAgentSkill(ctx, agentID, skill); err != nil {
		return nil, err
	}
	return map[string]any{"agent": name, "skill": skill, "bound": true}, nil
}

// unbindSkillTool removes one skill binding from an agent (idempotent). Super agent only.
type unbindSkillTool struct{}

func (t *unbindSkillTool) Meta() ToolMeta {
	return ToolMeta{
		Name:        "unbind_skill",
		Category:    CategoryAgentAdmin,
		Description: "Remove one skill binding from an agent (idempotent; no change when not bound). Only tenant-level skill names are accepted. Supervisor only.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"agent": map[string]any{"type": "string", "description": "Target agent name"},
				"skill": map[string]any{"type": "string", "description": "Tenant-level skill name to unbind"},
			},
			"required": []any{"agent", "skill"},
		},
	}
}

func (t *unbindSkillTool) Execute(ctx context.Context, c *Context, args map[string]any) (any, error) {
	name, agentID, err := resolveTargetAgent(ctx, c, args)
	if err != nil {
		return nil, err
	}
	skill := argString(args, "skill")
	if err := c.Skills.UnbindAgentSkill(ctx, agentID, skill); err != nil {
		return nil, err
	}
	return map[string]any{"agent": name, "skill": skill, "unbound": true}, nil
}

// searchToolsTool searches the **grantable** tool catalog (super agent only).
//
// Why it is needed: a grant operates on a string (`mcp:fs:read_file`), and the model has no access to the fact "which
// connections this tenant has, and which tools each connection offers" - without a query entry point it can only rely on the
// user naming a connection, or write the ref wrong and read the error. The latter is trial and error, not discovery.
//
// The output is deliberately the **grant ref** (mcp:fs:read_file / builtin:file) rather than the model-side tool name
// (mcp__fs__read_file): the next action of this tool is grant_agent_tool, which pastes the ref.
// It returns no argument schema either - that is only needed to "call", it consumes context, and it becomes visible once the
// grant is in place.
type searchToolsTool struct{}

func (t *searchToolsTool) Meta() ToolMeta {
	return ToolMeta{
		Name:     "search_tools",
		Category: CategoryToolGrant,
		Description: "Search the **grantable** tool catalog (supervisor only): builtin tools / connections (MCP, RPC) / plugins / custom tools / local endpoints / tool groups. " +
			"The returned refs can be pasted straight into grant_agent_tool (e.g. builtin:file for a whole group, mcp:fs for a whole connection, mcp:fs:read_file for a single tool). " +
			"With query omitted it only lists the group overview (group ref + member count + first member refs + availability) and does not expand to individual tools; " +
			"with a query it expands the matching tools (the keyword matches connection name / tool name / description / ref fragment, e.g. fs, read_file, mcp). " +
			"Passing agent filters the catalog by that agent's identity (tools marked \"supervisor only\" never appear for a non-supervisor) " +
			"and fills in the refs it has already been granted (granted) — look it up before granting, do not write refs from memory.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "Keyword: connection name / tool name / description / ref fragment (e.g. fs, read_file, mcp, builtin:file). Omit to list only the group overview",
				},
				"agent": map[string]any{
					"type":        "string",
					"description": "Optional. Target agent name: filter the catalog by its identity and fill in its current grant state. Omit to use the caller's own identity",
				},
			},
		},
	}
}

func (t *searchToolsTool) Execute(ctx context.Context, c *Context, args map[string]any) (any, error) {
	if c.ToolCatalog == nil {
		return nil, errors.New("tool catalog not available")
	}
	// By default the caller's own identity and grant state are used
	name, id := c.AgentName, c.AgentID
	if want := argString(args, "agent"); want != "" && want != c.AgentName {
		if c.Agents == nil {
			return nil, errors.New("agent manager not available")
		}
		info, err := c.Agents.Get(ctx, want)
		if err != nil {
			return nil, err
		}
		if info == nil {
			return nil, fmt.Errorf("agent %q not found", want)
		}
		name, id = info.Name, info.ID
	}
	// Read-only search: it does not check that the target is in the normal state (a deprecated agent's holdings can be inspected
	// too)
	return c.ToolCatalog.Search(ctx, argString(args, "query"), name, id)
}

// grantAgentToolTool grants one tool authorization to an agent (super agent only).
//
// When kind is omitted the system decides - the model cannot see the tool_deps each skill declares (they live in the
// skill_tool_grants table, not in the SKILL.md body), and guessing allow results in "granted but never effective".
// An explicit required / allow / deny is validated by its own semantics, see service/agent/toolgrants.go.
type grantAgentToolTool struct{}

func (t *grantAgentToolTool) Meta() ToolMeta {
	return ToolMeta{
		Name:     "grant_agent_tool",
		Category: CategoryToolGrant,
		Description: "Grant one tool authorization to a **specified agent** (supervisor only; the target must be editable). " +
			"kind: required (always injected, independent of skills) / allow (on demand: some skill must already declare that dependency, " +
			"and it is injected only after use_skill activates it) / deny (explicit refusal: not injected even if a skill declares it, and it overrides the existing row for that ref). " +
			"If kind is omitted the system decides: allow when some skill declares it, otherwise required. " +
			"ref looks like builtin:exec / builtin:file (category group), builtin:read_file (single tool), mcp:<connection>:<tool>, group:<group name>. " +
			"Only one row is kept per ref — changing kind overwrites the old row (the result says what was overwritten). " +
			"Tools marked \"supervisor only\" cannot be granted to a non-supervisor (the call is explicitly refused). " +
			"You may grant yourself (the supervisor is the admin): the tool set is recomputed on the next round of this run, no new session needed. " +
			"Deprecated agents always refuse writes, yourself included.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"agent": map[string]any{"type": "string", "description": "Target agent name (may be yourself)"},
				"ref":   map[string]any{"type": "string", "description": "Tool reference: builtin:<tool>, builtin:<category>, mcp:<connection>[:<tool>], rpc:<connection>[:<tool>], local:rpc[:<endpoint>], group:<group name>"},
				"kind": map[string]any{
					"type":        "string",
					"enum":        []any{"required", "allow", "deny"},
					"description": "Optional. required = always injected; allow = on demand (some skill must declare the dependency); deny = explicit refusal that overrides the old row. Omit to let the system decide",
				},
			},
			"required": []any{"agent", "ref"},
		},
	}
}

func (t *grantAgentToolTool) Execute(ctx context.Context, c *Context, args map[string]any) (any, error) {
	name, agentID, err := resolveTargetAgent(ctx, c, args)
	if err != nil {
		return nil, err
	}
	// D5 (revised): the super agent may grant itself - it is the administrator; the boundary rests on status (deprecated always
	// refuses, and only the admin side can change status, so self-unlocking cannot happen).
	if c.ToolGrants == nil {
		return nil, errors.New("tool grant manager not available")
	}
	ref := argString(args, "ref")
	kind, reason, err := c.ToolGrants.GrantAgentTool(ctx, name, agentID, ref, argString(args, "kind"))
	if err != nil {
		return nil, err
	}
	// It changed yourself: this run's tool set must be recomputed, otherwise the fresh grant is invisible to you (and gets
	// mistaken for a failure and retried).
	if name == c.AgentName {
		c.MarkToolsDirty()
	}
	return map[string]any{
		"agent": name, "ref": ref, "grant_kind": kind, "reason": reason, "granted": true,
	}, nil
}

// revokeAgentToolTool removes one tool authorization from an agent (super agent only).
type revokeAgentToolTool struct{}

func (t *revokeAgentToolTool) Meta() ToolMeta {
	return ToolMeta{
		Name:     "revoke_agent_tool",
		Category: CategoryToolGrant,
		Description: "Remove one tool authorization from a **specified agent** (supervisor only; the target must be editable; idempotent). " +
			"Removal is by exact ref and does not care whether the row was written as required / allow / deny (the result returns the kind of the removed row for auditing). " +
			"You may revoke your own (the supervisor is the admin): the tool set is recomputed on the next round of this run. " +
			"Check the tool_grants returned by read_agent first, so you don't delete a grant that is still in use.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"agent": map[string]any{"type": "string", "description": "Target agent name"},
				"ref":   map[string]any{"type": "string", "description": "Tool reference to remove (the same one used when granting)"},
			},
			"required": []any{"agent", "ref"},
		},
	}
}

func (t *revokeAgentToolTool) Execute(ctx context.Context, c *Context, args map[string]any) (any, error) {
	name, agentID, err := resolveTargetAgent(ctx, c, args)
	if err != nil {
		return nil, err
	}
	if c.ToolGrants == nil {
		return nil, errors.New("tool grant manager not available")
	}
	ref := argString(args, "ref")
	removed, kind, err := c.ToolGrants.RevokeAgentTool(ctx, agentID, ref)
	if err != nil {
		return nil, err
	}
	// Revoking your own grant needs the same recompute, otherwise the tools seem not to disappear until the next session.
	if name == c.AgentName {
		c.MarkToolsDirty()
	}
	return map[string]any{
		"agent": name, "ref": ref, "removed": removed, "grant_kind": kind, "revoked": true,
	}, nil
}

// resolveCallTarget locates the callee agent (valid name + exists); a non-empty selfName additionally rejects pointing at
// yourself.
// The write guards (non-super agent / not deprecated) stay with the caller - revoking a grant should not be blocked by them
// (stale data may be exactly what needs cleaning up).
func resolveCallTarget(ctx context.Context, c *Context, target, selfName string) (*AgentInfo, error) {
	if !ValidName(target) {
		return nil, fmt.Errorf("invalid agent name %q", target)
	}
	if selfName != "" && target == selfName {
		return nil, fmt.Errorf("agent %q cannot call itself", target)
	}
	if c.Agents == nil {
		return nil, errors.New("agent manager not available")
	}
	info, err := c.Agents.Get(ctx, target)
	if err != nil {
		return nil, err
	}
	if info == nil {
		return nil, fmt.Errorf("agent %q not found", target)
	}
	return info, nil
}

// allowAgentCallTool grants an agent permission to call another agent (super agent only).
//
// A call grant is one row in agent_relations - caller.go's check treats every agent alike (no row means refusal, with no
// exemption for the super agent), so "let the super agent call poet" really has to write that row.
type allowAgentCallTool struct{}

func (t *allowAgentCallTool) Meta() ToolMeta {
	return ToolMeta{
		Name:     "allow_agent_call",
		Category: CategoryToolGrant,
		Description: "Let a **specified agent** call another agent (supervisor only; idempotent). " +
			"This only writes one call relation (agent_relations); it is enforced when the call actually happens (call_agent). " +
			"The callee may be any non-deprecated agent (including the supervisor — when invoked it still has the full admin surface), but not yourself. " +
			"If the target has no released version yet the result carries a warning: the grant is still written, but calling it through a formal entry point (MCP / RPC / endpoint / flow) will fail. " +
			"Before changing anything, read_agent shows the target's current can_call / called_by.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"agent":  map[string]any{"type": "string", "description": "Caller agent name (must be editable)"},
				"target": map[string]any{"type": "string", "description": "Callee agent name (the supervisor included; cannot be yourself)"},
			},
			"required": []any{"agent", "target"},
		},
	}
}

func (t *allowAgentCallTool) Execute(ctx context.Context, c *Context, args map[string]any) (any, error) {
	name, agentID, err := resolveTargetAgent(ctx, c, args)
	if err != nil {
		return nil, err
	}
	info, err := resolveCallTarget(ctx, c, argString(args, "target"), name)
	if err != nil {
		return nil, err
	}
	// Write guard: a deprecated agent cannot be called at all, so nothing is written - otherwise a dead grant that can never be
	// used would be left behind.
	// supervisor is no longer excluded - it can be called, and when invoked it always reads the draft via
	// resolveDefinitionMode.
	if !models.IsRunnable(info.Status) {
		return nil, fmt.Errorf("agent %q is deprecated; it cannot be called", info.Name)
	}
	if c.Relations == nil {
		return nil, errors.New("agent relations manager not available")
	}
	if err := c.Relations.AllowCall(ctx, agentID, info.ID); err != nil {
		return nil, err
	}
	out := map[string]any{"agent": name, "target": info.Name, "allowed": true}
	// A grant and a version are two different things: this edge only means "calling is allowed", and the production chain still
	// requires the callee to have an is_current version.
	// When the target was never released, a parent agent calling it through a production entry (MCP / RPC / endpoint / flow) hits
	// ErrNoReleasedVersion and only the editor debugger can run it - so warn, do not block (the common path is "create the agent,
	// grant, then release").
	if !agentHasReleasedVersion(c.Schema, info.Name) {
		out["unreleased"] = true
		out["warning"] = fmt.Sprintf("agent %q has no released version yet: the grant is written, but calling it through the live entry points (MCP / RPC / endpoint / flow) reports that no released version exists yet; only the editor debugger can run it", info.Name)
	}
	return out, nil
}

// agentHasReleasedVersion reports whether the target already has a released version (is_current).
// It is only used for the warning: when the query is unavailable or errors it counts as "yes" - a failed probe must not make a
// successful grant look suspicious.
func agentHasReleasedVersion(schema, agentName string) bool {
	if db.DB == nil {
		return true
	}
	_, _, err := versioning.AgentCurrentVersion(schema, agentName)
	return !errors.Is(err, versions.ErrNoReleasedVersion)
}

// revokeAgentCallTool revokes an agent's permission to call another agent (super agent only).
type revokeAgentCallTool struct{}

func (t *revokeAgentCallTool) Meta() ToolMeta {
	return ToolMeta{
		Name:     "revoke_agent_call",
		Category: CategoryToolGrant,
		Description: "Revoke a **specified agent**'s permission to call another agent (supervisor only; idempotent, returns the number of rows deleted). " +
			"Check can_call from read_agent first, so you don't break a call chain that is still in use.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"agent":  map[string]any{"type": "string", "description": "Caller agent name"},
				"target": map[string]any{"type": "string", "description": "Callee agent name"},
			},
			"required": []any{"agent", "target"},
		},
	}
}

func (t *revokeAgentCallTool) Execute(ctx context.Context, c *Context, args map[string]any) (any, error) {
	name, agentID, err := resolveTargetAgent(ctx, c, args)
	if err != nil {
		return nil, err
	}
	// Revoking does not apply the write guard: stale data (such as main having been granted by mistake) must still be cleanable
	info, err := resolveCallTarget(ctx, c, argString(args, "target"), "")
	if err != nil {
		return nil, err
	}
	if c.Relations == nil {
		return nil, errors.New("agent relations manager not available")
	}
	removed, err := c.Relations.RevokeCall(ctx, agentID, info.ID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"agent": name, "target": info.Name, "removed": removed, "revoked": true}, nil
}

func init() {
	Register(&createAgentTool{})
	Register(&updateAgentTool{})
	Register(&deleteAgentTool{})
	Register(&createOwnSkillTool{})
	Register(&updateOwnSkillTool{})
	Register(&deleteOwnSkillTool{})
	Register(&createSharedSkillTool{})
	Register(&updateSharedSkillTool{})
	Register(&deleteSharedSkillTool{})
	Register(&createAgentSkillTool{})
	Register(&updateAgentSkillTool{})
	Register(&deleteAgentSkillTool{})
	Register(&bindSkillTool{})
	Register(&unbindSkillTool{})
	Register(&grantSkillToolTool{})
	Register(&revokeSkillToolTool{})
	Register(&searchToolsTool{})
	Register(&grantAgentToolTool{})
	Register(&revokeAgentToolTool{})
	Register(&allowAgentCallTool{})
	Register(&revokeAgentCallTool{})
	Register(&listAgentsTool{})
	Register(&listSkillsTool{})
	Register(&readAgentTool{})
	Register(&readSkillTool{})
	Register(&useSkillTool{})
	Register(&callAgentTool{})
	// The meta tools for custom tools (category custom-tool, super agent only by construction).
	Register(&createCustomToolTool{})
	Register(&updateCustomToolTool{})
	Register(&deleteCustomToolTool{})
	Register(&listCustomToolsTool{})
}
