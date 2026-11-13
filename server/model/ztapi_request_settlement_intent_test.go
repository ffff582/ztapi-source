package model

import (
	"errors"
	"sync"
	"testing"
)

func saveZTAPIIntentTest(t *testing.T, row ZTAPIRequestSettlement, evidence ZTAPISettlementEvidence) {
	t.Helper()
	if err := saveZTAPISettlementFinalizationIntent(row.OperationID, 100, settlementReplayUsage, settlementReplayDimensions, evidence); err != nil {
		t.Fatal(err)
	}
}

func TestZTAPISettlementIntentRecoversCrashBeforeDebit(t *testing.T) {
	db, row, evidence := setupZTAPISettlementEvidence(t)
	saveZTAPIIntentTest(t, row, evidence)
	assertZTAPISettlementBalances(t, db, row, 800, 800)
	if err := RetryZTAPISettlementFinalizations(10); err != nil {
		t.Fatal(err)
	}
	var original ZTAPISettlementLogOutbox
	if err := db.Where("operation_id = ?", row.OperationID).Take(&original).Error; err != nil {
		t.Fatal(err)
	}
	if err := RetryZTAPISettlementFinalizations(10); err != nil {
		t.Fatal(err)
	}
	assertZTAPISettlementReplayUnchanged(t, db, row, original)
}

func TestZTAPISettlementIntentRollbackThenPendingRetry(t *testing.T) {
	db, row, evidence := setupZTAPISettlementEvidence(t)
	if err := db.Exec(`CREATE TRIGGER reject_final_outbox BEFORE INSERT ON ztapi_settlement_log_outboxes BEGIN SELECT RAISE(ABORT, 'test failure'); END`).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := FinalizeZTAPIRequestSettlementWithEvidence(row.OperationID, 100, settlementReplayUsage, settlementReplayDimensions, evidence); err == nil {
		t.Fatal("expected financial transaction rollback")
	}
	assertZTAPISettlementBalances(t, db, row, 800, 800)
	for _, item := range []struct {
		model any
		want  int64
	}{{&ZTAPISettlementFinalizationIntent{}, 1}, {&BalanceLedger{}, 1}, {&ZTAPISupplierRefundCharge{}, 0}, {&ZTAPISettlementLogOutbox{}, 0}} {
		var count int64
		if err := db.Model(item.model).Count(&count).Error; err != nil || count != item.want {
			t.Fatalf("rollback %T count=%d err=%v", item.model, count, err)
		}
	}
	if _, err := PendZTAPIRequestSettlement(row.OperationID, settlementReplayUsage, `["settlement_retry_required"]`); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`DROP TRIGGER reject_final_outbox`).Error; err != nil {
		t.Fatal(err)
	}
	if err := RetryZTAPISettlementFinalizations(1); err != nil {
		t.Fatal(err)
	}
	var original ZTAPISettlementLogOutbox
	if err := db.Where("operation_id = ?", row.OperationID).Take(&original).Error; err != nil {
		t.Fatal(err)
	}
	if err := RetryZTAPISettlementFinalizations(1); err != nil {
		t.Fatal(err)
	}
	assertZTAPISettlementReplayUnchanged(t, db, row, original)
}

func TestZTAPISettlementIntentRejectsConflictAndLegacyBypass(t *testing.T) {
	db, row, evidence := setupZTAPISettlementEvidence(t)
	saveZTAPIIntentTest(t, row, evidence)
	changed := evidence
	changed.FinalAttempt = 1
	if err := saveZTAPISettlementFinalizationIntent(row.OperationID, 100, settlementReplayUsage, settlementReplayDimensions, changed); !errors.Is(err, ErrZTAPISettlementConflict) {
		t.Fatalf("conflicting attempt intent: %v", err)
	}
	if err := saveZTAPISettlementFinalizationIntent(row.OperationID, 101, settlementReplayUsage, settlementReplayDimensions, evidence); err == nil {
		t.Fatal("conflicting amount intent accepted")
	}
	if _, err := FinalizeZTAPIRequestSettlement(row.OperationID, 100, settlementReplayUsage, settlementReplayDimensions); !errors.Is(err, ErrZTAPISettlementConflict) {
		t.Fatalf("evidence-less bypass: %v", err)
	}
	var before ZTAPISettlementFinalizationIntent
	if err := db.Where("operation_id = ?", row.OperationID).Take(&before).Error; err != nil {
		t.Fatal(err)
	}
	evidence.ConsumeLog.UseTime++
	evidence.ConsumeLog.CreatedAt++
	saveZTAPIIntentTest(t, row, evidence)
	var after ZTAPISettlementFinalizationIntent
	if err := db.Where("operation_id = ?", row.OperationID).Take(&after).Error; err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("retry overwrote immutable intent")
	}
	assertZTAPISettlementBalances(t, db, row, 800, 800)
}

