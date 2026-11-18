-- ============================================================================
-- agents.is_supervisor：超级权限身份从「名字等于 main」改为「实体带属性」
-- ----------------------------------------------------------------------------
-- 背景：
--   过去「谁是超权 agent」由代码里的名字白名单判定（models.IsMainAgent("main")），
--   意味着租户永远只能有一个超权 agent，且它的名字不能变。
--   现在改为 agents 表上的布尔属性：main 只是**预置**的一个 supervisor，
--   租户可以再配出同等权限的 agent（允许多个）。
--
-- 判据变化（models.IsSupervisor(a *Agent)）：
--   * 缺省 core 工具集（models.SupervisorCoreGrants）
--   * 管理类工具（agent-admin / skill-admin / tool-grant / custom-tool）可用
--   * 跨域写文件（工具层的 checkWrite）
--   * 不可被其它 agent 调用
--   * 授权目录按「全量视角」产出（不再剔除管理组）
--
-- 不动的东西：
--   * models.IsProtectedAgent（名字名单）仍然只含 main —— 它保护的是「预置资产
--     不可删/不可改名」，与超权无关。新建的 supervisor 可以删、可以改名。
--   * 删除保护与身份判据刻意保持正交，见 shared/models/protected.go 的说明。
--
-- 幂等：可重复执行；自动遍历所有含 agents 表的租户 schema。
-- ============================================================================
DO $$
DECLARE
    s text;
BEGIN
    FOR s IN
        SELECT table_schema
          FROM information_schema.tables
         WHERE table_name = 'agents'
           AND table_schema NOT IN ('pg_catalog', 'information_schema', 'public', 'platform')
         ORDER BY table_schema
    LOOP
        EXECUTE format($f$
            ALTER TABLE %I.agents
                ADD COLUMN IF NOT EXISTS is_supervisor bool NOT NULL DEFAULT false
        $f$, s);

        -- 预置：现有 main 升格为 supervisor。只按名字匹配一次，之后完全靠属性。
        EXECUTE format($f$
            UPDATE %I.agents SET is_supervisor = true WHERE LOWER(name) = 'main'
        $f$, s);

        RAISE NOTICE 'agents.is_supervisor (default false) ready in schema: %', s;
    END LOOP;
END $$;

-- 验证：每个租户 schema 都应有该列，且 main 行已置 true
SELECT c.table_schema, c.column_name, c.data_type, c.column_default
  FROM information_schema.columns c
 WHERE c.table_name = 'agents' AND c.column_name = 'is_supervisor'
 ORDER BY c.table_schema;
