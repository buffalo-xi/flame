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
	"sync"

	"flame/nexus/service/agent/toolgrant"
	"flame/nexus/shared/assetpack"
	"flame/nexus/shared/auth"
	"flame/nexus/shared/config"
	"flame/nexus/shared/customtoolstore"
	"flame/nexus/shared/db"
	"flame/nexus/shared/listingpolicy"
	"flame/nexus/shared/models"
	"flame/nexus/shared/paths"
	"flame/nexus/shared/repository/sharedassets"
	"flame/tools/xhttp"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// buildMu 串行化「快照取号 → 写文件」这一段（理由见 assetpack.FreeBuild）。
// 上架是低频人工操作，排一条队列比维护一张 per-asset 锁表简单。
// 跨进程（多副本部署）的撞号窗口仍在，那一步要靠 DB 行锁。
var buildMu sync.Mutex

type listingRequest struct {
	Kind string `json:"kind"`
	Ref  string `json:"ref"` // skill: id；custom_tool: 名字
}

// mergeGlobalVarDeps 合并两批全局变量依赖（去重、升序）。
//
// 为什么是合并而不是覆盖：工作流的依赖在 planWorkflow 里扫（那时才拿得到整份定义），
// 通用那段扫的是 plan.payload —— 工作流的 payload 只有名字与描述，直接覆盖会把
// 已经扫到的引用抹掉。
func mergeGlobalVarDeps(a, b []string) []string {
	seen := make(map[string]struct{}, len(a)+len(b))
	out := make([]string, 0, len(a)+len(b))
	for _, list := range [][]string{a, b} {
		for _, name := range list {
			if name == "" {
				continue
			}
			if _, ok := seen[name]; ok {
				continue
			}
			seen[name] = struct{}{}
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// planFor 按类型取出「一次上架 / 导出需要的全部输入」。
//
// 上架与导出共用这一份组装逻辑：两边都是「读本租户实体 → 白名单化 payload → 声明依赖 →
// 整棵目录打包」，差别只在打包的落点（平台快照 vs 临时目录 → zip）。分成两份会让
// 「导出包里带了什么」与「上架出去的内容」慢慢走偏。
func planFor(kind assetpack.Kind, info *auth.AuthInfo, ref string) (listingPlan, error) {
	switch kind {
	case assetpack.KindSkill:
		return planSkill(info, ref)
	case assetpack.KindTool:
		return planTool(info, ref)
	case assetpack.KindAgent:
		return planAgent(info, ref)
	case assetpack.KindWorkflow:
		return planWorkflow(info, ref)
	}
	return listingPlan{}, fmt.Errorf("未知的资产类型：%s", kind)
}

// CreateListing 把本租户的一个定义域实体上架成平台资产（进入待审）。
//
// 三类都开：skill / custom_tool / agent。智能体只发「本体 + 技能名 + 工具 ref」，
// 私有技能目录不打包（见 listingAgent），装上之后的技能绑定与授权由 installAgent 重建。
func CreateListing(c *gin.Context) {
	info, ok := mustAuth(c)
	if !ok {
		return
	}

	var req listingRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrJSON(c, "Invalid request body", nil, "")
		return
	}
	kind := assetpack.Kind(strings.TrimSpace(req.Kind))
	if !kind.Valid() {
		xhttp.ErrCode(c, "asset.invalid_kind", map[string]any{"kind": string(kind)},
			"未知的资产类型（kind 只支持 skill / custom_tool / agent / workflow）", "")
		return
	}
	ref := strings.TrimSpace(req.Ref)
	if ref == "" {
		xhttp.ErrCode(c, "asset.ref_required", nil, "缺少 ref（skill / agent / workflow 用 id，custom_tool 用名字）", "")
		return
	}

	switch kind {
	case assetpack.KindSkill:
		listingSkill(c, info, ref)
	case assetpack.KindTool:
		listingTool(c, info, ref)
	case assetpack.KindAgent:
		listingAgent(c, info, ref)
	case assetpack.KindWorkflow:
		listingWorkflow(c, info, ref)
	}
}

// listingPlan 一次上架需要的全部输入（由各类型的 listingXxx 组装）。
type listingPlan struct {
	kind     assetpack.Kind
	name     string         // 资产名（= 源实体名）
	sourceID string         // 源实体 id（工具没有稳定 id 时用生成的占位值，仅溯源）
	entry    string         // 入口文件（快照里必须存在）
	srcDir   string         // 源目录（storage 逻辑 key）
	payload  map[string]any // 已白名单化的实体快照
	deps     assetpack.Deps
	warnings []string
	skipDirs []string             // 不随资产走的子目录（当前四类都不设：agent 的私有技能要整棵带走）
	trees    []assetpack.PackTree // 附加进包里的目录树（依赖的自定义工具本体）
	cleanup  func()               // 打包后要收掉的临时内容（工作流的 FLOW.json 暂存目录），可为 nil
}

// validatePlanName 名字校验：各类型统一走 assetpack（工作流走 ValidateWorkflowName 的
// 措辞，理由是「名字就是对外工具名」，不是路径安全；其余三类是目录名）。
func validatePlanName(kind assetpack.Kind, name string) error {
	if kind == assetpack.KindWorkflow {
		return assetpack.ValidateWorkflowName(name)
	}
	return assetpack.ValidateName(name)
}

// listingEntity 打包 + 落库 + 回包（各类型的公共尾巴）。
func listingEntity(c *gin.Context, info *auth.AuthInfo, plan listingPlan) {
	if plan.cleanup != nil {
		defer plan.cleanup()
	}
	if err := validatePlanName(plan.kind, plan.name); err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}

	st, serr := getStorage()
	if serr != nil {
		xhttp.ErrJSON(c, "storage not ready: "+serr.Error(), nil, "")
		return
	}

	// 同名重新上架 = 覆盖同一行并 build+1（唯一索引是 kind + source_schema + lower(name)）
	prev, qerr := findMine(plan.kind, info.Schema, plan.name)
	if qerr != nil {
		xhttp.ErrJSON(c, "query asset: "+qerr.Error(), nil, "")
		return
	}
	assetID := uuid.NewString()
	build := 1
	if prev != nil {
		assetID = prev.ID
	}

	ctx := context.Background()
	// 取号与写文件必须串行（理由见 assetpack.FreeBuild）：两路并发各自算出同一个 build，
	// 会把两份内容交错写进同一棵目录，而行只记住其中一份的清单。
	buildMu.Lock()
	if prev != nil {
		b, berr := assetpack.FreeBuild(ctx, st, assetID, prev.Build+1)
		if berr != nil {
			buildMu.Unlock()
			xhttp.ErrJSON(c, berr.Error(), nil, "")
			return
		}
		build = b
	}
	dstDir := paths.PlatformAssetKey(assetID, build)
	files, perr := assetpack.PackFilesWith(ctx, st, plan.srcDir, dstDir, plan.entry,
		assetpack.DefaultLimits(), assetpack.PackOptions{SkipDirs: plan.skipDirs, Trees: plan.trees})
	buildMu.Unlock()
	if perr != nil {
		_ = st.DeletePrefix(ctx, dstDir)
		xhttp.ErrJSON(c, perr.Error(), nil, "")
		return
	}

	// 依赖清单补上全局变量引用：包里只带名字，值在目标租户自己的 global_vars 里。
	// 放在这里而不是各 plan* 里：agent / skill 要读定义域目录下的 AGENT.md / SKILL.md
	// 才能扫到 ${global_vars.X}，而工作流的 srcDir 是导出临时目录，只扫 payload 就够。
	plan.deps.GlobalVars = mergeGlobalVarDeps(plan.deps.GlobalVars,
		assetpack.CollectGlobalVarRefs(ctx, st, plan.srcDir, plan.payload))

	clean, secretWarnings := assetpack.StripSecrets(plan.kind, plan.payload)
	warnings := append(append([]string{}, plan.warnings...), secretWarnings...)

	payloadJSON, perr2 := assetpack.EncodeJSONB(clean)
	if perr2 != nil {
		_ = st.DeletePrefix(ctx, dstDir)
		xhttp.ErrJSON(c, "encode payload: "+perr2.Error(), nil, "")
		return
	}
	manifest := assetpack.Manifest{
		Kind:  plan.kind,
		Name:  plan.name,
		Entry: plan.entry,
		Deps:  plan.deps,
		Files: files,
	}
	// 指纹要在「脱敏后的 payload + 最终文件清单」上算：同一个实体重新上架时，
	// 只有内容真的变了，指纹才会变（build 号变、指纹不变 = 可以告诉用户「没变」）
	if serr := assetpack.StampManifest(&manifest, payloadJSON); serr != nil {
		_ = st.DeletePrefix(ctx, dstDir)
		xhttp.ErrJSON(c, "compute digest: "+serr.Error(), nil, "")
		return
	}
	manifestJSON, merr := assetpack.EncodeJSONB(manifest)
	if merr != nil {
		_ = st.DeletePrefix(ctx, dstDir)
		xhttp.ErrJSON(c, "encode manifest: "+merr.Error(), nil, "")
		return
	}

	// 上架策略：免审（缺省）直接 listed，被设为「必须审核」的租户才进 pending。
	// 读不到策略就按必须审核处理（fail closed）—— 策略表出问题时宁可多一次人工确认，
	// 也不能让未审内容直接进市场。
	requireReview, rerr := listingpolicy.RequireReview(info.Schema)
	if rerr != nil {
		requireReview = true
	}
	status := sharedassets.StatusPending
	if !requireReview {
		status = sharedassets.StatusListed
	}

	a := &sharedassets.Asset{
		ID:           assetID,
		Kind:         string(plan.kind),
		Name:         plan.name,
		DisplayName:  plan.name,
		Description:  asString(clean["description"]),
		SourceSchema: info.Schema,
		SourceID:     plan.sourceID,
		Build:        build,
		Status:       status,
		Visibility:   sharedassets.VisibilityAll,
		Manifest:     manifestJSON,
		Payload:      payloadJSON,
		ListedBy:     sql.NullString{String: info.ID, Valid: strings.TrimSpace(info.ID) != ""},
		ListedByName: info.Name,
	}
	if dn := asString(clean["display_name"]); dn != "" {
		a.DisplayName = dn
	}

	var werr error
	if prev != nil {
		// 重新上架按同一策略落地：必须审核的租户回到 pending（内容变了不能拿上一次的通过背书），
		// 免审租户则直接是 listed；两种都清掉上轮的审核结论（UpdateListing 里做）
		werr = sharedassets.UpdateListing(a)
	} else {
		werr = sharedassets.Insert(a)
	}
	if werr != nil {
		// 行没写成就把刚铺的快照收掉：留一份没有行指向的目录只会让人以为是历史快照
		_ = st.DeletePrefix(ctx, dstDir)
		// 并发上架：另一路已经把行推到更高的号，这次的内容作废（不算服务器错误，用户刷新即可）
		if errors.Is(werr, sharedassets.ErrStaleBuild) {
			xhttp.ErrCode(c, "asset.stale_build", map[string]any{"id": a.ID},
				"该资产刚被另一次上架覆盖（快照已前进），请刷新后重试", "")
			return
		}
		xhttp.ErrJSON(c, "save asset: "+werr.Error(), nil, "")
		return
	}

	// 行已指向新快照，旧快照快照就没有读者了（安装是复制、导出只打当前快照、没有回滚通道）。
	// 不收的话「重新上架一次 = 多一份完整镜像」，磁盘只增不减。
	// 删不掉不该让上架失败：启动时的 CleanupSnapshots 还会再收一次。
	_ = assetpack.PruneOldBuilds(ctx, st, assetID, build)

	xhttp.OkJSON(c, "Success", gin.H{
		"asset": gin.H{
			"id": assetID, "kind": string(plan.kind), "name": plan.name,
			"build": build, "status": status,
		},
		// need_review=false 表示这条已经直接上架（该租户免审），前端据此改文案
		"need_review": requireReview,
		"manifest": gin.H{
			"files": len(files), "total_size": manifest.TotalSize(), "deps": len(plan.deps.Tools),
		},
		"warnings": warnings,
	}, "")
}

// findMine 查「本租户**上架过的**同类同名资产」（重新上架判定用）；没有返回 (nil, nil)。
//
// OriginLocal 不能省：同一台机器上，「我上架的」与「别人导入的」可以有同名同 source_schema
// 的行（唯一键含 origin_server）。少了这个条件，重新上架会命中导入行并把它改成自己的内容 ——
// 既污染溯源（origin_server 还是别站名），也让真正的原创行永远停在旧快照。
func findMine(kind assetpack.Kind, schema, name string) (*sharedassets.Asset, error) {
	list, err := sharedassets.List(sharedassets.Filter{
		Kind: string(kind), SourceSchema: schema, OriginLocal: true,
	})
	if err != nil {
		return nil, err
	}
	for i := range list {
		if strings.EqualFold(list[i].Name, name) {
			return &list[i], nil
		}
	}
	return nil, nil
}

// ===== 技能 =====

func listingSkill(c *gin.Context, info *auth.AuthInfo, id string) {
	plan, err := planSkill(info, id)
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	listingEntity(c, info, plan)
}

func planSkill(info *auth.AuthInfo, id string) (listingPlan, error) {
	s, err := skillRepo.Get(info.Schema, id)
	if err != nil {
		return listingPlan{}, err
	}
	if s == nil {
		return listingPlan{}, errors.New("Skill not found")
	}

	// 私有技能不能单独导出 / 上架：正文住在 agents/<agent>/skills/<name>（见 paths 包顶部约定），
	// 一旦离开那个智能体，落点只剩「提级成租户级」—— 归属永久丢失、可见性白名单不随包走，
	// 导回来要么谁都用不了，要么是个和私有技能同名的孤立副本。私有技能的正确通道是随它的
	// 智能体一起走（见 planAgent 的 payload.private_skills）；要共享就先提级成租户级技能。
	if string(s.AgentID) != "" {
		agentName := string(s.AgentName)
		if agentName == "" {
			agentName = "已删除的智能体"
		}
		return listingPlan{}, fmt.Errorf(
			"技能 %s 是「%s」的私有技能，不能单独导出或上架：请导出它所属的智能体（私有技能随智能体一起走），或先把它提级为租户级技能",
			s.Name, agentName)
	}

	// 受保护名 / 废弃态只给提示，不阻断上架或导出：内容与身份是两件事 ——
	// 权限、超权、归属一律不随包走（见 planAgent 与 installAgent）。
	plan := listingPlan{
		kind:     assetpack.KindSkill,
		name:     string(s.Name),
		sourceID: string(s.ID),
		entry:    "SKILL.md",
		// 租户级技能：tenants/<schema>/skills/<name>
		srcDir: paths.SkillsKey(info.Schema) + "/" + string(s.Name),
	}
	if models.IsProtectedSkill(string(s.Name)) {
		plan.warnings = append(plan.warnings,
			"这是系统内置技能（受保护名）：分发出去的是内容副本，目标租户里不会获得任何特殊身份")
	}
	if int(s.Status) == models.StatusDeprecated {
		plan.warnings = append(plan.warnings, "该技能处于废弃态：包内是它当前的内容")
	}

	payload, berr := portableSkillPayload(info.Schema, string(s.ID))
	if berr != nil {
		return listingPlan{}, fmt.Errorf("read skill row: %w", berr)
	}

	grants, gerr := grantRepo.ListSkillGrants(info.Schema, string(s.ID))
	if gerr != nil {
		return listingPlan{}, fmt.Errorf("read skill grants: %w", gerr)
	}
	deps := assetpack.Deps{Tools: make([]assetpack.ToolDep, 0, len(grants))}
	for _, g := range grants {
		deps.Tools = append(deps.Tools, assetpack.ToolDep{Type: g.ItemType, Ref: g.Ref})
	}

	plan.payload = payload
	plan.deps = deps
	if err := attachToolBundles(info, &plan); err != nil {
		return listingPlan{}, err
	}
	return plan, nil
}

// portableSkillPayload 读 skills 行的 to_jsonb，再收敛成白名单 payload。
//
// 走 to_jsonb 而不是给 models.Skill 逐字段赋值：列名与类型由数据库给出，新增列不会
// 因为这里忘了同步而静默丢失（真正的取舍在 assetpack.PortableSkill 的白名单里）。
func portableSkillPayload(schema, id string) (map[string]any, error) {
	var raw []byte
	if err := db.DB.QueryRow(
		`SELECT to_jsonb(x) FROM `+tenantTable(schema, "skills")+` x WHERE x.id = $1`, id).Scan(&raw); err != nil {
		return nil, err
	}
	var row map[string]any
	if err := json.Unmarshal(raw, &row); err != nil {
		return nil, err
	}
	return assetpack.PortableSkill(row), nil
}

// portablePrivateSkills 读某 agent 的私有技能行，收敛成「可在目标租户重建」的条目。
//
// 每项 = PortableSkill 白名单 + tool_grants（该私有技能自己的工具依赖）。
// 不带 id / agent_id / 时间戳：目标租户重新生成，归属指向新 agent。
// 废弃态不带（与 ListPrivateNames 同口径）：重建一个已废弃的技能没有意义。
func portablePrivateSkills(schema, agentID string) ([]map[string]any, error) {
	if agentID == "" {
		return nil, nil
	}
	rows, err := db.DB.Query(`SELECT to_jsonb(x) FROM `+tenantTable(schema, "skills")+` x
		WHERE x.agent_id = $1 AND x.status <> 3 ORDER BY x.name ASC`, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]map[string]any, 0, 4)
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var row map[string]any
		if err := json.Unmarshal(raw, &row); err != nil {
			return nil, err
		}
		item := assetpack.PortableSkill(row)

		grants, gerr := grantRepo.ListSkillGrants(schema, asString(row["id"]))
		if gerr != nil {
			return nil, gerr
		}
		deps := make([]map[string]any, 0, len(grants))
		for _, g := range grants {
			deps = append(deps, map[string]any{"type": g.ItemType, "ref": g.Ref})
		}
		item["tool_grants"] = deps
		out = append(out, item)
	}
	return out, rows.Err()
}

