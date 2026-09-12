package controller

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
)

type createUSDTTopUpOrderRequest struct {
	Amount int64 `json:"amount"`
}

type usdtTopUpOrderResponse struct {
	ID               int64  `json:"id"`
	TradeNo          string `json:"trade_no"`
	CreditUnits      int64  `json:"credit_units"`
	PayAmount        string `json:"pay_amount"`
	ReceivingAddress string `json:"receiving_address"`
	Network          string `json:"network"`
	Asset            string `json:"asset"`
	ExpiresAt        int64  `json:"expires_at"`
	Status           string `json:"status"`
	TxID             string `json:"tx_id,omitempty"`
	SettledAt        *int64 `json:"settled_at,omitempty"`
	ReviewReason     string `json:"review_reason,omitempty"`
}

func CreateUSDTTopUpOrder(c *gin.Context) {
	if !operation_setting.IsPaymentComplianceConfirmed() {
		writeUSDTTopUpError(c, http.StatusForbidden, "payment compliance confirmation is required")
		return
	}
	config, err := setting.LoadUSDTTopUpConfig()
	if err != nil || !config.Enabled {
		writeUSDTTopUpError(c, http.StatusServiceUnavailable, "USDT TRC-20 topup is unavailable")
		return
	}
	var request createUSDTTopUpOrderRequest
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeUSDTTopUpError(c, http.StatusBadRequest, "amount must be a whole USDT number")
		return
	}
	if err := ensureJSONBodyConsumed(decoder); err != nil {
		writeUSDTTopUpError(c, http.StatusBadRequest, "invalid request body")
		return
	}
	if request.Amount < config.MinTopUp {
		writeUSDTTopUpError(c, http.StatusBadRequest, "amount is below the minimum topup")
		return
	}
	order, err := model.CreateUSDTTopUpOrder(c.GetInt("id"), request.Amount, time.Now().UTC(), config)
	if err != nil {
		switch {
		case errors.Is(err, model.ErrUSDTTopUpCapacity):
			writeUSDTTopUpError(c, http.StatusConflict, "USDT payment amount capacity is temporarily exhausted")
		case errors.Is(err, model.ErrUSDTTopUpInvalidAmount):
			writeUSDTTopUpError(c, http.StatusBadRequest, "invalid topup amount")
		default:
			writeUSDTTopUpError(c, http.StatusInternalServerError, "unable to create USDT topup order")
		}
		return
	}
	service.WakeUSDTWatcher()
	c.JSON(http.StatusCreated, gin.H{"success": true, "data": projectUSDTTopUpOrder(order)})
}

func GetUSDTTopUpOrder(c *gin.Context) {
	order, err := model.GetUserUSDTTopUpOrder(c.GetInt("id"), strings.TrimSpace(c.Param("trade_no")))
	if err != nil {
		if errors.Is(err, model.ErrUSDTTopUpNotFound) {
			writeUSDTTopUpError(c, http.StatusNotFound, "USDT topup order not found")
			return
		}
		writeUSDTTopUpError(c, http.StatusInternalServerError, "unable to load USDT topup order")
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": projectUSDTTopUpOrder(order)})
}

func CancelUSDTTopUpOrder(c *gin.Context) {
	order, err := model.CancelUSDTTopUpOrder(c.GetInt("id"), strings.TrimSpace(c.Param("trade_no")), time.Now().UTC())
	if err != nil {
		switch {
		case errors.Is(err, model.ErrUSDTTopUpNotFound):
			writeUSDTTopUpError(c, http.StatusNotFound, "USDT topup order not found")
		case errors.Is(err, model.ErrUSDTTopUpStateConflict):
			writeUSDTTopUpError(c, http.StatusConflict, "USDT topup order can no longer be cancelled")
		default:
			writeUSDTTopUpError(c, http.StatusInternalServerError, "unable to cancel USDT topup order")
		}
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": projectUSDTTopUpOrder(order)})
}

func projectUSDTTopUpOrder(order *model.USDTTopUpOrder) usdtTopUpOrderResponse {
	if order == nil {
		return usdtTopUpOrderResponse{}
	}
	response := usdtTopUpOrderResponse{
		ID:               order.ID,
		TradeNo:          order.TradeNo,
		CreditUnits:      order.CreditUnits,
		PayAmount:        order.ExactPayAmountString(),
		ReceivingAddress: order.ReceivingAddress,
		Network:          order.Network,
		Asset:            order.Asset,
		ExpiresAt:        order.ExpiresAt,
		Status:           order.Status,
		SettledAt:        order.SettledAt,
		ReviewReason:     order.ReviewReason,
	}
	if order.TxID != nil {
		response.TxID = *order.TxID
	}
	return response
}

func ensureJSONBodyConsumed(decoder *json.Decoder) error {
	var extra interface{}
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func writeUSDTTopUpError(c *gin.Context, status int, message string) {
	c.JSON(status, gin.H{"success": false, "message": message})
}
