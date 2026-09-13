package model

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrZTAPIFinanceAlertInvalid = errors.New("invalid finance alert operation")
var ErrZTAPIFinanceAlertLeaseLost = errors.New("finance alert lease no longer owned")

const ZTAPIFinanceStaleReservationAge = 24 * time.Hour

type ZTAPIFinanceAlertOutbox struct {
	ID                 uint      `gorm:"primaryKey"`
	DedupKey           string    `gorm:"type:char(64);not null;uniqueIndex"`
	SourceKind         string    `gorm:"type:varchar(16);not null;index:ztapi_finance_source,priority:1"`
	SourceRecordID     uint      `gorm:"not null;index:ztapi_finance_source,priority:2"`
	SourceUpdatedAt    time.Time `gorm:"not null;index:ztapi_finance_source,priority:3"`
	SourceSettlementID uint      `gorm:"not null;default:0"`
	SourceAttempt      int       `gorm:"not null;default:0"`
	RequestReference   string    `gorm:"type:varchar(80);not null;default:''"`
	Reason             string    `gorm:"type:varchar(2048);not null"`
	Status             string    `gorm:"type:varchar(16);not null;index"`
	Attempts           int       `gorm:"not null;default:0"`
	LeaseToken         string    `gorm:"type:varchar(64);not null;default:''"`
	LeaseUntil         int64     `gorm:"type:bigint;not null;default:0"`
	NextAttemptAt      int64     `gorm:"type:bigint;not null;default:0;index"`
	LastReason         string    `gorm:"type:varchar(64);not null;default:''"`
	ReceiptJSON        string    `gorm:"type:text;not null"`
	CreatedAt          int64     `gorm:"type:bigint;not null"`
	SentAt             int64     `gorm:"type:bigint;not null;default:0"`
}

func (ZTAPIFinanceAlertOutbox) TableName() string { return "ztapi_finance_alert_outboxes" }

func MigrateZTAPIFinanceAlerts(db *gorm.DB) error {
	if db == nil {
		return ErrZTAPIFinanceAlertInvalid
	}
	return db.AutoMigrate(&ZTAPIHealthRequest{}, &ZTAPIFinanceAlertOutbox{})
}

