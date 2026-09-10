package model

import (
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestZTAPIEnterprisePriceBasisSnapshotFreezesPolicy(t *testing.T) {
	f := setupZTAPIPublicationGateFixture(t)
	next := f.config
	next.Published = true
	committed, err := UpdateZTAPIModelConfigAndBilling(&next, next.Version, nil)
	require.NoError(t, err)
	require.NotZero(t, committed.PublicationSnapshotID)

	var snapshot ZTAPIModelPublicationSnapshot
	require.NoError(t, f.db.First(&snapshot, committed.PublicationSnapshotID).Error)
	require.Equal(t, string(ZTAPIPricePolicyEnterprise40Margin), snapshot.PricePolicy)
}

func TestZTAPIEnterprisePriceBasisRejectsChangedSnapshotPolicy(t *testing.T) {
	db := setupZTAPIPublicCatalogTestDB(t)
	config := seedZTAPIPublicCatalogRecord(t, db, "gpt-5.5", "zt-gpt-5.5", ZTAPIProviderOpenAI,
		ZTAPIProtocolOpenAICompatible, []string{ZTAPIBillingDimensionInputTokens, ZTAPIBillingDimensionOutputTokens})
	// Seed a corrupted historical row solely to exercise read-side rejection.
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Model(&ZTAPIModelPublicationSnapshot{}).Where("id = ?", config.PublicationSnapshotID).
		Update("price_policy", string(ZTAPIPricePolicyPool60Margin)).Error)

	checksums, err := ztapiQuotationChecksums(db, []int64{config.PublicationSnapshotID})
	require.NoError(t, err)
	require.Empty(t, checksums, "the immutable snapshot policy must match its pinned price source")
}

func TestZTAPIPublicationRejectsPoolAsEnterprisePriceBasis(t *testing.T) {
	f := setupZTAPIPublicationGateFixture(t)
	require.NoError(t, f.db.Model(&f.price).Updates(map[string]any{
		"resource_type": "pool",
		"price_policy":  string(ZTAPIPricePolicyEnterprise40Margin),
	}).Error)
	blockers, err := ZTAPIPublicationBlockers(f.config.ID)
	require.NoError(t, err)
	require.Contains(t, blockers, "enterprise_price_basis_missing")

	next := f.config
	next.Published = true
	_, err = UpdateZTAPIModelConfigAndBilling(&next, f.config.Version, nil)
	require.Error(t, err)
	var count int64
	require.NoError(t, f.db.Model(&ZTAPIModelPublicationSnapshot{}).Count(&count).Error)
	require.Zero(t, count, "pool cost must not mint a published enterprise-priced snapshot")
}

func TestZTAPIPublicationRejectsUnknownPriceBasis(t *testing.T) {
	f := setupZTAPIPublicationGateFixture(t)
	require.NoError(t, f.db.Model(&f.price).Update("resource_type", "unspecified-resource").Error)
	blockers, err := ZTAPIPublicationBlockers(f.config.ID)
	require.NoError(t, err)
	require.Contains(t, blockers, "enterprise_price_basis_missing")
}

func TestZTAPIPublicationAcceptsDocumentedNonPoolPriceBases(t *testing.T) {
	for _, resource := range []string{"enterprise", "official", "original_resource"} {
		t.Run(resource, func(t *testing.T) {
			f := setupZTAPIPublicationGateFixture(t)
			require.NoError(t, f.db.Model(&f.price).Update("resource_type", resource).Error)
			blockers, err := ZTAPIPublicationBlockers(f.config.ID)
			require.NoError(t, err)
			require.Empty(t, blockers)
		})
	}
}

func TestZTAPIQuotationDropsPoolPricedLegacySnapshot(t *testing.T) {
	db := setupZTAPIPublicCatalogTestDB(t)
	config := seedZTAPIPublicCatalogRecord(t, db, "gpt-5.5", "zt-gpt-5.5", ZTAPIProviderOpenAI,
		ZTAPIProtocolOpenAICompatible, []string{ZTAPIBillingDimensionInputTokens, ZTAPIBillingDimensionOutputTokens})
	require.NoError(t, db.Model(&ZTAPIModelPriceSource{}).Where("model_config_id = ?", config.ID).
		Update("resource_type", "enterprise").Error)
	rows, err := loadZTAPIQuotedPublications(db)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.NoError(t, db.Model(&ZTAPIModelPriceSource{}).Where("model_config_id = ?", config.ID).
		Update("resource_type", "pool").Error)
	rows, err = loadZTAPIQuotedPublications(db)
	require.NoError(t, err)
	require.Empty(t, rows, "an old publication must not bypass the new price basis guard")
}

