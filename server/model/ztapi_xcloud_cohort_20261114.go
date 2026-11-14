package model

import (
	"errors"
	"math/big"
	"reflect"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/shopspring/decimal"
)

const ZTAPIPricePolicyXcloud20261114 ZTAPIPricePolicy = "xcloud_verified_20261114"
const ztapiXcloudEvidence20261114 = "8228ef012920100ba179100034279bdc555ae10e4a37dea863d81cfbf7072fcb"

type ztapiXcloudCohortQuote20261114 struct {
	id, primary    int
	source, public string
	family, group  string
	backupModel    string
	dimensions     []string
	groupedCredits []string
	previousRetail []string
}

func ztapiXcloudQuote20261114(source string) (ztapiXcloudCohortQuote20261114, error) {
	dimensions := []string{ZTAPIBillingDimensionInputTokens, ZTAPIBillingDimensionOutputTokens, ZTAPIBillingDimensionCacheRead}
	switch source {
	case "claude-fable-5-1":
		return ztapiXcloudCohortQuote20261114{97, 4, source, "zt-claude-fable-5-1", ZTAPIProviderAnthropic, "claude-max", "",
			append(dimensions, ZTAPIBillingDimensionCacheWrite5m, ZTAPIBillingDimensionCacheWrite1h),
			[]string{"10", "50", "0.25", "12.5", "20"}, []string{"8.19", "40.95", "0.39", "7.7206", "12.3530"}}, nil
	case "claude-sonnet-5":
		return ztapiXcloudCohortQuote20261114{55, 4, source, "zt-claude-sonnet-5", ZTAPIProviderAnthropic, "claude-max", "claude-sonnet-5-hc",
			append(dimensions, ZTAPIBillingDimensionCacheWrite5m, ZTAPIBillingDimensionCacheWrite1h),
			[]string{"2", "10", "0.2", "2.5", "4"}, []string{"1.0018", "6.4853", "0.139", "1.7603", "2.5015"}}, nil
	case "deepseek-v4.1-flash":
		return ztapiXcloudCohortQuote20261114{156, 8, source, "zt-deepseek-v4.1-flash", ZTAPIProviderDeepSeek, "cn", "",
			dimensions, []string{"0.084", "0.3", "0.0018"}, []string{"0.06552", "0.234", "0.00234"}}, nil
	default:
		return ztapiXcloudCohortQuote20261114{}, errors.New("model is outside the verified Xcloud cohort")
	}
}

func (q ztapiXcloudCohortQuote20261114) channels() []int {
	if q.backupModel != "" {
		return []int{2, q.primary}
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
		(q.backupModel != "" && channelID == 2 && baseURL == "https://ai.yunxinapi.com/hub" && physical == q.backupModel) {
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
	var yunxinRows []ZTAPIPriceReductionDimension20261107
	if q.backupModel != "" {
		_, _, _, yunxinRows, err = ztapiReductionApproval20261107(q.id)
		if err != nil {
			return target, err
		}
	}
	sale := make(map[string]string, len(q.dimensions))
	for i, dimension := range q.dimensions {
		cash := decimal.RequireFromString(q.groupedCredits[i]).Mul(decimal.RequireFromString("0.5"))
		ceiling := cash.Rat()
		if q.backupModel != "" {
			var backup *big.Rat
			for _, row := range yunxinRows {
				if row.Dimension == dimension && row.Tier == "1" && row.SourceModel == source {
					if err := checkXcloudBackupQuote20261114(row); err != nil {
						return target, err
					}
					backup, _ = new(big.Rat).SetString(row.YunxinCost)
				}
			}
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
		sale[dimension] = decimal.Max(decimal.RequireFromString(floor), decimal.RequireFromString(q.previousRetail[i])).StringFixed(10)
		if err := assignZTAPIPriceSourceCost(&target, dimension, cash); err != nil {
			return target, err
		}
	}
	rules, err := common.Marshal([]ztapiABFrozenSaleRule{{Conditions: []string{}, Sale: sale}})
	target.TokenPriceRulesJSON = string(rules)
	return target, err
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
	for dimension, value := range rules[0].Sale {
		sale[dimension] = decimal.RequireFromString(value)
	}
	return sale, nil
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
