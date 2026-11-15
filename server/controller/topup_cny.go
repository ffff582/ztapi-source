package controller

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

type cnyTopUpConfig struct {
	gateway service.CNYGateway
	methods []string
	rate    decimal.Decimal
	minFen  int64
	maxFen  int64
}

func cnyTopUpForUser(user int) (cnyTopUpConfig, bool) {
	if user <= 0 || !operation_setting.IsPaymentComplianceConfirmed() || os.Getenv("ZTAPI_CNY_TOPUP_ENABLED") != "true" {
		return cnyTopUpConfig{}, false
	}
	public := os.Getenv("ZTAPI_CNY_TOPUP_PUBLIC") == "true"
	if !public {
		allowed, err := strconv.Atoi(os.Getenv("ZTAPI_CNY_TOPUP_TEST_USER_ID"))
		if err != nil || user != allowed {
			return cnyTopUpConfig{}, false
		}
	}
	configuredMethods := os.Getenv("ZTAPI_CNY_TOPUP_METHODS")
	if configuredMethods == "" && !public {
		configuredMethods = "alipay,wxpay"
	}
	methods := strings.Split(configuredMethods, ",")
	for i, method := range methods {
		if (method != "alipay" && method != "wxpay") || slices.Contains(methods[:i], method) {
			return cnyTopUpConfig{}, false
		}
	}
	g, ok := service.LoadCNYGateway()
	rateRaw := os.Getenv("ZTAPI_CNY_TOPUP_CNY_PER_U")
	if len(rateRaw) == 0 || len(rateRaw) > 20 {
		return cnyTopUpConfig{}, false
	}
	rate, err := decimal.NewFromString(rateRaw)
	min, errMin := model.ParseCNYAmountFen(os.Getenv("ZTAPI_CNY_TOPUP_MIN_CNY"))
	var max int64
	if raw := os.Getenv("ZTAPI_CNY_TOPUP_MAX_CNY"); raw != "" {
		var errMax error
		max, errMax = model.ParseCNYAmountFen(raw)
		if errMax != nil || max < min {
			return cnyTopUpConfig{}, false
		}
	}
	if !ok || err != nil || rate.Exponent() < -8 || rate.LessThan(decimal.New(1, -2)) || rate.GreaterThan(decimal.NewFromInt(100)) ||
		errMin != nil {
		return cnyTopUpConfig{}, false
	}
	return cnyTopUpConfig{gateway: g, methods: methods, rate: rate, minFen: min, maxFen: max}, true
}

func GetCNYTopUpInfo(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	config, enabled := cnyTopUpForUser(c.GetInt("id"))
	data := gin.H{"enabled": enabled}
	if enabled {
		data["min_cny"] = formatCNYFen(config.minFen)
		if config.maxFen > 0 {
			data["max_cny"] = formatCNYFen(config.maxFen)
		}
		data["cny_per_u"] = config.rate.String()
		data["methods"] = config.methods
	}
	common.ApiSuccess(c, data)
}

