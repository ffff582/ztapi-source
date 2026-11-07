package service

import (
	"math"
	"regexp"
	"sort"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
	"github.com/tidwall/gjson"
)

const ztapiBillingDimensionEvidenceLimit = 32
const ztapiBillingDimensionEvidenceBytes = 16384

var ztapiBillingEvidencePricePattern = regexp.MustCompile(`^[0-9]+(?:\.[0-9]+)?$`)

type ztapiBillingDimensionEvidence struct {
	Version int `json:"version"`
	relaycommon.ZTAPIUsageDimensionResult
	Reasons           []string `json:"reasons,omitempty"`
	OmittedDimensions int      `json:"omitted_dimensions,omitempty"`
}

func ztapiBillingEvidenceDimension(name string) bool {
	switch name {
	case "input_tokens", "output_tokens", "total_tokens", "cache_read", "cache_write", "cache_write_5m", "cache_write_1h",
		"image_input_tokens", "image_output_tokens", "audio_input_tokens", "audio_output_tokens",
		"web_search", "web_search_preview", "file_search", "responses_hosted_usage_unconfirmed":
		return true
	}
	if len(name) > 96 {
		return false
	}
	parts := strings.Split(name, ":")
	if len(parts) != 3 || parts[0] != "image_generation" {
		return false
	}
	switch parts[1] {
	case "", "auto", "low", "medium", "high", "standard", "hd":
	default:
		return false
	}
	switch parts[2] {
	case "", "auto", "256x256", "512x512", "1024x1024", "1536x1024", "1024x1536", "1792x1024", "1024x1792":
		return true
	}
	return false
}

func ztapiBillingEvidenceReason(reason string) bool {
	if ztapiBillingEvidenceDimension(reason) || len(types.SanitizeZTAPIUsageIntegrityReasons([]string{reason})) != 0 {
		return true
	}
	switch reason {
	case "usage_semantic", "usage_zero_unconfirmed", "upstream_usage_missing", "upstream_billing_unconfirmed",
		"invalid_frozen_price", "token_tier_invalid", "charge_calculation_invalid", "dispatch_evidence_missing",
		"attempt_evidence_conflict", "settlement_retry_required", "missing_quote", "invalid_quantity", "invalid_unit",
		"invalid_quote", "unrecognized_dimension", "evidence_truncated", "duplicate_dimension":
		return true
	}
	return false
}

func sanitizeZTAPIBillingEvidenceReasons(reasons []string) []string {
	seen := map[string]bool{}
	for _, reason := range reasons {
		if !ztapiBillingEvidenceReason(reason) {
			reason = "unrecognized_dimension"
		}
		seen[reason] = true
	}
	truncated := len(seen) > ztapiBillingDimensionEvidenceLimit
	if truncated {
		delete(seen, "evidence_truncated")
	}
	safe := make([]string, 0, len(seen))
	for reason := range seen {
		safe = append(safe, reason)
	}
	sort.Strings(safe)
	if truncated {
		safe = append(safe[:ztapiBillingDimensionEvidenceLimit-1], "evidence_truncated")
		sort.Strings(safe)
	}
	return safe
}

func sanitizeZTAPIBillingDimensionEvidence(check relaycommon.ZTAPIUsageDimensionResult, reasons []string) *ztapiBillingDimensionEvidence {
	evidence := &ztapiBillingDimensionEvidence{Version: 1, ZTAPIUsageDimensionResult: check}
	evidence.MissingDimensions = sanitizeZTAPIBillingEvidenceReasons(check.MissingDimensions)
	evidence.InvalidDimensions = sanitizeZTAPIBillingEvidenceReasons(check.InvalidDimensions)
	evidence.Dimensions = nil
	evidence.Reasons = append([]string{}, reasons...)
	if len(check.MissingDimensions) > 0 {
		evidence.Reasons = append(evidence.Reasons, "missing_quote")
	}
	if check.UsageMissing {
		evidence.Reasons = append(evidence.Reasons, "upstream_usage_missing")
	}
	if check.UsageSemantic != "" && check.UsageSemantic != "openai" && check.UsageSemantic != "anthropic" {
		evidence.UsageSemantic = "invalid"
	}
	seen := map[string]bool{}
	for _, dimension := range check.Dimensions {
		if !ztapiBillingEvidenceDimension(dimension.Dimension) {
			evidence.OmittedDimensions++
			evidence.Reasons = append(evidence.Reasons, "unrecognized_dimension")
			continue
		}
		if seen[dimension.Dimension] {
			evidence.OmittedDimensions++
			evidence.Reasons = append(evidence.Reasons, "duplicate_dimension")
			continue
		}
		seen[dimension.Dimension] = true
		if len(evidence.Dimensions) >= ztapiBillingDimensionEvidenceLimit {
			evidence.OmittedDimensions++
			evidence.Reasons = append(evidence.Reasons, "evidence_truncated")
			continue
		}
		if dimension.Quantity <= 0 || dimension.Quantity > math.MaxInt64/8 {
			evidence.Reasons = append(evidence.Reasons, "invalid_quantity")
		}
		switch dimension.Unit {
		case "token", "call", "image":
		default:
			dimension.Unit = "unknown"
			evidence.Reasons = append(evidence.Reasons, "invalid_unit")
		}
		if dimension.QuoteState != relaycommon.ZTAPIQuoteStateMissing {
			valid := (dimension.QuoteState == relaycommon.ZTAPIQuoteStateQuoted || dimension.QuoteState == relaycommon.ZTAPIQuoteStateFree) &&
				len(dimension.UnitPriceUSD) <= 64 && ztapiBillingEvidencePricePattern.MatchString(dimension.UnitPriceUSD)
			if valid {
				price, err := decimal.NewFromString(dimension.UnitPriceUSD)
				valid = err == nil && ((dimension.QuoteState == relaycommon.ZTAPIQuoteStateQuoted && price.IsPositive()) ||
					(dimension.QuoteState == relaycommon.ZTAPIQuoteStateFree && price.IsZero()))
			}
			if !valid {
				dimension.QuoteState = relaycommon.ZTAPIQuoteStateMissing
				evidence.Reasons = append(evidence.Reasons, "invalid_quote")
			}
		}
		if dimension.QuoteState == relaycommon.ZTAPIQuoteStateMissing {
			dimension.UnitPriceUSD = ""
		}
		evidence.Dimensions = append(evidence.Dimensions, dimension)
	}
	evidence.Reasons = sanitizeZTAPIBillingEvidenceReasons(evidence.Reasons)
	for len(evidence.Dimensions) > 0 {
		raw, err := common.Marshal(evidence)
		if err == nil && len(raw) <= ztapiBillingDimensionEvidenceBytes {
			break
		}
		evidence.Dimensions = evidence.Dimensions[:len(evidence.Dimensions)-1]
		evidence.OmittedDimensions++
		evidence.Reasons = sanitizeZTAPIBillingEvidenceReasons(append(evidence.Reasons, "evidence_truncated"))
	}
	return evidence
}

