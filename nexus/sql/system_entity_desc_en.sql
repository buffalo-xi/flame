-- ============================================================================
-- 系统预置智能体 / 技能的描述改为英文（存量租户回填）
--
-- 为什么要改：这两个字段都会进模型上下文 ——
--   agents.description → system prompt 的「可调用协作的 agent」清单（callableCatalog）
--                        以及 list_agents 的工具回包
--   skills.description → system prompt 的技能清单（skillCatalog）；正式运行读的是
--                        skill_versions 当前行的快照，不是 skills 表的当前值
-- 新租户由 platform/provision/template/seed.sql 直接写入英文，本脚本处理存量 schema。
-- 改措辞时两处必须同改，否则新老租户的描述会分叉。
--
-- 每个租户要改两处，缺一不可：
--   1. 实体表（草稿、管理端列表、list_agents 读它）
--   2. 版本表的 is_current 行（正式运行读的那一份）。只改实体表，线上读到的仍是中文；
--      而且 versioning/state.go 会拿实体行与版本行比较，把这批系统实体判成
--      「有未发布改动」（界面上冒脏标）。
--   只改 is_current 行、不动历史版本：历史快照是「当时发布的内容」，不该被篡改。
--   当前版本就地改描述，等价于「发一版只改元数据的版本」，但不新增版本号。
--
-- 范围（只动系统预置的 5 个实体）：
--   agents → name = 'main'（预置管理入口，受保护不可改名）
--   skills → name IN (四个受保护技能) AND agent_id IS NULL
--            （限定租户级，避免误改某个 agent 私有空间里的同名技能）
--   租户自建的 agent / skill（含 t0 的示例数据）一律不动。
--
-- 幂等：重复执行结果一致；从未发布过版本的实体只更新实体表。
-- ============================================================================
DO $$
DECLARE
    s         text;
    main_desc text := 'Platform main agent: can create agents, skills and workflows';
    skill_names text[] := ARRAY[
        'agent-authoring', 'skill-authoring', 'custom-tool-authoring', 'flow-authoring'
    ];
    -- 与 skill_names 一一对应。文本与 template/seed.sql 逐字相同（新老租户不能分叉），
    -- 内容取存量租户里更详细的那一版，而不是 seed 原来的短句。
    -- custom-tool-authoring 顺带把过时的 script__ 前缀纠正为 custom__（见 models/customtool.go）。
    skill_descs text[] := ARRAY[
        'Read before creating, updating or deleting an agent: naming rules, how to write AGENT.md, how tools and skills are configured, and create_agent / update_agent usage plus common pitfalls',
        'Read before creating, updating or deleting a skill: choosing the scope (shared vs private), how to write SKILL.md, naming rules, tool usage and common pitfalls',
        'Turning a fixed piece of business logic into a tenant-level script tool (custom__ prefix): deciding whether a tool is the right fit, argument and output schemas, the code protocol, and a verification checklist. Read before creating or updating a script tool',
        'How to create and maintain workflows, and publish them as MCP / RPC endpoints'
    ];
BEGIN
    FOR s IN
        SELECT table_schema
          FROM information_schema.tables
         WHERE table_name = 'agents'
           AND table_schema NOT IN ('pg_catalog', 'information_schema', 'public', 'platform')
         ORDER BY table_schema
    LOOP
        -- ① main 的实体行
        EXECUTE format(
            'UPDATE %I.agents SET description = $1, updated_at = now() WHERE name = $2', s)
          USING main_desc, 'main';

        -- ② main 的当前版本快照（从未发布的租户没有这一行，UPDATE 影响 0 行即可）
        IF to_regclass(format('%I.agent_versions', s)) IS NOT NULL THEN
            EXECUTE format(
                'UPDATE %I.agent_versions v SET description = a.description
                   FROM %I.agents a
                  WHERE a.id = v.agent_id AND v.is_current AND a.name = $1', s, s)
              USING 'main';
        END IF;

        -- ③ 四个受保护技能的实体行（按名字批量更新，数组顺序一一对应）
        EXECUTE format(
            'UPDATE %I.skills SET description = d.dsc, updated_at = now()
               FROM unnest($1::text[], $2::text[]) AS d(nm, dsc)
              WHERE skills.name = d.nm AND skills.agent_id IS NULL', s)
          USING skill_names, skill_descs;

        -- ④ 这四个技能的当前版本快照
        IF to_regclass(format('%I.skill_versions', s)) IS NOT NULL THEN
            EXECUTE format(
                'UPDATE %I.skill_versions v SET description = s.description
                   FROM %I.skills s
                  WHERE s.id = v.skill_id AND v.is_current
                    AND s.agent_id IS NULL AND s.name = ANY($1::text[])', s, s)
              USING skill_names;
        END IF;
    END LOOP;
END $$;
