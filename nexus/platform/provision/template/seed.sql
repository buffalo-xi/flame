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
     'Platform main agent: can create agents, skills and workflows',
     '{}'::jsonb, 1, '[]'::jsonb, true, true, true);

-- 四个受保护技能（租户级：agent_id 为空，restricted=true 表示需显式绑定才可见）
INSERT INTO skills
    (id, name, description, metadata, status, agent_id, restricted, user_invocable)
VALUES
    ('a0000000-0000-4000-8000-000000000011', 'skill-authoring',
     'Read before creating, updating or deleting a skill: choosing the scope (shared vs private), how to write SKILL.md, naming rules, tool usage and common pitfalls',
     '{}'::jsonb, 1, NULL, true, false),
    ('a0000000-0000-4000-8000-000000000012', 'agent-authoring',
     'Read before creating, updating or deleting an agent: naming rules, how to write AGENT.md, how tools and skills are configured, and create_agent / update_agent usage plus common pitfalls',
     '{}'::jsonb, 1, NULL, true, false),
    ('a0000000-0000-4000-8000-000000000013', 'custom-tool-authoring',
     'Turning a fixed piece of business logic into a tenant-level script tool (custom__ prefix): deciding whether a tool is the right fit, argument and output schemas, the code protocol, and a verification checklist. Read before creating or updating a script tool',
     '{}'::jsonb, 1, NULL, true, false),
    ('a0000000-0000-4000-8000-000000000014', 'flow-authoring',
     'How to create and maintain workflows, and publish them as MCP / RPC endpoints',
     '{}'::jsonb, 1, NULL, true, false);

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

-- The LLM provider: DeepSeek over the OpenAI-compatible protocol.
--
-- api_key is intentionally empty: the key belongs to the tenant's own account and is entered once in the console. What is
-- seeded is the address, the protocol and the icon, so nobody has to look them up.
--
-- enabled=false because a keyless provider is not configured yet - it cannot serve a single call, so showing it as
-- "enabled" would be a claim the console cannot back. It also keeps the first edit short: an enabled provider is
-- read-only (its url / key were copied into the gateway record of every model enabled under it), so seeding it enabled
-- would force the operator to disable it before filling in the key.
INSERT INTO llm_providers (id, name, url, protocol, api_key, icon, note, enabled, extra_config)
VALUES
    ('a0000000-0000-4000-8000-000000000021', 'DeepSeek', 'https://api.deepseek.com', 'openai',
     '', '/static/llm/compatible.png', '', false, '{}'::jsonb);

-- 配套模型。name 同时是发给上游的模型名（LiteLLM 侧写作 openai/<name>），gateway_model 是网关里的别名
-- （<schema>:<gateway_model>）；两者一致最省心，上游若换了名字只需改 name。
-- extra_params 关掉深度思考，让普通对话走快路径；context_window 按 120k token 预填，实际可用的窗口以账户为准。
-- is_default=false：没有 key 之前不该占着默认位置，否则所有调用都会失败。
INSERT INTO llm_models (id, name, provider_id, gateway_model, is_default, context_window, extra_params,
                        capabilities, base_params)
VALUES
    ('a0000000-0000-4000-8000-000000000022', 'deepseek-flash',
     'a0000000-0000-4000-8000-000000000021', 'deepseek-flash', false, 120000,
     '{"thinking": {"type": "disabled"}}'::jsonb, '{}'::jsonb, '{}'::jsonb);
