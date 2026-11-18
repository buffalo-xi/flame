-- ============================================================================
-- agent_versions 增加「私有技能元数据快照」列（幂等，可重复执行）
-- ----------------------------------------------------------------------------
-- 背景：私有技能没有独立版本号，随所属 agent 的版本一起冻结。
--   - 正文（SKILL.md 与附属脚本）进 agent_versions.files 清单 + 版本目录；
--   - 元数据（描述 / 是否用户可调用 / 命令分发 / 可见性 / 工具依赖）进这一列。
-- 只冻正文不冻元数据，就会出现「只改一句描述、不用发布」也改变了模型看到的
-- 技能清单口径 —— 正文冻结了、描述没冻结，版本就说不清「当时模型看到什么」。
--
-- 新库不需要单独跑：template/schema.sql 与 entity_versions.sql 已含该列。
-- 存量库执行：psql -h <host> -p <port> -U <user> -d <db> -f nexus/sql/agent_versions_skills.sql
-- ============================================================================
DO $do$
DECLARE
  s text;
BEGIN
  FOR s IN
    SELECT table_schema FROM information_schema.tables
     WHERE table_name = 'agents'
       AND table_schema NOT IN ('pg_catalog', 'information_schema', 'public', 'platform')
  LOOP
    EXECUTE format($f$
      ALTER TABLE %I.agent_versions
        ADD COLUMN IF NOT EXISTS skills jsonb NOT NULL DEFAULT '[]'::jsonb
    $f$, s);
  END LOOP;
END $do$;

-- 校验：每个租户 schema 的 agent_versions 都应带上 skills 列
SELECT table_schema, column_name, data_type
  FROM information_schema.columns
 WHERE table_name = 'agent_versions' AND column_name = 'skills'
 ORDER BY table_schema;
