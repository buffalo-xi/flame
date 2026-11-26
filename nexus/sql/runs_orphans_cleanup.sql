-- ============================================================================
-- Delete run records whose flow no longer exists
-- ----------------------------------------------------------------------------
-- Why: flow_run_records / flow_node_records have no foreign key to flows (agent_run_records does cascade from agents), so
-- every flow deleted before the handler learned to clean up left its runs and node records behind. They are unreachable
-- from the flow's own pages, yet the cross-flow history still lists them with a dangling flow_id.
--
-- The repository now deletes both tables in the same transaction as the flow row
-- (flows.PostgresRepository.DeleteFlow), so this script is the one-off backfill for whatever piled up before that.
-- It is idempotent and safe to run twice.
-- Order: node records first, then the runs that point at the missing flow.
-- Run:
--   psql -h <host> -p <port> -U <user> -d <db> -f nexus/sql/runs_orphans_cleanup.sql
-- ============================================================================
DO $$
DECLARE
    s text;
    nodes bigint;
    runs bigint;
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

        RAISE NOTICE '%: removed % orphan run(s) and % node record(s)', s, runs, nodes;
    END LOOP;
END $$;

-- Verify: every schema must report 0 remaining orphans.
DO $$
DECLARE
    s text;
    left_over bigint;
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
        RAISE NOTICE '%: % orphan run(s) remaining', s, left_over;
    END LOOP;
END $$;
