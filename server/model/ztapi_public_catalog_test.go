package model

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/types"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupZTAPIPublicCatalogTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "public-catalog.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&ZTAPIModelConfig{}, &ZTAPIModelPriceSource{}, &ZTAPIModelPublicationSnapshot{}))
	previous := DB
	DB = db
	InvalidateZTAPIAliasCache()
	t.Cleanup(func() {
		sqlDB, sqlErr := db.DB()
		if sqlErr == nil {
			require.NoError(t, sqlDB.Close())
		}
		DB = previous
		InvalidateZTAPIAliasCache()
	})
	return db
}

func TestZTAPIPublicCatalogMediaExposesOnlyPublicCapabilitiesAndSalePricing(t *testing.T) {
	imageProtocol, _, err := types.ParseZTAPIImageProtocolContract(syntheticVerifiedImageProtocolJSON(t, "gp-image-2"))
	require.NoError(t, err)
	videoProtocol, _, err := types.ParseZTAPIVideoProtocolContract(canonicalZTAPIVideoProtocolForPublicationTest(t, "seedance-2.0"))
	require.NoError(t, err)
	imagePrice := mustCanonicalZTAPIMediaPriceContract(t, gpImage2ContractForTest(t))
	videoPrice := mustCanonicalZTAPIMediaPriceContract(t, seedanceContractForTest(t))

	items := buildZTAPIPublicCatalog([]ZTAPIRuntimePublication{
		{
			Modality: ZTAPIModalityImage, SourceModel: "gp-image-2", PublicName: "zt-gp-image-2",
			ProviderFamily: ZTAPIProviderOpenAI, Protocol: ZTAPIProtocolOpenAICompatible,
			Groups: []string{"default"}, SnapshotID: 101, MediaPriceContractJSON: imagePrice,
			ImageProtocolContract: &imageProtocol,
			InputPriceDisplay:     "4.0000000000", OutputPriceDisplay: "24.0000000000",
			BillingDimensions: []string{ZTAPIBillingDimensionInputTokens, ZTAPIBillingDimensionOutputTokens},
			SaleUSD: map[string]string{
				ZTAPIBillingDimensionInputTokens: "4.0000000000", ZTAPIBillingDimensionOutputTokens: "24.0000000000",
			},
		},
		{
			Modality: ZTAPIModalityVideo, SourceModel: "seedance-2.0", PublicName: "zt-seedance-2",
			ProviderFamily: ZTAPIProviderSeedance, Protocol: ZTAPIProtocolOpenAICompatible,
			Groups: []string{"default"}, SnapshotID: 102, MediaPriceContractJSON: videoPrice,
			VideoProtocolContract: &videoProtocol,
			InputPriceDisplay:     "8.0000000000", OutputPriceDisplay: "30.0000000000",
			BillingDimensions: []string{ZTAPIBillingDimensionInputTokens, ZTAPIBillingDimensionOutputTokens},
			SaleUSD: map[string]string{
				ZTAPIBillingDimensionInputTokens: "8.0000000000", ZTAPIBillingDimensionOutputTokens: "30.0000000000",
			},
		},
	})
	require.Len(t, items, 2)

	imageItem := items[0]
	require.Equal(t, ZTAPIModalityImage, imageItem.Modality)
	require.Equal(t, []constant.EndpointType{constant.EndpointTypeImages}, imageItem.SupportedEndpointTypes)
	require.Equal(t, []string{"1024x1024", "512x512"}, imageItem.SupportedOptions.Sizes)
	require.Equal(t, []string{"standard"}, imageItem.SupportedOptions.Qualities)
	require.Equal(t, []string{"url"}, imageItem.SupportedOptions.ResponseFormats)
	require.Equal(t, 1, imageItem.SupportedOptions.MinCount)
	require.Equal(t, 2, imageItem.SupportedOptions.MaxCount)
	require.Equal(t, "usd_per_million_tokens", imageItem.BillingUnit)
	require.Len(t, imageItem.PricingRules, 5)
	require.Equal(t, map[string]string{"image_output": "24.00"}, imageItem.PricingRules[2].SaleUSD)
	require.Empty(t, imageItem.InputPricePerMillion)
	require.Empty(t, imageItem.OutputPricePerMillion)
	require.Empty(t, imageItem.BillingDimensions)
	require.Empty(t, imageItem.SaleUSD)
	require.Equal(t, "multi_dimension", imageItem.BillingRule)

	videoItem := items[1]
	require.Equal(t, ZTAPIModalityVideo, videoItem.Modality)
	require.Equal(t, []constant.EndpointType{constant.EndpointTypeVideoTasks}, videoItem.SupportedEndpointTypes)
	require.Equal(t, []string{"720p"}, videoItem.SupportedOptions.Resolutions)
	require.Equal(t, []int{5}, videoItem.SupportedOptions.DurationSeconds)
	require.NotNil(t, videoItem.SupportedOptions.SupportsVideoInput)
	require.False(t, *videoItem.SupportedOptions.SupportsVideoInput)
	require.Equal(t, "usd_per_million_tokens", videoItem.BillingUnit)
	require.Len(t, videoItem.PricingRules, 8)
	require.Empty(t, videoItem.InputPricePerMillion)
	require.Empty(t, videoItem.OutputPricePerMillion)
	require.Empty(t, videoItem.BillingDimensions)
	require.Empty(t, videoItem.SaleUSD)
	require.Equal(t, "multi_dimension", videoItem.BillingRule)

	encoded, err := json.Marshal(items)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "cost_usd")
	require.NotContains(t, string(encoded), "source_cells")
	require.NotContains(t, string(encoded), "provider-video-exact")
}

