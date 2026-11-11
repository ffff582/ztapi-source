package controller

import (
	"context"
	"crypto/md5"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

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
	router.POST("/notify", CNYTopUpNotify)
	router.GET("/info", func(c *gin.Context) { c.Set("id", user.Id); GetCNYTopUpInfo(c) })
	notify := func(status, paid string, correctSig bool) *httptest.ResponseRecorder {
		canonical := "appId=app-fixture&mchNo=merchant-fixture&mchOrderNo=CNY-fixture&orderAmount=10.00&param=CNY-fixture&payAmount=" + paid + "&payStatus=" + status + "&tradeNo=fixture-trade"
		sign := fmt.Sprintf("%x", md5.Sum([]byte(canonical+key)))
		if !correctSig {
			sign = strings.Repeat("a", 32)
		}
		body := fmt.Sprintf(`{"appId":"app-fixture","mchNo":"merchant-fixture","mchOrderNo":"CNY-fixture","orderAmount":"10.00","param":"CNY-fixture","payAmount":%q,"payStatus":%q,"tradeNo":"fixture-trade","sign":%q,"sign_type":"MD5"}`, paid, status, sign)
		request := httptest.NewRequest("POST", "/notify", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json; charset=utf-8")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		return response
	}
	info := httptest.NewRecorder()
	router.ServeHTTP(info, httptest.NewRequest("GET", "/info", nil))
	require.Contains(t, info.Body.String(), `"enabled":false`)
	require.Equal(t, "fail", notify("2", "10.00", false).Body.String())
	require.Equal(t, "fail", notify("2", "9.99", true).Body.String())
	require.NoError(t, db.Exec(`CREATE TRIGGER reject_credit BEFORE INSERT ON balance_ledgers BEGIN SELECT RAISE(ABORT, 'forced ledger failure'); END`).Error)
	require.Equal(t, "fail", notify("2", "10.00", true).Body.String())
	require.NoError(t, db.First(&user, user.Id).Error)
	require.Zero(t, user.Quota)
	require.NoError(t, db.Exec(`DROP TRIGGER reject_credit`).Error)
	require.Equal(t, "success", notify("2", "10.00", true).Body.String())
	require.Equal(t, "success", notify("2", "10.00", true).Body.String())
	require.Equal(t, "success", notify("3", "10.00", true).Body.String())
	require.NoError(t, db.First(&user, user.Id).Error)
	require.Equal(t, int(order.Quota), user.Quota)
	require.NoError(t, db.First(&order, order.ID).Error)
	require.Equal(t, model.CNYTopUpPaid, order.State)
	require.Positive(t, order.RefundNoticeAt)
}
