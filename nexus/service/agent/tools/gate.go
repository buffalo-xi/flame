package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"flame/nexus/shared/models"
	"flame/nexus/shared/paths"
)

// Root-addressing gate: "a path starting with / = counted from the tenant root" (path syntax in paths.SafePathEx).
//
// The syntax is available to every agent, but **permission is graded by identity** (see the branching in resolveRead /
// resolveWrite):
//
//	super agent     -> the whole tenant (checkWrite; reads are unrestricted, only writes are gated)
//	non-super agent -> **read**: its own definition directory agents/{self}/** (AGENT.md included);
//	                   **write**: only its own private skill directory agents/{self}/skills/{skill}/**,
//	                   and it has to pass two status gates (checkWriteOwn). Other agents' definitions and all other entities
//	                   are unreachable.
//
// The read/write asymmetry is intentional: reading your own definition is "introspection" (self-description, diagnosis,
// comparing this run's behaviour with the previous one) and changes nothing; writing your own AGENT.md is an identity change
// and must be made by a human or a supervisor - otherwise "who produced this draft" cannot be answered, and the premise of
// "no self-release" in the version model is gone.
//
// Why a non-super agent gets this slot: under the old "private skill = a single-file knowledge document" rule, "editing your
// own skill" could only change the SKILL.md body (update_own_skill), while the super agent and the admin side have long been
// able to manage a whole skill directory (attached files / subdirectories). This slot aligns the three - an agent in edit mode
// can manage its own skill's attached resources with the same file tools.
//
// The unit of decision is the **entity directory** (= one database row):
//
//	agents/{agent}                  -> the agent entity
//	agents/{agent}/skills/{skill}   -> the skill entity (owned by {agent})
//	skills/{skill}                  -> the tenant-level skill entity
//	users/{uid}                     -> the user domain (the user themselves, or the super agent)
//
// Rules (both tiers):
//   - any file or subdirectory **inside** an entity directory (the AGENT.md / SKILL.md body included, plus any attached
//     resource) -> writable by the file tools, subject to that entity's status gate (models.IsModifiable); writing a private
//     agent skill also has to pass the **owning agent**'s status gate (a non-editable agent freezes the whole area, the super
//     agent included);
//   - writing / deleting the entity's **root directory itself** -> refused, it belongs to the meta tools (create_* / delete_*);
//   - writing a **non-existent entity** -> refused, pointing at create_* (this prevents directories growing that are not
//     registered).
//
// This file holds no rules: the single source of truth for status decisions is the pure predicates in shared/models; here we
// only recognise "path -> entity", fetch the instance and call the predicate. It does not stack on the business layer's gates
// (manager.go / skills.go) - writes through business methods are judged there, and only cross-domain writes that go straight to
// storage are judged here.

// pathTarget is the entity a tenant-root-relative path resolves to.
type pathTarget struct {
	kind   string // "user" | "agent" | "skill"
	owner  string // the owning agent name of a skill ("" for tenant-level); for an agent, the agent name
	name   string // entity name: agent name / skill name / uid
	isRoot bool   // the path is exactly the entity's root directory (not a file inside it)
}

// resolveTarget does pure string parsing (no IO, so it is unit-testable).
func resolveTarget(tenantRel string) (pathTarget, error) {
	seg := strings.Split(tenantRel, "/")
	if len(seg) < 2 || seg[0] == "" || seg[1] == "" {
		return pathTarget{}, fmt.Errorf("path not allowed (missing target): %s", tenantRel)
	}
	switch seg[0] {
	case paths.AgentsSubdir:
		agent := seg[1]
		if len(seg) >= 3 && seg[2] == paths.AgentSkillsSubdir {
			if len(seg) < 4 || seg[3] == "" {
				return pathTarget{}, fmt.Errorf("path not allowed (missing skill name): %s", tenantRel)
			}
			return pathTarget{kind: "skill", owner: agent, name: seg[3], isRoot: len(seg) == 4}, nil
		}
		return pathTarget{kind: "agent", owner: agent, name: agent, isRoot: len(seg) == 2}, nil
	case paths.TenantSkillsSubdir:
		return pathTarget{kind: "skill", owner: "", name: seg[1], isRoot: len(seg) == 2}, nil
	case paths.ToolsSubdir:
		// Tenant-level custom tool: tools/{name}/**. It is definition-domain content (the code lives on disk),
		// and the entity is a row of {schema}.custom_tools, a peer of skills/{name}.
		return pathTarget{kind: "custom_tool", name: seg[1], isRoot: len(seg) == 2}, nil
	case paths.UsersSubdir:
		return pathTarget{kind: "user", name: seg[1], isRoot: len(seg) == 2}, nil
	default:
		return pathTarget{}, fmt.Errorf("path not allowed: %s", tenantRel)
	}
}

