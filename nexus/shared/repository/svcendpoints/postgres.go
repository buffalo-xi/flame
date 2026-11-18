package repository

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"flame/nexus/shared/auth"
	"flame/nexus/shared/db"
	"flame/nexus/shared/models"
	"flame/nexus/shared/paths"
	"flame/tools/xsql"

	"github.com/google/uuid"
)

// PostgresRepository PostgreSQL实现的服务端点存储
type PostgresRepository struct{}

// NewPostgresRepository 创建PostgreSQL存储实例
func NewPostgresRepository() *PostgresRepository {
	return &PostgresRepository{}
}

// ErrManagedEndpoint 托管端点不能单独删除。
//
// flow 的 rpc 端点是建流程时自动补的（见 CreateManagedRPC）：名字 / 描述 / input_schema
// 全部派生自实体，唯一独立状态是 exposed。删掉它不是「清理历史」，而是把
// local:rpc:<name> 这类引用（存在授权表、技能依赖、工具组里）变成死引用，
// 同时让流程按名字不可调用。要停用它，用流程状态（停用 / 废弃）或收回对外。
var ErrManagedEndpoint = errors.New("RPC 端点随流程自动管理，不能单独删除；要停用请停用或废弃流程")

// ErrEndpointExposed 端点仍在对外开放时不能删除。
//
// 对外端点此刻可能正被租户外的调用方使用，静默删掉等于服务直接消失；先「收回对外」。
var ErrEndpointExposed = errors.New("端点仍在对外开放，请先「收回对外」再删除")

// columns svc_endpoints 表查询列
const columns = "id, name, description, created_at, updated_at, target_id, target_type, protocol, input_schema, exposed"

// listColumns 列表查询列（带 e. 前缀：JOIN agents / flows 后 name 等同名列会歧义）
const listColumns = "e.id, e.name, e.description, e.created_at, e.updated_at, e.target_id, e.target_type, e.protocol, e.input_schema, e.exposed"

// scanEndpoint 扫描行到 SvcEndpoint
func scanEndpoint(scanner interface{ Scan(...interface{}) error }, e *models.SvcEndpoint) error {
	return scanner.Scan(&e.ID, &e.Name, &e.Description, &e.CreatedAt, &e.UpdatedAt, &e.TargetID, &e.TargetType, &e.Protocol, &e.InputSchema, &e.Exposed)
}

// scopeColumns 单行查询列：比 columns 多一个「目标是否超级智能体」的子查询。
//
// 用子查询而不是 JOIN：GetByID / FindByName 只按主键 / 名字命中，本身不带 JOIN；
// 而凭证的「除超级智能体外」范围判定需要这个标记（rpc 的 findEndpoint 就在热路径上）。
func scopeColumns(schema string) string {
	return columns + fmt.Sprintf(`, CASE WHEN target_type = 'agent'
		THEN COALESCE((SELECT is_supervisor FROM %s.agents WHERE id = svc_endpoints.target_id), false)
		ELSE false END`, schema)
}

// scanEndpointScoped 扫描「端点 + 目标是否 supervisor」。
func scanEndpointScoped(scanner interface{ Scan(...interface{}) error }, e *models.SvcEndpoint) error {
	return scanner.Scan(&e.ID, &e.Name, &e.Description, &e.CreatedAt, &e.UpdatedAt, &e.TargetID, &e.TargetType, &e.Protocol, &e.InputSchema, &e.Exposed, &e.TargetSupervisor)
}

// GetByID 根据ID获取服务端点
func (r *PostgresRepository) GetByID(authInfo *auth.AuthInfo, id string) (*models.SvcEndpoint, error) {
	var svcEndpoint models.SvcEndpoint
	err := scanEndpointScoped(db.DB.QueryRow(
		fmt.Sprintf(`SELECT %s FROM %s.svc_endpoints WHERE id = $1`, scopeColumns(authInfo.Schema), authInfo.Schema), id,
	), &svcEndpoint)
	if err != nil {
		return nil, err
	}
	return &svcEndpoint, nil
}

