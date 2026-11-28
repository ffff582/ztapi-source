package model

import (
	"errors"
	"math"
	"reflect"
	"slices"
	"sort"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/shopspring/decimal"
)

const ZTAPIPricePolicyXcloud20261128 ZTAPIPricePolicy = types.ZTAPIXcloudPricePolicy20261128
const ZTAPIXcloudQuoteObservedAt20261128 int64 = 1795859891

func ztapiXcloudSourceQuotes20261128(source, group string) ([]ZTAPIXcloudQuote20261128, []string, map[string]decimal.Decimal, error) {
	quotes, err := ZTAPIXcloudCatalogQuotes20261128()
	if err != nil {
		return nil, nil, nil, err
	}
	var found []ZTAPIXcloudQuote20261128
	costs := map[string]decimal.Decimal{}
	for _, quote := range quotes {
		if quote.SourceModel != source || quote.Group != group {
			continue
		}
		if !slices.Contains(quote.Endpoints, "openai") {
			return nil, nil, nil, errors.New("Xcloud quote does not declare the selected transport")
		}
		found = append(found, quote)
		for dimension, rate := range quote.CashRates {
			if current, exists := costs[dimension]; !exists || rate.GreaterThan(current) {
				costs[dimension] = rate
			}
		}
	}
	if len(found) == 0 {
		return nil, nil, nil, errors.New("exact Xcloud source and group quote is missing")
	}
	dimensions := make([]string, 0, len(costs))
	for dimension := range costs {
		dimensions = append(dimensions, dimension)
	}
	sort.Strings(dimensions)
	for _, quote := range found {
		if len(quote.CashRates) != len(dimensions) {
			return nil, nil, nil, errors.New("Xcloud tiers have incomplete billing dimensions")
		}
	}
	return found, dimensions, costs, nil
}

func ztapiXcloudTierConditions20261128(quote ZTAPIXcloudQuote20261128) []string {
	if quote.MaxInputTokens == 272000 {
		return []string{"\u8f93\u5165\u957f\u5ea6\u2264272K"}
	}
	if quote.MinInputTokens == 272001 {
		return []string{"\u8f93\u5165\u957f\u5ea6>272K"}
	}
	return []string{}
}

func ztapiXcloudPriorRetail20261128(raw string, quotes []ZTAPIXcloudQuote20261128) ([]ztapiABFrozenSaleRule, error) {
	if raw == "" {
		return nil, nil
	}
	if len(raw) > 64<<10 || common.RejectDuplicateJsonObjectMembers(strings.NewReader(raw)) != nil {
		return nil, errors.New("invalid previous retail evidence")
	}
	var previous []ztapiABFrozenSaleRule
	if common.UnmarshalJsonStr(raw, &previous) != nil || len(previous) == 0 {
		return nil, errors.New("previous retail rules are unavailable")
	}
	global := len(previous) == 1 && len(previous[0].Conditions) == 0
	if !global && len(previous) != len(quotes) {
		return nil, errors.New("previous retail tiers require explicit reconciliation")
	}
	for i, rule := range previous {
		quote := quotes[i]
		if global {
			quote = quotes[0]
		} else if !reflect.DeepEqual(rule.Conditions, ztapiXcloudTierConditions20261128(quote)) {
			return nil, errors.New("previous retail context conditions changed")
		}
		for dimension, raw := range rule.Sale {
			price, err := decimal.NewFromString(raw)
			if _, exists := quote.CashRates[dimension]; !exists || err != nil || price.IsNegative() {
				return nil, errors.New("previous retail dimension does not match the new quote")
			}
		}
	}
	return previous, nil
}

