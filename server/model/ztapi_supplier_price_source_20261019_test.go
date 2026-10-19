package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func supplierPriceAnchorForTest(t *testing.T) ZTAPIModelPriceSource {
	t.Helper()
	quote, err := ZTAPIQuotationABEntries()
	require.NoError(t, err)
	old, err := BuildZTAPIABTextPriceSource(quote, "Gemini 3.1 Pro Preview", 11, 7, 1_789_000_000)
	require.NoError(t, err)
	old.ID, old.Version = 41, 1
	old.SaleMultiplier = "0.95"
	return old
}

func supplierRulesForTest(t *testing.T, source *ZTAPIModelPriceSource) []ztapiABFrozenSaleRule {
	t.Helper()
	var rules []ztapiABFrozenSaleRule
	require.NoError(t, common.Unmarshal([]byte(source.TokenPriceRulesJSON), &rules))
	return rules
}

func setSupplierRulesForTest(t *testing.T, source *ZTAPIModelPriceSource, rules []ztapiABFrozenSaleRule) {
	t.Helper()
	raw, err := common.Marshal(rules)
	require.NoError(t, err)
	source.TokenPriceRulesJSON = string(raw)
}

func TestSupplierPriceCore20261019PreservesEveryEffectiveRetailTier(t *testing.T) {
	old := supplierPriceAnchorForTest(t)
	before := old
	source, err := BuildZTAPISupplierPriceSource20261019(&old, 9, 1_792_368_000, platformFXParamsForTest("6.63", "6.7628"))
	require.NoError(t, err)
	require.Equal(t, before, old)
	require.Zero(t, source.ID)
	require.Zero(t, source.Version)
	require.Equal(t, "quote_frozen_retail_20261019", source.PricePolicy)
	require.Equal(t, "1.0000000000", source.SaleMultiplier)
	require.Equal(t, ZTAPISupplierWorkbook20261019SHA256, source.SourceDocumentChecksum)
	require.Equal(t, "CNY", source.Currency)
	oldRules, rules := supplierRulesForTest(t, &old), supplierRulesForTest(t, &source)
	require.Len(t, rules, 2)
	for tier := range rules {
		require.Equal(t, old.ID, rules[tier].RetailBasisID)
		require.Equal(t, oldRules[tier].Conditions, rules[tier].Conditions)
		for dim, sale := range oldRules[tier].Sale {
			want := decimal.RequireFromString(sale).Mul(decimal.RequireFromString(old.SaleMultiplier)).Round(10)
			require.Equal(t, want.StringFixed(10), rules[tier].Sale[dim])
		}
	}
	preview, err := BuildZTAPIModelPricePreview(&source)
	require.NoError(t, err)
	wantCost := decimal.RequireFromString("4").Mul(decimal.RequireFromString("0.86")).Mul(decimal.RequireFromString("6.7628")).Round(10)
	require.Equal(t, wantCost.StringFixed(10), source.InputPerMillion)
	require.Equal(t, wantCost.Div(decimal.RequireFromString("6.63")).Round(10).StringFixed(10), preview.InputCostUSDPerMillion)
	require.Equal(t, rules[1].Sale[ZTAPIBillingDimensionInputTokens], preview.InputSaleUSDPerMillion)
}

func TestSupplierPriceCore20261019Official78CannotRaiseAnyDimension(t *testing.T) {
	old, err := BuildZTAPI929QuotedModelPriceSource("Claude Opus 5", 11, 7, 1_789_000_000)
	require.NoError(t, err)
	old.ID = 41
	_, err = BuildZTAPISupplierPriceSource20261019(&old, 9, 1_792_368_000, ZTAPIABFXParams{})
	require.Error(t, err, "official 78%% cannot raise the older lower retail price")
	rules := supplierRulesForTest(t, &old)
	for dim, raw := range rules[0].Sale {
		rules[0].Sale[dim] = decimal.RequireFromString(raw).Mul(decimal.NewFromInt(2)).StringFixed(10)
	}
	setSupplierRulesForTest(t, &old, rules)
	source, err := BuildZTAPISupplierPriceSource20261019(&old, 9, 1_792_368_000, platformFXParamsForTest("6.63", "6.7628"))
	require.NoError(t, err)
	require.Equal(t, "3.9000000000", supplierRulesForTest(t, &source)[0].Sale[ZTAPIBillingDimensionInputTokens])
	preview, err := BuildZTAPIModelPricePreview(&source)
	require.NoError(t, err)
	require.Equal(t, "3.9000000000", preview.InputSaleUSDPerMillion)
}

