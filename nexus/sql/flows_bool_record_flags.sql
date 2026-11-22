-- ============================================================================
-- Turn the flow-level retention counts into two switches
-- ----------------------------------------------------------------------------
-- flows / flow_versions used to carry max_run_records and max_session_records as per-flow numbers. Both were really two
-- different things in one column: "is this recorded at all" (content: whether the workflow keeps run records / session
-- memory) and "how many" (policy: the tenant quota in platform.flow_configs). This keeps the switch and drops the number.
--
--   store_runs      = the old max_run_records <> 0   (-1 "unset" keeps records, 0 meant "store nothing")
--   session_memory  = the old max_session_records > 0 (-1 and 0 both meant "no session memory")
--
-- The two CHECK constraints from flow_limits_check_constraints.sql reference the dropped columns, so PostgreSQL drops
-- them with the column; they are recreated here with the columns that remain.
--
-- Idempotent: safe to run twice. Walks every tenant schema (platform is excluded).
-- Order: deploy the code that reads store_runs / session_memory first — the old binary still names the dropped columns.
-- Run:
--   psql -h <host> -p <port> -U <user> -d <db> -f nexus/sql/flows_bool_record_flags.sql
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
        -- 1. The switches (added with a default first, so existing rows are valid before the backfill).
        EXECUTE format('ALTER TABLE %I.flows ADD COLUMN IF NOT EXISTS store_runs boolean DEFAULT true NOT NULL', s);
        EXECUTE format('ALTER TABLE %I.flows ADD COLUMN IF NOT EXISTS session_memory boolean DEFAULT true NOT NULL', s);
        EXECUTE format('ALTER TABLE IF EXISTS %I.flow_versions ADD COLUMN IF NOT EXISTS store_runs boolean', s);
        EXECUTE format('ALTER TABLE IF EXISTS %I.flow_versions ADD COLUMN IF NOT EXISTS session_memory boolean', s);

        -- 2. Backfill from the counts, only where the old columns still exist (a second run must not overwrite them).
        IF EXISTS (SELECT 1 FROM information_schema.columns
                    WHERE table_schema = s AND table_name = 'flows' AND column_name = 'max_run_records') THEN
            EXECUTE format('UPDATE %I.flows SET store_runs = (max_run_records IS NULL OR max_run_records <> 0),
                                                  session_memory = (max_session_records > 0)', s);
        END IF;
        IF EXISTS (SELECT 1 FROM information_schema.columns
                    WHERE table_schema = s AND table_name = 'flow_versions' AND column_name = 'max_run_records') THEN
            EXECUTE format('UPDATE %I.flow_versions SET store_runs = (max_run_records IS NULL OR max_run_records <> 0),
                                                       session_memory = (max_session_records > 0)', s);
        END IF;

        -- 3. Drop the counts. This also drops flows_limits_range_check / flow_versions_limits_range_check, which
        --    reference them.
        EXECUTE format('ALTER TABLE %I.flows DROP COLUMN IF EXISTS max_run_records', s);
        EXECUTE format('ALTER TABLE %I.flows DROP COLUMN IF EXISTS max_session_records', s);
        EXECUTE format('ALTER TABLE IF EXISTS %I.flow_versions DROP COLUMN IF EXISTS max_run_records', s);
        EXECUTE format('ALTER TABLE IF EXISTS %I.flow_versions DROP COLUMN IF EXISTS max_session_records', s);

        -- 4. Recreate the range checks over the columns that remain.
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

        RAISE NOTICE '%: retention switches in place, per-flow counts dropped', s;
    END LOOP;
END $$;

-- Verify: no tenant table lists the old counts any more (an empty result is the success case).
SELECT table_schema, table_name, column_name
  FROM information_schema.columns
 WHERE table_name IN ('flows', 'flow_versions')
   AND column_name IN ('max_run_records', 'max_session_records')
   AND table_schema NOT IN ('pg_catalog', 'information_schema', 'public', 'platform')
 ORDER BY table_schema, table_name;

-- Verify: the switch distribution per tenant (a sanity read after the backfill).
DO $$
DECLARE
    s text;
    total bigint;
    off_runs bigint;
    off_mem bigint;
BEGIN
    FOR s IN
        SELECT table_schema
          FROM information_schema.columns
         WHERE table_name = 'flows' AND column_name = 'store_runs'
           AND table_schema NOT IN ('pg_catalog', 'information_schema', 'public', 'platform')
         ORDER BY table_schema
    LOOP
        EXECUTE format('SELECT count(*), count(*) FILTER (WHERE NOT store_runs), count(*) FILTER (WHERE NOT session_memory) FROM %I.flows', s)
           INTO total, off_runs, off_mem;
        RAISE NOTICE '%: % flows, % without run records, % without session memory', s, total, off_runs, off_mem;
    END LOOP;
END $$;