func TestZTAPIPublicPricingPreservesMediaCapabilityAndConditionalSaleContract(t *testing.T) {
	imageProtocol, _, err := types.ParseZTAPIImageProtocolContract(syntheticVerifiedImageProtocolJSON(t, "gp-image-2"))
	require.NoError(t, err)
	publication := ZTAPIRuntimePublication{
		Modality: ZTAPIModalityImage, SourceModel: "gp-image-2", PublicName: "zt-gp-image-2",
		ProviderFamily: ZTAPIProviderOpenAI, Protocol: ZTAPIProtocolOpenAICompatible,
		Groups: []string{"default"}, SnapshotID: 101,
		MediaPriceContractJSON: mustCanonicalZTAPIMediaPriceContract(t, gpImage2ContractForTest(t)),
		ImageProtocolContract:  &imageProtocol,
	}
	pricing := projectZTAPIPublicPricing([]ZTAPIRuntimePublication{publication})
	require.Len(t, pricing, 1)
	require.Equal(t, ZTAPIModalityImage, pricing[0].Modality)
	require.Equal(t, []constant.EndpointType{constant.EndpointTypeImages}, pricing[0].SupportedEndpointTypes)
	require.Equal(t, []string{"1024x1024", "512x512"}, pricing[0].SupportedOptions.Sizes)
	require.Len(t, pricing[0].PricingRules, 5)
	require.Equal(t, "usd_per_million_tokens", pricing[0].BillingUnit)
	require.Empty(t, pricing[0].InputPricePerMillion)
	require.Zero(t, pricing[0].ModelRatio)
	require.Zero(t, pricing[0].CompletionRatio)

	encoded, err := json.Marshal(pricing[0])
	require.NoError(t, err)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(encoded, &payload))
	require.Equal(t, "", payload["input_price_per_million"])
	require.Equal(t, "", payload["output_price_per_million"])
	require.Equal(t, []any{}, payload["billing_dimensions"])
	require.Equal(t, map[string]any{}, payload["sale_usd"])
}

