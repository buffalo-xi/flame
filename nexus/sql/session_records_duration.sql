-- ============================================================================
-- session_records.duration_ms：本轮执行耗时（毫秒）
-- ----------------------------------------------------------------------------
--   用途：会话历史里给每条助手回复显示「执行用时」（前端 Chat.vue 的消息头）。
--   * 0 = 未记录：存量行、或 flow 侧写会话记忆时暂不记耗时。
--   * 写入方：service/agent/agent_impl.go 的 recordSessionRound（agent 层统一落库，
--     agent / flow / rpc / mcp 四个入口共用，按 run_type 区分）。
--
-- 幂等：可重复执行；自动遍历所有含 session_records 表的租户 schema。
-- ============================================================================
DO $$
DECLARE
    s text;
BEGIN
    FOR s IN
        SELECT table_schema
          FROM information_schema.tables
         WHERE table_name = 'session_records'
           AND table_schema NOT IN ('pg_catalog', 'information_schema', 'public', 'platform')
         ORDER BY table_schema
    LOOP
        EXECUTE format(
            'ALTER TABLE %I.session_records ADD COLUMN IF NOT EXISTS duration_ms bigint NOT NULL DEFAULT 0', s);
        EXECUTE format(
            'COMMENT ON COLUMN %I.session_records.duration_ms IS ''本轮执行耗时（毫秒）；0=未记录''', s);

        RAISE NOTICE 'session_records.duration_ms ready in schema: %', s;
    END LOOP;
END $$;

-- 验证：应列出每个租户 schema 的 duration_ms 列，且 column_default 为 0
SELECT table_schema, column_name, data_type, column_default
  FROM information_schema.columns
 WHERE table_name = 'session_records' AND column_name = 'duration_ms'
 ORDER BY table_schema;
