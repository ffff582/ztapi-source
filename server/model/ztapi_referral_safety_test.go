package model

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestZTAPIReferralReconcileRealSettlementCacheFailureAndRollback(t *testing.T) {
	for _, rollback := range []bool{false, true} {
		name := "cache-committed"
		if rollback {
			name = "rolled-back"
		}
		t.Run(name, func(t *testing.T) {
			db, user, _, transfer, at := setupUSDTSettlementTest(t, "referral-"+name)
			require.NoError(t, MigrateZTAPIReferral(db))
			inviter := User{Username: "local-referral-inviter", AffCode: "local-cache-inviter", Status: common.UserStatusEnabled}
			require.NoError(t, db.Create(&inviter).Error)
			oldGuard := ZTAPIReferralBindingAdmissionGuard
			ZTAPIReferralBindingAdmissionGuard = func(*gorm.DB, time.Time) error { return nil }
			t.Cleanup(func() { ZTAPIReferralBindingAdmissionGuard = oldGuard })
			_, err := SetZTAPIReferralCampaign(context.Background(), ZTAPIReferralCampaignUpdate{OperatorID: 1, Enabled: true, Now: at.Add(-time.Minute), Readiness: referralReady()})
			require.NoError(t, err)
			referralBind(t, db, user.Id, inviter.Id, at.Add(-time.Minute))
			oldSync := balanceLedgerCacheSync
			balanceLedgerCacheSync = func(int) error { return errors.New("local synthetic cache failure") }
			t.Cleanup(func() { balanceLedgerCacheSync = oldSync })
			if rollback {
				require.NoError(t, db.Exec(`CREATE TRIGGER referral_abort_principal BEFORE INSERT ON balance_ledgers BEGIN SELECT RAISE(ABORT, 'local settlement rollback'); END`).Error)
			}
			_, err = SettleUSDTTopUp(transfer, at)
			if rollback {
				require.Error(t, err)
			} else {
				require.ErrorIs(t, err, ErrUSDTTopUpCacheSync)
			}
			r, err := ReconcileZTAPIReferralSettlements(context.Background(), 100)
			require.NoError(t, err)
			if rollback {
				require.Zero(t, r.Created)
			} else {
				require.Equal(t, 1, r.Created)
			}
			r, err = ReconcileZTAPIReferralSettlements(context.Background(), 100)
			require.NoError(t, err)
			require.Zero(t, r.Created)
		})
	}
}

func TestZTAPIReferralReconcileRealSettlementFromNegativeBalance(t *testing.T) {
	db, user, order, transfer, at := setupUSDTSettlementTest(t, "referral-negative-before")
	require.NoError(t, MigrateZTAPIReferral(db))
	inviter := User{Username: "negative-local-inviter", AffCode: "negative-local-invite", Status: common.UserStatusEnabled}
	require.NoError(t, db.Create(&inviter).Error)
	oldGuard, oldSync := ZTAPIReferralBindingAdmissionGuard, balanceLedgerCacheSync
	ZTAPIReferralBindingAdmissionGuard = func(*gorm.DB, time.Time) error { return nil }
	balanceLedgerCacheSync = func(int) error { return nil }
	t.Cleanup(func() { ZTAPIReferralBindingAdmissionGuard = oldGuard; balanceLedgerCacheSync = oldSync })
	_, err := SetZTAPIReferralCampaign(context.Background(), ZTAPIReferralCampaignUpdate{OperatorID: 1, Enabled: true, Now: at.Add(-time.Minute), Readiness: referralReady()})
	require.NoError(t, err)
	referralBind(t, db, user.Id, inviter.Id, at.Add(-time.Minute))
	paidQuota := adminTopUpQuota(&TopUp{Amount: order.CreditUnits, PaymentProvider: PaymentProviderUSDTTRC20})
	require.Positive(t, paidQuota)
	require.NoError(t, db.Model(&User{}).Where("id = ?", user.Id).Update("quota", -paidQuota/2).Error)
	settled, err := SettleUSDTTopUp(transfer, at)
	require.NoError(t, err)
	require.True(t, settled.Applied)
	require.Negative(t, settled.Ledger.BalanceBefore)
	require.Equal(t, settled.Ledger.BalanceBefore+settled.Ledger.Delta, settled.Ledger.BalanceAfter)
	r, err := ReconcileZTAPIReferralSettlements(context.Background(), 100)
	require.NoError(t, err)
	require.Equal(t, 1, r.Created, "settled debt recovery is still real paid principal")
	cs, _, err := ListZTAPIReferralCommissions(context.Background(), inviter.Id, 1, 10)
	require.NoError(t, err)
	require.Len(t, cs, 1)
	require.Equal(t, order.CreditUnits*1_000_000/20, cs[0].CommissionMicros)
}