// bundledToolTrees 把依赖里指向**本租户自定义工具**的那部分准备成包内附件。
//
// 只认 custom:<名字>：tools/ 下只有它们有本体。其余 scope 都不是本租户的文件 ——
// builtin 是代码自带、plugin 是磁盘插件、mcp/rpc/local 是远端连接、group/preset 只是组，
// 装到目标租户后照样能用，没有东西可带走。一层足够：自定义工具只依赖能力
// （sandbox.exec / egress），不依赖别的自定义工具，不存在依赖图。
//
// 这个判断上踩过两个坑，都表现为「打包莫名其妙失败或什么都没带」：
//   - 只过滤 builtin: 时 plugin:web_search 会被当成目录名，冒号在 Windows 上是非法文件名，
//     于是「没有本体可带」变成「打不开文件」，整个上架失败；
//   - 反过来，custom:<名字> 若连前缀一起当名字，tools/custom:xxx 并不存在，读契约拿到
//     ErrNoRows 被静默跳过 —— 自定义工具一个都没被带走，而这一步看不出任何异常。
//
// 返回的 bundles 只服务展示（包内 tool_bundles），安装时以文件名清单为准。
func bundledToolTrees(info *auth.AuthInfo, deps assetpack.Deps) ([]assetpack.PackTree, []map[string]any, error) {
	if len(deps.Tools) == 0 {
		return nil, nil, nil
	}
	store := customtoolstore.New(config.Cfg.Agent.RootDir, info.Schema)
	trees := make([]assetpack.PackTree, 0, len(deps.Tools))
	bundles := make([]map[string]any, 0, len(deps.Tools))
	seen := make(map[string]bool, len(deps.Tools))

	for _, d := range deps.Tools {
		name := customToolNameOfRef(d.Ref)
		if name == "" {
			continue
		}
		if seen[name] {
			continue
		}
		seen[name] = true

		t, err := store.Get("", name)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				continue // 有声明的 ref 却没有本体（工具已被删）：不是打包失败的理由
			}
			return nil, nil, err
		}
		trees = append(trees, assetpack.PackTree{
			SrcDir:    paths.ToolsKey(info.Schema) + "/" + name,
			DstPrefix: assetpack.BundledToolsPrefix + "/" + name,
			Required:  true, // 声明了依赖却说没有本体，宁可上架失败
		})
		bundles = append(bundles, map[string]any{
			"name":         t.Name,
			"display_name": t.DisplayName,
			"description":  t.Description,
		})
	}
	return trees, bundles, nil
}

