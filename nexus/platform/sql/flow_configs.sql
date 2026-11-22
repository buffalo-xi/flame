-- ============================================================================
-- platform.flow_configs: flow run limits (one system row + one row per tenant)
--
-- Why this file exists: the table had no CREATE TABLE anywhere in this repository. It had been created by hand in the
-- earliest environment, so every store that was provisioned from the SQL files lacked it - and the symptom was not a
-- startup failure but a generic "internal error" in the console, whose real cause ("relation platform.flow_configs
-- does not exist") only shows up in the response body. It hits the tenant page (flow / agent / quota limits are loaded
-- as soon as a tenant is selected) and tenant deletion (provision.Remove deletes this table's rows).
--
-- Semantics (identical to quota_configs):
--   tenant_id = ''    the system row: the global default and the hard ceiling
--   tenant_id = 't0'  a tenant row: overrides the system row, but can only tighten it
--   NULL              not set (fall back to the next level; the code fallback applies when the system row is unset too)
--   -1                unlimited (a real value, it never falls back)
--
-- Idempotent: safe to re-run. deploy/poc/entrypoint.sh applies the platform SQL on every boot.
-- Runtime reads go through shared/flowconfig (Redis cache, 5 min TTL backstop).
-- ============================================================================
CREATE TABLE IF NOT EXISTS platform.flow_configs (
    tenant_id           varchar(32) PRIMARY KEY,   -- '' = system row, 't0' = tenant row
    max_timeout         int NULL,                  -- seconds a single run may take
    max_recursion_depth int NULL,                  -- flow -> flow nesting depth
    max_node_count      int NULL,                  -- nodes a flow may contain
    max_exec_count      int NULL,                  -- cumulative node executions in one run
    max_loop_times      int NULL,                  -- loop node iteration cap
    max_run_records     int NULL,                  -- run history kept per flow
    max_session_records int NULL,                  -- session memories kept per session
    updated_at          timestamptz NOT NULL DEFAULT now()
);
