package model

import (
	"context"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var referralTestTime = time.Date(2024, 2, 29, 12, 30, 0, 0, time.UTC)

func referralTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "referral.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	old := DB
	oldGuard := ZTAPIReferralBindingAdmissionGuard
	ZTAPIReferralBindingAdmissionGuard = func(*gorm.DB, time.Time) error { return nil }
	DB = db
	t.Cleanup(func() { DB = old; ZTAPIReferralBindingAdmissionGuard = oldGuard; _ = sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(&User{}, &TopUp{}, &USDTTopUpOrder{}, &BalanceLedger{}))
	require.NoError(t, MigrateZTAPIReferral(db))
	for _, id := range []int{1, 2, 3} {
		require.NoError(t, db.Create(&User{Id: id, Username: fmt.Sprintf("referral%d", id), AffCode: fmt.Sprintf("ref%d", id), Status: common.UserStatusEnabled}).Error)
	}
	return db
}

func referralGate() ZTAPIReferralPriceGateInput {
	return ZTAPIReferralPriceGateInput{CatalogComplete: true, CatalogEvidence: "local-test-catalog", RequiredKeys: []string{"text/primary/input"}, Items: []ZTAPIReferralPriceGateItem{{
		Key: "text/primary/input", ModelMappingKnown: true, CostKnown: true, UsageKnown: true, PlatformFeeKnown: true,
		CustomerRevenue: "100", SupplierCostUpperBound: "85", UsageUpperBound: "1", BonusRate: "0", GroupMultiplier: "1", CashMultiplier: "1", PlatformFee: "0", Evidence: "explicit-test-fee-waiver-and-cost-proof",
	}}}
}

func referralReady() ZTAPIReferralReadinessInput {
	return ZTAPIReferralReadinessInput{PriceGate: referralGate(), FinanceReady: true, WorkerReady: true, PlatformFeeKnown: true, PlatformFeeEvidence: "local-test-explicit-waiver"}
}

func referralEnable(t *testing.T) {
	t.Helper()
	c, err := SetZTAPIReferralCampaign(context.Background(), ZTAPIReferralCampaignUpdate{OperatorID: 1, Enabled: true, Now: referralTestTime, Readiness: referralReady()})
	require.NoError(t, err)
	require.True(t, c.Enabled)
	require.Equal(t, int64(1), c.Version)
}

func referralBind(t *testing.T, db *gorm.DB, user, inviter int, at time.Time) {
	t.Helper()
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error { return RecordZTAPIReferralBindingTx(tx, user, inviter, at) }))
}

// Seed only local evidence; no cache, provider, account or network calls occur.
func referralSettlement(t *testing.T, db *gorm.DB, units int64, block, settled time.Time) USDTTopUpOrder {
	t.Helper()
	return referralSettlementForUser(t, db, 2, units, block, settled)
}

func referralSettlementForUser(t *testing.T, db *gorm.DB, userID int, units int64, block, settled time.Time) USDTTopUpOrder {
	t.Helper()
	trade := "test-" + common.GetUUID()
	topup := TopUp{UserId: userID, Amount: units, TradeNo: trade, PaymentMethod: PaymentMethodUSDTTRC20, PaymentProvider: PaymentProviderUSDTTRC20, Status: common.TopUpStatusSuccess, CreateTime: block.Add(-time.Minute).Unix(), CompleteTime: settled.Unix()}
	require.NoError(t, db.Create(&topup).Error)
	txID := strings.Repeat("a", 32) + strings.ReplaceAll(common.GetUUID(), "-", "")[:32]
	ms, at := block.UnixMilli(), settled.Unix()
	from := USDTTopUpContractAddress
	order := USDTTopUpOrder{TopUpID: topup.Id, UserID: userID, TradeNo: trade, Network: USDTTopUpNetworkTronMainnet, Asset: USDTTopUpAssetUSDT, ContractAddress: USDTTopUpContractAddress, ReceivingAddress: USDTTopUpContractAddress, CreditUnits: units, PayAmountMicros: units*1_000_000 + 370_000, SuffixCents: 37, Status: USDTTopUpStatusSettled, TxID: &txID, TxFrom: &from, BlockTimestampMS: &ms, SettledAt: &at, CreatedAt: block.Add(-time.Minute).Unix(), ExpiresAt: block.Add(time.Minute).Unix()}
	require.NoError(t, db.Create(&order).Error)
	quota := adminTopUpQuota(&topup)
	require.NoError(t, db.Create(&BalanceLedger{UserID: userID, Delta: quota, BalanceAfter: quota, SourceType: BalanceLedgerSourceUSDTTopUp, RequestID: trade, IdempotencyKey: "usdt-trc20:" + txID, Reason: "local settled principal", CreatedAt: settled}).Error)
	require.NoError(t, db.Create(&BalanceLedger{UserID: userID, Delta: quota / 20, BalanceBefore: quota, BalanceAfter: quota + quota/20, SourceType: BalanceLedgerSourceUSDTTopUpBonus, RequestID: trade, IdempotencyKey: fmt.Sprintf("usdt-trc20-bonus:%d", order.ID), Reason: "local excluded bonus", CreatedAt: settled}).Error)
	return order
}