func ztapiFrozenUsageDimensions(c *gin.Context, info *relaycommon.RelayInfo, usage *dto.Usage, frozenJSON string) relaycommon.ZTAPIUsageDimensionResult {
	var snapshot relaycommon.ZTAPIPublicationSnapshot
	selected := &relaycommon.ZTAPIPublicationSnapshot{}
	reason := ""
	if err := common.UnmarshalJsonStr(frozenJSON, &snapshot); err != nil {
		reason = "invalid_frozen_price"
	} else if usage == nil && strings.TrimSpace(snapshot.TokenPriceRulesJSON) == "" {
		selected = &snapshot
	} else if tier, _, err := relaycommon.SelectZTAPIFrozenTokenTier(&snapshot, usage); err == nil {
		selected = tier
	} else {
		reason = "token_tier_invalid"
	}
	check := relaycommon.ValidateZTAPIUsageDimensions(c, ztapiUsageValidationInfo(info, selected), usage)
	if reason != "" {
		check.InvalidDimensions = append(check.InvalidDimensions, reason)
		check.Pending = true
	}
	return check
}

func ztapiMeteringSnapshot(info *relaycommon.RelayInfo, usage *dto.Usage) *relaycommon.ZTAPIMeteringEvidence {
	if info == nil || info.ZTAPIMetering == nil {
		return nil
	}
	evidence := *info.ZTAPIMetering
	if usage != nil && usage.UsageSource != "" {
		evidence.UsageSource = usage.UsageSource
	} else if evidence.UsageSource == "" {
		evidence.UsageSource = "unverified"
		if usage == nil {
			evidence.UsageSource = "missing"
		}
	}
	return evidence.Sanitized()
}

func marshalZTAPIBillingUsage(info *relaycommon.RelayInfo, usage any) ([]byte, error) {
	return marshalZTAPIBillingUsageWithDimensions(info, usage, nil, nil)
}

func marshalZTAPIBillingUsageWithDimensions(info *relaycommon.RelayInfo, usage any, check *relaycommon.ZTAPIUsageDimensionResult, reasons []string) ([]byte, error) {
	textUsage, isText := usage.(*dto.Usage)
	if value, ok := usage.(dto.Usage); ok {
		textUsage, isText = &value, true
	}
	if usage == nil && check != nil {
		isText = true
	}
	if !isText || info == nil || (info.ZTAPIMetering == nil && check == nil) {
		return common.Marshal(usage)
	}
	evidence := ztapiMeteringSnapshot(info, textUsage)
	if textUsage != nil {
		copyUsage := *textUsage
		textUsage = &copyUsage
		if cost, err := common.Marshal(copyUsage.Cost); err != nil || len(cost) > 64 || gjson.ParseBytes(cost).Type != gjson.Number {
			textUsage.Cost = nil
		}
		switch textUsage.UsageSemantic {
		case "", "openai", "anthropic":
		default:
			textUsage.UsageSemantic = "invalid"
		}
		if evidence != nil {
			textUsage.UsageSource = evidence.UsageSource
		} else {
			switch textUsage.UsageSource {
			case "", "upstream_reported", "upstream_incomplete", "local_estimate", "missing", "unverified":
			default:
				textUsage.UsageSource = "unverified"
			}
		}
	}
	var dimensions *ztapiBillingDimensionEvidence
	if check != nil {
		dimensions = sanitizeZTAPIBillingDimensionEvidence(*check, reasons)
	}
	// Keep normalized usage compatible with existing replay/refund readers, and
	// persist the independently captured counters without changing client JSON.
	return common.Marshal(struct {
		*dto.Usage
		Evidence   *relaycommon.ZTAPIMeteringEvidence `json:"ztapi_metering_evidence"`
		Dimensions *ztapiBillingDimensionEvidence     `json:"ztapi_billing_dimension_evidence,omitempty"`
	}{textUsage, evidence, dimensions})
}
