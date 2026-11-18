package share

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"flame/nexus/shared/assetpack"
	"flame/nexus/shared/auth"
	"flame/nexus/shared/capability"
	"flame/nexus/shared/capabilitypolicy"
	"flame/nexus/shared/config"
	"flame/nexus/shared/customtoolstore"
	"flame/nexus/shared/db"
	"flame/nexus/shared/models"
	"flame/nexus/shared/paths"
	"flame/nexus/shared/quota"
	"flame/nexus/shared/repository/customtools"
	"flame/nexus/shared/repository/sharedassets"
	"flame/nexus/shared/secureval"
	"flame/nexus/shared/storage"
	"flame/tools/xhttp"
	"flame/tools/xsql"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type installRequest struct {
	AssetID string `json:"asset_id"`
	Name    string `json:"name"`   // 空 = 沿用资产名
	Status  *int   `json:"status"` // 空 = 1（四类都默认启用；自定义工具不再默认为停用）
	// Overwrite 目标租户已有同名实体时覆盖它（而不是让用户改名）。
	// 四类都支持（见各 installXxx）：覆盖只替换包里带来的内容，身份 / 授权 / 对外暴露配置 /
	// 版本历史一律保留。
	Overwrite bool `json:"overwrite"`
}

// installSource 一次安装的内容来源。
//
// 两个入口共用这份结构：从平台资产安装（snapDir = platform-assets/<id>/<ver>，带 asset 行）
// 与直接上传资产包导入（包内容已铺进本租户的临时目录，asset 为 nil）。冲突处理、配额、能力
// 预检、默认状态、warnings 全部一致，差别只有「文件从哪个目录读」与「要不要写安装流水」。
type installSource struct {
	kind     assetpack.Kind
	manifest assetpack.Manifest
	payload  map[string]any
	snapDir  string
	asset    *sharedassets.Asset // nil = 不是平台资产（直接导入的包），不写安装流水
	notices  []string            // 入口附带的提示（如导入包里被清空的凭据），并入回包 warnings
	// overwritten 本次是覆盖既有实体（由各 installXxx 在真的走了覆盖分支时置位），回包带给前端。
	overwritten bool
}

// Install 把平台资产复制成本租户的实体。
//
// 顺序刻意是「先文件、后 DB」：文件删除是幂等的，DB 回滚更贵；任一步失败都尽量把
// 已经铺出去的文件收掉，不留下没有行指向的孤儿目录。
func Install(c *gin.Context) {
	info, ok := mustAuth(c)
	if !ok {
		return
	}

	var req installRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrJSON(c, "Invalid request body", nil, "")
		return
	}
	asset, m, kind, name, rerr := resolveInstallTarget(info, req.AssetID, req.Name)
	if rerr != nil {
		xhttp.ErrJSON(c, rerr.Error(), nil, "")
		return
	}

	installFrom(c, info, &installSource{
		kind:     kind,
		manifest: m,
		payload:  payloadFromAsset(asset),
		snapDir:  paths.PlatformAssetKey(asset.ID, asset.Build),
		asset:    asset,
	}, name, req)
}

// resolveInstallTarget 校验「这份资产能不能装、装成什么名字」。
//
// Install 与 InspectAsset 共用这一份：预检与真装必须按同一套规矩判，
// 否则会出现「预检说过、点安装被拒」这种最让人恼火的错位。
// 返回的 error 就是直接给用户看的那句话。
func resolveInstallTarget(info *auth.AuthInfo, assetID, wantName string) (asset *sharedassets.Asset,
	m assetpack.Manifest, kind assetpack.Kind, name string, err error) {

	assetID = strings.TrimSpace(assetID)
	if assetID == "" {
		return nil, m, kind, name, errors.New("缺少 asset_id")
	}

	asset, err = sharedassets.Get(assetID)
	if err != nil {
		return nil, m, kind, name, errors.New(assetErrMsg("query asset", err))
	}
	if asset == nil {
		return nil, m, kind, name, errors.New("资产不存在")
	}
	if asset.Status != sharedassets.StatusListed {
		return nil, m, kind, name, errors.New("该资产尚未通过审核（当前状态：" + asset.Status + "）")
	}
	visible, verr := sharedassets.VisibleTo(asset, info.Schema)
	if verr != nil {
		return nil, m, kind, name, errors.New("check visibility: " + verr.Error())
	}
	if !visible {
		return nil, m, kind, name, errors.New("该资产未对你所在的租户开放")
	}

	if len(asset.Manifest) > 0 {
		if uerr := json.Unmarshal(asset.Manifest, &m); uerr != nil {
			return nil, m, kind, name, errors.New("资产清单损坏: " + uerr.Error())
		}
	}
	// 没有文件清单就没有内容可装：只写一行记录会得到一个「存在但打不开」的实体
	if len(m.Files) == 0 {
		return nil, m, kind, name, errors.New("资产没有文件清单，无法安装（请让提供方重新上架）")
	}

	kind = assetpack.Kind(asset.Kind)
	name = strings.TrimSpace(wantName)
	if name == "" {
		name = asset.Name
	}
	// 与上架 / 导出同一判据：工作流名的措辞不同（它是对外工具名），字符集一样
	if verr := validatePlanName(kind, name); verr != nil {
		return nil, m, kind, name, verr
	}
	return asset, m, kind, name, nil
}

// payloadFromAsset 把资产行里的 payload 解成 map（坏数据当空对象：展示字段缺失不影响安装）。
func payloadFromAsset(a *sharedassets.Asset) map[string]any {
	payload := map[string]any{}
	if a != nil && len(a.Payload) > 0 {
		_ = json.Unmarshal(a.Payload, &payload)
	}
	if payload == nil {
		payload = map[string]any{}
	}
	return payload
}

// installFrom 按类型分发安装（平台资产与上传包共用）。
func installFrom(c *gin.Context, info *auth.AuthInfo, src *installSource, name string, req installRequest) {
	// spec 闸放在分发之前：四类共用一道，不认识的清单语义直接拒绝。
	// 把不认识的字段静默丢掉，装出来的是「能跑但行为不对」的实体，比装不上更难排查。
	// 老包没有 spec（=0）按 1 处理，不受影响。
	if src.manifest.Spec > assetpack.ManifestSpec {
		xhttp.ErrJSON(c, fmt.Sprintf(
			"该资产包使用了更新版本的清单格式（spec %d，本程序支持到 %d）：请升级 Nexus 后再安装",
			src.manifest.Spec, assetpack.ManifestSpec), nil, "")
		return
	}

	switch src.kind {
	case assetpack.KindSkill:
		installSkill(c, info, src, name, req)
	case assetpack.KindTool:
		installTool(c, info, src, name, req)
	case assetpack.KindAgent:
		installAgent(c, info, src, name, req)
	case assetpack.KindWorkflow:
		installWorkflow(c, info, src, name, req)
	default:
		xhttp.ErrJSON(c, "未知的资产类型："+string(src.kind), nil, "")
	}
}

// ===== 技能 =====