// ensureExposableTarget 把端点对外开放前的目标状态校验：**只拒「废弃」**。
//
// 「对外提供入口」与「定义能不能改」是两件事：正常态当然可以对外。
//
// 历史：这里曾把编辑态自动置为锁定（对外即锁定）。副作用太大 —— 改一行提示词要先解锁，
// 而解锁会顺带收回端点、断一下流量。现在改为「端点对外开放不改状态；定义被改动时由编辑器提醒」。
func ensureExposableTarget(schema, targetType, targetID string) error {
	targetTable := "flows"
	if targetType == "agent" {
		targetTable = "agents"
	}
	var status int
	if err := db.DB.QueryRow(
		fmt.Sprintf(`SELECT status FROM %s.%s WHERE id = $1`, schema, targetTable), targetID,
	).Scan(&status); err != nil {
		if err == sql.ErrNoRows {
			return fmt.Errorf("%s with ID '%s' not found", targetType, targetID)
		}
		return err
	}
	if status == models.StatusDeprecated {
		return fmt.Errorf("%s 已废弃（status=%d），不能对外开放端点", targetType, status)
	}
	return nil
}

// Create 创建服务端点
func (r *PostgresRepository) Create(authInfo *auth.AuthInfo, svcEndpoint *models.SvcEndpoint) (string, error) {
	// 验证 TargetType
	if svcEndpoint.TargetType != "agent" && svcEndpoint.TargetType != "flow" {
		return "", fmt.Errorf("target_type must be either 'agent' or 'flow'")
	}

	// 验证 Protocol
	if svcEndpoint.Protocol != "mcp" && svcEndpoint.Protocol != "rpc" && svcEndpoint.Protocol != "llm" {
		return "", fmt.Errorf("protocol must be one of 'mcp', 'rpc', 'llm'")
	}

	// 验证 TargetID 不为空
	if svcEndpoint.TargetID == "" {
		return "", fmt.Errorf("target_id cannot be empty")
	}

	// 验证目标存在
	targetTable := "agents"
	if svcEndpoint.TargetType == "flow" {
		targetTable = "flows"
	}
	// 端点名恒等于目标实体名（对外名 = 实体名，避免两套名字不一致）
	var targetName string
	err := db.DB.QueryRow(
		fmt.Sprintf(`SELECT name FROM %s.%s WHERE id = $1`, authInfo.Schema, targetTable),
		svcEndpoint.TargetID,
	).Scan(&targetName)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", fmt.Errorf("%s with ID '%s' not found", svcEndpoint.TargetType, svcEndpoint.TargetID)
		}
		return "", fmt.Errorf("error checking target: %v", err)
	}
	// 传入 name 时必须与实体名一致（端点名恒等于实体名，不允许另起名字）
	if svcEndpoint.Name != "" && !strings.EqualFold(svcEndpoint.Name, targetName) {
		return "", fmt.Errorf("endpoint name must equal the %s name %q", svcEndpoint.TargetType, targetName)
	}
	svcEndpoint.Name = targetName

	// 工作流端点对外就是工具名（MCP tool.name / agent 侧 local__<name> / 授权 ref
	// local:rpc:<name>），这三个消费方都只认 ASCII 字母数字横线。名字不合法时
	// 这里直接拒绝对外开放 —— 否则会静默产出一个「对外了但授权不了、模型也调不到」的端点
	// （存量中文名工作流就是这么漏出去的）。
	if svcEndpoint.TargetType == "flow" && !paths.ValidName(targetName) {
		return "", fmt.Errorf("工作流名 %q 不能作为端点名：只能用字母、数字与横线（不能以横线开头，≤64）；"+
			"请先在编辑器里改名，再对外开放端点", targetName)
	}

	// 创建即对外：先校验目标状态（只有废弃被拒）；默认 exposed=false，通常不会走到这里
	if svcEndpoint.Exposed.Bool() {
		if err := ensureExposableTarget(authInfo.Schema, svcEndpoint.TargetType, svcEndpoint.TargetID); err != nil {
			return "", err
		}
	}

	// 建 flow 端点时，input_schema 始终覆盖为 flows.arguments（与 UpdateFlow 同步逻辑一致）
	if svcEndpoint.TargetType == "flow" {
		var args xsql.JSONB
		err = db.DB.QueryRow(
			fmt.Sprintf(`SELECT arguments FROM %s.flows WHERE id = $1`, authInfo.Schema),
			svcEndpoint.TargetID,
		).Scan(&args)
		if err != nil {
			return "", fmt.Errorf("error loading flow arguments: %v", err)
		}
		svcEndpoint.InputSchema = args
	}

	// 检查同 target + protocol 是否已存在
	var dupCount int
	err = db.DB.QueryRow(
		fmt.Sprintf(`SELECT COUNT(*) FROM %s.svc_endpoints WHERE target_id = $1 AND target_type = $2 AND protocol = $3`, authInfo.Schema),
		svcEndpoint.TargetID, svcEndpoint.TargetType, svcEndpoint.Protocol,
	).Scan(&dupCount)
	if err != nil {
		return "", fmt.Errorf("error checking duplicate: %v", err)
	}
	if dupCount > 0 {
		return "", fmt.Errorf("endpoint already exists for %s '%s' with protocol '%s'", svcEndpoint.TargetType, svcEndpoint.TargetID, svcEndpoint.Protocol)
	}

	// 生成独立 UUID
	endpointID := uuid.NewString()

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

	// 插入 svc_endpoints（新建端点 exposed 默认 false，需手动对外开放才对租户外服务）
	// 显式写入 created_at/updated_at = NOW()，避免依赖数据库列默认值导致为空
	_, err = tx.Exec(
		fmt.Sprintf(`INSERT INTO %s.svc_endpoints (id, name, description, created_at, updated_at, target_id, target_type, protocol, input_schema, exposed) VALUES ($1, $2, $3, NOW(), NOW(), $4, $5, $6, $7, $8)`, authInfo.Schema),
		endpointID, svcEndpoint.Name, svcEndpoint.Description, svcEndpoint.TargetID, svcEndpoint.TargetType, svcEndpoint.Protocol, svcEndpoint.InputSchema, svcEndpoint.Exposed,
	)
	if err != nil {
		return "", err
	}

	// 同步 endpoints（端点名不写回实体；名称已在开头强制取实体名）
	if err := syncEndpoints(tx, authInfo.Schema, svcEndpoint.TargetType, svcEndpoint.TargetID); err != nil {
		return "", err
	}

	svcEndpoint.ID = endpointID
	return endpointID, nil
}

