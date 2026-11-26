package admin

import (
	"testing"

	"github.com/gin-gonic/gin"
)

// SetupRoutes must accept a static segment next to a `:id` parameter: POST /flows/validate lives beside
// POST /flows/:id/... and a conflict panics the whole router at startup, which no unit test would otherwise catch.
func TestFlowsValidateRouteRegistered(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	SetupRoutes(r.Group("/admin"))
	for _, rt := range r.Routes() {
		if rt.Method == "POST" && rt.Path == "/admin/flows/validate" {
			return
		}
	}
	t.Fatal("POST /admin/flows/validate was not registered")
}
