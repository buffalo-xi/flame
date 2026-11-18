package agents

import (
	"sort"

	"flame/nexus/service/agent/tools"
	"flame/tools/xhttp"

	"github.com/gin-gonic/gin"
)

// ListBuiltinTools 列出可配置的内置工具（name/description），
// 供智能体设置页的「工具配置」渲染开关列表。
// 与运行时一致：config.tools 中显式 false 的工具被关闭，未配置的默认启用。
func ListBuiltinTools(c *gin.Context) {
	list := tools.List()
	items := make([]gin.H, 0, len(list))
	for _, t := range list {
		meta := t.Meta()
		category := meta.Category
		if category == "" {
			// 未标注分类：仅作面板分组展示用（"other" 已不是可授权的类别组，见 toolgrant.BuiltinCategories）
			category = "other"
		}
		items = append(items, gin.H{
			"name":        meta.Name,
			"category":    category,
			"description": meta.Description,
		})
	}
	// tools.List() 来自 map，顺序不固定：按 分类 → 名字 排序，前端可直接分组渲染
	sort.Slice(items, func(i, j int) bool {
		ci, cj := items[i]["category"].(string), items[j]["category"].(string)
		if ci != cj {
			return ci < cj
		}
		return items[i]["name"].(string) < items[j]["name"].(string)
	})

	xhttp.OkJSON(c, "Success", gin.H{"items": items}, "")
}