// CreateManagedRPC 在调用方的事务里给实体补一条 RPC 端点（exposed=false），并同步镜像。
//
// 流程要被别的流程按端点名调用（rpc 节点存的就是端点名）、要出现在工具目录里，
// 都依赖这一行；而端点名 / 描述 / input_schema 全部派生自实体 —— 所以它是实体的
// 一个协议槽位，不是用户手工资产（单独删除见 ErrManagedEndpoint）。
//
// 名字不合法的实体（中文名等）不建：端点名同时是工具名与授权 ref local:rpc:<name>，
// 建出来只会是模型调不到、也授权不了的死端点（Create 里拦的是同一件事）。
func CreateManagedRPC(tx *sql.Tx, schema, targetType, targetID, name, description string, inputSchema xsql.JSONB) error {
	if !paths.ValidName(name) {
		return nil
	}
	_, err := tx.Exec(
		fmt.Sprintf(`INSERT INTO %s.svc_endpoints (id, name, description, created_at, updated_at, target_id, target_type, protocol, input_schema, exposed) VALUES ($1, $2, $3, NOW(), NOW(), $4, $5, 'rpc', $6, false)`, schema),
		uuid.NewString(), name, description, targetID, targetType, inputSchema,
	)
	if err != nil {
		return err
	}
	return syncEndpoints(tx, schema, targetType, targetID)
}

// FindByName 按 protocol + name 精确查端点（对外调用按名定位用，大小写不敏感）；
// 不存在返回 (nil, nil)。不过滤 exposed：本租户直调允许调用未对外开放的端点。
func (r *PostgresRepository) FindByName(authInfo *auth.AuthInfo, protocol, name string) (*models.SvcEndpoint, error) {
	var e models.SvcEndpoint
	err := scanEndpointScoped(db.DB.QueryRow(
		fmt.Sprintf(`SELECT %s FROM %s.svc_endpoints WHERE protocol = $1 AND LOWER(name) = LOWER($2) LIMIT 1`, scopeColumns(authInfo.Schema), authInfo.Schema),
		protocol, name,
	), &e)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &e, nil
}

