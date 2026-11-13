package controller

import (
	"errors"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type ztapiXcloudPublicationRequest20261114 struct {
	model.ZTAPIXcloudPublication20261114
	Confirm bool `json:"confirm"`
}

func ApplyZTAPIXcloudPublication20261114(c *gin.Context) {
	if !ztapiReductionRoot20261107(c) {
		return
	}
	var req ztapiXcloudPublicationRequest20261114
	if c.ShouldBindJSON(&req) != nil || !req.Confirm || req.Version == 0 || req.SourceID <= 0 || req.VerificationID <= 0 ||
		!model.IsZTAPIXcloudCohort20261114(req.SourceModel) {
		c.JSON(400, gin.H{"success": false, "message": "请确认 Xcloud 模型、原报价版本和验收编号。"})
		return
	}
	snapshot, err := model.ApplyZTAPIXcloudPublicationWithGate20261114(c.GetInt("id"), req.ZTAPIXcloudPublication20261114, func(tx *gorm.DB) error {
		input, blockers, err := model.ProductionZTAPIReferralReadiness(c.Request.Context(), tx, time.Now().UTC())
		if err != nil || len(blockers) != 0 || !input.PlatformFeeKnown {
			return errors.New("prospective catalog has no matching protected cost evidence")
		}
		gate, err := model.EvaluateZTAPIReferralPriceGate(input.PriceGate)
		if err != nil || !gate.Allowed {
			return errors.New("prospective catalog failed the referral price floor")
		}
		return nil
	})
	if err != nil {
		respondZTAPICashMarginError20261030(c, err)
		return
	}
	common.ApiSuccess(c, snapshot)
}

func RestoreZTAPIXcloudPublication20261114(c *gin.Context) {
	if !ztapiReductionRoot20261107(c) {
		return
	}
	var req ztapiXcloudPublicationRequest20261114
	if c.ShouldBindJSON(&req) != nil || !req.Confirm || req.Version == 0 || req.SnapshotID <= 0 ||
		!model.IsZTAPIXcloudCohort20261114(req.SourceModel) {
		c.JSON(400, gin.H{"success": false, "message": "请确认要恢复的 Xcloud 发布版本。"})
		return
	}
	if err := model.RestoreZTAPIXcloudPublication20261114(c.GetInt("id"), req.SourceModel, req.Version, req.SnapshotID); err != nil {
		respondZTAPICashMarginError20261030(c, err)
		return
	}
	common.ApiSuccess(c, true)
}

func CheckZTAPIXcloudDowngrade20261114(c *gin.Context) {
	if !ztapiReductionRoot20261107(c) {
		return
	}
	result, err := model.CheckZTAPIXcloudDowngrade20261114()
	if err != nil {
		c.JSON(503, gin.H{"success": false, "message": "Xcloud 发布回退检查暂不可用。"})
		return
	}
	if !result.CanDowngrade {
		c.JSON(409, gin.H{"success": false, "message": "请先恢复原发布并排空使用新报价的请求。", "data": result})
		return
	}
	common.ApiSuccess(c, result)
}
