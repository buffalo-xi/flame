-- ============================================================================
-- 存量 agent 回填「缺省工具授权」
-- ----------------------------------------------------------------------------
-- 背景：
--   * 新建 agent 由代码在 CreateAgent 里物化 models.DefaultAgentTools（12 条 required）
--     —— shared/models/models.go + shared/repository/agents/postgres.go。
--   * 该改动只覆盖**新创建**的 agent；本脚本给**存量**补齐，使口径一致。
--
-- 口径（与创建路径完全一致）：
--   * 只补「零授权且非超级智能体」的 agent —— 已有任何授权行的一律不动（那是管理员刻意配的）；
--   * 例外：`builtin:use_skill` 单独补（见 2）。D28 之后「技能声明的工具」只有 use_skill 能激活，
--     缺了它，存量 agent 已配好的 allow 授权会集体静默失效 —— 这不是放宽权限，是把原有能力接回来；
--   * 全部为 required（恒注入，不受 deny / skill 依赖收窄影响）；
--   * **不含**「技能自建」三件套（`*_own_skill`）—— 缺省不给，由管理员按 agent 授权；
--   * item_type = 'tool'，ref = 'builtin:<name>'；
--   * 幂等：可重复执行；自动遍历所有含 agents 表的租户 schema。
--
-- 说明：工具清单在此**硬编码**，是因为它要与发布时代码里的 DefaultAgentTools 对齐；
--       若以后调整缺省集，改动代码后重新执行一份对应的回填即可。
-- ============================================================================
DO $$
DECLARE
    s    text;
    n    int;
    refs text[] := ARRAY[
        'read_file', 'list_files', 'file_exists',
        'write_file', 'edit_file', 'delete_file',
        'list_agents', 'read_agent', 'list_skills', 'read_skill', 'use_skill',
        'call_agent'
    ];
BEGIN
    FOR s IN
        SELECT table_schema
          FROM information_schema.tables
         WHERE table_name = 'agents'
           AND table_schema NOT IN ('pg_catalog', 'information_schema', 'public', 'platform')
         ORDER BY table_schema
    LOOP
        EXECUTE format($f$
            INSERT INTO %I.agent_tool_grants (agent_id, grant_kind, item_type, ref, updated_by, updated_at)
            SELECT a.id, 'required', 'tool', 'builtin:' || t.name, NULL, now()
              FROM %I.agents a
             CROSS JOIN unnest($1) AS t(name)
             WHERE lower(a.name) <> 'main'
               AND NOT EXISTS (SELECT 1 FROM %I.agent_tool_grants g WHERE g.agent_id = a.id)
            ON CONFLICT (agent_id, grant_kind, item_type, ref) DO NOTHING
        $f$, s, s, s) USING refs;

        GET DIAGNOSTICS n = ROW_COUNT;
        RAISE NOTICE 'backfilled % default tool grant row(s) in schema %', n, s;

        -- 2) 单独补 use_skill：D28 改成「use_skill 激活技能声明的工具」之后，
        --    没有这个工具的 agent 永远点不亮 allow 授权（read_skill 是纯查看）。
        --    只补缺：已有该 ref 的任何行（含显式 deny）一律跳过，不覆盖管理员的决定。
        EXECUTE format($f$
            INSERT INTO %I.agent_tool_grants (agent_id, grant_kind, item_type, ref, updated_by, updated_at)
            SELECT a.id, 'required', 'tool', 'builtin:use_skill', NULL, now()
              FROM %I.agents a
             WHERE lower(a.name) <> 'main'
               AND NOT EXISTS (
                     SELECT 1 FROM %I.agent_tool_grants g
                      WHERE g.agent_id = a.id
                        -- 三种 ref 都会展开出 use_skill：单工具行 / 整组 / 缺省预设。
                        -- 已有任一种就别再插显式行（否则库里多一条重复授权的噪音）。
                        AND g.ref IN ('builtin:use_skill', 'builtin:skill', 'preset:default')
                   )
            ON CONFLICT (agent_id, grant_kind, item_type, ref) DO NOTHING
        $f$, s, s, s);

        GET DIAGNOSTICS n = ROW_COUNT;
        RAISE NOTICE 'backfilled % use_skill grant row(s) in schema %', n, s;
    END LOOP;
END $$;

-- 验证（把 t0 换成目标 schema）：应为 0
SELECT count(*) AS agents_without_grants
  FROM t0.agents a
 WHERE lower(a.name) <> 'main'
   AND NOT EXISTS (SELECT 1 FROM t0.agent_tool_grants g WHERE g.agent_id = a.id);
