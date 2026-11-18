-- 平台级「全局硬闸」：工具能力的总开关（急停）。
--
-- 与 tenant_capabilities 的分工（两者都要过，缺一不可）：
--
--	platform.tenant_capabilities   这个租户**能不能用**（缺省拒绝，逐租户）
--	platform.capability_gates      这项能力**还开不开**（缺省放行，全局）
--
-- 生效 = (租户行 ?? 未开通) AND NOT blocked
--
-- 注意这不是「第四层回退」：回退链遇到有值的层就停下，而硬闸必须能压过租户的
-- 显式开启 —— 所以它是最后乘上去的 0，不是链上的一环。
--
-- 为什么独立成表，而不是在 tenant_capabilities 里加一行 tenant_id='*'：
--   - 缺省语义相反：能力缺省**拒绝**，闸缺省必须**放行**；混在一张表里必然搞反；
--   - 它不是「某个租户的策略」，塞进租户维度会让 Resolve/Rows 的两值判断
--     （租户行 / 无）长出第三个哨兵分支；
--   - 恢复缺省 = 删行，不会留下「一条永远为 true 的记录」误导排查。
--
-- 幂等：可重复执行。

CREATE TABLE IF NOT EXISTS platform.capability_gates (
    capability varchar(32) PRIMARY KEY,
    -- 反向命名（blocked 而不是 allowed）：闸是「拦」，没有行 / false 都表示不拦。
    blocked    boolean     NOT NULL DEFAULT false,
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- 键名白名单：脏 key 落库会让「没设闸」与「配错了」看起来一样。
-- 与 tenant_capabilities 同一做法：先 DROP IF EXISTS 再 ADD，扩容时只改这里。
ALTER TABLE platform.capability_gates
    DROP CONSTRAINT IF EXISTS capability_gates_key_check;
ALTER TABLE platform.capability_gates
    ADD CONSTRAINT capability_gates_key_check
    -- 过渡期同时允许 sandbox.script（2026-10 更名为 sandbox.code）：收窄留到删别名的版本，
    -- 否则老库重跑本文件会因存量旧行不满足约束而失败。
    CHECK (capability IN ('sandbox.exec', 'sandbox.shell', 'sandbox.code', 'sandbox.script', 'sandbox.browser', 'net.egress'));
