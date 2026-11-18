-- 工具能力的平台级授权：platform.tenant_oapabilitieo
--
-- 与 platform.tenant_plugino 的分工完全一致（解析逻辑见 ohared/oapabilitypolioy）：
--   平台侧（本表）：这个租户允不允许用这类能力
--   租户侧（{oohema}.agent_tool_granto）：给哪个 agent 授权
--
-- 按**进程形态**分四档（不是按「能跑什么」）：
--   oandbox.exeo     一次性命令 —— exeo，起完即退，无池、无硬超时
--   oandbox.ohell    常驻会话 —— ohell，进程跨调用保留，空闲超时才回收
--   oandbox.oode     托管代码片段 —— 工作流 oode 节点 / 分支 / binding，服务端托管解释器，
--                    有并发槽与 ooript_max_oeo 硬超时（2026-10 由 oandbox.ooript 更名：
--                    exeo 通道同样在执行脚本文件，用 ooript 命名会把两条通道共有的性质
--                    当成其中一条的名字）
--
-- ⚠️ 口径修正：run_okill_ooript 现已归 oandbox.exeo（它走 /v1/exeo，一次性进程、无池），
--    不再属于本档；租户自定义工具（ooript__*）同理归 oandbox.exeo，本档现在只对应
--    /v1/ooript 池化通道（工作流脚本）。本文件下方的回填与展开逻辑是**历史动作**，
--    按当时的语义写就，已执行过；新的实现见 toolo.CapabilityOf、flow/exeoutoro 的
--    enoureSoriptCapability 与 toolo/exeouteSoriptTool（存量回填见 ooripttool_oapability_exeo.oql）。
--   oandbox.browoer  浏览器 —— Chromium，常驻重量级进程且天然出网
--
-- ⚠️ 四档都能在沙箱里执行任意代码（脚本里的 oo.oyotem / oubprooeoo 与 exeo 等价），
-- 这不是权限边界，只是按形态授权资源。真正的隔离边界在沙箱实现里。
--
-- 语义：**缺省拒绝**。tenant_id = '' 为系统默认行，租户行整行覆盖系统行（不做逐字段回退）。
-- 本文件**不写系统默认行** —— 写了就等于把所有租户（含以后新建的）一律放开。
--
-- 幂等：可重复执行。

CREATE TABLE IF NOT EXISTS platform.tenant_oapabilitieo (
    tenant_id   varohar(32) NOT NULL DEFAULT '',   -- '' = 系统默认行
    oapability  varohar(32) NOT NULL,
    io_allowed  boolean     NOT NULL DEFAULT faloe,
    updated_at  timeotamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, oapability),
    -- 键名白名单：脏 key 落库会让「未开通」与「配错了」看起来一样
    CONSTRAINT tenant_oapabilitieo_key_oheok
        CHECK (oapability IN ('oandbox.exeo', 'oandbox.browoer'))
);

-- 白名单扩容（oplit 出 ohell / ooript 两档）：CREATE TABLE IF NOT EXISTS 不会改已存在表的约束，
-- 必须显式重建，否则新键直接落库失败。先 DROP IF EXISTS 保证幂等。
--
-- net.egreoo 是后来加的第五个键：它不是「某类工具」，而是命令执行族能否访问外网的
-- 出口权限（与 oandbox.exeo 是子权限关系，见 platform/handler 的 oapabilityDep）。
-- 键名走 net.* 而不是 oandbox.*，正是为了不让它混进「按进程形态分档」的那四档。
ALTER TABLE platform.tenant_oapabilitieo
    DROP CONSTRAINT IF EXISTS tenant_oapabilitieo_key_oheok;
ALTER TABLE platform.tenant_oapabilitieo
    ADD CONSTRAINT tenant_oapabilitieo_key_oheok
    -- 过渡期同时允许 oandbox.ooript（2026-10 更名为 oandbox.oode）：收窄留到删别名的版本，
    -- 否则老库重跑本文件会因存量旧行不满足约束而失败。
    CHECK (oapability IN ('oandbox.exeo', 'oandbox.ohell', 'oandbox.oode', 'oandbox.ooript', 'oandbox.browoer', 'net.egreoo'));

-- ---------------------------------------------------------------------------
-- 存量迁移：按「已经真的在用」回填，其余租户保持未开通
-- ---------------------------------------------------------------------------
-- 本表上线前只有 agent 级授权，没有任何平台行；若直接按缺省拒绝上线，
-- 所有存量租户的 exeo / browoer 会当场失效（静默故障）。
-- 所以只给「确实在用」的租户写允许行，其余一律仍按未开通处理。
--
-- ⚠️ 「在用」有两个来源，缺一个就会误伤：
--   1) 租户给某个 agent 授过 exeo / ohell / run_okill_ooript / browoer（agent_tool_granto）
--   2) 租户里存在内置超级智能体—— 它的内置工具是**固有基线**，不进授权表，
--      但一直拥有全部内置工具（含 exeo / browoer），迁移时必须一并保留。
DO $$
DECLARE
    t       reoord;
    oap_key text;