// QueuePendingZTAPIFinanceAlerts accounts for at most 100 pending source
// revisions. Exclusion happens in SQL before LIMIT, so old queued records
// cannot hide newer pending requests/refunds/attempts. No source payload is persisted.
// Reserved requests become alertable after 24 hours from creation, regardless
// of metadata updates. This scan never releases holds or changes source rows.
func QueuePendingZTAPIFinanceAlerts(ctx context.Context, db *gorm.DB, now time.Time, limit int) (int, error) {
	if db == nil || ctx == nil || limit <= 0 || limit > 100 {
		return 0, ErrZTAPIFinanceAlertInvalid
	}
	var sources []struct {
		SourceKind         string
		SourceRecordID     uint
		SourceUpdatedAt    time.Time
		ReasonRaw          string
		SourceSettlementID uint
		SourceAttempt      int
		RequestRaw         string
	}
	err := db.WithContext(ctx).Raw(`
SELECT 'settlement' AS source_kind, r.id AS source_record_id, r.updated_at AS source_updated_at,
 CASE WHEN r.status = ? THEN '["stale_reserved_hold"]' ELSE r.missing_dimensions_json END AS reason_raw,
 0 AS source_settlement_id, 0 AS source_attempt, '' AS request_raw
FROM ztapi_request_settlements r
WHERE (r.status = ? OR (r.status = ? AND r.created_at <= ?)) AND NOT EXISTS (
 SELECT 1 FROM ztapi_finance_alert_outboxes o WHERE o.source_kind = 'settlement' AND o.source_record_id = r.id AND o.source_updated_at >= r.updated_at)
 AND NOT EXISTS (
 SELECT 1 FROM ztapi_health_requests h WHERE h.request_id = r.request_id AND h.source = 'acceptance')
UNION ALL
SELECT 'refund' AS source_kind, r.id AS source_record_id, r.updated_at AS source_updated_at, r.pending_reason AS reason_raw,
 0 AS source_settlement_id, 0 AS source_attempt, '' AS request_raw
FROM ztapi_supplier_refunds r
WHERE r.status = ? AND NOT EXISTS (
 SELECT 1 FROM ztapi_finance_alert_outboxes o WHERE o.source_kind = 'refund' AND o.source_record_id = r.id AND o.source_updated_at >= r.updated_at)
UNION ALL
SELECT 'attempt_review' AS source_kind, r.id AS source_record_id, r.updated_at AS source_updated_at, r.pending_reason AS reason_raw,
 r.settlement_id AS source_settlement_id, r.attempt AS source_attempt, r.request_id AS request_raw
FROM ztapi_attempt_billing_reviews r
WHERE r.status = ? AND NOT EXISTS (
 SELECT 1 FROM ztapi_finance_alert_outboxes o WHERE o.source_kind = 'attempt_review' AND o.source_record_id = r.id AND o.source_updated_at >= r.updated_at)
 AND NOT EXISTS (
 SELECT 1 FROM ztapi_request_settlements s JOIN ztapi_health_requests h ON h.request_id = s.request_id AND h.source = 'acceptance'
 WHERE s.id = r.settlement_id)
ORDER BY source_updated_at ASC, source_kind ASC, source_record_id ASC LIMIT ?`, ZTAPISettlementReserved, ZTAPISettlementPending,
		ZTAPISettlementReserved, now.Add(-ZTAPIFinanceStaleReservationAge), "pending", "pending", limit).Scan(&sources).Error
	if err != nil {
		return 0, err
	}
	queued := 0
	for _, source := range sources {
		reason := ztapiFinanceSourceReason(source.SourceKind, source.ReasonRaw)
		key := fmt.Sprintf("%s:%d:%s", source.SourceKind, source.SourceRecordID, reason)
		job := ZTAPIFinanceAlertOutbox{DedupKey: fmt.Sprintf("%x", sha256.Sum256([]byte(key))), SourceKind: source.SourceKind, SourceRecordID: source.SourceRecordID,
			SourceUpdatedAt: source.SourceUpdatedAt, Reason: reason, Status: "pending", NextAttemptAt: now.Unix(), CreatedAt: now.Unix(), ReceiptJSON: ""}
		if source.SourceKind == "attempt_review" {
			job.SourceSettlementID, job.SourceAttempt = source.SourceSettlementID, source.SourceAttempt
			job.RequestReference = ZTAPIFinanceAlertRequestReference(source.RequestRaw)
		}
		// A changed timestamp with the same reason acknowledges the source
		// revision without resetting delivery state or creating another alert.
		err := db.WithContext(ctx).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "dedup_key"}}, DoUpdates: clause.Assignments(map[string]any{
			"source_updated_at": gorm.Expr("CASE WHEN source_updated_at < ? THEN ? ELSE source_updated_at END", source.SourceUpdatedAt, source.SourceUpdatedAt),
		})}).Create(&job).Error
		if err != nil {
			return queued, err
		}
		queued++
	}
	return queued, nil
}

// Managed request IDs are server-generated canonical UUIDs. Older or malformed
// IDs may contain customer text or credentials, so expose only a stable digest.
func ZTAPIFinanceAlertRequestReference(raw string) string {
	if id, err := uuid.Parse(raw); err == nil && id.String() == strings.ToLower(raw) {
		return id.String()
	}
	if strings.HasPrefix(raw, "sha256:") && len(raw) == 71 {
		if _, err := hex.DecodeString(raw[7:]); err == nil {
			return strings.ToLower(raw)
		}
	}
	return fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(raw)))
}