func TestSupplierPriceCore20261019RejectsLossInAnyTierAndInvalidFX(t *testing.T) {
	old := supplierPriceAnchorForTest(t)
	rules := supplierRulesForTest(t, &old)
	rules[0].Sale[ZTAPIBillingDimensionInputTokens] = "0.01"
	setSupplierRulesForTest(t, &old, rules)
	_, err := BuildZTAPISupplierPriceSource20261019(&old, 9, 1_792_368_000, ZTAPIABFXParams{})
	require.Error(t, err, "a profitable maximum tier must not hide a losing lower tier")
	old = supplierPriceAnchorForTest(t)
	old.SaleMultiplier = "1"
	rules = supplierRulesForTest(t, &old)
	rules[0].Sale[ZTAPIBillingDimensionInputTokens] = "1.8060000000"
	setSupplierRulesForTest(t, &old, rules)
	_, err = BuildZTAPISupplierPriceSource20261019(&old, 9, 1_792_368_000, ZTAPIABFXParams{})
	require.NoError(t, err, "exactly cost * 1.05 must be permitted")
	rules[0].Sale[ZTAPIBillingDimensionInputTokens] = "1.8059999999"
	setSupplierRulesForTest(t, &old, rules)
	_, err = BuildZTAPISupplierPriceSource20261019(&old, 9, 1_792_368_000, ZTAPIABFXParams{})
	require.Error(t, err, "one stored price unit below the minimum margin must be rejected")
	old = supplierPriceAnchorForTest(t)
	for _, fx := range []ZTAPIABFXParams{
		platformFXParamsForTest("6.63", ""), platformFXParamsForTest("0", "6.7628"),
		{Mode: "unknown"}, platformFXParamsForTest("6.63", "-1"),
	} {
		_, err := BuildZTAPISupplierPriceSource20261019(&old, 9, 1_792_368_000, fx)
		require.Error(t, err)
	}
}

func TestSupplierPriceCore20261019GenericRejectsMalformedFrozenRules(t *testing.T) {
	old := supplierPriceAnchorForTest(t)
	source, err := BuildZTAPISupplierPriceSource20261019(&old, 9, 1_792_368_000, ZTAPIABFXParams{})
	require.NoError(t, err)
	for name, change := range map[string]func(*ZTAPIModelPriceSource){
		"checksum":            func(s *ZTAPIModelPriceSource) { s.SourceDocumentChecksum = ZTAPIQuotation929SHA256 },
		"multiplier_discount": func(s *ZTAPIModelPriceSource) { s.SaleMultiplier = "0.9" },
		"multiplier_increase": func(s *ZTAPIModelPriceSource) { s.SaleMultiplier = "1.01" },
		"missing_rules":       func(s *ZTAPIModelPriceSource) { s.TokenPriceRulesJSON = "[]" },
		"zero_basis": func(s *ZTAPIModelPriceSource) {
			r := supplierRulesForTest(t, s)
			r[0].RetailBasisID = 0
			setSupplierRulesForTest(t, s, r)
		},
		"mixed_basis": func(s *ZTAPIModelPriceSource) {
			r := supplierRulesForTest(t, s)
			r[1].RetailBasisID++
			setSupplierRulesForTest(t, s, r)
		},
		"missing_dimension": func(s *ZTAPIModelPriceSource) {
			r := supplierRulesForTest(t, s)
			delete(r[0].Sale, ZTAPIBillingDimensionCacheRead)
			setSupplierRulesForTest(t, s, r)
		},
		"extra_dimension": func(s *ZTAPIModelPriceSource) {
			r := supplierRulesForTest(t, s)
			r[0].Sale[ZTAPIBillingDimensionImage] = "1"
			setSupplierRulesForTest(t, s, r)
		},
		"zero_sale": func(s *ZTAPIModelPriceSource) {
			r := supplierRulesForTest(t, s)
			r[0].Sale[ZTAPIBillingDimensionInputTokens] = "0"
			setSupplierRulesForTest(t, s, r)
		},
	} {
		t.Run(name, func(t *testing.T) { bad := source; change(&bad); require.Error(t, ValidateZTAPIModelPriceSource(&bad)) })
	}
	require.NoError(t, ValidateZTAPIPricePolicy("pool", ZTAPIPricePolicy(source.PricePolicy)))
	require.NoError(t, ValidateZTAPIPricePolicy("enterprise", ZTAPIPricePolicy(source.PricePolicy)))
	require.Error(t, ValidateZTAPIPricePolicy("official", ZTAPIPricePolicy(source.PricePolicy)))
}

