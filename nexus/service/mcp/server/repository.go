package server

import (
	"database/sql"
	"fmt"

	"flame/nexus/shared/auth"
	"flame/nexus/shared/db"
)

// GetServerConfig 按 auth_type 取本租户 mcp_server_configs 的一行
// 表在租户 schema 内（authInfo.Schema.mcp_server_configs）
func GetServerConfig(authInfo *auth.AuthInfo, authType string) (*McpServerConfig, error) {
	var cfg McpServerConfig
	var isActive sql.NullBool
	err := db.DB.QueryRow(
		fmt.Sprintf(`SELECT id, auth_type, auth_config, is_active, created_at, updated_at FROM %s.mcp_server_configs WHERE auth_type = $1`, authInfo.Schema),
		authType,
	).Scan(&cfg.ID, &cfg.AuthType, &cfg.AuthConfig, &isActive, &cfg.CreatedAt, &cfg.UpdatedAt)
	if err != nil {
		return nil, err
	}
	cfg.IsActive = isActive.Bool
	return &cfg, nil
}

// UpsertServerConfig 写入（不存在则插入，存在则更新）一行认证配置
// 满足 auth_type 唯一约束；auth_config 直接以 jsonb 值（xsql.JSONB 实现 driver.Valuer）写入
func UpsertServerConfig(authInfo *auth.AuthInfo, cfg *McpServerConfig) error {
	_, err := db.DB.Exec(
		fmt.Sprintf(`INSERT INTO %s.mcp_server_configs (id, auth_type, auth_config, is_active, created_at, updated_at)
			VALUES ($1, $2, $3, $4, NOW(), NOW())
			ON CONFLICT (auth_type) DO UPDATE SET auth_config = EXCLUDED.auth_config, is_active = EXCLUDED.is_active, updated_at = NOW()`, authInfo.Schema),
		cfg.NewID(), cfg.AuthType, cfg.AuthConfig, cfg.IsActive,
	)
	return err
}
