-- ============================================================================
-- 跨租户资源分发：已上架资产 + 可见范围 + 安装记录
--
-- 位置：管理 schema（config.database.platform_schema，默认 platform）
-- 执行：psql -h <host> -p <port> -U <user> -d <db> -f nexus/platform/sql/shared_assets.sql
-- 前置：必须先执行 nexus/platform/sql/platform.sql（本文件外键引用 platform.tenants）
--
-- 口径（与租户侧完全独立）：
--   * 这里是「平台审核 + 复制式分发」的存储层：一行 shared_assets 就是一个已上架的
--     快照。安装到目标租户后，实体归目标租户所有，与快照再无关系（不做跨租户引用）。
--   * 文件内容**不进库**：快照文件落 {root_dir}/platform-assets/<asset_id>/<build>/，
--     manifest.files 只放清单（path / size / sha256）。
--   * payload 是实体行快照，**已剥离敏感值**（custom_tools.configs 清空、agents.config
--     里疑似凭据的键置空）；安装端负责再生成新 uuid 与授权行。
--   * 上架人是租户侧账号（{schema}.users），只存 id + 展示名快照；不回查 users，
--     否则用户改名/停用会让历史资产漂移。平台审核人另记 reviewed_by（platform.users）。
--   * JSON 用 schema，SQL 列才是 schema_name —— 与 tenantView / createTenantRequest 同口径。
--
-- 幂等：可重复执行。
-- 旧库升级：先跑 nexus/platform/sql/asset_listing_rename.sql（published/offline → listed/delisted）。
-- 注意：约束写在建表语句内（Postgres 无 ADD CONSTRAINT IF NOT EXISTS）；
--       若将来需要给已存在的表补约束，参照 nexus/sql/tenant_user_roles.sql 的探测写法。
-- ============================================================================

-- ---------------------------------------------------------------------------
-- 1) 已上架资产（一个单元 = skill | custom_tool | agent | workflow）
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS platform.shared_assets (
    id                uuid         PRIMARY KEY,
    kind              varchar(16)  NOT NULL,                  -- skill | custom_tool | agent | workflow
    name              varchar(128) NOT NULL,                  -- 源实体名（安装时可改名）
    display_name      varchar(128) NOT NULL DEFAULT '',
    description       text         NOT NULL DEFAULT '',
    source_schema     varchar(32)  NOT NULL,                  -- 上架租户（= schema）
    source_id         uuid         NOT NULL,                  -- 源实体 id（仅溯源，不构成引用）
    build             integer      NOT NULL DEFAULT 1,
    status            varchar(16)  NOT NULL DEFAULT 'draft',  -- draft|pending|listed|rejected|delisted
    visibility        varchar(16)  NOT NULL DEFAULT 'all',    -- all | allowlist
    manifest          jsonb        NOT NULL DEFAULT '{}',     -- 依赖声明 + 文件清单（不含内容）
    payload           jsonb        NOT NULL DEFAULT '{}',     -- 实体行快照（已剥离敏感值）
    listed_by         uuid         NULL,                      -- 上架人（{schema}.users.id，仅审计）
    listed_by_name    varchar(64)  NOT NULL DEFAULT '',       -- 上架人展示名快照
    reviewed_by       uuid         NULL REFERENCES platform.users(id),
    reviewed_at       timestamptz  NULL,
    review_note       text         NOT NULL DEFAULT '',
    created_at        timestamptz  NOT NULL DEFAULT now(),
    updated_at        timestamptz  NOT NULL DEFAULT now(),
    CONSTRAINT shared_assets_kind_check       CHECK (kind IN ('skill','custom_tool','agent','workflow')),
    CONSTRAINT shared_assets_status_check     CHECK (status IN ('draft','pending','listed','rejected','delisted')),
    CONSTRAINT shared_assets_visibility_check CHECK (visibility IN ('all','allowlist')),
    -- source_schema 会被拼进 SQL 标识符与存储路径，格式先收一道（与 platform.tenants 同规则）
    CONSTRAINT shared_assets_source_check     CHECK (source_schema ~ '^[a-z][a-z0-9_]{1,31}$')
);

-- 同一租户的同类同名资产只有一行：重新上架 = 覆盖 + build+1（新快照写新目录，旧目录保留）
CREATE UNIQUE INDEX IF NOT EXISTS shared_assets_line_idx
    ON platform.shared_assets (kind, source_schema, lower(name));

-- 审核列表按状态 + 类型翻页
CREATE INDEX IF NOT EXISTS shared_assets_status_idx ON platform.shared_assets (status, kind);

-- ---------------------------------------------------------------------------
-- 2) 可见范围白名单
--
-- 仅 visibility = 'allowlist' 时读；**有行 = 允许**（与 tenant_plugins「没有行 = 开放」
-- 的写法刻意相反：这里的缺省是「只有上架方与审核通过的白名单能装」）。
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS platform.shared_asset_tenants (
    asset_id    uuid        NOT NULL REFERENCES platform.shared_assets(id) ON DELETE CASCADE,
    schema_name varchar(32) NOT NULL,
    PRIMARY KEY (asset_id, schema_name)
);

