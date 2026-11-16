package controller

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type ztapiWatchedAddressRequest struct {
	Address string `json:"address"`
	Label   string `json:"label"`
	Reason  string `json:"reason"`
	Confirm bool   `json:"confirm"`
}

type ztapiWatchedAddressEnabledRequest struct {
	Enabled bool   `json:"enabled"`
	Reason  string `json:"reason"`
	Confirm bool   `json:"confirm"`
}

func projectZTAPIWatchedAddress(record model.USDTWatchedReceivingAddress) gin.H {
	return gin.H{
		"id": record.ID, "address": record.Address, "label": record.Label,
		"enabled": record.Enabled, "operator_id": record.OperatorID,
		"created_at": record.CreatedAt, "updated_at": record.UpdatedAt,
	}
}

func respondZTAPIWatchedAddressError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, model.ErrUSDTWatchedAddressInvalid):
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "收款地址无效，请检查是否为有效的 TRON 地址。"})
	case errors.Is(err, model.ErrUSDTWatchedAddressDuplicate):
		c.JSON(http.StatusConflict, gin.H{"success": false, "message": "该地址已在监听列表中。"})
	case errors.Is(err, model.ErrUSDTWatchedAddressLimit):
		c.JSON(http.StatusConflict, gin.H{"success": false, "message": "启用的监听地址已达上限，请先停用其他地址。"})
	case errors.Is(err, gorm.ErrRecordNotFound):
		c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "监听地址不存在。"})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "监听地址操作失败。"})
	}
}

// GetZTAPIWatchedReceivingAddresses also reports the deployed receiving
// address, which is the only address an order asks a customer to pay and
// cannot be changed from here.
func GetZTAPIWatchedReceivingAddresses(c *gin.Context) {
	records, err := model.ListUSDTWatchedReceivingAddresses()
	if err != nil {
		respondZTAPIWatchedAddressError(c, err)
		return
	}
	projected := make([]gin.H, 0, len(records))
	for _, record := range records {
		projected = append(projected, projectZTAPIWatchedAddress(record))
	}
	receiving := ""
	if config, configErr := setting.LoadUSDTTopUpConfig(); configErr == nil {
		receiving = config.ReceivingAddress
	}
	common.ApiSuccess(c, gin.H{
		"receiving_address": receiving,
		"items":             projected,
		"max_enabled":       model.MaxEnabledUSDTWatchedAddresses,
	})
}

func CreateZTAPIWatchedReceivingAddress(c *gin.Context) {
	var request ztapiWatchedAddressRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "监听地址参数无效。"})
		return
	}
	if !request.Confirm || strings.TrimSpace(request.Reason) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "必须确认并填写添加原因。"})
		return
	}
	record, err := model.CreateUSDTWatchedReceivingAddress(
		request.Address, request.Label, c.GetInt("id"), time.Now().UTC(),
	)
	if err != nil {
		respondZTAPIWatchedAddressError(c, err)
		return
	}
	recordManageAuditFor(c, record.ID, "payment.watched_address_added", map[string]interface{}{
		"address": record.Address, "label": record.Label, "reason": request.Reason,
	})
	common.ApiSuccess(c, projectZTAPIWatchedAddress(*record))
}

func UpdateZTAPIWatchedReceivingAddress(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "无效的监听地址编号。"})
		return
	}
	var request ztapiWatchedAddressEnabledRequest
	if bindErr := c.ShouldBindJSON(&request); bindErr != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "监听地址参数无效。"})
		return
	}
	if !request.Confirm || strings.TrimSpace(request.Reason) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "必须确认并填写变更原因。"})
		return
	}
	record, err := model.SetUSDTWatchedReceivingAddressEnabled(
		id, request.Enabled, c.GetInt("id"), time.Now().UTC(),
	)
	if err != nil {
		respondZTAPIWatchedAddressError(c, err)
		return
	}
	recordManageAuditFor(c, record.ID, "payment.watched_address_updated", map[string]interface{}{
		"address": record.Address, "enabled": record.Enabled, "reason": request.Reason,
	})
	common.ApiSuccess(c, projectZTAPIWatchedAddress(*record))
}
