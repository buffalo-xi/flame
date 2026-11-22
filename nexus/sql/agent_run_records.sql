-- ============================================================================
-- Agent run log: the agent_run_records table
--
-- Why a new table instead of widening session_records:
--   * session_records is the **memory** the model reads back as context. Its session_id is uuid NOT NULL, so a
--     stateless MCP / RPC call (no params._meta.session_id) cannot be stored there at all - and those are
--     exactly the calls an operator needs to trace.
--   * deleting a memory row removes a turn from the conversation and from the model's context. A log row must
--     be deletable on its own, without touching any conversation.
--   * retention differs: memory is trimmed per (owner, session), the log per agent.
--
-- Scope: every tenant schema that has an agents table (the same loop pattern as session_records_duration.sql).
-- Idempotent: safe to re-run (IF NOT EXISTS + ON CONFLICT DO NOTHING).
-- Backfill: existing session_records rows with owner_type='agent' are copied so the page is not empty on the
-- first day. They are a lossy copy by design (user_id is not stored in session_records, and only a bounded
-- answer preview is kept). The row id is the memory row's run_id, not its own id: one run is one row, and
-- session_records.run_id has to point at it.
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
            CREATE TABLE IF NOT EXISTS %I.agent_run_records (
                -- The row id IS the run id: session_records.run_id points here, so there is no second copy of it.
                id uuid NOT NULL,
                agent_id uuid NOT NULL,
                session_id uuid,
                user_id uuid,
                run_type character varying(16),
                status character varying(16) DEFAULT 'ok'::character varying NOT NULL,
                err_msg text DEFAULT ''::text NOT NULL,
                duration_ms bigint DEFAULT 0 NOT NULL,
                started_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
                finished_at timestamp with time zone,
                question text DEFAULT ''::text NOT NULL,
                question_size integer DEFAULT 0 NOT NULL,
                answer text DEFAULT ''::text NOT NULL,
                answer_truncated boolean DEFAULT false NOT NULL,
                answer_size integer DEFAULT 0 NOT NULL,
                created_at timestamp with time zone DEFAULT now() NOT NULL,
                CONSTRAINT agent_run_records_pk PRIMARY KEY (id),
                -- Deleting an agent removes its log: the definition is gone, and the per-agent trim would
                -- never run for that id again (the rows would grow forever).
                CONSTRAINT agent_run_records_agent_fk FOREIGN KEY (agent_id) REFERENCES %I.agents(id) ON DELETE CASCADE
            )
        $f$, s, s);

        EXECUTE format(
            'CREATE INDEX IF NOT EXISTS idx_agent_run_records_agent ON %I.agent_run_records USING btree (agent_id, started_at DESC)', s);

        EXECUTE format(
            'COMMENT ON TABLE %I.agent_run_records IS ''agent run log (audit): one row per run, including stateless calls with no session''', s);
        EXECUTE format(
            'COMMENT ON COLUMN %I.agent_run_records.session_id IS ''NULL = stateless run (no _meta.session_id)''', s);
        EXECUTE format(
            'COMMENT ON COLUMN %I.agent_run_records.answer IS ''preview only (bounded); the full answer lives in session_records''', s);

        -- Backfill from the memory table: agent rows only (flow rows belong to the workflow history).
        -- The JOIN on agents is not decoration: session_records has no foreign key to agents, so a row can
        -- outlive its agent, and inserting such an orphan would violate agent_run_records_agent_fk.
        -- left() counts characters, so a multi-byte answer is never cut mid-character.
        EXECUTE format($f$
            INSERT INTO %I.agent_run_records
                (id, agent_id, session_id, user_id, run_type, status, err_msg, duration_ms,
                 started_at, finished_at, question, question_size, answer, answer_truncated, answer_size, created_at)
            SELECT r.run_id, r.owner_id, r.session_id, NULL,
                   COALESCE(r.run_type, 'call'), COALESCE(r.status, 'ok'), COALESCE(r.error, ''), r.duration_ms,
                   r.created_at, r.created_at,
                   COALESCE(r.input->>'content', ''),
                   char_length(COALESCE(r.input->>'content', '')),
                   left(COALESCE(r.output, ''), 4000),
                   char_length(COALESCE(r.output, '')) > 4000,
                   char_length(COALESCE(r.output, '')),
                   r.created_at
              FROM %I.session_records r
              JOIN %I.agents ag ON ag.id = r.owner_id
             WHERE r.owner_type = 'agent'
            ON CONFLICT (id) DO NOTHING
        $f$, s, s, s);

        -- Apply the runtime retention so a large memory table does not leave an oversized first page behind.
        -- The cap is the same one the runtime uses: platform.agent_configs.max_run_records for this tenant
        -- (code fallback 100 when the row is missing, matching shared/agentconfig). Platform SQL is applied before
        -- tenant scripts, so the table is expected to exist; without it this statement fails loudly rather than guessing.
        EXECUTE format($f$
            DELETE FROM %I.agent_run_records a
             USING (
                SELECT id, row_number() OVER (PARTITION BY agent_id ORDER BY started_at DESC, id DESC) AS rn
                  FROM %I.agent_run_records
             ) d
             WHERE a.id = d.id AND d.rn > COALESCE(
                 (SELECT max_run_records FROM platform.agent_configs WHERE tenant_id = %L), 100)
        $f$, s, s, s);

        RAISE NOTICE 'agent_run_records ready in schema: %', s;
    END LOOP;
END $$;

-- ============================================================================
-- Verification
-- ============================================================================
-- a) the table and its index exist in every tenant schema
--   SELECT table_schema, table_name FROM information_schema.tables
--    WHERE table_name = 'agent_run_records' ORDER BY table_schema;
--
-- b) row count per agent, and how many rows have no session (the calls the old page could not show)
--   SELECT agent_id, count(*) AS total, count(*) FILTER (WHERE session_id IS NULL) AS stateless
--     FROM t0.agent_run_records GROUP BY agent_id ORDER BY total DESC;
--
-- c) the newest rows carry status / duration_ms (id is the run id)
--   SELECT id, agent_id, session_id, status, duration_ms, left(question, 40), created_at
--     FROM t0.agent_run_records ORDER BY started_at DESC LIMIT 5;
--
-- Rollback (code must be rolled back with it: the repository/agentruns package, the admin handlers and the
-- desktop agent history page):
--   DROP TABLE IF EXISTS t0.agent_run_records;
