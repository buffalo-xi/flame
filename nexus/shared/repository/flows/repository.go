package repository

import (
	"flame/nexus/shared/auth"
	"flame/nexus/shared/models"
	"flame/tools/xsql"
)

// ListFlowsRequest holds the workflow instance list request parameters.
type ListFlowsRequest struct {
	Page      int      `json:"page"`
	Limit     int      `json:"limit"`
	Name      string   `json:"name"`
	SortBy    []string `json:"sort_by"`
	SortOrder []string `json:"sort_order"`
}

// Repository is the workflow instance storage interface.
type Repository interface {
	// ListFlows lists every workflow instance.
	ListFlows(authInfo *auth.AuthInfo, req *ListFlowsRequest) ([]models.Flow, int, error)
	// GetFlow returns one workflow instance by id.
	GetFlow(authInfo *auth.AuthInfo, id string) (*models.Flow, error)
	// ExistsFlowName reports whether a workflow of the same name already exists in the tenant (used by the agent name
	// check).
	ExistsFlowName(authInfo *auth.AuthInfo, name string) (bool, error)
	// FindByName returns one row by name (for the duplicate-name check and for "install over the same name"). Missing
	// returns (nil, nil).
	FindByName(authInfo *auth.AuthInfo, name string) (*models.Flow, error)
	// CreateFlow creates a workflow instance.
	CreateFlow(authInfo *auth.AuthInfo, Instance *models.Flow) (string, error)
	// UpdateFlow updates a workflow instance.
	UpdateFlow(authInfo *auth.AuthInfo, id string, Instance *models.Flow) error
	// UpdateFlowDiagram replaces only diagram (used by the edge tool): name/description/limits/breakpoints are untouched.
	UpdateFlowDiagram(authInfo *auth.AuthInfo, id string, diagram xsql.JSONB) error
	// UpdateFlowArguments replaces only arguments (the entry argument declaration): name/description/diagram/limits/
	// breakpoints are untouched, and svc_endpoints.input_schema is synced in the same transaction.
	UpdateFlowArguments(authInfo *auth.AuthInfo, id string, arguments xsql.JSONB) error
	// DeleteFlow deletes a workflow instance.
	DeleteFlow(authInfo *auth.AuthInfo, id string) error
	// ListBreakpoints lists a workflow's breakpoint node_ids.
	ListBreakpoints(authInfo *auth.AuthInfo, flowId string) ([]string, error)
	// SetBreakpoint sets or clears one breakpoint (persisted immediately while running, so a refresh does not lose it).
	SetBreakpoint(authInfo *auth.AuthInfo, flowId string, nodeID string, on bool) error
	// UpdateStatus updates status only (1=active enabled | 2=disabled).
	UpdateStatus(authInfo *auth.AuthInfo, id string, status int) error

	// ListFlowVersions is the version history (newest first, without diagram).
	ListFlowVersions(authInfo *auth.AuthInfo, flowID string) ([]FlowVersion, error)
	// ReleaseFlowVersion freezes the current working copy into a new version and makes it current.
	ReleaseFlowVersion(authInfo *auth.AuthInfo, flowID string, note string) (*FlowVersion, error)
	// RollbackFlowVersion switches "the version in use" to a historical one (a pure pointer change: no new version is
	// released and the draft is untouched).
	RollbackFlowVersion(authInfo *auth.AuthInfo, flowID string, version int) (*FlowVersion, error)
	// ApplyFlowVersionToDraft loads a historical version's content into the working copy (no release; production is
	// unchanged).
	ApplyFlowVersionToDraft(authInfo *auth.AuthInfo, flowID string, version int) error
	// DeleteFlowVersion deletes a historical version (the current one cannot be deleted; production and the draft are both
	// untouched).
	DeleteFlowVersion(authInfo *auth.AuthInfo, flowID string, version int) error
	// GetCurrentFlowVersion returns the definition of the released version: the production entry points (MCP / RPC / an
	// endpoint / a direct flow call) come through here; with no released version it returns ErrNoReleasedVersion and the
	// caller should refuse to execute rather than fall back to the draft.
	GetCurrentFlowVersion(authInfo *auth.AuthInfo, id string) (*models.Flow, error)
}