func referralFund(t *testing.T, units int64) (*gorm.DB, USDTTopUpOrder, time.Time) {
	t.Helper()
	db := referralTestDB(t)
	referralEnable(t)
	referralBind(t, db, 2, 1, referralTestTime)
	order := referralSettlement(t, db, units, referralTestTime.Add(time.Hour), referralTestTime.Add(2*time.Hour))
	r, err := ReconcileZTAPIReferralSettlements(context.Background(), 100)
	require.NoError(t, err)
	require.Equal(t, 1, r.Created)
	return db, order, referralTestTime.Add(2 * time.Hour).Add(7 * 24 * time.Hour)
}

func referralRequest(t *testing.T, amount int64, now time.Time, key string) *ZTAPIReferralWithdrawal {
	t.Helper()
	w, err := RequestZTAPIReferralWithdrawal(context.Background(), ZTAPIReferralWithdrawalRequest{UserID: 1, AmountMicros: amount, Address: USDTTopUpContractAddress, IdempotencyKey: key, Now: now})
	require.NoError(t, err)
	return w
}

func referralAction(w *ZTAPIReferralWithdrawal, now time.Time) ZTAPIReferralWithdrawalAction {
	return ZTAPIReferralWithdrawalAction{WithdrawalID: w.ID, ExpectedVersion: w.Version, OperatorID: 11, UserID: 1, Now: now}
}

func referralClaim(t *testing.T, w *ZTAPIReferralWithdrawal, now time.Time) *ZTAPIReferralWithdrawal {
	t.Helper()
	in := referralAction(w, now)
	in.FeeMicros = 1_000_000
	w, err := QuoteZTAPIReferralWithdrawal(context.Background(), in)
	require.NoError(t, err)
	w, err = ConfirmZTAPIReferralWithdrawal(context.Background(), referralAction(w, now))
	require.NoError(t, err)
	w, err = ClaimZTAPIReferralPaymentIntent(context.Background(), referralAction(w, now))
	require.NoError(t, err)
	return w
}

func referralEvidence(w *ZTAPIReferralWithdrawal, now time.Time) ZTAPIReferralPaymentEvidence {
	return ZTAPIReferralPaymentEvidence{TxID: strings.Repeat("b", 64), LogIndex: 0, Network: USDTTopUpNetworkTronMainnet, ContractAddress: USDTTopUpContractAddress, From: USDTTopUpContractAddress, To: w.Address, AmountMicros: w.NetAmountMicros, BlockTimestampMS: now.UnixMilli(), BlockNumber: 123, ReceiptSHA256: strings.Repeat("c", 64)}
}

func referralPay(t *testing.T, w *ZTAPIReferralWithdrawal, now time.Time) *ZTAPIReferralWithdrawal {
	t.Helper()
	w, err := ConfirmZTAPIReferralManualPayment(context.Background(), ZTAPIReferralPaymentConfirmation{WithdrawalID: w.ID, ExpectedVersion: w.Version, OperatorID: 11, Evidence: referralEvidence(w, now), Now: now})
	require.NoError(t, err)
	return w
}

