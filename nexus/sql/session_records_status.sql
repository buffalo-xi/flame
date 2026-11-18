-- ============================================================================
-- 会话记录补「结果状态」：session_records.status / error
--
-- 背景：失败或被打断的一轮在历史里是「一行空白回答」，用户无法区分
--   是自己点了停止、模型挂了、还是工具报错 —— 只能来问人。
--
--   status: ok=正常完成 / stopped=被主动终止 / failed=出错
--   error : 人话原因（来自 agent 的 SetError），status=ok 时为空
--
-- **只作用于 t0**：当前只有一个租户。将来有更多租户时，按下面的模式对每个
-- {schema} 各跑一遍（把 t0 换掉即可）。
--
-- 幂等：可重复执行（先查 information_schema 再 ALTER）。
-- 存量行：status 由列默认值补成 'ok' —— 历史事实未知，不猜成 failed。
-- PG 11+ 带常量默认值的 ADD COLUMN 不重写表，可直接在运行中的库执行。
--
-- 回滚：见文件末尾。
-- ============================================================================

BEGIN;

DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.columns
     WHERE table_schema = 't0' AND table_name = 'session_records' AND column_name = 'status'
  ) THEN
    ALTER TABLE t0.session_records
      ADD COLUMN status character varying(16) DEFAULT 'ok'::character varying NOT NULL;
  END IF;

  IF NOT EXISTS (
    SELECT 1 FROM information_schema.columns
     WHERE table_schema = 't0' AND table_name = 'session_records' AND column_name = 'error'
  ) THEN
    ALTER TABLE t0.session_records ADD COLUMN error text;
  END IF;
END $$;

COMMENT ON COLUMN t0.session_records.status IS '本轮结果：ok=正常完成 / stopped=被主动终止 / failed=出错（原因见 error）';
COMMENT ON COLUMN t0.session_records.error IS '失败或终止的人话原因；status=ok 时为空';

COMMIT;

-- ============================================================================
-- 执行后核对
-- ============================================================================
-- a) 两列都在，且 status 有默认值、带 NOT NULL
--   SELECT column_name, data_type, is_nullable, column_default
--     FROM information_schema.columns
--    WHERE table_schema='t0' AND table_name='session_records'
--      AND column_name IN ('status','error');
--
-- b) 存量行都补成了 ok（数量应等于改动前总行数）
--   SELECT status, count(*) FROM t0.session_records GROUP BY status;
--
-- c) 新写入的一轮带上状态（跑一次 agent 后看最新一行）
--   SELECT status, left(error, 60), output = '' AS empty_output, created_at
--     FROM t0.session_records ORDER BY created_at DESC LIMIT 5;

-- ============================================================================
-- 回滚（代码需同步回退：models.SessionRecord 的 Status/Error 与
--        repository/sessionrecords 的 SQL 列清单）
-- ============================================================================
-- BEGIN;
-- ALTER TABLE t0.session_records DROP COLUMN IF EXISTS error;
-- ALTER TABLE t0.session_records DROP COLUMN IF EXISTS status;
-- COMMIT;
