package model

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const BalanceLedgerSourceSupplierRefund = "supplier_refund"

var (
	ErrZTAPISupplierRefundPending      = errors.New("supplier refund is pending reconciliation")
	ErrZTAPISupplierRefundConflict     = errors.New("supplier refund identity conflicts with durable evidence")
	ErrZTAPISupplierRefundUnauthorized = errors.New("supplier refund approval requires enabled finance.write authority")
)

// Each independently billed attempt owns an immutable charge anchor.
type ZTAPISupplierRefundCharge struct {
	ID                 uint   `gorm:"primaryKey"`
	RequestID          string `gorm:"type:varchar(128);not null;uniqueIndex:ztapi_refund_charge_attempt,priority:1"`
	SettlementID       uint   `gorm:"not null;index:ztapi_refund_charge_settlement"`
	UserID             int    `gorm:"not null"`
	TokenID            int    `gorm:"not null"`
	Attempt            int    `gorm:"not null;uniqueIndex:ztapi_refund_charge_attempt,priority:2"`
	BillingProofID     uint   `gorm:"not null;default:0;index"`
	BillingOperationID string `gorm:"type:varchar(64);not null;default:''"`
	ChannelID          int    `gorm:"not null"`
	CredentialVersion  string `gorm:"type:varchar(128);not null"`
	UpstreamRequestID  string `gorm:"type:varchar(256);not null"`
	UpstreamTaskID     string `gorm:"type:varchar(256);not null"`
	UpstreamBillID     string `gorm:"type:varchar(256);not null"`
	PriceSnapshotJSON  string `gorm:"type:text;not null"`
	DimensionsJSON     string `gorm:"type:text;not null"`
	OriginalLedgerID   int    `gorm:"not null"`
	ChargedQuota       int64  `gorm:"type:bigint;not null"`
	TokenChargedQuota  int64  `gorm:"type:bigint;not null"`
	RefundedQuota      int64  `gorm:"type:bigint;not null"`
	TokenRefundedQuota int64  `gorm:"type:bigint;not null"`
	ProgressJSON       string `gorm:"type:text;not null"`
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

func (ZTAPISupplierRefundCharge) TableName() string { return "ztapi_supplier_refund_charges" }

// Submission contains assertions only. There is deliberately no Trusted,
// Approved, customer refund amount, or caller-supplied price field.
type ZTAPISupplierRefundSubmission struct {
	Source            string                     `json:"source"`
	ProofID           string                     `json:"proof_id"`
	RequestID         string                     `json:"request_id"`
	UserID            int                        `json:"user_id"`
	Attempt           int                        `json:"attempt"`
	ChannelID         int                        `json:"channel_id"`
	CredentialVersion string                     `json:"credential_version"`
	UpstreamRequestID string                     `json:"upstream_request_id"`
	UpstreamTaskID    string                     `json:"upstream_task_id"`
	UpstreamBillID    string                     `json:"upstream_bill_id"`
	Mode              string                     `json:"mode"`
	Units             []ZTAPISupplierRefundUnits `json:"units"`
	EvidenceReference string                     `json:"evidence_reference"`
}

type ZTAPISupplierRefund struct {
	ID                 uint   `gorm:"primaryKey"`
	ProofKey           string `gorm:"type:char(64);not null;uniqueIndex"`
	Source             string `gorm:"type:varchar(128);not null"`
	ProofID            string `gorm:"type:varchar(256);not null"`
	RequestID          string `gorm:"type:varchar(128);not null;index"`
	SubmissionJSON     string `gorm:"type:text;not null"`
	PayloadHash        string `gorm:"type:char(64);not null"`
	Status             string `gorm:"type:varchar(16);not null;index"`
	PendingReason      string `gorm:"type:varchar(64);not null"`
	ChargeID           uint   `gorm:"not null;index"`
	LedgerID           int    `gorm:"not null"`
	RefundedQuota      int64  `gorm:"type:bigint;not null"`
	TokenRefundedQuota int64  `gorm:"type:bigint;not null"`
	LastAttemptAt      int64  `gorm:"type:bigint;not null;default:0;index"`
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

func (ZTAPISupplierRefund) TableName() string { return "ztapi_supplier_refunds" }

type ZTAPISupplierRefundApproval struct {
	ID                    uint   `gorm:"primaryKey"`
	RefundID              uint   `gorm:"not null;uniqueIndex"`
	OperatorID            int    `gorm:"not null"`
	PayloadHash           string `gorm:"type:char(64);not null"`
	VerificationReference string `gorm:"type:varchar(512);not null"`
	CreatedAt             time.Time
}

func (ZTAPISupplierRefundApproval) TableName() string { return "ztapi_supplier_refund_approvals" }
func (*ZTAPISupplierRefundApproval) BeforeUpdate(*gorm.DB) error {
	return ErrZTAPISupplierRefundConflict
}
func (*ZTAPISupplierRefundApproval) BeforeDelete(*gorm.DB) error {
	return ErrZTAPISupplierRefundConflict
}

// Parent owns calling this migration; no implicit startup or global DB writes.
func MigrateZTAPISupplierRefund(db *gorm.DB) error {
	if err := db.AutoMigrate(&ZTAPISupplierRefundCharge{}, &ZTAPISupplierRefund{}, &ZTAPISupplierRefundApproval{}, &ZTAPIAttemptBillingReview{}, &ZTAPIAttemptBillingProof{}, &ZTAPIAttemptBillingApproval{}); err != nil {
		return err
	}
	return migrateZTAPISupplierRefundChargeIndexes(db, (ZTAPISupplierRefundCharge{}).TableName())
}

func migrateZTAPISupplierRefundChargeIndexes(db *gorm.DB, table string) error {
	// AutoMigrate cannot remove obsolete single-column uniqueness. Composite
	// uniqueness is installed first, and historical financial rows stay intact.
	for _, column := range []string{"request_id", "settlement_id"} {
		name := db.NamingStrategy.IndexName(table, column)
		if db.Migrator().HasIndex(table, name) {
			if err := db.Migrator().DropIndex(table, name); err != nil {
				return err
			}
		}
	}
	return nil
}

func ztapiSupplierRefundHash(value string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(value)))
}

