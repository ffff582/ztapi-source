package model

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

func TestUSDTConcurrentSuffixAllocationMySQL(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("ZTAPI_USDT_MYSQL_TEST_DSN"))
	required := strings.EqualFold(strings.TrimSpace(os.Getenv("ZTAPI_REQUIRE_MYSQL_INTEGRATION")), "true")
	if dsn == "" {
		if required {
			t.Fatal("ZTAPI_USDT_MYSQL_TEST_DSN is required when ZTAPI_REQUIRE_MYSQL_INTEGRATION=true")
		}
		t.Skip("ZTAPI_USDT_MYSQL_TEST_DSN is not configured")
	}

	safeDSN, _, err := validateBalanceLedgerMySQLTestDSN(dsn, "")
	require.NoError(t, err)
	db, err := gorm.Open(mysql.Open(safeDSN), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&TopUp{}, &USDTTopUpOrder{}, &USDTTopUpAmountLock{}))
	previousDB := DB
	previousUsingSQLite := common.UsingSQLite
	DB = db
	common.UsingSQLite = false
	sqlDB, err := db.DB()
	require.NoError(t, err)
	baseUserID := int(time.Now().Unix()%1_000_000)*100 + 100_000
	t.Cleanup(func() {
		_ = db.Where("user_id >= ? AND user_id < ?", baseUserID, baseUserID+20).Delete(&USDTTopUpOrder{}).Error
		_ = db.Where("user_id >= ? AND user_id < ?", baseUserID, baseUserID+20).Delete(&TopUp{}).Error
		_ = db.Where("credit_units = ?", 10).Delete(&USDTTopUpAmountLock{}).Error
		DB = previousDB
		common.UsingSQLite = previousUsingSQLite
		_ = sqlDB.Close()
	})

	config := testUSDTTopUpConfig()
	now := time.Now().UTC()
	const callers = 20
	orders := make(chan *USDTTopUpOrder, callers)
	errs := make(chan error, callers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(userID int) {
			defer wg.Done()
			<-start
			order, createErr := CreateUSDTTopUpOrder(userID, 10, now, config)
			if createErr != nil {
				errs <- createErr
				return
			}
			orders <- order
		}(baseUserID + i)
	}
	close(start)
	wg.Wait()
	close(orders)
	close(errs)
	for createErr := range errs {
		require.NoError(t, createErr)
	}

	suffixes := make(map[int]struct{}, callers)
	for order := range orders {
		suffixes[order.SuffixCents] = struct{}{}
	}
	require.Len(t, suffixes, callers)
	var linkedTopUps int64
	require.NoError(t, db.Model(&TopUp{}).Where("user_id >= ? AND user_id < ?", baseUserID, baseUserID+20).Count(&linkedTopUps).Error)
	require.Equal(t, int64(callers), linkedTopUps)
}

