package service

import (
	"context"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/shopspring/decimal"
)

type ZTAPIMediaReservationInput struct {
	OperationID          string
	RequestID            string
	UserID               int
	TokenID              int
	PublicModel          string
	PriceSnapshotJSON    string
	SelectorJSON         string
	ProtocolContractJSON string
	ProtocolEvidenceHash string
	QuotaPerUnit         string
	MaximumDimensions    map[string]string
	MaximumQuota         int64
}

type ztapiFrozenMediaReservation struct {
	relaycommon.ZTAPIPublicationSnapshot
	SelectorJSON              string            `json:"ztapi_media_selector"`
	ImageProtocolContractJSON string            `json:"ztapi_image_protocol_contract"`
	ProtocolEvidenceHash      string            `json:"ztapi_protocol_evidence_hash"`
	QuotaPerUnit              string            `json:"ztapi_quota_per_unit"`
	MaximumDimensions         map[string]string `json:"ztapi_maximum_dimensions"`
	MaximumQuota              int64             `json:"ztapi_maximum_quota"`
}

type ztapiMediaSelector struct {
	Modality       string `json:"modality"`
	N              int    `json:"n"`
	Quality        string `json:"quality"`
	ResponseFormat string `json:"response_format"`
	Size           string `json:"size"`
}

func BeginZTAPIMediaReservation(input ZTAPIMediaReservationInput) (*model.ZTAPIRequestSettlement, error) {
	if strings.TrimSpace(input.OperationID) == "" || strings.TrimSpace(input.RequestID) == "" ||
		input.UserID <= 0 || input.TokenID <= 0 || strings.TrimSpace(input.PublicModel) == "" ||
		input.MaximumQuota < 0 {
		return nil, model.ErrZTAPISettlementInvalid
	}

	var snapshot relaycommon.ZTAPIPublicationSnapshot
	if err := common.RejectDuplicateJsonObjectMembers(strings.NewReader(input.PriceSnapshotJSON)); err != nil {
		return nil, model.ErrZTAPISettlementInvalid
	}
	if err := common.UnmarshalJsonStr(input.PriceSnapshotJSON, &snapshot); err != nil ||
		snapshot.Modality != "image" || snapshot.PublicName != input.PublicModel {
		return nil, model.ErrZTAPISettlementInvalid
	}
	canonicalContract, err := types.CanonicalizeZTAPIMediaPriceContract(snapshot.MediaPriceContractJSON)
	if err != nil || canonicalContract != snapshot.MediaPriceContractJSON {
		return nil, model.ErrZTAPISettlementInvalid
	}

	selector, canonicalSelector, err := canonicalZTAPIMediaSelector(input.SelectorJSON)
	if err != nil {
		return nil, model.ErrZTAPISettlementInvalid
	}
	protocol, canonicalProtocol, err := types.ParseZTAPIImageProtocolContract(input.ProtocolContractJSON)
	if err != nil || canonicalProtocol != input.ProtocolContractJSON || protocol.EvidenceHash != input.ProtocolEvidenceHash {
		return nil, model.ErrZTAPISettlementInvalid
	}
	quotaPerUnit, err := parseCanonicalZTAPIQuotaPerUnit(input.QuotaPerUnit)
	if err != nil {
		return nil, model.ErrZTAPISettlementInvalid
	}
	maximumDimensions, maximumQuota, err := deriveZTAPIImageMaximumReservation(
		snapshot.MediaPriceContractJSON, protocol, selector, quotaPerUnit,
	)
	if err != nil || !equalZTAPIStringMaps(maximumDimensions, input.MaximumDimensions) || maximumQuota != input.MaximumQuota {
		return nil, model.ErrZTAPISettlementInvalid
	}
	frozen, err := common.Marshal(ztapiFrozenMediaReservation{
		ZTAPIPublicationSnapshot:  snapshot,
		SelectorJSON:              canonicalSelector,
		ImageProtocolContractJSON: canonicalProtocol,
		ProtocolEvidenceHash:      protocol.EvidenceHash,
		QuotaPerUnit:              quotaPerUnit.String(),
		MaximumDimensions:         maximumDimensions,
		MaximumQuota:              input.MaximumQuota,
	})
	if err != nil {
		return nil, model.ErrZTAPISettlementInvalid
	}

	var token model.Token
	if model.DB == nil || model.DB.Unscoped().First(&token, input.TokenID).Error != nil || token.UserId != input.UserID {
		return nil, model.ErrZTAPISettlementInvalid
	}

	return model.BeginZTAPIRequestSettlement(model.ZTAPIRequestSettlement{
		OperationID:       input.OperationID,
		RequestID:         input.RequestID,
		UserID:            input.UserID,
		TokenID:           input.TokenID,
		TokenUnlimited:    token.UnlimitedQuota,
		PublicModel:       input.PublicModel,
		PriceSnapshotJSON: string(frozen),
		ReservedQuota:     input.MaximumQuota,
	})
}

