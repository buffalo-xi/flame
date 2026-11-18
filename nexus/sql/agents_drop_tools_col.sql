-- ============================================================================
-- agents 表移除遗留列 tools
-- ----------------------------------------------------------------------------
-- 说明：
--   * 该列为历史遗留（早期"用 JSONB 列存 agent 工具列表"的设计残留，
--     后由工具授权体系 agent_tool_grants 取代），当前代码从未读取或写入
--     （agentColumns / INSERT / UPDATE 均不含该列，models.Agent 也无对应字段）。
--   * 幂等：可重复执行；自动遍历所有含 agents 表的租户 schema。
--   * 建议先确认无数据损失（见文件末尾的抽查语句，只报告不落盘）。
--   * 建表模板 nexus/platform/provision/tenant_template.sql 已同步移除该列。
-- 执行方式：
--   psql -h <host> -p <port> -U <user> -d <db> -f nexus/sql/agents_drop_tools_col.sql
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
        EXECUTE format('ALTER TABLE %I.agents DROP COLUMN IF EXISTS tools', s);
        RAISE NOTICE 'dropped %.agents.tools (if existed)', s;
    END LOOP;
END $$;

-- 验证：agents 表当前列
SELECT table_schema, column_name, data_type
  FROM information_schema.columns
 WHERE table_name = 'agents'
   AND table_schema NOT IN ('pg_catalog', 'information_schema', 'public', 'platform')
 ORDER BY table_schema, ordinal_position;

-- ----------------------------------------------------------------------------
-- 附：删除前抽查（确认无数据损失的备查语句，按需单独执行）
--
-- DO $$
-- DECLARE s text; n bigint;
-- BEGIN
--     FOR s IN
--         SELECT table_schema FROM information_schema.tables
--          WHERE table_name = 'agents'
--            AND table_schema NOT IN ('pg_catalog', 'information_schema', 'public', 'platform')
--     LOOP
--         EXECUTE format('SELECT count(*) FROM %I.agents WHERE tools IS NOT NULL', s) INTO n;
--         RAISE NOTICE '%: agents.tools 非空行 = %', s, n;
--     END LOOP;
-- END $$;
-- ============================================================================
