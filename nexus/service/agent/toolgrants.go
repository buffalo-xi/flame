package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"flame/nexus/service/agent/toolgrant"
	"flame/nexus/service/agent/tools"
	"flame/nexus/shared/auth"
	"flame/nexus/shared/models"
	agents "flame/nexus/shared/repository/agents"
	skills "flame/nexus/shared/repository/skills"
	"flame/nexus/shared/repository/toolgrants"
)

// toolGrantManager implements tools.ToolGrantsManager: it splits "grant one tool to an agent" into three steps - ref validation
// (toolgrant.Catalog), automatic allow / required assignment, and echoing the effective state.
//
// Dependency direction: the toolgrant package imports the tools package (it uses it to fetch built-in tools and categories, see catalog.go),
// so the validation and assignment logic can only live in this layer; the tools package exposes the interface and read-only views only - the
// same consideration as skillManager.buildToolDeps.
//
// The criterion is **the same source as the runtime**: candidates come only from the skills "activated in this round" (agentImpl feeds
// Context.LoadedSkills into the Resolver), and the binding relation is used here only to answer "after reading which skill will it light up" -
// the effective verdict is not recomputed in this layer.
type toolGrantManager struct {
	schema       string
	agentID      string // current agent uuid (written to updated_by)
	agentName    string // current agent name
	isSupervisor bool   // identity criterion (agents.is_supervisor)
	userID       string
	repo         toolgrants.Repository
	skills       skills.Repository
	// agentsRepo resolves the target agent's identity (used by targetIsSupervisor). Tests inject a stub; the runtime uses the real repository.
	agentsRepo agents.Repository
	// mode is the definition-reading mode: it decides which copy "the dependencies a skill declares" is taken from (see skillDeps).
	mode tools.DefinitionMode
	// meta is the version-metadata reader shared with skillManager: the admin side and the runtime side must share one source.
	meta *skillMetaReader
}

// NewToolGrantManager creates tools.ToolGrantsManager (tenant and current agent identity fixed).
//
// mode must be the same value passed to skillManager: in a production run "the dependencies a skill declares" comes from the current version,
// otherwise grant_agent_tool may judge an allow that never lights up at runtime (present in the draft, absent from the version).
func NewToolGrantManager(schema, agentID, agentName, userID string, isSupervisor bool, mode tools.DefinitionMode) tools.ToolGrantsManager {
	return &toolGrantManager{
		schema:       schema,
		agentID:      agentID,
		agentName:    agentName,
		isSupervisor: isSupervisor,
		userID:       userID,
		repo:         toolgrants.NewRepository(),
		skills:       skills.NewRepository(),
		agentsRepo:   agents.NewRepository(),
		mode:         mode,
		meta:         newSkillMetaReader(schema, agentID, agentName, agents.NewRepository(), mode),
	}
}

// agentRepo is the owning agent repository. Built lazily: a zero-value manager created directly in a test does not panic over a missing
// injection, and a test that needs a stub simply fills in agentsRepo.
func (m *toolGrantManager) agentRepo() agents.Repository {
	if m.agentsRepo == nil {
		m.agentsRepo = agents.NewRepository()
	}
	return m.agentsRepo
}

// metaReader is the version-metadata reader. Built lazily: a zero-value manager created directly in a test falls back to the draft criterion.
func (m *toolGrantManager) metaReader() *skillMetaReader {
	if m.meta == nil {
		m.meta = newSkillMetaReader(m.schema, m.agentID, m.agentName, agents.NewRepository(), m.mode)
	}
	return m.meta
}

// skillDeps returns the tool dependencies a skill declares, with the same criterion as the runtime side: a production run reads the snapshot
// of the current version (the frozen declaration); draft / no version information falls back to skill_tool_grants.
func (m *toolGrantManager) skillDeps(s models.Skill) ([]string, error) {
	if refs, ok := m.metaReader().depsOf(s); ok {
		return refs, nil
	}
	list, err := m.repo.ListSkillGrants(m.schema, s.ID)
	if err != nil {
		return nil, err
	}
	refs := make([]string, 0, len(list))
	for _, g := range list {
		refs = append(refs, g.Ref)
	}
	return refs, nil
}

