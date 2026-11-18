package llmproviderpresets

import (
	"flame/nexus/shared/repository/llmproviderpresets"
	"flame/tools/xhttp"

	"github.com/gin-gonic/gin"
)

// 全局Repository实例
var repo = repository.NewRepository()

// ListLLMProviderPresets 获取所有LLM服务商预设列表
func ListLLMProviderPresets(c *gin.Context) {
	presets, err := repo.ListAll()
	if err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}

	xhttp.OkJSON(c, "Success", gin.H{
		"items": presets,
		"total": len(presets),
	}, "")
}
