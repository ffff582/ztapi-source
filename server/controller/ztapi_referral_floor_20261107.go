package controller

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
)

var ztapiReferralFloorPreview20261107 = model.PreviewZTAPIReferralFloor20261107
var ztapiReferralFloorApply20261107 = model.ApplyZTAPIReferralFloor20261107

func PreviewZTAPIReferralFloor20261107(c *gin.Context) {
	r, ok := bindZTAPIPriceReduction20261107(c, false, false)
	if !ok {
		return
	}
	result, err := ztapiReferralFloorPreview20261107(r.Models, r.Rollback)
	if err != nil {
		respondZTAPICashMarginError20261030(c, err)
		return
	}
	common.ApiSuccess(c, result)
}

func ApplyZTAPIReferralFloor20261107(c *gin.Context)   { applyZTAPIReferralFloor20261107(c, false) }
func RestoreZTAPIReferralFloor20261107(c *gin.Context) { applyZTAPIReferralFloor20261107(c, true) }

func applyZTAPIReferralFloor20261107(c *gin.Context, rollback bool) {
	r, ok := bindZTAPIPriceReduction20261107(c, true, rollback)
	if !ok {
		return
	}
	result, err := ztapiReferralFloorApply20261107(c.GetInt("id"), r.Models, rollback)
	if err != nil {
		respondZTAPICashMarginError20261030(c, err)
		return
	}
	common.ApiSuccess(c, result)
}

func CheckZTAPIReferralFloorDowngrade20261107(c *gin.Context) {
	if !ztapiReductionRoot20261107(c) {
		return
	}
	result, err := model.CheckZTAPIReferralFloorDowngrade20261107()
	if err != nil {
		c.JSON(503, gin.H{"success": false, "message": "推广定价兼容性检查暂不可用。"})
		return
	}
	if !result.CanDowngrade {
		c.JSON(409, gin.H{"success": false, "message": "请先恢复原价格并排空使用新报价的在途请求，再回退不兼容的后端版本。", "data": result})
		return
	}
	common.ApiSuccess(c, result)
}
