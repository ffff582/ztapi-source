package model

import (
	"errors"
	"math/big"
	"reflect"
	"slices"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/shopspring/decimal"
)

const ZTAPIPricePolicyXcloud20261114 ZTAPIPricePolicy = types.ZTAPIXcloudPricePolicy20261114
const ztapiXcloudEvidence20261114 = "8228ef012920100ba179100034279bdc555ae10e4a37dea863d81cfbf7072fcb"

type ztapiXcloudCohortQuote20261114 struct {
	id, primary, backup int
	source, public      string
	family, group       string
	backupModel         string
	dimensions          []string
	groupedCredits      []string
	previousRetail      []string
	backupCell          string
	longCredits         []string
	longRetail          []string
}

var ztapiXcloudDraftClaude20261114 = map[string]struct {
	id, backup   int
	public, cell string
}{
	"claude-opus-4-6":   {50, 2, "zt-claude-opus-4.6", "E34"},
	"claude-opus-4-7":   {51, 2, "zt-claude-opus-4.7", "E33"},
	"claude-opus-4-8":   {52, 2, "zt-claude-opus-4.8", "E32"},
	"claude-sonnet-4-6": {54, 2, "zt-claude-sonnet-4.6", "E29"},
}

func ztapiXcloudQuote20261114(source string) (ztapiXcloudCohortQuote20261114, error) {
	dimensions := []string{ZTAPIBillingDimensionInputTokens, ZTAPIBillingDimensionOutputTokens, ZTAPIBillingDimensionCacheRead}
	claudeDimensions := append(dimensions[:len(dimensions):len(dimensions)], ZTAPIBillingDimensionCacheWrite5m, ZTAPIBillingDimensionCacheWrite1h)
	if draft, ok := ztapiXcloudDraftClaude20261114[source]; ok {
		q := ztapiXcloudCohortQuote20261114{id: draft.id, primary: 4, backup: draft.backup, source: source, public: draft.public,
			family: ZTAPIProviderAnthropic, group: "claude-max", backupModel: source + "-hc", backupCell: draft.cell,
			dimensions: claudeDimensions, groupedCredits: []string{"5", "25", "0.5", "6.25", "10"}, previousRetail: []string{"3.9", "19.5", "0.39", "4.875", "7.8"}}
		switch source {
		case "claude-sonnet-4-6":
			q.groupedCredits = []string{"3", "15", "0.3", "3.75", "6"}
			q.previousRetail = []string{"2.34", "11.7", "0.234", "2.925", "4.68"}
		}
		return q, nil
	}
	switch source {
	case "claude-haiku-4-5-20251001":
		return ztapiXcloudCohortQuote20261114{49, 4, 0, source, "zt-claude-haiku-4.5", ZTAPIProviderAnthropic, "claude-max", "",
			claudeDimensions, []string{"1", "5", "0.1", "1.25", "2"}, []string{"0.78", "3.9", "0.078", "0.975", "1.56"}, "", nil, nil}, nil
	case "claude-fable-5-1":
		return ztapiXcloudCohortQuote20261114{97, 4, 0, source, "zt-claude-fable-5-1", ZTAPIProviderAnthropic, "claude-max", "",
			claudeDimensions, []string{"10", "50", "0.25", "12.5", "20"}, []string{"8.19", "40.95", "0.39", "7.7206", "12.3530"}, "", nil, nil}, nil
	case "claude-sonnet-5":
		return ztapiXcloudCohortQuote20261114{55, 4, 2, source, "zt-claude-sonnet-5", ZTAPIProviderAnthropic, "claude-max", "claude-sonnet-5-hc",
			claudeDimensions, []string{"2", "10", "0.2", "2.5", "4"}, []string{"1.0018", "6.4853", "0.139", "1.7603", "2.5015"}, "", nil, nil}, nil
	case "claude-opus-5-5":
		return ztapiXcloudCohortQuote20261114{126, 6, 2, source, "zt-claude-opus-5.5", ZTAPIProviderAnthropic, "new-vip", source + "-hc",
			claudeDimensions, []string{"7.2", "36", "0.36", "9", "14.4"}, []string{"3.12", "15.6", "0.156", "3.9", "6.24"}, "E30", nil, nil}, nil
	case "claude-sonnet-5-5":
		return ztapiXcloudCohortQuote20261114{128, 6, 2, source, "zt-claude-sonnet-5.5", ZTAPIProviderAnthropic, "new-vip", source + "-hc",
			claudeDimensions, []string{"3.6", "3.6", "0.18", "4.5", "7.2"}, []string{"1.56", "7.8", "0.078", "1.95", "3.12"}, "E28", nil, nil}, nil
	case "deepseek-v4.1-flash":
		return ztapiXcloudCohortQuote20261114{156, 8, 0, source, "zt-deepseek-v4.1-flash", ZTAPIProviderDeepSeek, "cn", "",
			dimensions, []string{"0.084", "0.3", "0.0018"}, []string{"0.06552", "0.234", "0.00234"}, "", nil, nil}, nil
	case "gpt-6-sol":
		return ztapiXcloudCohortQuote20261114{136, 6, 2, source, "zt-gpt-6-sol", ZTAPIProviderOpenAI, "new-vip", source + "-hc",
			append(dimensions, ZTAPIBillingDimensionCacheWrite), []string{"3.6", "18", "0.36", "4.5"}, []string{"1.56", "7.8", "0.156", "1.95"}, "E19", nil, []string{"3.12", "11.7", "0.312", "3.9"}}, nil
	case "gpt-6-astra":
		return ztapiXcloudCohortQuote20261114{103, 6, 2, source, "zt-gpt-6-astra", ZTAPIProviderOpenAI, "new-vip", source + "-hc",
			append(dimensions, ZTAPIBillingDimensionCacheWrite), []string{"18", "90", "1.8", "22.5"}, []string{"7.8", "39", "0.78", "9.75"}, "E21", []string{"36", "135", "3.6", "45"}, []string{"15.6", "58.5", "1.56", "19.5"}}, nil
	case "gpt-5.6-sol":
		return ztapiXcloudCohortQuote20261114{82, 7, 2, source, "zt-gpt-5.6-sol", ZTAPIProviderOpenAI, "gpt-plus", source + "-hc",
			append(dimensions, ZTAPIBillingDimensionCacheWrite), []string{"5", "30", "5", "6.25"}, []string{"3.12", "15.6", "3.12", "3.9"}, "E23", nil, []string{"6.24", "23.4", "6.24", "7.8"}}, nil
	case "gpt-5.6-terra":
		return ztapiXcloudCohortQuote20261114{84, 7, 2, source, "zt-gpt-5.6-terra", ZTAPIProviderOpenAI, "gpt-plus", source + "-hc",
			append(dimensions, ZTAPIBillingDimensionCacheWrite), []string{"2.5", "15", "2.5", "3.125"}, []string{"1.56", "9.36", "1.56", "1.95"}, "E22", nil, []string{"3.12", "14.04", "3.12", "3.9"}}, nil
	default:
		return ztapiXcloudCohortQuote20261114{}, errors.New("model is outside the verified Xcloud cohort")
	}
}

