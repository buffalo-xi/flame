-- ============================================================================
-- 自定义工具的能力档修正：sandbox.script → sandbox.exec（存量回填）
-- ----------------------------------------------------------------------------
-- 背景（口径修正，不是新增功能）：
--   自定义工具（{schema}.custom_tools，对外名 custom__*）一直走 sb.Start → /v1/exec，
--   是**一次性进程、无池**，与 exec 工具、技能脚本 run_skill_script 同一条通道；
--   但它的能力闸写成了 sandbox.script —— 那是 /v1/script 池化通道（工作流代码节点）
--   的闸。判据应当是**通道**，不是「跑的是不是脚本文件」。
--
--   旧口径的两个后果：
--     1. 关 sandbox.script（本意只停工作流脚本）会把租户的自定义工具一起停掉；
--     2. 只开 sandbox.exec 的租户用不了自定义工具，却能拿 exec 跑同一段代码。
--
-- 本脚本要解决的问题：升级后自定义工具改判 exec 档，若某租户只开过 script、
--   从未开过 exec，其自定义工具会立刻失效（能力闸是缺省拒绝、且失败无报错）。
--
-- 判据「真的在用」：该租户有 custom_tools 表，且存在一条 agent_tool_grants 把
--   ref = 'custom:<name>' 以 required/allow 授出去（deny 是显式拒绝，不算使用）。
--
-- 安全性：这里只补平台的**能力**，不碰 agent_tool_grants。
--   工具仍要显式授权才对 agent 可见、可调，所以补开 exec 不会顺带把
--   任意命令执行权限送给任何 agent（能力与工具授权是两个正交的闸）。
--
-- 路径：本脚本只写 platform schema，不遍历租户表结构。
-- 幂等：可重复执行。
-- 顺序：**必须在新版 nexus 上线前或同时执行**。旧版仍按 script 档判闸，
--       多开的 exec 对它无害；反序（先上代码后跑本脚本）则有一段工具失效窗口。
-- ============================================================================

DO $$
DECLARE
    s text;
BEGIN
    FOR s IN
        SELECT DISTINCT t.schema_name
          FROM platform.tenants t
          JOIN information_schema.tables it
            ON it.table_schema = t.schema_name
           AND it.table_name = 'custom_tools'
         WHERE EXISTS (
             SELECT 1
               FROM information_schema.tables gt
              WHERE gt.table_schema = t.schema_name
                AND gt.table_name = 'agent_tool_grants'
         )
        ORDER BY t.schema_name
    LOOP
        -- 该租户是否真的把某个 custom:<name> 授出去过
        IF EXISTS (
            SELECT 1 FROM information_schema.columns c
             WHERE c.table_schema = s
               AND c.table_name = 'agent_tool_grants'
               AND c.column_name = 'ref'
        ) THEN
            EXECUTE format($q$
                INSERT INTO platform.tenant_capabilities
                       (tenant_id, capability, is_allowed, updated_at)
                SELECT $1, 'sandbox.exec', true, now()
                 WHERE EXISTS (
                     SELECT 1 FROM %I.agent_tool_grants g
                      WHERE g.ref LIKE 'custom:%%'
                        AND g.grant_kind IN ('required', 'allow')
                 )
                ON CONFLICT (tenant_id, capability) DO UPDATE
                   SET is_allowed = true,
                       updated_at  = now()
            $q$, s) USING s;
        END IF;
    END LOOP;
END $$;

-- 事后核对：应为 0 行（有自定义工具授权、却没有 exec 能力）。
--   SELECT g.table_schema
--     FROM information_schema.tables g
--    WHERE g.table_name = 'agent_tool_grants'
--      AND g.table_schema NOT IN ('pg_catalog', 'information_schema', 'public', 'platform')
--      AND EXISTS (SELECT 1 FROM information_schema.tables t
--                   WHERE t.table_schema = g.table_schema AND t.table_name = 'custom_tools')
--      AND NOT EXISTS (SELECT 1 FROM platform.tenant_capabilities c
--                       WHERE c.tenant_id = g.table_schema
--                         AND c.capability = 'sandbox.exec' AND c.is_allowed);
