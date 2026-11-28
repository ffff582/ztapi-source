package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestSupplierRetailComparisonUsesFrozenBillingPrecision(t *testing.T) {
	for _, tc := range []struct{ name, dimension, raw, multiplier, billed string }{
		{"glm47", "cache_read", "0.0620555914", "0.9", "0.0558500323"},
		{"glm51", "cache_read", "0.2016806723", "0.9", "0.1815126051"},
		{"geminiFlash", "output_tokens", "3.1237454751", "0.92", "2.8738458371"},
		{"geminiPro", "output_tokens", "12.4949819005", "0.92", "11.4953833485"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rules := func(price string) string {
				raw, err := common.Marshal([]ztapiABFrozenSaleRule{{Conditions: []string{}, Sale: map[string]string{tc.dimension: price}}})
				require.NoError(t, err)
				return string(raw)
			}
			old := ZTAPIModelPriceSource{SaleMultiplier: tc.multiplier, TokenPriceRulesJSON: rules(tc.raw)}
			target := ZTAPIModelPriceSource{SaleMultiplier: "1", TokenPriceRulesJSON: rules(tc.billed)}
			require.NoError(t, ztapiRepriceDoesNotRaise(&old, &target))
			raised := decimal.RequireFromString(tc.billed).Add(decimal.RequireFromString("0.0000000001"))
			target.TokenPriceRulesJSON = rules(raised.StringFixed(10))
			require.ErrorContains(t, ztapiRepriceDoesNotRaise(&old, &target), "would increase")
		})
	}
}

func supplierRepricingFixture20261019(t *testing.T) (*gorm.DB, ZTAPIModelConfig, ZTAPIModelPublicationSnapshot, ZTAPIModelPriceSource) {
	t.Helper()
	db := setupZTAPIPublicCatalogTestDB(t)
	require.NoError(t, db.AutoMigrate(&ZTAPIAuditEvent{}, &ZTAPICatalogLock{}, &ZTAPIFXPolicy{}))
	config := seedZTAPIPublicCatalogRecord(t, db, "gemini-3.1-pro-preview", "zt-gemini-3.1-pro-preview", "google",
		ZTAPIProtocolOpenAICompatible, []string{ZTAPIBillingDimensionInputTokens, ZTAPIBillingDimensionOutputTokens})
	quote, err := ZTAPIQuotationABEntries()
	require.NoError(t, err)
	// The legacy importer enforces the frozen provider identity as well.
	identities, err := ZTAPIQuotationEntries()
	require.NoError(t, err)
	bridge, err := BuildZTAPIABQuotationIdentityBridge(quote, identities)
	require.NoError(t, err)
	for _, identity := range bridge.Mapped {
		if identity.SourceModel == config.SourceModel {
			require.NoError(t, db.Model(&config).Update("provider_family", identity.ProviderFamily).Error)
		}
	}
	_, err = ApplyZTAPIABCommercialPricing(7, quote.WorkbookSHA256, []string{"Gemini 3.1 Pro Preview"})
	require.NoError(t, err)
	require.NoError(t, db.First(&config, config.ID).Error)
	var snapshot ZTAPIModelPublicationSnapshot
	var source ZTAPIModelPriceSource
	require.NoError(t, db.First(&snapshot, config.PublicationSnapshotID).Error)
	require.NoError(t, db.First(&source, snapshot.PriceSourceID).Error)
	return db, config, snapshot, source
}