// installSkill 安装技能（落点恒为租户级技能，agent_id 为 NULL）。
//
// 同名冲突有两种收口：默认给建议名让用户改名（不动既有实体）；req.Overwrite 时
// **覆盖既有技能**——文件整棵重铺、description / metadata / 命令配置按包替换、
// 工具依赖按包全量替换。刻意不随包走的三项：
//
//	restricted + agent_skill_bindings  可见性属于目标租户的授权配置（白名单无法跨租户重映射）
//	status                             生命周期状态（覆盖的是内容，不是这个技能的存在方式）
//	versions/skills/<name>/            发布历史属于被覆盖的这个实体自己
func installSkill(c *gin.Context, info *auth.AuthInfo, src *installSource, name string, req installRequest) {
	// 不再判「受保护名」：保护只约束删除 / 改名，不约束安装。占位靠的是重名冲突 ——
	// 目标租户里 skill-authoring 本来就存在，装同名会走下面的重名处理，用户确认后覆盖它。
	exist, ferr := skillRepo.FindByName(info.Schema, name, "")
	if ferr != nil {
		xhttp.ErrJSON(c, ferr.Error(), nil, "")
		return
	}
	var prev *models.Skill
	if exist != nil {
		if !req.Overwrite {
			// 冲突不静默：can_overwrite 让前端把它变成「确认覆盖」而不是只提示改名 ——
			// 覆盖会丢弃该技能未发布的草稿改动，必须由用户点过确认再带 overwrite=1 重试。
			xhttp.ErrJSON(c, "本租户已有同名技能："+name, gin.H{
				"suggested_name": suggestFreeSkillName(info.Schema, name),
				"can_overwrite":  true,
			}, "")
			return
		}
		prev = exist
	}
	// 私有技能不算冲突：FindByName(agentID="") 只认租户级（agent_id IS NULL）。

	// 配额预检：覆盖不新增实体，跳过 —— 否则顶到上限的租户连更新自己已有的技能都做不到。
	if prev == nil {
		if msg, qok := skillQuotaLeft(info.Schema); !qok {
			xhttp.ErrJSON(c, msg, nil, "")
			return
		}
	}

	st, serr := getStorage()
	if serr != nil {
		xhttp.ErrJSON(c, "storage not ready: "+serr.Error(), nil, "")
		return
	}

	ctx := context.Background()
	dstDir := paths.SkillsKey(info.Schema) + "/" + name
	if prev != nil {
		// 覆盖：整棵目录先删再铺。直接往上叠会把旧版独有、包里没有的文件留在原地
		// （删掉的引用文档会「复活」，还会被下次导出一起打进去）。
		if derr := st.DeletePrefix(ctx, dstDir); derr != nil {
			xhttp.ErrJSON(c, "清理既有技能目录失败: "+derr.Error(), nil, "")
			return
		}
	} else {
		// 新建：清同名残留的版本域 —— 目标租户里若还留着同名技能的旧版本目录（历史删除只删了
		// SKILL.md、或删除路径没清干净），装进来的这份草稿首次发布 v1 就会撞上它，
		// EnsureAbsent 报 version exists，错误信息完全指不到「是安装带进来的」。
		// 覆盖时不清：那份历史属于这个被覆盖的实体自己。
		if verr := st.DeletePrefix(ctx, paths.VersionEntityKey(info.Schema, paths.VersionSkills, name)); verr != nil {
			xhttp.ErrJSON(c, "清理同名技能的旧版本目录失败: "+verr.Error(), nil, "")
			return
		}
	}
	// 只落本体：_deps/ 是依赖的自定义工具，装到 custom_tools/ 而不是技能目录里
	missing, rerr := assetpack.RestoreFiles(ctx, st, src.snapDir, dstDir, assetpack.BodyFiles(src.manifest.Files))
	if rerr != nil {
		_ = st.DeletePrefix(ctx, dstDir)
		xhttp.ErrJSON(c, "写入技能文件失败: "+rerr.Error(), nil, "")
		return
	}

	// 只允许「正常 / 废弃」两种落点（skill 只有两态）。覆盖时缺省沿用目标既有状态：
	// 覆盖的是内容，不是生命周期 —— 把已废弃技能悄悄改回「正常」会让它在列表与授权里
	// 突然重新可用。
	status := models.StatusNormal
	if prev != nil {
		status = int(prev.Status)
	}
	if req.Status != nil && (*req.Status == 1 || *req.Status == 3) {
		status = *req.Status
	}

	warnings := missingWarnings(missing)
	var id string
	if prev != nil {
		// Update 只写 name / description / metadata / updated_by / user_invocable / command_*：
		// restricted 与 status 各有专用入口（白名单由 bind 维护，不能拿 SetVisibility 代劳 ——
		// 它会在事务里 DELETE 整张 agent_skill_bindings 再重建）。
		meta := xsql.JSONB{Data: map[string]any{}}
		if md, ok := src.payload["metadata"].(map[string]any); ok && len(md) > 0 {
			meta = xsql.JSONB{Data: md}
		}
		upd := &models.Skill{
			ID:              string(prev.ID),
			Name:            name,
			Description:     xsql.String(asString(src.payload["description"])),
			Metadata:        meta,
			UserInvocable:   xsql.Bool(asBool(src.payload["user_invocable"])),
			CommandDispatch: xsql.String(asString(src.payload["command_dispatch"])),
			CommandTool:     xsql.String(asString(src.payload["command_tool"])),
			UpdatedBy:       xsql.UUID(info.ID),
		}
		if uerr := skillRepo.Update(info.Schema, upd); uerr != nil {
			// 目录已被新内容替换：这里**不删**它。行是旧的、文件是新的，技能至少还能打开，
			// 补一次导入即可对齐；删掉反而会留下「有行、没文件」的坏实体，比不一致更糟。
			xhttp.ErrJSON(c, "覆盖技能记录失败（文件已写入，请重试导入）: "+uerr.Error(), nil, "")
			return
		}
		id = string(prev.ID)
		src.overwritten = true

		if int(prev.Status) != status {
			if sterr := skillRepo.UpdateStatus(info.Schema, id, status); sterr != nil {
				warnings = append(warnings, "状态未能改成目标值，已保留原状态："+sterr.Error())
			}
		}
		// 可见性（restricted + 白名单）成对保留目标既有配置。唯一的修补：目标本身就是
		// 「restricted=true 但一条绑定都没有」——那它对谁都不可见（visibleCond 两条都不成立），
		// 覆盖时顺手自愈成全员可见，别把这个坏组合原样留着。
		if bool(prev.Restricted) {
			if bindings, berr := skillRepo.ListBindings(info.Schema, id); berr != nil {
				warnings = append(warnings, "校验技能可见性失败："+berr.Error())
			} else if len(bindings) == 0 {
				if rerr := skillRepo.SetRestricted(info.Schema, id, false, info.ID); rerr != nil {
					warnings = append(warnings, "技能可见性自愈失败（它现在对任何智能体都不可见）："+rerr.Error())
				} else {
					warnings = append(warnings, "该技能原本是「仅授权可见」但一条授权都没有（谁都发现不了），已改为全员可见")
				}
			}
		}
		if models.IsProtectedSkill(name) {
			warnings = append(warnings, "覆盖的是系统内置技能（受保护名）：内容按包替换，受保护身份由名字决定")
		}
		warnings = append(warnings, "覆盖不会改动该技能在目标租户的可见性授权（哪些智能体可用）；如需调整请到「技能授权」里改")
	} else {
		var ierr error
		id, ierr = insertInstalledSkill(info, name, status, src.payload)
		if ierr != nil {
			_ = st.DeletePrefix(ctx, dstDir)
			xhttp.ErrJSON(c, "写入技能记录失败: "+ierr.Error(), nil, "")
			return
		}
	}

	// 先装随包带过来的工具，再写依赖 —— 顺序反了授权行会指向还不存在的工具
	warnings = append(warnings, installBundledTools(info, src)...)
	if prev != nil {
		warnings = append(warnings, replaceInstalledSkillGrants(info, id, src.manifest.Deps.Tools)...)
	} else {
		warnings = append(warnings, writeSkillGrants(info, id, src.manifest.Deps.Tools)...)
	}
	respondInstalled(c, info, src, name, id, missing, warnings)
}

// insertInstalledSkill 落一条技能记录：身份重新生成，归属强制为空（租户级）。
//
// restricted 恒为 false：可见性白名单（agent_skill_bindings）无法跨租户重映射，落 true 只会
// 得到「谁都没被授权 → 谁都发现不了」的技能（visibleCond 两条都不成立）。可见性属于目标租户的
// 授权配置，与智能体的 endpoints 同口径 —— 不随包迁移，装完到「技能授权」里设。
func insertInstalledSkill(info *auth.AuthInfo, name string, status int, payload map[string]any) (string, error) {
	id := uuid.NewString()

	meta, err := json.Marshal(payload["metadata"])
	if err != nil || string(meta) == "null" || string(meta) == "" {
		meta = []byte("{}")
	}
	now := time.Now()

	_, err = db.DB.Exec(`INSERT INTO `+tenantTable(info.Schema, "skills")+`
		(id, name, description, created_by, updated_by, metadata, status, restricted,
		 created_at, updated_at, agent_id, user_invocable, command_dispatch, command_tool)
		VALUES ($1,$2,$3,$4::uuid,$5::uuid,$6::jsonb,$7,$8,$9,$10,NULL,$11,$12,$13)`,
		id, name, asString(payload["description"]),
		info.ID, info.ID, string(meta), status, false,
		now, now,
		asBool(payload["user_invocable"]),
		asString(payload["command_dispatch"]),
		asString(payload["command_tool"]))
	if err != nil {
		return "", err
	}
	return id, nil
}