// customToolNameOfRef 从授权 ref 里取出「可随包带走」的自定义工具短名，其余一律返回空串。
//
// 判据看 ref 的 scope，不看行里的 item_type —— 后者是从 ref 推导出来的，历史数据可能对不上。
// 抽成独立函数是为了能被单测直接钉住：这里错一次的后果一边是上架失败（plugin: 前缀被当目录名），
// 一边是静默少带东西（custom: 前缀被当目录名，读契约拿到 ErrNoRows 后跳过）。
func customToolNameOfRef(ref string) string {
	parsed, err := toolgrant.ParseRef(ref)
	if err != nil || parsed.Scope != toolgrant.ScopeCustom {
		return ""
	}
	return parsed.Subject // 目录名与工具短名都不带 custom: 前缀
}

// ===== 智能体 =====

// listingAgent 上架一个智能体。
//
// 三个开关**随包带走**（is_supervisor / tools_unrestricted / skill_discovery）：它们描述
// 「这个智能体怎么跑」，目标租户装完后按包设置，`warnings` 里点名，不静默降权。
// 上架/导出共用这份组装逻辑，所以「导出再导入」与「上架后安装」得到同一份东西。
//
// 只有 agent_relations **刻意不带出去**：它表达「谁能调用它」，是 uuid 关系、跨租户无法
// 重映射，装完必须在目标租户重新配置。
//
// 私有技能**一起带走**：文件在 agents/<agent>/skills/**（整棵目录打包），元数据进
// payload.private_skills，安装时重建成新 agent 的私有技能（新 id、归属指向新 agent）。
// 仅带文件不带元数据是不行的 —— 没有 skills 行的目录在运行期完全不可见。
func listingAgent(c *gin.Context, info *auth.AuthInfo, id string) {
	plan, err := planAgent(info, id)
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	listingEntity(c, info, plan)
}

