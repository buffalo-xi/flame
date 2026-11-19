package agent

import (
	"context"
	"fmt"
	"strings"

	"flame/nexus/service/agent/tools"
	"flame/nexus/shared/auth"
	"flame/nexus/shared/models"
	"flame/nexus/shared/paths"
	"flame/nexus/shared/quota"
	agents "flame/nexus/shared/repository/agents"
	flows "flame/nexus/shared/repository/flows"
	"flame/nexus/shared/storage"
	"flame/tools/xlog"
	"flame/tools/xsql"
)

// agentManager implements tools.AgentManager, wrapping the agents repository.
type agentManager struct {
	schema       string
	agentID      string // current agent uuid (written as the grant creator when a new agent is created)
	agentName    string // current agent name
	isSupervisor bool   // identity criterion (agents.is_supervisor): only the supervisor may perform management actions
	userID       string // current user uuid (written to created_by)
	repo         agents.Repository
	flowRepo     flows.Repository // an agent name must not collide with a workflow
	store        storage.Storage  // definition-domain rename / delete go through storage (local / nfs / s3 alike)
}

// NewAgentManager creates tools.AgentManager (tenant schema and current agent identity fixed).
func NewAgentManager(schema, agentID, agentName, userID string, store storage.Storage, isSupervisor bool) tools.AgentManager {
	return &agentManager{schema: schema, agentID: agentID, agentName: agentName, isSupervisor: isSupervisor, userID: userID,
		repo: agents.NewRepository(), flowRepo: flows.NewPostgresRepository(), store: store}
}

func (m *agentManager) auth() *auth.AuthInfo {
	// Carry userID: the repository uses authInfo.ID to write created_by/updated_by (an empty string becomes NULL)
	return &auth.AuthInfo{Schema: m.schema, ID: m.userID}
}

// Create creates an agent instance (writes the database). The file (AGENT.md) is written by the meta tools through storage.
// The config column is not written here: the repository stores the default {}, leaving room for future expansion.
//
// isSupervisor=true means "create it directly as a supervisor": the repository materialises the core tool group, the two switches and the
// four authoring skills from the starter pack (the same result as SetSupervisor(true) after creation).
// A non-supervisor cannot create a supervisor - the execution layer already refuses by category, and this is defence in depth on top.
func (m *agentManager) Create(ctx context.Context, name, description string, isSupervisor bool) (string, error) {
	// Name rule backstop: letters, digits and hyphens only, 64 characters at most
	if !tools.ValidName(name) {
		return "", fmt.Errorf("invalid agent name %q (letters, digits and hyphens only, 64 characters at most)", name)
	}
	if isSupervisor && !m.isSupervisor {
		return "", fmt.Errorf("only a supervisor may create a supervisor")
	}
	// Check for a name collision first: it must not clash with another agent or a workflow (before any database write)
	if err := m.ensureNameFree(ctx, name, ""); err != nil {
		return "", err
	}
	// Quota check: the per-tenant agent cap
	cnt, err := quota.CountAgents(m.schema)
	if err != nil {
		return "", err
	}
	if err := models.CheckQuota("agent", cnt, quota.Resolve(m.schema).MaxAgents); err != nil {
		return "", err
	}

	inst := &models.Agent{
		Name:         name,
		Description:  xsql.String(description),
		Status:       xsql.Int(models.StatusNormal),
		IsSupervisor: isSupervisor,
	}
	// Default grant: grant the new agent to its creator (whoever built it can call it)
	return m.repo.CreateAgent(m.auth(), inst, m.agentID)
}

// List lists brief information for every agent instance.
func (m *agentManager) List(_ context.Context) ([]tools.AgentInfo, error) {
	// Limit: -1 = take everything (no longer bound by the implicit 1000-row cap)
	instances, _, err := m.repo.ListAgents(m.auth(), &agents.ListAgentsRequest{Page: 1, Limit: -1})
	if err != nil {
		return nil, err
	}
	out := make([]tools.AgentInfo, 0, len(instances))
	for _, inst := range instances {
		out = append(out, tools.AgentInfo{
			ID:                inst.ID,
			Name:              inst.Name,
			Description:       string(inst.Description),
			Status:            int(inst.Status),
			IsSupervisor:      inst.IsSupervisor,
			SkillDiscovery:    inst.SkillDiscovery,
			ToolsUnrestricted: inst.ToolsUnrestricted,
		})
	}
	return out, nil
}