-- ---------------------------------------------------------------------------
-- 3) 安装记录
--
-- 用途：① 前端标「已安装」；② P1 比较 build 提示「上游有新的快照」。
-- installed_name 可能与资产名不同（冲突时用户改名），installed_id 是安装后的实体 id。
-- 不建到各租户表的外键（跨 schema，且实体删除后记录应保留为审计线索）。
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS platform.shared_asset_installs (
    asset_id       uuid         NOT NULL REFERENCES platform.shared_assets(id) ON DELETE CASCADE,
    schema_name    varchar(32)  NOT NULL REFERENCES platform.tenants(schema_name),
    build          integer      NOT NULL,
    installed_name varchar(128) NOT NULL,
    installed_id   uuid         NOT NULL,
    installed_by       uuid        NULL,                    -- 安装人（{schema}.users.id）
    installed_by_name  varchar(64) NOT NULL DEFAULT '',     -- 安装人展示名快照
    installed_at   timestamptz  NOT NULL DEFAULT now(),
    PRIMARY KEY (asset_id, schema_name, installed_id)
);

CREATE INDEX IF NOT EXISTS shared_asset_installs_tenant_idx
    ON platform.shared_asset_installs (schema_name, installed_at DESC);

-- ---------------------------------------------------------------------------
-- 4) 跨服务器导入：来源维度（2026-11-02，可重复执行）
--
--   手工「导出资产包 / 导入资产包」时，导入行必须能说清「从哪台机器来的、对方叫什么 id」：
--   否则本机的 t0 与远端的 t0 会在唯一索引上互相抢占，也没法按 (来源, 资产, 版本) 去重。
--   本机上架的行缺省值（origin_server = ''、origin_asset_id = NULL），语义仍是「本地出品」。
-- ---------------------------------------------------------------------------
ALTER TABLE platform.shared_assets
    ADD COLUMN IF NOT EXISTS origin_server   varchar(64) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS origin_asset_id uuid        NULL,
    ADD COLUMN IF NOT EXISTS imported_at     timestamptz NULL,
    ADD COLUMN IF NOT EXISTS imported_by     uuid        NULL;

-- 唯一键升级：本机上架行的 origin_server='' 仍然按 (kind, schema, lower(name)) 唯一；
-- 导入行按来源站点分区，多台机器各自的 t0 同名技能可以并存。
DROP INDEX IF EXISTS platform.shared_assets_line_idx;
CREATE UNIQUE INDEX IF NOT EXISTS shared_assets_line_idx
    ON platform.shared_assets (origin_server, kind, source_schema, lower(name));

-- ============================================================================
-- 5) 工作流资产（2026-11-02，可重复执行）
--
--    工作流的本体是 {schema}.flows 里的一行 JSONB，盘上没有目录；上架时整行序列化成
--    FLOW.json 当入口文件，走与其它三类同一条文件管线。这里只是把 kind 的白名单放开。
--    已建过表的库必须跑这一段（CREATE TABLE IF NOT EXISTS 不会更新既有约束）。
-- ============================================================================
ALTER TABLE platform.shared_assets DROP CONSTRAINT IF EXISTS shared_assets_kind_check;
ALTER TABLE platform.shared_assets
    ADD CONSTRAINT shared_assets_kind_check
    CHECK (kind IN ('skill','custom_tool','agent','workflow'));

-- ============================================================================
-- 执行后核对
-- ============================================================================
-- a) 三张表与约束是否就位
--   \d platform.shared_assets
--   \d platform.shared_asset_tenants
--   \d platform.shared_asset_installs
--
-- b) 待审队列
--   SELECT id, kind, name, source_schema, listed_by_name, created_at
--     FROM platform.shared_assets WHERE status = 'pending' ORDER BY created_at;
--
-- c) 已上架但快照为空（异常行，应当为空）
--   SELECT id, kind, name FROM platform.shared_assets
--    WHERE status = 'listed' AND (payload = '{}'::jsonb OR NOT (manifest ? 'files'));
--
-- d) 引用了不存在租户的安装记录（应当为空）
--   SELECT i.* FROM platform.shared_asset_installs i
--     LEFT JOIN platform.tenants t ON t.schema_name = i.schema_name
--    WHERE t.schema_name IS NULL;
--
-- e) allowlist 资产的可见范围（visibility='allowlist' 却没有白名单行 = 谁都装不了）
--   SELECT a.id, a.name,
--          (SELECT count(*) FROM platform.shared_asset_tenants w WHERE w.asset_id = a.id) AS allow_cnt
--     FROM platform.shared_assets a
--    WHERE a.visibility = 'allowlist' AND a.status = 'listed';
