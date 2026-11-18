-- ============================================================================
-- 删除 svc_endpoints.config
-- ----------------------------------------------------------------------------
-- config 是 agent_services 时代留下的协议参数槽，运行时从来没有消费方：
--   MCP / RPC 只用 name + description + input_schema（flow 端点的 input_schema
--   由仓储 Create 强制覆盖成 flows.arguments）；agent 本地工具同理。
--   UI 侧新建端点表单已移除该字段，详情页的留档展示也一并去掉。
--
-- 执行顺序：**先发代码、再跑本脚本**。新代码按列名显式读写、不再提 config，
-- 旧库留着这一列也能跑；反过来先删列会让仍在运行的旧代码报错。
--
-- 幂等：DROP COLUMN IF EXISTS，可重复执行。
-- 执行：psql -h <host> -p <port> -U <user> -d <db> -f nexus/sql/svc_endpoints_drop_config.sql
-- ============================================================================
DO $$
DECLARE
  s text;
  n bigint;
BEGIN
  FOR s IN
    SELECT table_schema FROM information_schema.tables
     WHERE table_name = 'svc_endpoints'
       AND table_schema NOT IN ('pg_catalog', 'information_schema', 'public', 'platform')
     ORDER BY table_schema
  LOOP
    -- 非空值会随列一起消失：先报个数，跑完在日志里能看到影响面
    EXECUTE format(
      'SELECT count(*) FROM %I.svc_endpoints WHERE config IS NOT NULL AND config::text <> %L',
      s, '{}'
    ) INTO n;
    IF n > 0 THEN
      RAISE WARNING '% 有 % 行 config 非空，随列一起删除', s, n;
    END IF;

    EXECUTE format('ALTER TABLE %I.svc_endpoints DROP COLUMN IF EXISTS config', s);
  END LOOP;
END $$;

-- ============================================================================
-- 执行后核对：期望 0 行
--   SELECT table_schema, count(*) FROM information_schema.columns
--    WHERE table_name = 'svc_endpoints' AND column_name = 'config'
--    GROUP BY table_schema;
-- ============================================================================