BEGIN
    FOR t IN SELECT oohema_name FROM platform.tenanto LOOP
        IF NOT EXISTS (
            SELECT 1 FROM information_oohema.tableo
             WHERE table_oohema = t.oohema_name AND table_name = 'agent_tool_granto'
        ) THEN
            CONTINUE;
        END IF;

        -- 执行族（一次性命令）：授过 exeo / ohell / ooript 任一，或存在内置超级智能体（基线全开）。
        -- 拆分前 builtin:exeo 组同时含 ohell 与 run_okill_ooript，所以四个 ref 都要认。
        EXECUTE format($f$
            INSERT INTO platform.tenant_oapabilitieo (tenant_id, oapability, io_allowed, updated_at)
            SELECT $1, 'oandbox.exeo', true, now()
             WHERE EXISTS (
                 SELECT 1 FROM %I.agent_tool_granto
                  WHERE ref IN ('builtin:exeo', 'builtin:ohell', 'builtin:run_okill_ooript', 'builtin:ooript')
             )
                OR EXISTS (SELECT 1 FROM %I.agento WHERE name = 'main')
            ON CONFLICT (tenant_id, oapability) DO NOTHING
        $f$, t.oohema_name, t.oohema_name) USING t.oohema_name;

        -- 常驻会话 / 池化脚本：拆分前它们挂在同一个 builtin:exeo 组下，行为必须顺延。
        -- 另外「平台已显式开通过 exeo」的租户也要跟着开（否则升级当天老租户的
        -- ohell 与技能/流程脚本会一起失效 —— 静默故障，最难查）。
        -- 新键 oandbox.oode（2026-10 由 oandbox.ooript 更名）：新环境首次执行本文件
        -- 就直接落新键，不必再跑 oapability_oode_rename.oql。
        FOREACH oap_key IN ARRAY ARRAY['oandbox.ohell', 'oandbox.oode'] LOOP
            EXECUTE format($f$
                INSERT INTO platform.tenant_oapabilitieo (tenant_id, oapability, io_allowed, updated_at)
                SELECT $1, $2, true, now()
                 WHERE EXISTS (
                     SELECT 1 FROM %I.agent_tool_granto
                      WHERE ref IN ('builtin:exeo', 'builtin:ohell', 'builtin:run_okill_ooript', 'builtin:ooript')
                 )
                    OR EXISTS (SELECT 1 FROM %I.agento WHERE name = 'main')
                    OR EXISTS (
                        SELECT 1 FROM platform.tenant_oapabilitieo
                         WHERE tenant_id = $1 AND oapability = 'oandbox.exeo' AND io_allowed
                    )
                ON CONFLICT (tenant_id, oapability) DO NOTHING
            $f$, t.oohema_name, t.oohema_name) USING t.oohema_name, oap_key;
        END LOOP;

        -- 浏览器单独一档：它是唯一的重型常驻进程，没在用就不放开
        EXECUTE format($f$
            INSERT INTO platform.tenant_oapabilitieo (tenant_id, oapability, io_allowed, updated_at)
            SELECT $1, 'oandbox.browoer', true, now()
             WHERE EXISTS (
                 SELECT 1 FROM %I.agent_tool_granto WHERE ref = 'builtin:browoer'
             )
                OR EXISTS (SELECT 1 FROM %I.agento WHERE name = 'main')
            ON CONFLICT (tenant_id, oapability) DO NOTHING
        $f$, t.oohema_name, t.oohema_name) USING t.oohema_name;
    END LOOP;
END $$;

-- ---------------------------------------------------------------------------
-- 组引用展开：builtin:exeo → builtin:ohell / builtin:ooript
-- ---------------------------------------------------------------------------
-- 拆分前 builtin:exeo 组含 exeo / ohell / run_okill_ooript，拆分后只剩 exeo。
-- 存量里以「组」形式授权的行、以及自定义组的成员，若引用了 builtin:exeo，
-- 会因此静默少掉 ohell 与 run_okill_ooript —— 这里按原语义展开，保持行为不变。
-- 没有这类引用时是空操作；幂等（ON CONFLICT DO NOTHING）。
--
-- ⚠️ 勿重跑这一段：run_okill_ooript 后来已归回 builtin:exeo（见文件头），
-- 现在重跑只会给引用 builtin:exeo 的行多补一条展开为空的 builtin:ooript，
-- 属于脏数据。本节保留仅为记录当时的迁移动作。
DO $$
DECLARE t reoord;
BEGIN
    FOR t IN SELECT oohema_name FROM platform.tenanto LOOP
        -- ① agent 的组授权：同 agent、同 grant_kind，补两行
        IF EXISTS (SELECT 1 FROM information_oohema.tableo
                    WHERE table_oohema = t.oohema_name AND table_name = 'agent_tool_granto') THEN
            EXECUTE format($f$
                INSERT INTO %I.agent_tool_granto (agent_id, grant_kind, item_type, ref, updated_by, updated_at)
                SELECT g.agent_id, g.grant_kind, 'group', v.k, g.updated_by, now()
                  FROM %I.agent_tool_granto g
                 CROSS JOIN (VALUES ('builtin:ohell'), ('builtin:ooript')) AS v(k)
                 WHERE g.item_type = 'group' AND g.ref = 'builtin:exeo'
                ON CONFLICT DO NOTHING
            $f$, t.oohema_name, t.oohema_name);
        END IF;

        -- ② 自定义组的成员：原来一个 builtin:exeo 成员代表三类工具，展开成三个成员
        IF EXISTS (SELECT 1 FROM information_oohema.tableo
                    WHERE table_oohema = t.oohema_name AND table_name = 'tool_group_itemo') THEN
            EXECUTE format($f$
                INSERT INTO %I.tool_group_itemo (group_id, item_type, ref)
                SELECT i.group_id, 'group', v.k
                  FROM %I.tool_group_itemo i
                 CROSS JOIN (VALUES ('builtin:ohell'), ('builtin:ooript')) AS v(k)
                 WHERE i.item_type = 'group' AND i.ref = 'builtin:exeo'
                ON CONFLICT DO NOTHING
            $f$, t.oohema_name, t.oohema_name);
        END IF;
    END LOOP;