func ClaimZTAPIFinanceAlerts(ctx context.Context, db *gorm.DB, now time.Time, limit int, leaseSeconds int64) ([]ZTAPIFinanceAlertOutbox, error) {
	if db == nil || ctx == nil || limit <= 0 || limit > 10 || leaseSeconds < 30 || leaseSeconds > 3600 {
		return nil, ErrZTAPIFinanceAlertInvalid
	}
	eligible := "next_attempt_at <= ? AND (status = ? OR (status = ? AND lease_until <= ?))"
	var candidates []ZTAPIFinanceAlertOutbox
	if err := db.WithContext(ctx).Where(eligible, now.Unix(), "pending", "sending", now.Unix()).Order("next_attempt_at ASC, id ASC").Limit(limit).Find(&candidates).Error; err != nil {
		return nil, err
	}
	var claimed []ZTAPIFinanceAlertOutbox
	var failures []error
	for _, candidate := range candidates {
		token := common.GetUUID()
		var job ZTAPIFinanceAlertOutbox
		ready, retryReason := false, ""
		err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			// Take the outbox write lock first, including on SQLite. Source locks
			// stay held through claim commit; no network work runs in this transaction.
			updated := tx.Model(&ZTAPIFinanceAlertOutbox{}).Where("id = ?", candidate.ID).
				Where(eligible, now.Unix(), "pending", "sending", now.Unix()).Updates(map[string]any{
				"status": "sending", "lease_token": token, "lease_until": now.Unix() + leaseSeconds,
			})
			if updated.Error != nil || updated.RowsAffected == 0 {
				return updated.Error
			}
			if err := tx.Where("id = ? AND lease_token = ?", candidate.ID, token).Take(&job).Error; err != nil {
				return err
			}
			reason, err := ztapiFinanceValidateSource(tx, job, now)
			if err != nil {
				retryReason = reason
				return err
			}
			if reason != "" {
				return tx.Model(&job).Updates(map[string]any{
					"status": "cancelled", "last_reason": reason, "lease_token": "", "lease_until": 0,
				}).Error
			}
			if err := tx.Model(&job).UpdateColumn("attempts", gorm.Expr("attempts + 1")).Error; err != nil {
				return err
			}
			job.Attempts++
			ready = true
			return nil
		})
		if err != nil {
			if retryReason != "" {
				// A failed SQL read can abort PostgreSQL's transaction. Record the
				// retry only after rollback, fenced against any intervening claimant.
				retry := db.WithContext(ctx).Model(&ZTAPIFinanceAlertOutbox{}).
					Where("id = ? AND lease_token = ?", candidate.ID, candidate.LeaseToken).
					Where(eligible, now.Unix(), "pending", "sending", now.Unix()).Updates(map[string]any{
					"status": "pending", "last_reason": retryReason, "lease_token": "", "lease_until": 0,
					"next_attempt_at": now.Unix() + 60,
				})
				failures = append(failures, fmt.Errorf("finance alert %d: %s", candidate.ID, retryReason), retry.Error)
			} else {
				failures = append(failures, err)
			}
			continue
		}
		if ready {
			claimed = append(claimed, job)
		}
	}
	return claimed, errors.Join(failures...)
}

// Only an observed terminal state cancels a warning.
// Missing/deleted sources require operator review and remain retryable; a DB
// failure must never be interpreted as evidence that reconciliation finished.
func ztapiFinanceValidateSource(tx *gorm.DB, job ZTAPIFinanceAlertOutbox, now time.Time) (string, error) {
	query := tx.Clauses(clause.Locking{Strength: "UPDATE"})
	var raw string
	var err error
	switch job.SourceKind {
	case "settlement":
		var row ZTAPIRequestSettlement
		err = query.Select("id", "status", "missing_dimensions_json", "created_at").Take(&row, job.SourceRecordID).Error
		if err == nil {
			switch row.Status {
			case ZTAPISettlementSettled, ZTAPISettlementReleased:
				return "alert_source_resolved", nil
			case ZTAPISettlementPending:
				raw = row.MissingDimensionsJSON
			case ZTAPISettlementReserved:
				if row.CreatedAt.After(now.Add(-ZTAPIFinanceStaleReservationAge)) {
					return "alert_source_not_due", ErrZTAPIFinanceAlertInvalid
				}
				raw = `["stale_reserved_hold"]`
			default:
				return "alert_source_state_unknown", ErrZTAPIFinanceAlertInvalid
			}
		}
	case "refund":
		var row ZTAPISupplierRefund
		err = query.Select("id", "status", "pending_reason").Take(&row, job.SourceRecordID).Error
		if err == nil {
			switch row.Status {
			case "applied", "completed":
				return "alert_source_resolved", nil
			case "pending":
				raw = row.PendingReason
			default:
				return "alert_source_state_unknown", ErrZTAPIFinanceAlertInvalid
			}
		}
	case "attempt_review":
		var row ZTAPIAttemptBillingReview
		err = query.Select("id", "status", "pending_reason").Take(&row, job.SourceRecordID).Error
		if err == nil {
			switch row.Status {
			case "verified_billed", "verified_nocharge":
				return "alert_source_resolved", nil
			case "pending":
				raw = row.PendingReason
			default:
				return "alert_source_state_unknown", ErrZTAPIFinanceAlertInvalid
			}
		}
	default:
		return "alert_source_kind_unknown", ErrZTAPIFinanceAlertInvalid
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "alert_source_missing", err
	}
	if err != nil {
		return "alert_source_lookup_failed", err
	}
	if ztapiFinanceSourceReason(job.SourceKind, raw) != job.Reason {
		// A reason change is not proof of resolution. Keeping this unsent row
		// retryable also preserves lifetime dedup if the original reason recurs.
		return "alert_source_reason_changed", ErrZTAPIFinanceAlertInvalid
	}
	return "", nil
}

