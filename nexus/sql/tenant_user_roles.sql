-- ============================================================================
-- 租户用户：角色（role）+ 状态（status）
--
-- 口径（本方案只做两件事）：
--   1) role  只管「能不能登录控制台 / 进后台」：
--        manage  管理用户：可登录，能访问 /admin/* 后台接口
--        service 服务用户：不可登录，仅作为 API 调用身份（非绑定前提，绑定不限制角色）
--   2) status 只管「账号还能不能用」（登录与 API 两侧都看它）：
--        active / disabled / locked
--
-- 设计要点：
--   * role / status 用 varchar + CHECK（与本 schema 的 auth_type / grant_kind / item_type 一致）。
--     不用 is_active 布尔：状态必然会出现「锁定」这类第三态，布尔加态要改列。
--   * 列默认值 service（fail-closed：迁移之后由接口新建的行默认不可登录）。
--   * 回填：存量行一律 → manage，保留原有登录能力。
--     注意：绝不能按 svc_auth_bindings 回填 —— 绑定与角色已解耦，
--     否则会把已有 API Key 的管理用户降级成 service，直接登不进去。
--   * name 唯一索引：/login 用 WHERE name = $1 取单行，重名会让登录行为不确定。
--     若库中已有重名行，本步骤会失败 —— 先按文件末尾的核对 SQL 清理重复再执行。
--   * 幂等：可对每个租户 schema 重复执行。
--
-- 执行方式：
--   psql -h <host> -p <port> -U <user> -d <db> -f nexus/sql/tenant_user_roles.sql
--
-- 注意：本文件写于旧表名时期（um_users），**保留旧表名以便对未迁移的库重放**。
--   新库不需要执行；若要重放，请先执行 nexus/sql/tenant_users_rename.sql
--   （改名后本文件因扫不到 um_users 而自动跳过）。
-- ============================================================================
DO $$
DECLARE s text;
BEGIN
  FOR s IN
    SELECT table_schema FROM information_schema.tables
     WHERE table_name = 'um_users'
       AND table_schema NOT IN ('pg_catalog', 'information_schema', 'public', 'platform')
     ORDER BY table_schema
  LOOP
    -- 1) 角色列 / 状态列
    EXECUTE format($f$
      ALTER TABLE %I.um_users
        ADD COLUMN IF NOT EXISTS role   varchar(16) NOT NULL DEFAULT 'service',
        ADD COLUMN IF NOT EXISTS status varchar(16) NOT NULL DEFAULT 'active'
    $f$, s);

    -- 2) 时间戳（若表里还没有；用户管理页展示与审计用）
    EXECUTE format($f$
      ALTER TABLE %I.um_users
        ADD COLUMN IF NOT EXISTS created_at timestamptz NOT NULL DEFAULT now(),
        ADD COLUMN IF NOT EXISTS updated_at timestamptz NOT NULL DEFAULT now()
    $f$, s);

    -- 3) 回填存量用户：保留登录能力（此刻表内都是存量行；之后新建行取默认值 service）
    EXECUTE format($f$ UPDATE %I.um_users SET role = 'manage' $f$, s);

    -- 4) 约束（Postgres 不支持 ADD CONSTRAINT IF NOT EXISTS，先探测）
    IF NOT EXISTS (
      SELECT 1 FROM pg_constraint
       WHERE conname = 'um_users_role_check'
         AND conrelid = to_regclass(format('%I.um_users', s))
    ) THEN
      EXECUTE format($f$
        ALTER TABLE %I.um_users
          ADD CONSTRAINT um_users_role_check CHECK (role IN ('manage','service'))
      $f$, s);
    END IF;

    IF NOT EXISTS (
      SELECT 1 FROM pg_constraint
       WHERE conname = 'um_users_status_check'
         AND conrelid = to_regclass(format('%I.um_users', s))
    ) THEN
      EXECUTE format($f$
        ALTER TABLE %I.um_users
          ADD CONSTRAINT um_users_status_check CHECK (status IN ('active','disabled','locked'))
      $f$, s);
    END IF;

    -- 5) name 唯一（重名会让 /login 取到不确定的一行）
    EXECUTE format($f$
      CREATE UNIQUE INDEX IF NOT EXISTS um_users_name_uniq ON %I.um_users (name)
    $f$, s);
  END LOOP;
END $$;

-- ============================================================================
-- 执行后核对（schema 换成实际值，例如 t0）
-- ============================================================================
-- a) 角色/状态回填结果 + 哪些账号没有密码（无密码的即使 role=manage 也登不进去，
--    需在「用户管理」里重置密码，或把它改判为 service）
--   SELECT id, name, role, status, (COALESCE(password, '') = '') AS no_password
--     FROM t0.um_users ORDER BY role, name;
--
-- b) 加唯一索引前若有重复名，先清理（本文件执行失败时按此排查）
--   SELECT name, count(*) FROM t0.um_users GROUP BY name HAVING count(*) > 1;
--
-- c) 确认约束已生效
--   SELECT conname, pg_get_constraintdef(oid)
--     FROM pg_constraint WHERE conrelid = 't0.um_users'::regclass;
