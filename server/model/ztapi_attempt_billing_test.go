package model

import (
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

func TestZTAPIAttemptBillingAdditionalChargeRefundPreservesFinalReplay(t *testing.T) {
	db, row, originalEvidence := setupZTAPISettlementReplay(t, true)
	if err := MigrateZTAPIAttemptBilling(db); err != nil {
		t.Fatal(err)
	}
	if err := db.Transaction(func(tx *gorm.DB) error { return EnsureZTAPIAttemptBillingReviewsTx(tx, &row) }); err != nil {
		t.Fatal(err)
	}
	admin := createBalanceLedgerTestUser(t, db, "attempt-finance", 0)
	if err := db.Model(&admin).Update("role", common.RoleFinanceUser).Error; err != nil {
		t.Fatal(err)
	}
	if err := RecordZTAPIRequestAttemptResponse(row.OperationID, 1, 10, 502, "wire-first"); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&Channel{Id: 10}).Error; err != nil {
		t.Fatal(err)
	}
	proof, err := SubmitZTAPIAttemptBilling(ZTAPIAttemptBillingSubmission{Source: "supplier-a", ProofID: "bill-proof-1", RequestID: row.RequestID, UserID: row.UserID, Attempt: 1, ChannelID: 10, CredentialVersion: "cred-v1", UpstreamRequestID: "wire-first", UpstreamBillID: "bill-line-1", Kind: "billed", Usage: []ZTAPIAttemptBillingQuantity{{Dimension: "input_tokens", Quantity: 40}}, EvidenceReference: "statement-row-1", DistinctUsageReference: "separate executions row1 and final row2"})
	if err != nil {
		t.Fatal(err)
	}
	if err = ProcessZTAPIAttemptBilling(proof.ID, attemptBillingTestLog); !errors.Is(err, ErrZTAPIAttemptBillingPending) {
		t.Fatalf("unapproved debit: %v", err)
	}
	if err = ApproveZTAPIAttemptBilling(proof.ID, admin.Id, "finance-review-1", attemptBillingTestPricer); err != nil {
		t.Fatal(err)
	}
	if err = ProcessZTAPIAttemptBilling(proof.ID, attemptBillingTestLog); err != nil {
		t.Fatal(err)
	}
	if err = ProcessZTAPIAttemptBilling(proof.ID, attemptBillingTestLog); err != nil {
		t.Fatal(err)
	}
	assertZTAPISettlementBalances(t, db, row, 860, 860)
	var user User
	if err = db.First(&user, row.UserID).Error; err != nil {
		t.Fatal(err)
	}
	if user.UsedQuota != 140 || user.RequestCount != 1 {
		t.Fatalf("duplicate logical count or lost extra usage: %+v", user)
	}
	for _, a := range []struct {
		n, ch int
		wire  string
	}{{1, 10, "wire-first"}, {2, 11, "wire-final"}} {
		billID := ""
		if a.n == 1 {
			billID = "bill-line-1"
		}
		p, e := SubmitZTAPISupplierRefund(ZTAPISupplierRefundSubmission{Source: "supplier-a", ProofID: "refund-" + a.wire, RequestID: row.RequestID, UserID: row.UserID, Attempt: a.n, ChannelID: a.ch, CredentialVersion: "cred-v1", UpstreamRequestID: a.wire, UpstreamBillID: billID, Mode: "full", EvidenceReference: "refund-statement"})
		if e != nil {
			t.Fatal(e)
		}
		if e = ApproveZTAPISupplierRefund(p.ID, admin.Id, "verified-refund"); e != nil {
			t.Fatal(e)
		}
		if e = ProcessZTAPISupplierRefund(p.ID); e != nil {
			t.Fatal(e)
		}
		if e = ProcessZTAPISupplierRefund(p.ID); e != nil {
			t.Fatal(e)
		}
	}
	assertZTAPISettlementBalances(t, db, row, 1000, 1000)
	if _, err = FinalizeZTAPIRequestSettlementWithEvidence(row.OperationID, 100, settlementReplayUsage, settlementReplayDimensions, originalEvidence); err != nil {
		t.Fatal(err)
	}
	var saved ZTAPIRequestSettlement
	if err = db.First(&saved, row.ID).Error; err != nil {
		t.Fatal(err)
	}
	if saved.ChargedQuota != 100 || saved.TokenChargedQuota != 100 || saved.RefundedQuota != 140 || saved.UsageJSON != row.UsageJSON || saved.ChargeDimensionsJSON != row.ChargeDimensionsJSON || saved.FinalAttempt != 2 {
		t.Fatalf("original finalization mutated: %+v", saved)
	}
}

func attemptBillingTestPricer(parent ZTAPIRequestSettlement, input ZTAPIAttemptBillingSubmission) (ZTAPIAttemptBillingPriced, error) {
	dims := []ZTAPISupplierRefundDimension{}
	var total int64
	for _, u := range input.Usage {
		dims = append(dims, ZTAPISupplierRefundDimension{Dimension: u.Dimension, Units: strconv.FormatInt(u.Quantity, 10), UnitQuota: "1", ChargedQuota: u.Quantity})
		total += u.Quantity
	}
	return ZTAPIAttemptBillingPriced{Dimensions: dims, ConsumeLog: Log{Type: LogTypeConsume, UserId: parent.UserID, TokenId: parent.TokenID, RequestId: parent.RequestID, ModelName: parent.PublicModel, Quota: int(total), ChannelId: input.ChannelID, CreatedAt: 1, PromptTokens: int(total)}}, nil
}

