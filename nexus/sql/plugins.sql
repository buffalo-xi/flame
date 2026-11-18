-- ============================================================================
-- plugins：租户级插件配置（磁盘 Python 插件的「启用 + 实现 + 配置」）
-- ----------------------------------------------------------------------------
-- 插件本体在磁盘（{root_dir}/plugins/<kind>/<name>/...），本表只记：
--   kind      插件类型（当前恒为 tools；将来 sources 等新类型沿用本表）
--   name      插件名（= 目录名 = 授权 ref 的 subject），如 web_search
--   impl      主实现名（tavily / searxng / bing_rss ...）
--   fallbacks 兜底链（有序 JSON 数组）：主实现失败时按顺序再试
--   configs   按实现分键存的配置：{"tavily":{"api_key":"..."}} —— 切换主实现不丢另一家的 key
--
-- 唯一键是 (kind, name) 而不是 (name)：种类不同可以有同名插件，各自是独立命名空间，
-- 工具侧的 model 名字并不会因此含糊（插件执行器只处理 kind=tools）。
--
-- 失败方向：没有行 = 未启用（工具出现在目录里但 available=false）。
-- 幂等：可重复执行；自动遍历所有含 agents 表的租户 schema。
-- ============================================================================
DO $$
DECLARE
    s text;
BEGIN
    FOR s IN
        SELECT table_schema
          FROM information_schema.tables
         WHERE table_name = 'agents'
           AND table_schema NOT IN ('pg_catalog', 'information_schema', 'public', 'platform')
         ORDER BY table_schema
    LOOP
        EXECUTE format($ddl$
            CREATE TABLE IF NOT EXISTS %I.plugins (
                id uuid NOT NULL,
                kind character varying(16) DEFAULT 'tools'::character varying NOT NULL,
                name character varying(64) NOT NULL,
                impl character varying(64) DEFAULT ''::character varying NOT NULL,
                fallbacks jsonb DEFAULT '[]'::jsonb,
                configs jsonb DEFAULT '{}'::jsonb,
                is_active boolean DEFAULT false NOT NULL,
                created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
                updated_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
                last_used_at timestamp with time zone,
                last_error text DEFAULT ''::text NOT NULL,
                last_error_at timestamp with time zone
            )$ddl$, s);

        -- 对已存在的老表补列（ADD COLUMN IF NOT EXISTS 幂等）
        -- kind 的存量行按 tools 回填：这张表在加列之前只装过工具插件。
        EXECUTE format('ALTER TABLE %I.plugins ADD COLUMN IF NOT EXISTS kind varchar(16) NOT NULL DEFAULT %L', s, 'tools');
        EXECUTE format('ALTER TABLE %I.plugins ADD COLUMN IF NOT EXISTS last_used_at timestamptz', s);
        EXECUTE format('ALTER TABLE %I.plugins ADD COLUMN IF NOT EXISTS last_error text NOT NULL DEFAULT %L', s, '');
        EXECUTE format('ALTER TABLE %I.plugins ADD COLUMN IF NOT EXISTS last_error_at timestamptz', s);

        -- 约束用 IF NOT EXISTS 语义（PostgreSQL 的 ADD CONSTRAINT 不支持该关键字，
        -- 故先查 pg_constraint，重复执行不报错）
        IF NOT EXISTS (
            SELECT 1 FROM pg_constraint c
              JOIN pg_class t ON t.oid = c.conrelid
              JOIN pg_namespace n ON n.oid = t.relnamespace
             WHERE n.nspname = s AND t.relname = 'plugins' AND c.contype = 'p'
        ) THEN
            EXECUTE format('ALTER TABLE %I.plugins ADD CONSTRAINT plugins_pkey PRIMARY KEY (id)', s);
        END IF;

        -- 唯一键从 (name) 迁到 (kind, name)：老库先拆旧键，再挂新键。
        -- 两步都各自幂等，重复执行不会报错。
        IF EXISTS (
            SELECT 1 FROM pg_constraint c
              JOIN pg_class t ON t.oid = c.conrelid
              JOIN pg_namespace n ON n.oid = t.relnamespace
             WHERE n.nspname = s AND t.relname = 'plugins' AND c.conname = 'plugins_name_key'
        ) THEN
            EXECUTE format('ALTER TABLE %I.plugins DROP CONSTRAINT plugins_name_key', s);
        END IF;

        IF NOT EXISTS (
            SELECT 1 FROM pg_constraint c
              JOIN pg_class t ON t.oid = c.conrelid
              JOIN pg_namespace n ON n.oid = t.relnamespace
             WHERE n.nspname = s AND t.relname = 'plugins' AND c.conname = 'plugins_kind_name_key'
        ) THEN
            EXECUTE format('ALTER TABLE %I.plugins ADD CONSTRAINT plugins_kind_name_key UNIQUE (kind, name)', s);
        END IF;

        RAISE NOTICE 'plugins table ready in schema: %', s;
    END LOOP;
END $$;

-- 验证：应列出每个租户 schema 的 plugins 表
SELECT table_schema, table_name
  FROM information_schema.tables
 WHERE table_name = 'plugins'
 ORDER BY table_schema;