// writeSkillGrants 按清单重建工具依赖，返回需要提示的项。
//
// 刻意不因为某个 ref 不存在就整单失败：技能正文往往仍然有用，缺的那几个工具
// 由使用者在「工具授权」里补。但要明说一句，别让人以为依赖已经生效。
func writeSkillGrants(info *auth.AuthInfo, skillID string, deps []assetpack.ToolDep) []string {
	if len(deps) == 0 {
		return nil
	}
	var warnings []string
	failed := 0
	for _, d := range deps {
		if d.Ref == "" || d.Type == "" {
			continue
		}
		if _, err := grantRepo.AddSkillGrant(info.Schema, skillID,
			models.SkillToolGrant{SkillID: skillID, ItemType: d.Type, Ref: d.Ref}); err != nil {
			failed++
			warnings = append(warnings, fmt.Sprintf("工具依赖 %s 写入失败：%v", d.Ref, err))
		}
	}
	if failed == 0 {
		warnings = append(warnings, fmt.Sprintf(
			"已按声明写入 %d 条工具依赖；本租户缺少对应工具时它们不会生效，可在「工具授权」里调整", len(deps)))
	}
	return warnings
}

// replaceInstalledSkillGrants 覆盖安装时按清单**全量替换**该技能的工具依赖。
//
// 与 writeSkillGrants 的差别只有最后一步（ReplaceSkillGrants 而不是逐条 AddSkillGrant）：
// 覆盖语义下「包里没有声明的依赖」必须清掉，否则旧依赖会跟着新正文继续生效。
func replaceInstalledSkillGrants(info *auth.AuthInfo, skillID string, deps []assetpack.ToolDep) []string {
	grants := make([]models.SkillToolGrant, 0, len(deps))
	for _, d := range deps {
		if d.Ref == "" || d.Type == "" {
			continue
		}
		grants = append(grants, models.SkillToolGrant{SkillID: skillID, ItemType: d.Type, Ref: d.Ref})
	}
	if err := grantRepo.ReplaceSkillGrants(info.Schema, skillID, grants); err != nil {
		return []string{"工具依赖写入失败：" + err.Error()}
	}
	if len(grants) == 0 {
		return []string{"工具依赖已按包内清单清空（包内没有声明任何工具）"}
	}
	return []string{fmt.Sprintf(
		"工具依赖已按包内清单全量替换（%d 条）；本租户缺少对应工具时它们不会生效，可在「工具授权」里调整", len(grants))}
}

// skillQuotaLeft 配额预检：超限就不装，别让用户在最后一步才看到「装不下」。
func skillQuotaLeft(schema string) (string, bool) {
	limits := quota.Resolve(schema)
	if limits.MaxSkills < 0 {
		return "", true
	}
	var n int
	if err := db.DB.QueryRow(`SELECT count(*) FROM ` + tenantTable(schema, "skills")).Scan(&n); err != nil {
		return "统计技能数量失败: " + err.Error(), false
	}
	if n >= limits.MaxSkills {
		return fmt.Sprintf("技能数量已达配额上限（%d）", limits.MaxSkills), false
	}
	return "", true
}

// suggestFreeSkillName 给一个本租户确实没人用的建议名（最多试 50 个）。
func suggestFreeSkillName(schema, base string) string {
	cand := assetpack.SuggestName(base, nil)
	for i := 0; i < 50; i++ {
		s, err := skillRepo.FindByName(schema, cand, "")
		if err != nil || s == nil {
			return cand
		}
		cand = assetpack.SuggestName(cand, []string{cand})
	}
	return base
}

// ===== 自定义工具 =====

// installTool 安装脚本工具。
//
// 与技能最大的差别：**契约在文件里**（TOOL.json）。所以装完要从目标目录把契约读回来，
// 用它生成表行（configs 留空、status 默认启用），而不是从 payload 里抄一堆字段 ——
// 那样文件与行又会各说一套。
//
// 同名冲突两收口：默认给建议名；req.Overwrite 时覆盖既有工具 —— 脚本整棵重铺、契约字段
// 按包替换，但**配置值与创建人保留**（配置就是凭据，包里的 configs 是空的）。
func installTool(c *gin.Context, info *auth.AuthInfo, src *installSource, name string, req installRequest) {
	store := customtoolstore.New(config.Cfg.Agent.RootDir, info.Schema)
	_, derr := store.Get("", name)
	dirExists := derr == nil
	if derr != nil && !errors.Is(derr, sql.ErrNoRows) {
		xhttp.ErrJSON(c, "check tool: "+derr.Error(), nil, "")
		return
	}
	// 表行也查一次：目录在、行不在（历史残留）同样算「已存在」，覆盖时据它保住配置与创建人
	prev, rerr := customtools.NewRepository().Get(info.Schema, name)
	if rerr != nil && !errors.Is(rerr, sql.ErrNoRows) {
		xhttp.ErrJSON(c, "check tool: "+rerr.Error(), nil, "")
		return
	}
	if (dirExists || prev != nil) && !req.Overwrite {
		xhttp.ErrJSON(c, "本租户已有同名工具："+name, gin.H{
			"suggested_name": suggestFreeToolName(store, name),
			"can_overwrite":  true,
		}, "")
		return
	}

	st, serr := getStorage()
	if serr != nil {
		xhttp.ErrJSON(c, "storage not ready: "+serr.Error(), nil, "")
		return
	}

	// 装上即可用（不再默认停用）：安装是一次明确的用户动作，装完就要能调用；
	// 需要复核脚本的，装完自己停用即可。
	// 覆盖时缺省沿用目标既有状态 —— 覆盖的是脚本内容，不是「它是否可用」这个决定。
	status := models.StatusNormal
	if prev != nil {
		status = prev.Status
	}
	if req.Status != nil && (*req.Status == 1 || *req.Status == 2) {
		status = *req.Status
	}

	if dirExists && prev == nil {
		// 目录在、行不在（历史残留）：先清掉再铺，免得旧文件混进这次安装。
		// 行还没有，所以 placeTool 看不到 prev，这里替它做掉。
		if derr := st.DeletePrefix(context.Background(), paths.ToolsKey(info.Schema)+"/"+name); derr != nil {
			xhttp.ErrJSON(c, "清理残留的工具目录失败: "+derr.Error(), nil, "")
			return
		}
	}

	id, missing, warnings, perr := placeTool(context.Background(), st, info, src,
		src.manifest.Files, "", name, status, prev)
	if perr != nil {
		xhttp.ErrJSON(c, perr.Error(), nil, "")
		return
	}
	respondInstalled(c, info, src, name, id, missing, warnings)
}

// placeTool 把一个工具从快照铺到本租户目录并建行 / 覆盖行（prev 非 nil = 覆盖）。
//
// 市场单装与「随智能体 / 技能带过来的工具」共用这一份：契约在文件里（TOOL.json），
// 所以必须先落盘再从目标目录读回契约，行字段一律以契约为准 —— 从 payload 抄一份会让
// 文件与行各说一套。能力预检也在这里，装一个跑不起来的工具比装不上更让人困惑。
//
// prefix 用于包内附件（_deps/custom_tools/<name>/），普通安装传空。
func placeTool(ctx context.Context, st storage.Storage, info *auth.AuthInfo, src *installSource,
	files []assetpack.FileEntry, prefix, name string, status int, prev *models.CustomTool) (id string, missing, warnings []string, err error) {

	caps := capabilitypolicy.Snapshot(info.Schema)
	if !caps[capability.Exec] {
		return "", nil, nil, errors.New("本租户未开通「一次性执行」（sandbox.exec）能力，脚本工具装上也无法运行")
	}

	dstDir := paths.ToolsKey(info.Schema) + "/" + name
	if prev != nil {
		// 覆盖：整棵目录先删再铺。直接往上叠会把旧版独有的脚本 / 数据文件留在原地
		// （包里已删除的文件会「复活」，下次导出还会一起带走）。
		if derr := st.DeletePrefix(ctx, dstDir); derr != nil {
			return "", nil, nil, fmt.Errorf("清理既有工具目录失败: %w", derr)
		}
	} else {
		// 新建：版本域按 name 键，旧目录留着，装进来的工具首次发布 v1 会撞上 EnsureAbsent
		// （错误信息指不到安装）。这条路径同时服务「随包附带的自定义工具」，两边一次收口。
		if verr := st.DeletePrefix(ctx, paths.VersionEntityKey(info.Schema, paths.VersionTools, name)); verr != nil {
			return "", nil, nil, fmt.Errorf("清理同名工具的旧版本目录失败: %w", verr)
		}
	}

	missing, rerr := assetpack.RestoreFilesUnder(ctx, st, src.snapDir, dstDir, prefix, files)
	if rerr != nil {
		_ = st.DeletePrefix(ctx, dstDir)
		return "", nil, nil, fmt.Errorf("写入工具文件失败: %w", rerr)
	}

	tf, cerr := adoptToolContract(ctx, st, dstDir, name)
	if cerr != nil {
		_ = st.DeletePrefix(ctx, dstDir)
		return "", nil, nil, fmt.Errorf("工具契约不可用: %w", cerr)
	}
	if tf.Egress && !caps[capability.Egress] {
		_ = st.DeletePrefix(ctx, dstDir)
		return "", nil, nil, errors.New("该工具声明了出网，但本租户未开通 net.egress")
	}

	t := models.CustomTool{
		Name:      name,
		Status:    status,
		Configs:   xsql.JSONB{Data: map[string]any{}}, // 值不随资产走，等使用者自己填
		Egress:    tf.Egress,
		CreatedBy: info.ID,
	}
	if prev != nil {
		// 覆盖：身份 / 创建人 / 配置值都要留住 —— 配置就是凭据，包里的 configs 是空的
		// （值不随资产走），跟着包清空等于让工具当场失效。
		t.ID = prev.ID
		t.CreatedBy = prev.CreatedBy
		t.Configs = prev.Configs
	}
	tf.Apply(&t)

	repo := customtools.NewRepository()
	if prev != nil {
		if uerr := repo.Update(info.Schema, &t); uerr != nil {
			// 目录已是新脚本、行还是旧的：不删目录（删了就是「有目录没行」），重试导入即可对齐
			return "", nil, nil, fmt.Errorf("覆盖工具记录失败（文件已写入，请重试导入）: %w", uerr)
		}
	} else if cerr := repo.Create(info.Schema, &t); cerr != nil {
		_ = st.DeletePrefix(ctx, dstDir)
		return "", nil, nil, fmt.Errorf("写入工具记录失败: %w", cerr)
	}

	warnings = missingWarnings(missing)
	if len(tf.ConfigFields) > 0 {
		if prev != nil {
			warnings = append(warnings, fmt.Sprintf(
				"该工具声明了 %d 个配置项：已有的配置值按原样保留，契约里新增的项需要自己补齐", len(tf.ConfigFields)))
		} else {
			warnings = append(warnings, fmt.Sprintf(
				"该工具有 %d 个配置项需要填写，没填时调用会失败", len(tf.ConfigFields)))
		}
	}
	return t.ID, missing, warnings, nil
}

