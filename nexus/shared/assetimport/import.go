// Package assetimport 把「资产包」落成一份本机快照 + 一行平台资产记录。
//
// 三个入口共用这一份实现，保证「不信上游」的规则只有一处：
//
//	平台管理台  上传 zip（POST /platform/asset-imports）
//	平台管理台  从对端拉包（POST /platform/peers/:id/import）
//	租户资产市场 点安装时的按需拉取（POST /admin/share/pull-asset）
//
// 规则：解压后重新校验路径/体积/清单/sha256 → 重跑受保护名与脱敏 → 落 platform-assets/
// → 写 shared_assets（默认 pending，auto 策略才 listed）。任何一步失败都不留孤儿目录。
package assetimport

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"strings"
	"sync"
	"time"

	"flame/nexus/shared/assetpack"
	"flame/nexus/shared/paths"
	"flame/nexus/shared/repository/sharedassets"
	"flame/nexus/shared/storage"

	"github.com/google/uuid"
)

// Options 导入策略。
type Options struct {
	// Origin 来源站点名；空则取包里的 origin.server，再缺省 "remote"。
	Origin string
	// Force 覆盖同来源同名的既有资产（本地快照 +1，回到待审/listed 按 AutoApprove）。
	Force bool
	// AutoApprove 直接 listed（只给「安装策略 = auto」的可信对端用）。
	AutoApprove bool
	// ImportedBy 触发者（平台管理员或租户用户）的 uuid，仅审计用。
	ImportedBy string
}

// Result 导入结果。
type Result struct {
	AssetID      string
	Kind         string
	Name         string
	Build        int
	Status       string
	Origin       string
	SourceSchema string
	Overwritten  bool
	Warnings     []string
}

// ConflictError 同来源同名的资产已存在且未允许覆盖。
//
// 带上既有行的信息，调用方可以把它变成「当前 v3，确认覆盖？」这种可操作提示，
// 而不是一句干巴巴的冲突。怎么收口由调用方决定：平台导入弹一次确认（确认后带 force=1
// 重试），租户远端拉取回结构化 conflict 让用户点「覆盖」。所以**文案里不要写具体操作**
// （「勾选某复选框」之类），那属于某个界面的说法，换个入口就不成立了。
type ConflictError struct {
	Origin         string
	Name           string
	ExistingID     string
	ExistingBuild  int
	ExistingStatus string
}

func (e *ConflictError) Error() string {
	return fmt.Sprintf("origin %s already has an asset with the same kind and name: %s (currently v%d)",
		e.Origin, e.Name, e.ExistingBuild)
}

var (
	originRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	schemaRe = regexp.MustCompile(`^[a-z][a-z0-9_]{1,31}$`)

	// buildMu 串行化「快照取号 → 写文件」这一段（与 admin/share 的上架各持一把）。
	// 两处都写同一份 storage，但落点互不影响：上架只动本机原创行，导入只动导入行。
	buildMu sync.Mutex
)

