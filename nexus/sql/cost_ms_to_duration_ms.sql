-- ============================================================================
-- cost_ms → duration_ms：运行记录的耗时列统一命名
-- ----------------------------------------------------------------------------
--   flow_run_records / flow_node_records 的耗时列沿用早期 dump 的 cost_ms；
--   与 agent 事件协议里的 duration_ms 是同一概念，统一成一个名字。
--
--   幂等：旧列存在且新列不存在才改名；新环境（模板已用 duration_ms）直接跳过。
--   注意：纯改名，数据不动，无需回填。
-- ============================================================================
DO $$
DECLARE
    s  text;
    tb text;
BEGIN
    FOR s IN
        SELECT table_schema
          FROM information_schema.tables
         WHERE table_name IN ('flow_run_records', 'flow_node_records')
           AND table_schema NOT IN ('pg_catalog', 'information_schema', 'public', 'platform')
         GROUP BY table_schema
         ORDER BY table_schema
    LOOP
        FOREACH tb IN ARRAY ARRAY['flow_run_records', 'flow_node_records']
        LOOP
            IF EXISTS (SELECT 1 FROM information_schema.columns
                        WHERE table_schema = s AND table_name = tb AND column_name = 'cost_ms')
               AND NOT EXISTS (SELECT 1 FROM information_schema.columns
                        WHERE table_schema = s AND table_name = tb AND column_name = 'duration_ms')
            THEN
                EXECUTE format('ALTER TABLE %I.%I RENAME COLUMN cost_ms TO duration_ms', s, tb);
                -- 约束名里的 cost_ms 只是标识符，顺手对齐（不存在则跳过）
                IF EXISTS (SELECT 1 FROM pg_constraint
                            WHERE conname = format('pipe_%s_cost_ms_not_null',
                                   CASE tb WHEN 'flow_run_records' THEN 'run_records'
                                           ELSE 'node_records' END))
                THEN
                    EXECUTE format('ALTER TABLE %I.%I RENAME CONSTRAINT %I TO %I', s, tb,
                                   format('pipe_%s_cost_ms_not_null',
                                          CASE tb WHEN 'flow_run_records' THEN 'run_records'
                                                  ELSE 'node_records' END),
                                   format('pipe_%s_duration_ms_not_null',
                                          CASE tb WHEN 'flow_run_records' THEN 'run_records'
                                                  ELSE 'node_records' END));
                END IF;
                RAISE NOTICE '% .% → duration_ms', s, tb;
            END IF;
        END LOOP;
    END LOOP;
END $$;

-- 验证：应查不到任何 cost_ms，且三张表都有 duration_ms
SELECT table_schema, table_name, column_name
  FROM information_schema.columns
 WHERE column_name IN ('cost_ms', 'duration_ms')
   AND table_name IN ('flow_run_records', 'flow_node_records', 'session_records')
 ORDER BY table_schema, table_name;