// UpdateExposed 更新端点的对外状态（不影响其他字段）
func (r *PostgresRepository) UpdateExposed(authInfo *auth.AuthInfo, id string, exposed bool) error {
	// 先查 target_type / target_id
	var targetType, targetID string
	if err := db.DB.QueryRow(
		fmt.Sprintf(`SELECT target_type, target_id FROM %s.svc_endpoints WHERE id = $1`, authInfo.Schema), id,
	).Scan(&targetType, &targetID); err != nil {
		return err
	}

	// 对外开放前校验目标状态（收回不校验：任何状态都可以关）
	if exposed {
		if err := ensureExposableTarget(authInfo.Schema, targetType, targetID); err != nil {
			return err
		}
	}

	tx, err := db.DB.Begin()
	if err != nil {
		return err
	}

	result, err := tx.Exec(
		fmt.Sprintf(`UPDATE %s.svc_endpoints SET exposed = $1, updated_at = NOW() WHERE id = $2`, authInfo.Schema),
		exposed, id,
	)
	if err != nil {
		tx.Rollback()
		return err
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		tx.Rollback()
		return err
	}
	if rowsAffected == 0 {
		tx.Rollback()
		return sql.ErrNoRows
	}

	if err := syncEndpoints(tx, authInfo.Schema, targetType, targetID); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// Update 更新服务端点
func (r *PostgresRepository) Update(authInfo *auth.AuthInfo, id string, svcEndpoint *models.SvcEndpoint) error {
	// 名称不参与更新：端点名恒等于目标实体名（改名走实体改名流程 + repo 层同步）
	result, err := db.DB.Exec(
		fmt.Sprintf(`UPDATE %s.svc_endpoints SET description = $1, input_schema = $2, updated_at = NOW() WHERE id = $3`, authInfo.Schema),
		svcEndpoint.Description, svcEndpoint.InputSchema, id,
	)
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

// Delete 根据ID删除单个服务端点
//
// 两条拒绝（DeleteByTarget 不受影响：那是实体删除时的级联清理）：
//   - flow 的 rpc 端点由流程托管，见 ErrManagedEndpoint；
//   - 仍对外开放的端点先收回，见 ErrEndpointExposed。
func (r *PostgresRepository) Delete(authInfo *auth.AuthInfo, id string) error {
	// 先获取该端点的 target_type / target_id / protocol / exposed
	var targetType, targetID string
	var protocol sql.NullString
	var exposed bool
	err := db.DB.QueryRow(
		fmt.Sprintf(`SELECT target_type, target_id, protocol, exposed FROM %s.svc_endpoints WHERE id = $1`, authInfo.Schema), id,
	).Scan(&targetType, &targetID, &protocol, &exposed)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil
		}
		return err
	}

	if targetType == "flow" && protocol.String == "rpc" {
		return ErrManagedEndpoint
	}
	if exposed {
		return ErrEndpointExposed
	}

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

	// 删除该端点
	_, err = tx.Exec(fmt.Sprintf(`DELETE FROM %s.svc_endpoints WHERE id = $1`, authInfo.Schema), id)
	if err != nil {
		return err
	}

	// 同步目标的 endpoints（agent/flow 同一套机制）
	if err := syncEndpoints(tx, authInfo.Schema, targetType, targetID); err != nil {
		return err
	}

	return nil
}

// DeleteByTarget 删除指定目标的所有协议端点，并清除目标上的端点标记
func (r *PostgresRepository) DeleteByTarget(authInfo *auth.AuthInfo, targetType string, targetID string) error {
	if targetType != "agent" && targetType != "flow" {
		return fmt.Errorf("target_type must be either 'agent' or 'flow'")
	}

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

	// 删除该 target 的所有端点
	_, err = tx.Exec(
		fmt.Sprintf(`DELETE FROM %s.svc_endpoints WHERE target_id = $1 AND target_type = $2`, authInfo.Schema),
		targetID, targetType,
	)
	if err != nil {
		return err
	}

	// 清除目标的端点记录：清空 endpoints（agent/flow 同一套机制）
	targetTable := "flows"
	if targetType != "flow" {
		targetTable = "agents"
	}
	_, err = tx.Exec(
		fmt.Sprintf(`UPDATE %s.%s SET endpoints = '[]'::jsonb WHERE id = $1`, authInfo.Schema, targetTable),
		targetID,
	)
	if err != nil {
		return err
	}

	return nil
}

