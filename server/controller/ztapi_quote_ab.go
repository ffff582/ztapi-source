package controller

import (
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
)

var ztapiABPreviewer = model.PreviewZTAPIABCommercialPricing
var ztapiABRepricer = model.ApplyZTAPIABCommercialPricing

func GetZTAPIABPricingPreview(c *gin.Context) {
	preview, err := ztapiABPreviewer()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "A/B 报价预览失败。"})
		return
	}
	common.ApiSuccess(c, preview)
}

func RepriceZTAPIABCatalog(c *gin.Context) {
	var request struct {
		Confirm        bool     `json:"confirm"`
		WorkbookSHA256 string   `json:"workbook_sha256"`
		Models         []string `json:"models"`
	}
	if err := c.ShouldBindJSON(&request); err != nil || !request.Confirm || len(request.Models) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "请确认报价校验码和本次改价模型名单。"})
		return
	}
	preview, err := ztapiABPreviewer()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "无法核对本次报价校验码。"})
		return
	}
	if request.WorkbookSHA256 != preview.WorkbookSHA256 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "报价校验码不匹配，未修改模型价格。"})
		return
	}
	result, err := ztapiABRepricer(c.GetInt("id"), request.WorkbookSHA256, request.Models)
	if err != nil {
		c.JSON(http.StatusConflict, gin.H{"success": false, "message": "本次报价未发布，价格保持不变。", "detail": err.Error()})
		return
	}
	common.ApiSuccess(c, result)
}