// checkWrite is the root-addressing write gate (supervisor tier): identity (super agent only) + not the entity root + the
// target exists and is modifiable.
// The identity branch already happened at the call site (resolveWrite); one is kept here as defence in depth.
func checkWrite(ctx context.Context, c *Context, tenantRel string) error {
	if !c.IsSupervisor {
		return errors.New("only a supervisor may access paths outside the workspace")
	}
	t, err := resolveTarget(tenantRel)
	if err != nil {
		return err
	}
	// Writing / deleting the entity root directory itself is always refused; that belongs to the meta tools
	if t.isRoot {
		return fmt.Errorf("refusing to write the entity directory itself (%s); use the matching meta tool", tenantRel)
	}
	switch t.kind {
	case "user":
		// User domain: the user themselves or the super agent (identity was already confirmed outside this branch)
		if t.name != c.UserID {
			return fmt.Errorf("not allowed to access files of user %q", t.name)
		}
		return nil
	case "agent":
		if c.Agents == nil {
			return errors.New("agent manager not available")
		}
		info, err := c.Agents.Get(ctx, t.name)
		if err != nil {
			return err
		}
		if info == nil {
			return fmt.Errorf("agent %q not found; use create_agent to register a new agent", t.name)
		}
		if !models.IsModifiable(info.Status) {
			return fmt.Errorf("agent %q is deprecated; switch it back to normal first", t.name)
		}
		return nil
	case "skill":
		if c.Skills == nil {
			return errors.New("skill manager not available")
		}
		ownerID := ""
		if t.owner != "" {
			if c.Agents == nil {
				return errors.New("agent manager not available")
			}
			ai, err := c.Agents.Get(ctx, t.owner)
			if err != nil {
				return err
			}
			if ai == nil {
				return fmt.Errorf("agent %q not found", t.owner)
			}
			// Whether the owning agent may write its private skill - the same criterion as item 3 of checkWriteOwn
			// (there it judges "self", here "the owner"), and the super agent gets no exception: otherwise the same criterion
			// would freeze for a non-super agent and stay open for a supervisor.
			if !models.IsModifiable(ai.Status) {
				return fmt.Errorf("agent %q is deprecated; its private skills are frozen", t.owner)
			}
			ownerID = ai.ID
		}
		info, err := c.Skills.FindForWrite(ctx, ownerID, t.name)
		if err != nil {
			return err
		}
		if info == nil {
			return fmt.Errorf("skill %q not found; use create_own_skill / create_shared_skill / create_agent_skill to register a new skill", t.name)
		}
		if !models.IsModifiable(info.Status) {
			return fmt.Errorf("skill %q is deprecated; switch it back to normal first", t.name)
		}
		return nil
	case "custom_tool":
		if c.CustomTools == nil {
			return errors.New("script tool source not available")
		}
		status, found, err := c.CustomTools.Status(ctx, t.name)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("custom tool %q does not exist; create it with create_custom_tool (or the \"Custom tools\" page in the admin UI) instead of creating the directory directly", t.name)
		}
		if !models.IsModifiable(status) {
			return fmt.Errorf("custom tool %q is locked or deprecated; switch it back to normal first", t.name)
		}
		return nil
	}
	return fmt.Errorf("path not allowed: %s", tenantRel)
}

// checkReadOwn is the root-addressing **read** gate: the super agent is unrestricted; a non-super agent may read its own
// definition directory (agents/{self}/**, AGENT.md and private skills' attached files included), everything else is refused.
//
// Why reading is looser than writing: reading your own definition is introspection; writing your own AGENT.md is an identity
// change (see checkWriteOwn), and the two are not the same order. Reading does not query the database and sets no status gate
// - a deprecated entity can be read too (D4); when the path does not exist, storage reports not found.
func checkReadOwn(c *Context, tenantRel string) error {
	if c.IsSupervisor {
		return nil
	}
	t, err := resolveTarget(tenantRel)
	if err != nil {
		return err
	}
	// Your own definition directory: the whole tree is readable (AGENT.md, private skill bodies and attached files are in it)
	if t.kind == "agent" && t.name == c.AgentName {
		return nil
	}
	if t.kind == "skill" && t.owner == c.AgentName {
		return nil
	}
	return fmt.Errorf("not allowed to access %q: you may only read your own definition directory (/agents/%s/**), "+
		"and only /agents/%s/skills/<skill>/... inside it is writable", tenantRel, c.AgentName, c.AgentName)
}

