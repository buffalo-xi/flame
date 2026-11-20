package repository

import (
	"database/sql"
	"fmt"
	"strconv"
	"strings"

	"flame/nexus/shared/auth"
	"flame/nexus/shared/db"
	"flame/nexus/shared/models"

	"github.com/google/uuid"
)

// PostgresRepository is the PostgreSQL implementation of LLM model storage.
type PostgresRepository struct{}

// NewPostgresRepository creates a PostgreSQL storage instance.
func NewPostgresRepository() *PostgresRepository {
	return &PostgresRepository{}
}

// modelColumns are the columns read for a model.
const modelColumns = `id, name, context_window, base_params, provider_id, gateway_id, gateway_model, is_default, extra_params, capabilities, created_at, updated_at`

// allowedSortColumns is the ORDER BY column whitelist.
// sort_by comes from the request body, and concatenating it directly would open an injection point (Postgres allows
// subqueries in ORDER BY), so only whitelisted column names are accepted and the rest are dropped.
var allowedSortColumns = map[string]bool{
	"id": true, "name": true, "context_window": true, "base_params": true,
	"provider_id": true, "gateway_id": true, "gateway_model": true,
	"is_default": true, "extra_params": true, "capabilities": true,
	"created_at": true, "updated_at": true,
}

// scanModel scans one row into an LLMModel.
func scanModel(scanner interface{ Scan(...interface{}) error }, m *models.LLMModel) error {
	return scanner.Scan(
		&m.ID, &m.Name, &m.ContextWindow, &m.BaseParams,
		&m.ProviderID, &m.GatewayID, &m.GatewayModel, &m.IsDefault, &m.ExtraParams, &m.Capabilities, &m.CreatedAt, &m.UpdatedAt,
	)
}

// GetByID returns one LLM model by id.
func (r *PostgresRepository) GetByID(authInfo *auth.AuthInfo, id string) (*models.LLMModel, error) {
	var llmModel models.LLMModel
	err := db.DB.QueryRow(fmt.Sprintf(`SELECT %s FROM %s.llm_models WHERE id = $1`, modelColumns, authInfo.Schema), id).Scan(
		&llmModel.ID, &llmModel.Name, &llmModel.ContextWindow, &llmModel.BaseParams,
		&llmModel.ProviderID, &llmModel.GatewayID, &llmModel.GatewayModel, &llmModel.IsDefault, &llmModel.ExtraParams, &llmModel.Capabilities, &llmModel.CreatedAt, &llmModel.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}

	return &llmModel, nil
}