func TestZTAPIPublicationRejectsRoundedDownCachePrice(t *testing.T) {
	f := setupZTAPIPublicationGateFixture(t)
	require.NoError(t, f.db.Model(&f.price).Updates(map[string]any{
		"billing_dimensions":     `["input_tokens","output_tokens","cache_read"]`,
		"cache_read_per_million": "0.0333333333",
	}).Error)
	require.NoError(t, f.db.Model(&f.config).Update("cache_read_ratio", 0.0333).Error)
	blockers, err := ZTAPIPublicationBlockers(f.config.ID)
	require.NoError(t, err)
	require.Contains(t, blockers, ZTAPIPublicationBlockerPriceIncomplete)
	require.NoError(t, f.db.Model(&f.config).Update("cache_read_ratio", 0.03333333).Error)
	blockers, err = ZTAPIPublicationBlockers(f.config.ID)
	require.NoError(t, err)
	require.Empty(t, blockers)
}

func TestZTAPIPriceImportDerivesCacheRatiosAtDatabasePrecision(t *testing.T) {
	db := setupZTAPIModelEvidenceWriteTestDB(t)
	config := ZTAPIModelConfig{SourceModel: "deepseek-v4-flash", EnabledGroups: "[]", Version: 1,
		CacheReadRatio: 0.0333, CacheCreationRatio: 9, CacheCreation5mRatio: 9, CacheCreation1hRatio: 9}
	require.NoError(t, db.Create(&config).Error)
	source := validZTAPIPriceSourceForTest()
	source.ModelConfigID, source.SourceModel = config.ID, config.SourceModel
	source.BillingDimensions = `["input_tokens","output_tokens","cache_read","cache_write","cache_write_5m","cache_write_1h"]`
	source.CacheReadPerMillion = "0.0333333333"
	source.CacheWritePerMillion, source.CacheWrite5mPerMillion, source.CacheWrite1hPerMillion = "1.25", "1.25", "2"
	got, _, _, err := ImportZTAPIModelPriceSource(ZTAPIModelPriceSourceImport{
		Source: source, ExpectedVersion: 1, OperatorID: 1, Reason: "enterprise quotation import",
	})
	require.NoError(t, err)
	require.Equal(t, 0.03333333, got.CacheReadRatio)
	require.Equal(t, 1.25, got.CacheCreationRatio)
	require.Equal(t, 1.25, got.CacheCreation5mRatio)
	require.Equal(t, 2.0, got.CacheCreation1hRatio)
}

func TestZTAPIQuotationRejectsRelabeledPoolOnlyModels(t *testing.T) {
	for _, item := range []struct{ source, alias string }{
		{"claude-fable-5", "zt-claude-fable-5"}, {"claude-opus-5", "zt-claude-opus-5"},
	} {
		t.Run(item.source, func(t *testing.T) {
			db := setupZTAPIPublicCatalogTestDB(t)
			seedZTAPIPublicCatalogRecord(t, db, item.source, item.alias, ZTAPIProviderAnthropic,
				ZTAPIProtocolOpenAICompatible, []string{ZTAPIBillingDimensionInputTokens, ZTAPIBillingDimensionOutputTokens})
			rows, err := loadZTAPIQuotedPublications(db)
			require.NoError(t, err)
			require.Empty(t, rows, "resource labels alone cannot supply a missing enterprise quotation")
		})
	}
}

func TestZTAPIQuotationRejectsStaleCacheRatioAtReadBoundary(t *testing.T) {
	db := setupZTAPIPublicCatalogTestDB(t)
	config := seedZTAPIPublicCatalogRecord(t, db, "gpt-5.5", "zt-gpt-5.5", ZTAPIProviderOpenAI,
		ZTAPIProtocolOpenAICompatible, []string{ZTAPIBillingDimensionInputTokens, ZTAPIBillingDimensionOutputTokens, ZTAPIBillingDimensionCacheRead})
	checksums, err := ztapiQuotationChecksums(db, []int64{config.PublicationSnapshotID})
	require.NoError(t, err)
	require.Equal(t, ZTAPIQuotationSHA256, checksums[config.PublicationSnapshotID])
	// Seed a stale historical ratio solely to exercise read-side rejection.
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Model(&ZTAPIModelPublicationSnapshot{}).Where("id = ?", config.PublicationSnapshotID).
		Update("cache_read_ratio", 0.2).Error)
	checksums, err = ztapiQuotationChecksums(db, []int64{config.PublicationSnapshotID})
	require.NoError(t, err)
	require.Empty(t, checksums, "both warm and cold quotation reads must reject stale cache prices")
	rows, err := loadZTAPIQuotedPublications(db)
	require.NoError(t, err)
	require.Empty(t, rows)
}
