package model

import (
	"errors"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	ZTAPISupplierReconciliationPreviewed = "previewed"
	ZTAPISupplierReconciliationApplied   = "applied"

	ZTAPISupplierReconciliationActionUnmatched        = "unmatched"
	ZTAPISupplierReconciliationActionAttemptBilling   = "attempt_billing"
	ZTAPISupplierReconciliationActionExistingCharge   = "existing_charge"
	ZTAPISupplierReconciliationActionSupplierRefund   = "supplier_refund"
	ZTAPISupplierReconciliationActionNoChargeEvidence = "nocharge_evidence"
)

var (
	ErrZTAPISupplierReconciliationInvalid      = errors.New("invalid supplier reconciliation evidence")
	ErrZTAPISupplierReconciliationConflict     = errors.New("supplier reconciliation identity conflicts")
	ErrZTAPISupplierReconciliationOrphanRefund = errors.New("supplier reversal has no original debit")
	ErrZTAPISupplierReconciliationOverRefund   = errors.New("supplier reversals exceed original debit")
	ErrZTAPISupplierReconciliationLineage      = errors.New("supplier reversal lineage conflicts with original debit")

	ztapiSupplierSHA256Pattern   = regexp.MustCompile(`^[0-9a-f]{64}$`)
	ztapiSupplierCodePattern     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
	ztapiSupplierCurrencyPattern = regexp.MustCompile(`^[A-Z][A-Z0-9]{2,7}$`)
)

// ZTAPISupplierLedgerRecord is the transport-independent supplier statement
// contract. Raw provider bodies and credentials are deliberately absent.
type ZTAPISupplierLedgerRecord struct {
	SupplierRecordID string `json:"supplier_record_id" csv:"supplier_record_id"`
	RequestID        string `json:"request_id" csv:"request_id"`
	TaskID           string `json:"task_id" csv:"task_id"`
	CredentialRef    string `json:"credential_ref" csv:"credential_ref"`
	ProviderModel    string `json:"provider_model" csv:"provider_model"`
	ResourceType     string `json:"resource_type" csv:"resource_type"`
	OccurredAt       int64  `json:"occurred_at" csv:"occurred_at"`
	Billable         *bool  `json:"billable" csv:"billable"`
	DimensionsJSON   string `json:"dimensions_json" csv:"dimensions_json"`
	DebitAmount      string `json:"debit_amount" csv:"debit_amount"`
	Currency         string `json:"currency" csv:"currency"`
	ReversalID       string `json:"reversal_id" csv:"reversal_id"`
	OriginalRecordID string `json:"original_record_id" csv:"original_record_id"`
	ReversalAmount   string `json:"reversal_amount" csv:"reversal_amount"`
	RawEvidenceHash  string `json:"raw_evidence_hash" csv:"raw_evidence_hash"`
}

type ZTAPISupplierLedgerDimensions struct {
	UsageSemantic string                        `json:"usage_semantic,omitempty"`
	Usage         []ZTAPIAttemptBillingQuantity `json:"usage,omitempty"`
	Metering      []ZTAPISupplierRefundUnits    `json:"metering,omitempty"`
	RefundMode    string                        `json:"refund_mode,omitempty"`
	RefundUnits   []ZTAPISupplierRefundUnits    `json:"refund_units,omitempty"`
}

type ZTAPISupplierReconciliationImport struct {
	Supplier       string
	IdempotencyKey string
	FileChecksum   string
	PayloadHash    string
	Format         string
	OperatorID     int
	DryRun         bool
	Records        []ZTAPISupplierLedgerRecord
}