func TestZTAPIReferralRefundAndPaymentConcurrentRemainAtomic(t *testing.T) {
	db, order, now := referralFund(t, 400)
	w := referralClaim(t, referralRequest(t, 20_000_000, now, "pay-refund-race"), now)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	start := make(chan struct{})
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		_, err := ConfirmZTAPIReferralManualPayment(context.Background(), ZTAPIReferralPaymentConfirmation{WithdrawalID: w.ID, ExpectedVersion: w.Version, OperatorID: 11, Evidence: referralEvidence(w, now), Now: now})
		results <- err
	}()
	go func() {
		defer wg.Done()
		<-start
		_, err := ReverseZTAPIReferralRechargePrincipal(context.Background(), ZTAPIReferralRefundInput{OrderID: order.ID, CumulativePrincipalMicros: 400_000_000, OperatorID: 1, IdempotencyKey: "concurrent-refund", EvidenceReference: "verified-concurrent", Reason: "local", At: now})
		results <- err
	}()
	close(start)
	wg.Wait()
	close(results)
	for err := range results {
		require.NoError(t, err)
	}
	s, err := GetZTAPIReferralSummary(context.Background(), 1, now)
	require.NoError(t, err)
	require.Zero(t, s.AvailableCommissionMicros)
	require.Zero(t, s.WithdrawalReservedMicros)
	require.Equal(t, int64(20_000_000), s.PaidCommissionMicros)
	require.Equal(t, int64(20_000_000), s.ManualLossMicros)
	var n int64
	require.NoError(t, db.Model(&ZTAPIReferralLedgerEntry{}).Where("kind = ?", "manual_loss").Count(&n).Error)
	require.Equal(t, int64(1), n)
}

func TestZTAPIReferralWithdrawalIdempotencyScopedToUser(t *testing.T) {
	db, _, now := referralFund(t, 400)
	require.NoError(t, db.Create(&User{Id: 4, Username: "referral4", AffCode: "ref4", Status: common.UserStatusEnabled}).Error)
	referralBind(t, db, 4, 3, referralTestTime)
	referralSettlementForUser(t, db, 4, 400, referralTestTime.Add(time.Hour), referralTestTime.Add(2*time.Hour))
	_, err := ReconcileZTAPIReferralSettlements(context.Background(), 100)
	require.NoError(t, err)
	first := referralRequest(t, 20_000_000, now, "same-user-selected-key")
	in := ZTAPIReferralWithdrawalRequest{UserID: 3, AmountMicros: 20_000_000, Address: USDTTopUpContractAddress, IdempotencyKey: "same-user-selected-key", Now: now}
	second, err := RequestZTAPIReferralWithdrawal(context.Background(), in)
	require.NoError(t, err)
	require.NotEqual(t, first.ID, second.ID)
	require.Equal(t, 3, second.UserID)
	again, err := RequestZTAPIReferralWithdrawal(context.Background(), in)
	require.NoError(t, err)
	require.Equal(t, second.ID, again.ID)
	in.AmountMicros++
	_, err = RequestZTAPIReferralWithdrawal(context.Background(), in)
	require.ErrorIs(t, err, ErrZTAPIReferralConflict)
}