func canonicalZTAPIMediaSelector(raw string) (types.ZTAPIImageSelector, string, error) {
	if err := common.RejectDuplicateJsonObjectMembers(strings.NewReader(raw)); err != nil {
		return types.ZTAPIImageSelector{}, "", model.ErrZTAPISettlementInvalid
	}
	var value ztapiMediaSelector
	if err := common.DecodeJsonStrict(strings.NewReader(raw), &value); err != nil || value.Modality != "image" ||
		value.N <= 0 || value.Size == "" || value.Size != strings.TrimSpace(value.Size) ||
		value.Quality == "" || value.Quality != strings.TrimSpace(value.Quality) ||
		value.ResponseFormat == "" || value.ResponseFormat != strings.TrimSpace(value.ResponseFormat) {
		return types.ZTAPIImageSelector{}, "", model.ErrZTAPISettlementInvalid
	}
	encoded, err := common.Marshal(value)
	if err != nil || len(encoded) > 8192 {
		return types.ZTAPIImageSelector{}, "", model.ErrZTAPISettlementInvalid
	}
	return types.ZTAPIImageSelector{Size: value.Size, Quality: value.Quality, ResponseFormat: value.ResponseFormat, N: value.N}, string(encoded), nil
}

type ztapiImmutableMediaUsage struct {
	UpstreamRequestID string            `json:"upstream_request_id"`
	ResultCount       int               `json:"result_count"`
	ResultAvailable   bool              `json:"result_available"`
	Pending           bool              `json:"pending"`
	Reason            string            `json:"reason,omitempty"`
	RawUsageJSON      string            `json:"raw_usage_json"`
	SelectedRuleID    string            `json:"selected_rule_id,omitempty"`
	Dimensions        map[string]string `json:"dimensions,omitempty"`
	PriceRuleIDs      map[string]string `json:"price_rule_ids,omitempty"`
}

type ztapiMediaLogDimension struct {
	Dimension    string `json:"dimension"`
	Quantity     int64  `json:"quantity"`
	Unit         string `json:"unit"`
	QuoteState   string `json:"quote_state"`
	UnitPriceUSD string `json:"unit_price_usd"`
}

type ztapiMediaLogMetadata struct {
	BillingSource      string                   `json:"billing_source"`
	BillingStatus      string                   `json:"billing_status"`
	PublicationVersion uint64                   `json:"publication_version"`
	PriceSourceVersion uint64                   `json:"price_source_version"`
	BillingDimensions  []ztapiMediaLogDimension `json:"billing_dimensions"`
}

type ztapiMediaChargePart struct {
	Dimension string
	Units     decimal.Decimal
	UnitQuota decimal.Decimal
	SaleUSD   decimal.Decimal
	Exact     decimal.Decimal
	Charged   int64
}

