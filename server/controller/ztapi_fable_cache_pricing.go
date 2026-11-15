package controller

import (
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
)

var ztapiFableCachePublisher = model.PublishZTAPIFableUniformCacheWrite

func PublishZTAPIFableUniformCacheWrite(c *gin.Context) {
	var request struct {
		Confirm         bool   `json:"confirm"`
		ExpectedVersion uint64 `json:"expected_version"`
		Price           string `json:"cache_write_price_per_million"`
	}
	if err := c.ShouldBindJSON(&request); err != nil || !request.Confirm || request.ExpectedVersion == 0 || request.Price != model.ZTAPIFableUniformCacheWritePrice {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Confirmation, reviewed version and exact approved retail price required."})
		return
	}
	result, err := ztapiFableCachePublisher(c.GetInt("id"), request.ExpectedVersion)
	if err != nil {
		c.JSON(http.StatusConflict, gin.H{"success": false, "message": "No price update committed.", "detail": err.Error()})
		return
	}
	common.ApiSuccess(c, result)
}
