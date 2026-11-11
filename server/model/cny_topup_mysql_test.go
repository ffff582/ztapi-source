package model

import (
	"context"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

func TestCNYTopUpConcurrentSettlementMySQL(t *testing.T) {
	dsn := os.Getenv("ZTAPI_CNY_MYSQL_TEST_DSN")
	if dsn == "" {
		if os.Getenv("ZTAPI_REQUIRE_MYSQL_INTEGRATION") == "true" {
			t.Fatal("ZTAPI_CNY_MYSQL_TEST_DSN is required")
		}
		t.Skip("no isolated CNY MySQL test database configured")
	}
	safe, _, err := validateBalanceLedgerMySQLTestDSN(dsn, "")
	require.NoError(t, err)
	db, err := gorm.Open(mysql.Open(safe), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	previousDB, previousRedis := DB, common.RedisEnabled
	DB, common.RedisEnabled = db, false
	t.Cleanup(func() { DB, common.RedisEnabled = previousDB, previousRedis; _ = sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(&User{}, &CNYTopUpOrder{}, &BalanceLedger{}))
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	now := time.Now().UTC()
	suffix := fmt.Sprint(now.UnixNano())
	user := createBalanceLedgerTestUser(t, db, "cny-mysql-"+suffix[len(suffix)-9:], 0)
	input := CNYTopUpOrder{UserID: user.Id, RequestKey: "request-" + suffix, MerchantNo: "fixture", AppID: "fixture",
		Method: "wxpay", PayFen: 789, Quota: 500000, CNYPerUnit: "7.89", CreatedAt: now.Unix()}
	var created atomic.Int64
	const callers = 8
	parallel := func(f func(int) error) {
		t.Helper()
		var wg sync.WaitGroup
		errList := make(chan error, callers)
		start := make(chan struct{})
		for i := 0; i < callers; i++ {
			wg.Add(1)
			go func(n int) { defer wg.Done(); <-start; errList <- f(n) }(i)
		}
		close(start)
		wg.Wait()
		close(errList)
		for err := range errList {
			require.NoError(t, err)
		}
	}
	parallel(func(i int) error {
		candidate := input
		candidate.OrderNo = fmt.Sprintf("cny-%s-%d", suffix, i)
		_, fresh, err := CreateCNYTopUpOrder(ctx, candidate)
		if fresh {
			created.Add(1)
		}
		return err
	})
	require.EqualValues(t, 1, created.Load())
	var order CNYTopUpOrder
	require.NoError(t, db.Where("request_key = ?", input.RequestKey).First(&order).Error)
	proof := CNYPaymentEvidence{OrderNo: order.OrderNo, MerchantNo: input.MerchantNo, AppID: input.AppID, TradeNo: "trade-" + suffix,
		OrderFen: order.PayFen, PaidFen: order.PayFen}
	parallel(func(int) error { _, err := SettleCNYTopUp(ctx, proof, now); return err })
	require.NoError(t, db.First(&user, user.Id).Error)
	require.EqualValues(t, order.Quota, user.Quota)
	var count int64
	require.NoError(t, db.Model(&BalanceLedger{}).Where("request_id = ?", order.OrderNo).Count(&count).Error)
	require.EqualValues(t, 1, count)
	parallel(func(int) error { _, err := HoldCNYTopUpRefund(ctx, order.OrderNo, 1, now); return err })
	require.NoError(t, db.First(&user, user.Id).Error)
	require.Zero(t, user.Quota)
	require.NoError(t, db.Model(&BalanceLedger{}).Where("request_id = ?", order.OrderNo).Count(&count).Error)
	require.EqualValues(t, 2, count)
}