func TestSupplierPriceCore20261019DBRebuildRejectsForgeryAndKeepsOriginalAnchor(t *testing.T) {
	db := setupZTAPIPublicCatalogTestDB(t)
	old := supplierPriceAnchorForTest(t)
	require.NoError(t, db.Create(&old).Error)
	source, err := BuildZTAPISupplierPriceSource20261019(&old, 9, 1_792_368_000, platformFXParamsForTest("6.63", "6.7628"))
	require.NoError(t, err)
	source.Version = 2
	require.NoError(t, db.Create(&source).Error)
	require.NoError(t, ValidateZTAPISupplierPriceSource20261019(db, &source))
	for name, change := range map[string]func(*ZTAPIModelPriceSource){
		"cost":   func(s *ZTAPIModelPriceSource) { s.InputPerMillion = "0.01" },
		"fx":     func(s *ZTAPIModelPriceSource) { s.UpstreamCNYPerUSD = "6.9" },
		"code":   func(s *ZTAPIModelPriceSource) { s.QuotationModelCode = "forged" },
		"cell":   func(s *ZTAPIModelPriceSource) { s.QuotationCell = "E999" },
		"config": func(s *ZTAPIModelPriceSource) { s.ModelConfigID++ },
		"retail": func(s *ZTAPIModelPriceSource) {
			r := supplierRulesForTest(t, s)
			r[0].Sale[ZTAPIBillingDimensionInputTokens] = "100"
			setSupplierRulesForTest(t, s, r)
		},
		"self_anchor": func(s *ZTAPIModelPriceSource) {
			r := supplierRulesForTest(t, s)
			for i := range r {
				r[i].RetailBasisID = source.ID
			}
			setSupplierRulesForTest(t, s, r)
		},
		"not_older": func(s *ZTAPIModelPriceSource) { s.ID = old.ID },
	} {
		t.Run(name, func(t *testing.T) {
			bad := source
			change(&bad)
			require.Error(t, ValidateZTAPISupplierPriceSource20261019(db, &bad))
		})
	}
	rebuilt, err := BuildZTAPISupplierPriceSource20261019(&source, 9, source.QuotationEffectiveAt, platformFXParamsForTest("6.63", "6.7628"))
	require.NoError(t, err)
	require.Empty(t, ztapiABPriceSourceEvidenceMismatch(&source, &rebuilt))
	rebuilt, err = BuildZTAPISupplierPriceSource20261019(&source, 9, source.QuotationEffectiveAt, platformFXParamsForTest("6.8", "6.7628"))
	require.NoError(t, err)
	require.Equal(t, source.TokenPriceRulesJSON, rebuilt.TokenPriceRulesJSON)
	require.Equal(t, old.ID, supplierRulesForTest(t, &rebuilt)[0].RetailBasisID)
	require.Equal(t, "6.8000000000", rebuilt.PlatformCNYPerUnit)
	require.NoError(t, db.Delete(&old).Error)
	require.Error(t, ValidateZTAPISupplierPriceSource20261019(db, &source))
	_, err = BuildZTAPISupplierPriceSource20261019(&source, 9, source.QuotationEffectiveAt, platformFXParamsForTest("6.8", "6.7628"))
	require.Error(t, err)
}

