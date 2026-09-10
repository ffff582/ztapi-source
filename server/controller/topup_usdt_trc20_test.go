package controller

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestCreateUSDTTopUpOrderRejectsDisabledInvalidAndBelowMinimumRequests(t *testing.T) {
	setupUSDTTopUpControllerTest(t)
	confirmPaymentComplianceForTest(t)
	setUSDTTopUpControllerEnv(t, false)

	disabled := performUSDTTopUpControllerRequest(t, http.MethodPost, "/orders", 7, `{"amount":10}`, CreateUSDTTopUpOrder)
	require.Equal(t, http.StatusServiceUnavailable, disabled.Code)

	setUSDTTopUpControllerEnv(t, true)
	decimal := performUSDTTopUpControllerRequest(t, http.MethodPost, "/orders", 7, `{"amount":10.5}`, CreateUSDTTopUpOrder)
	require.Equal(t, http.StatusBadRequest, decimal.Code)
	belowMinimum := performUSDTTopUpControllerRequest(t, http.MethodPost, "/orders", 7, `{"amount":9}`, CreateUSDTTopUpOrder)
	require.Equal(t, http.StatusBadRequest, belowMinimum.Code)
}

func TestUSDTTopUpOrderHandlersCreateProjectIsolateAndCancel(t *testing.T) {
	db := setupUSDTTopUpControllerTest(t)
	confirmPaymentComplianceForTest(t)
	setUSDTTopUpControllerEnv(t, true)

	created := performUSDTTopUpControllerRequest(t, http.MethodPost, "/orders", 7, `{"amount":10}`, CreateUSDTTopUpOrder)
	require.Equal(t, http.StatusCreated, created.Code, created.Body.String())
	var envelope struct {
		Success bool                   `json:"success"`
		Data    usdtTopUpOrderResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(created.Body.Bytes(), &envelope))
	require.True(t, envelope.Success)
	require.Equal(t, int64(10), envelope.Data.CreditUnits)
	require.Regexp(t, `^10\.[0-9]{2}$`, envelope.Data.PayAmount)
	require.Equal(t, model.USDTTopUpNetworkTronMainnet, envelope.Data.Network)
	require.Equal(t, model.USDTTopUpAssetUSDT, envelope.Data.Asset)
	require.NotEmpty(t, envelope.Data.ReceivingAddress)
	require.NotContains(t, created.Body.String(), "test-trongrid-secret")

	otherUser := performUSDTTopUpControllerRequest(t, http.MethodGet, "/orders/"+envelope.Data.TradeNo, 8, "", GetUSDTTopUpOrder)
	require.Equal(t, http.StatusNotFound, otherUser.Code)
	owner := performUSDTTopUpControllerRequest(t, http.MethodGet, "/orders/"+envelope.Data.TradeNo, 7, "", GetUSDTTopUpOrder)
	require.Equal(t, http.StatusOK, owner.Code)
	require.Contains(t, owner.Body.String(), `"status":"pending"`)

	cancelled := performUSDTTopUpControllerRequest(t, http.MethodPost, "/orders/"+envelope.Data.TradeNo+"/cancel", 7, "", CancelUSDTTopUpOrder)
	require.Equal(t, http.StatusOK, cancelled.Code)
	require.Contains(t, cancelled.Body.String(), `"status":"expired"`)
	var topUp model.TopUp
	require.NoError(t, db.Where("trade_no = ?", envelope.Data.TradeNo).First(&topUp).Error)
	require.Equal(t, common.TopUpStatusExpired, topUp.Status)
}

func TestCreateUSDTTopUpOrderReturnsConflictWhenAllSuffixesAreCooling(t *testing.T) {
	setupUSDTTopUpControllerTest(t)
	confirmPaymentComplianceForTest(t)
	setUSDTTopUpControllerEnv(t, true)
	config := mustLoadUSDTTopUpControllerConfig(t)
	now := time.Now().UTC()
	for i := 0; i < 99; i++ {
		_, err := model.CreateUSDTTopUpOrder(100+i, 10, now, config)
		require.NoError(t, err)
	}

	response := performUSDTTopUpControllerRequest(t, http.MethodPost, "/orders", 7, `{"amount":10}`, CreateUSDTTopUpOrder)

	require.Equal(t, http.StatusConflict, response.Code)
	require.Contains(t, response.Body.String(), "capacity")
}

