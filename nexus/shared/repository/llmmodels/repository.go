package repository

import (
	"database/sql"

	"flame/nexus/shared/auth"
	"flame/nexus/shared/models"
)

// ListLLMModelsRequest holds the LLM model list parameters.
type ListLLMModelsRequest struct {
	Page       int      `json:"page" form:"page"`
	Limit      int      `json:"limit" form:"limit"`
	Name       string   `json:"name" form:"name"`
	ProviderID string   `json:"provider_id" form:"provider_id"`
	SortBy     []string `json:"sort_by" form:"sort_by"`
	SortOrder  []string `json:"sort_order" form:"sort_order"`
}

// LLMModelRepository is the LLM model repository interface.
type LLMModelRepository interface {
	// GetByID returns one LLM model by id.
	GetByID(authInfo *auth.AuthInfo, id string) (*models.LLMModel, error)

	// Create creates an LLM model.
	Create(authInfo *auth.AuthInfo, llmModel *models.LLMModel) (string, error)

	// Update updates an LLM model.
	Update(authInfo *auth.AuthInfo, id string, llmModel *models.LLMModel) error

	// Delete deletes one LLM model by id.
	Delete(authInfo *auth.AuthInfo, id string) error

	// List lists LLM models.
	List(authInfo *auth.AuthInfo, req *ListLLMModelsRequest) ([]models.LLMModel, int, error)

	// ListByProviderID lists the LLM models of one provider.
	ListByProviderID(authInfo *auth.AuthInfo, providerID string, req *ListLLMModelsRequest) ([]models.LLMModel, int, error)

	// GetDefault returns the default model.
	GetDefault(authInfo *auth.AuthInfo) (*models.LLMModel, error)

	// UpdateGatewayID updates a model's gateway_id.
	UpdateGatewayID(authInfo *auth.AuthInfo, modelID, gatewayID string) error

	// LockAndGetGatewayID locks the row inside the transaction and returns the current gateway_id (used by Provision
	// against concurrency).
	LockAndGetGatewayID(tx *sql.Tx, authInfo *auth.AuthInfo, modelID string) (string, error)

	// SetGatewayIDInTx sets gateway_id inside the transaction.
	SetGatewayIDInTx(tx *sql.Tx, authInfo *auth.AuthInfo, modelID, gatewayID string) error

	// ClearGatewayIDInTx locks the row and clears it conditionally: it sets NULL only when the current gateway_id equals
	// expectGatewayID.
	ClearGatewayIDInTx(tx *sql.Tx, authInfo *auth.AuthInfo, modelID, expectGatewayID string) (int64, error)

	// ClearAllGatewayIDs clears gateway_id on every model of the tenant that has one.
	ClearAllGatewayIDs(authInfo *auth.AuthInfo) error

	// ListSelectable returns the selectable models: provider.enabled=true and a non-empty gateway_id, deduplicated with
	// DISTINCT.
	ListSelectable(authInfo *auth.AuthInfo) ([]models.LLMModel, error)

	// ListAll returns every LLM model of the tenant (no paging, for batch work such as a gateway sync).
	ListAll(authInfo *auth.AuthInfo) ([]models.LLMModel, error)

	// DeleteCascade deletes a model row. Nothing references a model by id, so no reference needs clearing.
	DeleteCascade(authInfo *auth.AuthInfo, id string) error

	// CountEnabledByGatewayModel counts the enabled models (non-empty gateway_id) sharing a gateway_model.
	// An enabled model's gateway_model determines the LiteLLM model_name ("<schema>:<gateway_model>"), so a duplicate
	// would make the gateway routing ambiguous; this is the uniqueness check to run before enabling.
	CountEnabledByGatewayModel(authInfo *auth.AuthInfo, gatewayModel string) (int, error)
}
