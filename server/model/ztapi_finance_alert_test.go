package model

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestZTAPIFinanceAlertRequestReferenceDoesNotLeakArbitraryIDs(t *testing.T) {
	canonical := "01a07c2d-1d46-44a1-b294-2c3bea4912fd"
	require.Equal(t, canonical, ZTAPIFinanceAlertRequestReference(canonical))
	for _, raw := range []string{"sk-secret-request", "https://api.telegram.org/bot123456:private/sendMessage", "request\nInjected: secret", "", strings.Repeat("x", 128)} {
		want := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(raw)))
		require.Equal(t, want, ZTAPIFinanceAlertRequestReference(raw))
		require.Equal(t, want, ZTAPIFinanceAlertRequestReference(want), "stored references stay stable at delivery")
	}
}

func TestZTAPIFinanceAlertAttemptReasonsRemainActionable(t *testing.T) {
	for _, reason := range []string{"upstream_attempt_billing_unconfirmed", "awaiting_approval", "evidence_conflict",
		"original_request_missing", "original_owner_missing", "original_owner_deleted", "unsupported_parent_scope",
		"original_lineage_mismatch", "approval_conflict", "missing_distinct_usage_evidence", "billing_dimensions_pending",
		"billing_application_pending", "billing_log_unavailable", "overlapping_billed_usage", "existing_finalization_intent"} {
		require.Equal(t, reason, ZTAPIFinanceAlertSafeReason(reason))
	}
}

func TestZTAPIFinanceAlertImagePendingReasonsRemainActionable(t *testing.T) {
	for _, reason := range []string{
		"image_settlement_evidence_missing",
		"image_settlement_evidence_conflict",
		"image_usage_untrusted",
	} {
		require.Equal(t, reason, ZTAPIFinanceAlertSafeReason(reason))
	}
	require.Equal(t, "unclassified_pending_reason", ZTAPIFinanceAlertSafeReason("provider said sk-secret https://example.test"))
}

func TestZTAPIFinanceAlertMigrationAddsAttemptContextWithoutResettingDelivery(t *testing.T) {
	db, _, _, _ := setupZTAPISettlementLogTest(t)
	require.NoError(t, MigrateZTAPIFinanceAlerts(db))
	job := ZTAPIFinanceAlertOutbox{DedupKey: strings.Repeat("a", 64), SourceKind: "settlement", SourceRecordID: 1,
		SourceUpdatedAt: time.Unix(2000000000, 0).UTC(), Reason: "cache_write", Status: "sent", Attempts: 3,
		CreatedAt: 2000000000, SentAt: 2000000010, ReceiptJSON: `{"ok":true,"result":{"message_id":9,"date":2000000010}}`}
	require.NoError(t, db.Create(&job).Error)
	for _, column := range []string{"source_settlement_id", "source_attempt", "request_reference"} {
		require.NoError(t, db.Migrator().DropColumn(&ZTAPIFinanceAlertOutbox{}, column))
	}
	require.NoError(t, MigrateZTAPIFinanceAlerts(db))
	require.NoError(t, MigrateZTAPIFinanceAlerts(db))
	var stored ZTAPIFinanceAlertOutbox
	require.NoError(t, db.First(&stored, job.ID).Error)
	require.Equal(t, job, stored)
}

func TestZTAPIFinanceAlertQueueExcludesQueuedRowsBeforeLimit(t *testing.T) {
	db, _, _, _ := setupZTAPISettlementLogTest(t)
	require.NoError(t, db.AutoMigrate(&ZTAPISupplierRefund{}, &ZTAPIAttemptBillingReview{}))
	require.NoError(t, MigrateZTAPIFinanceAlerts(db))
	now := time.Unix(2000000000, 0).UTC()
	for i := 0; i < 105; i++ {
		require.NoError(t, db.Create(&ZTAPIRequestSettlement{OperationID: fmt.Sprintf("finance-op-%d", i), RequestID: fmt.Sprintf("finance-request-%d", i), Status: ZTAPISettlementPending, MissingDimensionsJSON: `["cache_write","input_tokens","cache_write"]`, UpdatedAt: now}).Error)
	}
	n, err := QueuePendingZTAPIFinanceAlerts(context.Background(), db, now, 100)
	require.NoError(t, err)
	require.Equal(t, 100, n)
	n, err = QueuePendingZTAPIFinanceAlerts(context.Background(), db, now, 100)
	require.NoError(t, err)
	require.Equal(t, 5, n, "already queued rows must not consume the selection limit")
	n, err = QueuePendingZTAPIFinanceAlerts(context.Background(), db, now, 100)
	require.NoError(t, err)
	require.Zero(t, n)
	var count int64
	require.NoError(t, db.Model(&ZTAPIFinanceAlertOutbox{}).Count(&count).Error)
	require.Equal(t, int64(105), count)
}

