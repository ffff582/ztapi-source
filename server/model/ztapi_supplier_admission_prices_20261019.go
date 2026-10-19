package model

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/shopspring/decimal"
)

type ztapiSupplierAdmissionPrice20261019 struct {
	SourceModel string
	Code        string
	Cell        string
	Fraction    string
	// GPT: input, cache read, generic write, output. Claude: input, cache
	// read, 5m write, 1h write, output. Empty entries are unquoted, not free.
	Tiers [][]string
}

// Called after the relay resolves every mapping hop, using the request's
// frozen price policy rather than a refreshed catalog or mutable route.
func ValidateZTAPISupplierAdmissionRoute20261019(sourceModel string, channelID int, upstreamModel string) error {
	name, _, found := ztapiSupplierAdmissionPriceForSource20261019(sourceModel)
	candidate, err := FindZTAPISupplierAdmissionModel20261019(name)
	if !found || err != nil || candidate.SourceModel != sourceModel || candidate.UpstreamModel == "" ||
		candidate.RequiredChannelID != channelID || candidate.UpstreamModel != upstreamModel {
		return errors.New("supplier_admission_physical_model_route_mismatch")
	}
	return nil
}

// Standard first-party rates frozen on 2026-10-19, not Batch/Flex/Fast rates.
// https://developers.openai.com/api/docs/pricing
// https://platform.claude.com/docs/en/about-claude/pricing
// This is price evidence only; discovery, protocol and metering gates remain mandatory.
var ztapiSupplierAdmissionPrices20261019 = map[string]ztapiSupplierAdmissionPrice20261019{
	"GPT 6.1 Sol":       {"gpt-6.1-sol", "zq-g61-s（hc）", "E18", "0.28", [][]string{{"2", "0.10", "2.5", "10"}, {"4", "0.20", "5", "15"}}},
	"GPT 6 Sol":         {"gpt-6-sol", "zq-g6-s（hc）", "E19", "0.28", [][]string{{"2", "0.20", "2.5", "10"}, {"4", "0.40", "5", "15"}}},
	"GPT 6 Luna":        {"gpt-6-luna", "zq-g6-l（hc）", "E20", "0.28", [][]string{{"0.10", "0.01", "0.125", "0.50"}, {"0.20", "0.02", "0.25", "0.75"}}},
	"GPT 6 Astra":       {"gpt-6-astra", "zq-g6-a（hc）", "E21", "0.28", [][]string{{"10", "1", "12.5", "50"}, {"20", "2", "25", "75"}}},
	"GPT 5.6 Terra":     {"gpt-5.6-terra", "zq-g56-t（hc）", "E22", "0.28", [][]string{{"2", "0.20", "2.5", "12"}, {"4", "0.40", "5", "18"}}},
	"GPT 5.6 Sol":       {"gpt-5.6-sol", "zq-g56-s（hc）", "E23", "0.28", [][]string{{"4", "0.40", "5", "20"}, {"8", "0.80", "10", "30"}}},
	"GPT 5.6 Luna":      {"gpt-5.6-luna", "zq-g56-l（hc）", "E24", "0.28", [][]string{{"0.20", "0.02", "0.25", "1.20"}, {"0.40", "0.04", "0.50", "1.80"}}},
	"GPT 5.5":           {"gpt-5.5", "zq-g55（hc）", "E25", "0.28", [][]string{{"5", "0.50", "", "30"}, {"10", "1", "", "45"}}},
	"GPT 5.4":           {"gpt-5.4", "zq-g54（hc）", "E26", "0.28", [][]string{{"2.5", "0.25", "", "15"}, {"5", "0.50", "", "22.50"}}},
	"Claude Sonnet 5.5": {"claude-sonnet-5-5", "zq-c-s55（hc）", "E28", "0.40", [][]string{{"2", "0.10", "2.5", "4", "10"}}},
	"Claude Opus 5.5":   {"claude-opus-5-5", "zq-c-o55（hc）", "E30", "0.40", [][]string{{"4", "0.20", "5", "8", "20"}}},
}