func TestZTAPIReferralBindingAdmissionFailClosedAndExistingExempt(t *testing.T) {
	db := referralTestDB(t)
	referralEnable(t)
	ZTAPIReferralBindingAdmissionGuard = nil
	err := db.Transaction(func(tx *gorm.DB) error { return RecordZTAPIReferralBindingTx(tx, 2, 1, referralTestTime) })
	require.ErrorIs(t, err, ErrZTAPIReferralNotReady)
	guardErr := errors.New("local expired fingerprint")
	ZTAPIReferralBindingAdmissionGuard = func(*gorm.DB, time.Time) error { return guardErr }
	err = db.Transaction(func(tx *gorm.DB) error { return RecordZTAPIReferralBindingTx(tx, 2, 1, referralTestTime) })
	require.ErrorIs(t, err, guardErr)
	var count int64
	require.NoError(t, db.Model(&ZTAPIReferralBinding{}).Count(&count).Error)
	require.Zero(t, count)
	ZTAPIReferralBindingAdmissionGuard = func(*gorm.DB, time.Time) error { return nil }
	referralBind(t, db, 2, 1, referralTestTime)
	ZTAPIReferralBindingAdmissionGuard = func(*gorm.DB, time.Time) error { return guardErr }
	referralBind(t, db, 2, 1, referralTestTime.Add(time.Minute))
	referralSettlement(t, db, 100, referralTestTime.Add(time.Hour), referralTestTime.Add(2*time.Hour))
	r, err := ReconcileZTAPIReferralSettlements(context.Background(), 100)
	require.NoError(t, err)
	require.Equal(t, 1, r.Created)
	_, err = SetZTAPIReferralCampaign(context.Background(), ZTAPIReferralCampaignUpdate{ExpectedVersion: 1, OperatorID: 1, Enabled: false, Now: referralTestTime.Add(time.Hour)})
	require.NoError(t, err)
	referralBind(t, db, 3, 1, referralTestTime.Add(2*time.Hour))
	require.NoError(t, db.Model(&ZTAPIReferralBinding{}).Count(&count).Error)
	require.Equal(t, int64(1), count)
}

func TestZTAPIReferralBindingAdmissionAccountLockOrder(t *testing.T) {
	db := referralTestDB(t)
	referralEnable(t)
	admitted, locked := false, false
	violations := []string{}
	ZTAPIReferralBindingAdmissionGuard = func(*gorm.DB, time.Time) error { admitted = true; return nil }
	require.NoError(t, db.Callback().Query().Before("gorm:query").Register("referral_test:binding_order", func(tx *gorm.DB) {
		if tx.Statement.Table == "ztapi_referral_account_locks" {
			if !admitted {
				violations = append(violations, "account before admission")
			}
			locked = true
		}
		if tx.Statement.Table == "users" && (!admitted || !locked) {
			violations = append(violations, "inviter read before admitted wallet lock")
		}
	}))
	t.Cleanup(func() { _ = db.Callback().Query().Remove("referral_test:binding_order") })
	referralBind(t, db, 2, 1, referralTestTime)
	require.True(t, locked)
	require.Empty(t, violations)
	_, err := SetZTAPIReferralCampaign(context.Background(), ZTAPIReferralCampaignUpdate{ExpectedVersion: 1, OperatorID: 1, Enabled: false, Now: referralTestTime.Add(time.Hour)})
	require.NoError(t, err)
	admitted, locked = false, false
	referralBind(t, db, 3, 1, referralTestTime.Add(2*time.Hour))
	require.False(t, admitted)
	require.False(t, locked)
}

func TestZTAPIReferralExplicitTimestampNotORMClock(t *testing.T) {
	db := referralTestDB(t)
	c := ZTAPIReferralCampaign{ID: 1, RuleVersion: ZTAPIReferralRuleVersion}
	require.NoError(t, db.Create(&c).Error)
	require.Zero(t, c.UpdatedAt, "campaign seed must not invent an activation/update time")
	referralEnable(t)
	referralBind(t, db, 2, 1, referralTestTime)
	referralSettlement(t, db, 400, referralTestTime.Add(time.Hour), referralTestTime.Add(2*time.Hour))
	_, err := ReconcileZTAPIReferralSettlements(context.Background(), 100)
	require.NoError(t, err)
	now := referralTestTime.Add(2 * time.Hour).Add(7 * 24 * time.Hour)
	w := referralRequest(t, 20_000_000, now, "explicit-time")
	in := referralAction(w, now)
	in.FeeMicros = 1_000_000
	w, err = QuoteZTAPIReferralWithdrawal(context.Background(), in)
	require.NoError(t, err)
	require.Equal(t, now.Unix(), w.UpdatedAt)
}