func TestGetUSDTTopUpOrderProjectsConfirmingSettledAndManualReviewStates(t *testing.T) {
	db := setupUSDTTopUpControllerTest(t)
	setUSDTTopUpControllerEnv(t, true)
	config := mustLoadUSDTTopUpControllerConfig(t)
	now := time.Now().UTC().Truncate(time.Second)
	confirming, err := model.CreateUSDTTopUpOrder(7, 12, now, config)
	require.NoError(t, err)
	require.NoError(t, db.Model(&model.USDTTopUpOrder{}).Where("id = ?", confirming.ID).Update("status", model.USDTTopUpStatusConfirming).Error)
	confirmingResponse := performUSDTTopUpControllerRequest(t, http.MethodGet, "/orders/"+confirming.TradeNo, 7, "", GetUSDTTopUpOrder)
	require.Equal(t, http.StatusOK, confirmingResponse.Code)
	require.Contains(t, confirmingResponse.Body.String(), `"status":"confirming"`)

	settled, err := model.CreateUSDTTopUpOrder(7, 10, now, config)
	require.NoError(t, err)
	txID := "projection-settled-tx"
	settledAt := now.Add(time.Minute).Unix()
	require.NoError(t, db.Model(&model.USDTTopUpOrder{}).Where("id = ?", settled.ID).Updates(map[string]interface{}{
		"status": model.USDTTopUpStatusSettled, "tx_id": txID, "settled_at": settledAt,
	}).Error)
	settledResponse := performUSDTTopUpControllerRequest(t, http.MethodGet, "/orders/"+settled.TradeNo, 7, "", GetUSDTTopUpOrder)
	require.Equal(t, http.StatusOK, settledResponse.Code)
	require.Contains(t, settledResponse.Body.String(), `"status":"settled"`)
	require.Contains(t, settledResponse.Body.String(), `"tx_id":"projection-settled-tx"`)
	require.Contains(t, settledResponse.Body.String(), fmt.Sprintf(`"settled_at":%d`, settledAt))

	review, err := model.CreateUSDTTopUpOrder(7, 11, now, config)
	require.NoError(t, err)
	require.NoError(t, db.Model(&model.USDTTopUpOrder{}).Where("id = ?", review.ID).Updates(map[string]interface{}{
		"status": model.USDTTopUpStatusManualReview, "review_reason": "ambiguous confirmed transfer",
	}).Error)
	reviewResponse := performUSDTTopUpControllerRequest(t, http.MethodGet, "/orders/"+review.TradeNo, 7, "", GetUSDTTopUpOrder)
	require.Equal(t, http.StatusOK, reviewResponse.Code)
	require.Contains(t, reviewResponse.Body.String(), `"status":"manual_review"`)
	require.Contains(t, reviewResponse.Body.String(), `"review_reason":"ambiguous confirmed transfer"`)
	for _, forbidden := range []string{"tx_from", "raw_response", "headers", "test-trongrid-secret"} {
		require.NotContains(t, reviewResponse.Body.String(), forbidden)
	}
}

func TestGetTopUpInfoAdvertisesUSDTOnlyWithComplianceAndCompleteConfiguration(t *testing.T) {
	setupUSDTTopUpControllerTest(t)
	setUSDTTopUpControllerEnv(t, true)
	payment := operation_setting.GetPaymentSetting()
	originalConfirmed := payment.ComplianceConfirmed
	originalVersion := payment.ComplianceTermsVersion
	t.Cleanup(func() {
		payment.ComplianceConfirmed = originalConfirmed
		payment.ComplianceTermsVersion = originalVersion
	})

	payment.ComplianceConfirmed = false
	payment.ComplianceTermsVersion = ""
	withoutCompliance := performUSDTTopUpControllerRequest(t, http.MethodGet, "/topup/info", 7, "", GetTopUpInfo)
	require.Equal(t, http.StatusOK, withoutCompliance.Code)
	require.Contains(t, withoutCompliance.Body.String(), `"enable_usdt_trc20_topup":false`)

	payment.ComplianceConfirmed = true
	payment.ComplianceTermsVersion = operation_setting.CurrentComplianceTermsVersion
	available := performUSDTTopUpControllerRequest(t, http.MethodGet, "/topup/info", 7, "", GetTopUpInfo)
	require.Equal(t, http.StatusOK, available.Code)
	for _, expected := range []string{`"enable_usdt_trc20_topup":true`, `"usdt_trc20_network":"tron-mainnet"`, `"usdt_trc20_asset":"USDT"`, `"usdt_trc20_min_topup":10`, `"usdt_trc20_order_ttl_seconds":600`} {
		require.Contains(t, available.Body.String(), expected)
	}
	require.NotContains(t, available.Body.String(), "test-trongrid-secret")
}

