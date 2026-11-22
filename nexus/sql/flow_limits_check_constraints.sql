-- ============================================================================
-- CHECK constraints on the remaining integer limits
-- ----------------------------------------------------------------------------
-- What: flows / flow_versions keep timeout and max_exec_count, and platform.flow_configs keeps
--   its quota columns. Every one of them is "unset / unlimited" at -1 and meaningful at 0 or
--   above, so a value below -1 is always a bug (a hand edit or a broken writer).
-- Why NOT VALID + VALIDATE: a pre-existing bad row then raises its own error and leaves the
--   constraint NOT VALID for that schema, instead of aborting the migration half way.
-- Note: max_session_records = 0 is rejected by the API but allowed here: the DDL keeps the
--   looser >= -1 so a legacy row does not fail validation.
-- Idempotent: each constraint is dropped first, then re-added.
-- Run:
--   psql -h <host> -p <port> -U <user> -d <db> -f nexus/sql/flow_limits_check_constraints.sql
-- ============================================================================
DO $$
DECLARE
    s text;
BEGIN
    FOR s IN
        SELECT table_schema
          FROM information_schema.tables
         WHERE table_name = 'flows'
           AND table_schema NOT IN ('pg_catalog', 'information_schema', 'public', 'platform')
         ORDER BY table_schema
    LOOP
        EXECUTE format('ALTER TABLE %I.flows DROP CONSTRAINT IF EXISTS flows_limits_range_check', s);
        EXECUTE format($c$ALTER TABLE %I.flows ADD CONSTRAINT flows_limits_range_check CHECK (
            timeout >= -1 AND max_exec_count >= -1) NOT VALID$c$, s);
        BEGIN
            EXECUTE format('ALTER TABLE %I.flows VALIDATE CONSTRAINT flows_limits_range_check', s);
        EXCEPTION WHEN check_violation THEN
            RAISE WARNING '%: flows has rows outside the limit range; the constraint stays NOT VALID', s;
        END;

        IF to_regclass(format('%I.flow_versions', s)) IS NOT NULL THEN
            EXECUTE format('ALTER TABLE %I.flow_versions DROP CONSTRAINT IF EXISTS flow_versions_limits_range_check', s);
            EXECUTE format($c$ALTER TABLE %I.flow_versions ADD CONSTRAINT flow_versions_limits_range_check CHECK (
                timeout >= -1 AND max_exec_count >= -1) NOT VALID$c$, s);
            BEGIN
                EXECUTE format('ALTER TABLE %I.flow_versions VALIDATE CONSTRAINT flow_versions_limits_range_check', s);
            EXCEPTION WHEN check_violation THEN
                RAISE WARNING '%: flow_versions has rows outside the limit range; the constraint stays NOT VALID', s;
            END;
        END IF;

        RAISE NOTICE '%: limit range constraints are in place', s;
    END LOOP;
END $$;

-- The quota table uses NULL for "not set", so the range check has to allow NULL.
DO $$
BEGIN
    IF to_regclass('platform.flow_configs') IS NOT NULL THEN
        ALTER TABLE platform.flow_configs DROP CONSTRAINT IF EXISTS flow_configs_limits_range_check;
        ALTER TABLE platform.flow_configs ADD CONSTRAINT flow_configs_limits_range_check CHECK (
            (max_timeout IS NULL OR max_timeout >= -1)
            AND (max_recursion_depth IS NULL OR max_recursion_depth >= -1)
            AND (max_node_count IS NULL OR max_node_count >= -1)
            AND (max_exec_count IS NULL OR max_exec_count >= -1)
            AND (max_loop_times IS NULL OR max_loop_times >= -1)
            AND (max_run_records IS NULL OR max_run_records >= -1)
            AND (max_session_records IS NULL OR max_session_records >= -1)) NOT VALID;
        BEGIN
            ALTER TABLE platform.flow_configs VALIDATE CONSTRAINT flow_configs_limits_range_check;
        EXCEPTION WHEN check_violation THEN
            RAISE WARNING 'platform.flow_configs has rows outside the limit range; the constraint stays NOT VALID';
        END;
    END IF;
END $$;

-- Verify: every constraint should be present and validated.
SELECT n.nspname AS schema, c.relname AS table, con.conname AS constraint, con.convalidated AS validated
  FROM pg_constraint con
  JOIN pg_class c ON c.oid = con.conrelid
  JOIN pg_namespace n ON n.oid = c.relnamespace
 WHERE con.conname IN ('flows_limits_range_check', 'flow_versions_limits_range_check', 'flow_configs_limits_range_check')
 ORDER BY n.nspname, c.relname;
