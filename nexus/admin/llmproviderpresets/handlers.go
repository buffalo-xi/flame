package llmproviderpresets

import (
	"flame/nexus/shared/repository/llmproviderpresets"
	"flame/tools/xhttp"

	"github.com/gin-gonic/gin"
)

// Package-level repository instance.
var repo = repository.NewRepository()

// ListLLMProviderPresets returns every provider preset.
func ListLLMProviderPresets(c *gin.Context) {
	presets, err := repo.ListAll()
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}

	xhttp.OkJSON(c, "Success", gin.H{
		"items": presets,
		"total": len(presets),
	}, "")
}