func TestZTAPIReferralPolicyExactMarginAndUnknownEvidence(t *testing.T) {
	in := referralGate()
	r, err := EvaluateZTAPIReferralPriceGate(in)
	require.NoError(t, err)
	require.True(t, r.Allowed)
	in.Items[0].SupplierCostUpperBound = "85.000001"
	r, err = EvaluateZTAPIReferralPriceGate(in)
	require.NoError(t, err)
	require.False(t, r.Allowed)
	for _, mutate := range []func(*ZTAPIReferralPriceGateItem){
		func(i *ZTAPIReferralPriceGateItem) { i.ModelMappingKnown = false },
		func(i *ZTAPIReferralPriceGateItem) { i.UsageKnown = false },
		func(i *ZTAPIReferralPriceGateItem) { i.CostKnown = false },
		func(i *ZTAPIReferralPriceGateItem) { i.PlatformFeeKnown = false },
		func(i *ZTAPIReferralPriceGateItem) { i.PlatformFee = "" },
	} {
		in = referralGate()
		mutate(&in.Items[0])
		r, err = EvaluateZTAPIReferralPriceGate(in)
		require.NoError(t, err)
		require.False(t, r.Allowed)
	}
	in = referralGate()
	in.RequiredKeys = append(in.RequiredKeys, "flash/backup/cache-read")
	r, err = EvaluateZTAPIReferralPriceGate(in)
	require.NoError(t, err)
	require.False(t, r.Allowed)
	in = referralGate()
	in.Items[0].GroupMultiplier = "2"
	in.Items[0].CashMultiplier = "0.5"
	r, err = EvaluateZTAPIReferralPriceGate(in)
	require.NoError(t, err)
	require.True(t, r.Allowed)
	in.Items[0].BonusRate = "0.05"
	r, err = EvaluateZTAPIReferralPriceGate(in)
	require.NoError(t, err)
	require.False(t, r.Allowed)
}

func TestZTAPIReferralPolicySupplierMultipliersNotCustomerRevenue(t *testing.T) {
	for _, v := range []struct{ cost, group, cash, bonus string }{
		{"1", "1.5", "0.5", "0.05"},
		{"1.2", "0.5", "1", "0"},
		{"0.75", "1", "1", "0.05"},
	} {
		in := referralGate()
		i := &in.Items[0]
		i.CustomerRevenue = "1"
		i.SupplierCostUpperBound = v.cost
		i.GroupMultiplier = v.group
		i.CashMultiplier = v.cash
		i.BonusRate = v.bonus
		r, err := EvaluateZTAPIReferralPriceGate(in)
		require.NoError(t, err)
		require.True(t, r.Allowed, "%+v", v)
	}
}

func TestZTAPIReferralBindingDefaultDisabledAndCalendarClamp(t *testing.T) {
	db := referralTestDB(t)
	c, err := GetZTAPIReferralCampaign(context.Background())
	require.NoError(t, err)
	require.False(t, c.Enabled)
	referralBind(t, db, 2, 1, referralTestTime)
	var n int64
	require.NoError(t, db.Model(&ZTAPIReferralBinding{}).Count(&n).Error)
	require.Zero(t, n)
	_, err = SetZTAPIReferralCampaign(context.Background(), ZTAPIReferralCampaignUpdate{OperatorID: 1, Enabled: true, Now: referralTestTime})
	require.Error(t, err)
	referralEnable(t)
	referralBind(t, db, 2, 1, referralTestTime)
	referralBind(t, db, 2, 1, referralTestTime.Add(time.Minute))
	var b ZTAPIReferralBinding
	require.NoError(t, db.First(&b).Error)
	require.Equal(t, time.Date(2025, 2, 28, 12, 30, 0, 0, time.UTC).Unix(), b.ExpiresAt)
	require.Error(t, db.Transaction(func(tx *gorm.DB) error { return RecordZTAPIReferralBindingTx(tx, 2, 3, referralTestTime) }))
	require.Error(t, db.Transaction(func(tx *gorm.DB) error { return RecordZTAPIReferralBindingTx(tx, 3, 3, referralTestTime) }))
	require.Error(t, db.Transaction(func(tx *gorm.DB) error { return RecordZTAPIReferralBindingTx(tx, 3, 99, referralTestTime) }))
	_, err = SetZTAPIReferralCampaign(context.Background(), ZTAPIReferralCampaignUpdate{ExpectedVersion: 1, OperatorID: 1, Enabled: false, Now: referralTestTime.Add(time.Hour)})
	require.NoError(t, err)
	referralBind(t, db, 3, 1, referralTestTime.Add(2*time.Hour))
	require.NoError(t, db.Model(&ZTAPIReferralBinding{}).Count(&n).Error)
	require.Equal(t, int64(1), n)
}

