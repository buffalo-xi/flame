-- ============================================================================
-- platform.agent_configs: agent run limits (one system row + one row per tenant)
--
-- Moved here from nexus/sql/: this is a platform table, and that directory is neither copied into the PoC image nor
-- walked by scripts/verify-sql.ps1, so a database provisioned from the SQL files never got the table. The failure was
-- invisible at startup and showed up as a generic "internal error" when the console opened the tenant page or a tenant
-- was deleted (provision.Remove deletes this table's rows).
--
-- Semantics (identical to flow_configs / quota_configs):
--   tenant_id = ''    the system row: the global default and the hard ceiling
--   tenant_id = 't0'  a tenant row: overrides the system row, but can only tighten it
--   NULL              not set (fall back to the next level; the code fallback applies when the system row is unset too)
--
-- ⚠️ The one difference from flow_configs: this table does **not** accept -1 (unlimited). All six columns are safety
-- valves rather than quotas:
--   max_iterations      = -1 -> the loop guard is gone, an agent can run forever
--   max_call_depth      = -1 -> A -> B -> A cascades can recurse without bound
--   max_tools           = -1 -> the tool set is not truncated and blows the context up
--   max_output_bytes    = -1 -> one command's output can eat the whole machine
--   max_run_records     = -1 -> the agent run log (agent_run_records) grows without bound on disk
--   max_session_records = -1 -> the session memory (session_records) grows without bound on disk
--
-- max_session_records is the agent-side counterpart of flow_configs.max_session_records: the two paths had different
-- retention sources (the agent side was a hard-coded 100 in service/agent), which made the same number unconfigurable
-- for one owner type and configurable for the other.
-- Both write paths (admin / platform handlers) refuse -1 and only accept >= 1; the CHECK below is the backstop.
-- shared/agentconfig additionally normalises <= 0 into the fallback, so a hand-edited row cannot bypass this.
--
-- Idempotent: safe to re-run. deploy/poc/entrypoint.sh applies the platform SQL on every boot.
-- Runtime reads go through shared/agentconfig (Redis cache, 5 min TTL backstop).
-- ============================================================================
CREATE TABLE IF NOT EXISTS platform.agent_configs (
    tenant_id        varchar(32) PRIMARY KEY,          -- '' = system row, 't0' = tenant row
    max_iterations   int         NULL,                 -- rounds per run
    max_call_depth   int         NULL,                 -- agent -> agent call depth
    max_tools        int         NULL,                 -- bound tool cap (truncated past it)
    max_output_bytes int         NULL,                 -- output byte cap for a single command
    max_run_records  int         NULL,                 -- agent run log rows kept per agent
    max_session_records int      NULL,                 -- session memory rows kept per session
    updated_at       timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT agent_configs_positive CHECK (
        (max_iterations   IS NULL OR max_iterations   >= 1) AND
        (max_call_depth   IS NULL OR max_call_depth   >= 1) AND
        (max_tools        IS NULL OR max_tools        >= 1) AND
        (max_output_bytes IS NULL OR max_output_bytes >= 1) AND
        (max_run_records  IS NULL OR max_run_records  >= 1) AND
        (max_session_records IS NULL OR max_session_records >= 1)
    )
);

-- Existing databases: CREATE TABLE IF NOT EXISTS neither adds a column nor replaces the inline CHECK, so both steps
-- are spelled out and are idempotent. Re-running the dropped/recreated constraint is what keeps an old row from
-- bypassing the >= 1 rule on the new column.
ALTER TABLE platform.agent_configs ADD COLUMN IF NOT EXISTS max_run_records int NULL;
ALTER TABLE platform.agent_configs ADD COLUMN IF NOT EXISTS max_session_records int NULL;
ALTER TABLE platform.agent_configs DROP CONSTRAINT IF EXISTS agent_configs_positive;
ALTER TABLE platform.agent_configs ADD CONSTRAINT agent_configs_positive CHECK (
    (max_iterations   IS NULL OR max_iterations   >= 1) AND
    (max_call_depth   IS NULL OR max_call_depth   >= 1) AND
    (max_tools        IS NULL OR max_tools        >= 1) AND
    (max_output_bytes IS NULL OR max_output_bytes >= 1) AND
    (max_run_records  IS NULL OR max_run_records  >= 1) AND
    (max_session_records IS NULL OR max_session_records >= 1)
);

COMMENT ON TABLE platform.agent_configs IS
    'Agent run limits: tenant_id = '''' is the system row (also the hard ceiling), ''t0'' a tenant row; NULL = not set; -1 is rejected';
COMMENT ON COLUMN platform.agent_configs.max_iterations IS 'Rounds per run (loop guard, code fallback 40)';
COMMENT ON COLUMN platform.agent_configs.max_call_depth IS 'Agent call depth (guards A -> B -> A recursion, code fallback 3)';
COMMENT ON COLUMN platform.agent_configs.max_tools IS 'Bound tool cap (truncated with a warning past it, code fallback 60)';
COMMENT ON COLUMN platform.agent_configs.max_output_bytes IS 'Output byte cap for a single command (code fallback 65536)';
COMMENT ON COLUMN platform.agent_configs.max_run_records IS
    'Agent run log rows kept per agent in agent_run_records (retention, code fallback 100); >= 1 only, no -1';
COMMENT ON COLUMN platform.agent_configs.max_session_records IS
    'Session memory rows kept per session in session_records (retention, code fallback 50, the same shape as flow_configs.max_session_records); >= 1 only, no -1';