// installBundledTools 把包里附带的自定义工具装进本租户（启用态，与单装一致）。
//
// 三条「不做」比「做」更重要：
//   - 目标已有同名工具不覆盖 —— 本地快照可能改过脚本或填过配置，装个智能体顺手把别人的
//     工具盖掉是不可接受的（overwrite 只作用于单装入口，附带路径永远传 nil）；
//   - 缺 sandbox.exec / net.egress 就跳过并说明，不让整次安装失败；
//   - 单个工具装失败只记 warning：智能体本体已经装好了，为它回滚不划算。
func installBundledTools(info *auth.AuthInfo, src *installSource) []string {
	attachments := assetpack.Attachments(src.manifest.Files)
	if len(attachments) == 0 {
		return nil
	}

	st, serr := getStorage()
	if serr != nil {
		return []string{"storage not ready，附带的自定义工具未安装：" + serr.Error()}
	}
	ctx := context.Background()
	store := customtoolstore.New(config.Cfg.Agent.RootDir, info.Schema)

	names := make([]string, 0, len(attachments))
	for name := range attachments {
		names = append(names, name)
	}
	sort.Strings(names)

	var warnings []string
	installed := make([]string, 0, len(names))
	for _, name := range names {
		// 名字直接进目录路径：附件是从 zip 里读出来的，不能假设它一定合法
		if err := assetpack.ValidateName(name); err != nil {
			warnings = append(warnings, "附带的自定义工具名不合法，未安装："+name)
			continue
		}
		if _, gerr := store.Get("", name); gerr == nil {
			warnings = append(warnings, fmt.Sprintf(
				"本租户已有同名工具 %s，保留本地快照（包内附带的没有覆盖）", name))
			continue
		} else if !errors.Is(gerr, sql.ErrNoRows) {
			warnings = append(warnings, fmt.Sprintf("检查工具 %s 失败，未安装：%v", name, gerr))
			continue
		}

		_, missing, w, perr := placeTool(ctx, st, info, src, attachments[name],
			assetpack.ToolAttachmentPrefix(name), name, models.StatusNormal, nil)
		if perr != nil {
			warnings = append(warnings, fmt.Sprintf("附带的自定义工具 %s 安装失败：%v", name, perr))
			continue
		}
		warnings = append(warnings, missing...)
		warnings = append(warnings, w...)
		installed = append(installed, name)
	}
	if len(installed) > 0 {
		warnings = append(warnings, fmt.Sprintf(
			"已随包安装 %d 个自定义工具（%s），均已启用；如需暂停请在「自定义工具」里停用",
			len(installed), strings.Join(installed, "、")))
	}
	return warnings
}

// adoptToolContract 从目标目录读回 TOOL.json，按目标名校正后写回并校验。
//
// 改名安装时必须重写契约里的 name：Validate 要求「契约名 == 目录名」，
// 不写回去的话这个工具从此每次加载都会报「name 与目录名不一致」。
func adoptToolContract(ctx context.Context, st storage.Storage, dir, name string) (*models.ToolFile, error) {

	raw, err := st.ReadFile(ctx, path.Join(dir, models.ToolFileName))
	if err != nil {
		return nil, fmt.Errorf("缺少 %s", models.ToolFileName)
	}
	tf, err := models.ParseToolFile(raw)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(tf.Name) != name {
		tf.Name = name
		b, merr := models.MarshalToolFile(tf)
		if merr != nil {
			return nil, merr
		}
		if werr := st.WriteFile(ctx, path.Join(dir, models.ToolFileName), b); werr != nil {
			return nil, werr
		}
	}
	if err := tf.Validate(name); err != nil {
		return nil, err
	}
	return tf, nil
}

// suggestFreeToolName 给一个本租户没被占用的工具名（目录与表行任一存在都算占用）。
func suggestFreeToolName(store *customtoolstore.Store, base string) string {
	cand := assetpack.SuggestName(base, nil)
	for i := 0; i < 50; i++ {
		if _, err := store.Get("", cand); errors.Is(err, sql.ErrNoRows) {
			return cand
		}
		cand = assetpack.SuggestName(cand, []string{cand})
	}
	return base
}

// ===== 智能体 =====

