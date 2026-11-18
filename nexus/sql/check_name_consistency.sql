-- ============================================================================
-- 命名一致性核对（只读，不修改任何数据）
--
-- 用法（psql）：
--   psql -f check_name_consistency.sql -v schema=tenant_xxx
--   psql -f check_name_consistency.sql -v schema=tenant_xxx -o report.txt
-- 也可先把 :schema 全部替换成实际 schema 再执行。
--
-- 背景：
--   1) svc_endpoints.name 历史上会在发布/更新端点时被回写实体名，现已改为
--      「端点名恒等于实体名，实体改名才同步端点名」；老数据可能仍有不一致。
--   2) agent / workflow 的重名校验已改为大小写不敏感（LOWER(name) 比较），
--      老数据可能残留仅大小写不同的同名资源。
-- ============================================================================

-- 1) 端点名 ≠ 实体名（历史回写造成的不一致）
SELECT 'agent endpoint name mismatch' AS check_name,
       e.id   AS endpoint_id,
       e.name AS endpoint_name,
       a.name AS target_name
FROM :schema.svc_endpoints e
JOIN :schema.agents a ON a.id = e.target_id
WHERE e.target_type = 'agent'
  AND e.name <> a.name
UNION ALL
SELECT 'flow endpoint name mismatch',
       e.id,
       e.name,
       f.name
FROM :schema.svc_endpoints e
JOIN :schema.flows f ON f.id = e.target_id
WHERE e.target_type = 'flow'
  AND e.name <> f.name;

-- 2) 仅大小写不同的同名 agent / flow（大小写不敏感后会互斥，需人工合并）
SELECT 'agent name case clash' AS check_name,
       LOWER(name)            AS lowered_name,
       COUNT(*)               AS cnt,
       STRING_AGG(name, ', ') AS names
FROM :schema.agents
GROUP BY LOWER(name) HAVING COUNT(*) > 1
UNION ALL
SELECT 'flow name case clash',
       LOWER(name),
       COUNT(*),
       STRING_AGG(name, ', ')
FROM :schema.flows
GROUP BY LOWER(name) HAVING COUNT(*) > 1;

-- 3) agent 与 flow 同名（跨表冲突，规则上应互斥；历史数据可能残留）
SELECT 'agent/flow name clash' AS check_name,
       a.name AS agent_name,
       f.name AS flow_name
FROM :schema.agents a
JOIN :schema.flows f ON LOWER(a.name) = LOWER(f.name);

-- 4) 同一 protocol 下端点名重复（RPC/MCP 对外调用会歧义）
SELECT 'duplicate endpoint name' AS check_name,
       protocol,
       LOWER(name)                          AS lowered_name,
       COUNT(*)                             AS cnt,
       STRING_AGG(id::text, ', ')           AS endpoint_ids
FROM :schema.svc_endpoints
GROUP BY protocol, LOWER(name) HAVING COUNT(*) > 1;

-- 5) type 前缀（agent/flow）下同名端点：同一实体多个协议端点同名是允许的，
--    这里仅列出跨实体重复，便于确认没有意外撞名。
SELECT 'cross-target duplicate endpoint name' AS check_name,
       protocol,
       LOWER(name)  AS lowered_name,
       COUNT(DISTINCT target_id) AS target_cnt,
       STRING_AGG(DISTINCT target_type || ':' || target_id::text, ', ') AS targets
FROM :schema.svc_endpoints
GROUP BY protocol, LOWER(name) HAVING COUNT(DISTINCT target_id) > 1;

-- 注意：以下两类问题 SQL 查不出来，需要文件系统核对 / 人工判断
--   a) agents.name 与工作区目录名不一致（tenants/<schema>/workspace/<name>）
--      历史上端点名回写 agents.name 时不会迁移目录，会导致 agent 读不到 AGENT.md。
--      核对方式（PowerShell，本地存储）：
--        $db = <上面第 1 类之外单独查 agents 的 name 列表>
--        Get-ChildItem <root>\tenants\<schema>\workspace -Directory | % { $_.Name }
--      比两边差集即可。
--   b) 上述任何不一致，修复前建议先备份，并以「文件所在目录」为准回写 DB 名。