// checkWriteOwn is the root-addressing **write** gate (non-super tier): it only allows inside your own private skill directory.
//
// Five criteria, none optional:
//  1. shape and ownership: must be agents/{self}/skills/{skill}/<non-empty>; everything else (another agent / another agent's
//     skill / tenant-level skills/ / users/) is refused;
//  2. the entity root directory itself cannot be written directly (same as D6: creating / changing / deleting an entity goes
//     through the meta tools);
//  3. **you are in the normal state** - a deprecated (3) agent can neither run nor be changed;
//  4. the skill is registered and belongs to you (FindForWrite looks it up by "own agentID + name");
//  5. the skill itself is in the normal state.
func checkWriteOwn(ctx context.Context, c *Context, tenantRel string) error {
	t, err := resolveTarget(tenantRel)
	if err != nil {
		return err
	}
	// 1) shape + ownership
	if t.kind != "skill" || t.owner != c.AgentName {
		return fmt.Errorf("not allowed to write %q: you may only change your own private skill directory (/agents/%s/skills/<skill>/...)",
			tenantRel, c.AgentName)
	}
	// 2) the entity root directory itself belongs to the meta tools
	if t.isRoot {
		return fmt.Errorf("refusing to write the skill directory itself (%s); use create_own_skill / update_own_skill / delete_own_skill", tenantRel)
	}
	// 3) you must be in the normal state
	if c.Agents == nil {
		return errors.New("agent manager not available")
	}
	self, err := c.Agents.Get(ctx, c.AgentName)
	if err != nil {
		return err
	}
	if self == nil {
		return fmt.Errorf("agent %q not found", c.AgentName)
	}
	if !models.IsModifiable(self.Status) {
		return fmt.Errorf("agent %q is deprecated; its private skills are frozen", c.AgentName)
	}
	// 4) the skill must be a private skill registered to you. With AgentID missing, FindForWrite would look it up as
	//    "tenant-level" (agentID=="" in FindByName means tenant-level) and mistake a shared skill for a private one - block that
	//    first.
	if c.AgentID == "" {
		return errors.New("no agent context for private skill")
	}
	if c.Skills == nil {
		return errors.New("skill manager not available")
	}
	info, err := c.Skills.FindForWrite(ctx, c.AgentID, t.name)
	if err != nil {
		return err
	}
	if info == nil {
		return fmt.Errorf("skill %q not found; use create_own_skill to register a new private skill", t.name)
	}
	// 5) the skill itself is modifiable
	if !models.IsModifiable(info.Status) {
		return fmt.Errorf("skill %q is locked or deprecated; switch it back to normal first", t.name)
	}
	return nil
}

// entityLikePath recognises the path shape of a "fake entity directory" inside the session workspace - i.e. imitating
// agents/{name}/... or skills/{name}/... under the tenant root (skills covers both the private and the tenant-level spelling).
//
// Why it needs its own check: such a path has no "/" prefix, so SafePathEx treats it as a workspace-relative path; for a
// non-super agent it is neither registered in the database nor effective, it only leaves a fake entity directory in the
// workspace and makes the model believe "it was created" - a direct source of hallucinated success. (Root addressing with a
// "/" prefix goes through checkWrite / checkWriteOwn and is not affected.)
//
// It returns the correct guidance for the model; ok=false means it does not look like an entity, so it passes as an ordinary
// workspace path.
// selfName separates "your own definition domain (which has a right answer: switch to root addressing)" from "someone else's
// definition domain".
// For someone else's entity we **must not** say "use the meta tools": create_agent / create_shared_skill are super (super
// agent only), and a non-super agent can never reach them (toolgrant.Catalog does not produce them + the execution-layer hard
// gate in registry.go).
func entityLikePath(rel, selfName string) (hint string, ok bool) {
	seg := strings.Split(rel, "/")
	if len(seg) < 2 || seg[1] == "" {
		return "", false
	}
	switch seg[0] {
	case paths.AgentsSubdir:
		if seg[1] == selfName {
			return fmt.Sprintf("this is how you address your own definition domain, but the leading \"/\" is missing, so it will not take effect; "+
				"to edit your own skills use /agents/%s/skills/<skill-name>/... (AGENT.md can only be changed through meta tools)", selfName), true
		}
		return "agent definitions cannot be created with file tools (and you do not have that permission anyway); to create or change another agent, ask an admin or the supervisor.", true
	case paths.TenantSkillsSubdir:
		return fmt.Sprintf("tenant-level shared skills cannot be changed here; for your own private skill use /agents/%s/skills/<skill-name>/...", selfName), true
	}
	return "", false
}

