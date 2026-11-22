-- ============================================================================
-- agent_run_records: id is the run identity; drop the duplicated run_id column
--
-- Why: the table carried "which run is this" twice - id (a uuid generated at insert, unrelated to anything) and
-- run_id (the id the caller actually ran under). session_records.run_id names the run, so the memory table pointed
-- at run_id while the audit log was keyed by an unrelated id: the two never matched, and "this run's memory" needed
-- a join on a non-key column. One identity removes the second copy.
--
-- Order matters: align the values first, verify, then drop the column. A run stored more than once cannot be
-- resolved by guessing, so it aborts the script; nothing is half-applied, because a DO block is one transaction.
--
-- Idempotent: schemas whose run_id column is already gone are skipped, so a re-run is a no-op.
--
-- Opt-in escape hatch for step 4 (must be set before the script runs):
--   SET flame.allow_orphan_agent_memory = 'on';
-- ============================================================================
DO $mig$
DECLARE
    s       text;
    aligned bigint;
    dup     bigint;
    sample  text;
    orphan  bigint;
    lenient boolean := lower(coalesce(current_setting('flame.allow_orphan_agent_memory', true), 'off'))
                       IN ('on', 'true', '1');
BEGIN
    FOR s IN
        SELECT table_schema
          FROM information_schema.columns
         WHERE table_name = 'agent_run_records'
           AND column_name = 'run_id'
           AND table_schema NOT IN ('pg_catalog', 'information_schema', 'public', 'platform')
         ORDER BY table_schema
    LOOP
        -- 1a. One run must be one row: two rows sharing a run_id.
        EXECUTE format($q$
            SELECT count(*), COALESCE(string_agg(DISTINCT run_id::text, ', '), '')
              FROM (SELECT run_id FROM %I.agent_run_records GROUP BY run_id HAVING count(*) > 1) d
        $q$, s) INTO dup, sample;
        IF dup > 0 THEN
            RAISE EXCEPTION 'schema %: % run id(s) stored more than once in agent_run_records (run_id: %); one run '
                            'must be one row - merge or delete the extra rows, then re-run', s, dup, left(sample, 300);
        END IF;

        -- 1b. The same defect in its other shape: a row whose id already is another row's run_id. Left undetected
        --     it would surface as a primary-key violation half-way through the UPDATE, naming neither run nor schema.
        EXECUTE format($q$
            SELECT count(*), COALESCE(string_agg(DISTINCT a.run_id::text, ', '), '')
              FROM %I.agent_run_records a
              JOIN %I.agent_run_records b ON b.id = a.run_id AND b.id <> a.id
        $q$, s, s) INTO dup, sample;
        IF dup > 0 THEN
            RAISE EXCEPTION 'schema %: % run id(s) already used as another row''s id in agent_run_records (run_id: '
                            '%); the same run is stored twice - merge or delete the extra rows, then re-run',
                            s, dup, left(sample, 300);
        END IF;

        -- 2. Align: the row id becomes the run id. A single statement is safe only because step 1 proved that no
        --    target id is currently held by another row; otherwise a swap of two ids would trip the primary key
        --    mid-statement.
        EXECUTE format('UPDATE %I.agent_run_records SET id = run_id WHERE id <> run_id', s);
        GET DIAGNOSTICS aligned = ROW_COUNT;

        -- 3. Backfill the audit row for agent memory that never got one (recordRun only logs a failure). Same lossy
        --    copy as the bootstrap script: the answer here is a bounded preview, the full text stays in memory.
        EXECUTE format($q$
            INSERT INTO %I.agent_run_records
                (id, agent_id, session_id, user_id, run_type, status, err_msg, duration_ms,
                 started_at, finished_at, question, question_size, answer, answer_truncated, answer_size, created_at)
            SELECT DISTINCT ON (sr.run_id)
                   sr.run_id, sr.owner_id, sr.session_id, NULL,
                   COALESCE(sr.run_type, 'call'), COALESCE(sr.status, 'ok'), COALESCE(sr.error, ''), sr.duration_ms,
                   sr.created_at, sr.created_at,
                   COALESCE(sr.input->>'content', ''),
                   char_length(COALESCE(sr.input->>'content', '')),
                   left(COALESCE(sr.output, ''), 4000),
                   char_length(COALESCE(sr.output, '')) > 4000,
                   char_length(COALESCE(sr.output, '')),
                   sr.created_at
              FROM %I.session_records sr
              JOIN %I.agents ag ON ag.id = sr.owner_id
             WHERE sr.owner_type = 'agent'
               AND NOT EXISTS (SELECT 1 FROM %I.agent_run_records a WHERE a.id = sr.run_id)
             ORDER BY sr.run_id, sr.created_at DESC
            ON CONFLICT (id) DO NOTHING
        $q$, s, s, s, s);

        -- 4. What is left names a run whose row cannot be written: the owning agent is gone, so the foreign key
        --    refuses it, and the memory row is the only evidence of that run. Deleting it silently would lose the
        --    last trace, so this aborts unless the operator opted in above.
        EXECUTE format($q$
            SELECT count(*) FROM %I.session_records sr
             WHERE sr.owner_type = 'agent'
               AND NOT EXISTS (SELECT 1 FROM %I.agent_run_records a WHERE a.id = sr.run_id)
        $q$, s, s) INTO orphan;
        IF orphan > 0 AND NOT lenient THEN
            RAISE EXCEPTION 'schema %: % agent memory row(s) name a run with no audit row (the owning agent is gone, '
                            'so agent_run_records cannot hold them). Set flame.allow_orphan_agent_memory = on to keep '
                            'the memory and finish, or clean those rows up first', s, orphan;
        ELSIF orphan > 0 THEN
            RAISE WARNING 'schema %: % agent memory row(s) kept without an audit row (flame.allow_orphan_agent_memory is on)', s, orphan;
        END IF;

        -- 5. One identity left: drop the duplicate.
        EXECUTE format('ALTER TABLE %I.agent_run_records DROP COLUMN run_id', s);
        EXECUTE format('COMMENT ON COLUMN %I.agent_run_records.id IS %L', s,
                       'run identity; session_records.run_id points here (no separate run_id column: one run is one row)');
        RAISE NOTICE 'schema %: agent run identity aligned (% row(s) rewritten), run_id column dropped', s, aligned;
    END LOOP;
END
$mig$;

-- ============================================================================
-- Verification (run after the migration)
-- ============================================================================
-- a) no schema still carries the duplicate column (must return no rows)
--   SELECT table_schema FROM information_schema.columns
--    WHERE table_name = 'agent_run_records' AND column_name = 'run_id';
--
-- b) every agent memory row names an audit row; only owner_type='flow' may stay unmatched
--   SELECT sr.owner_type, count(*) FROM t0.session_records sr
--     LEFT JOIN t0.agent_run_records a ON a.id = sr.run_id
--    WHERE a.id IS NULL GROUP BY sr.owner_type;
--
-- c) both tables now share the value
--   SELECT sr.run_id, sr.owner_type, a.id FROM t0.session_records sr
--     JOIN t0.agent_run_records a ON a.id = sr.run_id ORDER BY sr.created_at DESC LIMIT 5;