// Exists reports whether name already exists (exact match).
func (m *agentManager) Exists(ctx context.Context, name string) (bool, error) {
	inst, err := m.getByName(ctx, name)
	if err != nil {
		return false, err
	}
	return inst != nil, nil
}

// Get takes an agent's brief information (with id/status) by exact name; returns (nil, nil) when missing.
func (m *agentManager) Get(ctx context.Context, name string) (*tools.AgentInfo, error) {
	inst, err := m.getByName(ctx, name)
	if err != nil {
		return nil, err
	}
	if inst == nil {
		return nil, nil
	}
	return &tools.AgentInfo{
		ID:                inst.ID,
		Name:              inst.Name,
		Description:       string(inst.Description),
		Status:            int(inst.Status),
		IsSupervisor:      inst.IsSupervisor,
		SkillDiscovery:    inst.SkillDiscovery,
		ToolsUnrestricted: inst.ToolsUnrestricted,
	}, nil
}

// ensureNameFree checks that name is not taken by another agent / workflow (a non-empty excludeAgentID ignores the agent itself).
// The caller must invoke it before any database write / directory move, so a failed check leaves both the database and the workspace
// untouched.
func (m *agentManager) ensureNameFree(ctx context.Context, name, excludeAgentID string) error {
	inst, err := m.getByName(ctx, name)
	if err != nil {
		return err
	}
	if inst != nil && inst.ID != excludeAgentID {
		return fmt.Errorf("agent %q already exists", name)
	}
	exist, err := m.flowRepo.ExistsFlowName(m.auth(), name)
	if err != nil {
		return err
	}
	if exist {
		return fmt.Errorf("workflow %q already exists; agent name must be unique against workflows", name)
	}
	return nil
}

// getByName takes an instance by exact name (returns nil when missing).
// Note that ListAgents' Name is an ILIKE fuzzy match, so an exact case-insensitive comparison is needed.
//
// Limit must be -1 (all): the repository returns rows sorted by name ascending, and with a LIMIT the exact match may be pushed out of the
// window - with writer / writer-1 ... writer-9 in the tenant, looking up "writer-5" only gets the first N rows and an existing agent is
// judged missing (the same approach as flowsManager.find).
func (m *agentManager) getByName(_ context.Context, name string) (*models.Agent, error) {
	instances, _, err := m.repo.ListAgents(m.auth(), &agents.ListAgentsRequest{Name: name, Page: 1, Limit: -1})
	if err != nil {
		return nil, err
	}
	for i := range instances {
		if strings.EqualFold(instances[i].Name, name) {
			return &instances[i], nil
		}
	}
	return nil, nil
}

// Update updates the description or renames an agent by name (a rename also moves the definition-domain directory) and returns the final
// name.
func (m *agentManager) Update(ctx context.Context, name, newName, description string) (string, error) {
	inst, err := m.getByName(ctx, name)
	if err != nil {
		return "", err
	}
	if inst == nil {
		return "", fmt.Errorf("agent %q not found", name)
	}
	// Status gate + protection gate (the source of truth is the ensureAgent* functions in this package)
	if err := ensureAgentMutable(inst); err != nil {
		return "", err
	}
	if err := ensureAgentRenamable(inst, newName); err != nil {
		return "", err
	}

	finalName := inst.Name
	if newName != "" && newName != inst.Name {
		if !tools.ValidName(newName) {
			return "", fmt.Errorf("invalid agent name %q (letters, digits and hyphens only)", newName)
		}
		// Check the name collision first (other agents + workflows), then write the database / move the workspace
		if err := m.ensureNameFree(ctx, newName, inst.ID); err != nil {
			return "", err
		}
		inst.Name = newName
		finalName = newName
	}
	if description != "" {
		inst.Description = xsql.String(description)
	}
	// Rename -> move "draft directory + version domain", **disk first, then database** (the disk holds content, the database is the index).
	// The version directory is keyed by name: writing the database first would, if the disk move failed, make a production call assemble an
	// empty version directory under the new name.
	renamed := finalName != name
	if renamed {
		if rerr := paths.MoveEntityStorage(m.store, m.schema, paths.VersionAgents, name, finalName); rerr != nil {
			return "", fmt.Errorf("agent dir rename failed: %w", rerr)
		}
	}
	if err := m.repo.UpdateAgent(m.auth(), inst.ID, inst); err != nil {
		if renamed {
			// The database write failed -> move the disk back, so there is no half state of "new name on disk, old name in the database"
			if rerr := paths.MoveEntityStorage(m.store, m.schema, paths.VersionAgents, finalName, name); rerr != nil {
				xlog.Error("failed to roll back the definition-domain / version directory schema=%s %s->%s: %v", m.schema, finalName, name, rerr)
			}
		}
		return "", err
	}
	return finalName, nil
}

