-- ============================================================================
-- 能力键更名：sandbox.script → sandbox.code
-- ----------------------------------------------------------------------------
-- 为什么改：
--   sandbox.script 的语义是「按进程形态分档」里的第三档（对应 /v1/code 通道），
--   但 exec 通道同样在执行脚本文件（.py/.js/.lua/.sh），用 script 命名等于把两条
--   通道共有的性质当成其中一条的名字。真正的契约差别是「解释器与输入输出协议由谁
--   定」：exec 由调用方给完整 argv，code 通道由服务端按包装模板托管解释器。
--   也不叫 pool：池化是服务端实现，实现一变名字就失真。
--
-- 影响面：能力键是**库里的值**，不是代码常量。代码已同时认新旧键
--   （capability.Normalize），所以本脚本只负责把存量行搬到新键上，
--   上线顺序：先发代码（认两个键）→ 跑本脚本 → 下个版本删别名。
--
-- 幂等：可重复执行。
-- 顺序：⚠️ 先放宽白名单再 UPDATE。约束若还只允许旧键，UPDATE 会当场撞
--   tenant_capabilities_key_check（第一次执行就是踩在这上面）。
--   跑完后仍可重跑 tenant_capabilities.sql / capability_gates.sql 基线
--   （两者的 CHECK 已同时允许新旧键）。
-- ============================================================================

-- 1) 白名单：过渡期同时允许新旧键。
--    不在这里收窄成只剩新键 —— 本脚本可能先于基线文件被执行，而基线文件里的
--    CHECK 若已收窄，老环境重跑基线会因旧行不满足约束而失败。收窄留到删别名的版本。
ALTER TABLE platform.tenant_capabilities
    DROP CONSTRAINT IF EXISTS tenant_capabilities_key_check;
ALTER TABLE platform.tenant_capabilities
    ADD CONSTRAINT tenant_capabilities_key_check
    CHECK (capability IN ('sandbox.exec', 'sandbox.shell', 'sandbox.code', 'sandbox.script', 'sandbox.browser', 'net.egress'));

ALTER TABLE platform.capability_gates
    DROP CONSTRAINT IF EXISTS capability_gates_key_check;
ALTER TABLE platform.capability_gates
    ADD CONSTRAINT capability_gates_key_check
    CHECK (capability IN ('sandbox.exec', 'sandbox.shell', 'sandbox.code', 'sandbox.script', 'sandbox.browser', 'net.egress'));

-- 2) 两行同义并存时保留新键行（否则下一步 UPDATE 会撞主键）
DELETE FROM platform.tenant_capabilities o
 WHERE o.capability = 'sandbox.script'
   AND EXISTS (
       SELECT 1 FROM platform.tenant_capabilities n
        WHERE n.tenant_id = o.tenant_id
          AND n.capability = 'sandbox.code'
   );

DELETE FROM platform.capability_gates o
 WHERE o.capability = 'sandbox.script'
   AND EXISTS (
       SELECT 1 FROM platform.capability_gates n
        WHERE n.capability = 'sandbox.code'
   );

-- 3) 搬到新键
UPDATE platform.tenant_capabilities
   SET capability = 'sandbox.code',
       updated_at = now()
 WHERE capability = 'sandbox.script';

UPDATE platform.capability_gates
   SET capability = 'sandbox.code',
       updated_at = now()
 WHERE capability = 'sandbox.script';

-- 4) 核对：应无旧键残留（两行都是 0）
--   SELECT 'tenant_capabilities' AS tbl, count(*) FROM platform.tenant_capabilities WHERE capability = 'sandbox.script'
--   UNION ALL
--   SELECT 'capability_gates', count(*) FROM platform.capability_gates WHERE capability = 'sandbox.script';