func TestZTAPIPublicPricingHidesUnreportedGPTImageCacheBuckets(t *testing.T) {
	protocol, _, err := types.SealZTAPIImageProtocolContract(types.ZTAPIImageProtocolContract{
		Version: types.ZTAPIImageProtocolContractVersionV2, ProviderModel: "gpt-image-2",
		EndpointType: types.ZTAPIImageEndpointGeneration, Method: "POST", Path: "/v1/images/generations",
		WireProtocol: types.ZTAPIImageWireProtocolOpenAIImages, ProviderPath: "/v1/images/generations",
		Capabilities: types.ZTAPIImageCapabilities{Sizes: []string{"1024x1024"}, Qualities: []string{"low"}, ResponseFormats: []string{"b64_json"}, MinCount: 1, MaxCount: 1},
		Response:     types.ZTAPIImageResponseContract{Schema: "object_results_array", ResultsField: "data", ResultFields: map[string]string{"b64_json": "b64_json"}},
		Usage: types.ZTAPIImageUsageContract{UsageField: "usage", Fields: map[string]string{
			"text_input": "input_tokens_details.text_tokens", "image_input": "input_tokens_details.image_tokens", "image_output": "output_tokens_details.image_tokens",
		}, TotalField: "total_tokens", TotalSemantics: "sum_of_dimensions", CacheSemantics: "not_reported"},
		Reservations:    []types.ZTAPIImageReservationAuthority{{Size: "1024x1024", Quality: "low", ResponseFormat: "b64_json", N: 1, MaximumDimensions: map[string]string{"text_input": "200000", "image_input": "0", "image_output": "196"}}},
		RequestIDSource: types.ZTAPIResponseIDSourceHeader, RequestIDKey: "X-Request-ID", EvidenceVersion: types.ZTAPIImageEvidenceVersion,
		UpstreamRequestFields: map[string]string{"model": "required", "prompt": "required", "n": "required", "size": "required", "quality": "required", "response_format": "omit"},
	})
	require.NoError(t, err)

	publication := ZTAPIRuntimePublication{
		Modality: ZTAPIModalityImage, SourceModel: "gpt-image-2", PublicName: "zt-gp-image-2",
		MediaPriceContractJSON: mustCanonicalZTAPIMediaPriceContract(t, gpImage2ContractForTest(t)),
		ImageProtocolContract:  &protocol,
	}
	_, rules, _ := ztapiPublicMediaMetadata(publication)
	require.Len(t, rules, 3)
	dimensions := map[string]bool{}
	for _, rule := range rules {
		for dimension := range rule.SaleUSD {
			dimensions[dimension] = true
		}
	}
	require.Equal(t, map[string]bool{"text_input": true, "image_input": true, "image_output": true}, dimensions)
}

func TestZTAPIPublicCatalogTextJSONDoesNotGainMediaFields(t *testing.T) {
	items := buildZTAPIPublicCatalog([]ZTAPIRuntimePublication{{
		Modality: ZTAPIModalityText, SourceModel: "gpt-5.5", PublicName: "zt-gpt-5.5",
		ProviderFamily: ZTAPIProviderOpenAI, Protocol: ZTAPIProtocolOpenAICompatible,
		Groups: []string{"default"}, SnapshotID: 103,
		BillingDimensions: []string{ZTAPIBillingDimensionInputTokens, ZTAPIBillingDimensionOutputTokens},
		SaleUSD:           map[string]string{ZTAPIBillingDimensionInputTokens: "1", ZTAPIBillingDimensionOutputTokens: "2"},
	}})
	require.Len(t, items, 1)
	encoded, err := json.Marshal(items[0])
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "supported_options")
	require.NotContains(t, string(encoded), "pricing_rules")
	require.NotContains(t, string(encoded), "billing_unit")
}

func TestZTAPIPublicCatalogRejectsIncompleteMediaMetadata(t *testing.T) {
	items := buildZTAPIPublicCatalog([]ZTAPIRuntimePublication{{
		Modality: ZTAPIModalityImage, SourceModel: "candidate-image", PublicName: "zt-image",
		ProviderFamily: ZTAPIProviderOpenAI, Protocol: ZTAPIProtocolOpenAICompatible,
		Groups: []string{"default"}, SnapshotID: 104,
	}})
	require.Empty(t, items, "a media item without its frozen protocol and pricing contract must stay out of the public catalog")
}

