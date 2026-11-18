-- ============================================================================
-- 跨服务器资产市场：对端站点 + 市场出口令牌
--
-- 两个方向、两张表，别混在一起：
--   * peer_servers   —— **我去拉谁**（消费方视角）：对方地址、对方给我的 token、是否自动通过。
--   * market_tokens  —— **允许谁拉我**（生产方视角）：我发出的只读令牌，只存 sha256。
--
-- 为什么不用 platform.users 当对端身份：它是「人」，能登录管理台；
-- 对端只需要「读市场」这一件事，给它一个只能读 /platform/market/* 的令牌即可，
-- 泄露了也只影响资产导出，且可以单条吊销。
--
-- 幂等：可重复执行。
-- ============================================================================

-- ---------------------------------------------------------------------------
-- 1) 对端站点（我去拉的那一端）
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS platform.peer_servers (
    id            uuid PRIMARY KEY,
    name          varchar(64)  NOT NULL UNIQUE,          -- 站点名，导入时写进 origin_server（去重与展示）
    base_url      varchar(255) NOT NULL,                 -- 例如 https://test.example.com（不带 /platform）
    token         text         NOT NULL DEFAULT '',      -- 对方市场出口令牌（明文：出站调用要用；仅管理员可见）
    auto_approve  boolean      NOT NULL DEFAULT false,   -- 从该站导入后是否直接 listed（默认 false=待审）
    enabled       boolean      NOT NULL DEFAULT true,
    last_sync_at  timestamptz  NULL,
    last_error    text         NOT NULL DEFAULT '',
    created_at    timestamptz  NOT NULL DEFAULT now(),
    updated_at    timestamptz  NOT NULL DEFAULT now(),
    CONSTRAINT peer_servers_name_check CHECK (name ~ '^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$'),
    CONSTRAINT peer_servers_url_check  CHECK (base_url ~ '^https?://')
);

-- ---------------------------------------------------------------------------
-- 2) 市场出口令牌（允许别人来拉我的资产）
--    token 明文只在创建时返回一次，库里只留 sha256（hex）。
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS platform.market_tokens (
    id          uuid PRIMARY KEY,
    name        varchar(64)  NOT NULL UNIQUE,     -- 给谁用的（例如 prod-cluster）
    token_hash  varchar(64)  NOT NULL UNIQUE,     -- sha256(明文) 的 hex
    enabled     boolean      NOT NULL DEFAULT true,
    created_by  uuid         NULL REFERENCES platform.users(id),
    created_at  timestamptz  NOT NULL DEFAULT now(),
    last_used_at timestamptz NULL
);

-- ---------------------------------------------------------------------------
-- 3) 租户驱动拉取（P1.5）：可见范围 + 安装策略
--
--   对端不再是「平台逐个导入」，而是「租户在资产市场里看见它、点安装才拉包」。
--   平台在这里决定的只有两件事：**给谁看**（visible_to）与**拉下来要不要再审**（install_policy）。
--   auto_approve 是它的前身（布尔），已在代码里废弃，这里做一次迁移。
-- ---------------------------------------------------------------------------
ALTER TABLE platform.peer_servers
    ADD COLUMN IF NOT EXISTS visible_to     varchar(16) NOT NULL DEFAULT 'all',
    ADD COLUMN IF NOT EXISTS tenant_schemas jsonb       NOT NULL DEFAULT '[]',
    ADD COLUMN IF NOT EXISTS install_policy varchar(16) NOT NULL DEFAULT 'review';

UPDATE platform.peer_servers
   SET install_policy = 'auto'
 WHERE auto_approve AND install_policy = 'review';

-- ---------------------------------------------------------------------------
-- 4) 市场出口策略：要不要校验令牌
--
--   单行表（id 恒为 1）。没有行 = 关（必须带令牌）—— 与代码里的 fail-closed 一致：
--   忘配置绝不能变成「对所有人开放」。
--
--   为什么放库而不是 config.yaml：这是治理策略，平台管理员在管理端就能改；
--   写进配置文件意味着每次开关都要登机器改文件 + 重启，还把权限从「管理员」降级成
--   「有服务器 shell 的人」。同一类决定的先例是 capability_gates / plugin_gates。
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS platform.market_settings (
    id         smallint    PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    open       boolean     NOT NULL DEFAULT false,   -- true = 允许不带令牌读市场
    updated_at timestamptz NOT NULL DEFAULT now(),
    updated_by uuid        NULL REFERENCES platform.users(id)
);

-- ============================================================================
-- 执行后核对
-- ============================================================================
-- SELECT table_name FROM information_schema.tables
--  WHERE table_schema = 'platform' AND table_name IN ('peer_servers','market_tokens');
--  → 2 行
--
-- 用法：
--   1. 生产机（消费方）：资产审核 → 远端市场 → 新建对端（填 test 站地址 + 对方给的令牌）
--   2. 测试机（生产方）：资产审核 → 市场出口令牌 → 新建，把返回的明文令牌给生产机
--   3. 生产机点「拉取目录」→ 选择资产 → 「导入」→ 待审核 → 通过 → 租户可装
-- ============================================================================
