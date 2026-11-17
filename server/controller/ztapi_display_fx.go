package controller

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

func GetZTAPIDisplayFX(c *gin.Context) {
	c.Header("Cache-Control", "public, max-age=60")
	common.ApiSuccess(c, service.CurrentZTAPIDisplayFX())
}