// RetractByTarget 收回指定目标所有协议端点的对外（仅置 exposed=false，端点记录与配置保留）
func (r *PostgresRepository) RetractByTarget(authInfo *auth.AuthInfo, targetType string, targetID string) error {
	if targetType != "agent" && targetType != "flow" {
		return fmt.Errorf("target_type must be either 'agent' or 'flow'")
	}

	tx, err := db.DB.Begin()
	if err != nil {
		return err
	}

	_, err = tx.Exec(
		fmt.Sprintf(`UPDATE %s.svc_endpoints SET exposed = false, updated_at = NOW() WHERE target_id = $1 AND target_type = $2`, authInfo.Schema),
		targetID, targetType,
	)
	if err != nil {
		tx.Rollback()
		return err
	}

	if err := syncEndpoints(tx, authInfo.Schema, targetType, targetID); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// endpointWhere 按过滤条件拼 WHERE 子句与参数（不含分页与排序）。
// 列名统一带 e. 前缀：JOIN agents / flows 后 name 等同名列会歧义。
func endpointWhere(req *ListSvcEndpointsRequest) (string, []interface{}) {
	whereClause := ""
	args := []interface{}{}
	addCond := func(cond string, v interface{}) {
		if whereClause == "" {
			whereClause = " WHERE " + fmt.Sprintf(cond, len(args)+1)
		} else {
			whereClause += " AND " + fmt.Sprintf(cond, len(args)+1)
		}
		args = append(args, v)
	}

	if req.Name != "" {
		addCond("e.name ILIKE $%d", "%"+req.Name+"%")
	}
	if req.TargetType != "" {
		addCond("e.target_type = $%d", req.TargetType)
	}
	if req.TargetID != "" {
		addCond("e.target_id = $%d", req.TargetID)
	}
	if req.Protocol != "" {
		addCond("e.protocol = $%d", req.Protocol)
	}
	if req.Exposed != nil {
		addCond("e.exposed = $%d", *req.Exposed)
	}
	return whereClause, args
}

// endpointOrderBy 拼 ORDER BY 子句（默认按名称升序），返回值自带前导空格。
func endpointOrderBy(req *ListSvcEndpointsRequest) string {
	sortClauses := []string{}
	if len(req.SortBy) == 0 {
		sortClauses = append(sortClauses, "e.name asc")
	} else {
		for i, sortBy := range req.SortBy {
			sortOrder := "asc"
			if i < len(req.SortOrder) && (req.SortOrder[i] == "asc" || req.SortOrder[i] == "desc") {
				sortOrder = req.SortOrder[i]
			}
			sortClauses = append(sortClauses, "e."+sortBy+" "+sortOrder)
		}
	}
	return " ORDER BY " + strings.Join(sortClauses, ", ")
}

// endpointSelectSQL 拼查询主体：LEFT JOIN 目标实体取名称（实体已删除时为空串）。
// whereClause 为空表示不带 WHERE；排序与分页由调用方自行追加。
func endpointSelectSQL(schema, whereClause string) string {
	return fmt.Sprintf(`SELECT %s, COALESCE(a.name, f.name, '') AS target_name,
			COALESCE(a.is_supervisor, false) AS target_supervisor
		FROM %s.svc_endpoints e
		LEFT JOIN %s.agents a ON e.target_type = 'agent' AND a.id = e.target_id
		LEFT JOIN %s.flows f ON e.target_type = 'flow' AND f.id = e.target_id`,
		listColumns, schema, schema, schema) + whereClause
}

// scanEndpointRows 扫描「端点 + target_name」结果集
func scanEndpointRows(rows *sql.Rows) ([]models.SvcEndpoint, error) {
	var results []models.SvcEndpoint
	for rows.Next() {
		var svcEndpoint models.SvcEndpoint
		if err := rows.Scan(&svcEndpoint.ID, &svcEndpoint.Name, &svcEndpoint.Description,
			&svcEndpoint.CreatedAt, &svcEndpoint.UpdatedAt, &svcEndpoint.TargetID, &svcEndpoint.TargetType,
			&svcEndpoint.Protocol, &svcEndpoint.InputSchema, &svcEndpoint.Exposed,
			&svcEndpoint.TargetName, &svcEndpoint.TargetSupervisor); err != nil {
			return nil, err
		}
		results = append(results, svcEndpoint)
	}
	return results, rows.Err()
}

// List 获取服务端点列表（LEFT JOIN 目标实体，附带 target_name）
func (r *PostgresRepository) List(authInfo *auth.AuthInfo, req *ListSvcEndpointsRequest) ([]models.SvcEndpoint, int, error) {
	whereClause, args := endpointWhere(req)

	// 计算总数
	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM %s.svc_endpoints e", authInfo.Schema) + whereClause
	var total int
	err := db.DB.QueryRow(countQuery, args...).Scan(&total)
	if err != nil {
		return nil, 0, err
	}

	// 处理分页
	page := req.Page
	if page < 1 {
		page = 1
	}
	limit := req.Limit
	if limit < 1 {
		limit = 10
	}
	offset := (page - 1) * limit

	query := endpointSelectSQL(authInfo.Schema, whereClause) + endpointOrderBy(req) +
		fmt.Sprintf(" LIMIT $%d OFFSET $%d", len(args)+1, len(args)+2)
	args = append(args, limit, offset)

	rows, err := db.DB.Query(query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	results, err := scanEndpointRows(rows)
	if err != nil {
		return nil, 0, err
	}

	return results, total, nil
}

// ListAll 取出满足条件的全部端点（不分页、不统计总数）。
//
// 与 List 的唯一区别就是没有分页：List 的默认 limit=10 会静默截断，
// 凡是「要把整份目录装进内存」的场景（MCP / RPC 的工具注册）都必须走这里，
// 否则第 11 个端点起就会凭空消失。
func (r *PostgresRepository) ListAll(authInfo *auth.AuthInfo, req *ListSvcEndpointsRequest) ([]models.SvcEndpoint, error) {
	whereClause, args := endpointWhere(req)
	query := endpointSelectSQL(authInfo.Schema, whereClause) + endpointOrderBy(req)

	rows, err := db.DB.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanEndpointRows(rows)
}

// syncEndpoints 从 svc_endpoints 重新聚合目标的端点协议，写回其 endpoints jsonb 列。
// agent 与 flow 共用同一套机制（target_type 决定目标表）。
func syncEndpoints(tx *sql.Tx, schema, targetType, targetID string) error {
	targetTable := "flows"
	if targetType == "agent" {
		targetTable = "agents"
	}

	rows, err := tx.Query(
		fmt.Sprintf(`SELECT protocol, exposed FROM %s.svc_endpoints WHERE target_id = $1 AND target_type = $2 ORDER BY created_at ASC`, schema),
		targetID, targetType,
	)
	if err != nil {
		return err
	}
	defer rows.Close()

	endpoints := []models.EndpointProtocol{}
	for rows.Next() {
		var p models.EndpointProtocol
		if err := rows.Scan(&p.Protocol, &p.Exposed); err != nil {
			return err
		}
		endpoints = append(endpoints, p)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	endpointsJSON, err := json.Marshal(endpoints)
	if err != nil {
		return err
	}

	// 只写回 endpoints 列，**不动 status**：端点对外与「定义能不能改」已解耦。
	// 旧口径「有端点对外 ⇒ 自动锁定」已取消（改一行提示词就得先解锁、解锁又收回端点，
	// 代价与收益不成比例）。锁定态已从工作流 / agent / skill 全部取消。
	_, err = tx.Exec(
		fmt.Sprintf(`UPDATE %s.%s SET endpoints = $2 WHERE id = $1`, schema, targetTable),
		targetID, endpointsJSON,
	)
	return err
}