func FinalizeZTAPIImageSettlement(operationID string, evidence relaycommon.ZTAPIMediaUsageEvidence, attempt int) (*model.ZTAPIRequestSettlement, error) {
	row, frozen, err := loadZTAPIMediaSettlement(operationID)
	if err != nil {
		return nil, err
	}
	usageJSON, err := marshalZTAPIImageEvidence(evidence)
	if err != nil || evidence.Pending || !evidence.ResultAvailable || evidence.ResultCount <= 0 || strings.TrimSpace(evidence.UpstreamRequestID) == "" {
		return nil, model.ErrZTAPISettlementPending
	}
	if !ztapiImageUsageWithinMaximum(evidence.GetDimensions(), frozen.MaximumDimensions) {
		return nil, model.ErrZTAPISettlementPending
	}
	actual, dimensions, logDimensions, err := calculateZTAPIImageCharge(frozen.MediaPriceContractJSON, frozen.QuotaPerUnit, evidence)
	if err != nil || actual > row.InitialReservedQuota {
		return nil, model.ErrZTAPISettlementPending
	}
	dimensionsJSON, err := common.Marshal(dimensions)
	if err != nil {
		return nil, model.ErrZTAPISettlementInvalid
	}
	metadata, err := common.Marshal(ztapiMediaLogMetadata{
		BillingSource:      "wallet",
		BillingStatus:      "settled",
		PublicationVersion: frozen.Version,
		PriceSourceVersion: frozen.PriceSourceVersion,
		BillingDimensions:  logDimensions,
	})
	if err != nil {
		return nil, model.ErrZTAPISettlementInvalid
	}
	logEntry := model.Log{
		Type: model.LogTypeConsume, UserId: row.UserID, TokenId: row.TokenID,
		RequestId: row.RequestID, ModelName: row.PublicModel, Quota: int(actual),
		ChannelId: 0, CreatedAt: row.CreatedAt.Unix(), UpstreamRequestId: evidence.UpstreamRequestID,
		Other: string(metadata),
	}
	var finalAttempt model.ZTAPIRequestAttempt
	if model.DB == nil || model.DB.Where("settlement_id = ? AND attempt = ?", row.ID, attempt).Take(&finalAttempt).Error != nil {
		return nil, model.ErrZTAPISettlementConflict
	}
	logEntry.ChannelId = finalAttempt.ChannelID
	return model.FinalizeZTAPIRequestSettlementWithEvidence(operationID, actual, usageJSON, string(dimensionsJSON), model.ZTAPISettlementEvidence{
		FinalAttempt: attempt,
		ConsumeLog:   logEntry,
	})
}

func PendZTAPIImageSettlement(operationID string, evidence relaycommon.ZTAPIMediaUsageEvidence, reason string) (*model.ZTAPIRequestSettlement, error) {
	return pendZTAPIImageSettlement(operationID, evidence, reason, nil)
}

func pendZTAPIImageSettlement(operationID string, evidence relaycommon.ZTAPIMediaUsageEvidence, reason string, lineage *model.ZTAPIPendingSettlementLineage) (*model.ZTAPIRequestSettlement, error) {
	if strings.TrimSpace(reason) == "" || len(reason) > 256 {
		return nil, model.ErrZTAPISettlementInvalid
	}
	if _, _, err := loadZTAPIMediaSettlement(operationID); err != nil {
		return nil, err
	}
	usageJSON, err := marshalZTAPIImageEvidence(evidence)
	if err != nil {
		return nil, err
	}
	missing, err := common.Marshal([]string{reason})
	if err != nil {
		return nil, model.ErrZTAPISettlementInvalid
	}
	var row *model.ZTAPIRequestSettlement
	if lineage == nil {
		row, err = model.PendZTAPIRequestSettlement(operationID, usageJSON, string(missing))
	} else {
		row, err = model.PendZTAPIRequestSettlementWithLineage(operationID, usageJSON, string(missing), *lineage)
	}
	if err != nil {
		return nil, err
	}
	_, queueErr := model.QueuePendingZTAPIFinanceAlerts(context.Background(), model.DB, time.Now(), 100)
	return row, queueErr
}