func seedZTAPIPublicCatalogRecord(t *testing.T, db *gorm.DB, sourceModel, publicName, provider, protocol string, dimensions []string) ZTAPIModelConfig {
	t.Helper()
	groups, err := json.Marshal([]string{"default"})
	require.NoError(t, err)
	alias := publicName
	config := ZTAPIModelConfig{
		SourceModel: sourceModel, PublicName: &alias, Protocol: protocol,
		ProviderFamily: provider, Family: ZTAPIModelFamilyOpenAI,
		EnabledGroups: string(groups), Published: true, Version: 2,
	}
	require.NoError(t, db.Create(&config).Error)

	encodedDimensions, err := json.Marshal(dimensions)
	require.NoError(t, err)
	source := validZTAPIPriceSourceForTest()
	source.ModelConfigID = config.ID
	source.SourceModel = sourceModel
	source.SourceDocumentChecksum = ZTAPIQuotationSHA256
	source.BillingDimensions = string(encodedDimensions)
	if strings.Contains(source.BillingDimensions, ZTAPIBillingDimensionCacheRead) {
		source.CacheReadPerMillion = "0.2500000000"
	}
	require.NoError(t, db.Create(&source).Error)

	channels, _ := json.Marshal([]int{11, 13})
	snapshot := ZTAPIModelPublicationSnapshot{
		ModelConfigID: config.ID, ModelVersion: config.Version,
		SourceModel: sourceModel, PublicName: publicName, Protocol: protocol,
		ProviderFamily: provider, EnabledGroups: string(groups),
		AllowedChannelIDs: string(channels), PriceSourceID: source.ID,
		InputPricePerMillion: 1.6666666667, OutputPricePerMillion: 3.3333333333,
		CacheReadRatio: 0.25, CacheCreationRatio: 1.25,
		CacheCreation5mRatio: 1.25, CacheCreation1hRatio: 2,
		ImageRatio: 1, AudioRatio: 1, AudioCompletionRatio: 2,
		VerificationIDs: `[]`, IdentityUpdatedAt: 1, CreatedAt: 2,
	}
	require.NoError(t, db.Create(&snapshot).Error)
	require.NoError(t, db.Model(&config).Update("publication_snapshot_id", snapshot.ID).Error)
	config.PublicationSnapshotID = snapshot.ID
	return config
}

func TestZTAPIPublicCatalogUsesOnlyActiveImmutableSnapshots(t *testing.T) {
	db := setupZTAPIPublicCatalogTestDB(t)
	config := seedZTAPIPublicCatalogRecord(t, db, "gpt-5.5", "zt-gpt-5.5", ZTAPIProviderOpenAI, ZTAPIProtocolOpenAICompatible,
		[]string{ZTAPIBillingDimensionInputTokens, ZTAPIBillingDimensionOutputTokens})
	draftAlias := "draft-alias"
	require.NoError(t, db.Create(&ZTAPIModelConfig{
		SourceModel: "private-draft-id", PublicName: &draftAlias,
		EnabledGroups: `["default"]`, Published: false, Version: 1,
	}).Error)

	// Mutable configuration rows are not public authority after publication.
	require.NoError(t, db.Model(&config).Updates(map[string]any{
		"public_name": "tampered-alias", "input_price_per_million": 99,
	}).Error)

	catalog, err := ListZTAPIPublicCatalog()
	require.NoError(t, err)
	require.Len(t, catalog, 1)
	require.Equal(t, "zt-gpt-5.5", catalog[0].ModelName)
	require.Equal(t, "OpenAI", catalog[0].ProviderName)
	require.Equal(t, "1.6666666667", catalog[0].InputPricePerMillion)
	require.Equal(t, "3.3333333333", catalog[0].OutputPricePerMillion)
	require.Equal(t, "token", catalog[0].BillingRule)
	require.NotContains(t, ZTAPIPublicCatalogNames(catalog), "draft-alias")

	encoded, err := json.Marshal(catalog[0])
	require.NoError(t, err)
	require.NotContains(t, string(encoded), `"source_model"`)
	require.NotContains(t, string(encoded), "price_source_id")
	require.NotContains(t, string(encoded), "channel")
}