// RecordZTAPISupplierRefundChargeTx must run inside the final settlement
// transaction, after the settled request and original ledger have been saved.
func RecordZTAPISupplierRefundChargeTx(tx *gorm.DB, input ZTAPISupplierRefundCharge) (*ZTAPISupplierRefundCharge, error) {
	if tx == nil || strings.TrimSpace(input.RequestID) == "" || len(input.RequestID) > 128 || input.Attempt < 1 || input.Attempt > 2 || input.ChannelID <= 0 || strings.TrimSpace(input.CredentialVersion) == "" || len(input.CredentialVersion) > 128 ||
		len(input.UpstreamRequestID) > 256 || len(input.UpstreamTaskID) > 256 || len(input.UpstreamBillID) > 256 || strings.TrimSpace(input.UpstreamRequestID+input.UpstreamTaskID+input.UpstreamBillID) == "" {
		return nil, ErrZTAPISupplierRefundEvidence
	}
	if _, ok := tx.Statement.ConnPool.(gorm.TxCommitter); !ok {
		return nil, gorm.ErrInvalidTransaction
	}
	var request ZTAPIRequestSettlement
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("request_id = ?", input.RequestID).Take(&request).Error; err != nil {
		return nil, err
	}
	if request.RequestID != input.RequestID || request.Status != ZTAPISettlementSettled {
		return nil, ErrZTAPISupplierRefundPending
	}
	if request.FinalAttempt > 0 && request.FinalAttempt != input.Attempt {
		return nil, ErrZTAPISupplierRefundConflict
	}
	var otherFinal int64
	if err := tx.Model(&ZTAPISupplierRefundCharge{}).Where("request_id = ? AND billing_proof_id = 0 AND attempt <> ?", request.RequestID, input.Attempt).Count(&otherFinal).Error; err != nil {
		return nil, err
	}
	if otherFinal != 0 {
		return nil, ErrZTAPISupplierRefundConflict
	}
	var dims []ZTAPISupplierRefundDimension
	if err := common.UnmarshalJsonStr(request.ChargeDimensionsJSON, &dims); err != nil {
		return nil, ErrZTAPISupplierRefundEvidence
	}
	if _, err := planZTAPISupplierRefund(request.ChargedQuota, request.TokenChargedQuota, dims, nil, "full", nil); err != nil {
		return nil, err
	}
	if request.ChargedQuota > 0 {
		var ledger BalanceLedger
		if err := tx.First(&ledger, request.LastLedgerID).Error; err != nil {
			return nil, err
		}
		if ledger.UserID != request.UserID || ledger.RequestID != request.RequestID || (ledger.SourceType != BalanceLedgerSourceUsageReservation && ledger.SourceType != BalanceLedgerSourceUsageSettlement) {
			return nil, ErrZTAPISupplierRefundConflict
		}
	}
	var existing ZTAPISupplierRefundCharge
	if err := tx.Where("request_id = ? AND attempt = ?", input.RequestID, input.Attempt).Take(&existing).Error; err == nil {
		if existing.RequestID != input.RequestID || existing.Attempt != input.Attempt || existing.ChannelID != input.ChannelID || existing.CredentialVersion != input.CredentialVersion || existing.UpstreamRequestID != input.UpstreamRequestID || existing.UpstreamTaskID != input.UpstreamTaskID || existing.UpstreamBillID != input.UpstreamBillID || existing.SettlementID != request.ID || existing.UserID != request.UserID || existing.TokenID != request.TokenID || existing.ChargedQuota != request.ChargedQuota || existing.TokenChargedQuota != request.TokenChargedQuota || existing.PriceSnapshotJSON != request.PriceSnapshotJSON || existing.DimensionsJSON != request.ChargeDimensionsJSON {
			return nil, ErrZTAPISupplierRefundConflict
		}
		return &existing, nil
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	row := ZTAPISupplierRefundCharge{RequestID: request.RequestID, SettlementID: request.ID, UserID: request.UserID, TokenID: request.TokenID, Attempt: input.Attempt, ChannelID: input.ChannelID, CredentialVersion: input.CredentialVersion, UpstreamRequestID: input.UpstreamRequestID, UpstreamTaskID: input.UpstreamTaskID, UpstreamBillID: input.UpstreamBillID, PriceSnapshotJSON: request.PriceSnapshotJSON, DimensionsJSON: request.ChargeDimensionsJSON, OriginalLedgerID: request.LastLedgerID, ChargedQuota: request.ChargedQuota, TokenChargedQuota: request.TokenChargedQuota, ProgressJSON: "[]"}
	if err := tx.Create(&row).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

func SubmitZTAPISupplierRefund(input ZTAPISupplierRefundSubmission) (*ZTAPISupplierRefund, error) {
	if len(input.Source) > 128 || len(input.ProofID) > 256 || len(input.RequestID) > 128 || len(input.CredentialVersion) > 128 || len(input.UpstreamRequestID) > 256 || len(input.UpstreamTaskID) > 256 || len(input.UpstreamBillID) > 256 || len(input.EvidenceReference) > 512 || len(input.Units) > 128 {
		return nil, ErrZTAPISupplierRefundEvidence
	}
	payload, err := common.Marshal(input)
	if err != nil {
		return nil, err
	}
	if len(payload) > 65536 {
		return nil, ErrZTAPISupplierRefundEvidence
	}
	identity, _ := common.Marshal([]string{input.Source, input.ProofID})
	key := ztapiSupplierRefundHash(string(identity))
	// Missing source/proof identity is retained for review, never trusted. A
	// payload fingerprint deduplicates retries without inventing supplier IDs.
	if strings.TrimSpace(input.Source) == "" || strings.TrimSpace(input.ProofID) == "" {
		key = ztapiSupplierRefundHash("unidentified:" + string(payload))
	}
	row := ZTAPISupplierRefund{ProofKey: key, Source: input.Source, ProofID: input.ProofID, RequestID: input.RequestID, SubmissionJSON: string(payload), PayloadHash: ztapiSupplierRefundHash(string(payload)), Status: "pending", PendingReason: "awaiting_approval"}
	err = ztapiSettlementTransaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "proof_key"}}, DoNothing: true}).Create(&row).Error; err != nil {
			return err
		}
		var stored ZTAPISupplierRefund
		if err := tx.Where("proof_key = ?", key).Take(&stored).Error; err != nil {
			return err
		}
		if stored.SubmissionJSON != string(payload) {
			return ErrZTAPISupplierRefundConflict
		}
		row = stored
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// operatorID must come from authenticated server context, never the submitted
// JSON body. Database role/status checks are defense in depth, not HTTP auth.
func ApproveZTAPISupplierRefund(id uint, operatorID int, verificationReference string) error {
	if id == 0 || operatorID <= 0 || strings.TrimSpace(verificationReference) == "" || len(verificationReference) > 512 {
		return ErrZTAPISupplierRefundUnauthorized
	}
	return ztapiSettlementTransaction(func(tx *gorm.DB) error {
		var operator User
		if err := tx.Where("id = ?", operatorID).Take(&operator).Error; err != nil {
			return ErrZTAPISupplierRefundUnauthorized
		}
		if operator.Status != common.UserStatusEnabled || !common.HasAdminPermission(operator.Role, common.PermissionFinanceWrite) {
			return ErrZTAPISupplierRefundUnauthorized
		}
		var row ZTAPISupplierRefund
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&row, id).Error; err != nil {
			return err
		}
		var input ZTAPISupplierRefundSubmission
		if err := common.UnmarshalJsonStr(row.SubmissionJSON, &input); err != nil {
			return err
		}
		if strings.TrimSpace(input.Source) == "" || strings.TrimSpace(input.ProofID) == "" || strings.TrimSpace(input.EvidenceReference) == "" || row.PayloadHash != ztapiSupplierRefundHash(row.SubmissionJSON) {
			return ErrZTAPISupplierRefundEvidence
		}
		var existing ZTAPISupplierRefundApproval
		if err := tx.Where("refund_id = ?", id).Take(&existing).Error; err == nil {
			if existing.OperatorID != operatorID || existing.VerificationReference != verificationReference || existing.PayloadHash != row.PayloadHash {
				return ErrZTAPISupplierRefundConflict
			}
			return nil
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		return tx.Create(&ZTAPISupplierRefundApproval{RefundID: id, OperatorID: operatorID, PayloadHash: row.PayloadHash, VerificationReference: verificationReference}).Error
	})
}

