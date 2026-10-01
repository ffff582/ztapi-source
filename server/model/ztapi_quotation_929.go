package model

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/shopspring/decimal"
)

// ZTAPIQuotation929SHA256 identifies the 2026-09-29 quotation workbook.  It
// is intentionally separate from the 2026-09-15 manifest so an old price
// source cannot masquerade as the latest upstream quote.
const ZTAPIQuotation929SHA256 = "9a3071db812218cdd1c9cc814cc9047f35697f3a7e30de3f0cda93601c3421d4"

type ztapi929TextQuote struct {
	ModelName         string
	SourceModel       string
	PublicName        string
	ProviderFamily    string
	ModelCode         string
	QuotationCell     string
	OfficialPriceCell string
	Cost              map[string]string
}

var ztapi929TextQuotes = map[string]ztapi929TextQuote{
	"Claude Opus 5": {
		ModelName: "Claude Opus 5", SourceModel: "claude-opus-5", PublicName: "zt-claude-opus-5",
		ProviderFamily: ZTAPIProviderAnthropic, ModelCode: "zq-c-o5（hc）", QuotationCell: "D44",
		OfficialPriceCell: "E44",
		Cost: map[string]string{
			ZTAPIBillingDimensionInputTokens:  "2.2",
			ZTAPIBillingDimensionOutputTokens: "11",
			ZTAPIBillingDimensionCacheRead:    "0.22",
			ZTAPIBillingDimensionCacheWrite5m: "2.75",
			ZTAPIBillingDimensionCacheWrite1h: "4.4",
		},
	},
}

func BuildZTAPI929QuotedModelPriceSource(modelName string, modelConfigID, operatorID int, effectiveAt int64) (ZTAPIModelPriceSource, error) {
	if modelConfigID <= 0 || operatorID <= 0 || effectiveAt <= 0 {
		return ZTAPIModelPriceSource{}, errors.New("quotation source requires model, operator and effective time")
	}
	quote, ok := ztapi929TextQuotes[modelName]
	if !ok {
		return ZTAPIModelPriceSource{}, fmt.Errorf("2026-09-29 quotation has no implemented exact price basis for %q", modelName)
	}

	dimensions := make([]string, 0, len(quote.Cost))
	for dimension := range quote.Cost {
		dimensions = append(dimensions, dimension)
	}
	sort.Strings(dimensions)
	rules := []ztapiABFrozenSaleRule{{Conditions: []string{}, Sale: map[string]string{}}}
	for _, dimension := range dimensions {
		cost := decimal.RequireFromString(quote.Cost[dimension])
		rules[0].Sale[dimension] = cost.Div(decimal.RequireFromString("0.70")).Round(10).StringFixed(10)
	}
	rulesJSON, err := json.Marshal(rules)
	if err != nil {
		return ZTAPIModelPriceSource{}, err
	}
	dimensionsJSON, err := json.Marshal(dimensions)
	if err != nil {
		return ZTAPIModelPriceSource{}, err
	}

	source := ZTAPIModelPriceSource{
		ModelConfigID: modelConfigID, SourceModel: quote.SourceModel,
		ResourceType: "pool", PricePolicy: string(ZTAPIPricePolicyPool30Margin), SpendTier: "B",
		TokenPriceRulesJSON: string(rulesJSON), BillingDimensions: string(dimensionsJSON), Currency: "USD",
		InputPerMillion:        quote.Cost[ZTAPIBillingDimensionInputTokens],
		OutputPerMillion:       quote.Cost[ZTAPIBillingDimensionOutputTokens],
		CacheReadPerMillion:    quote.Cost[ZTAPIBillingDimensionCacheRead],
		CacheWrite5mPerMillion: quote.Cost[ZTAPIBillingDimensionCacheWrite5m],
		CacheWrite1hPerMillion: quote.Cost[ZTAPIBillingDimensionCacheWrite1h],
		CacheWritePerMillion:   "0",
		ImageUnitCost:          "0", AudioUnitCost: "0", RequestUnitCost: "0", CNYPerUSD: "0",
		PlatformCNYPerUnit: "0", UpstreamCNYPerUSD: "0", QuotationEffectiveAt: effectiveAt,
		SourceDocumentChecksum: ZTAPIQuotation929SHA256, QuotationGrade: "B",
		QuotationCell: quote.QuotationCell, OfficialPriceCell: quote.OfficialPriceCell,
		QuotationModelCode: quote.ModelCode, OperatorID: operatorID,
	}
	if err := ValidateZTAPIModelPriceSource(&source); err != nil {
		return ZTAPIModelPriceSource{}, err
	}
	return source, nil
}

func validateZTAPI929PriceSource(source *ZTAPIModelPriceSource) error {
	if source == nil || source.SourceDocumentChecksum != ZTAPIQuotation929SHA256 {
		return errors.New("2026-09-29 quotation source checksum is invalid")
	}
	var quote ztapi929TextQuote
	ok := false
	for _, candidate := range ztapi929TextQuotes {
		if candidate.SourceModel == source.SourceModel {
			quote = candidate
			ok = true
			break
		}
	}
	if !ok {
		return errors.New("2026-09-29 quotation source has no exact model identity")
	}
	expected, err := BuildZTAPI929QuotedModelPriceSource(quote.ModelName, source.ModelConfigID, source.OperatorID, source.QuotationEffectiveAt)
	if err != nil {
		return err
	}
	if field := ztapiABPriceSourceEvidenceMismatch(source, &expected); field != "" {
		return fmt.Errorf("2026-09-29 quotation source %s does not match the exact workbook", field)
	}
	return nil
}
