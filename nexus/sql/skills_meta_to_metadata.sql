-- ============================================================================
-- skills.meta -> skills.metadata（列名改回完整词）
-- ----------------------------------------------------------------------------
-- 说明：
--   * 该列原本就叫 metadata，早前被改名为 meta；此处改回完整命名。
--   * 幂等：仅当 meta 存在且 metadata 不存在时才改名，可重复执行。
--   * 自动遍历所有含 skills 表的租户 schema。
-- ============================================================================
DO $$
DECLARE
    s text;
BEGIN
    FOR s IN
        SELECT table_schema
          FROM information_schema.tables
         WHERE table_name = 'skills'
           AND table_schema NOT IN ('pg_catalog', 'information_schema', 'public', 'platform')
         ORDER BY table_schema
    LOOP
        IF EXISTS (SELECT 1 FROM information_schema.columns
                    WHERE table_schema = s AND table_name = 'skills' AND column_name = 'meta')
           AND NOT EXISTS (SELECT 1 FROM information_schema.columns
                    WHERE table_schema = s AND table_name = 'skills' AND column_name = 'metadata') THEN
            EXECUTE format('ALTER TABLE %I.skills RENAME COLUMN meta TO metadata', s);
            RAISE NOTICE 'renamed %.skills.meta -> metadata', s;
        ELSE
            RAISE NOTICE 'skip %: meta missing or metadata already exists', s;
        END IF;
    END LOOP;
END $$;

-- 验证：应列出每个租户 schema 的 metadata 列
SELECT table_schema, column_name, data_type
  FROM information_schema.columns
 WHERE table_name = 'skills' AND column_name IN ('meta', 'metadata')
 ORDER BY table_schema, column_name;
