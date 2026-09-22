package model

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/types"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

// ZTAPIPublicCatalogItem is the only model metadata shape exposed to users.
// Source model IDs, channel IDs, evidence IDs and upstream price source IDs are
// intentionally absent.
type ZTAPIPublicCatalogItem struct {
	Modality               string                       `json:"modality"`
	ModelName              string                       `json:"model_name"`
	ProviderFamily         string                       `json:"provider_family"`
	ProviderName           string                       `json:"provider_name"`
	Protocol               string                       `json:"protocol"`
	EnableGroups           []string                     `json:"enable_groups"`
	SupportedEndpointTypes []constant.EndpointType      `json:"supported_endpoint_types"`
	InputPricePerMillion   string                       `json:"input_price_per_million"`
	OutputPricePerMillion  string                       `json:"output_price_per_million"`
	BillingDimensions      []string                     `json:"billing_dimensions"`
	SaleUSD                map[string]string            `json:"sale_usd"`
	OfficialUSD            map[string]string            `json:"official_usd,omitempty"`
	TokenPriceRules        []ZTAPIPublicTokenPriceRule  `json:"token_price_rules,omitempty"`
	BillingRule            string                       `json:"billing_rule"`
	PricingVersion         string                       `json:"pricing_version"`
	SupportedOptions       *ZTAPIPublicSupportedOptions `json:"supported_options,omitempty"`
	PricingRules           []ZTAPIPublicPricingRule     `json:"pricing_rules,omitempty"`
	BillingUnit            string                       `json:"billing_unit,omitempty"`
}

type ZTAPIPublicSupportedOptions struct {
	Sizes              []string `json:"sizes,omitempty"`
	Qualities          []string `json:"qualities,omitempty"`
	ResponseFormats    []string `json:"response_formats,omitempty"`
	MinCount           int      `json:"min_count,omitempty"`
	MaxCount           int      `json:"max_count,omitempty"`
	Resolutions        []string `json:"resolutions,omitempty"`
	DurationSeconds    []int    `json:"duration_seconds,omitempty"`
	SupportsVideoInput *bool    `json:"supports_video_input,omitempty"`
}

type ZTAPIPublicPricingRule struct {
	ID          string            `json:"id"`
	Conditions  map[string]string `json:"conditions"`
	BillingUnit string            `json:"billing_unit"`
	SaleUSD     map[string]string `json:"sale_usd"`
	OfficialUSD map[string]string `json:"official_usd,omitempty"`
}

type ZTAPIPublicTokenPriceRule struct {
	Conditions  []string          `json:"conditions"`
	SaleUSD     map[string]string `json:"sale_usd"`
	OfficialUSD map[string]string `json:"official_usd,omitempty"`
}

func cloneZTAPIPublicTokenPriceRules(rules []ZTAPIPublicTokenPriceRule) []ZTAPIPublicTokenPriceRule {
	cloned := make([]ZTAPIPublicTokenPriceRule, len(rules))
	for index := range rules {
		cloned[index] = ZTAPIPublicTokenPriceRule{
			Conditions:  append([]string(nil), rules[index].Conditions...),
			SaleUSD:     copyZTAPIStringMap(rules[index].SaleUSD),
			OfficialUSD: copyZTAPIStringMap(rules[index].OfficialUSD),
		}
	}
	return cloned
}

func copyZTAPINestedStringMap(source map[string]map[string]string) map[string]map[string]string {
	if len(source) == 0 {
		return nil
	}
	cloned := make(map[string]map[string]string, len(source))
	for key, values := range source {
		cloned[key] = copyZTAPIStringMap(values)
	}
	return cloned
}