func TestSupplierRepricing20261019PreservesRetailRoutesAndHistory(t *testing.T) {
	db, config, previous, oldSource := supplierRepricingFixture20261019(t)
	preview, err := PreviewZTAPISupplierPricing20261019()
	require.NoError(t, err)
	require.Len(t, preview.Ready, 1)
	require.Equal(t, config.Version, preview.Ready[0].Version)
	result, err := ApplyZTAPISupplierPricing20261019(7, ZTAPISupplierWorkbook20261019SHA256,
		[]string{config.PublicNameValue()}, map[string]uint64{config.PublicNameValue(): config.Version})
	require.NoError(t, err)
	require.Equal(t, 1, result.Republished)
	var after ZTAPIModelConfig
	var snapshot ZTAPIModelPublicationSnapshot
	var source ZTAPIModelPriceSource
	require.NoError(t, db.First(&after, config.ID).Error)
	require.NoError(t, db.First(&snapshot, after.PublicationSnapshotID).Error)
	require.NoError(t, db.First(&source, snapshot.PriceSourceID).Error)
	require.Equal(t, previous.AllowedChannelIDs, snapshot.AllowedChannelIDs)
	require.Equal(t, previous.VerificationIDs, snapshot.VerificationIDs)
	require.Equal(t, previous.InputPricePerMillion, snapshot.InputPricePerMillion)
	require.Equal(t, previous.OutputPricePerMillion, snapshot.OutputPricePerMillion)
	require.Equal(t, ZTAPISupplierWorkbook20261019SHA256, source.SourceDocumentChecksum)
	require.Greater(t, source.QuotationEffectiveAt, oldSource.QuotationEffectiveAt)
	require.NoError(t, ValidateZTAPISupplierPriceSource20261019(db, &source))
	var historical ZTAPIModelPriceSource
	var historicalSnapshot ZTAPIModelPublicationSnapshot
	require.NoError(t, db.First(&historical, oldSource.ID).Error)
	require.NoError(t, db.First(&historicalSnapshot, previous.ID).Error)
	require.Equal(t, oldSource, historical)
	require.Equal(t, previous, historicalSnapshot)
	checksums, err := ztapiQuotationChecksums(db, []int64{snapshot.ID})
	require.NoError(t, err)
	require.Equal(t, ZTAPISupplierWorkbook20261019SHA256, checksums[snapshot.ID])
	second, err := ApplyZTAPISupplierPricing20261019(7, ZTAPISupplierWorkbook20261019SHA256,
		[]string{after.PublicNameValue()}, map[string]uint64{after.PublicNameValue(): after.Version})
	require.NoError(t, err)
	require.Equal(t, 1, second.Unchanged)
}

func TestSupplierRepricing20261019RollsBackAndRejectsStaleApproval(t *testing.T) {
	db, config, previous, _ := supplierRepricingFixture20261019(t)
	_, err := ApplyZTAPISupplierPricing20261019(7, ZTAPISupplierWorkbook20261019SHA256,
		[]string{config.PublicNameValue()}, map[string]uint64{config.PublicNameValue(): config.Version - 1})
	require.ErrorIs(t, err, ErrZTAPIModelVersionConflict)
	_, err = ApplyZTAPISupplierPricing20261019(7, ZTAPISupplierWorkbook20261019SHA256,
		[]string{config.PublicNameValue(), "zt-unverified-prime"}, map[string]uint64{config.PublicNameValue(): config.Version, "zt-unverified-prime": 1})
	require.Error(t, err)
	var after ZTAPIModelConfig
	require.NoError(t, db.First(&after, config.ID).Error)
	require.Equal(t, previous.ID, after.PublicationSnapshotID)
	var count int64
	require.NoError(t, db.Model(&ZTAPIAuditEvent{}).Where("action = ?", "model.supplier_quote_20261019_published").Count(&count).Error)
	require.Zero(t, count)
}

func supplierKimiRepricingFixture20261019(t *testing.T) (*gorm.DB, ZTAPIModelConfig, ZTAPIModelPublicationSnapshot, ZTAPIModelPriceSource) {
	t.Helper()
	db := setupZTAPIPublicCatalogTestDB(t)
	require.NoError(t, db.AutoMigrate(&ZTAPIAuditEvent{}, &ZTAPICatalogLock{}))
	seedZTAPIFXPolicyForTest(t, db, "")
	alias := "zt-kimi-k3"
	config := ZTAPIModelConfig{
		SourceModel: "kimi-k3", PublicName: &alias, Protocol: ZTAPIProtocolOpenAICompatible,
		ProviderFamily: ZTAPIProviderMoonshot, Family: ZTAPIModelFamilyOpenAI,
		EnabledGroups: `["default"]`, Published: true, Version: 2,
		CacheCreationRatio: 1.25, CacheCreation5mRatio: 1.25, CacheCreation1hRatio: 2,
		ImageRatio: 1, AudioRatio: 1, AudioCompletionRatio: 2,
	}
	require.NoError(t, db.Create(&config).Error)
	quote, err := ZTAPIQuotationABEntries()
	require.NoError(t, err)
	source, err := BuildZTAPIABTextPriceSourceWithFX(quote, "Kimi K3", config.ID, 7, 1_789_000_000, platformFXParamsForTest("6.63", ""))
	require.NoError(t, err)
	source.Version, source.SaleMultiplier = 1, "0.9"
	require.NoError(t, validateZTAPIABPriceSource(&source))
	require.NoError(t, db.Create(&source).Error)
	preview, err := BuildZTAPIModelPricePreview(&source)
	require.NoError(t, err)
	require.NoError(t, applyZTAPICommercialPreview(&config, preview))
	require.NoError(t, db.Save(&config).Error)
	snapshot, err := createZTAPIModelPublicationSnapshotTx(db, &config, config.Version, ztapiPublicationEvidence{
		AllowedChannelIDs: []int{2}, VerificationIDs: []int64{31}, PriceSourceID: source.ID, IdentityUpdatedAt: 1,
	})
	require.NoError(t, err)
	require.NoError(t, db.Model(&config).Update("publication_snapshot_id", snapshot.ID).Error)
	require.NoError(t, db.First(&config, config.ID).Error)
	require.NoError(t, db.First(&source, source.ID).Error)
	require.NoError(t, db.First(snapshot, snapshot.ID).Error)
	return db, config, *snapshot, source
}

