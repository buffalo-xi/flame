package agents

import (
	"sort"

	"flame/nexus/service/agent/tools"
	"flame/tools/xhttp"

	"github.com/gin-gonic/gin"
)

// ListBuiltinTools lists the configurable built-in tools (name / description),
// used by the tool configuration panel of the agent settings page to render the switches.
// Same rule as the run time: a tool explicitly set to false in config.tools is off, an unset one is on.
func ListBuiltinTools(c *gin.Context) {
	list := tools.List()
	items := make([]gin.H, 0, len(list))
	for _, t := range list {
		meta := t.Meta()
		category := meta.Category
		if category == "" {
			// No category: for panel grouping only ("other" is no longer an authorisable group, see toolgrant.BuiltinCategories).
			category = "other"
		}
		items = append(items, gin.H{
			"name":        meta.Name,
			"category":    category,
			"description": meta.Description,
		})
	}
	// tools.List() comes from a map, so the order is undefined: sort by category then name so the front end can group directly.
	sort.Slice(items, func(i, j int) bool {
		ci, cj := items[i]["category"].(string), items[j]["category"].(string)
		if ci != cj {
			return ci < cj
		}
		return items[i]["name"].(string) < items[j]["name"].(string)
	})

	xhttp.OkJSON(c, "Success", gin.H{"items": items}, "")
}