func planAgent(info *auth.AuthInfo, id string) (listingPlan, error) {
	a, err := agentRepo.GetAgent(info, id)
	if err != nil {
		return listingPlan{}, err
	}
	if a == nil {
		return listingPlan{}, errors.New("Agent not found")
	}
	// 没有任何实体态闸门：超权身份 / 受保护名 / 废弃态都只给提示，不阻断上架或导出。
	plan := listingPlan{
		kind:     assetpack.KindAgent,
		name:     a.Name,
		sourceID: a.ID,
		entry:    "AGENT.md",
		srcDir:   paths.AgentsKey(info.Schema, a.Name),
		payload: map[string]any{
			"name":        a.Name,
			"description": string(a.Description),
			"config":      a.Config.Data,
			// 三个开关随内容一起走：它们决定这个智能体在租户里怎么跑，不带出去就会在
			// 目标租户装出一个行为不同的副本（包主无法预期）。三个都写明（含 false），
			// 老版本读不到字段时按「沿用目标既有值 / 新建为 false」处理，见 installAgent。
			"is_supervisor":      a.IsSupervisor,
			"tools_unrestricted": a.ToolsUnrestricted,
			"skill_discovery":    a.SkillDiscovery,
		},
	}
	if a.IsSupervisor {
		plan.warnings = append(plan.warnings,
			"该智能体是超级智能体：开关随包带走，导入后目标租户里的同名实体也是超级智能体（安装时需要导入方确认）")
	}
	if models.IsProtectedAgent(a.Name) {
		plan.warnings = append(plan.warnings,
			"这是系统内置智能体（受保护名）：受保护身份由名字决定，与包无关；包里带的是内容与开关")
	}
	if int(a.Status) == models.StatusDeprecated {
		plan.warnings = append(plan.warnings, "该智能体处于废弃态：包内是它当前的内容")
	}

	if a.ToolsUnrestricted || a.SkillDiscovery {
		plan.warnings = append(plan.warnings,
			"「不限制工具」「可发现未绑定技能」两个开关随包带走：目标租户里按包内设置生效")
	}

	// 私有技能整棵带走：文件在 agents/<agent>/skills/**（不 skipDirs），元数据进 payload，
	// 安装时按它重建 skills 行 —— 只有文件没有行，目标租户里就是一目录看不见的孤儿。
	privateSkills, perr := portablePrivateSkills(info.Schema, id)
	if perr != nil {
		return listingPlan{}, fmt.Errorf("read private skills: %w", perr)
	}
	privates := make([]string, 0, len(privateSkills))
	for _, ps := range privateSkills {
		privates = append(privates, asString(ps["name"]))
	}
	if len(privates) > 0 {
		plan.warnings = append(plan.warnings, fmt.Sprintf(
			"该智能体的 %d 个私有技能会随包带走（%s），装到目标租户后仍是它的私有技能",
			len(privates), strings.Join(privates, "、")))
	}
	plan.payload["private_skills"] = privateSkills

	bound, berr := skillRepo.ListBoundSkillNames(info.Schema, id)
	if berr != nil {
		return listingPlan{}, fmt.Errorf("read skill bindings: %w", berr)
	}

	grants, gerr := grantRepo.ListAgentGrants(info.Schema, id)
	if gerr != nil {
		return listingPlan{}, fmt.Errorf("read agent grants: %w", gerr)
	}
	deps := assetpack.Deps{
		Skills:        bound,
		PrivateSkills: privates,
		Tools:         make([]assetpack.ToolDep, 0, len(grants)),
	}
	for _, g := range grants {
		deps.Tools = append(deps.Tools, assetpack.ToolDep{
			Type: g.ItemType, Ref: g.Ref, GrantKind: g.GrantKind,
		})
	}
	// 「我能调用谁」也记进依赖：授权行不随包走（uuid 关系无法重映射），
	// 但装上之前要能核对「目标租户有没有那个智能体」—— 否则只有运行期 call_agent
	// 失败才发现（导入前的依赖检查就是拿这份清单去比对）。
	calls, cerr := relationRepo.ListCallableByCaller(info.Schema, id)
	if cerr != nil {
		return listingPlan{}, fmt.Errorf("read call relations: %w", cerr)
	}
	deps.Agents = make([]string, 0, len(calls))
	for _, c := range calls {
		deps.Agents = append(deps.Agents, c.Name)
	}
	if len(deps.Agents) > 0 {
		plan.warnings = append(plan.warnings, fmt.Sprintf(
			"该智能体可调用 %d 个其他智能体（%s）；调用授权本身不随包迁移，装完要在「调用授权」里重新配置",
			len(deps.Agents), strings.Join(deps.Agents, "、")))
	}
	plan.deps = deps
	if err := attachToolBundles(info, &plan); err != nil {
		return listingPlan{}, err
	}
	return plan, nil
}

