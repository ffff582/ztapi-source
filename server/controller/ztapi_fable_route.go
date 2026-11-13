package controller

import (
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
)

func PublishZTAPIFableXcloudOnly(c *gin.Context) {
	var request struct {
		Version uint64 `json:"version"`
		Confirm bool   `json:"confirm"`
	}
	if c.ShouldBindJSON(&request) != nil || !request.Confirm || request.Version == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "请确认版本和仅保留 Xcloud 渠道。"})
		return
	}
	result, err := model.PublishZTAPIFableXcloudOnly(c.GetInt("id"), request.Version)
	if err != nil {
		c.JSON(http.StatusConflict, gin.H{"success": false, "message": "Fable 渠道收紧未完成，请刷新版本后重试。"})
		return
	}
	common.ApiSuccess(c, gin.H{"snapshot_id": result.ID, "version": result.ModelVersion, "allowed_channel_ids": result.ChannelIDs(), "prices_unchanged": true})
}