func TestZTAPIReferralReconcilePrincipalOnlyReplayClosedAndLateEvidence(t *testing.T) {
	db := referralTestDB(t)
	referralEnable(t)
	referralBind(t, db, 2, 1, referralTestTime)
	referralSettlement(t, db, 100, referralTestTime.Add(time.Hour), referralTestTime.Add(400*24*time.Hour))
	_, err := SetZTAPIReferralCampaign(context.Background(), ZTAPIReferralCampaignUpdate{ExpectedVersion: 1, OperatorID: 1, Enabled: false, Now: referralTestTime.Add(time.Minute)})
	require.NoError(t, err)
	for i := 0; i < 3; i++ {
		r, err := ReconcileZTAPIReferralSettlements(context.Background(), 1)
		require.NoError(t, err)
		if i == 0 {
			require.Equal(t, 1, r.Created)
		} else {
			require.Zero(t, r.Created)
		}
	}
	var cs []ZTAPIReferralCommission
	require.NoError(t, db.Find(&cs).Error)
	require.Len(t, cs, 1)
	require.Equal(t, int64(100_000_000), cs[0].PrincipalMicros)
	require.Equal(t, int64(5_000_000), cs[0].CommissionMicros)
	require.Equal(t, referralTestTime.Add(400*24*time.Hour).Unix(), cs[0].EarnedAt)
	require.Equal(t, cs[0].EarnedAt+604800, cs[0].AvailableAt)
}

func TestZTAPIReferralReconcileMissingEvidenceDoesNotStarveAndRollback(t *testing.T) {
	db := referralTestDB(t)
	referralEnable(t)
	referralBind(t, db, 2, 1, referralTestTime)
	first := referralSettlement(t, db, 100, referralTestTime.Add(time.Hour), referralTestTime.Add(2*time.Hour))
	require.NoError(t, db.Model(&USDTTopUpOrder{}).Where("id = ?", first.ID).Update("block_timestamp_ms", nil).Error)
	referralSettlement(t, db, 200, referralTestTime.Add(time.Hour), referralTestTime.Add(2*time.Hour))
	r, err := ReconcileZTAPIReferralSettlements(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, 1, r.Created)
	require.NoError(t, db.Model(&USDTTopUpOrder{}).Where("id = ?", first.ID).Update("block_timestamp_ms", referralTestTime.Add(time.Hour).UnixMilli()).Error)
	r, err = ReconcileZTAPIReferralSettlements(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, 1, r.Created)
	third := referralSettlement(t, db, 300, referralTestTime.Add(time.Hour), referralTestTime.Add(2*time.Hour))
	require.NoError(t, db.Exec(`CREATE TRIGGER referral_fail_ledger BEFORE INSERT ON ztapi_referral_ledger_entries BEGIN SELECT RAISE(ABORT, 'local rollback'); END`).Error)
	_, err = ReconcileZTAPIReferralSettlements(context.Background(), 10)
	require.Error(t, err)
	var n int64
	require.NoError(t, db.Model(&ZTAPIReferralCommission{}).Where("order_id = ?", third.ID).Count(&n).Error)
	require.Zero(t, n)
	require.NoError(t, db.Exec("DROP TRIGGER referral_fail_ledger").Error)
	r, err = ReconcileZTAPIReferralSettlements(context.Background(), 10)
	require.NoError(t, err)
	require.Equal(t, 1, r.Created)
}

func TestZTAPIReferralReconcileCampaignAndExclusiveTermBoundary(t *testing.T) {
	db := referralTestDB(t)
	referralEnable(t)
	referralBind(t, db, 2, 1, referralTestTime)
	for _, at := range []time.Time{referralTestTime.Add(-time.Second), time.Date(2025, 2, 28, 12, 30, 0, 0, time.UTC)} {
		referralSettlement(t, db, 100, at, at.Add(time.Hour))
	}
	r, err := ReconcileZTAPIReferralSettlements(context.Background(), 100)
	require.NoError(t, err)
	require.Zero(t, r.Created)
}

