package mcpconnections

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

// PostgresRepository is the PostgreSQL implementation of MCP connection storage.
type PostgresRepository struct{}

// NewRepository builds the repository.
func NewRepository() Repository {
	return &PostgresRepository{}
}

// compile-time interface assertion
var _ Repository = (*PostgresRepository)(nil)

// Create creates a connection and returns the new record id.
func (r *PostgresRepository) Create(authInfo *auth.AuthInfo, conn *models.McpConnection) (string, error) {
	id := conn.ID
	if id == "" {
		id = uuid.NewString()
	}
	query := fmt.Sprintf(`INSERT INTO %s.mcp_connections
		(id, name, display_name, description, url, command, args, work_dir, env, transport, auth, is_active)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12) RETURNING id`, authInfo.Schema)
	var returnedID string
	err := db.DB.QueryRow(query,
		id, conn.Name, nullString(conn.DisplayName), nullString(conn.Description), nullString(conn.URL),
		nullString(conn.Command), conn.Args, nullString(conn.WorkDir), conn.Env, conn.Transport, conn.Auth, conn.IsActive,
	).Scan(&returnedID)
	if err != nil {
		return "", wrapPgErr(err)
	}
	return returnedID, nil
}

// Update updates a connection by id (every field except id and is_active; an enabled connection cannot be
// modified).
func (r *PostgresRepository) Update(authInfo *auth.AuthInfo, id string, conn *models.McpConnection) error {
	// Read the stored is_active first: an enabled connection cannot be modified
	var isActive bool
	checkQuery := fmt.Sprintf(`SELECT is_active FROM %s.mcp_connections WHERE id=$1`, authInfo.Schema)
	if err := db.DB.QueryRow(checkQuery, id).Scan(&isActive); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return sql.ErrNoRows
		}
		return wrapPgErr(err)
	}
	if isActive {
		return fmt.Errorf("an enabled MCP connection cannot be modified")
	}

	query := fmt.Sprintf(`UPDATE %s.mcp_connections SET
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

// UpdateIsActive updates the enabled state.
func (r *PostgresRepository) UpdateIsActive(authInfo *auth.AuthInfo, id string, isActive bool) error {
	query := fmt.Sprintf(`UPDATE %s.mcp_connections SET is_active=$2, updated_at=CURRENT_TIMESTAMP WHERE id=$1`, authInfo.Schema)
	_, err := db.DB.Exec(query, id, isActive)
	return wrapPgErr(err)
}

// Delete removes a connection (its tools cascade).
func (r *PostgresRepository) Delete(authInfo *auth.AuthInfo, id string) error {
	query := fmt.Sprintf(`DELETE FROM %s.mcp_connections WHERE id=$1`, authInfo.Schema)
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

// GetByID fetches a connection by id.
func (r *PostgresRepository) GetByID(authInfo *auth.AuthInfo, id string) (*models.McpConnection, error) {
	query := fmt.Sprintf(`SELECT id,name,display_name,description,url,command,args,work_dir,env,transport,auth,is_active,created_at,updated_at
		FROM %s.mcp_connections WHERE id=$1`, authInfo.Schema)
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

// List lists connections (paged and filtered).
func (r *PostgresRepository) List(authInfo *auth.AuthInfo, req *ListMcpConnectionsRequest) ([]models.McpConnection, int, error) {
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

	// Count
	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM %s.mcp_connections", authInfo.Schema) + whereClause
	var total int
	if err := db.DB.QueryRow(countQuery, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	// Page
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
		FROM %s.mcp_connections`, authInfo.Schema) + whereClause +
		fmt.Sprintf(" ORDER BY created_at DESC LIMIT $%d OFFSET $%d", argIndex, argIndex+1)

	rows, err := db.DB.Query(listQuery, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []models.McpConnection
	for rows.Next() {
		conn, err := scanConnection(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *conn)
	}
	return out, total, rows.Err()
}

// ListTools lists the persisted tools of one connection.
func (r *PostgresRepository) ListTools(authInfo *auth.AuthInfo, connectionID string) ([]models.McpConnectionTool, error) {
	query := fmt.Sprintf(`SELECT id,connection_id,name,description,input_schema,updated_at
		FROM %s.mcp_connection_tools WHERE connection_id=$1 ORDER BY name`, authInfo.Schema)
	rows, err := db.DB.Query(query, connectionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.McpConnectionTool
	for rows.Next() {
		var t models.McpConnectionTool
		if err := rows.Scan(&t.ID, &t.ConnectionID, &t.Name, &t.Description, &t.InputSchema, &t.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ReplaceTools replaces a connection's tools wholesale: delete the old ones, then insert the new ones in bulk.
func (r *PostgresRepository) ReplaceTools(authInfo *auth.AuthInfo, connectionID string, tools []*models.McpConnectionTool) error {
	tx, err := db.DB.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(fmt.Sprintf(`DELETE FROM %s.mcp_connection_tools WHERE connection_id=$1`, authInfo.Schema), connectionID); err != nil {
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
		insert := fmt.Sprintf(`INSERT INTO %s.mcp_connection_tools
			(id, connection_id, name, description, input_schema, updated_at) VALUES `, authInfo.Schema) +
			strings.Join(valueStrings, ",")
		if _, err := tx.Exec(insert, valueArgs...); err != nil {
			return wrapPgErr(err)
		}
	}
	return tx.Commit()
}

// scanConnection scans a connection out of a row.
func scanConnection(scanner interface{ Scan(...any) error }) (*models.McpConnection, error) {
	var c models.McpConnection
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

// wrapPgErr wraps a database error as a business error.
func wrapPgErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return sql.ErrNoRows
	}
	return fmt.Errorf("mcp_connections: %w", err)
}
