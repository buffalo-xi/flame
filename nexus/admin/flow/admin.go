package flow

import (
	"github.com/gin-gonic/gin"
)

// RegisterRoutes wires the /flow metadata endpoints (the outer router already adds the /flow prefix).
func RegisterRoutes(router *gin.RouterGroup) {
	router.GET("/node-capabilities", GetNodeCapabilities)
	router.GET("/script-languages", GetCodeLanguages)
	router.GET("/limits", GetWorkflowLimits)
}
