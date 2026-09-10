package model

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/types"
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
}

type ZTAPIRuntimePublication struct {
	Modality               string
	ModelConfigID          int
	SnapshotID             int64
	PriceSourceID          int64
	PriceSourceVersion     uint64
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
	for _, rule := range contract.Rules {
		if billingUnit == "" {
			billingUnit = rule.BillingUnit
		} else if billingUnit != rule.BillingUnit {
			billingUnit = "mixed"
		}
		rules = append(rules, ZTAPIPublicPricingRule{
			ID: rule.ID, Conditions: copyZTAPIStringMap(rule.Conditions),
			BillingUnit: rule.BillingUnit, SaleUSD: copyZTAPIStringMap(rule.SaleUSD),
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
	if ZTAPIModelModality(snapshot.SourceModel) == ZTAPIModalityEmbedding {
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
		preview, err := BuildZTAPIModelPricePreview(&source)
		if err != nil {
			continue
		}
		if !ztapiStoredPriceMatchesPreview(snapshot.InputPricePerMillion, preview.InputSaleUSDPerMillion) ||
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
			PriceSourceID: source.ID, PriceSourceVersion: source.Version,
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
			MediaPriceContractJSON: snapshot.MediaPriceContractJSON,
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
		Modality:    publication.Modality,
		SourceModel: publication.SourceModel, PublicName: publication.PublicName,
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
			BillingRule:            ztapiBillingRule(publication.BillingDimensions),
			PricingVersion:         fmt.Sprintf("ztapi-snapshot-%d", publication.SnapshotID),
		}
		if publication.Modality == ZTAPIModalityImage || publication.Modality == ZTAPIModalityVideo {
			item.SupportedOptions, item.PricingRules, item.BillingUnit = ztapiPublicMediaMetadata(publication)
			if item.SupportedOptions == nil || len(item.PricingRules) == 0 || item.BillingUnit == "" {
				continue
			}
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
