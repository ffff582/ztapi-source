package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestCashPortfolio20261030AddsEnterpriseBasisWithoutChangingCosts(t *testing.T) {
	db := setupZTAPIPublicCatalogTestDB(t)
	quote, err := ZTAPIQuotationABEntries()
	require.NoError(t, err)
	config := seedZTAPIPublicCatalogRecord(t, db, "gpt-4.1-mini", "zt-gpt-4.1-mini", ZTAPIProviderOpenAI,
		ZTAPIProtocolOpenAICompatible, []string{ZTAPIBillingDimensionInputTokens, ZTAPIBillingDimensionOutputTokens, ZTAPIBillingDimensionCacheRead})
	base, err := BuildZTAPIABTextPriceSourceWithFX(quote, "GPT 4.1 Mini", config.ID, 7, ztapiFXPreviewEffectiveAt, platformFXParamsForTest("6.63", "6.7138"))
	require.NoError(t, err)
	base.Version = 1
	require.NoError(t, db.Create(&base).Error)
	before := base
	derived, err := BuildZTAPICashMarginSource20261030(db, &base, 9)
	require.NoError(t, err)
	require.Equal(t, before, base)
	require.True(t, ztapiSameDecimal(base.InputPerMillion, derived.InputPerMillion))
	require.Equal(t, base.ResourceType, derived.ResourceType)
	require.Equal(t, base.ID, derived.CashMarginBaseSourceID)
	require.NoError(t, ValidateZTAPICashMarginSource20261030(db, &derived))
	var rules []ztapiABFrozenSaleRule
	require.NoError(t, common.Unmarshal([]byte(derived.TokenPriceRulesJSON), &rules))
	require.NotEmpty(t, rules)
	for _, rule := range rules {
		for dimension, sale := range rule.Sale {
			cost := decimal.RequireFromString(ztapiPriceSourceValues(&base)[dimension]).Div(decimal.RequireFromString(base.CNYPerUSD))
			revenue := decimal.RequireFromString(sale).Div(decimal.RequireFromString("1.05"))
			require.True(t, revenue.Mul(decimal.RequireFromString("0.8")).GreaterThanOrEqual(cost), dimension)
		}
	}
}

type cashPortfolioFixtureCase struct{ name, source, public, provider string }

var cashPortfolioFixtureCases = []cashPortfolioFixtureCase{
	{"GPT 4.1 Mini", "gpt-4.1-mini", "zt-gpt-4.1-mini", ZTAPIProviderOpenAI},
	{"GPT 4.1 Nano", "gpt-4.1-nano", "zt-gpt-4.1-nano", ZTAPIProviderOpenAI},
	{"GPT 4o Mini", "gpt-4o-mini", "zt-gpt-4o-mini-2024-07-18", ZTAPIProviderOpenAI},
	{"GPT 5 Nano", "gpt-5-nano", "zt-gpt-5-nano", ZTAPIProviderOpenAI},
	{"Gemini 3.1 Flash Lite Preview", "gemini-3.1-flash-lite-preview", "zt-gemini-3.1-flash-lite-preview", ZTAPIProviderGoogle},
	{"Gemini 3.5 Flash", "gemini-3.5-flash", "zt-gemini-3.5-flash", ZTAPIProviderGoogle},
	{"Text Embedding 3 Small", "text-embedding-3-small", "zt-text-embedding-3-small", ZTAPIProviderOpenAI},
	{"Text Embedding Ada 002", "text-embedding-ada-002", "zt-text-embedding-ada-002", ZTAPIProviderOpenAI},
	{"Claude Fable 5", "claude-fable-5", "zt-claude-fable-5", ZTAPIProviderAnthropic},
	{"Claude Sonnet 5", "claude-sonnet-5", "zt-claude-sonnet-5", ZTAPIProviderAnthropic},
}

func seedCashPortfolioFixture(t *testing.T, db *gorm.DB, tc cashPortfolioFixtureCase) (ZTAPIModelConfig, ZTAPIModelPublicationSnapshot, ZTAPIModelPriceSource) {
	t.Helper()
	config := seedZTAPIPublicCatalogRecord(t, db, tc.source, tc.public, tc.provider, ZTAPIProtocolOpenAICompatible,
		[]string{ZTAPIBillingDimensionInputTokens, ZTAPIBillingDimensionOutputTokens})
	quote, err := ZTAPIQuotationABEntries()
	require.NoError(t, err)
	fx := platformFXParamsForTest("6.63", "6.7138")
	base, err := BuildZTAPIABTextPriceSourceWithFX(quote, tc.name, config.ID, 7, ztapiFXPreviewEffectiveAt, fx)
	if tc.source == "claude-sonnet-5" {
		base, err = BuildZTAPIABPoolOfficial78TextPriceSourceWithFX(quote, tc.name, config.ID, 7, ztapiFXPreviewEffectiveAt, fx)
	}
	require.NoError(t, err)
	base.Version = 2
	require.NoError(t, db.Create(&base).Error)
	if tc.source == "claude-sonnet-5" {
		base, err = BuildZTAPISupplierPriceSource20261019WithDB(db, &base, 7, 1_792_414_023, fx)
		require.NoError(t, err)
		base.Version = 3
		require.NoError(t, db.Create(&base).Error)
	}
	preview, err := BuildZTAPIModelPricePreview(&base)
	require.NoError(t, err)
	config.CacheReadRatio, config.CacheCreationRatio = 1, 1
	config.CacheCreation5mRatio, config.CacheCreation1hRatio = 1, 1
	config.ImageRatio, config.AudioRatio, config.AudioCompletionRatio = 1, 1, 1
	require.NoError(t, applyZTAPICommercialPreview(&config, preview))
	require.NoError(t, db.Save(&config).Error)
	channels, mode := []int{1}, ""
	if tc.provider == ZTAPIProviderAnthropic {
		channels = []int{2, 4}
	}
	if tc.source == "claude-fable-5" {
		mode = ZTAPIFableUniformCacheWriteMode
	}
	published, err := createZTAPIModelPublicationSnapshotTx(db, &config, config.Version, ztapiPublicationEvidence{PriceSourceID: base.ID, AllowedChannelIDs: channels, VerificationIDs: []int64{31}, IdentityUpdatedAt: 1, CacheWritePricingMode: mode})
	require.NoError(t, err)
	require.NoError(t, db.Model(&config).Update("publication_snapshot_id", published.ID).Error)
	require.NoError(t, db.First(&base, base.ID).Error)
	return config, *published, base
}