// injectedSkills lists the skills injected into the target agent (private + bound).
// Its use is narrowed to "which skill backs this allow row" - it no longer means "already effective".
func (m *toolGrantManager) injectedSkills(agentID string) ([]models.Skill, error) {
	return m.skills.ListInjected(m.schema, agentID)
}

// GrantAgentTool grants one tool to the target agent.
//
// kind says "how this grant takes effect", not "how strict it is":
//
//	""       automatic (default): a bound skill declares the dependency -> allow, otherwise required.
//	         The model cannot see skill_tool_grants, and a wrong guess costs "wrote an allow that never takes effect", so
//	         when kind is omitted the system computes it with the Resolver's own criterion.
//	required always injected: even when a skill declares it, it stays resident - for "this capability must not degrade to on-demand".
//	allow    on demand: a bound skill must declare it, otherwise the write is refused (the row would never take effect).
//	deny     explicit refusal: overrides the existing required / allow row of that ref (one row per ref), and is subtracted even when a
//	         skill declares it - the "brake" in the on-demand model.
//
// targetIsSupervisor reports whether the target agent carries the super-privilege attribute (possibly different from the caller).
//
// The catalog viewpoint must follow the **target** identity: granting a super tool to an ordinary agent only creates a dead ref that cannot be
// executed (the execution layer's IsSupervisorOnlyTool refuses it), and showing it invites a grant that is bound to fail.
// An empty agentID falls back to the caller's identity; an unresolvable target counts as false (the stricter default).
func (m *toolGrantManager) targetIsSupervisor(agentID string) bool {
	if agentID == "" {
		return m.isSupervisor
	}
	inst, err := m.agentRepo().GetAgent(&auth.AuthInfo{Schema: m.schema, ID: m.userID}, agentID)
	if err != nil || inst == nil {
		return false
	}
	return inst.IsSupervisor
}

func (m *toolGrantManager) GrantAgentTool(_ context.Context, agentName, agentID, ref, kind string) (string, string, error) {
	// Identity backstop: the tool layer already passed the supervisor-only classification gate; checked once more here (same style as
	// BindAgentSkill)
	if !m.isSupervisor {
		return "", "", errors.New("only the super agent may modify an agent's tool grants")
	}
	ref = strings.TrimSpace(ref)
	// The agent side may reference tool groups (allowGroup=true), the same criterion as the admin SetAgentToolGrants
	if err := toolgrant.ValidateRef(ref, true); err != nil {
		return "", "", fmt.Errorf("invalid tool ref %q: %w", ref, err)
	}
	kind = strings.ToLower(strings.TrimSpace(kind))
	switch kind {
	case "", models.GrantKindRequired, models.GrantKindAllow, models.GrantKindDeny:
	default:
		return "", "", fmt.Errorf("invalid grant kind %q: expected required / allow / deny, or omit it and let the system decide", kind)
	}
	// The viewpoint follows the target identity: when the target is not a supervisor the whole builtin:super group produces nothing ->
	// "supervisor only" tools are refused, avoiding a dead ref that cannot be executed (the execution layer's IsSupervisorOnlyTool refuses a
	// non-supervisor execution).
	cat := toolgrant.NewCatalog(m.schema, m.repo, m.targetIsSupervisor(agentID))
	want, err := cat.ToolsOfRef(ref)
	if err != nil {
		return "", "", err
	}
	if len(want) == 0 {
		return "", "", fmt.Errorf("tool ref %q not found or expands to no tools", ref)
	}

	declaring, err := m.declaringSkill(cat, agentID, want)
	if err != nil {
		return "", "", err
	}

	// With an explicit kind, read the old row first: Upsert overwrites by ref, and the caller needs to know which row was replaced.
	oldKind := ""
	if kind != "" {
		if oldKind, err = m.currentKind(agentID, ref); err != nil {
			return "", "", err
		}
	}

	auto := kind == ""
	if auto {
		if declaring != "" {
			kind = models.GrantKindAllow
		} else {
			kind = models.GrantKindRequired
		}
	}
	// An explicit allow must really be able to light up: without a declaring skill it is a dead grant that never takes effect.
	if !auto && kind == models.GrantKindAllow && declaring == "" {
		return "", "", fmt.Errorf(
			"writing allow would have no effect: no skill injected into %q currently declares a tool dependency on %q; "+
				"use required (always injected) instead, or add that dependency to its skill first", agentName, ref)
	}

	if err := m.repo.UpsertAgentGrant(m.schema, agentID, m.userID, models.AgentToolGrant{
		AgentID:   agentID,
		GrantKind: kind,
		ItemType:  toolgrant.ItemTypeOfRef(ref),
		Ref:       ref,
	}); err != nil {
		return "", "", err
	}
	return kind, grantReason(kind, declaring, oldKind, auto), nil
}