// attachToolBundles 把「依赖的自定义工具本体」挂进包：文件进 trees，元数据进 payload。
//
// 智能体与技能共用：两者都可能依赖自定义工具，而工具目录不在它们自己的目录下，
// 不挂进来装到目标租户就只剩一条指向不存在工具的授权行。
func attachToolBundles(info *auth.AuthInfo, plan *listingPlan) error {
	trees, bundles, err := bundledToolTrees(info, plan.deps)
	if err != nil {
		return fmt.Errorf("read tool deps: %w", err)
	}
	plan.trees = trees
	plan.payload["tool_bundles"] = bundles
	if len(bundles) > 0 {
		names := make([]string, 0, len(bundles))
		for _, b := range bundles {
			names = append(names, asString(b["name"]))
		}
		plan.warnings = append(plan.warnings, fmt.Sprintf(
			"依赖的 %d 个自定义工具会随包带走（%s），装到目标租户后一并安装（启用态）",
			len(bundles), strings.Join(names, "、")))
	}
	return nil
}

// ===== 自定义工具 =====

// listingTool 上架一个脚本工具。
//
// 工具的定义真源是目录里的 TOOL.json（见 models.ToolFile 注释），所以这里以文件为准：
// 读契约 → 校验 → 整棵目录打包。payload 只带展示用的两三个字段，参数/语言/入口
// 全部由 TOOL.json 随文件一起走，装到目标租户后再从文件解析成行。
func listingTool(c *gin.Context, info *auth.AuthInfo, name string) {
	plan, err := planTool(info, name)
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	listingEntity(c, info, plan)
}

