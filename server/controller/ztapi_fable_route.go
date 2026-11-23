package controller

import (
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
)

var restoreZTAPIFableBackup = model.RestoreZTAPIFableYunxinBackup

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

func RestoreZTAPIFableBackup(c *gin.Context) {
	if !ztapiReductionRoot20261107(c) {
		return
	}
	var request struct {
		Confirm   bool   `json:"confirm"`
		Version   uint64 `json:"version"`
		Snapshot  int64  `json:"snapshot_id"`
		PrimaryID int64  `json:"primary_verification_id"`
		BackupID  int64  `json:"backup_verification_id"`
	}
	if c.ShouldBindJSON(&request) != nil || !request.Confirm || request.Version == 0 || request.Snapshot <= 0 ||
		request.PrimaryID <= 0 || request.BackupID <= 0 || request.PrimaryID == request.BackupID {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "请确认当前发布版本、快照及两条线路的最新验收编号。"})
		return
	}
	result, err := restoreZTAPIFableBackup(c.GetInt("id"), request.Version, request.Snapshot, request.PrimaryID, request.BackupID, requireProtectedPublicationCost(c))
	if err != nil {
		c.JSON(http.StatusConflict, gin.H{"success": false, "message": "Fable 备用线路未启用，请核对报价、线路和验收状态。"})
		return
	}
	common.ApiSuccess(c, gin.H{"snapshot_id": result.ID, "version": result.ModelVersion, "allowed_channel_ids": result.ChannelIDs(), "prices_unchanged": true})
}