func CreateCNYTopUpOrder(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	user := c.GetInt("id")
	config, enabled := cnyTopUpForUser(user)
	if !enabled {
		writeUSDTTopUpError(c, http.StatusServiceUnavailable, "cny_topup_unavailable")
		return
	}
	var input struct {
		Amount     string `json:"amount_cny"`
		Method     string `json:"method"`
		RequestKey string `json:"idempotency_key"`
	}
	if !readCNYTopUpJSON(c, &input) || !model.ValidCNYPaymentID(input.RequestKey) || !slices.Contains(config.methods, input.Method) {
		writeUSDTTopUpError(c, http.StatusBadRequest, "invalid_cny_order")
		return
	}
	fen, err := model.ParseCNYAmountFen(input.Amount)
	if err != nil || fen < config.minFen || (config.maxFen > 0 && fen > config.maxFen) {
		writeUSDTTopUpError(c, http.StatusBadRequest, "invalid_cny_amount")
		return
	}
	quota, err := model.CNYTopUpQuota(fen, config.rate, decimal.NewFromFloat(common.QuotaPerUnit))
	if err != nil {
		writeUSDTTopUpError(c, http.StatusBadRequest, "invalid_cny_amount")
		return
	}
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		writeUSDTTopUpError(c, http.StatusInternalServerError, "cny_order_unavailable")
		return
	}
	ctx := c.Request.Context()
	order, fresh, err := model.CreateCNYTopUpOrder(ctx, model.CNYTopUpOrder{
		OrderNo: "CNY-" + hex.EncodeToString(token[:]), UserID: user, RequestKey: input.RequestKey,
		MerchantNo: config.gateway.MerchantNo, AppID: config.gateway.AppID, Method: input.Method,
		PayFen: fen, Quota: quota, CNYPerUnit: config.rate.String(), CreatedAt: time.Now().Unix(),
	})
	if err != nil {
		writeCNYTopUpMutationError(c, err)
		return
	}
	if fresh {
		checkout, gatewayErr := config.gateway.CreateCheckout(ctx, order, c.ClientIP())
		if gatewayErr == nil {
			order, gatewayErr = model.RecordCNYTopUpCheckout(ctx, order.OrderNo, checkout.TradeNo, checkout.URL, checkout.QRCode)
		}
		if gatewayErr != nil {
			// The provider may have accepted the order despite a timeout. Preserve
			// its identity instead of issuing another create with a fresh ID.
			logger.LogWarn(ctx, "CNY topup checkout requires reconciliation")
			if order.OrderNo == "" {
				writeUSDTTopUpError(c, http.StatusServiceUnavailable, "cny_checkout_pending")
				return
			}
		}
	}
	common.ApiSuccess(c, projectCNYTopUp(order))
}

func ListCNYTopUpOrders(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	orders, err := model.ListCNYTopUpOrders(c.Request.Context(), c.GetInt("id"))
	if err != nil {
		writeCNYTopUpMutationError(c, err)
		return
	}
	out := make([]gin.H, 0, len(orders))
	for _, order := range orders {
		out = append(out, projectCNYTopUp(order))
	}
	common.ApiSuccess(c, out)
}

func GetAdminCNYTopUpOrders(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	number := strings.TrimSpace(c.Query("order_no"))
	page, err := strconv.Atoi(c.DefaultQuery("p", "1"))
	if err != nil || page < 1 || page > 10000 || (number != "" && !model.ValidCNYPaymentID(number)) {
		writeUSDTTopUpError(c, http.StatusBadRequest, "invalid_cny_order_filter")
		return
	}
	const pageSize = 20
	rows, total, err := model.ListAdminCNYTopUpOrders(c.Request.Context(), number, (page-1)*pageSize, pageSize)
	if err != nil {
		writeUSDTTopUpError(c, http.StatusServiceUnavailable, "cny_orders_unavailable")
		return
	}
	items := make([]gin.H, 0, len(rows))
	for _, row := range rows {
		amount := formatCNYFen(row.PayFen)
		var paid any
		if row.PaidAt > 0 && row.ProviderTradeNo != nil &&
			(row.State == model.CNYTopUpPaid || row.State == model.CNYTopUpRefundHold || row.State == model.CNYTopUpRefunded) {
			paid = amount
		}
		items = append(items, gin.H{"order_no": row.OrderNo, "provider_trade_no": row.ProviderTradeNo, "method": row.Method,
			"amount_cny": amount, "paid_amount_cny": paid, "status": row.State, "created_at": row.CreatedAt})
	}
	common.ApiSuccess(c, gin.H{"items": items, "total": total, "page": page, "page_size": pageSize})
}

func CNYTopUpNotify(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	g, ok := service.LoadCNYGateway()
	if !ok {
		c.String(http.StatusBadRequest, "fail")
		return
	}
	handleCNYTopUpNotify(c, g, g.VerifyPaid)
}