func ztapiPublicOfficialPriceEvidence(source *ZTAPIModelPriceSource) (map[string]string, []ZTAPIPublicTokenPriceRule, map[string]map[string]string) {
	if source == nil || strings.TrimSpace(source.QuotationCell) == "" {
		return nil, nil, nil
	}
	quote, err := ZTAPIQuotationABEntries()
	if err != nil || source.SourceDocumentChecksum != quote.WorkbookSHA256 {
		return nil, nil, nil
	}
	var row *ZTAPIABQuotationEntry
	for index := range quote.Entries {
		if quote.Entries[index].QuotationCell == source.QuotationCell {
			row = &quote.Entries[index]
			break
		}
	}
	if row == nil || row.ModelCode != source.QuotationModelCode || !row.PricingBasis {
		return nil, nil, nil
	}
	fraction, err := decimal.NewFromString(row.QuotedFraction)
	if err != nil || !fraction.IsPositive() || fraction.GreaterThan(decimal.NewFromInt(1)) {
		return nil, nil, nil
	}
	if source.MediaPriceContractJSON != "" {
		contract, parseErr := types.ParseZTAPIMediaPriceContract(source.MediaPriceContractJSON)
		if parseErr != nil {
			return nil, nil, nil
		}
		media := make(map[string]map[string]string, len(contract.Rules))
		for _, rule := range contract.Rules {
			prices := make(map[string]string, len(rule.CostUSD))
			for dimension, rawCost := range rule.CostUSD {
				cost, priceErr := decimal.NewFromString(rawCost)
				if priceErr != nil || !cost.IsPositive() {
					return nil, nil, nil
				}
				prices[dimension] = cost.Div(fraction).Round(10).String()
			}
			media[rule.ID] = prices
		}
		return nil, nil, media
	}
	if len(row.TokenPriceRules) > 0 {
		rules := make([]ZTAPIPublicTokenPriceRule, 0, len(row.TokenPriceRules))
		for _, quotedRule := range row.TokenPriceRules {
			prices := make(map[string]string, len(quotedRule.Cost))
			for dimension, rawCost := range quotedRule.Cost {
				official, priceErr := ztapiPublicOfficialUSD(rawCost, quotedRule.Currency, fraction, source)
				if priceErr != nil {
					return nil, nil, nil
				}
				prices[dimension] = official.StringFixed(10)
			}
			rules = append(rules, ZTAPIPublicTokenPriceRule{
				Conditions: append([]string(nil), quotedRule.Conditions...), OfficialUSD: prices,
			})
		}
		return nil, rules, nil
	}
	preview, err := BuildZTAPIModelPricePreview(source)
	if err != nil {
		return nil, nil, nil
	}
	official := make(map[string]string, len(preview.CostUSD))
	for dimension, rawCost := range preview.CostUSD {
		cost, priceErr := decimal.NewFromString(rawCost)
		if priceErr != nil || !cost.IsPositive() {
			continue
		}
		official[dimension] = cost.Div(fraction).Round(10).StringFixed(10)
	}
	return official, nil, nil
}

func ztapiPublicOfficialUSD(rawCost, currency string, fraction decimal.Decimal, source *ZTAPIModelPriceSource) (decimal.Decimal, error) {
	cost, err := decimal.NewFromString(rawCost)
	if err != nil || !cost.IsPositive() {
		return decimal.Zero, errors.New("quotation cost is invalid")
	}
	official := cost.Div(fraction)
	if source.FXMode == ZTAPIFXModePlatformV1 {
		platform, platformErr := decimal.NewFromString(source.PlatformCNYPerUnit)
		if platformErr != nil || !platform.IsPositive() {
			return decimal.Zero, errors.New("platform FX rate is invalid")
		}
		switch currency {
		case "CNY":
			return official.Div(platform).Round(10), nil
		case "USD":
			upstream, upstreamErr := decimal.NewFromString(source.UpstreamCNYPerUSD)
			if upstreamErr != nil || !upstream.IsPositive() {
				return decimal.Zero, errors.New("upstream USD rate is invalid")
			}
			return official.Mul(upstream).Div(platform).Round(10), nil
		default:
			return decimal.Zero, errors.New("quotation currency is unsupported")
		}
	}
	if currency == "USD" {
		return official.Round(10), nil
	}
	if currency == "CNY" {
		rate, rateErr := decimal.NewFromString(source.CNYPerUSD)
		if rateErr != nil {
			return decimal.Zero, rateErr
		}
		return ConvertZTAPICNYCostToUSD(official, rate)
	}
	return decimal.Zero, errors.New("quotation currency is unsupported")
}

