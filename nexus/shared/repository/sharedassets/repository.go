// Package sharedassets 跨租户资源分发表的读写（platform.shared_assets /
// shared_asset_tenants / shared_asset_installs）。
//
// 放在 shared/repository 而不是两侧各写一份 SQL：租户侧（admin/share）要上架与安装，
// 平台侧（platform/handler）要审核与下架，**同一行**会被两边轮番改，列顺序一旦各写
// 一份，迟早出现「这边扫得动、那边扫错位」。平台包允许依赖 shared/*，租户包本来就在
// shared 之下，所以这里是对称的唯一落点。
//
// 平台 schema 名取自配置（config.Cfg.Database.PlatformSchema），不接受调用方传参 ——
// 表名是二次注入面，不能让请求体决定。
package sharedassets

import (
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"time"

	"flame/nexus/shared/config"
	"flame/nexus/shared/db"

	"github.com/google/uuid"

	"github.com/lib/pq"
)

// 资产状态（与 platform/sql/shared_assets.sql 的 CHECK 同口径）。
const (
	StatusDraft    = "draft"
	StatusPending  = "pending"
	StatusListed   = "listed"
	StatusRejected = "rejected"
	StatusDelisted = "delisted"
)

// 可见范围。
const (
	VisibilityAll       = "all"
	VisibilityAllowlist = "allowlist"
)

// kind 取值（与 assetpack.Kind 同口径；此处不复用那层是为了让本包可被任一侧单独使用）。
const (
	KindSkill    = "skill"
	KindTool     = "custom_tool"
	KindAgent    = "agent"
	KindWorkflow = "workflow"
)

// columns 列顺序与 Scan 一一对应（改表时两处同改）。
const columns = `id, kind, name, display_name, description, source_schema, source_id,
	build, status, visibility, manifest, payload, listed_by, listed_by_name,
	review_note, created_at, updated_at,
	origin_server, origin_asset_id, imported_at, imported_by`

// Asset 一行上架快照（JSON 列保留原始字节，用到时再解）。
type Asset struct {
	ID           string
	Kind         string
	Name         string
	DisplayName  string
	Description  string
	SourceSchema string
	SourceID     string
	Build        int
	Status       string
	Visibility   string
	Manifest     []byte
	Payload      []byte
	ListedBy     sql.NullString
	ListedByName string
	ReviewNote   string // 平台审核备注（驳回原因就写在这里）
	CreatedAt    time.Time
	UpdatedAt    time.Time

	// 跨服务器导入的来源维度（本机上架的行全部为缺省值：''/NULL）
	OriginServer  string         // 来源站点名（导入时由操作者确认，缺省取包内的 server 名）
	OriginAssetID sql.NullString // 对方那台机器上的 asset id（仅溯源，不构成引用）
	ImportedAt    sql.NullTime
	ImportedBy    sql.NullString
}

// Imported 是否是从别的服务器导入的（而不是本机租户上架）。
func (a *Asset) Imported() bool { return a != nil && a.OriginServer != "" }

// Scan 从一行（repo 内部与调用方自定义查询共用）扫描出 Asset。
func Scan(sc interface{ Scan(...any) error }, a *Asset) error {
	return sc.Scan(
		&a.ID, &a.Kind, &a.Name, &a.DisplayName, &a.Description,
		&a.SourceSchema, &a.SourceID, &a.Build, &a.Status, &a.Visibility,
		&a.Manifest, &a.Payload, &a.ListedBy, &a.ListedByName,
		&a.ReviewNote, &a.CreatedAt, &a.UpdatedAt,
		&a.OriginServer, &a.OriginAssetID, &a.ImportedAt, &a.ImportedBy,
	)
}

// Columns 供调用方拼自定义查询时保持列顺序一致。
func Columns() string { return columns }

func table(name string) string {
	return pq.QuoteIdentifier(config.Cfg.Database.PlatformSchema) + "." + name
}

// Get 按 id 取；不存在返回 (nil, nil)，与仓储存量包的语义一致。
func Get(id string) (*Asset, error) {
	var a Asset
	if err := Scan(db.DB.QueryRow(`SELECT `+columns+` FROM `+table("shared_assets")+` WHERE id = $1`, id), &a); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &a, nil
}

