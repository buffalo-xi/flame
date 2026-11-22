-- ============================================================================
-- agent_run_records: index for "which runs belong to this session"
--
-- Why: the agent history can be narrowed to one session, and the only index the table has is
-- (agent_id, started_at DESC) - a session lookup would scan every retained row of every agent. The
-- workflow run log has carried the equivalent index since it was created (idx_pipe_run_records_session);
-- this is the same one for the agent side.
--
-- Idempotent: CREATE INDEX IF NOT EXISTS, and schemas without the table are skipped.
-- ============================================================================
DO $mig$
DECLARE
    s text;
BEGIN
    FOR s IN
        SELECT table_schema
          FROM information_schema.tables
         WHERE table_name = 'agent_run_records'
           AND table_type = 'BASE TABLE'
           AND table_schema NOT IN ('pg_catalog', 'information_schema', 'public', 'platform')
         ORDER BY table_schema
    LOOP
        EXECUTE format(
            'CREATE INDEX IF NOT EXISTS idx_agent_run_records_session ON %I.agent_run_records USING btree (session_id, started_at DESC)', s);
        RAISE NOTICE 'schema %: idx_agent_run_records_session ensured', s;
    END LOOP;
END
$mig$;

-- Verification (run after the migration)
-- a) the index exists in every tenant schema (one row per schema)
--   SELECT schemaname, indexname FROM pg_indexes
--    WHERE tablename = 'agent_run_records' AND indexname = 'idx_agent_run_records_session'
--    ORDER BY schemaname;
--
-- b) it is actually used by a session lookup
--   EXPLAIN SELECT id FROM t0.agent_run_records WHERE session_id = '<uuid>' ORDER BY started_at DESC LIMIT 20;