func TestSupplierPriceCore20261019TransactionBuilderUsesCallerDB(t *testing.T) {
	db := setupZTAPIPublicCatalogTestDB(t)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	old := supplierPriceAnchorForTest(t)
	require.NoError(t, db.Create(&old).Error)
	source, err := BuildZTAPISupplierPriceSource20261019(&old, 9, 1_792_368_000, ZTAPIABFXParams{})
	require.NoError(t, err)
	source.Version = 2
	require.NoError(t, db.Create(&source).Error)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		rebuilt, err := BuildZTAPISupplierPriceSource20261019WithDB(tx, &source, 9, source.QuotationEffectiveAt, ZTAPIABFXParams{})
		if err != nil {
			return err
		}
		require.Equal(t, source.TokenPriceRulesJSON, rebuilt.TokenPriceRulesJSON)
		require.True(t, ztapiPublicationPriceBasisPermittedTx(tx, &source))
		return ValidateZTAPISupplierPriceSource20261019(tx, &source)
	}))
}

func TestSupplierPriceCore20261019ManifestHasExactAuditedIdentities(t *testing.T) {
	report, err := CurrentZTAPIABQuotationIdentityBridge()
	require.NoError(t, err)
	bySource := map[string]ZTAPIABModelIdentity{}
	for _, identity := range report.Mapped {
		bySource[identity.SourceModel] = identity
	}
	var manifest struct {
		Entries []ztapiSupplierPriceEntry20261019 `json:"entries"`
	}
	require.NoError(t, common.Unmarshal(ztapiSupplierPrices20261019JSON, &manifest))
	require.Len(t, manifest.Entries, 14)
	for _, entry := range manifest.Entries {
		t.Run(entry.SourceModel, func(t *testing.T) {
			identity, exists := bySource[entry.SourceModel]
			require.True(t, exists, "supplier manifest source_model must be an exact audited source ID, not its public alias")
			require.NoError(t, ValidateZTAPIQuotationIdentity(identity.SourceModel, identity.PublicName, identity.Protocol, identity.ProviderFamily, ZTAPISupplierWorkbook20261019SHA256))
		})
	}
}

func TestSupplierPriceCore20261019FXEvidenceRoundTripsAtStoredPrecision(t *testing.T) {
	db := setupZTAPIPublicCatalogTestDB(t)
	old := supplierPriceAnchorForTest(t)
	require.NoError(t, db.Create(&old).Error)
	source, err := BuildZTAPISupplierPriceSource20261019(&old, 9, 1_792_368_000, platformFXParamsForTest("6.630000000049", "6.762812345649"))
	require.NoError(t, err)
	require.NoError(t, ValidateZTAPISupplierPriceSource20261019(db, &source))
}

