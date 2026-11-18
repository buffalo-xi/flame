-- ============================================================================
-- 平台用户表改名：platform.pm_users → platform.users
--
-- 背景：
--   pm_ 前缀想表达 "platform"，但它与 schema 名重复，且 pm 在技术语境里更常被读成
--   package manager / process manager，属于"要查文档才知道含义"的缩写。
--   命名规则统一为 <schema>.<entity>：平台身份在 platform.users，租户账号在 <schema>.users。
--
--   **两者同名但完全独立**：
--     platform.users  —— 平台登录身份（role: admin / user），管理租户与平台用户
--     <schema>.users  —— 租户内账号（role: manage / service），登录控制台或作为 API 身份
--   因此平台表一律显式带 schema 前缀引用，禁止裸写 users。
--
-- 本脚本负责（platform schema，单表）：
--   * 表名      platform.pm_users → platform.users
--   * 约束      pm_users_* → users_*（pkey / name_key / role_check / status_check / *_not_null）
--   * 独立索引  pm_users_display_name_idx → users_display_name_idx
--     约束自带的索引（pkey / name_key）会随 RENAME CONSTRAINT 自动改名，不单独处理。
--
-- 不做的事：
--   * 不动数据、不改列；不重建 FK —— tenants.owner_user_id / shared_assets.reviewed_by /
--     publish_policy.updated_by / market_tokens.created_by·updated_by 都按表 OID 引用，
--     改名后自动跟随。
--
-- 幂等：platform.users 已存在则整体跳过（不覆盖，留给人确认是否撞名）。
-- 顺序：**必须先于新版本程序上线执行** —— 新代码按 platform.users 查询。
--
-- 实现说明：待改名对象先聚合成数组再遍历（FOREACH），不在 FOR ... IN SELECT
--   的游标上做 catalog 改动 —— 边扫 pg_constraint 边改名有漏行风险。
--
-- 执行方式：
--   psql -h <host> -p <port> -U <user> -d <db> -f nexus/sql/platform_users_rename.sql
-- ============================================================================
DO $$
DECLARE
  cons text[];
  idxs text[];
  o    text;
BEGIN
  IF to_regclass('platform.users') IS NOT NULL THEN
    RAISE NOTICE 'platform.users 已存在，跳过（请人工确认是否撞名）';
    RETURN;
  END IF;

  ALTER TABLE platform.pm_users RENAME TO users;
  RAISE NOTICE 'platform.pm_users → platform.users';

  -- 12 个约束：pkey / name_key / role_check / status_check + 8 个 NOT NULL
  SELECT array_agg(conname) INTO cons
    FROM pg_constraint
   WHERE conrelid = 'platform.users'::regclass
     AND conname LIKE 'pm\_users\_%';

  IF cons IS NOT NULL THEN
    FOREACH o IN ARRAY cons LOOP
      EXECUTE format('ALTER TABLE platform.users RENAME CONSTRAINT %I TO %I',
        o, regexp_replace(o, '^pm_users_', 'users_'));
    END LOOP;
  END IF;

  -- 约束自带索引已随约束改名；这里只兜住独立索引（display_name_idx）
  SELECT array_agg(relname) INTO idxs
    FROM pg_class
   WHERE relnamespace = 'platform'::regnamespace
     AND relkind = 'i'
     AND relname LIKE 'pm\_users\_%';

  IF idxs IS NOT NULL THEN
    FOREACH o IN ARRAY idxs LOOP
      EXECUTE format('ALTER INDEX platform.%I RENAME TO %I',
        o, regexp_replace(o, '^pm_users_', 'users_'));
    END LOOP;
  END IF;
END $$;

-- ============================================================================
-- 执行后核对：
--   a) 表名（应只有 platform.users，没有 platform.pm_users）
--     SELECT table_schema, table_name FROM information_schema.tables
--      WHERE table_name IN ('users','pm_users') ORDER BY 1,2;
--   b) 约束全部为 users_*，且没有 pm_users_* 残留
--     SELECT conname, contype FROM pg_constraint
--      WHERE conrelid = 'platform.users'::regclass ORDER BY conname;
--   c) 索引：users_pkey / users_name_key / users_display_name_idx
--     SELECT indexname FROM pg_indexes
--      WHERE schemaname = 'platform' AND tablename = 'users' ORDER BY 1;
--   d) 负责人关联仍正常（不能为空表）
--     SELECT t.schema_name, u.name AS owner
--       FROM platform.tenants t LEFT JOIN platform.users u ON u.id = t.owner_user_id;
-- ============================================================================