func TestZTAPIPublicCatalogReloadAcceptsPersistedPriceScale(t *testing.T) {
	db := setupZTAPIPublicCatalogTestDB(t)
	config := seedZTAPIPublicCatalogRecord(
		t, db, "qwen3.7-max", "zt-qwen-3.7-max", ZTAPIProviderQwen,
		ZTAPIProtocolOpenAICompatible,
		[]string{ZTAPIBillingDimensionInputTokens, ZTAPIBillingDimensionOutputTokens},
	)

	var snapshot ZTAPIModelPublicationSnapshot
	require.NoError(t, db.First(&snapshot, config.PublicationSnapshotID).Error)
	require.NoError(t, db.Model(&ZTAPIModelPriceSource{}).Where("id = ?", snapshot.PriceSourceID).Updates(map[string]any{
		"currency": "CNY", "input_per_million": "6.0000000000", "output_per_million": "18.0000000000",
		"cny_per_usd": "6.7852000000",
	}).Error)
	// Seed persisted scale values solely to exercise read-side decimal normalization.
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Model(&snapshot).Updates(map[string]any{
		"input_price_per_million": 1.51800979, "output_price_per_million": 4.55402936,
	}).Error)
	InvalidateZTAPIAliasCache()

	catalog, err := ListZTAPIPublicCatalog()
	require.NoError(t, err)
	require.Len(t, catalog, 1)
	require.Equal(t, "zt-qwen-3.7-max", catalog[0].ModelName)
	require.Equal(t, "1.5180097860", catalog[0].InputPricePerMillion)
	require.Equal(t, "4.5540293580", catalog[0].OutputPricePerMillion)
}

func TestZTAPIPublicCatalogPreservesComplexBillingDimensions(t *testing.T) {
	db := setupZTAPIPublicCatalogTestDB(t)
	seedZTAPIPublicCatalogRecord(t, db, "claude-sonnet-5", "zt-claude-sonnet-5", ZTAPIProviderAnthropic, ZTAPIProtocolOpenAICompatible,
		[]string{ZTAPIBillingDimensionInputTokens, ZTAPIBillingDimensionOutputTokens, ZTAPIBillingDimensionCacheRead})

	catalog, err := ListZTAPIPublicCatalog()
	require.NoError(t, err)
	require.Len(t, catalog, 1)
	require.Equal(t, "multi_dimension", catalog[0].BillingRule)
	require.ElementsMatch(t, []string{"cache_read", "input_tokens", "output_tokens"}, catalog[0].BillingDimensions)
	require.Equal(t, "0.4166666667", catalog[0].SaleUSD[ZTAPIBillingDimensionCacheRead])
	require.Equal(t, "Claude", catalog[0].ProviderName)
}

func TestZTAPIPublicCatalogRejectsLegacySnapshotWithoutFrozenPricing(t *testing.T) {
	db := setupZTAPIPublicCatalogTestDB(t)
	config := seedZTAPIPublicCatalogRecord(t, db, "gpt-5.5", "zt-gpt-5.5", ZTAPIProviderOpenAI, ZTAPIProtocolOpenAICompatible,
		[]string{ZTAPIBillingDimensionInputTokens, ZTAPIBillingDimensionOutputTokens})
	// Seed a legacy incomplete snapshot solely to exercise read-side rejection.
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Model(&ZTAPIModelPublicationSnapshot{}).
		Where("id = ?", config.PublicationSnapshotID).
		Update("input_price_per_million", 0).Error)

	catalog, err := ListZTAPIPublicCatalog()
	require.NoError(t, err)
	require.Empty(t, catalog)
}

func TestZTAPIRuntimePublicationCacheDeepCopiesImageProtocolContract(t *testing.T) {
	db := setupZTAPIPublicCatalogTestDB(t)
	config := seedZTAPIPublicCatalogRecord(t, db, "gpt-5.5", "zt-gpt-5.5", ZTAPIProviderOpenAI, ZTAPIProtocolOpenAICompatible,
		[]string{ZTAPIBillingDimensionInputTokens, ZTAPIBillingDimensionOutputTokens})
	contractJSON := syntheticVerifiedImageProtocolJSON(t, config.SourceModel)
	mediaPriceContractJSON := mustCanonicalZTAPIMediaPriceContract(t, geminiImageContractForTest(t))
	// Seed a legacy persisted contract solely to exercise read-side deep-copy behavior.
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Model(&ZTAPIModelPublicationSnapshot{}).
		Where("id = ?", config.PublicationSnapshotID).
		Updates(map[string]any{"image_protocol_contract_json": contractJSON, "media_price_contract_json": mediaPriceContractJSON}).Error)
	InvalidateZTAPIAliasCache()

	first, err := GetZTAPIRuntimePublication(config.PublicNameValue())
	require.NoError(t, err)
	require.NotNil(t, first.ImageProtocolContract)
	require.Equal(t, mediaPriceContractJSON, first.MediaPriceContractJSON)
	first.ImageProtocolContract.Capabilities.Sizes[0] = "changed"
	first.ImageProtocolContract.Response.ResultFields["url"] = "changed"

	second, err := GetZTAPIRuntimePublication(config.PublicNameValue())
	require.NoError(t, err)
	require.Equal(t, "1024x1024", second.ImageProtocolContract.Capabilities.Sizes[0])
	require.Equal(t, "url", second.ImageProtocolContract.Response.ResultFields["url"])
	require.Equal(t, mediaPriceContractJSON, second.MediaPriceContractJSON)
}