type ZTAPISupplierReconciliationBatch struct {
	ID             uint      `gorm:"primaryKey" json:"id"`
	Supplier       string    `gorm:"type:varchar(128);not null;uniqueIndex:ztapi_supplier_import_identity,priority:1" json:"supplier"`
	IdempotencyKey string    `gorm:"type:varchar(128);not null;uniqueIndex:ztapi_supplier_import_identity,priority:2" json:"idempotency_key"`
	FileChecksum   string    `gorm:"type:char(64);not null" json:"file_checksum"`
	PayloadHash    string    `gorm:"type:char(64);not null" json:"payload_hash"`
	Format         string    `gorm:"type:varchar(8);not null" json:"format"`
	OperatorID     int       `gorm:"not null" json:"operator_id"`
	Status         string    `gorm:"type:varchar(16);not null;index" json:"status"`
	RowCount       int       `gorm:"not null" json:"row_count"`
	MatchedCount   int       `gorm:"not null" json:"matched_count"`
	UnmatchedCount int       `gorm:"not null" json:"unmatched_count"`
	ReversalCount  int       `gorm:"not null" json:"reversal_count"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

func (ZTAPISupplierReconciliationBatch) TableName() string {
	return "ztapi_supplier_reconciliation_batches"
}

// Entry is immutable canonical evidence plus the lineage resolved at apply.
// Processing state lives separately so retries never rewrite supplier facts.
type ZTAPISupplierReconciliationEntry struct {
	ID               uint      `gorm:"primaryKey" json:"id"`
	BatchID          uint      `gorm:"not null;index" json:"batch_id"`
	Supplier         string    `gorm:"type:varchar(128);not null;uniqueIndex:ztapi_supplier_record_identity,priority:1" json:"supplier"`
	SupplierRecordID string    `gorm:"type:varchar(128);not null;uniqueIndex:ztapi_supplier_record_identity,priority:2" json:"supplier_record_id"`
	PayloadHash      string    `gorm:"type:char(64);not null" json:"payload_hash"`
	RecordJSON       string    `gorm:"type:text;not null" json:"record"`
	OriginalRecordID string    `gorm:"type:varchar(128);not null;index" json:"original_record_id"`
	ReversalAmount   string    `gorm:"type:varchar(64);not null" json:"reversal_amount"`
	SettlementID     uint      `gorm:"not null;index" json:"settlement_id"`
	UserID           int       `gorm:"not null;index" json:"user_id"`
	Attempt          int       `gorm:"not null" json:"attempt"`
	ChannelID        int       `gorm:"not null" json:"channel_id"`
	ChargeID         uint      `gorm:"not null" json:"charge_id"`
	ActionKind       string    `gorm:"type:varchar(32);not null;index" json:"action_kind"`
	MatchReason      string    `gorm:"type:varchar(64);not null" json:"match_reason"`
	CreatedAt        time.Time `json:"created_at"`
}

func (ZTAPISupplierReconciliationEntry) TableName() string {
	return "ztapi_supplier_reconciliation_entries"
}

func (*ZTAPISupplierReconciliationEntry) BeforeUpdate(*gorm.DB) error {
	return ErrZTAPISupplierReconciliationConflict
}

func (*ZTAPISupplierReconciliationEntry) BeforeDelete(*gorm.DB) error {
	return ErrZTAPISupplierReconciliationConflict
}

type ZTAPISupplierReconciliationAction struct {
	ID             uint      `gorm:"primaryKey" json:"id"`
	EntryID        uint      `gorm:"not null;uniqueIndex" json:"entry_id"`
	Kind           string    `gorm:"type:varchar(32);not null" json:"kind"`
	Status         string    `gorm:"type:varchar(24);not null;index" json:"status"`
	BillingProofID uint      `gorm:"not null" json:"billing_proof_id"`
	RefundProofID  uint      `gorm:"not null" json:"refund_proof_id"`
	LastErrorCode  string    `gorm:"type:varchar(64);not null" json:"last_error_code"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

func (ZTAPISupplierReconciliationAction) TableName() string {
	return "ztapi_supplier_reconciliation_actions"
}

type ZTAPISupplierReconciliationBatchEntry struct {
	ID      uint `gorm:"primaryKey"`
	BatchID uint `gorm:"not null;uniqueIndex:ztapi_supplier_batch_entry,priority:1;index"`
	EntryID uint `gorm:"not null;uniqueIndex:ztapi_supplier_batch_entry,priority:2;index"`
}

func (ZTAPISupplierReconciliationBatchEntry) TableName() string {
	return "ztapi_supplier_reconciliation_batch_entries"
}

type ZTAPISupplierReconciliationResult struct {
	Batch     ZTAPISupplierReconciliationBatch    `json:"batch"`
	Entries   []ZTAPISupplierReconciliationEntry  `json:"entries"`
	Actions   []ZTAPISupplierReconciliationAction `json:"actions"`
	Matched   int                                 `json:"matched"`
	Unmatched int                                 `json:"unmatched"`
	Reversals int                                 `json:"reversals"`
}

func MigrateZTAPISupplierReconciliation(db *gorm.DB) error {
	if db == nil {
		return ErrZTAPISupplierReconciliationInvalid
	}
	return db.AutoMigrate(&ZTAPISupplierReconciliationBatch{}, &ZTAPISupplierReconciliationEntry{}, &ZTAPISupplierReconciliationAction{}, &ZTAPISupplierReconciliationBatchEntry{})
}

func ParseZTAPISupplierLedgerDimensions(raw string) (ZTAPISupplierLedgerDimensions, error) {
	var dimensions ZTAPISupplierLedgerDimensions
	if strings.TrimSpace(raw) == "" || len(raw) > 65536 || common.RejectDuplicateJsonObjectMembers(strings.NewReader(raw)) != nil ||
		common.DecodeJsonStrict(strings.NewReader(raw), &dimensions) != nil {
		return dimensions, ErrZTAPISupplierReconciliationInvalid
	}
	if dimensions.UsageSemantic != "" && dimensions.UsageSemantic != "openai" && dimensions.UsageSemantic != "anthropic" {
		return dimensions, ErrZTAPISupplierReconciliationInvalid
	}
	if len(dimensions.Usage) > 0 && dimensions.UsageSemantic == "" {
		return dimensions, ErrZTAPISupplierReconciliationInvalid
	}
	seen := make(map[string]bool, len(dimensions.Usage))
	for _, quantity := range dimensions.Usage {
		switch quantity.Dimension {
		case "input_tokens", "output_tokens", "cache_read", "cache_write", "cache_write_5m", "cache_write_1h":
		default:
			return dimensions, ErrZTAPISupplierReconciliationInvalid
		}
		if seen[quantity.Dimension] || quantity.Quantity <= 0 || quantity.Quantity > maxBalanceLedgerQuota {
			return dimensions, ErrZTAPISupplierReconciliationInvalid
		}
		seen[quantity.Dimension] = true
	}
	if dimensions.RefundMode != "" && dimensions.RefundMode != "full" && dimensions.RefundMode != "partial" {
		return dimensions, ErrZTAPISupplierReconciliationInvalid
	}
	if len(dimensions.Usage) > 6 || len(dimensions.Metering) > 128 || len(dimensions.RefundUnits) > 128 || (len(dimensions.Usage) > 0 && len(dimensions.Metering) > 0) {
		return dimensions, ErrZTAPISupplierReconciliationInvalid
	}
	validateUnits := func(units []ZTAPISupplierRefundUnits) bool {
		seen := make(map[string]bool, len(units))
		for _, unit := range units {
			value, err := decimal.NewFromString(unit.Units)
			if unit.Dimension == "" || len(unit.Dimension) > 128 || seen[unit.Dimension] || err != nil || !value.IsPositive() || value.String() != unit.Units {
				return false
			}
			seen[unit.Dimension] = true
		}
		return true
	}
	if !validateUnits(dimensions.Metering) || !validateUnits(dimensions.RefundUnits) {
		return dimensions, ErrZTAPISupplierReconciliationInvalid
	}
	canonical, err := common.Marshal(dimensions)
	if err != nil || string(canonical) != raw {
		return dimensions, ErrZTAPISupplierReconciliationInvalid
	}
	return dimensions, nil
}

func normalizeZTAPISupplierLedgerRecord(record ZTAPISupplierLedgerRecord) (ZTAPISupplierLedgerRecord, string, string, decimal.Decimal, error) {
	record.SupplierRecordID = strings.TrimSpace(record.SupplierRecordID)
	record.RequestID = strings.TrimSpace(record.RequestID)
	record.TaskID = strings.TrimSpace(record.TaskID)
	record.CredentialRef = strings.TrimSpace(record.CredentialRef)
	record.ProviderModel = strings.TrimSpace(record.ProviderModel)
	record.ResourceType = strings.TrimSpace(record.ResourceType)
	record.Currency = strings.ToUpper(strings.TrimSpace(record.Currency))
	record.ReversalID = strings.TrimSpace(record.ReversalID)
	record.OriginalRecordID = strings.TrimSpace(record.OriginalRecordID)
	record.RawEvidenceHash = strings.ToLower(strings.TrimSpace(record.RawEvidenceHash))
	if !ztapiSupplierCodePattern.MatchString(record.SupplierRecordID) || (record.RequestID == "" && record.TaskID == "") ||
		len(record.RequestID) > 200 || len(record.TaskID) > 200 || record.CredentialRef == "" || len(record.CredentialRef) > 128 ||
		record.ProviderModel == "" || len(record.ProviderModel) > 255 || record.ResourceType == "" || len(record.ResourceType) > 64 ||
		record.OccurredAt <= 0 || record.OccurredAt > time.Now().Add(24*time.Hour).Unix() || !ztapiSupplierCurrencyPattern.MatchString(record.Currency) ||
		!ztapiSupplierSHA256Pattern.MatchString(record.RawEvidenceHash) || ztapiSupplierLooksSecret(record.CredentialRef) ||
		strings.ContainsAny(record.RequestID+record.TaskID+record.ProviderModel+record.ResourceType, "\r\n\x00") {
		return record, "", "", decimal.Zero, ErrZTAPISupplierReconciliationInvalid
	}
	dimensions, err := ParseZTAPISupplierLedgerDimensions(record.DimensionsJSON)
	if err != nil {
		return record, "", "", decimal.Zero, err
	}
	isReversal := record.ReversalID != "" || record.OriginalRecordID != "" || record.ReversalAmount != ""
	amountRaw := record.DebitAmount
	if isReversal {
		if !ztapiSupplierCodePattern.MatchString(record.ReversalID) || !ztapiSupplierCodePattern.MatchString(record.OriginalRecordID) || record.DebitAmount != "" || len(dimensions.Usage) != 0 || len(dimensions.Metering) != 0 {
			return record, "", "", decimal.Zero, ErrZTAPISupplierReconciliationInvalid
		}
		amountRaw = record.ReversalAmount
	} else if record.ReversalID != "" || record.OriginalRecordID != "" || record.ReversalAmount != "" || record.Billable == nil {
		return record, "", "", decimal.Zero, ErrZTAPISupplierReconciliationInvalid
	} else if dimensions.RefundMode != "" || len(dimensions.RefundUnits) != 0 || (*record.Billable && len(dimensions.Usage) == 0 && len(dimensions.Metering) == 0) || (!*record.Billable && (len(dimensions.Usage) != 0 || len(dimensions.Metering) != 0)) {
		return record, "", "", decimal.Zero, ErrZTAPISupplierReconciliationInvalid
	}
	amount, err := decimal.NewFromString(amountRaw)
	if err != nil || amount.IsNegative() || len(amountRaw) > 64 || amount.String() != amountRaw || (isReversal && !amount.IsPositive()) || (!isReversal && *record.Billable && !amount.IsPositive()) || (!isReversal && !*record.Billable && !amount.IsZero()) {
		return record, "", "", decimal.Zero, ErrZTAPISupplierReconciliationInvalid
	}
	raw, err := common.Marshal(record)
	if err != nil || len(raw) > 131072 {
		return record, "", "", decimal.Zero, ErrZTAPISupplierReconciliationInvalid
	}
	return record, string(raw), ztapiSupplierRefundHash(string(raw)), amount, nil
}

func ztapiSupplierLooksSecret(value string) bool {
	lower := strings.ToLower(strings.TrimSpace(value))
	return strings.HasPrefix(lower, "sk-") || strings.HasPrefix(lower, "bearer ") || strings.Contains(lower, "api_key=") || strings.Contains(lower, "apikey=") || strings.Contains(lower, "token=")
}

type ztapiSupplierNormalizedRecord struct {
	record     ZTAPISupplierLedgerRecord
	json       string
	hash       string
	amount     decimal.Decimal
	reversal   bool
	dimensions ZTAPISupplierLedgerDimensions
	existing   *ZTAPISupplierReconciliationEntry
}

func normalizeZTAPISupplierRecords(tx *gorm.DB, supplier string, records []ZTAPISupplierLedgerRecord) ([]ztapiSupplierNormalizedRecord, error) {
	if len(records) == 0 || len(records) > 1000 {
		return nil, ErrZTAPISupplierReconciliationInvalid
	}
	normalized := make([]ztapiSupplierNormalizedRecord, 0, len(records))
	byID := make(map[string]ztapiSupplierNormalizedRecord, len(records))
	for _, record := range records {
		n, raw, hash, amount, err := normalizeZTAPISupplierLedgerRecord(record)
		if err != nil {
			return nil, err
		}
		dimensions, _ := ParseZTAPISupplierLedgerDimensions(n.DimensionsJSON)
		item := ztapiSupplierNormalizedRecord{record: n, json: raw, hash: hash, amount: amount, reversal: n.OriginalRecordID != "", dimensions: dimensions}
		if prior, ok := byID[n.SupplierRecordID]; ok {
			if prior.hash != hash {
				return nil, ErrZTAPISupplierReconciliationConflict
			}
			continue
		}
		var existing ZTAPISupplierReconciliationEntry
		if err = tx.Where("supplier = ? AND supplier_record_id = ?", supplier, n.SupplierRecordID).Take(&existing).Error; err == nil {
			if existing.PayloadHash != hash || existing.RecordJSON != raw {
				return nil, ErrZTAPISupplierReconciliationConflict
			}
			item.existing = &existing
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
		byID[n.SupplierRecordID] = item
		normalized = append(normalized, item)
	}
	if err := validateZTAPISupplierReversals(tx, supplier, normalized, byID); err != nil {
		return nil, err
	}
	sort.SliceStable(normalized, func(i, j int) bool {
		return normalized[i].record.SupplierRecordID < normalized[j].record.SupplierRecordID
	})
	return normalized, nil
}

func validateZTAPISupplierReversals(tx *gorm.DB, supplier string, records []ztapiSupplierNormalizedRecord, current map[string]ztapiSupplierNormalizedRecord) error {
	totals := map[string]decimal.Decimal{}
	for _, item := range records {
		if !item.reversal {
			continue
		}
		original, ok := current[item.record.OriginalRecordID]
		if !ok {
			var entry ZTAPISupplierReconciliationEntry
			if err := tx.Where("supplier = ? AND supplier_record_id = ?", supplier, item.record.OriginalRecordID).Take(&entry).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return ErrZTAPISupplierReconciliationOrphanRefund
				}
				return err
			}
			var record ZTAPISupplierLedgerRecord
			if common.UnmarshalJsonStr(entry.RecordJSON, &record) != nil {
				return ErrZTAPISupplierReconciliationConflict
			}
			n, raw, hash, amount, err := normalizeZTAPISupplierLedgerRecord(record)
			if err != nil || raw != entry.RecordJSON || hash != entry.PayloadHash || n.OriginalRecordID != "" {
				return ErrZTAPISupplierReconciliationConflict
			}
			dimensions, _ := ParseZTAPISupplierLedgerDimensions(n.DimensionsJSON)
			original = ztapiSupplierNormalizedRecord{record: n, json: raw, hash: hash, amount: amount, dimensions: dimensions}
		}
		if original.reversal {
			return ErrZTAPISupplierReconciliationOrphanRefund
		}
		if item.record.RequestID != original.record.RequestID || item.record.TaskID != original.record.TaskID || item.record.CredentialRef != original.record.CredentialRef || item.record.ProviderModel != original.record.ProviderModel || item.record.ResourceType != original.record.ResourceType || item.record.Currency != original.record.Currency {
			return ErrZTAPISupplierReconciliationLineage
		}
		if item.dimensions.RefundMode == "full" && !item.amount.Equal(original.amount) {
			return ErrZTAPISupplierReconciliationInvalid
		}
		if item.dimensions.RefundMode == "partial" && len(item.dimensions.RefundUnits) == 0 {
			return ErrZTAPISupplierReconciliationInvalid
		}
		if item.dimensions.RefundMode == "" || len(item.dimensions.Usage) != 0 {
			return ErrZTAPISupplierReconciliationInvalid
		}
		total := totals[item.record.OriginalRecordID].Add(item.amount)
		var existing []ZTAPISupplierReconciliationEntry
		if err := tx.Where("supplier = ? AND original_record_id = ? AND supplier_record_id <> ?", supplier, item.record.OriginalRecordID, item.record.SupplierRecordID).Find(&existing).Error; err != nil {
			return err
		}
		for _, prior := range existing {
			amount, err := decimal.NewFromString(prior.ReversalAmount)
			if err != nil || amount.IsNegative() {
				return ErrZTAPISupplierReconciliationConflict
			}
			total = total.Add(amount)
		}
		if total.GreaterThan(original.amount) {
			return ErrZTAPISupplierReconciliationOverRefund
		}
		totals[item.record.OriginalRecordID] = totals[item.record.OriginalRecordID].Add(item.amount)
	}
	return nil
}

