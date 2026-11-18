-- ============================================================================
-- 回填：给存量流程补一条未对外的 RPC 端点
-- ----------------------------------------------------------------------------
-- 背景：流程创建时现在会自动补一条 exposed=false 的 RPC 端点（仓储 CreateFlow
-- 调用 svcendpoints.CreateManagedRPC）——流程要被别的流程按端点名调用（rpc 节点
-- 存的是端点名）、要出现在工具目录里，都依赖这一行。本脚本把存量流程补齐，
-- 让新旧行为一致。
--
-- 跳过的情况（与新代码一致）：
--   - 名字不是 ASCII 字母/数字/横线（端点名同时是工具名与授权 ref local:rpc:<name>，
--     中文名建出来是模型调不到、也授权不了的死端点）；
--   - 已有同 (target, protocol) 端点（用户手工建过 / 已对外开放的 rpc）。
--
-- 幂等：ON CONFLICT DO NOTHING + NOT EXISTS，可重复执行。
-- 执行：psql -h <host> -p <port> -U <user> -d <db> -f nexus/sql/svc_endpoints_backfill_flow_rpc.sql
-- ============================================================================
DO $$
DECLARE
  s text;
  inserted bigint;
BEGIN
  FOR s IN
    SELECT table_schema FROM information_schema.tables
     WHERE table_name = 'flows'
       AND table_schema NOT IN ('pg_catalog', 'information_schema', 'public', 'platform')
     ORDER BY table_schema
  LOOP
    EXECUTE format($f$
      INSERT INTO %I.svc_endpoints (id, name, description, created_at, updated_at, target_id, target_type, protocol, input_schema, exposed)
      SELECT gen_random_uuid(), f.name, f.description, NOW(), NOW(), f.id, 'flow', 'rpc', f.arguments, false
        FROM %I.flows f
       WHERE f.name ~ '^[A-Za-z0-9][A-Za-z0-9-]*$'
         AND length(f.name) <= 64
         AND NOT EXISTS (
           SELECT 1 FROM %I.svc_endpoints e
            WHERE e.target_id = f.id AND e.target_type = 'flow' AND e.protocol = 'rpc'
         )
      ON CONFLICT DO NOTHING
    $f$, s, s, s);
    GET DIAGNOSTICS inserted = ROW_COUNT;
    RAISE NOTICE '% 补了 % 条 RPC 端点', s, inserted;

    -- 重算 flows.endpoints 镜像（与仓储 syncEndpoints 同一口径：按 created_at 升序聚合）
    EXECUTE format($f$
      UPDATE %I.flows f
         SET endpoints = COALESCE((
               SELECT jsonb_agg(jsonb_build_object('protocol', e.protocol, 'exposed', e.exposed) ORDER BY e.created_at)
                 FROM %I.svc_endpoints e
                WHERE e.target_id = f.id AND e.target_type = 'flow'
             ), '[]'::jsonb)
       WHERE EXISTS (
         SELECT 1 FROM %I.svc_endpoints e WHERE e.target_id = f.id AND e.target_type = 'flow'
       )
    $f$, s, s, s);
  END LOOP;
END $$;

-- ============================================================================
-- 执行后核对：
--   未补上的流程（名字不合法的除外）期望为空：
--   SELECT f.id, f.name FROM <schema>.flows f
--    WHERE f.name ~ '^[A-Za-z0-9][A-Za-z0-9-]*$'
--      AND NOT EXISTS (SELECT 1 FROM <schema>.svc_endpoints e
--                       WHERE e.target_id = f.id AND e.target_type = 'flow' AND e.protocol = 'rpc');
-- ============================================================================
