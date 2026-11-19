-- 工具能力的平台级授权：platform.tenant_capabilities
--
-- 与 platform.tenant_plugins 的分工完全一致（解析逻辑见 shared/capabilitypolicy）：
--   平台侧（本表）：这个租户允不允许用这类能力
--   租户侧（{schema}.agent_tool_grants）：给哪个 agent 授权
--
-- 按**进程形态**分四档（不是按「能跑什么」）：
--   sandbox.exec     一次性命令 —— exec，起完即退，无池、无硬超时
--   sandbox.shell    常驻会话 —— shell，进程跨调用保留，空闲超时才回收
--   sandbox.code     托管代码片段 —— 工作流 code 节点 / 分支 / binding，服务端托管解释器，
--                    有并发槽与 script_max_sec 硬超时（2026-10 由 sandbox.script 更名：
--                    exec 通道同样在执行脚本文件，用 script 命名会把两条通道共有的性质
--                    当成其中一条的名字）
--
-- ⚠️ 口径修正：run_skill_script 现已归 sandbox.exec（它走 /v1/exec，一次性进程、无池），
--    不再属于本档；租户自定义工具（script__*）同理归 sandbox.exec，本档现在只对应
--    /v1/script 池化通道（工作流脚本）。本文件下方的回填与展开逻辑是**历史动作**，
--    按当时的语义写就，已执行过；新的实现见 tools.CapabilityOf、flow/executors 的
--    ensureScriptCapability 与 tools/executeScriptTool（存量回填见 customtool_capability_exec.sql）。
--   sandbox.browser  浏览器 —— Chromium，常驻重量级进程且天然出网
--
-- ⚠️ 四档都能在沙箱里执行任意代码（脚本里的 os.system / subprocess 与 exec 等价），
-- 这不是权限边界，只是按形态授权资源。真正的隔离边界在沙箱实现里。
--
-- 语义：**缺省拒绝**。tenant_id = '' 为系统默认行，租户行整行覆盖系统行（不做逐字段回退）。
-- 本文件**不写系统默认行** —— 写了就等于把所有租户（含以后新建的）一律放开。
--
-- 幂等：可重复执行。

CREATE TABLE IF NOT EXISTS platform.tenant_capabilities (
    tenant_id   varchar(32) NOT NULL DEFAULT '',   -- '' = 系统默认行
    capability  varchar(32) NOT NULL,
    is_allowed  boolean     NOT NULL DEFAULT false,
    updated_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, capability),
    -- 键名白名单：脏 key 落库会让「未开通」与「配错了」看起来一样
    CONSTRAINT tenant_capabilities_key_check
        CHECK (capability IN ('sandbox.exec', 'sandbox.browser'))
);

-- 白名单扩容（split 出 shell / script 两档）：CREATE TABLE IF NOT EXISTS 不会改已存在表的约束，
-- 必须显式重建，否则新键直接落库失败。先 DROP IF EXISTS 保证幂等。
--
-- net.egress 是后来加的第五个键：它不是「某类工具」，而是命令执行族能否访问外网的
-- 出口权限（与 sandbox.exec 是子权限关系，见 platform/handler 的 capabilityDep）。
-- 键名走 net.* 而不是 sandbox.*，正是为了不让它混进「按进程形态分档」的那四档。
ALTER TABLE platform.tenant_capabilities
    DROP CONSTRAINT IF EXISTS tenant_capabilities_key_check;
ALTER TABLE platform.tenant_capabilities
    ADD CONSTRAINT tenant_capabilities_key_check
    -- 过渡期同时允许 sandbox.script（2026-10 更名为 sandbox.code）：收窄留到删别名的版本，
    -- 否则老库重跑本文件会因存量旧行不满足约束而失败。
    CHECK (capability IN ('sandbox.exec', 'sandbox.shell', 'sandbox.code', 'sandbox.script', 'sandbox.browser', 'net.egress'));

-- ---------------------------------------------------------------------------
-- 存量迁移：按「已经真的在用」回填，其余租户保持未开通
-- ---------------------------------------------------------------------------
-- 本表上线前只有 agent 级授权，没有任何平台行；若直接按缺省拒绝上线，
-- 所有存量租户的 exec / browser 会当场失效（静默故障）。
-- 所以只给「确实在用」的租户写允许行，其余一律仍按未开通处理。
--
-- ⚠️ 「在用」有两个来源，缺一个就会误伤：
--   1) 租户给某个 agent 授过 exec / shell / run_skill_script / browser（agent_tool_grants）
--   2) 租户里存在内置超级智能体—— 它的内置工具是**固有基线**，不进授权表，
--      但一直拥有全部内置工具（含 exec / browser），迁移时必须一并保留。
DO $$
DECLARE
    t       record;
    cap_key text;
