package controller

import (
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
)

var ztapiSupplierRepricer20261019 = model.ApplyZTAPISupplierPricing20261019

func GetZTAPISupplierPricingPreview20261019(c *gin.Context) {
	preview, err := model.PreviewZTAPISupplierPricing20261019()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Supplier price preview unavailable."})
		return
	}
	common.ApiSuccess(c, preview)
}

func RepriceZTAPISupplierCatalog20261019(c *gin.Context) {
	var request struct {
		Confirm          bool              `json:"confirm"`
		WorkbookSHA256   string            `json:"workbook_sha256"`
		Models           []string          `json:"models"`
		ExpectedVersions map[string]uint64 `json:"expected_versions"`
	}
	if err := c.ShouldBindJSON(&request); err != nil || !request.Confirm || len(request.Models) == 0 ||
		request.WorkbookSHA256 != model.ZTAPISupplierWorkbook20261019SHA256 || len(request.Models) != len(request.ExpectedVersions) {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Exact quotation, selected models, versions and confirmation are required."})
		return
	}
	for _, name := range request.Models {
		if request.ExpectedVersions[name] == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Every selected model requires its reviewed version."})
			return
		}
	}
	result, err := ztapiSupplierRepricer20261019(c.GetInt("id"), request.WorkbookSHA256, request.Models, request.ExpectedVersions)
	if err != nil {
		c.JSON(http.StatusConflict, gin.H{"success": false, "message": "Price update rolled back; no changes were committed.", "detail": err.Error()})
		return
	}
	common.ApiSuccess(c, result)
}