func (q ztapiXcloudCohortQuote20261114) channels() []int {
	if q.backup != 0 {
		return []int{q.backup, q.primary}
	}
	return []int{q.primary}
}

func ValidateZTAPIXcloudRoute20261114(source string, channelID int, baseURL, physical string) error {
	q, err := ztapiXcloudQuote20261114(source)
	if err != nil {
		return err
	}
	baseURL = strings.TrimRight(baseURL, "/")
	if (channelID == q.primary && baseURL == "https://www.xcloudtoken.com" && physical == source) ||
		(q.backup != 0 && channelID == q.backup && baseURL == "https://ai.yunxinapi.com/hub" && physical == q.backupModel) {
		return nil
	}
	return errors.New("xcloud quotation does not match the channel and physical model")
}

func ztapiXcloudChannelPhysical20261114(channel *Channel, source string) (string, error) {
	if !channel.ZTAPIManaged || channel.Status != common.ChannelStatusEnabled {
		return "", errors.New("Xcloud cohort requires an enabled managed channel")
	}
	mapping := map[string]string{}
	raw := strings.TrimSpace(channel.GetModelMapping())
	if raw != "" && common.UnmarshalJsonStr(raw, &mapping) != nil {
		return "", errors.New("invalid model mapping for Xcloud cohort")
	}
	current := source
	seen := map[string]bool{}
	for mapping[current] != "" && mapping[current] != current {
		if seen[current] {
			return "", errors.New("cyclic model mapping for Xcloud cohort")
		}
		seen[current] = true
		current = mapping[current]
	}
	return current, ValidateZTAPIXcloudRoute20261114(source, channel.Id, channel.GetBaseURL(), current)
}

