-- ============================================================================
-- Add flow_node_records.post_error / handler_error
-- ----------------------------------------------------------------------------
-- Why: a node's on_error block has two fallbacks that can fail on their own -
--   the handler script and the post-processing script - and neither changes the
--   node's own status or err_code. Until now both were only visible in the run
--   logs (read_flow_run, the run panel), which is a prose channel: the reason
--   could not be queried, and a stored record lost it entirely once logs were
--   trimmed. They now sit next to err_msg as their own columns.
-- Why text with a default: both are diagnostic messages, have no length bound
--   worth guessing (a lua traceback or an "undeclared variable" path can be
--   long), and are never indexed or filtered. NOT NULL with DEFAULT '' keeps
--   the two states "no fallback error" and "empty" impossible to confuse.
-- Idempotent: ADD COLUMN IF NOT EXISTS; safe to run twice.
-- Order: run this BEFORE deploying the build that writes the columns. The node
--   INSERT names post_error / handler_error explicitly, so new code against the
--   old schema raises 42703 and SaveRun loses the whole run (it is one
--   transaction: the run row and its node rows alike). Running it earlier is
--   harmless for the old code, which simply does not write them yet.
-- Run:
--   psql -h <host> -p <port> -U <user> -d <db> -f nexus/sql/flow_node_records_post_handler_error.sql
-- ============================================================================
DO $$
DECLARE
    s text;
BEGIN
    FOR s IN
        SELECT table_schema
          FROM information_schema.tables
         WHERE table_name = 'flow_node_records'
           AND table_schema NOT IN ('pg_catalog', 'information_schema', 'public', 'platform')
         ORDER BY table_schema
    LOOP
        EXECUTE format('ALTER TABLE %I.flow_node_records ADD COLUMN IF NOT EXISTS post_error text DEFAULT %L::text NOT NULL', s, '');
        EXECUTE format('ALTER TABLE %I.flow_node_records ADD COLUMN IF NOT EXISTS handler_error text DEFAULT %L::text NOT NULL', s, '');
        RAISE NOTICE '%: flow_node_records.post_error / handler_error ready', s;
    END LOOP;
END $$;

-- Verification: every tenant schema has both columns.
SELECT table_schema, column_name, data_type, is_nullable, column_default
  FROM information_schema.columns
 WHERE table_name = 'flow_node_records'
   AND column_name IN ('post_error', 'handler_error')
   AND table_schema NOT IN ('pg_catalog', 'information_schema', 'public', 'platform')
 ORDER BY table_schema, column_name;
