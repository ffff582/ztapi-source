package router

import (
	"net/http"

	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

// MountZTAPIWorkbenchRoutes is an explicit server-side opt-in. It does not
// change or register against the public model endpoint.
func MountZTAPIWorkbenchRoutes(r gin.IRouter, taskLimitQuota int64) {
	if taskLimitQuota <= 0 {
		return
	}
	r.GET("/.well-known/ztapi-workbench", func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		c.JSON(http.StatusOK, gin.H{"protocol": "ztapi-workbench-first-party-v1", "budget": "wire-bound-v1", "dispatch": "enabled"})
	})
	mountWorkbenchBudgetRoutes(r, taskLimitQuota)
}

func mountWorkbenchBudgetRoutes(r gin.IRouter, taskLimitQuota int64, policy ...gin.HandlerFunc) {
	mountWorkbenchBudgetRoutesWithSelection(r, taskLimitQuota, policy, nil)
}

func mountWorkbenchBudgetRoutesWithSelection(r gin.IRouter, taskLimitQuota int64, policy []gin.HandlerFunc, selected gin.HandlerFunc) {
	group := r.Group("/__workbench_budget", append([]gin.HandlerFunc{middleware.WorkbenchTokenAuth()}, policy...)...)
	group.POST("/reserve", controller.ReserveZTAPIWorkbench(taskLimitQuota))
	group.POST("/release", controller.ReleaseZTAPIWorkbench)
	chain := []gin.HandlerFunc{func(c *gin.Context) {
		if c.Request.URL.RawQuery != "" || c.GetHeader("Content-Type") != "application/json" {
			c.AbortWithStatus(http.StatusBadRequest)
			return
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, service.ZTAPIWorkbenchMaxWire)
		c.Request.URL.Path = "/v1/responses"
		c.Request.RequestURI = "/v1/responses"
	}, middleware.Distribute()}
	if selected != nil {
		chain = append(chain, selected)
	}
	group.POST("/responses", append(chain, controller.RelayZTAPIWorkbench)...)
}

func mountWorkbenchMessagesRoutes(r gin.IRouter, limit int64, gate, selected gin.HandlerFunc) {
	group := r.Group("/__workbench_budget", middleware.WorkbenchTokenAuth(), gate)
	group.POST("/messages/reserve", controller.ReserveZTAPIWorkbenchMessages(limit))
	group.POST("/release", controller.ReleaseZTAPIWorkbench)
	group.POST("/messages", func(c *gin.Context) {
		if c.Request.URL.RawQuery != "" || c.Request.URL.ForceQuery || c.GetHeader("Content-Type") != "application/json" ||
			c.GetHeader("anthropic-beta") != "" {
			c.AbortWithStatus(http.StatusBadRequest)
			return
		}
		c.Request.Header.Set("anthropic-version", "2023-06-01")
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, service.ZTAPIWorkbenchMaxWire)
		c.Request.URL.Path = "/v1/messages"
		c.Request.RequestURI = "/v1/messages"
	}, middleware.Distribute(), selected, controller.RelayZTAPIWorkbenchMessages)
}
