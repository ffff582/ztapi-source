package model

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestUSDTSettlementCreditsBalanceAndLedgerExactlyOnce(t *testing.T) {
	db, user, order, transfer, settledAt := setupUSDTSettlementTest(t, "usdt-settle-once")

	result, err := SettleUSDTTopUp(transfer, settledAt)

	require.NoError(t, err)
	require.True(t, result.Applied)
	require.Equal(t, order.ID, result.Order.ID)
	require.Equal(t, transfer.TxID, dereferenceString(result.Order.TxID))
	require.Equal(t, adminTopUpQuota(&TopUp{Amount: order.CreditUnits, PaymentProvider: PaymentProviderUSDTTRC20}), result.QuotaAdded)

	assertUSDTSettlementPersisted(t, db, user, order, transfer, result.QuotaAdded)

	replayed, err := SettleUSDTTopUp(transfer, settledAt.Add(time.Second))
	require.NoError(t, err)
	require.False(t, replayed.Applied)
	require.Equal(t, result.Ledger.ID, replayed.Ledger.ID)
	assertUSDTSettlementPersisted(t, db, user, order, transfer, result.QuotaAdded)
}

func TestUSDTSettlementRollsBackEveryMutationWhenLedgerInsertFails(t *testing.T) {
	db, user, order, transfer, settledAt := setupUSDTSettlementTest(t, "usdt-settle-rollback")
	require.NoError(t, db.Exec(`CREATE TRIGGER reject_usdt_ledger BEFORE INSERT ON balance_ledgers BEGIN SELECT RAISE(ABORT, 'forced settlement ledger failure'); END`).Error)

	_, err := SettleUSDTTopUp(transfer, settledAt)

	require.Error(t, err)
	var reloadedUser User
	require.NoError(t, db.First(&reloadedUser, user.Id).Error)
	require.Zero(t, reloadedUser.Quota)
	var reloadedOrder USDTTopUpOrder
	require.NoError(t, db.First(&reloadedOrder, order.ID).Error)
	require.Equal(t, USDTTopUpStatusPending, reloadedOrder.Status)
	require.Nil(t, reloadedOrder.TxID)
	var topUp TopUp
	require.NoError(t, db.First(&topUp, order.TopUpID).Error)
	require.Equal(t, common.TopUpStatusPending, topUp.Status)
	var ledgerCount int64
	require.NoError(t, db.Model(&BalanceLedger{}).Count(&ledgerCount).Error)
	require.Zero(t, ledgerCount)
}

func TestUSDTSettlementRejectsTransferOutsideOrderEvidence(t *testing.T) {
	db, user, order, transfer, settledAt := setupUSDTSettlementTest(t, "usdt-settle-invalid")
	transfer.AmountMicros++

	_, err := SettleUSDTTopUp(transfer, settledAt)

	require.ErrorIs(t, err, ErrUSDTTopUpTransferMismatch)
	var reloadedUser User
	require.NoError(t, db.First(&reloadedUser, user.Id).Error)
	require.Zero(t, reloadedUser.Quota)
	var reloadedOrder USDTTopUpOrder
	require.NoError(t, db.First(&reloadedOrder, order.ID).Error)
	require.Equal(t, USDTTopUpStatusPending, reloadedOrder.Status)
}

func TestUSDTSettlementCacheFailureIsPostCommitAndRetrySafe(t *testing.T) {
	db, user, order, transfer, settledAt := setupUSDTSettlementTest(t, "usdt-settle-cache")
	originalSync := balanceLedgerCacheSync
	balanceLedgerCacheSync = func(int) error { return errors.New("synthetic cache outage") }
	t.Cleanup(func() { balanceLedgerCacheSync = originalSync })

	result, err := SettleUSDTTopUp(transfer, settledAt)

	require.ErrorIs(t, err, ErrUSDTTopUpCacheSync)
	require.NotNil(t, result)
	require.True(t, result.Applied)
	assertUSDTSettlementPersisted(t, db, user, order, transfer, result.QuotaAdded)

	balanceLedgerCacheSync = func(int) error { return nil }
	replayed, err := SettleUSDTTopUp(transfer, settledAt.Add(time.Second))
	require.NoError(t, err)
	require.False(t, replayed.Applied)
	assertUSDTSettlementPersisted(t, db, user, order, transfer, result.QuotaAdded)
}

func TestUSDTSettlementAcceptsInWindowTransferWhileOrderIsConfirming(t *testing.T) {
	db, user, order, transfer, settledAt := setupUSDTSettlementTest(t, "usdt-settle-confirming")
	require.NoError(t, db.Model(&USDTTopUpOrder{}).Where("id = ?", order.ID).Update("status", USDTTopUpStatusConfirming).Error)

	result, err := SettleUSDTTopUp(transfer, settledAt.Add(11*time.Minute))

	require.NoError(t, err)
	require.True(t, result.Applied)
	assertUSDTSettlementPersisted(t, db, user, order, transfer, result.QuotaAdded)
}

func setupUSDTSettlementTest(t *testing.T, username string) (*gorm.DB, User, *USDTTopUpOrder, TRC20Transfer, time.Time) {
	t.Helper()
	db := setupBalanceLedgerTestDB(t)
	require.NoError(t, db.AutoMigrate(&TopUp{}, &USDTTopUpOrder{}, &USDTTopUpAmountLock{}))
	user := createBalanceLedgerTestUser(t, db, username, 0)
	createdAt := time.Date(2026, time.August, 26, 9, 0, 0, 0, time.UTC)
	order, err := CreateUSDTTopUpOrder(user.Id, 10, createdAt, testUSDTTopUpConfig())
	require.NoError(t, err)
	transfer := TRC20Transfer{
		TxID:             fmt.Sprintf("tx-%s", username),
		From:             "TSender1111111111111111111111111111",
		To:               order.ReceivingAddress,
		ContractAddress:  order.ContractAddress,
		AmountMicros:     order.PayAmountMicros,
		BlockTimestampMS: createdAt.Add(time.Second).UnixMilli(),
	}
	return db, user, order, transfer, createdAt.Add(2 * time.Second)
}

func assertUSDTSettlementPersisted(t *testing.T, db *gorm.DB, user User, order *USDTTopUpOrder, transfer TRC20Transfer, quotaAdded int64) {
	t.Helper()
	var reloadedUser User
	require.NoError(t, db.First(&reloadedUser, user.Id).Error)
	require.Equal(t, int(quotaAdded), reloadedUser.Quota)
	var reloadedOrder USDTTopUpOrder
	require.NoError(t, db.First(&reloadedOrder, order.ID).Error)
	require.Equal(t, USDTTopUpStatusSettled, reloadedOrder.Status)
	require.Equal(t, transfer.TxID, dereferenceString(reloadedOrder.TxID))
	var topUp TopUp
	require.NoError(t, db.First(&topUp, order.TopUpID).Error)
	require.Equal(t, common.TopUpStatusSuccess, topUp.Status)
	var ledgers []BalanceLedger
	require.NoError(t, db.Find(&ledgers).Error)
	require.Len(t, ledgers, 1)
	require.Equal(t, "usdt-trc20:"+transfer.TxID, ledgers[0].IdempotencyKey)
	require.Equal(t, quotaAdded, ledgers[0].Delta)
}

func dereferenceString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