func loadZTAPIMediaSettlement(operationID string) (*model.ZTAPIRequestSettlement, ztapiFrozenMediaReservation, error) {
	var row model.ZTAPIRequestSettlement
	var frozen ztapiFrozenMediaReservation
	if model.DB == nil || strings.TrimSpace(operationID) == "" || model.DB.Where("operation_id = ?", operationID).Take(&row).Error != nil {
		return nil, frozen, model.ErrZTAPISettlementInvalid
	}
	if common.RejectDuplicateJsonObjectMembers(strings.NewReader(row.PriceSnapshotJSON)) != nil ||
		common.DecodeJsonStrict(strings.NewReader(row.PriceSnapshotJSON), &frozen) != nil || frozen.Modality != "image" ||
		frozen.PublicName != row.PublicModel || frozen.MaximumQuota != row.InitialReservedQuota || frozen.SelectorJSON == "" {
		return nil, frozen, model.ErrZTAPISettlementConflict
	}
	canonical, err := types.CanonicalizeZTAPIMediaPriceContract(frozen.MediaPriceContractJSON)
	if err != nil || canonical != frozen.MediaPriceContractJSON {
		return nil, frozen, model.ErrZTAPISettlementConflict
	}
	protocol, protocolJSON, err := types.ParseZTAPIImageProtocolContract(frozen.ImageProtocolContractJSON)
	if err != nil || protocolJSON != frozen.ImageProtocolContractJSON || protocol.EvidenceHash != frozen.ProtocolEvidenceHash {
		return nil, frozen, model.ErrZTAPISettlementConflict
	}
	selector, selectorJSON, err := canonicalZTAPIMediaSelector(frozen.SelectorJSON)
	if err != nil || selectorJSON != frozen.SelectorJSON {
		return nil, frozen, model.ErrZTAPISettlementConflict
	}
	quotaPerUnit, err := parseCanonicalZTAPIQuotaPerUnit(frozen.QuotaPerUnit)
	if err != nil {
		return nil, frozen, model.ErrZTAPISettlementConflict
	}
	maximumDimensions, maximumQuota, err := deriveZTAPIImageMaximumReservation(
		frozen.MediaPriceContractJSON, protocol, selector, quotaPerUnit,
	)
	if err != nil || !equalZTAPIStringMaps(maximumDimensions, frozen.MaximumDimensions) || maximumQuota != frozen.MaximumQuota {
		return nil, frozen, model.ErrZTAPISettlementConflict
	}
	return &row, frozen, nil
}

func marshalZTAPIImageEvidence(evidence relaycommon.ZTAPIMediaUsageEvidence) (string, error) {
	dimensions := evidence.GetDimensions()
	encodedDimensions := make(map[string]string, len(dimensions))
	for name, quantity := range dimensions {
		encodedDimensions[name] = quantity.String()
	}
	raw, err := common.Marshal(ztapiImmutableMediaUsage{
		UpstreamRequestID: evidence.UpstreamRequestID,
		ResultCount:       evidence.ResultCount,
		ResultAvailable:   evidence.ResultAvailable,
		Pending:           evidence.Pending,
		Reason:            evidence.Reason,
		RawUsageJSON:      string(evidence.GetRawUsageJSON()),
		SelectedRuleID:    evidence.SelectedRuleID,
		Dimensions:        encodedDimensions,
		PriceRuleIDs:      evidence.GetPriceRuleIDs(),
	})
	if err != nil || len(raw) > 65536 {
		return "", model.ErrZTAPISettlementInvalid
	}
	return string(raw), nil
}

func canonicalZTAPIQuotaPerUnit(value float64) (string, error) {
	raw := strconv.FormatFloat(value, 'f', -1, 64)
	parsed, err := decimal.NewFromString(raw)
	if err != nil || !parsed.IsPositive() {
		return "", model.ErrZTAPISettlementInvalid
	}
	return parsed.String(), nil
}

