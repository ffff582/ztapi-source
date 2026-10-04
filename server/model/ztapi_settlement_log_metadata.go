package model

import (
	"math"
	"regexp"

	"github.com/QuantumNous/new-api/common"
	"github.com/shopspring/decimal"
)

type ztapiSettlementLogDimension struct {
	Dimension    string `json:"dimension"`
	Quantity     int64  `json:"quantity"`
	Unit         string `json:"unit"`
	QuoteState   string `json:"quote_state"`
	UnitPriceUSD string `json:"unit_price_usd"`
}

type ztapiSettlementLogMetadata struct {
	BillingSource            string                        `json:"billing_source,omitempty"`
	BillingStatus            string                        `json:"billing_status,omitempty"`
	PublicationVersion       uint64                        `json:"publication_version,omitempty"`
	PriceSourceVersion       uint64                        `json:"price_source_version,omitempty"`
	UsageSemantic            string                        `json:"usage_semantic,omitempty"`
	BillingDimensions        []ztapiSettlementLogDimension `json:"billing_dimensions,omitempty"`
	ModelRatio               *float64                      `json:"model_ratio,omitempty"`
	ReasoningEffortReceived  string                        `json:"reasoning_effort_received,omitempty"`
	ReasoningEffortForwarded string                        `json:"reasoning_effort_forwarded,omitempty"`
	ReasoningEffortSource    string                        `json:"reasoning_effort_source,omitempty"`
	ReasoningTokensReported  *int                          `json:"reasoning_tokens_reported,omitempty"`
}

var ztapiSettlementLogDimensionName = regexp.MustCompile(`^[a-z][a-z0-9_:.-]{0,127}$`)

func validZTAPIReasoningEffort(value string) bool {
	switch value {
	case "", "none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra":
		return true
	default:
		return false
	}
}

func validZTAPIReasoningSource(value string) bool {
	switch value {
	case "", "request", "model_suffix", "parameter_override", "default", "codex_alias_mapping":
		return true
	default:
		return false
	}
}

func sanitizeZTAPISettlementLogMetadata(log *Log) error {
	var metadata ztapiSettlementLogMetadata
	if log.Other != "" {
		if err := common.UnmarshalJsonStr(log.Other, &metadata); err != nil {
			return ErrZTAPISettlementLogInvalid
		}
	}
	if (metadata.BillingSource != "" && metadata.BillingSource != "wallet") ||
		(metadata.BillingStatus != "" && metadata.BillingStatus != "settled") ||
		(metadata.UsageSemantic != "" && metadata.UsageSemantic != "openai" && metadata.UsageSemantic != "anthropic" && metadata.UsageSemantic != ZTAPIAttemptBillingUsageSemanticImage) ||
		len(metadata.BillingDimensions) > 128 ||
		!validZTAPIReasoningEffort(metadata.ReasoningEffortReceived) ||
		!validZTAPIReasoningEffort(metadata.ReasoningEffortForwarded) ||
		!validZTAPIReasoningSource(metadata.ReasoningEffortSource) ||
		(metadata.ReasoningTokensReported != nil && *metadata.ReasoningTokensReported < 0) {
		return ErrZTAPISettlementLogInvalid
	}
	if metadata.ModelRatio != nil && (math.IsNaN(*metadata.ModelRatio) || math.IsInf(*metadata.ModelRatio, 0) || *metadata.ModelRatio < 0) {
		return ErrZTAPISettlementLogInvalid
	}
	for _, dimension := range metadata.BillingDimensions {
		price, err := decimal.NewFromString(dimension.UnitPriceUSD)
		if !ztapiSettlementLogDimensionName.MatchString(dimension.Dimension) || dimension.Quantity < 0 ||
			(dimension.Unit != "token" && dimension.Unit != "call" && dimension.Unit != "image") || err != nil || price.IsNegative() ||
			(dimension.QuoteState != "quoted" && dimension.QuoteState != "free") ||
			(dimension.QuoteState == "free" && !price.IsZero()) || (dimension.QuoteState == "quoted" && !price.IsPositive()) {
			return ErrZTAPISettlementLogInvalid
		}
	}
	encoded, err := common.Marshal(metadata)
	if err != nil {
		return err
	}
	log.Other = string(encoded)
	log.Content = "managed usage settlement"
	log.ChannelName = ""
	return nil
}
