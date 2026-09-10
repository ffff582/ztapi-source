package service

import (
	"math"
	"sort"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var _ model.ZTAPIAttemptBillingPricer = PriceZTAPIAttemptBilling
var _ model.ZTAPIAttemptBillingLogEnqueuer = EnqueueZTAPIAttemptBillingLogTx

func EnqueueZTAPIAttemptBillingLogTx(tx *gorm.DB, parent *model.ZTAPIRequestSettlement, charge *model.ZTAPISupplierRefundCharge, operationID string, log model.Log) error {
	if tx == nil || tx.Statement == nil || parent == nil || charge == nil || parent.ID == 0 || charge.ID == 0 ||
		strings.TrimSpace(operationID) == "" || len(operationID) > 64 {
		return model.ErrZTAPIAttemptBillingInvalid
	}
	if _, transactional := tx.Statement.ConnPool.(gorm.TxCommitter); !transactional {
		return model.ErrZTAPIAttemptBillingInvalid
	}
	var storedParent model.ZTAPIRequestSettlement
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&storedParent, parent.ID).Error; err != nil {
		return err
	}
	if storedParent.Status != model.ZTAPISettlementSettled || storedParent.FinalAttempt != 2 ||
		parent.Status != storedParent.Status || parent.FinalAttempt != storedParent.FinalAttempt ||
		parent.OperationID != storedParent.OperationID || parent.RequestID != storedParent.RequestID ||
		parent.UserID != storedParent.UserID || parent.TokenID != storedParent.TokenID || parent.TokenUnlimited != storedParent.TokenUnlimited ||
		parent.PublicModel != storedParent.PublicModel || parent.PriceSnapshotJSON != storedParent.PriceSnapshotJSON ||
		parent.ChargedQuota != storedParent.ChargedQuota || parent.TokenChargedQuota != storedParent.TokenChargedQuota {
		return model.ErrZTAPIAttemptBillingConflict
	}
	var storedCharge model.ZTAPISupplierRefundCharge
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&storedCharge, charge.ID).Error; err != nil {
		return err
	}
	if charge.SettlementID != storedCharge.SettlementID || charge.RequestID != storedCharge.RequestID ||
		charge.UserID != storedCharge.UserID || charge.TokenID != storedCharge.TokenID || charge.Attempt != storedCharge.Attempt ||
		charge.ChannelID != storedCharge.ChannelID || charge.CredentialVersion != storedCharge.CredentialVersion ||
		charge.UpstreamRequestID != storedCharge.UpstreamRequestID || charge.UpstreamTaskID != storedCharge.UpstreamTaskID ||
		charge.UpstreamBillID != storedCharge.UpstreamBillID || charge.PriceSnapshotJSON != storedCharge.PriceSnapshotJSON ||
		charge.DimensionsJSON != storedCharge.DimensionsJSON || charge.OriginalLedgerID != storedCharge.OriginalLedgerID ||
		charge.ChargedQuota != storedCharge.ChargedQuota || charge.TokenChargedQuota != storedCharge.TokenChargedQuota ||
		charge.BillingProofID != storedCharge.BillingProofID || charge.BillingOperationID != storedCharge.BillingOperationID {
		return model.ErrZTAPIAttemptBillingConflict
	}
	if storedCharge.SettlementID != storedParent.ID || storedCharge.RequestID != storedParent.RequestID ||
		storedCharge.UserID != storedParent.UserID || storedCharge.TokenID != storedParent.TokenID ||
		storedCharge.PriceSnapshotJSON != storedParent.PriceSnapshotJSON || storedCharge.Attempt != 1 ||
		storedCharge.BillingProofID == 0 || operationID != storedCharge.BillingOperationID || operationID == storedParent.OperationID ||
		storedCharge.ChargedQuota < 0 || storedCharge.TokenChargedQuota < 0 || storedCharge.TokenChargedQuota > storedCharge.ChargedQuota {
		return model.ErrZTAPIAttemptBillingConflict
	}
	var attempt model.ZTAPIRequestAttempt
	if err := tx.Where("settlement_id = ? AND attempt = ?", storedParent.ID, storedCharge.Attempt).Take(&attempt).Error; err != nil {
		return err
	}
	if attempt.ChannelID != storedCharge.ChannelID || attempt.CredentialVersion != storedCharge.CredentialVersion ||
		attempt.UpstreamRequestID != storedCharge.UpstreamRequestID || log.ChannelId != storedCharge.ChannelID ||
		log.UserId != storedParent.UserID || log.TokenId != storedParent.TokenID || log.RequestId != storedParent.RequestID ||
		log.ModelName != storedParent.PublicModel || int64(log.Quota) != storedCharge.ChargedQuota {
		return model.ErrZTAPIAttemptBillingConflict
	}
	// This is an enqueuer argument only. Saving it would corrupt the parent's
	// original finalization and its independently replayable financial identity.
	shadow := storedParent
	shadow.OperationID = operationID
	shadow.FinalAttempt = storedCharge.Attempt
	shadow.ChargedQuota = storedCharge.ChargedQuota
	shadow.TokenChargedQuota = storedCharge.TokenChargedQuota
	return model.EnqueueZTAPISettlementLogTx(tx, &shadow, log)
}

