package model

import (
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

func TestDebtSettlementCanBeRepaidByAdminAdjustment(t *testing.T) {
	db := setupBalanceLedgerTestDB(t)
	user := createBalanceLedgerTestUser(t, db, "debt-admin-recovery", 100)

	if _, applied, err := SettleUserBalanceDebit(user.Id, 500, "debt-admin-settlement", "request-debt-admin"); err != nil || !applied {
		t.Fatalf("settle user into debt: applied=%v err=%v", applied, err)
	}
	assertBalanceRecoveryUser(t, db, user.Id, -400, common.UserStatusDisabled)

	if _, applied, err := AdjustUserBalance(BalanceAdjustment{
		UserID:         user.Id,
		OperatorID:     1,
		Delta:          100,
		Reason:         "partial debt repayment",
		IdempotencyKey: "debt-admin-partial",
		RequestID:      "request-debt-admin-partial",
		SourceType:     BalanceLedgerSourceAdmin,
	}); err != nil || !applied {
		t.Fatalf("partially repay debt: applied=%v err=%v", applied, err)
	}
	assertBalanceRecoveryUser(t, db, user.Id, -300, common.UserStatusDisabled)

	if _, applied, err := AdjustUserBalance(BalanceAdjustment{
		UserID:         user.Id,
		OperatorID:     1,
		Delta:          400,
		Reason:         "complete debt repayment",
		IdempotencyKey: "debt-admin-complete",
		RequestID:      "request-debt-admin-complete",
		SourceType:     BalanceLedgerSourceAdmin,
	}); err != nil || !applied {
		t.Fatalf("fully repay debt: applied=%v err=%v", applied, err)
	}
	assertBalanceRecoveryUser(t, db, user.Id, 100, common.UserStatusEnabled)
}

func TestDebtRecoveryDoesNotEnableManuallyDisabledUser(t *testing.T) {
	db := setupBalanceLedgerTestDB(t)
	user := createBalanceLedgerTestUser(t, db, "manual-disable-debt", -50)
	if err := db.Model(&User{}).Where("id = ?", user.Id).Update("status", common.UserStatusDisabled).Error; err != nil {
		t.Fatalf("manually disable user: %v", err)
	}

	if _, applied, err := AdjustUserBalance(BalanceAdjustment{
		UserID:         user.Id,
		OperatorID:     1,
		Delta:          100,
		Reason:         "repay manually disabled account",
		IdempotencyKey: "manual-disable-repayment",
		RequestID:      "request-manual-disable",
		SourceType:     BalanceLedgerSourceAdmin,
	}); err != nil || !applied {
		t.Fatalf("repay manually disabled account: applied=%v err=%v", applied, err)
	}
	assertBalanceRecoveryUser(t, db, user.Id, 50, common.UserStatusDisabled)
}

func TestExplicitAdminDisableOverridesDebtAutoRecovery(t *testing.T) {
	db := setupBalanceLedgerTestDB(t)
	user := createBalanceLedgerTestUser(t, db, "explicit-disable-debt", 100)
	if _, applied, err := SettleUserBalanceDebit(user.Id, 500, "explicit-disable-settlement", "request-explicit-disable"); err != nil || !applied {
		t.Fatalf("settle user into debt: applied=%v err=%v", applied, err)
	}

	updated, err := UpdateZTAPIUserStatus(
		user.Id,
		common.RoleRootUser,
		common.UserStatusDisabled,
		common.UserStatusDisabled,
	)
	if err != nil {
		t.Fatalf("explicitly disable indebted user: %v", err)
	}
	if updated.DebtSuspended {
		t.Fatal("explicit admin disable did not clear debt suspension marker")
	}

	if _, applied, err := AdjustUserBalance(BalanceAdjustment{
		UserID:         user.Id,
		OperatorID:     1,
		Delta:          500,
		Reason:         "repay explicitly disabled account",
		IdempotencyKey: "explicit-disable-repayment",
		RequestID:      "request-explicit-disable-repayment",
		SourceType:     BalanceLedgerSourceAdmin,
	}); err != nil || !applied {
		t.Fatalf("repay explicitly disabled account: applied=%v err=%v", applied, err)
	}
	assertBalanceRecoveryUser(t, db, user.Id, 100, common.UserStatusDisabled)
}

func TestDebtSuspendedUserCannotBeEnabledBeforeRepayment(t *testing.T) {
	db := setupBalanceLedgerTestDB(t)
	user := createBalanceLedgerTestUser(t, db, "premature-debt-enable", 100)
	if _, applied, err := SettleUserBalanceDebit(user.Id, 500, "premature-enable-settlement", "request-premature-enable"); err != nil || !applied {
		t.Fatalf("settle user into debt: applied=%v err=%v", applied, err)
	}

	if _, err := UpdateZTAPIUserStatus(
		user.Id,
		common.RoleRootUser,
		common.UserStatusEnabled,
		common.UserStatusDisabled,
	); err == nil {
		t.Fatal("enabled a user whose debt is still outstanding")
	}

	var stored User
	if err := db.First(&stored, user.Id).Error; err != nil {
		t.Fatalf("reload indebted user: %v", err)
	}
	if stored.Quota != -400 || stored.Status != common.UserStatusDisabled || !stored.DebtSuspended {
		t.Fatalf("indebted user mutated after rejected enable: quota/status/debt=%d/%d/%v", stored.Quota, stored.Status, stored.DebtSuspended)
	}
}

func TestDebtSettlementCanBeClearedByWalletRefund(t *testing.T) {
	db := setupBalanceLedgerTestDB(t)
	if err := db.AutoMigrate(&BillingRefundPending{}); err != nil {
		t.Fatalf("migrate billing refund: %v", err)
	}
	user := createBalanceLedgerTestUser(t, db, "debt-refund-recovery", 40)
	if _, applied, err := SettleUserBalanceDebit(user.Id, 90, "debt-refund-settlement", "request-debt-refund"); err != nil || !applied {
		t.Fatalf("settle user into debt: applied=%v err=%v", applied, err)
	}

	pending, err := EnsureBillingRefundPending(BillingRefundPending{
		UserID:         user.Id,
		Amount:         75,
		Kind:           BillingRefundKindWallet,
		IdempotencyKey: "billing-refund:debt-recovery:wallet",
		RequestID:      "request-debt-refund-recovery",
	})
	if err != nil {
		t.Fatalf("create pending refund: %v", err)
	}
	if err := ProcessBillingRefundPending(pending.ID); err != nil {
		t.Fatalf("process refund into indebted account: %v", err)
	}

	assertBalanceRecoveryUser(t, db, user.Id, 25, common.UserStatusEnabled)
	var stored BillingRefundPending
	if err := db.First(&stored, pending.ID).Error; err != nil {
		t.Fatalf("reload refund: %v", err)
	}
	if stored.Status != BillingRefundStatusCompleted {
		t.Fatalf("refund status=%q, want %q", stored.Status, BillingRefundStatusCompleted)
	}
}

func TestDebtSettlementCanBeClearedByAdminTopUp(t *testing.T) {
	db := setupBalanceLedgerTestDB(t)
	if err := db.AutoMigrate(&TopUp{}); err != nil {
		t.Fatalf("migrate topup: %v", err)
	}
	user := createBalanceLedgerTestUser(t, db, "debt-topup-recovery", 40)
	if _, applied, err := SettleUserBalanceDebit(user.Id, 90, "debt-topup-settlement", "request-debt-topup"); err != nil || !applied {
		t.Fatalf("settle user into debt: applied=%v err=%v", applied, err)
	}

	originalQuotaPerUnit := common.QuotaPerUnit
	common.QuotaPerUnit = 100
	t.Cleanup(func() { common.QuotaPerUnit = originalQuotaPerUnit })
	topUp := TopUp{
		UserId:          user.Id,
		Amount:          1,
		TradeNo:         "debt-topup-recovery-order",
		PaymentMethod:   "alipay",
		PaymentProvider: PaymentProviderEpay,
		Status:          common.TopUpStatusPending,
	}
	if err := db.Create(&topUp).Error; err != nil {
		t.Fatalf("create topup: %v", err)
	}
	if _, err := ProcessAdminTopUp(AdminTopUpMutation{
		ID:             topUp.Id,
		OperatorID:     1,
		ExpectedStatus: common.TopUpStatusPending,
		TargetStatus:   common.TopUpStatusSuccess,
		Reason:         "confirm paid debt recovery",
		RequestID:      "request-debt-topup-recovery",
	}); err != nil {
		t.Fatalf("complete topup into indebted account: %v", err)
	}

	assertBalanceRecoveryUser(t, db, user.Id, 50, common.UserStatusEnabled)
	if err := db.First(&topUp, topUp.Id).Error; err != nil {
		t.Fatalf("reload topup: %v", err)
	}
	if topUp.Status != common.TopUpStatusSuccess {
		t.Fatalf("topup status=%q, want %q", topUp.Status, common.TopUpStatusSuccess)
	}
}

func TestBillingRefundStopsRetryingAtDeadLetterLimit(t *testing.T) {
	db := setupBalanceLedgerTestDB(t)
	if err := db.AutoMigrate(&BillingRefundPending{}); err != nil {
		t.Fatalf("migrate billing refund: %v", err)
	}
	user := createBalanceLedgerTestUser(t, db, "refund-dead-letter", 100)
	pending, err := EnsureBillingRefundPending(BillingRefundPending{
		UserID:         user.Id,
		Amount:         10,
		Kind:           BillingRefundKindWallet,
		IdempotencyKey: "billing-refund:dead-letter:wallet",
		RequestID:      "request-refund-dead-letter",
	})
	if err != nil {
		t.Fatalf("create pending refund: %v", err)
	}
	if err := db.Delete(&user).Error; err != nil {
		t.Fatalf("delete refund user: %v", err)
	}

	const maxAttempts = 10
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if err := ProcessBillingRefundPending(pending.ID); err == nil {
			t.Fatalf("attempt %d unexpectedly succeeded", attempt)
		}
	}

	var stored BillingRefundPending
	if err := db.First(&stored, pending.ID).Error; err != nil {
		t.Fatalf("reload dead-letter refund: %v", err)
	}
	if stored.Status != "dead_letter" || stored.Attempts != maxAttempts {
		t.Fatalf("refund status=%q attempts=%d, want dead_letter/%d", stored.Status, stored.Attempts, maxAttempts)
	}
	lastError := stored.LastError
	if err := ProcessBillingRefundPending(pending.ID); err == nil || !strings.Contains(err.Error(), "dead letter") {
		t.Fatalf("processing dead letter err=%v, want explicit dead-letter error", err)
	}
	if err := db.First(&stored, pending.ID).Error; err != nil {
		t.Fatalf("reload refund after extra attempt: %v", err)
	}
	if stored.Attempts != maxAttempts || stored.LastError != lastError {
		t.Fatalf("dead letter mutated after retry: attempts=%d error=%q", stored.Attempts, stored.LastError)
	}

	processed, err := ProcessPendingBillingRefunds(10)
	if err != nil || processed != 0 {
		t.Fatalf("pending worker processed dead letter: processed=%d err=%v", processed, err)
	}
	pendingCount, err := CountPendingBillingRefunds()
	if err != nil || pendingCount != 0 {
		t.Fatalf("pending count=%d err=%v, want 0", pendingCount, err)
	}
	deadLetterCount, err := CountDeadLetterBillingRefunds()
	if err != nil || deadLetterCount != 1 {
		t.Fatalf("dead-letter count=%d err=%v, want 1", deadLetterCount, err)
	}
}

func assertBalanceRecoveryUser(t *testing.T, db *gorm.DB, userID, wantQuota, wantStatus int) {
	t.Helper()
	var user User
	if err := db.First(&user, userID).Error; err != nil {
		t.Fatalf("reload user %d: %v", userID, err)
	}
	if user.Quota != wantQuota || user.Status != wantStatus {
		t.Fatalf("user %d quota/status=%d/%d, want %d/%d", userID, user.Quota, user.Status, wantQuota, wantStatus)
	}
}
