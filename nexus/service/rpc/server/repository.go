package server

import (
	"database/sql"
	"fmt"

	"flame/nexus/shared/auth"
	"flame/nexus/shared/db"
)

// GetServerConfig reads one rpc_server_configs row of this tenant by auth_type.
// The table lives inside the tenant schema (authInfo.Schema.rpc_server_configs).
func GetServerConfig(authInfo *auth.AuthInfo, authType string) (*RpcServerConfig, error) {
	var cfg RpcServerConfig
	var isActive sql.NullBool
	err := db.DB.QueryRow(
		fmt.Sprintf(`SELECT id, auth_type, auth_config, is_active, created_at, updated_at FROM %s.rpc_server_configs WHERE auth_type = $1`, authInfo.Schema),
		authType,
	).Scan(&cfg.ID, &cfg.AuthType, &cfg.AuthConfig, &isActive, &cfg.CreatedAt, &cfg.UpdatedAt)
	if err != nil {
		return nil, err
	}
	cfg.IsActive = isActive.Bool
	return &cfg, nil
}

// UpsertServerConfig writes one auth config row (insert when absent, update when present).
// It satisfies the unique constraint on auth_type; auth_config is written straight as a jsonb value
// (xsql.JSONB implements driver.Valuer).
func UpsertServerConfig(authInfo *auth.AuthInfo, cfg *RpcServerConfig) error {
	_, err := db.DB.Exec(
		fmt.Sprintf(`INSERT INTO %s.rpc_server_configs (id, auth_type, auth_config, is_active, created_at, updated_at)
			VALUES ($1, $2, $3, $4, NOW(), NOW())
			ON CONFLICT (auth_type) DO UPDATE SET auth_config = EXCLUDED.auth_config, is_active = EXCLUDED.is_active, updated_at = NOW()`, authInfo.Schema),
		cfg.NewID(), cfg.AuthType, cfg.AuthConfig, cfg.IsActive,
	)
	return err
}
