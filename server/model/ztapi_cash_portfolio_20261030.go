package model

import (
	"errors"
	"fmt"
	"reflect"

	"github.com/QuantumNous/new-api/common"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

// These are the selected claude-max group quotes, already including its 1.5x
// factor. Owner-confirmed pay-100/get-200 funding applies 0.5 exactly once.
var ztapiCashXcloudGroupQuotes20261030 = map[string]map[string]string{
	"claude-fable-5":  {"input_tokens": "10.05", "output_tokens": "50.25", "cache_read": "0.75", "cache_write_5m": "12.45", "cache_write_1h": "20.25"},
	"claude-sonnet-5": {"input_tokens": "1.5", "output_tokens": "10.5", "cache_read": "0.225", "cache_write_5m": "2.85", "cache_write_1h": "4.05"},
}

var ztapiCashSonnetOriginalSale20261030 = map[string]string{
	"input_tokens": "1.5600000000", "output_tokens": "7.8000000000", "cache_read": "0.1560000000",
	"cache_write_5m": "1.9500000000", "cache_write_1h": "3.1200000000",
}

func validateZTAPICashMarginBase20261030(db *gorm.DB, base *ZTAPIModelPriceSource) error {
	contract, err := ztapiCashContractFor20261030(base.SourceModel)
	if err != nil || base.PricePolicy != string(contract.BasePolicy) {
		return errors.New("cash margin requires its exact approved original policy")
	}
	if contract.Image {
		return validateZTAPICashImageBase20261030(base)
	}
	if contract.Extended && contract.BasePolicy != ZTAPIPricePolicyQuoteFrozenRetail20261019 {
		_, err := ztapiCashABSourceTiers20261030(base)
		return err
	}
	return ValidateZTAPISupplierPriceSource20261019(db, base)
}

func reconstructZTAPICashPortfolioBase20261030(base *ZTAPIModelPriceSource) error {
	if base.SourceModel != "claude-sonnet-5" {
		original, err := ztapiCashRebuildABBase20261030(base)
		if err != nil {
			return err
		}
		base.TokenPriceRulesJSON = original.TokenPriceRulesJSON
		return nil
	}
	raw, err := common.Marshal([]ztapiABFrozenSaleRule{{Conditions: []string{}, Sale: copyZTAPIStringMap(ztapiCashSonnetOriginalSale20261030), RetailBasisID: 1}})
	if err == nil {
		base.TokenPriceRulesJSON = string(raw)
	}
	return err
}

func ztapiCashSupplierCostTiers20261030(base *ZTAPIModelPriceSource) ([]ztapiCashBasisTier20261030, error) {
	entry, err := ztapiSupplierPriceEntryFor20261019(base.SourceModel)
	if err != nil {
		return nil, err
	}
	fx, err := ztapiABFXParamsFromSource(base)
	if err != nil || !fx.PlatformRate.IsPositive() || (entry.Currency == "USD" && !fx.UpstreamUSDRate.IsPositive()) {
		return nil, errors.New("missing frozen procurement FX")
	}
	maximum := map[string]decimal.Decimal{}
	tiers := make([]ztapiCashBasisTier20261030, 0, len(entry.Rules))
	for _, quoted := range entry.Rules {
		costs := map[string]decimal.Decimal{}
		for dimension, raw := range quoted.Official {
			cost := decimal.RequireFromString(raw).Mul(decimal.RequireFromString(entry.Fraction)).Round(10)
			if entry.Currency == "USD" {
				cost = cost.Mul(fx.UpstreamUSDRate).Round(10)
			}
			costs[dimension] = cost
			if cost.GreaterThan(maximum[dimension]) {
				maximum[dimension] = cost
			}
		}
		tiers = append(tiers, ztapiCashBasisTier20261030{Conditions: append([]string{}, quoted.Conditions...), CostCNY: costs})
	}
	for dimension, raw := range ztapiPriceSourceValues(base) {
		cost, err := decimal.NewFromString(raw)
		if err != nil || !cost.Equal(maximum[dimension]) {
			return nil, fmt.Errorf("portfolio %s procurement differs from canonical quotation", dimension)
		}
	}
	return tiers, nil
}

func ztapiCashPortfolioCostTiers20261030(base *ZTAPIModelPriceSource) ([]ztapiCashBasisTier20261030, error) {
	if base.SourceModel == "claude-sonnet-5" {
		if _, _, err := ztapiSupplierFrozenRules20261019(base); err != nil {
			return nil, err
		}
		var original []ztapiABFrozenSaleRule
		if err := common.Unmarshal([]byte(base.TokenPriceRulesJSON), &original); err != nil || len(original) != 1 {
			return nil, errors.New("Sonnet original retail must have its approved single tier")
		}
		for dimension, expected := range ztapiCashSonnetOriginalSale20261030 {
			if !ztapiSameDecimal(original[0].Sale[dimension], expected) {
				return nil, errors.New("Sonnet original retail differs from the approved baseline")
			}
		}
		return ztapiCashSupplierCostTiers20261030(base)
	}
	return ztapiCashABSourceTiers20261030(base)
}

func deriveZTAPICashPortfolioSource20261030(base *ZTAPIModelPriceSource, operator int) (ZTAPIModelPriceSource, error) {
	contract, err := ztapiCashContractFor20261030(base.SourceModel)
	if err != nil || !contract.Extended || base.ID <= 0 || operator <= 0 || base.PricePolicy != string(contract.BasePolicy) ||
		base.CashMarginBaseSourceID != 0 || base.ResourceType != contract.Resource || base.Currency != "CNY" ||
		base.FXMode != ZTAPIFXModePlatformV1 || base.MediaPriceContractJSON != "" || !ztapiSameDecimal(base.SaleMultiplier, "1") {
		return ZTAPIModelPriceSource{}, errors.New("portfolio margin requires an unchanged approved procurement base")
	}
	tiers, err := ztapiCashPortfolioCostTiers20261030(base)
	if err != nil {
		return ZTAPIModelPriceSource{}, err
	}
	var original []ztapiABFrozenSaleRule
	if base.TokenPriceRulesJSON == "" && len(tiers) == 1 && len(tiers[0].CostCNY) == 1 && tiers[0].CostCNY[ZTAPIBillingDimensionInputTokens].IsPositive() {
		// Original embedding contracts intentionally store no tier rules.
		preview, err := BuildZTAPIModelPricePreview(base)
		if err != nil {
			return ZTAPIModelPriceSource{}, err
		}
		original = []ztapiABFrozenSaleRule{{Conditions: []string{}, Sale: copyZTAPIStringMap(preview.SaleUSD)}}
	} else if err := common.Unmarshal([]byte(base.TokenPriceRulesJSON), &original); err != nil {
		return ZTAPIModelPriceSource{}, errors.New("original retail tiers are incomplete")
	}
	if len(original) != len(tiers) {
		return ZTAPIModelPriceSource{}, errors.New("original retail tiers are incomplete")
	}
	fx, err := decimal.NewFromString(base.CNYPerUSD)
	if err != nil || !fx.IsPositive() {
		return ZTAPIModelPriceSource{}, errors.New("portfolio platform FX is missing")
	}
	rules := make([]ztapiABFrozenSaleRule, 0, len(tiers))
	for index, tier := range tiers {
		if !reflect.DeepEqual(tier.Conditions, original[index].Conditions) || len(tier.CostCNY) != len(original[index].Sale) {
			return ZTAPIModelPriceSource{}, errors.New("portfolio cost and retail conditions or dimensions differ")
		}
		rule := ztapiABFrozenSaleRule{Conditions: append([]string{}, tier.Conditions...), RetailBasisID: base.ID, Sale: map[string]string{}}
		for dimension, originalCost := range tier.CostCNY {
			coveredCost := originalCost
			if quote, mixed := ztapiCashXcloudGroupQuotes20261030[base.SourceModel]; mixed {
				raw, ok := quote[dimension]
				if !ok {
					return ZTAPIModelPriceSource{}, errors.New("missing selected Xcloud group dimension")
				}
				cashCNY := decimal.RequireFromString(raw).Mul(decimal.RequireFromString("0.5")).Mul(fx)
				if cashCNY.GreaterThan(coveredCost) {
					coveredCost = cashCNY
				}
			}
			sale := decimal.RequireFromString(ztapiCashSale20261030(coveredCost, fx, contract.Margin, 8))
			previous, err := decimal.NewFromString(original[index].Sale[dimension])
			if err != nil || !previous.IsPositive() {
				return ZTAPIModelPriceSource{}, errors.New("original retail dimension is invalid")
			}
			if base.SourceModel == "claude-fable-5" && (dimension == ZTAPIBillingDimensionCacheWrite5m || dimension == ZTAPIBillingDimensionCacheWrite1h) {
				previous = decimal.Max(previous, decimal.RequireFromString(ZTAPIFableUniformCacheWritePrice))
			}
			rule.Sale[dimension] = decimal.Max(sale, previous.RoundCeil(8)).StringFixed(10)
		}
		if base.SourceModel == "claude-fable-5" {
			uniform := decimal.Max(decimal.RequireFromString(rule.Sale[ZTAPIBillingDimensionCacheWrite5m]), decimal.RequireFromString(rule.Sale[ZTAPIBillingDimensionCacheWrite1h])).StringFixed(10)
			rule.Sale[ZTAPIBillingDimensionCacheWrite5m], rule.Sale[ZTAPIBillingDimensionCacheWrite1h] = uniform, uniform
		}
		rules = append(rules, rule)
	}
	raw, err := common.Marshal(rules)
	if err != nil {
		return ZTAPIModelPriceSource{}, err
	}
	target := *base
	target.ID, target.Version, target.CreatedAt = 0, 0, 0
	target.PricePolicy, target.SaleMultiplier = string(contract.Policy), "1.0000000000"
	target.CashMarginBaseSourceID, target.OperatorID, target.TokenPriceRulesJSON = base.ID, operator, string(raw)
	return target, nil
}

func ztapiCashCoveredCostUSD20261030(base *ZTAPIModelPriceSource) (map[string]string, error) {
	tiers, err := ztapiCashPortfolioCostTiers20261030(base)
	if err != nil {
		return nil, err
	}
	maximum := map[string]decimal.Decimal{}
	fx := decimal.RequireFromString(base.CNYPerUSD)
	for _, tier := range tiers {
		for dimension, raw := range tier.CostCNY {
			cost := raw.Div(fx)
			if quoted, ok := ztapiCashXcloudGroupQuotes20261030[base.SourceModel][dimension]; ok {
				cost = decimal.Max(cost, decimal.RequireFromString(quoted).Mul(decimal.RequireFromString("0.5")))
			}
			maximum[dimension] = decimal.Max(maximum[dimension], cost)
		}
	}
	result := map[string]string{}
	for dimension, cost := range maximum {
		result[dimension] = cost.StringFixed(10)
	}
	return result, nil
}

func validateZTAPICashSnapshotMode20261030(source *ZTAPIModelPriceSource, snapshot *ZTAPIModelPublicationSnapshot) error {
	if source.SourceModel == "claude-fable-5" && isZTAPICashMarginPolicy20261030(source.PricePolicy) {
		if snapshot.CacheWritePricingMode != ZTAPIFableCashFloorCacheWriteMode {
			return errors.New("cash-margin Fable requires its frozen uniform cache-write mode")
		}
	} else if snapshot.CacheWritePricingMode == ZTAPIFableCashFloorCacheWriteMode {
		return errors.New("cash-floor cache mode requires the derived Fable price source")
	}
	return nil
}