BEGIN
    FOR t IN SELECT schema_name FROM platform.tenants LOOP
        IF NOT EXISTS (
            SELECT 1 FROM information_schema.tables
             WHERE table_schema = t.schema_name AND table_name = 'agent_tool_grants'
        ) THEN
            CONTINUE;
        END IF;

        -- 执行族（一次性命令）：授过 exec / shell / script 任一，或存在内置超级智能体（基线全开）。
        -- 拆分前 builtin:exec 组同时含 shell 与 run_skill_script，所以四个 ref 都要认。
        EXECUTE format($f$
            INSERT INTO platform.tenant_capabilities (tenant_id, capability, is_allowed, updated_at)
            SELECT $1, 'sandbox.exec', true, now()
             WHERE EXISTS (
                 SELECT 1 FROM %I.agent_tool_grants
                  WHERE ref IN ('builtin:exec', 'builtin:shell', 'builtin:run_skill_script', 'builtin:script')
             )
                OR EXISTS (SELECT 1 FROM %I.agents WHERE name = 'main')
            ON CONFLICT (tenant_id, capability) DO NOTHING
        $f$, t.schema_name, t.schema_name) USING t.schema_name;

        -- 常驻会话 / 池化脚本：拆分前它们挂在同一个 builtin:exec 组下，行为必须顺延。
        -- 另外「平台已显式开通过 exec」的租户也要跟着开（否则升级当天老租户的
        -- shell 与技能/流程脚本会一起失效 —— 静默故障，最难查）。
        -- 新键 sandbox.code（2026-10 由 sandbox.script 更名）：新环境首次执行本文件
        -- 就直接落新键，不必再跑 capability_code_rename.sql。
        FOREACH cap_key IN ARRAY ARRAY['sandbox.shell', 'sandbox.code'] LOOP
            EXECUTE format($f$
                INSERT INTO platform.tenant_capabilities (tenant_id, capability, is_allowed, updated_at)
                SELECT $1, $2, true, now()
                 WHERE EXISTS (
                     SELECT 1 FROM %I.agent_tool_grants
                      WHERE ref IN ('builtin:exec', 'builtin:shell', 'builtin:run_skill_script', 'builtin:script')
                 )
                    OR EXISTS (SELECT 1 FROM %I.agents WHERE name = 'main')
                    OR EXISTS (
                        SELECT 1 FROM platform.tenant_capabilities
                         WHERE tenant_id = $1 AND capability = 'sandbox.exec' AND is_allowed
                    )
                ON CONFLICT (tenant_id, capability) DO NOTHING
            $f$, t.schema_name, t.schema_name) USING t.schema_name, cap_key;
        END LOOP;

        -- 浏览器单独一档：它是唯一的重型常驻进程，没在用就不放开
        EXECUTE format($f$
            INSERT INTO platform.tenant_capabilities (tenant_id, capability, is_allowed, updated_at)
            SELECT $1, 'sandbox.browser', true, now()
             WHERE EXISTS (
                 SELECT 1 FROM %I.agent_tool_grants WHERE ref = 'builtin:browser'
             )
                OR EXISTS (SELECT 1 FROM %I.agents WHERE name = 'main')
            ON CONFLICT (tenant_id, capability) DO NOTHING
        $f$, t.schema_name, t.schema_name) USING t.schema_name;
    END LOOP;
END $$;

-- ---------------------------------------------------------------------------
-- 组引用展开：builtin:exec → builtin:shell / builtin:script
-- ---------------------------------------------------------------------------
-- 拆分前 builtin:exec 组含 exec / shell / run_skill_script，拆分后只剩 exec。
-- 存量里以「组」形式授权的行、以及自定义组的成员，若引用了 builtin:exec，
-- 会因此静默少掉 shell 与 run_skill_script —— 这里按原语义展开，保持行为不变。
-- 没有这类引用时是空操作；幂等（ON CONFLICT DO NOTHING）。
--
-- ⚠️ 勿重跑这一段：run_skill_script 后来已归回 builtin:exec（见文件头），
-- 现在重跑只会给引用 builtin:exec 的行多补一条展开为空的 builtin:script，
-- 属于脏数据。本节保留仅为记录当时的迁移动作。
DO $$
DECLARE t record;
BEGIN
    FOR t IN SELECT schema_name FROM platform.tenants LOOP
        -- ① agent 的组授权：同 agent、同 grant_kind，补两行
        IF EXISTS (SELECT 1 FROM information_schema.tables
                    WHERE table_schema = t.schema_name AND table_name = 'agent_tool_grants') THEN
            EXECUTE format($f$
                INSERT INTO %I.agent_tool_grants (agent_id, grant_kind, item_type, ref, updated_by, updated_at)
                SELECT g.agent_id, g.grant_kind, 'group', v.k, g.updated_by, now()
                  FROM %I.agent_tool_grants g
                 CROSS JOIN (VALUES ('builtin:shell'), ('builtin:script')) AS v(k)
                 WHERE g.item_type = 'group' AND g.ref = 'builtin:exec'
                ON CONFLICT DO NOTHING
            $f$, t.schema_name, t.schema_name);
        END IF;

        -- ② 自定义组的成员：原来一个 builtin:exec 成员代表三类工具，展开成三个成员
        IF EXISTS (SELECT 1 FROM information_schema.tables
                    WHERE table_schema = t.schema_name AND table_name = 'tool_group_items') THEN
            EXECUTE format($f$
                INSERT INTO %I.tool_group_items (group_id, item_type, ref)
                SELECT i.group_id, 'group', v.k
                  FROM %I.tool_group_items i
                 CROSS JOIN (VALUES ('builtin:shell'), ('builtin:script')) AS v(k)
                 WHERE i.item_type = 'group' AND i.ref = 'builtin:exec'
                ON CONFLICT DO NOTHING
            $f$, t.schema_name, t.schema_name);
        END IF;
    END LOOP;
