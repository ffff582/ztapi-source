package controller

import (
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

var ztapiOKXAlipayBidReader = service.FetchZTAPIOKXAlipayUSDTBid

var ztapiPBOCMidRateReader = service.FetchZTAPIPBOCUSDCNYMidRate

func GetZTAPIFXPolicy(c *gin.Context) {
	current, err := model.CurrentZTAPIFXPolicy(model.DB)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "汇率设置读取失败。"})
		return
	}
	history, err := model.ListZTAPIFXPolicies(20)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "汇率设置记录读取失败。"})
		return
	}
	common.ApiSuccess(c, gin.H{"current": current, "history": history})
}

func CreateZTAPIFXPolicy(c *gin.Context) {
	var request struct {
		MarketCNYPerUSDT  string `json:"market_cny_per_usdt"`
		StopLossCNY       string `json:"stop_loss_cny"`
		UpstreamCNYPerUSD string `json:"upstream_cny_per_usd"`
		MarketSource      string `json:"market_source"`
		Reason            string `json:"reason"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "请填写汇率和修改原因。"})
		return
	}
	policy, err := model.CreateZTAPIFXPolicy(model.ZTAPIFXPolicyInput{
		MarketCNYPerUSDT: request.MarketCNYPerUSDT, StopLossCNY: request.StopLossCNY,
		UpstreamCNYPerUSD: request.UpstreamCNYPerUSD, MarketSource: request.MarketSource,
		Reason: request.Reason, OperatorID: c.GetInt("id"),
	})
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "汇率设置未保存：" + err.Error()})
		return
	}
	common.ApiSuccess(c, policy)
}

func GetZTAPIOKXAlipayRate(c *gin.Context) {
	bid, err := ztapiOKXAlipayBidReader(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"success": false, "message": "读取欧意支付宝报价失败：" + err.Error()})
		return
	}
	common.ApiSuccess(c, bid)
}

func GetZTAPIPBOCMidRate(c *gin.Context) {
	mid, err := ztapiPBOCMidRateReader(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"success": false, "message": "读取央行中间价失败：" + err.Error()})
		return
	}
	common.ApiSuccess(c, mid)
}

func GetZTAPIFXPricingPreview(c *gin.Context) {
	preview, err := model.PreviewZTAPIFXRepricing()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "汇率改价预览失败。", "detail": err.Error()})
		return
	}
	common.ApiSuccess(c, preview)
}

func ApplyZTAPIFXRepricing(c *gin.Context) {
	var request struct {
		Confirm bool `json:"confirm"`
	}
	if err := c.ShouldBindJSON(&request); err != nil || !request.Confirm {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "请先确认按当前汇率改价。"})
		return
	}
	result, err := model.ApplyZTAPIFXRepricing(c.GetInt("id"))
	if err != nil {
		c.JSON(http.StatusConflict, gin.H{"success": false, "message": "改价未完成，价格保持不变。", "detail": err.Error()})
		return
	}
	common.ApiSuccess(c, result)
}