// grantReason explains "what was actually written and why" - the model relays it verbatim.
//
// English, because this string is produced deep inside a tool call where nothing carries the caller's language (the run has a
// reply-language rule but no locale field). Same rule as skeleton.Default: with no language input, fall back to the product's
// primary language. The reply-language rule then has the model restate it in whatever language the user writes.
func grantReason(kind, declaring, oldKind string, auto bool) string {
	var b strings.Builder
	switch kind {
	case models.GrantKindAllow:
		if auto {
			fmt.Fprintf(&b, "Written as allow: injected skill %q declares this tool dependency, so it is injected on demand; "+
				"unbinding that skill stops it applying (re-run grant_agent_tool on this ref to upgrade it to required)", declaring)
		} else {
			fmt.Fprintf(&b, "Written as allow: skill %q declares this tool dependency; it is injected only after use_skill activates it", declaring)
		}
	case models.GrantKindRequired:
		if declaring != "" {
			fmt.Fprintf(&b, "Written as required: always injected (not downgraded to allow, although skill %q declares it)", declaring)
		} else {
			b.WriteString("Written as required: always injected")
		}
		if auto {
			b.WriteString(" (no injected skill declares this dependency, so allow would never take effect)")
		}
	case models.GrantKindDeny:
		b.WriteString("Written as deny: this ref is never injected, even if a skill declares it")
	}
	if oldKind != "" && oldKind != kind {
		fmt.Fprintf(&b, "; the existing row for this ref was %s and has been overwritten (one row per ref)", oldKind)
	}
	return b.String()
}

// declaringSkill checks whether any skill injected into the target declares a dependency on any tool in want, returning that skill's name
// (for the "what lights it up" explanation).
//
// The matching criterion is exactly that of Resolver.Resolve: both sides are **expanded into leaf tool refs** and intersected, rather than
// comparing ref strings - otherwise "the system thinks the allow takes effect while the intersection is empty" happens.
func (m *toolGrantManager) declaringSkill(cat *toolgrant.Catalog, agentID string, want []toolgrant.ToolInfo) (string, error) {
	wantSet := map[string]bool{}
	for _, t := range want {
		wantSet[t.Ref] = true
	}
	list, err := m.injectedSkills(agentID)
	if err != nil {
		return "", err
	}
	for _, s := range list {
		name, err := m.skillDeclaring(cat, s, wantSet)
		if err != nil {
			return "", err
		}
		if name != "" {
			return name, nil
		}
	}
	return "", nil
}

// currentKind returns the existing grant_kind of a ref (an empty string when missing). Only used to echo "which row was overwritten".
func (m *toolGrantManager) currentKind(agentID, ref string) (string, error) {
	grants, err := m.repo.ListAgentGrants(m.schema, agentID)
	if err != nil {
		return "", err
	}
	for _, g := range grants {
		if g.Ref == ref {
			return g.GrantKind, nil
		}
	}
	return "", nil
}

// skillDeclaring checks whether a skill's declared tool dependencies intersect wantSet (leaf tool refs); it returns the skill's name when they
// do (to tell "what lights it up") and "" otherwise.
func (m *toolGrantManager) skillDeclaring(cat *toolgrant.Catalog, s models.Skill, wantSet map[string]bool) (string, error) {
	deps, err := m.skillDeps(s)
	if err != nil {
		return "", err
	}
	for _, ref := range deps {
		sub, err := cat.ToolsOfRef(ref)
		if err != nil {
			return "", err
		}
		for _, t := range sub {
			if wantSet[t.Ref] {
				return s.Name, nil
			}
		}
	}
	return "", nil
}

