-- ============================================================================
-- supervisor 起步套件自检（只读，不改任何数据）
--
-- 用途：在执行 sql/supervisor_seed_suite.sql 之前 / 之后各跑一次。
--
--   * 第 1 段：列出所有 supervisor 的三项现状（开关、required 组、authoring 技能）。
--   * 第 2 段：统计「有授权行但缺 builtin:agent-admin 组」的 supervisor 数量。
--     这个数在执行 supervisor_seed_suite.sql 之前应当归零；归零后才可以认为
--     运行时的「按表解析」不会让任何 supervisor 掉工具。
--
-- 判定口径与 Go 侧一致：
--   core 组齐全 = required 里含 builtin:agent-admin（自举组，见 models.SupervisorCoreGrantRefs）
--   起步技能   = agent-authoring / skill-authoring / custom-tool-authoring / flow-authoring
-- ============================================================================
DO $$
DECLARE
    s     text;
    r     record;
    n     int;
    total int := 0;
BEGIN
    FOR s IN
        SELECT table_schema
          FROM information_schema.tables
         WHERE table_name = 'agents'
           AND table_schema NOT IN ('pg_catalog', 'information_schema', 'public', 'platform')
         ORDER BY table_schema
    LOOP
        RAISE NOTICE '---- schema: % ----', s;

        -- 1) 清单：每个 supervisor 的三项现状
        FOR r IN EXECUTE format($f$
            SELECT a.name,
                   a.tools_unrestricted,
                   a.skill_discovery,
                   (SELECT count(*) FROM %I.agent_tool_grants g
                     WHERE g.agent_id = a.id AND g.grant_kind = 'required')            AS required_rows,
                   EXISTS (SELECT 1 FROM %I.agent_tool_grants g
                            WHERE g.agent_id = a.id AND g.grant_kind = 'required'
                              AND g.ref = 'builtin:agent-admin')                        AS has_core_group,
                   (SELECT count(*) FROM %I.agent_skill_bindings b
                      JOIN %I.skills sk ON sk.id = b.skill_id
                     WHERE b.agent_id = a.id
                       AND sk.name IN ('agent-authoring', 'skill-authoring', 'custom-tool-authoring', 'flow-authoring'))
                                                                                        AS seed_skills
              FROM %I.agents a
             WHERE a.is_supervisor
             ORDER BY a.name
        $f$, s, s, s, s, s)
        LOOP
            RAISE NOTICE '  %: unrestricted=% discovery=% required=% core_group=% seed_skills=%/4',
                r.name, r.tools_unrestricted, r.skill_discovery, r.required_rows,
                r.has_core_group, r.seed_skills;
        END LOOP;

        -- 2) 统计：有授权行但没有 core 组（按表解析会掉工具的那一类）
        EXECUTE format($f$
            SELECT count(*) FROM %I.agents a
             WHERE a.is_supervisor
               AND EXISTS (SELECT 1 FROM %I.agent_tool_grants g WHERE g.agent_id = a.id)
               AND NOT EXISTS (SELECT 1 FROM %I.agent_tool_grants m
                                WHERE m.agent_id = a.id AND m.ref = 'builtin:agent-admin')
        $f$, s, s, s) INTO n;

        IF n > 0 THEN
            RAISE WARNING 'schema % 仍有 % 个 supervisor 未物化 core 组（按表解析会掉工具）', s, n;
            total := total + n;
        END IF;
    END LOOP;

    IF total = 0 THEN
        RAISE NOTICE '==== 全部 supervisor 均已物化 core 组：合计 0 ====';
    ELSE
        RAISE WARNING '==== 未物化 core 组的 supervisor 合计：% ====', total;
    END IF;
END $$;
