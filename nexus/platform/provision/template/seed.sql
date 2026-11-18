-- ============================================================================
-- 租户创建资源方案 · 预置资产
-- ----------------------------------------------------------------------------
-- 由 provision 在「建表之后」执行，此时 search_path 已指向新租户 schema，
-- 所以这里不带任何 schema 前缀。
--
-- 固定 uuid 是刻意的：授权行 / 绑定行靠它引用，脚本必须每次生成同一张图；
-- 租户之间不冲突（各在自己的 schema 里）。开通是一次性动作，本脚本不幂等。
--
-- 口径与 nexus/sql/main_core_tool_grants.sql、supervisor_on_demand_groups.sql 一致：
--   required 6 组（常驻）+ allow 5 组（随技能按需点亮）+ 四个 authoring 技能的工具依赖
--   + main 绑定这四份技能。那边改组名，这里必须同步改，否则新租户开出来是半残的。
-- ============================================================================

-- main：租户默认的管理入口（supervisor，工具全集，可见全部技能）
INSERT INTO agents
    (id, name, description, config, status, endpoints,
     tools_unrestricted, skill_discovery, is_supervisor)
VALUES
    ('a0000000-0000-4000-8000-000000000001', 'main',
     '租户默认的管理入口：建智能体、建技能、授工具，其余工作交给它调用其它智能体',
     '{}'::jsonb, 1, '[]'::jsonb, true, true, true);

-- 四个受保护技能（租户级：agent_id 为空，restricted=true 表示需显式绑定才可见）
INSERT INTO skills
    (id, name, description, metadata, status, agent_id, restricted, user_invocable)
VALUES
    ('a0000000-0000-4000-8000-000000000011', 'skill-authoring',
     '如何创建与维护技能', '{}'::jsonb, 1, NULL, true, false),
    ('a0000000-0000-4000-8000-000000000012', 'agent-authoring',
     '如何创建与维护智能体', '{}'::jsonb, 1, NULL, true, false),
    ('a0000000-0000-4000-8000-000000000013', 'custom-tool-authoring',
     '如何把业务逻辑落成自定义工具', '{}'::jsonb, 1, NULL, true, false),
    ('a0000000-0000-4000-8000-000000000014', 'flow-authoring',
     '如何创建与维护工作流，并发布为 mcp / rpc 端点', '{}'::jsonb, 1, NULL, true, false);

-- main 的工具授权：6 组常驻（required）+ 5 组按技能点亮（allow）
INSERT INTO agent_tool_grants (agent_id, grant_kind, item_type, ref) VALUES
    ('a0000000-0000-4000-8000-000000000001', 'required', 'group', 'builtin:file'),
    ('a0000000-0000-4000-8000-000000000001', 'required', 'group', 'builtin:exec'),
    ('a0000000-0000-4000-8000-000000000001', 'required', 'group', 'builtin:shell'),
    ('a0000000-0000-4000-8000-000000000001', 'required', 'group', 'builtin:agent'),
    ('a0000000-0000-4000-8000-000000000001', 'required', 'group', 'builtin:skill'),
    ('a0000000-0000-4000-8000-000000000001', 'required', 'group', 'builtin:agent-admin'),
    ('a0000000-0000-4000-8000-000000000001', 'allow',    'group', 'builtin:skill-own'),
    ('a0000000-0000-4000-8000-000000000001', 'allow',    'group', 'builtin:skill-admin'),
    ('a0000000-0000-4000-8000-000000000001', 'allow',    'group', 'builtin:tool-grant'),
    ('a0000000-0000-4000-8000-000000000001', 'allow',    'group', 'builtin:custom-tool'),
    ('a0000000-0000-4000-8000-000000000001', 'allow',    'group', 'builtin:flow-admin');

-- 技能的工具依赖（技能只"提出需求"，真正授权仍在上面的 allow 五条里）
INSERT INTO skill_tool_grants (skill_id, item_type, ref) VALUES
    ('a0000000-0000-4000-8000-000000000011', 'group', 'builtin:skill-own'),
    ('a0000000-0000-4000-8000-000000000011', 'group', 'builtin:skill-admin'),
    ('a0000000-0000-4000-8000-000000000012', 'group', 'builtin:tool-grant'),
    ('a0000000-0000-4000-8000-000000000012', 'group', 'builtin:skill-admin'),
    ('a0000000-0000-4000-8000-000000000013', 'group', 'builtin:custom-tool'),
    ('a0000000-0000-4000-8000-000000000014', 'group', 'builtin:flow-admin');

-- main 绑定这四份技能（起步套件：system prompt 里会带上它们的说明）
INSERT INTO agent_skill_bindings (agent_id, skill_id) VALUES
    ('a0000000-0000-4000-8000-000000000001', 'a0000000-0000-4000-8000-000000000011'),
    ('a0000000-0000-4000-8000-000000000001', 'a0000000-0000-4000-8000-000000000012'),
    ('a0000000-0000-4000-8000-000000000001', 'a0000000-0000-4000-8000-000000000013'),
    ('a0000000-0000-4000-8000-000000000001', 'a0000000-0000-4000-8000-000000000014');