func TestZTAPIFinanceAlertQueueDeduplicatesReasonAndSanitizesSecrets(t *testing.T) {
	db, _, _, _ := setupZTAPISettlementLogTest(t)
	require.NoError(t, db.AutoMigrate(&ZTAPISupplierRefund{}, &ZTAPIAttemptBillingReview{}))
	require.NoError(t, MigrateZTAPIFinanceAlerts(db))
	now := time.Unix(2000000000, 0).UTC()
	request := ZTAPIRequestSettlement{OperationID: "finance-secret-op", RequestID: "sk-secret-request", Status: ZTAPISettlementPending, MissingDimensionsJSON: `["cache_write","https://api.telegram.org/bot123456:secret/sendMessage"]`, UsageJSON: `{"prompt":"private"}`, UpdatedAt: now}
	require.NoError(t, db.Create(&request).Error)
	refund := ZTAPISupplierRefund{ProofKey: "finance-proof", RequestID: "private-request", Status: "pending", PendingReason: "awaiting_approval", SubmissionJSON: `{"api_key":"private"}`, UpdatedAt: now}
	require.NoError(t, db.Create(&refund).Error)
	n, err := QueuePendingZTAPIFinanceAlerts(context.Background(), db, now, 100)
	require.NoError(t, err)
	require.Equal(t, 2, n)
	var jobs []ZTAPIFinanceAlertOutbox
	require.NoError(t, db.Order("id").Find(&jobs).Error)
	raw, err := common.Marshal(jobs)
	require.NoError(t, err)
	for _, forbidden := range []string{"secret", "private", "http", "api_key"} {
		require.NotContains(t, string(raw), forbidden)
	}
	// Same reason with a new source revision must not create another alert.
	require.NoError(t, db.Model(&request).Updates(map[string]any{"missing_dimensions_json": `["https://api.telegram.org/bot123456:secret/sendMessage","cache_write"]`, "updated_at": now.Add(time.Second)}).Error)
	_, err = QueuePendingZTAPIFinanceAlerts(context.Background(), db, now.Add(time.Second), 100)
	require.NoError(t, err)
	var count int64
	require.NoError(t, db.Model(&ZTAPIFinanceAlertOutbox{}).Count(&count).Error)
	require.Equal(t, int64(2), count)
	require.NoError(t, db.Model(&refund).Updates(map[string]any{"pending_reason": "original_charge_unsettled", "updated_at": now.Add(2 * time.Second)}).Error)
	_, err = QueuePendingZTAPIFinanceAlerts(context.Background(), db, now.Add(2*time.Second), 100)
	require.NoError(t, err)
	require.NoError(t, db.Model(&ZTAPIFinanceAlertOutbox{}).Count(&count).Error)
	require.Equal(t, int64(3), count)
	n, err = QueuePendingZTAPIFinanceAlerts(context.Background(), db, now.Add(3*time.Second), 100)
	require.NoError(t, err)
	require.Zero(t, n)
}

