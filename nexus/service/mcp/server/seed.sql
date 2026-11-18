-- ============================================================================
-- MCP Server 认证配置 seed 脚本
-- ----------------------------------------------------------------------------
-- 作用：为每个租户 schema 预置 mcp_server_configs 的两行示例配置
--       （apikey / oauth），用户只需把 is_active 改为 true 并填入真实配置即可，
--        无需自己手动建行。
--
-- 用法：把下面所有的 <schema> 替换成实际租户 schema 名（如 t_abc123），
--       然后在本库执行本文件。可对每个租户重复执行（幂等，已建的行不会重复插入）。
--
-- 说明：
--   * 两行默认 is_active = false，避免在填好真实凭据前就对外放行导致认证失败。
--   * apikey 行带一个占位 client（secret 需自行替换），oauth 行给空配置骨架。
--   * 启用后修改配置请调用管理接口（UpsertServerConfig），或直接 UPDATE 本表。
-- ============================================================================

-- 1) 建表（如尚未创建，幂等）
-- id 不再有数据库默认值：由程序（服务端 UpsertServerConfig 的 cfg.NewID()）或本脚本显式提供
CREATE TABLE IF NOT EXISTS <schema>.mcp_server_configs (
    id uuid NOT NULL,
    auth_type varchar(20) NOT NULL,
    auth_config jsonb DEFAULT '{}'::jsonb NULL,
    is_active bool DEFAULT true NULL,
    created_at timestamptz DEFAULT now() NULL,
    updated_at timestamptz DEFAULT now() NULL,
    CONSTRAINT mcp_server_configs_check CHECK (auth_type IN ('apikey','oauth')),
    CONSTRAINT mcp_server_configs_pk PRIMARY KEY (id),
    CONSTRAINT mcp_server_configs_unique UNIQUE (auth_type)
);

-- 2) 预置 apikey 行（默认禁用，secret 为占位符，请替换为真实值）
-- gen_random_uuid() 是 PG 13+ 内置函数，不依赖 uuid-ossp；ON CONFLICT 命中时不会真正取值
INSERT INTO <schema>.mcp_server_configs (id, auth_type, auth_config, is_active)
VALUES (
    gen_random_uuid(),
    'apikey',
    '{"kind":"raw","clients":[{"client_id":"default","secret":"CHANGE_ME_apikey_secret","is_active":true}]}',
    false
)
ON CONFLICT (auth_type) DO NOTHING;

-- 3) 预置 oauth 行（默认禁用，填入 IdP 信息后启用）
INSERT INTO <schema>.mcp_server_configs (id, auth_type, auth_config, is_active)
VALUES (
    gen_random_uuid(),
    'oauth',
    '{"issuer":"","authorize_endpoint":"","jwks_url":"","audience":"","required_scope":""}',
    false
)
ON CONFLICT (auth_type) DO NOTHING;
