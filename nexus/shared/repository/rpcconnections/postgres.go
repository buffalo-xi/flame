package rpcconnections

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"flame/nexus/shared/auth"
	"flame/nexus/shared/db"
	"flame/nexus/shared/models"

	"github.com/google/uuid"
)

// PostgresRepository 基于 PostgreSQL 的 RPC 连接仓储实现
type PostgresRepository struct{}

// NewRepository 构造仓储
func NewRepository() Repository {
	return &PostgresRepository{}
}

// compile-time 断言实现接口
var _ Repository = (*PostgresRepository)(nil)

// Create 创建连接，返回新记录 ID。
// id 由程序生成（不再依赖数据库 DEFAULT uuid_generate_v4()）；调用方已带 ID 时沿用之。
func (r *PostgresRepository) Create(authInfo *auth.AuthInfo, conn *models.RpcConnection) (string, error) {
	id := conn.ID
	if id == "" {
		id = uuid.NewString()
	}
	query := fmt.Sprintf(`INSERT INTO %s.rpc_connections
		(id, name, display_name, description, url, command, args, work_dir, env, transport, auth, is_active)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, authInfo.Schema)
	if _, err := db.DB.Exec(query,
		id, conn.Name, nullString(conn.DisplayName), nullString(conn.Description), nullString(conn.URL),
		nullString(conn.Command), conn.Args, nullString(conn.WorkDir), conn.Env, conn.Transport, conn.Auth, conn.IsActive,
	); err != nil {
		return "", wrapPgErr(err)
	}
	return id, nil
}

// Update 更新连接（按 ID 更新除 ID、is_active 外的字段；已启用的连接不允许修改）
func (r *PostgresRepository) Update(authInfo *auth.AuthInfo, id string, conn *models.RpcConnection) error {
	// 先查数据库当前记录的 is_active 状态，已启用的连接不允许修改
	var isActive bool
	checkQuery := fmt.Sprintf(`SELECT is_active FROM %s.rpc_connections WHERE id=$1`, authInfo.Schema)
	if err := db.DB.QueryRow(checkQuery, id).Scan(&isActive); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return sql.ErrNoRows
		}
		return wrapPgErr(err)
	}
	if isActive {
		return fmt.Errorf("不允许修改已启用的 RPC 连接")
	}

	query := fmt.Sprintf(`UPDATE %s.rpc_connections SET
		name=COALESCE($2,name),
		display_name=COALESCE($3,display_name),
		description=$4,
		url=$5,
		command=$6,
		args=COALESCE($7,args),
		work_dir=COALESCE($8,work_dir),
		env=COALESCE($9,env),
		transport=COALESCE($10,transport),
		auth=COALESCE($11,auth),
		updated_at=CURRENT_TIMESTAMP
		WHERE id=$1`, authInfo.Schema)
	_, err := db.DB.Exec(query,
		id, nullString(conn.Name), nullString(conn.DisplayName), nullString(conn.Description), nullString(conn.URL),
		nullString(conn.Command), conn.Args, nullString(conn.WorkDir), conn.Env, nullString(conn.Transport), conn.Auth,
	)
	return wrapPgErr(err)
}

// UpdateIsActive 更新启用状态
func (r *PostgresRepository) UpdateIsActive(authInfo *auth.AuthInfo, id string, isActive bool) error {
	query := fmt.Sprintf(`UPDATE %s.rpc_connections SET is_active=$2, updated_at=CURRENT_TIMESTAMP WHERE id=$1`, authInfo.Schema)
	_, err := db.DB.Exec(query, id, isActive)
	return wrapPgErr(err)
}

// Delete 删除连接（工具表级联删除）
func (r *PostgresRepository) Delete(authInfo *auth.AuthInfo, id string) error {
	query := fmt.Sprintf(`DELETE FROM %s.rpc_connections WHERE id=$1`, authInfo.Schema)
	res, err := db.DB.Exec(query, id)
	if err != nil {
		return wrapPgErr(err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// GetByID 按 ID 获取连接
func (r *PostgresRepository) GetByID(authInfo *auth.AuthInfo, id string) (*models.RpcConnection, error) {
	query := fmt.Sprintf(`SELECT id,name,display_name,description,url,command,args,work_dir,env,transport,auth,is_active,created_at,updated_at
		FROM %s.rpc_connections WHERE id=$1`, authInfo.Schema)
	row := db.DB.QueryRow(query, id)
	conn, err := scanConnection(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, sql.ErrNoRows
		}
		return nil, err
	}
	return conn, nil
}

// List 列出连接（分页 + 过滤）
func (r *PostgresRepository) List(authInfo *auth.AuthInfo, req *ListRpcConnectionsRequest) ([]models.RpcConnection, int, error) {
	conditions := []string{}
	args := []any{}
	argIndex := 1

	if req.Name != "" {
		conditions = append(conditions, fmt.Sprintf("name ILIKE $%d", argIndex))
		args = append(args, "%"+req.Name+"%")
		argIndex++
	}
	if req.Transport != "" {
		conditions = append(conditions, fmt.Sprintf("transport=$%d", argIndex))
		args = append(args, req.Transport)
		argIndex++
	}
	if req.IsActive != nil {
		conditions = append(conditions, fmt.Sprintf("is_active=$%d", argIndex))
		args = append(args, *req.IsActive)
		argIndex++
	}

	whereClause := ""
	if len(conditions) > 0 {
		whereClause = " WHERE " + strings.Join(conditions, " AND ")
	}

	// 计数
	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM %s.rpc_connections", authInfo.Schema) + whereClause
	var total int
	if err := db.DB.QueryRow(countQuery, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	// 分页
	page := req.Page
	limit := req.Limit
	if page <= 0 {
		page = 1
	}
	if limit <= 0 {
		limit = 20
	}
	offset := (page - 1) * limit
	args = append(args, limit, offset)
	listQuery := fmt.Sprintf(`SELECT id,name,display_name,description,url,command,args,work_dir,env,transport,auth,is_active,created_at,updated_at
		FROM %s.rpc_connections`, authInfo.Schema) + whereClause +
		fmt.Sprintf(" ORDER BY created_at DESC LIMIT $%d OFFSET $%d", argIndex, argIndex+1)

	rows, err := db.DB.Query(listQuery, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []models.RpcConnection
	for rows.Next() {
		conn, err := scanConnection(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *conn)
	}
	return out, total, rows.Err()
}

// ListTools 列出某连接下已持久化的工具
func (r *PostgresRepository) ListTools(authInfo *auth.AuthInfo, connectionID string) ([]models.RpcConnectionTool, error) {
	query := fmt.Sprintf(`SELECT id,connection_id,name,description,input_schema,updated_at
		FROM %s.rpc_connection_tools WHERE connection_id=$1 ORDER BY name`, authInfo.Schema)
	rows, err := db.DB.Query(query, connectionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.RpcConnectionTool
	for rows.Next() {
		var t models.RpcConnectionTool
		if err := rows.Scan(&t.ID, &t.ConnectionID, &t.Name, &t.Description, &t.InputSchema, &t.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ReplaceTools 全量替换某连接下的工具：先删除旧工具，再批量插入新工具。
// id 由程序生成（不再依赖数据库 DEFAULT uuid_generate_v4()）。
func (r *PostgresRepository) ReplaceTools(authInfo *auth.AuthInfo, connectionID string, tools []*models.RpcConnectionTool) error {
	tx, err := db.DB.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(fmt.Sprintf(`DELETE FROM %s.rpc_connection_tools WHERE connection_id=$1`, authInfo.Schema), connectionID); err != nil {
		return wrapPgErr(err)
	}

	if len(tools) > 0 {
		valueStrings := make([]string, 0, len(tools))
		valueArgs := make([]any, 0, len(tools)*6)
		for i, t := range tools {
			base := i * 6
			valueStrings = append(valueStrings, fmt.Sprintf("($%d,$%d,$%d,$%d,$%d,$%d)", base+1, base+2, base+3, base+4, base+5, base+6))
			tid := t.ID
			if tid == "" {
				tid = uuid.NewString()
			}
			valueArgs = append(valueArgs, tid, connectionID, t.Name, t.Description, t.InputSchema, t.UpdatedAt)
		}
		insert := fmt.Sprintf(`INSERT INTO %s.rpc_connection_tools
			(id, connection_id, name, description, input_schema, updated_at) VALUES `, authInfo.Schema) +
			strings.Join(valueStrings, ",")
		if _, err := tx.Exec(insert, valueArgs...); err != nil {
			return wrapPgErr(err)
		}
	}
	return tx.Commit()
}

// scanConnection 从行扫描连接
func scanConnection(scanner interface{ Scan(...any) error }) (*models.RpcConnection, error) {
	var c models.RpcConnection
	var description, url, command, transport, workDir sql.NullString
	var isActive sql.NullBool
	err := scanner.Scan(
		&c.ID, &c.Name, &c.DisplayName, &description, &url, &command,
		&c.Args, &workDir, &c.Env, &transport, &c.Auth, &isActive, &c.CreatedAt, &c.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	c.Description = description.String
	c.URL = url.String
	c.Command = command.String
	c.WorkDir = workDir.String
	c.Transport = transport.String
	c.IsActive = isActive.Bool
	return &c, nil
}

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// wrapPgErr 包装数据库错误为业务错误
func wrapPgErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return sql.ErrNoRows
	}
	return fmt.Errorf("rpc_connections: %w", err)
}