func TestZTAPIFinanceAlertLeaseReclaimRejectsOldAcknowledgement(t *testing.T) {
	db, _, _, _ := setupZTAPISettlementLogTest(t)
	require.NoError(t, MigrateZTAPIFinanceAlerts(db))
	now := time.Unix(2000000000, 0).UTC()
	source := ZTAPIRequestSettlement{OperationID: "lease-op", RequestID: "lease-request", Status: ZTAPISettlementPending, MissingDimensionsJSON: `["cache_write"]`}
	require.NoError(t, db.Create(&source).Error)
	require.NoError(t, db.Create(&ZTAPIFinanceAlertOutbox{DedupKey: strings.Repeat("a", 64), SourceKind: "settlement", SourceRecordID: source.ID, Reason: "cache_write", Status: "pending", NextAttemptAt: now.Unix()}).Error)
	first, err := ClaimZTAPIFinanceAlerts(context.Background(), db, now, 10, 240)
	require.NoError(t, err)
	require.Len(t, first, 1)
	again, err := ClaimZTAPIFinanceAlerts(context.Background(), db, now, 10, 240)
	require.NoError(t, err)
	require.Empty(t, again)
	reclaimed, err := ClaimZTAPIFinanceAlerts(context.Background(), db, now.Add(241*time.Second), 10, 240)
	require.NoError(t, err)
	require.Len(t, reclaimed, 1)
	require.NotEqual(t, first[0].LeaseToken, reclaimed[0].LeaseToken)
	receipt := `{"ok":true,"result":{"message_id":17,"date":2000000241}}`
	require.Error(t, FinishZTAPIFinanceAlert(context.Background(), db, first[0], now.Add(242*time.Second), true, "telegram_accepted", receipt))
	require.NoError(t, FinishZTAPIFinanceAlert(context.Background(), db, reclaimed[0], now.Add(242*time.Second), true, "telegram_accepted", receipt))
	var stored ZTAPIFinanceAlertOutbox
	require.NoError(t, db.First(&stored, first[0].ID).Error)
	require.Equal(t, "sent", stored.Status)
	require.JSONEq(t, receipt, stored.ReceiptJSON)
}

func TestZTAPIFinanceAlertSourceLookupFailureRetainsRetry(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(fmt.Sprintf("missing_%t", missing), func(t *testing.T) {
			db, _, _, _ := setupZTAPISettlementLogTest(t)
			require.NoError(t, MigrateZTAPIFinanceAlerts(db))
			now := time.Unix(2000000000, 0).UTC()
			source := ZTAPIRequestSettlement{OperationID: "lookup-op", RequestID: "lookup-request", Status: ZTAPISettlementPending, MissingDimensionsJSON: `["cache_write"]`}
			require.NoError(t, db.Create(&source).Error)
			job := ZTAPIFinanceAlertOutbox{DedupKey: strings.Repeat("a", 64), SourceKind: "settlement", SourceRecordID: source.ID, Reason: "cache_write", Status: "pending", NextAttemptAt: now.Unix()}
			require.NoError(t, db.Create(&job).Error)
			reason := "alert_source_lookup_failed"
			if missing {
				reason = "alert_source_missing"
				require.NoError(t, db.Delete(&source).Error)
			} else {
				require.NoError(t, db.Callback().Query().Before("gorm:query").Register("finance_lookup_failure", func(tx *gorm.DB) {
					if tx.Statement.Table == "ztapi_request_settlements" {
						require.Implements(t, (*gorm.TxCommitter)(nil), tx.Statement.ConnPool, "source lookup must share the claim transaction")
						tx.AddError(errors.New("https://private.invalid/sk-secret"))
					}
				}))
			}
			claimed, err := ClaimZTAPIFinanceAlerts(context.Background(), db, now, 10, 240)
			require.Error(t, err, "operators must see unresolved source lookup failures")
			require.NotContains(t, err.Error(), "private")
			require.Empty(t, claimed)
			require.NoError(t, db.First(&job, job.ID).Error)
			require.Equal(t, "pending", job.Status)
			require.Equal(t, reason, job.LastReason)
			require.Greater(t, job.NextAttemptAt, now.Unix())
			require.Empty(t, job.LeaseToken)
			require.Zero(t, job.Attempts)
			require.Empty(t, job.ReceiptJSON)
			if missing {
				require.NoError(t, db.Create(&source).Error)
			} else {
				require.NoError(t, db.Callback().Query().Remove("finance_lookup_failure"))
			}
			claimed, err = ClaimZTAPIFinanceAlerts(context.Background(), db, now.Add(time.Hour), 10, 240)
			require.NoError(t, err)
			require.Len(t, claimed, 1, "a restored pending source remains deliverable")
		})
	}
}

