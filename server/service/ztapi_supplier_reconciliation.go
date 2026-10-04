package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"gorm.io/gorm"
)

const (
	ZTAPISupplierReconciliationMaxFileBytes = 1 << 20
	ZTAPISupplierReconciliationMaxRows      = 1000
)

var ErrZTAPISupplierReconciliationChecksum = errors.New("supplier reconciliation file checksum mismatch")

type ZTAPISupplierReconciliationRequest struct {
	Supplier       string `json:"supplier"`
	IdempotencyKey string `json:"idempotency_key"`
	FileChecksum   string `json:"file_checksum"`
	Format         string `json:"format"`
	Content        string `json:"content"`
	DryRun         bool   `json:"dry_run"`
}

var ztapiSupplierCSVHeaders = []string{
	"supplier_record_id", "request_id", "task_id", "credential_ref", "provider_model", "resource_type", "occurred_at", "billable", "dimensions_json", "debit_amount", "currency", "reversal_id", "original_record_id", "reversal_amount", "raw_evidence_hash",
}

func parseZTAPISupplierJSON(content []byte) ([]model.ZTAPISupplierLedgerRecord, error) {
	if common.RejectDuplicateJsonObjectMembers(bytes.NewReader(content)) != nil {
		return nil, model.ErrZTAPISupplierReconciliationInvalid
	}
	var records []model.ZTAPISupplierLedgerRecord
	if common.DecodeJsonStrict(bytes.NewReader(content), &records) != nil || len(records) == 0 || len(records) > ZTAPISupplierReconciliationMaxRows {
		return nil, model.ErrZTAPISupplierReconciliationInvalid
	}
	return records, nil
}

func parseZTAPISupplierCSV(content []byte) ([]model.ZTAPISupplierLedgerRecord, error) {
	reader := csv.NewReader(bytes.NewReader(content))
	reader.ReuseRecord = false
	reader.FieldsPerRecord = len(ztapiSupplierCSVHeaders)
	reader.TrimLeadingSpace = false
	header, err := reader.Read()
	if err != nil || len(header) != len(ztapiSupplierCSVHeaders) {
		return nil, model.ErrZTAPISupplierReconciliationInvalid
	}
	for i := range header {
		if header[i] != ztapiSupplierCSVHeaders[i] {
			return nil, model.ErrZTAPISupplierReconciliationInvalid
		}
	}
	records := make([]model.ZTAPISupplierLedgerRecord, 0)
	for {
		row, readErr := reader.Read()
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil || len(records) >= ZTAPISupplierReconciliationMaxRows {
			return nil, model.ErrZTAPISupplierReconciliationInvalid
		}
		occurredAt, parseErr := strconv.ParseInt(row[6], 10, 64)
		if parseErr != nil {
			return nil, model.ErrZTAPISupplierReconciliationInvalid
		}
		var billable *bool
		if row[7] != "" {
			parsed, boolErr := strconv.ParseBool(row[7])
			if boolErr != nil {
				return nil, model.ErrZTAPISupplierReconciliationInvalid
			}
			billable = &parsed
		}
		records = append(records, model.ZTAPISupplierLedgerRecord{
			SupplierRecordID: row[0], RequestID: row[1], TaskID: row[2], CredentialRef: row[3], ProviderModel: row[4], ResourceType: row[5], OccurredAt: occurredAt,
			Billable: billable, DimensionsJSON: row[8], DebitAmount: row[9], Currency: row[10], ReversalID: row[11], OriginalRecordID: row[12], ReversalAmount: row[13], RawEvidenceHash: row[14],
		})
	}
	if len(records) == 0 {
		return nil, model.ErrZTAPISupplierReconciliationInvalid
	}
	return records, nil
}