// Import 校验并落库。ra/size 是 zip 包；st 是平台存储（平台侧与租户侧都指向同一个 storage 根）。
func Import(ra io.ReaderAt, size int64, st storage.Storage, opt Options) (*Result, error) {
	if st == nil {
		return nil, errors.New("storage not ready")
	}
	pkg, files, err := assetpack.ReadPackage(ra, size, assetpack.DefaultLimits())
	if err != nil {
		return nil, fmt.Errorf("asset package validation failed: %w", err)
	}

	kind, err := ValidatePackage(pkg, files)
	if err != nil {
		return nil, err
	}

	// 重新脱敏：不信任上游做过的那一遍（对端可能快照更老 / 被改过 / 根本没做）
	var payload map[string]any
	if err := json.Unmarshal(pkg.Payload, &payload); err != nil || payload == nil {
		return nil, errors.New("the payload in the asset package is not a JSON object")
	}
	clean, secretWarnings := assetpack.StripSecrets(kind, payload)

	origin := strings.TrimSpace(opt.Origin)
	if origin == "" {
		origin = strings.TrimSpace(pkg.Origin.Server)
	}
	if origin == "" {
		origin = "remote"
	}
	if !originRe.MatchString(origin) {
		return nil, errors.New("the origin site name allows only letters, digits, dot, underscore and dash, at most 64 characters")
	}

	sourceSchema := strings.TrimSpace(pkg.Origin.SourceSchema)
	if !schemaRe.MatchString(sourceSchema) {
		// 远端 schema 只用于展示，本机不一定有同名租户；非法值不写进 CHECK 列
		sourceSchema = "remote"
	}

	existing, err := sharedassets.FindByOriginName(origin, string(kind), sourceSchema, pkg.Manifest.Name)
	if err != nil {
		return nil, fmt.Errorf("failed to query the existing import: %w", err)
	}
	if existing != nil && !opt.Force {
		return nil, &ConflictError{
			Origin: origin, Name: existing.Name,
			ExistingID: existing.ID, ExistingBuild: existing.Build, ExistingStatus: existing.Status,
		}
	}

	status := sharedassets.StatusPending
	if opt.AutoApprove {
		status = sharedassets.StatusListed
	}

	assetID := uuid.NewString()
	build := pkg.Origin.Build
	if build < 1 {
		build = 1
	}
	if existing != nil {
		assetID = existing.ID
		// 本地快照单调递增：远端包的 build 可能比本地旧（重导一篇旧包），不能让它倒退
		if build <= existing.Build {
			build = existing.Build + 1
		}
	}

	ctx := context.Background()
	// 取号与写文件必须串行（理由见 assetpack.FreeBuild）：并发导入各自算出同一个号，
	// 两份内容会交错写进同一棵目录，而行只记住其中一份的清单。
	// 覆盖导入时若目标号已有内容（同一快照重复导入 / 上次写了一半崩掉），跳过它 ——
	// 复用别人的目录会让清单与实际内容对不上。
	buildMu.Lock()
	if existing != nil {
		b, berr := assetpack.FreeBuild(ctx, st, assetID, build)
		if berr != nil {
			buildMu.Unlock()
			return nil, berr
		}
		build = b
	}
	dstDir := paths.PlatformAssetKey(assetID, build)
	var werr error
	for rel, data := range files {
		if werr = st.WriteFile(ctx, path.Join(dstDir, rel), data); werr != nil {
			break
		}
	}
	buildMu.Unlock()
	if werr != nil {
		_ = st.DeletePrefix(ctx, dstDir)
		return nil, fmt.Errorf("failed to write the asset files: %w", werr)
	}

	payloadJSON, err := json.Marshal(clean)
	if err != nil {
		_ = st.DeletePrefix(ctx, dstDir)
		return nil, fmt.Errorf("failed to serialise the payload: %w", err)
	}
	// 对端可能是旧版本（包里没有 spec / 指纹）：按本机口径补齐，保证本机每条资产行都带指纹，
	// 否则「重复安装的是不是同一份内容」在对端之间永远判不了。
	if pkg.Manifest.Digest == "" {
		if derr := assetpack.StampManifest(&pkg.Manifest, payloadJSON); derr != nil {
			_ = st.DeletePrefix(ctx, dstDir)
			return nil, fmt.Errorf("failed to compute the content digest: %w", derr)
		}
	}
	manifestJSON, err := json.Marshal(pkg.Manifest)
	if err != nil {
		_ = st.DeletePrefix(ctx, dstDir)
		return nil, fmt.Errorf("failed to serialise the manifest: %w", err)
	}

	a := &sharedassets.Asset{
		ID:            assetID,
		Kind:          string(kind),
		Name:          pkg.Manifest.Name,
		DisplayName:   asString(clean["display_name"]),
		Description:   asString(clean["description"]),
		SourceSchema:  sourceSchema,
		SourceID:      assetID, // 本机实体的溯源 id；远端 id 记在 OriginAssetID
		Build:         build,
		Status:        status,
		Visibility:    sharedassets.VisibilityAll,
		Manifest:      manifestJSON,
		Payload:       payloadJSON,
		ListedByName:  pkg.Origin.ListedBy,
		OriginServer:  origin,
		OriginAssetID: uuidOrNull(pkg.Origin.AssetID),
		ImportedAt:    sql.NullTime{Time: time.Now(), Valid: true},
		ImportedBy:    uuidOrNull(opt.ImportedBy),
	}

	if existing != nil {
		// 覆盖：复用行 id（安装记录仍指向同一资产），快照与内容更新后回到指定状态
		err = sharedassets.UpdateImported(a)
	} else {
		err = sharedassets.Insert(a)
	}
	if err != nil {
		_ = st.DeletePrefix(ctx, dstDir)
		// 并发导入：另一路已经把行推到更高的号，这份快照没人认领（内容本身是好的，重试会拿新号）
		if errors.Is(err, sharedassets.ErrStaleBuild) {
			return nil, errors.New("another import updated this asset concurrently; retry")
		}
		return nil, fmt.Errorf("failed to write the asset record: %w", err)
	}
	// 覆盖导入同样把快照推到新目录，旧快照就没有读者了（安装是复制、导出只打当前快照）。
	// 与上架共用同一套保留窗口；删不掉不影响导入，启动清扫会再收一次。
	_ = assetpack.PruneOldBuilds(ctx, st, assetID, build)

	warnings := make([]string, 0, 4)
	warnings = append(warnings, secretWarnings...)
	if existing != nil {
		warnings = append(warnings, fmt.Sprintf("overwrote the asset from origin %s; the local snapshot moved to v%d", origin, build))
	}
	if pkg.Origin.Build > 0 && pkg.Origin.Build != build {
		warnings = append(warnings, fmt.Sprintf("the package snapshot is v%d, the local record is v%d (local snapshots only move forward)",
			pkg.Origin.Build, build))
	}
	if kind == assetpack.KindAgent {
		warnings = append(warnings, "super powers / unrestricted tools / skill discovery do not travel with the package and stay off after install; "+
			"who may call this agent has to be configured again in the target tenant")
	}
	if kind == assetpack.KindWorkflow {
		warnings = append(warnings, "a workflow ships only a FLOW.json (the definition lives in the database): after install it is a draft, "+
			"the model / tool / sub-flow references in the graph have to be configured again in the target tenant, and credential variables were cleared when the asset was listed")
	}
	if len(pkg.Manifest.Deps.Skills) > 0 {
		warnings = append(warnings, fmt.Sprintf(
			"the asset declares %d skill dependencies; install matches them by name in the target tenant (a missing one is only a warning)", len(pkg.Manifest.Deps.Skills)))
	}
	if opt.AutoApprove {
		warnings = append(warnings, "this origin has auto-approve on, so the import is listed directly (imported_by is still recorded for traceability)")
	}

	return &Result{
		AssetID: assetID, Kind: string(kind), Name: pkg.Manifest.Name,
		Build: build, Status: status, Origin: origin, SourceSchema: sourceSchema,
		Overwritten: existing != nil, Warnings: warnings,
	}, nil
}

