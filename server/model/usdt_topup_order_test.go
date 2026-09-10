package model

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestUSDTTopUpExactAmountUsesIntegerMicros(t *testing.T) {
	order := USDTTopUpOrder{CreditUnits: 10, SuffixCents: 37}

	require.Equal(t, int64(10_370_000), order.ExactPayAmountMicros())
	require.Equal(t, "10.37", order.ExactPayAmountString())
}

func TestUSDTTopUpExactAmountRejectsInvalidValues(t *testing.T) {
	for _, order := range []USDTTopUpOrder{
		{CreditUnits: 9, SuffixCents: 37},
		{CreditUnits: 10, SuffixCents: 0},
		{CreditUnits: 10, SuffixCents: 100},
	} {
		_, err := order.ValidateExactPayAmount(10)
		require.Error(t, err)
	}
}

func TestCreateUSDTTopUpOrderReservesSuffixAndLinkedTopUpAtomically(t *testing.T) {
	db := setupUSDTTopUpOrderTestDB(t)
	now := time.Date(2026, time.August, 26, 8, 0, 0, 0, time.UTC)

	order, err := CreateUSDTTopUpOrder(7, 10, now, testUSDTTopUpConfig())

	require.NoError(t, err)
	require.GreaterOrEqual(t, order.SuffixCents, 1)
	require.LessOrEqual(t, order.SuffixCents, 99)
	require.Equal(t, int64(10_000_000+order.SuffixCents*10_000), order.PayAmountMicros)
	require.Equal(t, now.Add(10*time.Minute).Unix(), order.ExpiresAt)
	require.Equal(t, now.Add(24*time.Hour).Unix(), order.CooldownUntil)
	var topUp TopUp
	require.NoError(t, db.First(&topUp, order.TopUpID).Error)
	require.Equal(t, order.TradeNo, topUp.TradeNo)
	require.Equal(t, PaymentProviderUSDTTRC20, topUp.PaymentProvider)
	require.Equal(t, PaymentMethodUSDTTRC20, topUp.PaymentMethod)
	require.Equal(t, common.TopUpStatusPending, topUp.Status)
}

func TestCreateUSDTTopUpOrderRollsBackLinkedTopUpWhenProviderInsertFails(t *testing.T) {
	db := setupUSDTTopUpOrderTestDB(t)
	require.NoError(t, db.Exec(`CREATE TRIGGER reject_usdt_order BEFORE INSERT ON usdt_top_up_orders BEGIN SELECT RAISE(ABORT, 'forced provider failure'); END`).Error)

	_, err := CreateUSDTTopUpOrder(7, 10, time.Now().UTC(), testUSDTTopUpConfig())

	require.Error(t, err)
	var topUpCount int64
	require.NoError(t, db.Model(&TopUp{}).Count(&topUpCount).Error)
	require.Zero(t, topUpCount)
}

func TestCreateUSDTTopUpOrderReturnsCapacityAfterNinetyNineReservations(t *testing.T) {
	db := setupUSDTTopUpOrderTestDB(t)
	now := time.Date(2026, time.August, 26, 8, 0, 0, 0, time.UTC)
	seedReservedUSDTSuffixes(t, db, 10, now.Add(time.Hour).Unix())

	_, err := CreateUSDTTopUpOrder(7, 10, now, testUSDTTopUpConfig())

	require.ErrorIs(t, err, ErrUSDTTopUpCapacity)
}

func TestUSDTTopUpOwnershipCancellationExpiryAndRecovery(t *testing.T) {
	db := setupUSDTTopUpOrderTestDB(t)
	now := time.Date(2026, time.August, 26, 8, 0, 0, 0, time.UTC)
	order, err := CreateUSDTTopUpOrder(7, 10, now, testUSDTTopUpConfig())
	require.NoError(t, err)

	_, err = GetUserUSDTTopUpOrder(8, order.TradeNo)
	require.ErrorIs(t, err, ErrUSDTTopUpNotFound)

	pending, err := CountPendingUSDTTopUpOrders(now.Add(time.Minute))
	require.NoError(t, err)
	require.Equal(t, int64(1), pending)

	cooldownUntil := order.CooldownUntil
	cancelled, err := CancelUSDTTopUpOrder(7, order.TradeNo, now.Add(2*time.Minute))
	require.NoError(t, err)
	require.Equal(t, USDTTopUpStatusExpired, cancelled.Status)
	require.Equal(t, cooldownUntil, cancelled.CooldownUntil)

	var topUp TopUp
	require.NoError(t, db.First(&topUp, cancelled.TopUpID).Error)
	require.Equal(t, common.TopUpStatusExpired, topUp.Status)
	pending, err = CountPendingUSDTTopUpOrders(now.Add(3 * time.Minute))
	require.NoError(t, err)
	require.Zero(t, pending)

	second, err := CreateUSDTTopUpOrder(7, 11, now, testUSDTTopUpConfig())
	require.NoError(t, err)
	expired, err := ExpireUSDTTopUpOrders(now.Add(11 * time.Minute))
	require.NoError(t, err)
	require.Equal(t, int64(1), expired)
	reloaded, err := GetUserUSDTTopUpOrder(7, second.TradeNo)
	require.NoError(t, err)
	require.Equal(t, USDTTopUpStatusExpired, reloaded.Status)
}