func ztapiPublicTokenPriceRules(raw string, dimensions []string) ([]ZTAPIPublicTokenPriceRule, error) {
	var frozen []struct {
		Conditions []string          `json:"conditions"`
		Sale       map[string]string `json:"sale"`
	}
	if err := json.Unmarshal([]byte(raw), &frozen); err != nil || len(frozen) == 0 {
		return nil, errors.New("frozen token pricing rules are invalid")
	}
	rules := make([]ZTAPIPublicTokenPriceRule, 0, len(frozen))
	for _, rule := range frozen {
		for _, dimension := range dimensions {
			price, err := decimal.NewFromString(rule.Sale[dimension])
			if err != nil || !price.IsPositive() {
				return nil, fmt.Errorf("frozen token pricing rule lacks %s", dimension)
			}
		}
		rules = append(rules, ZTAPIPublicTokenPriceRule{
			Conditions: append([]string{}, rule.Conditions...), SaleUSD: copyZTAPIStringMap(rule.Sale),
		})
	}
	return rules, nil
}

func ztapiDiscountTokenPriceRules(raw string, multiplier decimal.Decimal) (string, error) {
	if strings.TrimSpace(raw) == "" || multiplier.Equal(decimal.NewFromInt(1)) {
		return raw, nil
	}
	var rules []struct {
		Conditions []string          `json:"conditions"`
		Sale       map[string]string `json:"sale"`
	}
	if err := common.UnmarshalJsonStr(raw, &rules); err != nil || len(rules) == 0 {
		return "", errors.New("frozen token pricing rules are invalid")
	}
	for index := range rules {
		if len(rules[index].Sale) == 0 {
			return "", errors.New("frozen token pricing rule has no sale prices")
		}
		for dimension, rawPrice := range rules[index].Sale {
			price, err := decimal.NewFromString(rawPrice)
			if err != nil || price.IsNegative() {
				return "", fmt.Errorf("frozen token pricing rule has invalid %s", dimension)
			}
			rules[index].Sale[dimension] = price.Mul(multiplier).Round(10).String()
		}
	}
	encoded, err := common.Marshal(rules)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

type ZTAPIRuntimePublication struct {
	Modality               string
	ModelConfigID          int
	SnapshotID             int64
	PriceSourceID          int64
	PriceSourceVersion     uint64
	SaleMultiplier         string
	Version                uint64
	SourceModel            string
	PublicName             string
	ProviderFamily         string
	Protocol               string
	Groups                 []string
	AllowedChannelIDs      []int
	InputPricePerMillion   float64
	OutputPricePerMillion  float64
	CacheReadRatio         float64
	CacheCreationRatio     float64
	CacheCreation5mRatio   float64
	CacheCreation1hRatio   float64
	ImageRatio             float64
	AudioRatio             float64
	AudioCompletionRatio   float64
	BillingDimensions      []string
	SaleUSD                map[string]string
	OfficialUSD            map[string]string
	TokenOfficialRules     []ZTAPIPublicTokenPriceRule
	MediaOfficialUSD       map[string]map[string]string
	TokenPriceRulesJSON    string
	MediaPriceContractJSON string
	InputPriceDisplay      string
	OutputPriceDisplay     string
	ImageProtocolContract  *types.ZTAPIImageProtocolContract `json:"-"`
	VideoProtocolContract  *types.ZTAPIVideoProtocolContract `json:"-"`
}

func ztapiProviderName(provider string) string {
	switch provider {
	case ZTAPIProviderOpenAI:
		return "OpenAI"
	case ZTAPIProviderAnthropic:
		return "Claude"
	case ZTAPIProviderGoogle:
		return "Gemini"
	case ZTAPIProviderDeepSeek:
		return "DeepSeek"
	case ZTAPIProviderDoubao:
		return "Doubao"
	case ZTAPIProviderQwen:
		return "Qwen"
	case ZTAPIProviderMoonshot:
		return "Kimi"
	case ZTAPIProviderGLM:
		return "GLM"
	case ZTAPIProviderMiniMax:
		return "MiniMax"
	case ZTAPIProviderSeedance:
		return "Seedance"
	case ZTAPIProviderSeedream:
		return "Seedream"
	case ZTAPIProviderVidu:
		return "Vidu"
	default:
		return "Other"
	}
}

func ztapiEndpointTypes(protocol, sourceModel string) []constant.EndpointType {
	return ztapiEndpointTypesForModality(protocol, sourceModel, ZTAPIModelModality(sourceModel))
}

func ztapiEndpointTypesForModality(protocol, sourceModel, modality string) []constant.EndpointType {
	if modality == ZTAPIModalityImage {
		return []constant.EndpointType{constant.EndpointTypeImages}
	}
	if modality == ZTAPIModalityVideo {
		return []constant.EndpointType{constant.EndpointTypeVideoTasks}
	}
	if protocol == ZTAPIProtocolOpenAICompatible && modality == ZTAPIModalityEmbedding {
		return []constant.EndpointType{constant.EndpointTypeEmbeddings}
	}
	switch protocol {
	case ZTAPIProtocolAnthropic:
		return []constant.EndpointType{constant.EndpointTypeAnthropic}
	case ZTAPIProtocolGemini:
		return []constant.EndpointType{constant.EndpointTypeGemini}
	default:
		if common.IsOpenAIResponseOnlyModel(sourceModel) {
			return []constant.EndpointType{constant.EndpointTypeOpenAIResponse}
		}
		return []constant.EndpointType{constant.EndpointTypeOpenAI}
	}
}

func ztapiPublicMediaMetadata(publication ZTAPIRuntimePublication) (*ZTAPIPublicSupportedOptions, []ZTAPIPublicPricingRule, string) {
	contract, err := types.ParseZTAPIMediaPriceContract(publication.MediaPriceContractJSON)
	if err != nil || contract.Modality != publication.Modality {
		return nil, nil, ""
	}
	rules := make([]ZTAPIPublicPricingRule, 0, len(contract.Rules))
	billingUnit := ""
	reportedImageDimensions := map[string]struct{}(nil)
	if publication.Modality == ZTAPIModalityImage && publication.ImageProtocolContract != nil &&
		types.IsZTAPIGPTImage2NotReportedUsageProtocol(*publication.ImageProtocolContract) {
		reportedImageDimensions = make(map[string]struct{}, len(publication.ImageProtocolContract.Usage.Fields))
		for dimension := range publication.ImageProtocolContract.Usage.Fields {
			reportedImageDimensions[dimension] = struct{}{}
		}
	}
	for _, rule := range contract.Rules {
		effectiveRule, effectiveErr := types.EffectiveZTAPIMediaPriceRule(rule, contract.SaleMultiplier)
		if effectiveErr != nil {
			return nil, nil, ""
		}
		rule = effectiveRule
		if reportedImageDimensions != nil {
			if len(rule.SaleUSD) != 1 {
				continue
			}
			reported := false
			for dimension := range rule.SaleUSD {
				_, reported = reportedImageDimensions[dimension]
			}
			if !reported {
				continue
			}
		}
		if billingUnit == "" {
			billingUnit = rule.BillingUnit
		} else if billingUnit != rule.BillingUnit {
			billingUnit = "mixed"
		}
		rules = append(rules, ZTAPIPublicPricingRule{
			ID: rule.ID, Conditions: copyZTAPIStringMap(rule.Conditions),
			BillingUnit: rule.BillingUnit, SaleUSD: copyZTAPIStringMap(rule.SaleUSD),
			OfficialUSD: copyZTAPIStringMap(publication.MediaOfficialUSD[rule.ID]),
		})
	}
	options := &ZTAPIPublicSupportedOptions{}
	switch publication.Modality {
	case ZTAPIModalityImage:
		if publication.ImageProtocolContract == nil {
			return nil, nil, ""
		}
		capabilities := publication.ImageProtocolContract.Capabilities
		options.Sizes = append([]string(nil), capabilities.Sizes...)
		options.Qualities = append([]string(nil), capabilities.Qualities...)
		options.ResponseFormats = append([]string(nil), capabilities.ResponseFormats...)
		options.MinCount = capabilities.MinCount
		options.MaxCount = capabilities.MaxCount
	case ZTAPIModalityVideo:
		if publication.VideoProtocolContract == nil {
			return nil, nil, ""
		}
		capabilities := publication.VideoProtocolContract.Capabilities
		options.Resolutions = append([]string(nil), capabilities.Resolutions...)
		options.DurationSeconds = append([]int(nil), capabilities.DurationSeconds...)
		supportsVideoInput := capabilities.SupportsVideoInput
		options.SupportsVideoInput = &supportsVideoInput
	default:
		return nil, nil, ""
	}
	return options, rules, billingUnit
}

func ztapiBillingRule(dimensions []string) string {
	if len(dimensions) == 1 && dimensions[0] == ZTAPIBillingDimensionInputTokens {
		return "input_only"
	}
	if len(dimensions) == 2 && dimensions[0] == ZTAPIBillingDimensionInputTokens && dimensions[1] == ZTAPIBillingDimensionOutputTokens {
		return "token"
	}
	return "multi_dimension"
}

func copyZTAPIStringMap(values map[string]string) map[string]string {
	result := make(map[string]string, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}

func validZTAPISnapshotPricing(snapshot *ZTAPIModelPublicationSnapshot) bool {
	if snapshot == nil {
		return false
	}
	modality := ZTAPIModelModality(snapshot.SourceModel)
	if modality == ZTAPIModalityImage || modality == ZTAPIModalityVideo {
		contract, err := types.ParseZTAPIMediaPriceContract(snapshot.MediaPriceContractJSON)
		canonical, canonicalErr := types.CanonicalizeZTAPIMediaPriceContract(snapshot.MediaPriceContractJSON)
		return err == nil && canonicalErr == nil && canonical == snapshot.MediaPriceContractJSON && contract.Modality == modality
	}
	if modality == ZTAPIModalityEmbedding {
		return finitePositiveZTAPIRatio(snapshot.InputPricePerMillion) && snapshot.OutputPricePerMillion == 0
	}
	return finitePositiveZTAPIRatio(snapshot.InputPricePerMillion) &&
		finitePositiveZTAPIRatio(snapshot.OutputPricePerMillion) &&
		finitePositiveZTAPIRatio(snapshot.CacheReadRatio) &&
		finitePositiveZTAPIRatio(snapshot.CacheCreationRatio) &&
		finitePositiveZTAPIRatio(snapshot.CacheCreation5mRatio) &&
		finitePositiveZTAPIRatio(snapshot.CacheCreation1hRatio) &&
		finitePositiveZTAPIRatio(snapshot.ImageRatio) &&
		finitePositiveZTAPIRatio(snapshot.AudioRatio) &&
		finitePositiveZTAPIRatio(snapshot.AudioCompletionRatio)
}

func loadZTAPIActivePublications(db *gorm.DB) ([]ZTAPIRuntimePublication, error) {
	if db == nil {
		return nil, errors.New("ZTAPI database is not initialized")
	}
	var configs []ZTAPIModelConfig
	if err := db.Where("published = ? AND publication_snapshot_id > 0", true).Order("id ASC").Find(&configs).Error; err != nil {
		return nil, err
	}
	result := make([]ZTAPIRuntimePublication, 0, len(configs))
	for i := range configs {
		var snapshot ZTAPIModelPublicationSnapshot
		if err := db.First(&snapshot, configs[i].PublicationSnapshotID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				continue
			}
			return nil, err
		}
		if snapshot.ModelConfigID != configs[i].ID || snapshot.ModelVersion != configs[i].Version || !validZTAPISnapshotPricing(&snapshot) {
			continue
		}
		groups := snapshot.Groups()
		if err := validateZTAPIPublicationGroups(groups); err != nil {
			continue
		}
		channels := snapshot.ChannelIDs()
		if len(channels) == 0 {
			continue
		}
		var source ZTAPIModelPriceSource
		if err := db.First(&source, snapshot.PriceSourceID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				continue
			}
			return nil, err
		}
		if source.ModelConfigID != snapshot.ModelConfigID || source.SourceModel != snapshot.SourceModel {
			continue
		}
		sourceMultiplier, multiplierErr := ztapiNormalizedSaleMultiplier(source.SaleMultiplier)
		snapshotMultiplier, snapshotMultiplierErr := ztapiNormalizedSaleMultiplier(snapshot.SaleMultiplier)
		if multiplierErr != nil || snapshotMultiplierErr != nil || !sourceMultiplier.Equal(snapshotMultiplier) {
			continue
		}
		if snapshot.TokenPriceRulesJSON != source.TokenPriceRulesJSON {
			continue
		}
		preview, err := BuildZTAPIModelPricePreview(&source)
		if err != nil {
			continue
		}
		officialUSD, tokenOfficialRules, mediaOfficialUSD := ztapiPublicOfficialPriceEvidence(&source)
		modality := ZTAPIModelModality(snapshot.SourceModel)
		runtimeTokenRules, err := ztapiDiscountTokenPriceRules(snapshot.TokenPriceRulesJSON, sourceMultiplier)
		if err != nil {
			continue
		}
		runtimeMediaContract := snapshot.MediaPriceContractJSON
		if modality == ZTAPIModalityImage || modality == ZTAPIModalityVideo {
			if snapshot.MediaPriceContractJSON == "" || snapshot.MediaPriceContractJSON != source.MediaPriceContractJSON {
				continue
			}
			runtimeMediaContract, err = types.WithZTAPIMediaSaleMultiplier(snapshot.MediaPriceContractJSON, sourceMultiplier.StringFixed(10))
			if err != nil {
				continue
			}
		} else if !ztapiStoredPriceMatchesPreview(snapshot.InputPricePerMillion, preview.InputSaleUSDPerMillion) ||
			!ztapiStoredPriceMatchesPreview(snapshot.OutputPricePerMillion, preview.OutputSaleUSDPerMillion) {
			continue
		}
		var imageProtocol *types.ZTAPIImageProtocolContract
		if snapshot.ImageProtocolContractJSON != "" {
			contract, canonical, contractErr := types.ParseZTAPIImageProtocolContract(snapshot.ImageProtocolContractJSON)
			if contractErr != nil || canonical != snapshot.ImageProtocolContractJSON || contract.ProviderModel != snapshot.SourceModel {
				continue
			}
			cloned := contract.Clone()
			imageProtocol = &cloned
		}
		var videoProtocol *types.ZTAPIVideoProtocolContract
		if snapshot.VideoProtocolContractJSON != "" {
			contract, canonical, contractErr := types.ParseZTAPIVideoProtocolContract(snapshot.VideoProtocolContractJSON)
			if contractErr != nil || canonical != snapshot.VideoProtocolContractJSON || contract.ProviderModel != snapshot.SourceModel {
				continue
			}
			mediaContract, mediaErr := types.ParseZTAPIMediaPriceContract(snapshot.MediaPriceContractJSON)
			if mediaErr != nil || types.ValidateZTAPIVideoPriceProtocolCompatibility(mediaContract, contract) != nil {
				continue
			}
			cloned := contract.Clone()
			videoProtocol = &cloned
		}
		if ZTAPIModelModality(snapshot.SourceModel) == ZTAPIModalityVideo && videoProtocol == nil {
			continue
		}
		result = append(result, ZTAPIRuntimePublication{
			ModelConfigID: configs[i].ID, SnapshotID: snapshot.ID, Version: snapshot.ModelVersion,
			PriceSourceID: source.ID, PriceSourceVersion: source.Version, SaleMultiplier: sourceMultiplier.StringFixed(10),
			Modality:    ZTAPIModelModality(snapshot.SourceModel),
			SourceModel: snapshot.SourceModel, PublicName: snapshot.PublicName,
			ProviderFamily: snapshot.ProviderFamily, Protocol: snapshot.Protocol,
			Groups: groups, AllowedChannelIDs: channels,
			InputPricePerMillion: snapshot.InputPricePerMillion, OutputPricePerMillion: snapshot.OutputPricePerMillion,
			CacheReadRatio: snapshot.CacheReadRatio, CacheCreationRatio: snapshot.CacheCreationRatio,
			CacheCreation5mRatio: snapshot.CacheCreation5mRatio, CacheCreation1hRatio: snapshot.CacheCreation1hRatio,
			ImageRatio: snapshot.ImageRatio, AudioRatio: snapshot.AudioRatio,
			AudioCompletionRatio:   snapshot.AudioCompletionRatio,
			BillingDimensions:      append([]string(nil), preview.BillingDimensions...),
			SaleUSD:                copyZTAPIStringMap(preview.SaleUSD),
			OfficialUSD:            copyZTAPIStringMap(officialUSD),
			TokenOfficialRules:     cloneZTAPIPublicTokenPriceRules(tokenOfficialRules),
			MediaOfficialUSD:       copyZTAPINestedStringMap(mediaOfficialUSD),
			TokenPriceRulesJSON:    runtimeTokenRules,
			MediaPriceContractJSON: runtimeMediaContract,
			InputPriceDisplay:      preview.InputSaleUSDPerMillion, OutputPriceDisplay: preview.OutputSaleUSDPerMillion,
			ImageProtocolContract: imageProtocol,
			VideoProtocolContract: videoProtocol,
		})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].ProviderFamily == result[j].ProviderFamily {
			return result[i].PublicName < result[j].PublicName
		}
		return result[i].ProviderFamily < result[j].ProviderFamily
	})
	return result, nil
}