func attemptBillingImageZeroPricer(parent ZTAPIRequestSettlement, input ZTAPIAttemptBillingSubmission) (ZTAPIAttemptBillingPriced, error) {
	dims := make([]ZTAPISupplierRefundDimension, 0, len(input.Usage))
	var total, prompt, completion int64
	for _, usage := range input.Usage {
		if usage.Quantity == 0 {
			continue
		}
		dims = append(dims, ZTAPISupplierRefundDimension{Dimension: usage.Dimension, Units: strconv.FormatInt(usage.Quantity, 10), UnitQuota: "1", ChargedQuota: usage.Quantity})
		total += usage.Quantity
		if usage.Dimension == "image_output" {
			completion += usage.Quantity
		} else {
			prompt += usage.Quantity
		}
	}
	return ZTAPIAttemptBillingPriced{Dimensions: dims, ConsumeLog: Log{Type: LogTypeConsume, UserId: parent.UserID, TokenId: parent.TokenID, RequestId: parent.RequestID, ModelName: parent.PublicModel, Quota: int(total), ChannelId: input.ChannelID, CreatedAt: 1, PromptTokens: int(prompt), CompletionTokens: int(completion)}}, nil
}

func attemptBillingTestLog(tx *gorm.DB, parent *ZTAPIRequestSettlement, charge *ZTAPISupplierRefundCharge, operationID string, log Log) error {
	shadow := *parent
	shadow.OperationID = operationID
	shadow.FinalAttempt = charge.Attempt
	shadow.ChargedQuota = charge.ChargedQuota
	return EnqueueZTAPISettlementLogTx(tx, &shadow, log)
}

type attemptBillingFixture struct {
	db       *gorm.DB
	row      ZTAPIRequestSettlement
	evidence ZTAPISettlementEvidence
	admin    User
}

func setupAttemptBillingFixture(t *testing.T, pending bool) attemptBillingFixture {
	t.Helper()
	db, row, evidence := setupZTAPISettlementEvidence(t)
	if err := MigrateZTAPIAttemptBilling(db); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&Channel{Id: 10}).Error; err != nil {
		t.Fatal(err)
	}
	if err := RecordZTAPIRequestAttemptResponse(row.OperationID, 1, 10, 502, "wire-first"); err != nil {
		t.Fatal(err)
	}
	var err error
	if pending {
		_, err = PendZTAPIRequestSettlement(row.OperationID, "{}", `["upstream_usage_missing"]`)
	} else {
		_, err = FinalizeZTAPIRequestSettlementWithEvidence(row.OperationID, 100, settlementReplayUsage, settlementReplayDimensions, evidence)
	}
	if err != nil {
		t.Fatal(err)
	}
	if err = db.First(&row, row.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err = db.Transaction(func(tx *gorm.DB) error { return EnsureZTAPIAttemptBillingReviewsTx(tx, &row) }); err != nil {
		t.Fatal(err)
	}
	admin := createBalanceLedgerTestUser(t, db, "attempt-review-finance", 0)
	if err = db.Model(&admin).Update("role", common.RoleFinanceUser).Error; err != nil {
		t.Fatal(err)
	}
	return attemptBillingFixture{db, row, evidence, admin}
}

func (f attemptBillingFixture) submission() ZTAPIAttemptBillingSubmission {
	return ZTAPIAttemptBillingSubmission{Source: "supplier", ProofID: "bill-proof", RequestID: f.row.RequestID, UserID: f.row.UserID, Attempt: 1, ChannelID: 10, CredentialVersion: "cred-v1", UpstreamRequestID: "wire-first", UpstreamBillID: "bill-line", Kind: "billed", UsageSemantic: "openai", Usage: []ZTAPIAttemptBillingQuantity{{Dimension: "input_tokens", Quantity: 40}}, EvidenceReference: "statement", DistinctUsageReference: "separate source executions"}
}

