-- ============================================================================
-- 端点域术语统一：published → exposed（对外开放 / 收回对外）
--
-- 背景：界面上一律叫「对外开放 / 收回对外」，代码里的符号与库里的列名跟界面同词。
--   动作：expose / retract        状态：svc_endpoints.exposed (bool)
--   JSON：agents.endpoints / flows.endpoints 镜像列里的 {"exposed": true}
--
-- **只作用于 t0**：当前只有一个租户。将来有更多租户时，按下面的模式对每个
-- {schema} 各跑一遍（把 t0 换掉即可）。
--
-- 顺序要点：先重写镜像 JSONB 的键，再改列名。只改列名会留下 {"published":true}，
-- 而模型只认 "exposed"，界面会显示成「未对外开放」。
--
-- 幂等：可重复执行（JSONB 那步有 LIKE 过滤，列改名需先确认旧列还在）。
--
-- 回滚：见文件末尾。
-- ============================================================================

BEGIN;

-- 1) 镜像列：agents.endpoints / flows.endpoints
--    [{"protocol":"mcp","published":true}] → [{"protocol":"mcp","exposed":true}]
UPDATE t0.agents
   SET endpoints = (
         SELECT jsonb_agg(jsonb_build_object(
                  'protocol', e->>'protocol',
                  'exposed',  COALESCE((e->>'exposed')::boolean, (e->>'published')::boolean)))
           FROM jsonb_array_elements(endpoints) e)
 WHERE endpoints IS NOT NULL
   AND endpoints::text LIKE '%"published"%';

UPDATE t0.flows
   SET endpoints = (
         SELECT jsonb_agg(jsonb_build_object(
                  'protocol', e->>'protocol',
                  'exposed',  COALESCE((e->>'exposed')::boolean, (e->>'published')::boolean)))
           FROM jsonb_array_elements(endpoints) e)
 WHERE endpoints IS NOT NULL
   AND endpoints::text LIKE '%"published"%';

-- 2) 列改名（已改过则跳过，保证可重复执行）
DO $$
BEGIN
  IF EXISTS (
    SELECT 1 FROM information_schema.columns
     WHERE table_schema = 't0' AND table_name = 'svc_endpoints' AND column_name = 'published'
  ) THEN
    ALTER TABLE t0.svc_endpoints RENAME COLUMN published TO exposed;
  END IF;
END $$;

COMMENT ON COLUMN t0.svc_endpoints.exposed IS '对外开放开关：true=租户外可调用（外部凭证 / MCP 注册），false=仅租户内部可用';

COMMIT;

-- ============================================================================
-- 执行后核对
-- ============================================================================
-- a) 列名与注释
--   SELECT column_name, col_description('t0.svc_endpoints'::regclass,
--          (SELECT attnum FROM pg_attribute WHERE attrelid='t0.svc_endpoints'::regclass AND attname='exposed'))
--     FROM information_schema.columns
--    WHERE table_schema='t0' AND table_name='svc_endpoints' AND column_name IN ('published','exposed');
--
-- b) 数据：新旧列名各跑一次，数量应一致
--   SELECT exposed, count(*) FROM t0.svc_endpoints GROUP BY exposed;
--
-- c) 镜像 JSON：不应再有 "published"
--   SELECT count(*) FROM t0.agents WHERE endpoints::text LIKE '%"published"%';
--   SELECT count(*) FROM t0.flows  WHERE endpoints::text LIKE '%"published"%';
--
-- ============================================================================
-- 回滚
-- ============================================================================
-- BEGIN;
-- ALTER TABLE t0.svc_endpoints RENAME COLUMN exposed TO published;
-- UPDATE t0.agents SET endpoints = replace(endpoints::text, '"exposed"', '"published"')::jsonb
--  WHERE endpoints::text LIKE '%"exposed"%';
-- UPDATE t0.flows  SET endpoints = replace(endpoints::text, '"exposed"', '"published"')::jsonb
--  WHERE endpoints::text LIKE '%"exposed"%';
-- COMMIT;
