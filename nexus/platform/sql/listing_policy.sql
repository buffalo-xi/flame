-- ============================================================================
-- 上架审核策略：租户上架资产到平台市场，要不要先过平台审核
--
-- 形状与 capability_gates / plugin_gates 一致（两级），但缺省相反：**上架免审**，
-- 需要把关的租户显式写行。上架审核对租户是日常摩擦，且入口已被「上架方主动上架 +
-- 安装方自行决定装不装」筛过一遍。
--   1) platform.listing_settings  全局默认（单行 id=1，没有行 = 免审）
--   2) platform.listing_policies  按租户覆盖（有行以行为准）
--
-- 为什么放库而不是 config.yaml：这是治理策略，平台管理员在管理端就能改；
-- 写进配置文件意味着每次调整都要登机器改文件 + 重启。
--
-- 可重复执行。
-- 旧库升级：先跑 nexus/platform/sql/asset_listing_rename.sql（把 publish_* 改成 listing_*）。
-- ============================================================================

CREATE TABLE IF NOT EXISTS platform.listing_settings (
    id             smallint    PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    require_review boolean     NOT NULL DEFAULT false, -- true = 上架进 pending 等审核
    updated_at     timestamptz NOT NULL DEFAULT now(),
    updated_by     uuid        NULL REFERENCES platform.users(id)
);

CREATE TABLE IF NOT EXISTS platform.listing_policies (
    schema_name    varchar(32) PRIMARY KEY
                   REFERENCES platform.tenants(schema_name) ON DELETE CASCADE,
    require_review boolean     NOT NULL,               -- 该租户是否必须审核
    updated_at     timestamptz NOT NULL DEFAULT now(),
    updated_by     uuid        NULL REFERENCES platform.users(id)
);

CREATE INDEX IF NOT EXISTS listing_policies_review_idx
    ON platform.listing_policies (require_review);

-- 缺省改为免审（2026-11-03）：列默认值与代码缺省对齐。已建过表的库必须跑这一句
-- （CREATE TABLE IF NOT EXISTS 不会更新既有列默认）。不改已有行的值。
ALTER TABLE platform.listing_settings ALTER COLUMN require_review SET DEFAULT false;

-- ----------------------------------------------------------------------------
-- 核对
-- ----------------------------------------------------------------------------
-- a) 全局默认（没有行 = false，即免审）
--   SELECT require_review, updated_at FROM platform.listing_settings WHERE id = 1;
-- b) 覆盖了哪些租户
--   SELECT schema_name, require_review, updated_at FROM platform.listing_policies
--    ORDER BY schema_name;
