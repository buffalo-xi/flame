package assetpack

import (
	"context"
	"regexp"
	"sort"
	"strings"

	"flame/nexus/shared/storage"
)

// 全局变量引用的写法，与运行期注入点一一对应：
//
//	config.inputs: {source_type: "global", source_key: NAME} —— 工作流（结构化，现行）
//	${global_vars.NAME}                                     —— 智能体 / 技能文档
//	{{.global_vars.NAME}}                                   —— 工作流的旧写法（已下线，仅用于识别残留）
//
// 三个地方共用这份正则（这里 / secrets.go 的保留判定 / 需求清单扫描），
// 合成一处是为了避免「哪天改了语法，只改了一边」。
var (
	refTemplateRe = regexp.MustCompile(`\{\{-?\s*\.global_vars\.([A-Za-z_][A-Za-z0-9_]*)`)
	refDocRe      = regexp.MustCompile(`\$\{global_vars\.([A-Za-z_][A-Za-z0-9_]*)`)
	// refInputRe 节点输入引用 {{ .input.NAME }}：值不在包里（来自输入变量的来源），
	// 脱敏时要当作引用保留，理由同 global_vars 引用。
	refInputRe = regexp.MustCompile(`\{\{-?\s*\.input\.([A-Za-z_][A-Za-z0-9_]*)`)
	// globalRefExactRe 整个字符串就是一个引用（不是 "Bearer xxx" 这种混合形态）。
	globalRefExactRe = regexp.MustCompile(
		`^(?:\{\{-?\s*\.global_vars\.[A-Za-z_][A-Za-z0-9_]*\s*\}\}` +
			`|\$\{global_vars\.[A-Za-z_][A-Za-z0-9_]*\}` +
			`|\{\{-?\s*\.input\.[A-Za-z_][A-Za-z0-9_]*\s*\}\})$`)
)

// GlobalVarRefsInText 抓一段文本里的全局变量引用名（去重、升序）。
func GlobalVarRefsInText(text string) []string {
	if text == "" {
		return nil
	}
	out := make([]string, 0, 2)
	for _, m := range refTemplateRe.FindAllStringSubmatch(text, -1) {
		out = append(out, m[1])
	}
	for _, m := range refDocRe.FindAllStringSubmatch(text, -1) {
		out = append(out, m[1])
	}
	return out
}

// GlobalVarRefsInValue 递归扫 JSON 形态的值（map / []any / string）。
//
// 打包时拿它扫 payload：工作流的 env_vars 与 diagram 都在这里面。
func GlobalVarRefsInValue(v any) []string {
	switch val := v.(type) {
	case string:
		return GlobalVarRefsInText(val)
	case map[string]any:
		var out []string
		for _, item := range val {
			out = append(out, GlobalVarRefsInValue(item)...)
		}
		return out
	case []any:
		var out []string
		for _, item := range val {
			out = append(out, GlobalVarRefsInValue(item)...)
		}
		return out
	default:
		return nil
	}
}

// GlobalVarRefsInNodeInputs 读节点输入变量里的全局变量来源：
// {source_type: "global", source_key: "引用名"}。
//
// 这条不能只靠正则扫 payload —— 那种写法里根本没有 {{ }} / ${}，只有 type + 名字。
// 按 key 名 "inputs" 递归找：工作流在 diagram.nodes[].data.config.inputs，
// 其余节点级配置结构相同，扫到就算，多算一个依赖只是预检里多一条提示。
func GlobalVarRefsInNodeInputs(v any) []string {
	var out []string
	var walk func(any)
	walk = func(val any) {
		switch item := val.(type) {
		case map[string]any:
			if inputs, ok := item["inputs"].(map[string]any); ok {
				out = append(out, globalRefsOfInputs(inputs)...)
			}
			for _, sub := range item {
				walk(sub)
			}
		case []any:
			for _, sub := range item {
				walk(sub)
			}
		}
	}
	walk(v)
	return out
}

// globalRefsOfInputs 从一个 inputs 表里挑出「来源是全局变量」的名字。
func globalRefsOfInputs(inputs map[string]any) []string {
	var out []string
	for _, raw := range inputs {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if sourceType, _ := item["source_type"].(string); sourceType != "global" {
			continue
		}
		if name, _ := item["source_key"].(string); strings.TrimSpace(name) != "" {
			out = append(out, strings.TrimSpace(name))
		}
	}
	return out
}

// CollectGlobalVarRefs 汇总一份资产对全局变量的依赖。
//
// 三个来源都扫：
//  1. 节点输入变量的结构化引用（source_type: "global"）—— 工作流的现行写法；
//  2. payload 文本里的引用（文档写法与工作流的旧写法，主要是兼容存量包）；
//  3. 定义域文档（*.md，AGENT.md / SKILL.md）里的 ${global_vars.X}。
//
// 只扫文档不扫脚本：脚本走沙箱执行，运行期不做文本替换 —— 扫进去会得到
// 「清单说要 X，实际运行时根本不读」的假依赖。
//
// st / dir 为空时跳过第 3 步（工作流没有定义域目录）。读不到的目录不报错：
// 依赖清单是「尽力而为」的核对信息，不该让打包失败。
func CollectGlobalVarRefs(ctx context.Context, st storage.Storage, dir string,
	payload map[string]any) []string {

	// payload 里的 jsonb 列在生产形态下是原始字节（json.RawMessage）。
	// 先归一成 map / []any 再扫：不归一这两步会静默扫出空清单，
	// 导入时就永远不会提示「目标租户缺哪个变量」。
	norm := deepCopyMap(payload)
	all := make([]string, 0, 4)
	all = append(all, GlobalVarRefsInNodeInputs(norm)...)
	all = append(all, GlobalVarRefsInValue(norm)...)
	if st != nil && dir != "" {
		all = append(all, collectDocRefs(ctx, st, dir, 0)...)
	}

	seen := make(map[string]struct{}, len(all))
	out := make([]string, 0, len(all))
	for _, name := range all {
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// 文档扫描的边界：定义域目录是用户自由编辑的，深度与文件数都必须设上限，
// 否则一个深目录能让上架卡住。
const (
	docScanMaxDepth = 3
	docScanMaxFiles = 64
)

func collectDocRefs(ctx context.Context, st storage.Storage, dir string, depth int) []string {
	if depth > docScanMaxDepth {
		return nil
	}
	entries, err := st.List(ctx, dir)
	if err != nil {
		return nil
	}

	out := make([]string, 0, 2)
	files := 0
	for _, e := range entries {
		if files >= docScanMaxFiles {
			break
		}
		child := dir + "/" + e.Name
		if e.IsDir {
			out = append(out, collectDocRefs(ctx, st, child, depth+1)...)
			continue
		}
		if !strings.HasSuffix(strings.ToLower(e.Name), ".md") {
			continue
		}
		content, rerr := st.ReadFile(ctx, child)
		if rerr != nil {
			continue
		}
		files++
		out = append(out, GlobalVarRefsInText(string(content))...)
	}
	return out
}