func planTool(info *auth.AuthInfo, name string) (listingPlan, error) {
	if err := assetpack.ValidateName(name); err != nil {
		return listingPlan{}, err
	}
	st, serr := getStorage()
	if serr != nil {
		return listingPlan{}, fmt.Errorf("storage not ready: %w", serr)
	}

	ctx := context.Background()
	dir := paths.ToolsKey(info.Schema) + "/" + name
	raw, rerr := st.ReadFile(ctx, path.Join(dir, models.ToolFileName))
	if rerr != nil {
		return listingPlan{}, fmt.Errorf("工具缺少契约文件 %s，无法上架", models.ToolFileName)
	}
	tf, perr := models.ParseToolFile(raw)
	if perr != nil {
		return listingPlan{}, fmt.Errorf("%s 解析失败: %w", models.ToolFileName, perr)
	}
	if err := tf.Validate(name); err != nil {
		return listingPlan{}, err
	}

	// 表行只提供 status 与 id（定义在文件里）；目录在、行缺失也算正常，状态按正常处理
	sourceID := uuid.NewString()
	status := models.StatusNormal
	if t, gerr := customtoolstore.New(config.Cfg.Agent.RootDir, info.Schema).Get("", name); gerr == nil && t != nil {
		sourceID = t.ID
		status = int(t.Status)
	} else if gerr != nil && !errors.Is(gerr, sql.ErrNoRows) {
		return listingPlan{}, fmt.Errorf("read tool row: %w", gerr)
	}
	// 没有任何实体态闸门：废弃态只给提示
	plan := listingPlan{
		kind:     assetpack.KindTool,
		name:     name,
		sourceID: sourceID,
		entry:    tf.Entry,
		srcDir:   dir,
		payload: map[string]any{
			"name":         name,
			"display_name": tf.DisplayName,
			"description":  tf.Description,
		},
		deps: assetpack.Deps{
			Requires: []string{"sandbox.exec"},
			Egress:   tf.Egress,
		},
	}
	if status == models.StatusDeprecated {
		plan.warnings = append(plan.warnings, "该工具处于废弃态：包内是它当前的内容")
	}
	if tf.Egress {
		plan.warnings = append(plan.warnings, "该工具声明了出网（egress）：目标租户需开通 net.egress 才能安装")
	}
	return plan, nil
}