func TestZTAPIReferralWithdrawalMinimumFreezeReplayAndConcurrent(t *testing.T) {
	_, _, now := referralFund(t, 800)
	for _, in := range []ZTAPIReferralWithdrawalRequest{
		{UserID: 1, AmountMicros: 19_999_999, Address: USDTTopUpContractAddress, IdempotencyKey: "below", Now: now},
		{UserID: 1, AmountMicros: 20_000_000, Address: USDTTopUpContractAddress, IdempotencyKey: "frozen", Now: now.Add(-time.Second)},
		{UserID: 1, AmountMicros: 20_000_000, Address: "invalid", IdempotencyKey: "address", Now: now},
	} {
		_, err := RequestZTAPIReferralWithdrawal(context.Background(), in)
		require.Error(t, err)
	}
	w := referralRequest(t, 20_000_000, now, "request1")
	duplicate := referralRequest(t, 20_000_000, now.Add(time.Second), "request1")
	require.Equal(t, w.ID, duplicate.ID)
	_, err := RequestZTAPIReferralWithdrawal(context.Background(), ZTAPIReferralWithdrawalRequest{UserID: 1, AmountMicros: 21_000_000, Address: USDTTopUpContractAddress, IdempotencyKey: "request1", Now: now})
	require.Error(t, err)
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := RequestZTAPIReferralWithdrawal(context.Background(), ZTAPIReferralWithdrawalRequest{UserID: 1, AmountMicros: 20_000_000, Address: USDTTopUpContractAddress, IdempotencyKey: fmt.Sprintf("race%d", i), Now: now})
			results <- err
		}(i)
	}
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		}
	}
	require.Equal(t, 1, success)
	s, err := GetZTAPIReferralSummary(context.Background(), 1, now)
	require.NoError(t, err)
	require.Zero(t, s.AvailableCommissionMicros)
	require.Equal(t, int64(40_000_000), s.WithdrawalReservedMicros)
}

func TestZTAPIReferralWithdrawalQuoteExpiryHolderUnknownAndEvidence(t *testing.T) {
	_, _, now := referralFund(t, 800)
	w := referralRequest(t, 20_000_000, now, "withdraw")
	in := referralAction(w, now)
	in.FeeMicros = 1_000_000
	w, err := QuoteZTAPIReferralWithdrawal(context.Background(), in)
	require.NoError(t, err)
	_, err = ConfirmZTAPIReferralWithdrawal(context.Background(), referralAction(w, now.Add(30*time.Minute)))
	require.Error(t, err)
	w, err = ConfirmZTAPIReferralWithdrawal(context.Background(), referralAction(w, now))
	require.NoError(t, err)
	var wg sync.WaitGroup
	outcomes := make(chan *ZTAPIReferralWithdrawal, 2)
	for _, admin := range []int{11, 12} {
		wg.Add(1)
		go func(admin int) {
			defer wg.Done()
			in := referralAction(w, now)
			in.OperatorID = admin
			claimed, err := ClaimZTAPIReferralPaymentIntent(context.Background(), in)
			if err == nil {
				outcomes <- claimed
			}
		}(admin)
	}
	wg.Wait()
	close(outcomes)
	var claimed []*ZTAPIReferralWithdrawal
	for c := range outcomes {
		claimed = append(claimed, c)
	}
	require.Len(t, claimed, 1)
	w = claimed[0]
	_, err = RejectZTAPIReferralWithdrawal(context.Background(), referralAction(w, now))
	require.Error(t, err)
	_, err = CancelZTAPIReferralWithdrawal(context.Background(), referralAction(w, now))
	require.Error(t, err)
	in = referralAction(w, now)
	in.OperatorID = w.HolderID
	in.TxID = strings.Repeat("d", 64)
	w, err = SubmitZTAPIReferralManualPayment(context.Background(), in)
	require.NoError(t, err)
	require.Equal(t, "submitted_unknown", w.Status)
	s, err := GetZTAPIReferralSummary(context.Background(), 1, now.Add(365*24*time.Hour))
	require.NoError(t, err)
	require.Equal(t, int64(20_000_000), s.WithdrawalReservedMicros)
	e := referralEvidence(w, now)
	_, err = ConfirmZTAPIReferralManualPayment(context.Background(), ZTAPIReferralPaymentConfirmation{WithdrawalID: w.ID, ExpectedVersion: w.Version, OperatorID: w.HolderID, Evidence: e, Now: now})
	require.Error(t, err)
	e.TxID = w.TxID
	for _, mutate := range []func(*ZTAPIReferralPaymentEvidence){
		func(e *ZTAPIReferralPaymentEvidence) { e.AmountMicros++ }, func(e *ZTAPIReferralPaymentEvidence) { e.BlockTimestampMS = (w.CreatedAt - 1) * 1000 },
		func(e *ZTAPIReferralPaymentEvidence) { e.Network = "testnet" }, func(e *ZTAPIReferralPaymentEvidence) { e.ContractAddress = "other" },
		func(e *ZTAPIReferralPaymentEvidence) { e.To = "other" }, func(e *ZTAPIReferralPaymentEvidence) { e.ReceiptSHA256 = "" },
	} {
		invalid := e
		mutate(&invalid)
		_, err = ConfirmZTAPIReferralManualPayment(context.Background(), ZTAPIReferralPaymentConfirmation{WithdrawalID: w.ID, ExpectedVersion: w.Version, OperatorID: w.HolderID, Evidence: invalid, Now: now})
		require.Error(t, err)
	}
	w, err = ConfirmZTAPIReferralManualPayment(context.Background(), ZTAPIReferralPaymentConfirmation{WithdrawalID: w.ID, ExpectedVersion: w.Version, OperatorID: w.HolderID, Evidence: e, Now: now})
	require.NoError(t, err)
	require.Equal(t, "paid", w.Status)
}