func TestZTAPISettlementIntentNeverChargesUnknownPending(t *testing.T) {
	for _, intentFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "no_intent", true: "existing_intent"}[intentFirst], func(t *testing.T) {
			db, row, evidence := setupZTAPISettlementEvidence(t)
			if intentFirst {
				saveZTAPIIntentTest(t, row, evidence)
			}
			if _, err := PendZTAPIRequestSettlement(row.OperationID, settlementReplayUsage, `["cache_write"]`); err != nil {
				t.Fatal(err)
			}
			if _, err := FinalizeZTAPIRequestSettlementWithEvidence(row.OperationID, 100, settlementReplayUsage, settlementReplayDimensions, evidence); !errors.Is(err, ErrZTAPISettlementPending) {
				t.Fatalf("unknown pending accepted: %v", err)
			}
			_ = RetryZTAPISettlementFinalizations(10)
			assertZTAPISettlementBalances(t, db, row, 800, 800)
			var saved ZTAPIRequestSettlement
			if err := db.First(&saved, row.ID).Error; err != nil || saved.Status != ZTAPISettlementPending {
				t.Fatalf("unknown pending changed: %+v %v", saved, err)
			}
		})
	}
}

func TestZTAPISettlementIntentRetryFairness(t *testing.T) {
	db, first, evidence := setupZTAPISettlementEvidence(t)
	saveZTAPIIntentTest(t, first, evidence)
	second := first
	second.OperationID, second.RequestID = "operation-2", "request-2"
	row, err := BeginZTAPIRequestSettlement(second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = BeginZTAPIRequestAttempt(row.OperationID, 11, "cred-v1", "chat"); err != nil {
		t.Fatal(err)
	}
	secondEvidence := evidence
	secondEvidence.FinalAttempt = 1
	secondEvidence.ConsumeLog.RequestId = row.RequestID
	saveZTAPIIntentTest(t, *row, secondEvidence)
	if err = db.Exec(`CREATE TRIGGER reject_first_final BEFORE INSERT ON ztapi_settlement_log_outboxes WHEN NEW.operation_id = 'operation-1' BEGIN SELECT RAISE(ABORT, 'first remains blocked'); END`).Error; err != nil {
		t.Fatal(err)
	}
	if err = RetryZTAPISettlementFinalizations(1); err == nil {
		t.Fatal("expected first failure")
	}
	if err = RetryZTAPISettlementFinalizations(1); err != nil {
		t.Fatal(err)
	}
	var saved ZTAPIRequestSettlement
	if err = db.First(&saved, row.ID).Error; err != nil || saved.Status != ZTAPISettlementSettled {
		t.Fatalf("later intent starved: %+v %v", saved, err)
	}
}

func TestZTAPISettlementCacheRetryFairness(t *testing.T) {
	db, row := setupZTAPISettlement(t)
	first, err := BeginZTAPIRequestSettlement(row)
	if err != nil {
		t.Fatal(err)
	}
	row.OperationID, row.RequestID = "operation-2", "request-2"
	second, err := BeginZTAPIRequestSettlement(row)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Model(&ZTAPIRequestSettlement{}).Where("id IN ?", []uint{first.ID, second.ID}).UpdateColumn("cache_sync_pending", true).Error; err != nil {
		t.Fatal(err)
	}
	if err = db.Exec(`CREATE TRIGGER keep_first_cache_pending BEFORE UPDATE OF cache_sync_pending ON ztapi_request_settlements WHEN OLD.operation_id = 'operation-1' BEGIN SELECT RAISE(ABORT, 'cache remains pending'); END`).Error; err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err = RetryZTAPISettlementCaches(1); err != nil {
			t.Fatal(err)
		}
	}
	var saved ZTAPIRequestSettlement
	if err = db.First(&saved, second.ID).Error; err != nil || saved.CacheSyncPending {
		t.Fatalf("later cache starved: %+v %v", saved, err)
	}
}

func TestZTAPISettlementIntentConcurrentWorkersDebitOnce(t *testing.T) {
	db, row, evidence := setupZTAPISettlementEvidence(t)
	saveZTAPIIntentTest(t, row, evidence)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- RetryZTAPISettlementFinalizations(1)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var original ZTAPISettlementLogOutbox
	if err := db.Where("operation_id = ?", row.OperationID).Take(&original).Error; err != nil {
		t.Fatal(err)
	}
	assertZTAPISettlementReplayUnchanged(t, db, row, original)
}

func TestZTAPISettlementIntentCorruptionNeverDebits(t *testing.T) {
	db, row, evidence := setupZTAPISettlementEvidence(t)
	saveZTAPIIntentTest(t, row, evidence)
	if err := db.Model(&ZTAPISettlementFinalizationIntent{}).Where("operation_id = ?", row.OperationID).UpdateColumn("actual_quota", 300).Error; err != nil {
		t.Fatal(err)
	}
	if err := RetryZTAPISettlementFinalizations(1); !errors.Is(err, ErrZTAPISettlementConflict) {
		t.Fatalf("corrupt intent accepted: %v", err)
	}
	assertZTAPISettlementBalances(t, db, row, 800, 800)
}