// ===== 工作流 =====

// listingWorkflow 上架一个工作流。
//
// 工作流的本体在 {schema}.flows 的一行 JSONB 里（diagram + 变量 + 限额），盘上没有目录。
// 为了让「打包 → 审核 → 导出包 → 导入 → 安装」这条文件管线原样复用，这里先把整行序列化成
// FLOW.json 写进一个临时目录，交给 listingEntity 打包，收尾时删掉临时目录 ——
// 快照（platform-assets/…）才是唯一真源，临时目录不留在租户树里。
//
// 三样刻意不带出去（见 assetpack.WorkflowSnapshot 的注释）：id / status / endpoints，
// 以及 created_by。装上按「正常」态落地，等使用方核对依赖后再发布版本。
func listingWorkflow(c *gin.Context, info *auth.AuthInfo, id string) {
	plan, err := planWorkflow(info, id)
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	listingEntity(c, info, plan)
}

func planWorkflow(info *auth.AuthInfo, id string) (listingPlan, error) {
	f, err := flowRepo.GetFlow(info, id)
	if err != nil {
		return listingPlan{}, fmt.Errorf("read workflow: %w", err)
	}
	if f == nil {
		return listingPlan{}, errors.New("工作流不存在")
	}
	// 没有任何实体态闸门：废弃态只给提示
	plan := listingPlan{
		kind:     assetpack.KindWorkflow,
		name:     f.Name,
		sourceID: f.ID,
		entry:    assetpack.WorkflowFileName,
	}
	if int(f.Status) == models.StatusDeprecated {
		plan.warnings = append(plan.warnings, "该工作流处于废弃态：包内是它当前的内容")
	}

	def := map[string]any{
		"name":                f.Name,
		"description":         string(f.Description),
		"diagram":             f.Diagram.Data,
		"arguments":           f.Arguments.Data,
		"outputs":             f.Outputs.Data,
		"env_vars":            f.EnvVars.Data,
		"flow_vars":           f.FlowVars.Data,
		"timeout":             int(f.Timeout),
		"max_recursion_depth": int(f.MaxRecursionDepth),
		"max_node_count":      int(f.MaxNodeCount),
		"max_exec_count":      int(f.MaxExecCount),
		"max_run_records":     int(f.MaxRunRecords),
		"max_session_records": int(f.MaxSessionRecords),
	}
	// 定义整份进 FLOW.json，凭据也就整份扫一遍（env_vars 与节点配置里的 api_key 都在里面）
	clean, secretWarnings := assetpack.StripSecrets(assetpack.KindWorkflow, def)
	raw, merr := json.Marshal(clean)
	if merr != nil {
		return listingPlan{}, fmt.Errorf("encode workflow: %w", merr)
	}
	// 依赖清单在这里扫：工作流的 plan.payload 只有名字与描述，全局变量引用躺在
	// diagram 与 env_vars 里（见下面 plan.payload 的赋值）。不在这儿扫，
	// 工作流资产的 GlobalVars 会永远是空的，导入时也就不会提示缺哪个变量。
	// 用脱敏后的 clean 扫：引用名会保留，内联凭据已被清成空串。
	plan.deps.GlobalVars = assetpack.CollectGlobalVarRefs(context.Background(), nil, "", clean)

	st, serr := getStorage()
	if serr != nil {
		return listingPlan{}, fmt.Errorf("storage not ready: %w", serr)
	}
	ctx := context.Background()
	tmpDir := path.Join(paths.TenantsSubdir, info.Schema, ".listing-tmp", uuid.NewString())
	if werr := st.WriteFile(ctx, path.Join(tmpDir, assetpack.WorkflowFileName), raw); werr != nil {
		_ = st.DeletePrefix(context.Background(), tmpDir)
		return listingPlan{}, fmt.Errorf("写入工作流快照失败: %w", werr)
	}

	plan.warnings = append(plan.warnings,
		"工作流里的模型 / 工具 / 子流程引用不会随资产迁移，装到目标租户后要在编辑器里逐项重配",
		"装上后是编辑态，可直接修改；核对完引用再启用")
	plan.warnings = append(plan.warnings, secretWarnings...)
	plan.srcDir = tmpDir
	plan.payload = map[string]any{
		"name":        f.Name,
		"description": string(f.Description),
	}
	// 临时目录只为让工作流复用文件管线；快照 / 导出包才是真源，收尾必须删掉
	plan.cleanup = func() { _ = st.DeletePrefix(context.Background(), tmpDir) }
	return plan, nil
}
