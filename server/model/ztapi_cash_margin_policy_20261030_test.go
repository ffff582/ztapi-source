package model

import (
	"testing"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestCashMargin20261030PoliciesDoNotReinterpretProcurementResource(t *testing.T) {
	for _, tc := range []struct{ resource, policy, want string }{
		{"enterprise", "cash_enterprise_20_20261030", "1.3125"},
		{"enterprise", "cash_pool_30_20261030", "1.5"},
		{"pool", "cash_pool_30_20261030", "1.5"},
	} {
		require.NoError(t, ValidateZTAPIPricePolicy(tc.resource, ZTAPIPricePolicy(tc.policy)))
		price, err := CalculateZTAPISalePriceForPolicy(decimal.NewFromInt(1), ZTAPIPricePolicy(tc.policy))
		require.NoError(t, err)
		require.Equal(t, tc.want, price.String())
	}
	old, err := CalculateZTAPISalePriceForPolicy(decimal.NewFromInt(1), ZTAPIPricePolicyEnterprise20Margin)
	require.NoError(t, err)
	require.Equal(t, "1.25", old.String())
}

func cashMarginFixture(t *testing.T, db *gorm.DB, name string) (ZTAPIModelConfig, ZTAPIModelPublicationSnapshot, ZTAPIModelPriceSource) {
	t.Helper()
	sourceName, publicName, channel := "gemini-3-flash-preview", "zt-gemini-3-flash-preview", 1
	if name == "Gemini 3.1 Pro Preview" {
		sourceName, publicName, channel = "gemini-3.1-pro-preview", "zt-gemini-3.1-pro-preview", 2
	} else if name == "Kimi K2.7 Code" {
		sourceName, publicName, channel = "kimi-k2.7-code", "zt-kimi-k2.7-code", 2
	}
	provider := ZTAPIProviderGoogle
	if channel == 2 && sourceName == "kimi-k2.7-code" {
		provider = ZTAPIProviderMoonshot
	}
	config := ZTAPIModelConfig{SourceModel: sourceName, PublicName: &publicName, Family: ZTAPIModelFamilyOpenAI,
		Protocol: ZTAPIProtocolOpenAICompatible, ProviderFamily: provider, Version: 1, Published: true, EnabledGroups: `["default"]`,
		CacheCreationRatio: 1, CacheCreation5mRatio: 1, CacheCreation1hRatio: 1, ImageRatio: 1, AudioRatio: 1, AudioCompletionRatio: 1}
	require.NoError(t, db.Create(&config).Error)
	quote, err := ZTAPIQuotationABEntries()
	require.NoError(t, err)
	anchor, err := BuildZTAPIABTextPriceSourceWithFX(quote, name, config.ID, 7, 1_789_000_000, platformFXParamsForTest("6.63", "6.7208"))
	require.NoError(t, err)
	anchor.Version = 1
	require.NoError(t, db.Create(&anchor).Error)
	base, err := BuildZTAPISupplierPriceSource20261019WithDB(db, &anchor, 7, 1_792_414_023, platformFXParamsForTest("6.63", "6.7208"))
	require.NoError(t, err)
	base.Version = 2
	require.NoError(t, db.Create(&base).Error)
	preview, err := BuildZTAPIModelPricePreview(&base)
	require.NoError(t, err)
	require.NoError(t, applyZTAPICommercialPreview(&config, preview))
	require.NoError(t, db.Save(&config).Error)
	snapshot, err := createZTAPIModelPublicationSnapshotTx(db, &config, config.Version, ztapiPublicationEvidence{
		PriceSourceID: base.ID, AllowedChannelIDs: []int{channel}, VerificationIDs: []int64{31}, IdentityUpdatedAt: 1})
	require.NoError(t, err)
	require.NoError(t, db.Model(&config).Update("publication_snapshot_id", snapshot.ID).Error)
	require.NoError(t, db.First(&base, base.ID).Error)
	require.NoError(t, db.First(snapshot, snapshot.ID).Error)
	return config, *snapshot, base
}

func TestCashMargin20261030DerivationPreservesBasisAndEveryTier(t *testing.T) {
	db := setupZTAPIPublicCatalogTestDB(t)
	for _, name := range []string{"Gemini 3 Flash Preview", "Gemini 3.1 Pro Preview", "Kimi K2.7 Code"} {
		_, _, base := cashMarginFixture(t, db, name)
		before := base
		target, err := BuildZTAPICashMarginSource20261030(db, &base, 9)
		require.NoError(t, err)
		require.Equal(t, before, base)
		require.Equal(t, base.ResourceType, target.ResourceType)
		require.Equal(t, base.InputPerMillion, target.InputPerMillion)
		require.Equal(t, base.OutputPerMillion, target.OutputPerMillion)
		require.Equal(t, base.CNYPerUSD, target.CNYPerUSD)
		require.Equal(t, base.QuotationEffectiveAt, target.QuotationEffectiveAt)
		require.Equal(t, base.ID, target.CashMarginBaseSourceID)
		require.NoError(t, ValidateZTAPICashMarginSource20261030(db, &target))
		entry, err := ztapiSupplierPriceEntryFor20261019(base.SourceModel)
		require.NoError(t, err)
		rules := supplierRulesForTest(t, &target)
		require.Len(t, rules, len(entry.Rules))
		share := decimal.RequireFromString("0.7")
		if name == "Gemini 3 Flash Preview" {
			share = decimal.RequireFromString("0.8")
		}
		for i, rule := range rules {
			require.Equal(t, entry.Rules[i].Conditions, rule.Conditions)
			for dim, official := range entry.Rules[i].Official {
				cost := decimal.RequireFromString(official).Mul(decimal.RequireFromString(entry.Fraction)).Round(10)
				if entry.Currency == "USD" {
					cost = cost.Mul(decimal.RequireFromString(base.UpstreamCNYPerUSD)).Round(10)
				}
				sale := decimal.RequireFromString(rule.Sale[dim])
				// Multiply rather than divide: no rounded division may hide an underpriced tier.
				require.True(t, sale.Mul(share).Mul(decimal.RequireFromString(base.CNYPerUSD)).GreaterThanOrEqual(cost.Mul(decimal.RequireFromString("1.05"))))
				require.True(t, sale.Equal(sale.Round(8)), "persisted 8-place prices must not round below the floor")
			}
		}
		preview, err := BuildZTAPIModelPricePreview(&target)
		require.NoError(t, err)
		require.NotEmpty(t, preview.SaleUSD)
	}
}

func TestCashMargin20261030RejectsForgedSourcesAndMissingBase(t *testing.T) {
	db := setupZTAPIPublicCatalogTestDB(t)
	_, _, base := cashMarginFixture(t, db, "Gemini 3.1 Pro Preview")
	target, err := BuildZTAPICashMarginSource20261030(db, &base, 9)
	require.NoError(t, err)
	for name, mutate := range map[string]func(*ZTAPIModelPriceSource){
		"cost":       func(s *ZTAPIModelPriceSource) { s.InputPerMillion = "0.01" },
		"fx":         func(s *ZTAPIModelPriceSource) { s.CNYPerUSD, s.PlatformCNYPerUnit = "7", "7" },
		"resource":   func(s *ZTAPIModelPriceSource) { s.ResourceType = "pool" },
		"base":       func(s *ZTAPIModelPriceSource) { s.CashMarginBaseSourceID++ },
		"model":      func(s *ZTAPIModelPriceSource) { s.SourceModel = "glm-5.2" },
		"config":     func(s *ZTAPIModelPriceSource) { s.ModelConfigID++ },
		"multiplier": func(s *ZTAPIModelPriceSource) { s.SaleMultiplier = "0.9" },
		"tiers":      func(s *ZTAPIModelPriceSource) { r := supplierRulesForTest(t, s); setSupplierRulesForTest(t, s, r[:1]) },
		"sale": func(s *ZTAPIModelPriceSource) {
			r := supplierRulesForTest(t, s)
			r[0].Sale[ZTAPIBillingDimensionCacheRead] = "99"
			setSupplierRulesForTest(t, s, r)
		},
	} {
		t.Run(name, func(t *testing.T) {
			bad := target
			mutate(&bad)
			require.Error(t, ValidateZTAPICashMarginSource20261030(db, &bad))
		})
	}
	badBase := base
	badBase.InputPerMillion = "0.01"
	_, err = BuildZTAPICashMarginSource20261030(db, &badBase, 9)
	require.Error(t, err)
	require.NoError(t, db.Delete(&base).Error)
	require.Error(t, ValidateZTAPICashMarginSource20261030(db, &target))
}

func TestCashMargin20261030SnapshotCannotBypassParentAndRouteValidation(t *testing.T) {
	db := setupZTAPIPublicCatalogTestDB(t)
	config, _, base := cashMarginFixture(t, db, "Gemini 3 Flash Preview")
	target, err := BuildZTAPICashMarginSource20261030(db, &base, 9)
	require.NoError(t, err)
	target.Version = 3
	require.NoError(t, db.Create(&target).Error)
	preview, err := BuildZTAPIModelPricePreview(&target)
	require.NoError(t, err)
	require.NoError(t, applyZTAPICommercialPreview(&config, preview))
	_, err = createZTAPIModelPublicationSnapshotTx(db, &config, config.Version+1, ztapiPublicationEvidence{
		PriceSourceID: target.ID, AllowedChannelIDs: []int{2}, VerificationIDs: []int64{31}})
	require.Error(t, err, "a valid 20%% source must not publish on the actual pool route")
	require.NoError(t, db.Exec("UPDATE ztapi_model_price_sources SET input_per_million = ? WHERE id = ?", "0.01", base.ID).Error)
	_, err = createZTAPIModelPublicationSnapshotTx(db, &config, config.Version+1, ztapiPublicationEvidence{
		PriceSourceID: target.ID, AllowedChannelIDs: []int{1}, VerificationIDs: []int64{31}})
	require.Error(t, err, "snapshot hooks must reload and verify original procurement evidence")
}
