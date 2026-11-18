-- ============================================================================
-- 归一 skills.metadata：NULL → '{}'，并加 NOT NULL（杜绝再次退化）
-- ----------------------------------------------------------------------------
-- 背景（为什么会出现 NULL 与 '{}' 并存）：
--   xsql.JSONB 的读写是**有损**的（见 tools/xsql/sql.go）：
--     * Scan：库里的 '{}' 会被读成 nil（`if s == "null" || s == "{}" { j.Data = nil }`）
--     * Value：nil 又被写回成 SQL NULL（`if j.Data == nil { return nil, nil }`）
--   于是只要走一次「读出来 → 改 → 写回去」的 Update，metadata 就从 '{}' 退化成 NULL。
--   仓库原本只在 Create 里做了 nil → map[string]any{} 的兜底，Update 漏了 ——
--   这正是「建后从未编辑过的行是 {}、被编辑过的行是 NULL」的分界线。
--
-- 本次修复分两处：
--   1. 代码：shared/repository/skills/postgres.go 的 Update 补上与 Create 同口径的兜底；
--   2. DB：本脚本把存量 NULL 归一为 '{}'，并加 NOT NULL 防止将来再出现 NULL（建表模板已同步）。
--
-- 幂等：可重复执行。已归一后再跑，UPDATE 影响 0 行，SET NOT NULL 也不报错。
-- 执行方式：
--   psql -h <host> -p <port> -U <user> -d <db> -f nexus/sql/skills_metadata_not_null.sql
-- ============================================================================
DO $$
DECLARE
    s text;
    normalized bigint;
    total_rows bigint;
    null_rows bigint;
    empty_rows bigint;
BEGIN
    FOR s IN
        SELECT table_schema
          FROM information_schema.tables
         WHERE table_name = 'skills'
           AND table_schema NOT IN ('pg_catalog', 'information_schema', 'public', 'platform')
         ORDER BY table_schema
    LOOP
        -- 1) 存量 NULL 归一为 '{}'（与列 DEFAULT 一致）
        EXECUTE format('UPDATE %I.skills SET metadata = ''{}''::jsonb WHERE metadata IS NULL', s);
        GET DIAGNOSTICS normalized = ROW_COUNT;

        -- 2) 列默认值（补齐历史 schema）+ NOT NULL 约束
        EXECUTE format('ALTER TABLE %I.skills ALTER COLUMN metadata SET DEFAULT ''{}''::jsonb', s);
        EXECUTE format('ALTER TABLE %I.skills ALTER COLUMN metadata SET NOT NULL', s);

        -- 3) 列注释（便于后来人理解为何是 NOT NULL）
        -- 注意：注释文本里**不要**再嵌单引号 —— format() 的 %I 拼接会把它吃掉导致语法错误
        EXECUTE format(
            'COMMENT ON COLUMN %I.skills.metadata IS ''扩展元数据：恒为非空 JSON 对象，空对象表示无扩展字段（NOT NULL）''',
            s);

        -- 4) 即时校验（期望 null_rows = 0）
        EXECUTE format('SELECT count(*) FROM %I.skills', s) INTO total_rows;
        EXECUTE format('SELECT count(*) FROM %I.skills WHERE metadata IS NULL', s) INTO null_rows;
        EXECUTE format('SELECT count(*) FROM %I.skills WHERE metadata = ''{}''::jsonb', s) INTO empty_rows;

        RAISE NOTICE '%.skills: normalized=%  total=%  is_null=%  empty_object=%',
            s, normalized, total_rows, null_rows, empty_rows;
    END LOOP;
END $$;

-- ----------------------------------------------------------------------------
-- 收尾检查：NOT NULL 是否已生效（期望 attnotnull = t），以及默认值
-- ----------------------------------------------------------------------------
SELECT c.table_schema,
       c.column_name,
       c.is_nullable,
       c.column_default
  FROM information_schema.columns c
 WHERE c.table_name = 'skills'
   AND c.column_name = 'metadata'
   AND c.table_schema NOT IN ('pg_catalog', 'information_schema', 'public', 'platform')
 ORDER BY c.table_schema;
-- ============================================================================
