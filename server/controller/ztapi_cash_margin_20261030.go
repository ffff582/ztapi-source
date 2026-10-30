package controller

import (
	"errors"
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

var ztapiCashMarginPreview20261030 = model.PreviewZTAPICashMargin20261030
var ztapiCashMarginApply20261030 = model.ApplyZTAPICashMargin20261030
var ztapiCashMarginDowngrade20261030 = model.CheckZTAPICashMarginDowngrade20261030

type ztapiCashMarginRequest20261030 struct {
	Confirm  bool                                     `json:"confirm"`
	Rollback bool                                     `json:"rollback"`
	Models   []model.ZTAPICashMarginSelection20261030 `json:"models"`
}

func bindZTAPICashMargin20261030(c *gin.Context, write, rollback bool) (ztapiCashMarginRequest20261030, bool) {
	var request ztapiCashMarginRequest20261030
	if c.ShouldBindJSON(&request) != nil || (write && !request.Confirm) || len(request.Models) == 0 || len(request.Models) > 14 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Confirm a scoped cash margin request with approved model/version/snapshot baselines."})
		return request, false
	}
	if !write {
		rollback = request.Rollback
	}
	seen := map[int]bool{}
	for _, item := range request.Models {
		if item.ModelID <= 0 || item.Version == 0 || item.SnapshotID <= 0 || seen[item.ModelID] ||
			(rollback && item.RestoreSnapshotID <= 0) || (!rollback && item.RestoreSnapshotID != 0) {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Invalid or duplicate cash margin baseline."})
			return request, false
		}
		seen[item.ModelID] = true
	}
	return request, true
}

func respondZTAPICashMarginError20261030(c *gin.Context, err error) {
	status := http.StatusBadRequest
	if errors.Is(err, model.ErrZTAPIModelVersionConflict) {
		status = http.StatusConflict
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		status = http.StatusNotFound
	}
	c.JSON(status, gin.H{"success": false, "message": "Cash margin operation rejected; no catalog changes committed.", "detail": err.Error()})
}

func PreviewZTAPICashMargin20261030(c *gin.Context) {
	request, ok := bindZTAPICashMargin20261030(c, false, false)
	if !ok {
		return
	}
	result, err := ztapiCashMarginPreview20261030(request.Models, request.Rollback)
	if err != nil {
		respondZTAPICashMarginError20261030(c, err)
		return
	}
	common.ApiSuccess(c, result)
}

func applyZTAPICashMargin20261030(c *gin.Context, rollback bool) {
	request, ok := bindZTAPICashMargin20261030(c, true, rollback)
	if !ok {
		return
	}
	result, err := ztapiCashMarginApply20261030(c.GetInt("id"), request.Models, rollback)
	if err != nil {
		respondZTAPICashMarginError20261030(c, err)
		return
	}
	common.ApiSuccess(c, result)
}

func ApplyZTAPICashMargin20261030(c *gin.Context)   { applyZTAPICashMargin20261030(c, false) }
func RestoreZTAPICashMargin20261030(c *gin.Context) { applyZTAPICashMargin20261030(c, true) }

func CheckZTAPICashMarginDowngrade20261030(c *gin.Context) {
	result, err := ztapiCashMarginDowngrade20261030()
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"success": false, "message": "Downgrade safety could not be established."})
		return
	}
	if !result.CanDowngrade {
		c.JSON(http.StatusConflict, gin.H{"success": false, "message": "Keep the compatible reader; binary downgrade is blocked.", "data": result})
		return
	}
	common.ApiSuccess(c, result)
}