// ValidatePackage 包的语义校验：类型、名字、受保护名、入口文件存在。
//
// 导出给「租户侧直接导入」（admin/share 的上传包安装）复用：那条路不落平台资产行，
// 但「包本身是否可信」的判据必须与平台侧逐字一致，否则同一份包在两个入口有两种结论。
func ValidatePackage(pkg *assetpack.Package, files map[string][]byte) (assetpack.Kind, error) {
	kind := assetpack.Kind(pkg.Manifest.Kind)
	if !kind.Valid() {
		return "", fmt.Errorf("unknown asset kind: %s", pkg.Manifest.Kind)
	}
	// 工作流不落目录，名字只用库列，但它的措辞不同（名字是对外工具名，见
	// assetpack.ValidateWorkflowName），字符集与其它三类一致
	if kind == assetpack.KindWorkflow {
		if err := assetpack.ValidateWorkflowName(pkg.Manifest.Name); err != nil {
			return "", err
		}
	} else if err := assetpack.ValidateName(pkg.Manifest.Name); err != nil {
		return "", err
	}
	// 不再按「受保护名」拒绝：保护名单只约束删除 / 改名，不约束搬运。名字冲突由安装时的
	// 重名检查兜住（目标租户里 main / skill-authoring 本就存在，会走 suggested_name），
	// 在这里拦一次只会让「自己导出的包换台机器装不回来」。

	// 入口文件：skill / agent 是固定的，custom_tool 由 TOOL.json 的 entry 决定（包里必须真有它）
	entry := kind.EntryFile()
	if entry == "" {
		entry = strings.TrimSpace(pkg.Manifest.Entry)
		if entry == "" {
			return "", errors.New("the custom tool package declares no entry file")
		}
	}
	if _, ok := files[entry]; !ok {
		return "", fmt.Errorf("the package is missing the entry file: %s", entry)
	}
	return kind, nil
}

func uuidOrNull(s string) sql.NullString {
	s = strings.TrimSpace(s)
	if s == "" {
		return sql.NullString{}
	}
	if _, err := uuid.Parse(s); err != nil {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

// asString 取 payload 里的字符串字段（值类型不对就按空处理，不报错）。
func asString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}