func TestUSDTConcurrentSettlementMySQL(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("ZTAPI_USDT_MYSQL_TEST_DSN"))
	required := strings.EqualFold(strings.TrimSpace(os.Getenv("ZTAPI_REQUIRE_MYSQL_INTEGRATION")), "true")
	if dsn == "" {
		if required {
			t.Fatal("ZTAPI_USDT_MYSQL_TEST_DSN is required when ZTAPI_REQUIRE_MYSQL_INTEGRATION=true")
		}
		t.Skip("ZTAPI_USDT_MYSQL_TEST_DSN is not configured")
	}

	safeDSN, _, err := validateBalanceLedgerMySQLTestDSN(dsn, "")
	require.NoError(t, err)
	db, err := gorm.Open(mysql.Open(safeDSN), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&User{}, &BalanceLedger{}, &TopUp{}, &USDTTopUpOrder{}, &USDTTopUpAmountLock{}))
	require.NoError(t, registerBalanceLedgerImmutability(db))
	previousDB := DB
	previousSQLite := common.UsingSQLite
	previousMySQL := common.UsingMySQL
	previousRedis := common.RedisEnabled
	DB = db
	common.UsingSQLite = false
	common.UsingMySQL = true
	common.RedisEnabled = false
	sqlDB, err := db.DB()
	require.NoError(t, err)
	unique := time.Now().UnixNano()
	username := fmt.Sprintf("usdtmysql%d", unique%1_000_000_000)
	user := User{Username: username, Password: "not-used", Status: common.UserStatusEnabled, Role: common.RoleCommonUser, AffCode: username + "-aff"}
	require.NoError(t, db.Create(&user).Error)
	t.Cleanup(func() {
		_ = db.Exec("DELETE FROM balance_ledgers WHERE user_id = ?", user.Id).Error
		_ = db.Exec("DELETE FROM usdt_top_up_orders WHERE user_id = ?", user.Id).Error
		_ = db.Exec("DELETE FROM top_ups WHERE user_id = ?", user.Id).Error
		_ = db.Exec("DELETE FROM users WHERE id = ?", user.Id).Error
		_ = db.Exec("DELETE FROM usdt_top_up_amount_locks WHERE credit_units = ?", 10).Error
		DB = previousDB
		common.UsingSQLite = previousSQLite
		common.UsingMySQL = previousMySQL
		common.RedisEnabled = previousRedis
		_ = sqlDB.Close()
	})

	createdAt := time.Now().UTC().Truncate(time.Second)
	order, err := CreateUSDTTopUpOrder(user.Id, 10, createdAt, testUSDTTopUpConfig())
	require.NoError(t, err)
	transfer := TRC20Transfer{
		TxID: fmt.Sprintf("mysql-settlement-%d", unique), From: "TSender1111111111111111111111111111",
		To: order.ReceivingAddress, ContractAddress: order.ContractAddress,
		AmountMicros: order.PayAmountMicros, BlockTimestampMS: createdAt.Add(time.Second).UnixMilli(),
	}

	const callers = 20
	start := make(chan struct{})
	results := make(chan *USDTSettlementResult, callers)
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			result, settleErr := SettleUSDTTopUp(transfer, createdAt.Add(2*time.Second))
			if settleErr != nil {
				errs <- settleErr
				return
			}
			results <- result
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)
	for settleErr := range errs {
		require.NoError(t, settleErr)
	}
	applied := 0
	for result := range results {
		if result.Applied {
			applied++
		}
	}
	require.Equal(t, 1, applied)
	var reloaded User
	require.NoError(t, db.First(&reloaded, user.Id).Error)
	expectedQuota := adminTopUpQuota(&TopUp{Amount: 10, PaymentProvider: PaymentProviderUSDTTRC20})
	require.Equal(t, int(expectedQuota), reloaded.Quota)
	var ledgerCount int64
	require.NoError(t, db.Model(&BalanceLedger{}).Where("user_id = ?", user.Id).Count(&ledgerCount).Error)
	require.Equal(t, int64(1), ledgerCount)
}