func TestZTAPIFinanceAlertResolvedSourcePreservesSentReceipt(t *testing.T) {
	db, _, _, _ := setupZTAPISettlementLogTest(t)
	require.NoError(t, MigrateZTAPIFinanceAlerts(db))
	now := time.Unix(2000000000, 0).UTC()
	job := ZTAPIFinanceAlertOutbox{DedupKey: strings.Repeat("a", 64), SourceKind: "settlement", SourceRecordID: 1,
		Reason: "cache_write", Status: "sent", Attempts: 1, SentAt: now.Unix(), ReceiptJSON: `{"ok":true,"result":{"message_id":9,"date":2000000000}}`}
	require.NoError(t, db.Create(&job).Error)
	require.NoError(t, db.First(&job, job.ID).Error)
	claimed, err := ClaimZTAPIFinanceAlerts(context.Background(), db, now, 10, 240)
	require.NoError(t, err)
	require.Empty(t, claimed)
	var stored ZTAPIFinanceAlertOutbox
	require.NoError(t, db.First(&stored, job.ID).Error)
	require.Equal(t, job, stored)
}

func TestZTAPIFinanceAlertChangedReasonCannotDiscardUnsentWarning(t *testing.T) {
	db, _, _, _ := setupZTAPISettlementLogTest(t)
	require.NoError(t, db.AutoMigrate(&ZTAPISupplierRefund{}, &ZTAPIAttemptBillingReview{}))
	require.NoError(t, MigrateZTAPIFinanceAlerts(db))
	now := time.Unix(2000000000, 0).UTC()
	source := ZTAPIRequestSettlement{OperationID: "changed-op", RequestID: "changed-request", Status: ZTAPISettlementPending,
		MissingDimensionsJSON: `["cache_write"]`, UpdatedAt: now}
	require.NoError(t, db.Create(&source).Error)
	_, err := QueuePendingZTAPIFinanceAlerts(context.Background(), db, now, 100)
	require.NoError(t, err)
	require.NoError(t, db.Model(&source).Updates(map[string]any{"missing_dimensions_json": `["input_tokens"]`, "updated_at": now.Add(time.Second)}).Error)
	claimed, err := ClaimZTAPIFinanceAlerts(context.Background(), db, now.Add(time.Second), 10, 240)
	require.ErrorContains(t, err, "alert_source_reason_changed")
	require.Empty(t, claimed, "do not deliver an obsolete reason")
	var job ZTAPIFinanceAlertOutbox
	require.NoError(t, db.First(&job).Error)
	require.Equal(t, "pending", job.Status, "changed reason does not prove resolution")
	require.Equal(t, "alert_source_reason_changed", job.LastReason)
	require.NoError(t, db.Model(&source).Updates(map[string]any{"missing_dimensions_json": `["cache_write"]`, "updated_at": now.Add(time.Hour)}).Error)
	_, err = QueuePendingZTAPIFinanceAlerts(context.Background(), db, now.Add(time.Hour), 100)
	require.NoError(t, err)
	claimed, err = ClaimZTAPIFinanceAlerts(context.Background(), db, now.Add(time.Hour), 10, 240)
	require.NoError(t, err)
	require.Len(t, claimed, 1, "an unsent reason that recurs must not be suppressed by dedup")
	require.Equal(t, job.ID, claimed[0].ID)
}

