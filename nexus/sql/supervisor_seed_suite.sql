-- ============================================================================
-- supervisor 起步套件回填（存量数据）
--
-- 背景：授予超级权限现在会自动补齐「起步套件」（见 agents.Repository.SetSupervisor
-- 与 promoteSupervisor）：
--   ① agents.tools_unrestricted / skill_discovery 置 true
--   ② 「必须导入」里的默认单工具种子 → 六个 core 组（含 builtin:agent-admin）
--   ③ 绑定 agent-authoring / skill-authoring / custom-tool-authoring / flow-authoring
--
-- 但已存在的 supervisor 不会再触发那段逻辑（管理端只在「值真的变了」时才调
-- SetSupervisor，而 main 又永远不允许降级后重授），所以需要本脚本回填一次。
--
-- 行为：
--   * 只处理 is_supervisor = true 的 agent；
--   * 仅当「必须导入」**恰好等于**普通 agent 的默认种子（一条不多、一条不少）时，
--     才整批替换成 core 组；已经定制过（增删过）的只追加 core 组，绝不删除；
--   * 技能绑定用 ON CONFLICT DO NOTHING，重复执行安全；
--   * 幂等：第二次执行时 required 已是 6 个组，不再满足「等于 12 条种子」，只做幂等追加。
--
-- 与 Go 侧的一致性：种子清单来自 shared/models.DefaultAgentToolGrants，
-- core 清单来自 shared/models.SupervisorCoreGrantRefs，技能来自 SupervisorSeedSkills。
-- 改那三处要同步本文件（与 sql/main_core_tool_grants.sql 同一约定）。
-- ============================================================================
DO $$
DECLARE
    s     text;
    a     record;
    n     integer;
    hit   integer;
    r     text;
    seed  text[] := ARRAY[
        'builtin:read_file', 'builtin:list_files', 'builtin:file_exists',
        'builtin:write_file', 'builtin:edit_file', 'builtin:delete_file',
        'builtin:list_agents', 'builtin:read_agent', 'builtin:list_skills',
        'builtin:read_skill', 'builtin:use_skill', 'builtin:call_agent'
    ];
    core  text[] := ARRAY[
        'builtin:file', 'builtin:exec', 'builtin:shell',
        'builtin:agent', 'builtin:skill', 'builtin:agent-admin'
    ];
    seed_skills text[] := ARRAY[
        'agent-authoring', 'skill-authoring', 'custom-tool-authoring', 'flow-authoring'
    ];
BEGIN
    FOR s IN
        SELECT table_schema
          FROM information_schema.tables
         WHERE table_name = 'agents'
           AND table_schema NOT IN ('pg_catalog', 'information_schema', 'public', 'platform')
         ORDER BY table_schema
    LOOP
        FOR a IN EXECUTE format(
            'SELECT id, name FROM %I.agents WHERE is_supervisor = true', s)
        LOOP
            -- ① 两个开关
            EXECUTE format(
                'UPDATE %I.agents
                    SET tools_unrestricted = true, skill_discovery = true, updated_at = now()
                  WHERE id = $1', s)
              USING a.id;

            -- ② 默认单工具种子 → core 组（严格相等才替换）
            EXECUTE format(
                'SELECT count(*), count(*) FILTER (WHERE ref = ANY($2::text[]))
                   FROM %I.agent_tool_grants
                  WHERE agent_id = $1 AND grant_kind = ''required''', s)
              INTO n, hit USING a.id, seed;

            IF n = array_length(seed, 1) AND hit = array_length(seed, 1) THEN
                EXECUTE format(
                    'DELETE FROM %I.agent_tool_grants
                      WHERE agent_id = $1 AND grant_kind = ''required''', s)
                  USING a.id;
                RAISE NOTICE 'supervisor % (%.%): 默认单工具种子 → core 组', a.name, s, a.name;
            END IF;

            FOREACH r IN ARRAY core LOOP
                EXECUTE format(
                    'INSERT INTO %I.agent_tool_grants
                        (agent_id, grant_kind, item_type, ref, updated_at)
                     VALUES ($1, ''required'', ''group'', $2, now())
                     ON CONFLICT DO NOTHING', s)
                  USING a.id, r;
            END LOOP;

            -- ③ 四份 authoring 技能（skill 不存在则跳过，不阻断）
            FOREACH r IN ARRAY seed_skills LOOP
                EXECUTE format(
                    'INSERT INTO %I.agent_skill_bindings (agent_id, skill_id)
                     SELECT $1, id FROM %I.skills
                      WHERE agent_id IS NULL AND name = $2 AND status <> 3
                     ON CONFLICT DO NOTHING', s, s)
                  USING a.id, r;
            END LOOP;
        END LOOP;
    END LOOP;
END $$;