func TestUSDTConfirmationGraceSettlementRaceMySQL(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("ZTAPI_USDT_MYSQL_TEST_DSN"))
	required := strings.EqualFold(strings.TrimSpace(os.Getenv("ZTAPI_REQUIRE_MYSQL_INTEGRATION")), "true")
	if dsn == "" {
		if required {
			t.Fatal("ZTAPI_USDT_MYSQL_TEST_DSN is required when ZTAPI_REQUIRE_MYSQL_INTEGRATION=true")
		}
		t.Skip("ZTAPI_USDT_MYSQL_TEST_DSN is not configured")
	}

	safeDSN, _, err := validateBalanceLedgerMySQLTestDSN(dsn, "")
	require.NoError(t, err)
	db, err := gorm.Open(mysql.Open(safeDSN), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&User{}, &BalanceLedger{}, &TopUp{}, &USDTTopUpOrder{}, &USDTTopUpAmountLock{}))
	require.NoError(t, registerBalanceLedgerImmutability(db))
	previousDB := DB
	previousSQLite := common.UsingSQLite
	previousMySQL := common.UsingMySQL
	previousRedis := common.RedisEnabled
	DB = db
	common.UsingSQLite = false
	common.UsingMySQL = true
	common.RedisEnabled = false
	sqlDB, err := db.DB()
	require.NoError(t, err)
	unique := time.Now().UnixNano()
	username := fmt.Sprintf("usdtgrace%d", unique%1_000_000_000)
	user := User{Username: username, Password: "not-used", Status: common.UserStatusEnabled, Role: common.RoleCommonUser, AffCode: username + "-aff"}
	require.NoError(t, db.Create(&user).Error)
	t.Cleanup(func() {
		_ = db.Exec("DELETE FROM balance_ledgers WHERE user_id = ?", user.Id).Error
		_ = db.Exec("DELETE FROM usdt_top_up_orders WHERE user_id = ?", user.Id).Error
		_ = db.Exec("DELETE FROM top_ups WHERE user_id = ?", user.Id).Error
		_ = db.Exec("DELETE FROM users WHERE id = ?", user.Id).Error
		_ = db.Exec("DELETE FROM usdt_top_up_amount_locks WHERE credit_units IN ?", []int64{10, 11}).Error
		DB = previousDB
		common.UsingSQLite = previousSQLite
		common.UsingMySQL = previousMySQL
		common.RedisEnabled = previousRedis
		_ = sqlDB.Close()
	})

	createdAt := time.Now().UTC().Truncate(time.Second)
	order, err := CreateUSDTTopUpOrder(user.Id, 10, createdAt, testUSDTTopUpConfig())
	require.NoError(t, err)
	transfer := TRC20Transfer{
		TxID: fmt.Sprintf("mysql-grace-%d", unique), From: "TSender1111111111111111111111111111",
		To: order.ReceivingAddress, ContractAddress: order.ContractAddress,
		AmountMicros: order.PayAmountMicros, BlockTimestampMS: createdAt.Add(9*time.Minute + 59*time.Second).UnixMilli(),
	}

	start := make(chan struct{})
	var transitionErr error
	var settlementErr error
	var settlement *USDTSettlementResult
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		_, _, transitionErr = AdvanceUSDTTopUpExpiryStates(createdAt.Add(10*time.Minute+30*time.Second), 2*time.Minute)
	}()
	go func() {
		defer wg.Done()
		<-start
		settlement, settlementErr = SettleUSDTTopUp(transfer, createdAt.Add(10*time.Minute+30*time.Second))
	}()
	close(start)
	wg.Wait()

	require.NoError(t, transitionErr)
	require.NoError(t, settlementErr)
	require.NotNil(t, settlement)
	require.True(t, settlement.Applied)
	settled, err := GetUserUSDTTopUpOrder(user.Id, order.TradeNo)
	require.NoError(t, err)
	require.Equal(t, USDTTopUpStatusSettled, settled.Status)
	var ledgerCount int64
	require.NoError(t, db.Model(&BalanceLedger{}).Where("user_id = ?", user.Id).Count(&ledgerCount).Error)
	require.Equal(t, int64(1), ledgerCount)

	expiringOrder, err := CreateUSDTTopUpOrder(user.Id, 11, createdAt, testUSDTTopUpConfig())
	require.NoError(t, err)
	confirming, expired, err := AdvanceUSDTTopUpExpiryStates(createdAt.Add(10*time.Minute+30*time.Second), 2*time.Minute)
	require.NoError(t, err)
	require.Equal(t, int64(1), confirming)
	require.Equal(t, int64(0), expired)
	confirming, expired, err = AdvanceUSDTTopUpExpiryStates(createdAt.Add(12*time.Minute+time.Second), 2*time.Minute)
	require.NoError(t, err)
	require.Equal(t, int64(0), confirming)
	require.Equal(t, int64(1), expired)
	finalOrder, err := GetUserUSDTTopUpOrder(user.Id, expiringOrder.TradeNo)
	require.NoError(t, err)
	require.Equal(t, USDTTopUpStatusExpired, finalOrder.Status)
	var linkedTopUp TopUp
	require.NoError(t, db.First(&linkedTopUp, expiringOrder.TopUpID).Error)
	require.Equal(t, common.TopUpStatusExpired, linkedTopUp.Status)
}