func ztapiSupplierAdmissionPriceForSource20261019(source string) (string, ztapiSupplierAdmissionPrice20261019, bool) {
	for name, entry := range ztapiSupplierAdmissionPrices20261019 {
		if entry.SourceModel == source {
			return name, entry, true
		}
	}
	return "", ztapiSupplierAdmissionPrice20261019{}, false
}

// BuildZTAPISupplierAdmissionTextPriceSource20261019 is deliberately pure: no
// DB writes, channel edits, probes or publication are performed by a preview.
func BuildZTAPISupplierAdmissionTextPriceSource20261019(name string, configID, operatorID int, effectiveAt int64, fx ZTAPIABFXParams) (ZTAPIModelPriceSource, error) {
	entry, found := ztapiSupplierAdmissionPrices20261019[name]
	if !found {
		return ZTAPIModelPriceSource{}, ErrZTAPIQuotationModelNotQuoted
	}
	if configID <= 0 || operatorID <= 0 || effectiveAt <= 0 ||
		fx.Mode != ZTAPIFXModePlatformV1 || !ztapiFXRateInRange(fx.PlatformRate.Round(10)) || !fx.UpstreamUSDRate.Round(10).IsPositive() {
		return ZTAPIModelPriceSource{}, errors.New("supplier admission requires identity, operator, effective time and frozen platform/upstream FX")
	}
	fx.PlatformRate, fx.UpstreamUSDRate = fx.PlatformRate.Round(10), fx.UpstreamUSDRate.Round(10)
	source := ZTAPIModelPriceSource{
		ModelConfigID: configID, SourceModel: entry.SourceModel,
		ResourceType: "pool", PricePolicy: string(ZTAPIPricePolicySupplierAdmission20261019),
		SpendTier: "B", QuotationGrade: "B", QuotationCell: entry.Cell, QuotationModelCode: entry.Code,
		Currency: "CNY", FXMode: ZTAPIFXModePlatformV1,
		CNYPerUSD: fx.PlatformRate.StringFixed(10), PlatformCNYPerUnit: fx.PlatformRate.StringFixed(10),
		UpstreamCNYPerUSD: fx.UpstreamUSDRate.StringFixed(10), SaleMultiplier: "1.0000000000",
		SourceDocumentChecksum: ZTAPISupplierWorkbook20261019SHA256,
		QuotationEffectiveAt:   effectiveAt, OperatorID: operatorID,
		ImageUnitCost: "0", AudioUnitCost: "0", RequestUnitCost: "0",
	}
	dimensions := []string{ZTAPIBillingDimensionInputTokens, ZTAPIBillingDimensionCacheRead, ZTAPIBillingDimensionCacheWrite, ZTAPIBillingDimensionOutputTokens}
	if strings.HasPrefix(entry.SourceModel, "claude-") {
		dimensions = []string{ZTAPIBillingDimensionInputTokens, ZTAPIBillingDimensionCacheRead, ZTAPIBillingDimensionCacheWrite5m, ZTAPIBillingDimensionCacheWrite1h, ZTAPIBillingDimensionOutputTokens}
	}
	maxCost := map[string]decimal.Decimal{}
	var rules []ztapiABFrozenSaleRule
	for tier, rates := range entry.Tiers {
		rule := ztapiABFrozenSaleRule{Conditions: []string{}, Sale: map[string]string{}}
		if len(entry.Tiers) == 2 {
			rule.Conditions = []string{"输入长度≤272K"}
			if tier == 1 {
				rule.Conditions = []string{"输入长度>272K"}
			}
		}
		if len(rates) != len(dimensions) {
			return ZTAPIModelPriceSource{}, errors.New("supplier admission dimensions differ from official prices")
		}
		for i, raw := range rates {
			if raw == "" {
				continue
			}
			official, err := decimal.NewFromString(raw)
			if err != nil || !official.IsPositive() {
				return ZTAPIModelPriceSource{}, errors.New("invalid official admission price")
			}
			cost := official.Mul(decimal.RequireFromString(entry.Fraction)).Mul(fx.UpstreamUSDRate).Round(10)
			sale := official.Mul(decimal.RequireFromString("0.78")).Round(10)
			if sale.LessThan(cost.Div(fx.PlatformRate).Round(10).Mul(ztapiMinimumMarginMultiplier)) {
				return ZTAPIModelPriceSource{}, fmt.Errorf("admission %s tier %d %s is below minimum margin", name, tier, dimensions[i])
			}
			rule.Sale[dimensions[i]] = sale.StringFixed(10)
			if cost.GreaterThan(maxCost[dimensions[i]]) {
				maxCost[dimensions[i]] = cost
			}
		}
		rules = append(rules, rule)
	}
	var billed []string
	for dimension := range maxCost {
		billed = append(billed, dimension)
	}
	sort.Strings(billed)
	dimensionsJSON, err := common.Marshal(billed)
	if err != nil {
		return ZTAPIModelPriceSource{}, err
	}
	rulesJSON, err := common.Marshal(rules)
	if err != nil {
		return ZTAPIModelPriceSource{}, err
	}
	source.BillingDimensions, source.TokenPriceRulesJSON = string(dimensionsJSON), string(rulesJSON)
	source.InputPerMillion = maxCost[ZTAPIBillingDimensionInputTokens].StringFixed(10)
	source.OutputPerMillion = maxCost[ZTAPIBillingDimensionOutputTokens].StringFixed(10)
	source.CacheReadPerMillion = maxCost[ZTAPIBillingDimensionCacheRead].StringFixed(10)
	source.CacheWritePerMillion = maxCost[ZTAPIBillingDimensionCacheWrite].StringFixed(10)
	source.CacheWrite5mPerMillion = maxCost[ZTAPIBillingDimensionCacheWrite5m].StringFixed(10)
	source.CacheWrite1hPerMillion = maxCost[ZTAPIBillingDimensionCacheWrite1h].StringFixed(10)
	return source, nil
}