func handleCNYTopUpNotify(c *gin.Context, g service.CNYGateway, verifyPaid func(context.Context, service.CNYPaymentNotice) error) {
	if c.Request.Method != http.MethodGet || c.Request.ContentLength != 0 || len(c.Request.TransferEncoding) != 0 || len(c.Request.URL.RawQuery) > 8192 {
		c.String(http.StatusBadRequest, "fail")
		return
	}
	notice, err := g.DecodeNotice([]byte(c.Request.URL.RawQuery))
	if err != nil {
		c.String(http.StatusBadRequest, "fail")
		return
	}
	ctx := c.Request.Context()
	evidence := notice.Evidence()
	order, err := model.ReadCNYTopUpPaymentOrder(ctx, evidence)
	if err == nil {
		switch notice.Status() {
		case service.CNYTradeSuccess:
			if order.State == model.CNYTopUpPending {
				err = verifyPaid(ctx, notice)
			}
			if err == nil {
				_, err = model.SettleCNYTopUp(ctx, evidence, time.Now().UTC())
			}
		case service.CNYTradeRefund:
			err = model.RecordCNYRefundNotice(ctx, evidence, time.Now().UTC())
		}
	}
	if err != nil {
		logger.LogWarn(ctx, "CNY topup notice requires reconciliation")
		c.String(http.StatusServiceUnavailable, "fail")
		return
	}
	c.String(http.StatusOK, "success")
}

func HoldCNYTopUpRefund(c *gin.Context) {
	order, err := model.HoldCNYTopUpRefund(c.Request.Context(), c.Param("order_no"), c.GetInt("id"), time.Now().UTC())
	if err != nil {
		writeCNYTopUpMutationError(c, err)
		return
	}
	common.ApiSuccess(c, projectCNYTopUp(order))
}

func ConfirmCNYTopUpRefund(c *gin.Context) {
	var receipt struct {
		TradeNo  string `json:"provider_trade_no"`
		RefundNo string `json:"provider_refund_no"`
		Amount   string `json:"refunded_cny"`
		Verified bool   `json:"verified_full_original_route_refund"`
	}
	if !readCNYTopUpJSON(c, &receipt) || !receipt.Verified || !model.ValidCNYPaymentID(receipt.TradeNo) {
		writeUSDTTopUpError(c, http.StatusBadRequest, "invalid_cny_refund_evidence")
		return
	}
	fen, err := model.ParseCNYAmountFen(receipt.Amount)
	if err != nil {
		writeUSDTTopUpError(c, http.StatusBadRequest, "invalid_cny_refund_evidence")
		return
	}
	order, err := model.ConfirmCNYTopUpRefund(c.Request.Context(), c.Param("order_no"), receipt.TradeNo, receipt.RefundNo, fen, c.GetInt("id"), time.Now().UTC())
	if err != nil {
		writeCNYTopUpMutationError(c, err)
		return
	}
	common.ApiSuccess(c, projectCNYTopUp(order))
}

func projectCNYTopUp(order model.CNYTopUpOrder) gin.H {
	credit := decimal.NewFromInt(order.Quota).DivRound(decimal.NewFromFloat(common.QuotaPerUnit), 6)
	return gin.H{"order_no": order.OrderNo, "status": order.State, "amount_cny": formatCNYFen(order.PayFen),
		"credit_u": credit.StringFixed(6), "cny_per_u": order.CNYPerUnit, "method": order.Method,
		"checkout_url": order.CheckoutURL, "checkout_qr": order.CheckoutQR, "created_at": order.CreatedAt,
		"paid_at": order.PaidAt, "refunded_at": order.RefundedAt}
}

func formatCNYFen(fen int64) string { return decimal.New(fen, -2).StringFixed(2) }

func readCNYTopUpJSON(c *gin.Context, out any) bool {
	raw, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, 4096))
	return err == nil && common.RejectDuplicateJsonObjectMembers(bytes.NewReader(raw)) == nil && common.DecodeJsonStrict(bytes.NewReader(raw), out) == nil
}

func writeCNYTopUpMutationError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		writeUSDTTopUpError(c, http.StatusNotFound, "cny_order_not_found")
	case errors.Is(err, model.ErrBalanceLedgerOverflow):
		writeUSDTTopUpError(c, http.StatusConflict, "cny_credit_capacity")
	case errors.Is(err, model.ErrCNYTopUpEvidence), errors.Is(err, model.ErrCNYTopUpConflict), errors.Is(err, model.ErrBalanceLedgerNegativeBalance):
		writeUSDTTopUpError(c, http.StatusConflict, "cny_order_conflict")
	default:
		writeUSDTTopUpError(c, http.StatusServiceUnavailable, "cny_order_unavailable")
	}
}