// Filter 列表过滤条件（零值即不过滤）。
type Filter struct {
	Kind         string
	Status       string
	SourceSchema string // 我上架的
	OriginServer string // 从哪个对端站点导入的（空串不参与过滤）
	// OriginLocal 只看本机上架的行（origin_server = ''）。与 OriginServer 互斥，
	// 同时设置时以 OriginLocal 为准（本机上架的行 origin_server 恒为空串）。
	OriginLocal bool
	Query       string // name / display_name / 描述 模糊匹配
	// VisibleTo 非空时只返回该租户可安装的：listed + (all 或 allowlist 含该租户)
	VisibleTo string
	Limit     int
	Offset    int
}

// where 构造 WHERE 子句。skipOrigin 用于「来源汇总」：即便当前按来源筛选，
// 下拉里也要能看到其它来源，否则选了 test 之后其余选项就消失了。
func where(f Filter, skipOrigin bool) ([]string, []any) {
	where := make([]string, 0, 6)
	args := make([]any, 0, 6)

	add := func(cond string, v any) {
		args = append(args, v)
		where = append(where, strings.ReplaceAll(cond, "?", "$"+itoa(len(args))))
	}

	if f.Kind != "" {
		add("kind = ?", f.Kind)
	}
	if f.Status != "" {
		add("status = ?", f.Status)
	}
	if f.SourceSchema != "" {
		add("source_schema = ?", f.SourceSchema)
	}
	if !skipOrigin {
		if f.OriginLocal {
			where = append(where, "origin_server = ''")
		} else if f.OriginServer != "" {
			add("origin_server = ?", f.OriginServer)
		}
	}
	if f.Query != "" {
		args = append(args, "%"+f.Query+"%")
		p := "$" + itoa(len(args))
		where = append(where, "(name ILIKE "+p+" OR display_name ILIKE "+p+" OR description ILIKE "+p+")")
	}
	if f.VisibleTo != "" {
		args = append(args, f.VisibleTo)
		p := "$" + itoa(len(args))
		where = append(where, "status = 'listed'")
		where = append(where, `(visibility <> 'allowlist' OR EXISTS (
			SELECT 1 FROM `+table("shared_asset_tenants")+` w WHERE w.asset_id = shared_assets.id AND w.schema_name = `+p+`))`)
	}
	return where, args
}

// List 按条件列资产。
func List(f Filter) ([]Asset, error) {
	where, args := where(f, false)

	q := `SELECT ` + columns + ` FROM ` + table("shared_assets")
	if len(where) > 0 {
		q += ` WHERE ` + strings.Join(where, " AND ")
	}
	q += ` ORDER BY updated_at DESC`
	if f.Limit > 0 {
		q += ` LIMIT ` + itoa(f.Limit) + ` OFFSET ` + itoa(f.Offset)
	}

	rows, err := db.DB.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]Asset, 0, 16)
	for rows.Next() {
		var a Asset
		if err := Scan(rows, &a); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// Count 与 List 同条件计数（忽略 Limit/Offset），用于「加载更多」时告诉前端还剩多少。
func Count(f Filter) (int, error) {
	where, args := where(f, false)
	q := `SELECT count(*) FROM ` + table("shared_assets")
	if len(where) > 0 {
		q += ` WHERE ` + strings.Join(where, " AND ")
	}
	var n int
	if err := db.DB.QueryRow(q, args...).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// CountByOrigin 按来源站点汇总条数（key 为空串 = 本机上架），供界面渲染「来源」下拉。
// 它忽略 Filter 里的来源条件，其余条件（kind / status / 可见性 / 搜索）照旧生效，
// 这样「筛选结果变了，来源选项不会跟着消失」。
func CountByOrigin(f Filter) (map[string]int, error) {
	where, args := where(f, true)
	q := `SELECT origin_server, count(*) FROM ` + table("shared_assets")
	if len(where) > 0 {
		q += ` WHERE ` + strings.Join(where, " AND ")
	}
	q += ` GROUP BY origin_server`

	rows, err := db.DB.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string]int, 4)
	for rows.Next() {
		var origin string
		var n int
		if err := rows.Scan(&origin, &n); err != nil {
			return nil, err
		}
		out[origin] = n
	}
	return out, rows.Err()
}

// Insert 新建一行（上架 / 导入）。
func Insert(a *Asset) error {
	_, err := db.DB.Exec(`INSERT INTO `+table("shared_assets")+`
		(id, kind, name, display_name, description, source_schema, source_id, build,
		 status, visibility, manifest, payload, listed_by, listed_by_name,
		 origin_server, origin_asset_id, imported_at, imported_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7::uuid,$8,$9,$10,$11::jsonb,$12::jsonb,$13::uuid,$14,
		        $15,$16::uuid,$17,$18::uuid)`,
		a.ID, a.Kind, a.Name, a.DisplayName, a.Description, a.SourceSchema, a.SourceID,
		a.Build, a.Status, a.Visibility, string(a.Manifest), string(a.Payload),
		a.ListedBy, a.ListedByName,
		a.OriginServer, a.OriginAssetID, a.ImportedAt, a.ImportedBy)
	return err
}

