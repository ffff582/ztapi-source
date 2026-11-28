package controller

import (
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

var ztapiXcloudStage20261128 = model.StageZTAPIXcloud20261128
var ztapiXcloudStageQuote20261128 = func() error {
	client := &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Get("https://www.xcloudtoken.com/api/pricing")
	if err != nil {
		return errors.New("Xcloud live quote unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return errors.New("Xcloud live quote rejected")
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, (2<<20)+1))
	if err != nil {
		return err
	}
	return model.ValidateZTAPIXcloudLiveQuote20261128(raw)
}
var ztapiXcloudStageDiscovery20261128 = func(source string, id int) ([]string, error) {
	channel, err := model.GetChannelById(id, true)
	if err != nil || !channel.ZTAPIManaged || !isZTAPISupportedChannelType(channel.Type) {
		return nil, errors.New("approved Xcloud channel is unavailable")
	}
	physical, err := service.ResolveZTAPIManagedUpstreamModel(channel, source)
	if err != nil || model.ValidateZTAPIXcloudRoute20261128(source, id, channel.GetBaseURL(), physical) != nil {
		return nil, errors.New("exact quoted Xcloud route is required")
	}
	return fetchZTAPIChannelUpstreamModelIDs(channel)
}

func StageZTAPIXcloud20261128(c *gin.Context) {
	if !ztapiReductionRoot20261107(c) {
		return
	}
	var req struct {
		model.ZTAPIXcloudStage20261128
		Confirm bool `json:"confirm"`
	}
	if c.ShouldBindJSON(&req) != nil || !req.Confirm || req.ChannelID <= 0 || req.SourceModel == "" || req.Group == "" {
		c.JSON(400, gin.H{"success": false, "message": "请确认型号、分组及原配置版本。"})
		return
	}
	if err := ztapiXcloudStageQuote20261128(); err != nil {
		c.JSON(409, gin.H{"success": false, "message": "Xcloud 当前报价与核验快照不一致或暂不可读，未改动配置。"})
		return
	}
	visible, err := ztapiXcloudStageDiscovery20261128(req.SourceModel, req.ChannelID)
	if err != nil {
		c.JSON(409, gin.H{"success": false, "message": "上游型号发现失败，未改动模型配置。"})
		return
	}
	result, err := ztapiXcloudStage20261128(c.GetInt("id"), req.ZTAPIXcloudStage20261128, visible, time.Now().UTC())
	if err != nil {
		respondZTAPICashMarginError20261030(c, err)
		return
	}
	common.ApiSuccess(c, result)
}

func ApplyZTAPIXcloudPublication20261128(c *gin.Context) {
	if !ztapiReductionRoot20261107(c) {
		return
	}
	var req struct {
		model.ZTAPIXcloudPublish20261128
		Confirm bool `json:"confirm"`
	}
	if c.ShouldBindJSON(&req) != nil || !req.Confirm || req.ConfigID <= 0 || req.Version == 0 || req.SourceID <= 0 || req.VerificationID <= 0 {
		c.JSON(400, gin.H{"success": false, "message": "请确认模型、报价和真实验收编号。"})
		return
	}
	result, err := model.ApplyZTAPIXcloudPublication20261128(c.GetInt("id"), req.ZTAPIXcloudPublish20261128, requireProtectedPublicationCost(c))
	if err != nil {
		respondZTAPICashMarginError20261030(c, err)
		return
	}
	common.ApiSuccess(c, result)
}

func RestoreZTAPIXcloudPublication20261128(c *gin.Context) {
	if !ztapiReductionRoot20261107(c) {
		return
	}
	var req struct {
		ConfigID   int    `json:"config_id"`
		Version    uint64 `json:"version"`
		SnapshotID int64  `json:"snapshot_id"`
		Confirm    bool   `json:"confirm"`
	}
	if c.ShouldBindJSON(&req) != nil || !req.Confirm || req.ConfigID <= 0 || req.Version == 0 || req.SnapshotID <= 0 {
		c.JSON(400, gin.H{"success": false, "message": "请确认要回滚的完整发布版本。"})
		return
	}
	if err := model.RestoreZTAPIXcloudPublication20261128(c.GetInt("id"), req.ConfigID, req.Version, req.SnapshotID, requireProtectedPublicationCost(c)); err != nil {
		respondZTAPICashMarginError20261030(c, err)
		return
	}
	common.ApiSuccess(c, true)
}
