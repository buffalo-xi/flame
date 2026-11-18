-- ============================================================================
-- 删除 agents.impl_type 列
--
-- 背景：该字段已废弃。运行时只有一种实现（agentImpl：prompt 取自工作区的
--       AGENT.md），filesystem / builtin / inline 三个取值在代码里毫无差异，
--       其中 inline 更会导致 "agent type not supported" 运行时错误。
--       配置它既无效果又会误导，因此移除。
--
-- 幂等：可重复执行；自动遍历所有含 agents 表的租户 schema。
-- 警告：DROP COLUMN 不可逆（列内数据会丢失）。虽然该字段值无实际作用，
--       执行前仍建议确认无需保留。
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
        IF EXISTS (SELECT 1
                     FROM information_schema.columns
                    WHERE table_schema = s
                      AND table_name = 'agents'
                      AND column_name = 'impl_type') THEN
            EXECUTE format('ALTER TABLE %I.agents DROP COLUMN impl_type', s);
            RAISE NOTICE 'dropped agents.impl_type in schema: %', s;
        END IF;
    END LOOP;
END $$;

-- 验证：应返回 0 行
SELECT table_schema, column_name
  FROM information_schema.columns
 WHERE table_name = 'agents' AND column_name = 'impl_type'
 ORDER BY 1;
