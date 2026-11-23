package router

import (
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/middleware"

	"github.com/gin-gonic/gin"
)

func SetRouter(router *gin.Engine, assets ThemeAssets) {
	SetApiRouter(router)
	SetDashboardRouter(router)
	SetRelayRouter(router)
	SetVideoRouter(router)
	mountConfiguredWorkbenchPilot(router)
	frontendBaseUrl := os.Getenv("FRONTEND_BASE_URL")
	if common.IsMasterNode && frontendBaseUrl != "" {
		frontendBaseUrl = ""
		common.SysLog("FRONTEND_BASE_URL is ignored on master node")
	}
	if frontendBaseUrl == "" {
		SetWebRouter(router, assets)
	} else {
		frontendBaseUrl = strings.TrimSuffix(frontendBaseUrl, "/")
		router.NoRoute(func(c *gin.Context) {
			c.Set(middleware.RouteTagKey, "web")
			if rejectWorkbenchFallback(c) {
				return
			}
			c.Redirect(http.StatusMovedPermanently, fmt.Sprintf("%s%s", frontendBaseUrl, c.Request.RequestURI))
		})
	}
}

func rejectWorkbenchFallback(c *gin.Context) bool {
	path := c.Request.URL.Path
	if path != "/.well-known/ztapi-workbench" && path != "/.well-known/ztapi-workbench/" &&
		path != "/api/workbench" && !strings.HasPrefix(path, "/api/workbench/") &&
		path != "/__workbench_budget" && !strings.HasPrefix(path, "/__workbench_budget/") {
		return false
	}
	c.Header("Cache-Control", "no-store")
	controller.RelayNotFound(c)
	return true
}