// GetByGatewayModel returns the first enabled model by gateway_model (the model name on the user side).
func (r *PostgresRepository) GetByGatewayModel(authInfo *auth.AuthInfo, gatewayModel string) (*models.LLMModel, error) {
	var llmModel models.LLMModel
	err := db.DB.QueryRow(fmt.Sprintf(`SELECT %s FROM %s.llm_models WHERE gateway_model = $1 ORDER BY is_default DESC, created_at ASC LIMIT 1`, modelColumns, authInfo.Schema), gatewayModel).Scan(
		&llmModel.ID, &llmModel.Name, &llmModel.ContextWindow, &llmModel.BaseParams,
		&llmModel.ProviderID, &llmModel.GatewayID, &llmModel.GatewayModel, &llmModel.IsDefault, &llmModel.ExtraParams, &llmModel.Capabilities, &llmModel.CreatedAt, &llmModel.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &llmModel, nil
}

// UpdateGatewayID updates a model's gateway_id.
func (r *PostgresRepository) UpdateGatewayID(authInfo *auth.AuthInfo, modelID, gatewayID string) error {
	_, err := db.DB.Exec(
		fmt.Sprintf(`UPDATE %s.llm_models SET gateway_id = $1, updated_at = now() WHERE id = $2`, authInfo.Schema),
		gatewayID, modelID,
	)
	return err
}

// LockAndGetGatewayID locks the row (FOR UPDATE) inside the transaction and returns the current gateway_id (used by
// Provision against concurrency).
func (r *PostgresRepository) LockAndGetGatewayID(tx *sql.Tx, authInfo *auth.AuthInfo, modelID string) (string, error) {
	var gid sql.NullString
	err := tx.QueryRow(fmt.Sprintf(`SELECT gateway_id FROM %s.llm_models WHERE id = $1 FOR UPDATE`, authInfo.Schema), modelID).Scan(&gid)
	if err != nil {
		return "", err
	}
	if !gid.Valid {
		return "", nil
	}
	return gid.String, nil
}

// SetGatewayIDInTx sets gateway_id inside the transaction (called by Provision once it succeeded with no conflict under
// the lock).
func (r *PostgresRepository) SetGatewayIDInTx(tx *sql.Tx, authInfo *auth.AuthInfo, modelID, gatewayID string) error {
	_, err := tx.Exec(fmt.Sprintf(`UPDATE %s.llm_models SET gateway_id = $1, updated_at = now() WHERE id = $2`, authInfo.Schema), gatewayID, modelID)
	return err
}

// ClearGatewayIDInTx locks the row and clears it conditionally: it sets NULL only when the current gateway_id equals
// expectGatewayID, returning the number of rows affected.
func (r *PostgresRepository) ClearGatewayIDInTx(tx *sql.Tx, authInfo *auth.AuthInfo, modelID, expectGatewayID string) (int64, error) {
	res, err := tx.Exec(fmt.Sprintf(
		`UPDATE %s.llm_models SET gateway_id = NULL, updated_at = now() WHERE id = $1 AND gateway_id = $2`,
		authInfo.Schema), modelID, expectGatewayID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ClearAllGatewayIDs clears gateway_id on every model of the tenant that has one.
func (r *PostgresRepository) ClearAllGatewayIDs(authInfo *auth.AuthInfo) error {
	_, err := db.DB.Exec(fmt.Sprintf(`UPDATE %s.llm_models SET gateway_id = NULL, updated_at = now() WHERE gateway_id IS NOT NULL AND gateway_id <> ''`, authInfo.Schema))
	return err
}

// Create creates an LLM model.
func (r *PostgresRepository) Create(authInfo *auth.AuthInfo, llmModel *models.LLMModel) (string, error) {
	// Open the transaction
	tx, err := db.DB.Begin()
	if err != nil {
		return "", err
	}
	defer func() {
		if err != nil {
			tx.Rollback()
			return
		}
		err = tx.Commit()
	}()

	// If this model is the default, clear the flag on every other model first
	if bool(llmModel.IsDefault) {
		_, err = tx.Exec(fmt.Sprintf(`UPDATE %s.llm_models SET is_default = false`, authInfo.Schema))
		if err != nil {
			return "", err
		}
	}

	// Create the new model (the id is generated in the program, no longer relying on a database DEFAULT)
	id := llmModel.ID
	if id == "" {
		id = uuid.NewString()
	}
	if _, err = tx.Exec(fmt.Sprintf(`INSERT INTO %s.llm_models
		(id, name, context_window, base_params, provider_id, gateway_model, is_default, extra_params, capabilities)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`, authInfo.Schema),
		id, llmModel.Name, llmModel.ContextWindow, llmModel.BaseParams,
		llmModel.ProviderID, llmModel.GatewayModel, llmModel.IsDefault, llmModel.ExtraParams, llmModel.Capabilities); err != nil {
		return "", err
	}

	return id, nil
}

// Update updates an LLM model.
func (r *PostgresRepository) Update(authInfo *auth.AuthInfo, id string, llmModel *models.LLMModel) error {
	// Open the transaction
	tx, err := db.DB.Begin()
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			tx.Rollback()
			return
		}
		err = tx.Commit()
	}()

	// If this model is the default, clear the flag on every other model first
	if bool(llmModel.IsDefault) {
		_, err = tx.Exec(fmt.Sprintf(`UPDATE %s.llm_models SET is_default = false WHERE id != $1`, authInfo.Schema), id)
		if err != nil {
			return err
		}
	}

	// Update the model
	result, err := tx.Exec(fmt.Sprintf(`UPDATE %s.llm_models SET name = $1, context_window = $2, base_params = $3, provider_id = $4, gateway_model = $5, is_default = $6, extra_params = $7, capabilities = $8, updated_at = now() WHERE id = $9`, authInfo.Schema),
		llmModel.Name, llmModel.ContextWindow, llmModel.BaseParams,
		llmModel.ProviderID, llmModel.GatewayModel, llmModel.IsDefault, llmModel.ExtraParams, llmModel.Capabilities, id)
	if err != nil {
		return err
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return err
	}

	if rowsAffected == 0 {
		return sql.ErrNoRows
	}

	return nil
}

// Delete deletes one LLM model by id.
func (r *PostgresRepository) Delete(authInfo *auth.AuthInfo, id string) error {
	result, err := db.DB.Exec(fmt.Sprintf(`DELETE FROM %s.llm_models WHERE id = $1`, authInfo.Schema), id)
	if err != nil {
		return err
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return err
	}

	if rowsAffected == 0 {
		return sql.ErrNoRows
	}

	return nil
}

// List lists LLM models.
func (r *PostgresRepository) List(authInfo *auth.AuthInfo, req *ListLLMModelsRequest) ([]models.LLMModel, int, error) {
	// Build the filter
	whereClause := ""
	args := []interface{}{}
	argIndex := 1

	if req.Name != "" {
		whereClause += " WHERE name ILIKE $" + strconv.Itoa(argIndex) + ""
		args = append(args, "%"+req.Name+"%")
		argIndex++
	}

	if req.ProviderID != "" {
		if whereClause == "" {
			whereClause += " WHERE"
		} else {
			whereClause += " AND"
		}
		whereClause += " provider_id = $" + strconv.Itoa(argIndex) + ""
		args = append(args, req.ProviderID)
		argIndex++
	}

	// Total
	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM %s.llm_models", authInfo.Schema) + whereClause
	var total int
	err := db.DB.QueryRow(countQuery, args...).Scan(&total)
	if err != nil {
		return nil, 0, err
	}

	// Paging: an unset limit (0) means 10; a negative one means everything (no LIMIT appended), for settings pages and the
	// like that take it all at once (the frontend sends -1).
	page := req.Page
	if page < 1 {
		page = 1
	}

	limit := req.Limit
	if limit == 0 {
		limit = 10
	}

	offset := (page - 1) * limit

	// Sorting: sort_by must pass the column whitelist (ORDER BY injection), invalid columns are dropped
	sortClauses := []string{}
	for i, sortBy := range req.SortBy {
		if !allowedSortColumns[sortBy] {
			continue
		}
		sortOrder := "asc"
		// Use the direction given for that column, when it is valid
		if i < len(req.SortOrder) && (req.SortOrder[i] == "asc" || req.SortOrder[i] == "desc") {
			sortOrder = req.SortOrder[i]
		}
		sortClauses = append(sortClauses, sortBy+" "+sortOrder)
	}
	// Fall back to the default when no valid sort column is left (which also covers "no sort_by was sent")
	if len(sortClauses) == 0 {
		sortClauses = append(sortClauses, "name asc")
	}

	// Build the ORDER BY clause
	sortClause := "ORDER BY " + strings.Join(sortClauses, ", ")

	// Build the query: append no LIMIT when taking everything (limit < 0), so Postgres never sees a negative value
	query := fmt.Sprintf("SELECT %s FROM %s.llm_models", modelColumns, authInfo.Schema) + whereClause + " " + sortClause
	if limit > 0 {
		query += " LIMIT $" + strconv.Itoa(argIndex) + " OFFSET $" + strconv.Itoa(argIndex+1)
		args = append(args, limit, offset)
	}

	// Run it
	rows, err := db.DB.Query(query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var results []models.LLMModel
	for rows.Next() {
		var llmModel models.LLMModel
		if err := scanModel(rows, &llmModel); err != nil {
			return nil, 0, err
		}
		results = append(results, llmModel)
	}

	return results, total, nil
}

// ListByProviderID lists LLM models of one provider.
func (r *PostgresRepository) ListByProviderID(authInfo *auth.AuthInfo, providerID string, req *ListLLMModelsRequest) ([]models.LLMModel, int, error) {
	// Build the filter
	whereClause := " WHERE provider_id = $1"
	args := []interface{}{providerID}
	argIndex := 2

	if req.Name != "" {
		whereClause += " AND name ILIKE $" + strconv.Itoa(argIndex) + ""
		args = append(args, "%"+req.Name+"%")
		argIndex++
	}

	// Total
	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM %s.llm_models", authInfo.Schema) + whereClause
	var total int
	err := db.DB.QueryRow(countQuery, args...).Scan(&total)
	if err != nil {
		return nil, 0, err
	}

	// Paging: an unset limit (0) means 10; a negative one means everything (no LIMIT appended), for settings pages and the
	// like that take it all at once (the frontend sends -1).
	page := req.Page
	if page < 1 {
		page = 1
	}

	limit := req.Limit
	if limit == 0 {
		limit = 10
	}

	offset := (page - 1) * limit

	// Sorting: sort_by must pass the column whitelist (ORDER BY injection), invalid columns are dropped
	sortClauses := []string{}
	for i, sortBy := range req.SortBy {
		if !allowedSortColumns[sortBy] {
			continue
		}
		sortOrder := "asc"
		// Use the direction given for that column, when it is valid
		if i < len(req.SortOrder) && (req.SortOrder[i] == "asc" || req.SortOrder[i] == "desc") {
			sortOrder = req.SortOrder[i]
		}
		sortClauses = append(sortClauses, sortBy+" "+sortOrder)
	}
	// Fall back to the default when no valid sort column is left (which also covers "no sort_by was sent")
	if len(sortClauses) == 0 {
		sortClauses = append(sortClauses, "name asc")
	}

	// Build the ORDER BY clause
	sortClause := "ORDER BY " + strings.Join(sortClauses, ", ")

	// Build the query: append no LIMIT when taking everything (limit < 0), so Postgres never sees a negative value
	query := fmt.Sprintf("SELECT %s FROM %s.llm_models", modelColumns, authInfo.Schema) + whereClause + " " + sortClause
	if limit > 0 {
		query += " LIMIT $" + strconv.Itoa(argIndex) + " OFFSET $" + strconv.Itoa(argIndex+1)
		args = append(args, limit, offset)
	}

	// Run it
	rows, err := db.DB.Query(query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var results []models.LLMModel
	for rows.Next() {
		var llmModel models.LLMModel
		if err := scanModel(rows, &llmModel); err != nil {
			return nil, 0, err
		}
		results = append(results, llmModel)
	}

	return results, total, nil
}

// GetDefault returns the default model.
func (r *PostgresRepository) GetDefault(authInfo *auth.AuthInfo) (*models.LLMModel, error) {
	var llmModel models.LLMModel
	err := db.DB.QueryRow(fmt.Sprintf(`SELECT m.id, m.name, m.context_window, m.base_params, m.provider_id, m.gateway_id, m.gateway_model, m.is_default, m.extra_params, m.capabilities, m.created_at, m.updated_at
		FROM %s.llm_models m
		WHERE m.is_default = true`, authInfo.Schema)).Scan(
		&llmModel.ID, &llmModel.Name, &llmModel.ContextWindow, &llmModel.BaseParams,
		&llmModel.ProviderID, &llmModel.GatewayID, &llmModel.GatewayModel, &llmModel.IsDefault, &llmModel.ExtraParams, &llmModel.Capabilities, &llmModel.CreatedAt, &llmModel.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}

	return &llmModel, nil
}

// ListSelectable returns the selectable models: provider.enabled=true and a non-empty gateway_id, deduplicated with
// DISTINCT.
func (r *PostgresRepository) ListSelectable(authInfo *auth.AuthInfo) ([]models.LLMModel, error) {
	query := fmt.Sprintf(`
		SELECT DISTINCT m.gateway_model, m.provider_id
		FROM %s.llm_models m
		JOIN %s.llm_providers p ON p.id = m.provider_id
		WHERE m.gateway_id IS NOT NULL AND m.gateway_id <> ''
		  AND p.enabled = true
		ORDER BY m.gateway_model ASC`, authInfo.Schema, authInfo.Schema)

	rows, err := db.DB.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []models.LLMModel
	for rows.Next() {
		var m models.LLMModel
		if err := rows.Scan(&m.GatewayModel, &m.ProviderID); err != nil {
			return nil, err
		}
		results = append(results, m)
	}
	return results, nil
}

// ListAll returns every LLM model of the tenant (no paging).
// It serves scenarios that must walk everything, such as a gateway sync, without being affected by the paging semantics of
// List.
func (r *PostgresRepository) ListAll(authInfo *auth.AuthInfo) ([]models.LLMModel, error) {
	query := fmt.Sprintf(`SELECT %s FROM %s.llm_models ORDER BY name ASC`, modelColumns, authInfo.Schema)
	rows, err := db.DB.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []models.LLMModel
	for rows.Next() {
		var llmModel models.LLMModel
		if err := scanModel(rows, &llmModel); err != nil {
			return nil, err
		}
		results = append(results, llmModel)
	}
	return results, rows.Err()
}

// DeleteCascade deletes an LLM model and clears llm_model_id on the agents that reference it.
// agents.llm_model_id has no foreign key constraint (see the FOREIGN KEY list in tenant_template.sql), so without an
// explicit cleanup a dangling reference is left behind. A missing model returns sql.ErrNoRows.
func (r *PostgresRepository) DeleteCascade(authInfo *auth.AuthInfo, id string) error {
	tx, err := db.DB.Begin()
	if err != nil {
		return err
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	// Release the references first
	if _, err := tx.Exec(
		fmt.Sprintf(`UPDATE %s.agents SET llm_model_id = NULL WHERE llm_model_id = $1`, authInfo.Schema), id); err != nil {
		return err
	}

	result, err := tx.Exec(fmt.Sprintf(`DELETE FROM %s.llm_models WHERE id = $1`, authInfo.Schema), id)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return sql.ErrNoRows
	}

	if err := tx.Commit(); err != nil {
		return err
	}
	tx = nil
	return nil
}

// CountEnabledByGatewayModel counts the enabled models (non-empty gateway_id) sharing a gateway_model.
// An enabled model's gateway_model determines the LiteLLM model_name ("<schema>:<gateway_model>"), so a duplicate would
// make the gateway routing ambiguous; this is the uniqueness check to run before enabling.
func (r *PostgresRepository) CountEnabledByGatewayModel(authInfo *auth.AuthInfo, gatewayModel string) (int, error) {
	var count int
	err := db.DB.QueryRow(fmt.Sprintf(`
		SELECT COUNT(*) FROM %s.llm_models
		WHERE gateway_model = $1
		  AND gateway_id IS NOT NULL AND gateway_id <> ''`, authInfo.Schema), gatewayModel).Scan(&count)
	if err != nil {
		return 0, err
	}
	return count, nil
}