func parseCanonicalZTAPIQuotaPerUnit(raw string) (decimal.Decimal, error) {
	parsed, err := decimal.NewFromString(raw)
	if err != nil || !parsed.IsPositive() || parsed.String() != raw {
		return decimal.Zero, model.ErrZTAPISettlementInvalid
	}
	return parsed, nil
}

func deriveZTAPIImageMaximumReservation(contractJSON string, protocol types.ZTAPIImageProtocolContract, selector types.ZTAPIImageSelector, quotaPerUnit decimal.Decimal) (map[string]string, int64, error) {
	contract, err := types.ParseZTAPIMediaPriceContract(contractJSON)
	if err != nil || types.ValidateZTAPIImagePriceProtocolCompatibility(contract, protocol) != nil {
		return nil, 0, model.ErrZTAPISettlementInvalid
	}
	authority, ok := protocol.FindReservationAuthority(selector)
	if !ok {
		return nil, 0, model.ErrZTAPISettlementInvalid
	}
	maximumDimensions := make(map[string]string, len(authority.MaximumDimensions))
	for dimension, rawQuantity := range authority.MaximumDimensions {
		quantity, parseErr := strconv.ParseInt(rawQuantity, 10, 64)
		if parseErr != nil || quantity < 0 || strconv.FormatInt(quantity, 10) != rawQuantity {
			return nil, 0, model.ErrZTAPISettlementInvalid
		}
		maximumDimensions[dimension] = rawQuantity
	}
	maximumExact, err := maximumZTAPIImageCharge(contract, maximumDimensions, quotaPerUnit)
	if err != nil {
		return nil, 0, err
	}
	rounded := maximumExact.Round(0)
	if rounded.IsNegative() || rounded.GreaterThan(decimal.NewFromInt(math.MaxInt32)) {
		return nil, 0, model.ErrZTAPISettlementInvalid
	}
	return maximumDimensions, rounded.IntPart(), nil
}

func maximumZTAPIImageCharge(contract types.ZTAPIMediaPriceContract, maximum map[string]string, quotaPerUnit decimal.Decimal) (decimal.Decimal, error) {
	if _, tiered := contract.Rules[0].Conditions["prompt_tokens_tier"]; tiered {
		maximumInput, err := strconv.ParseInt(maximum["input_tokens"], 10, 64)
		if err != nil {
			return decimal.Zero, model.ErrZTAPISettlementInvalid
		}
		best := decimal.Zero
		found := false
		for _, rule := range contract.Rules {
			tier := rule.Conditions["prompt_tokens_tier"]
			if tier == "gt_200k" && maximumInput <= 200000 {
				continue
			}
			quantities := maximum
			if tier == "lte_200k" && maximumInput > 200000 {
				quantities = cloneZTAPIStringMap(maximum)
				quantities["input_tokens"] = "200000"
			}
			exact, chargeErr := exactZTAPIImageRuleCharge(rule, quantities, quotaPerUnit)
			if chargeErr != nil {
				return decimal.Zero, chargeErr
			}
			if !found || exact.GreaterThan(best) {
				best = exact
			}
			found = true
		}
		if !found {
			return decimal.Zero, model.ErrZTAPISettlementInvalid
		}
		return best, nil
	}
	total := decimal.Zero
	for _, rule := range contract.Rules {
		exact, err := exactZTAPIImageRuleCharge(rule, maximum, quotaPerUnit)
		if err != nil {
			return decimal.Zero, err
		}
		total = total.Add(exact)
	}
	return total, nil
}