func TestZTAPIReferralWalletLockBeforeMutableSnapshot(t *testing.T) {
	db, order, now := referralFund(t, 800)
	w := referralRequest(t, 20_000_000, now, "lock-order")
	locked := false
	violations := []string{}
	require.NoError(t, db.Callback().Query().Before("gorm:query").Register("referral_test:lock_order", func(tx *gorm.DB) {
		if _, inTransaction := tx.Statement.ConnPool.(*sql.Tx); !inTransaction {
			return
		}
		if tx.Statement.Table == "ztapi_referral_account_locks" {
			locked = true
		}
		if !locked && (tx.Statement.Table == "ztapi_referral_withdrawals" || tx.Statement.Table == "ztapi_referral_commissions") {
			violations = append(violations, tx.Statement.Table)
		}
	}))
	t.Cleanup(func() { _ = db.Callback().Query().Remove("referral_test:lock_order") })
	in := referralAction(w, now)
	in.FeeMicros = 1_000_000
	w, err := QuoteZTAPIReferralWithdrawal(context.Background(), in)
	require.NoError(t, err)
	locked = false
	_, err = ReverseZTAPIReferralRechargePrincipal(context.Background(), ZTAPIReferralRefundInput{OrderID: order.ID, CumulativePrincipalMicros: 100_000_000, OperatorID: 1, IdempotencyKey: "lock-refund", EvidenceReference: "verified-lock-refund", Reason: "local", At: now})
	require.NoError(t, err)
	locked = false
	w, err = ConfirmZTAPIReferralWithdrawal(context.Background(), referralAction(w, now))
	require.NoError(t, err)
	w, err = ClaimZTAPIReferralPaymentIntent(context.Background(), referralAction(w, now))
	require.NoError(t, err)
	locked = false
	referralPay(t, w, now)
	require.Empty(t, violations, "wallet serialization must precede mutable snapshot reads")
}

func TestZTAPIReferralRefundEvidenceCannotChangeIdentityWithNewKey(t *testing.T) {
	_, order, now := referralFund(t, 800)
	in := ZTAPIReferralRefundInput{OrderID: order.ID, CumulativePrincipalMicros: 100_000_000, OperatorID: 1, IdempotencyKey: "refund-source1", EvidenceReference: "verified-unique-receipt", Reason: "local", At: now}
	c, err := ReverseZTAPIReferralRechargePrincipal(context.Background(), in)
	require.NoError(t, err)
	in.IdempotencyKey = "refund-source2"
	replayed, err := ReverseZTAPIReferralRechargePrincipal(context.Background(), in)
	require.NoError(t, err)
	require.Equal(t, c.ReversedMicros, replayed.ReversedMicros)
	in.IdempotencyKey = "refund-source3"
	in.CumulativePrincipalMicros = 200_000_000
	_, err = ReverseZTAPIReferralRechargePrincipal(context.Background(), in)
	require.Error(t, err, "same verified refund cannot assert a different cumulative principal")
}

func TestZTAPIReferralRefundEvidenceReplayAlsoReservesNewRequestKey(t *testing.T) {
	_, order, now := referralFund(t, 800)
	in := ZTAPIReferralRefundInput{OrderID: order.ID, CumulativePrincipalMicros: 100_000_000, OperatorID: 1, IdempotencyKey: "first-key", EvidenceReference: "one-refund-receipt", Reason: "local", At: now}
	_, err := ReverseZTAPIReferralRechargePrincipal(context.Background(), in)
	require.NoError(t, err)
	in.IdempotencyKey = "alias-key"
	_, err = ReverseZTAPIReferralRechargePrincipal(context.Background(), in)
	require.NoError(t, err)
	in.EvidenceReference = "another-receipt"
	in.CumulativePrincipalMicros = 200_000_000
	_, err = ReverseZTAPIReferralRechargePrincipal(context.Background(), in)
	require.Error(t, err, "an acknowledged request key cannot be repurposed after evidence replay")
}

func TestZTAPIReferralRefundConsumesUnpaidBeforePaidPreventsRewithdrawal(t *testing.T) {
	_, order, now := referralFund(t, 2000)
	referralPay(t, referralClaim(t, referralRequest(t, 40_000_000, now, "paid-first"), now), now)
	referralRequest(t, 60_000_000, now, "unpaid-second")
	c, err := ReverseZTAPIReferralRechargePrincipal(context.Background(), ZTAPIReferralRefundInput{OrderID: order.ID, CumulativePrincipalMicros: 1_200_000_000, OperatorID: 1, IdempotencyKey: "unpaid-first-refund", EvidenceReference: "verified-unpaid-first", Reason: "local", At: now})
	require.NoError(t, err)
	require.Zero(t, c.ManualLossMicros, "unpaid allocations cover the entire refund")
	ws, _, err := ListZTAPIReferralWithdrawals(context.Background(), 1, 1, 10, "manual_review")
	require.NoError(t, err)
	require.Len(t, ws, 1)
	_, err = CancelZTAPIReferralWithdrawal(context.Background(), referralAction(&ws[0], now))
	require.NoError(t, err)
	s, err := GetZTAPIReferralSummary(context.Background(), 1, now)
	require.NoError(t, err)
	require.Zero(t, s.AvailableCommissionMicros)
	require.Zero(t, s.ManualLossMicros)
	_, err = RequestZTAPIReferralWithdrawal(context.Background(), ZTAPIReferralWithdrawalRequest{UserID: 1, AmountMicros: 20_000_000, Address: USDTTopUpContractAddress, IdempotencyKey: "forbidden-rewithdraw", Now: now})
	require.Error(t, err)
}