func TestZTAPIReferralRefundCumulativeRoundingAndIdentity(t *testing.T) {
	db, order, now := referralFund(t, 100)
	for i, cumulative := range []int64{1, 19, 20, 21, 1_000_001} {
		c, err := ReverseZTAPIReferralRechargePrincipal(context.Background(), ZTAPIReferralRefundInput{OrderID: order.ID, CumulativePrincipalMicros: cumulative, OperatorID: 1, IdempotencyKey: fmt.Sprintf("refund%d", i), EvidenceReference: fmt.Sprintf("verified-refund-%d", i), Reason: "local regression", At: now})
		require.NoError(t, err)
		require.Equal(t, cumulative/20, c.ReversedMicros)
	}
	_, err := ReverseZTAPIReferralRechargePrincipal(context.Background(), ZTAPIReferralRefundInput{OrderID: order.ID, CumulativePrincipalMicros: 2_000_000, OperatorID: 1, IdempotencyKey: "refund4", EvidenceReference: "verified-refund-4", Reason: "local regression", At: now})
	require.Error(t, err)
	var entries []ZTAPIReferralLedgerEntry
	require.NoError(t, db.Where("kind = ?", "refund").Find(&entries).Error)
	var sum int64
	for _, e := range entries {
		sum += e.AmountMicros
	}
	require.Equal(t, int64(-50_000), sum)
}

func TestZTAPIReferralRefundReservedCancelCannotReleaseTwice(t *testing.T) {
	_, order, now := referralFund(t, 800)
	w := referralRequest(t, 30_000_000, now, "reserve")
	_, err := ReverseZTAPIReferralRechargePrincipal(context.Background(), ZTAPIReferralRefundInput{OrderID: order.ID, CumulativePrincipalMicros: 400_000_000, OperatorID: 1, IdempotencyKey: "refund-half", EvidenceReference: "verified-half", Reason: "local refund", At: now})
	require.NoError(t, err)
	ws, _, err := ListZTAPIReferralWithdrawals(context.Background(), 1, 1, 10, "")
	require.NoError(t, err)
	require.Equal(t, "manual_review", ws[0].Status)
	w = &ws[0]
	_, err = ClaimZTAPIReferralPaymentIntent(context.Background(), referralAction(w, now))
	require.Error(t, err)
	w, err = CancelZTAPIReferralWithdrawal(context.Background(), referralAction(w, now))
	require.NoError(t, err)
	_, err = CancelZTAPIReferralWithdrawal(context.Background(), referralAction(w, now))
	require.Error(t, err)
	s, err := GetZTAPIReferralSummary(context.Background(), 1, now)
	require.NoError(t, err)
	require.Equal(t, int64(20_000_000), s.AvailableCommissionMicros)
	require.Zero(t, s.WithdrawalReservedMicros)
	referralRequest(t, 20_000_000, now, "remaining")
	_, err = RequestZTAPIReferralWithdrawal(context.Background(), ZTAPIReferralWithdrawalRequest{UserID: 1, AmountMicros: 20_000_000, Address: USDTTopUpContractAddress, IdempotencyKey: "overdraw", Now: now})
	require.Error(t, err)
}

