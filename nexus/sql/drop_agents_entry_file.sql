-- ============================================================================
-- agents 表移除遗留列 entry_file
-- ----------------------------------------------------------------------------
-- 为什么删：
--   agents.entry_file 是**存了但从不被读**的列 —— 与先前删掉的
--   skills.{skill_category,source_type,skill_type} 同一性质。
--   引用点只有「默认值 / 落库 / 回显前端 / 接受 PUT 写入」，
--   而所有真正读入口文件的地方全部硬编码 "AGENT.md"：
--     * service/agent/agent_impl.go  loadSystemPrompt
--     * service/agent/tools/meta.go  create_agent / read_agent
--     * admin/agents/handlers.go     建 agent 时写骨架
--   admin/agents/handlers.go 的注释已直接点出问题：
--     「入口文件名固定为 AGENT.md …… 不做成可配置（否则会出现"配了却不生效"的误导）」
--
-- 代替方案（与 skills 侧同口径）：
--   入口文件名改为常量 shared/paths.AgentEntryFile = "AGENT.md"
--   （对照 paths.SkillEntryFile = "SKILL.md"，其注释写明"真源在此"）。
--   Go 侧与前端均改为引用该常量 / 字面量，不再经过 DB。
--
-- 幂等：可重复执行；自动遍历所有含 agents 表的租户 schema。
-- 建表模板 nexus/platform/provision/tenant_template.sql 已同步移除该列。
-- 执行方式：
--   psql -h <host> -p <port> -U <user> -d <db> -f nexus/sql/drop_agents_entry_file.sql
--
-- 顺序无关：该列 DEFAULT 'AGENT.md' 且可空，Go 侧停写后 INSERT 依然成立，
-- 所以「先上代码、后跑脚本」与「先跑脚本、后上代码」都安全。
-- ============================================================================
DO $$
DECLARE
    s text;
    non_default bigint;
BEGIN
    FOR s IN
        SELECT table_schema
          FROM information_schema.tables
         WHERE table_name = 'agents'
           AND table_schema NOT IN ('pg_catalog', 'information_schema', 'public', 'platform')
         ORDER BY table_schema
    LOOP
        -- 删除前抽查：若不是全部为 'AGENT.md'（或 NULL），说明有人写过别的值 ——
        -- 那些值本来就从未生效，但值得先看一眼再删
        EXECUTE format(
            'SELECT count(*) FROM %I.agents WHERE entry_file IS DISTINCT FROM ''AGENT.md''', s)
            INTO non_default;
        IF non_default > 0 THEN
            RAISE NOTICE '%: 注意 —— % 行的 entry_file 不是 AGENT.md（这些值从未生效）', s, non_default;
        END IF;

        -- 索引 / CHECK / COMMENT 会随列自动删除，无需单独处理
        EXECUTE format('ALTER TABLE %I.agents DROP COLUMN IF EXISTS entry_file', s);
        RAISE NOTICE 'dropped %.agents.entry_file (if existed)', s;
    END LOOP;
END $$;

-- 验证：agents 表当前列（应看不到 entry_file）
SELECT table_schema, column_name, data_type, is_nullable
  FROM information_schema.columns
 WHERE table_name = 'agents'
   AND table_schema NOT IN ('pg_catalog', 'information_schema', 'public', 'platform')
 ORDER BY table_schema, ordinal_position;

-- 残留约束引用（应为空）
SELECT conrelid::regclass AS tbl, conname
  FROM pg_constraint
 WHERE contype = 'c'
   AND pg_get_constraintdef(oid) ~ 'entry_file';
-- ============================================================================