// installAgent 安装智能体：本体文件 + 一行 agents + 私有技能 + 技能绑定 + 工具授权。
//
// 私有技能随目录一起还原，行由 restorePrivateSkills 按 payload.private_skills 重建
// （归属指向新 agent）。
//
// 三个开关（is_supervisor / tools_unrestricted / skill_discovery）**随包走**：它们决定
// 这个智能体在租户里怎么跑，不带走就会在目标租户装出一个行为不同的副本。代价是「导入」
// 与「在编辑页勾选」得到同样的权限面 —— 这两件事本来就是同一个权限面（同一个租户管理接口），
// 所以不额外加闸，只把结果写进 warnings 让人看见。
//
// 不随包走的只剩：
//
//	agent_relations  跨租户无法重映射，装完提示去重新配置
//	endpoints        对外暴露配置属于目标租户，覆盖时保留旧值
//
// 同名冲突有两种收口：默认给建议名让用户改名（不动既有实体）；req.Overwrite 时
// **覆盖既有智能体**——文件 / 配置 / 私有技能 / 技能绑定 / 工具授权 / 三个开关全部按包替换。
func installAgent(c *gin.Context, info *auth.AuthInfo, src *installSource, name string, req installRequest) {
	// 不判「受保护名」（main 等）：保护只约束删除 / 改名。目标租户里 main 本来就存在，
	// 装同名会命中下面的重名处理（改名 或 覆盖前确认），不需要再叠一道「禁止占用」。
	exist, eerr := agentRepo.ExistsName(info, name, "")
	if eerr != nil {
		xhttp.ErrJSON(c, "check name: "+eerr.Error(), nil, "")
		return
	}
	var prev *models.Agent
	if exist {
		if !req.Overwrite {
			// can_overwrite 让前端把它变成「确认覆盖」而不是只提示改名：
			// 覆盖一个既有智能体是破坏性动作，必须由用户点过确认再带 overwrite=1 重试。
			xhttp.ErrJSON(c, "本租户已有同名智能体："+name, gin.H{
				"suggested_name": suggestFreeAgentName(info, name),
				"can_overwrite":  true,
			}, "")
			return
		}
		p, perr := agentRepo.FindByName(info, name)
		if perr != nil {
			xhttp.ErrJSON(c, "read agent: "+perr.Error(), nil, "")
			return
		}
		if p == nil {
			exist = false // 判存在与取行之间被并发删掉了：退回新建
		} else {
			prev = p
		}
	}
	// 智能体与工作流共用一个名字空间（创建接口就是这么校验的）：这里放行会造出
	// 一个「按名字查找永远命中另一个」的实体。覆盖时同名工作流不可能存在（同一个名字空间），
	// 所以只在新建路径上查。
	if !exist {
		if flowExist, ferr := flowRepo.ExistsFlowName(info, name); ferr != nil {
			xhttp.ErrJSON(c, "check name: "+ferr.Error(), nil, "")
			return
		} else if flowExist {
			xhttp.ErrJSON(c, "本租户已有同名工作流，智能体名不能与工作流重名："+name, gin.H{
				"suggested_name": suggestFreeAgentName(info, name),
			}, "")
			return
		}
	}

	// 配额预检：与「新建智能体」同一条闸，别让超限的租户先把文件铺下去。
	// 覆盖不新增实体，跳过 —— 否则「已经顶到上限的租户连更新自己已有的智能体都做不到」。
	if !exist {
		if cnt, qerr := quota.CountAgents(info.Schema); qerr != nil {
			xhttp.ErrJSON(c, "统计智能体数量失败: "+qerr.Error(), nil, "")
			return
		} else if qerr := models.CheckQuota("agent", cnt, quota.Resolve(info.Schema).MaxAgents); qerr != nil {
			xhttp.ErrJSON(c, qerr.Error(), nil, "")
			return
		}
	}

	st, serr := getStorage()
	if serr != nil {
		xhttp.ErrJSON(c, "storage not ready: "+serr.Error(), nil, "")
		return
	}

	ctx := context.Background()
	dstDir := paths.AgentsKey(info.Schema, name)
	if exist {
		// 覆盖：整棵目录先删再铺。直接往上叠会把旧版独有、包里没有的文件留在原地
		// （删掉的脚本 / 旧私有技能目录会「复活」，还会被下次导出一起打进去）。
		if derr := st.DeletePrefix(ctx, dstDir); derr != nil {
			xhttp.ErrJSON(c, "清理既有智能体目录失败: "+derr.Error(), nil, "")
			return
		}
	} else {
		// 新建：清掉同名残留的版本域 —— 旧目录留着，首次发布 v1 会撞 EnsureAbsent
		// （错误信息指不到「是导入带进来的」）。版本目录在 versions/agents/<name>，
		// 与草稿目录不重叠；覆盖时不删（那份历史属于这个被覆盖的实体自己）。
		if verr := st.DeletePrefix(ctx, paths.VersionEntityKey(info.Schema, paths.VersionAgents, name)); verr != nil {
			xhttp.ErrJSON(c, "清理同名智能体的旧版本目录失败: "+verr.Error(), nil, "")
			return
		}
	}
	// 只落本体：_deps/ 是依赖的自定义工具，装到 custom_tools/ 而不是 agent 目录里
	missing, rerr := assetpack.RestoreFiles(ctx, st, src.snapDir, dstDir, assetpack.BodyFiles(src.manifest.Files))
	if rerr != nil {
		_ = st.DeletePrefix(ctx, dstDir)
		xhttp.ErrJSON(c, "写入智能体文件失败: "+rerr.Error(), nil, "")
		return
	}

	payload := src.payload

	// agent 只有两态：正常 / 废弃
	status := 1
	if req.Status != nil && (*req.Status == 1 || *req.Status == 3) {
		status = *req.Status
	}

	cfgMap := map[string]any{}
	if v, ok := payload["config"].(map[string]any); ok && len(v) > 0 {
		cfgMap = v
	}
	cfg := xsql.JSONB{Data: cfgMap}

	// 三个开关按包设置。老包（没有这些字段）覆盖时沿用目标既有值、新建时取 false ——
	// 不能直接断言成 false，否则一次覆盖就把管理员开过的开关静默关掉。
	// 这三个开关与「在智能体编辑页手动勾选」是同一个权限面：能调这个接口的人本来也能改它们。
	wantSupervisor := flagFrom(payload, "is_supervisor", prev != nil && prev.IsSupervisor)
	wantToolsUnrestricted := flagFrom(payload, "tools_unrestricted", prev != nil && prev.ToolsUnrestricted)
	wantSkillDiscovery := flagFrom(payload, "skill_discovery", prev != nil && prev.SkillDiscovery)

	ag := &models.Agent{
		Name:              name,
		Description:       xsql.String(asString(payload["description"])),
		Config:            cfg,
		Status:            xsql.Int(status),
		IsSupervisor:      wantSupervisor,
		ToolsUnrestricted: wantToolsUnrestricted,
		SkillDiscovery:    wantSkillDiscovery,
	}

	var id string
	warnings := missingWarnings(missing)
	// 覆盖时把包里被清空的凭据键换回目标已有值：与 placeTool 保留 configs、工作流
	// 按变量名保留 env_vars 的 secret 是同一套约定 —— 上架时凭据被清成空串，
	// 直接整体替换会把目标已经配好的 api_key / token 抹掉。
	// 覆盖换的是内容，不是凭据；目标配一次不容易，别让它在「更新版本」时白丢。
	if prev != nil {
		if target, isMap := prev.Config.Data.(map[string]any); isMap {
			if kept := assetpack.PreserveSecretKeys(cfgMap, target); kept > 0 {
				warnings = append(warnings, fmt.Sprintf(
					"已保留本租户原有的 %d 项凭据配置（包内这些项在上架时已清空）", kept))
			}
		}

		// UpdateAgent 是全字段覆盖：端点必须先把旧值回填（它属于本租户的对外暴露配置，
		// 包里没有也不该被抹掉）；两个开关按包走，不再回填。
		ag.Endpoints = prev.Endpoints
		if uerr := agentRepo.UpdateAgent(info, prev.ID, ag); uerr != nil {
			// 目录已经被新内容替换掉了：这里**不删**它。行还是旧的、文件是新的，
			// 智能体至少还能跑（AGENT.md 就是新正文），补一次导入即可对齐；
			// 删掉反而会留下「有行、没文件」的坏实体，比不一致更糟。
			xhttp.ErrJSON(c, "覆盖智能体记录失败（文件已写入，请重试导入）: "+uerr.Error(), nil, "")
			return
		}
		id = prev.ID
		src.overwritten = true
		// 超权身份是单独一列（UpdateAgent 刻意不写它，见其注释）：这里显式对齐。
		// 降级「本租户最后一个超级智能体」会被仓储拒绝 —— 那是保护不是故障，保留原值并提示。
		if prev.IsSupervisor != wantSupervisor {
			if serr := agentRepo.SetSupervisor(info, prev.ID, wantSupervisor); serr != nil {
				warnings = append(warnings, "超级智能体开关未能按包设置（"+serr.Error()+"），已保留原设置")
			} else if wantSupervisor {
				warnings = append(warnings, "已按包把该实体设为超级智能体（拥有全部内置工具、任何入口都读写草稿）")
			} else {
				warnings = append(warnings, "已按包取消该实体的超级智能体身份")
			}
		}
		// 覆盖时先把这个 agent 的私有技能记录清掉：包里没有的私有技能，文件已经随目录删了，
		// 留着行就是「点得开、正文不存在」的空壳（而且会污染列表与注入清单）。
		if derr := skillRepo.DeleteByAgentID(info.Schema, id); derr != nil {
			warnings = append(warnings, "清理既有私有技能记录失败："+derr.Error())
		}
		if models.IsProtectedAgent(name) {
			warnings = append(warnings, "覆盖的是系统内置智能体（受保护名）：内容与开关按包替换，受保护身份由名字决定")
		}
	} else {
		var cerr error
		id, cerr = agentRepo.CreateAgent(info, ag, "") // creatorAgentID 空 = 不写 agent_relations
		if cerr != nil {
			_ = st.DeletePrefix(ctx, dstDir)
			xhttp.ErrJSON(c, "写入智能体记录失败: "+cerr.Error(), nil, "")
			return
		}
		if wantSupervisor {
			warnings = append(warnings, "已按包把该实体设为超级智能体（拥有全部内置工具、任何入口都读写草稿）")
		}
	}
	// 私有技能重建：它属于这个 agent 自己的目录（文件已随目录还原），不是绑定关系
	warnings = append(warnings, restorePrivateSkills(info, id, payload["private_skills"])...)
	if prev != nil {
		// 覆盖：绑定按包内清单**全量替换**（只加不减会让包里没有的技能继续挂在这个 agent 上）
		warnings = append(warnings, replaceInstalledSkillBindings(info, id, src.manifest.Deps.Skills)...)
	} else {
		warnings = append(warnings, bindInstalledSkills(info, id, src.manifest.Deps.Skills)...)
	}
	// 先装随包带过来的工具，再写授权 —— 顺序反了授权行会指向还不存在的工具
	warnings = append(warnings, installBundledTools(info, src)...)
	if prev != nil {
		warnings = append(warnings, replaceAgentGrants(info, id, src.manifest.Deps.Tools)...)
	} else {
		warnings = append(warnings, writeAgentGrants(info, id, src.manifest.Deps.Tools)...)
	}
	// 调用关系是「谁能调用我」的反向授权，跨租户无法重映射；明说一句免得以为装完就能被调
	warnings = append(warnings, "调用授权（谁能调用该智能体）不会随资产迁移，需要在目标租户的「调用授权」里重新配置")

	respondInstalled(c, info, src, name, id, missing, warnings)
}

