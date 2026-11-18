-- ============================================================================
-- tenant_plugins：平台侧「租户 × 插件」停用记录
-- ----------------------------------------------------------------------------
-- 与 {schema}.plugins 的分工（两者都要过，缺一不可）：
--
--   本表（platform schema）  平台对某个租户**停用了没有** —— 平台写
--   {schema}.plugins         用哪个实现、key 是多少、是否启用 —— 租户写
--
-- 语义：**缺省开放**（2026-11-02 起）
--   没有行                   可以用：租户在插件页启用 + 选实现即可
--   有行且 is_allowed=false  **对本租户停用**（不影响其它租户）
--   有行且 is_allowed=true   等同缺省，冗余行（迁移脚本会清掉）
--
-- 全局急停在 platform.plugin_gates：缺省放行，一旦拦下压过本表。
--
-- 为什么缺省开放（产品决定）：插件本体是平台放进磁盘的可信代码，
-- 而凭证与成本都在租户侧（{schema}.plugins.configs 里是租户自己的 key），
-- 逐个租户开通只是运营负担。要收紧时仍能单租户停用，或用全局急停一键止血。
--
--   kind               插件类型（当前恒为 tools；与 {schema}.plugins.kind 同口径）
--
-- 主键含 kind：不同种类的插件是**独立命名空间**，同名（tools/web_search 与将来的
-- sources/web_search）不能互相覆盖停用状态。当前只有 tools，故存量行回填 'tools'。
--
-- 2026-10-21 移除了「系统默认行」（tenant_id = ''）：它只是个隐式缺省值，
-- 与「全局停用」（platform.plugin_gates）是两种完全不同的东西 —— 前者改缺省，
-- 后者压过一切。混在一张表里会让管理员分不清「我开的」和「本来就没有」，
-- 也会误以为它是总闸。现在库里用 CHECK (tenant_id <> '') 封死。
--
-- 历史：本表曾带 daily_limit / concurrency_limit 两列，但计数只在进程内
-- （重启清零、多副本各算各的），「每天最多 N 次」名不副实，且全库无人配置。
-- 已移除；将来要做真正的配额，应基于共享存储（Redis）重新设计，而不是恢复这两列。
--
-- 位置：管理 schema（config.database.platform_schema，默认 platform）
-- 执行：psql -h <host> -p <port> -U <user> -d <db> -f nexus/platform/sql/tenant_plugins.sql
-- 幂等：可重复执行。
-- ============================================================================

CREATE TABLE IF NOT EXISTS platform.tenant_plugins (
    tenant_id          varchar(32) NOT NULL,             -- 租户 schema（CHECK 已封死空串）
    kind               varchar(16) NOT NULL DEFAULT 'tools',  -- 插件类型（当前恒为 tools）
    plugin_name        varchar(64) NOT NULL,             -- 插件名（= plugins/<kind>/<name>）
    is_allowed         boolean     NOT NULL DEFAULT false,  -- 行只表示停用：手写 INSERT 时默认值落在「停用」这侧更安全
    updated_at         timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, kind, plugin_name)
);

-- 老库升级（CREATE TABLE IF NOT EXISTS 对已存在的表不生效，只能单独补）
ALTER TABLE platform.tenant_plugins ADD COLUMN IF NOT EXISTS kind varchar(16) NOT NULL DEFAULT 'tools';

-- 主键从 (tenant_id, plugin_name) 换成 (tenant_id, kind, plugin_name)。
-- PostgreSQL 不支持原地改主键列集，只能拆了重建；靠「主键列数」判断是否已经换过，
-- 比比对 pg_get_constraintdef 的字符串更稳（不受格式化差异影响）。
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint c
          JOIN pg_class t ON t.oid = c.conrelid
         WHERE t.relname = 'tenant_plugins' AND c.contype = 'p' AND array_length(c.conkey, 1) = 3
    ) THEN
        ALTER TABLE platform.tenant_plugins DROP CONSTRAINT IF EXISTS tenant_plugins_pkey;
        ALTER TABLE platform.tenant_plugins
            ADD CONSTRAINT tenant_plugins_pkey PRIMARY KEY (tenant_id, kind, plugin_name);
    END IF;
END $$;

-- 移除已废弃的配额列。
-- DROP COLUMN 会连带删掉依赖它的 CHECK 约束，但显式先删约束更清楚，
-- 也让重复执行时的行为可预期。
ALTER TABLE platform.tenant_plugins DROP CONSTRAINT IF EXISTS tenant_plugins_daily_check;
ALTER TABLE platform.tenant_plugins DROP CONSTRAINT IF EXISTS tenant_plugins_concurrency_check;
ALTER TABLE platform.tenant_plugins DROP COLUMN IF EXISTS daily_limit;
ALTER TABLE platform.tenant_plugins DROP COLUMN IF EXISTS concurrency_limit;

-- 按插件名反查（全部租户）用得上；主键已覆盖「某租户某类型某插件」。
CREATE INDEX IF NOT EXISTS tenant_plugins_name_idx ON platform.tenant_plugins (plugin_name);

-- ---------------------------------------------------------------------------
-- 2026-10-21：去掉「系统默认行」（tenant_id = ''）
-- ---------------------------------------------------------------------------
-- 现在简化为一层：生效 = 有租户行 ? 该行 : 未开通。
-- 幂等：可重复执行。
-- ---------------------------------------------------------------------------

-- 1) 把「靠系统行开着」的租户物化成租户行 —— 否则删掉系统行后它们会集体失效
INSERT INTO platform.tenant_plugins (tenant_id, kind, plugin_name, is_allowed, updated_at)
SELECT t.schema_name, s.kind, s.plugin_name, s.is_allowed, now()
  FROM platform.tenants t
 CROSS JOIN platform.tenant_plugins s
 WHERE s.tenant_id = '' AND s.is_allowed
ON CONFLICT (tenant_id, kind, plugin_name) DO NOTHING;

-- 2) 删掉全部系统行：is_allowed=false 的那些本来就等于「未开通」，不需物化
DELETE FROM platform.tenant_plugins WHERE tenant_id = '';

-- 3) 库层封死：不允许再出现空 tenant_id，否则「继承」会悄悄回来
ALTER TABLE platform.tenant_plugins
    DROP CONSTRAINT IF EXISTS tenant_plugins_tenant_not_empty;
ALTER TABLE platform.tenant_plugins
    ADD CONSTRAINT tenant_plugins_tenant_not_empty CHECK (tenant_id <> '');

-- ---------------------------------------------------------------------------
-- 2026-11-02：缺省从「拒绝」翻成「开放」，本表变成「停用记录」
-- ---------------------------------------------------------------------------
-- 没有行 = 可以用；有行且 is_allowed=false = 对本租户停用。
-- is_allowed=true 的行从此等同缺省、不再有任何作用，删掉让界面只剩「停用」。
-- is_allowed=false 的行保留：它们原先表示「显式不开通」，新语义下就是「停用」，
-- 行为不变（仍然是不可用），不需要清理。
-- 幂等：可重复执行。
-- ---------------------------------------------------------------------------
DELETE FROM platform.tenant_plugins WHERE tenant_id <> '' AND is_allowed;

-- 验证
SELECT tenant_id, kind, plugin_name, is_allowed
  FROM platform.tenant_plugins
 ORDER BY tenant_id, kind, plugin_name
 LIMIT 20;
