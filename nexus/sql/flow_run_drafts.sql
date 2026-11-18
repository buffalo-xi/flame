-- ============================================================================
-- flow_run_records.drafts：本次运行跟随过的草稿实体清单
-- ----------------------------------------------------------------------------
-- 调试会话可以跟随依赖的草稿（flow → flow）。既然跑的是工作副本，内容随时会变，
-- 事后必须能回答「这次跑的是哪几份草稿」，否则调试结果不可复现。
--
-- 缺省是空数组 []；正式运行永远为空（正式入口没有 DebugScope）。
-- 幂等：ADD COLUMN IF NOT EXISTS。
-- ============================================================================
DO $$
DECLARE
  s text;
BEGIN
  FOR s IN
    SELECT table_schema FROM information_schema.tables
     WHERE table_name = 'flow_run_records'
       AND table_schema NOT IN ('pg_catalog', 'information_schema', 'public', 'platform')
     ORDER BY table_schema
  LOOP
    EXECUTE format(
      'ALTER TABLE %I.flow_run_records
         ADD COLUMN IF NOT EXISTS drafts jsonb NOT NULL DEFAULT ''[]''::jsonb', s);
    RAISE NOTICE '%: flow_run_records.drafts OK', s;
  END LOOP;
END $$;

-- 校验：列已存在，且历史记录都是空数组
SELECT table_schema, column_name, data_type
  FROM information_schema.columns
 WHERE table_name = 'flow_run_records' AND column_name = 'drafts'
 ORDER BY table_schema;
