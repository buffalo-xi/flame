package versioning

// 版本状态（非 DB 列）：当前版本号 + 草稿是否有未发布改动。
//
// flow 的定义在数据库列里，比对可以直接写 SQL（见 repository/flows/versions.go 的
// fillVersionState）。agent / skill / tool 的定义是「列 + 目录」两半：
//   - 元数据（描述 / 可见性 / 参数 schema / 工具依赖）在版本行里，用 SQL IS DISTINCT FROM 比；
//   - 正文是目录，只能对着版本清单逐个文件比 sha256。
//
// 只比文件不比元数据，就会出现「改了描述、界面说已发布、线上还是旧的」——
// 这正是版本体系要防的那类谎话。所以两半都算。
//
// 读不到版本信息时返回 (0,false)：与 flow 同口径，不让详情页因为版本查询失败而报错。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path"
	"reflect"
	"strings"

	"flame/nexus/shared/assetpack"
	"flame/nexus/shared/db"
	"flame/nexus/shared/models"
	"flame/nexus/shared/paths"
	"flame/nexus/shared/storage"
	"flame/nexus/shared/versions"
)

// SkillVersionState 租户级技能的版本状态。
//
// 私有技能没有独立版本号（它的版本就是所属 agent 的版本）→ 恒返回 (0,false)，
// 前端据此显示「随所属智能体版本」，而不是「未发布」。
func SkillVersionState(ctx context.Context, st storage.Storage, schema string, s *models.Skill) (int, bool) {
	if string(s.AgentID) != "" {
		return 0, false
	}
	var (
		version     int
		rawFiles    []byte
		rawDeps     []byte
		metaChanged bool
	)
	err := db.DB.QueryRow(`SELECT v.version, COALESCE(v.files,'[]'::jsonb), COALESCE(v.tool_deps,'[]'::jsonb),
			(v.description IS DISTINCT FROM s.description
			 OR v.metadata::text IS DISTINCT FROM s.metadata::text
			 OR v.user_invocable IS DISTINCT FROM s.user_invocable
			 OR v.command_dispatch IS DISTINCT FROM s.command_dispatch
			 OR v.command_tool IS DISTINCT FROM s.command_tool
			 OR v.restricted IS DISTINCT FROM s.restricted)
		FROM `+schema+`.skill_versions v
		JOIN `+schema+`.skills s ON s.id = v.skill_id
		WHERE v.skill_id = $1 AND v.is_current`, s.ID).
		Scan(&version, &rawFiles, &rawDeps, &metaChanged)
	if err != nil {
		return 0, false
	}
	if metaChanged {
		return version, true
	}

	// 工具依赖：声明属于定义。与发布时同一个查询（ORDER BY ref），解成切片再比 ——
	// 直接比 JSON 字节会被键序 / 空白影响。
	curDeps, derr := SkillToolDepsSnapshot(schema, s.ID)
	if derr != nil {
		return version, true
	}
	if !sameDeps(rawDeps, curDeps) {
		return version, true
	}

	files, ferr := versions.Decode(rawFiles)
	if ferr != nil {
		return version, true
	}
	if st == nil {
		return version, false
	}
	changed, cerr := DraftChanged(ctx, st, path.Join(paths.SkillsKey(schema), s.Name), files, nil)
	if cerr != nil {
		return version, true
	}
	return version, changed
}

// ToolVersionState 自定义工具的版本状态（目录即实体，没有改名，只按 name 键）。
//
// configs 不比：它是运行时事实（key / 开关），不是定义；发布时本来也存成 '{}'。
func ToolVersionState(ctx context.Context, st storage.Storage, schema, toolName string) (int, bool) {
	var (
		version     int
		rawFiles    []byte
		metaChanged bool
	)
	err := db.DB.QueryRow(`SELECT v.version, COALESCE(v.files,'[]'::jsonb),
			(v.display_name IS DISTINCT FROM t.display_name
			 OR v.description IS DISTINCT FROM t.description
			 OR v.parameters::text IS DISTINCT FROM t.parameters::text
			 OR v.output::text IS DISTINCT FROM t.output::text
			 OR v.lang IS DISTINCT FROM t.lang
			 OR v.entry IS DISTINCT FROM t.entry
			 OR v.timeout_ms IS DISTINCT FROM t.timeout_ms
			 OR v.max_output_bytes IS DISTINCT FROM t.max_output_bytes
			 OR v.egress IS DISTINCT FROM t.egress
			 OR v.config_fields::text IS DISTINCT FROM t.config_fields::text)
		FROM `+schema+`.tool_versions v
		JOIN `+schema+`.custom_tools t ON t.id = v.tool_id
		WHERE t.name = $1 AND v.is_current`, toolName).
		Scan(&version, &rawFiles, &metaChanged)
	if err != nil {
		return 0, false
	}
	if metaChanged {
		return version, true
	}
	files, derr := versions.Decode(rawFiles)
	if derr != nil {
		return version, true
	}
	if st == nil {
		return version, false
	}
	changed, cerr := DraftChanged(ctx, st, path.Join(paths.ToolsKey(schema), toolName), files, nil)
	if cerr != nil {
		return version, true
	}
	return version, changed
}