func buildZTAPIXcloudSource20261114(source string, operator int) (ZTAPIModelPriceSource, error) {
	return ZTAPIXcloudPriceEvidence20261114(source, operator)
}

// ZTAPIXcloudPriceEvidence20261114 constructs the deterministic quote, not a publication.
func ZTAPIXcloudPriceEvidence20261114(source string, operator int) (ZTAPIModelPriceSource, error) {
	q, err := ztapiXcloudQuote20261114(source)
	if err != nil || operator <= 0 {
		return ZTAPIModelPriceSource{}, errors.New("verified Xcloud model and operator required")
	}
	dimensions, err := common.Marshal(q.dimensions)
	if err != nil {
		return ZTAPIModelPriceSource{}, err
	}
	target := ZTAPIModelPriceSource{
		ModelConfigID: q.id, SourceModel: q.source, ResourceType: "pool",
		PricePolicy: string(ZTAPIPricePolicyXcloud20261114), SaleMultiplier: "1",
		SpendTier: q.group, BillingDimensions: string(dimensions), Currency: "USD", CNYPerUSD: "0",
		QuotationEffectiveAt: 1794593243, SourceDocumentChecksum: ztapiXcloudEvidence20261114, OperatorID: operator,
	}
	clearZTAPIPriceSourceCosts(&target)
	rules := []ztapiABFrozenSaleRule{{Conditions: []string{}, Sale: map[string]string{}}}
	if len(q.longRetail) != 0 {
		rules[0].Conditions = []string{"\u8f93\u5165\u957f\u5ea6\u2264272K"}
		rules = append(rules, ztapiABFrozenSaleRule{Conditions: []string{"\u8f93\u5165\u957f\u5ea6>272K"}, Sale: map[string]string{}})
	}
	costs := make(map[string]decimal.Decimal, len(q.dimensions))
	for tier := range rules {
		var backupCosts map[string]*big.Rat
		if q.backup != 0 {
			backupCosts, err = ztapiXcloudBackupTierCosts20261114(q, tier)
			if err != nil {
				return target, err
			}
		}
		credits, retail := q.groupedCredits, q.previousRetail
		if tier > 0 {
			retail = q.longRetail
			if len(q.longCredits) != 0 {
				credits = q.longCredits
			}
		}
		for i, dimension := range q.dimensions {
			cash := decimal.RequireFromString(credits[i]).Mul(decimal.RequireFromString("0.5"))
			ceiling := cash.Rat()
			if q.backup != 0 {
				backup := backupCosts[dimension]
				if backup == nil || backup.Sign() <= 0 {
					return target, errors.New("missing verified Yunxin backup price dimension")
				}
				if backup.Cmp(ceiling) > 0 {
					ceiling = backup
				}
			}
			floor, err := ztapiReductionSale20261107(ceiling, "0.15")
			if err != nil {
				return target, err
			}
			rules[tier].Sale[dimension] = decimal.Max(decimal.RequireFromString(floor), decimal.RequireFromString(retail[i])).StringFixed(10)
			if tier == 0 || cash.GreaterThan(costs[dimension]) {
				costs[dimension] = cash
			}
		}
	}
	for dimension, cost := range costs {
		if err := assignZTAPIPriceSourceCost(&target, dimension, cost); err != nil {
			return target, err
		}
	}
	rawRules, err := common.Marshal(rules)
	target.TokenPriceRulesJSON = string(rawRules)
	return target, err
}

func ztapiXcloudBackupCosts20261114(q ztapiXcloudCohortQuote20261114) (map[string]*big.Rat, error) {
	return ztapiXcloudBackupTierCosts20261114(q, 0)
}