func validateZTAPISupplierImport(input ZTAPISupplierReconciliationImport) error {
	if !ztapiSupplierCodePattern.MatchString(input.Supplier) || !ztapiSupplierCodePattern.MatchString(input.IdempotencyKey) ||
		!ztapiSupplierSHA256Pattern.MatchString(input.FileChecksum) || !ztapiSupplierSHA256Pattern.MatchString(input.PayloadHash) ||
		(input.Format != "json" && input.Format != "csv") || input.OperatorID <= 0 || len(input.Records) == 0 || len(input.Records) > 1000 ||
		ztapiSupplierLooksSecret(input.Supplier) || ztapiSupplierLooksSecret(input.IdempotencyKey) {
		return ErrZTAPISupplierReconciliationInvalid
	}
	return nil
}

func supplierRecordSourceModel(priceJSON string) string {
	var values map[string]any
	if common.UnmarshalJsonStr(priceJSON, &values) != nil {
		return ""
	}
	for _, key := range []string{"source_model", "SourceModel"} {
		if value, ok := values[key].(string); ok {
			return value
		}
	}
	return ""
}

func matchZTAPISupplierRecord(tx *gorm.DB, supplier string, item ztapiSupplierNormalizedRecord) (ZTAPISupplierReconciliationEntry, error) {
	if item.existing != nil {
		return *item.existing, nil
	}
	entry := ZTAPISupplierReconciliationEntry{Supplier: supplier, SupplierRecordID: item.record.SupplierRecordID, PayloadHash: item.hash, RecordJSON: item.json, OriginalRecordID: item.record.OriginalRecordID, ReversalAmount: item.record.ReversalAmount, ActionKind: ZTAPISupplierReconciliationActionUnmatched, MatchReason: "lineage_not_found"}
	type candidate struct {
		settlement ZTAPIRequestSettlement
		attempt    ZTAPIRequestAttempt
	}
	candidates := map[uint]candidate{}
	if item.record.RequestID != "" {
		var attempts []ZTAPIRequestAttempt
		if err := tx.Where("upstream_request_id = ? AND credential_version = ?", item.record.RequestID, item.record.CredentialRef).Find(&attempts).Error; err != nil {
			return entry, err
		}
		for _, attempt := range attempts {
			var settlement ZTAPIRequestSettlement
			if err := tx.First(&settlement, attempt.SettlementID).Error; err != nil {
				return entry, err
			}
			candidates[attempt.ID] = candidate{settlement: settlement, attempt: attempt}
		}
	}
	if item.record.TaskID != "" {
		var tasks []ZTAPIMediaTask
		if err := tx.Where("upstream_task_id = ? AND credential_version = ?", item.record.TaskID, item.record.CredentialRef).Find(&tasks).Error; err != nil {
			return entry, err
		}
		taskCandidates := map[uint]candidate{}
		for _, task := range tasks {
			var attempt ZTAPIRequestAttempt
			if err := tx.Where("settlement_id = ? AND attempt = ? AND channel_id = ? AND credential_version = ?", task.SettlementID, task.Attempt, task.ChannelID, task.CredentialVersion).Take(&attempt).Error; err != nil {
				return entry, err
			}
			var settlement ZTAPIRequestSettlement
			if err := tx.First(&settlement, task.SettlementID).Error; err != nil {
				return entry, err
			}
			taskCandidates[attempt.ID] = candidate{settlement: settlement, attempt: attempt}
		}
		if item.record.RequestID == "" {
			candidates = taskCandidates
		} else {
			for id := range candidates {
				if _, ok := taskCandidates[id]; !ok {
					delete(candidates, id)
				}
			}
		}
	}
	if len(candidates) != 1 {
		if len(candidates) > 1 {
			entry.MatchReason = "lineage_ambiguous"
		}
		return entry, nil
	}
	var matched candidate
	for _, value := range candidates {
		matched = value
	}
	if sourceModel := supplierRecordSourceModel(matched.settlement.PriceSnapshotJSON); sourceModel != "" && sourceModel != item.record.ProviderModel {
		entry.MatchReason = "provider_model_mismatch"
		return entry, nil
	}
	entry.SettlementID, entry.UserID, entry.Attempt, entry.ChannelID = matched.settlement.ID, matched.settlement.UserID, matched.attempt.Attempt, matched.attempt.ChannelID
	var charge ZTAPISupplierRefundCharge
	chargeErr := tx.Where("request_id = ? AND attempt = ?", matched.settlement.RequestID, matched.attempt.Attempt).Take(&charge).Error
	if chargeErr == nil {
		if charge.SettlementID != matched.settlement.ID || charge.UserID != matched.settlement.UserID || charge.ChannelID != matched.attempt.ChannelID || charge.CredentialVersion != matched.attempt.CredentialVersion {
			return entry, ErrZTAPISupplierReconciliationLineage
		}
		entry.ChargeID = charge.ID
	} else if !errors.Is(chargeErr, gorm.ErrRecordNotFound) {
		return entry, chargeErr
	}
	if item.reversal {
		if chargeErr != nil {
			entry.MatchReason = "original_charge_pending"
			return entry, nil
		}
		entry.ActionKind, entry.MatchReason = ZTAPISupplierReconciliationActionSupplierRefund, "matched"
		return entry, nil
	}
	if chargeErr == nil {
		if item.record.Billable != nil && !*item.record.Billable {
			entry.ActionKind, entry.MatchReason = ZTAPISupplierReconciliationActionUnmatched, "supplier_nocharge_conflicts_existing_charge"
			return entry, nil
		}
		entry.ActionKind, entry.MatchReason = ZTAPISupplierReconciliationActionExistingCharge, "matched"
		return entry, nil
	}
	if item.record.Billable != nil && !*item.record.Billable {
		entry.ActionKind, entry.MatchReason = ZTAPISupplierReconciliationActionNoChargeEvidence, "matched"
		return entry, nil
	}
	if len(item.dimensions.Usage) == 0 {
		entry.MatchReason = "billing_dimensions_missing"
		return entry, nil
	}
	entry.ActionKind, entry.MatchReason = ZTAPISupplierReconciliationActionAttemptBilling, "matched"
	return entry, nil
}

