-- ============================================================================
-- Default system rows for the three limit tables (tenant_id = '')
--
-- Why seed at all: the code fallback is a safety net, not a configuration. A fresh deployment with no system row runs
-- every tenant on the built-in numbers and has no ceiling to tighten against - "the platform administrator can cap a
-- tenant" is true only on paper until somebody types the row in by hand. Seeding the system row makes the ceiling real
-- from the first boot and leaves the code fallback where it belongs: the answer to "the table is missing".
--
-- The system row is the **hard ceiling**, not the value a tenant gets: at runtime min(tenant value, system value)
-- applies, and a tenant inherits the system value only when its own row is unset. These numbers are therefore
-- deliberately larger than the code fallbacks (see shared/flowconfig, shared/agentconfig, shared/quota) - they say "how
-- far a tenant may go", not "what a tenant gets".
--
-- Idempotent and non-destructive: ON CONFLICT DO NOTHING writes a row only when the system row is missing entirely, so
-- an operator's edits - including a partially filled row - always win, and re-running this on every boot (see
-- deploy/poc/entrypoint.sh) is a no-op. The trade-off is the same as llm_presets_seed.sql: deleting the system row from
-- the console brings it back on the next boot; clear individual columns instead if a value must stay unset.
--
-- Execution order is declared in two places, not inferred: scripts/verify-sql.ps1 lists this file last, and
-- deploy/poc/Dockerfile copies it as 25-limit_defaults.sql (after the scripts that create the three tables). The zz_
-- prefix only keeps it at the bottom of the directory listing, so nobody mistakes a seed for a schema file.
-- ============================================================================

-- Flow run limits.
--   max_timeout         30 min: longer runs belong to an async entry point, not to a bigger timeout
--   max_loop_times      pinned to max_exec_count: every loop iteration records a node execution, so a larger loop
--                       ceiling could never be reached and would only look generous
--   max_run_records     / max_session_records: 10x / 4x the code fallback, so the ceiling is usable but still bounded
INSERT INTO platform.flow_configs
    (tenant_id, max_timeout, max_recursion_depth, max_node_count, max_exec_count, max_loop_times,
     max_run_records, max_session_records)
VALUES
    ('', 1800, 20, 500, 2000, 2000, 1000, 200)
ON CONFLICT (tenant_id) DO NOTHING;

-- Agent run limits.
--   max_output_bytes    262144 (256 KiB) matches maxSessionHistoryBytes, so one truncated record cannot swallow the
--                       whole conversation budget
--   max_tools           100: a schema-heavy set is allowed on the ceiling, but it must be an explicit choice
INSERT INTO platform.agent_configs
    (tenant_id, max_iterations, max_call_depth, max_tools, max_output_bytes, max_run_records, max_session_records)
VALUES
    ('', 120, 5, 100, 262144, 1000, 200)
ON CONFLICT (tenant_id) DO NOTHING;

-- Resource quotas.
--   max_session_storage_bytes 10737418240 = 10 GiB per session workspace (the code fallback is 1 GiB)
--   max_user_sessions         1000 sessions per user, counted per owner type (agent / flow each get one bucket)
INSERT INTO platform.quota_configs
    (tenant_id, max_agents, max_skills, max_flows, max_session_storage_bytes, max_user_sessions)
VALUES
    ('', 500, 1000, 1000, 10737418240, 1000)
ON CONFLICT (tenant_id) DO NOTHING;