// restorePrivateSkills 把包里随 agent 带过来的私有技能重建成新 agent 的私有行。
//
// 文件已经由 RestoreFiles 铺到 agents/<name>/skills/<skill>/ —— 但「有目录没有 skills 行」
// 在运行期是完全看不见的：技能列表点不到、文件工具找不到入口、agent 也加载不到正文。
// 所以文件与行必须成对出现。
//
// 失败不整单回滚：agent 本体已经装好，少一个私有技能只记 warning（用户还能手动补），
// 让整次安装失败反而把已经正确的内容一起扔掉。
func restorePrivateSkills(info *auth.AuthInfo, agentID string, raw any) []string {
	list, ok := raw.([]any)
	if !ok || len(list) == 0 {
		return nil
	}
	var warnings []string
	restored := make([]string, 0, len(list))
	for _, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name := strings.TrimSpace(asString(m["name"]))
		if !paths.ValidName(name) {
			warnings = append(warnings, "私有技能名不合法，未重建："+name)
			continue
		}

		sk := &models.Skill{
			Name:            name,
			Description:     xsql.String(asString(m["description"])),
			Metadata:        xsql.JSONB{Data: map[string]any{}},
			Status:          xsql.Int(models.StatusNormal),
			Restricted:      xsql.Bool(asBool(m["restricted"])),
			UserInvocable:   xsql.Bool(asBool(m["user_invocable"])),
			CommandDispatch: xsql.String(asString(m["command_dispatch"])),
			CommandTool:     xsql.String(asString(m["command_tool"])),
			AgentID:         xsql.UUID(agentID), // 归属指向新 agent，不是源 agent
			CreatedBy:       xsql.UUID(info.ID),
			UpdatedBy:       xsql.UUID(info.ID),
		}
		if md, ok := m["metadata"].(map[string]any); ok && len(md) > 0 {
			sk.Metadata = xsql.JSONB{Data: md}
		}

		id, err := skillRepo.Create(info.Schema, sk)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("私有技能 %s 重建失败：%v", name, err))
			continue
		}
		restored = append(restored, name)
		warnings = append(warnings, writeSkillGrants(info, id, toolDepsOf(m["tool_grants"]))...)
	}
	// 成功也要说一声：装完看不到私有技能时，用户第一反应是「没带上」，这类静默成功最难排查
	if len(restored) > 0 {
		warnings = append(warnings, fmt.Sprintf(
			"已随包重建 %d 个私有技能（%s）", len(restored), strings.Join(restored, "、")))
	}
	return warnings
}

// toolDepsOf 把包里的 [{type, ref}] 还原成工具依赖声明（坏项直接跳过）。
func toolDepsOf(raw any) []assetpack.ToolDep {
	list, ok := raw.([]any)
	if !ok {
		return nil
	}
	out := make([]assetpack.ToolDep, 0, len(list))
	for _, item := range list {
		if m, ok := item.(map[string]any); ok {
			out = append(out, assetpack.ToolDep{Type: asString(m["type"]), Ref: asString(m["ref"])})
		}
	}
	return out
}

// bindInstalledSkills 按名字把技能绑到新智能体。
//
// 只能用名字匹配：跨租户的 skill id 没有意义。而且只认同名「租户级」技能（agent_id 为
// 空的那些，FindByName 的既定口径）——别人的私有技能本来就看不见，跳过并记一条 warning，
// 静默少绑会让「这个 agent 怎么不按文档回答」变成一次漫长的排查。
func bindInstalledSkills(info *auth.AuthInfo, agentID string, names []string) []string {
	var warnings []string
	for _, sname := range names {
		s, err := skillRepo.FindByName(info.Schema, sname, "")
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("技能 %s 查询失败，未绑定：%v", sname, err))
			continue
		}
		if s == nil {
			warnings = append(warnings, "目标租户没有同名技能，未绑定："+sname)
			continue
		}
		// 绑一个已废弃的技能等于绑了个不存在的技能：留一条话，别让人去查「为什么没加载」
		if int(s.Status) == models.StatusDeprecated {
			warnings = append(warnings, "目标租户的同名技能已废弃，未绑定："+sname)
			continue
		}
		if _, berr := skillRepo.AddBinding(info.Schema, agentID, string(s.ID), info.ID); berr != nil {
			warnings = append(warnings, fmt.Sprintf("技能 %s 绑定失败：%v", sname, berr))
		}
	}
	if len(names) > 0 {
		warnings = append(warnings, fmt.Sprintf("已按名字尝试绑定 %d 个技能（缺失的见上）", len(names)))
	}
	return warnings
}

// replaceInstalledSkillBindings 覆盖安装时按包内清单**全量替换**技能绑定。
//
// 与 bindInstalledSkills 的差别只有最后一步（ReplaceByAgent 而不是逐条 AddBinding）：
// 覆盖语义下「包里没有的技能」必须解绑，否则旧绑定会跟着新内容继续生效。
// 名字匹配的口径与逐条版一致：只认租户级同名技能，缺失 / 已废弃的跳过并记 warning。
func replaceInstalledSkillBindings(info *auth.AuthInfo, agentID string, names []string) []string {
	var warnings []string
	ids := make([]string, 0, len(names))
	for _, sname := range names {
		s, err := skillRepo.FindByName(info.Schema, sname, "")
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("技能 %s 查询失败，未绑定：%v", sname, err))
			continue
		}
		if s == nil {
			warnings = append(warnings, "目标租户没有同名技能，未绑定："+sname)
			continue
		}
		if int(s.Status) == models.StatusDeprecated {
			warnings = append(warnings, "目标租户的同名技能已废弃，未绑定："+sname)
			continue
		}
		ids = append(ids, string(s.ID))
	}
	if err := skillRepo.ReplaceByAgent(info.Schema, agentID, ids, info.ID); err != nil {
		return append(warnings, "技能绑定重建失败："+err.Error())
	}
	warnings = append(warnings, fmt.Sprintf(
		"技能绑定已按包内清单全量替换（保留 %d 个，缺失的见上）", len(ids)))
	return warnings
}

// agentGrantsOf 把依赖声明转成授权行。
//
// grant_kind 缺省按 allow：老资产可能没记这个字段，按 required 会让 deny 再也收不回来。
func agentGrantsOf(agentID string, deps []assetpack.ToolDep) []models.AgentToolGrant {
	grants := make([]models.AgentToolGrant, 0, len(deps))
	for _, d := range deps {
		if d.Ref == "" || d.Type == "" {
			continue
		}
		kind := d.GrantKind
		if kind == "" {
			kind = models.GrantKindAllow
		}
		grants = append(grants, models.AgentToolGrant{
			AgentID: agentID, GrantKind: kind, ItemType: d.Type, Ref: d.Ref,
		})
	}
	return grants
}

// writeAgentGrants 新建安装时写工具授权（全量替换）。
//
// 空清单直接跳过：新建走的是 CreateAgent，它已经物化了缺省授权（普通 agent 的读写组 /
// supervisor 的 core 组），这里再 Replace 成空会把刚种下的东西抹掉。
func writeAgentGrants(info *auth.AuthInfo, agentID string, deps []assetpack.ToolDep) []string {
	if len(deps) == 0 {
		return nil
	}
	grants := agentGrantsOf(agentID, deps)
	if len(grants) == 0 {
		return nil
	}
	if err := grantRepo.ReplaceAgentGrants(info.Schema, agentID, info.ID, grants); err != nil {
		return []string{"工具授权写入失败：" + err.Error()}
	}
	return []string{fmt.Sprintf(
		"已写入 %d 条工具授权；本租户缺少对应工具时它们不会生效，可在「工具授权」里调整", len(grants))}
}

