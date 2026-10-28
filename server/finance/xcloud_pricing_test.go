// Copyright (C) 2026 QuantumNous. SPDX-License-Identifier: AGPL-3.0-or-later
package finance

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEstimateXcloudUsagePricesExclusiveCacheDimensions(t *testing.T) {
	estimate, err := EstimateXcloudUsage("claude-opus-4-8", []UsageDimension{
		{Dimension: "input_tokens", Quantity: 1_000_000},
		{Dimension: "output_tokens", Quantity: 1_000_000},
		{Dimension: "cache_read", Quantity: 1_000_000},
		{Dimension: "cache_write", Quantity: 1_000_000},
		{Dimension: "cache_write_5m", Quantity: 1_000_000},
		{Dimension: "cache_write_1h", Quantity: 1_000_000},
	})
	require.NoError(t, err)
	require.Equal(t, "26.500000", estimate.CostUSDT)
	require.Equal(t, "https://www.xcloudtoken.com/pricing", estimate.SourceURL)
	require.True(t, estimate.IsEstimate)
}

func TestEstimateXcloudUsageRejectsDuplicateDimension(t *testing.T) {
	_, err := EstimateXcloudUsage("claude-opus-4-8", []UsageDimension{
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
		"deepseek-v4.1-flash", "glm-5.2", "glm-5.3", "glm-5.3-flash",
		"gpt-5.5", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-6-astra", "gpt-6-luna",
		"gpt-6-sol", "kimi-k3", "qwen3.8-flash", "qwen3.8-max",
	}
	quotes := XcloudPublicQuotes()
	require.Len(t, quotes, len(models))
	for _, model := range models {
		quote, ok := xcloudQuoteByModel(model)
		require.Truef(t, ok, "missing live Xcloud quote for %s", model)
		require.Equal(t, "0.5", quote.DiscountFactor.String())
	}
}

func TestEstimateXcloudUsageRejectsUnquotedCacheDimension(t *testing.T) {
	_, err := EstimateXcloudUsage("deepseek-v4.1-flash", []UsageDimension{{
		Dimension: "cache_write_5m", Quantity: 1_000_000,
	}})
	require.ErrorIs(t, err, ErrXcloudQuoteMissing)
}
