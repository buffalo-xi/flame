package repository

import (
	"flame/nexus/shared/auth"
	"flame/nexus/shared/models"
)

// ListSvcEndpointsRequest holds the service endpoint list request parameters.
type ListSvcEndpointsRequest struct {
	Page       int      `json:"page" form:"page"`
	Limit      int      `json:"limit" form:"limit"`
	Name       string   `json:"name" form:"name"`
	TargetType string   `json:"target_type" form:"target_type"`
	TargetID   string   `json:"target_id" form:"target_id"`
	Protocol   string   `json:"protocol" form:"protocol"`
	Exposed    *bool    `json:"exposed" form:"exposed"`
	SortBy     []string `json:"sort_by" form:"sort_by"`
	SortOrder  []string `json:"sort_order" form:"sort_order"`
}

// SvcEndpointRepository is the service endpoint repository interface.
type SvcEndpointRepository interface {
	// GetByID returns one service endpoint by id.
	GetByID(authInfo *auth.AuthInfo, id string) (*models.SvcEndpoint, error)

	// FindByName looks an endpoint up exactly by protocol + name (used to locate one by name from outside, case
	// insensitive); missing returns (nil, nil).
	FindByName(authInfo *auth.AuthInfo, protocol, name string) (*models.SvcEndpoint, error)

	// Create creates a service endpoint.
	Create(authInfo *auth.AuthInfo, svcEndpoint *models.SvcEndpoint) (string, error)

	// Update updates a service endpoint.
	Update(authInfo *auth.AuthInfo, id string, svcEndpoint *models.SvcEndpoint) error

	// UpdateExposed updates the endpoint's exposed state (true=exposed to the outside, false=internal only).
	UpdateExposed(authInfo *auth.AuthInfo, id string, exposed bool) error

	// Delete deletes one service endpoint by id.
	Delete(authInfo *auth.AuthInfo, id string) error

	// DeleteByTarget deletes every protocol endpoint of a target and clears the endpoint marker on the target.
	DeleteByTarget(authInfo *auth.AuthInfo, targetType string, targetID string) error

	// RetractByTarget retracts every protocol endpoint of a target (only exposed=false; the endpoint records and their
	// configuration are kept).
	RetractByTarget(authInfo *auth.AuthInfo, targetType string, targetID string) error

	// List lists service endpoints.
	List(authInfo *auth.AuthInfo, req *ListSvcEndpointsRequest) ([]models.SvcEndpoint, int, error)

	// ListAll takes every endpoint matching the filters (no paging, no total count).
	// Scenarios that "load the whole catalogue into memory" (tool registration for MCP / RPC) must use it: List has a
	// default limit=10 and silently truncates.
	ListAll(authInfo *auth.AuthInfo, req *ListSvcEndpointsRequest) ([]models.SvcEndpoint, error)
}
