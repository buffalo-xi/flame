package repository

import (
	"flame/nexus/shared/auth"
	"flame/nexus/shared/models"
)

// ListLLMProvidersRequest LLM服务商列表请求参数
type ListLLMProvidersRequest struct {
	Page      int      `json:"page" form:"page"`
	Limit     int      `json:"limit" form:"limit"`
	Name      string   `json:"name" form:"name"`
	Protocol  string   `json:"protocol" form:"protocol"`
	SortBy    []string `json:"sort_by" form:"sort_by"`
	SortOrder []string `json:"sort_order" form:"sort_order"`
}

// LLMProviderRepository LLM服务商仓库接口
type LLMProviderRepository interface {
	// GetByID 根据ID获取LLM服务商
	GetByID(authInfo *auth.AuthInfo, id string) (*models.LLMProvider, error)

	// Create 创建LLM服务商
	Create(authInfo *auth.AuthInfo, llmProvider *models.LLMProvider) (string, error)

	// Update 更新LLM服务商
	Update(authInfo *auth.AuthInfo, id string, llmProvider *models.LLMProvider) error

	// Delete 根据ID删除LLM服务商
	Delete(authInfo *auth.AuthInfo, id string) error

	// List 获取LLM服务商列表
	List(authInfo *auth.AuthInfo, req *ListLLMProvidersRequest) ([]models.LLMProvider, int, error)
}
