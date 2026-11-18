package repository

import (
	"database/sql"

	"flame/nexus/shared/auth"
	"flame/nexus/shared/models"
)

// ListLLMModelsRequest LLM模型列表请求参数
type ListLLMModelsRequest struct {
	Page       int      `json:"page" form:"page"`
	Limit      int      `json:"limit" form:"limit"`
	Name       string   `json:"name" form:"name"`
	ProviderID string   `json:"provider_id" form:"provider_id"`
	SortBy     []string `json:"sort_by" form:"sort_by"`
	SortOrder  []string `json:"sort_order" form:"sort_order"`
}

// LLMModelRepository LLM模型仓库接口
type LLMModelRepository interface {
	// GetByID 根据ID获取LLM模型
	GetByID(authInfo *auth.AuthInfo, id string) (*models.LLMModel, error)

	// Create 创建LLM模型
	Create(authInfo *auth.AuthInfo, llmModel *models.LLMModel) (string, error)

	// Update 更新LLM模型
	Update(authInfo *auth.AuthInfo, id string, llmModel *models.LLMModel) error

	// Delete 根据ID删除LLM模型
	Delete(authInfo *auth.AuthInfo, id string) error

	// List 获取LLM模型列表
	List(authInfo *auth.AuthInfo, req *ListLLMModelsRequest) ([]models.LLMModel, int, error)

	// ListByProviderID 根据ProviderID获取LLM模型列表
	ListByProviderID(authInfo *auth.AuthInfo, providerID string, req *ListLLMModelsRequest) ([]models.LLMModel, int, error)

	// GetDefault 获取默认模型
	GetDefault(authInfo *auth.AuthInfo) (*models.LLMModel, error)

	// UpdateGatewayID 更新模型的 gateway_id
	UpdateGatewayID(authInfo *auth.AuthInfo, modelID, gatewayID string) error

	// LockAndGetGatewayID 事务内锁行并返回当前 gateway_id（供 Provision 防并发）
	LockAndGetGatewayID(tx *sql.Tx, authInfo *auth.AuthInfo, modelID string) (string, error)

	// SetGatewayIDInTx 事务内设置 gateway_id
	SetGatewayIDInTx(tx *sql.Tx, authInfo *auth.AuthInfo, modelID, gatewayID string) error

	// ClearGatewayIDInTx 锁行 + 条件清空：仅当当前 gateway_id == expectGatewayID 才置 NULL
	ClearGatewayIDInTx(tx *sql.Tx, authInfo *auth.AuthInfo, modelID, expectGatewayID string) (int64, error)

	// ClearAllGatewayIDs 批量将该租户下所有 gateway_id 非空的模型置空
	ClearAllGatewayIDs(authInfo *auth.AuthInfo) error

	// ListSelectable 返回可选择的模型：provider.enabled=true 且 gateway_id 非空，DISTINCT 去重
	ListSelectable(authInfo *auth.AuthInfo) ([]models.LLMModel, error)

	// ListAll 返回该租户下全部 LLM 模型（不分页，供网关同步等批处理使用）
	ListAll(authInfo *auth.AuthInfo) ([]models.LLMModel, error)

	// DeleteCascade 删除模型，并把引用它的 agent 的 llm_model_id 置空（同一事务）
	DeleteCascade(authInfo *auth.AuthInfo, id string) error

	// CountEnabledByGatewayModel 统计已启用（gateway_id 非空）且 gateway_model 同名的模型数量。
	// 已启用模型的 gateway_model 决定 LiteLLM 的 model_name（"<schema>:<gateway_model>"），
	// 重名会让网关侧路由歧义，因此启用前用它做唯一性校验。
	CountEnabledByGatewayModel(authInfo *auth.AuthInfo, gatewayModel string) (int, error)
}