func TestZTAPIFinanceAlertResolvedSourceCancelsExpiredLease(t *testing.T) {
	db, _, _, _ := setupZTAPISettlementLogTest(t)
	require.NoError(t, MigrateZTAPIFinanceAlerts(db))
	now := time.Unix(2000000000, 0).UTC()
	source := ZTAPIRequestSettlement{OperationID: "expired-op", RequestID: "expired-request", Status: ZTAPISettlementPending, MissingDimensionsJSON: `["cache_write"]`}
	require.NoError(t, db.Create(&source).Error)
	job := ZTAPIFinanceAlertOutbox{DedupKey: strings.Repeat("a", 64), SourceKind: "settlement", SourceRecordID: source.ID, Reason: "cache_write", Status: "pending", NextAttemptAt: now.Unix()}
	require.NoError(t, db.Create(&job).Error)
	claimed, err := ClaimZTAPIFinanceAlerts(context.Background(), db, now, 10, 240)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	require.NoError(t, db.Model(&source).Update("status", ZTAPISettlementSettled).Error)
	again, err := ClaimZTAPIFinanceAlerts(context.Background(), db, now.Add(241*time.Second), 10, 240)
	require.NoError(t, err)
	require.Empty(t, again)
	require.ErrorIs(t, FinishZTAPIFinanceAlert(context.Background(), db, claimed[0], now.Add(242*time.Second), true, "telegram_accepted",
		`{"ok":true,"result":{"message_id":9,"date":2000000000}}`), ErrZTAPIFinanceAlertLeaseLost)
	require.NoError(t, db.First(&job, job.ID).Error)
	require.Equal(t, "cancelled", job.Status)
	require.Equal(t, "alert_source_resolved", job.LastReason)
	require.Equal(t, 1, job.Attempts)
	require.Empty(t, job.ReceiptJSON)
}

func TestZTAPIFinanceAlertStaleReservedAgeBoundaryAndNoRelease(t *testing.T) {
	db, _, _, _ := setupZTAPISettlementLogTest(t)
	require.NoError(t, db.AutoMigrate(&ZTAPISupplierRefund{}, &ZTAPIAttemptBillingReview{}))
	require.NoError(t, MigrateZTAPIFinanceAlerts(db))
	now := time.Unix(2000000000, 0).UTC()
	var rows []ZTAPIRequestSettlement
	for i, fixture := range []struct {
		status string
		age    time.Duration
	}{
		{ZTAPISettlementReserved, 25 * time.Hour},
		{ZTAPISettlementReserved, 24 * time.Hour},
		{ZTAPISettlementReserved, 24*time.Hour - time.Second},
		{ZTAPISettlementSettled, 25 * time.Hour},
		{ZTAPISettlementReleased, 25 * time.Hour},
	} {
		row := ZTAPIRequestSettlement{OperationID: fmt.Sprintf("stale-op-%d", i), RequestID: fmt.Sprintf("stale-request-%d", i),
			Status: fixture.status, ReservedQuota: 73, TokenReservedQuota: 73, CreatedAt: now.Add(-fixture.age), UpdatedAt: now,
			MissingDimensionsJSON: `["private-provider-error"]`}
		require.NoError(t, db.Create(&row).Error)
		rows = append(rows, row)
	}
	n, err := QueuePendingZTAPIFinanceAlerts(context.Background(), db, now, 100)
	require.NoError(t, err)
	require.Equal(t, 2, n)
	var jobs []ZTAPIFinanceAlertOutbox
	require.NoError(t, db.Order("source_record_id").Find(&jobs).Error)
	require.Len(t, jobs, 2)
	for i, job := range jobs {
		require.Equal(t, "settlement", job.SourceKind)
		require.Equal(t, rows[i].ID, job.SourceRecordID)
		require.Equal(t, "stale_reserved_hold", job.Reason)
	}
	n, err = QueuePendingZTAPIFinanceAlerts(context.Background(), db, now, 100)
	require.NoError(t, err)
	require.Zero(t, n)
	n, err = QueuePendingZTAPIFinanceAlerts(context.Background(), db, now.Add(time.Second), 100)
	require.NoError(t, err)
	require.Equal(t, 1, n, "a previously recent reservation becomes eligible at 24 hours")
	for _, original := range rows {
		var stored ZTAPIRequestSettlement
		require.NoError(t, db.First(&stored, original.ID).Error)
		require.Equal(t, original, stored, "alert scanning must not mutate the reservation or its balances")
	}
}

