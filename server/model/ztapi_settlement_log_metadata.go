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
	BillingSource      string                        `json:"billing_source,omitempty"`
	BillingStatus      string                        `json:"billing_status,omitempty"`
	PublicationVersion uint64                        `json:"publication_version,omitempty"`
	PriceSourceVersion uint64                        `json:"price_source_version,omitempty"`
	UsageSemantic      string                        `json:"usage_semantic,omitempty"`
	BillingDimensions  []ztapiSettlementLogDimension `json:"billing_dimensions,omitempty"`
	ModelRatio         *float64                      `json:"model_ratio,omitempty"`
}

var ztapiSettlementLogDimensionName = regexp.MustCompile(`^[a-z][a-z0-9_:.-]{0,127}$`)

func sanitizeZTAPISettlementLogMetadata(log *Log) error {
	var metadata ztapiSettlementLogMetadata
	if log.Other != "" {
		if err := common.UnmarshalJsonStr(log.Other, &metadata); err != nil {
			return ErrZTAPISettlementLogInvalid
		}
	}
	if (metadata.BillingSource != "" && metadata.BillingSource != "wallet") ||
		(metadata.BillingStatus != "" && metadata.BillingStatus != "settled") ||
		(metadata.UsageSemantic != "" && metadata.UsageSemantic != "openai" && metadata.UsageSemantic != "anthropic") ||
		len(metadata.BillingDimensions) > 128 {
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
