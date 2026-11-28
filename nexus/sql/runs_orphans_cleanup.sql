-- ============================================================================
-- Delete rows whose flow no longer exists (run records, node records, breakpoints)
-- ----------------------------------------------------------------------------
-- Why: flow_run_records / flow_node_records / flow_breakpoints have no foreign key to flows (agent_run_records does
-- cascade from agents), so every flow deleted before the handler learned to clean up left them behind. Run records are
-- unreachable from the flow's own pages, yet the cross-flow history still lists them with a dangling flow_id; breakpoints
-- are per-flow editor state that the debugger would match on a dead flow.
--
-- The repository now deletes all three tables in the same transaction as the flow row
-- (flows.PostgresRepository.DeleteFlow), so this script is the one-off backfill for whatever piled up before that.
-- It is idempotent and safe to run twice.
-- Order: node records first, then the runs that point at the missing flow; breakpoints are independent.
-- Run:
--   psql -h <host> -p <port> -U <user> -d <db> -f nexus/sql/runs_orphans_cleanup.sql
-- ============================================================================
DO $$
DECLARE
    s text;
    nodes bigint;
    runs bigint;
    breaks bigint;
BEGIN
    FOR s IN
        SELECT table_schema
          FROM information_schema.tables
         WHERE table_name = 'flow_run_records'
           AND table_schema NOT IN ('pg_catalog', 'information_schema', 'public', 'platform')
         ORDER BY table_schema
    LOOP
        EXECUTE format($q$
            DELETE FROM %I.flow_node_records
             WHERE run_id IN (
                 SELECT r.id FROM %I.flow_run_records r
                  WHERE NOT EXISTS (SELECT 1 FROM %I.flows f WHERE f.id = r.flow_id)
             )$q$, s, s, s);
        GET DIAGNOSTICS nodes = ROW_COUNT;

        EXECUTE format($q$
            DELETE FROM %I.flow_run_records r
             WHERE NOT EXISTS (SELECT 1 FROM %I.flows f WHERE f.id = r.flow_id)$q$, s, s);
        GET DIAGNOSTICS runs = ROW_COUNT;

        EXECUTE format($q$
            DELETE FROM %I.flow_breakpoints b
             WHERE NOT EXISTS (SELECT 1 FROM %I.flows f WHERE f.id::text = b.flow_id)$q$, s, s);
        GET DIAGNOSTICS breaks = ROW_COUNT;

        RAISE NOTICE '%: removed % orphan run(s), % node record(s) and % breakpoint(s)', s, runs, nodes, breaks;
    END LOOP;
END $$;

-- Verify: every schema must report 0 remaining orphans.
DO $$
DECLARE
    s text;
    left_over bigint;
    left_breaks bigint;
BEGIN
    FOR s IN
        SELECT table_schema
          FROM information_schema.tables
         WHERE table_name = 'flow_run_records'
           AND table_schema NOT IN ('pg_catalog', 'information_schema', 'public', 'platform')
         ORDER BY table_schema
    LOOP
        EXECUTE format($q$SELECT count(*) FROM %I.flow_run_records r
                          WHERE NOT EXISTS (SELECT 1 FROM %I.flows f WHERE f.id = r.flow_id)$q$, s, s) INTO left_over;
        EXECUTE format($q$SELECT count(*) FROM %I.flow_breakpoints b
                          WHERE NOT EXISTS (SELECT 1 FROM %I.flows f WHERE f.id::text = b.flow_id)$q$, s, s) INTO left_breaks;
        RAISE NOTICE '%: % orphan run(s) and % orphan breakpoint(s) remaining', s, left_over, left_breaks;
    END LOOP;
END $$;