func loadZTAPISupplierImportResult(tx *gorm.DB, batch ZTAPISupplierReconciliationBatch) (*ZTAPISupplierReconciliationResult, error) {
	result := &ZTAPISupplierReconciliationResult{Batch: batch, Matched: batch.MatchedCount, Unmatched: batch.UnmatchedCount, Reversals: batch.ReversalCount}
	if batch.Status == ZTAPISupplierReconciliationApplied {
		if err := tx.Table("ztapi_supplier_reconciliation_entries AS entries").
			Select("entries.*").
			Joins("JOIN ztapi_supplier_reconciliation_batch_entries links ON links.entry_id = entries.id").
			Where("links.batch_id = ?", batch.ID).Order("entries.id").Find(&result.Entries).Error; err != nil {
			return nil, err
		}
		if len(result.Entries) > 0 {
			ids := make([]uint, 0, len(result.Entries))
			for _, entry := range result.Entries {
				ids = append(ids, entry.ID)
			}
			if err := tx.Where("entry_id IN ?", ids).Order("id").Find(&result.Actions).Error; err != nil {
				return nil, err
			}
		}
	}
	return result, nil
}

func PrepareZTAPISupplierReconciliation(db *gorm.DB, input ZTAPISupplierReconciliationImport) (*ZTAPISupplierReconciliationResult, error) {
	if db == nil || validateZTAPISupplierImport(input) != nil {
		return nil, ErrZTAPISupplierReconciliationInvalid
	}
	var result *ZTAPISupplierReconciliationResult
	err := db.Transaction(func(tx *gorm.DB) error {
		var operator User
		if err := tx.First(&operator, input.OperatorID).Error; err != nil || operator.Status != common.UserStatusEnabled || !common.HasAdminPermission(operator.Role, common.PermissionFinanceWrite) {
			return ErrZTAPISupplierRefundUnauthorized
		}
		normalized, err := normalizeZTAPISupplierRecords(tx, input.Supplier, input.Records)
		if err != nil {
			return err
		}
		matches := make([]ZTAPISupplierReconciliationEntry, 0, len(normalized))
		matched, unmatched, reversals := 0, 0, 0
		for _, item := range normalized {
			entry, matchErr := matchZTAPISupplierRecord(tx, input.Supplier, item)
			if matchErr != nil {
				return matchErr
			}
			if item.reversal {
				reversals++
			}
			if entry.ActionKind == ZTAPISupplierReconciliationActionUnmatched {
				unmatched++
			} else {
				matched++
			}
			matches = append(matches, entry)
		}
		var batch ZTAPISupplierReconciliationBatch
		lookup := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("supplier = ? AND idempotency_key = ?", input.Supplier, input.IdempotencyKey).Take(&batch).Error
		if errors.Is(lookup, gorm.ErrRecordNotFound) {
			if !input.DryRun {
				return ErrZTAPISupplierReconciliationInvalid
			}
			batch = ZTAPISupplierReconciliationBatch{Supplier: input.Supplier, IdempotencyKey: input.IdempotencyKey, FileChecksum: input.FileChecksum, PayloadHash: input.PayloadHash, Format: input.Format, OperatorID: input.OperatorID, Status: ZTAPISupplierReconciliationPreviewed, RowCount: len(normalized), MatchedCount: matched, UnmatchedCount: unmatched, ReversalCount: reversals}
			if err := tx.Create(&batch).Error; err != nil {
				return err
			}
		} else if lookup != nil {
			return lookup
		} else if batch.FileChecksum != input.FileChecksum || batch.PayloadHash != input.PayloadHash || batch.Format != input.Format || batch.OperatorID != input.OperatorID || batch.RowCount != len(normalized) || batch.MatchedCount != matched || batch.UnmatchedCount != unmatched || batch.ReversalCount != reversals {
			return ErrZTAPISupplierReconciliationConflict
		}
		if input.DryRun {
			if batch.Status != ZTAPISupplierReconciliationPreviewed && batch.Status != ZTAPISupplierReconciliationApplied {
				return ErrZTAPISupplierReconciliationConflict
			}
			result, err = loadZTAPISupplierImportResult(tx, batch)
			if err == nil && batch.Status == ZTAPISupplierReconciliationPreviewed {
				result.Entries = nil
				result.Actions = nil
			}
			return err
		}
		if batch.Status == ZTAPISupplierReconciliationApplied {
			result, err = loadZTAPISupplierImportResult(tx, batch)
			return err
		}
		for i := range matches {
			matches[i].BatchID = batch.ID
			var existing ZTAPISupplierReconciliationEntry
			lookup = tx.Where("supplier = ? AND supplier_record_id = ?", input.Supplier, matches[i].SupplierRecordID).Take(&existing).Error
			if lookup == nil {
				if existing.PayloadHash != matches[i].PayloadHash || existing.RecordJSON != matches[i].RecordJSON {
					return ErrZTAPISupplierReconciliationConflict
				}
				matches[i] = existing
			} else if errors.Is(lookup, gorm.ErrRecordNotFound) {
				if err := tx.Create(&matches[i]).Error; err != nil {
					return err
				}
			} else {
				return lookup
			}
			if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "batch_id"}, {Name: "entry_id"}}, DoNothing: true}).Create(&ZTAPISupplierReconciliationBatchEntry{BatchID: batch.ID, EntryID: matches[i].ID}).Error; err != nil {
				return err
			}
			action := ZTAPISupplierReconciliationAction{EntryID: matches[i].ID, Kind: matches[i].ActionKind, Status: "pending"}
			if action.Kind == ZTAPISupplierReconciliationActionUnmatched {
				action.Status, action.LastErrorCode = "unmatched", matches[i].MatchReason
			}
			if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "entry_id"}}, DoNothing: true}).Create(&action).Error; err != nil {
				return err
			}
		}
		if err := tx.Model(&ZTAPISupplierReconciliationBatch{}).Where("id = ? AND status = ?", batch.ID, ZTAPISupplierReconciliationPreviewed).Update("status", ZTAPISupplierReconciliationApplied).Error; err != nil {
			return err
		}
		batch.Status = ZTAPISupplierReconciliationApplied
		result, err = loadZTAPISupplierImportResult(tx, batch)
		return err
	})
	return result, err
}

