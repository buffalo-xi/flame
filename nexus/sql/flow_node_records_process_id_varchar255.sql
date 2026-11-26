-- ============================================================================
-- Widen flow_node_records.process_id to varchar(255)
-- ----------------------------------------------------------------------------
-- Why: process_id is the child-process chain built by ProcessManager.NewProcess
--   ("1_1_1_..."), so its length grows with nesting depth. A recursive function
--   adds at least two segments per level (the call's child process plus the
--   function body's), and max_recursion_depth is 100, so a run nested ~16 deep
--   already passes the old 64-character limit. The insert then fails with 22001
--   ("value too long for type character varying(64)"); because SaveRun is one
--   transaction, the whole run is lost - node rows and the run row alike - and
--   the persistence error is reported as a failed run.
-- Why 255 and not text: the column is history. Nothing joins, filters or
--   indexes on it (the UI does not read it either), so an unbounded column would
--   only trade "cannot write" for "writes a few hundred characters of 1_1_1".
--   Writer-side truncation (executors.storedProcessID) caps the value at this
--   width and keeps "...depth=N" instead of the tail.
-- Idempotent: safe to run twice; loops over every schema that has the table.
-- Order: backward compatible, so it can run before or after the code deploy.
-- Run:
--   psql -h <host> -p <port> -U <user> -d <db> -f nexus/sql/flow_node_records_process_id_varchar255.sql
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
        IF EXISTS (SELECT 1
                     FROM information_schema.columns
                    WHERE table_schema = s
                      AND table_name = 'flow_node_records'
                      AND column_name = 'process_id'
                      AND character_maximum_length IS DISTINCT FROM 255) THEN
            EXECUTE format('ALTER TABLE %I.flow_node_records ALTER COLUMN process_id TYPE varchar(255)', s);
            RAISE NOTICE '%: flow_node_records.process_id -> varchar(255)', s;
        ELSE
            RAISE NOTICE '%: flow_node_records.process_id already varchar(255)', s;
        END IF;
    END LOOP;
END $$;

-- Verification: every tenant schema shows 255 (and no schema was missed).
SELECT table_schema, data_type, character_maximum_length
  FROM information_schema.columns
 WHERE table_name = 'flow_node_records' AND column_name = 'process_id'
   AND table_schema NOT IN ('pg_catalog', 'information_schema', 'public', 'platform')
 ORDER BY table_schema;
