-- ============================================================================
-- Drop the flow-level max_recursion_depth / max_node_count columns
-- ----------------------------------------------------------------------------
-- Why:
--   * recursion depth is a property of the call graph (flow A calls B calls C), so no single
--     flow definition can declare it. Both depth gates now read platform.flow_configs only.
--   * the node count is a static property of the diagram: a run reads a fixed diagram, so a
--     runtime ceiling could never fire. It is checked at the write boundary instead (flow save,
--     asset install, editor) against the tenant quota.
--   * both were mirrored into flow_versions and compared for "dirty", so an operator tuning a
--     quota made every flow look changed until it was released again.
-- Idempotent: DROP COLUMN IF EXISTS, walks every tenant schema (platform is excluded).
-- Order: deploy the code that no longer reads or writes these columns first, then run this.
--   Until then the columns keep their DEFAULT -1 and stay harmless.
-- Templates already updated: nexus/platform/provision/template/schema.sql,
--   nexus/platform/provision/tenant_template.sql, nexus/sql/entity_versions.sql.
-- Run:
--   psql -h <host> -p <port> -U <user> -d <db> -f nexus/sql/flows_drop_tenant_limits.sql
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
        EXECUTE format('ALTER TABLE %I.flows DROP COLUMN IF EXISTS max_recursion_depth', s);
        EXECUTE format('ALTER TABLE %I.flows DROP COLUMN IF EXISTS max_node_count', s);
        EXECUTE format('ALTER TABLE IF EXISTS %I.flow_versions DROP COLUMN IF EXISTS max_recursion_depth', s);
        EXECUTE format('ALTER TABLE IF EXISTS %I.flow_versions DROP COLUMN IF EXISTS max_node_count', s);
        RAISE NOTICE '%: dropped the flow-level depth / node-count columns', s;
    END LOOP;
END $$;

-- Verify: the two columns must not be listed any more (an empty result is the success case).
SELECT table_schema, table_name, column_name
  FROM information_schema.columns
 WHERE table_name IN ('flows', 'flow_versions')
   AND column_name IN ('max_recursion_depth', 'max_node_count')
   AND table_schema NOT IN ('pg_catalog', 'information_schema', 'public', 'platform')
 ORDER BY table_schema, table_name;
