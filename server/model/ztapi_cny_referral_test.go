package model

import (
	"context"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestZTAPIReferralCNYMarginUsesConservativeFXAndBothFees(t *testing.T) {
	gate := referralGate()
	gate.Items[0].SupplierCostUpperBound = "80.95"
	gate.Items[0].BonusRate = "0.049"
	policy := ZTAPICNYReferralPolicy{CNYPerUnit: "7", FeeBPS: map[string]int{"alipay": 500, "wxpay": 500}}
	fx := decimal.RequireFromString("6.7174")
	margin, err := EvaluateZTAPICNYReferralMargin(gate, policy, fx)
	require.NoError(t, err)
	require.Equal(t, "0.123181", margin.StringFixed(6))
	_, err = EvaluateZTAPICNYReferralMargin(gate, policy, decimal.NewFromInt(7))
	require.ErrorIs(t, err, ErrZTAPIReferralNotReady)
	policy.FeeBPS["wxpay"] = 800
	_, err = EvaluateZTAPICNYReferralMargin(gate, policy, fx)
	require.ErrorIs(t, err, ErrZTAPIReferralNotReady)
	delete(policy.FeeBPS, "wxpay")
	_, err = EvaluateZTAPICNYReferralMargin(gate, policy, fx)
	require.ErrorIs(t, err, ErrZTAPIReferralNotReady)
}

func TestZTAPIReferralCNYActivationRejectsUnprovenFeesAndStaleMarketFX(t *testing.T) {
	now := referralTestTime.Unix()
	policy := ZTAPICNYReferralPolicy{
		MerchantNo: "merchant1", AppID: "app1", VerifiedAt: now - 900, EnabledAt: now - 800, ExpiresAt: now + 86400,
		FeeBPS: map[string]int{"alipay": 600, "wxpay": 600}, Evidence: "two sample receipts",
	}
	fx := &ZTAPIFXPolicy{CreatedAt: now, MarketCNYPerUSDT: "6.66", UpstreamCNYPerUSD: "6.7174"}
	_, err := cnyReferralReviewedFX(policy, fx, now)
	require.ErrorIs(t, err, ErrZTAPIReferralNotReady)
	policy.FeeEvidence = ZTAPICNYReferralFeeEvidence{
		MerchantNo: "merchant1", AppID: "app1", EffectiveAt: now - 1000, ExpiresAt: now + 86400,
		ScheduleReference: "signed merchant fee schedule", SettlementReference: "merchant payout terms",
	}
	policy.MarketObservedAt = now - 86401
	policy.ObservedMarketFX = "6.7"
	_, err = cnyReferralReviewedFX(policy, fx, now)
	require.ErrorIs(t, err, ErrZTAPIReferralNotReady)
	policy.MarketObservedAt = now - 3600
	policy.ObservedMarketFX = "6.75"
	rate, err := cnyReferralReviewedFX(policy, fx, now)
	require.NoError(t, err)
	require.Equal(t, "6.75", rate.String())
	policy.FeeEvidence.AppID = "wrong-app"
	_, err = cnyReferralReviewedFX(policy, fx, now)
	require.ErrorIs(t, err, ErrZTAPIReferralNotReady)
}

func TestZTAPIReferralCNYPrincipalUsesActuallyCreditedQuota(t *testing.T) {
	policy := ZTAPICNYReferralPolicy{CNYPerUnit: "7", QuotaPerUnit: 500000}
	order := CNYTopUpOrder{PayFen: 100, Quota: 71428, CNYPerUnit: "7"}
	principal, err := ztapiCNYReferralPrincipal(order, policy)
	require.NoError(t, err)
	require.EqualValues(t, 142856, principal)
	require.EqualValues(t, 7142, principal/20)
	order.Quota++
	_, err = ztapiCNYReferralPrincipal(order, policy)
	require.Error(t, err)
}

func cnyReferralFixture(t *testing.T) (*gorm.DB, ZTAPICNYReferralPolicy) {
	t.Helper()
	db := referralTestDB(t)
	require.NoError(t, db.AutoMigrate(&CNYTopUpOrder{}))
	referralEnable(t)
	referralBind(t, db, 2, 1, referralTestTime)
	return db, ZTAPICNYReferralPolicy{EnabledAt: referralTestTime.Add(time.Hour).Unix(), MerchantNo: "merchant1", AppID: "app1", CNYPerUnit: "7", QuotaPerUnit: 500000, FeeBPS: map[string]int{"alipay": 500, "wxpay": 500}}
}

func cnyReferralPaid(t *testing.T, policy ZTAPICNYReferralPolicy, method string, fen int64, created time.Time) CNYTopUpOrder {
	t.Helper()
	return cnyReferralPaidForUser(t, policy, method, fen, created, 2)
}

func cnyReferralPaidForUser(t *testing.T, policy ZTAPICNYReferralPolicy, method string, fen int64, created time.Time, userID int) CNYTopUpOrder {
	t.Helper()
	return cnyReferralTestSettle(t, policy, &policy, method, fen, created, userID)
}

func cnyReferralTestSettle(t *testing.T, policy ZTAPICNYReferralPolicy, admission *ZTAPICNYReferralPolicy, method string, fen int64, created time.Time, userID int) CNYTopUpOrder {
	t.Helper()
	ctx := context.Background()
	id := common.GetUUID()
	quota, err := CNYTopUpQuota(fen, decimal.NewFromInt(7), decimal.NewFromInt(policy.QuotaPerUnit))
	require.NoError(t, err)
	o := CNYTopUpOrder{OrderNo: "CNY-" + id, UserID: userID, RequestKey: id, MerchantNo: policy.MerchantNo, AppID: policy.AppID, Method: method, PayFen: fen, Quota: quota, CNYPerUnit: "7", CreatedAt: created.Unix()}
	_, _, err = createCNYTopUpOrder(ctx, o, func(context.Context, CNYTopUpOrder) *ZTAPICNYReferralPolicy { return admission })
	require.NoError(t, err)
	evidence := CNYPaymentEvidence{OrderNo: o.OrderNo, MerchantNo: o.MerchantNo, AppID: o.AppID, Method: method, OrderFen: fen, PaidFen: fen, TradeNo: "T" + id}
	_, err = RecordCNYTopUpCheckout(ctx, o.OrderNo, evidence.TradeNo, "https://checkout.example.test/pay", "")
	require.NoError(t, err)
	paid, err := SettleCNYTopUp(ctx, evidence, created.Add(2*time.Minute))
	require.NoError(t, err)
	return paid
}

func TestZTAPIReferralCNYNoHistoryOrDuplicateAcrossPaymentRails(t *testing.T) {
	db, policy := cnyReferralFixture(t)
	ctx := context.Background()
	start := time.Unix(policy.EnabledAt, 0)
	old := cnyReferralPaid(t, policy, "alipay", 100, start.Add(-time.Minute))
	closed := cnyReferralTestSettle(t, policy, nil, "alipay", 100, start.Add(time.Second), 2)
	alipay := cnyReferralPaid(t, policy, "alipay", 100, start.Add(time.Minute))
	wx := cnyReferralPaid(t, policy, "wxpay", 100, start.Add(2*time.Minute))
	usdt := referralSettlement(t, db, 100, start, start.Add(time.Minute))
	require.Equal(t, old.ID, usdt.ID)
	_, err := ReconcileZTAPIReferralSettlements(ctx, 100)
	require.NoError(t, err)
	for i := 0; i < 2; i++ {
		_, err = ReconcileZTAPICNYReferralSettlements(ctx, &policy, ZTAPICNYReferralCursor{}, 20, start.Add(time.Hour))
		require.NoError(t, err)
	}
	var rows []ZTAPIReferralCommission
	require.NoError(t, db.Order("id").Find(&rows).Error)
	require.Len(t, rows, 3)
	require.Equal(t, usdt.ID, rows[0].OrderID)
	require.Nil(t, rows[0].CNYOrderID)
	require.Zero(t, closed.ReferralPolicyEpoch)
	for i, order := range []CNYTopUpOrder{alipay, wx} {
		row := rows[i+1]
		require.Equal(t, order.ID, *row.CNYOrderID)
		require.Equal(t, order.Method, row.CNYMethod)
		require.EqualValues(t, 142856, row.PrincipalMicros)
		require.EqualValues(t, 7142, row.CommissionMicros)
		require.Equal(t, order.PaidAt+ZTAPIReferralFreezeSeconds, row.AvailableAt)
	}
}

func TestZTAPIReferralCNYRollingCandidateKeepsUSDTWorkingBeforeMigration(t *testing.T) {
	db := referralTestDB(t)
	referralEnable(t)
	referralBind(t, db, 2, 1, referralTestTime)
	require.NoError(t, db.Exec("DROP INDEX idx_ztapi_referral_commissions_cny_order_id").Error)
	require.NoError(t, db.Exec("ALTER TABLE ztapi_referral_commissions DROP COLUMN cny_order_id").Error)
	require.NoError(t, db.Exec("ALTER TABLE ztapi_referral_commissions DROP COLUMN cny_method").Error)
	order := referralSettlement(t, db, 100, referralTestTime.Add(time.Minute), referralTestTime.Add(time.Hour))
	result, err := ReconcileZTAPIReferralSettlements(t.Context(), 20)
	require.NoError(t, err)
	require.Equal(t, 1, result.Created)
	var commission ZTAPIReferralCommission
	require.NoError(t, db.Where("order_id = ?", order.ID).First(&commission).Error)
	require.EqualValues(t, 5_000_000, commission.CommissionMicros)
}

func TestZTAPIReferralCNYRefundHoldAndVerifiedReversal(t *testing.T) {
	db, policy := cnyReferralFixture(t)
	ctx := context.Background()
	start := time.Unix(policy.EnabledAt, 0)
	order := cnyReferralPaid(t, policy, "wxpay", 420000, start)
	_, err := ReconcileZTAPICNYReferralSettlements(ctx, &policy, ZTAPICNYReferralCursor{}, 20, start.Add(time.Hour))
	require.NoError(t, err)
	unfrozen := time.Unix(order.PaidAt+ZTAPIReferralFreezeSeconds, 0)
	w := referralRequest(t, 20_000_000, unfrozen, "cny-referral-withdrawal")
	_, err = HoldCNYTopUpRefund(ctx, order.OrderNo, 1, unfrozen.Add(time.Second))
	require.NoError(t, err)
	_, err = QuoteZTAPIReferralWithdrawal(ctx, ZTAPIReferralWithdrawalAction{WithdrawalID: w.ID, OperatorID: 1, ExpectedVersion: w.Version, FeeMicros: 1, Now: unfrozen.Add(2 * time.Second)})
	require.ErrorIs(t, err, ErrZTAPIReferralConflict)
	summary, err := GetZTAPIReferralSummary(ctx, 1, unfrozen.Add(2*time.Second))
	require.NoError(t, err)
	require.Zero(t, summary.AvailableCommissionMicros)
	_, err = ConfirmCNYTopUpRefund(ctx, order.OrderNo, *order.ProviderTradeNo, "REFUND1", order.PayFen, 1, unfrozen.Add(3*time.Second))
	require.NoError(t, err)
	for i := 0; i < 2; i++ {
		_, err = ReconcileZTAPICNYReferralSettlements(ctx, nil, ZTAPICNYReferralCursor{}, 20, unfrozen.Add(time.Minute))
		require.NoError(t, err)
	}
	var commission ZTAPIReferralCommission
	require.NoError(t, db.Where("cny_order_id = ?", order.ID).First(&commission).Error)
	require.Equal(t, commission.CommissionMicros, commission.ReversedMicros)
	require.Equal(t, commission.PrincipalMicros, commission.RefundedPrincipalMicros)
	var count int64
	require.NoError(t, db.Model(&ZTAPIReferralLedgerEntry{}).Where("commission_id = ? AND kind = ?", commission.ID, "refund").Count(&count).Error)
	require.EqualValues(t, 1, count)
}

func TestZTAPIReferralCNYRefundAfterPayoutUsesManualLoss(t *testing.T) {
	db, policy := cnyReferralFixture(t)
	ctx := context.Background()
	start := time.Unix(policy.EnabledAt, 0)
	order := cnyReferralPaid(t, policy, "alipay", 420000, start)
	_, err := ReconcileZTAPICNYReferralSettlements(ctx, &policy, ZTAPICNYReferralCursor{}, 20, start.Add(time.Hour))
	require.NoError(t, err)
	now := time.Unix(order.PaidAt+ZTAPIReferralFreezeSeconds, 0)
	withdrawal := referralRequest(t, 20_000_000, now, "cny-paid-before-refund")
	referralPay(t, referralClaim(t, withdrawal, now.Add(time.Second)), now.Add(time.Minute))
	_, err = HoldCNYTopUpRefund(ctx, order.OrderNo, 1, now.Add(2*time.Minute))
	require.NoError(t, err)
	_, err = ConfirmCNYTopUpRefund(ctx, order.OrderNo, *order.ProviderTradeNo, "REFUND-PAID", order.PayFen, 1, now.Add(3*time.Minute))
	require.NoError(t, err)
	_, err = ReconcileZTAPICNYReferralSettlements(ctx, nil, ZTAPICNYReferralCursor{}, 20, now.Add(4*time.Minute))
	require.NoError(t, err)
	var c ZTAPIReferralCommission
	require.NoError(t, db.Where("cny_order_id = ?", order.ID).First(&c).Error)
	require.EqualValues(t, 30_000_000, c.ReversedMicros)
	require.EqualValues(t, 20_000_000, c.ManualLossMicros)
	var inviter User
	require.NoError(t, db.First(&inviter, 1).Error)
	require.Zero(t, inviter.Quota)
}

func TestZTAPIReferralCNYClaimMustBeRecheckedBeforeManualPayment(t *testing.T) {
	_, policy := cnyReferralFixture(t)
	ctx := t.Context()
	start := time.Unix(policy.EnabledAt, 0)
	order := cnyReferralPaid(t, policy, "wxpay", 420000, start)
	_, err := ReconcileZTAPICNYReferralSettlements(ctx, &policy, ZTAPICNYReferralCursor{}, 20, start.Add(time.Hour))
	require.NoError(t, err)
	now := time.Unix(order.PaidAt+ZTAPIReferralFreezeSeconds, 0)
	withdrawal := referralRequest(t, 20_000_000, now, "cny-claim-before-refund")
	claim := referralClaim(t, withdrawal, now.Add(time.Second))
	action := referralAction(claim, now.Add(2*time.Second))
	checked, err := RecheckZTAPIReferralPaymentIntent(ctx, action)
	require.NoError(t, err)
	require.Equal(t, claim.Version, checked.Version)
	_, err = HoldCNYTopUpRefund(ctx, order.OrderNo, 1, now.Add(3*time.Second))
	require.NoError(t, err)
	action.Now = now.Add(4 * time.Second)
	_, err = RecheckZTAPIReferralPaymentIntent(ctx, action)
	require.ErrorIs(t, err, ErrZTAPIReferralConflict)
}