func TestZTAPIFinanceAlertAttemptReviewSurvivesSettledParent(t *testing.T) {
	db, _, _, _ := setupZTAPISettlementLogTest(t)
	require.NoError(t, db.AutoMigrate(&ZTAPISupplierRefund{}, &ZTAPIAttemptBillingReview{}))
	require.NoError(t, MigrateZTAPIFinanceAlerts(db))
	now := time.Unix(2000000000, 0).UTC()
	parent := ZTAPIRequestSettlement{OperationID: "attempt-alert-parent", RequestID: "01a07c2d-1d46-44a1-b294-2c3bea4912fd",
		Status: ZTAPISettlementSettled, ChargedQuota: 90, FinalAttempt: 2, CreatedAt: now, UpdatedAt: now}
	require.NoError(t, db.Create(&parent).Error)
	first := ZTAPIAttemptBillingReview{SettlementID: parent.ID, RequestID: parent.RequestID, Attempt: 1,
		Status: "pending", PendingReason: "upstream_attempt_billing_unconfirmed", UpdatedAt: now}
	require.NoError(t, db.Create(&first).Error)
	for i, status := range []string{"verified_billed", "verified_nocharge"} {
		require.NoError(t, db.Create(&ZTAPIAttemptBillingReview{SettlementID: parent.ID, RequestID: parent.RequestID,
			Attempt: i + 2, Status: status, PendingReason: first.PendingReason, UpdatedAt: now}).Error)
	}
	n, err := QueuePendingZTAPIFinanceAlerts(context.Background(), db, now, 100)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	var jobs []ZTAPIFinanceAlertOutbox
	require.NoError(t, db.Find(&jobs).Error)
	require.Len(t, jobs, 1)
	require.Equal(t, "attempt_review", jobs[0].SourceKind)
	require.Equal(t, first.ID, jobs[0].SourceRecordID)
	require.Equal(t, first.PendingReason, jobs[0].Reason)
	raw, err := common.Marshal(jobs[0])
	require.NoError(t, err)
	var projection map[string]any
	require.NoError(t, common.Unmarshal(raw, &projection))
	require.Equal(t, parent.RequestID, projection["RequestReference"])
	require.Equal(t, float64(parent.ID), projection["SourceSettlementID"])
	require.Equal(t, float64(1), projection["SourceAttempt"])
	n, err = QueuePendingZTAPIFinanceAlerts(context.Background(), db, now, 100)
	require.NoError(t, err)
	require.Zero(t, n)
	require.NoError(t, db.Model(&first).Updates(map[string]any{"pending_reason": "billing_application_pending", "updated_at": now.Add(time.Second)}).Error)
	n, err = QueuePendingZTAPIFinanceAlerts(context.Background(), db, now.Add(time.Second), 100)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	var stored ZTAPIRequestSettlement
	require.NoError(t, db.First(&stored, parent.ID).Error)
	require.Equal(t, parent, stored, "alerts must not change canonical charges or parent status")
}

func TestZTAPIFinanceAlertAttemptQueueRemainsBoundedAndRedactsIDs(t *testing.T) {
	db, _, _, _ := setupZTAPISettlementLogTest(t)
	require.NoError(t, db.AutoMigrate(&ZTAPISupplierRefund{}, &ZTAPIAttemptBillingReview{}))
	require.NoError(t, MigrateZTAPIFinanceAlerts(db))
	now := time.Unix(2000000000, 0).UTC()
	for i := 0; i < 105; i++ {
		require.NoError(t, db.Create(&ZTAPIAttemptBillingReview{SettlementID: uint(i + 1), RequestID: fmt.Sprintf("https://private.invalid/sk-secret-%d", i),
			Attempt: 1, Status: "pending", PendingReason: "http://private.invalid/secret", UpdatedAt: now}).Error)
	}
	for _, want := range []int{100, 5, 0} {
		n, err := QueuePendingZTAPIFinanceAlerts(context.Background(), db, now, 100)
		require.NoError(t, err)
		require.Equal(t, want, n)
	}
	var jobs []ZTAPIFinanceAlertOutbox
	require.NoError(t, db.Find(&jobs).Error)
	require.Len(t, jobs, 105)
	raw, err := common.Marshal(jobs)
	require.NoError(t, err)
	for _, secret := range []string{"private", "http", "sk-secret"} {
		require.NotContains(t, string(raw), secret)
	}
	require.Contains(t, string(raw), "sha256:")
	require.NoError(t, MigrateZTAPIFinanceAlerts(db))
	var count int64
	require.NoError(t, db.Model(&ZTAPIFinanceAlertOutbox{}).Count(&count).Error)
	require.Equal(t, int64(105), count, "repeat migration preserves queued reviews")
}
