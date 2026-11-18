package globalvars

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"strings"
	"sync"
	"time"

	"flame/nexus/shared/assetpack"
	"flame/nexus/shared/config"
	"flame/nexus/shared/db"
	"flame/nexus/shared/paths"
	"flame/nexus/shared/storage"
	"flame/tools/xhttp"

	"github.com/gin-gonic/gin"
)

// ============================================================================
// 引用面检查：删掉一个全局变量之前，先扫出还有哪些地方在引用它。
//
// 为什么值得单独扫一遍：变量被删后，引用它的节点/文档要到运行期才失败，
// 而那可能是几天后的一次定时任务。删的那一刻是唯一能把「你会坏掉什么」
// 摆在人面前的时机。
//
// 扫的两半：
//   1) 库里的结构化引用 —— flows.diagram / flows.env_vars / agents.config / skills.metadata；
//   2) 磁盘上的文档引用 —— AGENT.md / SKILL.md 里的 ${global_vars.X}。
// 只扫库会得出「没人用，放心删」的假结论，因为文档引用根本不在库里。
//
// 文档与智能体配置按子串 "global_vars.<名字>" 扫：${...} 与 {{.global_vars.X}} 都含这一段，
// 不必分别写正则。工作流不能这么扫 —— 它的现行写法（节点输入的 global 来源）里没有这段子串，
// 必须解析 diagram 看 inputs，否则会得出「没人用，放心删」的假结论。
// ============================================================================

// UsageHit 一处引用。
type UsageHit struct {
	Kind string `json:"kind"` // flow / agent / skill / agent_doc / skill_doc
	ID   string `json:"id,omitempty"`
	Name string `json:"name"`
}

// maxDocScanBytes 单个文档只读这么多。文档是给人看的说明，不是数据文件；
// 超过这个量的既不该是 .md，也不值得为一次删除检查全读进内存。
const maxDocScanBytes = 1 << 20

var (
	usageStoreOnce sync.Once
	usageStore     storage.Storage
	usageStoreErr  error
)

func usageStorage() (storage.Storage, error) {
	usageStoreOnce.Do(func() {
		usageStore, usageStoreErr = storage.New(config.Cfg.Agent.Storage, config.Cfg.Agent.RootDir)
	})
	return usageStore, usageStoreErr
}

// ScanUsage 扫出引用某全局变量的地方。
//
// 出错就整体失败（不返回「扫到一半」的结果）：调用方要靠它决定「能不能删」，
// 一次不完整的扫描给出的「没有引用」比不扫更危险。
func ScanUsage(schema, name string) ([]UsageHit, error) {
	if schema == "" || name == "" {
		return nil, fmt.Errorf("缺少租户或变量名")
	}
	needle := "global_vars." + name

	hits := make([]UsageHit, 0, 4)

	// 1) 工作流：节点输入变量的 global 来源（结构化）与文本里的旧写法。
	//    全局变量改成输入来源之后，库里已经不含 "global_vars.<名字>" 这段子串，
	//    只按子串扫会得出「没人用，放心删」的假结论 —— 必须解析 diagram 看 inputs。
	flowHits, err := scanFlows(schema, name, needle)
	if err != nil {
		return nil, fmt.Errorf("扫描工作流失败: %w", err)
	}
	hits = append(hits, flowHits...)

	// 2) 智能体：config 里可能有 agent 级的工具/模型配置
	agentHits, err := scanRows(schema, "agent", `SELECT id::text, name FROM `+schema+`.agents
		WHERE strpos(COALESCE(config::text, ''), $1) > 0`, needle)
	if err != nil {
		return nil, fmt.Errorf("扫描智能体失败: %w", err)
	}
	hits = append(hits, agentHits...)

	// 3) 技能：描述与元数据
	skillHits, err := scanRows(schema, "skill", `SELECT id::text, name FROM `+schema+`.skills
		WHERE strpos(COALESCE(metadata::text, ''), $1) > 0
		   OR strpos(COALESCE(description, ''), $1) > 0`, needle)
	if err != nil {
		return nil, fmt.Errorf("扫描技能失败: %w", err)
	}
	hits = append(hits, skillHits...)

	// 4) 文档：AGENT.md / SKILL.md（含 agent 私有技能）
	docHits, err := scanDocs(schema, needle)
	if err != nil {
		return nil, err
	}
	hits = append(hits, docHits...)

	return hits, nil
}

