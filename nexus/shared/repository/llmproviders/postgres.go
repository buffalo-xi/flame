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

// PostgresRepository PostgreSQL实现的LLM服务商存储
type PostgresRepository struct{}

// NewPostgresRepository 创建PostgreSQL存储实例
func NewPostgresRepository() *PostgresRepository {
	return &PostgresRepository{}
}

// allowedSortColumns ORDER BY 列名白名单。
// sort_by 来自请求体，直接拼接会形成注入点（Postgres 允许 ORDER BY 里放子查询），
// 因此只接受白名单内的列名，其余丢弃。
var allowedSortColumns = map[string]bool{
	"id": true, "name": true, "url": true, "protocol": true,
	"api_key": true, "icon": true, "enabled": true, "extra_config": true,
}

// GetByID 根据ID获取LLM服务商
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

// Create 创建LLM服务商（id 由程序生成，不再依赖数据库 DEFAULT）
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

// Update 更新LLM服务商
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

// Delete 根据ID删除LLM服务商
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

// List 获取LLM服务商列表
func (r *PostgresRepository) List(authInfo *auth.AuthInfo, req *ListLLMProvidersRequest) ([]models.LLMProvider, int, error) {
	// 构建查询条件
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

	// 计算总数
	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM %s.llm_providers", authInfo.Schema) + whereClause
	var total int
	err := db.DB.QueryRow(countQuery, args...).Scan(&total)
	if err != nil {
		return nil, 0, err
	}

	// 处理分页：limit 未指定（0）按 10；负数表示全量（不追加 LIMIT），
	// 供设置页等「一次拿完」的场景使用（前端传的就是 -1）。
	page := req.Page
	if page < 1 {
		page = 1
	}

	limit := req.Limit
	if limit == 0 {
		limit = 10
	}

	offset := (page - 1) * limit

	// 处理排序：sort_by 必须过列名白名单（防 ORDER BY 注入），非法列丢弃
	sortClauses := []string{}
	for i, sortBy := range req.SortBy {
		if !allowedSortColumns[sortBy] {
			continue
		}
		sortOrder := "asc"
		// 如果指定了对应列的排序顺序，使用指定的顺序
		if i < len(req.SortOrder) && (req.SortOrder[i] == "asc" || req.SortOrder[i] == "desc") {
			sortOrder = req.SortOrder[i]
		}
		sortClauses = append(sortClauses, sortBy+" "+sortOrder)
	}
	// 没有任何有效排序列时回退默认（同时覆盖「未传 sort_by」的情况）
	if len(sortClauses) == 0 {
		sortClauses = append(sortClauses, "name asc")
	}

	// 构建排序子句
	sortClause := "ORDER BY " + strings.Join(sortClauses, ", ")

	// 构建查询语句：全量（limit < 0）时不追加 LIMIT，避免 Postgres 对负值报错
	query := fmt.Sprintf("SELECT id, name, url, protocol, api_key, icon, enabled, extra_config FROM %s.llm_providers", authInfo.Schema) + whereClause + " " + sortClause
	if limit > 0 {
		query += " LIMIT $" + strconv.Itoa(argIndex) + " OFFSET $" + strconv.Itoa(argIndex+1)
		args = append(args, limit, offset)
	}

	// 执行查询
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
