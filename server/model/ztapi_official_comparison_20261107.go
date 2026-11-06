package model

import (
	_ "embed"
	"maps"
	"slices"

	"github.com/QuantumNous/new-api/common"
	"github.com/shopspring/decimal"
)

// Frozen public benchmarks are independent of procurement discounts and FX.
//
//go:embed ztapi_official_comparison_20261107.json
var ztapiOfficialComparisonJSON20261107 []byte

type ZTAPIPublicOfficialReference struct {
	URL       string `json:"url"`
	CheckedAt string `json:"checked_at"`
	Kind      string `json:"kind"`
	Note      string `json:"note"`
}

type ztapiOfficialComparison20261107 struct {
	SourceModel string                       `json:"source_model"`
	PublicName  string                       `json:"public_name"`
	Reference   ZTAPIPublicOfficialReference `json:"reference"`
	OfficialUSD map[string]string            `json:"official_usd"`
	TokenRules  []ZTAPIPublicTokenPriceRule  `json:"token_rules"`
	MediaRules  []ZTAPIPublicPricingRule     `json:"media_rules"`
}

func ztapiOfficialComparisons20261107() map[string]ztapiOfficialComparison20261107 {
	var document struct {
		Version int                               `json:"version"`
		Models  []ztapiOfficialComparison20261107 `json:"models"`
	}
	if common.Unmarshal(ztapiOfficialComparisonJSON20261107, &document) != nil || document.Version != 1 {
		return nil
	}
	result := make(map[string]ztapiOfficialComparison20261107, len(document.Models))
	for _, entry := range document.Models {
		if entry.SourceModel == "" || entry.PublicName == "" || entry.Reference.URL == "" {
			return nil
		}
		if _, duplicate := result[entry.SourceModel]; duplicate {
			return nil
		}
		result[entry.SourceModel] = entry
	}
	return result
}

var ztapiOfficialBenchmarks20261107 = ztapiOfficialComparisons20261107()

func ztapiOfficialDimensionsForSale20261107(sale, benchmark map[string]string) map[string]string {
	if len(sale) == 0 {
		return nil
	}
	result := make(map[string]string, len(sale))
	for dimension := range sale {
		raw, found := benchmark[dimension]
		price, err := decimal.NewFromString(raw)
		if !found || err != nil || !price.IsPositive() {
			return nil
		}
		result[dimension] = price.String()
	}
	return result
}

func applyZTAPIOfficialComparison20261107(sourceModel string, item *ZTAPIPublicCatalogItem) {
	entry, found := ztapiOfficialBenchmarks20261107[sourceModel]
	if sourceModel == "deepseek-v4-flash" {
		// An upstream alias is not evidence of the current official SKU.
		item.OfficialUSD = nil
		item.OfficialReference = nil
		return
	}
	if !found || entry.PublicName != item.ModelName {
		return
	}
	item.OfficialUSD, item.OfficialReference = nil, nil
	for i := range item.TokenPriceRules {
		item.TokenPriceRules[i].OfficialUSD = nil
	}
	for i := range item.PricingRules {
		item.PricingRules[i].OfficialUSD = nil
	}
	if len(item.TokenPriceRules) > 0 {
		if len(entry.TokenRules) != len(item.TokenPriceRules) {
			return
		}
		verified := make([]map[string]string, len(item.TokenPriceRules))
		for i, rule := range item.TokenPriceRules {
			if !slices.Equal(rule.Conditions, entry.TokenRules[i].Conditions) {
				return
			}
			comparableSale := rule.SaleUSD
			if sourceModel == "claude-fable-5" {
				comparableSale = copyZTAPIStringMap(rule.SaleUSD)
				// Uniform writes have no confirmed TTL; other dimensions still compare.
				delete(comparableSale, ZTAPIBillingDimensionCacheWrite)
			}
			verified[i] = ztapiOfficialDimensionsForSale20261107(comparableSale, entry.TokenRules[i].OfficialUSD)
			if verified[i] == nil {
				return
			}
		}
		for i := range item.TokenPriceRules {
			item.TokenPriceRules[i].OfficialUSD = verified[i]
		}
	} else if len(item.PricingRules) > 0 {
		verified := make([]map[string]string, len(item.PricingRules))
		for i, rule := range item.PricingRules {
			for _, benchmark := range entry.MediaRules {
				if rule.ID == benchmark.ID && rule.BillingUnit == benchmark.BillingUnit && maps.Equal(rule.Conditions, benchmark.Conditions) {
					verified[i] = ztapiOfficialDimensionsForSale20261107(rule.SaleUSD, benchmark.OfficialUSD)
					break
				}
			}
			if verified[i] == nil {
				return
			}
		}
		for i := range item.PricingRules {
			item.PricingRules[i].OfficialUSD = verified[i]
		}
	} else {
		item.OfficialUSD = ztapiOfficialDimensionsForSale20261107(item.SaleUSD, entry.OfficialUSD)
		if item.OfficialUSD == nil {
			return
		}
	}
	reference := entry.Reference
	if sourceModel == "claude-fable-5" {
		reference.Note = "同型号官网价快照；统一缓存写入没有已确认的 TTL，不展示该项官网折扣。输入、输出和缓存读取分别比较。"
	}
	item.OfficialReference = &reference
}
