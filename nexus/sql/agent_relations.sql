-- ============================================================================
-- agent_relations：agent 之间的调用授权表
-- ----------------------------------------------------------------------------
-- 语义：
--   无行 → caller 不可调用 callee（默认拒绝）
--   有行 → 允许调用
--
-- 自动授权：
--   agent 通过 create_agent 创建新 agent 时，在同一事务内自动写入
--   (创建者, 新 agent) 一行 —— 见 agents.Repository.CreateAgent。
--   非 agent 创建（如管理端人工创建）不自动写入，由请求显式指定。
--
-- 说明：
--   * 【不使用外键】：agent 删除时的关联清理，由应用层在同一事务内完成
--     （agents.Repository.DeleteAgent，双向 caller_id / callee_id）。
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
        EXECUTE format($f$
            CREATE TABLE IF NOT EXISTS %I.agent_relations (
                caller_id  uuid        NOT NULL,
                callee_id  uuid        NOT NULL,
                created_by uuid        NULL,
                created_at timestamptz NOT NULL DEFAULT now(),
                PRIMARY KEY (caller_id, callee_id)
            )$f$, s);

        -- 支撑反查「谁可以调用我」
        EXECUTE format('CREATE INDEX IF NOT EXISTS %I ON %I.agent_relations (callee_id)',
            s || '_agent_relations_callee_idx', s);

        EXECUTE format($c$COMMENT ON TABLE %I.agent_relations IS 'agent 调用授权：无行=不可调用(默认拒绝)；创建者自动获得对新 agent 的调用权'$c$, s);

        RAISE NOTICE 'agent_relations ready in schema: %', s;
    END LOOP;
END $$;

-- 验证：应列出每个租户 schema 一行
SELECT table_schema, table_name FROM information_schema.tables
 WHERE table_name = 'agent_relations' ORDER BY 1;
