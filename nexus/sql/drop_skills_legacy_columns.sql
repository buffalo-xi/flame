-- ============================================================================
-- skills 表移除三个遗留列：skill_category / source_type / skill_type
-- ----------------------------------------------------------------------------
-- 说明：
--   * 三列均为历史遗留（建表模板从外部产品继承而来），**代码从不读取或写入**，
--     也没有任何逻辑根据它们的取值分支。
--   * skill_category 尤其明显：建表时就带 CHECK (skill_category = 'business')，
--     只允许一个取值 —— 死字段的直接证据。
--   * skill_type 的 plugin 取值在前端标注为"预留"，前后端均无行为差异。
--   * 幂等：可重复执行；自动遍历所有含 skills 表的租户 schema。
--   * 建表模板 nexus/platform/provision/tenant_template.sql 已同步移除这三列
--     （含 3 个 CHECK 约束、2 个索引、2 条列注释）。
-- 执行方式：
--   psql -h <host> -p <port> -U <user> -d <db> -f nexus/sql/drop_skills_legacy_columns.sql
--
-- 为什么可以安全删除：
--   - DB 层：三列均 NOT NULL + DEFAULT，无外键引用、无其它表/视图依赖；
--     依赖它们的两个索引（idx_tenant_agent_skills_category / idx_tenant_agent_skills_type）
--     随列一起删除。
--   - Go 层：引用点只有 skillColumns / scanSkill / INSERT / UPDATE / List 过滤
--     （shared/repository/skills）、skillToMap / validSkillType / validSourceType
--     （admin/skills）、models.Skill 的三个字段、tools 的 SkillInfo/SkillInput，
--     以及三个创建工具的 skill_type 参数 —— 已全部同步移除（go build ./... 通过）。
--   - 顺序无关：DEFAULT 保证 Go 侧停写这三列后 INSERT 仍然成立，
--     所以"先上代码、后跑本脚本"与"先跑脚本、后上代码"都安全。
-- ============================================================================
DO $$
DECLARE
    s text;
BEGIN
    FOR s IN
        SELECT table_schema
          FROM information_schema.tables
         WHERE table_name = 'skills'
           AND table_schema NOT IN ('pg_catalog', 'information_schema', 'public', 'platform')
         ORDER BY table_schema
    LOOP
        -- 索引会随列自动删除，这里显式写出只为留下可读的执行痕迹（IF EXISTS 保证幂等）
        EXECUTE format('DROP INDEX IF EXISTS %I.idx_tenant_agent_skills_category', s);
        EXECUTE format('DROP INDEX IF EXISTS %I.idx_tenant_agent_skills_type', s);

        -- DROP COLUMN 会连带删除该列上的 CHECK 约束与 COMMENT，无需单独处理
        EXECUTE format('ALTER TABLE %I.skills DROP COLUMN IF EXISTS skill_category', s);
        EXECUTE format('ALTER TABLE %I.skills DROP COLUMN IF EXISTS source_type', s);
        EXECUTE format('ALTER TABLE %I.skills DROP COLUMN IF EXISTS skill_type', s);

        RAISE NOTICE 'dropped %.skills.{skill_category,source_type,skill_type} (if existed)', s;
    END LOOP;
END $$;

-- 验证：skills 表当前列（应看不到那三列）
SELECT table_schema, column_name, data_type
  FROM information_schema.columns
 WHERE table_name = 'skills'
   AND table_schema NOT IN ('pg_catalog', 'information_schema', 'public', 'platform')
 ORDER BY table_schema, ordinal_position;

-- ----------------------------------------------------------------------------
-- 附：删除前抽查（确认无数据损失的备查语句，按需单独执行）
--
-- DO $$
-- DECLARE s text; n bigint;
-- BEGIN
--     FOR s IN
--         SELECT table_schema FROM information_schema.tables
--          WHERE table_name = 'skills'
--            AND table_schema NOT IN ('pg_catalog', 'information_schema', 'public', 'platform')
--     LOOP
--         -- 期望 0：既有 skill 全部是默认取值。若不为 0，说明有人手工写过这些列，
--         -- 删除前值得先看一眼（尤其是 skill_type / source_type 是否被当作业务字段用）
--         EXECUTE format('SELECT count(*) FROM %I.skills
--                          WHERE skill_type <> ''document''
--                             OR skill_category <> ''business''
--                             OR source_type <> ''user''', s) INTO n;
--         RAISE NOTICE '%: 非默认取值的 skill 行 = %', s, n;
--     END LOOP;
-- END $$;
-- ============================================================================