func assertSupplierKimiHistory20261019(t *testing.T, db *gorm.DB, snapshot ZTAPIModelPublicationSnapshot, source ZTAPIModelPriceSource) {
	t.Helper()
	var historical ZTAPIModelPriceSource
	var historicalSnapshot ZTAPIModelPublicationSnapshot
	require.NoError(t, db.First(&historical, source.ID).Error)
	require.NoError(t, db.First(&historicalSnapshot, snapshot.ID).Error)
	require.Equal(t, source, historical)
	require.Equal(t, snapshot, historicalSnapshot)
}

func assertSupplierKimiRepricingCounts20261019(t *testing.T, db *gorm.DB, sources, snapshots, audits int64) {
	t.Helper()
	for _, tc := range []struct {
		model any
		want  int64
	}{{&ZTAPIModelPriceSource{}, sources}, {&ZTAPIModelPublicationSnapshot{}, snapshots}, {&ZTAPIAuditEvent{}, audits}} {
		var count int64
		require.NoError(t, db.Model(tc.model).Count(&count).Error)
		require.Equal(t, tc.want, count, "%T", tc.model)
	}
}

func TestSupplierKimiRepricing20261019PreviewAllowsVerifiedTTLAdditionWithoutWrites(t *testing.T) {
	db, config, snapshot, source := supplierKimiRepricingFixture20261019(t)
	preview, err := PreviewZTAPISupplierPricing20261019()
	require.NoError(t, err)
	t.Logf("K3 preview ready=%d blocked=%+v", len(preview.Ready), preview.Blocked)
	require.Empty(t, preview.Blocked)
	require.Len(t, preview.Ready, 1)
	change := preview.Ready[0]
	require.Equal(t, config.PublicNameValue(), change.PublicName)
	require.Equal(t, source.ID, change.PreviousSourceID)
	for _, dimension := range []string{ZTAPIBillingDimensionInputTokens, ZTAPIBillingDimensionOutputTokens, ZTAPIBillingDimensionCacheRead} {
		require.Equal(t, change.Before.SaleUSD[dimension], change.After.SaleUSD[dimension], dimension)
	}
	input := decimal.RequireFromString(change.Before.SaleUSD[ZTAPIBillingDimensionInputTokens])
	require.Equal(t, input.StringFixed(10), change.After.SaleUSD[ZTAPIBillingDimensionCacheWrite5m])
	require.Equal(t, input.Mul(decimal.NewFromInt(2)).StringFixed(10), change.After.SaleUSD[ZTAPIBillingDimensionCacheWrite1h])
	require.NotContains(t, change.After.SaleUSD, ZTAPIBillingDimensionCacheWrite)
	var after ZTAPIModelConfig
	require.NoError(t, db.First(&after, config.ID).Error)
	require.Equal(t, config, after)
	assertSupplierKimiHistory20261019(t, db, snapshot, source)
	assertSupplierKimiRepricingCounts20261019(t, db, 1, 1, 0)
}

