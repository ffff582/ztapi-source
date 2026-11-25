package router

import (
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func seedWorkbenchRouterPublicModel(t *testing.T, db *gorm.DB, channelID int, sourceModel, publicName string, extraChannels ...int) {
	t.Helper()
	now := common.GetTimestamp()
	config := model.ZTAPIModelConfig{
		SourceModel: sourceModel, PublicName: &publicName,
		Family: model.ZTAPIModelFamilyOpenAI, Protocol: model.ZTAPIProtocolOpenAICompatible,
		ProviderFamily:      model.ZTAPIProviderOpenAI,
		InputCostPerMillion: 1, OutputCostPerMillion: 2,
		InputPricePerMillion: 1.6666666667, OutputPricePerMillion: 3.3333333333,
		CacheReadRatio: 0.1, CacheCreationRatio: 1.25,
		CacheCreation5mRatio: 1.25, CacheCreation1hRatio: 2,
		ImageRatio: 1, AudioRatio: 1, AudioCompletionRatio: 2,
		EnabledGroups: `["default"]`, Published: true, Version: 1,
		CreatedAt: now, UpdatedAt: now,
	}
	if strings.HasPrefix(sourceModel, "claude-") {
		config.Family, config.ProviderFamily = model.ZTAPIModelFamilyClaude, model.ZTAPIProviderAnthropic
		config.CacheReadRatio = 0.25
	}
	if err := db.Create(&config).Error; err != nil {
		t.Fatalf("create router model publication: %v", err)
	}
	price := model.ZTAPIModelPriceSource{
		ModelConfigID: config.ID, SourceModel: sourceModel,
		ResourceType: "enterprise", SpendTier: "test",
		BillingDimensions: `["input_tokens","output_tokens"]`, Currency: "USD",
		InputPerMillion: "1", OutputPerMillion: "2",
		CacheReadPerMillion: "0", CacheWritePerMillion: "0",
		CacheWrite5mPerMillion: "0", CacheWrite1hPerMillion: "0",
		ImageUnitCost: "0", AudioUnitCost: "0", RequestUnitCost: "0", CNYPerUSD: "0",
		QuotationEffectiveAt: now, SourceDocumentChecksum: model.ZTAPIQuotationSHA256,
		OperatorID: 1, Version: 1, CreatedAt: now,
	}
	if sourceModel == "gpt-6.1-sol" || sourceModel == "claude-opus-5-5" {
		quote := "GPT 6.1 Sol"
		if sourceModel == "claude-opus-5-5" {
			quote = "Claude Opus 5.5"
		}
		var err error
		price, err = model.BuildZTAPISupplierAdmissionTextPriceSource20261019(quote, config.ID, 1, now, model.ZTAPIABFXParams{
			Mode: model.ZTAPIFXModePlatformV1, PlatformRate: decimal.RequireFromString("6.63"), UpstreamUSDRate: decimal.RequireFromString("6.7628"),
		})
		require.NoError(t, err)
		price.Version, price.CreatedAt = 1, now
		preview, err := model.BuildZTAPIModelPricePreview(&price)
		require.NoError(t, err)
		config.InputPricePerMillion = decimal.RequireFromString(preview.InputSaleUSDPerMillion).InexactFloat64()
		config.OutputPricePerMillion = decimal.RequireFromString(preview.OutputSaleUSDPerMillion).InexactFloat64()
		input := decimal.RequireFromString(preview.InputCostUSDPerMillion)
		config.CacheReadRatio = decimal.RequireFromString(preview.CostUSD[model.ZTAPIBillingDimensionCacheRead]).Div(input).Round(8).InexactFloat64()
		for dimension, ratio := range map[string]*float64{
			model.ZTAPIBillingDimensionCacheWrite:   &config.CacheCreationRatio,
			model.ZTAPIBillingDimensionCacheWrite5m: &config.CacheCreation5mRatio,
			model.ZTAPIBillingDimensionCacheWrite1h: &config.CacheCreation1hRatio,
		} {
			if value, ok := preview.CostUSD[dimension]; ok {
				*ratio = decimal.RequireFromString(value).Div(input).Round(8).InexactFloat64()
			}
		}
		require.NoError(t, db.Save(&config).Error)
	}
	if config.ProviderFamily == model.ZTAPIProviderAnthropic && sourceModel != "claude-fable-5" && sourceModel != "claude-opus-5-5" {
		price.BillingDimensions = `["input_tokens","output_tokens","cache_read"]`
		price.CacheReadPerMillion = "0.25"
	}
	if sourceModel == "claude-fable-5" || sourceModel == "glm-5.3" {
		if sourceModel == "claude-fable-5" {
			quote, err := model.ZTAPIQuotationABEntries()
			require.NoError(t, err)
			price, err = model.BuildZTAPIABTextPriceSource(quote, "Claude Fable 5", config.ID, 1, now)
			require.NoError(t, err)
		} else {
			config.ProviderFamily = model.ZTAPIProviderGLM
			price.ResourceType, price.SpendTier = "pool", "xcloud-cn"
			price.PricePolicy = string(model.ZTAPIPricePolicyXcloudGLM53)
			price.SourceDocumentChecksum = model.ZTAPIXcloudGLM53EvidenceSHA256
			price.InputPerMillion, price.OutputPerMillion, price.CacheReadPerMillion = "2.4", "8.4", "0.6"
			price.BillingDimensions = `["cache_read","input_tokens","output_tokens"]`
			price.SaleMultiplier = "1"
		}
		price.Version, price.CreatedAt = 1, now
		preview, err := model.BuildZTAPIModelPricePreview(&price)
		require.NoError(t, err)
		config.InputPricePerMillion = decimal.RequireFromString(preview.InputSaleUSDPerMillion).InexactFloat64()
		config.OutputPricePerMillion = decimal.RequireFromString(preview.OutputSaleUSDPerMillion).InexactFloat64()
		input := decimal.RequireFromString(preview.CostUSD[model.ZTAPIBillingDimensionInputTokens])
		for dimension, ratio := range map[string]*float64{
			model.ZTAPIBillingDimensionCacheRead:    &config.CacheReadRatio,
			model.ZTAPIBillingDimensionCacheWrite:   &config.CacheCreationRatio,
			model.ZTAPIBillingDimensionCacheWrite5m: &config.CacheCreation5mRatio,
			model.ZTAPIBillingDimensionCacheWrite1h: &config.CacheCreation1hRatio,
		} {
			if value, ok := preview.CostUSD[dimension]; ok {
				*ratio = decimal.RequireFromString(value).Div(input).Round(8).InexactFloat64()
			}
		}
		require.NoError(t, db.Save(&config).Error)
	}
	if err := db.Create(&price).Error; err != nil {
		t.Fatalf("create router model price evidence: %v", err)
	}
	channelIDs, err := common.Marshal(append(extraChannels, channelID))
	if err != nil {
		t.Fatalf("marshal router model channels: %v", err)
	}
	snapshot := model.ZTAPIModelPublicationSnapshot{
		ModelConfigID: config.ID, ModelVersion: config.Version,
		SourceModel: sourceModel, PublicName: publicName,
		Protocol: config.Protocol, ProviderFamily: config.ProviderFamily,
		EnabledGroups: config.EnabledGroups, AllowedChannelIDs: string(channelIDs), PriceSourceID: price.ID,
		PricePolicy: price.PricePolicy, TokenPriceRulesJSON: price.TokenPriceRulesJSON, SaleMultiplier: price.SaleMultiplier,
		InputPricePerMillion: config.InputPricePerMillion, OutputPricePerMillion: config.OutputPricePerMillion,
		CacheReadRatio: config.CacheReadRatio, CacheCreationRatio: config.CacheCreationRatio,
		CacheCreation5mRatio: config.CacheCreation5mRatio, CacheCreation1hRatio: config.CacheCreation1hRatio,
		ImageRatio: config.ImageRatio, AudioRatio: config.AudioRatio,
		AudioCompletionRatio: config.AudioCompletionRatio,
		VerificationIDs:      `[]`, IdentityUpdatedAt: now, CreatedAt: now,
	}
	if err := db.Create(&snapshot).Error; err != nil {
		t.Fatalf("create router model publication snapshot: %v", err)
	}
	if err := db.Model(&config).Update("publication_snapshot_id", snapshot.ID).Error; err != nil {
		t.Fatalf("attach router model publication snapshot: %v", err)
	}
	model.InvalidateZTAPIAliasCache()
}
