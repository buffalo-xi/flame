-- ============================================================================
-- 租户用户表改名：um_users → users
--
-- 背景：
--   租户 schema 里的用户表历史上叫 um_users（"um" 是更早期命名的遗留，已无表意），
--   平台侧则是 platform.users（同名但完全独立，见 platform_users_rename.sql）。
--   同语义的东西叫法不一致，读 SQL 时容易混淆，故统一为 users。
--
-- 本脚本负责：
--   * 表名        um_users               → users
--   * 主键        um_users_pk            → users_pkey
--   * 唯一约束    um_user_unique         → users_name_key
--   * CHECK       um_users_role_check    → users_role_check
--   * CHECK       um_users_status_check  → users_status_check
--   * 唯一索引    um_users_display_name_idx → users_display_name_idx
--   * 重复索引    um_users_name_uniq     → 直接删除
--     它与唯一约束 um_user_unique 作用在同一列 name 上，是两套完全重复的唯一性保证
--     （写放大、无收益）。唯一性由改名后的 users_name_key 继续保证。
--     若该索引被人为改成非唯一/多列，执行前请先人工确认。
--
-- 不做的事：
--   * 不改列、不动数据、不建外键 —— user_quotas / svc_auth_bindings 只存 user_id，
--     本仓库一贯不在租户侧建 FK。
--   * NOT NULL 约束名（早期 sys_users_* / um_users_*_not_null）一并改成 users_*。
--     PG 17+ 才把 NOT NULL 约束记进 pg_constraint，更早版本扫不到会静默跳过 ——
--     此时名字仍带旧前缀，但功能无损。
--
-- 幂等：可重复执行。表/约束/索引已改过的会被探测跳过；已改名的 schema 不会再被选中。
-- 顺序：**必须先于新版本程序上线执行** —— 新代码全部按 <schema>.users 查询。
--
-- 实现说明：待处理对象先聚合成数组再遍历（FOREACH），不在 FOR ... IN SELECT
--   的游标上做 catalog 改动 —— 边扫 pg_constraint 边改名有漏行风险。
--
-- 执行方式：
--   psql -h <host> -p <port> -U <user> -d <db> -f nexus/sql/tenant_users_rename.sql
-- ============================================================================
DO $$
DECLARE
  schemas text[];
  cons    text[];
  s       text;
  c       text;
BEGIN
  SELECT array_agg(table_schema ORDER BY table_schema) INTO schemas
    FROM information_schema.tables
   WHERE table_name = 'um_users'
     AND table_schema NOT IN ('pg_catalog', 'information_schema', 'public', 'platform');

  IF schemas IS NULL THEN
    RAISE NOTICE '没有需要改名的 um_users 表';
    RETURN;
  END IF;

  FOREACH s IN ARRAY schemas LOOP
    -- 目标表已存在 = 早前迁过或撞名：不覆盖，留给人确认
    IF to_regclass(format('%I.users', s)) IS NOT NULL THEN
      RAISE NOTICE '[%] users 表已存在，跳过（请人工确认是否撞名）', s;
      CONTINUE;
    END IF;

    EXECUTE format('ALTER TABLE %I.um_users RENAME TO users', s);
    RAISE NOTICE '[%] um_users → users', s;

    -- 约束改名（Postgres 没有 RENAME CONSTRAINT IF EXISTS，先探测；结果先落地到数组）
    SELECT array_agg(conname) INTO cons
      FROM pg_constraint
     WHERE conrelid = to_regclass(format('%I.users', s))
       AND conname IN ('um_users_pk', 'um_user_unique',
                       'um_users_role_check', 'um_users_status_check',
                       'um_users_role_not_null', 'um_users_status_not_null',
                       'sys_users_name_not_null', 'sys_users_id_not_null');

    IF cons IS NOT NULL THEN
      FOREACH c IN ARRAY cons LOOP
        EXECUTE format('ALTER TABLE %I.users RENAME CONSTRAINT %I TO %I',
          s, c,
          CASE c
            WHEN 'um_users_pk'              THEN 'users_pkey'
            WHEN 'um_user_unique'           THEN 'users_name_key'
            WHEN 'um_users_role_check'      THEN 'users_role_check'
            WHEN 'um_users_status_check'    THEN 'users_status_check'
            WHEN 'um_users_role_not_null'   THEN 'users_role_not_null'
            WHEN 'um_users_status_not_null' THEN 'users_status_not_null'
            WHEN 'sys_users_name_not_null'  THEN 'users_name_not_null'
            ELSE                                 'users_id_not_null'
          END);
      END LOOP;
    END IF;

    -- 重复的唯一索引：删掉比改名更合理（与 users_name_key 完全重复）
    IF to_regclass(format('%I.um_users_name_uniq', s)) IS NOT NULL THEN
      EXECUTE format('DROP INDEX %I.um_users_name_uniq', s);
      RAISE NOTICE '[%] 删除重复唯一索引 um_users_name_uniq', s;
    END IF;

    IF to_regclass(format('%I.um_users_display_name_idx', s)) IS NOT NULL THEN
      EXECUTE format('ALTER INDEX %I.um_users_display_name_idx RENAME TO users_display_name_idx', s);
    END IF;
  END LOOP;
END $$;

-- ============================================================================
-- 执行后核对：
--   a) 确认没有残留旧表名（应为 0 行）
--     SELECT table_schema FROM information_schema.tables
--      WHERE table_name = 'um_users'
--        AND table_schema NOT IN ('pg_catalog','information_schema','public','platform');
--   b) 确认约束已改名（每个租户应看到 users_pkey / users_name_key / users_*_check）
--     SELECT conrelid::regclass AS tbl, conname
--       FROM pg_constraint
--      WHERE conrelid = 't0.users'::regclass;
--   c) 确认重复索引已删除、其余索引已改名
--     SELECT indexname FROM pg_indexes WHERE schemaname = 't0' AND tablename = 'users';
-- ============================================================================
