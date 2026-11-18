package flow

import (
	"github.com/gin-gonic/gin"
)

// RegisterRoutes 注册 /flow 元数据接口（外层已带 /flow 前缀）
func RegisterRoutes(router *gin.RouterGroup) {
	router.GET("/node-capabilities", GetNodeCapabilities)
	router.GET("/script-languages", GetCodeLanguages)
	router.GET("/limits", GetWorkflowLimits)
}
