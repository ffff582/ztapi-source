package model

import (
	"context"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestCNYTopUpAmountAndQuoteUseExactArithmetic(t *testing.T) {
	for _, invalid := range []string{"12.371", "12.37e0", "0.00", "-1.00", " 12.37"} {
		_, err := ParseCNYAmountFen(invalid)
		require.ErrorIs(t, err, ErrCNYTopUpEvidence, invalid)
	}
	fen, err := ParseCNYAmountFen("12.370")
	require.NoError(t, err)
	require.EqualValues(t, 1237, fen)
	quota, err := CNYTopUpQuota(fen, decimal.RequireFromString("6.67"), decimal.NewFromInt(500000))
	require.NoError(t, err)
	require.EqualValues(t, 927286, quota)
}

func cnyTestOrder(t *testing.T) (*gorm.DB, User, CNYTopUpOrder, CNYPaymentEvidence, time.Time) {
	t.Helper()
	db := setupBalanceLedgerTestDB(t)
	require.NoError(t, db.AutoMigrate(&CNYTopUpOrder{}))
	user := createBalanceLedgerTestUser(t, db, "cny-payer", 0)
	now := time.Unix(1794394000, 0).UTC()
	order := CNYTopUpOrder{OrderNo: "CNY-fake-1", UserID: user.Id, RequestKey: "test-key", MerchantNo: "fixture-merchant", AppID: "fixture-app", Method: "alipay", PayFen: 1001, Quota: 750000, CNYPerUnit: "6.673", CreatedAt: now.Unix()}
	saved, fresh, err := CreateCNYTopUpOrder(context.Background(), order)
	require.NoError(t, err)
	require.True(t, fresh)
	evidence := CNYPaymentEvidence{OrderNo: order.OrderNo, MerchantNo: order.MerchantNo, AppID: order.AppID, TradeNo: "trade-fixture-1", Method: order.Method, OrderFen: order.PayFen, PaidFen: order.PayFen}
	_, err = SettleCNYTopUp(context.Background(), evidence, now)
	require.ErrorIs(t, err, ErrCNYTopUpEvidence)
	saved, err = RecordCNYTopUpCheckout(context.Background(), saved.OrderNo, evidence.TradeNo, "https://checkout.example.test/pay/1", "")
	require.NoError(t, err)
	return db, user, saved, evidence, now
}

func TestCNYTopUpFreezesQuoteAndSettlesOnce(t *testing.T) {
	db, user, order, proof, now := cnyTestOrder(t)
	ctx := context.Background()
	changed := order
	changed.OrderNo, changed.CNYPerUnit, changed.Quota = "CNY-retry", "7.1", 700000
	got, fresh, err := CreateCNYTopUpOrder(ctx, changed)
	require.NoError(t, err)
	require.False(t, fresh)
	require.Equal(t, order.Quota, got.Quota)
	require.Equal(t, order.OrderNo, got.OrderNo)
	changed.PayFen++
	_, _, err = CreateCNYTopUpOrder(ctx, changed)
	require.ErrorIs(t, err, ErrCNYTopUpConflict)

	invalid := proof
	invalid.PaidFen--
	_, err = SettleCNYTopUp(ctx, invalid, now)
	require.ErrorIs(t, err, ErrCNYTopUpEvidence)
	settled, err := SettleCNYTopUp(ctx, proof, now.Add(time.Hour))
	require.NoError(t, err)
	require.Equal(t, CNYTopUpPaid, settled.State)
	_, err = SettleCNYTopUp(ctx, proof, now.Add(2*time.Hour))
	require.NoError(t, err)
	require.NoError(t, db.First(&user, user.Id).Error)
	require.Equal(t, int(order.Quota), user.Quota)
	var rows []BalanceLedger
	require.NoError(t, db.Find(&rows).Error)
	require.Len(t, rows, 1)
	require.Equal(t, order.Quota, rows[0].Delta)
}

func TestCNYTopUpBeforeReferralMigrationStillAcceptsCheckoutAndSettlement(t *testing.T) {
	db, _, first, proof, now := cnyTestOrder(t)
	require.NoError(t, db.Exec("ALTER TABLE cny_top_up_orders DROP COLUMN referral_policy_epoch").Error)
	t.Setenv("ZTAPI_CNY_REFERRAL_POLICY_FILE", "")
	ctx := t.Context()
	first.OrderNo, first.RequestKey = "CNY-before-referral-migration", "new-request-key"
	saved, fresh, err := CreateCNYTopUpOrder(ctx, first)
	require.NoError(t, err)
	require.True(t, fresh)
	require.Zero(t, saved.ReferralPolicyEpoch)
	proof.OrderNo, proof.TradeNo = saved.OrderNo, "trade-before-referral-migration"
	_, err = RecordCNYTopUpCheckout(ctx, saved.OrderNo, proof.TradeNo, "https://checkout.example.test/pay/2", "")
	require.NoError(t, err)
	for range 2 {
		settled, err := SettleCNYTopUp(ctx, proof, now.Add(time.Minute))
		require.NoError(t, err)
		require.Equal(t, CNYTopUpPaid, settled.State)
	}
	var count int64
	require.NoError(t, db.Model(&BalanceLedger{}).Where("request_id = ?", saved.OrderNo).Count(&count).Error)
	require.EqualValues(t, 1, count)
}

func TestCNYTopUpRespectsAvailableLedgerCapacityBeforeCheckout(t *testing.T) {
	db, user, pending, _, _ := cnyTestOrder(t)
	ctx := context.Background()
	remaining := int64(10000)
	require.NoError(t, db.Model(&User{}).Where("id = ?", user.Id).Update("quota", maxBalanceLedgerQuota-pending.Quota-remaining).Error)
	retry, fresh, err := CreateCNYTopUpOrder(ctx, pending)
	require.NoError(t, err)
	require.False(t, fresh)
	require.Equal(t, pending.OrderNo, retry.OrderNo)
	next := pending
	next.OrderNo, next.RequestKey, next.Quota = "CNY-over", "test-over", remaining+1
	_, _, err = CreateCNYTopUpOrder(ctx, next)
	require.ErrorIs(t, err, ErrBalanceLedgerOverflow)
	next.Quota = remaining
	_, fresh, err = CreateCNYTopUpOrder(ctx, next)
	require.NoError(t, err)
	require.True(t, fresh)
}

func TestCNYTopUpCommitFailureLeavesNoCreditOrPaidOrder(t *testing.T) {
	db, user, order, proof, now := cnyTestOrder(t)
	require.NoError(t, db.Exec(`CREATE TRIGGER reject_cny_ledger BEFORE INSERT ON balance_ledgers BEGIN SELECT RAISE(ABORT, 'forced write failure'); END`).Error)
	_, err := SettleCNYTopUp(context.Background(), proof, now)
	require.Error(t, err)
	require.NoError(t, db.First(&user, user.Id).Error)
	require.Zero(t, user.Quota)
	require.NoError(t, db.First(&order, order.ID).Error)
	require.Equal(t, CNYTopUpPending, order.State)
	require.NotNil(t, order.ProviderTradeNo)
	require.Equal(t, proof.TradeNo, *order.ProviderTradeNo)
	require.Zero(t, order.PaidAt)
}

func TestCNYTopUpRefundHoldAndVerifiedCompletionAreIdempotent(t *testing.T) {
	db, user, order, proof, now := cnyTestOrder(t)
	ctx := context.Background()
	_, err := SettleCNYTopUp(ctx, proof, now)
	require.NoError(t, err)
	require.NoError(t, db.Model(&User{}).Where("id = ?", user.Id).Update("quota", order.Quota-1).Error)
	_, err = HoldCNYTopUpRefund(ctx, order.OrderNo, 1, now)
	require.ErrorIs(t, err, ErrBalanceLedgerNegativeBalance)
	require.NoError(t, db.Model(&User{}).Where("id = ?", user.Id).Update("quota", order.Quota).Error)
	_, err = HoldCNYTopUpRefund(ctx, order.OrderNo, 1, now)
	require.NoError(t, err)
	_, err = HoldCNYTopUpRefund(ctx, order.OrderNo, 1, now)
	require.NoError(t, err)
	_, err = ConfirmCNYTopUpRefund(ctx, order.OrderNo, proof.TradeNo, "refund-fixture", order.PayFen-1, 1, now)
	require.ErrorIs(t, err, ErrCNYTopUpEvidence)
	confirmed, err := ConfirmCNYTopUpRefund(ctx, order.OrderNo, proof.TradeNo, "refund-fixture", order.PayFen, 1, now)
	require.NoError(t, err)
	require.Equal(t, CNYTopUpRefunded, confirmed.State)
	_, err = ConfirmCNYTopUpRefund(ctx, order.OrderNo, proof.TradeNo, "refund-fixture", order.PayFen, 1, now)
	require.NoError(t, err)
	_, err = SettleCNYTopUp(ctx, proof, now)
	require.NoError(t, err)
	require.NoError(t, db.First(&user, user.Id).Error)
	require.Zero(t, user.Quota)
	var rows []BalanceLedger
	require.NoError(t, db.Find(&rows).Error)
	require.Len(t, rows, 2)
	require.Equal(t, -order.Quota, rows[1].Delta)
}
