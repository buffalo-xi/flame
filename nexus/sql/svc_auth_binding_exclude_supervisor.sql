-- ============================================================================
-- 凭证端点访问范围：加「排除超级智能体」中间档（存量租户）
--
-- 背景：范围本来只有两档 —— 不限制 / 白名单。常用诉求是「除了超级智能体，其他都能调」：
-- supervisor 的端点等于对外开放「建 / 改 / 删 agent、改授权」，但不该为此维护一份白名单
-- （新发布的普通端点还得记着手工补）。
--
-- 三档语义：
--   restrict_targets=false, exclude_supervisor=false  → 不限制（全部已发布端点）
--   restrict_targets=false, exclude_supervisor=true   → 除超级智能体外全部
--   restrict_targets=true                             → 仅白名单里的目标（忽略 exclude_supervisor）
--
-- 安全默认不变：新建绑定仍是 restrict_targets=true（受限 + 空白名单 = 一个都调不到）。
-- 存量行 exclude_supervisor=false，升级后行为与之前完全一致。
--
-- 幂等：可重复执行。
-- 新建租户走 platform/provision/template/schema.sql，不需要本脚本。
-- ============================================================================
DO $$
DECLARE s text;
BEGIN
    FOR s IN
        SELECT table_schema
          FROM information_schema.tables
         WHERE table_name = 'svc_auth_bindings'
           AND table_schema NOT IN ('pg_catalog', 'information_schema', 'public', 'platform')
         ORDER BY table_schema
    LOOP
        -- DEFAULT false 对存量行无副作用：false 就是「不排除」，与升级前行为一致
        EXECUTE format(
            'ALTER TABLE %I.svc_auth_bindings
             ADD COLUMN IF NOT EXISTS exclude_supervisor boolean NOT NULL DEFAULT false', s);

        EXECUTE format(
            'COMMENT ON COLUMN %I.svc_auth_bindings.exclude_supervisor IS
             ''仅 restrict_targets=false 时生效：true=放行全部已发布端点但排除超级智能体（supervisor 端点等于对外开放租户管理能力）；restrict_targets=true 时忽略''', s);

        RAISE NOTICE 'schema %: svc_auth_bindings.exclude_supervisor 就绪', s;
    END LOOP;
END $$;
