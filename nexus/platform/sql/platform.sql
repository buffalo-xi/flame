-- ============================================================================
-- 平台多租户管理：平台身份表 + 租户注册表
--
-- 位置：管理 schema（config.database.platform_schema，默认 platform）
-- 执行：psql -h <host> -p <port> -U <user> -d <db> -f nexus/platform/sql/platform.sql
--
-- 口径（与租户侧完全独立）：
--   * platform.users 一张表装两种平台身份（与各租户 <schema>.users 同名但不同物）：
--       admin 管理员 —— 管理所有租户与所有平台用户
--       user  用户   —— 只能看/管自己名下的租户（归属记在 tenants.owner_user_id）
--     平台用户与各租户 <schema>.users **没有任何关系**，密码/状态都在本表。
--   * tenants 记录"租户属于哪个用户"（owner_user_id，1:N，可空 = 管理员代管），
--     而不是在用户行上写租户 —— 一个用户可以负责多个租户。
--   * role / status 用 varchar + CHECK（与本仓库租户侧 auth_type / role / status 的一致做法）。
--   * schema_name 加正则 CHECK：它是会被拼进 SQL 标识符与存储路径的值，格式先收一道；
--     运行期还有注册表白名单（nexus/shared/tenants）作为第二道。
--   * 不物理删除：用户 status=disabled、租户 status=deleted（保留审计与绑定线索）。
--
-- 幂等：可重复执行。
-- 注意：约束写在建表语句内（Postgres 无 ADD CONSTRAINT IF NOT EXISTS）；
--       若将来需要给已存在的表补约束，参照 nexus/sql/tenant_user_roles.sql 的探测写法。
-- ============================================================================

-- ---------------------------------------------------------------------------
-- 1) 平台用户（管理员 + 用户）
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS platform.users (
    id           uuid        PRIMARY KEY,
    name         varchar(64) NOT NULL UNIQUE,              -- 登录名：唯一、创建后不可改
    display_name varchar(64) NOT NULL DEFAULT '',
    password     varchar(100) NOT NULL,                   -- bcrypt（auth.HashPassword 产物，60 字符）
    role         varchar(16) NOT NULL,                    -- admin | user
    status       varchar(16) NOT NULL DEFAULT 'active',   -- active | disabled | locked
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT users_role_check   CHECK (role   IN ('admin','user')),
    CONSTRAINT users_status_check CHECK (status IN ('active','disabled','locked'))
);

-- 登录只按 name 查单行（且 name 有唯一约束），无需额外索引。

-- ---------------------------------------------------------------------------
-- 2) 租户注册表（租户 → 负责人）
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS platform.tenants (
    schema_name   varchar(32) PRIMARY KEY,                -- t0 / t1 / ...（= Postgres schema 名）
    display_name  varchar(64) NOT NULL DEFAULT '',
    status        varchar(16) NOT NULL DEFAULT 'active',  -- active | suspended | deleted | pending
    plan          varchar(32) NOT NULL DEFAULT '',
    owner_user_id uuid        NULL REFERENCES platform.users(id),  -- 负责人（1:N，可空）
    remark        text        NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    created_by    uuid        NULL,
    CONSTRAINT tenants_status_check CHECK (status IN ('active','suspended','deleted','pending')),
    -- 只允许小写字母开头、字母数字下划线，长度 2-32：这是会被拼进 SQL 标识符的值
    CONSTRAINT tenants_schema_check CHECK (schema_name ~ '^[a-z][a-z0-9_]{1,31}$')
);

CREATE INDEX IF NOT EXISTS tenants_owner_idx  ON platform.tenants (owner_user_id);
CREATE INDEX IF NOT EXISTS tenants_status_idx ON platform.tenants (status);

-- ---------------------------------------------------------------------------
-- 3) 配额（复用既有 platform.quota_configs，不在此建表）
--
--   列：tenant_id(PK) / max_agents / max_skills / max_flows
--   语义：tenant_id = '' 为系统行；NULL = 未设置（回退系统行）；-1 = 不限制
--   平台侧写配额的实现见 nexus/platform/repository/tenants.SetQuota（事务内先删后插，
--   不依赖该表上唯一的约束名/形态），写完调用 shared/quota.Invalidate。
-- ---------------------------------------------------------------------------

-- ---------------------------------------------------------------------------
-- 4) 首个平台管理员
--
--   不在 SQL 里插：密码是 bcrypt，SQL 里写不了。
--   由**环境初始数据**预置（与其它缺省数据一起装载）—— nexus 启动不再创建任何平台账号，
--   配置里也没有 bootstrap_admin / ddl_dsn 两个字段了。
--
--   登录名固定为 plat（auth.BootstrapAdminName，不可配置）：该账号在管理台不可修改、不可删除。
--   初始数据里必须存在这一行 active 的 admin，否则平台管理台没有任何可登录的账号。
-- ----------------------------------------------------------------------------

-- ============================================================================
-- 执行后核对
-- ============================================================================
-- a) 两张表与约束是否就位
--   \d platform.users
--   \d platform.tenants
--
-- b) 当前平台身份分布
--   SELECT role, status, count(*) FROM platform.users GROUP BY role, status;
--
-- c) 租户与负责人（找"无主租户"）
--   SELECT t.schema_name, t.status, u.name AS owner
--     FROM platform.tenants t LEFT JOIN platform.users u ON u.id = t.owner_user_id
--    ORDER BY (t.owner_user_id IS NULL) DESC, t.schema_name;
--
-- d) 负责人不是「用户」身份的异常行（应当为空）
--   SELECT t.schema_name, u.name, u.role
--     FROM platform.tenants t JOIN platform.users u ON u.id = t.owner_user_id
--    WHERE u.role <> 'user';
--
-- e) 在租户表里但未注册的 schema（白名单会拒绝登录，需要补注册或清理）
--   SELECT table_schema FROM information_schema.tables
--    WHERE table_name = 'users'
--      AND table_schema NOT IN ('pg_catalog','information_schema','public','platform')
--      AND table_schema NOT IN (SELECT schema_name FROM platform.tenants)
--    ORDER BY table_schema;