func UpdateZTAPISupplierReconciliationAction(entryID uint, status string, billingProofID, refundProofID uint, errorCode string) error {
	if DB == nil || entryID == 0 || (status != "pending" && status != "submitted" && status != "linked" && status != "unmatched" && status != "failed") || len(errorCode) > 64 {
		return ErrZTAPISupplierReconciliationInvalid
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		var action ZTAPISupplierReconciliationAction
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("entry_id = ?", entryID).Take(&action).Error; err != nil {
			return err
		}
		var entry ZTAPISupplierReconciliationEntry
		if err := tx.First(&entry, entryID).Error; err != nil {
			return err
		}
		if action.Kind != entry.ActionKind {
			return ErrZTAPISupplierReconciliationConflict
		}
		valid := false
		switch status {
		case "pending":
			valid = billingProofID == 0 && refundProofID == 0 && errorCode == ""
		case "submitted":
			valid = errorCode == "" && ((action.Kind == ZTAPISupplierReconciliationActionAttemptBilling || action.Kind == ZTAPISupplierReconciliationActionNoChargeEvidence) && billingProofID > 0 && refundProofID == 0 || action.Kind == ZTAPISupplierReconciliationActionSupplierRefund && refundProofID > 0 && billingProofID == 0)
		case "linked":
			valid = action.Kind == ZTAPISupplierReconciliationActionExistingCharge && entry.ChargeID > 0 && billingProofID == 0 && refundProofID == 0 && errorCode == ""
		case "unmatched":
			valid = action.Kind == ZTAPISupplierReconciliationActionUnmatched && billingProofID == 0 && refundProofID == 0 && errorCode == entry.MatchReason && errorCode != ""
		case "failed":
			valid = billingProofID == 0 && refundProofID == 0 && errorCode != ""
		}
		if !valid {
			return ErrZTAPISupplierReconciliationInvalid
		}
		if action.Status == status && action.BillingProofID == billingProofID && action.RefundProofID == refundProofID && action.LastErrorCode == errorCode {
			return nil
		}
		if action.Status != "pending" && action.Status != "failed" {
			return ErrZTAPISupplierReconciliationConflict
		}
		result := tx.Model(&ZTAPISupplierReconciliationAction{}).Where("id = ? AND status IN ?", action.ID, []string{"pending", "failed"}).Updates(map[string]any{"status": status, "billing_proof_id": billingProofID, "refund_proof_id": refundProofID, "last_error_code": errorCode})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrZTAPISupplierReconciliationConflict
		}
		return nil
	})
}