// parseRootAddr only normalises the path and recognises "is this root addressing"; it contains **no gate at all**.
// Parsing and authorising are split deliberately: if the write side reused the read side, it would hit the read gate first and
// report an error worded for reads (the read and write allowlists have the same shape, but the gates and the wording differ -
// see checkReadOwn / checkWriteOwn).
func parseRootAddr(c *Context, p string) (sp, rel string, fromRoot bool, err error) {
	rel, fromRoot, err = SafePathEx(p, true)
	if err != nil {
		return "", "", false, err
	}
	if !fromRoot {
		return c.StoragePath(rel), rel, false, nil
	}
	return c.TenantPath(rel), rel, true, nil
}

// isVersionDomain reports whether a path falls in the version domain (versions/ under the tenant root).
//
// The version domain is the historical snapshot of "the definition running in production": it may contain credentials,
// policies and deprecated skill bodies that were rolled back, so a model reading it is a historical leak; writing into it is
// worse still - it silently changes production. So it is closed to **every identity** (the super agent included).
func isVersionDomain(tenantRel string) bool {
	seg := strings.Split(tenantRel, "/")
	return len(seg) > 0 && seg[0] == paths.VersionsSubdir
}

// resolveRead resolves a tool argument path into a storage key (read side).
// Root addressing branches by identity: the super agent is unrestricted (D4: reads are unrestricted); a non-super agent may
// read its own definition directory (agents/{self}/**, AGENT.md and private skills included).
//
// Exception: the version domain is unreachable for everyone. It is not in checkReadOwn's switch - the super agent's read never
// goes through that layer at all (checkReadOwn returns nil on its first line), so this one must be blocked before the identity
// branch.
func resolveRead(c *Context, p string) (sp, rel string, fromRoot bool, err error) {
	sp, rel, fromRoot, err = parseRootAddr(c, p)
	if err != nil || !fromRoot {
		return sp, rel, fromRoot, err
	}
	if isVersionDomain(rel) {
		return "", "", false, fmt.Errorf("version directories are not exposed to file tools: %s"+
			" (historical versions are only for rollback and audit; read the body through read_skill / the entity's file API)", rel)
	}
	if err := checkReadOwn(c, rel); err != nil {
		return "", "", false, err
	}
	return sp, rel, true, nil
}

// resolveWrite is like resolveRead, but write-side tools pass extra gates:
//  1. root addressing ("/" prefix) -> the super agent goes through checkWrite (the whole tenant + entity status gates);
//     a non-super agent goes through checkWriteOwn (its own private skill directory only + the double status gate of itself and
//     the skill);
//  2. a fake entity directory inside the workspace -> entityLikePath (non-super agents only). Strictness B: refuse only when
//     the target **does not exist** (i.e. "a fake entity is being created"); an existing one passes as an ordinary workspace
//     file, which covers every hallucination scenario without breaking a user-uploaded directory of the same name.
func resolveWrite(ctx context.Context, c *Context, p string) (sp, rel string, fromRoot bool, err error) {
	sp, rel, fromRoot, err = parseRootAddr(c, p)
	if err != nil {
		return "", "", false, err
	}
	if fromRoot {
		if c.IsSupervisor {
			if err := checkWrite(ctx, c, rel); err != nil {
				return "", "", false, err
			}
		} else if err := checkWriteOwn(ctx, c, rel); err != nil {
			return "", "", false, err
		}
		return sp, rel, true, nil
	}
	if c.Storage != nil && !c.IsSupervisor {
		if hint, ok := entityLikePath(rel, c.AgentName); ok {
			exists, e := c.Storage.Exists(ctx, sp)
			if e != nil {
				return "", "", false, e
			}
			if !exists {
				return "", "", false, fmt.Errorf("path %q resolves inside your session workspace, so it will not take effect: %s", rel, hint)
			}
		}
	}
	return sp, rel, false, nil
}