func ztapiXcloudBackupTierCosts20261114(q ztapiXcloudCohortQuote20261114, tier int) (map[string]*big.Rat, error) {
	costs := make(map[string]*big.Rat, len(q.dimensions))
	admissionClaude := q.source == "claude-opus-5-5" || q.source == "claude-sonnet-5-5"
	if len(q.longRetail) != 0 || admissionClaude {
		_, row, found := ztapiSupplierAdmissionPriceForSource20261019(q.source)
		dimensions := []string{ZTAPIBillingDimensionInputTokens, ZTAPIBillingDimensionCacheRead, ZTAPIBillingDimensionCacheWrite, ZTAPIBillingDimensionOutputTokens}
		tierCount := 2
		if admissionClaude {
			tierCount = 1
			dimensions = []string{ZTAPIBillingDimensionInputTokens, ZTAPIBillingDimensionCacheRead, ZTAPIBillingDimensionCacheWrite5m, ZTAPIBillingDimensionCacheWrite1h, ZTAPIBillingDimensionOutputTokens}
		}
		if !found || q.backup != 2 || row.Cell != q.backupCell || len(row.Tiers) != tierCount || tier < 0 || tier >= len(row.Tiers) || len(row.Tiers[tier]) != len(dimensions) {
			return nil, errors.New("Yunxin tiered quotation changed")
		}
		fraction, ok := new(big.Rat).SetString(row.Fraction)
		if !ok || fraction.Sign() <= 0 || fraction.Cmp(big.NewRat(1, 1)) > 0 {
			return nil, errors.New("invalid Yunxin tiered purchase fraction")
		}
		for i, dimension := range dimensions {
			price, ok := new(big.Rat).SetString(row.Tiers[tier][i])
			if !ok || price.Sign() <= 0 {
				return nil, errors.New("invalid Yunxin tiered purchase price")
			}
			costs[dimension] = price.Mul(price, fraction).Mul(price, big.NewRat(67208, 66300))
		}
		return costs, nil
	}
	if q.backupCell == "" {
		_, _, _, rows, err := ztapiReductionApproval20261107(q.id)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			if row.SourceModel == q.source && row.Tier == "1" {
				if err := checkXcloudBackupQuote20261114(row); err != nil {
					return nil, err
				}
				costs[row.Dimension], _ = new(big.Rat).SetString(row.YunxinCost)
			}
		}
		return costs, nil
	}
	row, err := ztapiSupplierPriceEntryFor20261019(q.source)
	if err != nil {
		return nil, err
	}
	if q.backup != 2 || row.QuotationCell != q.backupCell || row.Grade != "B" || row.Currency != "USD" || len(row.Rules) != 1 || len(row.Rules[0].Conditions) != 0 {
		return nil, errors.New("Yunxin pool quotation changed")
	}
	fraction, _ := new(big.Rat).SetString(row.Fraction)
	// Use the same cash conversion as the frozen Yunxin purchase evidence.
	fx := big.NewRat(67208, 66300)
	for _, dimension := range q.dimensions {
		price, ok := new(big.Rat).SetString(row.Rules[0].Official[dimension])
		if !ok || price.Sign() <= 0 || fraction == nil || fraction.Sign() <= 0 {
			return nil, errors.New("missing verified Yunxin backup price dimension")
		}
		costs[dimension] = price.Mul(price, fraction).Mul(price, fx)
	}
	return costs, nil
}

func checkXcloudBackupQuote20261114(row ZTAPIPriceReductionDimension20261107) error {
	if !reflect.DeepEqual(row.Channels, []int{2, 4}) {
		return errors.New("Yunxin backup quotation does not bind the approved routes")
	}
	// This checks the old immutable quote, including its original Xcloud
	// comparison. Only the Yunxin half is reused for the new retail floor.
	_, err := ztapiReductionRowCost20261107(row)
	return err
}

func ztapiXcloudRules20261114(source *ZTAPIModelPriceSource) (map[string]decimal.Decimal, error) {
	expected, err := buildZTAPIXcloudSource20261114(source.SourceModel, source.OperatorID)
	if err != nil {
		return nil, err
	}
	if source.ModelConfigID != expected.ModelConfigID || source.CashMarginBaseSourceID != 0 || source.MediaPriceContractJSON != "" ||
		source.QuotationEffectiveAt != expected.QuotationEffectiveAt || !ztapiSameDecimal(source.SaleMultiplier, "1") ||
		ztapiABPriceSourceEvidenceMismatch(source, &expected) != "" {
		return nil, errors.New("Xcloud source differs from the approved quote and price")
	}
	var rules []ztapiABFrozenSaleRule
	if err := common.UnmarshalJsonStr(expected.TokenPriceRulesJSON, &rules); err != nil {
		return nil, err
	}
	sale := make(map[string]decimal.Decimal, len(rules[0].Sale))
	for _, rule := range rules {
		for dimension, value := range rule.Sale {
			price := decimal.RequireFromString(value)
			if old, found := sale[dimension]; !found || price.GreaterThan(old) {
				sale[dimension] = price
			}
		}
	}
	return sale, nil
}