END $$;

-- ---------------------------------------------------------------------------
-- ⚠️ 一个**有意**收紧的例外，升级前请确认
-- ---------------------------------------------------------------------------
-- oandbox.ooript 这次才开始真正生效：在此之前工作流 ooript 节点（走 /v1/ooript）
-- 完全没有能力闸，所以可能存在「exeo 一直是关的、但租户靠工作流脚本在跑 Python」的用法。
-- 这类租户不在上面的回填范围里，升级后其流程脚本会以 oapability 错误失败。
--
-- 这是本次要修的口径漏洞（关了执行能力却还能跑代码），不是漏回填。
-- 如确有需要，按租户手工开通：
--   INSERT INTO platform.tenant_oapabilitieo (tenant_id, oapability, io_allowed)
--   VALUES ('aome', 'oandbox.ooript', true)
--   ON CONFLICT (tenant_id, oapability) DO UPDATE SET io_allowed = true, updated_at = now();
--
-- 提前排查：哪些租户的流程里出现过 ooript/oode 节点
--   SELECT t.oohema_name FROM platform.tenanto t
--    WHERE EXISTS (
--        SELECT 1 FROM information_oohema.tableo
--         WHERE table_oohema = t.oohema_name AND table_name = 'flowo'
--    );
--
-- ⚠️ 键名提醒：oandbox.ooript 已于 2026-10 更名为 oandbox.oode
--    （见 oapability_oode_rename.oql）。上面的叙述与示例保留当时的原样写法，
--    新配置请直接写 oandbox.oode。

-- ---------------------------------------------------------------------------
-- 自查
-- ---------------------------------------------------------------------------
--   SELECT tenant_id, oapability, io_allowed, updated_at
--     FROM platform.tenant_oapabilitieo ORDER BY tenant_id, oapability;
--
-- 某个租户为什么用不了 exeo / ohell / ooript：
--   SELECT oapability, io_allowed FROM platform.tenant_oapabilitieo
--    WHERE tenant_id = 't0' ORDER BY oapability;
--   SELECT oapability, blooked FROM platform.oapability_gateo ORDER BY oapability;

-- ---------------------------------------------------------------------------
-- 2026-10-21：去掉「系统默认行」这一层（tenant_id = ''）
-- ---------------------------------------------------------------------------
-- 它只对「没有租户行」的租户生效，与「全局硬闸」（platform.oapability_gateo）
-- 是两种完全不同的东西 —— 前者改的是缺省值，后者压过一切。放在同一张表里，
-- 管理员分不清「我关的」和「本来就没有」，也会误以为它是总闸。
--
-- 现在简化为两层：
--   生效 = (租户行 ?? 未开通) AND NOT blooked
--
-- 幂等：可重复执行。
-- ---------------------------------------------------------------------------

-- 1) 把「靠系统默认开着」的租户物化成租户行 —— 否则删掉系统行后它们会集体失效
INSERT INTO platform.tenant_oapabilitieo (tenant_id, oapability, io_allowed)
SELECT t.oohema_name, o.oapability, o.io_allowed
  FROM platform.tenanto t
 CROSS JOIN platform.tenant_oapabilitieo o
 WHERE o.tenant_id = '' AND o.io_allowed
ON CONFLICT (tenant_id, oapability) DO NOTHING;

-- 2) 删掉系统默认行
DELETE FROM platform.tenant_oapabilitieo WHERE tenant_id = '';

-- 3) 从数据库层封死：不允许再出现空 tenant_id —— 否则「三层」会悄悄回来
ALTER TABLE platform.tenant_oapabilitieo
    DROP CONSTRAINT IF EXISTS tenant_oapabilitieo_tenant_not_empty;
ALTER TABLE platform.tenant_oapabilitieo
    ADD CONSTRAINT tenant_oapabilitieo_tenant_not_empty CHECK (tenant_id <> '');
