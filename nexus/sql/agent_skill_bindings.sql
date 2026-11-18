-- ============================================================================
-- agent_skill_bindings：agent 与 skill 的授权关联表（纯白名单）
-- ----------------------------------------------------------------------------
-- 语义：
--   仅当 skills.restricted = true 时生效 —— 该 skill 只对本表列出的 agent 可见。
--   restricted = false（默认）时本表不参与判定（数据保留，便于来回切换开关）。
--
-- 说明：
--   * 表名用 agent_skill_bindings（"绑定"），明确与 skills 表区分。
--   * 【不使用外键】：agent / skill 删除时的关联清理，由应用层在同一事务内完成
--     - 删 skill   → skills.Repository.Delete / DeleteByAgentID
--     - 删 agent   → agents.Repository.DeleteAgent
--   * 存量数据零迁移：现有 skill 都没有关联行 → 行为与引入前一致。
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
        EXECUTE format($f$
            CREATE TABLE IF NOT EXISTS %I.agent_skill_bindings (
                agent_id   uuid        NOT NULL,
                skill_id   uuid        NOT NULL,
                created_by uuid        NULL,
                created_at timestamptz NOT NULL DEFAULT now(),
                PRIMARY KEY (agent_id, skill_id)
            )$f$, s);

        EXECUTE format(
            'CREATE INDEX IF NOT EXISTS %I ON %I.agent_skill_bindings (skill_id)',
            s || '_agent_skill_bindings_skill_idx', s);

        -- 兼容历史版本：移除 enabled 列（白名单语义不再需要）
        EXECUTE format('ALTER TABLE %I.agent_skill_bindings DROP COLUMN IF EXISTS enabled', s);

        -- 若历史版本建过外键，这里一并移除（幂等；兼容新旧约束名）
        EXECUTE format('ALTER TABLE %I.agent_skill_bindings DROP CONSTRAINT IF EXISTS agent_skills_agent_id_fkey', s);
        EXECUTE format('ALTER TABLE %I.agent_skill_bindings DROP CONSTRAINT IF EXISTS agent_skills_skill_id_fkey', s);
        EXECUTE format('ALTER TABLE %I.agent_skill_bindings DROP CONSTRAINT IF EXISTS agent_skill_bindings_agent_id_fkey', s);
        EXECUTE format('ALTER TABLE %I.agent_skill_bindings DROP CONSTRAINT IF EXISTS agent_skill_bindings_skill_id_fkey', s);

        EXECUTE format(
            $c$COMMENT ON TABLE %I.agent_skill_bindings IS 'agent 与 skill 的授权白名单：仅当 skills.restricted=true 时生效；无外键，级联由应用层事务保证'$c$,
            s);

        RAISE NOTICE 'agent_skill_bindings ready in schema: %', s;
    END LOOP;
END $$;

-- 验证：应列出每个租户 schema 一行；且不应再有外键
SELECT table_schema, table_name FROM information_schema.tables
 WHERE table_name = 'agent_skill_bindings' ORDER BY 1;

SELECT c.conname FROM pg_constraint c
  JOIN pg_class t ON t.oid = c.conrelid
 WHERE t.relname = 'agent_skill_bindings' AND c.contype = 'f';