func BuildZTAPIXcloudPriceSource20261128(config ZTAPIModelConfig, group string, operator int, priorRulesJSON string) (ZTAPIModelPriceSource, error) {
	public, provider, protocol, err := ZTAPIXcloudPublicIdentity20261128(config.SourceModel)
	if err != nil || config.ID <= 0 || config.PublicNameValue() != public || config.ProviderFamily != provider || config.Protocol != protocol || operator <= 0 {
		return ZTAPIModelPriceSource{}, errors.New("exact Xcloud catalog identity and operator are required")
	}
	quotes, dimensions, costs, err := ztapiXcloudSourceQuotes20261128(config.SourceModel, group)
	if err != nil {
		return ZTAPIModelPriceSource{}, err
	}
	previous, err := ztapiXcloudPriorRetail20261128(priorRulesJSON, quotes)
	if err != nil {
		return ZTAPIModelPriceSource{}, err
	}
	encodedDimensions, err := common.Marshal(dimensions)
	if err != nil {
		return ZTAPIModelPriceSource{}, err
	}
	source := ZTAPIModelPriceSource{ModelConfigID: config.ID, SourceModel: config.SourceModel, ResourceType: "pool",
		SpendTier: group, BillingDimensions: string(encodedDimensions), Currency: "USD", CNYPerUSD: "0", OperatorID: operator,
		PricePolicy: string(ZTAPIPricePolicyXcloud20261128), SaleMultiplier: "1", SourceDocumentChecksum: ZTAPIXcloudQuoteSHA25620261128,
		QuotationEffectiveAt: ZTAPIXcloudQuoteObservedAt20261128, PlatformCNYPerUnit: "0", UpstreamCNYPerUSD: "0"}
	clearZTAPIPriceSourceCosts(&source)
	for dimension, cost := range costs {
		if err := assignZTAPIPriceSourceCost(&source, dimension, cost); err != nil {
			return source, err
		}
	}
	rules := make([]ztapiABFrozenSaleRule, len(quotes))
	for i, quote := range quotes {
		rules[i] = ztapiABFrozenSaleRule{Conditions: ztapiXcloudTierConditions20261128(quote), Sale: map[string]string{}}
		for dimension, cost := range quote.CashRates {
			retail, err := ZTAPIXcloudRetailFloor20261128(cost)
			if err != nil {
				return source, err
			}
			if len(previous) > 0 {
				index := i
				if len(previous) == 1 {
					index = 0
				}
				if old, exists := previous[index].Sale[dimension]; exists {
					retail = decimal.Max(retail, decimal.RequireFromString(old))
				}
			}
			rules[i].Sale[dimension] = retail.StringFixed(10)
		}
	}
	encodedRules, err := common.Marshal(rules)
	source.TokenPriceRulesJSON = string(encodedRules)
	return source, err
}

func ztapiXcloudKnownZeroCost20261128(source *ZTAPIModelPriceSource, dimension string) bool {
	if source == nil || source.PricePolicy != string(ZTAPIPricePolicyXcloud20261128) {
		return false
	}
	quotes, _, _, err := ztapiXcloudSourceQuotes20261128(source.SourceModel, source.SpendTier)
	if err != nil {
		return false
	}
	for _, quote := range quotes {
		price, exists := quote.CashRates[dimension]
		if !exists || !price.IsZero() {
			return false
		}
	}
	return true
}

