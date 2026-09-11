package service

import (
	"math"
	"sort"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/shopspring/decimal"
	"github.com/tidwall/gjson"
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
		len(submission.Usage) == 0 || len(submission.Usage) > 6 || len(parent.PriceSnapshotJSON) > 65536 {
		return invalid, model.ErrZTAPIAttemptBillingInvalid
	}
	if submission.UsageSemantic == "ztapi_image" {
		return priceZTAPIImageAttemptBilling(parent, submission)
	}
	if submission.UsageSemantic != "openai" && submission.UsageSemantic != "anthropic" {
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

func priceZTAPIImageAttemptBilling(parent model.ZTAPIRequestSettlement, submission model.ZTAPIAttemptBillingSubmission) (model.ZTAPIAttemptBillingPriced, error) {
	invalid := model.ZTAPIAttemptBillingPriced{}
	var frozen ztapiFrozenMediaReservation
	if common.RejectDuplicateJsonObjectMembers(strings.NewReader(parent.PriceSnapshotJSON)) != nil ||
		common.DecodeJsonStrict(strings.NewReader(parent.PriceSnapshotJSON), &frozen) != nil ||
		frozen.Modality != model.ZTAPIModalityImage || frozen.PublicName != parent.PublicModel ||
		frozen.PublicationID <= 0 || frozen.Version == 0 || frozen.PriceSourceID <= 0 || frozen.PriceSourceVersion == 0 {
		return invalid, model.ErrZTAPIAttemptBillingInvalid
	}
	canonical, err := types.CanonicalizeZTAPIMediaPriceContract(frozen.MediaPriceContractJSON)
	if err != nil || canonical != frozen.MediaPriceContractJSON {
		return invalid, model.ErrZTAPIAttemptBillingInvalid
	}
	contract, err := types.ParseZTAPIMediaPriceContract(frozen.MediaPriceContractJSON)
	if err != nil || contract.Modality != model.ZTAPIModalityImage {
		return invalid, model.ErrZTAPIAttemptBillingInvalid
	}
	protocol, protocolJSON, err := types.ParseZTAPIImageProtocolContract(frozen.ImageProtocolContractJSON)
	if err != nil || protocolJSON != frozen.ImageProtocolContractJSON || protocol.EvidenceHash != frozen.ProtocolEvidenceHash ||
		types.ValidateZTAPIImagePriceProtocolCompatibility(contract, protocol) != nil {
		return invalid, model.ErrZTAPIAttemptBillingInvalid
	}
	dimensions := make(map[string]decimal.Decimal, len(submission.Usage))
	positive := false
	for _, quantity := range submission.Usage {
		if quantity.Quantity < 0 || quantity.Quantity > math.MaxInt32 {
			return invalid, model.ErrZTAPIAttemptBillingInvalid
		}
		if _, duplicate := dimensions[quantity.Dimension]; duplicate {
			return invalid, model.ErrZTAPIAttemptBillingInvalid
		}
		dimensions[quantity.Dimension] = decimal.NewFromInt(quantity.Quantity)
		positive = positive || quantity.Quantity > 0
	}
	if !positive || !ztapiImageDimensionsMatchProtocol(dimensions, protocol) ||
		!ztapiImageUsageWithinMaximum(dimensions, frozen.MaximumDimensions) ||
		!matchesZTAPIImageRawUsage(protocol, submission.RawUsageJSON, dimensions) {
		return invalid, model.ErrZTAPIAttemptBillingInvalid
	}
	selectedRuleID, ruleIDs, err := deriveZTAPIImageAttemptRules(contract, dimensions)
	if err != nil || submission.SelectedRuleID != selectedRuleID || !equalZTAPIStringMaps(submission.PriceRuleIDs, ruleIDs) {
		return invalid, model.ErrZTAPIAttemptBillingInvalid
	}
	quota, chargeDimensions, logDimensions, err := calculateZTAPIImageChargeDimensions(frozen.MediaPriceContractJSON, frozen.QuotaPerUnit, selectedRuleID, dimensions, ruleIDs)
	if err != nil {
		return invalid, model.ErrZTAPIAttemptBillingInvalid
	}
	var input, output int64
	for name, quantity := range dimensions {
		if name == "output_tokens" || name == "image_output" {
			output += quantity.IntPart()
		} else {
			input += quantity.IntPart()
		}
	}
	metadata, err := common.Marshal(map[string]any{
		"billing_source": "wallet", "billing_status": "settled", "billing_dimensions": logDimensions,
		"publication_version": frozen.Version, "price_source_version": frozen.PriceSourceVersion,
		"usage_semantic": submission.UsageSemantic, "selected_rule_id": selectedRuleID,
	})
	if err != nil {
		return invalid, err
	}
	return model.ZTAPIAttemptBillingPriced{Dimensions: chargeDimensions, ConsumeLog: model.Log{
		Type: model.LogTypeConsume, UserId: parent.UserID, TokenId: parent.TokenID, RequestId: parent.RequestID,
		ModelName: parent.PublicModel, ChannelId: submission.ChannelID, Quota: int(quota),
		PromptTokens: int(input), CompletionTokens: int(output), CreatedAt: parent.CreatedAt.Unix(), Other: string(metadata),
	}}, nil
}

func matchesZTAPIImageRawUsage(protocol types.ZTAPIImageProtocolContract, raw string, dimensions map[string]decimal.Decimal) bool {
	if raw == "" || len(raw) > 65536 || common.RejectDuplicateJsonObjectMembers(strings.NewReader(raw)) != nil || !gjson.Valid(raw) {
		return false
	}
	var canonicalValue map[string]any
	if common.DecodeJsonStrict(strings.NewReader(raw), &canonicalValue) != nil || canonicalValue == nil {
		return false
	}
	if relaycommon.ZTAPIGPTImage2UsagePendingReason([]byte(raw), protocol) != "" {
		return false
	}
	canonical, err := common.Marshal(canonicalValue)
	if err != nil || string(canonical) != raw {
		return false
	}
	var total int64
	for dimension, field := range protocol.Usage.Fields {
		quantity, ok := dimensions[dimension]
		result := gjson.Get(raw, field)
		if !ok || !result.Exists() || result.Type != gjson.Number {
			return false
		}
		parsed, err := decimal.NewFromString(result.Raw)
		if err != nil || !parsed.Equal(quantity) || !parsed.Equal(decimal.NewFromInt(parsed.IntPart())) || parsed.IsNegative() || total > math.MaxInt64-parsed.IntPart() {
			return false
		}
		total += parsed.IntPart()
	}
	result := gjson.Get(raw, protocol.Usage.TotalField)
	if !result.Exists() || result.Type != gjson.Number {
		return false
	}
	parsed, err := decimal.NewFromString(result.Raw)
	return err == nil && parsed.Equal(decimal.NewFromInt(total))
}

func ztapiImageDimensionsMatchProtocol(dimensions map[string]decimal.Decimal, protocol types.ZTAPIImageProtocolContract) bool {
	if len(dimensions) != len(protocol.Usage.Fields) {
		return false
	}
	for dimension := range protocol.Usage.Fields {
		if _, ok := dimensions[dimension]; !ok {
			return false
		}
	}
	return true
}

func deriveZTAPIImageAttemptRules(contract types.ZTAPIMediaPriceContract, dimensions map[string]decimal.Decimal) (string, map[string]string, error) {
	if len(contract.Rules) == 0 || len(dimensions) == 0 {
		return "", nil, model.ErrZTAPIAttemptBillingInvalid
	}
	ruleIDs := make(map[string]string, len(dimensions))
	if _, tiered := contract.Rules[0].Conditions["prompt_tokens_tier"]; tiered {
		if len(dimensions) != 2 {
			return "", nil, model.ErrZTAPIAttemptBillingInvalid
		}
		input, inputOK := dimensions["input_tokens"]
		_, outputOK := dimensions["output_tokens"]
		if !inputOK || !outputOK {
			return "", nil, model.ErrZTAPIAttemptBillingInvalid
		}
		tier := "lte_200k"
		if input.GreaterThan(decimal.NewFromInt(200000)) {
			tier = "gt_200k"
		}
		for _, rule := range contract.Rules {
			if rule.Conditions["prompt_tokens_tier"] == tier {
				ruleIDs["input_tokens"], ruleIDs["output_tokens"] = rule.ID, rule.ID
				return rule.ID, ruleIDs, nil
			}
		}
		return "", nil, model.ErrZTAPIAttemptBillingInvalid
	}
	for dimension := range dimensions {
		matched := false
		for _, rule := range contract.Rules {
			if len(rule.SaleUSD) != 1 {
				return "", nil, model.ErrZTAPIAttemptBillingInvalid
			}
			if _, ok := rule.SaleUSD[dimension]; ok && rule.Conditions["token_bucket"] == dimension {
				ruleIDs[dimension] = rule.ID
				matched = true
				break
			}
		}
		if !matched {
			return "", nil, model.ErrZTAPIAttemptBillingInvalid
		}
	}
	return "", ruleIDs, nil
}

func ztapiAttemptTokenDimension(name string) bool {
	switch name {
	case "input_tokens", "output_tokens", "cache_read", "cache_write", "cache_write_5m", "cache_write_1h":
		return true
	default:
		return false
	}
}
