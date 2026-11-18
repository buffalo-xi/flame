-- ============================================================================
-- 会话配额：空间上限 + 会话数上限
--
-- 位置：平台 schema（config.database.platform_schema，默认 platform）
-- 执行：psql -h <host> -p <port> -U <user> -d <db> -f nexus/platform/sql/quota_storage.sql
--
-- 语义完全沿用 quota_configs 既有约定：
--   tenant_id = ''  系统行（所有租户的硬顶）
--   tenant_id = 'x' 租户行（NULL = 回退系统行）
--   NULL = 未设置（逐级回退）、-1 = 不限制、正数 = 上限
--
-- 两个字段：
--   max_session_storage_bytes 单个会话工作区（users/{uid}/sessions/{sid}/）的字节上限
--   max_user_sessions         单个用户拥有的会话数量上限（超限拒绝新建，不删旧会话）
--
-- 幂等：可重复执行。
-- ============================================================================

ALTER TABLE platform.quota_configs
    ADD COLUMN IF NOT EXISTS max_session_storage_bytes bigint NULL;

ALTER TABLE platform.quota_configs
    ADD COLUMN IF NOT EXISTS max_user_sessions int NULL;

-- ----------------------------------------------------------------------------
-- 会话用量计数表（租户 schema 内，每个租户一份）
--
-- 只在「没有 Redis」的部署里作为计数后端使用（见 shared/quota/usage.go 的 PGStore）：
-- 有 Redis 时计数在 Redis，本表不写。
-- 放在租户 schema 里是为了随租户 DROP SCHEMA 一起消失，不需要额外清理逻辑。
--
-- 老租户补表：把 %I 换成租户 schema 逐个执行（当前库只有一个租户 t0）：
--
--   CREATE TABLE IF NOT EXISTS t0.session_usage (
--       scope_key   text PRIMARY KEY,   -- 工作场地路径：tenants/{schema}/users/{uid}/sessions/{sid}
--       used_bytes  bigint NOT NULL DEFAULT 0,
--       verified_at timestamptz NOT NULL DEFAULT now()
--   );
--
-- scope_key 用文本而不是 uuid：session 与将来的 run 场地共用同一张表。
--
-- 单用户配额覆盖（老租户同样按 schema 逐个执行）：
--
--   CREATE TABLE IF NOT EXISTS t0.user_quotas (
--       user_id                   uuid PRIMARY KEY REFERENCES t0.users(id) ON DELETE CASCADE,
--       max_sessions              int    NULL,   -- NULL = 跟随租户 max_user_sessions
--       max_session_storage_bytes bigint NULL,   -- NULL = 跟随租户 max_session_storage_bytes
--       updated_by                uuid   NULL,
--       updated_at                timestamptz NOT NULL DEFAULT now()
--   );
-- ----------------------------------------------------------------------------

-- ============================================================================
-- 执行后核对
-- ============================================================================
-- a) 两个列是否就位
--   SELECT column_name, data_type FROM information_schema.columns
--    WHERE table_schema = 'platform' AND table_name = 'quota_configs'
--    ORDER BY ordinal_position;
--
-- b) 系统行与租户行
--   SELECT * FROM platform.quota_configs ORDER BY tenant_id;
-- ============================================================================
