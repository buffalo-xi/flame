-- ============================================================================
-- 资产包内容指纹（digest）
--
-- 背景：Manifest 里原来只有 kind/name/entry/deps/files，没有「这份包的内容标识」。
--   于是「同一份包重复导入」会照常走一遍覆盖（目录重铺、版本状态被改成「有未发布改动」），
--   「A 站装的和 B 站装的是不是同一份内容」也无从判断。
--
-- 做法（不新增表，也不改 shared_assets）：
--   platform.shared_assets.manifest 里多了两个 JSON 键：
--     spec    清单/payload 的语义版本（整数，缺省 = 1；比程序支持的新 → 导入端拒绝）
--     digest  内容指纹 = sha256(归一化 payload + 依赖声明 + 按 path 排序的文件清单)
--   manifest 是 jsonb，加键不需要 DDL。
--
--   只有「安装记录」需要一个列，用来在**覆盖前**回答「上次装的是不是同一份」：
--   写记录之前读一次，指纹相同就在回包 warnings 里明说「这次覆盖没带来变化」。
--
-- 幂等：ADD COLUMN IF NOT EXISTS。
--
-- 执行：
--   psql -h <host> -p 5432 -U <user> -d <db> -f nexus/platform/sql/asset_pack_digest.sql
-- ============================================================================

ALTER TABLE platform.shared_asset_installs
    ADD COLUMN IF NOT EXISTS digest varchar(64) NOT NULL DEFAULT '';

COMMENT ON COLUMN platform.shared_asset_installs.digest
    IS '安装时那份资产包的内容指纹（见 assetpack.StampManifest）；老记录为空串';

-- ============================================================================
-- 执行后核对
-- ============================================================================
-- a) 列已就位
--   \d platform.shared_asset_installs
-- b) 现有资产里带指纹的比例（本次上线前上架的都是空指纹，属正常）
--   SELECT kind, count(*) AS total,
--          count(*) FILTER (WHERE manifest ? 'digest') AS with_digest
--     FROM platform.shared_assets GROUP BY kind ORDER BY kind;
-- c) 安装记录里的指纹
--   SELECT asset_id, schema_name, installed_name, build, digest
--     FROM platform.shared_asset_installs ORDER BY installed_at DESC LIMIT 20;
--
-- 回滚
--   ALTER TABLE platform.shared_asset_installs DROP COLUMN IF EXISTS digest;
--   （manifest 里的 spec / digest 是 JSON 键，回滚程序版本后会被忽略，不需要清理）