func parseZTAPISupplierRecords(format string, content []byte) ([]model.ZTAPISupplierLedgerRecord, error) {
	switch format {
	case "json":
		return parseZTAPISupplierJSON(content)
	case "csv":
		return parseZTAPISupplierCSV(content)
	default:
		return nil, model.ErrZTAPISupplierReconciliationInvalid
	}
}

func ImportZTAPISupplierReconciliation(ctx context.Context, operatorID int, request ZTAPISupplierReconciliationRequest) (*model.ZTAPISupplierReconciliationResult, error) {
	content := []byte(request.Content)
	if ctx == nil || model.DB == nil || operatorID <= 0 || len(content) == 0 || len(content) > ZTAPISupplierReconciliationMaxFileBytes {
		return nil, model.ErrZTAPISupplierReconciliationInvalid
	}
	checksum := fmt.Sprintf("%x", sha256.Sum256(content))
	if request.FileChecksum != checksum {
		return nil, ErrZTAPISupplierReconciliationChecksum
	}
	records, err := parseZTAPISupplierRecords(request.Format, content)
	if err != nil {
		return nil, err
	}
	canonical, err := common.Marshal(records)
	if err != nil {
		return nil, model.ErrZTAPISupplierReconciliationInvalid
	}
	payloadHash := fmt.Sprintf("%x", sha256.Sum256(canonical))
	input := model.ZTAPISupplierReconciliationImport{
		Supplier: request.Supplier, IdempotencyKey: request.IdempotencyKey, FileChecksum: checksum, PayloadHash: payloadHash,
		Format: request.Format, OperatorID: operatorID, DryRun: request.DryRun, Records: records,
	}
	result, err := model.PrepareZTAPISupplierReconciliation(model.DB.WithContext(ctx), input)
	if err != nil || request.DryRun {
		return result, err
	}
	for _, action := range result.Actions {
		if action.Status == "submitted" || action.Status == "linked" || action.Status == "unmatched" {
			continue
		}
		var entry model.ZTAPISupplierReconciliationEntry
		if err = model.DB.WithContext(ctx).First(&entry, action.EntryID).Error; err != nil {
			return nil, err
		}
		billingID, refundID, status, code := processZTAPISupplierReconciliationEntry(ctx, entry)
		if updateErr := model.UpdateZTAPISupplierReconciliationAction(entry.ID, status, billingID, refundID, code); updateErr != nil {
			return nil, updateErr
		}
	}
	return model.GetZTAPISupplierReconciliationBatch(model.DB.WithContext(ctx), request.Supplier, request.IdempotencyKey)
}

