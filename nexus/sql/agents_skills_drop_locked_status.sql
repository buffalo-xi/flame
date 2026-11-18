-- ============================================================================
-- agent / skill 取消「锁定」态（status=2）：三态 → 两态（1 正常 / 3 废弃）
-- ----------------------------------------------------------------------------
-- 为什么取消：agent / skill 的「线上冻结」同样已由版本模型表达（is_current + 发布），
--   改草稿不影响线上，锁定原本要防的「改坏线上」不会发生；剩下的效果只有「禁止改定义」，
--   于是出问题必须先解锁才能改，解锁那一刻定义又可改 —— 把风险让了回来。
--   工作流此前已做同样收敛（见 flows_drop_locked_status.sql）。
--
-- 随之下线：
--   * agents.locked_skill_edit 列 —— 它唯一的语义是「锁定态仍允许写自己名下的私有技能」，
--     锁定态没了，这个开关就没有意义（判据只剩「归属 agent 是否正常态」）；
--   * 管理端 PUT /admin/agents/:id/locked-skill-edit（代码已删，见 admin/agents/handlers.go）。
--
-- 保留的范围：自定义工具仍用 status=2 表示「停用」（老口径，不是冻结定义的「锁定」），
--   本脚本不碰 custom_tools。
--
-- 顺序：先发代码（后端不再接受 status=2），再跑本脚本。
--   代码先发时旧数据里的 status=2 只是不再出现，不影响读取（IsModifiable 对它返回 false，
--   IsRunnable 返回 true，与原来一致）；切到「正常」即可。
--
-- 幂等：UPDATE 只命中 status=2；约束先 DROP IF EXISTS 再 ADD；删列用 IF EXISTS。
-- 执行：psql -h <host> -p <port> -U <user> -d <db> -f nexus/sql/agents_skills_drop_locked_status.sql
--
-- 跑之前看影响面：
--   SELECT 'agents' AS t, status, count(*) FROM <schema>.agents GROUP BY 2
--   UNION ALL
--   SELECT 'skills', status, count(*) FROM <schema>.skills GROUP BY 2 ORDER BY 1, 2;
-- ============================================================================
DO $$
DECLARE
  s text;
  n bigint;
BEGIN
  FOR s IN
    SELECT table_schema FROM information_schema.tables
     WHERE table_name = 'agents'
       AND table_schema NOT IN ('pg_catalog', 'information_schema', 'public', 'platform')
     ORDER BY table_schema
  LOOP
    -- ① 锁定态一律切回「正常」：锁定只冻结定义，不涉及对外，切回不会动端点
    EXECUTE format('UPDATE %I.agents SET status = 1 WHERE status = 2', s);
    GET DIAGNOSTICS n = ROW_COUNT;
    IF n > 0 THEN
      RAISE NOTICE '% 有 % 条锁定智能体已切回正常', s, n;
    END IF;

    EXECUTE format('ALTER TABLE %I.agents DROP CONSTRAINT IF EXISTS agents_status_check', s);
    EXECUTE format('ALTER TABLE %I.agents ADD CONSTRAINT agents_status_check CHECK (status = ANY (ARRAY[1, 3]))', s);
    EXECUTE format($c$COMMENT ON COLUMN %I.agents.status IS '1:正常,3:废弃（不设「锁定」：线上由版本管理，废弃自动收回端点）'$c$, s);

    -- ② 开关列下线
    EXECUTE format('ALTER TABLE %I.agents DROP COLUMN IF EXISTS locked_skill_edit', s);

    -- ③ skill 侧同样两态（skills 本身没有 CHECK 约束，取值合法性由后端校验；
    --    这里只清数据 + 对齐列注释，保持「迁移过的库」与「新建租户」同构）
    IF to_regclass(format('%I.skills', s)) IS NOT NULL THEN
      EXECUTE format('UPDATE %I.skills SET status = 1 WHERE status = 2', s);
      GET DIAGNOSTICS n = ROW_COUNT;
      IF n > 0 THEN
        RAISE NOTICE '% 有 % 条锁定技能已切回正常', s, n;
      END IF;
      EXECUTE format($c$COMMENT ON COLUMN %I.skills.status IS '1:正常,3:废弃（skill 无端点；不设「锁定」）'$c$, s);
    END IF;

    RAISE NOTICE '% 已完成：agents.status / skills.status ∈ {1,3}，已删 agents.locked_skill_edit', s;
  END LOOP;
END $$;

-- ============================================================================
-- 执行后核对：
--   SELECT status, count(*) FROM <schema>.agents GROUP BY 1 ORDER BY 1;  -- 期望只有 1 / 3
--   SELECT status, count(*) FROM <schema>.skills GROUP BY 1 ORDER BY 1;  -- 期望只有 1 / 3
--   SELECT table_schema FROM information_schema.columns
--    WHERE table_name = 'agents' AND column_name = 'locked_skill_edit';  -- 期望 0 行
-- ============================================================================