func exactZTAPIImageRuleCharge(rule types.ZTAPIMediaPriceRule, quantities map[string]string, quotaPerUnit decimal.Decimal) (decimal.Decimal, error) {
	total := decimal.Zero
	for dimension, rawPrice := range rule.SaleUSD {
		rawQuantity, ok := quantities[dimension]
		price, priceErr := decimal.NewFromString(rawPrice)
		quantity, quantityErr := decimal.NewFromString(rawQuantity)
		if !ok || priceErr != nil || quantityErr != nil || price.IsNegative() || quantity.IsNegative() {
			return decimal.Zero, model.ErrZTAPISettlementInvalid
		}
		total = total.Add(quantity.Mul(price).Mul(quotaPerUnit).Div(decimal.NewFromInt(1_000_000)))
	}
	return total, nil
}

func cloneZTAPIStringMap(source map[string]string) map[string]string {
	clone := make(map[string]string, len(source))
	for key, value := range source {
		clone[key] = value
	}
	return clone
}

func ztapiImageUsageWithinMaximum(actual map[string]decimal.Decimal, maximum map[string]string) bool {
	if len(actual) != len(maximum) || len(actual) == 0 {
		return false
	}
	for dimension, rawMaximum := range maximum {
		quantity, ok := actual[dimension]
		maximumValue, err := decimal.NewFromString(rawMaximum)
		if !ok || err != nil || quantity.IsNegative() || !quantity.Equal(decimal.NewFromInt(quantity.IntPart())) || quantity.GreaterThan(maximumValue) {
			return false
		}
	}
	return true
}

func equalZTAPIStringMaps(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		if right[key] != value {
			return false
		}
	}
	return true
}

func calculateZTAPIImageCharge(contractJSON, quotaPerUnitRaw string, evidence relaycommon.ZTAPIMediaUsageEvidence) (int64, []model.ZTAPISupplierRefundDimension, []ztapiMediaLogDimension, error) {
	return calculateZTAPIImageChargeDimensions(contractJSON, quotaPerUnitRaw, evidence.SelectedRuleID, evidence.GetDimensions(), evidence.GetPriceRuleIDs())
}