func TestZTAPISettlementIntentDoesNotCreateOnRetryMarkerWithoutIntent(t *testing.T) {
	db, row, evidence := setupZTAPISettlementEvidence(t)
	if _, err := PendZTAPIRequestSettlement(row.OperationID, settlementReplayUsage, `["settlement_retry_required"]`); err != nil {
		t.Fatal(err)
	}
	if _, err := FinalizeZTAPIRequestSettlementWithEvidence(row.OperationID, 100, settlementReplayUsage, settlementReplayDimensions, evidence); !errors.Is(err, ErrZTAPISettlementPending) {
		t.Fatalf("reconstructed absent intent: %v", err)
	}
	if err := RetryZTAPISettlementFinalizations(1); err != nil {
		t.Fatal(err)
	}
	assertZTAPISettlementBalances(t, db, row, 800, 800)
}

func TestZTAPISettlementIntentSkipsAlreadyReleased(t *testing.T) {
	db, row, evidence := setupZTAPISettlementEvidence(t)
	saveZTAPIIntentTest(t, row, evidence)
	// Simulate an existing terminal row; workers must not revive it.
	if err := db.Model(&ZTAPIRequestSettlement{}).Where("id = ?", row.ID).UpdateColumn("status", ZTAPISettlementReleased).Error; err != nil {
		t.Fatal(err)
	}
	if err := RetryZTAPISettlementFinalizations(1); err != nil {
		t.Fatal(err)
	}
	assertZTAPISettlementBalances(t, db, row, 800, 800)
	var saved ZTAPISettlementFinalizationIntent
	if err := db.Where("operation_id = ?", row.OperationID).Take(&saved).Error; err != nil || saved.CompletedAt == 0 {
		t.Fatalf("terminal intent not retired: %+v %v", saved, err)
	}
}

func TestZTAPISettlementConcurrentBeginReservesOnce(t *testing.T) {
	db, input := setupZTAPISettlement(t)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := BeginZTAPIRequestSettlement(input)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	assertZTAPISettlementBalances(t, db, input, 800, 800)
	var count int64
	if err := db.Model(&BalanceLedger{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("begin ledger count=%d err=%v", count, err)
	}
}

func TestZTAPISettlementIntentFinishCannotDowngradeKnownUsage(t *testing.T) {
	db, row, evidence := setupZTAPISettlementEvidence(t)
	saveZTAPIIntentTest(t, row, evidence)
	// Final debit and its immediate pending handoff failed; deferred request
	// cleanup only knows dispatch occurred, but durable intent still owns usage.
	pending, err := PendZTAPIRequestSettlement(row.OperationID, "{}", `["upstream_billing_unconfirmed"]`)
	if err != nil {
		t.Fatal(err)
	}
	if pending.UsageJSON != settlementReplayUsage || pending.MissingDimensionsJSON != `["settlement_retry_required"]` {
		t.Fatalf("known intent downgraded: %+v", pending)
	}
	if _, err = PendZTAPIRequestSettlement(row.OperationID, "{}", `["upstream_billing_unconfirmed"]`); err != nil {
		t.Fatal(err)
	}
	if err = RetryZTAPISettlementFinalizations(1); err != nil {
		t.Fatal(err)
	}
	assertZTAPISettlementBalances(t, db, row, 900, 900)
}

func TestZTAPISettlementReleaseCreditMayLeaveWalletNegative(t *testing.T) {
	db, input := setupZTAPISettlement(t)
	first, err := BeginZTAPIRequestSettlement(input)
	if err != nil {
		t.Fatal(err)
	}
	input.OperationID, input.RequestID = "operation-2", "request-2"
	second, err := BeginZTAPIRequestSettlement(input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = FinalizeZTAPIRequestSettlement(first.OperationID, 1400, `{"input":1400}`, `[{"dimension":"input_tokens","units":"1400","unit_quota":"1","charged_quota":1400}]`); err != nil {
		t.Fatal(err)
	}
	assertZTAPISettlementBalances(t, db, *second, -600, 0)
	if _, err = ReleaseZTAPIRequestSettlement(second.OperationID); err != nil {
		t.Fatal(err)
	}
	if _, err = ReleaseZTAPIRequestSettlement(second.OperationID); err != nil {
		t.Fatal(err)
	}
	assertZTAPISettlementBalances(t, db, *second, -400, 200)
	var user User
	if err = db.First(&user, second.UserID).Error; err != nil {
		t.Fatal(err)
	}
	if !user.DebtSuspended || user.UsedQuota != 1400 || user.RequestCount != 1 {
		t.Fatalf("release changed debt or usage: %+v", user)
	}
}
