-- ============================================================================
-- flow_run_records: add session_id / user_id
--
-- Why: the agent run log has always carried both dimensions (who triggered a run, and which conversation it belonged
-- to). The flow run log kept only flow_id, so "show me this user's runs" could not be answered except by joining
-- session_records - and a run without a session (the debugger, a scheduled trigger, a plain HTTP call) has no row there
-- at all, which made those runs invisible to such a query.
--
-- Existing rows keep NULL. The information was never captured, so most of it cannot be invented; the one exception is
-- the subset that did write a session_records row, because that row names both the session and the run. Those columns
-- are backfilled below. Everything else stays NULL and reads as "unknown", which is the truth.
--
-- Idempotent: ADD COLUMN IF NOT EXISTS / CREATE INDEX IF NOT EXISTS, and the backfill only touches rows whose
-- session_id is still NULL.
-- ============================================================================
DO $mig$
DECLARE
    s text;
BEGIN
    FOR s IN
        SELECT table_schema
          FROM information_schema.tables
         WHERE table_name = 'flow_run_records'
           AND table_schema NOT IN ('pg_catalog', 'information_schema', 'public', 'platform')
         ORDER BY table_schema
    LOOP
        EXECUTE format('ALTER TABLE %I.flow_run_records ADD COLUMN IF NOT EXISTS session_id uuid', s);
        EXECUTE format('ALTER TABLE %I.flow_run_records ADD COLUMN IF NOT EXISTS user_id uuid', s);

        -- Same names as the tenant template, so a migrated schema and a freshly provisioned one stay identical.
        EXECUTE format('CREATE INDEX IF NOT EXISTS idx_pipe_run_records_user ON %I.flow_run_records USING btree (user_id, started_at DESC)', s);
        EXECUTE format('CREATE INDEX IF NOT EXISTS idx_pipe_run_records_session ON %I.flow_run_records USING btree (session_id)', s);

        -- Backfill from the memory row of the same run: it names the session, and the session names the user. DISTINCT ON
        -- keeps this a single row per run even if a run somehow wrote more than one memory row.
        EXECUTE format($sql$
            UPDATE %I.flow_run_records r
               SET session_id = m.session_id,
                   user_id    = m.user_id
              FROM (
                    SELECT DISTINCT ON (sr.run_id) sr.run_id, sr.session_id, sess.user_id
                      FROM %I.session_records sr
                      JOIN %I.sessions sess ON sess.id = sr.session_id
                     WHERE sr.run_id IS NOT NULL
                     ORDER BY sr.run_id, sr.created_at DESC
                   ) m
             WHERE m.run_id = r.id
               AND r.session_id IS NULL
        $sql$, s, s, s);
    END LOOP;
END
$mig$;
