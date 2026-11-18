package flow

import (
	"sort"

	"flame/nexus/service/flow/executors"
	"flame/nexus/shared/auth"
	"flame/nexus/shared/flowconfig"
	"flame/nexus/shared/sandboxinfo"
	"flame/tools/xhttp"

	"github.com/gin-gonic/gin"
)

func GetNodeCapabilities(c *gin.Context) {
	caps := executors.GetNodeCapabilities()
	xhttp.OkJSON(c, "Success", caps, "")
}

// GetCodeLanguages 返回**工作流编辑器**的语言下拉：脚本节点能选哪些语言。
//
// 真源是沙箱：只有它知道当前 mode 下配了哪些双工语言（sandboxd 的 script.pools，
// 与 /v1/code 拒语言的 unsupported_lang 同源）。本地不再保留任何语言名单 ——
// 语言选择完全由沙箱决定，想增删语言去 sandboxd 侧改 script.pools 即可。
//
// 拿不到沙箱能力快照（没起来 / 未配置 / 旧版本 sandboxd 不上报 script_langs）时
// 返回空列表：前端在空响应时保底成 ["lua"]（flows/Flow.vue 只在 data.length > 0 时
// 覆盖），保证编辑器仍可用，而不是退回一份可能已过时的本地名单。
func GetCodeLanguages(c *gin.Context) {
	if langs, ok := sandboxinfo.CodeLangs(); ok {
		xhttp.OkJSON(c, "Success", orderLangs(langs), "")
		return
	}
	xhttp.OkJSON(c, "Success", []string{}, "")
}

// langOrder 下拉的展示顺序：lua 打头，其余按字典序。
//
// 为什么不照搬沙箱的字典序：前端拿 scriptLanguages[0] 当缺省语言（flows/Flow.vue
// 注入给各节点属性面板的 defaultLang），而 lua 是那个**该**当缺省的语言 ——
// µs 级高频调用，分支/循环条件用得最多。所以这个顺序是接口的一部分，不是装饰。
var langOrder = []string{"lua", "python", "js"}

// orderLangs 按 langOrder 重排；不在 langOrder 里的语言按字典序追加在尾部
// （沙箱新增一种语言，不该因为这里不认识就被吞掉）。
func orderLangs(langs []string) []string {
	seen := make(map[string]bool, len(langs))
	out := make([]string, 0, len(langs))
	for _, want := range langOrder {
		for _, l := range langs {
			if l == want && !seen[l] {
				seen[l] = true
				out = append(out, l)
			}
		}
	}
	rest := make([]string, 0, len(langs))
	for _, l := range langs {
		if !seen[l] {
			rest = append(rest, l)
		}
	}
	sort.Strings(rest)
	return append(out, rest...)
}

// GetWorkflowLimits 获取工作流系统限制配置
func GetWorkflowLimits(c *gin.Context) {
	limits := gin.H{}
	if authInfo, exists := c.Get("authInfo"); exists {
		l := flowconfig.Resolve(authInfo.(*auth.AuthInfo).Schema)
		limits["max_timeout"] = l.MaxTimeout
		limits["max_recursion_depth"] = l.MaxRecursionDepth
		limits["max_node_count"] = l.MaxNodeCount
		limits["max_exec_count"] = l.MaxExecCount
		limits["max_loop_times"] = l.MaxLoopTimes
		limits["max_run_records"] = l.MaxRunRecords
		limits["max_session_records"] = l.MaxSessionRecords
	}
	xhttp.OkJSON(c, "Success", limits, "")
}
