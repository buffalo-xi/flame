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

// GetCodeLanguages returns the language dropdown of the **flow editor**: which languages a script node may use.
//
// The source of truth is the sandbox: only it knows which duplex languages the current mode has (sandboxd's script.pools,
// the same source as the unsupported_lang rejection on /v1/code). No language list is kept locally any more:
// the sandbox owns the choice, so adding or removing a language means editing script.pools in sandboxd.
//
// Without a sandbox capability snapshot (not running / not configured / an older sandboxd that does not
// report script_langs) the list is empty: the front end falls back to ["lua"] for an empty response (flows/Flow.vue
// only overrides when data.length > 0), so the editor stays usable instead of falling back to a stale local list.
func GetCodeLanguages(c *gin.Context) {
	if langs, ok := sandboxinfo.CodeLangs(); ok {
		xhttp.OkJSON(c, "Success", orderLangs(langs), "")
		return
	}
	xhttp.OkJSON(c, "Success", []string{}, "")
}

// langOrder is the display order of the dropdown: lua first, then alphabetical.
//
// Why not follow the sandbox order: the front end takes scriptLanguages[0] as the default language (flows/Flow.vue
// injects it into every node property panel as defaultLang), and lua is the one that **should** be the default -
// microsecond-level hot path, used most in branch and loop conditions. So the order is part of the API, not decoration.
var langOrder = []string{"lua", "python", "js"}

// orderLangs re-orders by langOrder and appends unknown languages alphabetically at the end
// (a language newly added by the sandbox must not be swallowed just because this list does not know it).
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

// GetWorkflowLimits returns the system-wide flow limits.
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
