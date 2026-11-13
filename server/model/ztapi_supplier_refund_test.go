package model

import (
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

type supplierRefundFixture struct {
	db      *gorm.DB
	user    User
	admin   User
	token   Token
	charge  ZTAPISupplierRefundCharge
	request ZTAPIRequestSettlement
}

func newSupplierRefundFixture(t *testing.T) supplierRefundFixture {
	t.Helper()
	db := setupBalanceLedgerTestDB(t)
	return seedSupplierRefundFixture(t, db, "")
}

func seedSupplierRefundFixture(t *testing.T, db *gorm.DB, suffix string) supplierRefundFixture {
	t.Helper()
	if err := db.AutoMigrate(&Token{}, &ZTAPIRequestSettlement{}); err != nil {
		t.Fatal(err)
	}
	if err := MigrateZTAPISupplierRefund(db); err != nil {
		t.Fatal(err)
	}
	user := createBalanceLedgerTestUser(t, db, "sr-customer"+suffix, 900)
	admin := createBalanceLedgerTestUser(t, db, "sr-finance"+suffix, 0)
	if err := db.Model(&admin).Update("role", common.RoleAdminUser).Error; err != nil {
		t.Fatal(err)
	}
	token := Token{UserId: user.Id, KeyHash: ztapiSupplierRefundHash("supplier-test-token" + suffix), RemainQuota: 900, UsedQuota: 100, Status: common.TokenStatusEnabled}
	if err := db.Create(&token).Error; err != nil {
		t.Fatal(err)
	}
	ledger := BalanceLedger{UserID: user.Id, Delta: -100, BalanceBefore: 1000, BalanceAfter: 900, RequestID: "supplier-request" + suffix, IdempotencyKey: "supplier-original" + suffix, SourceType: BalanceLedgerSourceUsageSettlement, Reason: "original final charge"}
	if err := db.Create(&ledger).Error; err != nil {
		t.Fatal(err)
	}
	dims, err := common.Marshal([]ZTAPISupplierRefundDimension{
		{Dimension: "input", Units: "10", UnitQuota: "2", ChargedQuota: 20, TokenChargedQuota: 20},
		{Dimension: "output", Units: "10", UnitQuota: "8", ChargedQuota: 80, TokenChargedQuota: 80},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := ZTAPIRequestSettlement{OperationID: "supplier-operation" + suffix, RequestID: ledger.RequestID, UserID: user.Id, TokenID: token.Id, PublicModel: "test", PriceSnapshotJSON: `{"input":"2","output":"8"}`, Status: "settled", ChargedQuota: 100, TokenChargedQuota: 100, ChargeDimensionsJSON: string(dims), LastLedgerID: ledger.ID}
	if err := db.Create(&request).Error; err != nil {
		t.Fatal(err)
	}
	candidate := ZTAPISupplierRefundCharge{RequestID: request.RequestID, Attempt: 2, ChannelID: 20, CredentialVersion: "credential-v1", UpstreamRequestID: "supplier-wire-2"}
	var charge *ZTAPISupplierRefundCharge
	err = db.Transaction(func(tx *gorm.DB) error {
		var err error
		charge, err = RecordZTAPISupplierRefundChargeTx(tx, candidate)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return supplierRefundFixture{db: db, user: user, admin: admin, token: token, charge: *charge, request: request}
}

func (f supplierRefundFixture) input(proofID string) ZTAPISupplierRefundSubmission {
	return ZTAPISupplierRefundSubmission{Source: "supplier-a:account-1", ProofID: proofID, RequestID: f.request.RequestID, UserID: f.user.Id, Attempt: f.charge.Attempt, ChannelID: f.charge.ChannelID, CredentialVersion: f.charge.CredentialVersion, UpstreamRequestID: f.charge.UpstreamRequestID, Mode: "full", EvidenceReference: "verified-statement-row:1"}
}

func (f supplierRefundFixture) approved(t *testing.T, input ZTAPISupplierRefundSubmission) *ZTAPISupplierRefund {
	t.Helper()
	p, err := SubmitZTAPISupplierRefund(input)
	if err != nil {
		t.Fatal(err)
	}
	if err := ApproveZTAPISupplierRefund(p.ID, f.admin.Id, "finance-review:1"); err != nil {
		t.Fatal(err)
	}
	return p
}

func (f supplierRefundFixture) assertBalance(t *testing.T, want int) {
	t.Helper()
	var user User
	var token Token
	if err := f.db.First(&user, f.user.Id).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.First(&token, f.token.Id).Error; err != nil {
		t.Fatal(err)
	}
	if user.Quota != want || token.RemainQuota != want || token.UsedQuota != 1000-want {
		t.Fatalf("wallet=%d token=%d used=%d want=%d", user.Quota, token.RemainQuota, token.UsedQuota, want)
	}
}

func TestZTAPISupplierRefundRequiresSeparateEnabledFinanceApproval(t *testing.T) {
	f := newSupplierRefundFixture(t)
	p, err := SubmitZTAPISupplierRefund(f.input("trust-boundary"))
	if err != nil {
		t.Fatal(err)
	}
	if err := ProcessZTAPISupplierRefund(p.ID); !errors.Is(err, ErrZTAPISupplierRefundPending) {
		t.Fatalf("unapproved processing: %v", err)
	}
	if err := ApproveZTAPISupplierRefund(p.ID, f.user.Id, "customer-says-trusted"); !errors.Is(err, ErrZTAPISupplierRefundUnauthorized) {
		t.Fatalf("customer approval: %v", err)
	}
	if err := f.db.Model(&User{}).Where("id = ?", f.admin.Id).Update("status", common.UserStatusDisabled).Error; err != nil {
		t.Fatal(err)
	}
	if err := ApproveZTAPISupplierRefund(p.ID, f.admin.Id, "disabled-admin"); !errors.Is(err, ErrZTAPISupplierRefundUnauthorized) {
		t.Fatalf("disabled admin approval: %v", err)
	}
	f.assertBalance(t, 900)
}

func TestZTAPISupplierRefundFullReplayAndSourceIdentity(t *testing.T) {
	f := newSupplierRefundFixture(t)
	in := f.input("proof-1")
	p := f.approved(t, in)
	for i := 0; i < 3; i++ {
		if err := ProcessZTAPISupplierRefund(p.ID); err != nil {
			t.Fatal(err)
		}
	}
	f.assertBalance(t, 1000)
	var request ZTAPIRequestSettlement
	if err := f.db.First(&request, f.request.ID).Error; err != nil {
		t.Fatal(err)
	}
	if request.RefundedQuota != 100 {
		t.Fatalf("request refund=%d", request.RefundedQuota)
	}
	var count int64
	if err := f.db.Model(&BalanceLedger{}).Where("source_type = ?", BalanceLedgerSourceSupplierRefund).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("refund ledger count=%d", count)
	}
	same, err := SubmitZTAPISupplierRefund(in)
	if err != nil || same.ID != p.ID {
		t.Fatalf("proof replay=%+v %v", same, err)
	}
	in.UserID++
	if _, err := SubmitZTAPISupplierRefund(in); !errors.Is(err, ErrZTAPISupplierRefundConflict) {
		t.Fatalf("proof identity rebind accepted: %v", err)
	}
	in = f.input("proof-1")
	in.Source = "supplier-b:account-2"
	other, err := SubmitZTAPISupplierRefund(in)
	if err != nil || other.ID == p.ID {
		t.Fatalf("source not namespaced: %+v %v", other, err)
	}
}

func TestZTAPISupplierRefundPartialThenFullNeverExceedsOriginal(t *testing.T) {
	f := newSupplierRefundFixture(t)
	in := f.input("partial-1")
	in.Mode = "partial"
	in.Units = []ZTAPISupplierRefundUnits{{"output", "2.5"}}
	p := f.approved(t, in)
	if err := ProcessZTAPISupplierRefund(p.ID); err != nil {
		t.Fatal(err)
	}
	f.assertBalance(t, 920)
	in.ProofID = "over-limit"
	in.Units = []ZTAPISupplierRefundUnits{{"output", "8"}}
	bad := f.approved(t, in)
	if err := ProcessZTAPISupplierRefund(bad.ID); !errors.Is(err, ErrZTAPISupplierRefundPending) {
		t.Fatalf("over-limit=%v", err)
	}
	f.assertBalance(t, 920)
	full := f.approved(t, f.input("remaining-full"))
	if err := ProcessZTAPISupplierRefund(full.ID); err != nil {
		t.Fatal(err)
	}
	f.assertBalance(t, 1000)
	second := f.approved(t, f.input("other-full"))
	if err := ProcessZTAPISupplierRefund(second.ID); err != nil {
		t.Fatal(err)
	}
	f.assertBalance(t, 1000)
}

func TestZTAPISupplierRefundAmbiguousLineageStaysPending(t *testing.T) {
	for _, mutation := range []struct {
		name   string
		change func(*ZTAPISupplierRefundSubmission)
	}{
		{"wrong customer", func(p *ZTAPISupplierRefundSubmission) { p.UserID++ }},
		{"wrong request", func(p *ZTAPISupplierRefundSubmission) { p.RequestID = "missing" }},
		{"earlier failed attempt", func(p *ZTAPISupplierRefundSubmission) { p.Attempt = 1 }},
		{"wrong upstream", func(p *ZTAPISupplierRefundSubmission) { p.UpstreamRequestID = "other" }},
		{"no upstream", func(p *ZTAPISupplierRefundSubmission) { p.UpstreamRequestID = "" }},
		{"wrong credential", func(p *ZTAPISupplierRefundSubmission) { p.CredentialVersion = "old" }},
		{"dollar-only refund", func(p *ZTAPISupplierRefundSubmission) { p.Mode = "partial" }},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			f := newSupplierRefundFixture(t)
			in := f.input("ambiguous")
			mutation.change(&in)
			p := f.approved(t, in)
			if err := ProcessZTAPISupplierRefund(p.ID); !errors.Is(err, ErrZTAPISupplierRefundPending) {
				t.Fatalf("ambiguous evidence=%v", err)
			}
			var row ZTAPISupplierRefund
			if err := f.db.First(&row, p.ID).Error; err != nil {
				t.Fatal(err)
			}
			if row.Status != "pending" || row.PendingReason == "" {
				t.Fatalf("evidence lost: %+v", row)
			}
			f.assertBalance(t, 900)
		})
	}
}

func TestZTAPISupplierRefundAtomicRollbackThenRetry(t *testing.T) {
	f := newSupplierRefundFixture(t)
	p := f.approved(t, f.input("rollback"))
	const callback = "supplier-refund-test:fail-token"
	if err := f.db.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "tokens" {
			_ = tx.AddError(errors.New("injected token failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	if err := ProcessZTAPISupplierRefund(p.ID); err == nil {
		t.Fatal("injected failure ignored")
	}
	f.assertBalance(t, 900)
	var count int64
	if err := f.db.Model(&BalanceLedger{}).Where("source_type = ?", BalanceLedgerSourceSupplierRefund).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("wallet ledger escaped rollback: %d", count)
	}
	if err := f.db.Callback().Update().Remove(callback); err != nil {
		t.Fatal(err)
	}
	if err := ProcessZTAPISupplierRefund(p.ID); err != nil {
		t.Fatal(err)
	}
	f.assertBalance(t, 1000)
}

func TestZTAPISupplierRefundConcurrentNotifications(t *testing.T) {
	f := newSupplierRefundFixture(t)
	p := f.approved(t, f.input("concurrent"))
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- ProcessZTAPISupplierRefund(p.ID) }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	f.assertBalance(t, 1000)
}

func TestZTAPISupplierRefundChargeCannotRebindRequest(t *testing.T) {
	f := newSupplierRefundFixture(t)
	in := ZTAPISupplierRefundCharge{RequestID: f.request.RequestID, Attempt: 1, ChannelID: 10, CredentialVersion: "v1", UpstreamRequestID: "failed-attempt"}
	err := f.db.Transaction(func(tx *gorm.DB) error { _, err := RecordZTAPISupplierRefundChargeTx(tx, in); return err })
	if !errors.Is(err, ErrZTAPISupplierRefundConflict) {
		t.Fatalf("rebound charged attempt: %v", err)
	}
}

func TestZTAPISupplierRefundSubmissionConcurrentReplay(t *testing.T) {
	f := newSupplierRefundFixture(t)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := SubmitZTAPISupplierRefund(f.input("same-proof")); errs <- err }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var count int64
	if err := f.db.Model(&ZTAPISupplierRefund{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatal(fmt.Sprintf("duplicate submissions: %d", count))
	}
}

func TestZTAPISupplierRefundUnlimitedMismatchCannotCommitWallet(t *testing.T) {
	f := newSupplierRefundFixture(t)
	if err := f.db.Model(&ZTAPIRequestSettlement{}).Where("id = ?", f.request.ID).Update("token_unlimited", true).Error; err != nil {
		t.Fatal(err)
	}
	p := f.approved(t, f.input("inconsistent-unlimited"))
	if err := ProcessZTAPISupplierRefund(p.ID); !errors.Is(err, ErrZTAPISupplierRefundPending) {
		t.Fatalf("expected pending: %v", err)
	}
	f.assertBalance(t, 900)
	var count int64
	if err := f.db.Model(&BalanceLedger{}).Where("source_type = ?", BalanceLedgerSourceSupplierRefund).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("pending validation committed %d wallet credits", count)
	}
}

func TestZTAPISupplierRefundCacheFailureResumesWithoutDoubleCredit(t *testing.T) {
	f := newSupplierRefundFixture(t)
	p := f.approved(t, f.input("cache-retry"))
	original := balanceLedgerCacheSync
	t.Cleanup(func() { balanceLedgerCacheSync = original })
	balanceLedgerCacheSync = func(int) error { return errors.New("injected cache outage") }
	if err := ProcessZTAPISupplierRefund(p.ID); !errors.Is(err, ErrBalanceLedgerCacheSync) {
		t.Fatalf("cache error=%v", err)
	}
	f.assertBalance(t, 1000)
	var row ZTAPISupplierRefund
	if err := f.db.First(&row, p.ID).Error; err != nil {
		t.Fatal(err)
	}
	if row.Status != "applied" {
		t.Fatalf("lost applied checkpoint: %+v", row)
	}
	balanceLedgerCacheSync = original
	count, err := RetryZTAPISupplierRefunds(0, 100)
	if err != nil || count != 1 {
		t.Fatalf("recovery count=%d err=%v", count, err)
	}
	f.assertBalance(t, 1000)
	if err := f.db.First(&row, p.ID).Error; err != nil {
		t.Fatal(err)
	}
	if row.Status != "completed" {
		t.Fatalf("retry state=%s", row.Status)
	}
}

func TestZTAPISupplierRefundConcurrentDistinctProofsRespectCap(t *testing.T) {
	f := newSupplierRefundFixture(t)
	var ids []uint
	for i := 0; i < 8; i++ {
		in := f.input(fmt.Sprintf("distinct-%d", i))
		in.Mode = "partial"
		in.Units = []ZTAPISupplierRefundUnits{{"output", "2"}}
		ids = append(ids, f.approved(t, in).ID)
	}
	var wg sync.WaitGroup
	errs := make(chan error, len(ids))
	for _, id := range ids {
		wg.Add(1)
		go func(id uint) { defer wg.Done(); errs <- ProcessZTAPISupplierRefund(id) }(id)
	}
	wg.Wait()
	close(errs)
	pending := 0
	for err := range errs {
		if errors.Is(err, ErrZTAPISupplierRefundPending) {
			pending++
		} else if err != nil && !errors.Is(err, ErrBalanceLedgerCacheSync) {
			t.Fatal(err)
		}
	}
	if pending != 3 {
		t.Fatalf("pending=%d want3", pending)
	}
	// A later concurrent refund may supersede the cache-generation CAS. Its
	// earlier credit is still applied; retry must only finish cache delivery.
	if _, err := RetryZTAPISupplierRefunds(0, 100); err != nil {
		t.Fatal(err)
	}
	f.assertBalance(t, 980)
}

func TestZTAPISupplierRefundChargeRequiresTransaction(t *testing.T) {
	f := newSupplierRefundFixture(t)
	if _, err := RecordZTAPISupplierRefundChargeTx(f.db, ZTAPISupplierRefundCharge{RequestID: f.request.RequestID, Attempt: 2, ChannelID: 20, CredentialVersion: "credential-v1", UpstreamRequestID: "supplier-wire-2"}); err == nil {
		t.Fatal("accepted nontransactional charge recording")
	}
}

func TestZTAPISupplierRefundUnidentifiedEvidenceRetainedButNotApproved(t *testing.T) {
	f := newSupplierRefundFixture(t)
	input := f.input("")
	input.Source = ""
	p, err := SubmitZTAPISupplierRefund(input)
	if err != nil {
		t.Fatal(err)
	}
	if err := ApproveZTAPISupplierRefund(p.ID, f.admin.Id, "verified"); !errors.Is(err, ErrZTAPISupplierRefundEvidence) {
		t.Fatalf("approved missing source/id: %v", err)
	}
	rows, err := ListPendingZTAPISupplierRefunds(0, 100)
	if err != nil || len(rows) != 1 || rows[0].ID != p.ID {
		t.Fatalf("missing review queue record: %+v %v", rows, err)
	}
	f.assertBalance(t, 900)
}

func TestZTAPISupplierRefundSettledRequestRequired(t *testing.T) {
	f := newSupplierRefundFixture(t)
	p := f.approved(t, f.input("unsettled"))
	if err := f.db.Model(&ZTAPIRequestSettlement{}).Where("id = ?", f.request.ID).Update("status", "pending").Error; err != nil {
		t.Fatal(err)
	}
	if err := ProcessZTAPISupplierRefund(p.ID); !errors.Is(err, ErrZTAPISupplierRefundPending) {
		t.Fatalf("refunded unsettled request: %v", err)
	}
	f.assertBalance(t, 900)
}

func TestZTAPISupplierRefundOverflowRollsBackTokenAndLedger(t *testing.T) {
	f := newSupplierRefundFixture(t)
	p := f.approved(t, f.input("overflow"))
	if err := f.db.Model(&User{}).Where("id = ?", f.user.Id).Update("quota", maxBalanceLedgerQuota).Error; err != nil {
		t.Fatal(err)
	}
	if err := ProcessZTAPISupplierRefund(p.ID); !errors.Is(err, ErrBalanceLedgerOverflow) {
		t.Fatalf("overflow=%v", err)
	}
	var token Token
	if err := f.db.First(&token, f.token.Id).Error; err != nil {
		t.Fatal(err)
	}
	if token.RemainQuota != 900 || token.UsedQuota != 100 {
		t.Fatalf("token changed on failed wallet: %+v", token)
	}
}

func TestZTAPISupplierRefundChangedFrozenPricePending(t *testing.T) {
	f := newSupplierRefundFixture(t)
	p := f.approved(t, f.input("changed-price"))
	if err := f.db.Model(&ZTAPIRequestSettlement{}).Where("id = ?", f.request.ID).Update("price_snapshot_json", `{"input":"99"}`).Error; err != nil {
		t.Fatal(err)
	}
	if err := ProcessZTAPISupplierRefund(p.ID); !errors.Is(err, ErrZTAPISupplierRefundPending) {
		t.Fatalf("changed price=%v", err)
	}
	f.assertBalance(t, 900)
}

func TestZTAPISupplierRefundFinancePermissionCanApprove(t *testing.T) {
	f := newSupplierRefundFixture(t)
	if err := f.db.Model(&User{}).Where("id = ?", f.admin.Id).Update("role", common.RoleFinanceUser).Error; err != nil {
		t.Fatal(err)
	}
	p, err := SubmitZTAPISupplierRefund(f.input("finance-role"))
	if err != nil {
		t.Fatal(err)
	}
	if err := ApproveZTAPISupplierRefund(p.ID, f.admin.Id, "finance-role-review"); err != nil {
		t.Fatalf("explicit finance.write authority denied: %v", err)
	}
}

func TestZTAPISupplierRefundRetryQueueDoesNotStarveLaterApprovedProofs(t *testing.T) {
	f := newSupplierRefundFixture(t)
	for i := 0; i < 3; i++ {
		in := f.input(fmt.Sprintf("pending-prefix-%d", i))
		in.RequestID = "unknown-request"
		f.approved(t, in)
	}
	f.approved(t, f.input("eligible-after-pending"))
	for i := 0; i < 4; i++ {
		if _, err := RetryZTAPISupplierRefunds(0, 1); err != nil {
			t.Fatal(err)
		}
	}
	f.assertBalance(t, 1000)
}