func ztapiXcloudRules20261128(source *ZTAPIModelPriceSource) (map[string]decimal.Decimal, error) {
	if source == nil || source.ModelConfigID <= 0 || source.OperatorID <= 0 || source.PricePolicy != string(ZTAPIPricePolicyXcloud20261128) ||
		source.SourceDocumentChecksum != ZTAPIXcloudQuoteSHA25620261128 || source.QuotationEffectiveAt != ZTAPIXcloudQuoteObservedAt20261128 ||
		(source.CreatedAt != 0 && source.CreatedAt < ZTAPIXcloudQuoteObservedAt20261128) ||
		source.ResourceType != "pool" || source.Currency != "USD" || source.CashMarginBaseSourceID != 0 || source.MediaPriceContractJSON != "" ||
		source.FXMode != "" || !ztapiSameDecimal(source.CNYPerUSD, "0") || !ztapiSameDecimal(source.SaleMultiplier, "1") ||
		!ztapiSameDecimal(source.PlatformCNYPerUnit, "0") || !ztapiSameDecimal(source.UpstreamCNYPerUSD, "0") {
		return nil, errors.New("Xcloud price source does not match its dated evidence")
	}
	quotes, dimensions, costs, err := ztapiXcloudSourceQuotes20261128(source.SourceModel, source.SpendTier)
	if err != nil {
		return nil, err
	}
	actualDimensions, err := parseZTAPIBillingDimensions(source.BillingDimensions)
	if err != nil || !reflect.DeepEqual(actualDimensions, dimensions) {
		return nil, errors.New("Xcloud source billing dimensions changed")
	}
	for dimension, value := range ztapiPriceSourceValues(source) {
		if !ztapiSameDecimal(value, costs[dimension].String()) {
			return nil, errors.New("Xcloud frozen purchase rate differs from the quote")
		}
	}
	if len(source.TokenPriceRulesJSON) > 64<<10 || common.RejectDuplicateJsonObjectMembers(strings.NewReader(source.TokenPriceRulesJSON)) != nil {
		return nil, errors.New("invalid Xcloud frozen retail JSON")
	}
	var rules []ztapiABFrozenSaleRule
	if common.UnmarshalJsonStr(source.TokenPriceRulesJSON, &rules) != nil || len(rules) != len(quotes) {
		return nil, errors.New("Xcloud frozen retail tiers are incomplete")
	}
	retail := map[string]decimal.Decimal{}
	for i, rule := range rules {
		if !reflect.DeepEqual(rule.Conditions, ztapiXcloudTierConditions20261128(quotes[i])) || len(rule.Sale) != len(dimensions) {
			return nil, errors.New("Xcloud frozen retail context or dimensions changed")
		}
		for _, dimension := range dimensions {
			price, err := decimal.NewFromString(rule.Sale[dimension])
			floor, floorErr := ZTAPIXcloudRetailFloor20261128(quotes[i].CashRates[dimension])
			if err != nil || floorErr != nil || price.IsNegative() || price.LessThan(floor) {
				return nil, errors.New("Xcloud retail is below the approved cash margin floor")
			}
			if old, exists := retail[dimension]; !exists || price.GreaterThan(old) {
				retail[dimension] = price
			}
		}
	}
	return retail, nil
}

func ZTAPIXcloudChannelGroup20261128(channel int) (string, bool) {
	group, exists := map[int]string{4: "claude-max", 5: "claude-kiro", 6: "new-vip", 7: "gpt-plus", 8: "国产分组"}[channel]
	return group, exists
}

func ValidateZTAPIXcloudRoute20261128(source string, channel int, baseURL, physical string) error {
	group, known := ZTAPIXcloudChannelGroup20261128(channel)
	if !known || physical != source || strings.TrimRight(baseURL, "/") != "https://www.xcloudtoken.com" {
		return errors.New("Xcloud route does not match the exact quoted supplier model")
	}
	_, err := ZTAPIXcloudQuoteFor20261128(source, group, 0)
	return err
}

func ztapiXcloudChannelPhysical20261128(channel *Channel, source, group string) (string, error) {
	if channel == nil || !channel.ZTAPIManaged || channel.Status != common.ChannelStatusEnabled {
		return "", errors.New("Xcloud requires an enabled managed channel")
	}
	actualGroup, known := ZTAPIXcloudChannelGroup20261128(channel.Id)
	if !known || actualGroup != group {
		return "", errors.New("Xcloud channel does not match the frozen quotation group")
	}
	mapping := map[string]string{}
	raw := strings.TrimSpace(channel.GetModelMapping())
	if raw != "" && (common.RejectDuplicateJsonObjectMembers(strings.NewReader(raw)) != nil || common.UnmarshalJsonStr(raw, &mapping) != nil) {
		return "", errors.New("invalid Xcloud model mapping")
	}
	physical := source
	seen := map[string]bool{}
	for mapping[physical] != "" && mapping[physical] != physical {
		if seen[physical] {
			return "", errors.New("cyclic Xcloud model mapping")
		}
		seen[physical] = true
		physical = mapping[physical]
	}
	return physical, ValidateZTAPIXcloudRoute20261128(source, channel.Id, channel.GetBaseURL(), physical)
}

