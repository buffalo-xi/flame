package plugins

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"flame/nexus/shared/auth"
	"flame/nexus/shared/db"
	"flame/nexus/shared/models"
	"flame/tools/xsql"

	"github.com/google/uuid"
)

// PostgresRepository 基于 PostgreSQL 的插件配置仓储。
type PostgresRepository struct{}

// NewRepository 构造仓储。
func NewRepository() Repository { return &PostgresRepository{} }

var _ Repository = (*PostgresRepository)(nil)

// selectColumns 列顺序须与 scanPlugin 一致。kind 紧跟 id，便于对照唯一键 (kind, name)。
const selectColumns = `id,kind,name,impl,fallbacks,configs,is_active,created_at,updated_at,last_used_at,last_error,last_error_at`

// Get 按 (kind, name) 取一条。
func (r *PostgresRepository) Get(authInfo *auth.AuthInfo, kind, name string) (*models.Plugin, error) {
	q := fmt.Sprintf(`SELECT %s FROM %s.%s WHERE kind=$1 AND name=$2`, selectColumns, authInfo.Schema, tableName)
	p, err := scanPlugin(db.DB.QueryRow(q, kind, name))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, sql.ErrNoRows
		}
		return nil, wrapPgErr(err)
	}
	return p, nil
}

// List 列出本租户的插件配置（全部 kind）。不分 kind 是刻意的：调用方都是一次取全。
func (r *PostgresRepository) List(authInfo *auth.AuthInfo) ([]models.Plugin, error) {
	q := fmt.Sprintf(`SELECT %s FROM %s.%s ORDER BY kind, name`, selectColumns, authInfo.Schema, tableName)
	rows, err := db.DB.Query(q)
	if err != nil {
		return nil, wrapPgErr(err)
	}
	defer rows.Close()

	out := make([]models.Plugin, 0, 8)
	for rows.Next() {
		p, err := scanPlugin(rows)
		if err != nil {
			return nil, wrapPgErr(err)
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// Upsert 按 (kind, name) 插入或更新。落库前把 JSON 列的 nil 归一成 [] / {}，
// 避免显式 NULL 覆盖掉表默认值。
//
// kind 为空直接报错而不是回退成 "tools"：DB 的 DEFAULT 只在不显式传该列时生效，
// 一旦把空串写进去，那一行就再也不会被任何按 kind 过滤的查询读到 —— 插件会
// 「保存成功但凭空消失」，是最难查的一类故障。
func (r *PostgresRepository) Upsert(authInfo *auth.AuthInfo, p *models.Plugin) error {
	if p == nil {
		return fmt.Errorf("plugins: nil plugin")
	}
	kind := strings.TrimSpace(p.Kind)
	if kind == "" {
		return fmt.Errorf("plugins: kind 为空（必须显式指定，见 models.Plugin.Kind）")
	}
	id := p.ID
	if id == "" {
		id = uuid.NewString()
	}
	q := fmt.Sprintf(`INSERT INTO %s.%s (id,kind,name,impl,fallbacks,configs,is_active)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT (kind, name) DO UPDATE SET
			impl=EXCLUDED.impl,
			fallbacks=EXCLUDED.fallbacks,
			configs=EXCLUDED.configs,
			is_active=EXCLUDED.is_active,
			updated_at=CURRENT_TIMESTAMP`, authInfo.Schema, tableName)
	_, err := db.DB.Exec(q, id, kind, p.Name, p.Impl, jsonOr(p.Fallbacks, "[]"), jsonOr(p.Configs, "{}"), p.IsActive)
	return wrapPgErr(err)
}

// SetActive 只改启用状态。
func (r *PostgresRepository) SetActive(authInfo *auth.AuthInfo, kind, name string, active bool) error {
	q := fmt.Sprintf(`UPDATE %s.%s SET is_active=$3, updated_at=CURRENT_TIMESTAMP WHERE kind=$1 AND name=$2`, authInfo.Schema, tableName)
	res, err := db.DB.Exec(q, kind, name, active)
	if err != nil {
		return wrapPgErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// Delete 删除本租户的插件配置。
func (r *PostgresRepository) Delete(authInfo *auth.AuthInfo, kind, name string) error {
	q := fmt.Sprintf(`DELETE FROM %s.%s WHERE kind=$1 AND name=$2`, authInfo.Schema, tableName)
	res, err := db.DB.Exec(q, kind, name)
	if err != nil {
		return wrapPgErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// maxLastErrorRunes last_error 的落库上限（按字符截断，避免把多字节字符切坏）。
// 够排障即可，不该把整段栈塞进一行。
const maxLastErrorRunes = 200

// TouchUsed 记一次成功调用，并清掉上一次错误。
//
// 刻意不碰 updated_at：那一列表示「配置什么时候改过」，
// 被调用频率污染之后就没法判断配置是否过期了。
func (r *PostgresRepository) TouchUsed(authInfo *auth.AuthInfo, kind, name string) error {
	q := fmt.Sprintf(`UPDATE %s.%s
		SET last_used_at=CURRENT_TIMESTAMP, last_error='', last_error_at=NULL
		WHERE kind=$1 AND name=$2`, authInfo.Schema, tableName)
	// 行可能已经被删：可观测信息写不进去不算调用失败
	_, err := db.DB.Exec(q, kind, name)
	return ignoreNoRows(err)
}

// SetError 记一次失败（消息截断后落库）。
func (r *PostgresRepository) SetError(authInfo *auth.AuthInfo, kind, name, message string) error {
	q := fmt.Sprintf(`UPDATE %s.%s
		SET last_error=$3, last_error_at=CURRENT_TIMESTAMP
		WHERE kind=$1 AND name=$2`, authInfo.Schema, tableName)
	_, err := db.DB.Exec(q, kind, name, truncateRunes(message, maxLastErrorRunes))
	return ignoreNoRows(err)
}

func ignoreNoRows(err error) error {
	if err == nil {
		return nil
	}
	return wrapPgErr(err)
}

func truncateRunes(s string, n int) string {
	rs := []rune(s)
	if len(rs) <= n {
		return s
	}
	return string(rs[:n]) + "…"
}

func scanPlugin(scanner interface{ Scan(...any) error }) (*models.Plugin, error) {
	var p models.Plugin
	var isActive sql.NullBool
	var lastUsed, lastErrAt sql.NullTime
	var lastErr sql.NullString
	err := scanner.Scan(&p.ID, &p.Kind, &p.Name, &p.Impl, &p.Fallbacks, &p.Configs, &isActive,
		&p.CreatedAt, &p.UpdatedAt, &lastUsed, &lastErr, &lastErrAt)
	if err != nil {
		return nil, err
	}
	p.IsActive = isActive.Bool
	if lastUsed.Valid {
		t := lastUsed.Time
		p.LastUsedAt = &t
	}
	if lastErrAt.Valid {
		t := lastErrAt.Time
		p.LastErrorAt = &t
	}
	p.LastError = lastErr.String
	return &p, nil
}

// jsonOr 保留已有 JSON；空值用 fallback 字面量兜底（如 "[]" / "{}"）。
func jsonOr(j xsql.JSONB, fallback string) any {
	v, err := j.Value()
	if err != nil || v == nil {
		return json.RawMessage(fallback)
	}
	return v
}

func wrapPgErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return sql.ErrNoRows
	}
	return fmt.Errorf("plugins: %w", err)
}
