package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/require"
)

func TestSupplierAdmissionMediaCannotFallThroughToTextMetering(t *testing.T) {
	for _, source := range []string{"gemini-nano-banana-2.1", "gemini-3.1-flash-lite-image", "gemini-3.1-flash-image", "gemini-3-pro-image"} {
		require.Equal(t, ZTAPIModalityImage, ZTAPIModelModality(source), source)
	}
	require.Equal(t, ZTAPIModalityVideo, ZTAPIModelModality("doubao-seedance-2-5-260628"))
	require.Equal(t, ZTAPIModalityText, ZTAPIModelModality("unknown-image-model"), "unknown names must not gain media authority from a suffix")
}

func TestSupplierAdmissionPricePolicySurvivesRuntimeCache(t *testing.T) {
	publication := runtimePublicationFromCache(ztapiPublishedModel{PricePolicy: string(ZTAPIPricePolicySupplierAdmission20261019)})
	require.Equal(t, string(ZTAPIPricePolicySupplierAdmission20261019), publication.PricePolicy)
}

func TestSupplierAdmissionExactIdentitySurvivesRuntimeQuotationChecks(t *testing.T) {
	for _, tc := range []struct{ source, public, provider string }{
		{"gpt-6.1-sol", "zt-gpt-6.1-sol", ZTAPIProviderOpenAI},
		{"gpt-6-sol", "zt-gpt-6-sol", ZTAPIProviderOpenAI},
		{"claude-opus-5-5", "zt-claude-opus-5.5", ZTAPIProviderAnthropic},
		{"claude-sonnet-5-5", "zt-claude-sonnet-5.5", ZTAPIProviderAnthropic},
	} {
		t.Run(tc.source, func(t *testing.T) {
			require.NoError(t, ValidateZTAPIQuotationIdentity(tc.source, tc.public, ZTAPIProtocolOpenAICompatible, tc.provider, ZTAPISupplierWorkbook20261019SHA256))
			require.Error(t, ValidateZTAPIQuotationIdentity(tc.source, tc.public+"-other", ZTAPIProtocolOpenAICompatible, tc.provider, ZTAPISupplierWorkbook20261019SHA256))
			require.Error(t, ValidateZTAPIQuotationIdentity(tc.source, tc.public, ZTAPIProtocolOpenAICompatible, ZTAPIProviderOther, ZTAPISupplierWorkbook20261019SHA256))
			require.Error(t, ValidateZTAPIQuotationIdentity(tc.source+"-hc", tc.public, ZTAPIProtocolOpenAICompatible, tc.provider, ZTAPISupplierWorkbook20261019SHA256))
		})
	}
}

func TestSupplierAdmissionQuotedImportUsesLatestGPTAndClaudePrices(t *testing.T) {
	db := setupZTAPIPublicCatalogTestDB(t)
	seedZTAPIFXPolicyForTest(t, db, "6.7628")
	for _, tc := range []struct{ name, source, input, output string }{
		{"GPT 6.1 Sol", "gpt-6.1-sol", "3.1200000000", "11.7000000000"},
		{"GPT 6 Sol", "gpt-6-sol", "3.1200000000", "11.7000000000"},
		{"GPT 6 Luna", "gpt-6-luna", "0.1560000000", "0.5850000000"},
		{"GPT 6 Astra", "gpt-6-astra", "15.6000000000", "58.5000000000"},
		{"GPT 5.6 Sol", "gpt-5.6-sol", "6.2400000000", "23.4000000000"},
		{"GPT 5.6 Terra", "gpt-5.6-terra", "3.1200000000", "14.0400000000"},
		{"GPT 5.6 Luna", "gpt-5.6-luna", "0.3120000000", "1.4040000000"},
		{"GPT 5.5", "gpt-5.5", "7.8000000000", "35.1000000000"},
		{"GPT 5.4", "gpt-5.4", "3.9000000000", "17.5500000000"},
		{"Claude Opus 5.5", "claude-opus-5-5", "3.1200000000", "15.6000000000"},
		{"Claude Sonnet 5.5", "claude-sonnet-5-5", "1.5600000000", "7.8000000000"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source, err := BuildZTAPIQuotedModelPriceSource(tc.name, 11, 7, 1_792_419_000)
			require.NoError(t, err)
			require.Equal(t, tc.source, source.SourceModel)
			require.Equal(t, ZTAPISupplierWorkbook20261019SHA256, source.SourceDocumentChecksum)
			preview, err := BuildZTAPIModelPricePreview(&source)
			require.NoError(t, err)
			require.Equal(t, tc.input, preview.InputSaleUSDPerMillion)
			require.Equal(t, tc.output, preview.OutputSaleUSDPerMillion)
			require.True(t, ztapiPublicationPriceBasisPermittedTx(db, &source))
			var count int64
			require.NoError(t, db.Model(&ZTAPIModelPriceSource{}).Count(&count).Error)
			require.Zero(t, count, "building and checking a candidate must never import or publish it")
		})
	}
}