func ProcessZTAPISupplierRefund(id uint) error {
	var initial ZTAPISupplierRefund
	if err := DB.First(&initial, id).Error; err != nil {
		return err
	}
	var processed ZTAPISupplierRefund
	pending := false
	err := ztapiSettlementTransaction(func(tx *gorm.DB) error {
		var request ZTAPIRequestSettlement
		requestErr := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("request_id = ?", initial.RequestID).Take(&request).Error
		if requestErr != nil && !errors.Is(requestErr, gorm.ErrRecordNotFound) {
			return requestErr
		}
		var user *User
		var token *Token
		var ownerErr error
		if requestErr == nil {
			user, token, ownerErr = ztapiSettlementOwners(tx, &request)
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&processed, id).Error; err != nil {
			return err
		}
		if processed.Status == "applied" || processed.Status == "completed" {
			return nil
		}
		pend := func(reason string) error {
			pending = true
			return tx.Model(&ZTAPISupplierRefund{}).Where("id = ?", id).Updates(map[string]any{"status": "pending", "pending_reason": reason}).Error
		}
		if processed.PayloadHash != ztapiSupplierRefundHash(processed.SubmissionJSON) || processed.RequestID != initial.RequestID {
			return pend("evidence_conflict")
		}
		var approval ZTAPISupplierRefundApproval
		if err := tx.Where("refund_id = ?", id).Take(&approval).Error; errors.Is(err, gorm.ErrRecordNotFound) {
			return pend("awaiting_approval")
		} else if err != nil {
			return err
		}
		if approval.PayloadHash != processed.PayloadHash {
			return pend("approval_conflict")
		}
		if requestErr != nil {
			return pend("original_request_missing")
		}
		if request.Status != ZTAPISettlementSettled {
			return pend("original_charge_unsettled")
		}
		if ownerErr != nil {
			if errors.Is(ownerErr, gorm.ErrRecordNotFound) || errors.Is(ownerErr, ErrZTAPISettlementConflict) {
				return pend("original_owner_missing")
			}
			return ownerErr
		}
		if user.DeletedAt.Valid || token.DeletedAt.Valid {
			return pend("original_owner_deleted")
		}
		var input ZTAPISupplierRefundSubmission
		if err := common.UnmarshalJsonStr(processed.SubmissionJSON, &input); err != nil {
			return pend("evidence_invalid")
		}
		var charge ZTAPISupplierRefundCharge
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("request_id = ? AND attempt = ?", request.RequestID, input.Attempt).Take(&charge).Error; errors.Is(err, gorm.ErrRecordNotFound) {
			return pend("billed_attempt_missing")
		} else if err != nil {
			return err
		}
		if input.UserID != request.UserID || input.RequestID != request.RequestID || input.Attempt != charge.Attempt || input.ChannelID != charge.ChannelID || input.CredentialVersion != charge.CredentialVersion || input.UpstreamRequestID != charge.UpstreamRequestID || input.UpstreamTaskID != charge.UpstreamTaskID || input.UpstreamBillID != charge.UpstreamBillID {
			return pend("original_lineage_mismatch")
		}
		if charge.SettlementID != request.ID || charge.UserID != request.UserID || charge.TokenID != request.TokenID || charge.PriceSnapshotJSON != request.PriceSnapshotJSON {
			return pend("original_charge_mismatch")
		}
		if charge.BillingProofID == 0 {
			if charge.ChargedQuota != request.ChargedQuota || charge.TokenChargedQuota != request.TokenChargedQuota || charge.DimensionsJSON != request.ChargeDimensionsJSON {
				return pend("original_charge_mismatch")
			}
		} else if err := validateZTAPIAdditionalChargeTx(tx, &request, &charge); err != nil {
			return pend("original_charge_mismatch")
		}
		var aggregate struct{ Refunded int64 }
		if err := tx.Model(&ZTAPISupplierRefundCharge{}).Select("COALESCE(SUM(refunded_quota),0) AS refunded").Where("request_id = ?", request.RequestID).Scan(&aggregate).Error; err != nil {
			return err
		}
		if aggregate.Refunded != request.RefundedQuota {
			return pend("original_charge_mismatch")
		}
		var dims []ZTAPISupplierRefundDimension
		var previous []ZTAPISupplierRefundProgress
		if common.UnmarshalJsonStr(charge.DimensionsJSON, &dims) != nil || common.UnmarshalJsonStr(charge.ProgressJSON, &previous) != nil {
			return pend("charge_dimensions_invalid")
		}
		plan, e := planZTAPISupplierRefund(charge.ChargedQuota, charge.TokenChargedQuota, dims, previous, input.Mode, input.Units)
		if e != nil {
			return pend("reversal_dimensions_ambiguous")
		}
		var priorQuota, priorToken int64
		for _, p := range previous {
			priorQuota += p.Quota
			priorToken += p.TokenQuota
		}
		if priorQuota != charge.RefundedQuota || priorToken != charge.TokenRefundedQuota || plan.Quota > charge.ChargedQuota-charge.RefundedQuota || plan.TokenQuota > charge.TokenChargedQuota-charge.TokenRefundedQuota {
			return pend("cumulative_refund_cap")
		}
		if request.TokenUnlimited && charge.TokenChargedQuota != 0 {
			return pend("token_charge_mismatch")
		}
		ledger, e := ztapiSettlementWalletDeltaTx(tx, user, plan.Quota, request.RequestID, "supplier-refund:"+processed.ProofKey, "approved supplier usage reversal", BalanceLedgerSourceSupplierRefund, false)
		if e != nil {
			return e
		}
		if plan.TokenQuota > 0 {
			applied, e := ztapiSettlementTokenDeltaTx(tx, token, plan.TokenQuota, false)
			if e != nil {
				return e
			}
			if applied != plan.TokenQuota {
				return ErrZTAPISupplierRefundConflict
			}
		}
		progress, e := common.Marshal(plan.Progress)
		if e != nil {
			return e
		}
		if e = tx.Model(&ZTAPISupplierRefundCharge{}).Where("id = ?", charge.ID).Updates(map[string]any{"progress_json": string(progress), "refunded_quota": charge.RefundedQuota + plan.Quota, "token_refunded_quota": charge.TokenRefundedQuota + plan.TokenQuota}).Error; e != nil {
			return e
		}
		if e = tx.Model(&ZTAPIRequestSettlement{}).Where("id = ?", request.ID).Updates(map[string]any{"refunded_quota": request.RefundedQuota + plan.Quota, "cache_sync_pending": true}).Error; e != nil {
			return e
		}
		ledgerID := 0
		if ledger != nil {
			ledgerID = ledger.ID
		}
		processed.Status = "applied"
		processed.ChargeID = charge.ID
		processed.RefundedQuota = plan.Quota
		processed.TokenRefundedQuota = plan.TokenQuota
		processed.LedgerID = ledgerID
		return tx.Model(&ZTAPISupplierRefund{}).Where("id = ?", id).Updates(map[string]any{"status": "applied", "pending_reason": "", "charge_id": charge.ID, "refunded_quota": plan.Quota, "token_refunded_quota": plan.TokenQuota, "ledger_id": ledgerID}).Error
	})
	if err != nil {
		return err
	}
	if pending {
		return ErrZTAPISupplierRefundPending
	}
	if processed.Status == "completed" {
		return nil
	}
	// Applied is durable before cache invalidation. A retry only refreshes caches.
	var request ZTAPIRequestSettlement
	if err := DB.Where("request_id = ?", processed.RequestID).Take(&request).Error; err != nil {
		return err
	}
	syncZTAPISettlementCaches(&request)
	if request.CacheSyncPending {
		return ErrBalanceLedgerCacheSync
	}
	return ztapiSettlementTransaction(func(tx *gorm.DB) error {
		return tx.Model(&ZTAPISupplierRefund{}).Where("id = ? AND status = ?", id, "applied").Update("status", "completed").Error
	})
}

