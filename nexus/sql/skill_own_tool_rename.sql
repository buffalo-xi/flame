-- ============================================================================
-- skill 私有工具改名：builtin:{create,update,delete}_skill → builtin:*_own_skill
-- ----------------------------------------------------------------------------
-- 背景：
--   * 「建 / 改 / 删 skill」按空间拆成两套工具：私有加 own 后缀，共享用 shared 后缀
--     （create_own_skill / create_shared_skill …），命名对称，减少模型在近似同名工具间误选。
--   * 工具名是授权 ref 的组成部分，改名后存量 ref 会变成「引用无匹配工具」
--     （toolgrant 解析时找不到对应 ToolInfo → warning + 该工具静默消失），必须同步改写。
--
-- 覆盖三张表（ref 的全部落库位置，见 toolgrants/postgres.go 的 CountRefUsages）：
--   agent_tool_grants   PK (agent_id, grant_kind, item_type, ref)
--   skill_tool_grants   PK (skill_id, item_type, ref)   —— skill 声明的工具依赖
--   tool_group_items    PK (group_id, item_type, ref)   —— 自定义组成员
--
-- 顺序：与代码发布同批执行即可。窗口期内只有「存量非超级智能体 agent」会短暂缺这三个工具
--       （main 走「零授权 = 全部内置工具」分支，压根不读 DB，不受影响）。
--
-- 幂等：可重复执行；自动遍历所有含 agent_tool_grants 表的租户 schema。
-- 冲突：先删「旧新并存」的旧行再改，避免 PK 冲突（新名字理论上不存在，但不依赖这个假设）。
-- ============================================================================
DO $$
DECLARE
    s text;
    n int;
BEGIN
    FOR s IN
        SELECT table_schema
          FROM information_schema.tables
         WHERE table_name = 'agent_tool_grants'
           AND table_schema NOT IN ('pg_catalog', 'information_schema', 'public', 'platform')
         ORDER BY table_schema
    LOOP
        -- 1) agent 工具授权
        EXECUTE format($f$
            DELETE FROM %I.agent_tool_grants o
             WHERE o.item_type = 'tool'
               AND o.ref IN ('builtin:create_skill', 'builtin:update_skill', 'builtin:delete_skill')
               AND EXISTS (
                     SELECT 1 FROM %I.agent_tool_grants n
                      WHERE n.agent_id   = o.agent_id
                        AND n.grant_kind = o.grant_kind
                        AND n.item_type  = o.item_type
                        AND n.ref        = replace(o.ref, '_skill', '_own_skill'))
        $f$, s, s);

        EXECUTE format($f$
            UPDATE %I.agent_tool_grants
               SET ref = replace(ref, '_skill', '_own_skill'), updated_at = now()
             WHERE item_type = 'tool'
               AND ref IN ('builtin:create_skill', 'builtin:update_skill', 'builtin:delete_skill')
        $f$, s);
        GET DIAGNOSTICS n = ROW_COUNT;
        RAISE NOTICE 'agent_tool_grants: % row(s) renamed in schema %', n, s;

        -- 2) skill 工具依赖
        IF to_regclass(format('%I.skill_tool_grants', s)) IS NOT NULL THEN
            EXECUTE format($f$
                DELETE FROM %I.skill_tool_grants o
                 WHERE o.item_type = 'tool'
                   AND o.ref IN ('builtin:create_skill', 'builtin:update_skill', 'builtin:delete_skill')
                   AND EXISTS (
                         SELECT 1 FROM %I.skill_tool_grants n
                          WHERE n.skill_id  = o.skill_id
                            AND n.item_type = o.item_type
                            AND n.ref       = replace(o.ref, '_skill', '_own_skill'))
            $f$, s, s);

            EXECUTE format($f$
                UPDATE %I.skill_tool_grants
                   SET ref = replace(ref, '_skill', '_own_skill')
                 WHERE item_type = 'tool'
                   AND ref IN ('builtin:create_skill', 'builtin:update_skill', 'builtin:delete_skill')
            $f$, s);
            GET DIAGNOSTICS n = ROW_COUNT;
            RAISE NOTICE 'skill_tool_grants: % row(s) renamed in schema %', n, s;
        END IF;

        -- 3) 自定义组成员
        IF to_regclass(format('%I.tool_group_items', s)) IS NOT NULL THEN
            EXECUTE format($f$
                DELETE FROM %I.tool_group_items o
                 WHERE o.item_type = 'tool'
                   AND o.ref IN ('builtin:create_skill', 'builtin:update_skill', 'builtin:delete_skill')
                   AND EXISTS (
                         SELECT 1 FROM %I.tool_group_items n
                          WHERE n.group_id  = o.group_id
                            AND n.item_type = o.item_type
                            AND n.ref       = replace(o.ref, '_skill', '_own_skill'))
            $f$, s, s);

            EXECUTE format($f$
                UPDATE %I.tool_group_items
                   SET ref = replace(ref, '_skill', '_own_skill')
                 WHERE item_type = 'tool'
                   AND ref IN ('builtin:create_skill', 'builtin:update_skill', 'builtin:delete_skill')
            $f$, s);
            GET DIAGNOSTICS n = ROW_COUNT;
            RAISE NOTICE 'tool_group_items: % row(s) renamed in schema %', n, s;
        END IF;
    END LOOP;
END $$;

-- 验证（把 t0 换成目标 schema）：三行都应为 0
SELECT 'agent_tool_grants' AS tbl, count(*) FROM t0.agent_tool_grants
 WHERE ref IN ('builtin:create_skill', 'builtin:update_skill', 'builtin:delete_skill')
UNION ALL
SELECT 'skill_tool_grants', count(*) FROM t0.skill_tool_grants
 WHERE ref IN ('builtin:create_skill', 'builtin:update_skill', 'builtin:delete_skill')
UNION ALL
SELECT 'tool_group_items', count(*) FROM t0.tool_group_items
 WHERE ref IN ('builtin:create_skill', 'builtin:update_skill', 'builtin:delete_skill');
