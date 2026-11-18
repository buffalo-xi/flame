-- ============================================================================
-- 凭证端点访问控制：建列 + 建表（存量租户）
--
-- 背景：apikey / oauth 绑定过去只回答「你是谁」，不回答「你能调用哪些端点」。
-- 现在给每条绑定加一个访问范围：
--   restrict_targets = true + 有行      → 只允许 svc_auth_binding_targets 里的目标（新建绑定的缺省）
--   restrict_targets = true + 无行      → 一个都不允许（新建后尚未配置时的状态）
--   restrict_targets = false            → 不限制（需显式关闭；存量绑定在迁移时保持 false）
--
-- 安全默认：新建即受限。默认放开的话，「忘了配」等于「能调全部端点」，
-- 而忘了配恰恰是最常见的状态。
--
-- 授权对象是**目标实体**（agent / flow），不是端点：
--   同一个 agent 通常有 rpc 与 mcp 两条端点，按端点授权会出现
--   「只勾了 rpc，MCP 连接就全空」以及「端点下架重发后授权失效」。
--
-- 幂等：可重复执行。
-- 新建租户走 platform/provision/tenant_template.sql，不需要本脚本。
-- ============================================================================
DO $$
DECLARE
    s text;
BEGIN
    FOR s IN
        SELECT table_schema
          FROM information_schema.tables
         WHERE table_name = 'svc_auth_bindings'
           AND table_schema NOT IN ('pg_catalog', 'information_schema', 'public', 'platform')
         ORDER BY table_schema
    LOOP
        -- 必须分两步：
        --   ① 以 false 落列 —— 存量行全部为 false，升级后行为与之前完全一致；
        --   ② 再把**默认值**改成 true —— 只影响之后新建的绑定。
        -- 如果合成一句 ADD COLUMN ... DEFAULT true，ADD COLUMN 会把默认值回填到所有存量行，
        -- 存量 key 会在升级瞬间变成「受限 + 空白名单」= 一个端点都调不到，直接全线 401。
        EXECUTE format(
            'ALTER TABLE %I.svc_auth_bindings
             ADD COLUMN IF NOT EXISTS restrict_targets boolean NOT NULL DEFAULT false', s);
        EXECUTE format(
            'ALTER TABLE %I.svc_auth_bindings
             ALTER COLUMN restrict_targets SET DEFAULT true', s);

        EXECUTE format(
            'CREATE TABLE IF NOT EXISTS %I.svc_auth_binding_targets (
                 binding_id  uuid        NOT NULL,
                 target_type varchar(16) NOT NULL,
                 target_id   uuid        NOT NULL,
                 created_at  timestamptz NOT NULL DEFAULT now(),
                 CONSTRAINT svc_auth_binding_targets_type_ck
                     CHECK (target_type IN (''agent'', ''flow''))
             )', s);

        -- 主键：重复「建表 IF NOT EXISTS」时约束已存在会报错，故单独判存在
        IF NOT EXISTS (
            SELECT 1 FROM pg_constraint c
              JOIN pg_class t ON t.oid = c.conrelid
              JOIN pg_namespace n ON n.oid = t.relnamespace
             WHERE n.nspname = s AND t.relname = 'svc_auth_binding_targets'
               AND c.conname = 'svc_auth_binding_targets_pkey'
        ) THEN
            EXECUTE format(
                'ALTER TABLE %I.svc_auth_binding_targets
                 ADD CONSTRAINT svc_auth_binding_targets_pkey
                 PRIMARY KEY (binding_id, target_type, target_id)', s);
        END IF;

        IF NOT EXISTS (
            SELECT 1 FROM pg_constraint c
              JOIN pg_class t ON t.oid = c.conrelid
              JOIN pg_namespace n ON n.oid = t.relnamespace
             WHERE n.nspname = s AND t.relname = 'svc_auth_binding_targets'
               AND c.conname = 'svc_auth_binding_targets_binding_id_fkey'
        ) THEN
            EXECUTE format(
                'ALTER TABLE %I.svc_auth_binding_targets
                 ADD CONSTRAINT svc_auth_binding_targets_binding_id_fkey
                 FOREIGN KEY (binding_id) REFERENCES %I.svc_auth_bindings(id) ON DELETE CASCADE', s, s);
        END IF;

        EXECUTE format(
            'CREATE INDEX IF NOT EXISTS idx_svc_auth_binding_targets_target
                 ON %I.svc_auth_binding_targets (target_type, target_id)', s);

        EXECUTE format(
            'COMMENT ON COLUMN %I.svc_auth_bindings.restrict_targets IS
             ''是否限制该凭证可调用的端点目标：缺省 true（新建即受限，必须显式勾选目标）；true 时按 svc_auth_binding_targets 列出的目标，空集合表示一个都不允许；false=不限制（需显式关闭）。存量行在迁移时保持原值 false''', s);

        RAISE NOTICE 'schema %: svc_auth_binding_targets 就绪', s;
    END LOOP;
END $$;