// replaceAgentGrants 覆盖安装时写工具授权：**即使包里一条都没声明也要清空旧授权**。
// 覆盖的语义是「以包为准」，留着旧授权会让新内容跑在旧的工具可见性上。
func replaceAgentGrants(info *auth.AuthInfo, agentID string, deps []assetpack.ToolDep) []string {
	grants := agentGrantsOf(agentID, deps)
	if err := grantRepo.ReplaceAgentGrants(info.Schema, agentID, info.ID, grants); err != nil {
		return []string{"工具授权写入失败：" + err.Error()}
	}
	if len(grants) == 0 {
		return []string{"工具授权已按包内清单清空（包内没有声明任何工具）"}
	}
	return []string{fmt.Sprintf(
		"工具授权已按包内清单全量替换（%d 条）；本租户缺少对应工具时它们不会生效", len(grants))}
}

// suggestFreeAgentName 给一个本租户没被占用的智能体名（最多试 50 个）。
func suggestFreeAgentName(info *auth.AuthInfo, base string) string {
	cand := assetpack.SuggestName(base, nil)
	for i := 0; i < 50; i++ {
		exist, err := agentRepo.ExistsName(info, cand, "")
		if err != nil || !exist {
			return cand
		}
		cand = assetpack.SuggestName(cand, []string{cand})
	}
	return base
}

// ===== 工作流 =====

// installWorkflow 安装工作流：从快照里的 FLOW.json 重新建一行 {schema}.flows。
//
// 与其它三类的差别：不落任何文件（定义只在库里），所以「先文件、后 DB」那条顺序在这里
// 退化成一次 INSERT，失败也不用清理目录。
//
// 装上就是「正常」态（status=1）。早期这里落的是 2，本意是「停用」，但 flows 的 2 曾是
// **锁定**（只读、仍可运行），既不是停用也让用户改不了 —— 工作流根本没有「停用」这个状态
// （2 现已从工作流 / agent / skill 全部取消，只剩自定义工具用它表示「停用」）。
// 引用需要核对这件事改成提示（warnings）而不是把记录锁死。
func installWorkflow(c *gin.Context, info *auth.AuthInfo, src *installSource, name string, req installRequest) {
	prev, perr := flowRepo.FindByName(info, name)
	if perr != nil {
		xhttp.ErrJSON(c, "check name: "+perr.Error(), nil, "")
		return
	}
	if prev != nil {
		if !req.Overwrite {
			xhttp.ErrJSON(c, "本租户已有同名工作流："+name, gin.H{
				"suggested_name": suggestFreeFlowName(info, name),
				"can_overwrite":  true,
			}, "")
			return
		}
		// 废弃态不能改定义（UpdateFlow 会拒）：提前说清，别让用户走完确认才发现
		if int(prev.Status) == models.StatusDeprecated {
			xhttp.ErrJSON(c, "本租户的同名工作流处于废弃态，覆盖前请先把它切回「正常」", nil, "")
			return
		}
	} else {
		// 工作流与智能体共用一个名字空间（创建接口就是这么校验的）：放行会造出
		// 一个「按名字查找永远命中另一个」的实体。覆盖的是同一个实体，不可能撞 agent，只在新建时查。
		if agentExist, aerr := agentRepo.ExistsName(info, name, ""); aerr != nil {
			xhttp.ErrJSON(c, "check name: "+aerr.Error(), nil, "")
			return
		} else if agentExist {
			xhttp.ErrJSON(c, "本租户已有同名智能体，工作流名不能与智能体重名："+name, gin.H{
				"suggested_name": suggestFreeFlowName(info, name),
			}, "")
			return
		}
		// 配额预检：与「新建工作流」同一条闸，别让超限的租户先建出行再回滚；覆盖不新增实体，跳过
		if cnt, qerr := quota.CountFlows(info.Schema); qerr != nil {
			xhttp.ErrJSON(c, "统计工作流数量失败: "+qerr.Error(), nil, "")
			return
		} else if qerr := models.CheckQuota("flow", cnt, quota.Resolve(info.Schema).MaxFlows); qerr != nil {
			xhttp.ErrJSON(c, qerr.Error(), nil, "")
			return
		}
	}

	st, serr := getStorage()
	if serr != nil {
		xhttp.ErrJSON(c, "storage not ready: "+serr.Error(), nil, "")
		return
	}
	ctx := context.Background()
	raw, rerr := st.ReadFile(ctx, path.Join(src.snapDir, assetpack.WorkflowFileName))
	if rerr != nil {
		xhttp.ErrJSON(c, "快照里没有工作流定义（"+assetpack.WorkflowFileName+"）：请让提供方重新上架", nil, "")
		return
	}
	var def assetpack.WorkflowSnapshot
	if uerr := json.Unmarshal(raw, &def); uerr != nil {
		xhttp.ErrJSON(c, "工作流定义损坏: "+uerr.Error(), nil, "")
		return
	}

	// 覆盖同名工作流时保留目标已配好的 secret 值。
	//
	// 包里的 secret 在上架时就被清空了（跨租户不该带凭据），但「包里没有」不等于
	// 「目标该被抹掉」：同名覆盖换的是内容，不是凭据。按变量名把目标现有的值补回
	// 包里空着的那些；目标没有的（本次新增的密钥变量）仍为空，装完要人填。
	preservedSecrets := false
	if prev != nil {
		merged, merr := secureval.MergeEnvVarSecrets(jsonbBytes(prev.EnvVars), def.EnvVars)
		if merr != nil {
			xhttp.ErrJSON(c, "合并环境变量失败: "+merr.Error(), nil, "")
			return
		}
		if string(merged) != string(def.EnvVars) {
			preservedSecrets = true
		}
		def.EnvVars = json.RawMessage(merged)
	}

	f := &models.Flow{
		Name:              name,
		Description:       xsql.String(def.Description),
		Diagram:           jsonbFromRaw(def.Diagram),
		Arguments:         jsonbFromRaw(def.Arguments),
		Outputs:           jsonbFromRaw(def.Outputs),
		EnvVars:           jsonbFromRaw(def.EnvVars),
		FlowVars:          jsonbFromRaw(def.FlowVars),
		Timeout:           xsql.Int(def.Timeout),
		MaxRecursionDepth: xsql.Int(def.MaxRecursionDepth),
		MaxNodeCount:      xsql.Int(def.MaxNodeCount),
		MaxExecCount:      xsql.Int(def.MaxExecCount),
		MaxRunRecords:     xsql.Int(def.MaxRunRecords),
		MaxSessionRecords: xsql.Int(def.MaxSessionRecords),
	}

	// 只允许「正常 / 废弃」两种落点；覆盖时缺省沿用目标既有状态（覆盖的是内容，不是生命周期）
	status := models.StatusNormal
	if prev != nil {
		status = int(prev.Status)
	}
	if req.Status != nil && (*req.Status == 1 || *req.Status == 3) {
		status = *req.Status
	}

	warnings := []string{
		"工作流里的模型 / 工具 / 子流程引用不会随资产迁移，运行前请逐项核对",
	}
	switch {
	case prev == nil:
		warnings = append(warnings,
			"凭据类变量（env_vars 与节点里的 api_key 等）已在上架时清空，需要重新填写")
	case len(def.EnvVars) == 0:
		// 包内整块 env_vars 为空：合并无从对齐，目标原有的变量会随本次覆盖移除。
		// 这不该被说成「已保留」—— 那会让人以为密钥还在。
		warnings = append(warnings,
			"包内未携带环境变量，本次覆盖会移除目标原有的环境变量（含密钥），需要重新配置")
	case preservedSecrets:
		warnings = append(warnings,
			"同名工作流已覆盖：已按变量名保留本租户原有的密钥值；包内新增的密钥类变量为空，需要填写")
	default:
		warnings = append(warnings,
			"同名工作流已覆盖：包内的密钥类变量为空，且没有可沿用的原值，需要填写")
	}

	var id string
	if prev != nil {
		// UpdateFlow 不写 status / endpoints / 断点，也不动 flow_versions：对外暴露配置、
		// 调试状态与发布历史都属于目标租户。覆盖改的是草稿 —— 正式入口跑的是已发布版本。
		if uerr := flowRepo.UpdateFlow(info, prev.ID, f); uerr != nil {
			xhttp.ErrJSON(c, "覆盖工作流定义失败: "+uerr.Error(), nil, "")
			return
		}
		id = prev.ID
		src.overwritten = true
		warnings = append(warnings,
			"覆盖只替换草稿（图 / 变量 / 限额）：已发布的版本、对外端点与断点保留原样；"+
				"要让新内容对外生效，需在编辑器里发布新版本")
		if status != int(prev.Status) {
			if uerr := flowRepo.UpdateStatus(info, id, status); uerr != nil {
				warnings = append(warnings, "状态未能改成目标值，已保留原状态："+uerr.Error())
			}
		}
	} else {
		var cerr error
		id, cerr = flowRepo.CreateFlow(info, f)
		if cerr != nil {
			xhttp.ErrJSON(c, "写入工作流记录失败: "+cerr.Error(), nil, "")
			return
		}
		// CreateFlow 的列缺省就是「正常」（1）；只有调用方显式要求废弃时才多写一次
		if status != models.StatusNormal {
			if uerr := flowRepo.UpdateStatus(info, id, status); uerr != nil {
				warnings = append(warnings, "工作流已创建，但状态没改成目标值："+uerr.Error())
			}
		}
	}
	respondInstalled(c, info, src, name, id, nil, warnings)
}

