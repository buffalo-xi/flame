-- ============================================================================
-- supervisor 的「按需能力」：allow 授权 + 技能工具依赖
-- ----------------------------------------------------------------------------
-- 目的：把 supervisor 的上下文从 37 个工具压到 19 个（常驻 6 组），其余五组按需点亮。
--   常驻（required，见 sql/main_core_tool_grants.sql）：
--     file / exec / shell / agent / skill / agent-admin
--   按需（本脚本，allow + 技能依赖）：
--     skill-own / skill-admin       ← use_skill(skill-authoring)
--     tool-grant（+ skill-admin）   ← use_skill(agent-authoring)
--     custom-tool                   ← use_skill(custom-tool-authoring)
--     flow-admin                    ← use_skill(flow-authoring)
--
-- 生效链路（缺一环就静默不生效，read_agent 的 effective/via_skill 可查）：
--   required 无此组 → allow 有此组 → 某个**本轮已激活**（use_skill 过）的技能在
--   skill_tool_grants 里声明了它 → 候选 ∩ allow → 生效
--
-- 两件事：
--   1. 给**所有 supervisor** 落五条 allow 组行（判据 is_supervisor，不是 name='main'：
--      租户可以再配出同等权限的 agent，只给 main 落会让新 supervisor 在关掉
--      「不限制工具」后立刻失去这几类能力）
--   2. 给四个 *-authoring 技能写工具依赖
--
-- 历史：这里曾有第 3 步「摘掉 main 对三个 authoring 技能的绑定」。那个前提
--   （「技能依赖随绑定永久生效」）在 D28「用过即注入」之后已不成立：现在绑定只决定
--   system prompt 里有没有这条注释，**不产生工具候选**，use_skill 才进 schema 且 run
--   结束即收。加上 supervisor 起步套件会主动绑定这三份技能，再摘就是互相拆台，故删除。
--
-- 幂等：可重复执行。
-- ============================================================================
DO $$
DECLARE
    s text;
    n int;
BEGIN
    FOR s IN
        SELECT table_schema
          FROM information_schema.tables
         WHERE table_name = 'agents'
           AND table_schema NOT IN ('pg_catalog', 'information_schema', 'public', 'platform')
         ORDER BY table_schema
    LOOP
        -- 1) supervisor 的「可使用」：五个按需组
        --    判据用 is_supervisor：内置 main 与后续授予的走同一套（见 agents.CreateAgent /
        --    promoteSupervisor）。这些行在「不限制工具」开启时不参与判定（候选直接给），
        --    但它们是关掉该开关后唯一的生效依据，也是管理端面板上「这些能力存在」的记录。
        EXECUTE format($f$
            INSERT INTO %I.agent_tool_grants (agent_id, grant_kind, item_type, ref, updated_by, updated_at)
            SELECT a.id, 'allow', 'group', t.ref, NULL, now()
              FROM %I.agents a
             CROSS JOIN unnest($1) AS t(ref)
             WHERE a.is_supervisor
            ON CONFLICT (agent_id, grant_kind, item_type, ref) DO NOTHING
        $f$, s, s)
        USING ARRAY['builtin:skill-own', 'builtin:skill-admin',
                    'builtin:tool-grant', 'builtin:custom-tool',
                    'builtin:flow-admin'];
        GET DIAGNOSTICS n = ROW_COUNT;
        RAISE NOTICE 'allow grants: % row(s) in schema %', n, s;

        -- 2) 四个 authoring 技能的工具依赖（技能只能「提出需求」，授权仍在 allow 里）
        EXECUTE format($f$
            INSERT INTO %I.skill_tool_grants (skill_id, item_type, ref)
            SELECT sk.id, 'group', d.dep
              FROM %I.skills sk
             CROSS JOIN (VALUES
                    ('skill-authoring',      'builtin:skill-own'),
                    ('skill-authoring',      'builtin:skill-admin'),
                    ('agent-authoring',      'builtin:tool-grant'),
                    ('agent-authoring',      'builtin:skill-admin'),
                    ('custom-tool-authoring','builtin:custom-tool'),
                    ('flow-authoring',       'builtin:flow-admin')
                 ) AS d(skill, dep)
             WHERE sk.name = d.skill
               AND sk.agent_id IS NULL          -- 只认租户级技能
               AND NOT EXISTS (
                     SELECT 1 FROM %I.skill_tool_grants g
                      WHERE g.skill_id = sk.id AND g.ref = d.dep
                   )
        $f$, s, s, s);
        GET DIAGNOSTICS n = ROW_COUNT;
        RAISE NOTICE 'skill tool deps: % row(s) in schema %', n, s;

        -- 3) 不摘 supervisor 对三个 authoring 技能的绑定。
        --
        --    原第 3 步的依据是「技能依赖随绑定永久生效，不摘就省不下上下文」——这个前提
        --    在 D28「用过即注入」之后已不成立：绑定只决定 system prompt 里有没有这条注释，
        --    不产生工具候选；工具要 use_skill 激活才进 schema、run 结束即收。
        --    而 supervisor 起步套件（CreateAgent / promoteSupervisor）会主动绑定这三份技能，
        --    在这里 DELETE 等于自己把起步套件拆掉，故整步移除。
    END LOOP;
END $$;

-- 验证
--   * supervisor 的 required：6 个 core 组（+ 管理员显式加的 browser / 插件）
--   * supervisor 的 allow：5 个按需组
--   * 四个 authoring 技能各有依赖
--   * supervisor 绑着这四份技能（起步套件的结果，不是 bug）
SELECT 'grants' AS what, g.grant_kind, g.ref
  FROM t0.agent_tool_grants g JOIN t0.agents a ON a.id = g.agent_id
 WHERE a.is_supervisor
UNION ALL
SELECT 'skill_deps', sk.name, d.ref
  FROM t0.skill_tool_grants d JOIN t0.skills sk ON sk.id = d.skill_id
 WHERE sk.name IN ('skill-authoring', 'agent-authoring', 'custom-tool-authoring', 'flow-authoring')
 ORDER BY 1, 2, 3;
