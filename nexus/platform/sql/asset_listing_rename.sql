-- ============================================================================
-- 资产域术语统一：publish / published / offline → listing / listed / delisted
--
-- 背景：界面上一律叫「上架 / 下架」，代码与库统一跟界面一致（见 nexus/docs/terminology.md）。
--   动作：list / delist        状态：draft | pending | listed | rejected | delisted
--
-- 影响对象（都在管理 schema，默认 platform）：
--   shared_assets.published_by        → listed_by
--   shared_assets.published_by_name   → listed_by_name
--   shared_assets.status 'published'  → 'listed'
--   shared_assets.status 'offline'    → 'delisted'
--   publish_settings                  → listing_settings
--   publish_policies                  → listing_policies
--   publish_policies_review_idx       → listing_policies_review_idx
--
-- 端点域的 svc_endpoints.published **不在本脚本范围**：那里的语义是「对外开放」，
-- 已单独由 nexus/sql/svc_endpoints_exposed.sql 改成 exposed。
--
-- 幂等：每一步先探测再执行，可重复跑。
-- 顺序：**必须先于新版本程序上线执行** —— 新代码按 listed/delisted 与 listing_* 读写。
--
-- 执行：
--   psql -h <host> -p 5432 -U <user> -d <db> -f nexus/platform/sql/asset_listing_rename.sql
-- ============================================================================
DO $$
BEGIN
  -- 1) 列改名（published_by / published_by_name）
  IF EXISTS (
    SELECT 1 FROM information_schema.columns
     WHERE table_schema = 'platform' AND table_name = 'shared_assets'
       AND column_name = 'published_by'
  ) THEN
    ALTER TABLE platform.shared_assets RENAME COLUMN published_by TO listed_by;
  END IF;

  IF EXISTS (
    SELECT 1 FROM information_schema.columns
     WHERE table_schema = 'platform' AND table_name = 'shared_assets'
       AND column_name = 'published_by_name'
  ) THEN
    ALTER TABLE platform.shared_assets RENAME COLUMN published_by_name TO listed_by_name;
  END IF;

  -- 2) 状态值迁移 + CHECK 重建
  --    先删约束再改值：旧 CHECK 只允许 published/offline，直接 UPDATE 会被它拦下。
  ALTER TABLE platform.shared_assets DROP CONSTRAINT IF EXISTS shared_assets_status_check;
  UPDATE platform.shared_assets SET status = 'listed'   WHERE status = 'published';
  UPDATE platform.shared_assets SET status = 'delisted' WHERE status = 'offline';
  ALTER TABLE platform.shared_assets
    ADD CONSTRAINT shared_assets_status_check
    CHECK (status IN ('draft', 'pending', 'listed', 'rejected', 'delisted'));

  -- 3) 策略表与索引改名（只在旧名还在、新名还没有时动）
  IF to_regclass('platform.publish_settings') IS NOT NULL
     AND to_regclass('platform.listing_settings') IS NULL THEN
    ALTER TABLE platform.publish_settings RENAME TO listing_settings;
  END IF;

  IF to_regclass('platform.publish_policies') IS NOT NULL
     AND to_regclass('platform.listing_policies') IS NULL THEN
    ALTER TABLE platform.publish_policies RENAME TO listing_policies;
  END IF;

  IF to_regclass('platform.publish_policies_review_idx') IS NOT NULL
     AND to_regclass('platform.listing_policies_review_idx') IS NULL THEN
    ALTER INDEX platform.publish_policies_review_idx RENAME TO listing_policies_review_idx;
  END IF;
END $$;

-- 4) 注释按新术语重写（旧库的列注释还写着「发布人」）

COMMENT ON TABLE  platform.shared_assets          IS '跨租户分发的资产快照（一行 = 一个版本）';
COMMENT ON COLUMN platform.shared_assets.status   IS 'draft|pending|listed|rejected|delisted（草稿|待审核|已上架|已驳回|已下架）';
COMMENT ON COLUMN platform.shared_assets.listed_by      IS '上架人（{schema}.users.id，仅审计）';
COMMENT ON COLUMN platform.shared_assets.listed_by_name IS '上架人展示名快照';

-- ============================================================================
-- 执行后核对
-- ============================================================================
-- a) 列已改名
--   SELECT column_name FROM information_schema.columns
--    WHERE table_schema='platform' AND table_name='shared_assets'
--      AND column_name LIKE '%listed%';
--
-- b) 状态取值只剩新词（应无 published / offline）
--   SELECT status, count(*) FROM platform.shared_assets GROUP BY status;
--
-- c) 策略表已改名
--   SELECT to_regclass('platform.listing_settings'), to_regclass('platform.listing_policies');
--   SELECT require_review FROM platform.listing_settings WHERE id = 1;
