-- ============================================================================
-- agents 表移除遗留列 skills
-- ----------------------------------------------------------------------------
-- 说明：
--   * 该列为历史遗留（早期"用 JSONB 列存 skill 归属"的设计残留），
--     当前代码从未读取或写入（agentColumns / INSERT / UPDATE 均不含该列）。
--   * 数据已确认为全 NULL，删除无数据损失。
--   * 幂等：可重复执行；自动遍历所有含 agents 表的租户 schema。
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
        EXECUTE format('ALTER TABLE %I.agents DROP COLUMN IF EXISTS skills', s);
        RAISE NOTICE 'dropped %.agents.skills (if existed)', s;
    END LOOP;
END $$;

-- 验证：agents 表当前列
SELECT table_schema, column_name, data_type
  FROM information_schema.columns
 WHERE table_name = 'agents'
   AND table_schema NOT IN ('pg_catalog', 'information_schema', 'public', 'platform')
 ORDER BY table_schema, ordinal_position;
