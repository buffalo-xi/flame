-- ============================================================================
-- 三态生命周期：agents / flows / skills 的 status 由「启用(1)/停用(2)」改为「正常/锁定/废弃」
--
-- 口径：
--   1 正常：可修改、可运行 —— 新建默认值
--   2 锁定：**不可修改**、可运行（"线上冻结"）—— 只由手工设置（端点对外开放不会带入，见下）
--   3 废弃：**不可修改、不可运行** —— 只由手工设置
--
-- 存量迁移口径：
--   旧的 2（停用）= "不在跑" → 一律迁到 3（废弃）。
--   代价：它们同时变成"不可修改"，要恢复编辑需先切回「正常」（界面与接口都能切）。
--
-- 联动的自动化由后端实现，不在本脚本里（列 CHECK 只负责取值合法）：
--   端点对外开放(exposed = true) → **不改状态**：对外提供入口 ≠ 定义冻结。
--       早期曾把编辑态自动置 2，代价是改一行提示词要先解锁，而解锁又会收回端点、断流量；
--       现在改为「端点对外后仍可编辑，保存时由编辑器二次确认」（见 svcendpoints.ensureExposableTarget）。
--   状态切「废弃」(3) → 自动收回该目标全部端点对外（exposed = false，记录保留）；
--       切回 1 不收回端点（见 admin/flows 的 SetFlowStatus、admin/agents 的 SetAgentStatus）。
--
-- 幂等：可重复执行（约束按定义探测后重建；UPDATE 只打 status = 2 的行）。
--
-- 执行方式：
--   psql -h <host> -p <port> -U <user> -d <db> -f nexus/sql/tenant_status_three_state.sql
-- ============================================================================
DO $$
DECLARE
  s text;
  t text;
  c record;
BEGIN
  -- 以「有 agents 表的 schema」界定租户 schema
  FOR s IN
    SELECT table_schema FROM information_schema.tables
     WHERE table_name = 'agents'
       AND table_schema NOT IN ('pg_catalog', 'information_schema', 'public', 'platform')
     ORDER BY table_schema
  LOOP
    -- 1) 删掉 status 列上的旧 CHECK 约束。
    --    模板里的约束名并不统一（agents_status_check / flow_instances_status_check / …），
    --    所以按"定义里提到 status"来探测，而不是按名字。
    FOREACH t IN ARRAY ARRAY['agents', 'flows', 'skills']
    LOOP
      FOR c IN
        SELECT conname FROM pg_constraint
         WHERE conrelid = to_regclass(format('%I.%I', s, t))
           AND contype = 'c'
           AND pg_get_constraintdef(oid) ILIKE '%status%'
      LOOP
        EXECUTE format('ALTER TABLE %I.%I DROP CONSTRAINT %I', s, t, c.conname);
      END LOOP;
    END LOOP;

    -- 2) 存量 2（旧的"停用"）→ 3（废弃）
    EXECUTE format('UPDATE %I.agents SET status = 3 WHERE status = 2', s);
    EXECUTE format('UPDATE %I.flows  SET status = 3 WHERE status = 2', s);

    -- 3) 三态 CHECK：只给 agents / flows 加，与开通模板 template/schema.sql 保持一致。
    --    skills 本来就没有 CHECK（它的取值合法性由后端校验），此处**不加**，避免"迁移过的库"
    --    比"新建的租户"多一条约束 —— 两边 schema 必须同构。要加的话先改模板，再放开下面两行注释。
    EXECUTE format('ALTER TABLE %I.agents ADD CONSTRAINT agents_status_check CHECK (status IN (1, 2, 3))', s);
    EXECUTE format('ALTER TABLE %I.flows  ADD CONSTRAINT flow_instances_status_check CHECK (status IN (1, 2, 3))', s);
    -- EXECUTE format('ALTER TABLE %I.skills ADD CONSTRAINT skills_status_check CHECK (status IN (1, 2, 3))', s);

    -- 4) 注释与模板对齐
    EXECUTE format($f$COMMENT ON COLUMN %I.agents.status IS '1:正常,2:锁定,3:废弃（锁定仅手工设置，废弃自动下架端点）'$f$, s);
    EXECUTE format($f$COMMENT ON COLUMN %I.flows.status  IS '1:正常,2:锁定,3:废弃（锁定仅手工设置，废弃自动下架端点）'$f$, s);
    EXECUTE format($f$COMMENT ON COLUMN %I.skills.status IS '1:正常,2:锁定,3:废弃（skill 无端点，锁定仅能手工设置）'$f$, s);
  END LOOP;
END $$;

-- ============================================================================
-- 执行后核对（schema 换成实际值，例如 t0）
-- ============================================================================
-- a) 约束已生效（应为 (status = ANY (ARRAY[1, 2, 3]))）
--   SELECT conname, pg_get_constraintdef(oid)
--     FROM pg_constraint WHERE conrelid = 't0.agents'::regclass AND contype = 'c';
--
-- b) 状态分布（迁移后应无 2；旧的"停用"都变成 3）
--   SELECT status, count(*) FROM t0.agents GROUP BY status ORDER BY status;
--   SELECT status, count(*) FROM t0.flows  GROUP BY status ORDER BY status;
--
-- c) 一致性自检：状态=废弃(3) 却还有已发布端点 —— 应为 0 行
--    （切废弃时后端会自动下架；有行说明走了手工改库或收回失败）
--   SELECT id, name FROM t0.agents
--    WHERE status = 3 AND endpoints @> '[{"exposed": true}]'::jsonb;
--   SELECT id, name FROM t0.flows
--    WHERE status = 3 AND endpoints @> '[{"exposed": true}]'::jsonb;
