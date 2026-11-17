package controller

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
)

var ztapiPriceReductionPreview20261107 = model.PreviewZTAPIPriceReduction20261107
var ztapiPriceReductionApply20261107 = model.ApplyZTAPIPriceReduction20261107
var ztapiPriceReductionDowngrade20261107 = model.CheckZTAPIPriceReductionDowngrade20261107

type ztapiPriceReductionRequest20261107 struct {
	Confirm  bool                                         `json:"confirm"`
	Rollback bool                                         `json:"rollback"`
	Models   []model.ZTAPIPriceReductionSelection20261107 `json:"models"`
}

func ztapiReductionRoot20261107(c *gin.Context) bool {
	if c.GetInt("role") != common.RoleRootUser || c.GetInt("id") <= 0 {
		c.JSON(403, gin.H{"success": false, "message": "Root operator required."})
		return false
	}
	return true
}

func bindZTAPIPriceReduction20261107(c *gin.Context, write, rollback bool) (ztapiPriceReductionRequest20261107, bool) {
	var r ztapiPriceReductionRequest20261107
	if !ztapiReductionRoot20261107(c) {
		return r, false
	}
	if c.ShouldBindJSON(&r) != nil || (write && !r.Confirm) || len(r.Models) == 0 || len(r.Models) > 16 || (write && !rollback && r.Rollback) {
		c.JSON(400, gin.H{"success": false, "message": "Confirm scoped dated reduction with model/version/snapshot/source baselines."})
		return r, false
	}
	if !write {
		rollback = r.Rollback
	}
	seen := map[int]bool{}
	for _, s := range r.Models {
		if s.ModelID <= 0 || s.Version == 0 || s.SnapshotID <= 0 || s.SourceID <= 0 || seen[s.ModelID] || (rollback && s.RestoreSnapshotID <= 0) || (!rollback && s.RestoreSnapshotID != 0) {
			c.JSON(400, gin.H{"success": false, "message": "Invalid or duplicate dated reduction baseline."})
			return r, false
		}
		seen[s.ModelID] = true
	}
	return r, true
}

func GetZTAPIPriceReductionBaseline20261107(c *gin.Context) {
	if ztapiReductionRoot20261107(c) {
		common.ApiSuccess(c, model.ApprovedZTAPIPriceReductionSelections20261107())
	}
}

func PreviewZTAPIPriceReduction20261107(c *gin.Context) {
	r, ok := bindZTAPIPriceReduction20261107(c, false, false)
	if !ok {
		return
	}
	result, err := ztapiPriceReductionPreview20261107(r.Models, r.Rollback)
	if err != nil {
		respondZTAPICashMarginError20261030(c, err)
		return
	}
	common.ApiSuccess(c, result)
}

func applyZTAPIPriceReduction20261107(c *gin.Context, rollback bool) {
	r, ok := bindZTAPIPriceReduction20261107(c, true, rollback)
	if !ok {
		return
	}
	result, err := ztapiPriceReductionApply20261107(c.GetInt("id"), r.Models, rollback)
	if err != nil {
		respondZTAPICashMarginError20261030(c, err)
		return
	}
	common.ApiSuccess(c, result)
}

func ApplyZTAPIPriceReduction20261107(c *gin.Context)    { applyZTAPIPriceReduction20261107(c, false) }
func RollbackZTAPIPriceReduction20261107(c *gin.Context) { applyZTAPIPriceReduction20261107(c, true) }

func CheckZTAPIPriceReductionDowngrade20261107(c *gin.Context) {
	if !ztapiReductionRoot20261107(c) {
		return
	}
	result, err := ztapiPriceReductionDowngrade20261107()
	if err != nil {
		c.JSON(503, gin.H{"success": false, "message": "Dated-policy downgrade safety unavailable."})
		return
	}
	if !result.CanDowngrade {
		c.JSON(409, gin.H{"success": false, "message": "Restore original publications, quiesce admissions and drain new-policy requests before binary downgrade.", "data": result})
		return
	}
	common.ApiSuccess(c, result)
}