func TestSupplierAdmissionFreezesFivePercentCacheAndWholeRequestTiers(t *testing.T) {
	db := setupZTAPIPublicCatalogTestDB(t)
	seedZTAPIFXPolicyForTest(t, db, "6.7628")
	source, err := BuildZTAPIQuotedModelPriceSource("GPT 6.1 Sol", 11, 7, 1_792_419_000)
	require.NoError(t, err)
	var rules []ztapiABFrozenSaleRule
	require.NoError(t, common.Unmarshal([]byte(source.TokenPriceRulesJSON), &rules))
	require.Len(t, rules, 2)
	require.Equal(t, []string{"输入长度≤272K"}, rules[0].Conditions)
	require.Equal(t, []string{"输入长度>272K"}, rules[1].Conditions)
	require.Equal(t, "0.0780000000", rules[0].Sale[ZTAPIBillingDimensionCacheRead])
	require.Equal(t, "0.1560000000", rules[1].Sale[ZTAPIBillingDimensionCacheRead])
	require.Equal(t, "1.9500000000", rules[0].Sale[ZTAPIBillingDimensionCacheWrite])
	require.Equal(t, "7.5743360000", source.InputPerMillion)
	claude, err := BuildZTAPIQuotedModelPriceSource("Claude Opus 5.5", 12, 7, 1_792_419_000)
	require.NoError(t, err)
	preview, err := BuildZTAPIModelPricePreview(&claude)
	require.NoError(t, err)
	require.Equal(t, "0.1560000000", preview.SaleUSD[ZTAPIBillingDimensionCacheRead])
	require.Equal(t, "3.9000000000", preview.SaleUSD[ZTAPIBillingDimensionCacheWrite5m])
	require.Equal(t, "6.2400000000", preview.SaleUSD[ZTAPIBillingDimensionCacheWrite1h])
}

func TestSupplierAdmissionRejectsForgedCostsDimensionsAndRetail(t *testing.T) {
	source, err := BuildZTAPISupplierAdmissionTextPriceSource20261019("GPT 6.1 Sol", 11, 7, 1_792_419_000, platformFXParamsForTest("6.63", "6.7628"))
	require.NoError(t, err)
	for name, change := range map[string]func(*ZTAPIModelPriceSource){
		"cost":                    func(s *ZTAPIModelPriceSource) { s.InputPerMillion = "0.01" },
		"unquoted_model":          func(s *ZTAPIModelPriceSource) { s.SourceModel = "gpt-6.1-sol-fake" },
		"enterprise_route_policy": func(s *ZTAPIModelPriceSource) { s.ResourceType = "enterprise" },
		"quotation_cell":          func(s *ZTAPIModelPriceSource) { s.QuotationCell = "E999" },
		"checksum":                func(s *ZTAPIModelPriceSource) { s.SourceDocumentChecksum = ZTAPIQuotationSHA256 },
		"discount":                func(s *ZTAPIModelPriceSource) { s.SaleMultiplier = "0.99" },
		"cache_read_omission":     func(s *ZTAPIModelPriceSource) { s.BillingDimensions = `["input_tokens","output_tokens","cache_write"]` },
		"tier_price": func(s *ZTAPIModelPriceSource) {
			r := supplierRulesForTest(t, s)
			r[0].Sale[ZTAPIBillingDimensionCacheRead] = "0.156"
			setSupplierRulesForTest(t, s, r)
		},
		"tier_boundary": func(s *ZTAPIModelPriceSource) {
			r := supplierRulesForTest(t, s)
			r[0].Conditions = []string{"输入长度≤200K"}
			setSupplierRulesForTest(t, s, r)
		},
	} {
		t.Run(name, func(t *testing.T) {
			bad := source
			change(&bad)
			require.Error(t, ValidateZTAPIModelPriceSource(&bad))
			require.False(t, ztapiPublicationPriceBasisPermittedTx(nil, &bad))
		})
	}
	_, err = BuildZTAPISupplierAdmissionTextPriceSource20261019("GPT 6.1 Sol", 11, 7, 1_792_419_000, platformFXParamsForTest("6.63", "100"))
	require.Error(t, err, "every tier must retain the minimum margin under frozen FX")
	_, err = BuildZTAPISupplierAdmissionTextPriceSource20261019("GPT 6.1 Sol", 11, 7, 1_792_419_000, ZTAPIABFXParams{})
	require.Error(t, err, "new admission must not silently use legacy FX")
}