func ListZTAPIPublicCatalog() ([]ZTAPIPublicCatalogItem, error) {
	publications, err := listCachedZTAPIRuntimePublications()
	if err != nil {
		return nil, err
	}
	return buildZTAPIPublicCatalog(publications), nil
}

func runtimePublicationFromCache(publication ztapiPublishedModel) ZTAPIRuntimePublication {
	return ZTAPIRuntimePublication{
		ModelConfigID: publication.ModelConfigID, SnapshotID: publication.SnapshotID, Version: publication.Version,
		PriceSourceID: publication.PriceSourceID, PriceSourceVersion: publication.PriceSourceVersion,
		SaleMultiplier: publication.SaleMultiplier,
		Modality:       publication.Modality,
		SourceModel:    publication.SourceModel, PublicName: publication.PublicName,
		ProviderFamily: publication.ProviderFamily, Protocol: publication.Protocol,
		Groups:                append([]string(nil), publication.Groups...),
		AllowedChannelIDs:     append([]int(nil), publication.AllowedChannelIDs...),
		InputPricePerMillion:  publication.InputPricePerMillion,
		OutputPricePerMillion: publication.OutputPricePerMillion,
		CacheReadRatio:        publication.CacheReadRatio, CacheCreationRatio: publication.CacheCreationRatio,
		CacheCreation5mRatio: publication.CacheCreation5mRatio, CacheCreation1hRatio: publication.CacheCreation1hRatio,
		ImageRatio: publication.ImageRatio, AudioRatio: publication.AudioRatio,
		AudioCompletionRatio:   publication.AudioCompletionRatio,
		BillingDimensions:      append([]string(nil), publication.BillingDimensions...),
		SaleUSD:                copyZTAPIStringMap(publication.SaleUSD),
		OfficialUSD:            copyZTAPIStringMap(publication.OfficialUSD),
		TokenOfficialRules:     cloneZTAPIPublicTokenPriceRules(publication.TokenOfficialRules),
		MediaOfficialUSD:       copyZTAPINestedStringMap(publication.MediaOfficialUSD),
		TokenPriceRulesJSON:    publication.TokenPriceRulesJSON,
		MediaPriceContractJSON: publication.MediaPriceContractJSON,
		InputPriceDisplay:      publication.InputPriceDisplay, OutputPriceDisplay: publication.OutputPriceDisplay,
		ImageProtocolContract: cloneZTAPIImageProtocolContract(publication.ImageProtocolContract),
		VideoProtocolContract: cloneZTAPIVideoProtocolContract(publication.VideoProtocolContract),
	}
}