func TestZTAPIRuntimePublicationCacheDeepCopiesVideoProtocolContract(t *testing.T) {
	db := setupZTAPIPublicCatalogTestDB(t)
	config := seedZTAPIPublicCatalogRecord(t, db, "provider-video-exact", "zt-seedance-2-fast", ZTAPIProviderSeedance, ZTAPIProtocolOpenAICompatible,
		[]string{ZTAPIBillingDimensionInputTokens, ZTAPIBillingDimensionOutputTokens})
	originalQuotation := ztapiQuotation
	ztapiQuotation.Entries = append(append([]ZTAPIQuotationEntry(nil), ztapiQuotation.Entries...), ZTAPIQuotationEntry{
		Label: "synthetic-video", SourceModel: config.SourceModel, PublicName: config.PublicNameValue(),
		Protocol: config.Protocol, ProviderFamily: config.ProviderFamily, Modality: ZTAPIModalityVideo, Status: "mapped",
		QuoteRows: []ZTAPIQuotationRow{{Resource: "企业资源"}},
	})
	t.Cleanup(func() { ztapiQuotation = originalQuotation })
	contractJSON := canonicalZTAPIVideoProtocolForPublicationTest(t, config.SourceModel)
	mediaPriceContractJSON := mustCanonicalZTAPIMediaPriceContract(t, seedanceContractForTest(t))
	var snapshot ZTAPIModelPublicationSnapshot
	require.NoError(t, db.First(&snapshot, config.PublicationSnapshotID).Error)
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Model(&ZTAPIModelPriceSource{}).
		Where("id = ?", snapshot.PriceSourceID).Updates(map[string]any{
		"price_policy":              string(ZTAPIPricePolicyEnterprise20Margin),
		"media_price_contract_json": mediaPriceContractJSON,
	}).Error)
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Model(&ZTAPIModelPublicationSnapshot{}).
		Where("id = ?", config.PublicationSnapshotID).
		Updates(map[string]any{
			"price_policy":                 string(ZTAPIPricePolicyEnterprise20Margin),
			"input_price_per_million":      1.25,
			"output_price_per_million":     2.5,
			"video_protocol_contract_json": contractJSON,
			"media_price_contract_json":    mediaPriceContractJSON,
		}).Error)
	InvalidateZTAPIAliasCache()

	first, err := GetZTAPIRuntimePublication(config.PublicNameValue())
	require.NoError(t, err)
	require.NotNil(t, first.VideoProtocolContract)
	require.Equal(t, mediaPriceContractJSON, first.MediaPriceContractJSON)
	first.VideoProtocolContract.Capabilities.Resolutions[0] = "changed"
	first.VideoProtocolContract.States.Accepted[0] = "changed"
	first.VideoProtocolContract.Usage.Fields["input_tokens"] = "changed"

	second, err := GetZTAPIRuntimePublication(config.PublicNameValue())
	require.NoError(t, err)
	require.Equal(t, "720p", second.VideoProtocolContract.Capabilities.Resolutions[0])
	require.Equal(t, "queued", second.VideoProtocolContract.States.Accepted[0])
	require.Equal(t, "data.usage.input_tokens", second.VideoProtocolContract.Usage.Fields["input_tokens"])
}