// jsonbFromRaw 把 FLOW.json 里的原始片段转成 jsonb 列值；空片段 / null 落成 nil。
func jsonbFromRaw(raw json.RawMessage) xsql.JSONB {
	if len(raw) == 0 || string(raw) == "null" {
		return xsql.JSONB{}
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return xsql.JSONB{}
	}
	return xsql.JSONB{Data: v}
}

// jsonbBytes 取 JSONB 的原始字节（Data 可能是 []byte / RawMessage / string）。
// 与上面的 jsonbFromRaw 互为反向：合并 env_vars 时要的是原始形态，不是解码后的 map。
func jsonbBytes(j xsql.JSONB) []byte {
	switch v := j.Data.(type) {
	case nil:
		return nil
	case []byte:
		return v
	case json.RawMessage:
		return []byte(v)
	case string:
		return []byte(v)
	default:
		b, err := json.Marshal(j.Data)
		if err != nil {
			return nil
		}
		return b
	}
}

// suggestFreeFlowName 给一个本租户没被占用的工作流名（最多试 50 个）。
// 两个名字空间都要避开：工作流与智能体共用一张「按名字查找」的表。
func suggestFreeFlowName(info *auth.AuthInfo, base string) string {
	cand := assetpack.SuggestName(base, nil)
	for i := 0; i < 50; i++ {
		flowExist, ferr := flowRepo.ExistsFlowName(info, cand)
		agentExist, aerr := agentRepo.ExistsName(info, cand, "")
		if ferr == nil && aerr == nil && !flowExist && !agentExist {
			return cand
		}
		cand = assetpack.SuggestName(cand, []string{cand})
	}
	return base
}

// ===== 公共尾巴 =====

// respondInstalled 回包 + 写安装记录（记录失败只提示，不影响这次安装）。
//
// 直接上传资产包导入时没有平台资产行（src.asset 为 nil），也就不该写安装流水 ——
// 流水表的语义是「从平台资产装的」，凭空塞一条不存在的 asset_id 只会让「已安装」标错。
func respondInstalled(c *gin.Context, info *auth.AuthInfo, src *installSource, name, id string, missing, warnings []string) {
	// 全局变量缺失要在这里说：装完不会立刻报错，第一次跑到相关节点才失败，
	// 而那时用户看到的是一条与「安装」毫无关联的节点错误。
	warnings = append(warnings, missingGlobalVarWarnings(info, src.manifest.Deps.GlobalVars)...)

	// 覆盖前先记下「上次装的是不是同一份内容」：写记录之后这一条的指纹就变成新的了。
	// 只提示、不阻断 —— 指纹说明的是「两个包一样」，不能证明实体当前内容没被本地改过。
	if src.overwritten && src.manifest.Digest != "" {
		if prevDigest, derr := sharedassets.LatestInstallDigest(info.Schema, id); derr == nil &&
			prevDigest == src.manifest.Digest {
			warnings = append(warnings, "本次覆盖的包与上次安装时是同一份内容（指纹 "+
				assetpack.ShortDigest(src.manifest.Digest)+"）：若之后没在本地改过，覆盖并没有带来变化")
		}
	}
	if a := src.asset; a != nil {
		if err := sharedassets.RecordInstall(a.ID, info.Schema, a.Build, name, id, info.ID, info.Name, src.manifest.Digest); err != nil {
			warnings = append(warnings, "安装记录写入失败（不影响使用）："+err.Error())
		}
	}
	body := gin.H{
		"installed":     gin.H{"name": name, "id": id},
		"kind":          string(src.kind),
		"warnings":      append(append([]string{}, src.notices...), warnings...),
		"skipped_files": missing,
		// 前端据此把提示从「已导入」改成「已覆盖导入」；覆盖是破坏性动作，回包必须说清
		"overwritten": src.overwritten,
		// 内容指纹与清单语义版本：前端展示溯源用，也是「同一份包」判断的依据
		"digest": src.manifest.Digest,
		"spec":   src.manifest.Spec,
	}
	if a := src.asset; a != nil {
		body["from_asset"] = a.ID
		body["from_build"] = a.Build
		body["source_schema"] = a.SourceSchema
		body["source_by_name"] = a.ListedByName
	}
	xhttp.OkJSON(c, "Success", body, "")
}

// DeleteInstall 删一条安装记录（DELETE /admin/share/installs/:asset_id/:installed_id）。
//
// 只删流水，**不会卸载**已经装进租户的技能 / 工具 / 智能体。
// 允许租户自己删的理由：实体被删掉、或者装到了不想留的对象上时，那条记录会一直
// 把资产标成「已安装」，反过来挡住重新安装或误判快照状态。删记录就是「把账抹平」。
func DeleteInstall(c *gin.Context) {
	info, ok := mustAuth(c)
	if !ok {
		return
	}
	assetID := strings.TrimSpace(c.Param("asset_id"))
	installedID := strings.TrimSpace(c.Param("installed_id"))
	if assetID == "" || installedID == "" {
		xhttp.ErrJSON(c, "缺少 asset_id / installed_id", nil, "")
		return
	}
	deleted, err := sharedassets.DeleteInstall(assetID, info.Schema, installedID)
	if err != nil {
		xhttp.ErrJSON(c, assetErrMsg("delete install record", err), nil, "")
		return
	}
	if !deleted {
		xhttp.ErrJSON(c, "安装记录不存在（可能已被删除）", nil, "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"deleted": true}, "")
}

// ClearInstalls DELETE /admin/share/installs —— 清空本租户的全部安装记录。
//
// 同样只删流水、不卸载实体。用一条 DELETE 而不是逐条调用：清空要么全成要么全不成，
// 删到一半失败剩下的记录反而更难解释。
func ClearInstalls(c *gin.Context) {
	info, ok := mustAuth(c)
	if !ok {
		return
	}
	n, err := sharedassets.ClearInstalls(info.Schema)
	if err != nil {
		xhttp.ErrJSON(c, assetErrMsg("clear install records", err), nil, "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"deleted": n}, "")
}

// missingGlobalVarWarnings 列出包里引用、但本租户还没有的全局变量。
//
// 只提示不阻断：变量可以先建后装，也可以先在别处试用；但「缺了会怎样」
// 必须说清（运行到相关节点直接失败，不是静默空值）。
func missingGlobalVarWarnings(info *auth.AuthInfo, names []string) []string {
	if len(names) == 0 {
		return nil
	}
	missing := make([]string, 0, len(names))
	for _, name := range names {
		v, err := globalVarRepo.Get(info.Schema, name)
		if err != nil || v == nil {
			missing = append(missing, name)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return []string{fmt.Sprintf(
		"该资产引用了 %d 个本租户还没有的全局变量（%s）：先在「设置 → 全局变量」创建，否则运行到相关节点会直接失败",
		len(missing), strings.Join(missing, "、"))}
}

func missingWarnings(missing []string) []string {
	out := make([]string, 0, len(missing))
	for _, f := range missing {
		out = append(out, "快照缺少文件，已跳过："+f)
	}
	return out
}

func asString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// flagFrom 读包内的布尔开关。
//
// 字段缺失（老版本的包）时用 fallback，而不是当成 false：覆盖导入会把既有实体上
// 管理员开过的「超级智能体 / 不限制工具 / 可发现未绑定技能」静默关掉，那是降权，不是缺省。
func flagFrom(payload map[string]any, key string, fallback bool) bool {
	if v, ok := payload[key].(bool); ok {
		return v
	}
	return fallback
}

func asBool(v any) bool {
	b, _ := v.(bool)
	return b
}