// AgentVersionState 智能体的版本状态。
//
// 三段都要比，否则会漏：
//  1. agents 的定义列（描述 / config / 两个开关）；
//  2. 私有技能的**元数据**（描述 / 可见性 / 工具依赖）—— 它在版本行 skills 列里；
//  3. 正文目录（AGENT.md + 私有技能文件），排除已废弃的私有技能（发布时也排除）。
func AgentVersionState(ctx context.Context, st storage.Storage, schema, agentID, agentName string) (int, bool) {
	var (
		version     int
		rawFiles    []byte
		rawSkills   []byte
		metaChanged bool
	)
	err := db.DB.QueryRow(`SELECT v.version, COALESCE(v.files,'[]'::jsonb), COALESCE(v.skills,'[]'::jsonb),
			(v.description IS DISTINCT FROM a.description
			 OR v.config::text IS DISTINCT FROM a.config::text
			 OR v.tools_unrestricted IS DISTINCT FROM a.tools_unrestricted
			 OR v.skill_discovery IS DISTINCT FROM a.skill_discovery)
		FROM `+schema+`.agent_versions v
		JOIN `+schema+`.agents a ON a.id = v.agent_id
		WHERE v.agent_id = $1 AND v.is_current`, agentID).
		Scan(&version, &rawFiles, &rawSkills, &metaChanged)
	if err != nil {
		return 0, false
	}
	if metaChanged {
		return version, true
	}

	// 私有技能元数据：与发布时同一个查询、同样 ORDER BY name，逐字段比较
	now, serr := PrivateSkillsSnapshot(schema, agentID)
	if serr != nil || !sameSkillSnapshot(rawSkills, now) {
		return version, true
	}

	files, derr := versions.Decode(rawFiles)
	if derr != nil {
		return version, true
	}
	if st == nil {
		return version, false
	}
	// 与发布时同一套排除规则：已废弃的私有技能不进快照，比对时也不能把它们算成「有改动」
	skip, kerr := DeprecatedPrivateSkillDirs(schema, agentID)
	if kerr != nil {
		return version, true
	}
	changed, cerr := DraftChanged(ctx, st, paths.AgentsKey(schema, agentName), files, skip)
	if cerr != nil {
		return version, true
	}
	return version, changed
}

// sameSkillSnapshot 比较两份私有技能元数据快照。
// 都解成 []SkillMeta 再 DeepEqual：JSON 的键序不稳定，直接比字节会误报。
func sameSkillSnapshot(a, b []byte) bool {
	var sa, sb []SkillMeta
	if len(a) > 0 {
		if err := json.Unmarshal(a, &sa); err != nil {
			return false
		}
	}
	if len(b) > 0 {
		if err := json.Unmarshal(b, &sb); err != nil {
			return false
		}
	}
	if len(sa) == 0 && len(sb) == 0 {
		return true
	}
	return reflect.DeepEqual(sa, sb)
}

// sameDeps 比较两份工具依赖快照（都来自同一查询，顺序稳定）。
func sameDeps(a, b []byte) bool {
	var da, db2 []toolDep
	if len(a) > 0 {
		if err := json.Unmarshal(a, &da); err != nil {
			return false
		}
	}
	if len(b) > 0 {
		if err := json.Unmarshal(b, &db2); err != nil {
			return false
		}
	}
	if len(da) == 0 && len(db2) == 0 {
		return true
	}
	return reflect.DeepEqual(da, db2)
}

// DraftChanged 报告草稿目录与已冻结清单是否不一致：
// 多文件、少文件、任一文件内容（sha256）不同都算改动。
//
// skipDirs 是相对草稿目录的路径（如 "skills/old"），与发布时排除的目录同口径；
// 这些前缀下的内容既不算多文件、也不参与比较。
func DraftChanged(ctx context.Context, st storage.Storage, draftDir string, files []assetpack.FileEntry, skipDirs []string) (bool, error) {
	if st == nil {
		return false, nil
	}
	want := make(map[string]string, len(files))
	for _, f := range files {
		want[strings.TrimPrefix(f.Path, "/")] = f.SHA256
	}
	skip := func(rel string) bool {
		for _, d := range skipDirs {
			if rel == d || strings.HasPrefix(rel, d+"/") {
				return true
			}
		}
		return false
	}

	changed := false
	seen := make(map[string]bool, len(want))
	var walk func(dir, rel string) error
	walk = func(dir, rel string) error {
		entries, err := st.List(ctx, dir)
		if err != nil {
			return err
		}
		for _, e := range entries {
			childRel := e.Name
			if rel != "" {
				childRel = path.Join(rel, e.Name)
			}
			if skip(childRel) {
				continue
			}
			if e.IsDir {
				if err := walk(path.Join(dir, e.Name), childRel); err != nil {
					return err
				}
				continue
			}
			seen[childRel] = true
			sum, ok := want[childRel]
			if !ok {
				changed = true // 版本里没有这个文件
				continue
			}
			data, rerr := st.ReadFile(ctx, path.Join(dir, e.Name))
			if rerr != nil {
				return rerr
			}
			h := sha256.Sum256(data)
			if hex.EncodeToString(h[:]) != sum {
				changed = true
			}
		}
		return nil
	}
	if err := walk(draftDir, ""); err != nil {
		return false, err
	}
	if len(want) != len(seen) {
		changed = true // 版本里有、草稿里没有
	}
	return changed, nil
}