func processZTAPISupplierReconciliationEntry(ctx context.Context, entry model.ZTAPISupplierReconciliationEntry) (uint, uint, string, string) {
	if entry.ActionKind == model.ZTAPISupplierReconciliationActionUnmatched {
		return 0, 0, "unmatched", entry.MatchReason
	}
	if entry.ActionKind == model.ZTAPISupplierReconciliationActionExistingCharge {
		if entry.ChargeID == 0 {
			return 0, 0, "failed", "charge_missing"
		}
		return 0, 0, "linked", ""
	}
	record, dimensions, err := model.LoadZTAPISupplierReconciliationRecord(entry)
	if err != nil {
		return 0, 0, "failed", "evidence_conflict"
	}
	var settlement model.ZTAPIRequestSettlement
	if err = model.DB.WithContext(ctx).First(&settlement, entry.SettlementID).Error; err != nil || settlement.UserID != entry.UserID {
		return 0, 0, "failed", "lineage_missing"
	}
	var attempt model.ZTAPIRequestAttempt
	if err = model.DB.WithContext(ctx).Where("settlement_id = ? AND attempt = ?", entry.SettlementID, entry.Attempt).Take(&attempt).Error; err != nil || attempt.ChannelID != entry.ChannelID || attempt.CredentialVersion != record.CredentialRef {
		return 0, 0, "failed", "lineage_mismatch"
	}
	reference := fmt.Sprintf("supplier-import:%s:%s:%s", entry.Supplier, entry.SupplierRecordID, entry.PayloadHash[:16])
	switch entry.ActionKind {
	case model.ZTAPISupplierReconciliationActionAttemptBilling, model.ZTAPISupplierReconciliationActionNoChargeEvidence:
		kind := "billed"
		usage := dimensions.Usage
		semantic := dimensions.UsageSemantic
		billID := record.SupplierRecordID
		distinct := "supplier unique bill record bound to exact attempt lineage"
		if entry.ActionKind == model.ZTAPISupplierReconciliationActionNoChargeEvidence {
			kind, usage, semantic, billID, distinct = "nocharge", nil, "", "", ""
		}
		proof, submitErr := model.SubmitZTAPIAttemptBilling(model.ZTAPIAttemptBillingSubmission{
			Source: entry.Supplier, ProofID: record.SupplierRecordID, RequestID: settlement.RequestID, UserID: settlement.UserID,
			Attempt: attempt.Attempt, ChannelID: attempt.ChannelID, CredentialVersion: attempt.CredentialVersion,
			UpstreamRequestID: attempt.UpstreamRequestID, UpstreamTaskID: record.TaskID, UpstreamBillID: billID, Kind: kind, UsageSemantic: semantic, Usage: usage,
			SelectedRuleID: dimensions.SelectedRuleID, PriceRuleIDs: dimensions.PriceRuleIDs, RawUsageJSON: dimensions.RawUsageJSON,
			EvidenceReference: reference, DistinctUsageReference: distinct,
		})
		if submitErr != nil {
			return 0, 0, "failed", ztapiSupplierActionErrorCode(submitErr)
		}
		return proof.ID, 0, "submitted", ""
	case model.ZTAPISupplierReconciliationActionSupplierRefund:
		var charge model.ZTAPISupplierRefundCharge
		if entry.ChargeID == 0 || model.DB.WithContext(ctx).First(&charge, entry.ChargeID).Error != nil || charge.RequestID != settlement.RequestID || charge.UserID != settlement.UserID || charge.Attempt != attempt.Attempt || charge.ChannelID != attempt.ChannelID || charge.CredentialVersion != attempt.CredentialVersion {
			return 0, 0, "failed", "charge_lineage_mismatch"
		}
		proof, submitErr := model.SubmitZTAPISupplierRefund(model.ZTAPISupplierRefundSubmission{
			Source: entry.Supplier, ProofID: record.ReversalID, RequestID: settlement.RequestID, UserID: settlement.UserID,
			Attempt: charge.Attempt, ChannelID: charge.ChannelID, CredentialVersion: charge.CredentialVersion,
			UpstreamRequestID: charge.UpstreamRequestID, UpstreamTaskID: charge.UpstreamTaskID, UpstreamBillID: charge.UpstreamBillID,
			Mode: dimensions.RefundMode, Units: dimensions.RefundUnits, EvidenceReference: reference,
		})
		if submitErr != nil {
			return 0, 0, "failed", ztapiSupplierActionErrorCode(submitErr)
		}
		return 0, proof.ID, "submitted", ""
	default:
		return 0, 0, "failed", "unsupported_action"
	}
}

func ztapiSupplierActionErrorCode(err error) string {
	switch {
	case errors.Is(err, model.ErrZTAPIAttemptBillingConflict), errors.Is(err, model.ErrZTAPISupplierRefundConflict), errors.Is(err, model.ErrZTAPISettlementConflict):
		return "evidence_conflict"
	case errors.Is(err, model.ErrZTAPIAttemptBillingInvalid), errors.Is(err, model.ErrZTAPISupplierRefundEvidence), errors.Is(err, model.ErrZTAPISupplierReconciliationInvalid):
		return "evidence_invalid"
	case errors.Is(err, gorm.ErrRecordNotFound):
		return "lineage_missing"
	default:
		return "submission_failed"
	}
}