func TestCashPortfolio20261030AllTenApplyReadAndRestore(t *testing.T) {
	db := setupZTAPIPublicCatalogTestDB(t)
	require.NoError(t, db.AutoMigrate(&ZTAPIAuditEvent{}, &ZTAPICatalogLock{}, &ZTAPIRequestSettlement{}))
	var selections []ZTAPICashMarginSelection20261030
	var originals []ZTAPIModelPublicationSnapshot
	for _, tc := range cashPortfolioFixtureCases {
		config, snapshot, _ := seedCashPortfolioFixture(t, db, tc)
		selections = append(selections, ZTAPICashMarginSelection20261030{ModelID: config.ID, Version: config.Version, SnapshotID: snapshot.ID})
		originals = append(originals, snapshot)
	}
	preview, err := PreviewZTAPICashMargin20261030(selections, false)
	require.NoError(t, err)
	require.Len(t, preview, 10)
	result, err := ApplyZTAPICashMargin20261030(9, selections, false)
	require.NoError(t, err)
	require.Equal(t, 10, result.Republished)
	retried, err := ApplyZTAPICashMargin20261030(9, selections, false)
	require.NoError(t, err)
	require.Equal(t, 10, retried.Unchanged)
	publications, err := loadZTAPIQuotedPublications(db)
	require.NoError(t, err)
	require.Len(t, publications, 10)
	guard, err := CheckZTAPICashMarginDowngrade20261030()
	require.NoError(t, err)
	require.False(t, guard.CanDowngrade)
	require.Len(t, guard.ActiveModels, 10)
	publicPrices := projectZTAPIPublicPricing(publications)
	for _, row := range publicPrices {
		if row.ModelName == "zt-claude-sonnet-5" {
			require.InDelta(t, 0.16875/1.56, *row.CacheRatio, 0.00000001)
		}
		if row.ModelName == "zt-claude-fable-5" {
			require.Equal(t, "cache_write", row.BillingDimensions[len(row.BillingDimensions)-1])
			require.NotEmpty(t, row.TokenPriceRules)
			for _, tier := range row.TokenPriceRules {
				require.NotContains(t, tier.SaleUSD, "cache_write_5m")
				require.NotContains(t, tier.SaleUSD, "cache_write_1h")
				require.True(t, decimal.RequireFromString(tier.SaleUSD["cache_write"]).GreaterThan(decimal.RequireFromString(ZTAPIFableUniformCacheWritePrice)))
			}
		}
	}
	var rollback []ZTAPICashMarginSelection20261030
	for index, selected := range selections {
		var config ZTAPIModelConfig
		require.NoError(t, db.First(&config, selected.ModelID).Error)
		rollback = append(rollback, ZTAPICashMarginSelection20261030{ModelID: config.ID, Version: config.Version, SnapshotID: config.PublicationSnapshotID, RestoreSnapshotID: originals[index].ID})
	}
	result, err = ApplyZTAPICashMargin20261030(9, rollback, true)
	require.NoError(t, err)
	require.Equal(t, 10, result.Republished)
	publications, err = loadZTAPIQuotedPublications(db)
	require.NoError(t, err)
	require.Len(t, publications, 10)
	for index, selected := range selections {
		var config ZTAPIModelConfig
		var restored ZTAPIModelPublicationSnapshot
		require.NoError(t, db.First(&config, selected.ModelID).Error)
		require.NoError(t, db.First(&restored, config.PublicationSnapshotID).Error)
		require.Equal(t, originals[index].PricePolicy, restored.PricePolicy)
		require.Equal(t, originals[index].CacheWritePricingMode, restored.CacheWritePricingMode)
		require.Equal(t, originals[index].InputPricePerMillion, restored.InputPricePerMillion)
	}
	guard, err = CheckZTAPICashMarginDowngrade20261030()
	require.NoError(t, err)
	require.True(t, guard.CanDowngrade)
}

func TestCashPortfolio20261030SonnetGroupAndCacheFloors(t *testing.T) {
	db := setupZTAPIPublicCatalogTestDB(t)
	_, _, base := seedCashPortfolioFixture(t, db, cashPortfolioFixtureCases[9])
	derived, err := BuildZTAPICashMarginSource20261030(db, &base, 9)
	require.NoError(t, err)
	preview, err := BuildZTAPIModelPricePreview(&derived)
	require.NoError(t, err)
	for dimension, expected := range map[string]string{"input_tokens": "1.56", "output_tokens": "7.875", "cache_read": "0.16875", "cache_write_5m": "2.1375", "cache_write_1h": "3.12"} {
		require.True(t, ztapiSameDecimal(preview.SaleUSD[dimension], expected), dimension)
	}
}