// ZTAPIXcloudQuotedCost20261114 prices the actual channel using only the
// approved immutable price source, never the retail rate or latest catalog.
func ZTAPIXcloudQuotedCost20261114(source *ZTAPIModelPriceSource, public string, channels []int, channel int, baseURL string, usage map[string]int64) (decimal.Decimal, error) {
	if source == nil {
		return decimal.Zero, errors.New("missing frozen Xcloud source")
	}
	q, err := ztapiXcloudQuote20261114(source.SourceModel)
	if err != nil || public != q.public || !reflect.DeepEqual(channels, q.channels()) || len(usage) == 0 {
		return decimal.Zero, errors.New("Xcloud quote does not match the frozen route set")
	}
	if _, err := ztapiXcloudRules20261114(source); err != nil {
		return decimal.Zero, err
	}
	baseURL = strings.TrimRight(baseURL, "/")
	backup := q.backup != 0 && channel == q.backup && baseURL == "https://ai.yunxinapi.com/hub"
	if !backup && (channel != q.primary || baseURL != "https://www.xcloudtoken.com") {
		return decimal.Zero, errors.New("Xcloud quote does not match the actual supplier")
	}
	tier := 0
	if len(q.longRetail) != 0 {
		prompt := decimal.NewFromInt(usage[ZTAPIBillingDimensionInputTokens]).Add(decimal.NewFromInt(usage[ZTAPIBillingDimensionCacheRead])).Add(decimal.NewFromInt(usage[ZTAPIBillingDimensionCacheWrite]))
		if prompt.GreaterThan(decimal.NewFromInt(272000)) {
			tier = 1
		}
	}
	credits := q.groupedCredits
	if tier > 0 && len(q.longCredits) != 0 {
		credits = q.longCredits
	}
	var backupCosts map[string]*big.Rat
	if backup {
		backupCosts, err = ztapiXcloudBackupTierCosts20261114(q, tier)
		if err != nil {
			return decimal.Zero, err
		}
	}
	cost := new(big.Rat)
	for i, d := range q.dimensions {
		quantity, ok := usage[d]
		if !ok {
			continue
		}
		if quantity < 0 {
			return decimal.Zero, errors.New("negative supplier usage")
		}
		rate := decimal.RequireFromString(credits[i]).Shift(-6).Div(decimal.NewFromInt(2)).Rat()
		if backup {
			if backupCosts[d] == nil {
				return decimal.Zero, errors.New("missing quoted Yunxin cost")
			}
			rate = new(big.Rat).Quo(backupCosts[d], big.NewRat(1_000_000, 1))
		}
		cost.Add(cost, rate.Mul(rate, big.NewRat(quantity, 1)))
	}
	for d := range usage {
		if !slices.Contains(q.dimensions, d) {
			return decimal.Zero, errors.New("unquoted supplier usage")
		}
	}
	return decimal.NewFromBigRat(cost, 16), nil
}

func ztapiValidateXcloudSnapshot20261114(source *ZTAPIModelPriceSource, snapshot *ZTAPIModelPublicationSnapshot) error {
	q, err := ztapiXcloudQuote20261114(source.SourceModel)
	if err != nil {
		return err
	}
	if snapshot.ModelConfigID != q.id || snapshot.SourceModel != q.source || snapshot.PublicName != q.public ||
		snapshot.Protocol != ZTAPIProtocolOpenAICompatible || snapshot.ProviderFamily != q.family ||
		(snapshot.PricePolicy != "" && snapshot.PricePolicy != string(ZTAPIPricePolicyXcloud20261114)) || snapshot.CacheWritePricingMode != "" ||
		!reflect.DeepEqual(snapshot.ChannelIDs(), q.channels()) {
		return errors.New("Xcloud publication does not match the approved model and route set")
	}
	preview, err := BuildZTAPIModelPricePreview(source)
	if err != nil {
		return err
	}
	if !ztapiStoredPriceMatchesPreview(snapshot.InputPricePerMillion, preview.InputSaleUSDPerMillion) ||
		!ztapiStoredPriceMatchesPreview(snapshot.OutputPricePerMillion, preview.OutputSaleUSDPerMillion) ||
		!ztapiCacheRatiosMatchPreview(&ZTAPIModelConfig{CacheReadRatio: snapshot.CacheReadRatio, CacheCreationRatio: snapshot.CacheCreationRatio, CacheCreation5mRatio: snapshot.CacheCreation5mRatio, CacheCreation1hRatio: snapshot.CacheCreation1hRatio}, preview) {
		return errors.New("Xcloud snapshot price does not match the approved source")
	}
	return nil
}