func TestZTAPIReferralPaymentTransferIdentityCannotPayTwoWithdrawals(t *testing.T) {
	_, _, now := referralFund(t, 800)
	first := referralClaim(t, referralRequest(t, 20_000_000, now, "pay1"), now)
	second := referralClaim(t, referralRequest(t, 20_000_000, now, "pay2"), now)
	first = referralPay(t, first, now)
	in := ZTAPIReferralPaymentConfirmation{WithdrawalID: second.ID, ExpectedVersion: second.Version, OperatorID: 11, Evidence: referralEvidence(second, now), Now: now}
	_, err := ConfirmZTAPIReferralManualPayment(context.Background(), in)
	require.Error(t, err)
	in.Evidence.LogIndex = 1
	paid, err := ConfirmZTAPIReferralManualPayment(context.Background(), in)
	require.NoError(t, err)
	require.Equal(t, "paid", paid.Status)
	_, err = ConfirmZTAPIReferralManualPayment(context.Background(), in)
	require.NoError(t, err, "exact verified evidence replay is idempotent")
	in.Evidence.To = "different"
	_, err = ConfirmZTAPIReferralManualPayment(context.Background(), in)
	require.Error(t, err)
}

func TestZTAPIReferralWithdrawalFeeChangeRequiresFreshConfirmation(t *testing.T) {
	_, _, now := referralFund(t, 400)
	w := referralRequest(t, 20_000_000, now, "requote")
	in := referralAction(w, now)
	in.FeeMicros = 1_000_000
	w, err := QuoteZTAPIReferralWithdrawal(context.Background(), in)
	require.NoError(t, err)
	w, err = ConfirmZTAPIReferralWithdrawal(context.Background(), referralAction(w, now))
	require.NoError(t, err)
	in = referralAction(w, now)
	in.FeeMicros = 2_000_000
	w, err = QuoteZTAPIReferralWithdrawal(context.Background(), in)
	require.NoError(t, err)
	_, err = ClaimZTAPIReferralPaymentIntent(context.Background(), referralAction(w, now))
	require.Error(t, err)
	in = referralAction(w, now)
	in.UserID = 2
	_, err = ConfirmZTAPIReferralWithdrawal(context.Background(), in)
	require.Error(t, err)
	w, err = ConfirmZTAPIReferralWithdrawal(context.Background(), referralAction(w, now))
	require.NoError(t, err)
	w, err = ClaimZTAPIReferralPaymentIntent(context.Background(), referralAction(w, now))
	require.NoError(t, err)
	_, err = QuoteZTAPIReferralWithdrawal(context.Background(), referralAction(w, now))
	require.Error(t, err)
}

func TestZTAPIReferralPaidRefundAndAuditFailureAreAtomic(t *testing.T) {
	db, order, now := referralFund(t, 800)
	w := referralPay(t, referralClaim(t, referralRequest(t, 30_000_000, now, "paid-refund"), now), now)
	in := ZTAPIReferralRefundInput{OrderID: order.ID, CumulativePrincipalMicros: 400_000_000, OperatorID: 1, IdempotencyKey: "paid-half", EvidenceReference: "verified-paid-half", Reason: "local", At: now}
	require.NoError(t, db.Exec(`CREATE TRIGGER referral_reject_refund_audit BEFORE INSERT ON ztapi_referral_audits WHEN NEW.action = 'refund' BEGIN SELECT RAISE(ABORT, 'local refund rollback'); END`).Error)
	_, err := ReverseZTAPIReferralRechargePrincipal(context.Background(), in)
	require.Error(t, err)
	s, err := GetZTAPIReferralSummary(context.Background(), 1, now)
	require.NoError(t, err)
	require.Zero(t, s.ReversedCommissionMicros)
	require.Zero(t, s.ManualLossMicros)
	require.Equal(t, int64(10_000_000), s.AvailableCommissionMicros)
	require.NoError(t, db.Exec("DROP TRIGGER referral_reject_refund_audit").Error)
	c, err := ReverseZTAPIReferralRechargePrincipal(context.Background(), in)
	require.NoError(t, err)
	require.Equal(t, int64(20_000_000), c.ReversedMicros)
	require.Equal(t, int64(10_000_000), c.ManualLossMicros)
	_, err = ReverseZTAPIReferralRechargePrincipal(context.Background(), in)
	require.NoError(t, err)
	s, err = GetZTAPIReferralSummary(context.Background(), 1, now)
	require.NoError(t, err)
	require.Zero(t, s.AvailableCommissionMicros)
	require.Equal(t, int64(10_000_000), s.ManualLossMicros)
	require.Equal(t, w.AmountMicros, s.PaidCommissionMicros)
}

