-- 平台级「全局停用」：插件的急停总闸。
--
-- 与 tenant_plugins 的分工（两者都要过，缺一不可）：
--
--	platform.tenant_plugins   这个租户**授没授权**（缺省拒绝，逐租户）
--	platform.plugin_gates     这个插件**还开不开**（缺省放行，全局）
--
-- 生效 = (租户行 ?? 未开通) AND NOT blocked
--
-- 注意这不是「第三层回退」：回退链遇到有值的层就停下，而硬闸必须能压过租户的
-- 显式开启 —— 所以它是最后乘上去的 0，不是链上的一环。
--
-- 为什么独立成表，而不是在 tenant_plugins 里加一行 tenant_id='*'：
--   - 缺省语义相反：授权缺省**拒绝**，闸缺省必须**放行**；混在一张表里必然搞反；
--   - 它不是「某个租户的策略」，塞进租户维度会让 Resolve/Rows 的单值判断
--     长出第三个哨兵分支；
--   - 恢复缺省 = 删行，不会留下「一条永远为 false 的记录」误导排查。
--
-- 为什么需要它（删掉系统默认行之后更需要）：停用一个插件只剩两条路 ——
-- 给 N 个租户逐个写 false（改掉每家的配置，事后不知道谁原本是 true），
-- 或者直接删行（同样丢配置）。急停是最不该动配置的操作。
--
-- 位置：管理 schema（config.database.platform_schema，默认 platform）
-- 执行：psql -h <host> -p <port> -U <user> -d <db> -f nexus/platform/sql/plugin_gates.sql
-- 幂等：可重复执行。
-- ============================================================================

CREATE TABLE IF NOT EXISTS platform.plugin_gates (
    -- 与 tenant_plugins 同口径：不同种类的插件是独立命名空间，
    -- 同名（tools/web_search 与将来的 sources/web_search）不能互相误伤。
    kind        varchar(16) NOT NULL DEFAULT 'tools',
    plugin_name varchar(64) NOT NULL,
    -- 反向命名（blocked 而不是 allowed）：闸是「拦」，没有行 / false 都表示不拦。
    blocked     boolean     NOT NULL DEFAULT false,
    updated_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (kind, plugin_name)
);

-- 刻意不做插件名白名单：名字来自磁盘扫描（plugininfo.Tools / service/plugin Registry），
-- 无法在 SQL 里穷举；PUT 接口侧用 mustPlugin 校验名字在清单里，避免脏名落库。

-- 验证
SELECT kind, plugin_name, blocked, updated_at
  FROM platform.plugin_gates
 ORDER BY kind, plugin_name
 LIMIT 20;