func TestSupplierPriceCore20261019CNYProcurementUsesPlatformWithoutBuffer(t *testing.T) {
	quote, err := ZTAPIQuotationABEntries()
	require.NoError(t, err)
	for _, name := range []string{"GLM 5.2", "GLM 5.1", "GLM 5", "GLM 4.7", "Kimi K2.6", "Kimi K2.7 Code"} {
		t.Run(name, func(t *testing.T) {
			old, err := BuildZTAPIABTextPriceSourceWithFX(quote, name, 11, 7, 1_789_000_000, platformFXParamsForTest("6.63", ""))
			require.NoError(t, err)
			old.ID = 41
			source, err := BuildZTAPISupplierPriceSource20261019(&old, 9, 1_792_368_000, platformFXParamsForTest("6.63", ""))
			require.NoError(t, err)
			entry, err := ztapiSupplierPriceEntryFor20261019(old.SourceModel)
			require.NoError(t, err)
			maxInput := decimal.Zero
			for _, rule := range entry.Rules {
				input := decimal.RequireFromString(rule.Official[ZTAPIBillingDimensionInputTokens]).Mul(decimal.RequireFromString(entry.Fraction)).Round(10)
				if input.GreaterThan(maxInput) {
					maxInput = input
				}
			}
			require.Equal(t, maxInput.StringFixed(10), source.InputPerMillion)
			require.True(t, decimal.RequireFromString(source.UpstreamCNYPerUSD).IsZero())
			preview, err := BuildZTAPIModelPricePreview(&source)
			require.NoError(t, err)
			require.Equal(t, maxInput.Div(decimal.RequireFromString("6.63")).Round(10).StringFixed(10), preview.InputCostUSDPerMillion)
			require.NoError(t, ValidateZTAPIQuotationIdentity(source.SourceModel, "zt-"+source.SourceModel, ZTAPIProtocolOpenAICompatible,
				func() string {
					if name == "Kimi K2.6" || name == "Kimi K2.7 Code" {
						return ZTAPIProviderMoonshot
					}
					return ZTAPIProviderGLM
				}(), source.SourceDocumentChecksum))
		})
	}
	for _, sourceModel := range []string{"kimi-k3", "kimi-k2.5"} {
		_, err := ztapiSupplierPriceEntryFor20261019(sourceModel)
		require.ErrorIs(t, err, ErrZTAPIQuotationModelNotQuoted)
	}
}

func publishSupplierPriceForCoreTest(t *testing.T) (*gorm.DB, ZTAPIModelConfig, ZTAPIModelPriceSource) {
	t.Helper()
	db := setupZTAPIPublicCatalogTestDB(t)
	require.NoError(t, db.AutoMigrate(&ZTAPIAuditEvent{}, &ZTAPICatalogLock{}, &ZTAPIFXPolicy{}))
	config := seedZTAPIPublicCatalogRecord(t, db, "gemini-3.1-pro-preview", "zt-gemini-3.1-pro-preview", ZTAPIProviderGoogle,
		ZTAPIProtocolOpenAICompatible, []string{ZTAPIBillingDimensionInputTokens, ZTAPIBillingDimensionOutputTokens})
	quote, err := ZTAPIQuotationABEntries()
	require.NoError(t, err)
	_, err = ApplyZTAPIABCommercialPricing(7, quote.WorkbookSHA256, []string{"Gemini 3.1 Pro Preview"})
	require.NoError(t, err)
	require.NoError(t, db.First(&config, config.ID).Error)
	var previous ZTAPIModelPublicationSnapshot
	var old ZTAPIModelPriceSource
	require.NoError(t, db.First(&previous, config.PublicationSnapshotID).Error)
	require.NoError(t, db.First(&old, previous.PriceSourceID).Error)
	target, err := BuildZTAPISupplierPriceSource20261019(&old, 7, 1_792_368_000, ZTAPIABFXParams{})
	require.NoError(t, err)
	preview, err := BuildZTAPIModelPricePreview(&target)
	require.NoError(t, err)
	require.NoError(t, republishZTAPIPriceSourceTx(db, &config, previous, &old, &target, preview, 7))
	invalidateZTAPICatalogCaches()
	require.NoError(t, db.First(&config, config.ID).Error)
	return db, config, target
}