// scanFlows 扫工作流：输入变量的 global 来源（现行写法）+ 文本里的旧写法。
//
// 先用 SQL 粗筛（含 "global" 或含旧写法子串的行），再在 Go 侧解析 diagram 认真判：
// 把全部 diagram 拉出来解析的代价随流程数线性增长，粗筛能挡掉绝大多数行。
func scanFlows(schema, name, needle string) ([]UsageHit, error) {
	rows, err := db.DB.Query(`SELECT id::text, name, COALESCE(diagram::text, '') FROM `+schema+`.flows
		WHERE strpos(COALESCE(diagram::text, ''), '"global"') > 0
		   OR strpos(COALESCE(diagram::text, ''), $1) > 0`, needle)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []UsageHit
	for rows.Next() {
		var id, flowName, diagram string
		if err := rows.Scan(&id, &flowName, &diagram); err != nil {
			return nil, err
		}
		if flowRefsGlobalVar(diagram, name) {
			out = append(out, UsageHit{Kind: "flow", ID: id, Name: flowName})
		}
	}
	return out, rows.Err()
}

// flowRefsGlobalVar 判断一份 diagram 是否引用了名为 name 的全局变量。
//
// 两条判定：输入变量的 global 来源（现行），以及文本里残留的旧写法
// {{.global_vars.NAME}} —— 旧写法已经跑不起来了，但删掉变量同样会让它更坏。
func flowRefsGlobalVar(diagram, name string) bool {
	if diagram == "" {
		return false
	}
	if strings.Contains(diagram, "global_vars."+name) {
		return true
	}
	if !strings.Contains(diagram, `"global"`) {
		return false
	}
	var root any
	if err := json.Unmarshal([]byte(diagram), &root); err != nil {
		return false
	}
	for _, ref := range assetpack.GlobalVarRefsInNodeInputs(root) {
		if ref == name {
			return true
		}
	}
	return false
}

func scanRows(schema, kind, query, needle string) ([]UsageHit, error) {
	rows, err := db.DB.Query(query, needle)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []UsageHit
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		out = append(out, UsageHit{Kind: kind, ID: id, Name: name})
	}
	return out, rows.Err()
}

// scanDocs 走存储抽象扫租户定义域里的 .md。
//
// 只扫 agents/ 与 skills/ 两棵树：versions/ 下是历史快照，引用它不等于「现在还在用」，
// 把它算进来会让删除提示永远带着一堆早就发布过的旧副本。
func scanDocs(schema, needle string) ([]UsageHit, error) {
	st, err := usageStorage()
	if err != nil {
		return nil, fmt.Errorf("打开存储失败，文档引用未纳入检查: %w", err)
	}
	walker, ok := st.(storage.Walker)
	if !ok {
		return nil, fmt.Errorf("当前存储后端不支持遍历，文档里的引用未纳入检查")
	}

	ctx := context.Background()
	dirs := []string{
		path.Join(paths.TenantsSubdir, schema, paths.AgentsSubdir),
		paths.SkillsKey(schema),
	}

	var out []UsageHit
	for _, dir := range dirs {
		werr := walker.WalkPrefix(ctx, dir, func(rel string, size int64, _ time.Time) error {
			if size <= 0 || size > maxDocScanBytes || !strings.HasSuffix(strings.ToLower(rel), ".md") {
				return nil
			}
			data, rerr := st.ReadFile(ctx, rel)
			if rerr != nil {
				return nil // 遍历到读之间被删掉了：跳过，别让一次删除检查因此失败
			}
			if strings.Contains(string(data), needle) {
				out = append(out, UsageHit{Kind: docKind(rel), Name: rel})
			}
			return nil
		})
		if werr != nil {
			return nil, fmt.Errorf("扫描文档失败: %w", werr)
		}
	}
	return out, nil
}

func docKind(rel string) string {
	if strings.HasSuffix(strings.ToLower(rel), strings.ToLower(paths.AgentEntryFile)) {
		return "agent_doc"
	}
	return "skill_doc"
}

// GlobalVarUsage 查询某个全局变量的引用面（删除前的确认框用它）。
func GlobalVarUsage(c *gin.Context) {
	info, ok := authInfo(c)
	if !ok {
		return
	}
	name := strings.TrimSpace(c.Param("name"))
	if name == "" {
		xhttp.ErrJSON(c, "变量名不能为空", nil, "")
		return
	}

	hits, err := ScanUsage(info.Schema, name)
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"items": hits, "total": len(hits)}, "")
}

// describeHits 把引用面压成一句话：变量名 + 前几处位置，避免错误信息过长。
func describeHits(hits []UsageHit) string {
	const limit = 5
	parts := make([]string, 0, limit)
	for i, h := range hits {
		if i >= limit {
			parts = append(parts, fmt.Sprintf("…等 %d 处", len(hits)))
			break
		}
		parts = append(parts, h.KindLabel()+h.Name)
	}
	return strings.Join(parts, "、")
}

// KindLabel 引用类型的中文说法（出错信息里直接用）。
func (h UsageHit) KindLabel() string {
	switch h.Kind {
	case "flow":
		return "工作流 "
	case "agent":
		return "智能体 "
	case "skill":
		return "技能 "
	case "agent_doc":
		return "文档 "
	case "skill_doc":
		return "文档 "
	default:
		return ""
	}
}