// SetUnrestricted sets the "unrestricted tools" switch (agents.tools_unrestricted) of the target agent (by uuid).
//
// Semantics (see toolgrant/resolve.go): when the candidates (tool dependencies declared by injected skills) are non-empty they are no longer
// narrowed by the allow list - it relaxes what the allow filter does, not "blindly enable every tool".
// D5 (revised): the super agent may set it for itself - it is the administrator; the boundary comes from status: a deprecated agent (the super
// agent included) accepts no write, and only the admin side can change status, so it cannot unlock itself.
func (m *agentManager) SetUnrestricted(_ context.Context, agentID string, v bool) error {
	if !m.isSupervisor {
		return fmt.Errorf("only the super agent may modify an agent's tool grants")
	}
	if agentID == "" {
		return fmt.Errorf("target agent id is empty")
	}
	return m.repo.SetUnrestricted(m.auth(), agentID, v)
}

// SetSupervisor sets the super-privilege attribute (agents.is_supervisor) of the target agent (by uuid).
//
// Only the supervisor may change it: a non-supervisor cannot even reach the management tools, so this is defence in depth.
// The target may be itself - demoting itself is stopped by the repository's "last supervisor" rule, or the tenant would lose its management
// surface for good.
// The target must be in the normal state (consistent with D5: a deprecated agent accepts no attribute change).
func (m *agentManager) SetSupervisor(_ context.Context, agentID string, v bool) error {
	if !m.isSupervisor {
		return fmt.Errorf("only the supervisor may change an agent's super-privilege attribute")
	}
	if agentID == "" {
		return fmt.Errorf("target agent id is empty")
	}
	inst, err := m.repo.GetAgent(m.auth(), agentID)
	if err != nil {
		return err
	}
	if inst == nil {
		return fmt.Errorf("agent %q not found", agentID)
	}
	if err := ensureAgentMutable(inst); err != nil {
		return err
	}
	return m.repo.SetSupervisor(m.auth(), agentID, v)
}

// Delete deletes an agent by name (and cleans up its definition-domain directory).
func (m *agentManager) Delete(ctx context.Context, name string) error {
	inst, err := m.getByName(ctx, name)
	if err != nil {
		return err
	}
	if inst == nil {
		return fmt.Errorf("agent %q not found", name)
	}
	// Protection gate + status gate
	if err := ensureAgentDeletable(inst); err != nil {
		return err
	}
	// Data removal: private skill rows, grant relations and the agents row are handled by the repository in one transaction
	// (skills.agent_id has no foreign key, so the cascade can only be guaranteed by the application).
	if err := m.repo.DeleteAgent(m.auth(), inst.ID); err != nil {
		return err
	}
	// Clean the definition-domain directory and the version domain together (a database cascade cannot delete the disk).
	// A failure is only logged: the database is the index and leftovers on disk are swept at startup - a delete that already took effect must
	// not be reported as failed (the same criterion as the admin/agents delete).
	if err := paths.DeleteEntityStorage(m.store, m.schema, paths.VersionAgents, name); err != nil {
		xlog.Error("failed to clean the definition-domain / version directory schema=%s name=%s: %v", m.schema, name, err)
	}
	return nil
}

// ensureAgentMutable is the status gate: only the normal state may change a definition (single source of truth: models.IsModifiable).
func ensureAgentMutable(inst *models.Agent) error {
	if !inst.IsModifiable() {
		return fmt.Errorf("agent %q is deprecated; switch it back to normal first", inst.Name)
	}
	return nil
}

// ensureAgentRenamable is the protection gate: a protected agent may not be renamed.
func ensureAgentRenamable(inst *models.Agent, newName string) error {
	if models.IsProtectedAgent(inst.Name) && newName != "" && newName != inst.Name {
		return fmt.Errorf("protected agent %q cannot be renamed", inst.Name)
	}
	return nil
}

// ensureAgentDeletable is the protection gate plus the status gate.
func ensureAgentDeletable(inst *models.Agent) error {
	if models.IsProtectedAgent(inst.Name) {
		return fmt.Errorf("protected agent %q cannot be deleted", inst.Name)
	}
	return ensureAgentMutable(inst)
}