func TestSupplierPriceCore20261019QuotationWarmReadRejectsTampering(t *testing.T) {
	db, config, source := publishSupplierPriceForCoreTest(t)
	require.NoError(t, ValidateZTAPIQuotationIdentity(source.SourceModel, config.PublicNameValue(), config.Protocol, config.ProviderFamily, source.SourceDocumentChecksum))
	require.Error(t, ValidateZTAPIQuotationIdentity(source.SourceModel, "zt-forged", config.Protocol, config.ProviderFamily, source.SourceDocumentChecksum))
	checksums, err := ztapiQuotationChecksums(db, []int64{config.PublicationSnapshotID})
	require.NoError(t, err)
	require.Equal(t, source.SourceDocumentChecksum, checksums[config.PublicationSnapshotID])
	require.True(t, ztapiPublicationPriceBasisPermitted(&source))
	require.NoError(t, db.Model(&source).Update("input_per_million", "0.01").Error)
	checksums, err = ztapiQuotationChecksumsAllowingStaleABPrice(db, []int64{config.PublicationSnapshotID}, true)
	require.NoError(t, err)
	require.Empty(t, checksums)
}

func TestSupplierPriceCore20261019FXRepricerExplicitlySkipsNewWorkbook(t *testing.T) {
	db, config, source := publishSupplierPriceForCoreTest(t)
	seedZTAPIFXPolicyForTest(t, db, "6.7628")
	preview, err := PreviewZTAPIFXRepricing()
	require.NoError(t, err)
	require.Len(t, preview.Rows, 1)
	require.Equal(t, "skipped", preview.Rows[0].Status)
	require.Contains(t, preview.Rows[0].Reason, "2026-10-19")
	result, err := ApplyZTAPIFXRepricing(7)
	require.NoError(t, err)
	require.Zero(t, result.Republished)
	require.Contains(t, result.Skipped, config.PublicNameValue())
	var after ZTAPIModelConfig
	require.NoError(t, db.First(&after, config.ID).Error)
	require.Equal(t, config.PublicationSnapshotID, after.PublicationSnapshotID)
	var retained ZTAPIModelPriceSource
	require.NoError(t, db.First(&retained, source.ID).Error)
	require.Equal(t, source.SourceDocumentChecksum, retained.SourceDocumentChecksum)
}

func TestSupplierPriceCore20261019LegacyCommercialV2CannotOverwriteNewWorkbook(t *testing.T) {
	db, config, source := publishSupplierPriceForCoreTest(t)
	var snapshot ZTAPIModelPublicationSnapshot
	require.NoError(t, db.First(&snapshot, config.PublicationSnapshotID).Error)
	var beforeSources, beforeSnapshots, beforeAudits int64
	require.NoError(t, db.Model(&ZTAPIModelPriceSource{}).Count(&beforeSources).Error)
	require.NoError(t, db.Model(&ZTAPIModelPublicationSnapshot{}).Count(&beforeSnapshots).Error)
	require.NoError(t, db.Model(&ZTAPIAuditEvent{}).Count(&beforeAudits).Error)
	result, err := ApplyZTAPICommercialPricingV2(7)
	require.NoError(t, err)
	require.Equal(t, 1, result.Unchanged)
	require.Zero(t, result.Imported)
	require.Zero(t, result.Republished)
	var after ZTAPIModelConfig
	var retainedSource ZTAPIModelPriceSource
	var retainedSnapshot ZTAPIModelPublicationSnapshot
	require.NoError(t, db.First(&after, config.ID).Error)
	require.NoError(t, db.First(&retainedSource, source.ID).Error)
	require.NoError(t, db.First(&retainedSnapshot, snapshot.ID).Error)
	require.Equal(t, config, after)
	require.Empty(t, ztapiABPriceSourceEvidenceMismatch(&source, &retainedSource))
	require.Equal(t, snapshot, retainedSnapshot)
	var afterSources, afterSnapshots, afterAudits int64
	require.NoError(t, db.Model(&ZTAPIModelPriceSource{}).Count(&afterSources).Error)
	require.NoError(t, db.Model(&ZTAPIModelPublicationSnapshot{}).Count(&afterSnapshots).Error)
	require.NoError(t, db.Model(&ZTAPIAuditEvent{}).Count(&afterAudits).Error)
	require.Equal(t, beforeSources, afterSources)
	require.Equal(t, beforeSnapshots, afterSnapshots)
	require.Equal(t, beforeAudits, afterAudits)
}
