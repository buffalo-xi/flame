-- ============================================================================
-- 移除旧内置预设组后的存量清理
-- ----------------------------------------------------------------------------
-- 背景：
--   * PresetGroups 只保留 preset:default，原 readonly / file-write / exec / collaborate 已删。
--   * 存量若有引用它们的行（agent 授权 / 自定义工具组成员 / skill 工具依赖），
--     ToolsOfRef 会解析为空 → 只记 warning、不报错，但**那些工具会静默消失**。
--   * 本脚本把旧预设引用展开成等价的 builtin:<tool> 行（保留原 grant_kind），再删原行。
--
-- 映射（与旧定义一致）：
--   preset:readonly    → read_file, list_files, file_exists, list_agents, read_agent, list_skills, read_skill
--   preset:file-write  → write_file, edit_file, delete_file
--   preset:exec        → exec
--   preset:collaborate → call_agent
--
-- 幂等：可重复执行；自动遍历所有含 agents 表的租户 schema。
-- ============================================================================
DO $$
DECLARE
    s    text;
    n    int;
    map_sql text := $m$ (VALUES
        ('preset:readonly','read_file'),('preset:readonly','list_files'),
        ('preset:readonly','file_exists'),('preset:readonly','list_agents'),
        ('preset:readonly','read_agent'),('preset:readonly','list_skills'),
        ('preset:readonly','read_skill'),
        ('preset:file-write','write_file'),('preset:file-write','edit_file'),
        ('preset:file-write','delete_file'),
        ('preset:exec','exec'),
        ('preset:collaborate','call_agent')
    ) $m$;
BEGIN
    FOR s IN
        SELECT table_schema
          FROM information_schema.tables
         WHERE table_name = 'agents'
           AND table_schema NOT IN ('pg_catalog', 'information_schema', 'public', 'platform')
         ORDER BY table_schema
    LOOP
        -- 1) agent 工具授权（required / allow / deny 三态都展开）
        EXECUTE format($f$
            INSERT INTO %I.agent_tool_grants (agent_id, grant_kind, item_type, ref, updated_by, updated_at)
            SELECT g.agent_id, g.grant_kind, 'tool', 'builtin:' || m.tool, g.updated_by, now()
              FROM %I.agent_tool_grants g
              JOIN %s AS m(preset, tool) ON m.preset = g.ref
            ON CONFLICT (agent_id, grant_kind, item_type, ref) DO NOTHING
        $f$, s, s, map_sql);
        GET DIAGNOSTICS n = ROW_COUNT;
        RAISE NOTICE 'expanded % agent_tool_grants row(s) in %', n, s;

        -- 2) 自定义工具组成员
        IF to_regclass(format('%I.tool_group_items', s)) IS NOT NULL THEN
            EXECUTE format($f$
                INSERT INTO %I.tool_group_items (group_id, item_type, ref)
                SELECT i.group_id, 'tool', 'builtin:' || m.tool
                  FROM %I.tool_group_items i
                  JOIN %s AS m(preset, tool) ON m.preset = i.ref
                ON CONFLICT (group_id, item_type, ref) DO NOTHING
            $f$, s, s, map_sql);
        END IF;

        -- 3) skill 工具依赖
        IF to_regclass(format('%I.skill_tool_grants', s)) IS NOT NULL THEN
            EXECUTE format($f$
                INSERT INTO %I.skill_tool_grants (skill_id, item_type, ref)
                SELECT i.skill_id, 'tool', 'builtin:' || m.tool
                  FROM %I.skill_tool_grants i
                  JOIN %s AS m(preset, tool) ON m.preset = i.ref
                ON CONFLICT (skill_id, item_type, ref) DO NOTHING
            $f$, s, s, map_sql);
        END IF;

        -- 4) 删除旧预设引用（preset:default 保留）
        EXECUTE format($f$ DELETE FROM %I.agent_tool_grants
            WHERE ref LIKE 'preset:%%' AND ref <> 'preset:default' $f$, s);
        IF to_regclass(format('%I.tool_group_items', s)) IS NOT NULL THEN
            EXECUTE format($f$ DELETE FROM %I.tool_group_items
                WHERE ref LIKE 'preset:%%' AND ref <> 'preset:default' $f$, s);
        END IF;
        IF to_regclass(format('%I.skill_tool_grants', s)) IS NOT NULL THEN
            EXECUTE format($f$ DELETE FROM %I.skill_tool_grants
                WHERE ref LIKE 'preset:%%' AND ref <> 'preset:default' $f$, s);
        END IF;
    END LOOP;
END $$;

-- 验证（把 t0 换成目标 schema）：三行都应为 0
SELECT 'agent_tool_grants' AS tbl, count(*) FROM t0.agent_tool_grants
 WHERE ref LIKE 'preset:%' AND ref <> 'preset:default'
UNION ALL
SELECT 'tool_group_items', count(*) FROM t0.tool_group_items
 WHERE ref LIKE 'preset:%' AND ref <> 'preset:default'
UNION ALL
SELECT 'skill_tool_grants', count(*) FROM t0.skill_tool_grants
 WHERE ref LIKE 'preset:%' AND ref <> 'preset:default';
