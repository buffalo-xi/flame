package repository

import (
	"flame/nexus/shared/auth"
	"flame/nexus/shared/models"
)

// ListLLMProvidersRequest holds the LLM provider list parameters.
type ListLLMProvidersRequest struct {
	Page      int      `json:"page" form:"page"`
	Limit     int      `json:"limit" form:"limit"`
	Name      string   `json:"name" form:"name"`
	Protocol  string   `json:"protocol" form:"protocol"`
	SortBy    []string `json:"sort_by" form:"sort_by"`
	SortOrder []string `json:"sort_order" form:"sort_order"`
}

// LLMProviderRepository is the LLM provider repository interface.
type LLMProviderRepository interface {
	// GetByID returns one LLM provider by id.
	GetByID(authInfo *auth.AuthInfo, id string) (*models.LLMProvider, error)

	// Create creates an LLM provider.
	Create(authInfo *auth.AuthInfo, llmProvider *models.LLMProvider) (string, error)

	// Update updates an LLM provider.
	Update(authInfo *auth.AuthInfo, id string, llmProvider *models.LLMProvider) error

	// Delete deletes one LLM provider by id.
	Delete(authInfo *auth.AuthInfo, id string) error

	// List lists LLM providers.
	List(authInfo *auth.AuthInfo, req *ListLLMProvidersRequest) ([]models.LLMProvider, int, error)
}