// FindByOriginName 按「来源站点 + 类型 + 源 schema + 名字」查导入行（导入去重用，名字不区分大小写）。
//
// 与唯一索引 shared_assets_line_idx 同口径：本机上架行 origin_server = ”，永远查不到。
func FindByOriginName(originServer, kind, sourceSchema, name string) (*Asset, error) {
	var a Asset
	err := Scan(db.DB.QueryRow(`SELECT `+columns+` FROM `+table("shared_assets")+
		` WHERE origin_server = $1 AND kind = $2 AND source_schema = $3 AND lower(name) = lower($4)`,
		originServer, kind, sourceSchema, name), &a)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// FindByOriginAsset 按「来源站点 + 对方 asset id」找本机对应行（按需拉取的去重快路径）：
// 同一份远端资产被多个租户安装时只下载一次，共用同一个本机资产行。
func FindByOriginAsset(originServer, originAssetID string) (*Asset, error) {
	originServer = strings.TrimSpace(originServer)
	originAssetID = strings.TrimSpace(originAssetID)
	if originServer == "" || originAssetID == "" {
		return nil, nil
	}
	if _, err := uuid.Parse(originAssetID); err != nil {
		return nil, nil // 对方给了个非 uuid 的 id，当作没导入过
	}
	var a Asset
	err := Scan(db.DB.QueryRow(`SELECT `+columns+` FROM `+table("shared_assets")+
		` WHERE origin_server = $1 AND origin_asset_id = $2::uuid ORDER BY build DESC LIMIT 1`,
		originServer, originAssetID), &a)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// ErrStaleBuild 本次算出来的 build 已经过时：并发上架 / 导入的另一路把行推到了更高的号。
//
// 为什么要把这件事变成错误而不是照写：build 是单调的（快照只增不减、旧号会被
// PruneOldBuilds 收掉）。慢一步的那次写入若照写，行会退回一个可能已经被删掉的旧号，
// 变成「行指着一个不存在的快照」—— 导出、安装、对端拉取全部落空，且看不出原因。
// 调用方拿到它应当把刚铺的快照收掉，并提示用户刷新后重试。
var ErrStaleBuild = errors.New("该资产已被另一次上架/导入覆盖（快照号已前进）")

// staleOrErr 把「0 行受影响」翻成 ErrStaleBuild（`WHERE build < $n` 就是靠它生效的）。
func staleOrErr(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrStaleBuild
	}
	return nil
}

// UpdateImported 覆盖导入：换内容与快照、回到待审，并刷新来源溯源（id 不变，安装记录仍指向同一资产）。
//
// WHERE 带 `build < $4`：两次并发导入各自算出同一个号时，先落库的那次说了算，
// 后到的那次拿 ErrStaleBuild（而不是把行改回旧号）。
func UpdateImported(a *Asset) error {
	res, err := db.DB.Exec(`UPDATE `+table("shared_assets")+`
		SET display_name = $2, description = $3, build = $4, status = $5,
		    manifest = $6::jsonb, payload = $7::jsonb,
		    origin_asset_id = $8::uuid, imported_at = $9, imported_by = $10::uuid,
		    reviewed_by = NULL, reviewed_at = NULL, review_note = '', updated_at = now()
		WHERE id = $1 AND build < $4`,
		a.ID, a.DisplayName, a.Description, a.Build, a.Status,
		string(a.Manifest), string(a.Payload),
		a.OriginAssetID, a.ImportedAt, a.ImportedBy)
	return staleOrErr(res, err)
}

// UpdateListing 重新上架（覆盖）：改内容 + 回到待审，并清空上轮的审核结论。
//
// WHERE 同样带 `build < $5`（理由见 UpdateImported）。
func UpdateListing(a *Asset) error {
	res, err := db.DB.Exec(`UPDATE `+table("shared_assets")+`
		SET display_name = $2, description = $3, source_id = $4::uuid, build = $5,
		    status = $6, manifest = $7::jsonb, payload = $8::jsonb,
		    listed_by = $9::uuid, listed_by_name = $10,
		    reviewed_by = NULL, reviewed_at = NULL, review_note = '', updated_at = now()
		WHERE id = $1 AND build < $5`,
		a.ID, a.DisplayName, a.Description, a.SourceID, a.Build,
		a.Status, string(a.Manifest), string(a.Payload),
		a.ListedBy, a.ListedByName)
	return staleOrErr(res, err)
}

// Review 平台审核：改状态 + 记录审核人与备注。
func Review(id, status, note, reviewerID string) error {
	_, err := db.DB.Exec(`UPDATE `+table("shared_assets")+`
		SET status = $2, review_note = $3, reviewed_by = NULLIF($4, '')::uuid,
		    reviewed_at = now(), updated_at = now()
		WHERE id = $1`, id, status, note, reviewerID)
	return err
}

// SetStatus 只改状态（租户侧撤回用：不碰审核人与审核时间）。
func SetStatus(id, status string) error {
	_, err := db.DB.Exec(`UPDATE `+table("shared_assets")+`
		SET status = $2, updated_at = now() WHERE id = $1`, id, status)
	return err
}

// Delete 删除一行（草稿撤回）。
func Delete(id string) error {
	_, err := db.DB.Exec(`DELETE FROM `+table("shared_assets")+` WHERE id = $1`, id)
	return err
}

// SetVisibility 设置可见范围；allowlist 时全量替换白名单（事务）。
func SetVisibility(id, visibility string, schemas []string) error {
	tx, err := db.DB.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(`UPDATE `+table("shared_assets")+`
		SET visibility = $2, updated_at = now() WHERE id = $1`, id, visibility); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM `+table("shared_asset_tenants")+` WHERE asset_id = $1`, id); err != nil {
		return err
	}
	if visibility == VisibilityAllowlist {
		for _, s := range schemas {
			s = strings.TrimSpace(s)
			if s == "" {
				continue
			}
			if _, err := tx.Exec(`INSERT INTO `+table("shared_asset_tenants")+` (asset_id, schema_name)
				VALUES ($1,$2) ON CONFLICT DO NOTHING`, id, s); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

// VisibleSchemas 某资产的白名单租户（allowlist 模式）。
func VisibleSchemas(id string) ([]string, error) {
	rows, err := db.DB.Query(`SELECT schema_name FROM `+table("shared_asset_tenants")+
		` WHERE asset_id = $1 ORDER BY schema_name`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]string, 0, 4)
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// VisibleTo 该资产是否对某租户可见（all 恒可见；allowlist 看白名单）。
func VisibleTo(a *Asset, schema string) (bool, error) {
	if a.Visibility != VisibilityAllowlist {
		return true, nil
	}
	var one int
	err := db.DB.QueryRow(`SELECT 1 FROM `+table("shared_asset_tenants")+
		` WHERE asset_id = $1 AND schema_name = $2`, a.ID, schema).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// TenantDisplayName 租户展示名（查不到退回 schema，别让空串漏到界面上）。
func TenantDisplayName(schema string) string {
	var name string
	if err := db.DB.QueryRow(`SELECT display_name FROM `+table("tenants")+
		` WHERE schema_name = $1`, schema).Scan(&name); err != nil || strings.TrimSpace(name) == "" {
		return schema
	}
	return name
}

// Install 一条安装记录（含资产侧信息，列表直接用）。
type Install struct {
	AssetID         string
	Kind            string
	AssetName       string
	SourceSchema    string
	Build           int
	LatestBuild     int
	InstalledName   string
	InstalledID     string
	InstalledBy     string
	InstalledByName string
	InstalledAt     time.Time
	// Digest 装进去的那份内容指纹（老记录为空串）；用于前端提示「与上次装的是同一份」
	Digest string
}

// RecordInstall 写一条安装记录。
//
// digest 是「装进去的那份内容」的指纹（见 assetpack.StampManifest）：下次再装同名实体时
// 靠它判断「是不是同一份内容」。上传包没有 asset_id 时传空串。
func RecordInstall(assetID, schema string, build int, installedName, installedID, by, byName, digest string) error {
	_, err := db.DB.Exec(`INSERT INTO `+table("shared_asset_installs")+`
		(asset_id, schema_name, build, installed_name, installed_id, installed_by, installed_by_name, digest)
		VALUES ($1,$2,$3,$4,$5,NULLIF($6,'')::uuid,$7,$8)`,
		assetID, schema, build, installedName, installedID, by, byName, digest)
	return err
}

// LatestInstallDigest 某实体最近一次安装记录里的内容指纹（没有记录 / 老记录返回空串）。
//
// 只用来「提示」而不是「阻断」：实体可能在安装后被本地改过，指纹只能说明
// 「这次装的包和你上次装的包一样」，不能说明实体当前内容与包一致。
func LatestInstallDigest(schema, installedID string) (string, error) {
	var d string
	err := db.DB.QueryRow(`SELECT COALESCE(digest, '') FROM `+table("shared_asset_installs")+`
		 WHERE schema_name = $1 AND installed_id = $2
		 ORDER BY installed_at DESC LIMIT 1`, schema, installedID).Scan(&d)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return d, nil
}

// InstallsBy 某租户的安装记录（带资产名、kind、最新快照，供「有新快照」提示）。
func InstallsBy(schema string) ([]Install, error) {
	rows, err := db.DB.Query(`
		SELECT i.asset_id, a.kind, a.name, a.source_schema, i.build, a.build,
		       i.installed_name, i.installed_id, COALESCE(i.installed_by_name, ''), i.installed_at,
		       COALESCE(i.digest, '')
		  FROM `+table("shared_asset_installs")+` i
		  JOIN `+table("shared_assets")+` a ON a.id = i.asset_id
		 WHERE i.schema_name = $1
		 ORDER BY i.installed_at DESC`, schema)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]Install, 0, 8)
	for rows.Next() {
		var i Install
		if err := rows.Scan(&i.AssetID, &i.Kind, &i.AssetName, &i.SourceSchema,
			&i.Build, &i.LatestBuild, &i.InstalledName, &i.InstalledID,
			&i.InstalledByName, &i.InstalledAt, &i.Digest); err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

// DeleteMineUnlisted 删掉某租户所有「还没通过审核」的本机上架行，返回被删的 id。
//
// 只删「本机原创（origin_server 为空）且未通过审核」的行：
//   - 已通过审核的资产有人在用，删除会让安装记录悬空，只能由平台下架或删除；
//   - 导入行不是「我的上架」，删它属于远端市场的事。
//
// 用一条 DELETE ... RETURNING 而不是「先查再逐条删」：列表和删除之间别人可能又发了一条，
// 一条语句保证「删掉的正好是这一批」。
func DeleteMineUnlisted(schema string) ([]string, error) {
	rows, err := db.DB.Query(`DELETE FROM `+table("shared_assets")+`
		WHERE source_schema = $1 AND origin_server = '' AND status <> 'listed'
		RETURNING id`, schema)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// DeleteInstall 删一条安装记录，返回是否真的有行被删。
//
// 只删流水，**不动**已安装的实体：记录是「我装过什么」的账本，卸载是另一回事。
// schema 由调用方从身份里取（不能用请求体），否则能删掉别的租户的记录。
func DeleteInstall(assetID, schema, installedID string) (bool, error) {
	res, err := db.DB.Exec(`DELETE FROM `+table("shared_asset_installs")+`
		WHERE asset_id = $1 AND schema_name = $2 AND installed_id = $3`,
		assetID, schema, installedID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// ClearInstalls 删掉某租户的全部安装流水，返回删了多少条。
//
// 与 DeleteInstall 同口径：只抹账本，不卸载实体。一条 DELETE 而不是循环调用，
// 是为了「清空」这件事要么全成、要么全不成 —— 删到一半报错，剩下的记录更难解释。
func ClearInstalls(schema string) (int64, error) {
	res, err := db.DB.Exec(`DELETE FROM `+table("shared_asset_installs")+` WHERE schema_name = $1`, schema)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// CountInstalls 某资产被多少个租户装过（审核页看「这东西真的有人在用吗」）。
func CountInstalls(assetID string) (int, error) {
	var n int
	err := db.DB.QueryRow(`SELECT count(DISTINCT schema_name) FROM `+table("shared_asset_installs")+
		` WHERE asset_id = $1`, assetID).Scan(&n)
	return n, err
}

// InstalledBuilds 本租户已安装的 assetID → 最高快照（列表标「已安装 / 有新快照」）。
func InstalledBuilds(schema string) map[string]int {
	out := map[string]int{}
	rows, err := db.DB.Query(`SELECT asset_id, max(build) FROM `+table("shared_asset_installs")+
		` WHERE schema_name = $1 GROUP BY asset_id`, schema)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var (
			id  string
			ver int
		)
		if err := rows.Scan(&id, &ver); err == nil {
			out[id] = ver
		}
	}
	return out
}

func itoa(i int) string { return strconv.Itoa(i) }
