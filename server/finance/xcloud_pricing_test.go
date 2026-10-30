// Copyright (C) 2026 QuantumNous. SPDX-License-Identifier: AGPL-3.0-or-later
package finance

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEstimateXcloudUsagePricesExclusiveCacheDimensions(t *testing.T) {
	estimate, err := EstimateXcloudUsageForGroup("claude-fable-5", "claude-max", "base", []UsageDimension{
		{Dimension: "input_tokens", Quantity: 1_000_000},
		{Dimension: "output_tokens", Quantity: 1_000_000},
		{Dimension: "cache_read", Quantity: 1_000_000},
		{Dimension: "cache_write_5m", Quantity: 1_000_000},
		{Dimension: "cache_write_1h", Quantity: 1_000_000},
	})
	require.NoError(t, err)
	require.Equal(t, "46.875", estimate.CostUSDT)
	require.Equal(t, "https://www.xcloudtoken.com/pricing", estimate.SourceURL)
	require.True(t, estimate.IsEstimate)
}

func TestXcloudGroupAndPrepaidRate(t *testing.T) {
	tests := []struct{ model, group, tier, expected string }{
		{"glm-5.3", "国产分组", "固定", "2.4"},
		{"claude-sonnet-5", "claude-max", "base", "0.75"},
		{"gpt-6-sol", "new-vip", "base", "1.9"},
	}
	for _, tc := range tests {
		t.Run(tc.model, func(t *testing.T) {
			got, err := EstimateXcloudUsageForGroup(tc.model, tc.group, tc.tier, []UsageDimension{{Dimension: "input_tokens", Quantity: 1_000_000}})
			require.NoError(t, err)
			require.Equal(t, tc.expected, got.CostUSDT)
		})
	}
}

func TestXcloudRejectsUnknownGroupTierAndOverlappingWriteDimensions(t *testing.T) {
	dims := []UsageDimension{{Dimension: "input_tokens", Quantity: 1}}
	_, err := EstimateXcloudUsageForGroup("claude-fable-5", "new-vip", "base", dims)
	require.ErrorIs(t, err, ErrXcloudQuoteMissing)
	_, err = EstimateXcloudUsageForGroup("claude-fable-5", "claude-max", "unknown", dims)
	require.ErrorIs(t, err, ErrXcloudQuoteMissing)
	_, err = EstimateXcloudUsageForGroup("claude-fable-5", "claude-max", "base", []UsageDimension{
		{Dimension: "cache_write", Quantity: 1}, {Dimension: "cache_write_5m", Quantity: 1},
	})
	require.Error(t, err)
}

func TestXcloudEstimatePreservesSubMicroUnitCostsBeforeAggregation(t *testing.T) {
	got, err := EstimateXcloudUsageForGroup("claude-sonnet-5", "claude-max", "base", []UsageDimension{{Dimension: "input_tokens", Quantity: 1}})
	require.NoError(t, err)
	require.Equal(t, "0.00000075", got.CostUSDT)
}

func TestXcloudLongContextTierUsesItsOwnGroupPrice(t *testing.T) {
	dims := []UsageDimension{{Dimension: "input_tokens", Quantity: 1_000_000}}
	standard, err := EstimateXcloudUsageForGroup("gpt-6-astra", "new-vip", "standard", dims)
	require.NoError(t, err)
	long, err := EstimateXcloudUsageForGroup("gpt-6-astra", "new-vip", "long_context", dims)
	require.NoError(t, err)
	require.Equal(t, "9.5", standard.CostUSDT)
	require.Equal(t, "19", long.CostUSDT)
}

func TestXcloudEstimateCannotTreatMissingUsageAsFree(t *testing.T) {
	_, err := EstimateXcloudUsageForGroup("glm-5.3", "国产分组", "固定", nil)
	require.Error(t, err)
}

func TestEstimateXcloudUsageRejectsDuplicateDimension(t *testing.T) {
	_, err := EstimateXcloudUsageForGroup("claude-opus-4-8", "claude-max", "固定", []UsageDimension{
		{Dimension: "input_tokens", Quantity: 1},
		{Dimension: "input_tokens", Quantity: 1},
	})
	require.Error(t, err)
}

func TestEstimateXcloudUsageRequiresPublicQuote(t *testing.T) {
	estimate, err := EstimateXcloudUsage("claude-opus-5", []UsageDimension{{
		Dimension: "input_tokens", Quantity: 1_000_000,
	}})
	require.ErrorIs(t, err, ErrXcloudQuoteMissing)
	require.Equal(t, "0", estimate.CostUSDT)
	require.False(t, estimate.IsEstimate)
}

func TestXcloudPublicQuotesCoverLivePricingCatalog(t *testing.T) {
	models := []string{
		"claude-fable-5", "claude-fable-5-1", "claude-haiku-4-5-20251001",
		"claude-opus-4-6", "claude-opus-4-7", "claude-opus-4-8", "claude-opus-5-5",
		"claude-sonnet-4-6", "claude-sonnet-5", "claude-sonnet-5-5",
		"deepseek-v4.1-flash", "glm-5.3",
		"gpt-5.5", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-6-astra", "gpt-6-luna",
		"gpt-6-sol", "kimi-k3",
	}
	quotes := XcloudPublicQuotes()
	require.Len(t, quotes, len(models)+1, "astra has two independently quoted tiers")
	for _, model := range models {
		quote, ok := xcloudQuoteByModel(model)
		require.Truef(t, ok, "missing live Xcloud quote for %s", model)
		require.Equal(t, "0.5", quote.DiscountFactor.String())
	}
}

func TestEstimateXcloudUsageRejectsUnquotedCacheDimension(t *testing.T) {
	_, err := EstimateXcloudUsageForGroup("deepseek-v4.1-flash", "国产分组", "base", []UsageDimension{{
		Dimension: "cache_write_5m", Quantity: 1_000_000,
	}})
	require.ErrorIs(t, err, ErrXcloudQuoteMissing)
}