// List supplies the durable manual-review queue. No raw provider body or
// customer prompt is accepted by this module; references point to safe evidence.
func ListPendingZTAPISupplierRefunds(afterID uint, limit int) ([]ZTAPISupplierRefund, error) {
	return ListZTAPISupplierRefunds("pending", afterID, limit)
}

func ListZTAPISupplierRefunds(status string, afterID uint, limit int) ([]ZTAPISupplierRefund, error) {
	if limit < 1 || limit > 1000 {
		return nil, ErrZTAPISupplierRefundEvidence
	}
	if status != "pending" && status != "applied" && status != "completed" && status != "all" {
		return nil, ErrZTAPISupplierRefundEvidence
	}
	var rows []ZTAPISupplierRefund
	query := DB.Where("id > ?", afterID)
	if status != "all" {
		query = query.Where("status = ?", status)
	}
	err := query.Order("id ASC").Limit(limit).Find(&rows).Error
	return rows, err
}

func RetryZTAPISupplierRefunds(afterID uint, limit int) (int, error) {
	if limit < 1 || limit > 1000 {
		return 0, ErrZTAPISupplierRefundEvidence
	}
	var rows []ZTAPISupplierRefund
	if err := DB.Where("id > ? AND status IN ?", afterID, []string{"pending", "applied"}).Order("last_attempt_at ASC, id ASC").Limit(limit).Find(&rows).Error; err != nil {
		return 0, err
	}
	done := 0
	var firstErr error
	for _, row := range rows {
		// Scheduling survives financial rollback so a permanently ambiguous or
		// overflowing head item cannot starve later eligible refunds.
		if err := ztapiSettlementTransaction(func(tx *gorm.DB) error {
			return tx.Model(&ZTAPISupplierRefund{}).Where("id = ? AND status IN ?", row.ID, []string{"pending", "applied"}).UpdateColumn("last_attempt_at", time.Now().UnixNano()).Error
		}); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if err := ProcessZTAPISupplierRefund(row.ID); err != nil {
			if !errors.Is(err, ErrZTAPISupplierRefundPending) && firstErr == nil {
				firstErr = err
			}
		} else {
			done++
		}
	}
	return done, firstErr
}
