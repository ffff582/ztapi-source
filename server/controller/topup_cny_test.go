package controller

import (
	"context"
	"crypto/md5"
	"errors"
	"fmt"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestCNYPublicOrdersRequireExplicitPaymentMethods(t *testing.T) {
	setting := operation_setting.GetPaymentSetting()
	previous := *setting
	t.Cleanup(func() { *setting = previous })
	setting.ComplianceConfirmed = true
	setting.ComplianceTermsVersion = operation_setting.CurrentComplianceTermsVersion
	t.Setenv("ZTAPI_CNY_TOPUP_MERCHANT", "merchant-fixture")
	t.Setenv("ZTAPI_CNY_TOPUP_APP", "app-fixture")
	t.Setenv("ZTAPI_CNY_TOPUP_KEY", "fake-gateway-key-for-local-tests")
	t.Setenv("ZTAPI_CNY_TOPUP_CHECKOUT_HOST", "checkout.example.test")
	t.Setenv("ZTAPI_CNY_TOPUP_CALLBACK_ORIGIN", "https://ztapi.vip")
	t.Setenv("ZTAPI_CNY_TOPUP_ENABLED", "true")
	t.Setenv("ZTAPI_CNY_TOPUP_PUBLIC", "true")
	t.Setenv("ZTAPI_CNY_TOPUP_MIN_CNY", "1.00")
	t.Setenv("ZTAPI_CNY_TOPUP_MAX_CNY", "50.00")
	t.Setenv("ZTAPI_CNY_TOPUP_CNY_PER_U", "7")
	t.Setenv("ZTAPI_CNY_TOPUP_METHODS", "")
	_, ok := cnyTopUpForUser(1)
	require.False(t, ok)
	t.Setenv("ZTAPI_CNY_TOPUP_METHODS", "alipay")
	t.Setenv("ZTAPI_CNY_TOPUP_MAX_CNY", "")
	_, ok = cnyTopUpForUser(1)
	require.True(t, ok)
	router := gin.New()
	router.GET("/info", func(c *gin.Context) { c.Set("id", 1); GetCNYTopUpInfo(c) })
	router.POST("/orders", func(c *gin.Context) { c.Set("id", 1); CreateCNYTopUpOrder(c) })
	info := httptest.NewRecorder()
	router.ServeHTTP(info, httptest.NewRequest("GET", "/info", nil))
	require.Contains(t, info.Body.String(), `"methods":["alipay"]`)
	require.NotContains(t, info.Body.String(), `"max_cny"`)
	order := httptest.NewRecorder()
	router.ServeHTTP(order, httptest.NewRequest("POST", "/orders", strings.NewReader(`{"amount_cny":"1.00","method":"wxpay","idempotency_key":"fixture-key"}`)))
	require.Equal(t, 400, order.Code)
	require.Contains(t, order.Body.String(), "invalid_cny_order")
	t.Setenv("ZTAPI_CNY_TOPUP_METHODS", "alipay,unknown")
	_, ok = cnyTopUpForUser(1)
	require.False(t, ok)
}

func TestCNYNoticeAcknowledgesOnlyDurableMatchingPayment(t *testing.T) {
	const key = "fake-gateway-key-for-local-tests"
	t.Setenv("ZTAPI_CNY_TOPUP_MERCHANT", "merchant-fixture")
	t.Setenv("ZTAPI_CNY_TOPUP_APP", "app-fixture")
	t.Setenv("ZTAPI_CNY_TOPUP_KEY", key)
	t.Setenv("ZTAPI_CNY_TOPUP_CHECKOUT_HOST", "checkout.example.test")
	t.Setenv("ZTAPI_CNY_TOPUP_CALLBACK_ORIGIN", "https://cny-test.ztapi.vip")
	t.Setenv("ZTAPI_CNY_TOPUP_ENABLED", "false")
	previousDB, previousRedis := model.DB, common.RedisEnabled
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	model.DB, common.RedisEnabled = db, false
	t.Cleanup(func() { model.DB, common.RedisEnabled = previousDB, previousRedis; _ = sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.BalanceLedger{}, &model.CNYTopUpOrder{}))
	user := model.User{Username: "cny-test", Status: common.UserStatusEnabled}
	require.NoError(t, db.Create(&user).Error)
	order, _, err := model.CreateCNYTopUpOrder(context.Background(), model.CNYTopUpOrder{
		OrderNo: "CNY-fixture", UserID: user.Id, RequestKey: "request-fixture", MerchantNo: "merchant-fixture", AppID: "app-fixture",
		Method: "alipay", PayFen: 1000, Quota: 500000, CNYPerUnit: "10", CreatedAt: time.Now().Add(-time.Minute).Unix(),
	})
	require.NoError(t, err)
	router := gin.New()
	gateway, ok := service.LoadCNYGateway()
	require.True(t, ok)
	verified, checks := false, 0
	router.GET("/notify", func(c *gin.Context) {
		handleCNYTopUpNotify(c, gateway, func(_ context.Context, notice service.CNYPaymentNotice) error {
			checks++
			require.Equal(t, service.CNYTradeSuccess, notice.Status())
			require.Equal(t, "fixture-trade", notice.Evidence().TradeNo)
			if !verified {
				return errors.New("fixture upstream did not verify")
			}
			return nil
		})
	})
	router.GET("/info", func(c *gin.Context) { c.Set("id", user.Id); GetCNYTopUpInfo(c) })
	notify := func(status, paid, method string, correctSig bool) *httptest.ResponseRecorder {
		canonical := "money=" + paid + "&out_trade_no=CNY-fixture&param=CNY-fixture&pid=app-fixture&trade_no=fixture-trade&trade_status=" + status + "&type=" + method
		sign := fmt.Sprintf("%x", md5.Sum([]byte(canonical+key)))
		if !correctSig {
			sign = strings.Repeat("a", 32)
		}
		query := url.Values{"pid": {"app-fixture"}, "out_trade_no": {"CNY-fixture"}, "param": {"CNY-fixture"},
			"money": {paid}, "trade_status": {status}, "trade_no": {"fixture-trade"}, "type": {method}, "sign": {sign}, "sign_type": {"MD5"}}
		request := httptest.NewRequest("GET", "/notify?"+query.Encode(), nil)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		return response
	}
	info := httptest.NewRecorder()
	router.ServeHTTP(info, httptest.NewRequest("GET", "/info", nil))
	require.Contains(t, info.Body.String(), `"enabled":false`)
	require.Equal(t, "fail", notify(service.CNYTradeSuccess, "10.00", "alipay", true).Body.String())
	require.Zero(t, checks)
	_, err = model.RecordCNYTopUpCheckout(context.Background(), order.OrderNo, "fixture-trade", "", "https://checkout.example.test/qr/1")
	require.NoError(t, err)
	require.Equal(t, "fail", notify(service.CNYTradeSuccess, "10.00", "alipay", false).Body.String())
	require.Equal(t, "fail", notify(service.CNYTradeSuccess, "9.99", "alipay", true).Body.String())
	require.Equal(t, "fail", notify(service.CNYTradeSuccess, "10.00", "wxpay", true).Body.String())
	require.Equal(t, "success", notify("TRADE_PENDING", "10.00", "alipay", true).Body.String())
	require.Zero(t, checks)
	require.Equal(t, "fail", notify(service.CNYTradeSuccess, "10.00", "alipay", true).Body.String())
	require.Equal(t, 1, checks)
	verified = true
	require.NoError(t, db.Exec(`CREATE TRIGGER reject_credit BEFORE INSERT ON balance_ledgers BEGIN SELECT RAISE(ABORT, 'forced ledger failure'); END`).Error)
	require.Equal(t, "fail", notify(service.CNYTradeSuccess, "10.00", "alipay", true).Body.String())
	require.NoError(t, db.First(&user, user.Id).Error)
	require.Zero(t, user.Quota)
	require.NoError(t, db.Exec(`DROP TRIGGER reject_credit`).Error)
	require.Equal(t, "success", notify(service.CNYTradeSuccess, "10.00", "alipay", true).Body.String())
	require.Equal(t, "success", notify(service.CNYTradeSuccess, "10.00", "alipay", true).Body.String())
	require.Equal(t, "success", notify(service.CNYTradeRefund, "10.00", "alipay", true).Body.String())
	require.Equal(t, 3, checks)
	require.NoError(t, db.First(&user, user.Id).Error)
	require.Equal(t, int(order.Quota), user.Quota)
	var ledgerRows int64
	require.NoError(t, db.Model(&model.BalanceLedger{}).Where("user_id = ?", user.Id).Count(&ledgerRows).Error)
	require.EqualValues(t, 1, ledgerRows)
	require.NoError(t, db.First(&order, order.ID).Error)
	require.Equal(t, model.CNYTopUpPaid, order.State)
	require.Empty(t, order.CheckoutQR)
	require.Positive(t, order.RefundNoticeAt)
}
