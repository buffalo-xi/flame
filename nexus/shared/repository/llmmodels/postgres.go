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

// PostgresRepository PostgreSQL实现的LLM模型存储
type PostgresRepository struct{}

// NewPostgresRepository 创建PostgreSQL存储实例
func NewPostgresRepository() *PostgresRepository {
	return &PostgresRepository{}
}

// 模型查询的字段列表
const modelColumns = `id, name, context_window, base_params, provider_id, gateway_id, gateway_model, is_default, extra_params, capabilities, created_at, updated_at`

// allowedSortColumns ORDER BY 列名白名单。
// sort_by 来自请求体，直接拼接会形成注入点（Postgres 允许 ORDER BY 里放子查询），
// 因此只接受白名单内的列名，其余丢弃。
var allowedSortColumns = map[string]bool{
	"id": true, "name": true, "context_window": true, "base_params": true,
	"provider_id": true, "gateway_id": true, "gateway_model": true,
	"is_default": true, "extra_params": true, "capabilities": true,
	"created_at": true, "updated_at": true,
}

// scanModel 将一行扫描到 LLMModel
func scanModel(scanner interface{ Scan(...interface{}) error }, m *models.LLMModel) error {
	return scanner.Scan(
		&m.ID, &m.Name, &m.ContextWindow, &m.BaseParams,
		&m.ProviderID, &m.GatewayID, &m.GatewayModel, &m.IsDefault, &m.ExtraParams, &m.Capabilities, &m.CreatedAt, &m.UpdatedAt,
	)
}

// GetByID 根据ID获取LLM模型
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

// GetByGatewayModel 根据 gateway_model（用户侧模型名）获取首个启用模型
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

// UpdateGatewayID 更新模型的 gateway_id
func (r *PostgresRepository) UpdateGatewayID(authInfo *auth.AuthInfo, modelID, gatewayID string) error {
	_, err := db.DB.Exec(
		fmt.Sprintf(`UPDATE %s.llm_models SET gateway_id = $1, updated_at = now() WHERE id = $2`, authInfo.Schema),
		gatewayID, modelID,
	)
	return err
}

// LockAndGetGatewayID 事务内锁行（FOR UPDATE）并返回当前 gateway_id（供 Provision 防并发）
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

// SetGatewayIDInTx 事务内设置 gateway_id（Provision 成功且锁内无冲突时调用）
func (r *PostgresRepository) SetGatewayIDInTx(tx *sql.Tx, authInfo *auth.AuthInfo, modelID, gatewayID string) error {
	_, err := tx.Exec(fmt.Sprintf(`UPDATE %s.llm_models SET gateway_id = $1, updated_at = now() WHERE id = $2`, authInfo.Schema), gatewayID, modelID)
	return err
}

// ClearGatewayIDInTx 锁行 + 条件清空：仅当当前 gateway_id == expectGatewayID 才置 NULL，返回受影响行数
func (r *PostgresRepository) ClearGatewayIDInTx(tx *sql.Tx, authInfo *auth.AuthInfo, modelID, expectGatewayID string) (int64, error) {
	res, err := tx.Exec(fmt.Sprintf(
		`UPDATE %s.llm_models SET gateway_id = NULL, updated_at = now() WHERE id = $1 AND gateway_id = $2`,
		authInfo.Schema), modelID, expectGatewayID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ClearAllGatewayIDs 批量将该租户下所有 gateway_id 非空的模型置空
func (r *PostgresRepository) ClearAllGatewayIDs(authInfo *auth.AuthInfo) error {
	_, err := db.DB.Exec(fmt.Sprintf(`UPDATE %s.llm_models SET gateway_id = NULL, updated_at = now() WHERE gateway_id IS NOT NULL AND gateway_id <> ''`, authInfo.Schema))
	return err
}

// Create 创建LLM模型
func (r *PostgresRepository) Create(authInfo *auth.AuthInfo, llmModel *models.LLMModel) (string, error) {
	// 开始事务
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

	// 如果当前模型设置为默认，则将其他所有模型的默认值设置为false
	if bool(llmModel.IsDefault) {
		_, err = tx.Exec(fmt.Sprintf(`UPDATE %s.llm_models SET is_default = false`, authInfo.Schema))
		if err != nil {
			return "", err
		}
	}

	// 创建新模型（id 由程序生成，不再依赖数据库 DEFAULT）
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

// Update 更新LLM模型
func (r *PostgresRepository) Update(authInfo *auth.AuthInfo, id string, llmModel *models.LLMModel) error {
	// 开始事务
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

	// 如果当前模型设置为默认，则将其他所有模型的默认值设置为false
	if bool(llmModel.IsDefault) {
		_, err = tx.Exec(fmt.Sprintf(`UPDATE %s.llm_models SET is_default = false WHERE id != $1`, authInfo.Schema), id)
		if err != nil {
			return err
		}
	}

	// 更新模型
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

// Delete 根据ID删除LLM模型
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

// List 获取LLM模型列表
func (r *PostgresRepository) List(authInfo *auth.AuthInfo, req *ListLLMModelsRequest) ([]models.LLMModel, int, error) {
	// 构建查询条件
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

	// 计算总数
	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM %s.llm_models", authInfo.Schema) + whereClause
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
	query := fmt.Sprintf("SELECT %s FROM %s.llm_models", modelColumns, authInfo.Schema) + whereClause + " " + sortClause
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

// ListByProviderID 根据ProviderID获取LLM模型列表
func (r *PostgresRepository) ListByProviderID(authInfo *auth.AuthInfo, providerID string, req *ListLLMModelsRequest) ([]models.LLMModel, int, error) {
	// 构建查询条件
	whereClause := " WHERE provider_id = $1"
	args := []interface{}{providerID}
	argIndex := 2

	if req.Name != "" {
		whereClause += " AND name ILIKE $" + strconv.Itoa(argIndex) + ""
		args = append(args, "%"+req.Name+"%")
		argIndex++
	}

	// 计算总数
	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM %s.llm_models", authInfo.Schema) + whereClause
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
	query := fmt.Sprintf("SELECT %s FROM %s.llm_models", modelColumns, authInfo.Schema) + whereClause + " " + sortClause
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

// GetDefault 获取默认模型
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

// ListSelectable 返回可选择的模型：provider.enabled=true 且 gateway_id 非空，DISTINCT 去重
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

// ListAll 返回该租户下全部 LLM 模型（不分页）。
// 供网关同步这类需要全量遍历的场景使用，避免受 List 的分页语义影响。
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

// DeleteCascade 删除 LLM 模型，并把引用它的 agent 的 llm_model_id 置空。
// agents.llm_model_id 没有外键约束（见 tenant_template.sql 的 FOREIGN KEY 列表），
// 不显式清理会留下悬空引用。模型不存在时返回 sql.ErrNoRows。
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

	// 先解开引用
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

// CountEnabledByGatewayModel 统计已启用（gateway_id 非空）且 gateway_model 同名的模型数量。
// 已启用模型的 gateway_model 决定 LiteLLM 的 model_name（"<schema>:<gateway_model>"），
// 重名会让网关侧路由歧义，因此启用前用它做唯一性校验。
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