func PriceZTAPIAttemptBilling(parent model.ZTAPIRequestSettlement, submission model.ZTAPIAttemptBillingSubmission) (model.ZTAPIAttemptBillingPriced, error) {
	invalid := model.ZTAPIAttemptBillingPriced{}
	if parent.ID == 0 || parent.UserID <= 0 || parent.TokenID <= 0 || parent.CreatedAt.Unix() <= 0 ||
		parent.RequestID == "" || len(parent.RequestID) > 64 || parent.PublicModel == "" ||
		(parent.Status != model.ZTAPISettlementSettled && parent.Status != model.ZTAPISettlementPending) ||
		submission.RequestID != parent.RequestID || submission.UserID != parent.UserID || submission.ChannelID <= 0 ||
		submission.Attempt < 1 || submission.Attempt > 2 || submission.Kind != "billed" ||
		(submission.UsageSemantic != "openai" && submission.UsageSemantic != "anthropic") ||
		len(submission.Usage) == 0 || len(submission.Usage) > 6 || len(parent.PriceSnapshotJSON) > 65536 {
		return invalid, model.ErrZTAPIAttemptBillingInvalid
	}
	var snapshot relaycommon.ZTAPIPublicationSnapshot
	if err := common.UnmarshalJsonStr(parent.PriceSnapshotJSON, &snapshot); err != nil ||
		snapshot.PublicName != parent.PublicModel || snapshot.PriceSourceID <= 0 || snapshot.PriceSourceVersion == 0 {
		return invalid, model.ErrZTAPIAttemptBillingInvalid
	}
	listed := make(map[string]bool, len(snapshot.BillingDimensions))
	for _, name := range snapshot.BillingDimensions {
		if listed[name] {
			return invalid, model.ErrZTAPIAttemptBillingInvalid
		}
		listed[name] = true
	}
	usage := append([]model.ZTAPIAttemptBillingQuantity(nil), submission.Usage...)
	sort.Slice(usage, func(i, j int) bool { return usage[i].Dimension < usage[j].Dimension })
	check := relaycommon.ZTAPIUsageDimensionResult{Managed: true, UsageSemantic: submission.UsageSemantic}
	var input, output int64
	for i, quantity := range usage {
		if !ztapiAttemptTokenDimension(quantity.Dimension) || quantity.Quantity < 0 || quantity.Quantity > math.MaxInt32 ||
			(i > 0 && usage[i-1].Dimension == quantity.Dimension) {
			return invalid, model.ErrZTAPIAttemptBillingInvalid
		}
		if quantity.Quantity == 0 {
			continue
		}
		if !listed[quantity.Dimension] || (snapshot.Modality == model.ZTAPIModalityEmbedding && quantity.Dimension != "input_tokens") {
			return invalid, model.ErrZTAPIAttemptBillingInvalid
		}
		price, err := decimal.NewFromString(strings.TrimSpace(snapshot.SaleUSD[quantity.Dimension]))
		if err != nil || price.IsNegative() {
			return invalid, model.ErrZTAPIAttemptBillingInvalid
		}
		state := relaycommon.ZTAPIQuoteStateQuoted
		if price.IsZero() {
			state = relaycommon.ZTAPIQuoteStateFree
		}
		check.Dimensions = append(check.Dimensions, relaycommon.ZTAPIUsageDimension{
			Dimension: quantity.Dimension, Quantity: quantity.Quantity, Unit: "token", QuoteState: state, UnitPriceUSD: price.Shift(-6).String(),
		})
		// Proof buckets are already exclusive. Cache stays in the input total
		// for reporting, but is priced only in its own dimension.
		if quantity.Dimension == "output_tokens" {
			output += quantity.Quantity
		} else {
			input += quantity.Quantity
		}
	}
	if len(check.Dimensions) == 0 || input+output > math.MaxInt32 {
		return invalid, model.ErrZTAPIAttemptBillingInvalid
	}
	quota, dimensions, err := calculateZTAPIChargeDimensions(check)
	if err != nil {
		return invalid, err
	}
	metadata, err := common.Marshal(map[string]any{
		"billing_source": "wallet", "billing_status": "settled", "billing_dimensions": check.Dimensions,
		"publication_version": snapshot.Version, "price_source_version": snapshot.PriceSourceVersion, "usage_semantic": submission.UsageSemantic,
	})
	if err != nil {
		return invalid, err
	}
	return model.ZTAPIAttemptBillingPriced{Dimensions: dimensions, ConsumeLog: model.Log{
		Type: model.LogTypeConsume, UserId: parent.UserID, TokenId: parent.TokenID, RequestId: parent.RequestID,
		ModelName: parent.PublicModel, ChannelId: submission.ChannelID, Quota: quota,
		PromptTokens: int(input), CompletionTokens: int(output), CreatedAt: parent.CreatedAt.Unix(), Other: string(metadata),
	}}, nil
}

func ztapiAttemptTokenDimension(name string) bool {
	switch name {
	case "input_tokens", "output_tokens", "cache_read", "cache_write", "cache_write_5m", "cache_write_1h":
		return true
	default:
		return false
	}
}