func TestSupplierAdmissionDoesNotInventUnquotedCacheWrites(t *testing.T) {
	for _, name := range []string{"GPT 5.4", "GPT 5.5"} {
		source, err := BuildZTAPISupplierAdmissionTextPriceSource20261019(name, 11, 7, 1_792_419_000, platformFXParamsForTest("6.63", "6.7628"))
		require.NoError(t, err)
		preview, err := BuildZTAPIModelPricePreview(&source)
		require.NoError(t, err)
		require.NotContains(t, preview.BillingDimensions, ZTAPIBillingDimensionCacheWrite)
		require.NotContains(t, preview.SaleUSD, ZTAPIBillingDimensionCacheWrite)
	}
}

func TestSupplierAdmissionNeverPublishesThroughEnterpriseOrWrongPoolSKU(t *testing.T) {
	for _, tc := range []struct {
		name      string
		channelID int
		physical  string
		wantRoute bool
		extra     map[string]string
	}{
		{"enterprise_not_a_pool_fallback", 1, "gpt-6.1-sol-hc", false, nil},
		{"wrong_pool_sku", 2, "gpt-6-sol-hc", false, nil},
		{"exact_pool_sku", 2, "gpt-6.1-sol-hc", true, nil},
		{"chained_wrong_sku", 2, "gpt-6.1-sol-hc", false, map[string]string{"gpt-6.1-sol-hc": "gpt-6-sol-hc"}},
		{"mapping_cycle", 2, "gpt-6.1-sol-hc", false, map[string]string{"gpt-6.1-sol-hc": "gpt-6.1-sol"}},
		{"chained_exact_sku", 2, "gpt-6.1-sol-hc", true, map[string]string{"gpt-6.1-sol": "supplier-alias", "supplier-alias": "gpt-6.1-sol-hc"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := setupZTAPIPublicationGateFixture(t)
			alias := "zt-gpt-6.1-sol"
			f.config.SourceModel, f.config.PublicName, f.config.ProviderFamily = "gpt-6.1-sol", &alias, ZTAPIProviderOpenAI
			require.NoError(t, f.db.Save(&f.config).Error)
			f.identity.PublicName, f.identity.ProviderFamily = alias, ZTAPIProviderOpenAI
			require.NoError(t, f.db.Save(&f.identity).Error)
			modelMap := map[string]string{"gpt-6.1-sol": tc.physical}
			for from, to := range tc.extra {
				modelMap[from] = to
			}
			mapping, err := common.Marshal(modelMap)
			require.NoError(t, err)
			raw := string(mapping)
			f.channel.Models, f.channel.ModelMapping = "gpt-6.1-sol", &raw
			if tc.channelID == 2 {
				f.channel.Id = 2
				require.NoError(t, f.db.Create(&f.channel).Error)
			} else {
				require.NoError(t, f.db.Save(&f.channel).Error)
			}
			require.NoError(t, f.channel.UpdateAbilities(f.db))
			require.NoError(t, f.db.Model(&ZTAPIDiscoveredModel{}).Where("snapshot_id = ?", f.snapshot.ID).
				Updates(map[string]any{"channel_id": tc.channelID, "source_model": tc.physical}).Error)
			require.NoError(t, f.db.Model(&f.snapshot).Update("channel_id", tc.channelID).Error)
			f.verification.ChannelID = tc.channelID
			f.verification.ID = 0
			require.NoError(t, f.db.Create(&f.verification).Error)
			price, err := BuildZTAPISupplierAdmissionTextPriceSource20261019("GPT 6.1 Sol", f.config.ID, 7, 1_792_419_000, platformFXParamsForTest("6.63", "6.7628"))
			require.NoError(t, err)
			price.Version = f.price.Version + 1
			require.NoError(t, f.db.Create(&price).Error)
			blockers, err := ZTAPIPublicationBlockers(f.config.ID)
			require.NoError(t, err)
			if tc.wantRoute {
				require.NotContains(t, blockers, ZTAPIPublicationBlockerRouteUnavailable)
			} else {
				require.Contains(t, blockers, ZTAPIPublicationBlockerRouteUnavailable)
			}
		})
	}
}
