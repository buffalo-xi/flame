-- ============================================================================
-- 工作流取消「锁定」态（status=2）：三态 → 两态（1 正常 / 3 废弃）
-- ----------------------------------------------------------------------------
-- 为什么取消：工作流的「线上冻结」已由版本模型表达 ——
--   线上只跑 is_current 那一版，改草稿不影响线上，发布才生效。
--   锁定原本要防的「改坏线上」天然不会发生；剩下的效果只有「禁止发布」，
--   于是出问题必须先解锁才能发修复版，解锁那一刻定义又可改 —— 把风险让了回来。
-- 端点对外与状态解耦，也不依赖锁定态。
--
-- 后续：agent / skill 也已做同样收敛（见 agents_skills_drop_locked_status.sql），
--   三张实体表的 status 现在都是两态（1 正常 / 3 废弃）。
--
-- 顺序：先发代码（后端不再接受 status=2），再跑本脚本。
--   代码先发时旧数据里的 status=2 只是不再出现，不影响读取（IsModifiable 对它返回 false，
--   IsRunnable 返回 true，与原来一致）；切到「正常」即可。
--
-- 幂等：UPDATE 只命中 status=2；约束先 DROP IF EXISTS 再 ADD。
-- 执行：psql -h <host> -p <port> -U <user> -d <db> -f nexus/sql/flows_drop_locked_status.sql
--
-- 跑之前看影响面：
--   SELECT status, count(*) FROM <schema>.flows GROUP BY 1 ORDER BY 1;
-- ============================================================================
DO $$
DECLARE
  s text;
  n bigint;
BEGIN
  FOR s IN
    SELECT table_schema FROM information_schema.tables
     WHERE table_name = 'flows'
       AND table_schema NOT IN ('pg_catalog', 'information_schema', 'public', 'platform')
     ORDER BY table_schema
  LOOP
    -- 锁定态一律切回「正常」：锁定只冻结定义，不涉及对外，切回不会动端点
    EXECUTE format('UPDATE %I.flows SET status = 1 WHERE status = 2', s);
    GET DIAGNOSTICS n = ROW_COUNT;
    IF n > 0 THEN
      RAISE NOTICE '% 有 % 条锁定流程已切回正常', s, n;
    END IF;

    EXECUTE format('ALTER TABLE %I.flows DROP CONSTRAINT IF EXISTS flow_instances_status_check', s);
    EXECUTE format('ALTER TABLE %I.flows ADD CONSTRAINT flow_instances_status_check CHECK (status = ANY (ARRAY[1, 3]))', s);
    EXECUTE format($c$COMMENT ON COLUMN %I.flows.status IS '1:正常,3:废弃（工作流不设「锁定」：线上由版本管理，废弃自动收回端点）'$c$, s);

    RAISE NOTICE '% 已完成：status ∈ {1,3}', s;
  END LOOP;
END $$;

-- ============================================================================
-- 执行后核对：
--   SELECT status, count(*) FROM <schema>.flows GROUP BY 1 ORDER BY 1;  -- 期望只有 1 / 3
-- ============================================================================