func TestSupplierKimiRepricing20261019ApplyReplaysWithoutChangingHistory(t *testing.T) {
	db, config, previous, oldSource := supplierKimiRepricingFixture20261019(t)
	result, err := ApplyZTAPISupplierPricing20261019(7, ZTAPISupplierWorkbook20261019SHA256,
		[]string{config.PublicNameValue()}, map[string]uint64{config.PublicNameValue(): config.Version})
	t.Logf("K3 apply result=%+v error=%v", result, err)
	require.NoError(t, err)
	require.Equal(t, 1, result.Imported)
	require.Equal(t, 1, result.Republished)
	var after ZTAPIModelConfig
	var snapshot ZTAPIModelPublicationSnapshot
	var source ZTAPIModelPriceSource
	require.NoError(t, db.First(&after, config.ID).Error)
	require.NoError(t, db.First(&snapshot, after.PublicationSnapshotID).Error)
	require.NoError(t, db.First(&source, snapshot.PriceSourceID).Error)
	require.Equal(t, config.Version+1, after.Version)
	require.Equal(t, previous.AllowedChannelIDs, snapshot.AllowedChannelIDs)
	require.Equal(t, previous.VerificationIDs, snapshot.VerificationIDs)
	require.Equal(t, previous.InputPricePerMillion, snapshot.InputPricePerMillion)
	require.Equal(t, previous.OutputPricePerMillion, snapshot.OutputPricePerMillion)
	require.Equal(t, ZTAPISupplierWorkbook20261019SHA256, source.SourceDocumentChecksum)
	require.NoError(t, ValidateZTAPISupplierPriceSource20261019(db, &source))
	rules := supplierRulesForTest(t, &source)
	require.Len(t, rules, 1)
	require.Equal(t, oldSource.ID, rules[0].RetailBasisID)
	require.Len(t, rules[0].Sale, 5)
	require.NotContains(t, rules[0].Sale, ZTAPIBillingDimensionCacheWrite)
	assertSupplierKimiHistory20261019(t, db, previous, oldSource)
	assertSupplierKimiRepricingCounts20261019(t, db, 2, 2, 1)
	second, err := ApplyZTAPISupplierPricing20261019(7, ZTAPISupplierWorkbook20261019SHA256,
		[]string{after.PublicNameValue()}, map[string]uint64{after.PublicNameValue(): after.Version})
	require.NoError(t, err)
	require.Equal(t, 1, second.Unchanged)
	require.Zero(t, second.Imported)
	require.Zero(t, second.Republished)
	_, err = ApplyZTAPISupplierPricing20261019(7, ZTAPISupplierWorkbook20261019SHA256,
		[]string{config.PublicNameValue()}, map[string]uint64{config.PublicNameValue(): config.Version})
	require.ErrorIs(t, err, ErrZTAPIModelVersionConflict)
	var replayed ZTAPIModelConfig
	require.NoError(t, db.First(&replayed, config.ID).Error)
	require.Equal(t, after, replayed)
	assertSupplierKimiHistory20261019(t, db, previous, oldSource)
	assertSupplierKimiHistory20261019(t, db, snapshot, source)
	assertSupplierKimiRepricingCounts20261019(t, db, 2, 2, 1)
}