END $$;

-- ---------------------------------------------------------------------------
-- ⚠️ 一个**有意**收紧的例外，升级前请确认
-- ---------------------------------------------------------------------------
-- sandbox.script 这次才开始真正生效：在此之前工作流 script 节点（走 /v1/script）
-- 完全没有能力闸，所以可能存在「exec 一直是关的、但租户靠工作流脚本在跑 Python」的用法。
-- 这类租户不在上面的回填范围里，升级后其流程脚本会以 capability 错误失败。
--
-- 这是本次要修的口径漏洞（关了执行能力却还能跑代码），不是漏回填。
-- 如确有需要，按租户手工开通：
--   INSERT INTO platform.tenant_capabilities (tenant_id, capability, is_allowed)
--   VALUES ('acme', 'sandbox.script', true)
--   ON CONFLICT (tenant_id, capability) DO UPDATE SET is_allowed = true, updated_at = now();
--
-- 提前排查：哪些租户的流程里出现过 script/code 节点
--   SELECT t.schema_name FROM platform.tenants t
--    WHERE EXISTS (
--        SELECT 1 FROM information_schema.tables
--         WHERE table_schema = t.schema_name AND table_name = 'flows'
--    );
--
-- ⚠️ 键名提醒：sandbox.script 已于 2026-10 更名为 sandbox.code
--    （见 capability_code_rename.sql）。上面的叙述与示例保留当时的原样写法，
--    新配置请直接写 sandbox.code。

-- ---------------------------------------------------------------------------
-- 自查
-- ---------------------------------------------------------------------------
--   SELECT tenant_id, capability, is_allowed, updated_at
--     FROM platform.tenant_capabilities ORDER BY tenant_id, capability;
--
-- 某个租户为什么用不了 exec / shell / script：
--   SELECT capability, is_allowed FROM platform.tenant_capabilities
--    WHERE tenant_id = 't0' ORDER BY capability;
--   SELECT capability, blocked FROM platform.capability_gates ORDER BY capability;

-- ---------------------------------------------------------------------------
-- 2026-10-21：去掉「系统默认行」这一层（tenant_id = ''）
-- ---------------------------------------------------------------------------
-- 它只对「没有租户行」的租户生效，与「全局硬闸」（platform.capability_gates）
-- 是两种完全不同的东西 —— 前者改的是缺省值，后者压过一切。放在同一张表里，
-- 管理员分不清「我关的」和「本来就没有」，也会误以为它是总闸。
--
-- 现在简化为两层：
--   生效 = (租户行 ?? 未开通) AND NOT blocked
--
-- 幂等：可重复执行。
-- ---------------------------------------------------------------------------

-- 1) 把「靠系统默认开着」的租户物化成租户行 —— 否则删掉系统行后它们会集体失效
INSERT INTO platform.tenant_capabilities (tenant_id, capability, is_allowed)
SELECT t.schema_name, o.capability, o.is_allowed
  FROM platform.tenants t
 CROSS JOIN platform.tenant_capabilities o
 WHERE o.tenant_id = '' AND o.is_allowed
ON CONFLICT (tenant_id, capability) DO NOTHING;

-- 2) 删掉系统默认行
DELETE FROM platform.tenant_capabilities WHERE tenant_id = '';

-- 3) 从数据库层封死：不允许再出现空 tenant_id —— 否则「三层」会悄悄回来
ALTER TABLE platform.tenant_capabilities
    DROP CONSTRAINT IF EXISTS tenant_capabilities_tenant_not_empty;
ALTER TABLE platform.tenant_capabilities
    ADD CONSTRAINT tenant_capabilities_tenant_not_empty CHECK (tenant_id <> '');