func ztapiValidateXcloudSnapshot20261128(source *ZTAPIModelPriceSource, snapshot *ZTAPIModelPublicationSnapshot) error {
	if source == nil || snapshot == nil {
		return errors.New("missing Xcloud source or publication")
	}
	public, provider, protocol, err := ZTAPIXcloudPublicIdentity20261128(source.SourceModel)
	channels := snapshot.ChannelIDs()
	if err != nil || len(channels) != 1 || snapshot.PriceSourceID != source.ID || snapshot.ModelConfigID != source.ModelConfigID ||
		snapshot.SourceModel != source.SourceModel || snapshot.PublicName != public || snapshot.ProviderFamily != provider || snapshot.Protocol != protocol ||
		snapshot.PricePolicy != string(ZTAPIPricePolicyXcloud20261128) || snapshot.TokenPriceRulesJSON != source.TokenPriceRulesJSON ||
		snapshot.CacheWritePricingMode != "" || !ztapiSameDecimal(snapshot.SaleMultiplier, "1") {
		return errors.New("Xcloud publication does not match its immutable quotation and identity")
	}
	group, known := ZTAPIXcloudChannelGroup20261128(channels[0])
	if !known || group != source.SpendTier || ValidateZTAPIXcloudRoute20261128(source.SourceModel, channels[0], "https://www.xcloudtoken.com", source.SourceModel) != nil {
		return errors.New("Xcloud publication does not match its exact supplier group")
	}
	preview, err := BuildZTAPIModelPricePreview(source)
	if err != nil {
		return err
	}
	if !ztapiStoredPriceMatchesPreview(snapshot.InputPricePerMillion, preview.InputSaleUSDPerMillion) ||
		!ztapiStoredPriceMatchesPreview(snapshot.OutputPricePerMillion, preview.OutputSaleUSDPerMillion) ||
		!ztapiCacheRatiosMatchPreview(&ZTAPIModelConfig{CacheReadRatio: snapshot.CacheReadRatio, CacheCreationRatio: snapshot.CacheCreationRatio,
			CacheCreation5mRatio: snapshot.CacheCreation5mRatio, CacheCreation1hRatio: snapshot.CacheCreation1hRatio}, preview) {
		return errors.New("Xcloud publication prices do not match the frozen price source")
	}
	return nil
}

func ZTAPIXcloudQuotedCost20261128(source *ZTAPIModelPriceSource, public string, channels []int, channel int, baseURL string, usage map[string]int64) (decimal.Decimal, error) {
	if _, err := ztapiXcloudRules20261128(source); err != nil {
		return decimal.Zero, err
	}
	expectedPublic, _, _, err := ZTAPIXcloudPublicIdentity20261128(source.SourceModel)
	group, known := ZTAPIXcloudChannelGroup20261128(channel)
	if err != nil || expectedPublic != public || !known || group != source.SpendTier || !slices.Equal(channels, []int{channel}) ||
		strings.TrimRight(baseURL, "/") != "https://www.xcloudtoken.com" || len(usage) == 0 {
		return decimal.Zero, errors.New("Xcloud source does not match the actual model and route")
	}
	var prompt int64
	for dimension, quantity := range usage {
		if quantity < 0 {
			return decimal.Zero, errors.New("negative Xcloud supplier usage")
		}
		if dimension != ZTAPIBillingDimensionOutputTokens {
			if quantity > math.MaxInt64-prompt {
				return decimal.Zero, errors.New("Xcloud prompt usage overflow")
			}
			prompt += quantity
		}
	}
	quote, err := ZTAPIXcloudQuoteFor20261128(source.SourceModel, group, prompt)
	if err != nil {
		return decimal.Zero, err
	}
	cost := decimal.Zero
	for dimension, quantity := range usage {
		rate, quoted := quote.CashRates[dimension]
		if !quoted && quantity != 0 {
			return decimal.Zero, errors.New("unquoted Xcloud supplier usage dimension")
		}
		cost = cost.Add(rate.Mul(decimal.NewFromInt(quantity)).Shift(-6))
	}
	return cost, nil
}
