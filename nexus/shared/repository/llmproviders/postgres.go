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

// PostgresRepository is the PostgreSQL implementation of LLM provider storage.
type PostgresRepository struct{}

// NewPostgresRepository creates a PostgreSQL storage instance.
func NewPostgresRepository() *PostgresRepository {
	return &PostgresRepository{}
}

// allowedSortColumns is the ORDER BY column whitelist.
// sort_by comes from the request body, and concatenating it directly would open an injection point (Postgres allows
// subqueries in ORDER BY), so only whitelisted column names are accepted and the rest are dropped.
var allowedSortColumns = map[string]bool{
	"id": true, "name": true, "url": true, "protocol": true,
	"api_key": true, "icon": true, "enabled": true, "extra_config": true,
}

// GetByID returns one LLM provider by id.
func (r *PostgresRepository) GetByID(authInfo *auth.AuthInfo, id string) (*models.LLMProvider, error) {
	var llmProvider models.LLMProvider
	err := db.DB.QueryRow(fmt.Sprintf(`SELECT id, name, url, protocol, api_key, icon, enabled, extra_config FROM %s.llm_providers WHERE id = $1`, authInfo.Schema), id).Scan(
		&llmProvider.ID, &llmProvider.Name, &llmProvider.URL, &llmProvider.Protocol, &llmProvider.APIKey, &llmProvider.Icon, &llmProvider.Enabled, &llmProvider.ExtraConfig,
	)
	if err != nil {
		return nil, err
	}

	return &llmProvider, nil
}

// Create creates an LLM provider (the id is generated in the program, no longer relying on a database DEFAULT).
func (r *PostgresRepository) Create(authInfo *auth.AuthInfo, llmProvider *models.LLMProvider) (string, error) {
	id := llmProvider.ID
	if id == "" {
		id = uuid.NewString()
	}
	if _, err := db.DB.Exec(fmt.Sprintf(`INSERT INTO %s.llm_providers
		(id, name, url, protocol, api_key, icon, enabled, extra_config)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`, authInfo.Schema),
		id, llmProvider.Name, llmProvider.URL, llmProvider.Protocol, llmProvider.APIKey,
		llmProvider.Icon, llmProvider.Enabled, llmProvider.ExtraConfig); err != nil {
		return "", err
	}
	return id, nil
}

// Update updates an LLM provider.
func (r *PostgresRepository) Update(authInfo *auth.AuthInfo, id string, llmProvider *models.LLMProvider) error {
	result, err := db.DB.Exec(fmt.Sprintf(`UPDATE %s.llm_providers SET name = $1, url = $2, protocol = $3, api_key = $4, icon = $5, enabled = $6, extra_config = $7 WHERE id = $8`, authInfo.Schema),
		llmProvider.Name, llmProvider.URL, llmProvider.Protocol, llmProvider.APIKey, llmProvider.Icon, llmProvider.Enabled, llmProvider.ExtraConfig, id)
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

// Delete deletes one LLM provider by id.
func (r *PostgresRepository) Delete(authInfo *auth.AuthInfo, id string) error {
	result, err := db.DB.Exec(fmt.Sprintf(`DELETE FROM %s.llm_providers WHERE id = $1`, authInfo.Schema), id)
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

// List lists LLM providers.
func (r *PostgresRepository) List(authInfo *auth.AuthInfo, req *ListLLMProvidersRequest) ([]models.LLMProvider, int, error) {
	// Build the filter
	whereClause := ""
	args := []interface{}{}
	argIndex := 1

	if req.Name != "" {
		whereClause += " WHERE name ILIKE $" + strconv.Itoa(argIndex) + ""
		args = append(args, "%"+req.Name+"%")
		argIndex++
	}

	if req.Protocol != "" {
		if whereClause == "" {
			whereClause += " WHERE"
		} else {
			whereClause += " AND"
		}
		whereClause += " protocol ILIKE $" + strconv.Itoa(argIndex) + ""
		args = append(args, "%"+req.Protocol+"%")
		argIndex++
	}

	// Total
	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM %s.llm_providers", authInfo.Schema) + whereClause
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
	query := fmt.Sprintf("SELECT id, name, url, protocol, api_key, icon, enabled, extra_config FROM %s.llm_providers", authInfo.Schema) + whereClause + " " + sortClause
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

	var results []models.LLMProvider
	for rows.Next() {
		var llmProvider models.LLMProvider
		if err := rows.Scan(&llmProvider.ID, &llmProvider.Name, &llmProvider.URL, &llmProvider.Protocol, &llmProvider.APIKey, &llmProvider.Icon, &llmProvider.Enabled, &llmProvider.ExtraConfig); err != nil {
			return nil, 0, err
		}
		results = append(results, llmProvider)
	}

	return results, total, nil
}