func LoadZTAPISupplierReconciliationRecord(entry ZTAPISupplierReconciliationEntry) (ZTAPISupplierLedgerRecord, ZTAPISupplierLedgerDimensions, error) {
	var record ZTAPISupplierLedgerRecord
	if common.UnmarshalJsonStr(entry.RecordJSON, &record) != nil {
		return record, ZTAPISupplierLedgerDimensions{}, ErrZTAPISupplierReconciliationConflict
	}
	normalized, raw, hash, _, err := normalizeZTAPISupplierLedgerRecord(record)
	if err != nil || raw != entry.RecordJSON || hash != entry.PayloadHash || normalized.SupplierRecordID != entry.SupplierRecordID {
		return record, ZTAPISupplierLedgerDimensions{}, ErrZTAPISupplierReconciliationConflict
	}
	dimensions, err := ParseZTAPISupplierLedgerDimensions(normalized.DimensionsJSON)
	return normalized, dimensions, err
}

func GetZTAPISupplierReconciliationBatch(db *gorm.DB, supplier, idempotencyKey string) (*ZTAPISupplierReconciliationResult, error) {
	if db == nil {
		return nil, ErrZTAPISupplierReconciliationInvalid
	}
	var batch ZTAPISupplierReconciliationBatch
	if err := db.Where("supplier = ? AND idempotency_key = ?", supplier, idempotencyKey).Take(&batch).Error; err != nil {
		return nil, err
	}
	return loadZTAPISupplierImportResult(db, batch)
}