func ztapiSupplierAdmissionFrozenRules20261019(source *ZTAPIModelPriceSource) ([]ztapiABFrozenSaleRule, map[string]decimal.Decimal, error) {
	if source == nil || source.PricePolicy != string(ZTAPIPricePolicySupplierAdmission20261019) {
		return nil, nil, errors.New("supplier admission policy is missing")
	}
	name, _, found := ztapiSupplierAdmissionPriceForSource20261019(source.SourceModel)
	if !found {
		return nil, nil, ErrZTAPIQuotationModelNotQuoted
	}
	fx, err := ztapiABFXParamsFromSource(source)
	if err != nil {
		return nil, nil, err
	}
	expected, err := BuildZTAPISupplierAdmissionTextPriceSource20261019(name, source.ModelConfigID, source.OperatorID, source.QuotationEffectiveAt, fx)
	if err != nil {
		return nil, nil, err
	}
	if mismatch := ztapiABPriceSourceEvidenceMismatch(source, &expected); mismatch != "" ||
		!ztapiSameDecimal(source.SaleMultiplier, expected.SaleMultiplier) || source.MediaPriceContractJSON != "" {
		return nil, nil, errors.New("supplier admission price differs from exact quotation and first-party evidence: " + mismatch)
	}
	var rules []ztapiABFrozenSaleRule
	if err := common.Unmarshal([]byte(expected.TokenPriceRulesJSON), &rules); err != nil {
		return nil, nil, err
	}
	maxSale := map[string]decimal.Decimal{}
	for _, rule := range rules {
		for dimension, raw := range rule.Sale {
			price := decimal.RequireFromString(raw)
			if price.GreaterThan(maxSale[dimension]) {
				maxSale[dimension] = price
			}
		}
	}
	return rules, maxSale, nil
}