// RevokeAgentTool removes one grant of an agent (by ref, idempotent) and returns the number of deleted rows plus the kind of the deleted row.
//
// The kind is echoed for auditing: one ref has one row, but that row may be required / allow / deny, and "what was revoked" differs a lot in
// meaning from "an allow was revoked".
func (m *toolGrantManager) RevokeAgentTool(_ context.Context, agentID, ref string) (int64, string, error) {
	if !m.isSupervisor {
		return 0, "", errors.New("only the super agent may modify an agent's tool grants")
	}
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return 0, "", errors.New("tool ref is empty")
	}
	// The ref's existence is not validated: a dangling grant whose source is gone (deleted connection / endpoint) must still be revocable
	oldKind, err := m.currentKind(agentID, ref)
	if err != nil {
		return 0, "", err
	}
	removed, err := m.repo.DeleteAgentGrant(m.schema, agentID, ref)
	if err != nil {
		return 0, "", err
	}
	return removed, oldKind, nil
}

// ListAgentGrants lists the target agent's grants and their current effective state (read_agent shows the current picture with it).
//
// Three verdicts, all anchored at "the start of the run":
//
//	required -> always effective;
//	allow    -> a bound/private skill declares it but it is **not activated yet** (pending): it lights up after the model calls use_skill;
//	allow    -> no skill declares it: never effective (spinning), with a remedy suggested.
//
// The binding relation only answers "after reading which skill will it light up", no longer "already effective" - the effective moment is
// use_skill, which this function cannot foresee, so everything is marked pending rather than effective.
func (m *toolGrantManager) ListAgentGrants(_ context.Context, agentName, agentID string) ([]tools.AgentGrantView, error) {
	grants, err := m.repo.ListAgentGrants(m.schema, agentID)
	if err != nil {
		return nil, err
	}
	cat := toolgrant.NewCatalog(m.schema, m.repo, m.targetIsSupervisor(agentID))
	list, err := m.injectedSkills(agentID)
	if err != nil {
		return nil, err
	}
	expand := func(ref string) []string {
		sub, eerr := cat.ToolsOfRef(ref)
		if eerr != nil {
			return nil
		}
		out := make([]string, 0, len(sub))
		for _, t := range sub {
			out = append(out, t.Ref)
		}
		return out
	}
	declaring := func(expanded []string) string {
		want := map[string]bool{}
		for _, r := range expanded {
			want[r] = true
		}
		for _, s := range list {
			if name, serr := m.skillDeclaring(cat, s, want); serr == nil && name != "" {
				return name
			}
		}
		return ""
	}
	return grantViews(grants, expand, declaring), nil
}

// grantViews is pure computation: grants -> read-only views (no repository access, so it is easy to unit test).
//
//	required -> always effective (independent of activation);
//	deny     -> explicit refusal: nothing is injected even when a skill declares it (no effect on required; one row per ref, the admin side
//	            forbids both at once);
//	allow + a skill declares it -> pending: not yet effective, it lights up after use_skill activates that skill (via_skill points at it);
//	allow + nobody declares it  -> spinning: never effective, a note and a remedy are given.
func grantViews(grants []models.AgentToolGrant,
	expand func(ref string) []string, declaring func(expanded []string) string) []tools.AgentGrantView {
	out := make([]tools.AgentGrantView, 0, len(grants))
	for _, g := range grants {
		v := tools.AgentGrantView{Kind: g.GrantKind, Ref: g.Ref, ItemType: g.ItemType}
		switch {
		case g.GrantKind == models.GrantKindRequired:
			v.Effective = true
		case g.GrantKind == models.GrantKindDeny:
			// deny only subtracts from the candidates: it must not be marked pending / spinning, or "explicitly forbidden" would read as
			// "waiting to take effect".
			v.Note = "Explicit deny: not injected even if a skill declares this dependency"
		case declaring != nil:
			if name := declaring(expand(g.Ref)); name != "" {
				v.Pending = true
				v.ViaSkill = name
				v.Note = "Declared by a bound or private skill, but not activated yet in this run: it applies after use_skill " + name
				break
			}
			v.Note = "allow has no effect: no bound or private skill declares this dependency; " +
				"re-run grant_agent_tool on this ref to upgrade it to required"
		default:
			v.Note = "allow has no effect: no bound or private skill declares this dependency"
		}
		out = append(out, v)
	}
	return out
}