func TestSupplierKimiRepricing20261019ComparisonExemptsOnlyIndependentlyVerifiedTTLs(t *testing.T) {
	db, _, snapshot, old := supplierKimiRepricingFixture20261019(t)
	target, err := BuildZTAPISupplierPriceSource20261019WithDB(db, &old, 7, 1_792_419_000, platformFXParamsForTest("6.63", ""))
	require.NoError(t, err)
	require.NoError(t, ValidateZTAPISupplierPriceSource20261019(db, &target))
	require.Error(t, ztapiRepriceDoesNotRaise(&old, &target), "the shared comparison must retain its strict missing-dimension contract")
	beforeOld, beforeTarget := old, target
	require.NoError(t, ztapiSupplierRepriceDoesNotRaise20261019(db, &old, &target))
	require.Equal(t, beforeOld, old)
	require.Equal(t, beforeTarget, target)
	for _, dimension := range []string{ZTAPIBillingDimensionInputTokens, ZTAPIBillingDimensionOutputTokens, ZTAPIBillingDimensionCacheRead} {
		t.Run(dimension+"_increase", func(t *testing.T) {
			lower := old
			rules := supplierRulesForTest(t, &lower)
			rules[0].Sale[dimension] = decimal.RequireFromString(rules[0].Sale[dimension]).Sub(decimal.RequireFromString("0.000000001")).StringFixed(10)
			setSupplierRulesForTest(t, &lower, rules)
			require.ErrorContains(t, ztapiSupplierRepriceDoesNotRaise20261019(db, &lower, &target), "would increase")
		})
	}
	for _, tc := range []struct {
		name   string
		mutate func(*ZTAPIModelPriceSource, *ZTAPIModelPriceSource)
	}{
		{"unverified_cost", func(_, s *ZTAPIModelPriceSource) { s.CacheWrite5mPerMillion = "0.01" }},
		{"unverified_ttl_retail", func(_, s *ZTAPIModelPriceSource) {
			rules := supplierRulesForTest(t, s)
			rules[0].Sale[ZTAPIBillingDimensionCacheWrite1h] = "999"
			setSupplierRulesForTest(t, s, rules)
		}},
		{"unquoted_generic_write", func(_, s *ZTAPIModelPriceSource) {
			rules := supplierRulesForTest(t, s)
			rules[0].Sale[ZTAPIBillingDimensionCacheWrite] = "1"
			setSupplierRulesForTest(t, s, rules)
		}},
		{"missing_read", func(_, s *ZTAPIModelPriceSource) {
			rules := supplierRulesForTest(t, s)
			delete(rules[0].Sale, ZTAPIBillingDimensionCacheRead)
			setSupplierRulesForTest(t, s, rules)
		}},
		{"missing_1h", func(_, s *ZTAPIModelPriceSource) {
			rules := supplierRulesForTest(t, s)
			delete(rules[0].Sale, ZTAPIBillingDimensionCacheWrite1h)
			setSupplierRulesForTest(t, s, rules)
		}},
		{"different_model", func(_, s *ZTAPIModelPriceSource) { s.SourceModel = "kimi-k2.6" }},
		{"other_model_pair", func(s, next *ZTAPIModelPriceSource) { s.SourceModel, next.SourceModel = "kimi-k2.6", "kimi-k2.6" }},
		{"different_config", func(_, s *ZTAPIModelPriceSource) { s.ModelConfigID++ }},
		{"different_policy", func(_, s *ZTAPIModelPriceSource) { s.PricePolicy = string(ZTAPIPricePolicyPool30Margin) }},
		{"different_workbook", func(_, s *ZTAPIModelPriceSource) { s.SourceDocumentChecksum = ZTAPIQuotationSHA256 }},
		{"incomplete_legacy_dimensions", func(s, _ *ZTAPIModelPriceSource) {
			rules := supplierRulesForTest(t, s)
			delete(rules[0].Sale, ZTAPIBillingDimensionCacheRead)
			setSupplierRulesForTest(t, s, rules)
		}},
		{"unexpected_legacy_dimension", func(s, _ *ZTAPIModelPriceSource) {
			rules := supplierRulesForTest(t, s)
			rules[0].Sale[ZTAPIBillingDimensionCacheWrite] = "1"
			setSupplierRulesForTest(t, s, rules)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			previous, next := old, target
			tc.mutate(&previous, &next)
			require.Error(t, ztapiSupplierRepriceDoesNotRaise20261019(db, &previous, &next))
		})
	}
	require.Error(t, ztapiSupplierRepriceDoesNotRaise20261019(nil, &old, &target))
	require.Error(t, ztapiSupplierRepriceDoesNotRaise20261019(db, nil, &target))
	detachedAnchor := old
	detachedAnchor.ID++
	require.ErrorContains(t, ztapiSupplierRepriceDoesNotRaise20261019(db, &detachedAnchor, &target), "current original retail anchor")
	assertSupplierKimiHistory20261019(t, db, snapshot, old)
}

func TestSupplierKimiRepricing20261019ReplayRejectsChangedHistoricalAnchor(t *testing.T) {
	db, config, _, old := supplierKimiRepricingFixture20261019(t)
	_, err := ApplyZTAPISupplierPricing20261019(7, ZTAPISupplierWorkbook20261019SHA256,
		[]string{config.PublicNameValue()}, map[string]uint64{config.PublicNameValue(): config.Version})
	require.NoError(t, err)
	var after ZTAPIModelConfig
	var snapshot ZTAPIModelPublicationSnapshot
	var source ZTAPIModelPriceSource
	require.NoError(t, db.First(&after, config.ID).Error)
	require.NoError(t, db.First(&snapshot, after.PublicationSnapshotID).Error)
	require.NoError(t, db.First(&source, snapshot.PriceSourceID).Error)
	rules := supplierRulesForTest(t, &old)
	rules[0].Sale[ZTAPIBillingDimensionCacheRead] = "0.3"
	setSupplierRulesForTest(t, &old, rules)
	require.NoError(t, db.Model(&old).Update("token_price_rules_json", old.TokenPriceRulesJSON).Error)
	preview, err := PreviewZTAPISupplierPricing20261019()
	require.NoError(t, err)
	require.Empty(t, preview.Ready)
	require.Len(t, preview.Blocked, 1)
	_, err = ApplyZTAPISupplierPricing20261019(7, ZTAPISupplierWorkbook20261019SHA256,
		[]string{after.PublicNameValue()}, map[string]uint64{after.PublicNameValue(): after.Version})
	require.Error(t, err)
	var unchanged ZTAPIModelConfig
	require.NoError(t, db.First(&unchanged, config.ID).Error)
	require.Equal(t, after, unchanged)
	assertSupplierKimiHistory20261019(t, db, snapshot, source)
	assertSupplierKimiRepricingCounts20261019(t, db, 2, 2, 1)
}