func TestAdminSettingsUSDTWatcherProjectionContainsNoSecretShapedFields(t *testing.T) {
	db := setupUSDTTopUpControllerTest(t)
	setUSDTTopUpControllerEnv(t, true)
	require.NoError(t, db.AutoMigrate(&model.ZTAPIModelConfig{}))
	recorder := performUSDTTopUpControllerRequest(t, http.MethodGet, "/admin/settings", 1, "", GetAdminSettings)
	require.Equal(t, http.StatusOK, recorder.Code)
	body := strings.ToLower(recorder.Body.String())
	require.Contains(t, body, `"usdt_trc20"`)
	require.Contains(t, body, `"confirming_orders":0`)
	for _, forbidden := range []string{"test-trongrid-secret", `"api_key"`, `"authorization"`, `"raw_response"`, `"headers"`} {
		require.NotContains(t, body, forbidden)
	}
}

func setupUSDTTopUpControllerTest(t *testing.T) *gorm.DB {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "usdt-controller.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.User{}, &model.TopUp{}, &model.BalanceLedger{}, &model.USDTTopUpOrder{},
		&model.USDTTopUpAmountLock{}, &model.USDTWatcherLease{},
	))
	previousDB := model.DB
	previousSQLite := common.UsingSQLite
	previousMySQL := common.UsingMySQL
	previousPostgres := common.UsingPostgreSQL
	previousRedis := common.RedisEnabled
	model.DB = db
	common.UsingSQLite = true
	common.UsingMySQL = false
	common.UsingPostgreSQL = false
	common.RedisEnabled = false
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() {
		model.DB = previousDB
		common.UsingSQLite = previousSQLite
		common.UsingMySQL = previousMySQL
		common.UsingPostgreSQL = previousPostgres
		common.RedisEnabled = previousRedis
		_ = sqlDB.Close()
	})
	return db
}

func setUSDTTopUpControllerEnv(t *testing.T, enabled bool) {
	t.Helper()
	t.Setenv("USDT_TRC20_TOPUP_ENABLED", fmt.Sprintf("%t", enabled))
	t.Setenv("USDT_TRC20_RECEIVING_ADDRESS", "TJSdKoxvYJofK6CQBNnXwMM9kS1t4Sj3V2")
	t.Setenv("TRONGRID_API_KEY", "test-trongrid-secret")
	t.Setenv("USDT_TRC20_MIN_TOPUP", "10")
	t.Setenv("USDT_TRC20_ORDER_TTL_SECONDS", "600")
	t.Setenv("USDT_TRC20_POLL_INTERVAL_SECONDS", "5")
	t.Setenv("USDT_TRC20_SUFFIX_COOLDOWN_SECONDS", "86400")
}

func mustLoadUSDTTopUpControllerConfig(t *testing.T) setting.USDTTopUpConfig {
	t.Helper()
	config, err := setting.LoadUSDTTopUpConfig()
	require.NoError(t, err)
	return config
}

func performUSDTTopUpControllerRequest(t *testing.T, method string, path string, userID int, body string, handler gin.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	engine := gin.New()
	engine.Handle(method, path, func(c *gin.Context) {
		c.Set("id", userID)
		parts := strings.Split(strings.Trim(path, "/"), "/")
		if len(parts) >= 2 && parts[0] == "orders" {
			c.Params = append(c.Params, gin.Param{Key: "trade_no", Value: parts[1]})
		}
		handler(c)
	})
	request := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)
	return recorder
}