func cloneZTAPIImageProtocolContract(contract *types.ZTAPIImageProtocolContract) *types.ZTAPIImageProtocolContract {
	if contract == nil {
		return nil
	}
	cloned := contract.Clone()
	return &cloned
}

func cloneZTAPIVideoProtocolContract(contract *types.ZTAPIVideoProtocolContract) *types.ZTAPIVideoProtocolContract {
	if contract == nil {
		return nil
	}
	cloned := contract.Clone()
	return &cloned
}

func listCachedZTAPIRuntimePublications() ([]ZTAPIRuntimePublication, error) {
	if err := ensureZTAPIAliasCache(); err != nil {
		return nil, err
	}
	ztapiAliasCache.RLock()
	result := make([]ZTAPIRuntimePublication, 0, len(ztapiAliasCache.aliases))
	for _, publication := range ztapiAliasCache.aliases {
		result = append(result, runtimePublicationFromCache(publication))
	}
	ztapiAliasCache.RUnlock()
	sort.Slice(result, func(i, j int) bool {
		if result[i].ProviderFamily == result[j].ProviderFamily {
			return result[i].PublicName < result[j].PublicName
		}
		return result[i].ProviderFamily < result[j].ProviderFamily
	})
	return result, nil
}

// ListZTAPIActiveRuntimePublications returns defensive copies of the immutable
// publication snapshots for trusted server-side consumers. Callers must not
// serialize SourceModel or AllowedChannelIDs into user-facing responses.
func ListZTAPIActiveRuntimePublications() ([]ZTAPIRuntimePublication, error) {
	return listCachedZTAPIRuntimePublications()
}