func TestAdvanceUSDTTopUpExpiryStatesUsesConfirmationGrace(t *testing.T) {
	db := setupUSDTTopUpOrderTestDB(t)
	createdAt := time.Date(2026, time.August, 26, 8, 0, 0, 0, time.UTC)
	order, err := CreateUSDTTopUpOrder(7, 10, createdAt, testUSDTTopUpConfig())
	require.NoError(t, err)

	confirming, expired, err := AdvanceUSDTTopUpExpiryStates(createdAt.Add(10*time.Minute+time.Second), 2*time.Minute)
	require.NoError(t, err)
	require.Equal(t, int64(1), confirming)
	require.Equal(t, int64(0), expired)
	var duringGrace USDTTopUpOrder
	require.NoError(t, db.First(&duringGrace, order.ID).Error)
	require.Equal(t, USDTTopUpStatusConfirming, duringGrace.Status)
	var pendingTopUp TopUp
	require.NoError(t, db.First(&pendingTopUp, order.TopUpID).Error)
	require.Equal(t, common.TopUpStatusPending, pendingTopUp.Status)

	confirming, expired, err = AdvanceUSDTTopUpExpiryStates(createdAt.Add(12*time.Minute+time.Second), 2*time.Minute)
	require.NoError(t, err)
	require.Equal(t, int64(0), confirming)
	require.Equal(t, int64(1), expired)
	var afterGrace USDTTopUpOrder
	require.NoError(t, db.First(&afterGrace, order.ID).Error)
	require.Equal(t, USDTTopUpStatusExpired, afterGrace.Status)
	var expiredTopUp TopUp
	require.NoError(t, db.First(&expiredTopUp, order.TopUpID).Error)
	require.Equal(t, common.TopUpStatusExpired, expiredTopUp.Status)
}

func setupUSDTTopUpOrderTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "usdt-topup.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&TopUp{}, &USDTTopUpOrder{}, &USDTTopUpAmountLock{}))
	previousDB := DB
	previousUsingSQLite := common.UsingSQLite
	DB = db
	common.UsingSQLite = true
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() {
		DB = previousDB
		common.UsingSQLite = previousUsingSQLite
		_ = sqlDB.Close()
	})
	return db
}

func testUSDTTopUpConfig() setting.USDTTopUpConfig {
	return setting.USDTTopUpConfig{
		Enabled:          true,
		ReceivingAddress: "T111111111111111111111111111111111",
		TronGridAPIKey:   "test-only-key",
		MinTopUp:         10,
		OrderTTL:         10 * time.Minute,
		PollInterval:     5 * time.Second,
		SuffixCooldown:   24 * time.Hour,
	}
}

func seedReservedUSDTSuffixes(t *testing.T, db *gorm.DB, creditUnits int64, cooldownUntil int64) {
	t.Helper()
	for suffix := 1; suffix <= 99; suffix++ {
		tradeNo := fmt.Sprintf("seed-%d-%d", creditUnits, suffix)
		topUp := TopUp{UserId: 99, Amount: creditUnits, TradeNo: tradeNo, PaymentMethod: PaymentMethodUSDTTRC20, PaymentProvider: PaymentProviderUSDTTRC20, Status: common.TopUpStatusPending}
		require.NoError(t, db.Create(&topUp).Error)
		order := USDTTopUpOrder{
			TopUpID: topUp.Id, UserID: 99, TradeNo: tradeNo, Network: USDTTopUpNetworkTronMainnet,
			Asset: USDTTopUpAssetUSDT, ContractAddress: USDTTopUpContractAddress,
			ReceivingAddress: "T111111111111111111111111111111111", CreditUnits: creditUnits,
			PayAmountMicros: creditUnits*1_000_000 + int64(suffix)*10_000, SuffixCents: suffix,
			Status: USDTTopUpStatusPending, ExpiresAt: cooldownUntil, CooldownUntil: cooldownUntil,
			CreatedAt: cooldownUntil - 1, UpdatedAt: cooldownUntil - 1,
		}
		require.NoError(t, db.Create(&order).Error)
	}
}
