package model

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSupplierKimiCachePricePreservesRetailAndAddsOnlyQuotedTTLs(t *testing.T) {
	db := setupZTAPIPublicCatalogTestDB(t)
	quote, err := ZTAPIQuotationABEntries()
	require.NoError(t, err)
	old, err := BuildZTAPIABTextPriceSourceWithFX(quote, "Kimi K3", 11, 7, 1_789_000_000, platformFXParamsForTest("6.63", ""))
	require.NoError(t, err)
	old.ID, old.Version, old.SaleMultiplier = 41, 1, "0.8"
	rules := supplierRulesForTest(t, &old)
	rules[0].Sale = map[string]string{"input_tokens": "5", "output_tokens": "20", "cache_read": "1"}
	setSupplierRulesForTest(t, &old, rules)
	require.NoError(t, db.Create(&old).Error)
	before := old
	source, err := BuildZTAPISupplierPriceSource20261019(&old, 9, 1_792_419_000, platformFXParamsForTest("6.63", ""))
	require.NoError(t, err)
	require.Equal(t, before, old, "preparing a new contract must not change its immutable retail anchor")
	preview, err := BuildZTAPIModelPricePreview(&source)
	require.NoError(t, err)
	require.Equal(t, "4.0000000000", preview.SaleUSD[ZTAPIBillingDimensionInputTokens])
	require.Equal(t, "16.0000000000", preview.SaleUSD[ZTAPIBillingDimensionOutputTokens])
	require.Equal(t, "0.8000000000", preview.SaleUSD[ZTAPIBillingDimensionCacheRead])
	require.Equal(t, "4.0000000000", preview.SaleUSD[ZTAPIBillingDimensionCacheWrite5m])
	require.Equal(t, "8.0000000000", preview.SaleUSD[ZTAPIBillingDimensionCacheWrite1h])
	require.NotContains(t, preview.SaleUSD, ZTAPIBillingDimensionCacheWrite, "unspecified TTL must not silently receive a guessed price")
	require.Equal(t, "14.6000000000", source.CacheWrite5mPerMillion)
	require.Equal(t, "29.2000000000", source.CacheWrite1hPerMillion)
	require.NoError(t, ValidateZTAPISupplierPriceSource20261019(db, &source))
	for _, tc := range []struct {
		name   string
		mutate func(*ZTAPIModelPriceSource)
	}{
		{"write_5m_cost", func(s *ZTAPIModelPriceSource) { s.CacheWrite5mPerMillion = "1" }},
		{"write_1h_cost", func(s *ZTAPIModelPriceSource) { s.CacheWrite1hPerMillion = "1" }},
		{"write_5m_retail", func(s *ZTAPIModelPriceSource) {
			rules := supplierRulesForTest(t, s)
			rules[0].Sale["cache_write_5m"] = "1"
			setSupplierRulesForTest(t, s, rules)
		}},
		{"write_1h_retail", func(s *ZTAPIModelPriceSource) {
			rules := supplierRulesForTest(t, s)
			rules[0].Sale["cache_write_1h"] = "1"
			setSupplierRulesForTest(t, s, rules)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			forged := source
			tc.mutate(&forged)
			require.Error(t, ValidateZTAPISupplierPriceSource20261019(db, &forged))
		})
	}
}
