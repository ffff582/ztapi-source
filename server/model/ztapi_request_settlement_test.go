package model

import (
	"errors"
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

func setupZTAPISettlement(t *testing.T) (*gorm.DB, ZTAPIRequestSettlement) {
	t.Helper()
	db := setupBalanceLedgerTestDB(t)
	if err := db.AutoMigrate(&Token{}, &ZTAPIRequestSettlement{}, &ZTAPISettlementFinalizationIntent{}, &ZTAPIAttemptBillingReview{}, &ZTAPIAttemptBillingProof{}, &ZTAPIAttemptBillingApproval{}); err != nil {
		t.Fatal(err)
	}
	user := createBalanceLedgerTestUser(t, db, "durable-billing", 1000)
	token := Token{UserId: user.Id, KeyHash: "durable-token", Status: common.TokenStatusEnabled, RemainQuota: 1000, ExpiredTime: -1}
	if err := db.Create(&token).Error; err != nil {
		t.Fatal(err)
	}
	return db, ZTAPIRequestSettlement{OperationID: "operation-1", RequestID: "request-1", UserID: user.Id, TokenID: token.Id, PublicModel: "zt-test", PriceSnapshotJSON: `{"version":1,"input":1}`, ReservedQuota: 200}
}

func assertZTAPISettlementBalances(t *testing.T, db *gorm.DB, row ZTAPIRequestSettlement, wallet, token int) {
	t.Helper()
	var u User
	var k Token
	if err := db.First(&u, row.UserID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&k, row.TokenID).Error; err != nil {
		t.Fatal(err)
	}
	if u.Quota != wallet || k.RemainQuota != token {
		t.Fatalf("wallet/token=%d/%d want %d/%d", u.Quota, k.RemainQuota, wallet, token)
	}
}

func TestZTAPISettlementReserveIsAtomicAndDurable(t *testing.T) {
	db, input := setupZTAPISettlement(t)
	row, err := BeginZTAPIRequestSettlement(input)
	if err != nil {
		t.Fatal(err)
	}
	assertZTAPISettlementBalances(t, db, *row, 800, 800)
	again, err := BeginZTAPIRequestSettlement(input)
	if err != nil || again.ID != row.ID {
		t.Fatalf("retry=%+v %v", again, err)
	}
	assertZTAPISettlementBalances(t, db, *row, 800, 800)
	input.PriceSnapshotJSON = `{"version":2}`
	if _, err = BeginZTAPIRequestSettlement(input); !errors.Is(err, ErrZTAPISettlementConflict) {
		t.Fatalf("changed snapshot accepted: %v", err)
	}
	if err = db.Exec(`CREATE TRIGGER reject_hold_update BEFORE UPDATE ON ztapi_request_settlements BEGIN SELECT RAISE(ABORT, 'test failure'); END`).Error; err != nil {
		t.Fatal(err)
	}
	if _, err = ReserveZTAPIRequestSettlement(row.OperationID, 300); err == nil {
		t.Fatal("expected rollback")
	}
	assertZTAPISettlementBalances(t, db, *row, 800, 800)
}

func TestZTAPISettlementPendingSurvivesReconstructionAndCannotRefund(t *testing.T) {
	db, input := setupZTAPISettlement(t)
	row, err := BeginZTAPIRequestSettlement(input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = PendZTAPIRequestSettlement(row.OperationID, `{"cache_write":10}`, `["cache_write"]`); err != nil {
		t.Fatal(err)
	}
	// Re-enter through persisted identity, not an in-memory BillingSession flag.
	if _, err = ReleaseZTAPIRequestSettlement(row.OperationID); !errors.Is(err, ErrZTAPISettlementPending) {
		t.Fatalf("pending released: %v", err)
	}
	if _, err = FinalizeZTAPIRequestSettlement(row.OperationID, 0, "{}", "{}"); !errors.Is(err, ErrZTAPISettlementPending) {
		t.Fatalf("pending settled as zero: %v", err)
	}
	assertZTAPISettlementBalances(t, db, *row, 800, 800)
	var persisted ZTAPIRequestSettlement
	if err = db.First(&persisted, row.ID).Error; err != nil {
		t.Fatal(err)
	}
	if persisted.Status != ZTAPISettlementPending || persisted.MissingDimensionsJSON != `["cache_write"]` {
		t.Fatalf("lost pending: %+v", persisted)
	}
}

func TestZTAPISettlementConcurrentFinalizeAndDebt(t *testing.T) {
	db, input := setupZTAPISettlement(t)
	row, err := BeginZTAPIRequestSettlement(input)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := FinalizeZTAPIRequestSettlement(row.OperationID, 1400, `{"input":1400}`, `[{"dimension":"input_tokens","units":"1400","unit_quota":"1","charged_quota":1400}]`)
			errs <- e
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	assertZTAPISettlementBalances(t, db, *row, -400, 0)
	var u User
	db.First(&u, row.UserID)
	if !u.DebtSuspended || u.Status != common.UserStatusDisabled || u.UsedQuota != 1400 || u.RequestCount != 1 {
		t.Fatalf("debt/counters: %+v", u)
	}
	var got ZTAPIRequestSettlement
	db.First(&got, row.ID)
	if got.ChargedQuota != 1400 || got.TokenChargedQuota != 1000 {
		t.Fatalf("actual debt lost: %+v", got)
	}
	if _, err = FinalizeZTAPIRequestSettlement(row.OperationID, 1300, "{}", "{}"); !errors.Is(err, ErrZTAPISettlementConflict) {
		t.Fatalf("conflicting settlement: %v", err)
	}
}

func TestZTAPISettlementRejectsWrongCustomerTokenBeforeDebit(t *testing.T) {
	db, input := setupZTAPISettlement(t)
	input.UserID = createBalanceLedgerTestUser(t, db, "other-owner", 1000).Id
	if _, err := BeginZTAPIRequestSettlement(input); err == nil {
		t.Fatal("cross customer token accepted")
	}
	assertZTAPISettlementBalances(t, db, input, 1000, 1000)
}

func TestZTAPISettlementAttemptsPersistBeforeDispatchAndRejectThird(t *testing.T) {
	db, input := setupZTAPISettlement(t)
	if err := db.AutoMigrate(&ZTAPIRequestAttempt{}); err != nil {
		t.Fatal(err)
	}
	row, err := BeginZTAPIRequestSettlement(input)
	if err != nil {
		t.Fatal(err)
	}
	first, err := BeginZTAPIRequestAttempt(row.OperationID, 1, "cred-v1", "chat")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = BeginZTAPIRequestAttempt(row.OperationID, 1, "cred-v1", "chat"); !errors.Is(err, ErrZTAPISettlementConflict) {
		t.Fatalf("same channel retry: %v", err)
	}
	second, err := BeginZTAPIRequestAttempt(row.OperationID, 2, "cred-v2", "responses")
	if err != nil {
		t.Fatal(err)
	}
	if first.Attempt != 1 || second.Attempt != 2 {
		t.Fatal("lost attempt sequence")
	}
	if _, err = BeginZTAPIRequestAttempt(row.OperationID, 3, "cred-v3", "chat"); !errors.Is(err, ErrZTAPISettlementConflict) {
		t.Fatalf("third attempt: %v", err)
	}
	if _, err = ReleaseZTAPIRequestSettlement(row.OperationID); !errors.Is(err, ErrZTAPISettlementPending) {
		t.Fatalf("dispatched reservation refunded: %v", err)
	}
}

func TestZTAPIPendingSettlementWithLineageBindsLatestSuccessfulAttempt(t *testing.T) {
	db, input := setupZTAPISettlement(t)
	if err := db.AutoMigrate(&ZTAPIRequestAttempt{}); err != nil {
		t.Fatal(err)
	}
	if err := MigrateZTAPISupplierRefund(db); err != nil {
		t.Fatal(err)
	}
	row, err := BeginZTAPIRequestSettlement(input)
	if err != nil {
		t.Fatal(err)
	}
	first, err := BeginZTAPIRequestAttempt(row.OperationID, 10, "cred-v1", "/v1/images/generations")
	if err != nil {
		t.Fatal(err)
	}
	if err = RecordZTAPIRequestAttemptResponse(row.OperationID, first.Attempt, first.ChannelID, 502, "wire-first"); err != nil {
		t.Fatal(err)
	}
	second, err := BeginZTAPIRequestAttempt(row.OperationID, 11, "cred-v2", "/v1/images/generations")
	if err != nil {
		t.Fatal(err)
	}
	if err = RecordZTAPIRequestAttemptResponse(row.OperationID, second.Attempt, second.ChannelID, 200, ""); err != nil {
		t.Fatal(err)
	}

	usage := `{"upstream_request_id":"wire-second","pending":true}`
	missing := `["image_usage_untrusted"]`
	lineage := ZTAPIPendingSettlementLineage{FinalAttempt: second.Attempt, ChannelID: second.ChannelID, UpstreamRequestID: "wire-second"}
	pending, err := PendZTAPIRequestSettlementWithLineage(row.OperationID, usage, missing, lineage)
	if err != nil {
		t.Fatal(err)
	}
	if pending.Status != ZTAPISettlementPending || pending.FinalAttempt != second.Attempt || pending.ChargedQuota != 0 || pending.RefundedQuota != 0 {
		t.Fatalf("pending lineage or finance state mismatch: %+v", pending)
	}
	var storedAttempt ZTAPIRequestAttempt
	if err = db.Where("settlement_id = ? AND attempt = ?", row.ID, second.Attempt).Take(&storedAttempt).Error; err != nil {
		t.Fatal(err)
	}
	if storedAttempt.UpstreamRequestID != "wire-second" || storedAttempt.HTTPStatus != 200 {
		t.Fatalf("successful attempt lineage not bound: %+v", storedAttempt)
	}

	replayed, err := PendZTAPIRequestSettlementWithLineage(row.OperationID, usage, missing, lineage)
	if err != nil || replayed.ID != pending.ID || replayed.FinalAttempt != second.Attempt {
		t.Fatalf("exact replay mismatch: row=%+v err=%v", replayed, err)
	}
	conflictingAttempt := lineage
	conflictingAttempt.FinalAttempt = first.Attempt
	conflictingAttempt.ChannelID = first.ChannelID
	if _, err = PendZTAPIRequestSettlementWithLineage(row.OperationID, usage, missing, conflictingAttempt); !errors.Is(err, ErrZTAPISettlementConflict) {
		t.Fatalf("conflicting attempt accepted: %v", err)
	}
	conflictingUpstream := lineage
	conflictingUpstream.UpstreamRequestID = "wire-other"
	if _, err = PendZTAPIRequestSettlementWithLineage(row.OperationID, usage, missing, conflictingUpstream); !errors.Is(err, ErrZTAPISettlementConflict) {
		t.Fatalf("conflicting upstream request accepted: %v", err)
	}
	var ledgers, chargeAnchors int64
	if err = db.Model(&BalanceLedger{}).Where("request_id = ?", row.RequestID).Count(&ledgers).Error; err != nil {
		t.Fatal(err)
	}
	if err = db.Model(&ZTAPISupplierRefundCharge{}).Where("request_id = ?", row.RequestID).Count(&chargeAnchors).Error; err != nil {
		t.Fatal(err)
	}
	if ledgers != 1 || chargeAnchors != 0 {
		t.Fatalf("pending lineage changed funds or created a charge anchor: ledgers=%d anchors=%d", ledgers, chargeAnchors)
	}
}

func TestZTAPIPendingSettlementWithLineageSealsOneDelayedUpstreamRequestID(t *testing.T) {
	db, input := setupZTAPISettlement(t)
	if err := db.AutoMigrate(&ZTAPIRequestAttempt{}); err != nil {
		t.Fatal(err)
	}
	if err := MigrateZTAPISupplierRefund(db); err != nil {
		t.Fatal(err)
	}
	if err := MigrateZTAPISettlementLogOutbox(db); err != nil {
		t.Fatal(err)
	}
	row, err := BeginZTAPIRequestSettlement(input)
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := BeginZTAPIRequestAttempt(row.OperationID, 10, "cred-v1", "/v1/images/generations")
	if err != nil {
		t.Fatal(err)
	}
	if err = RecordZTAPIRequestAttemptResponse(row.OperationID, attempt.Attempt, attempt.ChannelID, 200, ""); err != nil {
		t.Fatal(err)
	}
	usage := `{"pending":true}`
	missing := `["image_settlement_evidence_missing"]`
	lineage := ZTAPIPendingSettlementLineage{FinalAttempt: attempt.Attempt, ChannelID: attempt.ChannelID}
	if _, err = PendZTAPIRequestSettlementWithLineage(row.OperationID, usage, missing, lineage); err != nil {
		t.Fatal(err)
	}

	lineage.UpstreamRequestID = "wire-delayed"
	if _, err = PendZTAPIRequestSettlementWithLineage(row.OperationID, usage, missing, lineage); err != nil {
		t.Fatal(err)
	}
	var storedAttempt ZTAPIRequestAttempt
	if err = db.Where("settlement_id = ? AND attempt = ?", row.ID, attempt.Attempt).Take(&storedAttempt).Error; err != nil {
		t.Fatal(err)
	}
	if storedAttempt.UpstreamRequestID != lineage.UpstreamRequestID {
		t.Fatalf("delayed upstream request ID not sealed: %+v", storedAttempt)
	}
	if _, err = PendZTAPIRequestSettlementWithLineage(row.OperationID, usage, missing, lineage); err != nil {
		t.Fatalf("exact enriched replay failed: %v", err)
	}
	lineage.UpstreamRequestID = "wire-conflict"
	if _, err = PendZTAPIRequestSettlementWithLineage(row.OperationID, usage, missing, lineage); !errors.Is(err, ErrZTAPISettlementConflict) {
		t.Fatalf("second delayed upstream request ID accepted: %v", err)
	}

	var saved ZTAPIRequestSettlement
	if err = db.First(&saved, row.ID).Error; err != nil {
		t.Fatal(err)
	}
	var ledgers, chargeAnchors, consumeLogOutbox int64
	if err = db.Model(&BalanceLedger{}).Where("request_id = ?", row.RequestID).Count(&ledgers).Error; err != nil {
		t.Fatal(err)
	}
	if err = db.Model(&ZTAPISupplierRefundCharge{}).Where("request_id = ?", row.RequestID).Count(&chargeAnchors).Error; err != nil {
		t.Fatal(err)
	}
	if err = db.Model(&ZTAPISettlementLogOutbox{}).Where("operation_id = ?", row.OperationID).Count(&consumeLogOutbox).Error; err != nil {
		t.Fatal(err)
	}
	if saved.ChargedQuota != 0 || saved.RefundedQuota != 0 || ledgers != 1 || chargeAnchors != 0 || consumeLogOutbox != 0 {
		t.Fatalf("lineage enrichment changed finance state: settlement=%+v ledgers=%d anchors=%d logs=%d", saved, ledgers, chargeAnchors, consumeLogOutbox)
	}
}

func TestZTAPISettlementDeletedOwnerReleaseKeepsLedgerConsistent(t *testing.T) {
	db, input := setupZTAPISettlement(t)
	row, err := BeginZTAPIRequestSettlement(input)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Delete(&User{}, row.UserID).Error; err != nil {
		t.Fatal(err)
	}
	if _, err = ReleaseZTAPIRequestSettlement(row.OperationID); err != nil {
		t.Fatal(err)
	}
	var user User
	if err = db.Unscoped().First(&user, row.UserID).Error; err != nil {
		t.Fatal(err)
	}
	if user.Quota != 1000 {
		t.Fatalf("deleted owner's ledger disagrees with quota %d", user.Quota)
	}
}

func TestZTAPISettlementRejectsUnreconstructableCharge(t *testing.T) {
	db, input := setupZTAPISettlement(t)
	row, err := BeginZTAPIRequestSettlement(input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = FinalizeZTAPIRequestSettlement(row.OperationID, 100, "{}", "[]"); err == nil {
		t.Fatal("positive charge with no dimensions accepted")
	}
	assertZTAPISettlementBalances(t, db, *row, 800, 800)
}

func TestZTAPISettlementExhaustedTokenNeedsNoSecondTokenWrite(t *testing.T) {
	db, input := setupZTAPISettlement(t)
	input.ReservedQuota = 1000
	row, err := BeginZTAPIRequestSettlement(input)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Exec(`CREATE TRIGGER forbid_noop_token_update BEFORE UPDATE ON tokens BEGIN SELECT RAISE(ABORT, 'token must not be written again'); END`).Error; err != nil {
		t.Fatal(err)
	}
	if _, err = FinalizeZTAPIRequestSettlement(row.OperationID, 1400, `{"input":1400}`, `[{"dimension":"input_tokens","units":"1400","unit_quota":"1","charged_quota":1400}]`); err != nil {
		t.Fatal(err)
	}
	assertZTAPISettlementBalances(t, db, *row, -400, 0)
}
