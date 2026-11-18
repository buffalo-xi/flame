-- ============================================================================
-- skills.restricted：可见性收窄开关（默认 true = 默认拒绝）
-- ----------------------------------------------------------------------------
--   true（默认）  → 仅授权 agent 可见（企业级最小权限：不显式授权即不可见）
--   false         → 全员可见（广播）
--
-- 说明：
--   * 仅对「租户级 skill」（agent_id IS NULL）有意义；私有 skill 的可见性由归属决定。
--   * 失败方向：默认 true 时"忘记配授权"= 功能不可用（可发现）；
--     默认 false 时"忘记配"= 全员可见（静默泄露）。
--   * 幂等：可重复执行；自动遍历所有含 skills 表的租户 schema。
-- ============================================================================
DO $$
DECLARE
    s text;
BEGIN
    FOR s IN
        SELECT table_schema
          FROM information_schema.tables
         WHERE table_name = 'skills'
           AND table_schema NOT IN ('pg_catalog', 'information_schema', 'public', 'platform')
         ORDER BY table_schema
    LOOP
        -- 新环境：建列即默认 true
        EXECUTE format(
            'ALTER TABLE %I.skills ADD COLUMN IF NOT EXISTS restricted boolean NOT NULL DEFAULT true', s);
        -- 存量环境：纠正历史默认值 false
        EXECUTE format(
            'ALTER TABLE %I.skills ALTER COLUMN restricted SET DEFAULT true', s);

        RAISE NOTICE 'skills.restricted (default true) ready in schema: %', s;
    END LOOP;
END $$;

-- 验证：应列出每个租户 schema 的 restricted 列，且 column_default 为 true
SELECT table_schema, column_name, data_type, column_default
  FROM information_schema.columns
 WHERE table_name = 'skills' AND column_name = 'restricted'
 ORDER BY table_schema;