func ztapiFinanceSourceReason(kind, raw string) string {
	if kind == "settlement" {
		var codes []string
		if len(raw) > 16384 || common.UnmarshalJsonStr(raw, &codes) != nil || len(codes) > 64 {
			raw = "unclassified_pending_reason"
		} else {
			raw = strings.Join(codes, ",")
		}
	}
	return ZTAPIFinanceAlertSafeReason(raw)
}

func FinishZTAPIFinanceAlert(ctx context.Context, db *gorm.DB, job ZTAPIFinanceAlertOutbox, now time.Time, ok bool, reason, receipt string) error {
	if db == nil || ctx == nil || job.ID == 0 || job.LeaseToken == "" {
		return ErrZTAPIFinanceAlertInvalid
	}
	updates := map[string]any{"lease_token": "", "lease_until": 0}
	if ok {
		safe, err := ztapiHealthDeliveryReceipt(receipt)
		if err != nil || safe == "" {
			return ErrZTAPIFinanceAlertInvalid
		}
		updates["status"], updates["last_reason"], updates["receipt_json"], updates["sent_at"] = "sent", "telegram_accepted", safe, now.Unix()
	} else {
		switch reason {
		case "alert_recipient_missing_or_invalid", "alert_payload_error", "alert_request_invalid", "alert_transport_error", "alert_http_status", "alert_response_invalid", "alert_telegram_rejected":
		default:
			reason = "alert_delivery_error"
		}
		backoff := min(int64(3600), int64(60)<<min(max(job.Attempts-1, 0), 6))
		updates["status"], updates["last_reason"], updates["receipt_json"], updates["next_attempt_at"] = "pending", reason, "", now.Unix()+backoff
	}
	updated := db.WithContext(ctx).Model(&ZTAPIFinanceAlertOutbox{}).Where("id = ? AND status = ? AND lease_token = ?", job.ID, "sending", job.LeaseToken).Updates(updates)
	if updated.Error != nil {
		return updated.Error
	}
	if updated.RowsAffected != 1 {
		return ErrZTAPIFinanceAlertLeaseLost
	}
	return nil
}

// Accept known internal codes only; arbitrary error strings may contain URLs,
// credentials, provider bodies or customer data even if they look like IDs.
func ZTAPIFinanceAlertSafeReason(raw string) string {
	known := map[string]bool{}
	for _, name := range strings.Fields(`input_tokens output_tokens total_tokens cache_read cache_write cache_write_5m cache_write_1h image_input_tokens image_output_tokens audio_input_tokens audio_output_tokens web_search web_search_preview file_search
image_settlement_evidence_missing image_settlement_evidence_conflict image_usage_untrusted
usage_semantic usage_zero_unconfirmed upstream_usage_missing upstream_billing_unconfirmed invalid_frozen_price charge_calculation_invalid dispatch_evidence_missing attempt_evidence_conflict settlement_retry_required stale_reserved_hold
upstream_attempt_billing_unconfirmed unsupported_parent_scope missing_distinct_usage_evidence billing_dimensions_pending billing_application_pending billing_log_unavailable overlapping_billed_usage existing_finalization_intent
awaiting_approval evidence_conflict approval_conflict original_request_missing original_charge_unsettled original_owner_missing original_owner_deleted billed_attempt_missing evidence_invalid original_lineage_mismatch original_charge_mismatch charge_dimensions_invalid reversal_dimensions_ambiguous cumulative_refund_cap token_charge_mismatch unclassified_pending_reason`) {
		known[name] = true
	}
	set := map[string]bool{}
	if len(raw) > 16384 {
		raw = "unclassified_pending_reason"
	}
	for _, code := range strings.Split(raw, ",") {
		code = strings.TrimSpace(code)
		if !known[code] {
			code = "unclassified_pending_reason"
		}
		set[code] = true
	}
	codes := make([]string, 0, len(set))
	for code := range set {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	return strings.Join(codes, ",")
}