func calculateZTAPIImageChargeDimensions(contractJSON, quotaPerUnitRaw, selectedRuleID string, dimensions map[string]decimal.Decimal, ruleIDs map[string]string) (int64, []model.ZTAPISupplierRefundDimension, []ztapiMediaLogDimension, error) {
	contract, err := types.ParseZTAPIMediaPriceContract(contractJSON)
	if err != nil {
		return 0, nil, nil, err
	}
	if len(dimensions) == 0 || len(dimensions) != len(ruleIDs) {
		return 0, nil, nil, model.ErrZTAPISettlementInvalid
	}
	rules := make(map[string]types.ZTAPIMediaPriceRule, len(contract.Rules))
	for _, rule := range contract.Rules {
		rules[rule.ID] = rule
	}
	if err := validateZTAPIImageEvidenceRuleIdentity(contract, selectedRuleID, dimensions, ruleIDs); err != nil {
		return 0, nil, nil, err
	}
	quotaPerUnit, err := parseCanonicalZTAPIQuotaPerUnit(quotaPerUnitRaw)
	if err != nil {
		return 0, nil, nil, model.ErrZTAPISettlementInvalid
	}

	names := make([]string, 0, len(dimensions))
	for name := range dimensions {
		names = append(names, name)
	}
	sort.Strings(names)
	parts := make([]ztapiMediaChargePart, 0, len(names))
	totalExact := decimal.Zero
	for _, name := range names {
		quantity := dimensions[name]
		if quantity.IsNegative() || !quantity.Equal(decimal.NewFromInt(quantity.IntPart())) {
			return 0, nil, nil, model.ErrZTAPISettlementInvalid
		}
		ruleID, ok := ruleIDs[name]
		rule, ruleOK := rules[ruleID]
		saleRaw, priceOK := rule.SaleUSD[name]
		if !ok || !ruleOK || !priceOK || (selectedRuleID != "" && selectedRuleID != ruleID) {
			return 0, nil, nil, model.ErrZTAPISettlementInvalid
		}
		sale, parseErr := decimal.NewFromString(saleRaw)
		if parseErr != nil || sale.IsNegative() {
			return 0, nil, nil, model.ErrZTAPISettlementInvalid
		}
		unitQuota := sale.Mul(quotaPerUnit).Div(decimal.NewFromInt(1_000_000))
		exact := quantity.Mul(unitQuota)
		parts = append(parts, ztapiMediaChargePart{Dimension: name, Units: quantity, UnitQuota: unitQuota, SaleUSD: sale, Exact: exact, Charged: exact.Floor().IntPart()})
		totalExact = totalExact.Add(exact)
	}
	total := totalExact.Round(0).IntPart()
	if total < 0 || total > math.MaxInt32 {
		return 0, nil, nil, model.ErrZTAPISettlementInvalid
	}
	allocated := int64(0)
	for _, part := range parts {
		allocated += part.Charged
	}
	ranking := make([]int, len(parts))
	for i := range ranking {
		ranking[i] = i
	}
	sort.SliceStable(ranking, func(i, j int) bool {
		left := parts[ranking[i]].Exact.Sub(parts[ranking[i]].Exact.Floor())
		right := parts[ranking[j]].Exact.Sub(parts[ranking[j]].Exact.Floor())
		if !left.Equal(right) {
			return left.GreaterThan(right)
		}
		return parts[ranking[i]].Dimension < parts[ranking[j]].Dimension
	})
	for i := int64(0); i < total-allocated; i++ {
		parts[ranking[i]].Charged++
	}

	chargeDimensions := make([]model.ZTAPISupplierRefundDimension, 0, len(parts))
	logDimensions := make([]ztapiMediaLogDimension, 0, len(parts))
	for _, part := range parts {
		if part.Units.IsZero() {
			continue
		}
		chargeDimensions = append(chargeDimensions, model.ZTAPISupplierRefundDimension{
			Dimension: part.Dimension, Units: part.Units.String(), UnitQuota: part.UnitQuota.String(), ChargedQuota: part.Charged,
		})
		logDimensions = append(logDimensions, ztapiMediaLogDimension{
			Dimension: part.Dimension, Quantity: part.Units.IntPart(), Unit: "token", QuoteState: "quoted", UnitPriceUSD: part.SaleUSD.String(),
		})
	}
	return total, chargeDimensions, logDimensions, nil
}

func validateZTAPIImageEvidenceRuleIdentity(contract types.ZTAPIMediaPriceContract, selectedRuleID string, dimensions map[string]decimal.Decimal, ruleIDs map[string]string) error {
	if _, tiered := contract.Rules[0].Conditions["prompt_tokens_tier"]; tiered {
		input, ok := dimensions["input_tokens"]
		if !ok || !input.Equal(decimal.NewFromInt(input.IntPart())) {
			return model.ErrZTAPISettlementInvalid
		}
		tier := "lte_200k"
		if input.GreaterThan(decimal.NewFromInt(200000)) {
			tier = "gt_200k"
		}
		expectedRuleID := ""
		for _, rule := range contract.Rules {
			if rule.Conditions["prompt_tokens_tier"] == tier {
				expectedRuleID = rule.ID
				break
			}
		}
		if expectedRuleID == "" || selectedRuleID != expectedRuleID {
			return model.ErrZTAPISettlementInvalid
		}
		for dimension := range dimensions {
			if ruleIDs[dimension] != expectedRuleID {
				return model.ErrZTAPISettlementInvalid
			}
		}
		return nil
	}
	if selectedRuleID != "" {
		return model.ErrZTAPISettlementInvalid
	}
	for dimension := range dimensions {
		ruleID := ruleIDs[dimension]
		matched := false
		for _, rule := range contract.Rules {
			if rule.ID == ruleID && rule.Conditions["token_bucket"] == dimension {
				matched = true
				break
			}
		}
		if !matched {
			return model.ErrZTAPISettlementInvalid
		}
	}
	return nil
}
