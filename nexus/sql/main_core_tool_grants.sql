-- ============================================================================
-- 存量 main 回填「core 工具授权」（组形态）
-- ----------------------------------------------------------------------------
-- 背景：
--   main 过去的工具集是**代码里全开**（toolgrant.BuiltinTools 全量），库里查不到，
--   也没法按租户调整。现在 main 与普通 agent 同口径：常驻 = agent_tool_grants 里
--   有 required 行，种子由 models.SupervisorCoreGrantRefs 定义（六个组，展开 19 个工具）。
--   * 新建租户 / 新建 main：由 agents.CreateAgent 直接物化，不需要本脚本；
--   * 存量租户：跑本脚本补齐，否则运行时会落到「迁移闸 → 缺省全开」的保险丝分支
--     （功能不丢，但会持续打 Error 日志，且授权表仍不是真源）。
--
-- 为什么是六个**组**而不是逐个工具：
--   组是原子的 —— 误删一个 = 少一整类，面板上一眼能看出来；而单工具行会有几十条，
--   删错一个（比如 read_file）系统不报任何错，只是静默少一个工具。
--   代价是组内新增工具自动进 core，这正是「core = 系统能力基线」该有的语义。
--
-- 幂等判据：main 已有 builtin:agent-admin 这一组行即视为已回填，本脚本跳过。
--   Go 运行时与管理端预览**不再**用这个标志判「是否迁移」——那段迁移闸已随存量迁移
--   完成删除（见 shared/models/models.go 里的说明）。这里保留它只为一条：
--   「重跑不会把管理员手工删掉的组加回来」。
--   用**单组标志**而不是「数够 6 条」：管理员可以合法删掉某一组（例如不要 shell），
--   一删就不达标的话判据就失效了。
--   也不能用「有没有任意 core 组」：file / skill 两组在旧口径下可能被单独授过。
--
-- 口径：
--   * 只补 main，不动任何其它 agent；
--   * 全部 required（恒注入）：allow 会被 skill 依赖收窄，常驻集不能用 allow；
--   * item_type='group'，ref='builtin:<组名>'；
--   * 不含 browser（任务能力，按租户显式授 required，用完删）；
--   * 不含 skill-own / skill-admin / tool-grant / custom-tool（跟随技能按需激活）；
--   * 幂等：可重复执行；自动遍历所有含 agents 表的租户 schema。
--
-- 说明：组清单在此**硬编码**，必须与发布时代码里的 models.SupervisorCoreGrantRefs 对齐
--       （有测试 TestSupervisorCoreGrantRefsMatchSQL 守住）。
-- ============================================================================
DO $$
DECLARE
    s    text;
    n    int;
    refs text[] := ARRAY[
        'builtin:file',
        'builtin:exec',
        'builtin:shell',
        'builtin:agent',
        'builtin:skill',
        'builtin:agent-admin'
    ];
    -- 上一版（逐工具）种子的 ref 清单：迁移时清掉，避免与组行重复、也避免
    -- 「删掉一行就收回一个工具」的旧语义残留。
    -- 注意不碰 builtin:browser —— 那是管理员可能显式授过的任务能力。
    legacy text[] := ARRAY[
        'builtin:read_file', 'builtin:write_file', 'builtin:edit_file',
        'builtin:list_files', 'builtin:delete_file', 'builtin:file_exists',
        'builtin:exec', 'builtin:run_skill_script', 'builtin:shell',
        'builtin:list_agents', 'builtin:read_agent', 'builtin:call_agent',
        'builtin:list_skills', 'builtin:read_skill',
        'builtin:create_agent', 'builtin:update_agent', 'builtin:delete_agent',
        'builtin:create_shared_skill', 'builtin:update_shared_skill', 'builtin:delete_shared_skill',
        'builtin:create_agent_skill', 'builtin:update_agent_skill', 'builtin:delete_agent_skill',
        'builtin:bind_skill', 'builtin:unbind_skill',
        'builtin:grant_agent_tool', 'builtin:revoke_agent_tool',
        'builtin:allow_agent_call', 'builtin:revoke_agent_call',
        'builtin:create_custom_tool', 'builtin:update_custom_tool',
        'builtin:delete_custom_tool', 'builtin:list_custom_tools'
    ];
BEGIN
    FOR s IN
        SELECT table_schema
          FROM information_schema.tables
         WHERE table_name = 'agents'
           AND table_schema NOT IN ('pg_catalog', 'information_schema', 'public', 'platform')
         ORDER BY table_schema
    LOOP
        -- 1) 清掉上一版的逐工具 core 行（只删「旧种子清单里的单工具行」，
        --    管理员自己加的 browser / 插件 / 自定义组一概不动）
        EXECUTE format($f$
            DELETE FROM %I.agent_tool_grants g
             USING %I.agents a
             WHERE g.agent_id = a.id
               AND lower(a.name) = 'main'
               AND g.item_type = 'tool'
               AND g.ref = ANY ($1)
               -- 已迁移过就整体跳过（见文件头的单组标志）
               AND NOT EXISTS (
                     SELECT 1 FROM %I.agent_tool_grants m
                      WHERE m.agent_id = a.id AND m.ref = 'builtin:agent-admin'
                   )
        $f$, s, s, s) USING legacy;

        -- 2) 落六个组行
        EXECUTE format($f$
            INSERT INTO %I.agent_tool_grants (agent_id, grant_kind, item_type, ref, updated_by, updated_at)
            SELECT a.id, 'required', 'group', t.ref, NULL, now()
              FROM %I.agents a
             CROSS JOIN unnest($1) AS t(ref)
             WHERE lower(a.name) = 'main'
               AND NOT EXISTS (
                     SELECT 1 FROM %I.agent_tool_grants m
                      WHERE m.agent_id = a.id AND m.ref = 'builtin:agent-admin'
                   )
            ON CONFLICT (agent_id, grant_kind, item_type, ref) DO NOTHING
        $f$, s, s, s) USING refs;

        GET DIAGNOSTICS n = ROW_COUNT;
        RAISE NOTICE 'backfilled % main core group row(s) in schema %', n, s;
    END LOOP;
END $$;

-- 验证（把 t0 换成目标 schema）：
--   * 六个组行 + 原有的 plugin / 自定义授权；
--   * 若 core 组行数为 0，说明本脚本没执行到该 schema（运行时走保险丝）。
SELECT g.ref, g.grant_kind, g.item_type
  FROM t0.agent_tool_grants g
  JOIN t0.agents a ON a.id = g.agent_id
 WHERE lower(a.name) = 'main'
 ORDER BY g.ref;