func buildZTAPIPublicCatalog(publications []ZTAPIRuntimePublication) []ZTAPIPublicCatalogItem {
	items := make([]ZTAPIPublicCatalogItem, 0, len(publications))
	for _, publication := range publications {
		item := ZTAPIPublicCatalogItem{
			Modality:               publication.Modality,
			ModelName:              publication.PublicName,
			ProviderFamily:         publication.ProviderFamily,
			ProviderName:           ztapiProviderName(publication.ProviderFamily),
			Protocol:               publication.Protocol,
			EnableGroups:           append([]string(nil), publication.Groups...),
			SupportedEndpointTypes: ztapiEndpointTypesForModality(publication.Protocol, publication.SourceModel, publication.Modality),
			InputPricePerMillion:   publication.InputPriceDisplay,
			OutputPricePerMillion:  publication.OutputPriceDisplay,
			BillingDimensions:      append([]string(nil), publication.BillingDimensions...),
			SaleUSD:                copyZTAPIStringMap(publication.SaleUSD),
			OfficialUSD:            copyZTAPIStringMap(publication.OfficialUSD),
			BillingRule:            ztapiBillingRule(publication.BillingDimensions),
			PricingVersion:         fmt.Sprintf("ztapi-snapshot-%d", publication.SnapshotID),
		}
		if publication.TokenPriceRulesJSON != "" {
			rules, err := ztapiPublicTokenPriceRules(publication.TokenPriceRulesJSON, publication.BillingDimensions)
			if err != nil {
				continue
			}
			item.TokenPriceRules = rules
			if len(publication.TokenOfficialRules) == len(item.TokenPriceRules) {
				for index := range item.TokenPriceRules {
					item.TokenPriceRules[index].OfficialUSD = copyZTAPIStringMap(publication.TokenOfficialRules[index].OfficialUSD)
				}
			}
			item.InputPricePerMillion = ""
			item.OutputPricePerMillion = ""
			item.SaleUSD = map[string]string{}
			item.OfficialUSD = map[string]string{}
		}
		if publication.Modality == ZTAPIModalityImage || publication.Modality == ZTAPIModalityVideo {
			item.SupportedOptions, item.PricingRules, item.BillingUnit = ztapiPublicMediaMetadata(publication)
			if item.SupportedOptions == nil || len(item.PricingRules) == 0 || item.BillingUnit == "" {
				continue
			}
			item.InputPricePerMillion = ""
			item.OutputPricePerMillion = ""
			item.BillingDimensions = []string{}
			item.SaleUSD = map[string]string{}
			item.OfficialUSD = map[string]string{}
			item.BillingRule = "multi_dimension"
		}
		items = append(items, item)
	}
	return items
}

func GetZTAPIRuntimePublication(publicName string) (*ZTAPIRuntimePublication, error) {
	if err := ensureZTAPIAliasCache(); err != nil {
		return nil, err
	}
	publicName = strings.TrimSpace(publicName)
	ztapiAliasCache.RLock()
	publication, ok := ztapiAliasCache.aliases[publicName]
	ztapiAliasCache.RUnlock()
	if ok {
		runtime := runtimePublicationFromCache(publication)
		return &runtime, nil
	}
	return nil, gorm.ErrRecordNotFound
}

func FilterZTAPIPublicCatalogByGroups(items []ZTAPIPublicCatalogItem, groups []string) []ZTAPIPublicCatalogItem {
	groups = normalizeZTAPIGroups(groups)
	result := make([]ZTAPIPublicCatalogItem, 0, len(items))
	for _, item := range items {
		for _, group := range groups {
			if ztapiGroupAllowed(item.EnableGroups, group) {
				result = append(result, item)
				break
			}
		}
	}
	return result
}

func ZTAPIPublicCatalogNames(items []ZTAPIPublicCatalogItem) []string {
	names := make([]string, 0, len(items))
	for _, item := range items {
		names = append(names, item.ModelName)
	}
	return names
}