func TestZTAPIReferralRefundUnknownPaidManualLossDoesNotDebitFutureCommission(t *testing.T) {
	db, order, now := referralFund(t, 400)
	w := referralClaim(t, referralRequest(t, 20_000_000, now, "pending"), now)
	in := referralAction(w, now)
	in.TxID = strings.Repeat("b", 64)
	w, err := SubmitZTAPIReferralManualPayment(context.Background(), in)
	require.NoError(t, err)
	_, err = ReverseZTAPIReferralRechargePrincipal(context.Background(), ZTAPIReferralRefundInput{OrderID: order.ID, CumulativePrincipalMicros: 400_000_000, OperatorID: 1, IdempotencyKey: "full", EvidenceReference: "verified-full", Reason: "local full refund", At: now})
	require.NoError(t, err)
	s, err := GetZTAPIReferralSummary(context.Background(), 1, now)
	require.NoError(t, err)
	require.Equal(t, int64(20_000_000), s.WithdrawalReservedMicros)
	require.Zero(t, s.ManualLossMicros)
	w = referralPay(t, w, now)
	s, err = GetZTAPIReferralSummary(context.Background(), 1, now)
	require.NoError(t, err)
	require.Equal(t, int64(20_000_000), s.ManualLossMicros)
	require.Zero(t, s.AvailableCommissionMicros)
	referralSettlement(t, db, 400, now.Add(time.Hour), now.Add(time.Hour))
	_, err = ReconcileZTAPIReferralSettlements(context.Background(), 100)
	require.NoError(t, err)
	s, err = GetZTAPIReferralSummary(context.Background(), 1, now.Add(time.Hour).Add(7*24*time.Hour))
	require.NoError(t, err)
	require.Equal(t, int64(20_000_000), s.AvailableCommissionMicros)
	var u User
	require.NoError(t, db.First(&u, 1).Error)
	require.Zero(t, u.Quota)
}

func TestZTAPIReferralWithdrawalAuditFailureRollsBackAndLedgerImmutable(t *testing.T) {
	db, _, now := referralFund(t, 400)
	require.NoError(t, db.Exec(`CREATE TRIGGER referral_fail_audit BEFORE INSERT ON ztapi_referral_audits BEGIN SELECT RAISE(ABORT, 'local rollback'); END`).Error)
	_, err := RequestZTAPIReferralWithdrawal(context.Background(), ZTAPIReferralWithdrawalRequest{UserID: 1, AmountMicros: 20_000_000, Address: USDTTopUpContractAddress, IdempotencyKey: "rollback", Now: now})
	require.Error(t, err)
	var n int64
	require.NoError(t, db.Model(&ZTAPIReferralAllocation{}).Count(&n).Error)
	require.Zero(t, n)
	require.NoError(t, db.Exec("DROP TRIGGER referral_fail_audit").Error)
	s, err := GetZTAPIReferralSummary(context.Background(), 1, now)
	require.NoError(t, err)
	require.Equal(t, int64(20_000_000), s.AvailableCommissionMicros)
	var e ZTAPIReferralLedgerEntry
	require.NoError(t, db.First(&e).Error)
	require.Error(t, db.Model(&e).Update("amount_micros", 0).Error)
	require.Error(t, db.Session(&gorm.Session{SkipHooks: true}).Table(e.TableName()).Where("id = ?", e.ID).Update("amount_micros", 0).Error)
	require.Error(t, db.Delete(&e).Error)
}

func TestZTAPIReferralMicrosPrecisionOverflowAndContext(t *testing.T) {
	for _, invalid := range []string{"1.0000001", "-1", "1e6", "9223372036854.775808", "NaN", " 1.0", ".1", "1."} {
		_, err := ParseZTAPIReferralMicros(invalid)
		require.Error(t, err, invalid)
	}
	n, err := ParseZTAPIReferralMicros("9223372036854.775807")
	require.NoError(t, err)
	require.Equal(t, int64(math.MaxInt64), n)
	require.Equal(t, "9223372036854.775807", FormatZTAPIReferralMicros(n))
	require.Equal(t, "-9223372036854.775808", FormatZTAPIReferralMicros(math.MinInt64))
	referralTestDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = ReconcileZTAPIReferralSettlements(ctx, 10)
	require.True(t, errors.Is(err, context.Canceled))
}
