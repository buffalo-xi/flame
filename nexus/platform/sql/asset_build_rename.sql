-- ============================================================================
-- 资产快照序号改名：version → build
--
-- 背景：资产的 version 不是实体的版本，而是「第几次打包上架」的序号 ——
--   每次上架产出一份快照目录 platform-assets/<asset_id>/<build>/，序号 +1。
--   叫 build 才不会和实体内容修订、版本发布混在一起（见 nexus/docs/terminology.md）。
--
-- 影响：
--   platform.shared_assets.version          → build
--   platform.shared_asset_installs.version  → build
--
-- 磁盘目录**不动**：目录名是数字，不含字段名。
--
-- 幂等：先探测旧列是否还在。
-- 注意：导出包 origin.version 同步改成 origin.build，改前导出的包不再能导入（不做兼容）。
--
-- 执行：
--   psql -h <host> -p 5432 -U <user> -d <db> -f nexus/platform/sql/asset_build_rename.sql
-- ============================================================================
DO $$
BEGIN
  IF EXISTS (
    SELECT 1 FROM information_schema.columns
     WHERE table_schema = 'platform' AND table_name = 'shared_assets' AND column_name = 'version'
  ) THEN
    ALTER TABLE platform.shared_assets RENAME COLUMN version TO build;
  END IF;

  IF EXISTS (
    SELECT 1 FROM information_schema.columns
     WHERE table_schema = 'platform' AND table_name = 'shared_asset_installs' AND column_name = 'version'
  ) THEN
    ALTER TABLE platform.shared_asset_installs RENAME COLUMN version TO build;
  END IF;
END $$;

COMMENT ON COLUMN platform.shared_assets.build IS '打包快照序号：每次上架 +1，对应 platform-assets/<id>/<build>/ 一份快照';
COMMENT ON COLUMN platform.shared_asset_installs.build IS '安装时的那份快照序号';

-- ============================================================================
-- 执行后核对
-- ============================================================================
-- a) 列已改名
--   SELECT table_name, column_name FROM information_schema.columns
--    WHERE table_schema='platform' AND column_name IN ('version','build')
--      AND table_name IN ('shared_assets','shared_asset_installs');
-- b) 数据
--   SELECT id, kind, name, build, status FROM platform.shared_assets ORDER BY build;
--   SELECT asset_id, schema_name, build, installed_name FROM platform.shared_asset_installs;
--
-- 回滚
--   ALTER TABLE platform.shared_assets RENAME COLUMN build TO version;
--   ALTER TABLE platform.shared_asset_installs RENAME COLUMN build TO version;