func TestZTAPIReferralReadinessUnknownFeesAndSourcesBlockEnable(t *testing.T) {
	referralTestDB(t)
	for _, mutate := range []func(*ZTAPIReferralReadinessInput){
		func(in *ZTAPIReferralReadinessInput) { in.PlatformFeeKnown = false },
		func(in *ZTAPIReferralReadinessInput) { in.PlatformFeeEvidence = "" },
		func(in *ZTAPIReferralReadinessInput) { in.WorkerReady = false },
		func(in *ZTAPIReferralReadinessInput) { in.FinanceReady = false },
		func(in *ZTAPIReferralReadinessInput) { in.PriceGate.Items[0].ModelMappingKnown = false },
	} {
		in := referralReady()
		mutate(&in)
		r, err := CheckZTAPIReferralReadiness(context.Background(), in)
		require.NoError(t, err)
		require.False(t, r.Ready)
		require.NotEmpty(t, r.BlockedReasons)
		_, err = SetZTAPIReferralCampaign(context.Background(), ZTAPIReferralCampaignUpdate{OperatorID: 1, Enabled: true, Now: referralTestTime, Readiness: in})
		require.Error(t, err)
	}
}

func TestZTAPIReferralReconcileCyclicScanRepairsOlderEvidence(t *testing.T) {
	db := referralTestDB(t)
	referralEnable(t)
	referralBind(t, db, 2, 1, referralTestTime)
	bad := referralSettlement(t, db, 100, referralTestTime.Add(time.Hour), referralTestTime.Add(2*time.Hour))
	good := referralSettlement(t, db, 200, referralTestTime.Add(time.Hour), referralTestTime.Add(2*time.Hour))
	require.NoError(t, db.Model(&TopUp{}).Where("id = ?", bad.TopUpID).Update("status", "pending").Error)
	r, err := ReconcileZTAPIReferralSettlements(context.Background(), 1)
	require.NoError(t, err)
	require.Zero(t, r.Created)
	r, err = ReconcileZTAPIReferralSettlements(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, 1, r.Created)
	require.NoError(t, db.Model(&TopUp{}).Where("id = ?", bad.TopUpID).Update("status", "success").Error)
	r, err = ReconcileZTAPIReferralSettlements(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, 1, r.Created)
	var cs []ZTAPIReferralCommission
	require.NoError(t, db.Order("order_id").Find(&cs).Error)
	require.Len(t, cs, 2)
	require.Equal(t, good.ID, cs[1].OrderID)
}

func TestZTAPIReferralImmutabilityTableAliasAndAudit(t *testing.T) {
	db, _, now := referralFund(t, 400)
	for _, table := range []string{"ztapi_referral_ledger_entries AS e", "ztapi_referral_audits AS a", "ztapi_referral_bindings AS b"} {
		err := db.Session(&gorm.Session{SkipHooks: true}).Table(table).Where("id > ?", 0).Update("id", 100).Error
		require.ErrorIs(t, err, ErrZTAPIReferralImmutable, table)
	}
	w := referralClaim(t, referralRequest(t, 20_000_000, now, "holder"), now)
	in := ZTAPIReferralPaymentConfirmation{WithdrawalID: w.ID, ExpectedVersion: w.Version, OperatorID: 999, Evidence: referralEvidence(w, now), Now: now}
	_, err := ConfirmZTAPIReferralManualPayment(context.Background(), in)
	require.Error(t, err)
	in.OperatorID = 11
	in.Evidence.TxID = strings.Repeat("0", 64)
	in.Evidence.LogIndex = -1
	_, err = ConfirmZTAPIReferralManualPayment(context.Background(), in)
	require.Error(t, err)
}
