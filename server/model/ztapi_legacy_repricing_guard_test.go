package model

import (
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func seedBoundXcloud53ForRepricingTest(t *testing.T, db *gorm.DB) ZTAPIModelConfig {
	t.Helper()
	config := seedZTAPIPublicCatalogRecord(t, db, "glm-5.3", "zt-glm-5.3", ZTAPIProviderGLM,
		ZTAPIProtocolOpenAICompatible, []string{ZTAPIBillingDimensionInputTokens, ZTAPIBillingDimensionOutputTokens, ZTAPIBillingDimensionCacheRead})
	source := ZTAPIModelPriceSource{
		ModelConfigID: config.ID, SourceModel: config.SourceModel, Version: 2,
		ResourceType: "pool", PricePolicy: string(ZTAPIPricePolicyXcloudGLM53), SpendTier: "xcloud-cn",
		Currency: "USD", BillingDimensions: `["cache_read","input_tokens","output_tokens"]`,
		InputPerMillion: "2.4", OutputPerMillion: "8.4", CacheReadPerMillion: "0.6",
		CacheWritePerMillion: "0", CacheWrite5mPerMillion: "0", CacheWrite1hPerMillion: "0",
		ImageUnitCost: "0", AudioUnitCost: "0", RequestUnitCost: "0", CNYPerUSD: "0",
		SaleMultiplier: "1", QuotationEffectiveAt: 1793318400, OperatorID: 7,
		SourceDocumentChecksum: ZTAPIXcloudGLM53EvidenceSHA256,
	}
	require.NoError(t, db.Create(&source).Error)
	preview, err := BuildZTAPIModelPricePreview(&source)
	require.NoError(t, err)
	require.NoError(t, applyZTAPICommercialPreview(&config, preview))
	config.CacheCreationRatio, config.CacheCreation5mRatio, config.CacheCreation1hRatio = 1, 1, 1
	config.ImageRatio, config.AudioRatio, config.AudioCompletionRatio = 1, 1, 1
	require.NoError(t, db.Save(&config).Error)
	snapshot, err := createZTAPIModelPublicationSnapshotTx(db, &config, config.Version, ztapiPublicationEvidence{
		PriceSourceID: source.ID, AllowedChannelIDs: []int{8}, VerificationIDs: []int64{}, IdentityUpdatedAt: 1,
	})
	require.NoError(t, err)
	require.NoError(t, db.Model(&config).Update("publication_snapshot_id", snapshot.ID).Error)
	require.NoError(t, db.First(&config, config.ID).Error)
	publications, err := loadZTAPIQuotedPublications(db)
	require.NoError(t, err)
	require.Len(t, publications, 1)
	return config
}

type legacyRepricingStateForTest struct {
	Configs   []ZTAPIModelConfig
	Sources   []ZTAPIModelPriceSource
	Snapshots []ZTAPIModelPublicationSnapshot
	Audits    []ZTAPIAuditEvent
}

func readLegacyRepricingStateForTest(t *testing.T, db *gorm.DB) legacyRepricingStateForTest {
	t.Helper()
	var state legacyRepricingStateForTest
	require.NoError(t, db.Order("id").Find(&state.Configs).Error)
	require.NoError(t, db.Order("id").Find(&state.Sources).Error)
	require.NoError(t, db.Order("id").Find(&state.Snapshots).Error)
	require.NoError(t, db.Order("id").Find(&state.Audits).Error)
	return state
}

func TestZTAPILegacyRepricingPreservesBoundXcloud53(t *testing.T) {
	for _, path := range []string{"ab", "fx", "commercial_v2"} {
		t.Run(path, func(t *testing.T) {
			db := setupZTAPIPublicCatalogTestDB(t)
			require.NoError(t, db.AutoMigrate(&ZTAPIAuditEvent{}, &ZTAPICatalogLock{}))
			config := seedBoundXcloud53ForRepricingTest(t, db)
			seedZTAPIFXPolicyForTest(t, db, "6.7628")
			quote, err := ZTAPIQuotationABEntries()
			require.NoError(t, err)
			// The bound source, not a newer unbound import, owns the live policy.
			unbound, err := BuildZTAPIABTextPriceSourceWithFX(quote, "GLM 5.3", config.ID, 7, ztapiFXPreviewEffectiveAt,
				platformFXParamsForTest("6.63", "6.7628"))
			require.NoError(t, err)
			unbound.Version = 3
			require.NoError(t, db.Create(&unbound).Error)
			before := readLegacyRepricingStateForTest(t, db)
			var result ZTAPICommercialRepricingResult
			switch path {
			case "ab":
				result, err = ApplyZTAPIABCommercialPricing(7, quote.WorkbookSHA256, []string{"GLM 5.3"})
			case "fx":
				result, err = ApplyZTAPIFXRepricing(7)
			case "commercial_v2":
				result, err = ApplyZTAPICommercialPricingV2(7)
			}
			require.NoError(t, err)
			require.Zero(t, result.Republished, "legacy repricing must not replace the dedicated Xcloud price")
			require.Contains(t, result.Skipped, config.PublicNameValue())
			require.Equal(t, before, readLegacyRepricingStateForTest(t, db), "no config, source, snapshot or audit may change")
		})
	}
}

func TestZTAPILegacyRepricingFXPreviewSkipsXcloud53(t *testing.T) {
	db := setupZTAPIPublicCatalogTestDB(t)
	require.NoError(t, db.AutoMigrate(&ZTAPIAuditEvent{}, &ZTAPICatalogLock{}))
	seedBoundXcloud53ForRepricingTest(t, db)
	seedZTAPIFXPolicyForTest(t, db, "6.7628")
	before := readLegacyRepricingStateForTest(t, db)
	preview, err := PreviewZTAPIFXRepricing()
	require.NoError(t, err)
	require.Len(t, preview.Rows, 1)
	row := preview.Rows[0]
	require.Equal(t, "skipped", row.Status)
	require.Equal(t, "dedicated_policy", row.Kind)
	require.Contains(t, row.Reason, string(ZTAPIPricePolicyXcloudGLM53))
	require.Equal(t, "3.1500000000", row.CurrentInputSale)
	require.Equal(t, "11.0250000000", row.CurrentOutputSale)
	require.Empty(t, row.ProposedInput)
	require.Equal(t, before, readLegacyRepricingStateForTest(t, db))
}

func TestZTAPILegacyRepricingPreservesCashPolicies(t *testing.T) {
	db := setupZTAPIPublicCatalogTestDB(t)
	require.NoError(t, db.AutoMigrate(&ZTAPIAuditEvent{}, &ZTAPICatalogLock{}))
	seedZTAPIFXPolicyForTest(t, db, "6.7628")
	names := []string{"Gemini 3 Flash Preview", "Gemini 3.1 Pro Preview", "Kimi K2.7 Code"}
	var selections []ZTAPICashMarginSelection20261030
	for _, name := range names {
		config, snapshot, _ := cashMarginFixture(t, db, name)
		selections = append(selections, ZTAPICashMarginSelection20261030{ModelID: config.ID, Version: config.Version, SnapshotID: snapshot.ID})
	}
	_, err := ApplyZTAPICashMargin20261030(7, selections, false)
	require.NoError(t, err)
	before := readLegacyRepricingStateForTest(t, db)
	quote, err := ZTAPIQuotationABEntries()
	require.NoError(t, err)
	result, err := ApplyZTAPIABCommercialPricing(7, quote.WorkbookSHA256, names)
	require.NoError(t, err)
	require.Zero(t, result.Republished)
	require.Len(t, result.Skipped, 3)
	result, err = ApplyZTAPIFXRepricing(7)
	require.NoError(t, err)
	require.Zero(t, result.Republished)
	result, err = ApplyZTAPICommercialPricingV2(7)
	require.NoError(t, err)
	require.Zero(t, result.Republished)
	preview, err := PreviewZTAPIFXRepricing()
	require.NoError(t, err)
	require.Len(t, preview.Rows, 3)
	for _, row := range preview.Rows {
		require.Equal(t, "skipped", row.Status)
		require.Equal(t, "dedicated_policy", row.Kind)
	}
	require.Equal(t, before, readLegacyRepricingStateForTest(t, db))
}

func TestZTAPILegacyRepricingStillRepricesOtherExternalSources(t *testing.T) {
	db := setupZTAPIPublicCatalogTestDB(t)
	require.NoError(t, db.AutoMigrate(&ZTAPIAuditEvent{}, &ZTAPICatalogLock{}))
	protected := seedBoundXcloud53ForRepricingTest(t, db)
	other := seedZTAPIPublicCatalogRecord(t, db, "glm-5.2", "zt-glm-5.2", ZTAPIProviderGLM,
		ZTAPIProtocolOpenAICompatible, []string{ZTAPIBillingDimensionInputTokens, ZTAPIBillingDimensionOutputTokens})
	seedZTAPIFXPolicyForTest(t, db, "6.7628")
	for _, upstream := range []string{"6.7628", "6.8100"} {
		require.NoError(t, db.Model(&ZTAPIFXPolicy{}).Where("id > ?", 0).Update("upstream_cny_per_usd", upstream).Error)
		result, err := ApplyZTAPIFXRepricing(7)
		require.NoError(t, err)
		require.Contains(t, result.Skipped, protected.PublicNameValue())
		var after ZTAPIModelConfig
		require.NoError(t, db.First(&after, protected.ID).Error)
		require.Equal(t, protected, after)
	}
	var repriced ZTAPIModelConfig
	require.NoError(t, db.First(&repriced, other.ID).Error)
	require.NotEqual(t, other.PublicationSnapshotID, repriced.PublicationSnapshotID)
	var snapshot ZTAPIModelPublicationSnapshot
	require.NoError(t, db.First(&snapshot, repriced.PublicationSnapshotID).Error)
	var source ZTAPIModelPriceSource
	require.NoError(t, db.First(&source, snapshot.PriceSourceID).Error)
	require.Equal(t, ZTAPIFXModePlatformV1, source.FXMode)
	require.NoError(t, validateZTAPIABPriceSource(&source))
}