func (f attemptBillingFixture) approve(t *testing.T, input ZTAPIAttemptBillingSubmission) *ZTAPIAttemptBillingProof {
	t.Helper()
	p, err := SubmitZTAPIAttemptBilling(input)
	if err != nil {
		t.Fatal(err)
	}
	if err = ApproveZTAPIAttemptBilling(p.ID, f.admin.Id, "verified", attemptBillingTestPricer); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestZTAPIAttemptBillingNoChargeDoesNotCreditSettledCustomer(t *testing.T) {
	f := setupAttemptBillingFixture(t, false)
	input := f.submission()
	input.Kind = "nocharge"
	input.Usage = nil
	p := f.approve(t, input)
	if err := ProcessZTAPIAttemptBilling(p.ID, nil); err != nil {
		t.Fatal(err)
	}
	assertZTAPISettlementBalances(t, f.db, f.row, 900, 900)
	reviews, err := ListZTAPIAttemptBillingReviews("verified_nocharge", 0, 10)
	if err != nil || len(reviews) != 1 {
		t.Fatalf("nocharge review %v %v", reviews, err)
	}
	input.ProofID = "conflicting-bill"
	input.Kind = "billed"
	input.Usage = f.submission().Usage
	other, err := SubmitZTAPIAttemptBilling(input)
	if err != nil {
		t.Fatal(err)
	}
	if err = ApproveZTAPIAttemptBilling(other.ID, f.admin.Id, "verified", attemptBillingTestPricer); !errors.Is(err, ErrZTAPIAttemptBillingPending) {
		t.Fatalf("contradictory bill approved: %v", err)
	}
}

func setupPendingNoChargeAttempts(t *testing.T, count int) (*gorm.DB, ZTAPIRequestSettlement, User) {
	t.Helper()
	db, input := setupZTAPISettlement(t)
	if err := db.AutoMigrate(&ZTAPIRequestAttempt{}, &ZTAPIPendingResolution{}, &Channel{}); err != nil {
		t.Fatal(err)
	}
	if err := MigrateZTAPIAttemptBilling(db); err != nil {
		t.Fatal(err)
	}
	row, err := BeginZTAPIRequestSettlement(input)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= count; i++ {
		channelID := 20 + i
		if err = db.Create(&Channel{Id: channelID}).Error; err != nil {
			t.Fatal(err)
		}
		if _, err = BeginZTAPIRequestAttempt(row.OperationID, channelID, "cred-v1", "chat"); err != nil {
			t.Fatal(err)
		}
		if err = RecordZTAPIRequestAttemptResponse(row.OperationID, i, channelID, 502, "wire-"+strconv.Itoa(i)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = PendZTAPIRequestSettlement(row.OperationID, "{}", `["upstream_usage_missing"]`); err != nil {
		t.Fatal(err)
	}
	if err = db.First(row, row.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err = db.Transaction(func(tx *gorm.DB) error { return EnsureZTAPIAttemptBillingReviewsTx(tx, row) }); err != nil {
		t.Fatal(err)
	}
	admin := createBalanceLedgerTestUser(t, db, "pending-nocharge-finance-"+strconv.Itoa(count), 0)
	if err = db.Model(&admin).Update("role", common.RoleFinanceUser).Error; err != nil {
		t.Fatal(err)
	}
	return db, *row, admin
}

func approvePendingNoChargeAttempt(t *testing.T, row ZTAPIRequestSettlement, admin User, attempt int) *ZTAPIAttemptBillingProof {
	t.Helper()
	input := ZTAPIAttemptBillingSubmission{
		Source: "supplier", ProofID: "nocharge-proof-" + strconv.Itoa(attempt), RequestID: row.RequestID,
		UserID: row.UserID, Attempt: attempt, ChannelID: 20 + attempt, CredentialVersion: "cred-v1",
		UpstreamRequestID: "wire-" + strconv.Itoa(attempt), Kind: "nocharge", EvidenceReference: "statement-row-" + strconv.Itoa(attempt),
	}
	proof, err := SubmitZTAPIAttemptBilling(input)
	if err != nil {
		t.Fatal(err)
	}
	if err = ApproveZTAPIAttemptBilling(proof.ID, admin.Id, "verified-nocharge-"+strconv.Itoa(attempt), nil); err != nil {
		t.Fatal(err)
	}
	return proof
}

func TestZTAPIAttemptBillingPendingSingleNoChargeReleasesReservationExactlyOnce(t *testing.T) {
	db, row, admin := setupPendingNoChargeAttempts(t, 1)
	proof := approvePendingNoChargeAttempt(t, row, admin, 1)
	assertZTAPISettlementBalances(t, db, row, 1000, 1000)
	var saved ZTAPIRequestSettlement
	if err := db.First(&saved, row.ID).Error; err != nil || saved.Status != ZTAPISettlementReleased {
		t.Fatalf("pending nocharge not released: %+v %v", saved, err)
	}
	if err := ApproveZTAPIAttemptBilling(proof.ID, admin.Id, "verified-nocharge-1", nil); err != nil {
		t.Fatal(err)
	}
	assertZTAPISettlementBalances(t, db, row, 1000, 1000)
	if err := db.Model(&ZTAPIAttemptBillingProof{}).Where("id = ?", proof.ID).Updates(map[string]any{"status": "approved", "pending_reason": "nocharge_release_pending"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := ProcessZTAPIAttemptBilling(proof.ID, nil); err != nil {
		t.Fatal(err)
	}
	var recovered ZTAPIAttemptBillingProof
	if err := db.First(&recovered, proof.ID).Error; err != nil || recovered.Status != "completed" {
		t.Fatalf("released nocharge retry did not close proof: %+v %v", recovered, err)
	}
	assertZTAPISettlementBalances(t, db, row, 1000, 1000)
}

func TestZTAPIAttemptBillingPendingDualNoChargeWaitsForEveryAttempt(t *testing.T) {
	db, row, admin := setupPendingNoChargeAttempts(t, 2)
	approvePendingNoChargeAttempt(t, row, admin, 1)
	assertZTAPISettlementBalances(t, db, row, 800, 800)
	var pending ZTAPIRequestSettlement
	if err := db.First(&pending, row.ID).Error; err != nil || pending.Status != ZTAPISettlementPending {
		t.Fatalf("partially proven request was released: %+v %v", pending, err)
	}
	approvePendingNoChargeAttempt(t, row, admin, 2)
	assertZTAPISettlementBalances(t, db, row, 1000, 1000)
	var reviews []ZTAPIAttemptBillingReview
	if err := db.Where("request_id = ?", row.RequestID).Order("attempt").Find(&reviews).Error; err != nil {
		t.Fatal(err)
	}
	if len(reviews) != 2 || reviews[0].Status != "verified_nocharge" || reviews[1].Status != "verified_nocharge" {
		t.Fatalf("nocharge reviews not closed: %+v", reviews)
	}
}

func TestZTAPIAttemptBillingAcceptsCanonicalImageUsageSubmission(t *testing.T) {
	f := setupAttemptBillingFixture(t, true)
	input := f.submission()
	input.UsageSemantic = "ztapi_image"
	input.Usage = []ZTAPIAttemptBillingQuantity{{Dimension: "input_tokens", Quantity: 200000}, {Dimension: "output_tokens", Quantity: 1}}
	input.SelectedRuleID = "lte_200k"
	input.PriceRuleIDs = map[string]string{"input_tokens": "lte_200k", "output_tokens": "lte_200k"}
	input.RawUsageJSON = `{"input_tokens":200000,"output_tokens":1,"total_tokens":200001}`
	proof, err := SubmitZTAPIAttemptBilling(input)
	if err != nil {
		t.Fatal(err)
	}
	if proof.ID == 0 || proof.Status != "pending" {
		t.Fatalf("unexpected proof: %+v", proof)
	}
}

func TestZTAPIAttemptBillingAcceptsCanonicalGPTImageThreeDimensionSubmission(t *testing.T) {
	f := setupAttemptBillingFixture(t, true)
	input := f.submission()
	input.UsageSemantic = ZTAPIAttemptBillingUsageSemanticImage
	input.Usage = []ZTAPIAttemptBillingQuantity{{Dimension: "text_input", Quantity: 18}, {Dimension: "image_input", Quantity: 0}, {Dimension: "image_output", Quantity: 196}}
	input.PriceRuleIDs = map[string]string{"text_input": "text_input", "image_input": "image_input", "image_output": "image_output"}
	input.RawUsageJSON = `{"input_tokens":18,"input_tokens_details":{"image_tokens":0,"text_tokens":18},"output_tokens":196,"output_tokens_details":{"image_tokens":196,"text_tokens":0},"total_tokens":214}`
	proof, err := SubmitZTAPIAttemptBilling(input)
	if err != nil {
		t.Fatal(err)
	}
	if proof.ID == 0 || proof.Status != "pending" {
		t.Fatalf("unexpected proof: %+v", proof)
	}
}

func TestZTAPIAttemptBillingAppliesExplicitZeroImageBucketsWithoutRefundDimensions(t *testing.T) {
	f := setupAttemptBillingFixture(t, true)
	input := f.submission()
	input.Attempt = 2
	input.ChannelID = 11
	input.UpstreamRequestID = "wire-final"
	input.UsageSemantic = ZTAPIAttemptBillingUsageSemanticImage
	input.Usage = []ZTAPIAttemptBillingQuantity{{Dimension: "text_input", Quantity: 2}, {Dimension: "text_cached_input", Quantity: 0}, {Dimension: "image_input", Quantity: 3}, {Dimension: "image_cached_input", Quantity: 0}, {Dimension: "image_output", Quantity: 2}}
	input.PriceRuleIDs = map[string]string{"text_input": "text_input", "text_cached_input": "text_cached_input", "image_input": "image_input", "image_cached_input": "image_cached_input", "image_output": "image_output"}
	input.RawUsageJSON = `{"image_cached_input":0,"image_input":3,"image_output":2,"text_cached_input":0,"text_input":2,"total_tokens":7}`
	proof, err := SubmitZTAPIAttemptBilling(input)
	if err != nil {
		t.Fatal(err)
	}
	if err = ApproveZTAPIAttemptBilling(proof.ID, f.admin.Id, "verified image zero buckets", attemptBillingImageZeroPricer); err != nil {
		t.Fatal(err)
	}
}

func TestZTAPIAttemptBillingConcurrentDuplicateAndRollback(t *testing.T) {
	f := setupAttemptBillingFixture(t, false)
	input := f.submission()
	p := f.approve(t, input)
	again, err := SubmitZTAPIAttemptBilling(input)
	if err != nil || again.ID != p.ID {
		t.Fatalf("duplicate proof %v %v", again, err)
	}
	input.Usage[0].Quantity++
	if _, err = SubmitZTAPIAttemptBilling(input); !errors.Is(err, ErrZTAPIAttemptBillingConflict) {
		t.Fatalf("mutated proof accepted: %v", err)
	}
	failed := errors.New("log failure")
	if err = ProcessZTAPIAttemptBilling(p.ID, func(*gorm.DB, *ZTAPIRequestSettlement, *ZTAPISupplierRefundCharge, string, Log) error { return failed }); !errors.Is(err, failed) {
		t.Fatal(err)
	}
	assertZTAPISettlementBalances(t, f.db, f.row, 900, 900)
	var count int64
	if err = f.db.Model(&ZTAPISupplierRefundCharge{}).Where("request_id = ?", f.row.RequestID).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("charge escaped rollback: %d %v", count, err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- ProcessZTAPIAttemptBilling(p.ID, attemptBillingTestLog) }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	assertZTAPISettlementBalances(t, f.db, f.row, 860, 860)
	var user User
	f.db.First(&user, f.row.UserID)
	if user.UsedQuota != 140 || user.RequestCount != 1 {
		t.Fatal("concurrent duplicate consumption")
	}
}

func TestZTAPIAttemptBillingAuthorityAndMissingDimensions(t *testing.T) {
	f := setupAttemptBillingFixture(t, false)
	p, err := SubmitZTAPIAttemptBilling(f.submission())
	if err != nil {
		t.Fatal(err)
	}
	if err = ApproveZTAPIAttemptBilling(p.ID, f.row.UserID, "self", attemptBillingTestPricer); !errors.Is(err, ErrZTAPISupplierRefundUnauthorized) {
		t.Fatal(err)
	}
	if err = ApproveZTAPIAttemptBilling(p.ID, f.admin.Id, "verified", func(ZTAPIRequestSettlement, ZTAPIAttemptBillingSubmission) (ZTAPIAttemptBillingPriced, error) {
		return ZTAPIAttemptBillingPriced{}, ErrZTAPISettlementPending
	}); !errors.Is(err, ErrZTAPIAttemptBillingPending) {
		t.Fatal(err)
	}
	assertZTAPISettlementBalances(t, f.db, f.row, 900, 900)
}

func TestZTAPIAttemptBillingMigrationReplacesOldUniqueIndexes(t *testing.T) {
	f := setupAttemptBillingFixture(t, false)
	for _, column := range []string{"request_id", "settlement_id"} {
		name := f.db.NamingStrategy.IndexName((ZTAPISupplierRefundCharge{}).TableName(), column)
		if err := f.db.Exec("CREATE UNIQUE INDEX " + name + " ON ztapi_supplier_refund_charges (" + column + ")").Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := MigrateZTAPIAttemptBilling(f.db); err != nil {
		t.Fatal(err)
	}
	if err := MigrateZTAPIAttemptBilling(f.db); err != nil {
		t.Fatal(err)
	}
	p := f.approve(t, f.submission())
	if err := ProcessZTAPIAttemptBilling(p.ID, attemptBillingTestLog); err != nil {
		t.Fatal(err)
	}
	var charge ZTAPISupplierRefundCharge
	if err := f.db.Where("request_id = ? AND attempt = 1", f.row.RequestID).Take(&charge).Error; err != nil {
		t.Fatal(err)
	}
	charge.ID = 0
	if err := f.db.Create(&charge).Error; err == nil {
		t.Fatal("composite uniqueness absent")
	}
}

func TestZTAPIAttemptBillingTerminalFinalReviewIsResolved(t *testing.T) {
	f := setupAttemptBillingFixture(t, true)
	// Trusted final usage saved before the earlier generic pending transition.
	if err := f.db.Model(&ZTAPIRequestSettlement{}).Where("id = ?", f.row.ID).Updates(map[string]any{"status": ZTAPISettlementReserved}).Error; err != nil {
		t.Fatal(err)
	}
	saveZTAPIIntentTest(t, f.row, f.evidence)
	if _, err := PendZTAPIRequestSettlement(f.row.OperationID, settlementReplayUsage, `["settlement_retry_required"]`); err != nil {
		t.Fatal(err)
	}
	if err := RetryZTAPISettlementFinalizations(1); err != nil {
		t.Fatal(err)
	}
	var final ZTAPIAttemptBillingReview
	if err := f.db.Where("request_id = ? AND attempt = 2", f.row.RequestID).Take(&final).Error; err != nil || final.Status != "verified_billed" {
		t.Fatalf("ghost final review: %+v %v", final, err)
	}
}

func TestZTAPIAttemptBillingWholeNoChargeResolvesAllReviews(t *testing.T) {
	f := setupAttemptBillingFixture(t, true)
	if err := f.db.AutoMigrate(&ZTAPIPendingResolution{}); err != nil {
		t.Fatal(err)
	}
	proof := ZTAPINoChargeProof{Source: "supplier", ProofID: "whole-nocharge", VerificationReference: "all supplier lines", Attempts: []ZTAPINoChargeAttempt{{Attempt: 1, ChannelID: 10, CredentialVersion: "cred-v1", UpstreamRequestID: "wire-first", VerificationReference: "first"}, {Attempt: 2, ChannelID: 11, CredentialVersion: "cred-v1", UpstreamRequestID: "wire-final", VerificationReference: "last"}}}
	if _, err := ResolveZTAPIPendingNoCharge(f.row.ID, f.admin.Id, proof); err != nil {
		t.Fatal(err)
	}
	rows, err := ListZTAPIAttemptBillingReviews("pending", 0, 10)
	if err != nil || len(rows) != 0 {
		t.Fatalf("ghost nocharge reviews: %+v %v", rows, err)
	}
}

func TestZTAPIAttemptBillingVerifiedPendingFinalRecoversOriginalHold(t *testing.T) {
	f := setupAttemptBillingFixture(t, true)
	input := f.submission()
	input.Attempt = 2
	input.ChannelID = 11
	input.UpstreamRequestID = "wire-final"
	input.Usage[0].Quantity = 60
	p := f.approve(t, input)
	var staged ZTAPIRequestSettlement
	if err := f.db.First(&staged, f.row.ID).Error; err != nil {
		t.Fatal(err)
	}
	if staged.Status != ZTAPISettlementPending || staged.MissingDimensionsJSON != `["settlement_retry_required"]` {
		t.Fatalf("trusted final not durably handed off: %+v", staged)
	}
	var intents int64
	if err := f.db.Model(&ZTAPISettlementFinalizationIntent{}).Where("request_id = ?", f.row.RequestID).Count(&intents).Error; err != nil || intents != 1 {
		t.Fatalf("intent=%d %v", intents, err)
	}
	if err := ProcessZTAPIAttemptBilling(p.ID, nil); err != nil {
		t.Fatal(err)
	}
	if err := RetryZTAPISettlementFinalizations(10); err != nil {
		t.Fatal(err)
	}
	if err := ProcessZTAPIAttemptBilling(p.ID, nil); err != nil {
		t.Fatal(err)
	}
	assertZTAPISettlementBalances(t, f.db, f.row, 940, 940)
	var user User
	f.db.First(&user, f.row.UserID)
	if user.UsedQuota != 60 || user.RequestCount != 1 {
		t.Fatalf("wrong original count %+v", user)
	}
	refund, err := SubmitZTAPISupplierRefund(ZTAPISupplierRefundSubmission{Source: "supplier", ProofID: "refund-final", RequestID: f.row.RequestID, UserID: f.row.UserID, Attempt: 2, ChannelID: 11, CredentialVersion: "cred-v1", UpstreamRequestID: "wire-final", UpstreamBillID: "bill-line", Mode: "full", EvidenceReference: "verified refund"})
	if err != nil {
		t.Fatal(err)
	}
	if err = ApproveZTAPISupplierRefund(refund.ID, f.admin.Id, "verified"); err != nil {
		t.Fatal(err)
	}
	if err = ProcessZTAPISupplierRefund(refund.ID); err != nil {
		t.Fatal(err)
	}
	assertZTAPISettlementBalances(t, f.db, f.row, 1000, 1000)
}

func TestZTAPIAttemptBillingPendingFinalRollbackAndNoChargeGuard(t *testing.T) {
	f := setupAttemptBillingFixture(t, true)
	input := f.submission()
	input.Attempt = 2
	input.ChannelID = 11
	input.UpstreamRequestID = "wire-final"
	p := f.approve(t, input)
	if err := f.db.AutoMigrate(&ZTAPIPendingResolution{}); err != nil {
		t.Fatal(err)
	}
	proof := ZTAPINoChargeProof{Source: "supplier", ProofID: "contradiction", VerificationReference: "claimed", Attempts: []ZTAPINoChargeAttempt{{Attempt: 1, ChannelID: 10, CredentialVersion: "cred-v1", UpstreamRequestID: "wire-first", VerificationReference: "first"}, {Attempt: 2, ChannelID: 11, CredentialVersion: "cred-v1", UpstreamRequestID: "wire-final", VerificationReference: "last"}}}
	if _, err := ResolveZTAPIPendingNoCharge(f.row.ID, f.admin.Id, proof); !errors.Is(err, ErrZTAPISettlementConflict) {
		t.Fatalf("approved final bill waived: %v", err)
	}
	if err := f.db.Exec(`CREATE TRIGGER reject_approved_final_log BEFORE INSERT ON ztapi_settlement_log_outboxes BEGIN SELECT RAISE(ABORT, 'injected log failure'); END`).Error; err != nil {
		t.Fatal(err)
	}
	if err := ProcessZTAPIAttemptBilling(p.ID, nil); err == nil {
		t.Fatal("late transaction failure ignored")
	}
	assertZTAPISettlementBalances(t, f.db, f.row, 800, 800)
	if err := f.db.Exec(`DROP TRIGGER reject_approved_final_log`).Error; err != nil {
		t.Fatal(err)
	}
	if err := RetryZTAPISettlementFinalizations(10); err != nil {
		t.Fatal(err)
	}
	if err := RetryZTAPIAttemptBillings(10, nil); err != nil {
		t.Fatal(err)
	}
	assertZTAPISettlementBalances(t, f.db, f.row, 960, 960)
}

func TestZTAPIAttemptBillingRejectsKnownDuplicateUsage(t *testing.T) {
	for _, kind := range []string{"bill_line", "same_wire", "missing_line", "missing_distinct_reference"} {
		t.Run(kind, func(t *testing.T) {
			f := setupAttemptBillingFixture(t, false)
			input := f.submission()
			switch kind {
			case "bill_line":
				if err := f.db.Model(&ZTAPISupplierRefundCharge{}).Where("request_id = ? AND attempt = 2", f.row.RequestID).UpdateColumn("upstream_bill_id", input.UpstreamBillID).Error; err != nil {
					t.Fatal(err)
				}
			case "same_wire":
				input.UpstreamRequestID = "wire-final"
				if err := f.db.Model(&ZTAPIRequestAttempt{}).Where("settlement_id = ? AND attempt = 1", f.row.ID).UpdateColumn("upstream_request_id", input.UpstreamRequestID).Error; err != nil {
					t.Fatal(err)
				}
			case "missing_line":
				input.UpstreamBillID = ""
			case "missing_distinct_reference":
				input.DistinctUsageReference = ""
			}
			p, err := SubmitZTAPIAttemptBilling(input)
			if err != nil {
				t.Fatal(err)
			}
			if err = ApproveZTAPIAttemptBilling(p.ID, f.admin.Id, "verified", attemptBillingTestPricer); !errors.Is(err, ErrZTAPIAttemptBillingPending) {
				t.Fatalf("duplicate/unknown usage approved: %v", err)
			}
			assertZTAPISettlementBalances(t, f.db, f.row, 900, 900)
		})
	}
}

func TestZTAPIAttemptBillingRejectsUpstreamTaskReusedAcrossRequests(t *testing.T) {
	f := setupAttemptBillingFixture(t, false)
	if err := f.db.AutoMigrate(&ZTAPIMediaTask{}); err != nil {
		t.Fatal(err)
	}
	input := f.submission()
	input.UpstreamTaskID = "shared-upstream-task"
	if err := authorizeZTAPIMediaTaskMutation(f.db).Create(&ZTAPIMediaTask{SettlementID: f.row.ID, RequestID: f.row.RequestID, UserID: f.row.UserID, TokenID: f.row.TokenID, PublicTaskID: "public-task-current", PublicModel: f.row.PublicModel, Action: "generate", Attempt: input.Attempt, ChannelID: input.ChannelID, CredentialVersion: input.CredentialVersion, UpstreamTaskID: input.UpstreamTaskID}).Error; err != nil {
		t.Fatal(err)
	}
	other := f.row
	other.ID = 0
	other.OperationID = "other-operation"
	other.RequestID = "other-request"
	if err := f.db.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	if err := authorizeZTAPIMediaTaskMutation(f.db).Create(&ZTAPIMediaTask{SettlementID: other.ID, RequestID: other.RequestID, UserID: other.UserID, TokenID: other.TokenID, PublicTaskID: "public-task-other", PublicModel: other.PublicModel, Action: "generate", Attempt: 1, ChannelID: input.ChannelID, CredentialVersion: input.CredentialVersion, UpstreamTaskID: input.UpstreamTaskID}).Error; err != nil {
		t.Fatal(err)
	}
	proof, err := SubmitZTAPIAttemptBilling(input)
	if err != nil {
		t.Fatal(err)
	}
	if err = ApproveZTAPIAttemptBilling(proof.ID, f.admin.Id, "verified", attemptBillingTestPricer); !errors.Is(err, ErrZTAPIAttemptBillingPending) {
		t.Fatalf("reused upstream task accepted: %v", err)
	}
}

func TestZTAPIAttemptBillingLogCallbackMustPersistAtomicOutbox(t *testing.T) {
	f := setupAttemptBillingFixture(t, false)
	p := f.approve(t, f.submission())
	if err := ProcessZTAPIAttemptBilling(p.ID, func(*gorm.DB, *ZTAPIRequestSettlement, *ZTAPISupplierRefundCharge, string, Log) error { return nil }); err == nil {
		t.Fatal("missing atomic log accepted")
	}
	assertZTAPISettlementBalances(t, f.db, f.row, 900, 900)
}

func TestZTAPIAttemptBillingLongWireNoChargeRemainsReviewable(t *testing.T) {
	f := setupAttemptBillingFixture(t, false)
	input := f.submission()
	input.UpstreamRequestID = strings.Repeat("w", 180)
	input.Kind = "nocharge"
	input.Usage = nil
	if err := f.db.Model(&ZTAPIRequestAttempt{}).Where("settlement_id = ? AND attempt = 1", f.row.ID).UpdateColumn("upstream_request_id", input.UpstreamRequestID).Error; err != nil {
		t.Fatal(err)
	}
	p := f.approve(t, input)
	if err := ProcessZTAPIAttemptBilling(p.ID, nil); err != nil {
		t.Fatal(err)
	}
	assertZTAPISettlementBalances(t, f.db, f.row, 900, 900)
}

func TestZTAPIAttemptBillingLongWireBilledAndRefund(t *testing.T) {
	for _, final := range []bool{false, true} {
		t.Run(strconv.FormatBool(final), func(t *testing.T) {
			f := setupAttemptBillingFixture(t, final)
			input := f.submission()
			if final {
				input.Attempt = 2
				input.ChannelID = 11
			}
			input.UpstreamRequestID = strings.Repeat("w", 200)
			if err := f.db.Model(&ZTAPIRequestAttempt{}).Where("settlement_id = ? AND attempt = ?", f.row.ID, input.Attempt).UpdateColumn("upstream_request_id", input.UpstreamRequestID).Error; err != nil {
				t.Fatal(err)
			}
			p := f.approve(t, input)
			if err := ProcessZTAPIAttemptBilling(p.ID, attemptBillingTestLog); err != nil {
				t.Fatal(err)
			}
			var charge ZTAPISupplierRefundCharge
			if err := f.db.Where("request_id = ? AND attempt = ?", input.RequestID, input.Attempt).Take(&charge).Error; err != nil {
				t.Fatal(err)
			}
			op := charge.BillingOperationID
			if final {
				op = f.row.OperationID
				var intent ZTAPISettlementFinalizationIntent
				if err := f.db.Where("operation_id = ?", op).Take(&intent).Error; err != nil {
					t.Fatal(err)
				}
				var evidence ZTAPISettlementEvidence
				if err := common.UnmarshalJsonStr(intent.EvidenceJSON, &evidence); err != nil || evidence.ConsumeLog.UpstreamRequestId != input.UpstreamRequestID {
					t.Fatalf("intent lost wire ID: %+v %v", evidence, err)
				}
			}
			var job ZTAPISettlementLogOutbox
			if err := f.db.Where("operation_id = ?", op).Take(&job).Error; err != nil {
				t.Fatal(err)
			}
			if err := f.db.AutoMigrate(&Log{}, &ZTAPISettlementLogReceipt{}); err != nil {
				t.Fatal(err)
			}
			id, err := deliverZTAPISettlementLog(f.db, job)
			if err != nil {
				t.Fatal(err)
			}
			var log Log
			if err := f.db.First(&log, id).Error; err != nil || log.UpstreamRequestId != input.UpstreamRequestID || charge.UpstreamRequestID != input.UpstreamRequestID {
				t.Fatalf("truncated persisted wire: %+v %v", log, err)
			}
			r, err := SubmitZTAPISupplierRefund(ZTAPISupplierRefundSubmission{Source: input.Source, ProofID: "long-wire-refund", RequestID: input.RequestID, UserID: input.UserID, Attempt: input.Attempt, ChannelID: input.ChannelID, CredentialVersion: input.CredentialVersion, UpstreamRequestID: input.UpstreamRequestID, UpstreamBillID: input.UpstreamBillID, Mode: "full", EvidenceReference: "verified refund"})
			if err != nil {
				t.Fatal(err)
			}
			if err = ApproveZTAPISupplierRefund(r.ID, f.admin.Id, "verified"); err != nil {
				t.Fatal(err)
			}
			if err = ProcessZTAPISupplierRefund(r.ID); err != nil {
				t.Fatal(err)
			}
			want := 900
			if final {
				want = 1000
			}
			assertZTAPISettlementBalances(t, f.db, f.row, want, want)
		})
	}
}

func TestZTAPIAttemptBillingPendingPreimageIsImmutable(t *testing.T) {
	f := setupAttemptBillingFixture(t, true)
	const originalUsage = `{"native_usage":{"input_tokens":17},"cache_missing":true}`
	const originalMissing = `["cache_read"]`
	if err := f.db.Model(&ZTAPIRequestSettlement{}).Where("id = ?", f.row.ID).Updates(map[string]any{"usage_json": originalUsage, "missing_dimensions_json": originalMissing}).Error; err != nil {
		t.Fatal(err)
	}
	input := f.submission()
	input.Attempt = 2
	input.ChannelID = 11
	input.UpstreamRequestID = "wire-final"
	p := f.approve(t, input)
	var saved map[string]any
	if err := f.db.Model(&ZTAPIAttemptBillingApproval{}).Where("proof_id = ?", p.ID).Take(&saved).Error; err != nil {
		t.Fatal(err)
	}
	if saved["pending_usage_json"] != originalUsage || saved["pending_missing_dimensions_json"] != originalMissing {
		t.Fatalf("original pending evidence lost: %+v", saved)
	}
	if err := ApproveZTAPIAttemptBilling(p.ID, f.admin.Id, "verified", nil); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Exec("UPDATE ztapi_attempt_billing_approvals SET pending_usage_json = ? WHERE proof_id = ?", `{}`, p.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := ApproveZTAPIAttemptBilling(p.ID, f.admin.Id, "verified", nil); !errors.Is(err, ErrZTAPIAttemptBillingConflict) {
		t.Fatalf("approval replay accepted changed preimage: %v", err)
	}
	if err := ProcessZTAPIAttemptBilling(p.ID, nil); !errors.Is(err, ErrZTAPIAttemptBillingPending) {
		t.Fatalf("tampered pending preimage trusted: %v", err)
	}
	if err := RetryZTAPISettlementFinalizations(10); err == nil {
		t.Fatal("generic intent retry bypassed tampered approval")
	}
	assertZTAPISettlementBalances(t, f.db, f.row, 800, 800)
}

func TestZTAPIAttemptBillingRechecksOverlapBeforeDebit(t *testing.T) {
	f := setupAttemptBillingFixture(t, false)
	p := f.approve(t, f.submission())
	if err := f.db.Model(&ZTAPISupplierRefundCharge{}).Where("request_id = ? AND attempt = 2", f.row.RequestID).UpdateColumn("upstream_bill_id", f.submission().UpstreamBillID).Error; err != nil {
		t.Fatal(err)
	}
	if err := ProcessZTAPIAttemptBilling(p.ID, attemptBillingTestLog); !errors.Is(err, ErrZTAPIAttemptBillingPending) {
		t.Fatalf("newly known overlap debited: %v", err)
	}
	assertZTAPISettlementBalances(t, f.db, f.row, 900, 900)
}

func TestZTAPIAttemptBillingGenericFinalRetryRejectsLateWireOverlap(t *testing.T) {
	for _, attemptWorkerFirst := range []bool{false, true} {
		t.Run(strconv.FormatBool(attemptWorkerFirst), func(t *testing.T) {
			f := setupAttemptBillingFixture(t, true)
			// The original response had no wire ID; a later observation may fill it.
			if err := f.db.Model(&ZTAPIRequestAttempt{}).Where("settlement_id = ? AND attempt = 1", f.row.ID).UpdateColumn("upstream_request_id", "").Error; err != nil {
				t.Fatal(err)
			}
			input := f.submission()
			input.Attempt, input.ChannelID, input.UpstreamRequestID = 2, 11, "wire-final"
			p := f.approve(t, input)
			if err := RecordZTAPIRequestAttemptResponse(f.row.OperationID, 1, 10, 502, "wire-final"); err != nil {
				t.Fatal(err)
			}
			if attemptWorkerFirst {
				if err := ProcessZTAPIAttemptBilling(p.ID, nil); !errors.Is(err, ErrZTAPIAttemptBillingPending) {
					t.Fatalf("attempt worker accepted overlap: %v", err)
				}
			}
			if err := RetryZTAPISettlementFinalizations(10); err == nil {
				t.Error("generic finalization retry accepted late duplicate wire identity")
			}
			assertZTAPISettlementBalances(t, f.db, f.row, 800, 800)
			var row ZTAPIRequestSettlement
			if err := f.db.First(&row, f.row.ID).Error; err != nil {
				t.Fatal(err)
			}
			if row.Status != ZTAPISettlementPending || row.ChargedQuota != 0 {
				t.Fatalf("ambiguous final charge committed: %+v", row)
			}
			var saved ZTAPIAttemptBillingProof
			if err := f.db.First(&saved, p.ID).Error; err != nil {
				t.Fatal(err)
			}
			if saved.Status != "approved" || saved.ChargeID != 0 {
				t.Fatalf("ambiguous proof marked applied: %+v", saved)
			}
			var intent ZTAPISettlementFinalizationIntent
			if err := f.db.Where("request_id = ?", row.RequestID).Take(&intent).Error; err != nil || intent.CompletedAt != 0 {
				t.Fatalf("ambiguous intent completed: %+v %v", intent, err)
			}
			var count int64
			if err := f.db.Model(&ZTAPISettlementLogOutbox{}).Where("operation_id = ?", row.OperationID).Count(&count).Error; err != nil || count != 0 {
				t.Fatalf("log escaped rollback: %d %v", count, err)
			}
		})
	}
}
