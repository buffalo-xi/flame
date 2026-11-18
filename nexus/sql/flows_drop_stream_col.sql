-- ============================================================================
-- flows 表移除遗留列 stream
-- ----------------------------------------------------------------------------
-- 说明：
--   * 该列为历史遗留（早期"工作流实例自带一个流式开关"的设计残留），
--     后由「客户端在请求里带 progressToken 订阅进度」取代，当前代码从未读取
--     或写入（flows 仓储的 SELECT / INSERT / UPDATE 均不含该列，
--     models.Flow 也无对应字段）。
--   * 为什么要废弃：流式与否不是端点属性，而是**调用方的意愿**。
--     MCP 2026-07-28 用 params._meta.progressToken 表达；server 无权替客户端决定。
--     服务端还留着这个开关，就会出现「端点说流式、客户端没订阅」的空转。
--   * 幂等：可重复执行；自动遍历所有含 flows 表的租户 schema。
--   * **发布顺序**：先上不含该列的代码版本，再执行本脚本。反过来会立刻 500。
--   * 建表模板 nexus/platform/provision/tenant_template.sql 已同步移除该列
--     （新租户不需要跑本脚本）。
-- 执行方式：
--   psql -h <host> -p <port> -U <user> -d <db> -f nexus/sql/flows_drop_stream_col.sql
-- ============================================================================
DO $$
DECLARE
    s text;
BEGIN
    FOR s IN
        SELECT table_schema
          FROM information_schema.tables
         WHERE table_name = 'flows'
           AND table_schema NOT IN ('pg_catalog', 'information_schema', 'public', 'platform')
         ORDER BY table_schema
    LOOP
        EXECUTE format('ALTER TABLE %I.flows DROP COLUMN IF EXISTS stream', s);
        RAISE NOTICE 'dropped %.flows.stream (if existed)', s;
    END LOOP;
END $$;

-- 验证：flows 表当前列（应不含 stream）
SELECT table_schema, column_name, data_type
  FROM information_schema.columns
 WHERE table_name = 'flows'
   AND table_schema NOT IN ('pg_catalog', 'information_schema', 'public', 'platform')
 ORDER BY table_schema, ordinal_position;

-- ----------------------------------------------------------------------------
-- 附：删除前抽查（确认无数据损失的备查语句，按需单独执行）
--
-- 该列只可能是 true/false，且从未参与运行期判定；如仍有疑虑可先看取值分布：
--
-- DO $$
-- DECLARE s text; n_true bigint; n_false bigint;
-- BEGIN
--     FOR s IN
--         SELECT table_schema FROM information_schema.tables
--          WHERE table_name = 'flows'
--            AND table_schema NOT IN ('pg_catalog', 'information_schema', 'public', 'platform')
--     LOOP
--         EXECUTE format('SELECT count(*) FILTER (WHERE stream), count(*) FILTER (WHERE NOT stream) FROM %I.flows', s)
--            INTO n_true, n_false;
--         RAISE NOTICE '%: flows.stream true=% false=%', s, n_true, n_false;
--     END LOOP;
-- END $$;
-- ============================================================================
