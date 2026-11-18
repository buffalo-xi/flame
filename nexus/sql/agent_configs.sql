-- ============================================================================
-- platform.agent_configs：智能体运行参数（平台硬顶 + 租户设置）
-- ----------------------------------------------------------------------------
--   语义（与 platform.flow_configs / quota_configs 完全一致）：
--     tenant_id = ''      系统行（全局默认，同时是硬顶）
--     tenant_id = 't0'    租户行（覆盖系统行，但只能往下压）
--     NULL                未设置（回退上级；系统行也未设置时用代码 fallback）
--
--   ⚠️ 与 flow_configs 的唯一差别：本表**不接受 -1（不限制）**。
--   这四个字段都是「安全阀」而不是「配额」：
--     max_iterations   = -1 → 取消防死循环，agent 永远跑不完也不停
--     max_call_depth   = -1 → A→B→A 的级联可以无限递归
--     max_tools        = -1 → 工具集不截断，直接把上下文顶爆
--     max_output_bytes = -1 → 一条命令的输出可以吃掉整台机器的内存
--   所以写入入口（admin / platform 两侧 handler）一律拒绝 -1，只接受 >= 1，
--   并由下面的 CHECK 约束兜底。Resolve 侧另把 <= 0 归一成 fallback，
--   防止有人绕过接口直接改库写出怪值。
--
--   运行期读取走 shared/agentconfig.Resolve（Redis 缓存，5min TTL 兜底）。
--   写库后必须调用 agentconfig.Invalidate / InvalidateAll，否则要等一个 TTL 周期。
--
-- 幂等：可重复执行。
-- ============================================================================
CREATE TABLE IF NOT EXISTS platform.agent_configs (
    tenant_id        varchar(32) PRIMARY KEY,          -- '' = 系统行，'t0' = 租户行
    max_iterations   int         NULL,                 -- 单次 run 最大轮次
    max_call_depth   int         NULL,                 -- agent 级联调用最大深度
    max_tools        int         NULL,                 -- 绑定工具数量上限（超出截断）
    max_output_bytes int         NULL,                 -- 单条命令输出字节上限
    updated_at       timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT agent_configs_positive CHECK (
        (max_iterations   IS NULL OR max_iterations   >= 1) AND
        (max_call_depth   IS NULL OR max_call_depth   >= 1) AND
        (max_tools        IS NULL OR max_tools        >= 1) AND
        (max_output_bytes IS NULL OR max_output_bytes >= 1)
    )
);

COMMENT ON TABLE platform.agent_configs IS
    '智能体运行参数：tenant_id='''' 为系统行（硬顶），''t0'' 为租户行；NULL=未设置；本表不接受 -1';
COMMENT ON COLUMN platform.agent_configs.max_iterations IS '单次 run 最大轮次（防死循环，代码 fallback 80）';
COMMENT ON COLUMN platform.agent_configs.max_call_depth IS 'agent 级联调用最大深度（防 A→B→A 递归，fallback 3）';
COMMENT ON COLUMN platform.agent_configs.max_tools IS '绑定工具数量上限（超出截断并告警，fallback 50）';
COMMENT ON COLUMN platform.agent_configs.max_output_bytes IS '单条命令输出字节上限（fallback 65536）';

-- 验证：表已建、约束在位
SELECT conname, pg_get_constraintdef(oid)
  FROM pg_constraint
 WHERE conrelid = 'platform.agent_configs'::regclass
 ORDER BY conname;
